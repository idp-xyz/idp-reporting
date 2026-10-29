package pdf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/barcode"
	r "idp.local/reporting/reporting"
)

// Fixtures of the schema 2.1 tests (S03-M): templates that use a code, an image and the issue number, and the pictures they draw.
// They are shared by the layout tests, which run without Typst, and by the Typst tests that write evidence for the checker.

func barcodeItem(id, source, v string, x, y, w, h float64, readable bool, size int) doctemplates.Item {
	it := doctemplates.Item{ID: id, Type: doctemplates.TypeBarcode, Source: source, X: x, Y: y, Width: w, Height: h, Symbology: doctemplates.SymbologyCode128, HumanReadable: readable}
	if readable {
		it.FontSize = size
	}
	switch source {
	case doctemplates.SourceLiteral:
		it.Text = v
	case doctemplates.SourceField:
		it.Field = v
	}
	return it
}

func qrItem(id, source, v string, x, y, size float64, ecc string) doctemplates.Item {
	it := doctemplates.Item{ID: id, Type: doctemplates.TypeQRCode, Source: source, X: x, Y: y, Size: size, ECC: ecc}
	switch source {
	case doctemplates.SourceLiteral:
		it.Text = v
	case doctemplates.SourceField:
		it.Field = v
	}
	return it
}

func imageItem(id string, x, y, w, h float64, data []byte) doctemplates.Item {
	sum := sha256.Sum256(data)
	return doctemplates.Item{ID: id, Type: doctemplates.TypeImage, X: x, Y: y, Width: w, Height: h, Image: &doctemplates.ImageRef{ID: strings.Repeat("a", 32), SHA256: hex.EncodeToString(sum[:])}}
}

func numberText(id string, x, y, w, h float64, size int, align string) doctemplates.Item {
	return doctemplates.Item{ID: id, Type: doctemplates.TypeText, Source: doctemplates.SourceIssueNumber, X: x, Y: y, Width: w, Height: h, FontSize: size, Align: align}
}

// coded21 turns a banded document into a schema 2.1 one.
func coded21(b doctemplates.Bands) doctemplates.Document {
	d := banded(b)
	d.Schema = doctemplates.SchemaCoded
	return d
}

// valuesOfCodes are the values bound to the texts and codes of d as BindGroup binds them: a literal as it is, the rest as given.
func valuesOfCodes(d doctemplates.Document, bound map[string]string) map[string]string {
	out := map[string]string{}
	for _, bi := range d.Items() {
		it := bi.Item
		if (it.Type != doctemplates.TypeText && it.Type != doctemplates.TypeBarcode && it.Type != doctemplates.TypeQRCode) || it.Source == doctemplates.SourceIssueNumber {
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

// logoPNG is a logo with transparency: a rounded dark blue badge with a white cross on a transparent ground, an alpha edge
// around it, w by h pixels.
func logoPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	blue := color.NRGBA{R: 0x1d, G: 0x4e, B: 0x89, A: 0xff}
	for y := range h {
		for x := range w {
			// A rounded rectangle: distance to the nearest corner centre beyond the radius is outside.
			rad := float64(min(w, h)) / 4
			cx := min(max(float64(x), rad), float64(w)-rad)
			cy := min(max(float64(y), rad), float64(h)-rad)
			dx, dy := float64(x)-cx, float64(y)-cy
			d := dx*dx + dy*dy
			switch {
			case d <= (rad-1)*(rad-1):
				im.SetNRGBA(x, y, blue)
			case d <= rad*rad:
				im.SetNRGBA(x, y, color.NRGBA{R: blue.R, G: blue.G, B: blue.B, A: 0x80})
			}
		}
	}
	bar := max(h/8, 2)
	draw.Draw(im, image.Rect(w/2-bar/2, h/5, w/2+bar/2+1, h-h/5), image.NewUniform(color.NRGBA{255, 255, 255, 255}), image.Point{}, draw.Src)
	draw.Draw(im, image.Rect(w/5, h/2-bar/2, w-w/5, h/2+bar/2+1), image.NewUniform(color.NRGBA{255, 255, 255, 255}), image.Point{}, draw.Src)
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// stampJPEG is a photographic-looking JPEG: a smooth gradient with a dark frame, w by h pixels.
func stampJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{R: uint8(40 + 160*x/w), G: uint8(90 + 100*y/h), B: uint8(200 - 120*x/w), A: 255}
			if x < 3 || y < 3 || x >= w-3 || y >= h-3 {
				c = color.RGBA{A: 255}
			}
			im.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, &jpeg.Options{Quality: 88}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// widestOf returns the widest character of chars at a size, bold or not, by the plain advances of the measurer.
func widestOf(t testing.TB, m *measurer, chars string, size float64) string {
	t.Helper()
	best, most := "", -1.0
	for _, c := range chars {
		w, err := m.width(string(c), size, false)
		if err != nil {
			t.Fatal(err)
		}
		if w > most {
			best, most = string(c), w
		}
	}
	return best
}

// legalNumbers are issue numbers that stress the layouts: the widest and the longest, the shortest, and ones whose Code 128 symbol
// switches code sets.
func legalNumbers(t testing.TB, m *measurer, size float64) []string {
	t.Helper()
	first := widestOf(t, m, barcode.IssueNumberAlphabet[:62], size)
	rest := widestOf(t, m, barcode.IssueNumberAlphabet, size)
	numbers := []string{first + strings.Repeat(rest, 31), strings.Repeat("W", 32), strings.Repeat("0", 32), strings.Repeat("a", 32), "1", "A", "0",
		"SH-000001", "AR-2026-000123", "1234567890123456789012345678901.", "12345678901234567890123456789012", "a1234b1234c1234d1234e1234f12345",
		"INV.2026_10-0001", "x2026102800000000000000000000000", "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", "abcdefghijklmnopqrstuvwxyz012345", "0123456789abcdef0123456789ABCDEF"}
	for _, n := range numbers {
		if !r.ValidIssueNumber(n) {
			t.Fatalf("%q is not a legal number", n)
		}
	}
	return numbers
}
