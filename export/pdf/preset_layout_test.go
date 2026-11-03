package pdf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// Page presets and the 2.x overprint profile at the layout (S06-B). No Typst: the fake font makes every width exact.

// overprintDocument is a 2.0 document with room around everything, so that offsets fit: its bands hold a literal, a field, a line and a
// rectangle, its table is inset 6 mm.
func overprintDocument() doctemplates.Document {
	d := banded(doctemplates.Bands{
		PageHeader: fixedBand(20, textItem("e1", "literal", "标题", 6, 6, 60, 8, 12, "left"), textItem("e2", "field", "customer", 80, 6, 60, 8, 12, "left"), lineItem("e3", 6, 16, 150, 16, 0.5)),
		Detail:     detailOf(tableItem("e4", 6, 170, 10, doctemplates.ItemColumn{Field: "customer", Width: 100}, doctemplates.ItemColumn{Field: "amount", Width: 70})),
		PageFooter: fixedBand(20, rectItem("e5", 6, 4, 40, 8, 0.5, "grey2"), textItem("e6", "page", "{page}/{pages}", 60, 4, 40, 8, 10, "left")),
	})
	return d
}

func withProfile(d doctemplates.Document, x, y float64, variables bool) doctemplates.Document {
	d.Page = &doctemplates.Page{Print: &doctemplates.PagePrint{OffsetX: x, OffsetY: y, VariablesOnly: variables}}
	return d
}

func overprintValues(d doctemplates.Document) map[string]string {
	return valuesFor(d, map[string]string{"e2": "客户甲"})
}

func TestOverprintOffsetMovesEveryElementTheTableAndItsPagesButNotTheStrip(t *testing.T) {
	base := overprintDocument()
	shifted := withProfile(base, 3, 2, false)
	a, err := layOut(base, overprintValues(base), nil, groupRows(70, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	b, err := layOut(shifted, overprintValues(shifted), nil, groupRows(70, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Pages) != len(b.Pages) || len(a.Pages) < 2 {
		t.Fatalf("pages %d and %d", len(a.Pages), len(b.Pages))
	}
	dx, dy := 3*mm, 2*mm
	for n := range a.Pages {
		pa, pb := a.Pages[n], b.Pages[n]
		if len(pa.Texts) != len(pb.Texts) || len(pa.Cells) != len(pb.Cells) || len(pa.Rules) != len(pb.Rules) || len(pa.Boxes) != len(pb.Boxes) || len(pa.Fills) != len(pb.Fills) {
			t.Fatalf("page %d: the offset changed what is drawn", n+1)
		}
		for i, g := range pa.Texts {
			if h := pb.Texts[i]; !near(h.X-g.X, dx) || !near(h.Baseline-g.Baseline, dy) || h.Text != g.Text {
				t.Fatalf("page %d text %d moved by %v, %v", n+1, i, h.X-g.X, h.Baseline-g.Baseline)
			}
		}
		for i, c := range pa.Cells {
			if e := pb.Cells[i]; !near(e.X-c.X, dx) || !near(e.Y-c.Y, dy) || !near(e.W, c.W) {
				t.Fatalf("page %d cell %d", n+1, i)
			}
		}
		for i, l := range pa.Rules {
			if m := pb.Rules[i]; !near(m.X0-l.X0, dx) || !near(m.Y1-l.Y1, dy) {
				t.Fatalf("page %d rule %d", n+1, i)
			}
		}
		for i, x := range pa.Boxes {
			if y := pb.Boxes[i]; !near(y.X-x.X, dx) || !near(y.Y-x.Y, dy) {
				t.Fatalf("page %d box %d", n+1, i)
			}
		}
	}
	if a.Banner.Left != b.Banner.Left || a.Banner.Top != b.Banner.Top || a.Banner.Baseline != b.Banner.Baseline {
		t.Fatal("the offset moved the strip")
	}
}

func TestVariablesOnlyPrintsTheVariablesAndTheTableValuesOnly(t *testing.T) {
	d := withProfile(overprintDocument(), 0, 0, true)
	p, err := layOut(d, overprintValues(d), nil, groupRows(5, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	pg := p.Pages[0]
	if len(pg.Rules) != 0 || len(pg.Boxes) != 0 || len(pg.Cells) != 0 || len(pg.Fills) != 0 {
		t.Fatalf("decoration or table rules in a variables-only print: %d %d %d %d", len(pg.Rules), len(pg.Boxes), len(pg.Cells), len(pg.Fills))
	}
	var all string
	for _, g := range pg.Texts {
		all += g.Text
	}
	for _, want := range []string{"客户甲", "1/1", "客户0"} {
		if !bytes.Contains([]byte(all), []byte(want)) {
			t.Errorf("%q is a variable and is printed", want)
		}
	}
	for _, not := range []string{"标题", "金额"} {
		if bytes.Contains([]byte(all), []byte(not)) {
			t.Errorf("%q is a literal or a header and is not printed", not)
		}
	}
	// The same document in full mode draws all of it.
	f := withProfile(overprintDocument(), 0, 0, false)
	q, err := layOut(f, overprintValues(f), nil, groupRows(5, 0), plainFake())
	if err != nil || len(q.Pages[0].Rules) == 0 || len(q.Pages[0].Boxes) == 0 || len(q.Pages[0].Cells) == 0 {
		t.Fatal("full mode", err)
	}
	// A literal that is the preview's banner text is no refusal when it is not printed, as in 1.x.
	d.Bands.PageHeader.Elements[0].Text = bannerText
	if _, err = issueLayout(d, overprintValues(d), nil, groupRows(5, 0), plainFake()); err != nil {
		t.Fatalf("variables-only leaves the banner text literal out: %v", err)
	}
	f.Bands.PageHeader.Elements[0].Text = bannerText
	if _, err = issueLayout(f, overprintValues(f), nil, groupRows(5, 0), plainFake()); !errors.Is(err, r.ErrUnsupported) {
		t.Fatalf("in full mode it is refused: %v", err)
	}
}

func pngOf(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

func TestBackgroundOfA2xProfileIsDrawnInFullModeAndOnlyThePageSizeOnes(t *testing.T) {
	data := pngOf(210, 297)
	sum := sha256.Sum256(data)
	ref := &doctemplates.BackgroundRef{ID: "0123456789abcdef0123456789abcdef", SHA256: hex.EncodeToString(sum[:])}
	d := overprintDocument()
	d.Page = &doctemplates.Page{Print: &doctemplates.PagePrint{Background: ref}}
	values := overprintValues(d)
	p, err := layOut(d, values, data, groupRows(3, 0), plainFake())
	if err != nil || !p.Background {
		t.Fatal("full mode draws the background", err)
	}
	for name, c := range map[string]struct {
		d  doctemplates.Document
		bg []byte
		is error
	}{
		"missing in full mode":      {d, nil, r.ErrInvalid},
		"given without a reference": {overprintDocument(), data, r.ErrInvalid},
		"another digest":            {d, pngOf(210, 298), r.ErrIntegrity},
		"not a PNG":                 {d, []byte("%PDF-1.7 not a png............................"), r.ErrIntegrity},
		"over the budget":           {d, make([]byte, doctemplates.MaxBackgroundBytes+1), r.ErrBudget},
	} {
		if _, err := layOut(c.d, values, c.bg, groupRows(3, 0), plainFake()); !errors.Is(err, c.is) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// In variables-only mode none is drawn, and one given is refused.
	v := d
	v.Page = &doctemplates.Page{Print: &doctemplates.PagePrint{Background: ref, VariablesOnly: true}}
	if p, err = layOut(v, values, nil, groupRows(3, 0), plainFake()); err != nil || p.Background {
		t.Fatal("variables-only", err)
	}
	if _, err = layOut(v, values, data, groupRows(3, 0), plainFake()); !errors.Is(err, r.ErrInvalid) {
		t.Fatal("a background in variables-only mode", err)
	}
	// A PNG of the wrong aspect for the page's preset: a landscape picture for an A4 portrait page.
	land := pngOf(297, 210)
	lsum := sha256.Sum256(land)
	d.Page.Print.Background = &doctemplates.BackgroundRef{ID: ref.ID, SHA256: hex.EncodeToString(lsum[:])}
	if _, err = layOut(d, values, land, groupRows(3, 0), plainFake()); !errors.Is(err, r.ErrInvalid) {
		t.Fatalf("a landscape background on a portrait page: %v", err)
	}
	d.Page.Preset = "a4_landscape"
	if _, err = layOut(d, values, land, groupRows(3, 0), plainFake()); err != nil {
		t.Fatalf("the same picture on its own page: %v", err)
	}
}

func TestEveryPresetLaysOutInsideItsSafeRegionAndWritesItsPageSize(t *testing.T) {
	for _, pre := range doctemplates.Presets {
		bw := pre.BandWidth()
		d := banded(doctemplates.Bands{
			PageHeader: fixedBand(8, textItem("e1", "literal", "标题", 0, 0, bw, 6, 10, "left")),
			Detail:     detailOf(tableItem("e2", 0, bw, 8, doctemplates.ItemColumn{Field: "customer", Width: bw / 2}, doctemplates.ItemColumn{Field: "amount", Width: bw / 2})),
			PageFooter: fixedBand(8, textItem("e3", "page", "{page}/{pages}", 0, 0, bw, 6, 8, "right")),
		})
		if pre.ID != "a4" {
			d.Page = &doctemplates.Page{Preset: pre.ID}
		}
		d.Bands.Detail.Elements[0].Columns[0].Width = math.Round(bw*5) / 10
		d.Bands.Detail.Elements[0].Columns[1].Width = bw - d.Bands.Detail.Elements[0].Columns[0].Width
		p, err := layOut(d, valuesFor(d, nil), nil, groupRows(40, 0), plainFake())
		if err != nil {
			t.Fatalf("%s: %v", pre.ID, err)
		}
		if len(p.Pages) < 2 {
			t.Errorf("%s: 40 rows fit one page of %v mm", pre.ID, pre.Bottom-pre.Top)
		}
		left, right, top, bottom := pre.Left*mm, (pre.Width-pre.Left)*mm, pre.Top*mm, pre.Bottom*mm
		for n, pg := range p.Pages {
			for _, g := range pg.Texts {
				if g.X < left-1e-6 || g.X+g.Width > right+1e-6 || g.Baseline < top || g.Baseline > bottom+1e-6 {
					t.Fatalf("%s page %d: %q at %.2f, %.2f is outside the safe region", pre.ID, n+1, g.Text, g.X, g.Baseline)
				}
			}
			for _, c := range pg.Cells {
				if c.X < left-1e-6 || c.X+c.W > right+1e-6 || c.Y < top-1e-6 || c.Y+c.H > bottom+1e-6 {
					t.Fatalf("%s page %d: a cell outside the safe region", pre.ID, n+1)
				}
			}
		}
		if want := bannerFor(pre.Strip); !reflectEqualBanner(p.Banner, want) {
			t.Errorf("%s: the banner is not the preset's strip", pre.ID)
		}
		data, _ := json.Marshal(p)
		hasPage := bytes.Contains(data, []byte(`"page":{`))
		if hasPage == (pre.ID == "a4") {
			t.Errorf("%s: data.json carries a page size: %v (A4 leaves it out so that its bytes are what they were)", pre.ID, hasPage)
		}
		if p.Page != nil && (p.Page.Width != pre.Width || p.Page.Height != pre.Height) {
			t.Errorf("%s: page size %+v", pre.ID, p.Page)
		}
	}
}

func reflectEqualBanner(a, b banner) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestStampLinesReadAsTheStamp(t *testing.T) {
	stamp := "ISSUED No. AR-7 - 2026-10-28T06:12:03Z - 0123456789abcdef0123456789abcdef"
	if got := stampLines(stamp, 1); len(got) != 1 || got[0] != stamp {
		t.Fatal(got)
	}
	got := stampLines(stamp, 2)
	if len(got) != 2 || got[0]+" "+got[1] != stamp || got[0] != "ISSUED No. AR-7 -" {
		t.Fatal(got)
	}
	if got = stampLines("ISSUED", 2); len(got) != 1 {
		t.Fatal(got)
	}
}
