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
type Renderer interface {
	Issue(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData, string, time.Time) ([]byte, int, error)
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

// Issue renders one issue job. The frozen configuration must still be the template's published
// version (the same version number and the same canonical bytes), else ErrUnpublished: a
// withdrawn or superseded template issues nothing, however long the job waited. The values are
// bound from the snapshot the worker verified, never from the request. The request's issue
// time (S03-H) is the product's: a request without one is invalid, and the renderer dates the
// document with it, so that the same request renders the same bytes.
func (i *Issuer) Issue(ctx context.Context, req r.IssueRequest) (r.IssueResult, error) {
	if e := ctx.Err(); e != nil {
		return r.IssueResult{}, e
	}
	j := req.Job
	if j.Kind != "issue" || j.Template == "" || req.Stamp == "" || req.At.IsZero() {
		return r.IssueResult{}, r.ErrInvalid
	}
	var d doctemplates.Document
	if e := r.DecodeStrict(strings.NewReader(j.Template), &d); e != nil {
		return r.IssueResult{}, r.ErrIntegrity
	}
	frozen, e := doctemplates.Canonical(d)
	if e != nil {
		return r.IssueResult{}, e
	}
	if string(frozen) != j.Template {
		return r.IssueResult{}, r.ErrIntegrity
	}
	published, e := i.store.Published(j.Owner.Identity, j.TemplateID)
	if errors.Is(e, r.ErrNotFound) {
		return r.IssueResult{}, r.ErrUnpublished
	}
	if e != nil {
		return r.IssueResult{}, e
	}
	current, e := doctemplates.Canonical(published.Layout)
	if e != nil {
		return r.IssueResult{}, e
	}
	if published.Version != j.TemplateVersion || !bytes.Equal(current, frozen) {
		return r.IssueResult{}, r.ErrUnpublished
	}
	values, e := doctemplates.Bind(d, req.Snapshot, j.RowIndex)
	if e != nil {
		return r.IssueResult{}, e
	}
	table, e := doctemplates.BindTable(d, req.Snapshot)
	if e != nil {
		return r.IssueResult{}, e
	}
	var background []byte
	if d.Background != nil {
		if i.assets == nil {
			return r.IssueResult{}, r.ErrUnsupported
		}
		if background, e = i.assets.Resolve(j.Owner.Identity, d.Binding, *d.Background); e != nil {
			return r.IssueResult{}, e
		}
	}
	if d.Print != nil && d.Print.Mode == "variables" {
		background = nil
	}
	pdf, pages, e := i.renderer.Issue(ctx, d, values, background, table, req.Stamp, req.At)
	if e != nil {
		return r.IssueResult{}, e
	}
	if len(pdf) < 8 || !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) > r.MaxArtifactBytes || pages < 1 || pages > r.MaxIssuePages {
		return r.IssueResult{}, r.ErrIntegrity
	}
	return r.IssueResult{PDF: pdf, Pages: pages}, nil
}

var _ r.Issuer = (*Issuer)(nil)
