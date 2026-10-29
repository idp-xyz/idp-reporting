package pdf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/barcode"
	r "idp.local/reporting/reporting"
)

// Schema 2.1 layout tests (S03-M). They run without Typst, with a fake font, so that every width is exact.

// numberFake is a font whose characters differ in width: W is the widest letter, - the widest of the alphabet (and no number may
// start with it), m and w are wide, digits and dots narrow. Every other character is 0.5 em.
func numberFake() *measurer {
	m := fakeMetrics()
	m.plain = true
	adv := m.advance
	m.advance = func(ch rune, bold bool) (float64, bool, error) {
		switch ch {
		case 'W':
			return 0.95, true, nil
		case 'M':
			return 0.85, true, nil
		case 'm':
			return 0.9, true, nil
		case 'w':
			return 0.8, true, nil
		case '-':
			return 1.2, true, nil
		case '.':
			return 0.3, true, nil
		case '_':
			return 0.55, true, nil
		}
		if ch >= '0' && ch <= '9' {
			return 0.6, true, nil
		}
		return adv(ch, bold)
	}
	return m
}

func codedDoc(elements ...doctemplates.Item) doctemplates.Document {
	return coded21(doctemplates.Bands{PageHeader: fixedBand(40, elements...), Detail: detailOf(twoColumns("e60", 10))})
}

// layCoded lays out a coded document and returns its first page.
func layCoded(t testing.TB, d doctemplates.Document, bound map[string]string, c coded, m *measurer) (*page, error) {
	t.Helper()
	if err := doctemplates.Validate(d); err != nil {
		t.Fatalf("the test document is not valid: %v", err)
	}
	return layOutWith(d, valuesOfCodes(d, bound), nil, groupRows(3, 0), m, c)
}

func mustLay(t testing.TB, d doctemplates.Document, bound map[string]string, c coded, m *measurer) *page {
	t.Helper()
	p, err := layCoded(t, d, bound, c, m)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return p
}

func refusedWith(t testing.TB, err error, reason, band, element string) {
	t.Helper()
	d, ok := doctemplates.DetailOf(err)
	if !errors.Is(err, r.ErrUnsupported) || !ok || d.Reason != reason || d.Band != band || d.Element != element {
		t.Errorf("%v; detail %+v, want %s in %s at %s", err, d, reason, band, element)
	}
}

func TestCode128IsDrawnAtTheGeometryOfTheADR(t *testing.T) {
	const value = "Hello-123"
	d := codedDoc(barcodeItem("e1", "literal", value, 5, 2, 100, 20, false, 0))
	p := mustLay(t, d, nil, coded{}, plainFake())
	modules, err := barcode.Code128(value)
	if err != nil {
		t.Fatal(err)
	}
	total := len(modules) + 20 // ten modules of quiet zone on each side, inside the box
	left, top := (bandX+5)*mm, (safeTop+2)*mm
	module := 100 * mm / float64(total)
	runs := barcode.Runs(modules)
	bars := p.Pages[0].Bars
	if len(bars) != len(runs) {
		t.Fatalf("%d bars for %d runs", len(bars), len(runs))
	}
	for i, run := range runs {
		b := bars[i]
		if !near(b.X, left+float64(10+run.Start)*module) || !near(b.W, float64(run.Len)*module) || !near(b.Y, top) || !near(b.H, 20*mm) {
			t.Errorf("bar %d: %+v, want x %.4f w %.4f y %.4f h %.4f", i, b, left+float64(10+run.Start)*module, float64(run.Len)*module, top, 20*mm)
		}
	}
	// The symbol starts ten modules in and ends ten modules before the right edge of the box; the module is uniform and wide enough.
	first, last := bars[0], bars[len(bars)-1]
	if !near(first.X, left+10*module) || !near(last.X+last.W, left+100*mm-10*module) || module/mm < barcode.Code128MinModule {
		t.Errorf("quiet zones: first %+v last %+v module %.4f mm", first, last, module/mm)
	}
	// No other drawing, and no text, where nothing was asked for.
	if len(p.Pages[0].Images) != 0 {
		t.Error("an image")
	}
	// The same input lays out the same bytes.
	again := mustLay(t, d, nil, coded{}, plainFake())
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Error("two layouts of the same input differ")
	}
}

func TestCode128PrintsItsValueUnderTheBarsInsideItsBox(t *testing.T) {
	d := codedDoc(barcodeItem("e1", "field", "order", 0, 0, 120, 26, true, 10))
	p := mustLay(t, d, map[string]string{"e1": "INV-0001"}, coded{}, plainFake())
	zone := humanGap + (ascent+descent)*10 // points under the bars
	bar := p.Pages[0].Bars[0]
	if !near(bar.H, 26*mm-zone) || !near(bar.Y, safeTop*mm) {
		t.Errorf("bars %+v, want height %.3f", bar, 26*mm-zone)
	}
	// The baseline is the descent above the bottom of the box; the line is centred in the box and inside it.
	bottom := (safeTop + 26) * mm
	var texts []glyph
	for _, g := range p.Pages[0].Texts {
		if near(g.Baseline, bottom-descent*10) {
			texts = append(texts, g)
		}
	}
	if len(texts) != 8 {
		t.Fatalf("%d glyphs of the value", len(texts))
	}
	if !near(texts[0].Baseline, bottom-descent*10) {
		t.Errorf("baseline %.3f, want %.3f", texts[0].Baseline, bottom-descent*10)
	}
	width := 8 * 5.0
	if !near(texts[0].X, (bandX+60)*mm-width/2) || !near(texts[7].X+texts[7].Width, (bandX+60)*mm+width/2) {
		t.Errorf("the value is not centred: %+v .. %+v", texts[0], texts[7])
	}
	// The bars end above the line, with the gap between.
	if bar.Y+bar.H > texts[0].Baseline-ascent*10+1e-9 || !near(texts[0].Baseline-ascent*10-(bar.Y+bar.H), humanGap) {
		t.Errorf("the line does not follow the bars by the gap")
	}
	// A value wider than the box less the margin does not fit as a line, as a text does not.
	d = codedDoc(barcodeItem("e1", "field", "order", 0, 0, 120, 26, true, 28))
	_, err := layCoded(t, d, map[string]string{"e1": "ABCDEFGHIJKLMNOPQRSTUVWXYZ"}, coded{}, plainFake())
	refusedWith(t, err, doctemplates.ReasonDoesNotFit, doctemplates.BandPageHeader, "e1")
}

// matrixOf rebuilds the modules of a QR code from the rectangles it was drawn with.
func matrixOf(t testing.TB, bars []rect, left, top, module float64, quiet, dim int) [][]bool {
	t.Helper()
	m := make([][]bool, dim)
	for i := range m {
		m[i] = make([]bool, dim)
	}
	for _, b := range bars {
		row := int(math.Round((b.Y-top)/module)) - quiet
		col := int(math.Round((b.X-left)/module)) - quiet
		n := int(math.Round(b.W / module))
		if !near(b.H, module) || math.Abs(b.Y-top-float64(row+quiet)*module) > 1e-6 || math.Abs(b.X-left-float64(col+quiet)*module) > 1e-6 || math.Abs(b.W-float64(n)*module) > 1e-6 {
			t.Fatalf("a rectangle that is not on the module grid: %+v", b)
		}
		for k := range n {
			m[row][col+k] = true
		}
	}
	return m
}

func TestQRIsDrawnModuleForModuleInsideItsBox(t *testing.T) {
	for _, ecc := range []string{"L", "M", "Q", "H"} {
		const value = "https://example.invalid/pay?ref=INV-2026-0001"
		d := codedDoc(qrItem("e1", "literal", value, 7, 3, 30, ecc))
		p := mustLay(t, d, nil, coded{}, plainFake())
		want, err := barcode.QR(value, ecc)
		if err != nil {
			t.Fatal(err)
		}
		total := len(want) + 8
		module := 30 * mm / float64(total)
		left, top := (bandX+7)*mm, (safeTop+3)*mm
		got := matrixOf(t, p.Pages[0].Bars, left, top, module, 4, len(want))
		for y := range want {
			for x := range want[y] {
				if got[y][x] != want[y][x] {
					t.Fatalf("%s: module (%d, %d) differs", ecc, x, y)
				}
			}
		}
		// One rectangle per run of dark modules in a row, no more.
		runs := 0
		for _, row := range want {
			runs += len(barcode.Runs(row))
		}
		if runs != len(p.Pages[0].Bars) {
			t.Errorf("%s: %d rectangles for %d runs", ecc, len(p.Pages[0].Bars), runs)
		}
		// The symbol keeps four modules of quiet zone inside the box on every side.
		for _, b := range p.Pages[0].Bars {
			if b.X < left+4*module-1e-6 || b.X+b.W > left+30*mm-4*module+1e-6 || b.Y < top+4*module-1e-6 || b.Y+b.H > top+30*mm-4*module+1e-6 {
				t.Fatalf("%s: a rectangle in the quiet zone: %+v", ecc, b)
			}
		}
	}
}

func TestCodesAreRefusedNeverShrunk(t *testing.T) {
	const band = doctemplates.BandPageHeader
	// Code 128 of 9 characters is 134 modules and 154 with the quiet zones: 0.25 mm each needs a box of 38.5 mm.
	for width, ok := range map[float64]bool{38.5: true, 38.4: false, 30: false, 100: true} {
		d := codedDoc(barcodeItem("e1", "literal", "Hello-123", 0, 0, width, 20, false, 0))
		_, err := layCoded(t, d, nil, coded{}, plainFake())
		if ok && err != nil {
			t.Errorf("a box of %.1f mm: %v", width, err)
		}
		if !ok {
			refusedWith(t, err, doctemplates.ReasonDoesNotFit, band, "e1")
		}
	}
	// A bar is at least the larger of 5 mm and 15 % of the symbol's width: 134/154 of 120 mm is 104.4 mm, and 15.67 mm.
	for height, ok := range map[float64]bool{15.7: true, 15.6: false, 5: false, 40: true} {
		d := codedDoc(barcodeItem("e1", "literal", "Hello-123", 0, 0, 120, height, false, 0))
		_, err := layCoded(t, d, nil, coded{}, plainFake())
		if ok && err != nil {
			t.Errorf("bars of %.1f mm: %v", height, err)
		}
		if !ok {
			refusedWith(t, err, doctemplates.ReasonDoesNotFit, band, "e1")
		}
	}
	// A short symbol needs 5 mm: "A" is 46 modules, 66 with the quiet zones; 17 mm wide, its symbol is 11.9 mm and 15 % of it 1.8 mm.
	d := codedDoc(barcodeItem("e1", "literal", "A", 0, 0, 17, 5, false, 0))
	if _, err := layCoded(t, d, nil, coded{}, plainFake()); err != nil {
		t.Errorf("5 mm bars of a short symbol: %v", err)
	}
	// The line of the value takes its height out of the bars: 10 pt is 1 + 14.6 pt, 5.5 mm.
	// The line of the value takes its height out of the bars, a gap of 1 pt and a line of 1.46 font sizes: 15.6 pt, 5.5 mm, at 10 pt.
	// A box of 10.6 mm leaves bars of 5.098 mm, one of 10.5 mm bars of 4.997 mm.
	d = codedDoc(barcodeItem("e1", "literal", "A", 0, 0, 17, 10.6, true, 10))
	if _, err := layCoded(t, d, nil, coded{}, plainFake()); err != nil {
		t.Errorf("a box that holds 5 mm of bars and the line: %v", err)
	}
	d = codedDoc(barcodeItem("e1", "literal", "A", 0, 0, 17, 10.5, true, 10))
	_, err := layCoded(t, d, nil, coded{}, plainFake())
	refusedWith(t, err, doctemplates.ReasonDoesNotFit, band, "e1")
	// A QR code of 11 bytes is version 1 at L: 21 modules and 29 with the quiet zone; 0.4 mm each is 11.6 mm.
	for size, ok := range map[float64]bool{11.6: true, 11.5: false, 10: false, 30: true} {
		d := codedDoc(qrItem("e1", "literal", "hello world", 0, 0, size, "L"))
		_, err := layCoded(t, d, nil, coded{}, plainFake())
		if ok && err != nil {
			t.Errorf("a QR code of %.1f mm: %v", size, err)
		}
		if !ok {
			refusedWith(t, err, doctemplates.ReasonDoesNotFit, band, "e1")
		}
	}
	// A value the symbology cannot encode is not altered: its characters are refused, an empty value too, and too long a value does not fit.
	for name, c := range map[string]struct {
		it     doctemplates.Item
		value  string
		reason string
	}{
		"a letter outside Code 128":      {barcodeItem("e1", "field", "order", 0, 0, 120, 20, false, 0), "café", doctemplates.ReasonNotCovered},
		"an ideograph in Code 128":       {barcodeItem("e1", "field", "order", 0, 0, 120, 20, false, 0), "字", doctemplates.ReasonNotCovered},
		"a control in Code 128":          {barcodeItem("e1", "field", "order", 0, 0, 120, 20, false, 0), "a\tb", doctemplates.ReasonNotCovered},
		"nothing in Code 128":            {barcodeItem("e1", "field", "order", 0, 0, 120, 20, false, 0), "", doctemplates.ReasonNotCovered},
		"65 characters in Code 128":      {barcodeItem("e1", "field", "order", 0, 0, 190, 40, false, 0), strings.Repeat("a", 65), doctemplates.ReasonDoesNotFit},
		"a control in a QR code":         {qrItem("e1", "field", "order", 0, 0, 40, "M"), "a\x01b", doctemplates.ReasonNotCovered},
		"nothing in a QR code":           {qrItem("e1", "field", "order", 0, 0, 40, "M"), "", doctemplates.ReasonNotCovered},
		"257 bytes in a QR code":         {qrItem("e1", "field", "order", 0, 0, 40, "L"), strings.Repeat("a", 257), doctemplates.ReasonDoesNotFit},
		"86 ideographs in a QR code":     {qrItem("e1", "field", "order", 0, 0, 40, "L"), strings.Repeat("字", 86), doctemplates.ReasonDoesNotFit},
		"a literal Code 128 with an acc": {barcodeItem("e1", "literal", "café", 0, 0, 120, 20, false, 0), "café", doctemplates.ReasonNotCovered},
	} {
		_, err := layCoded(t, codedDoc(c.it), map[string]string{"e1": c.value}, coded{}, plainFake())
		refusedWith(t, err, c.reason, band, "e1")
		if err != nil && strings.Contains(err.Error(), c.value) && len(c.value) > 2 {
			t.Errorf("%s: the error carries the value: %v", name, err)
		}
	}
	// The element is named wherever it is: here the second code of the document footer.
	d = coded21(doctemplates.Bands{DocumentFooter: fixedBand(30, qrItem("e1", "literal", "ok", 0, 0, 20, "M"), barcodeItem("e2", "literal", "ok", 30, 0, 10, 20, false, 0)), Detail: detailOf(twoColumns("e60", 10))})
	_, err = layCoded(t, d, nil, coded{}, plainFake())
	refusedWith(t, err, doctemplates.ReasonDoesNotFit, doctemplates.BandDocumentFooter, "e2")
}

func TestAnImageIsContainedAndCheckedAgainstItsDigest(t *testing.T) {
	wide, tall := logoPNG(t, 400, 100), logoPNG(t, 100, 300)
	for name, c := range map[string]struct {
		data       []byte
		w, h       float64 // the box in millimetres
		dw, dh     float64 // the picture in millimetres
		dx, dy     float64 // its offset in the box in millimetres
		pxW, pxH   int
		formatName string
	}{
		"a wide picture in a square box":  {wide, 36, 36, 36, 9, 0, 13.5, 400, 100, "png"},
		"a tall picture in a wide box":    {tall, 60, 30, 10, 30, 25, 0, 100, 300, "png"},
		"a picture of the box's aspect":   {logoPNG(t, 200, 100), 40, 20, 40, 20, 0, 0, 200, 100, "png"},
		"a small picture is scaled up":    {logoPNG(t, 10, 10), 30, 30, 30, 30, 0, 0, 10, 10, "png"},
		"a photograph (JPEG) in a square": {stampJPEG(t, 300, 150), 36, 36, 36, 18, 0, 9, 300, 150, "jpeg"},
	} {
		d := codedDoc(imageItem("e1", 12, 3, c.w, c.h, c.data))
		p := mustLay(t, d, nil, coded{images: map[string][]byte{"e1": c.data}}, plainFake())
		if len(p.Pages[0].Images) != 1 || len(p.Pages[0].Bars) != 0 || len(p.Media) != 1 {
			t.Fatalf("%s: %+v", name, p.Pages[0])
		}
		im := p.Pages[0].Images[0]
		left, top := (bandX+12)*mm, (safeTop+3)*mm
		if !near(im.W, c.dw*mm) || !near(im.H, c.dh*mm) || math.Abs(im.X-(left+c.dx*mm)) > 1e-9 || math.Abs(im.Y-(top+c.dy*mm)) > 1e-9 {
			t.Errorf("%s: %+v, want %.2f x %.2f mm at %.2f, %.2f", name, im, c.dw, c.dh, c.dx, c.dy)
		}
		if !bytes.Equal(p.Media[im.File], c.data) || !strings.HasSuffix(im.File, map[string]string{"png": ".png", "jpeg": ".jpg"}[c.formatName]) {
			t.Errorf("%s: the file %s", name, im.File)
		}
	}
	sum := sha256.Sum256(wide)
	d := codedDoc(imageItem("e1", 0, 0, 40, 20, wide))
	for name, c := range map[string]struct {
		images map[string][]byte
		want   error
	}{
		"no image":                 {nil, r.ErrInvalid},
		"another element's image":  {map[string][]byte{"e2": wide}, r.ErrInvalid},
		"another picture":          {map[string][]byte{"e1": tall}, r.ErrIntegrity},
		"bytes that are no image":  {map[string][]byte{"e1": []byte("not an image")}, r.ErrIntegrity},
		"the digest of other data": {map[string][]byte{"e1": append(append([]byte{}, wide...), 0)}, r.ErrIntegrity},
	} {
		_, err := layCoded(t, d, nil, coded{images: c.images}, plainFake())
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// Bytes that match the digest but are no picture are invalid, and a picture past the limit is a budget error.
	junk := []byte("\x89PNG\r\n\x1a\nnot really")
	sum = sha256.Sum256(junk)
	d.Bands.PageHeader.Elements[0].Image.SHA256 = hex.EncodeToString(sum[:])
	if _, err := layCoded(t, d, nil, coded{images: map[string][]byte{"e1": junk}}, plainFake()); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a PNG that does not decode: %v", err)
	}
	big := make([]byte, doctemplates.MaxImageBytes+1)
	sum = sha256.Sum256(big)
	d.Bands.PageHeader.Elements[0].Image.SHA256 = hex.EncodeToString(sum[:])
	if _, err := layCoded(t, d, nil, coded{images: map[string][]byte{"e1": big}}, plainFake()); !errors.Is(err, r.ErrBudget) {
		t.Errorf("an image over the limit: %v", err)
	}
}

func glyphString(p *page) string {
	var b strings.Builder
	for _, g := range p.Pages[0].Texts {
		b.WriteString(g.Text)
	}
	return b.String()
}

func TestThePreviewPrintsAPlaceholderNotANumber(t *testing.T) {
	d := codedDoc(
		numberText("e1", 0, 0, 80, 8, 10, "left"),
		barcodeItem("e2", "issue_number", "", 0, 10, 100, 20, false, 0),
		qrItem("e3", "issue_number", "", 120, 5, 30, "M"))
	p := mustLay(t, d, nil, coded{mode: numberPreview}, plainFake())
	if got := glyphString(p); !strings.Contains(got, barcode.PreviewNumber) || strings.Count(got, barcode.PreviewNumber) != 1 {
		t.Errorf("the text of the page: %q", got)
	}
	// The codes encode the placeholder, as it is.
	modules, _ := barcode.Code128(barcode.PreviewNumber)
	matrix, _ := barcode.QR(barcode.PreviewNumber, "M")
	wantRects := len(barcode.Runs(modules))
	for _, row := range matrix {
		wantRects += len(barcode.Runs(row))
	}
	if len(p.Pages[0].Bars) != wantRects {
		t.Errorf("%d rectangles, want the placeholder's %d", len(p.Pages[0].Bars), wantRects)
	}
	if barcode.PreviewNumber != "PREVIEW-NUMBER" || r.ValidIssueNumber(barcode.PreviewNumber) != true {
		t.Error("the placeholder is not the text the ADR names")
	}
	// A preview carries the number of nobody: whatever the layout is asked, the placeholder is the same.
	again := mustLay(t, d, nil, coded{mode: numberPreview, number: "SH-000001"}, plainFake())
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Error("a preview with a number laid out differently")
	}
}

func TestTheIssueShowsTheProductsNumberInEveryElement(t *testing.T) {
	d := codedDoc(
		numberText("e1", 0, 0, 80, 8, 10, "left"),
		barcodeItem("e2", "issue_number", "", 0, 10, 110, 22, true, 9),
		qrItem("e3", "issue_number", "", 120, 5, 30, "Q"))
	p := mustLay(t, d, nil, coded{mode: numberIssue, number: "SH-000001"}, plainFake())
	if got := glyphString(p); strings.Count(got, "SH-000001") != 2 || strings.Contains(got, barcode.PreviewNumber) {
		t.Errorf("the text of the page: %q", got)
	}
	modules, _ := barcode.Code128("SH-000001")
	matrix, _ := barcode.QR("SH-000001", "Q")
	want := len(barcode.Runs(modules))
	for _, row := range matrix {
		want += len(barcode.Runs(row))
	}
	if len(p.Pages[0].Bars) != want {
		t.Errorf("%d rectangles, want %d", len(p.Pages[0].Bars), want)
	}
	// A number the contract does not allow, or none, is invalid: nothing is printed as it is.
	for _, n := range []string{"", "-a", "a b", strings.Repeat("a", 33), "café", "a/b"} {
		if _, err := layCoded(t, d, nil, coded{mode: numberIssue, number: n}, plainFake()); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("number %q: %v", n, err)
		}
	}
}

// TestTheCheckProvesTheFitOfEveryLegalNumber: with the number the worst case, a template passes if and only if every legal number
// fits, in a text (the widest number at its size), a Code 128 (the most modules 32 characters can need) and a QR code (32 bytes at
// its level). The widths of the boxes are swept through the thresholds, and every number of a large set is laid out as an issue.
func TestTheCheckProvesTheFitOfEveryLegalNumber(t *testing.T) {
	m := numberFake()
	numbers := legalNumbers(t, m, 10)
	rnd := rand.New(rand.NewSource(7))
	for range 60 {
		n := 1 + rnd.Intn(32)
		var b strings.Builder
		for i := range n {
			alphabet := barcode.IssueNumberAlphabet
			if i == 0 {
				alphabet = alphabet[:62]
			}
			b.WriteByte(alphabet[rnd.Intn(len(alphabet))])
		}
		numbers = append(numbers, b.String())
	}
	// With - the widest character, the widest legal number is a letter and 31 hyphens, not 32 hyphens.
	if got, err := (&bandedLayout{m: m, c: coded{mode: numberWorst}}).number(doctemplates.Detail{}, 10, false); err != nil || got != "W"+strings.Repeat("-", 31) || !r.ValidIssueNumber(got) {
		t.Fatalf("the widest legal number is %q (%v)", got, err)
	}
	fits := func(d doctemplates.Document, c coded) bool {
		_, err := layCoded(t, d, nil, c, m)
		if err != nil && !errors.Is(err, r.ErrUnsupported) {
			t.Fatal(err)
		}
		if err != nil {
			refusedWith(t, err, doctemplates.ReasonDoesNotFit, doctemplates.BandPageHeader, "e1")
		}
		return err == nil
	}
	cases := map[string]func(width float64) doctemplates.Document{
		"a text": func(w float64) doctemplates.Document { return codedDoc(numberText("e1", 0, 0, w, 8, 10, "left")) },
		"a barcode": func(w float64) doctemplates.Document {
			return codedDoc(barcodeItem("e1", "issue_number", "", 0, 0, w, 40, false, 0))
		},
		"a barcode that prints its number": func(w float64) doctemplates.Document {
			return codedDoc(barcodeItem("e1", "issue_number", "", 0, 0, w, 40, true, 10))
		},
		"a QR code at L": func(w float64) doctemplates.Document { return codedDoc(qrItem("e1", "issue_number", "", 0, 0, w, "L")) },
		"a QR code at H": func(w float64) doctemplates.Document { return codedDoc(qrItem("e1", "issue_number", "", 0, 0, w, "H")) },
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		build := cases[name]
		// The widest box a band takes is 190 mm and a QR code's is its height, 40 mm; a fit never goes away as the box widens, so the
		// least width at which the check passes is found on the grid by halving, then checked against the numbers on both sides of it.
		most := 1900
		if strings.HasPrefix(name, "a QR code") {
			most = 400
		}
		check := func(tenthsOfMM int) bool { return fits(build(float64(tenthsOfMM)/10), coded{mode: numberWorst}) }
		if !check(most) || check(100) {
			t.Fatalf("%s: the check passes at 10 mm or does not pass at %.1f mm", name, float64(most)/10)
		}
		lo, hi := 100, most // lo does not fit, hi does
		for hi-lo > 1 {
			if mid := (lo + hi) / 2; check(mid) {
				hi = mid
			} else {
				lo = mid
			}
		}
		for _, width := range []int{lo, hi, hi + 1, most} {
			d := build(float64(width) / 10)
			all := true
			for _, n := range numbers {
				if !fits(d, coded{mode: numberIssue, number: n}) {
					all = false
					break
				}
			}
			if all != (width >= hi) {
				t.Fatalf("%s at %.1f mm: every number fits is %v, the check's threshold is %.1f mm", name, float64(width)/10, all, float64(hi)/10)
			}
		}
	}
}

func TestACodeOfTheIssueNumberNeedsTheWorstCaseModules(t *testing.T) {
	// 407 modules of 0.25 mm: a box of 101.75 mm, which is 101.8 on the grid.
	for width, ok := range map[float64]bool{101.8: true, 101.7: false} {
		d := codedDoc(barcodeItem("e1", "issue_number", "", 0, 0, width, 40, false, 0))
		_, err := layCoded(t, d, nil, coded{mode: numberWorst}, plainFake())
		if ok && err != nil {
			t.Errorf("%.1f mm: %v", width, err)
		}
		if !ok {
			refusedWith(t, err, doctemplates.ReasonDoesNotFit, doctemplates.BandPageHeader, "e1")
		}
	}
	if worst, err := barcode.IssueNumberCode128Modules(); err != nil || worst+20 != 407 {
		t.Errorf("the worst case is %d modules (%v)", worst+20, err)
	}
	// A QR code of 32 bytes: 25 modules at L, 29 at M and Q, 33 at H; 8 more with the quiet zone, 0.4 mm each: 13.2, 14.8, 14.8 and 16.4 mm.
	for _, c := range []struct {
		ecc  string
		size float64
	}{{"L", 13.2}, {"M", 14.8}, {"Q", 14.8}, {"H", 16.4}} {
		d := codedDoc(qrItem("e1", "issue_number", "", 0, 0, c.size, c.ecc))
		if _, err := layCoded(t, d, nil, coded{mode: numberWorst}, plainFake()); err != nil {
			t.Errorf("%s at %.1f mm: %v", c.ecc, c.size, err)
		}
		d = codedDoc(qrItem("e1", "issue_number", "", 0, 0, c.size-0.1, c.ecc))
		_, err := layCoded(t, d, nil, coded{mode: numberWorst}, plainFake())
		refusedWith(t, err, doctemplates.ReasonDoesNotFit, doctemplates.BandPageHeader, "e1")
	}
}

func TestADocumentWithoutCodesLaysOutAsBefore(t *testing.T) {
	// A 2.0 document writes no key of a 2.1 page: its data.json is what it always was.
	d := banded(doctemplates.Bands{PageHeader: fixedBand(14, textItem("e1", "literal", "标题", 0, 0, 100, 9, 16, "left")), Detail: detailOf(twoColumns("e60", 10))})
	p, err := layOut(d, valuesFor(d, nil), nil, groupRows(3, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	for _, key := range []string{`"bars"`, `"images"`, `"media"`} {
		if bytes.Contains(b, []byte(key)) {
			t.Errorf("a 2.0 page writes %s", key)
		}
	}
	// A 2.1 document that uses none of the new elements lays out the same pages as the 2.0 document of the same bands.
	c := d
	c.Schema = doctemplates.SchemaCoded
	q, err := layOut(c, valuesFor(c, nil), nil, groupRows(3, 0), plainFake())
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(q)
	if !bytes.Equal(b, b2) {
		t.Error("a 2.1 document without codes lays out differently from the 2.0 document")
	}
}

func TestTheBannerTextIsNotEncodedInAnIssuedDocument(t *testing.T) {
	d := codedDoc(barcodeItem("e1", "field", "order", 0, 0, 190, 40, false, 0), qrItem("e2", "field", "order", 0, 0, 30, "L"))
	d.Bands.PageHeader.Elements[1].X = 150
	for _, el := range []string{"e1", "e2"} {
		v := map[string]string{el: "x " + bannerText}
		other := "e1"
		if el == "e1" {
			other = "e2"
		}
		v[other] = "ok"
		vals := valuesOfCodes(d, v)
		if _, err := issueLayout(d, vals, nil, groupRows(3, 0), plainFake()); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("%s: a code of the banner text was issued: %v", el, err)
		} else {
			refusedWith(t, err, doctemplates.ReasonNotCovered, doctemplates.BandPageHeader, el)
		}
	}
}

func TestTheBarcodeLibraryIsOnlyEverCalledThroughTheWrapper(t *testing.T) {
	// The wrapper is the one importer of the third-party package (ADR 0005, decision 2): this package takes modules from export/barcode.
	src, err := readSources(".")
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range src {
		if strings.Contains(text, "github.com/boombuler/barcode") {
			t.Errorf("%s imports the encoder directly", name)
		}
	}
}

// readSources returns the Go files of a directory that are not tests, by name.
func readSources(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			out[e.Name()] = string(b)
		}
	}
	return out, nil
}
