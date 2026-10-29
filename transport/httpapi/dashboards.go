package httpapi

import (
	"idp.local/reporting/dashboards"
	r "idp.local/reporting/reporting"
	"net/http"
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
	mountPrivateDocuments(mux, base, authenticate, store, authorize, func(req *http.Request, p r.Principal, d dashboards.Document) error {
		_, e := describeBinding(req, p, d.Binding)
		return e
	}, func(d dashboards.Document) any { return d.Binding })
	return secured(mux), nil
}
