package pdf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// form returns a schema 1.1 document with the given elements.
func form(mode string, ox, oy float64, elements ...doctemplates.Element) doctemplates.Document {
	return doctemplates.Document{Schema: "1.1", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Elements: elements, Print: &doctemplates.PrintProfile{Mode: mode, OffsetX: ox, OffsetY: oy}}
}

func box(id, source, text string, x, y, w float64, size int, align string) doctemplates.Element {
	field := ""
	if source != "literal" {
		field, text = "f", ""
	}
	return doctemplates.Element{ID: id, Source: source, Field: field, Text: text, X: x, Y: y, Width: w, Height: 10, FontSize: size, Align: align}
}

// plainFake is fakeMetrics for documents: advances alone, one em for CJK and half an em for
// the rest.
func plainFake(blocked ...rune) *measurer {
	m := fakeMetrics(blocked...)
	m.plain = true
	return m
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLayOutPlacesValuesByTheDocumentRule(t *testing.T) {
	d := form("full", 1.2, -0.5, box("n1", "literal", "标题", 20, 30, 80, 12, "left"), box("n2", "row", "", 20, 50, 60, 10, "right"), box("n3", "row", "", 20, 70, 60, 10, "center"))
	p, err := layOut(d, map[string]string{"n1": "标题", "n2": "1234.50", "n3": "ab"}, nil, nil, plainFake())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Pages[0].Texts) != 2+7+2 || p.Background {
		t.Fatalf("%+v", p)
	}
	// 1 mm is 72/25.4 pt; the baseline is 1.16 font sizes below the top of the box; each
	// cluster follows the one before it by its advance; a right-aligned value ends at the
	// right edge and a centred one is centred.
	title := p.Pages[0].Texts[:2]
	if !near(title[0].X, 21.2*mm) || !near(title[1].X, 21.2*mm+12) || !near(title[0].Baseline, 29.5*mm+1.16*12) || !near(title[0].Width, 12) || title[0].Text != "标" || title[1].Text != "题" || title[0].Size != 12 {
		t.Errorf("title %+v", title)
	}
	amount := p.Pages[0].Texts[2:9]
	if !near(amount[0].X, 81.2*mm-7*5) || !near(amount[6].X, 81.2*mm-5) || !near(amount[0].Baseline, 49.5*mm+11.6) || amount[0].Text != "1" || amount[6].Text != "0" {
		t.Errorf("amount %+v", amount)
	}
	centred := p.Pages[0].Texts[9:]
	if !near(centred[0].X, 51.2*mm-5) || !near(centred[1].X, 51.2*mm) {
		t.Errorf("centred %+v", centred)
	}
	if b := p.Banner; b.Text != "PREVIEW - NOT AN ISSUED DOCUMENT" || !near(b.Left, 10*mm) || !near(b.Baseline, 10*mm+11.6) {
		t.Errorf("banner %+v", b)
	}
	// The variable layer leaves out literal text.
	if p, err = layOut(form("variables", 0, 0, d.Elements...), map[string]string{"n1": "标题", "n2": "1", "n3": "2"}, nil, nil, plainFake()); err != nil || len(p.Pages[0].Texts) != 2 || p.Pages[0].Texts[0].Text != "1" {
		t.Errorf("variables mode: %+v %v", p, err)
	}
}

func TestLayOutKeepsClustersTogetherAndDropsTrailingSpace(t *testing.T) {
	d := form("full", 0, 0, box("n1", "row", "", 20, 30, 80, 10, "right"))
	// A combining mark stays with its base; trailing spaces of any kind are not drawn, leading
	// ones are; a value of spaces only draws nothing.
	for value, want := range map[string][]string{
		"éx":   {"é", "x"},
		"ab 　 ": {"a", "b"},
		"  ab":  {" ", " ", "a", "b"},
		"   ":   {},
	} {
		p, err := layOut(d, map[string]string{"n1": value}, nil, nil, plainFake())
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		got := []string{}
		for _, g := range p.Pages[0].Texts {
			got = append(got, g.Text)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q: clusters %q, want %q", value, got, want)
		}
		if value == "éx" && (!near(p.Pages[0].Texts[0].Width, 10) || !near(p.Pages[0].Texts[1].X, p.Pages[0].Texts[0].X+10) || !near(p.Pages[0].Texts[1].X+5, 100*mm)) {
			t.Errorf("%q: %+v", value, p.Pages[0].Texts)
		}
	}
}

func TestLayOutRefusesWhatItCannotPrintExactly(t *testing.T) {
	d := form("full", 0, 0, box("n1", "row", "", 20, 30, 20, 10, "left"))
	for name, value := range map[string]string{
		"a value wider than its box": strings.Repeat("宽", 20),
		"an uncovered character":     "☃",
		"a soft hyphen":              "a­b",
		"a line break":               "a\nb",
		"a tab":                      "a\tb",
		"a control character":        "a\x7fb",
		"too long":                   strings.Repeat("a", 257),
		"a leading combining mark":   "\u0301abc",
		"combining marks only":       "\u0301\u0301",
	} {
		if _, err := layOut(d, map[string]string{"n1": value}, nil, nil, plainFake('☃')); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("%s: %v; want unsupported", name, err)
		}
	}
	for name, values := range map[string]map[string]string{"a missing value": {}, "invalid UTF-8": {"n1": "a\xffb"}} {
		if _, err := layOut(d, values, nil, nil, plainFake()); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%s: %v; want invalid", name, err)
		}
	}
	// The box is 20 mm = 56.69 pt, and a value must leave 2 pt free: with the fake font's 5 pt
	// characters at 10 pt, ten (50 pt) fit and eleven (55 pt) do not; at 8 pt thirteen
	// (52 pt) fit and fourteen (56 pt) do not. Trailing spaces do not count: they are not drawn.
	for size, longest := range map[int]int{10: 10, 8: 13} {
		d.Elements[0].FontSize = size
		fits := strings.Repeat("a", longest)
		if _, err := layOut(d, map[string]string{"n1": fits + "  "}, nil, nil, plainFake()); err != nil {
			t.Errorf("%d characters at %d pt: %v", longest, size, err)
		}
		if _, err := layOut(d, map[string]string{"n1": fits + "a"}, nil, nil, plainFake()); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("%d characters at %d pt: %v", longest+1, size, err)
		}
	}
}

func TestLayOutDrawsOnlyTheNamedBackgroundInFullMode(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	sum := sha256.Sum256(png)
	ref := &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: hex.EncodeToString(sum[:])}
	full := form("full", 0, 0, box("n1", "row", "", 20, 30, 60, 10, "left"))
	full.Schema, full.Background = "1.2", ref
	variables := full
	variables.Print = &doctemplates.PrintProfile{Mode: "variables"}
	values := map[string]string{"n1": "1"}
	if p, err := layOut(full, values, png, nil, plainFake()); err != nil || !p.Background {
		t.Errorf("full mode with its background: %+v %v", p, err)
	}
	other := append([]byte{}, png...)
	other[20] = 1
	for name, c := range map[string]struct {
		d    doctemplates.Document
		png  []byte
		want error
	}{
		"full mode without the background": {full, nil, r.ErrInvalid},
		"another background":               {full, other, r.ErrIntegrity},
		"a background in variables mode":   {variables, png, r.ErrInvalid},
		"a background the template lacks":  {form("full", 0, 0, full.Elements...), png, r.ErrInvalid},
		"a background over the size limit": {full, make([]byte, doctemplates.MaxBackgroundBytes+1), r.ErrBudget},
	} {
		if _, err := layOut(c.d, values, c.png, nil, plainFake()); !errors.Is(err, c.want) {
			t.Errorf("%s: %v; want %v", name, err, c.want)
		}
	}
	notPNG := []byte("GIF89a not a png")
	sum = sha256.Sum256(notPNG)
	full.Background = &doctemplates.BackgroundRef{ID: ref.ID, SHA256: hex.EncodeToString(sum[:])}
	if _, err := layOut(full, values, notPNG, nil, plainFake()); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a background that is not a PNG: %v", err)
	}
	if p, err := layOut(variables, values, nil, nil, plainFake()); err != nil || p.Background {
		t.Errorf("variables mode: %+v %v", p, err)
	}
}

func TestAcquireLimitsConcurrentRenders(t *testing.T) {
	w := &Writer{cfg: Config{Timeout: 50 * time.Millisecond}, slots: make(chan struct{}, 1)}
	take := func(ctx context.Context) error {
		bounded, cancel, budget := w.bounded(ctx)
		defer cancel()
		return budget(acquire(bounded, w.slots))
	}
	if err := take(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The slot is taken: a second render waits for the timeout and is a budget error; one
	// whose context ends first gets that error. After a release the slot is free again.
	if err := take(context.Background()); !errors.Is(err, r.ErrBudget) {
		t.Errorf("second render: %v; want budget", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := take(ctx); !errors.Is(err, context.Canceled) || errors.Is(err, r.ErrBudget) {
		t.Errorf("cancelled render: %v", err)
	}
	release(w.slots)
	if err := take(context.Background()); err != nil {
		t.Errorf("after release: %v", err)
	}
	// The render itself counts against the same deadline: a context that bounded already ended
	// is a budget error too.
	bounded, cancel, budget := w.bounded(context.Background())
	defer cancel()
	<-bounded.Done()
	if err := budget(bounded.Err()); !errors.Is(err, r.ErrBudget) {
		t.Errorf("after the deadline: %v", err)
	}
	// Previews hold all but one slot, at least one: with two slots, one; with one, one.
	for concurrency, previews := range map[int]int{1: 1, 2: 1, 4: 3} {
		if got := max(1, concurrency-1); got != previews {
			t.Errorf("concurrency %d: %d preview slots", concurrency, got)
		}
	}
}

func TestWiderThanMeasuredIsUnsupported(t *testing.T) {
	assertion := &RenderError{Status: "exit status 1", stderr: []byte("main.typ:24:5: error: assertion failed: " + widerThanMeasured)}
	if err := widerThanMeasuredError(assertion); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("the template's assertion: %v", err)
	}
	other := &RenderError{Status: "exit status 1", stderr: []byte("main.typ:1:1: error: unknown variable")}
	if err := widerThanMeasuredError(other); err != other {
		t.Errorf("another failure: %v", err)
	}
	if err := widerThanMeasuredError(r.ErrBudget); err != r.ErrBudget {
		t.Errorf("a budget error: %v", err)
	}
	// The probe documents and the probe background are what New renders at startup.
	d, values, background := documentProbe()
	if doctemplates.Validate(d) != nil || len(values) != 3 || len(background) == 0 {
		t.Errorf("probe %+v %v", d, values)
	}
}

// TestIssueLayoutIsThePreviewLayoutPlusTheBannerTextRefusal: issueLayout is what an issued document
// and its check (Writer.CheckIssue, which runs it with the fonts' plain measurer) have in common.
// It differs from the preview's layOut by one refusal alone: the preview's banner text is never
// printed on an issued document, not even as data, as a value or as a table cell (in variables mode
// the literals are not printed, so they are not data). Everything layOut refuses stands, with the
// same errors, and what passes is the preview's pages: the stamp is the caller's.
func TestIssueLayoutIsThePreviewLayoutPlusTheBannerTextRefusal(t *testing.T) {
	columns := []doctemplates.TableColumn{{Field: "customer", Width: 60}, {Field: "amount", Width: 60}}
	rows := func(cell string) *doctemplates.TableData {
		data := amounts(3)
		if cell != "" {
			data.Rows[1][0] = cell
		}
		return data
	}
	row := box("n1", "row", "", 20, 30, 80, 10, "left")
	literal := box("n1", "literal", bannerText, 20, 30, 100, 10, "left")
	second := box("n2", "row", "", 20, 50, 80, 10, "left")
	for name, c := range map[string]struct {
		d      doctemplates.Document
		values map[string]string
		table  *doctemplates.TableData
		banner bool  // issueLayout refuses it for the banner text, layOut does not
		want   error // what both refuse otherwise
	}{
		"a value that fits":                       {form("full", 0, 0, row), map[string]string{"n1": "x"}, nil, false, nil},
		"the banner text as a value":              {form("full", 0, 0, row), map[string]string{"n1": "x " + bannerText}, nil, true, nil},
		"the banner text as a literal in full":    {form("full", 0, 0, literal), map[string]string{"n1": bannerText}, nil, true, nil},
		"the banner text as a literal, variables": {form("variables", 0, 0, literal, second), map[string]string{"n1": bannerText, "n2": "x"}, nil, false, nil},
		"the banner text as a value, variables":   {form("variables", 0, 0, row), map[string]string{"n1": bannerText}, nil, true, nil},
		"a table that fits":                       {tabled("full", 20, 40, 120, 60, 10, columns...), map[string]string{"n1": "x"}, rows(""), false, nil},
		"the banner text as a table cell":         {tabled("full", 20, 40, 120, 60, 10, columns...), map[string]string{"n1": "x"}, rows(bannerText), true, nil},
		"a value wider than its box":              {form("full", 0, 0, row), map[string]string{"n1": strings.Repeat("宽", 30)}, nil, false, r.ErrUnsupported},
		"an uncovered character":                  {form("full", 0, 0, row), map[string]string{"n1": "☃"}, nil, false, r.ErrUnsupported},
		"a missing value":                         {form("full", 0, 0, row), map[string]string{}, nil, false, r.ErrInvalid},
		"a table total that is not the sum":       {tabled("full", 20, 40, 120, 60, 10, columns...), map[string]string{"n1": "x"}, func() *doctemplates.TableData { d := rows(""); d.Totals["amount"] = "1.00"; return d }(), false, r.ErrIntegrity},
		"a table without rows":                    {tabled("full", 20, 40, 120, 60, 10, columns...), map[string]string{"n1": "x"}, nil, false, r.ErrInvalid},
	} {
		page, issueErr := issueLayout(c.d, c.values, nil, c.table, plainFake('☃'))
		_, layErr := layOut(c.d, c.values, nil, c.table, plainFake('☃'))
		switch {
		case c.banner:
			if !errors.Is(issueErr, r.ErrUnsupported) || !strings.Contains(issueErr.Error(), "banner text") || layErr != nil {
				t.Errorf("%s: issue layout %v, preview layout %v; want the banner text refused for the issue alone", name, issueErr, layErr)
			}
		case c.want != nil:
			if !errors.Is(issueErr, c.want) || !errors.Is(layErr, c.want) {
				t.Errorf("%s: issue layout %v, preview layout %v; want both %v", name, issueErr, layErr, c.want)
			}
		default:
			if issueErr != nil || layErr != nil || page.Banner.Text != bannerText || page.Banner.Fill != bannerFill || page.Title != "Document preview" {
				t.Errorf("%s: %v %v %+v", name, issueErr, layErr, page)
			}
		}
	}
}
