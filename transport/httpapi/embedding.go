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
		return send(w, 200, capabilities(service, p, config.Configured(p.Integrations)))
	}))
	return secured(mux), nil
}

// MaxProfileLabel bounds the profile label of WithProfile.
const MaxProfileLabel = 64

// ValidProfileLabel says whether text may be shown as the profile: 1 to 64 printable ASCII characters without a space at either end.
func ValidProfileLabel(label string) bool {
	if label == "" || len(label) > MaxProfileLabel || label[0] == ' ' || label[len(label)-1] == ' ' {
		return false
	}
	for _, c := range []byte(label) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// WithProfile answers the capabilities of an authenticated caller with the given profile label (the production server's profile_label) in place
// of the demo's, and with `embedding` as WithEmbedding computes it (config may be nil: nothing is configured). It changes nothing else:
// production_approved stays false, and no parameter can make it true.
func WithProfile(fallback http.Handler, service *r.Service, authenticate Authenticate, label string, config *embedding.Config) (http.Handler, error) {
	if fallback == nil || service == nil || authenticate == nil || !ValidProfileLabel(label) {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	mux.HandleFunc("GET /api/reporting/v1/capabilities", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		return send(w, 200, capabilitiesLabelled(service, p, config.Configured(p.Integrations), label))
	}))
	return secured(mux), nil
}

// Secured wraps a handler of a host's own routes (the production server's health and readiness answers) in the headers of every answer of
// this API.
func Secured(next http.Handler) http.Handler { return secured(next) }

// The values of X-Reporting-Data-Class. The core API and every mount say DataClassSimulator, as they always did; a production server
// (S05-A) wraps its whole handler in WithDataClass so that its answers say DataClassBusiness, and a server that allows simulators keeps the first.
//
// The header says what kind of server answers, on every answer, a 404 included. It does not say where one dataset comes from: that is
// the data class of a result's evidence and of a catalog entry (S08-A, ADR 0012 decision 6), the word of the connection's adapter, which
// has a third value, manual-entry. The two meet in one rule: a server whose header says business-data has no connection whose data
// class is contract-simulator (it refuses to start with one); a server whose header says contract-simulator may serve data of any class.
const (
	DataClassSimulator = r.DataClassSimulator
	DataClassBusiness  = r.DataClassBusiness
)

// WithDataClass makes every answer of next carry exactly one X-Reporting-Data-Class, the given class, whatever the routes beneath set: the API's
// own, the health endpoints, the console, a 404, a 405, a redirect. Only the two classes above are accepted.
func WithDataClass(next http.Handler, class string) (http.Handler, error) {
	if next == nil || (class != DataClassSimulator && class != DataClassBusiness) {
		return nil, r.ErrInvalid
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		next.ServeHTTP(&classWriter{ResponseWriter: w, class: class}, req)
	}), nil
}

type classWriter struct {
	http.ResponseWriter
	class string
	sent  bool
}

func (c *classWriter) stamp() {
	if !c.sent {
		c.sent = true
		c.Header().Set("X-Reporting-Data-Class", c.class)
	}
}
func (c *classWriter) WriteHeader(code int) { c.stamp(); c.ResponseWriter.WriteHeader(code) }
func (c *classWriter) Write(b []byte) (int, error) {
	c.stamp()
	return c.ResponseWriter.Write(b)
}
func (c *classWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
