//go:build linux

package pdf

// Tests with the real pinned Typst binary and fonts. They run when REPORTING_TYPST,
// REPORTING_TYPST_SHA256 and REPORTING_PDF_FONTS are set, and fail instead of skipping when
// REPORTING_PDF_REQUIRE=1. With REPORTING_PDF_EVIDENCE set to a directory they also write the
// documents and what each must show there, for scripts/check_pdf_export.py and veraPDF.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

func typstConfig(t *testing.T) Config {
	t.Helper()
	c := Config{Typst: os.Getenv("REPORTING_TYPST"), TypstSHA256: os.Getenv("REPORTING_TYPST_SHA256"), FontDir: os.Getenv("REPORTING_PDF_FONTS")}
	if c.Typst == "" || c.TypstSHA256 == "" || c.FontDir == "" {
		if os.Getenv("REPORTING_PDF_REQUIRE") == "1" {
			t.Fatal("REPORTING_PDF_REQUIRE=1 but REPORTING_TYPST, REPORTING_TYPST_SHA256 or REPORTING_PDF_FONTS is not set")
		}
		t.Skip("set REPORTING_TYPST, REPORTING_TYPST_SHA256 and REPORTING_PDF_FONTS to run Typst")
	}
	c.Now = func() time.Time { return created }
	return c
}

func typstWriter(t *testing.T, change func(*Config)) *Writer {
	t.Helper()
	c := typstConfig(t)
	if change != nil {
		change(&c)
	}
	w, err := New(c)
	if err != nil {
		var re *RenderError
		if errors.As(err, &re) {
			t.Fatalf("%v\n%s", err, re.output())
		}
		t.Fatal(err)
	}
	return w
}

// expectation is what scripts/check_pdf_export.py checks one document against. The data are
// the snapshot's own values; the script derives the printed text itself, so a defect in build
// is not copied into what the document must show. Only the page geometry comes from the layout.
type expectation struct {
	File       string       `json:"file"`
	Landscape  bool         `json:"landscape"`
	Columns    [][2]float64 `json:"columns"` // left and right edge of each column, in points
	Margin     float64      `json:"margin"`
	Inset      float64      `json:"inset"`
	MinPages   int          `json:"min_pages"`
	Title      string       `json:"title"`
	Labels     []label      `json:"labels"`
	Metric     []bool       `json:"metric"` // metric numbers are printed with thousands separators
	Dimensions int          `json:"dimensions"`
	Rows       [][]string   `json:"rows"`
	Totals     []string     `json:"totals"` // in metric order
	Meta       []string     `json:"meta"`
	Footer     []string     `json:"footer"`
}

type label struct {
	Label    string `json:"label"`
	Currency string `json:"currency"`
	Unit     string `json:"unit"`
}

func evidence(t *testing.T, name string, pdf []byte, d *document, s r.Snapshot, minPages int) {
	t.Helper()
	dir := os.Getenv("REPORTING_PDF_EVIDENCE")
	if dir == "" {
		return
	}
	q := s.Query
	e := expectation{File: name + ".pdf", Landscape: d.Landscape, Margin: marginX, Inset: insetX, MinPages: minPages,
		Title: q.Dataset.ID, Dimensions: len(q.Dimensions), Rows: s.Result.Rows, Totals: []string{},
		Meta:   []string{q.Dataset.ID + "@" + q.Dataset.Version, "截至 " + q.AsOf, q.Timezone, strings.Join(q.Companies, "、"), strconv.Itoa(len(s.Result.Rows)) + " 行", "生成于 2026-10-27 08:30:00 UTC"},
		Footer: []string{"合同模拟器 / 合成数据（非真实业务数据）", "快照 " + s.ID, q.Dataset.ID + "@" + q.Dataset.Version, "截至 " + q.AsOf, "来源水位 " + s.Prepared.Watermark}}
	if e.Rows == nil {
		e.Rows = [][]string{}
	}
	x := marginX
	for _, w := range d.Widths {
		e.Columns = append(e.Columns, [2]float64{x, x + w})
		x += w
	}
	for j, c := range s.Result.Columns {
		e.Labels = append(e.Labels, label{c.Label, c.Currency, c.Unit})
		e.Metric = append(e.Metric, j >= len(q.Dimensions) && (c.Type == "decimal" || c.Type == "integer"))
	}
	for _, m := range q.Metrics {
		e.Totals = append(e.Totals, s.Result.Totals[m.ID])
	}
	b, _ := json.MarshalIndent(e, "", " ")
	for file, content := range map[string][]byte{name + ".pdf": pdf, name + ".json": b} {
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTypstExportsThroughTheService(t *testing.T) {
	w := typstWriter(t, nil)
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(time.Now)
	s, err := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": w}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	j, err := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if j, err = s.Job(ctx, p, j.ID); err != nil || j.SnapshotID == "" {
		t.Fatal(j, err)
	}
	e, err := s.SubmitExportFormat(ctx, p, j.SnapshotID, "pdf", "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if e, err = s.Job(ctx, p, e.ID); err != nil || e.State != "succeeded" {
		t.Fatal(e, err)
	}
	art, err := s.Download(ctx, p, e.ArtifactID)
	if err != nil || art.Format != "pdf" || !bytes.HasPrefix(art.Content, []byte("%PDF-")) {
		t.Fatal(art.Format, err)
	}
	snap, err := repo.Snapshot(ctx, p.Identity, j.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := build(snap, newMeasurer(w.fonts), created)
	if err != nil {
		t.Fatal(err)
	}
	evidence(t, "financial", art.Content, d, snap, 1)
}

// hostile holds strings that would be markup, code or line-breaking traps if the data were
// interpolated into Typst source, and a row taller than a page.
func hostile() r.Snapshot {
	s := table(2, 1)
	s.Result.Columns[0].Label, s.Result.Columns[1].Label, s.Result.Columns[2].Label = "项目", "说明", "金额"
	tall := strings.Repeat("应收账款，含税（13%）及运费；客户“加急”。", 260)
	values := []string{
		`#import "@preview/cetz:0.3.0": *`,
		`#read("/etc/passwd") #sys.inputs`,
		`$x^2$ *粗体* _斜体_ [链接](x) <标签> @引用 \ // 注释 /* 块 */ ` + "`code`",
		`"引号"与‘单引号’，标点。！？；：……——（括号）《书名》`,
		"）开头的右括号与。句号",
		"第一行\n第二行\r\n第三行\t制表后",
		"https://example.com/" + strings.Repeat("segment/", 30) + "?q=" + strings.Repeat("x", 80),
		"INV-" + strings.Repeat("0123456789", 30),
		"扩展B区：𬌗 与 combining é",
		tall,
		"  前后空格  ",
		"",
	}
	for i, v := range values {
		amount := []string{"-0.01", "123456789012345678.99", "0.00", "1000.10"}[i%4]
		s.Result.Rows = append(s.Result.Rows, []string{"行 " + strconv.Itoa(i+1), v, amount})
	}
	return s
}

func TestTypstLaysOutDocuments(t *testing.T) {
	w := typstWriter(t, nil)
	wide := table(17, 3)
	for i := range 40 {
		row := []string{}
		for j := range 20 {
			if j < 17 {
				row = append(row, fmt.Sprintf("值%d-%d", i, j))
			} else {
				row = append(row, fmt.Sprintf("%d.%02d", i*1000+j, j))
			}
		}
		wide.Result.Rows = append(wide.Result.Rows, row)
	}
	many := table(2, 1)
	for i := range r.MaxRows {
		many.Result.Rows = append(many.Result.Rows, []string{fmt.Sprintf("公司%d", i%7), fmt.Sprintf("摘要 %d：应收账款含税及运费", i), fmt.Sprintf("%d.%02d", i*1234567, i%100)})
	}
	// Runs of punctuation that no line may start or end with, in narrow middle and last columns.
	marks := table(8, 1)
	for i := range 6 {
		row := []string{"紧急" + strings.Repeat("！", 60), strings.Repeat("。", 40), strings.Repeat("（", 30) + "开", "、、、、、、、、、、、、、、、、", "a ）b ）c",
			strings.Repeat("甲&", 10) + "乙、Receivable。", "¡¡¡    " + strings.Repeat("x", 12) + " ¿¿ yyyy ー#ー%Ⅰ文", strings.Repeat("—a", 12) + "⸺b⸻c——", strconv.Itoa(i) + ".00"}
		if i%2 == 1 {
			row[7] = strings.Repeat("！", 50)
		}
		marks.Result.Rows = append(marks.Result.Rows, row)
	}
	// Runs of every length around the width of their cell, with brackets glued on, next to a
	// long text that makes the columns shrink; and a bold total that is the widest number.
	edge := table(2, 1)
	edge.Result.Totals["m0"] = "-999999999999999999999999.99"
	for k := 1; k <= 40; k++ {
		long := ""
		if k == 1 {
			long = strings.Repeat("长文本说明，迫使各列收窄。", 30)
		}
		edge.Result.Rows = append(edge.Result.Rows, []string{long, strings.Repeat("W", k) + "（" + strings.Repeat("x", k) + "）", "-999999999999999999999999.99"})
	}
	// Text of every kind mixed at random, with a fixed seed, in columns squeezed so narrow that
	// numbers break across lines.
	mixed := table(9, 1)
	alphabet := []rune("甲乙丙丁文字表格数据汉ーあア、。，．！？：；（）「」《》“”‘’·…—#$%&*+-/<=>@[]{}~^|_ ¡¿Ⅰ①ＡＢａｂ０１abcxyzWMil.,;:!?'\"0123456789éü(jfVﬁ  \n")
	rnd := rand.New(rand.NewPCG(11, 1))
	for range 20 {
		row := make([]string, 10)
		for j := range 9 {
			b := make([]rune, rnd.IntN(70))
			for i := range b {
				b[i] = alphabet[rnd.IntN(len(alphabet))]
			}
			row[j] = string(b)
		}
		row[9] = strconv.Itoa(rnd.IntN(1e9)) + ".00"
		mixed.Result.Rows = append(mixed.Result.Rows, row)
	}
	// Pairs the font kerns outward, values that start with a line break, and ligature
	// characters.
	kerned := table(2, 1)
	for k := 1; k <= 30; k++ {
		long := ""
		if k == 1 {
			long = strings.Repeat("长文本说明，迫使各列收窄。", 30)
		}
		kerned.Result.Rows = append(kerned.Result.Rows, []string{long, strings.Repeat("(j", k) + strings.Repeat("fV", k%5), strconv.Itoa(k) + ".00"})
	}
	kerned.Result.Rows = append(kerned.Result.Rows, []string{"\n前", "\r\nabc", "1.00"}, []string{"   \nabc", "ﬁ ﬂ ﬀ only", "2.00"})
	docs := []struct {
		name     string
		s        r.Snapshot
		minPages int
	}{
		{"mixed", mixed, 1},
		{"kerned", kerned, 1},
		{"hostile", hostile(), 3},
		{"punctuation", marks, 1},
		{"edges", edge, 1},
		{"emptynodims", table(0, 2), 1},
		{"wide", wide, 1},
		{"nodims", table(0, 2, []string{"1.00", "-2.50"}), 1},
		{"empty", table(1, 1), 1},
		{"rows1000", many, 10},
	}
	for _, doc := range docs {
		t.Run(doc.name, func(t *testing.T) {
			b, err := w.Write(t.Context(), doc.s)
			if err != nil {
				var re *RenderError
				if errors.As(err, &re) {
					t.Fatalf("%v\n%s", err, re.output())
				}
				t.Fatal(err)
			}
			again, err := w.Write(t.Context(), doc.s)
			if err != nil || !bytes.Equal(b, again) {
				t.Fatalf("two renders with the same clock differ (%v)", err)
			}
			d, err := build(doc.s, newMeasurer(w.fonts), created)
			if err != nil {
				t.Fatal(err)
			}
			if doc.name == "wide" && !d.Landscape {
				t.Error("20 columns are not laid out on a landscape page")
			}
			evidence(t, doc.name, b, d, doc.s, doc.minPages)
		})
	}
	later := typstWriter(t, func(c *Config) { c.Now = func() time.Time { return created.Add(time.Hour) } })
	a, _ := w.Write(t.Context(), docs[2].s)
	b, _ := later.Write(t.Context(), docs[2].s)
	if bytes.Equal(a, b) {
		t.Error("the creation time is not recorded")
	}
}

func TestTypstFailsClosed(t *testing.T) {
	w := typstWriter(t, nil)
	ctx := t.Context()

	// An emoji, an Extension B ideograph and a zero-width space that Noto Sans SC does not have,
	// and a Hangul tone mark, which Typst would draw with a dotted circle.
	for _, v := range []string{"笑脸 😀", "扩展B区 \U00020000", "零宽\u200b空格", strings.Repeat("\u302e", 60)} {
		if _, err := w.Write(ctx, table(1, 1, []string{v, "1.00"})); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("%q: %v; want unsupported", v, err)
		}
	}

	// 32 narrow columns of long runs over 1000 rows lay out to more than 8 MiB.
	huge := table(MaxColumns, 0)
	for range r.MaxRows {
		row := make([]string, MaxColumns)
		for j := range row {
			row[j] = strings.Repeat("x", 30)
		}
		huge.Result.Rows = append(huge.Result.Rows, row)
	}
	if _, err := w.Write(ctx, huge); !errors.Is(err, r.ErrBudget) {
		t.Errorf("a document over the artifact limit: %v; want budget", err)
	}

	small := typstWriter(t, func(c *Config) { c.MemoryBytes = 256 << 20 })
	if _, err := small.Write(ctx, huge); !errors.Is(err, r.ErrBudget) {
		t.Errorf("Typst over its memory limit: %v; want budget", err)
	}
	var re *RenderError

	// Sources the server never writes, run in the same sandbox. Without a network, a package
	// import fails when name resolution times out after five seconds. Fonts other than the
	// configured ones are unknown families, which fail the run: fonts built into Typst and
	// fonts of the host. TYPST_FEATURES=html would make the html module known; Typst sees it
	// only if it inherits the environment.
	limited := typstWriter(t, func(c *Config) { c.Timeout = 10 * time.Second })
	t.Setenv("TYPST_FEATURES", "html")
	sources := map[string]string{
		"a package import":               `#import "@preview/example:0.1.0": *`,
		"a file outside the root":        `#read("/etc/hostname")`,
		"an unknown font family":         `#text(font: "No Such Family", fallback: false)[x]`,
		"a font built into Typst":        `#text(font: "Libertinus Serif", fallback: false)[x]`,
		"a setting from the environment": `#html.elem("p")[x]`,
	}
	output := map[string]string{"a package import": "failed to download package", "a setting from the environment": "unknown variable: html"}
	// A family the host offers, if any: Typst lists the host's fonts unless told otherwise.
	listing, err := limited.run(ctx, t.TempDir(), "fonts", "--ignore-embedded-fonts")
	if err != nil {
		t.Fatal(err)
	}
	if host := strings.SplitN(strings.TrimSpace(string(listing)), "\n", 2)[0]; host != "" {
		sources["a font of the host"] = `#text(font: "` + host + `", fallback: false)[x]`
	} else {
		t.Log("the host has no fonts of its own")
	}
	for name, source := range sources {
		_, err := limited.renderSource(ctx, source)
		if !errors.As(err, &re) {
			t.Errorf("%s: %v; want a Typst failure", name, err)
		} else if !strings.Contains(re.output(), output[name]) {
			t.Errorf("%s: %s", name, re.output())
		}
	}
	quick := typstWriter(t, func(c *Config) { c.Timeout = time.Second })
	start := time.Now()
	if _, err := quick.renderSource(ctx, `#for i in range(1000) { for j in range(1000) { for k in range(1000) { } } }`); !errors.Is(err, r.ErrBudget) {
		t.Errorf("a loop past the time limit: %v; want budget", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the time limit stopped Typst after %s", took)
	}

	// A binary replaced or modified after startup is refused.
	bin := filepath.Join(t.TempDir(), "typst")
	data, err := os.ReadFile(typstConfig(t).Typst)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, data, 0o700); err != nil {
		t.Fatal(err)
	}
	copied := typstWriter(t, func(c *Config) { c.Typst = bin })
	f, err := os.OpenFile(bin, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0})
	f.Close()
	if _, err := copied.Write(ctx, table(1, 1, []string{"a", "1.00"})); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a modified binary: %v; want integrity failure", err)
	}
}

// renderSource runs Typst on a source the server would never write, in the usual sandbox.
func (w *Writer) renderSource(ctx context.Context, source string) ([]byte, error) {
	dir, err := os.MkdirTemp(w.cfg.WorkDir, "reporting-pdf-test-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "main.typ"), []byte(source), 0o600); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dir, "packages"), 0o700); err != nil {
		return nil, err
	}
	return w.render(ctx, dir, created)
}

// fontsNamed copies the configured fonts with their family name changed to one that Typst
// shortens: it drops a trailing region word such as "CN".
func fontsNamed(t *testing.T, from, to string) string {
	t.Helper()
	src := typstConfig(t).FontDir
	dir := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	utf16 := func(s string) []byte {
		var b []byte
		for _, c := range s {
			b = append(b, 0, byte(c))
		}
		return b
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b = bytes.ReplaceAll(bytes.ReplaceAll(b, []byte(from), []byte(to)), utf16(from), utf16(to))
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestTypstRefusesFontsItNamesDifferently(t *testing.T) {
	c := typstConfig(t)
	c.FontDir = fontsNamed(t, "Noto Sans SC", "Noto Sans CN")
	_, err := New(c)
	if err == nil || !strings.Contains(err.Error(), "other font families") {
		t.Fatalf("fonts that Typst calls \"Noto Sans\" while their name table says \"Noto Sans CN\": %v", err)
	}
}

// TestTypstWithAHardCoreLimitOfZero runs in a child process, because lowering a hard limit
// cannot be undone.
func TestTypstWithAHardCoreLimitOfZero(t *testing.T) {
	if os.Getenv("REPORTING_PDF_CORE_ZERO") == "1" {
		if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{}); err != nil {
			t.Fatal(err)
		}
		_, err := New(typstConfig(t))
		fmt.Printf("NEW_RESULT=%v\n", err)
		return
	}
	typstConfig(t)
	pattern, err := os.ReadFile("/proc/sys/kernel/core_pattern")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTypstWithAHardCoreLimitOfZero$", "-test.count=1")
	cmd.Env = append(os.Environ(), "REPORTING_PDF_CORE_ZERO=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	piped := strings.HasPrefix(string(pattern), "|")
	refused := strings.Contains(string(out), "NEW_RESULT=pdf: the hard core limit is 0")
	if piped != refused || (!piped && !strings.Contains(string(out), "NEW_RESULT=<nil>")) {
		t.Fatalf("core_pattern %q: %s", pattern, out)
	}
}

// documentExpectation is what scripts/check_pdf_export.py checks a document preview against:
// the template's own boxes in millimetres and the values, from which it derives positions.
type documentExpectation struct {
	Kind           string            `json:"kind"`
	File           string            `json:"file"`
	Background     bool              `json:"background"`
	BackgroundFile string            `json:"background_file,omitempty"` // the PNG drawn, next to the PDF
	Mode           string            `json:"mode"`                      // full or variables
	OffsetX        float64           `json:"offset_x"`
	OffsetY        float64           `json:"offset_y"`
	Elements       []documentElement `json:"elements"`
	Table          *tableExpectation `json:"table,omitempty"`
}

// tableExpectation describes a document's table for the checker: the region and type size,
// the columns with their labels and kinds, the raw rows and the snapshot totals; the checker
// recomputes the carried sums and derives every position itself.
type tableExpectation struct {
	X       float64             `json:"x"`
	Y       float64             `json:"y"`
	Width   float64             `json:"width"`
	Height  float64             `json:"height"`
	Size    int                 `json:"size"`
	Columns []tableColumnExpect `json:"columns"`
	Rows    [][]string          `json:"rows"`
	Totals  map[string]string   `json:"totals"`
	// HeaderLines is the header's height in lines, which the variable layer does not draw.
	HeaderLines int `json:"header_lines"`
}

type tableColumnExpect struct {
	Field  string  `json:"field"`
	Label  string  `json:"label"`
	Width  float64 `json:"width"`
	Metric bool    `json:"metric"`
	Scale  int     `json:"scale"`
}

type documentElement struct {
	Text   string  `json:"text"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Size   int     `json:"size"`
	Align  string  `json:"align"`
}

func documentEvidence(t *testing.T, name string, pdf []byte, d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData, fonts *fontSet) {
	t.Helper()
	dir := os.Getenv("REPORTING_PDF_EVIDENCE")
	if dir == "" {
		return
	}
	e := documentExpectation{Kind: "document", File: name + ".pdf", Background: background != nil, Mode: d.Print.Mode, OffsetX: d.Print.OffsetX, OffsetY: d.Print.OffsetY, Elements: []documentElement{}}
	if d.Table != nil {
		te := &tableExpectation{X: d.Table.X, Y: d.Table.Y, Width: d.Table.Width, Height: d.Table.Height, Size: d.Table.FontSize, Rows: table.Rows, Totals: table.Totals}
		if p, err := layOut(d, values, background, table, newPlainMeasurer(fonts)); err == nil {
			te.HeaderLines = p.HeaderLines
		}
		for i, c := range d.Table.Columns {
			col := table.Columns[i]
			label := col.Label
			switch {
			case col.Currency != "" && col.Unit != "":
				label += "（" + col.Currency + " · " + col.Unit + "）"
			case col.Currency != "":
				label += "（" + col.Currency + "）"
			case col.Unit != "":
				label += "（" + col.Unit + "）"
			}
			te.Columns = append(te.Columns, tableColumnExpect{Field: c.Field, Label: label, Width: c.Width, Metric: table.Metric[i] && (col.Type == "decimal" || col.Type == "integer"), Scale: col.Scale})
		}
		e.Table = te
	}
	if background != nil {
		e.BackgroundFile = name + ".background.png"
		if err := os.WriteFile(filepath.Join(dir, e.BackgroundFile), background, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, el := range d.Elements {
		if d.Print.Mode == "variables" && el.Source == "literal" {
			continue
		}
		e.Elements = append(e.Elements, documentElement{Text: values[el.ID], X: el.X, Y: el.Y, Width: el.Width, Height: el.Height, Size: el.FontSize, Align: el.Align})
	}
	b, _ := json.MarshalIndent(e, "", " ")
	for file, content := range map[string][]byte{name + ".pdf": pdf, name + ".json": b} {
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTypstRendersDocuments(t *testing.T) {
	w := typstWriter(t, nil)
	png, err := os.ReadFile("../../doctemplates/testdata/a4-background.png")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(png)
	elements := []doctemplates.Element{
		{ID: "n1", Source: "literal", Text: "套打底稿预览 Form", X: 20, Y: 30, Width: 120, Height: 12, FontSize: 14, Align: "left"},
		{ID: "n2", Source: "row", Field: "ar.open_balance", X: 20, Y: 50, Width: 80, Height: 8, FontSize: 10, Align: "right"},
		{ID: "n3", Source: "total", Field: "ar.open_balance", X: 110.5, Y: 50, Width: 80, Height: 8, FontSize: 10, Align: "center"},
		{ID: "n4", Source: "row", Field: "customer", X: 20, Y: 249.7, Width: 170, Height: 16, FontSize: 28, Align: "left"},
	}
	values := map[string]string{"n1": "套打底稿预览 Form", "n2": "9007199254740893.00", "n3": "9007199254740993.01", "n4": "  客户 a  b"}
	doc := func(mode string, ox, oy float64) doctemplates.Document {
		return doctemplates.Document{Schema: "1.2", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}}, Elements: elements,
			Print: &doctemplates.PrintProfile{Mode: mode, OffsetX: ox, OffsetY: oy}, Background: &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: hex.EncodeToString(sum[:])}}
	}
	// Values that Typst would otherwise shape differently from plain advances: Chinese next to
	// Latin and digits, Chinese punctuation at the start, in a row and alone, dashes, kerned
	// pairs, combining marks, leading and trailing spaces of both kinds, at every alignment.
	hard := []string{"张江路123号A座", "客户：华东区「重点」，联系人：张三。", "a—a—a⸺b⸻c", "ab   ", "  ab  ", "café e\u0301", "甲&甲&乙、Receivable。", "……——",
		"ー#ー%Ⅰ文", "「（中）」。。、", "（甲）", "。", "ab\u3000\u3000", "\u3000ab", "9007199254740893.00", "(j(j(j fV", "W"}
	hardDoc := doctemplates.Document{Schema: "1.1", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}}, Print: &doctemplates.PrintProfile{Mode: "full"}}
	hardValues := map[string]string{}
	aligns := []string{"left", "right", "center"}
	for i, v := range hard {
		id := "n" + strconv.Itoa(i+1)
		e := doctemplates.Element{ID: id, Source: "literal", Text: v, X: 15 + float64(i%2)*95, Y: 30 + float64(i/2)*12, Width: 85, Height: 10, FontSize: 12, Align: aligns[i%3]}
		if i >= 15 {
			e.Y, e.Height, e.FontSize = 140, 16, 28
		} else if i == 14 {
			e.FontSize = 8
		}
		hardDoc.Elements = append(hardDoc.Elements, e)
		hardValues[id] = v
	}
	// A background that is not A4-shaped (the asset store refuses one, the renderer fits it).
	narrow := grayPNG(700, 891)
	narrowSum := sha256.Sum256(narrow)
	fitted := doc("full", 0, 0)
	fitted.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("b", 32), SHA256: hex.EncodeToString(narrowSum[:])}
	// A document with a detail table (schema 1.3): boxes above, the table below, over several
	// pages, with and without the background, in full and variables mode.
	tableDoc := func(mode string, withBackground bool) doctemplates.Document {
		d := doctemplates.Document{Schema: "1.3", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
			Elements: []doctemplates.Element{
				{ID: "n1", Source: "literal", Text: "对账单 Statement", X: 20, Y: 30, Width: 120, Height: 12, FontSize: 14, Align: "left"},
				{ID: "n2", Source: "row", Field: "customer", X: 20, Y: 46, Width: 80, Height: 8, FontSize: 10, Align: "left"},
				{ID: "n3", Source: "total", Field: "ar.open_balance", X: 110.5, Y: 46, Width: 80, Height: 8, FontSize: 10, Align: "right"},
			},
			Print: &doctemplates.PrintProfile{Mode: mode},
			Table: &doctemplates.Table{X: 15, Y: 60, Width: 180, Height: 200, FontSize: 9, Columns: []doctemplates.TableColumn{{Field: "customer", Width: 40}, {Field: "note", Width: 80}, {Field: "amount", Width: 60}}}}
		if withBackground {
			d.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: hex.EncodeToString(sum[:])}
		}
		return d
	}
	tableData := statementRows(60)
	tableValues := map[string]string{"n1": "对账单 Statement", "n2": "华东区客户", "n3": "9007199254740993.01"}
	// The hard values of the box fixture as table cells, with print offsets: Chinese next to
	// Latin, punctuation in runs, dashes, kerned pairs, marks and spaces, each on its own line
	// or wrapped, so that every character's position is checked in a table too.
	hardTableDoc := tableDoc("full", false)
	hardTableDoc.Print = &doctemplates.PrintProfile{Mode: "full", OffsetX: -2.5, OffsetY: 3.1}
	hardTableDoc.Table = &doctemplates.Table{X: 20, Y: 60, Width: 100, Height: 120, FontSize: 12, Columns: []doctemplates.TableColumn{{Field: "note", Width: 60}, {Field: "amount", Width: 40}}}
	hardTableData := &doctemplates.TableData{Columns: []r.Column{{ID: "note", Label: "备注", Type: "string"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2}}, Metric: []bool{false, true}, Totals: map[string]string{}}
	hardCents := int64(0)
	for i, v := range hard {
		hardTableData.Rows = append(hardTableData.Rows, []string{v, fmt.Sprintf("%d.%02d", i*10, i)})
		hardCents += int64(i*1000 + i)
	}
	hardTableData.Totals["amount"] = fmt.Sprintf("%d.%02d", hardCents/100, hardCents%100)
	// A column whose fractions grow row by row, next to an integer column.
	mixedDoc := tableDoc("full", false)
	mixedDoc.Table = &doctemplates.Table{X: 15, Y: 60, Width: 180, Height: 60, FontSize: 10, Columns: []doctemplates.TableColumn{{Field: "customer", Width: 100}, {Field: "mixed", Width: 40}, {Field: "count", Width: 40}}}
	mixedData := &doctemplates.TableData{Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "mixed", Label: "混合", Type: "decimal"}, {ID: "count", Label: "件数", Type: "integer"}}, Metric: []bool{false, true, true},
		Rows: [][]string{{"a", "1", "3"}, {"b", "2.5", "-1"}, {"c", "0.125", "0"}, {"d", "-1", "10"}}, Totals: map[string]string{"mixed": "2.625", "count": "12"}}
	// The variable layer with labels that wrap to three lines: the header's height is reserved
	// although nothing of it is drawn.
	wrapHeaderDoc := tableDoc("variables", false)
	wrapHeaderDoc.Table = &doctemplates.Table{X: 15, Y: 60, Width: 180, Height: 80, FontSize: 10, Columns: []doctemplates.TableColumn{{Field: "customer", Width: 100}, {Field: "amount", Width: 40}, {Field: "count", Width: 40}}}
	wrapHeaderData := &doctemplates.TableData{Columns: []r.Column{{ID: "customer", Label: "客户名称（合同主体，含分支机构及关联方）", Type: "string"}, {ID: "amount", Label: "应收账款未结余额合计", Type: "decimal", Scale: 2, Currency: "CNY"}, {ID: "count", Label: "Open invoices outstanding at the cut-off date", Type: "integer"}}, Metric: []bool{false, true, true},
		Rows: [][]string{{"甲", "1.00", "1"}, {"乙", "2.00", "2"}}, Totals: map[string]string{"amount": "3.00", "count": "3"}}
	// One text column with blank rows among the values, and no metric at all.
	blankDoc := tableDoc("full", false)
	blankDoc.Table = &doctemplates.Table{X: 15, Y: 60, Width: 180, Height: 40, FontSize: 10, Columns: []doctemplates.TableColumn{{Field: "note", Width: 180}}}
	blankData := &doctemplates.TableData{Columns: []r.Column{{ID: "note", Label: "备注", Type: "string"}}, Metric: []bool{false}, Totals: map[string]string{},
		Rows: [][]string{{"first"}, {""}, {"   "}, {"after two blank rows"}, {"\n"}, {"last"}}}
	// Twelve columns at 8 pt, eleven of them 12 mm wide: every column a metric with amounts
	// under one yuan whose carried sums still fit, the first column a metric too (its sum is
	// not shown; it holds the labels, so it is 48 mm wide).
	narrowDoc := tableDoc("full", false)
	narrowDoc.Table = &doctemplates.Table{X: 15, Y: 60, Width: 180, Height: 120, FontSize: 8}
	narrowData := &doctemplates.TableData{Totals: map[string]string{}}
	cents := func(i, j int) int64 { return int64((i*7+j*13)%90 + 1) }
	for j := range 12 {
		id := "m" + strconv.Itoa(j)
		width := 12.0
		if j == 0 {
			width = 48
		}
		narrowDoc.Table.Columns = append(narrowDoc.Table.Columns, doctemplates.TableColumn{Field: id, Width: width})
		narrowData.Columns = append(narrowData.Columns, r.Column{ID: id, Label: "指标" + strconv.Itoa(j), Type: "decimal", Scale: 2})
		narrowData.Metric = append(narrowData.Metric, true)
		total := int64(0)
		for i := range 40 {
			total += cents(i, j)
		}
		narrowData.Totals[id] = fmt.Sprintf("%d.%02d", total/100, total%100)
	}
	for i := range 40 {
		row := make([]string, 12)
		for j := range 12 {
			row[j] = fmt.Sprintf("%d.%02d", cents(i, j)/100, cents(i, j)%100)
		}
		narrowData.Rows = append(narrowData.Rows, row)
	}
	for _, c := range []struct {
		name   string
		d      doctemplates.Document
		values map[string]string
		png    []byte
		table  *doctemplates.TableData
	}{
		{"document_full", doc("full", 0, 0), values, png, nil},
		{"document_calibrated", doc("full", 1.2, -0.5), values, png, nil},
		{"document_variables", doc("variables", -2.3, 4), values, nil, nil},
		{"document_hard", hardDoc, hardValues, nil, nil},
		{"document_fitted", fitted, values, narrow, nil},
		{"document_table", tableDoc("full", true), tableValues, png, tableData},
		{"document_table_variables", tableDoc("variables", false), tableValues, nil, tableData},
		{"document_table_narrow", narrowDoc, tableValues, nil, narrowData},
		{"document_table_empty", tableDoc("full", false), tableValues, nil, &doctemplates.TableData{Columns: tableData.Columns, Metric: tableData.Metric, Rows: [][]string{}, Totals: map[string]string{"amount": "0.00"}}},
		{"document_table_blank", blankDoc, tableValues, nil, blankData},
		{"document_table_hard", hardTableDoc, tableValues, nil, hardTableData},
		{"document_table_mixed", mixedDoc, tableValues, nil, mixedData},
		{"document_table_wrapheader_variables", wrapHeaderDoc, tableValues, nil, wrapHeaderData},
	} {
		out, err := w.Render(t.Context(), c.d, c.values, c.png, c.table)
		if err != nil {
			var re *RenderError
			if errors.As(err, &re) {
				t.Fatalf("%s: %v\n%s", c.name, err, re.output())
			}
			t.Fatalf("%s: %v", c.name, err)
		}
		again, err := w.Render(t.Context(), c.d, c.values, c.png, c.table)
		if err != nil || !bytes.Equal(out, again) {
			t.Fatalf("%s: two renders with the same clock differ (%v)", c.name, err)
		}
		documentEvidence(t, c.name, out, c.d, c.values, c.png, c.table, w.fonts)
	}
	// Refused before Typst runs: a value too wide for its box, one the fonts lack, a Hangul
	// tone mark (Typst would add a dotted circle), a tab and a leading combining mark.
	for value, want := range map[string]error{strings.Repeat("宽", 40): r.ErrUnsupported, "😀": r.ErrUnsupported, "a\u302eb": r.ErrUnsupported, "a\tb": r.ErrUnsupported, "\u0301abc": r.ErrUnsupported} {
		v := map[string]string{"n1": "x", "n2": value, "n3": "1", "n4": "1"}
		if _, err := w.Render(t.Context(), doc("variables", 0, 0), v, nil, nil); !errors.Is(err, want) {
			t.Errorf("%q: %v; want %v", value, err, want)
		}
	}
	// The upload check renders on the background: a PNG the templates accept passes.
	if err := w.BackgroundFits(t.Context(), png); err != nil {
		t.Errorf("background fit: %v", err)
	}
	// The template's own assertion: a glyph declared narrower than Typst draws it fails the
	// render, and that is an unsupported value.
	p, err := layOut(doc("variables", 0, 0), map[string]string{"n2": "W", "n3": "1", "n4": "1"}, nil, nil, newPlainMeasurer(w.fonts))
	if err != nil {
		t.Fatal(err)
	}
	p.Pages[0].Texts[0].Width = 0
	if _, err := w.renderDocument(t.Context(), p, nil, nil, created); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a glyph wider than declared: %v; want unsupported", err)
	}
	// Renders share the Writer's slots: six at once all succeed, byte for byte alike.
	results := make([][]byte, 6)
	errs := make([]error, 6)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = w.Render(t.Context(), hardDoc, hardValues, nil, nil)
		}()
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || !bytes.Equal(results[i], results[0]) {
			t.Errorf("concurrent render %d: %v", i, errs[i])
		}
	}
}

// grayPNG is a white PNG of the given size with a grey frame.
func grayPNG(w, h int) []byte {
	im := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(255)
			if x < 8 || y < 8 || x >= w-8 || y >= h-8 {
				v = 120
			}
			im.SetGray(x, y, color.Gray{Y: v})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func TestTypstSlotsBoundPreviewsAndExports(t *testing.T) {
	w := typstWriter(t, func(c *Config) { c.Concurrency, c.Timeout = 1, 1500*time.Millisecond })
	d, values, _ := documentProbe()
	d.Schema, d.Background = "1.1", nil
	// The only slot is held: a preview, an export and the upload check all wait for the timeout
	// and are budget errors; once it is free, they render.
	w.slots <- struct{}{}
	if _, err := w.Render(t.Context(), d, values, nil, nil); !errors.Is(err, r.ErrBudget) {
		t.Errorf("preview with no slot: %v; want budget", err)
	}
	if _, err := w.Write(t.Context(), probe); !errors.Is(err, r.ErrBudget) {
		t.Errorf("export with no slot: %v; want budget", err)
	}
	if err := w.BackgroundFits(t.Context(), probeBackground()); !errors.Is(err, r.ErrBudget) {
		t.Errorf("upload check with no slot: %v; want budget", err)
	}
	release(w.slots)
	if _, err := w.Render(t.Context(), d, values, nil, nil); err != nil {
		t.Errorf("preview with the slot free: %v", err)
	}
	// Previews hold at most all but one of the slots (here: the one preview slot), which keeps
	// the export worker from waiting behind them: with the preview slot held, a preview waits
	// in vain while an export still runs.
	w.previews <- struct{}{}
	if _, err := w.Render(t.Context(), d, values, nil, nil); !errors.Is(err, r.ErrBudget) {
		t.Errorf("preview with no preview slot: %v; want budget", err)
	}
	if _, err := w.Write(t.Context(), probe); err != nil {
		t.Errorf("export while previews are saturated: %v", err)
	}
	release(w.previews)
}

func TestNewRefusesBadLimits(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"concurrency below one": func(c *Config) { c.Concurrency = -1 },
		"concurrency above 64":  func(c *Config) { c.Concurrency = 65 },
		"jobs above 64":         func(c *Config) { c.Jobs = 65 },
		"timeout over the work": func(c *Config) { c.Timeout = r.WorkTimeout },
		"memory under 256 MiB":  func(c *Config) { c.MemoryBytes = 1 << 20 },
	} {
		c := Config{Typst: "/nonexistent", TypstSHA256: strings.Repeat("0", 64), FontDir: t.TempDir()}
		change(&c)
		if _, err := New(c); err == nil || !strings.Contains(err.Error(), "limits") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// statementRows is the table of the statement fixtures: a customer, a note that wraps (long
// Chinese without spaces, English with spaces, a value's own line break, a run wider than the
// cell) and an exact amount, with the exact total.
func statementRows(n int) *doctemplates.TableData {
	data := &doctemplates.TableData{Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "note", Label: "备注", Type: "string"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2, Currency: "CNY"}}, Metric: []bool{false, false, true}, Totals: map[string]string{}}
	cents := int64(0)
	for i := range n {
		note := fmt.Sprintf("第%d行 备注：%s", i, strings.Repeat("甲乙丙丁", i%5))
		switch {
		case i%7 == 0:
			note += "\nsecond line of the note, wrapped at spaces when it is too long for the cell"
		case i%11 == 0:
			note += " averyveryverylongwordwithoutanyspaces_abcdefghijklmnopqrstuvwxyz0123456789"
		case i%13 == 0:
			note = ""
		}
		v := int64(100*i*100 + 25) // 100i + 0.25
		if i%3 == 0 {
			v = -v
		}
		cents += v
		sign := ""
		if v < 0 {
			sign, v = "-", -v
		}
		data.Rows = append(data.Rows, []string{fmt.Sprintf("客户%d", i), note, fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)})
	}
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	data.Totals["amount"] = fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
	return data
}

// singleBox is a document of one box that prints the customer.
func singleBox() doctemplates.Document {
	return doctemplates.Document{Schema: "1.1", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Elements: []doctemplates.Element{{ID: "n1", Source: "row", Field: "customer", X: 20, Y: 30, Width: 120, Height: 12, FontSize: 14, Align: "left"}}, Print: &doctemplates.PrintProfile{Mode: "full"}}
}

// issueStamp is the line an issued document carries (S03-H): "ISSUED No. <number> - <issue time>
// - <record id>", the number being the string the source product reserved and the time UTC to
// the second. It is spelled out here, not taken from the reporting package, so that the
// contract the checker verifies is stated where the documents are made.
func issueStamp(number string, at time.Time, id string) string {
	return "ISSUED No. " + number + " - " + at.UTC().Format("2006-01-02T15:04:05Z") + " - " + id
}

// infoCreation is how a PDF's information dictionary records the creation date at.
func infoCreation(at time.Time) []byte {
	return []byte("/CreationDate(D:" + at.UTC().Format("20060102150405") + "Z)")
}

// TestTypstIssuesDocuments renders official documents (S03-G, S03-H): the preview's pages with
// the stamp of the product's number in the banner strip, in the text colour (checked on the
// laid-out page; the checker verifies the drawn PDF); the page count is returned; an empty
// stamp, one with a control character or one naming the banner is invalid, one that would not
// fit the strip or that the fonts lack is unsupported. The product's issue time, not the
// writer's clock, is the PDF's creation date: writers whose clocks differ issue the same
// bytes, another issue time gives other bytes, and an issue time that is not a UTC whole second
// is invalid.
func TestTypstIssuesDocuments(t *testing.T) {
	w := typstWriter(t, nil)
	// A writer whose clock is a day and a half ahead.
	later := typstWriter(t, func(c *Config) { c.Now = func() time.Time { return created.Add(36 * time.Hour) } })
	png, err := os.ReadFile("../../doctemplates/testdata/a4-background.png")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(png)
	tableDoc := func(mode string, withBackground bool) doctemplates.Document {
		d := doctemplates.Document{Schema: "1.3", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
			Elements: []doctemplates.Element{
				{ID: "n1", Source: "literal", Text: "对账单 Statement", X: 20, Y: 30, Width: 120, Height: 12, FontSize: 14, Align: "left"},
				{ID: "n2", Source: "row", Field: "customer", X: 20, Y: 46, Width: 80, Height: 8, FontSize: 10, Align: "left"},
				{ID: "n3", Source: "total", Field: "ar.open_balance", X: 110.5, Y: 46, Width: 80, Height: 8, FontSize: 10, Align: "right"},
			},
			Print: &doctemplates.PrintProfile{Mode: mode, OffsetX: 0.5, OffsetY: -0.3},
			Table: &doctemplates.Table{X: 15, Y: 60, Width: 180, Height: 200, FontSize: 9, Columns: []doctemplates.TableColumn{{Field: "customer", Width: 40}, {Field: "note", Width: 80}, {Field: "amount", Width: 60}}}}
		if withBackground {
			d.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: hex.EncodeToString(sum[:])}
		}
		return d
	}
	values := map[string]string{"n1": "对账单 Statement", "n2": "华东区客户", "n3": "9007199254740993.01"}
	single := singleBox()
	id := strings.Repeat("7", 32)
	at := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	for _, c := range []struct {
		name   string
		d      doctemplates.Document
		values map[string]string
		png    []byte
		table  *doctemplates.TableData
		number string
		pages  int
	}{
		{"document_issue", tableDoc("full", true), values, png, statementRows(60), "AR-000012", 3},
		{"document_issue_variables", tableDoc("variables", false), values, nil, statementRows(60), "SH-999999", 3},
		{"document_issue_single", single, map[string]string{"n1": "华东区客户"}, nil, nil, "AR-000001", 1},
	} {
		stamp := issueStamp(c.number, at, id)
		out, pages, err := w.Issue(t.Context(), c.d, c.values, c.png, c.table, stamp, at)
		if err != nil {
			var re *RenderError
			if errors.As(err, &re) {
				t.Fatalf("%s: %v\n%s", c.name, err, re.output())
			}
			t.Fatalf("%s: %v", c.name, err)
		}
		if pages != c.pages {
			t.Errorf("%s: %d pages, want %d", c.name, pages, c.pages)
		}
		again, _, err := w.Issue(t.Context(), c.d, c.values, c.png, c.table, stamp, at)
		if err != nil || !bytes.Equal(out, again) {
			t.Fatalf("%s: two issues of the same inputs differ (%v)", c.name, err)
		}
		// The product's issue time dates the document, not the writer's clock: a writer whose
		// clock differs issues the same bytes; another issue time under the same stamp gives
		// other bytes (the creation date alone differs); the previews, which carry their
		// writers' clocks, differ.
		fromLater, _, err := later.Issue(t.Context(), c.d, c.values, c.png, c.table, stamp, at)
		if err != nil || !bytes.Equal(out, fromLater) {
			t.Fatalf("%s: writers whose clocks differ issue different bytes (%v)", c.name, err)
		}
		otherTime, _, err := w.Issue(t.Context(), c.d, c.values, c.png, c.table, stamp, at.Add(time.Second))
		if err != nil || bytes.Equal(out, otherTime) {
			t.Fatalf("%s: another issue time gives the same bytes (%v)", c.name, err)
		}
		preview, err := w.Render(t.Context(), c.d, c.values, c.png, c.table)
		if err != nil || bytes.Equal(preview, out) {
			t.Fatalf("%s: the issue is the preview (%v)", c.name, err)
		}
		laterPreview, err := later.Render(t.Context(), c.d, c.values, c.png, c.table)
		if err != nil || bytes.Equal(preview, laterPreview) {
			t.Fatalf("%s: previews of writers whose clocks differ are alike (%v)", c.name, err)
		}
		if !bytes.Contains(out, infoCreation(at)) || bytes.Contains(out, infoCreation(created)) || !bytes.Contains(preview, infoCreation(created)) || bytes.Contains(preview, infoCreation(at)) {
			t.Errorf("%s: the issue is not dated with the issue time or the preview not with the writer's clock", c.name)
		}
		documentEvidence(t, c.name, out, c.d, c.values, c.png, c.table, w.fonts)
		issueEvidence(t, c.name, out, preview, c.number, at, id, pages)
		// What Typst is told to draw: the stamp in the strip in the text colour on every page
		// (the strip is one banner for all pages), the preview's title gone, and no glyph of
		// the banner text anywhere; the preview keeps its banner.
		p, err := w.issuePage(c.d, c.values, c.png, c.table, stamp)
		if err != nil || p.Banner.Text != stamp || p.Banner.Fill != stampFill || p.Title != "Issued document" || len(p.Pages) != c.pages {
			t.Fatalf("%s: issue page %+v %v", c.name, p.Banner, err)
		}
		// The stamp is set at its own size, 8 pt, from the strip's left edge and top: the
		// banner's 10 pt would not hold every number the contract allows.
		if p.Banner.Size != stampSize || stampSize != 8 || !near(p.Banner.Baseline, 10*mm+ascent*8) || !near(p.Banner.Left, 10*mm) || !near(p.Banner.Top, 10*mm) {
			t.Fatalf("%s: the stamp is not 8 pt in the strip: %+v", c.name, p.Banner)
		}
		for _, pg := range p.Pages {
			texts := ""
			for _, g := range pg.Texts {
				texts += g.Text
			}
			if strings.Contains(texts, bannerText) {
				t.Errorf("%s: a page of the issue draws the banner text", c.name)
			}
		}
		pv, err := layOut(c.d, c.values, c.png, c.table, newPlainMeasurer(w.fonts))
		if err != nil || pv.Banner.Text != bannerText || pv.Banner.Fill != bannerFill || pv.Banner.Size != 10 || !near(pv.Banner.Baseline, 10*mm+ascent*10) {
			t.Fatalf("%s: preview banner %+v %v", c.name, pv.Banner, err)
		}
	}
	// Refused before Typst runs: an empty stamp, one with a control character, one naming
	// the preview banner, one longer than the strip.
	for stamp, want := range map[string]error{"": r.ErrInvalid, "ISSUED\tNo. 1": r.ErrInvalid, "PREVIEW - NOT AN ISSUED DOCUMENT": r.ErrInvalid, strings.Repeat("W", 120): r.ErrUnsupported, "ISSUED 😀": r.ErrUnsupported} {
		if _, _, err := w.Issue(t.Context(), single, map[string]string{"n1": "x"}, nil, nil, stamp, at); !errors.Is(err, want) {
			t.Errorf("stamp %q: %v; want %v", stamp, err, want)
		}
	}
	// The issue time is the product's: a UTC whole second that a PDF can carry as its creation
	// date, else invalid. The first and the last second Typst can write are issued.
	for name, bad := range map[string]time.Time{
		"zero":        {},
		"not in UTC":  at.In(time.FixedZone("UTC+8", 8*3600)),
		"fractional":  at.Add(500 * time.Millisecond),
		"before 1970": time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC),
		"after 9999":  time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		if _, _, err := w.Issue(t.Context(), single, map[string]string{"n1": "x"}, nil, nil, issueStamp("AR-000001", bad, id), bad); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("issue time %s: %v; want invalid", name, err)
		}
	}
	for name, edge := range map[string]time.Time{"first second of 1970": time.Unix(0, 0).UTC(), "last second of 9999": time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)} {
		out, _, err := w.Issue(t.Context(), single, map[string]string{"n1": "x"}, nil, nil, issueStamp("AR-000001", edge, id), edge)
		if err != nil || !bytes.Contains(out, infoCreation(edge)) {
			t.Errorf("issue time %s: %v", name, err)
		}
	}
	// The banner text as a value or a cell is refused: an issued document never says PREVIEW.
	stamp := issueStamp("AR-000001", at, id)
	if _, _, err := w.Issue(t.Context(), single, map[string]string{"n1": "x " + bannerText}, nil, nil, stamp, at); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("banner text as a value: %v; want unsupported", err)
	}
	cells := statementRows(2)
	cells.Rows[1][1] = bannerText
	if _, _, err := w.Issue(t.Context(), tableDoc("full", false), values, nil, cells, stamp, at); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("banner text as a cell: %v; want unsupported", err)
	}
	// An issue takes an export slot, not a preview slot: with the previews saturated it still
	// renders; with no slot at all it is a budget error.
	w.previews <- struct{}{}
	if _, _, err := w.Issue(t.Context(), single, map[string]string{"n1": "x"}, nil, nil, stamp, at); err != nil {
		t.Errorf("issue while previews are saturated: %v", err)
	}
	release(w.previews)
}

// TestTypstIssuesTheWidestLegalStamp: the worker must never void a number the product reserved
// because of our layout, so the stamp of the widest number, record id and issue time the contract
// allows has to fit the strip, with margin, and render. Widths are the plain advances the writer
// measures, at the size the stamp is drawn at: 32 times the widest glyph of A-Za-z0-9._- as the
// number, 32 times the widest lowercase hex digit as the record id, and the widest legal time.
func TestTypstIssuesTheWidestLegalStamp(t *testing.T) {
	w := typstWriter(t, nil)
	m := newPlainMeasurer(w.fonts)
	width := func(s string, size float64) float64 {
		v, err := m.width(s, size, false)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		return v
	}
	widest := func(chars string) string {
		best := ""
		for _, c := range chars {
			if best == "" || width(string(c), stampSize) > width(best, stampSize) {
				best = string(c)
			}
		}
		return best
	}
	number := strings.Repeat(widest("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-"), 32)
	id := strings.Repeat(widest("0123456789abcdef"), 32)
	if len(number) != 32 || !r.ValidIssueNumber(number) || len(id) != 32 {
		t.Fatalf("the widest number %q or id %q is not one the contract allows", number, id)
	}
	// Every legal time prints digits where 9999-12-31T23:59:59Z does, so none is wider than that
	// shape drawn with the widest digit; the latest second is as wide as it, and so the widest.
	latest := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	stampTime := latest.UTC().Format("2006-01-02T15:04:05Z")
	digit := widest("0123456789")
	shape := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return rune(digit[0])
		}
		return c
	}, stampTime)
	if width(shape, stampSize)-width(stampTime, stampSize) > 1e-9 {
		t.Fatalf("a legal time can be wider than %s: %s is %.3f pt, %s %.3f pt", stampTime, shape, width(shape, stampSize), stampTime, width(stampTime, stampSize))
	}
	stamp := issueStamp(number, latest, id)
	got := width(stamp, stampSize)
	t.Logf("the widest legal stamp, %q: %.2f pt at %v pt; the strip is %.2f pt, with a margin of %.2f pt; at the banner's %v pt it would be %.2f pt", stamp, got, stampSize, bannerW*mm, fitMargin, bannerSize, width(stamp, bannerSize))
	if got > bannerW*mm-fitMargin {
		t.Errorf("the widest legal stamp is %.2f pt, the strip %.2f pt less a margin of %.2f pt", got, bannerW*mm, fitMargin)
	}
	if width(stamp, bannerSize) <= bannerW*mm {
		t.Errorf("the widest legal stamp fits the strip at the banner's size: the stamp's own size no longer matters")
	}
	out, pages, err := w.Issue(t.Context(), singleBox(), map[string]string{"n1": "x"}, nil, nil, stamp, latest)
	if err != nil || pages != 1 || !bytes.Contains(out, infoCreation(latest)) {
		t.Errorf("the widest legal stamp does not render: %v", err)
	}
}

// issueExpectation is the record the checker verifies an issued document against: the stamp
// recomputed from the product's number (a string), the time and the id, the digest and page
// count of the PDF, and the preview of the same document, which must not pass as the issue.
type issueExpectation struct {
	Number      string `json:"number"`
	IssueID     string `json:"issue_id"`
	IssuedAt    string `json:"issued_at"`
	Stamp       string `json:"stamp"`
	SHA256      string `json:"sha256"`
	Pages       int    `json:"pages"`
	PreviewFile string `json:"preview_file"`
}

func issueEvidence(t *testing.T, name string, pdf, preview []byte, number string, at time.Time, id string, pages int) {
	t.Helper()
	dir := os.Getenv("REPORTING_PDF_EVIDENCE")
	if dir == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var e map[string]any
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pdf)
	e["issue"] = issueExpectation{Number: number, IssueID: id, IssuedAt: at.UTC().Format("2006-01-02T15:04:05Z"), Stamp: issueStamp(number, at, id), SHA256: hex.EncodeToString(sum[:]), Pages: pages, PreviewFile: name + ".preview.pdf"}
	b, _ := json.MarshalIndent(e, "", " ")
	for file, content := range map[string][]byte{name + ".json": b, name + ".preview.pdf": preview} {
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
