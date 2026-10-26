// Package httpapi exposes the contract-simulator profile, not production SSO.
package httpapi

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	r "idp.local/reporting/reporting"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

type Authenticate func(*http.Request) (r.Principal, error)

func DemoBearer(secret string, p r.Principal) (Authenticate, error) {
	if len(secret) < 32 || !p.Identity.Valid() || len(p.Integrations) == 0 {
		return nil, r.ErrInvalid
	}
	p.Integrations = slices.Clone(p.Integrations)
	return func(req *http.Request) (r.Principal, error) {
		if len(req.Header.Values("Authorization")) != 1 || !hmac.Equal([]byte(req.Header.Get("Authorization")), []byte("Bearer "+secret)) {
			return r.Principal{}, r.ErrDenied
		}
		copy := p
		copy.Integrations = slices.Clone(p.Integrations)
		return copy, nil
	}, nil
}
func Handler(service *r.Service, authenticate Authenticate) (http.Handler, error) {
	if service == nil || authenticate == nil {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	bind := func(pattern string, fn func(http.ResponseWriter, *http.Request, r.Principal) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			p, e := authenticate(req)
			if e != nil || !p.Identity.Valid() {
				status := 401
				w.Header().Set("WWW-Authenticate", `Bearer realm="reporting", error="invalid_token"`)
				scheme, _, _ := strings.Cut(req.Header.Get("Authorization"), " ")
				if !strings.EqualFold(scheme, "Bearer") {
					w.Header().Set("WWW-Authenticate", `Bearer realm="reporting"`)
				}
				var insufficient interface{ InsufficientScope() bool }
				if errors.As(e, &insufficient) && insufficient.InsufficientScope() {
					status = 403
					w.Header().Set("WWW-Authenticate", `Bearer realm="reporting", error="insufficient_scope"`)
				}
				problem(w, r.ErrDenied, status)
				return
			}
			ctx, cancel := context.WithTimeout(req.Context(), r.WorkTimeout)
			defer cancel()
			req = req.WithContext(ctx)
			if e = fn(w, req, p); e != nil {
				problem(w, e, 0)
			}
		})
	}
	const base = "/api/reporting/v1"
	bind("GET "+base+"/session", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		return send(w, 200, map[string]any{"identity": map[string]string{"tenant": p.Tenant, "issuer": p.Issuer, "subject": p.Subject}, "integrations": slices.Clone(p.Integrations)})
	})
	bind("GET "+base+"/capabilities", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		return send(w, 200, map[string]any{"profile": "S01-B-contract-simulators", "production_approved": false, "query_shapes": "published catalog only", "formats": []string{"xlsx"}, "source_interfaces": []string{"DescribeCatalog", "PrepareRead", "ReadPage", "AuthorizeCurrent"}, "routes": []string{"GET /catalog", "POST /queries", "POST /snapshot-jobs", "GET /jobs/{id}", "POST /jobs/{id}/cancel", "GET /snapshots/{id}/rows", "POST /exports", "GET /artifacts/{id}/content"}})
	})
	bind("GET "+base+"/catalog", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		values := req.URL.Query()
		if len(values) != 1 || len(values["integration_id"]) != 1 {
			return r.ErrInvalid
		}
		v, e := service.Catalog(req.Context(), p, values.Get("integration_id"))
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("POST "+base+"/queries", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var q r.QuerySpec
		if e := decode(req, &q); e != nil {
			return e
		}
		v, e := service.Query(req.Context(), p, q)
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("POST "+base+"/snapshot-jobs", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var q r.QuerySpec
		if e := decode(req, &q); e != nil {
			return e
		}
		key, e := idem(req)
		if e != nil {
			return e
		}
		v, e := service.SubmitSnapshot(req.Context(), p, q, key)
		if e != nil {
			return e
		}
		w.Header().Set("Location", base+"/jobs/"+v.ID)
		return send(w, 202, v)
	})
	bind("GET "+base+"/jobs/{id}", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		v, e := service.Job(req.Context(), p, req.PathValue("id"))
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("POST "+base+"/jobs/{id}/cancel", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		b, e := io.ReadAll(io.LimitReader(req.Body, 1))
		if e != nil || len(b) != 0 {
			return r.ErrInvalid
		}
		v, e := service.Cancel(req.Context(), p, req.PathValue("id"))
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("GET "+base+"/snapshots/{id}/rows", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		values := req.URL.Query()
		for k, v := range values {
			if (k != "limit" && k != "cursor") || len(v) != 1 {
				return r.ErrInvalid
			}
		}
		limit := 50
		var e error
		if raw := values.Get("limit"); raw != "" {
			limit, e = strconv.Atoi(raw)
			if e != nil {
				return r.ErrInvalid
			}
		}
		v, e := service.Rows(req.Context(), p, req.PathValue("id"), limit, values.Get("cursor"))
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("POST "+base+"/exports", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			SnapshotID string `json:"snapshot_id"`
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		key, e := idem(req)
		if e != nil {
			return e
		}
		v, e := service.SubmitExport(req.Context(), p, input.SnapshotID, key)
		if e != nil {
			return e
		}
		w.Header().Set("Location", base+"/jobs/"+v.ID)
		return send(w, 202, v)
	})
	bind("GET "+base+"/artifacts/{id}/content", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		v, e := service.Download(req.Context(), p, req.PathValue("id"))
		if e != nil {
			return e
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="report-`+v.ID+`.xlsx"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(v.Content)))
		w.Header().Set("X-Content-SHA256", v.SHA256)
		if _, e = w.Write(v.Content); e != nil {
			panic(http.ErrAbortHandler)
		}
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Reporting-Data-Class", "contract-simulator")
		mux.ServeHTTP(w, req)
	}), nil
}
func decode(req *http.Request, out any) error {
	kind, _, e := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if e != nil || kind != "application/json" {
		return r.ErrInvalid
	}
	return r.DecodeStrict(req.Body, out)
}
func idem(req *http.Request) (string, error) {
	if len(req.Header.Values("Idempotency-Key")) != 1 {
		return "", r.ErrInvalid
	}
	key := req.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) != key {
		return "", r.ErrInvalid
	}
	return key, nil
}
func send(w http.ResponseWriter, status int, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, e = w.Write(append(b, '\n')); e != nil {
		panic(http.ErrAbortHandler)
	}
	return nil
}
func problem(w http.ResponseWriter, e error, status int) {
	code := "EXECUTION_FAILED"
	if status == 0 {
		status = 500
	}
	for _, candidate := range []struct {
		err    error
		status int
	}{{r.ErrInvalid, 400}, {r.ErrUnsupported, 422}, {r.ErrDenied, 403}, {r.ErrNotFound, 404}, {r.ErrConflict, 409}, {r.ErrIntegrity, 409}, {r.ErrBudget, 413}, {r.ErrUnavailable, 503}, {r.ErrLease, 409}} {
		if errors.Is(e, candidate.err) {
			code = candidate.err.Error()
			if status != 401 {
				status = candidate.status
			}
			break
		}
	}
	_ = send(w, status, map[string]string{"error": code})
}
