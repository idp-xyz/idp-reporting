//go:build linux

package pdf

// Page presets, the 2.x overprint profile, the sample pack and the raised limits through the real pinned Typst and fonts (S06-B).
// With REPORTING_PDF_EVIDENCE set each document is written with what scripts/check_pdf_export.py must find in it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/barcode"
	r "idp.local/reporting/reporting"
)

var mediaBoxPattern = regexp.MustCompile(`/MediaBox\s*\[\s*0\s+0\s+([0-9.]+)\s+([0-9.]+)\s*\]`)

// pageSizes returns the size, in millimetres, of every page of a PDF, from its media boxes.
func pageSizes(t *testing.T, pdf []byte) [][2]float64 {
	t.Helper()
	var out [][2]float64
	for _, m := range mediaBoxPattern.FindAllSubmatch(pdf, -1) {
		w, _ := strconv.ParseFloat(string(m[1]), 64)
		h, _ := strconv.ParseFloat(string(m[2]), 64)
		out = append(out, [2]float64{w / mm, h / mm})
	}
	if len(out) == 0 {
		t.Fatal("no media box in the PDF")
	}
	return out
}

func requirePageSize(t *testing.T, name string, pdf []byte, pre doctemplates.Preset) {
	t.Helper()
	for _, s := range pageSizes(t, pdf) {
		if math.Abs(s[0]-pre.Width) > 0.01 || math.Abs(s[1]-pre.Height) > 0.01 {
			t.Fatalf("%s: a page of %.2f x %.2f mm, the preset %s is %v x %v", name, s[0], s[1], pre.ID, pre.Width, pre.Height)
		}
	}
}

// presetTemplate is an invoice-like 2.0 template sized for a preset: it spans the preset's bands, so that the table, the header texts and the
// footer sum all sit on the edges of its safe region.
func presetTemplate(pre doctemplates.Preset) doctemplates.Document {
	bw := pre.BandWidth()
	half := math.Floor(bw*5) / 10
	table := tableItem("e9", 0, bw, 8, doctemplates.ItemColumn{Field: "item", Width: math.Round((bw-30)*10) / 10}, doctemplates.ItemColumn{Field: "amount", Width: 30, Align: "right"})
	d := doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Group: &doctemplates.GroupSpec{Fields: []string{"order"}},
		Bands: &doctemplates.Bands{
			PageHeader:     fixedBand(10, bolded(textItem("e1", "literal", "发票 Invoice", 0, 0, half, 7, 10, "left")), textItem("e2", "page", "{page}/{pages}", half, 0, bw-half, 7, 8, "right")),
			DocumentHeader: fixedBand(11, textItem("e3", "field", "order", 0, 1, half, 7, 10, "left"), textItem("e4", "field", "customer", half, 1, bw-half, 7, 9, "right"), lineItem("e5", 0, 9.5, bw, 9.5, 0.5)),
			Detail:         detailOf(table),
			DocumentFooter: fixedBand(10, rectItem("e6", 0, 1, bw, 8, 0.5, "grey1"), bolded(textItem("e7", "group_sum", "amount", 1, 2, bw-2, 6, 9, "right"))),
			PageFooter:     fixedBand(8, textItem("e8", "literal", "IDP Reporting", 0, 1, bw, 6, 8, "center")),
		}}
	if pre.ID != doctemplates.DefaultPreset.ID {
		d.Page = &doctemplates.Page{Preset: pre.ID}
	}
	return d
}

func presetRows(n int) r.Snapshot {
	var rows [][]string
	for i := range n {
		rows = append(rows, invoiceLine("INV-9", "华东贸易有限公司", "上海", fmt.Sprintf("物料 %02d %s", i, strings.Repeat("甲", i%3)), i+1, fmt.Sprintf("%d.%02d", 10*i+5, i), "1"))
	}
	return invoiceSnapshot(rows)
}

func TestTypstRendersEveryPresetAtItsSizeAndIssuesWithItsStamp(t *testing.T) {
	w := typstWriter(t, nil)
	at := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	id := strings.Repeat("7", 32)
	for _, pre := range doctemplates.Presets {
		t.Run(pre.ID, func(t *testing.T) {
			d := presetTemplate(pre)
			snap := presetRows(45)
			_, values, tables, sums := bandedGroups(t, d, snap)
			out := renderBanded(t, w, "preset_"+pre.ID, d, values[0], tables[0], sums[0])
			requirePageSize(t, "preview", out, pre)
			if n := len(pageSizes(t, out)); n < 2 {
				t.Errorf("45 rows on one %s page", pre.ID)
			}
			// The issue: the stamp in the strip of every page, in the strip's lines; the same document through the check.
			number := "AR-" + strings.Repeat("W", 20)
			stamp := issueStamp(number, at, id)
			issued, pages, err := w.Issue(t.Context(), d, values[0], nil, tables[0], stamp, at)
			if err != nil || pages != len(pageSizes(t, issued)) {
				t.Fatal(err, pages)
			}
			requirePageSize(t, "issue", issued, pre)
			p, err := w.issuePage(d, values[0], nil, tables[0], stamp)
			if err != nil || len(p.Banner.Extra)+1 != pre.Strip.StampLines || p.Banner.Size != pre.Strip.StampSize {
				t.Fatalf("%+v %v", p.Banner, err)
			}
			if err := w.CheckIssue(t.Context(), d, values[0], nil, tables[0]); err != nil {
				t.Fatal(err)
			}
			preview, err := w.Render(t.Context(), d, values[0], nil, tables[0])
			if err != nil {
				t.Fatal(err)
			}
			bandedEvidence(t, "preset_"+pre.ID+"_issued", issued, d, values[0], tables[0], sums[0], w)
			issueEvidence(t, "preset_"+pre.ID+"_issued", issued, preview, number, at, id, pages)
		})
	}
}

// TestTypstTheWidestLegalStampFitsEveryPresetsStrip is the proof that an issue never fails on the layout of its stamp after the product
// reserved the number, for every preset: the widest number, record id and time the contract allows, broken as the preset breaks them,
// fit the strip with the margin, and the lines stand inside the strip.
func TestTypstTheWidestLegalStampFitsEveryPresetsStrip(t *testing.T) {
	w := typstWriter(t, nil)
	m := newPlainMeasurer(w.fonts)
	width := func(s string, size float64) float64 {
		v, err := m.width(s, size, false)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		return v
	}
	widest := func(chars string, size float64) string {
		best := ""
		for _, c := range chars {
			if best == "" || width(string(c), size) > width(best, size) {
				best = string(c)
			}
		}
		return best
	}
	latest := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	for _, pre := range doctemplates.Presets {
		s := pre.Strip
		number := strings.Repeat(widest("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-", s.StampSize), 32)
		id := strings.Repeat(widest("0123456789abcdef", s.StampSize), 32)
		if !r.ValidIssueNumber(number) {
			t.Fatal(number)
		}
		stamp := issueStamp(number, latest, id)
		lines := stampLines(stamp, s.StampLines)
		if len(lines) != s.StampLines {
			t.Errorf("%s: the widest stamp is in %d lines, the preset has %d", pre.ID, len(lines), s.StampLines)
		}
		most := 0.0
		for _, l := range lines {
			most = math.Max(most, width(l, s.StampSize))
		}
		t.Logf("%s: the widest legal stamp is %d line(s), the longest %.2f pt of %.2f pt (margin %v pt); on one line %.2f pt", pre.ID, len(lines), most, s.Width*mm, fitMargin, width(stamp, s.StampSize))
		if most > s.Width*mm-fitMargin {
			t.Errorf("%s: the widest stamp's line is %.2f pt, the strip %.2f pt less the margin", pre.ID, most, s.Width*mm)
		}
		// The banner, too, and the lines stand inside the strip, ascent above the first baseline and descent below the last.
		if b := width(bannerText, s.BannerSize); b > s.Width*mm-fitMargin {
			t.Errorf("%s: the preview banner is %.2f pt wide", pre.ID, b)
		}
		bottom := s.Y*mm + ascent*s.StampSize + float64(len(lines)-1)*stampLead*s.StampSize + descent*s.StampSize
		if bottom > (s.Y+s.Height)*mm+1e-6 || s.Y*mm+ascent*s.BannerSize+descent*s.BannerSize > (s.Y+s.Height)*mm+1e-6 {
			t.Errorf("%s: the lines run past the strip: %.2f pt of %.2f pt", pre.ID, bottom, (s.Y+s.Height)*mm)
		}
		d := presetTemplate(pre)
		_, values, tables, _ := bandedGroups(t, d, presetRows(3))
		if _, _, err := w.Issue(t.Context(), d, values[0], nil, tables[0], stamp, latest); err != nil {
			t.Errorf("%s: the widest legal stamp does not issue: %v", pre.ID, err)
		}
		// A stamp one character wider than the strip holds is refused rather than drawn out of it.
		long := stamp + strings.Repeat(widest("0123456789abcdef", s.StampSize), 40)
		if len(long) > 128 {
			long = long[:128]
		}
		if _, _, err := w.Issue(t.Context(), d, values[0], nil, tables[0], issueStamp(number, latest, strings.Repeat("W", 100)), latest); err == nil {
			t.Errorf("%s: a stamp wider than the strip is drawn", pre.ID)
		}
	}
}

func TestTypstRendersEverySampleWithItsOwnRows(t *testing.T) {
	w := typstWriter(t, nil)
	all, err := doctemplates.Samples()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		t.Run(s.Kind, func(t *testing.T) {
			snap := s.Snapshot()
			group, err := doctemplates.GroupOf(s.Document, snap, 0)
			if err != nil {
				t.Fatal(err)
			}
			values, table, err := doctemplates.BindGroup(s.Document, snap, group)
			if err != nil {
				t.Fatal(err)
			}
			pre, _ := s.Document.PagePreset()
			with := doctemplates.Resolved{}
			out, err := w.RenderWith(t.Context(), s.Document, values, nil, table, with)
			if err != nil {
				t.Fatal(err)
			}
			requirePageSize(t, s.Kind, out, pre)
			if err := w.CheckIssueWith(t.Context(), s.Document, values, nil, table, with); err != nil {
				t.Errorf("the check refuses what renders: %v", err)
			}
			bandedEvidence(t, "sample_"+strings.TrimPrefix(s.Kind, "_"), out, s.Document, values, table, groupSums(s.Document, snap, group), w)
		})
	}
}

// overprintTemplate is presetTemplate with room around it for offsets, in a preset's bands.
func overprintTemplate(pre doctemplates.Preset, x, y float64, variables bool, ref *doctemplates.BackgroundRef) doctemplates.Document {
	bw := pre.BandWidth()
	table := tableItem("e9", 6, bw-12, 8, doctemplates.ItemColumn{Field: "item", Width: math.Round((bw-42)*10) / 10}, doctemplates.ItemColumn{Field: "amount", Width: 30, Align: "right"})
	d := doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Group: &doctemplates.GroupSpec{Fields: []string{"order"}},
		Page:  &doctemplates.Page{Preset: pre.ID, Print: &doctemplates.PagePrint{OffsetX: x, OffsetY: y, VariablesOnly: variables, Background: ref}},
		Bands: &doctemplates.Bands{
			PageHeader:     fixedBand(20, bolded(textItem("e1", "literal", "发票 Invoice", 6, 6, bw/2-6, 7, 10, "left")), textItem("e2", "field", "order", bw/2, 6, bw/2-6, 7, 10, "left"), lineItem("e3", 6, 15, bw-6, 15, 0.5)),
			DocumentFooter: fixedBand(20, rectItem("e6", 6, 2, bw-12, 8, 0.5, "grey1"), bolded(textItem("e7", "group_sum", "amount", 8, 3, bw-16, 6, 9, "right"))),
			Detail:         detailOf(table),
			PageFooter:     fixedBand(20, textItem("e8", "page", "{page}/{pages}", 6, 6, 40, 6, 8, "left")),
		}}
	return d
}

func TestTypstDrawsTheOverprintProfileOfA2xTemplate(t *testing.T) {
	w := typstWriter(t, nil)
	for _, c := range []struct {
		name       string
		preset     string
		x, y       float64
		variables  bool
		background bool
	}{
		{"full_offset", "a4", 3, -2, false, false},
		{"full_background", "a4", 0, 0, false, true},
		{"full_background_offset", "a4", -4, 5, false, true},
		{"variables_offset", "a4", 3.5, 2.5, true, false},
		{"variables_background", "a4", 2, 0, true, true},
		{"landscape_background", "a4_landscape", 1, 1, false, true},
		{"label_variables", "label_100x150", 1.5, 1, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			pre, _ := doctemplates.PresetByID(c.preset)
			var png []byte
			var ref *doctemplates.BackgroundRef
			if c.background {
				png = pngOf(int(pre.Width*2), int(pre.Height*2))
				// A frame, so that the picture is ink the checker finds under the page.
				ref = refOf(png)
			}
			d := overprintTemplate(pre, c.x, c.y, c.variables, ref)
			snap := presetRows(60)
			_, values, tables, sums := bandedGroups(t, d, snap)
			drawn := png
			if c.variables {
				drawn = nil
			}
			out, err := w.Render(t.Context(), d, values[0], drawn, tables[0])
			if err != nil {
				t.Fatal(err)
			}
			again, _ := w.Render(t.Context(), d, values[0], drawn, tables[0])
			if !bytes.Equal(out, again) {
				t.Fatal("two renders differ")
			}
			requirePageSize(t, c.name, out, pre)
			if c.background && !c.variables && !bytes.Contains(out, []byte("/Subtype/Image")) && !bytes.Contains(out, []byte("/Subtype /Image")) {
				t.Log("the image object is in a compressed stream; the checker verifies it")
			}
			if err := w.CheckIssue(t.Context(), d, values[0], drawn, tables[0]); err != nil {
				t.Fatal(err)
			}
			bandedEvidence(t, "overprint_"+c.name, out, d, values[0], tables[0], sums[0], w, evidenceExtras{Background: drawn})
			stamp := issueStamp("AR-1", time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC), strings.Repeat("5", 32))
			at := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
			if _, _, err := w.Issue(t.Context(), d, values[0], drawn, tables[0], stamp, at); err != nil {
				t.Fatalf("issue: %v", err)
			}
		})
	}
}

func TestTypstTheRaisedLimitsRenderCheckAndIssueWithinTheBudget(t *testing.T) {
	w := typstWriter(t, nil)
	// 96 elements in one band, 128 in the template: the page header holds an issue-number text across the band and 95 fields and
	// literals in a grid; the document footer holds the codes of the number (a Code 128 across the band, five QR codes) and lines.
	var header []doctemplates.Item
	header = append(header, numberText("e1", 0, 0, 190, 5.5, 8, "left"))
	for i := 1; i < 96; i++ {
		col, row := (i-1)%3, (i-1)/3
		x, y := float64(col)*63.5, float64(row+1)*6.5
		id := "e" + strconv.Itoa(i+1)
		if i%2 == 0 {
			header = append(header, textItem(id, "field", "customer", x, y, 60, 5.5, 8, "left"))
		} else {
			header = append(header, textItem(id, "literal", fmt.Sprintf("字段 %02d", i), x, y, 60, 5.5, 8, "left"))
		}
	}
	footer := []doctemplates.Item{barcodeItem("e97", "issue_number", "", 0, 0, 190, 38, true, 8)}
	for i := range 5 {
		footer = append(footer, qrItem("e"+strconv.Itoa(98+i), "issue_number", "", float64(i)*30, 40, 20, "H"))
	}
	var rules []doctemplates.Item
	for i := range 25 {
		rules = append(rules, lineItem("e"+strconv.Itoa(103+i), 0, 0.2*float64(i), 190, 0.2*float64(i), 0.25*float64(1+i%4)))
	}
	d := coded21(doctemplates.Bands{
		PageHeader:     fixedBand(6, rules...),
		DocumentHeader: fixedBand(214, header...),
		Detail:         detailOf(tableItem("e128", 0, 190, 8, doctemplates.ItemColumn{Field: "item", Width: 130}, doctemplates.ItemColumn{Field: "amount", Width: 60, Align: "right"})),
		DocumentFooter: fixedBand(62, footer...),
	})
	d.Group = &doctemplates.GroupSpec{Fields: []string{"order"}}
	if err := doctemplates.Validate(d); err != nil {
		t.Fatalf("128 elements, 96 in one band: %v", err)
	}
	if len(d.Items()) != doctemplates.MaxItems || len(d.Bands.DocumentHeader.Elements) != doctemplates.MaxBandItems {
		t.Fatalf("not at the limits: %d, %d", len(d.Items()), len(d.Bands.PageHeader.Elements))
	}
	snap := presetRows(40)
	_, values, tables, sums := bandedGroups(t, d, snap)
	started := time.Now()
	out, err := w.RenderWith(t.Context(), d, values[0], nil, tables[0], doctemplates.Resolved{})
	preview := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	if err := w.CheckIssueWith(t.Context(), d, values[0], nil, tables[0], doctemplates.Resolved{}); err != nil {
		t.Fatalf("the pre-issue proof over the widest legal number: %v", err)
	}
	check := time.Since(started)
	// The widest legal number, as a text in the 190 mm box at 8 pt and in every code, issues.
	m := newPlainMeasurer(w.fonts)
	best := ""
	for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-" {
		if a, _ := m.width(string(c), 8, false); best == "" {
			best = string(c)
		} else if b, _ := m.width(best, 8, false); a > b {
			best = string(c)
		}
	}
	number := strings.Repeat(best, 32)
	at := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	started = time.Now()
	issued, pages, err := w.IssueWith(t.Context(), d, values[0], nil, tables[0], doctemplates.Resolved{Number: number}, issueStamp(number, at, strings.Repeat("7", 32)), at)
	issue := time.Since(started)
	if err != nil || pages < 1 {
		t.Fatalf("the widest legal number: %v", err)
	}
	t.Logf("128 elements, 96 in one band (S06-B): preview %v (%d bytes), pre-issue check %v, issue %v", preview, len(out), check, issue)
	if preview > 15*time.Second {
		t.Errorf("the preview took %v, the budget is 15 s", preview)
	}
	if _, err := barcode.Code128(number); err != nil {
		t.Fatal(err)
	}
	bandedEvidence(t, "limits_128", out, d, values[0], tables[0], sums[0], w)
	_ = issued
}

func sha256Sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func refOf(png []byte) *doctemplates.BackgroundRef {
	sum := sha256Sum(png)
	return &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: sum}
}
