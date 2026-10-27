package httpapi

import (
	"errors"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"net/http"
	"strconv"
)

// mountPrivateDocuments shares immutable metadata/version/release HTTP behavior.
func mountPrivateDocuments[T any](mux *http.ServeMux, base string, authenticate Authenticate, store *privatedocs.Store[T], authorize func(*http.Request, r.Principal, T) error, authorizeBinding func(*http.Request, r.Principal, T) error, binding func(T) any) {
	mux.HandleFunc("GET "+base, authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		all, e := store.List(p.Identity)
		if e != nil {
			return e
		}
		out := []map[string]any{}
		for _, entry := range all {
			if e = authorize(req, p, entry.Layout); e != nil {
				if errors.Is(e, r.ErrDenied) || errors.Is(e, r.ErrNotFound) {
					continue
				}
				return e
			}
			out = append(out, map[string]any{"id": entry.ID, "version": entry.Version, "binding": binding(entry.Layout)})
		}
		return send(w, 200, out)
	}))
	mux.HandleFunc("GET "+base+"/{id}", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		q := req.URL.Query()
		if len(q) != 1 || len(q["version"]) != 1 {
			return r.ErrInvalid
		}
		version, e := strconv.Atoi(q.Get("version"))
		if e != nil {
			return r.ErrInvalid
		}
		entry, e := store.Get(p.Identity, req.PathValue("id"), version)
		if e != nil {
			return e
		}
		if e = authorize(req, p, entry.Layout); e != nil {
			return e
		}
		return send(w, 200, map[string]any{"id": entry.ID, "version": entry.Version, "config": entry.Layout})
	}))
	// History/withdrawal authorize the fixed binding; publish/read authorize actual target fields.
	mux.HandleFunc("GET "+base+"/{id}/release", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		entry, e := store.Get(p.Identity, req.PathValue("id"), 1)
		if e != nil {
			return e
		}
		if e = authorizeBinding(req, p, entry.Layout); e != nil {
			return e
		}
		state, e := store.Release(p.Identity, entry.ID)
		if e != nil {
			return e
		}
		return send(w, 200, state)
	}))
	mux.HandleFunc("GET "+base+"/{id}/published", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		entry, e := store.Published(p.Identity, req.PathValue("id"))
		if e != nil {
			return e
		}
		if e = authorize(req, p, entry.Layout); e != nil {
			return e
		}
		return send(w, 200, map[string]any{"id": entry.ID, "version": entry.Version, "config": entry.Layout})
	}))
	mux.HandleFunc("POST "+base+"/{id}/release", authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			Action   string `json:"action"`
			Version  int    `json:"version"`
			Expected int    `json:"expected_revision"`
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		if input.Action != "publish" && input.Action != "withdraw" {
			return r.ErrInvalid
		}
		target := 1
		if input.Action == "publish" {
			target = input.Version
		}
		entry, e := store.Get(p.Identity, req.PathValue("id"), target)
		if e != nil {
			return e
		}
		if input.Action == "publish" {
			if e = authorize(req, p, entry.Layout); e != nil {
				return e
			}
		} else {
			if e = authorizeBinding(req, p, entry.Layout); e != nil {
				return e
			}
		}
		state, e := store.Publish(p.Identity, entry.ID, input.Action, input.Version, input.Expected)
		if e != nil {
			return e
		}
		return send(w, 200, state)
	}))
	mux.HandleFunc("POST "+base, authenticated(authenticate, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			ID       string `json:"id"`
			Expected int    `json:"expected_version"`
			Config   T      `json:"config"`
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		if e := authorize(req, p, input.Config); e != nil {
			return e
		}
		entry, e := store.Save(p.Identity, input.ID, input.Expected, input.Config)
		if e != nil {
			return e
		}
		return send(w, 201, map[string]any{"id": entry.ID, "version": entry.Version, "config": entry.Layout})
	}))

}
