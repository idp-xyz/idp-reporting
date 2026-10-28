package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"net/http"
)

type DocumentRenderer interface {
	Render(context.Context, doctemplates.Document, map[string]string, []byte) ([]byte, error)
}

// WithDocumentPreviews mounts opt-in ephemeral single-row previews; no issuance or artifact history.
func WithDocumentPreviews(fallback http.Handler, s *r.Service, auth Authenticate, renderer DocumentRenderer, assets *doctemplates.Assets) (http.Handler, error) {
	if fallback == nil || s == nil || auth == nil || renderer == nil {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	mux.HandleFunc("POST /api/reporting/v1/document-previews", authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			Config     doctemplates.Document `json:"config"`
			SnapshotID string                `json:"snapshot_id"`
			RowIndex   *int                  `json:"row_index"`
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		if input.RowIndex == nil {
			return r.ErrInvalid
		}
		if e := doctemplates.Validate(input.Config); e != nil {
			return e
		}
		return renderDocumentPreview(w, req, s, p, renderer, input.Config, input.SnapshotID, *input.RowIndex, assets)
	}))
	return mux, nil
}

func describeDocumentBinding(req *http.Request, s *r.Service, p r.Principal, b doctemplates.Binding) (r.Dataset, error) {
	catalog, e := s.Catalog(req.Context(), p, b.Integration)
	if e != nil {
		return r.Dataset{}, e
	}
	for _, d := range catalog {
		if d.Ref == b.Dataset {
			return d, nil
		}
	}
	return r.Dataset{}, r.ErrDenied
}
func authorizeDocument(req *http.Request, s *r.Service, p r.Principal, config doctemplates.Document, assets *doctemplates.Assets) error {
	if e := doctemplates.Validate(config); e != nil {
		return e
	}
	if _, e := resolveDocumentBackground(p, config, assets); e != nil {
		return e
	}
	d, e := describeDocumentBinding(req, s, p, config.Binding)
	if e != nil {
		return e
	}
	columns := map[string]bool{}
	metrics := map[string]bool{}
	for _, c := range d.Columns {
		columns[c.ID] = true
	}
	for _, m := range d.Metrics {
		metrics[m.ID] = true
	}
	for _, v := range config.Elements {
		if v.Source != "literal" && (!columns[v.Field] || (v.Source == "total" && !metrics[v.Field])) {
			return r.ErrDenied
		}
	}
	return nil
}
func renderDocumentPreview(w http.ResponseWriter, req *http.Request, s *r.Service, p r.Principal, renderer DocumentRenderer, config doctemplates.Document, snapshotID string, index int, assets *doctemplates.Assets) error {
	snap, e := s.AuthorizedSnapshot(req.Context(), p, snapshotID)
	if e != nil {
		return e
	}
	values, e := doctemplates.Bind(config, snap, index)
	if e != nil {
		return e
	}

	if e = authorizeDocument(req, s, p, config, assets); e != nil {
		return e
	}
	background, e := resolveDocumentBackground(p, config, assets)
	if e != nil {
		return e
	}
	if config.Print != nil && config.Print.Mode == "variables" {
		background = nil
	}
	pdf, e := renderer.Render(req.Context(), config, values, background)
	if e != nil {
		return e
	}
	if len(pdf) > r.MaxArtifactBytes || len(pdf) < 8 || string(pdf[:5]) != "%PDF-" {
		return r.ErrIntegrity
	}
	if _, e = s.AuthorizedSnapshot(req.Context(), p, snapshotID); e != nil {
		return e
	}
	if e = authorizeDocument(req, s, p, config, assets); e != nil {
		return e
	}
	digest := sha256.Sum256(pdf)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="document-preview.pdf"`)
	w.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
	w.WriteHeader(200)
	_, e = w.Write(pdf)
	return e
}
