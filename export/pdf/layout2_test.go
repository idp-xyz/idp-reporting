package pdf

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// Schema 2.0 layout tests (S03-J). They run without Typst: the fake font of the layout tests (an
// ideograph is one em, any other character half an em, bold Latin 0.6 em) makes every width exact.

func textItem(id, source, v string, x, y, w, h float64, size int, align string) doctemplates.Item {
	it := doctemplates.Item{ID: id, Type: doctemplates.TypeText, Source: source, X: x, Y: y, Width: w, Height: h, FontSize: size, Align: align}
	if source == doctemplates.SourceLiteral || source == doctemplates.SourcePage {
		it.Text = v
	} else {
		it.Field = v
	}
	return it
}

func wrapped(it doctemplates.Item) doctemplates.Item { it.Wrap = true; return it }
func bolded(it doctemplates.Item) doctemplates.Item  { it.Bold = true; return it }

func lineItem(id string, x1, y1, x2, y2, stroke float64) doctemplates.Item {
	return doctemplates.Item{ID: id, Type: doctemplates.TypeLine, X1: x1, Y1: y1, X2: x2, Y2: y2, Stroke: stroke}
}

func rectItem(id string, x, y, w, h, stroke float64, fill string) doctemplates.Item {
	return doctemplates.Item{ID: id, Type: doctemplates.TypeRect, X: x, Y: y, Width: w, Height: h, Stroke: stroke, Fill: fill}
}

func tableItem(id string, x, w float64, size int, columns ...doctemplates.ItemColumn) doctemplates.Item {
	return doctemplates.Item{ID: id, Type: doctemplates.TypeTable, X: x, Width: w, FontSize: size, Columns: columns}
}

func fixedBand(h float64, items ...doctemplates.Item) *doctemplates.Band {
	return &doctemplates.Band{Height: h, Elements: append([]doctemplates.Item{}, items...)}
}

func detailOf(t doctemplates.Item) *doctemplates.DetailBand {
	return &doctemplates.DetailBand{Elements: []doctemplates.Item{t}}
}

func banded(b doctemplates.Bands) doctemplates.Document {
	return doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}}, Bands: &b}
}

// twoColumns is the table of the fixtures: a customer and an amount column.
func twoColumns(id string, size int) doctemplates.Item {
	return tableItem(id, 0, 190, size, doctemplates.ItemColumn{Field: "customer", Width: 100}, doctemplates.ItemColumn{Field: "amount", Width: 90})
}

// groupRows is the table data of a group: amounts(n) without the snapshot's totals, whose rows are
// the snapshot rows from first on.
func groupRows(n, first int) *doctemplates.TableData {
	data := amounts(n)
	data.Totals = map[string]string{}
	for i := range n {
		data.Index = append(data.Index, first+i)
	}
	return data
}

// values of the text elements of d as they are bound: a literal or page text as it is, the rest as given.
func valuesFor(d doctemplates.Document, bound map[string]string) map[string]string {
	out := map[string]string{}
	for _, bi := range d.Items() {
		it := bi.Item
		if it.Type != doctemplates.TypeText {
			continue
		}
		if v, ok := bound[it.ID]; ok {
			out[it.ID] = v
		} else {
			out[it.ID] = it.Text
		}
	}
	return out
}

// textsOn returns the texts of a page drawn on a baseline (points), left to right, as one string.
func textsOn(pg pageData, baseline float64) string {
	var glyphs []glyph
	for _, g := range pg.Texts {
		if math.Abs(g.Baseline-baseline) < 1e-6 {
			glyphs = append(glyphs, g)
		}
	}
	sort.SliceStable(glyphs, func(i, j int) bool { return glyphs[i].X < glyphs[j].X })
	var b strings.Builder
	for _, g := range glyphs {
		b.WriteString(g.Text)
	}
	return b.String()
}

func detailAt(err error) doctemplates.Detail {
	d, _ := doctemplates.DetailOf(err)
	return d
}

func rowOf(d doctemplates.Detail) int {
	if d.Row == nil {
		return -1
	}
	return *d.Row
}

func TestBandedLayOutStacksTheBandsOnEveryPage(t *testing.T) {
	d := banded(doctemplates.Bands{
		PageHeader:     fixedBand(14, textItem("e1", "literal", "标题", 0, 0, 100, 9, 16, "left"), textItem("e2", "page", "第 {page}/{pages}", 110, 0, 80, 9, 10, "right"), lineItem("e3", 0, 12, 190, 12, 0.5)),
		DocumentHeader: fixedBand(20, textItem("e4", "field", "customer", 0, 2, 90, 8, 12, "left")),
		Detail:         detailOf(twoColumns("e5", 10)),
		DocumentFooter: fixedBand(15, rectItem("e6", 100, 2, 90, 10, 0.5, "grey2"), bolded(textItem("e7", "group_sum", "amount", 102, 3, 86, 8, 12, "right"))),
		PageFooter:     fixedBand(10, textItem("e8", "literal", "页脚", 0, 2, 100, 6, 8, "left")),
	})
	values := valuesFor(d, map[string]string{"e4": "客户甲", "e7": "123.45"})
	p, err := layOut(d, values, nil, groupRows(70, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	// The detail band is 201 mm on the first page (59 to 260 mm), 221 mm on the others (39 to 260
	// mm); a one-line row at 10 pt is 18.6 pt: page 1 holds the header, 28 rows and the carry-forward
	// row, page 2 the header, the brought-forward row, 30 rows and a carry-forward row, page 3 the
	// rest and the total.
	if len(p.Pages) != 3 || p.Background || p.Banner.Text != bannerText || !near(p.Banner.Top, 10*mm) || !near(p.Banner.Height, 8*mm) || !near(p.Banner.Left, 10*mm) {
		t.Fatalf("%d pages %+v", len(p.Pages), p.Banner)
	}
	rowH := 2*tableInsetY + (ascent+descent)*10
	for n, pg := range p.Pages {
		page := n + 1
		// The page header and footer on every page: the 16 pt title and the page text, and the rule.
		head := 25*mm + ascent*16
		if got := textsOn(pg, head); got != "标题" {
			t.Errorf("page %d title %q", page, got)
		}
		if got, want := textsOn(pg, 25*mm+ascent*10), fmt.Sprintf("第 %d/3", page); got != want {
			t.Errorf("page %d number %q, want %q", page, got, want)
		}
		if got := textsOn(pg, (270-10+2)*mm+ascent*8); got != "页脚" {
			t.Errorf("page %d footer %q", page, got)
		}
		var ruled int
		for _, ru := range pg.Rules {
			if ru.Stroke == 0.5 {
				ruled++
				if !near(ru.X0, 10*mm) || !near(ru.X1, 200*mm) || !near(ru.Y0, 37*mm) || !near(ru.Y1, 37*mm) {
					t.Errorf("page %d rule %+v", page, ru)
				}
			}
		}
		if ruled != 1 {
			t.Errorf("page %d has %d header rules", page, ruled)
		}
		// The document header on the first page only; the table under it, or under the page header.
		first := textsOn(pg, (39+2)*mm+ascent*12)
		if (page == 1) != (first == "客户甲") {
			t.Errorf("page %d document header %q", page, first)
		}
		top := 39 * mm
		if page == 1 {
			top = 59 * mm
		}
		if got := textsOn(pg, top+tableInsetY+ascent*10); !strings.HasPrefix(got, "客户") || !strings.Contains(got, "金额（CNY）") {
			t.Errorf("page %d table header %q at %.2f", page, got, top)
		}
		// Nothing above the safe region's top (25 mm, below the banner strip of 10 to 18 mm) or below its bottom
		// (270 mm): texts, rules and rectangles alike; and every page carries the banner the strip is kept for.
		for _, g := range pg.Texts {
			if g.Baseline-ascent*g.Size < 25*mm-1e-6 || g.Baseline+descent*g.Size > 270*mm+1e-6 {
				t.Errorf("page %d: %q outside the safe region", page, g.Text)
			}
		}
		for _, ru := range pg.Rules {
			if math.Min(ru.Y0, ru.Y1) < 25*mm-1e-6 || math.Max(ru.Y0, ru.Y1) > 270*mm+1e-6 || math.Min(ru.X0, ru.X1) < 10*mm-1e-6 || math.Max(ru.X0, ru.X1) > 200*mm+1e-6 {
				t.Errorf("page %d: a rule outside the safe region: %+v", page, ru)
			}
		}
		for _, b := range pg.Boxes {
			if b.Y < 25*mm-1e-6 || b.Y+b.H > 270*mm+1e-6 || b.X < 10*mm-1e-6 || b.X+b.W > 200*mm+1e-6 {
				t.Errorf("page %d: a rectangle outside the safe region: %+v", page, b)
			}
		}
	}
	// The document footer once, on the last page, directly under the total row: header, brought-forward
	// row, 12 rows and the total row from the page's top.
	end := 39*mm + 15*rowH
	last := p.Pages[2]
	if len(p.Pages[0].Boxes)+len(p.Pages[1].Boxes) != 0 || len(last.Boxes) != 1 {
		t.Fatalf("boxes %+v %+v %+v", p.Pages[0].Boxes, p.Pages[1].Boxes, last.Boxes)
	}
	if b := last.Boxes[0]; !near(b.X, 110*mm) || !near(b.Y, end+2*mm) || !near(b.W, 90*mm) || !near(b.H, 10*mm) || b.Stroke != 0.5 || b.Fill != 232 {
		t.Errorf("document footer rectangle %+v, end of the table %.2f", b, end)
	}
	if got := textsOn(last, end+3*mm+ascent*12); got != "123.45" {
		t.Errorf("document footer sum %q", got)
	}
	for _, g := range last.Texts {
		if g.Text == "1" && g.Size == 12 && !g.Bold {
			t.Errorf("the bold sum drawn regular: %+v", g)
		}
	}
	// The total row is the last of the table: the footer starts where it ends.
	if got := textsOn(last, end-rowH+tableInsetY+ascent*10); !strings.HasPrefix(got, "合计") {
		t.Errorf("total row %q", got)
	}
}

func TestBandedPageTextsAreFilledAfterPagination(t *testing.T) {
	d := banded(doctemplates.Bands{
		PageHeader: fixedBand(10, textItem("e1", "page", "{pages} 页中的第 {page} 页 {page}/{pages}", 0, 0, 190, 8, 10, "center")),
		Detail:     detailOf(twoColumns("e2", 10)),
	})
	// 69 rows are 3 pages here (header 10 mm): page 1 holds 29 rows (25 + 10 = 35 mm to 270 mm, 235 mm
	// is 666 pt: header, 34 rows and the carry-forward row), so use the count the layout gives.
	p, err := layOut(d, valuesFor(d, nil), nil, groupRows(100, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	pages := len(p.Pages)
	if pages < 3 {
		t.Fatalf("%d pages", pages)
	}
	for n := range p.Pages {
		want := fmt.Sprintf("%d 页中的第 %d 页 %d/%d", pages, n+1, n+1, pages)
		if got := textsOn(p.Pages[n], 25*mm+ascent*10); got != want {
			t.Errorf("page %d: %q, want %q", n+1, got, want)
		}
		// A centred text is centred as a whole on each page, with the digits of its page.
		var left, right float64 = math.MaxFloat64, 0
		for _, g := range p.Pages[n].Texts {
			if math.Abs(g.Baseline-(25*mm+ascent*10)) < 1e-6 {
				left, right = math.Min(left, g.X), math.Max(right, g.X+g.Width)
			}
		}
		if !near((left+right)/2, 105*mm) {
			t.Errorf("page %d: text centred at %.3f, want %.3f", n+1, (left+right)/2, 105*mm)
		}
	}
	// The widest page text is checked on every page: a box that holds page 1's text but not the
	// last page's refuses the whole document.
	d.Bands.PageHeader.Elements[0] = textItem("e1", "page", "{page}/{pages}", 0, 0, 10, 8, 10, "left")
	if _, err := layOut(d, valuesFor(d, nil), nil, groupRows(40, 0), plainFake()); err != nil {
		t.Errorf("a short page text that fits every page: %v", err)
	}
	// 3201 rows are 100 pages here (33 on the first, 32 on each of the others): "10/100" is six
	// characters of 5 pt, "1/100" five. A box of 10 mm (28.3 pt, with the 2 pt margin 26.3 pt) holds
	// five (25 pt) but not six (30 pt), so page 10 is the first it refuses although page 1 fits.
	short := func(n int) error {
		_, err := layOut(d, valuesFor(d, nil), nil, groupRows(n, 0), plainFake())
		return err
	}
	if err := short(30); err != nil {
		t.Errorf("a page text of 4 characters: %v", err)
	}
	if err := short(3201); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a page text too wide on later pages: %v", err)
	} else if det := detailAt(err); det.Reason != doctemplates.ReasonDoesNotFit || det.Band != doctemplates.BandPageHeader || det.Element != "e1" {
		t.Errorf("detail %+v", det)
	}
}

func TestBandedDocumentFooterMovesWholeOrIsRefused(t *testing.T) {
	// The detail band is 201 mm on the first page (59 to 260 mm with a page footer of 10 mm). 28 rows
	// are the most a single page holds with its header and total row: 30 lines of 18.6 pt (558 pt) end
	// at 255.85 mm.
	build := func(footer, pageFooter float64) doctemplates.Document {
		return banded(doctemplates.Bands{
			PageHeader:     fixedBand(14, textItem("e1", "literal", "头", 0, 0, 100, 9, 12, "left")),
			DocumentHeader: fixedBand(20, textItem("e2", "literal", "首", 0, 0, 100, 9, 12, "left")),
			Detail:         detailOf(twoColumns("e3", 10)),
			DocumentFooter: fixedBand(footer, rectItem("e4", 0, 0, 100, footer, 0.5, "none")),
			PageFooter:     fixedBand(pageFooter, textItem("e6", "literal", "脚", 0, 0, 100, 5, 8, "left")),
		})
	}
	layout := func(footer, pageFooter float64, rows int) (*page, error) {
		d := build(footer, pageFooter)
		return layOut(d, valuesFor(d, nil), nil, groupRows(rows, 0), plainFake())
	}
	rowH := 2*tableInsetY + (ascent+descent)*10
	end := 59*mm + 30*rowH
	// A page footer of 9.1 mm leaves 5.05 mm under the total row, one of 9.2 mm 4.95 mm: a footer of 5 mm
	// fits under the total row, whole, in the first case, and in the second it is on a page of its own,
	// below the page header, with the page footer; the table's pages are as they were.
	p, err := layout(5, 9.1, 28)
	if err != nil || len(p.Pages) != 1 || len(p.Pages[0].Boxes) != 1 || !near(p.Pages[0].Boxes[0].Y, end) {
		t.Fatalf("a footer that fits: %v %+v", err, p)
	}
	p, err = layout(5, 9.2, 28)
	if err != nil || len(p.Pages) != 2 {
		t.Fatalf("a footer that does not fit: %v", err)
	}
	if len(p.Pages[0].Boxes) != 0 || len(p.Pages[1].Boxes) != 1 || !near(p.Pages[1].Boxes[0].Y, 39*mm) || !near(p.Pages[1].Boxes[0].H, 5*mm) {
		t.Errorf("boxes %+v %+v", p.Pages[0].Boxes, p.Pages[1].Boxes)
	}
	if textsOn(p.Pages[1], 25*mm+ascent*12) != "头" || textsOn(p.Pages[1], (270-9.2)*mm+ascent*8) != "脚" || textsOn(p.Pages[0], (25+14)*mm+ascent*12) != "首" {
		t.Errorf("the moved footer's page: %+v", p.Pages[1].Texts)
	}
	for _, g := range p.Pages[1].Texts {
		if g.Text == "首" || strings.HasPrefix(g.Text, "合计") || strings.HasPrefix(g.Text, "客户") {
			t.Errorf("the table or the document header on the footer's page: %q", g.Text)
		}
	}
	// With one row more the table is two pages, and the footer is under its total row again.
	p, err = layout(5, 9.2, 29)
	if err != nil || len(p.Pages) != 2 || len(p.Pages[1].Boxes) != 1 || p.Pages[1].Boxes[0].Y < 39*mm+3*rowH {
		t.Fatalf("a footer after a short last page: %v", err)
	}
	// A footer that fits no empty page (the page header and footer take 24 mm of the 245) is refused:
	// 221 mm fits exactly, 221.1 mm does not.
	if _, err = layout(221, 10, 28); err != nil {
		t.Errorf("a footer that fills an empty page: %v", err)
	}
	_, err = layout(221.1, 10, 28)
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonBandOverflow || det.Band != doctemplates.BandDocumentFooter || det.Element != "" || det.Row != nil || det.Column != "" {
		t.Errorf("a footer taller than an empty page: %v %+v", err, det)
	}
}

func TestBandedWrapBreaksLikeTableCellsAndRefusesWhatTheBoxCannotHold(t *testing.T) {
	// A 100 pt wide box (35.3 mm) at 10 pt in the fake font: 98 pt of line (the 2 pt margin), 19
	// Latin characters or 9 ideographs; the box is 20 mm (56.7 pt) high: 1.46 + 1.5 * 2 = 4.46 font
	// sizes for three lines (44.6 pt), a fourth line needs 59.6 pt.
	box := func(h float64, align string) doctemplates.Item {
		return wrapped(textItem("e1", "field", "customer", 0, 0, 35.3, h, 10, align))
	}
	lay := func(it doctemplates.Item, v string) (*page, error) {
		d := banded(doctemplates.Bands{PageHeader: fixedBand(30, it), Detail: detailOf(twoColumns("e2", 10))})
		return layOut(d, valuesFor(d, map[string]string{"e1": v}), nil, groupRows(1, 0), plainFake())
	}
	base := 25*mm + ascent*10
	lines := func(p *page) []string {
		var out []string
		for k := 0; ; k++ {
			s := textsOn(p.Pages[0], base+float64(k)*lineStep*10)
			if s == "" {
				return out
			}
			out = append(out, s)
		}
	}
	for v, want := range map[string]string{
		"甲乙丙丁戊己庚辛壬癸":                  "甲乙丙丁戊己庚辛壬|癸",
		"one two three four five six": "one two three four|five six",
		"abcdefghijklmnopqrstuvwxyz":  "abcdefghijklmnopqrs|tuvwxyz",
		"first\nsecond":               "first|second",
		"first\r\nsecond\rthird":      "first|second|third",
		"tab\there":                   "tab here",
		"  lead":                      "  lead",
		"trail   \n  next":            "trail|  next",
		"one\n\ntwo":                  "one",
		"":                            "",
	} {
		p, err := lay(box(20, "left"), v)
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		got := strings.Join(lines(p), "|")
		if v == "one\n\ntwo" {
			// An empty line takes its place and draws nothing.
			if got != "one" || textsOn(p.Pages[0], base+2*lineStep*10) != "two" {
				t.Errorf("an empty line: %q", got)
			}
			continue
		}
		if got != want {
			t.Errorf("%q: lines %q, want %q", v, got, want)
		}
	}
	// Three lines fit, four do not; the 20 mm box leaves room for exactly three.
	if _, err := lay(box(20, "left"), "a\nb\nc"); err != nil {
		t.Errorf("three lines: %v", err)
	}
	_, err := lay(box(20, "left"), "a\nb\nc\nd")
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonDoesNotFit || det.Band != doctemplates.BandPageHeader || det.Element != "e1" {
		t.Errorf("four lines in a three-line box: %v %+v", err, det)
	}
	// 15.8 mm holds two lines (1.46 + 1.5 = 2.96 sizes = 29.6 pt = 10.44 mm); three need 44.6 pt = 15.73 mm.
	if _, err := lay(box(15.7, "left"), "a\nb\nc"); err == nil {
		t.Error("three lines in 15.7 mm")
	}
	if _, err := lay(box(15.8, "left"), "a\nb\nc"); err != nil {
		t.Errorf("three lines in 15.8 mm: %v", err)
	}
	// Each line is aligned on its own: right-aligned lines end at the box's right edge, centred ones are centred.
	p, err := lay(box(20, "right"), "ab\nabcd")
	if err != nil {
		t.Fatal(err)
	}
	right := (10 + 35.3) * mm
	for k, width := range []float64{10, 20} {
		var end float64
		for _, g := range p.Pages[0].Texts {
			if math.Abs(g.Baseline-(base+float64(k)*lineStep*10)) < 1e-6 {
				end = math.Max(end, g.X+g.Width)
			}
		}
		if !near(end, right) {
			t.Errorf("line %d (%.0f pt) ends at %.3f, want %.3f", k+1, width, end, right)
		}
	}
	// A wrapped value is bound with line breaks; one that is not wrapped may not hold any.
	d := banded(doctemplates.Bands{PageHeader: fixedBand(30, textItem("e1", "field", "customer", 0, 0, 100, 10, 10, "left")), Detail: detailOf(twoColumns("e2", 10))})
	_, err = layOut(d, valuesFor(d, map[string]string{"e1": "a\nb"}), nil, groupRows(1, 0), plainFake())
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonNotCovered || det.Element != "e1" {
		t.Errorf("a line break without wrap: %v %+v", err, det)
	}
	// Bold text is measured bold, and drawn so.
	p, err = lay(bolded(box(20, "left")), "abc")
	var bold glyph
	for _, g := range p.Pages[0].Texts {
		if g.Text == "a" {
			bold = g
		}
	}
	if err != nil || !bold.Bold || !near(bold.Width, 6) {
		t.Errorf("bold: %v %+v", err, bold)
	}
	// A character the fonts lack is not covered; a single character wider than the box is too wide.
	_, err = layOut(banded(doctemplates.Bands{PageHeader: fixedBand(30, box(20, "left")), Detail: detailOf(twoColumns("e2", 10))}), map[string]string{"e1": "a☃"}, nil, groupRows(1, 0), plainFake('☃'))
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonNotCovered || det.Element != "e1" || det.Band != doctemplates.BandPageHeader {
		t.Errorf("an uncovered character: %v %+v", err, det)
	}
	narrow := wrapped(textItem("e1", "field", "customer", 0, 0, 10, 20, 28, "left"))
	_, err = layOut(banded(doctemplates.Bands{PageHeader: fixedBand(30, narrow), Detail: detailOf(twoColumns("e2", 10))}), map[string]string{"e1": "甲"}, nil, groupRows(1, 0), plainFake())
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonDoesNotFit || det.Element != "e1" {
		t.Errorf("a character wider than the box: %v %+v", err, det)
	}
	// A combining mark cannot start a line, and a value over 1024 characters is too long.
	_, err = lay(box(20, "left"), "́abc")
	if det := detailAt(err); det.Reason != doctemplates.ReasonNotCovered {
		t.Errorf("a leading mark: %v", err)
	}
	_, err = lay(box(20, "left"), strings.Repeat("a", 1025))
	if det := detailAt(err); det.Reason != doctemplates.ReasonDoesNotFit {
		t.Errorf("1025 characters: %v", err)
	}
}

func TestBandedSingleLineTextsAreSetLikeBoxesOf1x(t *testing.T) {
	d := banded(doctemplates.Bands{
		PageHeader: fixedBand(30,
			textItem("e1", "literal", "标题", 0, 0, 80, 10, 12, "left"),
			textItem("e2", "field", "customer", 0, 12, 60, 10, 10, "right"),
			bolded(textItem("e3", "field", "amount", 70, 12, 60, 10, 10, "center")),
		),
		Detail: detailOf(twoColumns("e4", 10)),
	})
	p, err := layOut(d, map[string]string{"e1": "标题", "e2": "1234.50", "e3": "ab"}, nil, groupRows(1, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	// The elements' glyphs follow the table's on the page: pick them by baseline and weight.
	pick := func(baseline float64, bold bool) []glyph {
		var out []glyph
		for _, g := range p.Pages[0].Texts {
			if near(g.Baseline, baseline) && g.Bold == bold {
				out = append(out, g)
			}
		}
		return out
	}
	// 1 mm is 72/25.4 pt; the band starts at x 10 mm, y 25 mm; the baseline is 1.16 font sizes below the box's top.
	title := pick(25*mm+1.16*12, false)
	if len(title) != 2 || !near(title[0].X, 10*mm) || !near(title[1].X, 10*mm+12) || title[0].Text != "标" || title[0].Size != 12 {
		t.Errorf("title %+v", title)
	}
	amount := pick((25+12)*mm+11.6, false)
	if len(amount) != 7 || !near(amount[0].X, 70*mm-7*5) || !near(amount[6].X, 70*mm-5) {
		t.Errorf("amount %+v", amount)
	}
	centred := pick((25+12)*mm+11.6, true)
	if len(centred) != 2 || !near(centred[0].X, 110*mm-6) || !near(centred[1].X, 110*mm) || !near(centred[0].Width, 6) {
		t.Errorf("centred %+v", centred)
	}
	// What the 1.x boxes refuse, a 2.0 text refuses with the place: a long value, one that holds a control, an uncovered character.
	for name, c := range map[string]struct {
		v      string
		reason string
	}{
		"wider than the box": {strings.Repeat("宽", 20), doctemplates.ReasonDoesNotFit},
		"over 256":           {strings.Repeat("a", 257), doctemplates.ReasonDoesNotFit},
		"a control":          {"a\tb", doctemplates.ReasonNotCovered},
		"a soft hyphen":      {"a­b", doctemplates.ReasonNotCovered},
		"a leading mark":     {"́abc", doctemplates.ReasonNotCovered},
	} {
		_, err := layOut(d, map[string]string{"e1": c.v, "e2": "1", "e3": "1"}, nil, groupRows(1, 0), plainFake())
		if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != c.reason || det.Band != doctemplates.BandPageHeader || det.Element != "e1" {
			t.Errorf("%s: %v %+v", name, err, det)
		}
	}
	for name, values := range map[string]map[string]string{"a missing value": {"e1": "x", "e2": "x"}, "invalid UTF-8": {"e1": "a\xffb", "e2": "x", "e3": "x"}} {
		if _, err := layOut(d, values, nil, groupRows(1, 0), plainFake()); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A 2.0 document takes no background, and needs its table's data.
	if _, err := layOut(d, map[string]string{"e1": "x", "e2": "x", "e3": "x"}, []byte("\x89PNG\r\n\x1a\n"), groupRows(1, 0), plainFake()); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a background: %v", err)
	}
	if _, err := layOut(d, map[string]string{"e1": "x", "e2": "x", "e3": "x"}, nil, nil, plainFake()); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("no table data: %v", err)
	}
}

func TestBandedLinesAndRectanglesAreDrawnWhereGoPutsThem(t *testing.T) {
	d := banded(doctemplates.Bands{
		PageHeader: fixedBand(20, lineItem("e1", 5, 3, 105.5, 3.5, 0.25), lineItem("e2", 0, 19, 190, 19, 2), rectItem("e3", 10, 5, 50, 8, 0, "grey1"), rectItem("e4", 70, 5, 50, 8, 1.5, "none"), rectItem("e5", 130, 5, 50, 8, 0.25, "grey4"), rectItem("e6", 130, 14, 50, 4, 0, "grey3")),
		PageFooter: fixedBand(10, lineItem("e7", 0, 0, 190, 0, 1)),
		Detail:     detailOf(twoColumns("e8", 10)),
	})
	p, err := layOut(d, valuesFor(d, nil), nil, groupRows(1, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	pg := p.Pages[0]
	want := []rule{
		{X0: 15 * mm, Y0: 28 * mm, X1: 115.5 * mm, Y1: 28.5 * mm, Stroke: 0.25},
		{X0: 10 * mm, Y0: 44 * mm, X1: 200 * mm, Y1: 44 * mm, Stroke: 2},
		{X0: 10 * mm, Y0: 260 * mm, X1: 200 * mm, Y1: 260 * mm, Stroke: 1},
	}
	// The table's total rule (1 pt) is among the rules too; the elements' come in band order.
	var got []rule
	for _, ru := range pg.Rules {
		if !(near(ru.Y0, ru.Y1) && ru.Stroke == totalStroke && near(ru.X0, 10*mm) && !near(ru.Y0, 260*mm)) {
			got = append(got, ru)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("rules %+v, want %+v", got, want)
	}
	for i := range want {
		if !near(got[i].X0, want[i].X0) || !near(got[i].Y0, want[i].Y0) || !near(got[i].X1, want[i].X1) || !near(got[i].Y1, want[i].Y1) || got[i].Stroke != want[i].Stroke {
			t.Errorf("rule %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	wantBoxes := []frame{
		{X: 20 * mm, Y: 30 * mm, W: 50 * mm, H: 8 * mm, Stroke: 0, Fill: 245},
		{X: 80 * mm, Y: 30 * mm, W: 50 * mm, H: 8 * mm, Stroke: 1.5, Fill: noFill},
		{X: 140 * mm, Y: 30 * mm, W: 50 * mm, H: 8 * mm, Stroke: 0.25, Fill: 190},
		{X: 140 * mm, Y: 39 * mm, W: 50 * mm, H: 4 * mm, Stroke: 0, Fill: 215},
	}
	if len(pg.Boxes) != len(wantBoxes) {
		t.Fatalf("boxes %+v, want %+v", pg.Boxes, wantBoxes)
	}
	for i, w := range wantBoxes {
		g := pg.Boxes[i]
		if !near(g.X, w.X) || !near(g.Y, w.Y) || !near(g.W, w.W) || !near(g.H, w.H) || g.Stroke != w.Stroke || g.Fill != w.Fill {
			t.Errorf("box %d: %+v, want %+v", i, g, w)
		}
	}
	// The JSON the page program reads: the key is there when a page has rectangles and a 1.x page does not write it.
	b, _ := json.Marshal(pg)
	if !strings.Contains(string(b), `"boxes":[{"x":`) {
		t.Errorf("%s", b)
	}
	if b, _ := json.Marshal(newPage(nil)); strings.Contains(string(b), "boxes") {
		t.Errorf("a page without rectangles: %s", b)
	}
}

func TestBandedColumnsAlignAndLabelAsTheTemplateSays(t *testing.T) {
	t10 := tableItem("e1", 0, 190, 10,
		doctemplates.ItemColumn{Field: "customer", Width: 100, Align: "center", Label: "客户名称"},
		doctemplates.ItemColumn{Field: "amount", Width: 90, Align: "left", Label: "Amount due"})
	d := banded(doctemplates.Bands{Detail: detailOf(t10)})
	p, err := layOut(d, nil, nil, groupRows(2, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	pg := p.Pages[0]
	top := 25 * mm
	// The header is the template's literal label, with no currency suffix, aligned like the column.
	header := textsOn(pg, top+tableInsetY+ascent*10)
	if header != "客户名称Amount due" {
		t.Errorf("header %q", header)
	}
	column := func(from, to float64, baseline float64) (left, right float64) {
		left, right = math.MaxFloat64, 0
		for _, g := range pg.Texts {
			if math.Abs(g.Baseline-baseline) < 1e-6 && g.X >= from-1e-6 && g.X < to {
				left, right = math.Min(left, g.X), math.Max(right, g.X+g.Width)
			}
		}
		return
	}
	// A centred cell is centred between the column's insets; a left-aligned amount starts at the left inset.
	l, rr := column(10*mm, 110*mm, top+tableInsetY+ascent*10)
	if !near((l+rr)/2, 60*mm) {
		t.Errorf("centred header at %.3f", (l+rr)/2)
	}
	row1 := top + (2*tableInsetY + (ascent+descent)*10) + tableInsetY + ascent*10
	if l, _ := column(110*mm, 200*mm, row1); !near(l, 110*mm+tableInsetX) {
		t.Errorf("left-aligned amount starts at %.3f, want %.3f", l, 110*mm+tableInsetX)
	}
	if l, rr := column(10*mm, 110*mm, row1); !near((l+rr)/2, 60*mm) {
		t.Errorf("centred cell at %.3f", (l+rr)/2)
	}
	// Without align an amount column is right-aligned and a text column left-aligned, and the header
	// is the dataset's label with its currency.
	plain := banded(doctemplates.Bands{Detail: detailOf(twoColumns("e1", 10))})
	q, err := layOut(plain, nil, nil, groupRows(2, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	if got := textsOn(q.Pages[0], top+tableInsetY+ascent*10); got != "客户金额（CNY）" {
		t.Errorf("default header %q", got)
	}
	var amount glyph
	for _, g := range q.Pages[0].Texts {
		if g.Text == "0.25" && math.Abs(g.Baseline-row1) < 1e-6 {
			amount = g
		}
	}
	if !near(amount.X+amount.Width, 200*mm-tableInsetX) {
		t.Errorf("default amount ends at %.3f", amount.X+amount.Width)
	}
	// The carried and total rows follow the column's alignment, the sums in the metric columns' own.
	if got := textsOn(q.Pages[0], top+3*(2*tableInsetY+(ascent+descent)*10)+tableInsetY+ascent*10); !strings.HasPrefix(got, "合计") {
		t.Errorf("total row %q", got)
	}
}

func TestBandedTableRefusalsNameTheRowAndTheColumn(t *testing.T) {
	lay := func(size int, columns []doctemplates.ItemColumn, data *doctemplates.TableData, bands ...func(*doctemplates.Bands)) (*page, error) {
		b := doctemplates.Bands{Detail: detailOf(tableItem("e9", 0, 190, size, columns...))}
		for _, f := range bands {
			f(&b)
		}
		d := banded(b)
		return layOut(d, valuesFor(d, nil), nil, data, plainFake('☃'))
	}
	columns := []doctemplates.ItemColumn{{Field: "customer", Width: 100}, {Field: "amount", Width: 90}}
	check := func(name string, err error, reason, column string, row int) {
		t.Helper()
		det := detailAt(err)
		if !errors.Is(err, r.ErrUnsupported) || det.Reason != reason || det.Band != doctemplates.BandDetail || det.Element != "e9" || det.Column != column || rowOf(det) != row {
			t.Errorf("%s: %v; detail %+v", name, err, det)
		}
	}
	// An uncovered character in a cell: the snapshot row, not the group's position.
	data := groupRows(3, 40)
	data.Rows[1][0] = "客☃"
	_, err := lay(10, columns, data)
	check("an uncovered cell", err, doctemplates.ReasonNotCovered, "customer", 41)
	// An uncovered character in the header label of a column.
	data = groupRows(3, 40)
	data.Columns[1].Label = "☃"
	_, err = lay(10, columns, data)
	check("an uncovered header", err, doctemplates.ReasonNotCovered, "amount", -1)
	// A character wider than its column: a 10 mm column holds 28 - 6 = 22 pt of content.
	_, err = lay(28, []doctemplates.ItemColumn{{Field: "customer", Width: 100}, {Field: "amount", Width: 90}}, groupRows(1, 7))
	if err == nil {
		t.Fatal("the 28 pt table is not valid")
	}
	// (the validator allows only 8-12 pt tables; the layout is checked directly for 12 pt below)
	narrow := []doctemplates.ItemColumn{{Field: "customer", Width: 150}, {Field: "amount", Width: 40}}
	// An amount wider than its column: 40 mm is 113.4 pt, 107.4 of content; at 12 pt "123456789012345.00"
	// is 18 characters of 6 pt in the fake font (108 pt).
	data = groupRows(2, 10)
	data.Rows[1][1] = "123456789012345.00"
	_, err = lay(12, narrow, data)
	check("an amount", err, doctemplates.ReasonDoesNotFit, "amount", 11)
	// A carried sum wider than its column, with no row.
	data = groupRows(0, 0)
	for i := range 20 {
		data.Rows = append(data.Rows, []string{"客", "99999999999999.99"})
		data.Index = append(data.Index, 100+i)
	}
	_, err = lay(12, []doctemplates.ItemColumn{{Field: "customer", Width: 140}, {Field: "amount", Width: 50}}, data)
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonDoesNotFit || det.Column != "amount" || det.Row != nil {
		t.Errorf("a carried sum: %v %+v", err, det)
	}
	// The label of the carried rows wider than the first column: 8 mm is 22.7 pt, 16.7 of content.
	_, err = lay(10, []doctemplates.ItemColumn{{Field: "customer", Width: 8}, {Field: "amount", Width: 182}}, groupRows(2, 0))
	check("a carried label", err, doctemplates.ReasonDoesNotFit, "customer", -1)
	// The header and total row taller than the detail band: a 190 mm band less 24 mm; a header of 14 lines
	// (100 ideographs a line is 100 pt at 10 pt in a 100 mm column: 283 pt, 28 per line) is too tall.
	data = groupRows(0, 0)
	data.Columns[0].Label = strings.Repeat("甲", 2500)
	_, err = lay(10, columns, data)
	det := detailAt(err)
	if !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonBandOverflow || det.Band != doctemplates.BandDetail || det.Element != "e9" || det.Row != nil || det.Column != "" {
		t.Errorf("a header taller than the band: %v %+v", err, det)
	}
	// A row taller than the page's room: 200 lines of 15 pt is 3000 pt; the snapshot row is named.
	data = groupRows(3, 30)
	data.Rows[2][0] = strings.Repeat("甲\n", 200)
	_, err = lay(10, columns, data)
	check("a row taller than a page", err, doctemplates.ReasonBandOverflow, "", 32)
	// More than 100 pages: 41 rows... find the rows from the band the page leaves the table.
	short := func(b *doctemplates.Bands) {
		b.PageHeader = fixedBand(100, textItem("e1", "literal", "x", 0, 0, 100, 9, 12, "left"))
		b.PageFooter = fixedBand(101, textItem("e2", "literal", "x", 0, 0, 100, 9, 12, "left"))
	}
	// 245 - 201 = 44 mm = 124.7 pt of room at 10 pt: on page 1 the header, one row and the carry-forward
	// row (3 x 18.6 = 55.8 pt, and 4 rows 74.4 pt, 5 rows 93, 6 rows 111.6, 7 rows 130.2), so page 1 holds the
	// header, 4 rows and the carry-forward row (6 x 18.6 = 111.6); later pages the header, the
	// brought-forward row, 3 rows and the tail.
	pagesFor := func(rows int) (int, error) {
		p, err := lay(10, columns, groupRows(rows, 0), short)
		if err != nil {
			return 0, err
		}
		return len(p.Pages), nil
	}
	per1, perN := 4, 3
	if n, err := pagesFor(per1 + 99*perN); err != nil || n != 100 {
		t.Fatalf("exactly 100 pages: %d %v", n, err)
	}
	_, err = pagesFor(per1 + 99*perN + 1)
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonTooManyPages || det.Band != "" || det.Element != "" || det.Column != "" || det.Row != nil {
		t.Errorf("101 pages: %v %+v", err, det)
	}
	// A footer that needs its own page after 100 table pages is the 101st.
	footer := func(b *doctemplates.Bands) {
		short(b)
		b.DocumentFooter = fixedBand(30, textItem("e3", "literal", "x", 0, 0, 100, 9, 12, "left"))
	}
	if p, err := lay(10, columns, groupRows(per1+97*perN+1, 0), footer); err != nil || len(p.Pages) != 100 {
		t.Errorf("99 table pages and a footer page: %v", err)
	}
	_, err = lay(10, columns, groupRows(per1+99*perN, 0), footer)
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonTooManyPages {
		t.Errorf("100 table pages and a footer page: %v %+v", err, det)
	}
}

func TestBandedLayoutIsDeterministicAndDoesNotChangeItsInput(t *testing.T) {
	d := banded(doctemplates.Bands{
		PageHeader:     fixedBand(14, textItem("e1", "page", "{page}/{pages}", 0, 0, 80, 9, 10, "right"), rectItem("e2", 0, 10, 190, 3, 0.5, "grey1")),
		Detail:         detailOf(twoColumns("e3", 10)),
		DocumentFooter: fixedBand(15, wrapped(textItem("e4", "field", "customer", 0, 0, 100, 12, 10, "left"))),
	})
	values := valuesFor(d, map[string]string{"e4": "一二三 four five six seven"})
	data := groupRows(75, 3)
	before, _ := json.Marshal([]any{d, values, data})
	a, err := layOut(d, values, nil, data, plainFake())
	if err != nil {
		t.Fatal(err)
	}
	b, err := layOut(d, values, nil, data, plainFake())
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Error("two layouts of the same input differ")
	}
	if after, _ := json.Marshal([]any{d, values, data}); string(before) != string(after) {
		t.Error("the layout changed its input")
	}
}

func TestRefusalDetailsOfTheBoxesAndTheTableOf1x(t *testing.T) {
	box1 := func(id string) doctemplates.Element { return box(id, "row", "", 20, 30, 20, 10, "left") }
	check := func(name string, err error, reason, element, column string, row int) {
		t.Helper()
		det := detailAt(err)
		if !errors.Is(err, r.ErrUnsupported) || det.Reason != reason || det.Element != element || det.Column != column || rowOf(det) != row || det.Band != "" {
			t.Errorf("%s: %v; detail %+v", name, err, det)
		}
	}
	d := form("full", 0, 0, box1("n3"))
	for name, c := range map[string]struct {
		v      string
		reason string
	}{
		"wider than its box": {strings.Repeat("宽", 20), doctemplates.ReasonDoesNotFit},
		"too long":           {strings.Repeat("a", 257), doctemplates.ReasonDoesNotFit},
		"uncovered":          {"☃", doctemplates.ReasonNotCovered},
		"a control":          {"a\tb", doctemplates.ReasonNotCovered},
		"a soft hyphen":      {"a­b", doctemplates.ReasonNotCovered},
		"a leading mark":     {"́abc", doctemplates.ReasonNotCovered},
	} {
		_, err := layOut(d, map[string]string{"n3": c.v}, nil, nil, plainFake('☃'))
		check(name, err, c.reason, "n3", "", -1)
	}
	// The table of 1.3: the column by its field id and the snapshot row, no band, no element.
	columns := []doctemplates.TableColumn{{Field: "customer", Width: 60}, {Field: "amount", Width: 60}}
	tbl := tabled("full", 20, 40, 120, 60, 10, columns...)
	data := amounts(5)
	data.Rows[3][0] = "客☃"
	_, err := layOut(tbl, map[string]string{"n1": "x"}, nil, data, plainFake('☃'))
	check("an uncovered cell", err, doctemplates.ReasonNotCovered, "", "customer", 3)
	data = amounts(5)
	data.Rows[4][1] = "123456789.00"
	narrow := tabled("full", 20, 40, 120, 60, 10, doctemplates.TableColumn{Field: "customer", Width: 104}, doctemplates.TableColumn{Field: "amount", Width: 16})
	_, err = layOut(narrow, map[string]string{"n1": "x"}, nil, data, plainFake())
	check("an amount", err, doctemplates.ReasonDoesNotFit, "", "amount", 4)
	data = amounts(3)
	data.Rows[1][0] = strings.Repeat("甲\n", 40)
	_, err = layOut(tbl, map[string]string{"n1": "x"}, nil, data, plainFake())
	check("a row taller than the region", err, doctemplates.ReasonDoesNotFit, "", "", 1)
	short := tabled("full", 20, 40, 120, 30, 10, columns...)
	_, err = layOut(short, map[string]string{"n1": "x"}, nil, amounts(102), plainFake())
	check("102 pages", err, doctemplates.ReasonTooManyPages, "", "", -1)
	label := tabled("full", 20, 40, 120, 60, 10, doctemplates.TableColumn{Field: "customer", Width: 12}, doctemplates.TableColumn{Field: "amount", Width: 108})
	_, err = layOut(label, map[string]string{"n1": "x"}, nil, amounts(3), plainFake())
	check("a carried label", err, doctemplates.ReasonDoesNotFit, "", "customer", -1)
	data = amounts(0)
	data.Columns[0].Label = strings.Repeat("甲", 112)
	_, err = layOut(tabled("full", 20, 40, 120, 44.8, 10, columns...), map[string]string{"n1": "x"}, nil, data, plainFake())
	check("a header and total row taller than the region", err, doctemplates.ReasonDoesNotFit, "", "", -1)
	// The preview banner text of an issued document, as a value and as a cell.
	_, err = issueLayout(d, map[string]string{"n3": bannerText}, nil, nil, plainFake())
	check("the banner as a value", err, doctemplates.ReasonNotCovered, "n3", "", -1)
	data = amounts(3)
	data.Rows[2][0] = bannerText
	_, err = issueLayout(tbl, map[string]string{"n1": "x"}, nil, data, plainFake())
	check("the banner as a cell", err, doctemplates.ReasonNotCovered, "", "customer", 2)
}

func TestBandedIssueLayoutRefusesTheBannerTextWherever2xHoldsIt(t *testing.T) {
	d := banded(doctemplates.Bands{
		PageHeader: fixedBand(20, textItem("e1", "literal", "ok", 0, 0, 100, 9, 12, "left"), textItem("e2", "field", "customer", 0, 10, 100, 9, 12, "left")),
		Detail:     detailOf(twoColumns("e3", 10)),
	})
	data := groupRows(2, 5)
	if _, err := issueLayout(d, valuesFor(d, map[string]string{"e2": "x"}), nil, data, plainFake()); err != nil {
		t.Fatal(err)
	}
	_, err := issueLayout(d, valuesFor(d, map[string]string{"e2": "a " + bannerText}), nil, data, plainFake())
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonNotCovered || det.Band != doctemplates.BandPageHeader || det.Element != "e2" {
		t.Errorf("the banner text as a value: %v %+v", err, det)
	}
	data.Rows[1][1] = bannerText
	data.Rows[1][0] = bannerText
	_, err = issueLayout(d, valuesFor(d, map[string]string{"e2": "x"}), nil, data, plainFake())
	if det := detailAt(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonNotCovered || det.Band != doctemplates.BandDetail || det.Element != "e3" || det.Column != "customer" || rowOf(det) != 6 {
		t.Errorf("the banner text as a cell: %v %+v", err, det)
	}
	// Previews print it: only an issue refuses it.
	if _, err := layOut(d, valuesFor(d, map[string]string{"e2": "a " + bannerText}), nil, groupRows(2, 5), plainFake()); err != nil {
		t.Errorf("the preview layout refused the banner text: %v", err)
	}
}

// A 2.0 document is issued (S03-K): the pages of an issue are the pages of its preview, bands, table, footer and all, and
// differ only in the strip, which the writer sets; a 2.0 document has no background, so one is refused.
func TestIssueLayoutOfA2xDocumentIsItsPreviewLayoutAndTakesNoBackground(t *testing.T) {
	d := banded(doctemplates.Bands{
		PageHeader:     fixedBand(20, textItem("e1", "literal", "ok", 0, 0, 100, 9, 12, "left"), textItem("e2", "field", "customer", 0, 10, 100, 9, 12, "left"), textItem("e4", "page", "{page}/{pages}", 110, 0, 80, 9, 12, "right")),
		Detail:         detailOf(twoColumns("e3", 10)),
		DocumentFooter: fixedBand(15, textItem("e5", "literal", "total", 0, 1, 100, 9, 12, "left")),
	})
	values := valuesFor(d, map[string]string{"e2": "x"})
	issued, err := issueLayout(d, values, nil, groupRows(60, 5), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	previewed, err := layOut(d, values, nil, groupRows(60, 5), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	if len(issued.Pages) < 2 || !reflect.DeepEqual(issued.Pages, previewed.Pages) || !reflect.DeepEqual(issued.Footer, previewed.Footer) {
		t.Fatalf("the issue of a 2.0 document is laid out as %d pages, its preview as %d", len(issued.Pages), len(previewed.Pages))
	}
	if _, err = issueLayout(d, values, []byte("\x89PNG\r\n\x1a\n"), groupRows(60, 5), plainFake()); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a background for a 2.0 document: %v", err)
	}
}

func TestMinimumDetailHeightIsFourOneLineRows(t *testing.T) {
	// The validator's least detail band is the table's header, brought-forward, one body and
	// carry-forward row, one line each: the layout's own row height times four.
	for size := doctemplates.MinTableFontSize; size <= doctemplates.MaxTableFontSize; size++ {
		lay := &tableLayout{size: float64(size)}
		row := lay.rowHeight([]cell{{lines: []string{"x"}}})
		want := int(math.Ceil(4*row/mm*10 - 1e-9))
		if got := doctemplates.MinDetailHeightTenths(size); got != want {
			t.Errorf("%d pt: %d tenths of a millimetre, want %d", size, got, want)
		}
	}
}

func TestSumDecimalsIsTheTablesRule(t *testing.T) {
	// The group sum of doctemplates and the table's column are one rule: the same sums for the same values.
	for _, c := range []struct {
		scale  int
		values []string
	}{
		{2, []string{"1.50", "2", "-0.25", "9007199254740993.01"}},
		{0, []string{"1", "2.5", "0.125", "-1"}},
		{2, []string{"1", "2"}},
		{0, []string{"-5", "2"}},
		{3, []string{"0.5"}},
		{2, nil},
		{0, []string{"0"}},
		{4, []string{"-0.0001", "0.00005"}},
	} {
		col := &column{scale: c.scale}
		for _, v := range c.values {
			if err := col.add(v); err != nil {
				t.Fatal(err)
			}
		}
		got, err := doctemplates.SumDecimals(c.values, c.scale)
		if err != nil || got != col.String() {
			t.Errorf("%v at scale %d: %q (%v), the table's column says %q", c.values, c.scale, got, err, col.String())
		}
	}
	if _, err := doctemplates.SumDecimals([]string{"1", "1,000"}, 2); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a value that is not a decimal: %v", err)
	}
}

// TestBandedGroupSumPrintsAsTheTablePrintsItsTotals: the platform computes a group sum, so it prints it as it
// prints a table total, with grouped digits and the scale the sum was bound with, whether or not the field is a
// column of the table; the table's own total and the sum of the same rows are one text.
func TestBandedGroupSumPrintsAsTheTablePrintsItsTotals(t *testing.T) {
	sum := func(id, field string, x float64) doctemplates.Item {
		return textItem(id, "group_sum", field, x, 0, 60, 8, 10, "left")
	}
	d := banded(doctemplates.Bands{
		PageHeader: fixedBand(20, sum("e1", "amount", 0), sum("e2", "weight", 70)), // weight is no column of the table
		Detail:     detailOf(twoColumns("e3", 10)),
	})
	data := groupRows(5, 0) // amounts 0.25, 100.25, ... 400.25: 1001.25 in all
	lay := func(amount, weight string) (*page, error) {
		return layOut(d, map[string]string{"e1": amount, "e2": weight}, nil, data, plainFake())
	}
	p, err := lay("1001.25", "1234567.800")
	if err != nil {
		t.Fatal(err)
	}
	pg := p.Pages[0]
	base := 25*mm + ascent*10
	var first, second []string
	for _, g := range pg.Texts {
		if near(g.Baseline, base) {
			if g.X < 70*mm {
				first = append(first, g.Text)
			} else {
				second = append(second, g.Text)
			}
		}
	}
	// 1,001.25 as the table's total row prints it, and the weight with grouped digits and its three decimals.
	if got := strings.Join(first, ""); got != "1,001.25" {
		t.Errorf("the amount sum prints %q", got)
	}
	if got := strings.Join(second, ""); got != "1,234,567.800" {
		t.Errorf("the weight sum prints %q", got)
	}
	var totalText string
	for _, g := range pg.Texts {
		if g.Bold && g.Text == "1,001.25" {
			totalText = g.Text
		}
	}
	if totalText != "1,001.25" {
		t.Errorf("the table's total is not the same text: %q", totalText)
	}
	// Signs, scales and zero as the table groups them.
	for in, want := range map[string]string{"-1234.5": "-1,234.5", "1000000": "1,000,000", "0.00": "0.00", "999": "999", "-999": "-999", "1000": "1,000", "-1000.001": "-1,000.001", "9007199254740993.01": "9,007,199,254,740,993.01"} {
		q, err := lay(in, "1")
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		var got []string
		for _, g := range q.Pages[0].Texts {
			if near(g.Baseline, base) && g.X < 70*mm {
				got = append(got, g.Text)
			}
		}
		if strings.Join(got, "") != want {
			t.Errorf("%s prints %q, want %q", in, strings.Join(got, ""), want)
		}
	}
	// What is not an exact decimal is not printed: the bound sum is an integrity failure here.
	for _, in := range []string{"1e3", "1,000", " 1", ""} {
		if _, err := lay(in, "1"); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%q: %v", in, err)
		}
	}
	// The grouped text is the one that must fit: 1234567.89 is ten characters of 5 pt (50 pt) in a box that
	// holds 54.7 pt, and 1,234,567.89 twelve (60 pt).
	narrow := banded(doctemplates.Bands{PageHeader: fixedBand(20, textItem("e1", "group_sum", "amount", 0, 0, 20, 8, 10, "left")), Detail: detailOf(twoColumns("e3", 10))})
	_, err = layOut(narrow, map[string]string{"e1": "1234567.89"}, nil, data, plainFake())
	det, _ := doctemplates.DetailOf(err)
	if !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonDoesNotFit || det.Band != doctemplates.BandPageHeader || det.Element != "e1" {
		t.Errorf("a grouped sum wider than its box: %v %+v", err, det)
	}
	if _, err = layOut(narrow, map[string]string{"e1": "123456.89"}, nil, data, plainFake()); err != nil {
		t.Errorf("123,456.89 fits: %v", err)
	}
	// A literal or a field that looks like a number is printed as it is: only the platform's own sum is grouped.
	plain := banded(doctemplates.Bands{PageHeader: fixedBand(20, textItem("e1", "field", "amount", 0, 0, 60, 8, 10, "left")), Detail: detailOf(twoColumns("e3", 10))})
	q, err := layOut(plain, map[string]string{"e1": "1234567.89"}, nil, data, plainFake())
	if err != nil {
		t.Fatal(err)
	}
	var raw []string
	for _, g := range q.Pages[0].Texts {
		if near(g.Baseline, base) {
			raw = append(raw, g.Text)
		}
	}
	if strings.Join(raw, "") != "1234567.89" {
		t.Errorf("a field prints %q", strings.Join(raw, ""))
	}
}

// TestGroupDigitsIsTheTablesRule: doctemplates.GroupDigits (for the width of a sample) and the layout's
// groupDigits are one rule.
func TestGroupDigitsIsTheTablesRule(t *testing.T) {
	for _, v := range []string{"0", "1", "12", "123", "1234", "12345", "123456", "1234567", "-1", "-1234", "-1234567.89", "0.5", "1000.000", "9007199254740993.01", "-0.01", "100", "1000000000000"} {
		if a, b := groupDigits(v), doctemplates.GroupDigits(v); a != b {
			t.Errorf("%s: the layout prints %q, doctemplates %q", v, a, b)
		}
	}
	for _, v := range []string{"", "1e3", "1,000", "x"} {
		if got := doctemplates.GroupDigits(v); got != v {
			t.Errorf("%q: %q", v, got)
		}
	}
}
