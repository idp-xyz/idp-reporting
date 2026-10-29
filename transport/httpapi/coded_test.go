package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/transport/httpapi"
)

// Schema 2.1 over HTTP (S03-M, ADR 0005): the private image store and its routes, previews and checks of templates with codes and
// images, authorization of every reference a template makes.

// codedRenderer is a renderer for schema 2.1 without Typst: a preview and a check record what they were given, as the PDF writer's
// RenderWith and CheckIssueWith are given it.
type codedRenderer struct {
	renders []codedCall
	checks  []codedCall
	refuse  func(codedCall) error
}

type codedCall struct {
	values map[string]string
	table  *doctemplates.TableData
	with   doctemplates.Resolved
}

func (c *codedRenderer) Render(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) ([]byte, error) {
	return nil, r.ErrUnsupported // a 2.1 template is never drawn without its images
}
func (c *codedRenderer) CheckIssue(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) error {
	return r.ErrUnsupported
}
func (c *codedRenderer) RenderWith(_ context.Context, _ doctemplates.Document, values map[string]string, _ []byte, table *doctemplates.TableData, with doctemplates.Resolved) ([]byte, error) {
	call := codedCall{values, table, with}
	c.renders = append(c.renders, call)
	if c.refuse != nil {
		if e := c.refuse(call); e != nil {
			return nil, e
		}
	}
	return []byte("%PDF-1.7\ncoded preview\n%%EOF"), nil
}
func (c *codedRenderer) CheckIssueWith(_ context.Context, _ doctemplates.Document, values map[string]string, _ []byte, table *doctemplates.TableData, with doctemplates.Resolved) error {
	call := codedCall{values, table, with}
	c.checks = append(c.checks, call)
	if c.refuse != nil {
		return c.refuse(call)
	}
	return nil
}

// plainRenderer is a renderer that cannot draw schema 2.1.
type plainRenderer struct{}

func (plainRenderer) Render(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) ([]byte, error) {
	return []byte("%PDF-1.7\nplain\n%%EOF"), nil
}
func (plainRenderer) CheckIssue(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) error {
	return nil
}

type codedFixture struct {
	*checkFixture
	renderer *codedRenderer
	h        http.Handler
	store    *doctemplates.Store
}

func newCodedFixture(t *testing.T, customers []string, renderer httpapi.DocumentRenderer) *codedFixture {
	t.Helper()
	f := &codedFixture{checkFixture: newCheckFixture(t, customers, true), renderer: &codedRenderer{}}
	if renderer == nil {
		renderer = f.renderer
	}
	demo, _ := httpapi.DemoBearer(secret, contractsim.DemoPrincipal())
	auth := func(req *http.Request) (r.Principal, error) {
		v, e := demo(req)
		if f.other {
			v.Subject = "other"
		}
		return v, e
	}
	fallback, _ := httpapi.Handler(f.service, auth)
	var e error
	if fallback, e = httpapi.WithDocumentAssets(fallback, f.service, auth, f.assets); e != nil {
		t.Fatal(e)
	}
	if fallback, e = httpapi.WithDocumentPreviews(fallback, f.service, auth, renderer, f.assets); e != nil {
		t.Fatal(e)
	}
	if f.store, e = doctemplates.Open(filepath.Join(t.TempDir(), "templates")); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.store.Close() })
	if f.h, e = httpapi.WithDocumentTemplates(fallback, f.service, auth, f.store, renderer, f.assets); e != nil {
		t.Fatal(e)
	}
	return f
}

func (f *codedFixture) binding() doctemplates.Binding {
	return doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset}
}

func (f *codedFixture) do(method, path string, body any, authorized bool) *httptest.ResponseRecorder {
	f.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	return request(f.h, method, "/api/reporting/v1/"+path, raw, authorized)
}

func refOf(s doctemplates.ImageSummary) *doctemplates.ImageRef { ref := s.Ref(); return &ref }

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			im.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: 200})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			im.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *codedFixture) upload(data []byte, b doctemplates.Binding) (int, doctemplates.ImageSummary, string) {
	f.t.Helper()
	w := f.do("POST", "document-images", map[string]any{"binding": b, "image": base64.StdEncoding.EncodeToString(data)}, true)
	var s doctemplates.ImageSummary
	if w.Code == 201 {
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			f.t.Fatal(err)
		}
	}
	return w.Code, s, w.Body.String()
}

// codedTemplate is a 2.1 statement grouped by customer: a logo, the customer id as text and as Code 128 and QR code, the issue number
// as text and as a QR code in the footer.
func (f *codedFixture) codedTemplate(ref *doctemplates.ImageRef) doctemplates.Document {
	text := func(id, source, v string, x, y float64) doctemplates.Item {
		it := doctemplates.Item{ID: id, Type: "text", Source: source, X: x, Y: y, Width: 60, Height: 8, FontSize: 10, Align: "left"}
		switch source {
		case "literal":
			it.Text = v
		case "field":
			it.Field = v
		}
		return it
	}
	header := []doctemplates.Item{
		text("e1", "field", "customer.id", 0, 0),
		text("e2", "issue_number", "", 100, 0),
		{ID: "e3", Type: "barcode", Source: "field", Field: "customer.id", X: 0, Y: 10, Width: 100, Height: 20, Symbology: "code128"},
		{ID: "e4", Type: "qrcode", Source: "literal", Text: "https://example.invalid/statement", X: 110, Y: 10, Size: 20, ECC: "M"},
	}
	if ref != nil {
		header = append(header, doctemplates.Item{ID: "e5", Type: "image", X: 140, Y: 10, Width: 40, Height: 20, Image: ref})
	}
	return doctemplates.Document{Schema: "2.1", Kind: "document", Binding: f.binding(), Group: &doctemplates.GroupSpec{Fields: []string{"customer.id"}},
		Bands: &doctemplates.Bands{
			PageHeader: &doctemplates.Band{Height: 32, Elements: header},
			Detail: &doctemplates.DetailBand{Elements: []doctemplates.Item{{ID: "e6", Type: "table", X: 0, Width: 190, FontSize: 10, Columns: []doctemplates.ItemColumn{
				{Field: "customer.id", Width: 60}, {Field: "aging.bucket", Width: 60}, {Field: "ar.open_balance", Width: 70}}}}},
			DocumentFooter: &doctemplates.Band{Height: 30, Elements: []doctemplates.Item{{ID: "e7", Type: "qrcode", Source: "issue_number", X: 0, Y: 0, Size: 25, ECC: "H"}}},
		}}
}

func (f *codedFixture) preview(d doctemplates.Document, row int) (int, string) {
	f.t.Helper()
	w := f.do("POST", "document-previews", map[string]any{"config": d, "snapshot_id": f.snapshot, "row_index": row}, true)
	return w.Code, w.Body.String()
}

func TestImagesAreUploadedListedAndServedToTheirOwnerOnly(t *testing.T) {
	f := newCodedFixture(t, []string{"A", "B"}, nil)
	binding := f.binding()
	// Both formats are taken, the format read from the content; the answer is what the platform kept.
	code, first, body := f.upload(pngBytes(t, 120, 60), binding)
	if code != 201 || first.Format != "png" || first.Width != 120 || first.Height != 60 || first.Binding != binding || len(first.ID) != 32 || len(first.SHA256) != 64 {
		t.Fatalf("%d %s", code, body)
	}
	code, second, body := f.upload(jpegBytes(t, 80, 40), binding)
	if code != 201 || second.Format != "jpeg" || second.Width != 80 || second.Height != 40 {
		t.Fatalf("%d %s", code, body)
	}
	// The listing is the caller's, with what the platform kept; the answers of the image routes are never cached or sniffed.
	w := f.do("GET", "document-images", nil, true)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("listing headers: %v", w.Header())
	}
	var listed []doctemplates.ImageSummary
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed) != 2 || listed[0].ID != first.ID || listed[1].ID != second.ID || listed[1].Format != "jpeg" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "\"data\"") || strings.Contains(w.Body.String(), "iVBOR") {
		t.Errorf("the listing carries the bytes: %s", w.Body.String())
	}
	// The content is the bytes, as the image they are, with the digest, never cached and never sniffed.
	for _, c := range []struct {
		s     doctemplates.ImageSummary
		media string
	}{{first, "image/png"}, {second, "image/jpeg"}} {
		w := f.do("GET", "document-images/"+c.s.ID+"/content", nil, true)
		sum := sha256.Sum256(w.Body.Bytes())
		if w.Code != 200 || w.Header().Get("Content-Type") != c.media || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
			w.Header().Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) || hex.EncodeToString(sum[:]) != c.s.SHA256 || w.Header().Get("Content-Length") == "" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("%s: %d %v", c.media, w.Code, w.Header())
		}
	}
	// Anonymous callers get nothing; another identity sees neither the listing nor the bytes, and a stranger's id is not found.
	if w := f.do("GET", "document-images/"+first.ID+"/content", nil, false); w.Code != 401 {
		t.Errorf("anonymous content: %d", w.Code)
	}
	if w := f.do("GET", "document-images", nil, false); w.Code != 401 {
		t.Errorf("anonymous listing: %d", w.Code)
	}
	f.other = true
	if w := f.do("GET", "document-images/"+first.ID+"/content", nil, true); w.Code != 404 || strings.Contains(w.Body.String(), "PNG") {
		t.Errorf("another identity's content: %d", w.Code)
	}
	if w := f.do("GET", "document-images", nil, true); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("another identity's listing: %d %s", w.Code, w.Body.String())
	}
	f.other = false
	if w := f.do("GET", "document-images/"+strings.Repeat("0", 32)+"/content", nil, true); w.Code != 404 {
		t.Errorf("an unknown id: %d", w.Code)
	}
	if w := f.do("GET", "document-images/not-an-id/content", nil, true); w.Code != 400 {
		t.Errorf("a malformed id: %d", w.Code)
	}
	// What is refused, with no value in the answer: bytes that are not an image, base64 that is not, a dataset the caller may not read,
	// a declared type nobody asked for, too many bytes, too many pixels.
	big := pngBytes(t, 3, 3)
	other := binding
	other.Dataset.Version = "9.9.9"
	for name, c := range map[string]struct {
		body   map[string]any
		status int
	}{
		"not an image":        {map[string]any{"binding": binding, "image": base64.StdEncoding.EncodeToString([]byte("GIF89a not supported"))}, 400},
		"not base64":          {map[string]any{"binding": binding, "image": "***"}, 400},
		"another dataset":     {map[string]any{"binding": other, "image": base64.StdEncoding.EncodeToString(big)}, 403},
		"a key too many":      {map[string]any{"binding": binding, "image": base64.StdEncoding.EncodeToString(big), "type": "image/png"}, 400},
		"no binding":          {map[string]any{"image": base64.StdEncoding.EncodeToString(big)}, 403},
		"over the byte limit": {map[string]any{"binding": binding, "image": strings.Repeat("A", base64.StdEncoding.EncodedLen(doctemplates.MaxImageBytes)+4)}, 413},
	} {
		w := f.do("POST", "document-images", c.body, true)
		if w.Code != c.status {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if code, _, body := f.upload(pngBytes(t, 2001, 8), binding); code != 400 {
		t.Errorf("2001 pixels wide: %d %s", code, body)
	}
	if code, _, body := f.upload(jpegBytes(t, 8, 2001), binding); code != 400 {
		t.Errorf("2001 pixels high: %d %s", code, body)
	}
	if w := f.do("POST", "document-images", map[string]any{"binding": binding, "image": base64.StdEncoding.EncodeToString(big)}, false); w.Code != 401 {
		t.Errorf("anonymous upload: %d", w.Code)
	}
	// An identity keeps at most 16 images for a dataset.
	for i := 2; i < doctemplates.MaxImagesPerDataset; i++ {
		if code, _, body := f.upload(pngBytes(t, 4+i, 4), binding); code != 201 {
			t.Fatalf("image %d: %d %s", i+1, code, body)
		}
	}
	if code, _, _ := f.upload(pngBytes(t, 99, 4), binding); code != 413 {
		t.Errorf("a 17th image: %d", code)
	}
}

func TestACodedTemplateIsPreviewedWithItsImagesAndBoundValues(t *testing.T) {
	f := newCodedFixture(t, []string{"A", "B", "A"}, nil)
	_, logo, _ := f.upload(pngBytes(t, 120, 60), f.binding())
	d := f.codedTemplate(refOf(logo))
	code, body := f.preview(d, 2)
	if code != 200 || !strings.HasPrefix(body, "%PDF-") || len(f.renderer.renders) != 1 {
		t.Fatalf("%d %q", code, body)
	}
	call := f.renderer.renders[0]
	// The values are the group's: a text and a code of a field and the literal of a QR code; the issue number is no value of the snapshot.
	want := map[string]string{"e1": "A", "e3": "A", "e4": "https://example.invalid/statement"}
	if len(call.values) != len(want) {
		t.Errorf("values %v", call.values)
	}
	for k, v := range want {
		if call.values[k] != v {
			t.Errorf("values %v", call.values)
		}
	}
	if _, has := call.values["e2"]; has {
		t.Error("the issue number is bound as a value")
	}
	if len(call.with.Images) != 1 || !bytes.Equal(call.with.Images["e5"], pngBytes(t, 120, 60)) && len(call.with.Images["e5"]) == 0 || call.with.Number != "" {
		t.Errorf("resolved %+v", call.with)
	}
	// A renderer that cannot draw 2.1 refuses it, and a check likewise; a 1.x template previews as before.
	g := newCodedFixture(t, []string{"A", "B", "A"}, plainRenderer{})
	_, logo2, _ := g.upload(pngBytes(t, 120, 60), g.binding())
	if code, body := g.preview(g.codedTemplate(refOf(logo2)), 0); code != 422 {
		t.Errorf("a renderer without 2.1: %d %s", code, body)
	}
	if w := g.do("POST", "document-checks", map[string]any{"config": g.codedTemplate(refOf(logo2)), "snapshot_id": g.snapshot, "rows": []int{0}}, true); w.Code != 422 {
		t.Errorf("a checker without 2.1: %d %s", w.Code, w.Body.String())
	}
	if code, _ := g.preview(g.config(), 0); code != 200 {
		t.Errorf("a 1.x template: %d", code)
	}
}

func TestEveryReferenceOfACodedTemplateIsAuthorized(t *testing.T) {
	f := newCodedFixture(t, []string{"A", "B", "A"}, nil)
	_, mine, _ := f.upload(pngBytes(t, 120, 60), f.binding())
	// Another identity's image is not found, whatever its digest; so a template naming it is refused where it is saved and previewed.
	stranger := contractsim.DemoPrincipal().Identity
	stranger.Subject = "other"
	theirs, err := f.assets.Images.Put(t.Context(), stranger, f.binding(), pngBytes(t, 50, 50))
	if err != nil {
		t.Fatal(err)
	}
	for name, ref := range map[string]doctemplates.ImageRef{
		"another identity's image": theirs.Ref(),
		"an unknown image":         {ID: strings.Repeat("0", 32), SHA256: mine.SHA256},
		"a wrong digest":           {ID: mine.ID, SHA256: strings.Repeat("0", 64)},
	} {
		ref := ref
		d := f.codedTemplate(&ref)
		code, body := f.preview(d, 0)
		want := map[string]int{"another identity's image": 404, "an unknown image": 404, "a wrong digest": 409}[name]
		if code != want {
			t.Errorf("%s: preview %d %s", name, code, body)
		}
		save := f.do("POST", "document-templates", map[string]any{"id": "", "expected_version": 0, "config": d}, true)
		if save.Code != want {
			t.Errorf("%s: save %d %s", name, save.Code, save.Body.String())
		}
		if w := f.do("POST", "document-checks", map[string]any{"config": d, "snapshot_id": f.snapshot, "rows": []int{0}}, true); w.Code != want {
			t.Errorf("%s: check %d %s", name, w.Code, w.Body.String())
		}
	}
	if len(f.renderer.renders) != 0 || len(f.renderer.checks) != 0 {
		t.Fatalf("a template whose image is refused was drawn or checked")
	}
	// An image of another binding is denied: the same bytes, uploaded for a dataset the template does not name, cannot be named by it.
	otherBinding := f.binding()
	otherBinding.Dataset.Version = "2.0.0"
	asset, err := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Close()
	foreign, err := asset.Images.Put(t.Context(), contractsim.DemoPrincipal().Identity, otherBinding, pngBytes(t, 10, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.assets.Images.Resolve(contractsim.DemoPrincipal().Identity, f.binding(), foreign.Ref()); err == nil {
		t.Fatal("an image that was never stored here resolved")
	}
	stored, err := f.assets.Images.Put(t.Context(), contractsim.DemoPrincipal().Identity, otherBinding, pngBytes(t, 10, 10))
	if err != nil {
		t.Fatal(err)
	}
	d := f.codedTemplate(refOf(stored))
	if code, body := f.preview(d, 0); code != 403 {
		t.Errorf("an image of another binding: %d %s", code, body)
	}
	// A deployment with no image store cannot draw an image: unsupported.
	nostore := newCodedFixture(t, []string{"A"}, nil)
	nostore.assets.Images = nil
	d = nostore.codedTemplate(refOf(mine))
	if code, _ := nostore.preview(d, 0); code != 422 {
		t.Errorf("no image store: %d", code)
	}
	// A field of a code the caller's catalog lacks is denied, as a field of a text is.
	f.adapter.allowed = "ar.open_balance"
	d = f.codedTemplate(refOf(mine))
	if code, _ := f.preview(d, 0); code != 403 {
		t.Errorf("a field removed from the catalog: %d", code)
	}
	f.adapter.allowed = ""
	d = f.codedTemplate(refOf(mine))
	d.Bands.PageHeader.Elements[2].Field = "ghost"
	if code, _ := f.preview(d, 0); code != 403 {
		t.Errorf("a barcode field the dataset lacks: %d", code)
	}
	d = f.codedTemplate(refOf(mine))
	d.Bands.PageHeader.Elements[3] = doctemplates.Item{ID: "e4", Type: "qrcode", Source: "field", Field: "ghost", X: 110, Y: 10, Size: 20, ECC: "M"}
	if code, _ := f.preview(d, 0); code != 403 {
		t.Errorf("a QR code field the dataset lacks: %d", code)
	}
	// A template that is authorized is saved, opened and published like any.
	good := f.codedTemplate(refOf(mine))
	save := f.do("POST", "document-templates", map[string]any{"id": "", "expected_version": 0, "config": good}, true)
	if save.Code != 200 && save.Code != 201 {
		t.Fatalf("save: %d %s", save.Code, save.Body.String())
	}
	var saved struct {
		ID      string                `json:"id"`
		Version int                   `json:"version"`
		Config  doctemplates.Document `json:"config"`
	}
	if err := json.Unmarshal(save.Body.Bytes(), &saved); err != nil || saved.Config.Schema != "2.1" || saved.Config.Bands.PageHeader.Elements[4].Image.ID != mine.ID {
		t.Fatalf("%v %s", err, save.Body.String())
	}
	if w := f.do("GET", "document-templates/"+saved.ID+"?version=1", nil, true); w.Code != 200 {
		t.Errorf("open: %d", w.Code)
	}
}

func TestACodesValueTheSymbologyCannotEncodeIsRefusedWithItsPlace(t *testing.T) {
	// The customer ids are the values of the Code 128 element e3: ASCII is encoded; a customer with a character outside Code 128 is refused with
	// the element, the reason and no value, by the preview and by the binding of an issue.
	f := newCodedFixture(t, []string{"A", "客户", "C"}, nil)
	_, logo, _ := f.upload(pngBytes(t, 120, 60), f.binding())
	d := f.codedTemplate(refOf(logo))
	code, body := f.preview(d, 1)
	var answer struct {
		Error  string               `json:"error"`
		Detail *doctemplates.Detail `json:"detail"`
	}
	_ = json.Unmarshal([]byte(body), &answer)
	if code != 422 || answer.Detail == nil || answer.Detail.Reason != "NOT_COVERED" || answer.Detail.Band != "page_header" || answer.Detail.Element != "e3" || strings.Contains(body, "客户") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := f.preview(d, 0); code != 200 {
		t.Errorf("a value that encodes: %d", code)
	}
	// The check says it per row, with the same detail, and takes the other groups through the checker.
	w := f.do("POST", "document-checks", map[string]any{"config": d, "snapshot_id": f.snapshot, "rows": []int{0, 1, 2}}, true)
	var checked struct {
		Rows []struct {
			RowIndex int                  `json:"row_index"`
			OK       bool                 `json:"ok"`
			Code     string               `json:"code"`
			Detail   *doctemplates.Detail `json:"detail"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &checked); err != nil || w.Code != 200 || len(checked.Rows) != 3 || !checked.Rows[0].OK || checked.Rows[1].OK || checked.Rows[1].Detail == nil || checked.Rows[1].Detail.Element != "e3" || !checked.Rows[2].OK || strings.Contains(w.Body.String(), "客户") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// Only the groups the checker is asked about reach it, with the images and no number: it proves every number itself.
	if len(f.renderer.checks) != 2 || len(f.renderer.checks[0].with.Images) != 1 || f.renderer.checks[0].with.Number != "" {
		t.Errorf("checks %+v", f.renderer.checks)
	}
	// A refusal of the renderer, with a detail, is the group's answer, as for 2.0.
	f.renderer.refuse = func(codedCall) error {
		return doctemplates.Refuse(r.ErrUnsupported, doctemplates.Detail{Reason: "DOES_NOT_FIT", Band: "page_header", Element: "e4"})
	}
	w = f.do("POST", "document-checks", map[string]any{"config": d, "snapshot_id": f.snapshot, "rows": []int{0}}, true)
	if err := json.Unmarshal(w.Body.Bytes(), &checked); err != nil || w.Code != 200 || len(checked.Rows) != 1 || checked.Rows[0].OK || checked.Rows[0].Detail == nil || checked.Rows[0].Detail.Element != "e4" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
