package httpapi

import (
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"net/http"
	"strconv"
)

// WithDocumentTemplates mounts owner-private template metadata, independently of the optional PDF runtime.
func WithDocumentTemplates(fallback http.Handler, service *r.Service, authenticate Authenticate, store *doctemplates.Store, renderer DocumentRenderer, assets *doctemplates.Assets) (http.Handler, error) {
	if fallback == nil || service == nil || authenticate == nil || store == nil {
		return nil, r.ErrInvalid
	}
	authorize := func(req *http.Request, p r.Principal, d doctemplates.Document) error {
		return authorizeDocument(req, service, p, d, assets)
	}
	describeBinding := func(req *http.Request, p r.Principal, b doctemplates.Binding) (r.Dataset, error) {
		return describeDocumentBinding(req, service, p, b)
	}
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	const base = "/api/reporting/v1/document-templates"
	mountPrivateDocuments(mux, base, authenticate, store, authorize, func(req *http.Request, p r.Principal, d doctemplates.Document) error {
		_, e := describeBinding(req, p, d.Binding)
		return e
	}, func(d doctemplates.Document) any { return d.Binding })
	if renderer != nil {
		mux.HandleFunc("POST "+base+"/{id}/previews", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			var input struct {
				Version    *int   `json:"version"`
				SnapshotID string `json:"snapshot_id"`
				RowIndex   *int   `json:"row_index"`
			}
			if e := decode(req, &input); e != nil {
				return e
			}
			if input.Version == nil || input.RowIndex == nil {
				return r.ErrInvalid
			}
			entry, e := store.Get(p.Identity, req.PathValue("id"), *input.Version)
			if e != nil {
				return e
			}
			w.Header().Set("X-Template-ID", entry.ID)
			w.Header().Set("X-Template-Version", strconv.Itoa(entry.Version))
			return renderDocumentPreview(w, req, service, p, renderer, entry.Layout, input.SnapshotID, *input.RowIndex, assets)
		}))
	}
	// Official issue (S03-G): the published version, frozen into a durable job; the row is
	// bound here once so that a wrong row, a field the snapshot lacks or a value the previews
	// could not print as the template binds it is refused at once, and again by the worker from
	// the stored snapshot. What only the renderer's layout can refuse (a value too wide for its
	// box, a table that cannot be laid out) is not asked here: the worker's issuer check finds it
	// before the source product is asked for a number (S03-H), so that it costs the product no
	// reservation to void, and the render repeats it. Mounted with the templates whether or not
	// the service can issue, so that a deployment without the renderer answers 422, not 404; so
	// does an integration whose adapter cannot issue (S03-H): the source product numbers and
	// confirms every document.
	{
		mux.HandleFunc("POST /api/reporting/v1/issues", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			var input struct {
				TemplateID string `json:"template_id"`
				Version    *int   `json:"version"`
				SnapshotID string `json:"snapshot_id"`
				RowIndex   *int   `json:"row_index"`
			}
			if e := decode(req, &input); e != nil {
				return e
			}
			if input.Version == nil || input.RowIndex == nil {
				return r.ErrInvalid
			}
			key, e := idem(req)
			if e != nil {
				return e
			}
			if !service.CanIssue() {
				return r.ErrUnsupported
			}
			if _, e = store.Get(p.Identity, input.TemplateID, *input.Version); e != nil {
				return e
			}
			entry, e := store.Published(p.Identity, input.TemplateID)
			if errors.Is(e, r.ErrNotFound) || (e == nil && entry.Version != *input.Version) {
				return r.ErrUnpublished
			}
			if e != nil {
				return e
			}
			if e = authorize(req, p, entry.Layout); e != nil {
				return e
			}
			frozen, e := doctemplates.Canonical(entry.Layout)
			if e != nil {
				return e
			}
			snap, e := service.AuthorizedSnapshot(req.Context(), p, input.SnapshotID)
			if e != nil {
				return e
			}
			if _, e = doctemplates.Bind(entry.Layout, snap, *input.RowIndex); e != nil {
				return e
			}
			if _, e = doctemplates.BindTable(entry.Layout, snap); e != nil {
				return e
			}
			v, e := service.SubmitIssue(req.Context(), p, r.IssueInput{TemplateID: entry.ID, TemplateVersion: entry.Version, Template: frozen, SnapshotID: snap.ID, RowIndex: *input.RowIndex}, key)
			if e != nil {
				return e
			}
			w.Header().Set("Location", "/api/reporting/v1/jobs/"+v.ID)
			return send(w, 202, v)
		}))
	}
	return mux, nil
}
