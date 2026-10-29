package httpapi

import (
	"idp.local/reporting/embedding"
	r "idp.local/reporting/reporting"
	"net/http"
)

// WithEmbedding tells an authenticated caller whether embedding is configured for its integrations (ADR 0006): the capabilities
// answer gains `embedding: true` when at least one of the caller's integrations has an allowed embedding origin, and never says
// which origins. The embed pages themselves are served by the console package, which holds the same configuration; this mount adds
// no other route and grants nothing. A nil configuration is refused: leave the mount out instead.
func WithEmbedding(fallback http.Handler, service *r.Service, authenticate Authenticate, config *embedding.Config) (http.Handler, error) {
	if fallback == nil || service == nil || authenticate == nil || config == nil {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	mux.HandleFunc("GET /api/reporting/v1/capabilities", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		return send(w, 200, capabilities(service, config.Configured(p.Integrations)))
	}))
	return secured(mux), nil
}
