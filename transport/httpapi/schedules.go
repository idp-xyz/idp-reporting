package httpapi

import (
	"io"
	"net/http"
	"strconv"

	r "idp.local/reporting/reporting"
)

// WithSchedules mounts the scheduled reports of S16-A (docs/S16A_SCOPE.md) under /api/reporting/v1/schedules: a person creates, lists, reads,
// pauses, resumes and deletes their own schedules and lists a schedule's runs. Every route is a viewer's (RequiredRole: the role of the jobs a
// run submits), and every change is recorded as schedule.change (AuditAction). The delivery endpoint is the deployment's: no route reads or
// writes it (ADR 0016's rule for connections and bindings).
func WithSchedules(fallback http.Handler, service *r.Service, authenticate Authenticate, schedules *r.Schedules) (http.Handler, error) {
	if fallback == nil || service == nil || authenticate == nil || schedules == nil {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	bind := func(pattern string, fn func(http.ResponseWriter, *http.Request, r.Principal) error) {
		mux.HandleFunc(pattern, authenticated(authenticate, fn))
	}
	const base = "/api/reporting/v1/schedules"
	bind("POST "+base, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var in r.ScheduleInput
		if e := decode(w, req, &in, bodyQuery); e != nil {
			return e
		}
		key, e := idem(req)
		if e != nil {
			return e
		}
		v, e := schedules.Create(req.Context(), p, in, key)
		if e != nil {
			return e
		}
		w.Header().Set("Location", base+"/"+v.ID)
		return send(w, 201, v)
	})
	bind("GET "+base, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		l, e := scheduleListing(req)
		if e != nil {
			return e
		}
		v, e := schedules.List(req.Context(), p, l)
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("GET "+base+"/{id}", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		if len(req.URL.Query()) != 0 {
			return r.ErrInvalid
		}
		v, e := schedules.Get(req.Context(), p, req.PathValue("id"))
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	state := func(to string) func(http.ResponseWriter, *http.Request, r.Principal) error {
		return func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			b, e := io.ReadAll(io.LimitReader(req.Body, 1))
			if e != nil || len(b) != 0 || len(req.URL.Query()) != 0 {
				return r.ErrInvalid
			}
			v, e := schedules.SetState(req.Context(), p, req.PathValue("id"), to)
			if e != nil {
				return e
			}
			return send(w, 200, v)
		}
	}
	bind("POST "+base+"/{id}/pause", state(r.SchedulePaused))
	bind("POST "+base+"/{id}/resume", state(r.ScheduleActive))
	bind("DELETE "+base+"/{id}", state(r.ScheduleDeleted))
	bind("GET "+base+"/{id}/runs", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		l, e := scheduleListing(req)
		if e != nil {
			return e
		}
		if l.Limit > r.MaxRunListing {
			return r.ErrInvalid
		}
		v, e := schedules.Runs(req.Context(), p, req.PathValue("id"), l)
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	return secured(mux), nil
}

// scheduleListing reads a listing's query: limit (1..100, default 50) and before (the id of the last entry of the previous page), each at most
// once and not empty; any other key is ErrInvalid.
func scheduleListing(req *http.Request) (r.ScheduleListing, error) {
	l := r.ScheduleListing{Limit: 50}
	for key, v := range req.URL.Query() {
		if len(v) != 1 || v[0] == "" {
			return l, r.ErrInvalid
		}
		switch key {
		case "limit":
			n, e := strconv.Atoi(v[0])
			if e != nil || n < 1 || n > r.MaxScheduleListing {
				return l, r.ErrInvalid
			}
			l.Limit = n
		case "before":
			l.Before = v[0]
		default:
			return l, r.ErrInvalid
		}
	}
	if !l.Valid(r.MaxScheduleListing) {
		return l, r.ErrInvalid
	}
	return l, nil
}
