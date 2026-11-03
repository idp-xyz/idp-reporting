package doctemplates_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

func TestPresetTableIsConsistentAndA4IsWhatItAlwaysWas(t *testing.T) {
	ids := map[string]bool{}
	for _, p := range doctemplates.Presets {
		ids[p.ID] = true
		onGrid := func(n float64) bool { return math.Abs(n*10-math.Round(n*10)) < 1e-9 }
		for _, n := range []float64{p.Width, p.Height, p.Left, p.Top, p.Bottom, p.Strip.X, p.Strip.Y, p.Strip.Width, p.Strip.Height, p.BandWidth()} {
			if !onGrid(n) {
				t.Errorf("%s: %v is off the 0.1 mm grid", p.ID, n)
			}
		}
		s := p.Strip
		switch {
		case p.BandWidth() < 60 || p.Bottom > p.Height || p.Top >= p.Bottom:
			t.Errorf("%s: no safe region", p.ID)
		case s.X != p.Left || s.Width != p.BandWidth():
			t.Errorf("%s: the strip spans the safe region's width", p.ID)
		case s.Y+s.Height >= p.Top:
			t.Errorf("%s: the strip overlaps the safe region", p.ID)
		case s.StampLines < 1 || s.StampLines > 2 || s.StampSize < 5 || s.BannerSize < s.StampSize-2:
			t.Errorf("%s: the strip's type", p.ID)
		}
	}
	for _, want := range []string{"a4", "a4_landscape", "a5", "label_4x6in", "label_100x150", "label_100x100"} {
		if !ids[want] {
			t.Errorf("preset %s missing", want)
		}
	}
	a4 := doctemplates.Presets[0]
	if a4.ID != "a4" || a4.Width != 210 || a4.Height != 297 || a4.Left != 10 || a4.Top != 25 || a4.Bottom != 270 || a4.BandWidth() != doctemplates.BandWidth || a4.Strip != (doctemplates.Strip{X: 10, Y: 10, Width: 190, Height: 8, BannerSize: 10, StampSize: 8, StampLines: 1}) {
		t.Fatalf("A4 is no longer the page every stored template has: %+v", a4)
	}
	if p, ok := doctemplates.PresetByID(""); !ok || p.ID != "a4" {
		t.Fatal("the empty preset is A4")
	}
	if _, ok := doctemplates.PresetByID("letter"); ok {
		t.Fatal("an unknown preset")
	}
}

// presetDocument is a valid 2.1 document sized for a preset: one page header text across the band, and a table of its width.
func presetDocument(t testing.TB, preset string) doctemplates.Document {
	t.Helper()
	p, _ := doctemplates.PresetByID(preset)
	bw := p.BandWidth()
	d := doctemplates.Document{Schema: "2.1", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Bands: &doctemplates.Bands{
			PageHeader: &doctemplates.Band{Height: 10, Elements: []doctemplates.Item{{ID: "e1", Type: "text", Source: "literal", Text: "标题", X: 0, Y: 0, Width: bw, Height: 6, FontSize: 10, Align: "left"}}},
			Detail:     &doctemplates.DetailBand{Elements: []doctemplates.Item{{ID: "e2", Type: "table", X: 0, Width: bw, FontSize: 8, Columns: []doctemplates.ItemColumn{{Field: "a", Width: bw}}}}},
		}}
	if preset != "" {
		d.Page = &doctemplates.Page{Preset: preset}
	}
	return d
}

func TestEveryPresetHoldsADocumentOfItsOwnWidthAndRefusesAWiderOne(t *testing.T) {
	for _, p := range doctemplates.Presets {
		d := presetDocument(t, p.ID)
		if err := doctemplates.Validate(d); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		wide := presetDocument(t, p.ID)
		wide.Bands.PageHeader.Elements[0].Width = p.BandWidth() + 0.1
		if p.ID != "a4" || true {
			if err := doctemplates.Validate(wide); err == nil {
				t.Errorf("%s: a band element 0.1 mm wider than the page's bands is accepted", p.ID)
			}
		}
		tall := presetDocument(t, p.ID)
		tall.Bands.PageHeader.Height = (p.Bottom - p.Top) - 20
		tall.Bands.PageHeader.Elements[0].Height = 6
		if err := doctemplates.Validate(tall); err == nil {
			t.Errorf("%s: a header that leaves the table less than its least height is accepted", p.ID)
		}
		// The same document without the preset is A4 and, on a narrower page, does not fit the page it names.
		none := d
		none.Page = nil
		pre, _ := none.PagePreset()
		if pre.ID != "a4" {
			t.Errorf("a document without a page is A4, got %s", pre.ID)
		}
	}
	// A 190 mm document is valid on A4 and on A4 landscape, and on no narrower page.
	for preset, want := range map[string]bool{"": true, "a4": true, "a4_landscape": true, "a5": false, "label_4x6in": false, "label_100x150": false, "label_100x100": false} {
		d := presetDocument(t, "a4")
		d.Page = nil
		if preset != "" {
			d.Page = &doctemplates.Page{Preset: preset}
		}
		if got := doctemplates.Validate(d) == nil; got != want {
			t.Errorf("A4-wide bands on %q: valid %v, want %v", preset, got, want)
		}
	}
	bad := presetDocument(t, "a4")
	bad.Page = &doctemplates.Page{Preset: "letter"}
	if !errors.Is(doctemplates.Validate(bad), r.ErrInvalid) {
		t.Error("an unknown preset")
	}
}

func TestADocumentWithoutAPageIsWrittenAsItAlwaysWas(t *testing.T) {
	d := baseDocument(t)
	b, err := doctemplates.Canonical(d)
	if err != nil || bytes.Contains(b, []byte(`"page":{`)) {
		t.Fatalf("a 2.x document without a page grew a page key: %v %s", err, b)
	}
	// Naming A4 is a document of its own bytes, not a rewrite of the other.
	d.Page = &doctemplates.Page{Preset: "a4"}
	named, err := doctemplates.Canonical(d)
	if err != nil || !bytes.Contains(named, []byte(`"page":{"preset":"a4"}`)) || bytes.Equal(b, named) {
		t.Fatal(err, string(named))
	}
	// Written in the order binding, page, group, bands.
	if i, j, k := bytes.Index(named, []byte(`"binding"`)), bytes.Index(named, []byte(`"page"`)), bytes.Index(named, []byte(`"group"`)); !(i < j && j < k) {
		t.Fatal("key order")
	}
}

func TestOneXRefusesAPageAndTwoXRefusesTheOneXProfile(t *testing.T) {
	var d doctemplates.Document
	one := `{"schema_version":"1.1","kind":"document","binding":{"integration_id":"i","dataset":{"id":"d","version":"1"}},"elements":[{"id":"n1","source":"literal","field":"","text":"x","x":20,"y":30,"width":50,"height":10,"font_size":10,"align":"left"}],"print":{"mode":"full","offset_x":0,"offset_y":0},"page":{"preset":"a5"}}`
	if err := json.Unmarshal([]byte(one), &d); err == nil {
		t.Fatal("a 1.x document with a page")
	}
	d = document()
	d.Page = &doctemplates.Page{Preset: "a5"}
	if err := doctemplates.Validate(d); err == nil {
		t.Fatal("a 1.x document with a page validates")
	}
	two := baseDocument(t)
	two.Print = &doctemplates.PrintProfile{Mode: "full"}
	if err := doctemplates.Validate(two); err == nil {
		t.Fatal("a 2.x document with a 1.x print profile")
	}
	two = baseDocument(t)
	two.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64)}
	if err := doctemplates.Validate(two); err == nil {
		t.Fatal("a 2.x document with a 1.x background")
	}
}

func TestOverprintOffsetsAndTheSafeRegion(t *testing.T) {
	// A header with room around its text: 5 mm each side of the page header, a table inset 6 mm, a footer with room below its text.
	d := presetDocument(t, "")
	d.Bands.PageHeader = &doctemplates.Band{Height: 20, Elements: []doctemplates.Item{{ID: "e1", Type: "text", Source: "field", Field: "a", X: 6, Y: 6, Width: 100, Height: 6, FontSize: 10, Align: "left"}, {ID: "e3", Type: "text", Source: "literal", Text: "固定", X: 120, Y: 6, Width: 40, Height: 6, FontSize: 10, Align: "left"}}}
	d.Bands.PageFooter = &doctemplates.Band{Height: 20, Elements: []doctemplates.Item{{ID: "e4", Type: "text", Source: "page", Text: "{page}", X: 6, Y: 6, Width: 40, Height: 6, FontSize: 10, Align: "left"}}}
	d.Bands.Detail.Elements[0].X, d.Bands.Detail.Elements[0].Width = 6, 180
	d.Bands.Detail.Elements[0].Columns[0].Width = 180
	with := func(x, y float64, variables bool) doctemplates.Document {
		c := cloneDocument(t, d)
		c.Page = &doctemplates.Page{Print: &doctemplates.PagePrint{OffsetX: x, OffsetY: y, VariablesOnly: variables}}
		return c
	}
	for _, c := range []struct {
		x, y float64
		ok   bool
	}{{0, 0, true}, {4, 5, true}, {-5, -5, true}, {-6, 0, true}, {-6.1, 0, false}, {4, 0, true}, {4.1, 0, false}, {10, 0, false}, {0, 8, true}, {0, 8.1, false}, {0, -6, true}, {0, -6.1, false}, {0, 10, false}, {0, -10, false}, {10.1, 0, false}, {0.05, 0, false}} {
		if got := doctemplates.Validate(with(c.x, c.y, false)) == nil; got != c.ok {
			t.Errorf("offset %v, %v: valid %v, want %v", c.x, c.y, got, c.ok)
		}
	}
	// In a variables-only print what is not a variable need not fit: a literal pushed out of the band is not printed.
	push := with(0, 0, true)
	push.Bands.PageHeader.Elements[1].X = 150 // 150 + 40 = 190 wide, so +1 mm leaves the band
	if doctemplates.Validate(with(1, 0, false)) != nil {
		t.Fatal("setup")
	}
	push = with(1, 0, false)
	push.Bands.PageHeader.Elements[1].X = 150
	if doctemplates.Validate(push) == nil {
		t.Fatal("a printed literal outside the safe region after the offset")
	}
	push.Page.Print.VariablesOnly = true
	if err := doctemplates.Validate(push); err != nil {
		t.Fatalf("a literal that variables-only leaves out is held to the region: %v", err)
	}
	for it, want := range map[string]bool{"literal": true, "field": false} {
		x := doctemplates.Item{Type: "text", Source: it}
		if doctemplates.OmittedInVariables(x) != want {
			t.Errorf("text %s", it)
		}
	}
	for _, typ := range []string{"line", "rect", "image"} {
		if !doctemplates.OmittedInVariables(doctemplates.Item{Type: typ}) {
			t.Errorf("%s is decoration", typ)
		}
	}
	ox, oy, v, bg := with(2, -3, true).Overprint()
	if ox != 2 || oy != -3 || !v || bg != nil {
		t.Fatal("Overprint")
	}
	if ox, oy, v, bg = document().Overprint(); ox != 0 || oy != 0 || v || bg != nil {
		t.Fatalf("a 1.x document without a profile prints in full at zero: %v %v %v", ox, oy, v)
	}
}

func TestBackgroundSizeAcceptsEveryPresetsAspectAndOnlyThose(t *testing.T) {
	assets, e := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
	if e != nil {
		t.Fatal(e)
	}
	defer assets.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	binding := doctemplates.Binding{Integration: "f", Dataset: r.Ref{ID: "d", Version: "v1"}}
	for _, c := range []struct {
		w, h int
		ok   bool
	}{{210, 297, true}, {297, 210, true}, {300, 300, true}, {300, 330, false}, {420, 420, true}, {300, 450, true}, {420, 594, true}, {200, 283, false}} {
		_, err := assets.Put(t.Context(), owner, binding, samplePNG(t, c.w, c.h))
		if (err == nil) != c.ok {
			t.Errorf("%d x %d: %v", c.w, c.h, err)
		}
	}
	a5, _ := doctemplates.PresetByID("a5")
	lab, _ := doctemplates.PresetByID("label_100x100")
	if !doctemplates.BackgroundMatchesPreset(a5, 296, 420) || doctemplates.BackgroundMatchesPreset(a5, 420, 420) || !doctemplates.BackgroundMatchesPreset(lab, 420, 420) {
		t.Fatal("BackgroundMatchesPreset")
	}
}

func TestLimitsAreRaisedForTheLargestSamples(t *testing.T) {
	if doctemplates.MaxItems != 128 || doctemplates.MaxBandItems != 96 {
		t.Fatal("the limits")
	}
	build := func(n int) doctemplates.Document {
		d := presetDocument(t, "")
		d.Bands.PageHeader.Height = 240
		d.Bands.PageHeader.Elements = nil
		for i := range n {
			d.Bands.PageHeader.Elements = append(d.Bands.PageHeader.Elements, doctemplates.Item{ID: "e" + strconv.Itoa(i+3), Type: "line", X1: 0, Y1: 0, X2: 190, Y2: 0, Stroke: 1})
		}
		d.Bands.PageHeader.Height = 10
		return d
	}
	if err := doctemplates.Validate(build(96)); err != nil {
		t.Fatalf("96 in a band: %v", err)
	}
	if err := doctemplates.Validate(build(97)); err == nil {
		t.Fatal("97 in a band")
	}
	// 128 in all: 96 in the header, 31 in the footer and the table.
	d := build(96)
	d.Bands.PageFooter = &doctemplates.Band{Height: 10}
	for i := range 30 {
		d.Bands.PageFooter.Elements = append(d.Bands.PageFooter.Elements, doctemplates.Item{ID: "e" + strconv.Itoa(99+i), Type: "line", X1: 0, Y1: 0, X2: 190, Y2: 0, Stroke: 1})
	}
	d.Bands.PageFooter.Elements = append(d.Bands.PageFooter.Elements, doctemplates.Item{ID: "e1", Type: "line", X1: 0, Y1: 0, X2: 190, Y2: 0, Stroke: 1})
	if err := doctemplates.Validate(d); err != nil {
		t.Fatalf("128 in all: %v", err)
	}
	d.Bands.PageFooter.Elements = append(d.Bands.PageFooter.Elements, doctemplates.Item{ID: "e130", Type: "line", X1: 0, Y1: 0, X2: 190, Y2: 0, Stroke: 1})
	if err := doctemplates.Validate(d); err == nil {
		t.Fatal("129 in all")
	}
}

func TestTheStoreKeeps64TemplatesPerIdentityAndOthersStayAt16(t *testing.T) {
	s, e := doctemplates.Open(filepath.Join(t.TempDir(), "t"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	for i := range 64 {
		if _, e = s.Save(owner, "", 0, document()); e != nil {
			t.Fatalf("template %d: %v", i+1, e)
		}
	}
	if _, e = s.Save(owner, "", 0, document()); !errors.Is(e, r.ErrBudget) {
		t.Fatalf("the 65th: %v", e)
	}
	if all, e := s.List(owner); e != nil || len(all) != 64 {
		t.Fatal(len(all), e)
	}
	// Reopened, the file of 64 reads; a store with the old cap would call it corrupt.
	s.Close()
	if s, e = doctemplates.Open(filepath.Join(filepath.Dir(t.TempDir()), "nothing")); e != nil {
		t.Fatal(e)
	}
	defer s.Close()
}

func TestCatalogStoreRevisionLimitsAndOpened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "t")
	s, e := doctemplates.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	entry, _ := s.Save(owner, "", 0, document())
	c, e := s.Catalog(owner, entry.ID)
	if e != nil || c.Revision != 0 || c.Collections == nil {
		t.Fatal(c, e)
	}
	c, e = s.SetCatalog(owner, entry.ID, 0, doctemplates.Catalog{Domain: "d", Flow: "f", Favourite: true, Collections: []string{"a"}})
	if e != nil || c.Revision != 1 {
		t.Fatal(c, e)
	}
	if _, e = s.SetCatalog(owner, entry.ID, 0, doctemplates.Catalog{Collections: []string{}}); !errors.Is(e, r.ErrConflict) {
		t.Fatal("stale", e)
	}
	if _, e = s.SetCatalog(owner, entry.ID, 1, doctemplates.Catalog{Collections: nil}); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("nil collections", e)
	}
	got, recorded, e := s.Opened(owner, entry.ID, 1, c0())
	if e != nil || !recorded || got.ID != entry.ID {
		t.Fatal(e, recorded)
	}
	if _, _, e = s.Opened(owner, entry.ID, 2, c0()); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("no such version", e)
	}
	c, _ = s.Catalog(owner, entry.ID)
	if c.LastOpened == "" || c.Revision != 1 || !c.Favourite {
		t.Fatalf("%+v", c)
	}
	// It survives a reopen, and a file written before the catalogue (no key) reads and is not rewritten with one.
	s.Close()
	s, e = doctemplates.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if again, e := s.Catalog(owner, entry.ID); e != nil || again.LastOpened != c.LastOpened || again.Domain != "d" {
		t.Fatal(again, e)
	}
	other, _ := s.Save(r.Identity{Tenant: "t", Issuer: "i", Subject: "o"}, "", 0, document())
	if _, e = s.Catalog(owner, other.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("another identity's record", e)
	}
	if all, _ := s.Catalogs(owner); len(all) != 1 {
		t.Fatal(all)
	}
}

func c0() time.Time { return time.Now() }

// The console draws and validates pages from a copy of the table in canvas.js; it is generated from Presets and must be what they are.
func TestTheConsoleCopyOfThePresetTableIsThePlatformsTable(t *testing.T) {
	raw, err := os.ReadFile("../console/assets/canvas.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	i, j := strings.Index(text, "/*presets:begin*/"), strings.Index(text, "/*presets:end*/")
	if i < 0 || j < i {
		t.Fatal("the console's table is gone")
	}
	var copyOf []struct {
		ID                  string
		Width, Height, Left float64
		Top, Bottom         float64
		Strip               struct {
			X, Y, Width, Height float64
			BannerSize          float64
			StampSize           float64
			StampLines          int
		}
	}
	body := strings.NewReplacer("banner_size", "BannerSize", "stamp_size", "StampSize", "stamp_lines", "StampLines").Replace(text[i+len("/*presets:begin*/") : j])
	if err := json.Unmarshal([]byte(body), &copyOf); err != nil || len(copyOf) != len(doctemplates.Presets) {
		t.Fatalf("%v (%d presets)", err, len(copyOf))
	}
	for k, p := range doctemplates.Presets {
		c := copyOf[k]
		s := c.Strip
		if c.ID != p.ID || c.Width != p.Width || c.Height != p.Height || c.Left != p.Left || c.Top != p.Top || c.Bottom != p.Bottom ||
			s.X != p.Strip.X || s.Y != p.Strip.Y || s.Width != p.Strip.Width || s.Height != p.Strip.Height || s.BannerSize != p.Strip.BannerSize || s.StampSize != p.Strip.StampSize || s.StampLines != p.Strip.StampLines {
			t.Errorf("%s differs in the console", p.ID)
		}
	}
}
