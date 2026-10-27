package pdf

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
)

// fakeMetrics covers every character except the blocked ones: ideographs, kana and full-width
// forms are 1 em, others half an em, bold Latin 0.6 em.
func fakeMetrics(blocked ...rune) *measurer {
	return &measurer{families: []string{"Fake Sans"}, ems: map[glyphKey]float64{}, advance: func(ch rune, bold bool) (float64, bool, error) {
		switch {
		case slices.Contains(blocked, ch):
			return 0, false, nil
		case ideograph(ch) || (ch >= 0x3000 && ch <= 0x30FF) || (ch >= 0xFF00 && ch <= 0xFFEF):
			return 1, true, nil
		case bold:
			return 0.6, true, nil
		}
		return 0.5, true, nil
	}}
}

// table returns a snapshot with string dimensions and decimal metrics in CNY.
func table(dims, metrics int, rows ...[]string) r.Snapshot {
	s := r.Snapshot{
		ID:       "0123456789abcdef0123456789abcdef",
		Query:    r.QuerySpec{Dataset: r.Ref{ID: "finance.ledger", Version: "2026.10"}, Companies: []string{"c-east", "c-west"}, AsOf: "2026-10-27", Timezone: "Asia/Shanghai"},
		Prepared: r.PreparedRead{Watermark: "wm-42"},
		Result:   r.Result{Totals: map[string]string{}, Rows: rows, Evidence: r.Evidence{DataClass: "contract-simulator"}},
	}
	for i := range dims {
		id := "d" + strconv.Itoa(i)
		s.Query.Dimensions = append(s.Query.Dimensions, id)
		s.Result.Columns = append(s.Result.Columns, r.Column{ID: id, Label: "维度" + strconv.Itoa(i), Type: "string"})
	}
	for i := range metrics {
		id := "m" + strconv.Itoa(i)
		s.Query.Metrics = append(s.Query.Metrics, r.Ref{ID: id, Version: "1"})
		s.Result.Columns = append(s.Result.Columns, r.Column{ID: id, Label: "金额" + strconv.Itoa(i), Type: "decimal", Scale: 2, Currency: "CNY"})
		s.Result.Totals[id] = "1234567.89"
	}
	return s
}

var created = time.Date(2026, 10, 27, 8, 30, 0, 0, time.UTC)

func TestGroupDigitsChangesNoDigit(t *testing.T) {
	for in, want := range map[string]string{
		"0": "0", "12": "12", "123": "123", "1234": "1,234", "-1234567.89": "-1,234,567.89",
		"1000000": "1,000,000", "-0.50": "-0.50", "999999.000000000000000001": "999,999.000000000000000001",
	} {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%q) = %q, want %q", in, got, want)
		}
		if strings.ReplaceAll(groupDigits(in), ",", "") != in {
			t.Errorf("groupDigits(%q) changed a digit", in)
		}
	}
}

func TestPrintableKeepsEveryCharacterButLineBreaksAndTabs(t *testing.T) {
	if got := printable("a\r\nb\rc\td  #[*$]<x>"); got != "a\nb\nc d  #[*$]<x>" {
		t.Fatalf("printable = %q", got)
	}
}

func TestBuildLaysOutTheSnapshotAsGiven(t *testing.T) {
	s := table(2, 2, []string{"华东", "2026", "1234567.89", "-0.50"}, []string{"华西", "2025", "0.00", "1000.00"})
	s.Result.Columns[1].Type = "integer"
	s.Result.Columns[3].Currency, s.Result.Columns[3].Unit = "", "笔"
	d, err := build(s, fakeMetrics(), created)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]any{{"华东", "2026", "1,234,567.89", "-0.50"}, {"华西", "2025", "0.00", "1,000.00"}}
	if !jsonEqual(d.Rows, want) {
		t.Errorf("rows = %v, want %v (dimensions verbatim, metrics grouped)", d.Rows, want)
	}
	if !jsonEqual(d.Header, []any{"维度0", "维度1", "金额0（CNY）", "金额1（笔）"}) {
		t.Errorf("header = %v", d.Header)
	}
	if !jsonEqual(d.TotalRow, []any{"合计", "", "1,234,567.89", "1,234,567.89"}) || d.TotalList != nil {
		t.Errorf("totals = %v / %v", d.TotalRow, d.TotalList)
	}
	if !slices.Equal(d.Align, []string{"left", "right", "right", "right"}) || d.Landscape || d.Empty || !slices.Equal(d.Fonts, []string{"Fake Sans"}) {
		t.Errorf("align %v landscape %v empty %v fonts %v", d.Align, d.Landscape, d.Empty, d.Fonts)
	}
	footer, meta := d.Footer.(string), d.Meta.(string)
	for _, part := range []string{"合同模拟器 / 合成数据（非真实业务数据）", "快照 " + s.ID, "finance.ledger@2026.10", "截至 2026-10-27", "来源水位 wm-42"} {
		if !strings.Contains(footer, part) {
			t.Errorf("footer %q lacks %q", footer, part)
		}
	}
	for _, part := range []string{"finance.ledger@2026.10", "（Asia/Shanghai）", "公司 c-east、c-west", "2 行", "生成于 2026-10-27 08:30:00 UTC"} {
		if !strings.Contains(meta, part) {
			t.Errorf("meta %q lacks %q", meta, part)
		}
	}
	if d.Heading != "finance.ledger" || d.Title != "finance.ledger" {
		t.Errorf("heading %v title %q", d.Heading, d.Title)
	}
}

func TestBuildWithoutDimensionsListsTotals(t *testing.T) {
	d, err := build(table(0, 2, []string{"1.00", "2.00"}), fakeMetrics(), created)
	if err != nil {
		t.Fatal(err)
	}
	if d.TotalRow != nil || !jsonEqual(d.TotalList, []any{"金额0（CNY）：1,234,567.89", "金额1（CNY）：1,234,567.89"}) {
		t.Fatalf("totals = %v / %v", d.TotalRow, d.TotalList)
	}
}

func TestBuildMarksAnEmptySnapshot(t *testing.T) {
	d, err := build(table(1, 1), fakeMetrics(), created)
	if err != nil || !d.Empty || len(d.Rows) != 0 || len(d.Header) != 2 {
		t.Fatal(d, err)
	}
	if b, _ := json.Marshal(d); !strings.Contains(string(b), `"rows":[]`) {
		t.Errorf("rows are not an empty list: %s", b)
	}
}

func TestBuildRefusesCharactersTheFontsDoNotCover(t *testing.T) {
	const missing = '☃'
	m := string(missing)
	for name, change := range map[string]func(*r.Snapshot){
		"value":     func(s *r.Snapshot) { s.Result.Rows[0][0] = "a" + m },
		"label":     func(s *r.Snapshot) { s.Result.Columns[0].Label = m },
		"currency":  func(s *r.Snapshot) { s.Result.Columns[1].Currency = m },
		"unit":      func(s *r.Snapshot) { s.Result.Columns[1].Unit = m },
		"company":   func(s *r.Snapshot) { s.Query.Companies = []string{m} },
		"watermark": func(s *r.Snapshot) { s.Prepared.Watermark = m },
		"dataset":   func(s *r.Snapshot) { s.Query.Dataset.ID = m },
		"class":     func(s *r.Snapshot) { s.Result.Evidence.DataClass = m },
		"timezone":  func(s *r.Snapshot) { s.Query.Timezone = m },
	} {
		s := table(1, 1, []string{"a", "1.00"})
		change(&s)
		if _, err := build(s, fakeMetrics(missing), created); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("%s: %v; want unsupported", name, err)
		}
	}
	// The labels of the template are checked like the data.
	if _, err := build(table(1, 1, []string{"a", "1.00"}), fakeMetrics('页'), created); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("an uncovered label character: %v; want unsupported", err)
	}
	// Tabs and carriage returns are not printed, so they need no glyph.
	if _, err := build(table(1, 1, []string{"a\tb\r\nc\rd", "1.00"}), fakeMetrics('\t', '\r'), created); err != nil {
		t.Errorf("tabs and carriage returns: %v", err)
	}
}

func TestBuildRefusesMalformedOrTooWideSnapshots(t *testing.T) {
	short := table(1, 1, []string{"a"})
	noTotal := table(1, 1, []string{"a", "1.00"})
	delete(noTotal.Result.Totals, "m0")
	extraColumn := table(1, 1, []string{"a", "1.00"})
	extraColumn.Result.Columns = append(extraColumn.Result.Columns, r.Column{ID: "x", Label: "x", Type: "string"})
	badNumber := table(1, 1, []string{"a", "1234x"})
	badTotal := table(1, 1, []string{"a", "1.00"})
	badTotal.Result.Totals["m0"] = "1,000.00"
	for name, s := range map[string]r.Snapshot{"short row": short, "missing total": noTotal, "extra column": extraColumn, "no columns": {}, "a malformed number": badNumber, "a malformed total": badTotal} {
		if _, err := build(s, fakeMetrics(), created); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%s: %v; want integrity failure", name, err)
		}
	}
	if _, err := build(table(MaxColumns+1, 0), fakeMetrics(), created); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("%d columns: %v; want unsupported", MaxColumns+1, err)
	}
	if _, err := build(table(MaxColumns, 0), fakeMetrics(), created); err != nil {
		t.Errorf("%d columns: %v", MaxColumns, err)
	}
	if _, err := (&Writer{}).Write(t.Context(), table(1, 0, make([][]string, r.MaxRows+1)...)); !errors.Is(err, r.ErrBudget) {
		t.Errorf("%d rows: %v; want budget", r.MaxRows+1, err)
	}
}

func TestMaxColumnsIsWhatFitsALandscapePage(t *testing.T) {
	avail := a4Long - 2*marginX
	if MaxColumns*minColumn > avail || (MaxColumns+1)*minColumn <= avail {
		t.Fatalf("MaxColumns = %d, but %.2f pt fit %.2f columns", MaxColumns, avail, avail/minColumn)
	}
}

func TestFitKeepsColumnsWithinThePage(t *testing.T) {
	const avail = 500.0
	if w, err := fit([]float64{100, 10, 200}, []float64{50, 10, 30}, avail); err != nil || !slices.Equal(w, []float64{100, minColumn, 200}) {
		t.Errorf("natural widths that fit: %v %v", w, err)
	}
	// Narrow columns keep their natural width; the wide one takes the rest.
	if w, err := fit([]float64{30, 2000, 120}, []float64{25, 104, 120}, avail); err != nil || w[0] != 30 || w[2] != 120 || w[1] < 349.9 || sum(w) > avail {
		t.Errorf("one wide column: %v %v", w, err)
	}
	if _, err := fit(make([]float64, 21), make([]float64, 21), avail); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("21 columns of at least %.0f pt in %.0f pt: %v", minColumn, avail, err)
	}
	rnd := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		n := 1 + rnd.IntN(20)
		natural, least := make([]float64, n), make([]float64, n)
		for j := range n {
			natural[j] = rnd.Float64() * 400
			least[j] = natural[j] * rnd.Float64()
		}
		w, err := fit(natural, least, avail)
		if err != nil {
			t.Fatal(err)
		}
		fits := sum(clampAll(natural, minColumn)) <= avail
		for j := range n {
			if w[j] < minColumn-0.01 || (fits && w[j] < math.Max(natural[j], minColumn)-0.01) {
				t.Fatalf("column %d of %v / %v: %v", j, natural, least, w)
			}
		}
		if sum(w) > avail+1e-9 {
			t.Fatalf("widths %v exceed %v", w, avail)
		}
		// When columns are shrunk, none is wider than a shrunk column unless its least width
		// demands it.
		base := clampAll(least, minColumn)
		if sum(base) <= avail {
			for j := range n {
				if w[j] < math.Max(natural[j], minColumn)-0.01 {
					for k := range n {
						if w[k] > math.Max(w[j], base[k])+0.02 {
							t.Fatalf("column %d (%.2f) is wider than shrunk column %d (%.2f): %v / %v", k, w[k], j, w[j], natural, least)
						}
					}
				}
			}
		}
	}
}

func TestSegmentCutsOnlyRunsWiderThanTheirBox(t *testing.T) {
	m := fakeMetrics()
	if got, err := m.segment("short words 中文。", bodySize, false, 100); err != nil || got != "short words 中文。" {
		t.Errorf("text that fits: %v %v", got, err)
	}
	run := strings.Repeat("A", 60) + " " // the space before an ideograph ends the run
	got, err := m.segment("前文 "+run+"后文\n下一行", bodySize, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := got.([]any)
	if !ok || len(parts) != 3 || parts[0] != "前文 " || parts[2] != "后文\n下一行" {
		t.Fatalf("segment = %#v", got)
	}
	cut := parts[1].(pieces).Pieces
	if strings.Join(cut, "") != run || len(cut) < 2 {
		t.Fatalf("pieces %v do not rebuild the run", cut)
	}
	for _, p := range cut {
		if w, _ := m.width(p, bodySize, false); w*slack > 50 {
			t.Errorf("piece %q is %.1f pt wide, over 50 pt", p, w*slack)
		}
	}
	// A combining mark stays with the character before it.
	marked := strings.Repeat("é", 40)
	got, _ = m.segment(marked, bodySize, false, 30)
	for _, p := range got.([]any)[0].(pieces).Pieces {
		if strings.HasPrefix(p, "́") {
			t.Fatalf("a piece starts with a combining mark: %q", p)
		}
	}
}

func TestRunsBreakOnlyWhereALineMayBreak(t *testing.T) {
	for in, want := range map[string][]string{
		"中文。": {"中", "文。"},
		"应收账款，含税（13%）及运费": {"应", "收", "账", "款，含", "税（13%）及", "运", "费"},
		"a b  c":   {"a ", "b  ", "c"},
		"（ a":      {"（ a"},       // not after opening punctuation
		"a ）b":     {"a ）b"},      // not before closing punctuation
		"紧急！！！":    {"紧", "急！！！"}, // a line may not start with "！"
		"x 々 ー":    {"x 々 ー"},
		"  a":      {"  a"},
		"“ a” b":   {"“ a” ", "b"},
		"¡¡¡    x": {"¡¡¡    x"}, // ¡ and ¿ open like brackets
		"¿ a b":    {"¿ a ", "b"},
	} {
		var got []string
		if err := runs(in, func(run string) error { got = append(got, run); return nil }); err != nil || !slices.Equal(got, want) {
			t.Errorf("runs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSegmentCutsRunsOfPunctuation(t *testing.T) {
	m := fakeMetrics()
	for _, in := range []string{"紧急" + strings.Repeat("！", 60), strings.Repeat("。", 40), strings.Repeat("（", 30) + "a"} {
		got, err := m.segment(in, bodySize, false, 40)
		if err != nil {
			t.Fatal(err)
		}
		if flatten(got) != in {
			t.Fatalf("segment(%q) printed %q", in, flatten(got))
		}
		parts, ok := got.([]any)
		if !ok {
			t.Fatalf("a %d-character run of punctuation was not cut: %q", len([]rune(in)), got)
		}
		for _, p := range parts {
			text := ""
			if s, ok := p.(string); ok {
				text = s
			} else {
				for _, piece := range p.(pieces).Pieces {
					if w, _ := m.width(piece, bodySize, false); w*slack > 40 {
						t.Errorf("piece %q is %.1f pt wide", piece, w*slack)
					}
				}
				continue
			}
			if w, _ := m.width(text, bodySize, false); w*slack > 40 && len([]rune(text)) > 1 {
				t.Errorf("uncut text %q is %.1f pt wide", text, w*slack)
			}
		}
	}
}

func TestWidthCountsTheSpaceBetweenChineseAndLatin(t *testing.T) {
	m := fakeMetrics()
	plain, _ := m.width("维度", bodySize, false)
	mixed, _ := m.width("维度5x", bodySize, false)
	if want := plain + (0.25+0.5+0.5)*bodySize; mixed != want {
		t.Fatalf("width of 维度5x = %v, want %v", mixed, want)
	}
}

func TestWidthCountsAGapBesideEveryChineseCharacter(t *testing.T) {
	m := fakeMetrics()
	for in, ems := range map[string]float64{"文#文": 1 + 0.25 + 0.5 + 0.25 + 1, "ーa": 1 + 0.25 + 0.5, "a b": 1.5, "文文": 2} {
		if w, _ := m.width(in, 1, false); w != ems {
			t.Errorf("width of %q = %v em, want %v", in, w, ems)
		}
	}
}

func TestACharacterWiderThanItsCellIsRefused(t *testing.T) {
	if _, err := fakeMetrics().segment("中中", bodySize, false, 5); !errors.Is(err, r.ErrUnsupported) {
		t.Fatalf("ideographs in a 5 pt cell: %v; want unsupported", err)
	}
}

func TestSoftHyphensAreRefused(t *testing.T) {
	// Typst prints a soft hyphen as "-" where it breaks the line, which the data does not hold.
	if _, err := build(table(1, 1, []string{"bbbb\u00adcccc", "1.00"}), fakeMetrics(), created); !errors.Is(err, r.ErrUnsupported) {
		t.Fatalf("a soft hyphen: %v; want unsupported", err)
	}
}

func TestTypstFontListingIsParsed(t *testing.T) {
	out := "Noto Sans SC\n  ├ /f/NotoSansSC-Bold.otf\n  │   Style: Normal, Weight: 700, Stretch: 100%\n  └ /f/My Fonts/R.otf\n      Style: Normal, Weight: 400, Stretch: 100%\nGo Mono\n  └ /f/m.ttf\n      Style: Italic, Weight: 400, Stretch: 100%\n\n"
	got, err := typstFaces(out)
	want := []listedFace{{"Noto Sans SC", "/f/NotoSansSC-Bold.otf", "Normal", 700}, {"Noto Sans SC", "/f/My Fonts/R.otf", "Normal", 400}, {"Go Mono", "/f/m.ttf", "Italic", 400}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("typstFaces = %v, %v", got, err)
	}
	if _, err := typstFaces("Family\n  ? something else\n"); err == nil {
		t.Error("an unexpected listing line is accepted")
	}
}

func TestSegmentLosesNoCharacter(t *testing.T) {
	m := fakeMetrics()
	alphabet := []rune("aZ09-_/.:,;!?@#$%^&*()[]{}<>\"'\\| \n中文，。（）「」ﬁé́")
	rnd := rand.New(rand.NewPCG(3, 4))
	for range 3000 {
		b := make([]rune, rnd.IntN(120))
		for i := range b {
			b[i] = alphabet[rnd.IntN(len(alphabet))]
		}
		in := string(b)
		got, err := m.segment(in, bodySize, rnd.IntN(2) == 0, 16+rnd.Float64()*200)
		if err != nil {
			t.Fatal(err)
		}
		if out := flatten(got); out != in {
			t.Fatalf("segment(%q) printed %q", in, out)
		}
	}
}

func TestRenderErrorHidesTypstOutput(t *testing.T) {
	e := &RenderError{Status: "exit status 1", stderr: []byte("error: row value secret-123")}
	if strings.Contains(e.Error(), "secret") || e.output() == "" {
		t.Fatalf("Error() = %q", e.Error())
	}
	var tl head
	for range 100 {
		tl.Write(make([]byte, 1000))
	}
	if len(tl.b) != maxOutput {
		t.Fatalf("kept %d bytes of stderr", len(tl.b))
	}
}

func flatten(c any) string {
	if s, ok := c.(string); ok {
		return s
	}
	var b strings.Builder
	for _, p := range c.([]any) {
		if s, ok := p.(string); ok {
			b.WriteString(s)
		} else {
			b.WriteString(strings.Join(p.(pieces).Pieces, ""))
		}
	}
	return b.String()
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
