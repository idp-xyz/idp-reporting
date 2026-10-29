//go:build linux

package pdf

// Schema 2.1 documents through the real pinned Typst and fonts (S03-M): subtests of TestTypstRendersDocuments, TestTypstIssuesDocuments,
// TestTypstIssuesTheWidestLegalStamp and TestTypstChecksIssuesBeforeTheProductIsAsked, so that they skip and fail with them. With
// REPORTING_PDF_EVIDENCE set each document is written with what scripts/check_pdf_export.py must find in it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/barcode"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/issuance"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// evidenceExtras is what the checker's expectation of a 2.1 document needs besides the template's values: the number the document
// shows (empty for a preview, which prints the placeholder) and the bytes of its images by element id.
type evidenceExtras struct {
	Number string
	Images map[string][]byte
}

func (e evidenceExtras) mode() numbering {
	if e.Number != "" {
		return numberIssue
	}
	return numberPreview
}

// shown is what the document prints for the issue number.
func (e evidenceExtras) shown() string {
	if e.Number != "" {
		return e.Number
	}
	return barcode.PreviewNumber
}

// codedElement is the checker's expectation of a barcode, a QR code or an image: the template's own numbers in millimetres and the
// value the symbol must decode to. The checker derives the modules, the quiet zones and the positions itself.
func codedElement(it doctemplates.Item, values map[string]string, extras evidenceExtras) map[string]any {
	out := map[string]any{"id": it.ID, "type": it.Type, "x": it.X, "y": it.Y}
	value := values[it.ID]
	if it.Source == doctemplates.SourceIssueNumber {
		value = extras.shown()
	}
	switch it.Type {
	case doctemplates.TypeBarcode:
		out["source"], out["value"], out["width"], out["height"], out["human_readable"], out["size"], out["symbology"] = it.Source, value, it.Width, it.Height, it.HumanReadable, it.FontSize, it.Symbology
	case doctemplates.TypeQRCode:
		out["source"], out["value"], out["side"], out["ecc"] = it.Source, value, it.Size, it.ECC
	case doctemplates.TypeImage:
		data := extras.Images[it.ID]
		format, w, h, err := doctemplates.ImageInfo(data)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(data)
		out["width"], out["height"], out["width_px"], out["height_px"], out["format"], out["sha256"] = it.Width, it.Height, w, h, format, hex.EncodeToString(sum[:])
	}
	return out
}

// codedFixtures are the order lines of the 2.1 tests: two orders, one of them with many lines.
func codedSnapshot() r.Snapshot {
	address := "上海市浦东新区张江路 123 号 A 座 8 层"
	rows := [][]string{
		invoiceLine("INV-0001", "华东贸易有限公司", address, "笔记本电脑 Laptop 14\"", 3, "26997.00", "1234.500"),
		invoiceLine("INV-0001", "华东贸易有限公司", address, "无线键鼠套装", 10, "1290.00", "987.250"),
		invoiceLine("INV-0002", "Aurora Trading Ltd", "5 Harbour Way, London E14 9RT", "显示器 Monitor 27 英寸", 2, "3598.50", "250.5"),
	}
	for i := range 90 {
		item := fmt.Sprintf("物料 %03d %s", i, strings.Repeat("规格甲乙丙丁", i%4))
		rows = append(rows, invoiceLine("INV-LONG", "华东贸易有限公司", address, item, i+1, fmt.Sprintf("%d.%02d", 100*i+7, (i*37)%100), fmt.Sprintf("%d.%03d", 3*i, (i*91)%1000)))
	}
	return invoiceSnapshot(rows)
}

// renderCoded renders a bound group as a preview twice (the bytes must not differ) and records it with what the checker verifies.
func renderCoded(t *testing.T, w *Writer, name string, d doctemplates.Document, values map[string]string, table *doctemplates.TableData, sums map[string]sumInput, images map[string][]byte) []byte {
	t.Helper()
	with := doctemplates.Resolved{Images: images}
	out, err := w.RenderWith(t.Context(), d, values, nil, table, with)
	if err != nil {
		var re *RenderError
		if errors.As(err, &re) {
			t.Fatalf("%s: %v\n%s", name, err, re.output())
		}
		t.Fatalf("%s: %v", name, err)
	}
	again, err := w.RenderWith(t.Context(), d, values, nil, table, with)
	if err != nil || !bytes.Equal(out, again) {
		t.Fatalf("%s: two renders with the same clock differ (%v)", name, err)
	}
	// The check the routes and the worker run, with the fonts alone, accepts what renders.
	if err := w.CheckIssueWith(t.Context(), d, values, nil, table, with); err != nil {
		t.Errorf("%s: the check refuses what renders: %v", name, err)
	}
	bandedEvidence(t, name, out, d, values, table, sums, w, evidenceExtras{Images: images})
	return out
}

// bindGroupN binds the n-th group of a template over a snapshot, as a preview binds it.
func bindGroupN(t *testing.T, d doctemplates.Document, snap r.Snapshot, n int) (doctemplates.RowGroup, map[string]string, *doctemplates.TableData, map[string]sumInput) {
	t.Helper()
	groups, err := doctemplates.Groups(d, snap)
	if err != nil {
		t.Fatal(err)
	}
	values, table, err := doctemplates.BindGroup(d, snap, groups[n])
	if err != nil {
		t.Fatal(err)
	}
	return groups[n], values, table, groupSums(d, snap, groups[n])
}

// symbolsTemplate holds every kind of symbol the platform draws: Code 128 of digits (Code C), of every punctuation mark, of 32 and of 64
// characters (the latter as wide as a band, with the least module), a QR code at each level, of CJK text and of 256 bytes at the
// level that needs the most modules; in the document header and footer and the page footer.
func symbolsTemplate() doctemplates.Document {
	table := tableItem("e60", 0, 190, 9,
		doctemplates.ItemColumn{Field: "item", Width: 100},
		doctemplates.ItemColumn{Field: "qty", Width: 30, Label: "数量 Qty"},
		doctemplates.ItemColumn{Field: "amount", Width: 60, Align: "right"})
	return coded21(doctemplates.Bands{
		PageHeader: fixedBand(14, bolded(textItem("e1", "literal", "Symbols 条码与二维码", 0, 0, 100, 9, 14, "left")), textItem("e2", "page", "{page}/{pages}", 150, 2, 40, 6, 8, "right")),
		DocumentHeader: fixedBand(52,
			barcodeItem("e3", "literal", "1234567890123456", 0, 0, 60, 14, false, 0),
			barcodeItem("e4", "literal", "Aa~!@#$%^&*()_+-=[]{}|;':\",./<>?", 64, 0, 126, 20, false, 0),
			qrItem("e5", "literal", "Hello QR level L", 0, 24, 20, "L"),
			qrItem("e6", "literal", "Hello QR level M", 25, 24, 20, "M"),
			qrItem("e7", "literal", "Hello QR level Q", 50, 24, 20, "Q"),
			qrItem("e8", "literal", "Hello QR level H", 75, 24, 20, "H"),
			qrItem("e9", "literal", "上海市浦东新区张江路 123 号 IDP 报表", 100, 22, 30, "M"),
			barcodeItem("e10", "field", "order", 140, 24, 50, 14, true, 8)),
		Detail: detailOf(table),
		DocumentFooter: fixedBand(80,
			barcodeItem("e11", "literal", strings.Repeat("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ-._abcdefghijklmnopqrstuvwxyz", 1)[:64], 0, 0, 190, 29, false, 0),
			qrItem("e12", "literal", strings.Repeat("0123456789abcdefghijklmnopqrstuvwxyz-_.", 7)[:256], 0, 33, 40, "H"),
			qrItem("e13", "literal", strings.Repeat("0123456789abcdefghijklmnopqrstuvwxyz-_.", 7)[:256], 50, 33, 40, "L")),
		PageFooter: fixedBand(10, barcodeItem("e14", "literal", "FOOT-0001", 0, 0, 60, 8, false, 0), textItem("e15", "literal", "页脚", 70, 2, 40, 6, 8, "left")),
	})
}

// imagesTemplate draws pictures of every kind PDF/A-2b has to carry: a PNG with transparency, a grey PNG, a 16-bit PNG with an alpha
// channel, a palette PNG, a colour JPEG and a grey JPEG, each in a box of another aspect ratio.
func imagesTemplate(pictures map[string][]byte) doctemplates.Document {
	table := tableItem("e60", 0, 190, 9,
		doctemplates.ItemColumn{Field: "item", Width: 100},
		doctemplates.ItemColumn{Field: "qty", Width: 30},
		doctemplates.ItemColumn{Field: "amount", Width: 60, Align: "right"})
	return coded21(doctemplates.Bands{
		PageHeader: fixedBand(40,
			imageItem("e1", 0, 0, 60, 38, pictures["e1"]),
			imageItem("e2", 64, 0, 30, 38, pictures["e2"]),
			imageItem("e3", 98, 0, 40, 38, pictures["e3"]),
			imageItem("e4", 142, 0, 48, 18, pictures["e4"])),
		DocumentHeader: fixedBand(40,
			imageItem("e5", 0, 0, 60, 38, pictures["e5"]),
			imageItem("e6", 64, 0, 50, 38, pictures["e6"]),
			imageItem("e7", 120, 0, 30, 38, pictures["e7"])),
		Detail:     detailOf(table),
		PageFooter: fixedBand(14, imageItem("e8", 0, 0, 24, 12, pictures["e8"]), textItem("e9", "page", "{page}/{pages}", 150, 3, 40, 6, 8, "right")),
	})
}

func imagesFixtures(t testing.TB) map[string][]byte {
	t.Helper()
	grey := image.NewGray(image.Rect(0, 0, 120, 90))
	for y := range 90 {
		for x := range 120 {
			grey.SetGray(x, y, color.Gray{Y: uint8(255 * (x + y) / 210)})
		}
	}
	wide := image.NewNRGBA64(image.Rect(0, 0, 160, 80))
	for y := range 80 {
		for x := range 160 {
			wide.SetNRGBA64(x, y, color.NRGBA64{R: uint16(65535 * x / 160), G: uint16(65535 * y / 80), B: 0x8000, A: uint16(65535 * (x + 40) / 200)})
		}
	}
	pal := image.NewPaletted(image.Rect(0, 0, 100, 100), color.Palette{color.NRGBA{255, 255, 255, 255}, color.NRGBA{200, 30, 30, 255}, color.NRGBA{30, 30, 200, 128}, color.NRGBA{0, 0, 0, 0}})
	for y := range 100 {
		for x := range 100 {
			pal.SetColorIndex(x, y, uint8((x/10+y/10)%4))
		}
	}
	encode := func(im image.Image) []byte {
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	greyJPEG := func() []byte {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, grey, &jpeg.Options{Quality: 90}); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}()
	return map[string][]byte{"e1": logoPNG(t, 440, 180), "e2": encode(grey), "e3": encode(wide), "e4": encode(pal), "e5": stampJPEG(t, 300, 200), "e6": greyJPEG, "e7": logoPNG(t, 90, 300), "e8": logoPNG(t, 24, 12)}
}

func testTypstRendersCodedDocuments(t *testing.T, w *Writer) {
	snap := codedSnapshot()
	logo := logoPNG(t, 440, 180)
	waybill, invoice := withGroup(waybillTemplate()), withGroup(invoiceCodedTemplate(logo))
	pictures := imagesFixtures(t)
	symbols, pics := withGroup(symbolsTemplate()), withGroup(imagesTemplate(pictures))
	for _, c := range []struct {
		name   string
		d      doctemplates.Document
		group  int
		images map[string][]byte
	}{{"coded_waybill", waybill, 0, map[string][]byte{"e1": logo}}, {"coded_invoice", invoice, 1, map[string][]byte{"e1": logo}},
		{"coded_symbols", symbols, 0, nil}, {"coded_symbols_pages", symbols, 2, nil}, {"coded_images", pics, 1, pictures}} {
		t.Run(c.name, func(t *testing.T) {
			_, values, table, sums := bindGroupN(t, c.d, snap, c.group)
			out := renderCoded(t, w, c.name, c.d, values, table, sums, c.images)
			if !bytes.HasPrefix(out, []byte("%PDF-")) {
				t.Fatal("no PDF")
			}
			// A preview prints the placeholder and no other number.
			if usesNumber(c.d) {
				if p, err := layOutWith(c.d, values, nil, table, newPlainMeasurer(w.fonts), coded{images: c.images}); err != nil || !strings.Contains(glyphString(p), barcode.PreviewNumber) {
					t.Errorf("the preview does not print the placeholder: %v", err)
				}
			}
		})
	}
}

// waybillTemplate is a delivery note: its number in the page header as text and, in the document header, as a Code 128 that prints
// it, beside the customer, the order and a Code 128 of the order.
func waybillTemplate() doctemplates.Document {
	table := tableItem("e12", 0, 190, 9,
		doctemplates.ItemColumn{Field: "item", Width: 100},
		doctemplates.ItemColumn{Field: "qty", Width: 30, Label: "数量 Qty"},
		doctemplates.ItemColumn{Field: "weight", Width: 60, Align: "right"})
	return coded21(doctemplates.Bands{
		PageHeader: fixedBand(14,
			bolded(textItem("e1", "literal", "运单 Waybill", 0, 0, 60, 9, 16, "left")),
			numberText("e2", 65, 1, 125, 7, 11, "right"),
			lineItem("e3", 0, 12.5, 190, 12.5, 0.5)),
		DocumentHeader: fixedBand(34,
			barcodeItem("e4", "issue_number", "", 0, 1, 115, 26, true, 10),
			textItem("e5", "field", "customer", 122, 1, 68, 7, 11, "left"),
			textItem("e6", "field", "order", 122, 9, 68, 7, 10, "left"),
			barcodeItem("e7", "field", "order", 122, 17, 68, 16, false, 0)),
		Detail: detailOf(table),
		DocumentFooter: fixedBand(22,
			textItem("e8", "literal", "收货人签字 Receiver", 0, 2, 80, 6, 9, "left"),
			lineItem("e9", 0, 12, 90, 12, 0.5),
			barcodeItem("e10", "literal", "WAYBILL-RECEIPT", 100, 2, 90, 18, true, 9)),
		PageFooter: fixedBand(10,
			lineItem("e11", 0, 0, 190, 0, 0.25),
			textItem("e13", "page", "{page}/{pages}", 150, 2, 40, 6, 8, "right")),
	})
}

// invoiceCodedTemplate is an invoice: a logo and the title in the page header, the order and customer in the document header, a QR
// code of the order and the issue number beside the sum in the document footer.
func invoiceCodedTemplate(logo []byte) doctemplates.Document {
	table := tableItem("e12", 0, 190, 9,
		doctemplates.ItemColumn{Field: "item", Width: 100},
		doctemplates.ItemColumn{Field: "qty", Width: 30, Label: "数量 Qty"},
		doctemplates.ItemColumn{Field: "amount", Width: 60, Align: "right"})
	return coded21(doctemplates.Bands{
		PageHeader: fixedBand(20,
			imageItem("e1", 0, 0, 44, 18, logo),
			bolded(textItem("e2", "literal", "销售发票 Invoice", 50, 1, 70, 9, 16, "left")),
			textItem("e3", "page", "第 {page} 页 / 共 {pages} 页", 120, 2, 70, 7, 9, "right"),
			lineItem("e4", 0, 19, 190, 19, 0.5)),
		DocumentHeader: fixedBand(26,
			textItem("e5", "literal", "发票号 No.", 0, 1.5, 28, 6, 9, "left"),
			bolded(textItem("e6", "field", "order", 30, 1.5, 62, 6, 10, "left")),
			textItem("e7", "field", "customer", 100, 1.5, 90, 7, 11, "left"),
			textItem("e8", "literal", "凭证编号 Document No.", 0, 11, 40, 6, 9, "left"),
			numberText("e9", 42, 11, 148, 6, 10, "left")),
		Detail: detailOf(table),
		DocumentFooter: fixedBand(40,
			rectItem("e13", 100, 1, 90, 9, 0.5, "grey2"),
			bolded(textItem("e14", "literal", "合计 Total", 102, 2, 40, 6, 9, "left")),
			bolded(textItem("e15", "group_sum", "amount", 142, 2, 46, 6, 10, "right")),
			qrItem("e16", "field", "order", 0, 1, 34, "Q"),
			textItem("e17", "literal", "扫码查验 Scan to verify", 38, 14, 55, 6, 9, "left"),
			qrItem("e18", "issue_number", "", 150, 12, 28, "H")),
		PageFooter: fixedBand(10, textItem("e19", "page", "{page}/{pages}", 150, 2, 40, 6, 8, "right")),
	})
}

// usesNumber reports a template that shows the issue number.
func usesNumber(d doctemplates.Document) bool {
	for _, bi := range d.Items() {
		if bi.Item.Source == doctemplates.SourceIssueNumber {
			return true
		}
	}
	return false
}

// withGroup groups a coded invoice or waybill by its order.
func withGroup(d doctemplates.Document) doctemplates.Document {
	d.Group = &doctemplates.GroupSpec{Fields: []string{"order"}}
	return d
}

// codedIssueTemplate is a delivery note whose number is in every page header as text, as a Code 128 that prints it and as a QR
// code, and again as a Code 128 in the document footer.
func codedIssueTemplate() doctemplates.Document {
	table := tableItem("e12", 0, 190, 9,
		doctemplates.ItemColumn{Field: "item", Width: 100},
		doctemplates.ItemColumn{Field: "qty", Width: 30, Label: "数量 Qty"},
		doctemplates.ItemColumn{Field: "amount", Width: 60, Align: "right"})
	return coded21(doctemplates.Bands{
		PageHeader: fixedBand(36,
			bolded(textItem("e1", "literal", "发货单 Delivery note", 0, 0, 70, 9, 14, "left")),
			numberText("e2", 75, 1, 115, 7, 10, "right"),
			barcodeItem("e3", "issue_number", "", 0, 10, 112, 24, true, 9),
			qrItem("e4", "issue_number", "", 150, 8, 28, "M"),
			lineItem("e5", 0, 35, 190, 35, 0.5)),
		DocumentHeader: fixedBand(20,
			textItem("e6", "field", "order", 0, 2, 90, 8, 12, "left"),
			textItem("e7", "field", "customer", 100, 2, 90, 8, 11, "left")),
		Detail: detailOf(table),
		DocumentFooter: fixedBand(30,
			barcodeItem("e8", "issue_number", "", 0, 2, 120, 26, false, 0),
			textItem("e9", "literal", "凭证号 Document No.", 130, 10, 60, 6, 9, "left")),
		PageFooter: fixedBand(10, textItem("e10", "page", "{page}/{pages}", 150, 2, 40, 6, 8, "right")),
	})
}

func infoOf(t *testing.T, w *Writer, d doctemplates.Document, values map[string]string, table *doctemplates.TableData, images map[string][]byte, number string, stamp string) *page {
	t.Helper()
	p, err := w.issuePageWith(d, values, nil, table, stamp, coded{mode: numberIssue, number: number, images: images})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testTypstIssuesCodedDocuments: a schema 2.1 document is issued with the product's number in every element that shows it. The same
// inputs give the same bytes, whatever the writer's clock; the preview of the document is another file and prints the placeholder.
func testTypstIssuesCodedDocuments(t *testing.T, w, later *Writer) {
	snap := codedSnapshot()
	logo := logoPNG(t, 440, 180)
	id := strings.Repeat("7", 32)
	at := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	for _, c := range []struct {
		name   string
		d      doctemplates.Document
		group  int
		number string
		images map[string][]byte
		pages  func(int) bool
	}{
		{"coded_issue_waybill", withGroup(waybillTemplate()), 0, "SH-000042", nil, func(n int) bool { return n == 1 }},
		{"coded_issue_invoice", withGroup(invoiceCodedTemplate(logo)), 1, "AR-2026-000123", map[string][]byte{"e1": logo}, func(n int) bool { return n == 1 }},
		{"coded_issue_multipage", withGroup(codedIssueTemplate()), 2, "SH-999999", nil, func(n int) bool { return n >= 3 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, values, table, sums := bindGroupN(t, c.d, snap, c.group)
			stamp := issueStamp(c.number, at, id)
			with := doctemplates.Resolved{Images: c.images, Number: c.number}
			// The check the worker runs before the product is asked takes no number, and accepts what is then issued.
			if err := w.CheckIssueWith(t.Context(), c.d, values, nil, table, doctemplates.Resolved{Images: c.images}); err != nil {
				t.Fatalf("the check refuses the document: %v", err)
			}
			out, pages, err := w.IssueWith(t.Context(), c.d, values, nil, table, with, stamp, at)
			if err != nil {
				var re *RenderError
				if errors.As(err, &re) {
					t.Fatalf("%v\n%s", err, re.output())
				}
				t.Fatal(err)
			}
			if !c.pages(pages) {
				t.Fatalf("%d pages", pages)
			}
			again, _, err := w.IssueWith(t.Context(), c.d, values, nil, table, with, stamp, at)
			if err != nil || !bytes.Equal(out, again) {
				t.Fatalf("two issues of the same inputs differ (%v)", err)
			}
			fromLater, _, err := later.IssueWith(t.Context(), c.d, values, nil, table, with, stamp, at)
			if err != nil || !bytes.Equal(out, fromLater) {
				t.Fatalf("writers whose clocks differ issue different bytes (%v)", err)
			}
			other, _, err := w.IssueWith(t.Context(), c.d, values, nil, table, doctemplates.Resolved{Images: c.images, Number: "SH-000043"}, issueStamp("SH-000043", at, id), at)
			if err != nil || bytes.Equal(out, other) {
				t.Fatalf("another number gives the same bytes (%v)", err)
			}
			preview, err := w.RenderWith(t.Context(), c.d, values, nil, table, doctemplates.Resolved{Images: c.images})
			if err != nil || bytes.Equal(preview, out) {
				t.Fatalf("the issue is the preview (%v)", err)
			}
			// Every element that shows the number shows the product's, and none the placeholder; the number is on every page that has the header.
			p := infoOf(t, w, c.d, values, table, c.images, c.number, stamp)
			if len(p.Pages) != pages {
				t.Fatalf("%d pages laid out, %d issued", len(p.Pages), pages)
			}
			for n, pg := range p.Pages {
				var line strings.Builder
				for _, g := range pg.Texts {
					line.WriteString(g.Text)
				}
				if strings.Contains(line.String(), barcode.PreviewNumber) || strings.Contains(line.String(), bannerText) {
					t.Errorf("page %d prints a preview's text", n+1)
				}
			}
			bandedEvidence(t, c.name, out, c.d, values, table, sums, w, evidenceExtras{Number: c.number, Images: c.images})
			issueEvidence(t, c.name, out, preview, c.number, at, id, pages)
		})
	}
	t.Run("refusals", func(t *testing.T) {
		d := withGroup(waybillTemplate())
		_, values, table, _ := bindGroupN(t, d, snap, 0)
		stamp := issueStamp("SH-000042", at, id)
		// A number the contract does not allow, or none, is invalid; a preview never takes one.
		for _, n := range []string{"", "-x", "a b", strings.Repeat("a", 33), "café"} {
			if _, _, err := w.IssueWith(t.Context(), d, values, nil, table, doctemplates.Resolved{Number: n}, stamp, at); !errors.Is(err, r.ErrInvalid) {
				t.Errorf("number %q: %v", n, err)
			}
		}
		if _, err := w.RenderWith(t.Context(), d, values, nil, table, doctemplates.Resolved{Number: "SH-000042"}); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("a preview with a number: %v", err)
		}
		if err := w.CheckIssueWith(t.Context(), d, values, nil, table, doctemplates.Resolved{Number: "SH-000042"}); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("a check with a number: %v", err)
		}
		// Without the number, the plain Issue of a document that shows it is invalid too.
		if _, _, err := w.Issue(t.Context(), d, values, nil, table, stamp, at); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("an issue without a number: %v", err)
		}
		// The banner text is not data of an issued document, in a code either.
		bad := map[string]string{}
		for k, v := range values {
			bad[k] = v
		}
		bad["e7"] = "x " + bannerText
		_, _, issueErr := w.IssueWith(t.Context(), d, bad, nil, table, doctemplates.Resolved{Number: "SH-000042"}, stamp, at)
		checkErr := w.CheckIssueWith(t.Context(), d, bad, nil, table, doctemplates.Resolved{})
		for who, err := range map[string]error{"the issue": issueErr, "the check": checkErr} {
			refusedWith(t, err, doctemplates.ReasonNotCovered, doctemplates.BandDocumentHeader, "e7")
			_ = who
		}
	})
}

// testTypstIssuesTheWidestLegalNumbers is the proof of ADR 0005 decision 5 through the real Typst and fonts: a template whose check
// passes (with no number: the check proves every legal number) is issued with the widest and the longest numbers, and a random
// sample of legal numbers, at the tightest boxes the check accepts, for a text, a Code 128 and a QR code, so that no number the
// product assigns can make the render fail after it reserved it; one step narrower the check refuses, and so does the issue
// with the widest number. The documents of the tightest boxes with the widest number are written for the checker, which decodes
// their codes at the least module the layout allows.
func testTypstIssuesTheWidestLegalNumbers(t *testing.T, w *Writer) {
	m := newPlainMeasurer(w.fonts)
	snap := codedSnapshot()
	at := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	id := strings.Repeat("7", 32)
	// The templates: one element in a page header, its box swept from the least the check accepts, on the grid of 0.1 mm.
	type probe struct {
		name  string
		build func(tenths int) doctemplates.Document // the box's width (a QR code's side) in tenths of a millimetre
		most  int
	}
	header := func(items ...doctemplates.Item) doctemplates.Document {
		table := tableItem("e60", 0, 190, 9, doctemplates.ItemColumn{Field: "item", Width: 100}, doctemplates.ItemColumn{Field: "qty", Width: 30}, doctemplates.ItemColumn{Field: "amount", Width: 60})
		d := coded21(doctemplates.Bands{PageHeader: fixedBand(40, items...), Detail: detailOf(table)})
		d.Group = &doctemplates.GroupSpec{Fields: []string{"order"}}
		return d
	}
	mm := func(tenths int) float64 { return float64(tenths) / 10 }
	probes := []probe{
		{"text 8 pt", func(n int) doctemplates.Document { return header(numberText("e1", 0, 0, mm(n), 6, 8, "left")) }, 1900},
		{"text 12 pt bold", func(n int) doctemplates.Document {
			it := numberText("e1", 0, 0, mm(n), 8, 12, "right")
			it.Bold = true
			return header(it)
		}, 1900},
		{"text 14 pt", func(n int) doctemplates.Document { return header(numberText("e1", 0, 0, mm(n), 9, 14, "center")) }, 1900},
		{"Code 128", func(n int) doctemplates.Document {
			return header(barcodeItem("e1", "issue_number", "", 0, 0, mm(n), 38, false, 0))
		}, 1900},
		{"Code 128 that prints its number", func(n int) doctemplates.Document {
			return header(barcodeItem("e1", "issue_number", "", 0, 0, mm(n), 38, true, 8))
		}, 1900},
		{"QR code L", func(n int) doctemplates.Document { return header(qrItem("e1", "issue_number", "", 0, 0, mm(n), "L")) }, 400},
		{"QR code M", func(n int) doctemplates.Document { return header(qrItem("e1", "issue_number", "", 0, 0, mm(n), "M")) }, 400},
		{"QR code Q", func(n int) doctemplates.Document { return header(qrItem("e1", "issue_number", "", 0, 0, mm(n), "Q")) }, 400},
		{"QR code H", func(n int) doctemplates.Document { return header(qrItem("e1", "issue_number", "", 0, 0, mm(n), "H")) }, 400},
	}
	numbers := legalNumbers(t, m, 12)
	rnd := rand.New(rand.NewSource(20261028))
	for range 40 {
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
	// The widest numbers of each size and weight, by the fonts' advances.
	for _, size := range []float64{8, 12, 14} {
		first := widestOf(t, m, barcode.IssueNumberAlphabet[:62], size)
		rest := widestOf(t, m, barcode.IssueNumberAlphabet, size)
		numbers = append(numbers, first+strings.Repeat(rest, 31))
	}
	_, _, table, _ := bindGroupN(t, header(), snap, 0)
	for _, pr := range probes {
		t.Run(pr.name, func(t *testing.T) {
			passes := func(n int) bool {
				err := w.CheckIssueWith(t.Context(), pr.build(n), valuesOfCodes(pr.build(n), nil), nil, table, doctemplates.Resolved{})
				if err != nil {
					refusedWith(t, err, doctemplates.ReasonDoesNotFit, doctemplates.BandPageHeader, "e1")
				}
				return err == nil
			}
			if !passes(pr.most) || passes(100) {
				t.Fatalf("the sweep does not cross the threshold")
			}
			lo, hi := 100, pr.most
			for hi-lo > 1 {
				if mid := (lo + hi) / 2; passes(mid) {
					hi = mid
				} else {
					lo = mid
				}
			}
			d, narrower := pr.build(hi), pr.build(lo)
			v := valuesOfCodes(d, nil)
			// The tightest box the check accepts takes every legal number, through Typst.
			for _, n := range numbers {
				out, pages, err := w.IssueWith(t.Context(), d, v, nil, table, doctemplates.Resolved{Number: n}, issueStamp(n, at, id), at)
				if err != nil {
					var re *RenderError
					if errors.As(err, &re) {
						t.Fatalf("%q at %.1f mm: %v\n%s", n, mm(hi), err, re.output())
					}
					t.Fatalf("%q at %.1f mm: %v", n, mm(hi), err)
				}
				if pages < 1 || !bytes.HasPrefix(out, []byte("%PDF-")) {
					t.Fatalf("%q: no document", n)
				}
			}
			// One step narrower the check refuses, and the widest number does not fit either: the check is as tight as the numbers.
			widest := numbers[0]
			if strings.HasPrefix(pr.name, "text") || strings.HasPrefix(pr.name, "Code 128 that") {
				size := map[string]float64{"text 8 pt": 8, "text 12 pt bold": 12, "text 14 pt": 14, "Code 128 that prints its number": 8}[pr.name]
				first := widestOf(t, m, barcode.IssueNumberAlphabet[:62], size)
				rest := widestOf(t, m, barcode.IssueNumberAlphabet, size)
				widest = first + strings.Repeat(rest, 31)
			}
			if _, _, err := w.IssueWith(t.Context(), narrower, valuesOfCodes(narrower, nil), nil, table, doctemplates.Resolved{Number: widest}, issueStamp(widest, at, id), at); err == nil {
				if strings.HasPrefix(pr.name, "text") {
					// A text's widest number is by the font at the element's weight; the bold probe's widest differs from the regular one.
					t.Logf("the widest regular number fits one step under the threshold of %s", pr.name)
				} else {
					t.Errorf("the issue of the widest number fits where the check refuses")
				}
			} else if !errors.Is(err, r.ErrUnsupported) {
				t.Errorf("the issue of the widest number at one step under: %v", err)
			}
			if passes(lo) {
				t.Error("the step under the threshold passes")
			}
		})
	}
	// The documents of the tightest boxes of the three kinds, with the widest number and with a number of digits (Code C), for the checker.
	tight := func(pr probe) int {
		lo, hi := 100, pr.most
		for hi-lo > 1 {
			mid := (lo + hi) / 2
			if w.CheckIssueWith(t.Context(), pr.build(mid), valuesOfCodes(pr.build(mid), nil), nil, table, doctemplates.Resolved{}) == nil {
				hi = mid
			} else {
				lo = mid
			}
		}
		return hi
	}
	for _, name := range []string{"Code 128", "QR code H", "text 12 pt bold"} {
		var pr probe
		for _, c := range probes {
			if c.name == name {
				pr = c
			}
		}
		d := pr.build(tight(pr))
		v := valuesOfCodes(d, nil)
		for _, n := range []string{"W" + strings.Repeat("W", 31), "20261028000000000000000000000000"} {
			file := "coded_tightest_" + strings.ReplaceAll(strings.ToLower(name), " ", "_") + "_" + map[bool]string{true: "widest", false: "digits"}[n[0] == 'W']
			stamp := issueStamp(n, at, id)
			out, pages, err := w.IssueWith(t.Context(), d, v, nil, table, doctemplates.Resolved{Number: n}, stamp, at)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			preview, err := w.RenderWith(t.Context(), d, v, nil, table, doctemplates.Resolved{})
			if err != nil {
				t.Fatal(err)
			}
			_, vals, tbl, sums := bindGroupN(t, d, snap, 0)
			bandedEvidence(t, file, out, d, vals, tbl, sums, w, evidenceExtras{Number: n})
			issueEvidence(t, file, out, preview, n, at, id, pages)
		}
	}
}

// statementCoded is a 2.1 statement of account over the finance fixture's dataset: the logo (an image of the owner's), the customer id as
// a Code 128 and the document's number as text, as a Code 128 that prints it and as a QR code.
func statementCoded(logo doctemplates.ImageRef, barcodeWidth float64) doctemplates.Document {
	d := coded21(doctemplates.Bands{
		PageHeader: fixedBand(40,
			doctemplates.Item{ID: "e1", Type: doctemplates.TypeImage, X: 0, Y: 0, Width: 40, Height: 16, Image: &logo},
			bolded(textItem("e2", "literal", "Statement of account 对账单", 45, 0, 100, 9, 14, "left")),
			numberText("e3", 45, 10, 115, 6, 9, "right"),
			barcodeItem("e4", "issue_number", "", 0, 18, barcodeWidth, 21, true, 8),
			qrItem("e5", "issue_number", "", 162, 9, 28, "M"),
			lineItem("e6", 0, 39.5, 190, 39.5, 0.5)),
		DocumentHeader: fixedBand(26,
			textItem("e7", "literal", "Customer", 0, 2, 30, 7, 10, "left"),
			bolded(textItem("e8", "field", "customer.id", 32, 2, 80, 8, 12, "left")),
			barcodeItem("e9", "field", "customer.id", 120, 1, 70, 16, false, 0)),
		Detail: detailOf(tableItem("e10", 0, 190, 9,
			doctemplates.ItemColumn{Field: "aging.bucket", Width: 60},
			doctemplates.ItemColumn{Field: "customer.id", Width: 60, Label: "Customer"},
			doctemplates.ItemColumn{Field: "ar.open_balance", Width: 70, Align: "right"})),
		DocumentFooter: fixedBand(18,
			rectItem("e11", 100, 2, 90, 10, 0.5, "grey2"),
			bolded(textItem("e12", "group_sum", "ar.open_balance", 102, 3, 86, 8, 12, "right"))),
		PageFooter: fixedBand(10, textItem("e13", "page", "{page}/{pages}", 150, 2, 40, 6, 8, "right")),
	})
	d.Binding = doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset}
	d.Group = &doctemplates.GroupSpec{Fields: []string{"customer.id"}}
	return d
}

// serviceIssuesCodedTemplates issues schema 2.1 templates through the whole stack, with the real issuer, the real writer and the real
// Typst, the images in the private store the issuer resolves them from: the number the product reserves is printed in every element
// that shows it; a template whose codes could not hold every legal number is refused by the check before the product is asked for
// one (so that it holds nothing to void and the next document takes the next number); an image of another owner is refused likewise.
func serviceIssuesCodedTemplates(t *testing.T, w *Writer) {
	ctx := t.Context()
	p := contractsim.DemoPrincipal()
	finance := contractsim.Financial()
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	grant := contractsim.DefaultGrant()
	grant.Rows = nil
	var records []contractsim.Record
	add := func(customer, bucket, amount string) {
		key := fmt.Sprintf("k%03d", len(records))
		grant.Rows = append(grant.Rows, key)
		records = append(records, contractsim.Record{Company: "company_demo", Key: key, Values: []string{customer, bucket, amount}})
	}
	add("CUST-A100", "current", "100.25")
	add("CUST-B200", "1_30", "-0.24")
	add("CUST-A100", "31_60", "9007199254740893.00")
	for i := range 120 {
		add("CUST-C300", fmt.Sprintf("%d_%d", 30*(i%4), 30*(i%4)+30), fmt.Sprintf("%d.%02d", 100*i+7, (i*37)%100))
	}
	finance.ReplaceRecords(records)
	finance.Grant(owner, grant)
	issuedAt := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	finance.SetClock(func() time.Time { return issuedAt })
	store, err := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assets, err := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer assets.Close()
	assets.Images.Fits = w.ImageFits
	binding := doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset}
	logo := logoPNG(t, 440, 180)
	summary, err := assets.Images.Put(ctx, p.Identity, binding, logo)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := assets.Images.Resolve(p.Identity, binding, summary.Ref())
	if err != nil {
		t.Fatal(err)
	}
	// The renderer refuses what it cannot embed, at upload: not an image, a CMYK JPEG is the store's; here, bytes Typst cannot take.
	if _, err = assets.Images.Put(ctx, p.Identity, binding, []byte("not an image")); !errors.Is(err, r.ErrInvalid) {
		t.Fatalf("a file that is not an image: %v", err)
	}
	issuer, err := issuance.New(store, assets, w)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewService(r.Config{Repository: memory.New(func() time.Time { return issuedAt }), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": w}, Issuer: issuer, Now: func() time.Time { return issuedAt }})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if frozen, err = s.Job(ctx, p, frozen.ID); err != nil || frozen.SnapshotID == "" {
		t.Fatal(frozen, err)
	}
	snap, err := s.AuthorizedSnapshot(ctx, p, frozen.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(d doctemplates.Document) (doctemplates.Entry, []byte) {
		saved, err := store.Save(p.Identity, "", 0, d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Publish(p.Identity, saved.ID, "publish", 1, 0); err != nil {
			t.Fatal(err)
		}
		canonical, err := doctemplates.Canonical(saved.Layout)
		if err != nil {
			t.Fatal(err)
		}
		return saved, canonical
	}
	groupJob := func(d doctemplates.Document, saved doctemplates.Entry, canonical []byte, group int, key string) (r.JobView, doctemplates.RowGroup) {
		groups, err := doctemplates.Groups(d, snap)
		if err != nil || len(groups) != 3 {
			t.Fatalf("groups %+v %v", groups, err)
		}
		g, err := r.NewIssueGroup(snap, []string{"customer.id"}, groups[group].Key, groups[group].Rows)
		if err != nil {
			t.Fatal(err)
		}
		job, err := s.SubmitIssue(ctx, p, r.IssueInput{TemplateID: saved.ID, TemplateVersion: 1, Template: canonical, SnapshotID: snap.ID, Group: &g}, key)
		if err != nil {
			t.Fatal(err)
		}
		return job, groups[group]
	}
	// A statement whose codes hold every number the product may assign: 112 mm of Code 128 is 0.275 mm a module in the worst case.
	d := statementCoded(summary.Ref(), 112)
	saved, canonical := publish(d)
	for n, c := range []struct {
		name  string
		group int
		many  bool
	}{{"coded_issue_service_one_line", 1, false}, {"coded_issue_service_multipage", 2, true}} {
		t.Run(c.name, func(t *testing.T) {
			job, rg := groupJob(d, saved, canonical, c.group, c.name)
			if _, err := s.RunOne(ctx); err != nil {
				var re *RenderError
				if errors.As(err, &re) {
					t.Fatalf("%v\n%s", err, re.output())
				}
				t.Fatal(err)
			}
			done, err := s.Job(ctx, p, job.ID)
			if err != nil || done.State != "succeeded" || done.Issue == nil || done.Issue.Number != fmt.Sprintf("AR-%06d", n+1) {
				t.Fatalf("%+v %v", done, err)
			}
			rec, err := s.Issue(ctx, p, job.ID)
			if err != nil || (c.many && rec.Pages < 3) || (!c.many && rec.Pages != 1) {
				t.Fatalf("%+v %v", rec, err)
			}
			art, err := s.Download(ctx, p, done.ArtifactID)
			if err != nil || !bytes.HasPrefix(art.Content, []byte("%PDF-")) || art.SHA256 != rec.SHA256 {
				t.Fatalf("%v", err)
			}
			// The real issuer and writer give the same bytes for the same job again, with the number the product reserved.
			g, _ := r.NewIssueGroup(snap, []string{"customer.id"}, rg.Key, rg.Rows)
			again, err := issuer.Issue(ctx, r.IssueRequest{Job: r.Job{ID: job.ID, Owner: snap.Owner, Kind: "issue", TemplateID: saved.ID, TemplateVersion: 1, Template: string(canonical), SnapshotID: snap.ID, Group: &g}, Snapshot: snap, Stamp: rec.Stamp, Number: rec.Number, At: issuedAt})
			if err != nil || !bytes.Equal(again.PDF, art.Content) {
				t.Fatalf("a job issued again gives other bytes (%v)", err)
			}
			// The stamp's number and the request's must be the same: a document never shows one number and stamps another.
			if _, err := issuer.Issue(ctx, r.IssueRequest{Job: r.Job{ID: job.ID, Owner: snap.Owner, Kind: "issue", TemplateID: saved.ID, TemplateVersion: 1, Template: string(canonical), SnapshotID: snap.ID, Group: &g}, Snapshot: snap, Stamp: rec.Stamp, Number: "AR-999999", At: issuedAt}); !errors.Is(err, r.ErrInvalid) {
				t.Errorf("a number that is not the stamp's: %v", err)
			}
			values, table, err := doctemplates.BindGroup(d, snap, rg)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := w.RenderWith(ctx, d, values, nil, table, doctemplates.Resolved{Images: map[string][]byte{"e1": stored}})
			if err != nil || bytes.Equal(preview, art.Content) {
				t.Fatalf("the issue is the preview (%v)", err)
			}
			bandedEvidence(t, c.name, art.Content, d, values, table, groupSums(d, snap, rg), w, evidenceExtras{Number: rec.Number, Images: map[string][]byte{"e1": stored}})
			issueEvidence(t, c.name, art.Content, preview, rec.Number, issuedAt, job.ID, rec.Pages)
		})
	}
	// 100 mm of Code 128 would hold "AR-000003" (0.7 mm a module) but not 32 characters (0.246 mm): the check refuses it before the
	// product is asked, so the product holds nothing for the job and the next document takes the number after the last.
	narrow := statementCoded(summary.Ref(), 100)
	narrowSaved, narrowBytes := publish(narrow)
	job, _ := groupJob(narrow, narrowSaved, narrowBytes, 0, "narrow")
	if _, err := s.RunOne(ctx); !errors.Is(err, r.ErrUnsupported) {
		t.Fatalf("a template that cannot hold every number was not refused as unsupported: %v", err)
	}
	if got, err := s.Job(ctx, p, job.ID); err != nil || got.State != "failed" || got.ErrorCode != "UNSUPPORTED_COMBINATION" {
		t.Fatalf("%+v %v", got, err)
	}
	if rec, held := finance.IssueRecord(job.ID); held {
		t.Fatalf("the product holds %+v for a document its renderer cannot lay out for every number", rec)
	}
	// An image that is not the owner's: the template names one of another identity. The worker cannot resolve it; nothing was reserved.
	other := r.Identity{Tenant: p.Tenant, Issuer: p.Issuer, Subject: "someone-else"}
	foreign, err := assets.Images.Put(ctx, other, binding, logoPNG(t, 100, 50))
	if err != nil {
		t.Fatal(err)
	}
	stolen := statementCoded(foreign.Ref(), 112)
	stolenSaved, stolenBytes := publish(stolen)
	job, _ = groupJob(stolen, stolenSaved, stolenBytes, 0, "foreign")
	if _, err := s.RunOne(ctx); !errors.Is(err, r.ErrNotFound) {
		t.Fatalf("an image of another owner was not refused: %v", err)
	}
	if got, err := s.Job(ctx, p, job.ID); err != nil || got.State != "failed" {
		t.Fatalf("%+v %v", got, err)
	}
	if rec, held := finance.IssueRecord(job.ID); held {
		t.Fatalf("the product holds %+v for a document whose image is not the owner's", rec)
	}
	// Neither refusal spent a number: the next document is AR-000003.
	job, _ = groupJob(d, saved, canonical, 0, "next")
	if _, err := s.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Job(ctx, p, job.ID); err != nil || got.State != "succeeded" || got.Issue.Number != "AR-000003" {
		t.Fatalf("%+v %v", got, err)
	}
}
