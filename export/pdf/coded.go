package pdf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/barcode"
	r "idp.local/reporting/reporting"
)

// Schema 2.1 layout (ADR 0005, S03-M): barcodes, QR codes, images and the issue number. Go encodes (export/barcode), places every
// module and every picture, and checks the fit; Typst draws what it is told: filled rectangles for the modules of a code and
// an image at a rectangle. A code that would need modules narrower than its symbology allows, a bar lower than a scanner needs
// or a box too small for its symbol is refused, never shrunk.

// numbering says what the issue number is in this layout.
type numbering int

const (
	// numberPreview: every preview prints and encodes the placeholder barcode.PreviewNumber, so that no preview carries a value that looks like a number.
	numberPreview numbering = iota
	// numberWorst: the pre-issue check, which proves the fit for every legal number: a text with the widest number of 32
	// characters at its size, a Code 128 with the most modules 32 such characters can need, a QR code with 32 bytes.
	numberWorst
	// numberIssue: the product's number.
	numberIssue
)

// coded is what a layout of a 2.1 document needs besides its values: how the issue number is set, the number itself when
// issuing, and the bytes of the image each image element draws, by element id. The zero value is a preview without images.
type coded struct {
	mode   numbering
	number string
	images map[string][]byte
}

const (
	// humanGap is the space, in points, between the bars of a Code 128 and the line of its value.
	humanGap = 1.0
	// barHeightMM and barHeightRatio: a bar is at least the larger of 5 mm and 15 % of the symbol's width high.
	barHeightMM    = 5.0
	barHeightRatio = 0.15
	// moduleEpsilon absorbs the rounding of the box width on the 0.1 mm grid when it is divided by the modules.
	moduleEpsilon = 1e-9
)

// RenderWith is Render for a schema 2.1 document with what it needs besides its values: the bytes of its images by element
// id. A preview prints the placeholder barcode.PreviewNumber wherever the document shows the issue number.
func (w *Writer) RenderWith(ctx context.Context, d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData, with doctemplates.Resolved) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if with.Number != "" {
		return nil, r.ErrInvalid
	}
	p, err := layOutWith(d, values, background, table, newPlainMeasurer(w.fonts), coded{mode: numberPreview, images: with.Images})
	if err != nil {
		return nil, err
	}
	return w.renderDocument(ctx, p, background, w.previews, w.cfg.Now())
}

// CheckIssueWith is CheckIssue for a schema 2.1 document: the number the product will assign is not known yet, so the
// check proves the layout for every number it may assign (1 to 32 characters of A-Z a-z 0-9 . _ -), not for one.
func (w *Writer) CheckIssueWith(ctx context.Context, d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData, with doctemplates.Resolved) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if with.Number != "" {
		return r.ErrInvalid
	}
	_, err := issueLayoutWith(d, values, background, table, newPlainMeasurer(w.fonts), coded{mode: numberWorst, images: with.Images})
	return err
}

// IssueWith is Issue for a schema 2.1 document: with.Number is the number the product reserved, printed wherever the document
// shows the issue number. It is required when the document does. The number has been proved to fit by CheckIssueWith, so that
// the layout of an issue can not fail for it.
func (w *Writer) IssueWith(ctx context.Context, d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData, with doctemplates.Resolved, stamp string, at time.Time) ([]byte, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if !validIssueTime(at) {
		return nil, 0, fmt.Errorf("pdf: the issue time is not a UTC whole second between 1970 and 9999: %w", r.ErrInvalid)
	}
	p, err := w.issuePageWith(d, values, background, table, stamp, coded{mode: numberIssue, number: with.Number, images: with.Images})
	if err != nil {
		return nil, 0, err
	}
	out, err := w.renderDocument(ctx, p, background, nil, at)
	if err != nil {
		return nil, 0, err
	}
	return out, len(p.Pages), nil
}

// ImageFits draws a probe page that holds the image and reports whether the result stays within the artifact limit and Typst can
// embed it, so that an image is refused at upload rather than failing every later preview. Its errors are those of Render.
func (w *Writer) ImageFits(ctx context.Context, data []byte) error {
	format, iw, ih, err := doctemplates.ImageInfo(data)
	if err != nil {
		return err
	}
	p, err := probePage(newPlainMeasurer(w.fonts))
	if err != nil {
		return err
	}
	name := "image-probe." + imageExtension(format)
	x, y, dw, dh := contain(60*mm, 60*mm, 90*mm, 90*mm, iw, ih)
	p.Pages[0].Images = []placedImage{{File: name, X: x, Y: y, W: dw, H: dh}}
	p.Media = map[string][]byte{name: data}
	_, err = w.renderDocument(ctx, p, nil, w.previews, w.cfg.Now())
	return err
}

// tenths is a length in millimetres on the 0.1 mm grid as a whole number of tenths.
func tenths(n float64) int { return int(math.Round(n * 10)) }

func imageExtension(format string) string {
	if format == doctemplates.FormatJPEG {
		return "jpg"
	}
	return "png"
}

// contain fits an image of iw by ih pixels into the box at (left, top) of w by h points, scaled to touch the box on one axis,
// centred on the other, aspect ratio kept; it returns the rectangle the picture covers.
func contain(left, top, w, h float64, iw, ih int) (x, y, dw, dh float64) {
	scale := math.Min(w/float64(iw), h/float64(ih))
	dw, dh = float64(iw)*scale, float64(ih)*scale
	return left + (w-dw)/2, top + (h-dh)/2, dw, dh
}

// number is the value of an issue number text of the given size, as this layout sets it. For the worst case it is the widest
// number the product may assign at this size: a letter or digit first (a number starts with one), then 31 of the widest
// characters of the alphabet; the plain advances add up, so no legal number is wider.
func (b *bandedLayout) number(place doctemplates.Detail, size float64, bold bool) (string, error) {
	switch b.c.mode {
	case numberPreview:
		return barcode.PreviewNumber, nil
	case numberIssue:
		if !r.ValidIssueNumber(b.c.number) {
			return "", r.ErrInvalid
		}
		return b.c.number, nil
	}
	widest := func(chars string) (string, error) {
		best, most := "", -1.0
		for _, c := range chars {
			w, err := b.m.width(string(c), size, bold)
			if err != nil {
				return "", placed(err, place)
			}
			if w > most {
				best, most = string(c), w
			}
		}
		return best, nil
	}
	first, err := widest(barcode.IssueNumberAlphabet[:62])
	if err != nil {
		return "", err
	}
	rest, err := widest(barcode.IssueNumberAlphabet)
	if err != nil {
		return "", err
	}
	return first + strings.Repeat(rest, barcode.MaxIssueNumber-1), nil
}

// codeValues are the value a code encodes and, for a barcode that prints its value, the line it prints. They differ only in the
// worst case of the issue number, where the symbol is the one with the most modules and the line the widest.
func (b *bandedLayout) codeValues(it doctemplates.Item, place doctemplates.Detail) (encoded, shown string, err error) {
	if it.Source == doctemplates.SourceIssueNumber {
		if b.c.mode == numberWorst {
			encoded = strings.Repeat("W", barcode.MaxIssueNumber) // 32 characters that are no digits: the most modules (barcode.IssueNumberCode128Modules)
			if it.Type == doctemplates.TypeBarcode && it.HumanReadable {
				shown, err = b.number(place, float64(it.FontSize), false)
			}
			return encoded, shown, err
		}
		n, err := b.number(place, float64(it.FontSize), false)
		return n, n, err
	}
	v, ok := b.values[it.ID]
	if !ok || !utf8.ValidString(v) {
		return "", "", r.ErrInvalid
	}
	return v, v, nil
}

// refuseCode turns a symbology's refusal of a value into the layout's: a character it cannot encode (or none), or too many.
func refuseCode(err error, place doctemplates.Detail) error {
	switch err {
	case barcode.ErrNotCovered:
		return refuse(doctemplates.ReasonNotCovered, place, "pdf: a value the symbology cannot encode")
	case barcode.ErrTooLong:
		return refuse(doctemplates.ReasonDoesNotFit, place, "pdf: a value longer than the symbology takes")
	}
	return fmt.Errorf("pdf: the barcode encoder failed: %w", r.ErrIntegrity)
}

// barcode lays out one Code 128 element whose band has its top edge at top (points). The symbol lies inside the box, with ten
// modules of quiet zone on each side; the module is the box's width divided by the modules and at least 0.25 mm; the bars are
// the box's height less the line of the value, if it prints it, and at least the larger of 5 mm and 15 % of the symbol's width.
func (b *bandedLayout) barcode(pg *pageData, name string, it doctemplates.Item, top float64) error {
	place := doctemplates.Detail{Band: name, Element: it.ID}
	encoded, shown, err := b.codeValues(it, place)
	if err != nil {
		return err
	}
	modules, err := barcode.Code128(encoded)
	if err != nil {
		return refuseCode(err, place)
	}
	total := len(modules) + 2*barcode.Code128Quiet
	// The module is at least 0.25 mm: width / total >= 0.25, in whole tenths of a millimetre.
	if 2*tenths(it.Width) < 5*total {
		return refuse(doctemplates.ReasonDoesNotFit, place, "pdf: a barcode needs modules narrower than the least a scanner reads")
	}
	left, boxTop := (bandX+it.X)*mm, top+it.Y*mm
	moduleW := it.Width * mm / float64(total)
	symbolMM := float64(len(modules)) * it.Width / float64(total)
	zone := 0.0
	if it.HumanReadable {
		zone = humanGap + (ascent+descent)*float64(it.FontSize)
	}
	barH := it.Height*mm - zone
	if barH+fitEpsilon < math.Max(barHeightMM, barHeightRatio*symbolMM)*mm {
		return refuse(doctemplates.ReasonDoesNotFit, place, "pdf: a barcode's bars are lower than a scanner needs")
	}
	for _, run := range barcode.Runs(modules) {
		pg.Bars = append(pg.Bars, rect{X: left + float64(barcode.Code128Quiet+run.Start)*moduleW, Y: boxTop, W: float64(run.Len) * moduleW, H: barH})
	}
	if !it.HumanReadable {
		return nil
	}
	// The value, centred under the bars, set and checked like a text in a box as wide as the barcode's and as high as the line.
	line := doctemplates.Item{ID: it.ID, Type: doctemplates.TypeText, X: it.X, Width: it.Width, FontSize: it.FontSize, Align: "center",
		Y: it.Y + (it.Height*mm-(ascent+descent)*float64(it.FontSize))/mm}
	glyphs, err := b.text(name, line, shown)
	if err != nil {
		return err
	}
	for _, g := range glyphs {
		g.Baseline += top
		pg.Texts = append(pg.Texts, g)
	}
	return nil
}

// qrcode lays out one QR element: a square box with four modules of quiet zone on each side, a module of the box's side divided
// by the modules (at least 0.4 mm), and one rectangle for each run of dark modules in a row.
func (b *bandedLayout) qrcode(pg *pageData, name string, it doctemplates.Item, top float64) error {
	place := doctemplates.Detail{Band: name, Element: it.ID}
	encoded, _, err := b.codeValues(it, place)
	if err != nil {
		return err
	}
	matrix, err := barcode.QR(encoded, it.ECC)
	if err != nil {
		return refuseCode(err, place)
	}
	total := len(matrix) + 2*barcode.QRQuiet
	// The module is at least 0.4 mm: size / total >= 0.4, in whole tenths of a millimetre.
	if tenths(it.Size) < 4*total {
		return refuse(doctemplates.ReasonDoesNotFit, place, "pdf: a QR code needs modules narrower than the least a scanner reads")
	}
	left, boxTop := (bandX+it.X)*mm, top+it.Y*mm
	module := it.Size * mm / float64(total)
	for row, modules := range matrix {
		for _, run := range barcode.Runs(modules) {
			pg.Bars = append(pg.Bars, rect{X: left + float64(barcode.QRQuiet+run.Start)*module, Y: boxTop + float64(barcode.QRQuiet+row)*module, W: float64(run.Len) * module, H: module})
		}
	}
	return nil
}

// image lays out one image element: the picture the caller resolved for it, checked against the digest the template names, drawn
// contained in the box (scaled to touch it on one axis, centred on the other, aspect ratio kept).
func (b *bandedLayout) image(pg *pageData, name string, it doctemplates.Item, top float64) error {
	data, ok := b.c.images[it.ID]
	if !ok || it.Image == nil {
		return r.ErrInvalid
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != it.Image.SHA256 {
		return r.ErrIntegrity
	}
	format, iw, ih, err := doctemplates.ImageInfo(data)
	if err != nil {
		return err
	}
	file := "image-" + hex.EncodeToString(sum[:8]) + "." + imageExtension(format)
	b.page.Media[file] = data
	x, y, dw, dh := contain((bandX+it.X)*mm, top+it.Y*mm, it.Width*mm, it.Height*mm, iw, ih)
	pg.Images = append(pg.Images, placedImage{File: file, X: x, Y: y, W: dw, H: dh})
	return nil
}

// probePage is an empty page of a document preview, for a render that is not a document of a template.
func probePage(m *measurer) (*page, error) {
	p := &page{Title: "Document preview", Fonts: m.families, Pages: []pageData{newPage(nil)},
		Banner: banner{Left: bannerX * mm, Top: bannerY * mm, Baseline: bannerY*mm + ascent*bannerSize, Width: bannerW * mm, Height: bannerH * mm, Size: bannerSize, Text: bannerText, Fill: bannerFill}}
	if _, err := m.width(bannerText, bannerSize, false); err != nil {
		return nil, err
	}
	return p, nil
}
