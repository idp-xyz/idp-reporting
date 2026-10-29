package issuance_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/doctemplates"
	"idp.local/reporting/issuance"
	r "idp.local/reporting/reporting"
)

// Schema 2.1 (S03-M, ADR 0005): the issuer resolves the images of the owner, passes the product's number to the render and nothing else
// changes in the issue flow.

// resolvedRenderer is a renderer that can draw 2.1: it records what the layout check and the issue were given.
type resolvedRenderer struct {
	fakeRenderer
	checkedWith   doctemplates.Resolved
	issuedWith    doctemplates.Resolved
	resolvedCalls int
}

func (f *resolvedRenderer) CheckIssueWith(ctx context.Context, d doctemplates.Document, values map[string]string, bg []byte, table *doctemplates.TableData, with doctemplates.Resolved) error {
	f.checkedWith = with
	return f.fakeRenderer.CheckIssue(ctx, d, values, bg, table)
}

func (f *resolvedRenderer) IssueWith(ctx context.Context, d doctemplates.Document, values map[string]string, bg []byte, table *doctemplates.TableData, with doctemplates.Resolved, stamp string, at time.Time) ([]byte, int, error) {
	f.issuedWith = with
	f.resolvedCalls++
	return f.fakeRenderer.Issue(ctx, d, values, bg, table, stamp, at)
}

func logo(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 40, 20))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// codedDocument is bandedDocument as 2.1: the logo in the page header, the customer as a QR code and the number as a QR code in the footer.
func codedDocument(ref *doctemplates.ImageRef) doctemplates.Document {
	d := bandedDocument("customer")
	d.Schema = doctemplates.SchemaCoded
	d.Bands.PageHeader = &doctemplates.Band{Height: 30, Elements: []doctemplates.Item{
		{ID: "e1", Type: "text", Source: "field", Field: "customer", X: 0, Y: 2, Width: 45, Height: 8, FontSize: 12, Align: "left"},
		{ID: "e4", Type: "text", Source: "issue_number", X: 90, Y: 2, Width: 70, Height: 8, FontSize: 10, Align: "right"},
		{ID: "e5", Type: "image", X: 0, Y: 10, Width: 40, Height: 18, Image: ref},
		{ID: "e6", Type: "qrcode", Source: "field", Field: "customer", X: 50, Y: 0, Size: 28, ECC: "Q"},
	}}
	d.Bands.DocumentFooter = &doctemplates.Band{Height: 30, Elements: []doctemplates.Item{
		{ID: "e2", Type: "text", Source: "group_sum", Field: "amount", X: 0, Y: 1, Width: 60, Height: 8, FontSize: 10, Align: "right"},
		{ID: "e7", Type: "qrcode", Source: "issue_number", X: 100, Y: 0, Size: 25, ECC: "M"},
	}}
	return d
}

func TestIssuerIssuesASchema21TemplateWithItsImagesAndTheProductsNumber(t *testing.T) {
	ctx := context.Background()
	snap := groupSnapshot()
	owner := snap.Owner
	assets, e := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
	if e != nil {
		t.Fatal(e)
	}
	defer assets.Close()
	binding := doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}}
	stored, e := assets.Images.Put(ctx, owner.Identity, binding, logo(t))
	if e != nil {
		t.Fatal(e)
	}
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	ref := stored.Ref()
	v, e := store.Save(owner.Identity, "", 0, codedDocument(&ref))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Publish(owner.Identity, v.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	frozen, _ := doctemplates.Canonical(v.Layout)
	group, e := r.NewIssueGroup(snap, []string{"customer"}, []string{"乙"}, []int{1, 3})
	if e != nil {
		t.Fatal(e)
	}
	job := r.Job{ID: strings.Repeat("7", 32), Owner: owner, Kind: "issue", TemplateID: v.ID, TemplateVersion: 1, Template: string(frozen), Group: &group}
	renderer := &resolvedRenderer{fakeRenderer: fakeRenderer{out: []byte("%PDF-1.7 issued"), pages: 1}}
	issuer, _ := issuance.New(store, assets, renderer)
	// The check is asked before the product, with the images the owner has and no number: the renderer proves every number.
	if e := issuer.Check(ctx, job, snap); e != nil || renderer.checks != 1 || len(renderer.checkedWith.Images) != 1 || renderer.checkedWith.Number != "" {
		t.Fatalf("check: %v %+v", e, renderer.checkedWith)
	}
	if got := renderer.checkedWith.Images["e5"]; len(got) == 0 || !bytes.HasPrefix(got, []byte("\x89PNG")) {
		t.Errorf("the image the check was given: %d bytes", len(got))
	}
	if renderer.checkedValues["e6"] != "乙" || renderer.checkedValues["e1"] != "乙" {
		t.Errorf("values %v", renderer.checkedValues)
	}
	if _, has := renderer.checkedValues["e4"]; has {
		t.Error("the issue number was bound from the snapshot")
	}
	// The issue passes the number the product reserved, which is the stamp's, and the images; the plain Issue is never used.
	stamp := r.IssueStamp("SH-000042", issuedAt, job.ID)
	req := r.IssueRequest{Job: job, Snapshot: snap, Stamp: stamp, Number: "SH-000042", At: issuedAt}
	out, e := issuer.Issue(ctx, req)
	if e != nil || out.Pages != 1 || renderer.resolvedCalls != 1 || renderer.calls != 1 || renderer.issuedWith.Number != "SH-000042" || len(renderer.issuedWith.Images) != 1 || renderer.stamp != stamp {
		t.Fatalf("issue: %v %+v", e, renderer.issuedWith)
	}
	// A number that is not the stamp's, one the contract does not allow, or none: the document would show one number and stamp another.
	for name, number := range map[string]string{"another number": "SH-000043", "none": "", "a number with a space": "SH 42", "too long": strings.Repeat("a", 33)} {
		bad := req
		bad.Number = number
		before := renderer.resolvedCalls
		if _, e := issuer.Issue(ctx, bad); !errors.Is(e, r.ErrInvalid) || renderer.resolvedCalls != before {
			t.Errorf("%s: %v", name, e)
		}
	}
	// A renderer that cannot draw 2.1 cannot issue or check it: unsupported, before anything is rendered.
	plain := &fakeRenderer{out: []byte("%PDF-1.7 issued"), pages: 1}
	plainIssuer, _ := issuance.New(store, assets, plain)
	if _, e := plainIssuer.Issue(ctx, req); !errors.Is(e, r.ErrUnsupported) || plain.calls != 0 {
		t.Errorf("a renderer without 2.1, issued: %v", e)
	}
	if e := plainIssuer.Check(ctx, job, snap); !errors.Is(e, r.ErrUnsupported) || plain.checks != 0 {
		t.Errorf("a renderer without 2.1, checked: %v", e)
	}
	// The image of another owner is not found, and without an image store the template is unsupported: neither is rendered.
	foreign := job
	foreign.Owner = r.Owner{Identity: r.Identity{Tenant: owner.Identity.Tenant, Issuer: owner.Identity.Issuer, Subject: "other"}, Integration: "i"}
	renderer.checks = 0
	if e := issuer.Check(ctx, foreign, snap); !errors.Is(e, r.ErrNotFound) && !errors.Is(e, r.ErrUnpublished) {
		t.Errorf("another owner: %v", e)
	}
	without, _ := issuance.New(store, nil, renderer)
	if e := without.Check(ctx, job, snap); !errors.Is(e, r.ErrUnsupported) || renderer.checks != 0 {
		t.Errorf("without an image store: %v", e)
	}
	// A renderer's refusal of the layout for some number is the check's, before the product is asked.
	renderer.checkErr = doctemplates.Refuse(r.ErrUnsupported, doctemplates.Detail{Reason: doctemplates.ReasonDoesNotFit, Band: "page_header", Element: "e6"})
	if e := issuer.Check(ctx, job, snap); !errors.Is(e, r.ErrUnsupported) {
		t.Errorf("a layout refusal: %v", e)
	} else if d, ok := doctemplates.DetailOf(e); !ok || d.Element != "e6" {
		t.Errorf("the refusal lost its place: %+v", d)
	}
}
