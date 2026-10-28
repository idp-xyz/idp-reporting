// Package issuance renders issue jobs (S03-G): the frozen template of a job, bound to one row
// of the job's frozen snapshot, stamped with the issue number. It is the reporting.Issuer the
// worker calls; it never admits anything itself.
package issuance

import (
	"bytes"
	"context"
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
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

// Issuer renders issue jobs against the template store they were admitted from. Assets may be
// nil on a deployment without backgrounds: a template naming one is then unsupported.
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

// Check verifies what an issue job needs that does not depend on the product or on rendering
// (S03-H): the frozen configuration decodes, is canonical and is still the published version,
// the row and the table bind, the background resolves (or is unsupported) and the renderer's
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
	pdf, pages, e := i.renderer.Issue(ctx, doc.d, doc.values, doc.background, doc.table, req.Stamp, req.At)
	if e != nil {
		return r.IssueResult{}, e
	}
	if len(pdf) < 8 || !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) > r.MaxArtifactBytes || pages < 1 || pages > r.MaxIssuePages {
		return r.IssueResult{}, r.ErrIntegrity
	}
	return r.IssueResult{PDF: pdf, Pages: pages}, nil
}

var _ r.Issuer = (*Issuer)(nil)
