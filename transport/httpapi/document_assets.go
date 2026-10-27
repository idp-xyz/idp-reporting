package httpapi

import (
	"encoding/base64"
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"net/http"
)

// WithDocumentAssets enables bounded PNG ingestion; raw assets are never public.
func WithDocumentAssets(fallback http.Handler, s *r.Service, auth Authenticate, assets *doctemplates.Assets) (http.Handler, error) {
	if fallback == nil || s == nil || auth == nil || assets == nil {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	const base = "/api/reporting/v1/document-backgrounds"
	mux.HandleFunc("POST "+base, authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			Binding doctemplates.Binding `json:"binding"`
			PNG     string               `json:"png"`
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		if _, e := describeDocumentBinding(req, s, p, input.Binding); e != nil {
			return e
		}
		if len(input.PNG) > base64.StdEncoding.EncodedLen(doctemplates.MaxBackgroundBytes) {
			return r.ErrBudget
		}
		data, e := base64.StdEncoding.Strict().DecodeString(input.PNG)
		if e != nil {
			return r.ErrInvalid
		}
		ref, e := assets.Put(p.Identity, input.Binding, data)
		if e != nil {
			return e
		}
		return send(w, 201, ref)
	}))
	mux.HandleFunc("GET "+base, authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		all, e := assets.List(p.Identity)
		if e != nil {
			return e
		}
		out := []doctemplates.BackgroundSummary{}
		for _, v := range all {
			if _, e = describeDocumentBinding(req, s, p, v.Binding); e != nil {
				if errors.Is(e, r.ErrDenied) || errors.Is(e, r.ErrNotFound) {
					continue
				}
				return e
			}
			out = append(out, v)
		}
		return send(w, 200, out)
	}))
	return mux, nil
}

func resolveDocumentBackground(p r.Principal, d doctemplates.Document, assets *doctemplates.Assets) ([]byte, error) {
	if d.Background == nil {
		return nil, nil
	}
	if assets == nil {
		return nil, r.ErrUnavailable
	}
	return assets.Resolve(p.Identity, d.Binding, *d.Background)
}
