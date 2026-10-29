// Package issuance renders issue jobs (S03-G): the frozen template of a job, bound to one row
// of the job's frozen snapshot (a 1.x template) or to the row group the job froze (a schema 2.0
// template, S03-K), stamped with the issue number. It is the reporting.Issuer the worker calls;
// it never admits anything itself.
package issuance

import (
	"bytes"
	"context"
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"slices"
	"strings"
	"time"
)

// Renderer draws an issued document: export/pdf.Writer.Issue. Its last two arguments are the
// stamp and the issue time the product reserved, which is also the PDF's creation date.
// CheckIssue is the layout of Issue without the stamp and without the render, in the same
// arguments: it refuses what Issue refuses for the document, its values, background and table,
// so that it can be asked before the product reserves a number.
type Renderer interface {
	Issue(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData, string, time.Time) ([]byte, int, error)
	CheckIssue(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) error
}

// ResolvedRenderer is the part of the renderer that draws a schema 2.1 template (ADR 0005, S03-M), which needs the bytes of its
// images and, when issued, the product's number: export/pdf.Writer.IssueWith and CheckIssueWith. A renderer without it cannot issue
// a 2.1 template, which is then unsupported. CheckIssueWith proves the layout for every number the product may assign.
type ResolvedRenderer interface {
	IssueWith(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData, doctemplates.Resolved, string, time.Time) ([]byte, int, error)
	CheckIssueWith(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData, doctemplates.Resolved) error
}

// Issuer renders issue jobs against the template store they were admitted from. Assets may be
// nil on a deployment without backgrounds and images: a template naming one is then unsupported.
type Issuer struct {
	store    *doctemplates.Store
	assets   *doctemplates.Assets
	renderer Renderer
}

func New(store *doctemplates.Store, assets *doctemplates.Assets, renderer Renderer) (*Issuer, error) {
	if store == nil || renderer == nil {
		return nil, r.ErrInvalid
	}
	return &Issuer{store: store, assets: assets, renderer: renderer}, nil
}

// document is what the checks of a job make of it: the frozen template, the values the row and
// the table bind and the background that resolves, ready for the renderer.
type document struct {
	d          doctemplates.Document
	values     map[string]string
	table      *doctemplates.TableData
	background []byte
	images     map[string][]byte // a 2.1 template's images by element id
}

// prepare makes the document of a job and its verified snapshot, and is the whole of what can be
// known without rendering. The frozen configuration must still be the template's published
// version (the same version number and the same canonical bytes), else ErrUnpublished: a
// withdrawn or superseded template issues nothing, however long the job waited. The values are
// bound from the snapshot the worker verified, never from the request.
func (i *Issuer) prepare(j r.Job, snap r.Snapshot) (document, error) {
	if j.Kind != "issue" || j.Template == "" {
		return document{}, r.ErrInvalid
	}
	var d doctemplates.Document
	if e := r.DecodeStrict(strings.NewReader(j.Template), &d); e != nil {
		return document{}, r.ErrIntegrity
	}
	frozen, e := doctemplates.Canonical(d)
	if e != nil {
		return document{}, e
	}
	// A 2.0 template is issued by row group (S03-K), a 1.x template by row: a job that names a 2.0 template
	// without a group could only be bound by row, and a 1.x template with a group has none to bind.
	banded := d.Banded()
	switch {
	case banded && j.Group == nil:
		return document{}, r.ErrUnsupported
	case !banded && j.Group != nil:
		return document{}, r.ErrInvalid
	}
	if string(frozen) != j.Template {
		return document{}, r.ErrIntegrity
	}
	published, e := i.store.Published(j.Owner.Identity, j.TemplateID)
	if errors.Is(e, r.ErrNotFound) {
		return document{}, r.ErrUnpublished
	}
	if e != nil {
		return document{}, e
	}
	current, e := doctemplates.Canonical(published.Layout)
	if e != nil {
		return document{}, e
	}
	if published.Version != j.TemplateVersion || !bytes.Equal(current, frozen) {
		return document{}, r.ErrUnpublished
	}
	if banded {
		doc, e := bindGroup(d, j, snap)
		if e != nil || !d.Coded() {
			return doc, e
		}
		// The images a 2.1 template draws are resolved with the same authorization as a background: the owner's, of the
		// template's binding, with the digest the template names.
		if doc.images, e = i.resolveImages(j.Owner.Identity, d); e != nil {
			return document{}, e
		}
		return doc, nil
	}
	values, e := doctemplates.Bind(d, snap, j.RowIndex)
	if e != nil {
		return document{}, e
	}
	table, e := doctemplates.BindTable(d, snap)
	if e != nil {
		return document{}, e
	}
	var background []byte
	if d.Background != nil {
		if i.assets == nil {
			return document{}, r.ErrUnsupported
		}
		if background, e = i.assets.Resolve(j.Owner.Identity, d.Binding, *d.Background); e != nil {
			return document{}, e
		}
	}
	if d.Print != nil && d.Print.Mode == "variables" {
		background = nil
	}
	return document{d: d, values: values, table: table, background: background}, nil
}

// resolveImages returns the bytes of each image element of d by element id.
func (i *Issuer) resolveImages(owner r.Identity, d doctemplates.Document) (map[string][]byte, error) {
	images := map[string][]byte{}
	for _, bi := range d.Items() {
		if bi.Item.Type != doctemplates.TypeImage {
			continue
		}
		if i.assets == nil || i.assets.Images == nil {
			return nil, r.ErrUnsupported
		}
		data, e := i.assets.Images.Resolve(owner, d.Binding, *bi.Item.Image)
		if e != nil {
			return nil, e
		}
		images[bi.Item.ID] = data
	}
	return images, nil
}

// bindGroup makes the document of a group job: the frozen group must be the verified snapshot's, whole, under
// the template's own group fields, and its values and table are bound from the snapshot's rows, never from the
// request. A schema 2.0 document has no background.
func bindGroup(d doctemplates.Document, j r.Job, snap r.Snapshot) (document, error) {
	var fields []string
	if d.Group != nil {
		fields = d.Group.Fields
	}
	if !slices.Equal(fields, j.Group.Fields) || r.VerifyGroup(snap, *j.Group) != nil {
		return document{}, r.ErrIntegrity
	}
	values, table, e := doctemplates.BindGroup(d, snap, doctemplates.RowGroup{Key: j.Group.Key, Rows: j.Group.Rows})
	if e != nil {
		return document{}, e
	}
	return document{d: d, values: values, table: table}, nil
}

// Check verifies what an issue job needs that does not depend on the product or on rendering
// (S03-H): the frozen configuration decodes, is canonical and is still the published version,
// the row and the table bind (a 2.0 template, S03-K: the frozen group is the snapshot's, whole, under
// the template's own group fields, and binds as a preview binds it), the background resolves (or is unsupported) and the renderer's
// layout accepts the values. The worker asks it before it asks the product for a number, so that
// none of these refusals costs the product a reservation it must void. It takes no stamp and no
// issue time, runs no Typst and fails exactly as Issue does for the same reasons; Issue repeats
// all of it, because the template can be withdrawn between the two calls.
func (i *Issuer) Check(ctx context.Context, j r.Job, snap r.Snapshot) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	doc, e := i.prepare(j, snap)
	if e != nil {
		return e
	}
	if doc.d.Coded() {
		rr, ok := i.renderer.(ResolvedRenderer)
		if !ok {
			return r.ErrUnsupported
		}
		return rr.CheckIssueWith(ctx, doc.d, doc.values, doc.background, doc.table, doctemplates.Resolved{Images: doc.images})
	}
	return i.renderer.CheckIssue(ctx, doc.d, doc.values, doc.background, doc.table)
}

// Issue renders one issue job, after the checks of Check. The request's issue time (S03-H) is
// the product's: a request without one is invalid, and the renderer dates the document with it,
// so that the same request renders the same bytes.
func (i *Issuer) Issue(ctx context.Context, req r.IssueRequest) (r.IssueResult, error) {
	if e := ctx.Err(); e != nil {
		return r.IssueResult{}, e
	}
	if req.Job.Kind != "issue" || req.Job.Template == "" || req.Stamp == "" || req.At.IsZero() {
		return r.IssueResult{}, r.ErrInvalid
	}
	doc, e := i.prepare(req.Job, req.Snapshot)
	if e != nil {
		return r.IssueResult{}, e
	}
	var pdf []byte
	var pages int
	if doc.d.Coded() {
		rr, ok := i.renderer.(ResolvedRenderer)
		if !ok {
			return r.IssueResult{}, r.ErrUnsupported
		}
		// The number the worker passes is the one in the stamp: a document that shows it must show the product's.
		if !r.ValidIssueNumber(req.Number) || !strings.HasPrefix(req.Stamp, "ISSUED No. "+req.Number+" - ") {
			return r.IssueResult{}, r.ErrInvalid
		}
		pdf, pages, e = rr.IssueWith(ctx, doc.d, doc.values, doc.background, doc.table, doctemplates.Resolved{Images: doc.images, Number: req.Number}, req.Stamp, req.At)
	} else {
		pdf, pages, e = i.renderer.Issue(ctx, doc.d, doc.values, doc.background, doc.table, req.Stamp, req.At)
	}
	if e != nil {
		return r.IssueResult{}, e
	}
	if len(pdf) < 8 || !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) > r.MaxArtifactBytes || pages < 1 || pages > r.MaxIssuePages {
		return r.IssueResult{}, r.ErrIntegrity
	}
	return r.IssueResult{PDF: pdf, Pages: pages}, nil
}

var _ r.Issuer = (*Issuer)(nil)
