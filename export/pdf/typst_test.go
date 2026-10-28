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
	Kind       string            `json:"kind"`
	File       string            `json:"file"`
	Background bool              `json:"background"`
	OffsetX    float64           `json:"offset_x"`
	OffsetY    float64           `json:"offset_y"`
	Elements   []documentElement `json:"elements"`
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

func documentEvidence(t *testing.T, name string, pdf []byte, d doctemplates.Document, values map[string]string, background bool) {
	t.Helper()
	dir := os.Getenv("REPORTING_PDF_EVIDENCE")
	if dir == "" {
		return
	}
	e := documentExpectation{Kind: "document", File: name + ".pdf", Background: background, OffsetX: d.Print.OffsetX, OffsetY: d.Print.OffsetY, Elements: []documentElement{}}
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
		}
		hardDoc.Elements = append(hardDoc.Elements, e)
		hardValues[id] = v
	}
	for _, c := range []struct {
		name   string
		d      doctemplates.Document
		values map[string]string
		png    []byte
	}{
		{"document_full", doc("full", 0, 0), values, png},
		{"document_calibrated", doc("full", 1.2, -0.5), values, png},
		{"document_variables", doc("variables", -2.3, 4), values, nil},
		{"document_hard", hardDoc, hardValues, nil},
	} {
		out, err := w.Render(t.Context(), c.d, c.values, c.png)
		if err != nil {
			var re *RenderError
			if errors.As(err, &re) {
				t.Fatalf("%s: %v\n%s", c.name, err, re.output())
			}
			t.Fatalf("%s: %v", c.name, err)
		}
		again, err := w.Render(t.Context(), c.d, c.values, c.png)
		if err != nil || !bytes.Equal(out, again) {
			t.Fatalf("%s: two renders with the same clock differ (%v)", c.name, err)
		}
		documentEvidence(t, c.name, out, c.d, c.values, c.png != nil)
	}
	// Refused before Typst runs: a value too wide for its box, one the fonts lack, a Hangul
	// tone mark (Typst would add a dotted circle) and a tab.
	for value, want := range map[string]error{strings.Repeat("宽", 40): r.ErrUnsupported, "😀": r.ErrUnsupported, "a\u302eb": r.ErrUnsupported, "a\tb": r.ErrInvalid} {
		v := map[string]string{"n1": "x", "n2": value, "n3": "1", "n4": "1"}
		if _, err := w.Render(t.Context(), doc("variables", 0, 0), v, nil); !errors.Is(err, want) {
			t.Errorf("%q: %v; want %v", value, err, want)
		}
	}
	// The upload check renders on the background: a PNG the templates accept passes.
	if err := w.BackgroundFits(t.Context(), png); err != nil {
		t.Errorf("background fit: %v", err)
	}
	// Renders share the Writer's slots: six at once all succeed, byte for byte alike.
	results := make([][]byte, 6)
	errs := make([]error, 6)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = w.Render(t.Context(), hardDoc, hardValues, nil)
		}()
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || !bytes.Equal(results[i], results[0]) {
			t.Errorf("concurrent render %d: %v", i, errs[i])
		}
	}
}
