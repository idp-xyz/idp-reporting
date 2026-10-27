package httpapi

import (
	"errors"
	"idp.local/reporting/dashboards"
	r "idp.local/reporting/reporting"
	"net/http"
	"strconv"
)

// WithDashboards mounts opt-in owner-private dashboard versions, distinct from report layouts.
func WithDashboards(fallback http.Handler, service *r.Service, authenticate Authenticate, store *dashboards.Store) (http.Handler, error) {
	if fallback == nil || service == nil || authenticate == nil || store == nil {
		return nil, r.ErrInvalid
	}
	describeBinding := func(req *http.Request, p r.Principal, binding dashboards.Binding) (r.Dataset, error) {
		catalog, e := service.Catalog(req.Context(), p, binding.Integration)
		if e != nil {
			return r.Dataset{}, e
		}
		for _, dataset := range catalog {
			if dataset.Ref == binding.Dataset {
				return dataset, nil
			}
		}
		return r.Dataset{}, r.ErrDenied
	}
	authorize := func(req *http.Request, p r.Principal, d dashboards.Document) error {
		if e := dashboards.Validate(d); e != nil {
			return e
		}
		dataset, e := describeBinding(req, p, d.Binding)
		if e != nil {
			return e
		}
		columns := map[string]r.Column{}
		metrics := map[string]bool{}
		for _, c := range dataset.Columns {
			columns[c.ID] = c
		}
		for _, m := range dataset.Metrics {
			metrics[m.ID] = true
		}
		for _, w := range d.Widgets {
			if w.Type == "metric" {
				c, ok := columns[*w.Field]
				if !ok || !metrics[*w.Field] || (c.Type != "decimal" && c.Type != "integer") {
					return r.ErrDenied
				}
			} else if w.Type == "bar" {
				c, ok := columns[*w.Field]
				if !ok || (c.Type != "decimal" && c.Type != "integer") {
					return r.ErrDenied
				}
				if _, ok := columns[*w.Category]; !ok {
					return r.ErrDenied
				}
			} else {
				for _, id := range w.Columns {
					if _, ok := columns[id]; !ok {
						return r.ErrDenied
					}
				}
			}
		}
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	const base = "/api/reporting/v1/dashboards"
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
			out = append(out, map[string]any{"id": entry.ID, "version": entry.Version, "binding": entry.Layout.Binding})
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
		if _, e = describeBinding(req, p, entry.Layout.Binding); e != nil {
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
			if _, e = describeBinding(req, p, entry.Layout.Binding); e != nil {
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
			ID       string              `json:"id"`
			Expected int                 `json:"expected_version"`
			Config   dashboards.Document `json:"config"`
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
	return mux, nil
}
