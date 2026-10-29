package doctemplates_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// Schema 2.1 (S03-M, ADR 0005): the keys of the new element types, the binding of the values of codes, the private image store.

func codedInvoice(items ...doctemplates.Item) doctemplates.Document {
	d := invoice([]string{"order"})
	d.Schema = doctemplates.SchemaCoded
	d.Bands.DocumentFooter = band(40, items...)
	return d
}

func barcodeOf(id, source, v string) doctemplates.Item {
	it := doctemplates.Item{ID: id, Type: doctemplates.TypeBarcode, Source: source, X: 0, Y: 0, Width: 120, Height: 20, Symbology: doctemplates.SymbologyCode128}
	switch source {
	case "literal":
		it.Text = v
	case "field":
		it.Field = v
	}
	return it
}

func qrOf(id, source, v string) doctemplates.Item {
	it := doctemplates.Item{ID: id, Type: doctemplates.TypeQRCode, Source: source, X: 130, Y: 0, Size: 30, ECC: "Q"}
	switch source {
	case "literal":
		it.Text = v
	case "field":
		it.Field = v
	}
	return it
}

func TestCodedItemsWriteOnlyTheKeysOfTheirTypes(t *testing.T) {
	ref := doctemplates.ImageRef{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64)}
	readable := barcodeOf("e11", "issue_number", "")
	readable.HumanReadable, readable.FontSize = true, 9
	for name, c := range map[string]struct {
		it   doctemplates.Item
		keys string
	}{
		"a barcode":                       {barcodeOf("e10", "literal", "x"), "field,height,human_readable,id,source,symbology,text,type,width,x,y"},
		"a barcode that prints its value": {readable, "field,font_size,height,human_readable,id,source,symbology,text,type,width,x,y"},
		"a QR code":                       {qrOf("e12", "field", "order"), "ecc,field,id,size,source,text,type,x,y"},
		"an image":                        {doctemplates.Item{ID: "e13", Type: doctemplates.TypeImage, X: 0, Y: 0, Width: 20, Height: 10, Image: &ref}, "height,id,image,type,width,x,y"},
		"the issue number as text":        {doctemplates.Item{ID: "e14", Type: doctemplates.TypeText, Source: doctemplates.SourceIssueNumber, X: 0, Y: 0, Width: 60, Height: 8, FontSize: 10, Align: "left"}, "align,bold,field,font_size,height,id,source,text,type,width,wrap,x,y"},
	} {
		raw, err := json.Marshal(c.it)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		if got := strings.Join(names, ","); got != c.keys {
			t.Errorf("%s: keys %s, want %s", name, got, c.keys)
		}
		// What is written reads back as the same item.
		var back doctemplates.Item
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, _ := json.Marshal(back)
		if !bytes.Equal(raw, again) {
			t.Errorf("%s: %s then %s", name, raw, again)
		}
	}
	// An image with no reference has no form to write.
	if _, err := json.Marshal(doctemplates.Item{ID: "e1", Type: doctemplates.TypeImage}); err == nil {
		t.Error("an image without a reference was written")
	}
	// A document of schema 2.1 keeps the keys of 2.0; a 2.0 document refuses the 2.1 elements and source.
	d := codedInvoice(barcodeOf("e8", "literal", "A-1"))
	raw, err := json.Marshal(d)
	if err != nil || !strings.Contains(string(raw), `"schema_version":"2.1"`) || strings.Contains(string(raw), `"print"`) {
		t.Fatalf("%s %v", raw, err)
	}
	var back doctemplates.Document
	if err := json.Unmarshal(raw, &back); err != nil || doctemplates.Validate(back) != nil || !back.Coded() || !back.Banded() {
		t.Fatalf("%v %v", err, doctemplates.Validate(back))
	}
	two := d
	two.Schema = doctemplates.SchemaBanded
	if err := doctemplates.Validate(two); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a barcode in a 2.0 document: %v", err)
	}
	if (doctemplates.Document{Schema: "1.3"}).Banded() || (doctemplates.Document{Schema: "2.0"}).Coded() {
		t.Error("Banded and Coded name the wrong versions")
	}
}

func TestTheValuesOfCodesAreBoundLikeTheValuesOfTexts(t *testing.T) {
	s := orders(
		line("INV-1", "客户", "a", "1", "1.00"), line("INV-2", "客户", "b", "1", "2.00"), line("INV-1", "客户", "c", "1", "3.00"),
	)
	at := func(it doctemplates.Item, x, y, w, h float64) doctemplates.Item {
		it.X, it.Y = x, y
		if it.Type == doctemplates.TypeQRCode {
			it.Size = w
		} else {
			it.Width, it.Height = w, h
		}
		return it
	}
	d := codedInvoice(
		at(barcodeOf("e8", "literal", "WAYBILL-7"), 0, 0, 100, 16),
		at(barcodeOf("e9", "field", "order"), 0, 20, 100, 16),
		at(qrOf("e10", "field", "customer"), 110, 0, 25, 0),
		at(qrOf("e11", "literal", "https://example.invalid/x"), 140, 0, 25, 0),
		doctemplates.Item{ID: "e12", Type: doctemplates.TypeText, Source: doctemplates.SourceIssueNumber, X: 110, Y: 28, Width: 60, Height: 8, FontSize: 10, Align: "left"},
		at(barcodeOf("e13", "issue_number", ""), 0, 40, 100, 16),
	)
	d.Bands.DocumentFooter.Height = 60
	if err := doctemplates.Validate(d); err != nil {
		t.Fatal(err)
	}
	groups, err := doctemplates.Groups(d, s)
	if err != nil || len(groups) != 2 {
		t.Fatalf("%v %v", groups, err)
	}
	values, _, err := doctemplates.BindGroup(d, s, groups[0])
	if err != nil {
		t.Fatal(err)
	}
	// A literal as it is, a field of the group (the key or a value the rows share), the issue number not at all: it is the renderer's.
	want := map[string]string{"e1": "INV-1", "e2": "客户", "e3": "{page}/{pages}", "e4": "标题", "e8": "WAYBILL-7", "e9": "INV-1", "e10": "客户", "e11": "https://example.invalid/x"}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("value %s = %q, want %q", k, values[k], v)
		}
	}
	for _, k := range []string{"e12", "e13"} {
		if _, has := values[k]; has {
			t.Errorf("%s: the issue number is bound as a value", k)
		}
	}
	// A field that is no group field must be the same in every row: picking one row's value would be a silent decision.
	d.Bands.DocumentFooter.Elements[1].Field = "note"
	if _, _, err = doctemplates.BindGroup(d, s, groups[0]); !errors.Is(err, r.ErrUnsupported) || detailOf(t, err).Reason != doctemplates.ReasonInconsistentGroup || detailOf(t, err).Element != "e9" {
		t.Errorf("an inconsistent barcode field: %v", err)
	}
	d.Bands.DocumentFooter.Elements[1].Field = "order"
	// What a symbology cannot encode is refused with its place and no value; the reason is the one of a text. (The header's own text of the
	// customer is made the order's, so that the codes are the elements that refuse.)
	d.Bands.PageHeader.Elements[1].Field = "order"
	for name, c := range map[string]struct {
		rows   [][]string
		reason string
		el     string
	}{
		"a letter outside Code 128": {[][]string{line("é", "客户", "", "1", "1")}, doctemplates.ReasonNotCovered, "e9"},
		"an empty Code 128 value":   {[][]string{line("", "客户", "", "1", "1")}, doctemplates.ReasonNotCovered, "e9"},
		"65 characters":             {[][]string{line(strings.Repeat("a", 65), "客户", "", "1", "1")}, doctemplates.ReasonDoesNotFit, "e9"},
		"a control in a QR code":    {[][]string{line("A", "a\x01b", "", "1", "1")}, doctemplates.ReasonNotCovered, "e10"},
		"257 bytes in a QR code":    {[][]string{line("A", strings.Repeat("x", 257), "", "1", "1")}, doctemplates.ReasonDoesNotFit, "e10"},
		"86 ideographs":             {[][]string{line("A", strings.Repeat("字", 86), "", "1", "1")}, doctemplates.ReasonDoesNotFit, "e10"},
	} {
		snap := orders(c.rows...)
		gs, err := doctemplates.Groups(d, snap)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = doctemplates.BindGroup(d, snap, gs[0])
		det := detailOf(t, err)
		if !errors.Is(err, r.ErrUnsupported) || det.Reason != c.reason || det.Element != c.el || det.Band != doctemplates.BandDocumentFooter {
			t.Errorf("%s: %v %+v", name, err, det)
		}
		if strings.Contains(err.Error(), "é") || strings.Contains(err.Error(), strings.Repeat("a", 65)) {
			t.Errorf("%s: the error carries the value: %v", name, err)
		}
	}
	for v, want := range map[string]string{"A-1": "", "a b": "", "é": doctemplates.ReasonNotCovered, "": doctemplates.ReasonNotCovered, strings.Repeat("a", 65): doctemplates.ReasonDoesNotFit} {
		if got := doctemplates.CodeValueReason(barcodeOf("e1", "field", "x"), v); got != want {
			t.Errorf("code 128 %q: %q, want %q", v, got, want)
		}
	}
}

func detailOf(t testing.TB, err error) doctemplates.Detail {
	t.Helper()
	d, _ := doctemplates.DetailOf(err)
	return d
}

func TestAJPEGKeepsItsPixelsAndLosesItsMetadata(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			im.SetRGBA(x, y, color.RGBA{R: uint8(4 * x), G: uint8(5 * y), B: 128, A: 255})
		}
	}
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, im, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	// An EXIF segment with a location, an XMP packet, an ICC profile and a comment, after the start of the image.
	segment := func(marker byte, body string) []byte {
		n := len(body) + 2
		return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, body...)
	}
	data := plain.Bytes()
	var withMeta []byte
	withMeta = append(withMeta, data[:2]...)
	withMeta = append(withMeta, segment(0xE1, "Exif\x00\x00GPS 31.2304N 121.4737E")...)
	withMeta = append(withMeta, segment(0xE1, "http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")...)
	withMeta = append(withMeta, segment(0xE2, "ICC_PROFILE\x00\x01\x01profile")...)
	withMeta = append(withMeta, segment(0xFE, "a comment about the photographer")...)
	withMeta = append(withMeta, data[2:]...)
	withMeta = append(withMeta, []byte("trailing garbage after the end of the image")...)
	a, err := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	binding := doctemplates.Binding{Integration: "f", Dataset: r.Ref{ID: "d", Version: "1"}}
	got, err := a.Images.Put(t.Context(), owner, binding, withMeta)
	if err != nil || got.Format != "jpeg" || got.Width != 64 || got.Height != 48 {
		t.Fatalf("%+v %v", got, err)
	}
	kept, err := a.Images.Resolve(owner, binding, got.Ref())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"Exif", "GPS", "xmpmeta", "ICC_PROFILE", "photographer", "trailing garbage"} {
		if bytes.Contains(kept, []byte(leak)) {
			t.Errorf("the stored image still holds %q", leak)
		}
	}
	// The pixels are those the original decodes to: nothing was recompressed.
	was, _ := jpeg.Decode(bytes.NewReader(plain.Bytes()))
	now, err := jpeg.Decode(bytes.NewReader(kept))
	if err != nil || !bytes.Equal(pixels(was), pixels(now)) {
		t.Errorf("the pixels changed: %v", err)
	}
	// The same bytes in give the same bytes out, and one stripped already is kept as it is.
	again, err := a.Images.Put(t.Context(), owner, binding, kept)
	if err != nil || again.SHA256 != got.SHA256 {
		t.Errorf("a stripped image changed on its way in: %v", err)
	}
	// Truncated and spliced streams are refused, as is one that is not a JPEG at all.
	for name, bad := range map[string][]byte{
		"truncated":      plain.Bytes()[:plain.Len()/2],
		"no end":         plain.Bytes()[:plain.Len()-2],
		"a bad length":   append(append([]byte{}, plain.Bytes()[:2]...), 0xFF, 0xE0, 0xFF, 0xFF, 0),
		"only a marker":  {0xFF, 0xD8, 0xFF},
		"no start":       plain.Bytes()[2:],
		"a text file":    []byte("hello"),
		"empty":          nil,
		"a GIF":          []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"a PNG header":   []byte("\x89PNG\r\n\x1a\n"),
		"an SVG picture": []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
	} {
		if _, err := a.Images.Put(t.Context(), owner, binding, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestACMYKJPEGIsRefused: a JPEG of four components needs a colour profile that PDF/A-2b would require the file to declare, so it is not
// stored; a stream whose frame is lossless or arithmetic-coded is refused as well.
func TestACMYKJPEGIsRefused(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 16, 16))
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, im, nil); err != nil {
		t.Fatal(err)
	}
	data := plain.Bytes()
	// The frame header (SOF0): 8 bytes and 3 per component. Rewrite it with four components, as a CMYK image has.
	at := bytes.Index(data, []byte{0xFF, 0xC0})
	if at < 0 {
		t.Fatal("no baseline frame")
	}
	length := int(data[at+2])<<8 | int(data[at+3])
	sof := []byte{0xFF, 0xC0, 0, 8 + 12, 8, 0, 16, 0, 16, 4, 1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0, 4, 0x11, 0}
	cmyk := append(append(append([]byte{}, data[:at]...), sof...), data[at+2+length:]...)
	if _, _, _, err := doctemplates.ImageInfo(cmyk); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a JPEG of four components: %v", err)
	}
	// The frame of a lossless or arithmetic-coded JPEG has another marker (SOF3, SOF9): refused by the segment scanner as well.
	for _, marker := range []byte{0xC3, 0xC9, 0xCA} {
		other := append([]byte{}, data...)
		other[at+1] = marker
		a, err := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Images.Put(t.Context(), r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}, doctemplates.Binding{Integration: "f", Dataset: r.Ref{ID: "d", Version: "1"}}, other); err == nil {
			t.Errorf("a frame of marker %#x was stored", marker)
		}
		a.Close()
	}
}

func pixels(im image.Image) []byte {
	b := im.Bounds()
	var out []byte
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := im.At(x, y).RGBA()
			out = append(out, byte(r>>8), byte(g>>8), byte(bl>>8), byte(a>>8))
		}
	}
	return out
}

func TestImagesAreNormalizedBoundedAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "assets")
	a, err := doctemplates.OpenAssets(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	binding := doctemplates.Binding{Integration: "finance", Dataset: r.Ref{ID: "d", Version: "v1"}}
	// A PNG is decoded and written again: a text chunk is not kept, the pixels are, the alpha channel included.
	im := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	im.SetNRGBA(3, 4, color.NRGBA{R: 200, G: 10, B: 10, A: 100})
	var raw bytes.Buffer
	if err := png.Encode(&raw, im); err != nil {
		t.Fatal(err)
	}
	withText := insertChunk(t, raw.Bytes(), "tEXt", []byte("Author\x00somebody"))
	if !bytes.Contains(withText, []byte("somebody")) {
		t.Fatal("the fixture has no text chunk")
	}
	ref, err := a.Images.Put(t.Context(), owner, binding, withText)
	if err != nil || ref.Format != "png" || ref.Width != 40 || ref.Height != 20 || ref.Binding != binding {
		t.Fatalf("%+v %v", ref, err)
	}
	data, err := a.Images.Resolve(owner, binding, ref.Ref())
	if err != nil || bytes.Contains(data, []byte("somebody")) {
		t.Fatalf("the text chunk was kept: %v", err)
	}
	back, err := png.Decode(bytes.NewReader(data))
	if err != nil || color.NRGBAModel.Convert(back.At(3, 4)) != (color.NRGBA{R: 200, G: 10, B: 10, A: 100}) {
		t.Errorf("the pixel changed: %v %v", back.At(3, 4), err)
	}
	// The listing and the content are the owner's; the digest is the bytes' own.
	list, err := a.Images.List(owner)
	if err != nil || len(list) != 1 || list[0].ID != ref.ID || list[0].SHA256 != ref.SHA256 {
		t.Fatalf("%+v %v", list, err)
	}
	summary, content, err := a.Images.Content(owner, ref.ID)
	if err != nil || summary.Format != "png" || !bytes.Equal(content, data) {
		t.Fatalf("%+v %v", summary, err)
	}
	for name, who := range map[string]r.Identity{
		"another subject": {Tenant: "t", Issuer: "i", Subject: "other"},
		"another tenant":  {Tenant: "other", Issuer: "i", Subject: "s"},
		"another issuer":  {Tenant: "t", Issuer: "other", Subject: "s"},
	} {
		if _, err := a.Images.Resolve(who, binding, ref.Ref()); !errors.Is(err, r.ErrNotFound) {
			t.Errorf("%s: resolve %v", name, err)
		}
		if _, _, err := a.Images.Content(who, ref.ID); !errors.Is(err, r.ErrNotFound) {
			t.Errorf("%s: content %v", name, err)
		}
		if l, err := a.Images.List(who); err != nil || len(l) != 0 {
			t.Errorf("%s: list %v %v", name, l, err)
		}
	}
	// Another binding is denied, another digest is an integrity failure, a malformed reference is invalid.
	other := binding
	other.Dataset.Version = "v2"
	if _, err := a.Images.Resolve(owner, other, ref.Ref()); !errors.Is(err, r.ErrDenied) {
		t.Errorf("another binding: %v", err)
	}
	if _, err := a.Images.Resolve(owner, binding, doctemplates.ImageRef{ID: ref.ID, SHA256: strings.Repeat("0", 64)}); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("another digest: %v", err)
	}
	if _, err := a.Images.Resolve(owner, binding, doctemplates.ImageRef{ID: "x", SHA256: ref.SHA256}); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a malformed reference: %v", err)
	}
	// Limits: the pixels on a side (2000), the bytes (512 KiB, as uploaded and as kept), no owner, no binding.
	if _, err := a.Images.Put(t.Context(), owner, binding, flatPNG(t, 2000, 2)); err != nil {
		t.Errorf("2000 pixels wide: %v", err)
	}
	for name, bad := range map[string][]byte{"2001 wide": flatPNG(t, 2001, 2), "2001 high": flatPNG(t, 2, 2001), "no bytes": nil, "over the limit": append(flatPNG(t, 4, 4), make([]byte, doctemplates.MaxImageBytes)...)} {
		if _, err := a.Images.Put(t.Context(), owner, binding, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := a.Images.Put(t.Context(), r.Identity{}, binding, withText); !errors.Is(err, r.ErrDenied) {
		t.Errorf("no owner: %v", err)
	}
	if _, err := a.Images.Put(t.Context(), owner, doctemplates.Binding{}, withText); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("no binding: %v", err)
	}
	// A renderer that cannot draw an image refuses it at upload with its own error, and nothing is stored.
	refuse := errors.New("probe refused")
	a.Images.Fits = func(context.Context, []byte) error { return refuse }
	before, _ := a.Images.List(owner)
	if _, err := a.Images.Put(t.Context(), owner, binding, flatPNG(t, 10, 10)); !errors.Is(err, refuse) {
		t.Errorf("a probe that refuses: %v", err)
	}
	if after, _ := a.Images.List(owner); len(after) != len(before) {
		t.Error("an image the renderer refused was stored")
	}
	a.Images.Fits = nil
	// At most 16 for an identity and a binding; the quota is per binding, and another identity has its own.
	for i := len(before); i < doctemplates.MaxImagesPerDataset; i++ {
		if _, err := a.Images.Put(t.Context(), owner, binding, flatPNG(t, 10+i, 10)); err != nil {
			t.Fatalf("image %d: %v", i+1, err)
		}
	}
	if _, err := a.Images.Put(t.Context(), owner, binding, flatPNG(t, 200, 10)); !errors.Is(err, r.ErrBudget) {
		t.Errorf("a 17th image for one binding: %v", err)
	}
	if _, err := a.Images.Put(t.Context(), r.Identity{Tenant: "t", Issuer: "i", Subject: "other"}, binding, flatPNG(t, 10, 10)); err != nil {
		t.Errorf("another identity's first image: %v", err)
	}
	// What is stored survives a restart, and a store with a damaged image refuses to open it.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := doctemplates.OpenAssets(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.Images.Resolve(owner, binding, ref.Ref()); err != nil || !bytes.Equal(got, data) {
		t.Errorf("after a restart: %v", err)
	}
	// The directory is single-writer: a second open of it is refused, backgrounds and images alike.
	if _, err := doctemplates.OpenAssets(dir); !errors.Is(err, r.ErrConflict) {
		t.Errorf("a second open: %v", err)
	}
}

func flatPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	im := image.NewGray(image.Rect(0, 0, w, h))
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// insertChunk adds a chunk after the header chunk of a PNG, with a correct CRC.
func insertChunk(t testing.TB, data []byte, kind string, body []byte) []byte {
	t.Helper()
	const ihdrEnd = 8 + 4 + 4 + 13 + 4
	chunk := make([]byte, 0, 12+len(body))
	chunk = append(chunk, byte(len(body)>>24), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	chunk = append(chunk, kind...)
	chunk = append(chunk, body...)
	crc := crc32IEEE(append([]byte(kind), body...))
	chunk = append(chunk, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	return append(append(append([]byte{}, data[:ihdrEnd]...), chunk...), data[ihdrEnd:]...)
}

func crc32IEEE(b []byte) uint32 {
	crc := ^uint32(0)
	for _, v := range b {
		crc ^= uint32(v)
		for range 8 {
			if crc&1 == 1 {
				crc = crc>>1 ^ 0xEDB88320
			} else {
				crc >>= 1
			}
		}
	}
	return ^crc
}

func TestImagesAreSafeForConcurrentUse(t *testing.T) {
	a, err := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	binding := doctemplates.Binding{Integration: "f", Dataset: r.Ref{ID: "d", Version: "1"}}
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Images.Put(t.Context(), owner, binding, flatPNG(t, 8+i, 8)); err != nil {
				t.Error(err)
			}
			if _, err := a.Images.List(owner); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if l, _ := a.Images.List(owner); len(l) != 6 {
		t.Errorf("%d images", len(l))
	}
}
