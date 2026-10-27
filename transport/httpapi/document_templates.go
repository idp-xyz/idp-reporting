package httpapi

import (
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
	return mux, nil
}
