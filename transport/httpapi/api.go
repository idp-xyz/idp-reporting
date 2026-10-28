// Package httpapi exposes the contract-simulator profile, not production SSO.
package httpapi

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"idp.local/reporting/layouts"
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
func Handler(service *r.Service, authenticate Authenticate, layoutStores ...*layouts.Store) (http.Handler, error) {
	if len(layoutStores) > 1 || (len(layoutStores) == 1 && layoutStores[0] == nil) {
		return nil, r.ErrInvalid
	}
	if service == nil || authenticate == nil {
		return nil, r.ErrInvalid
	}
	mux := http.NewServeMux()
	bind := func(pattern string, fn func(http.ResponseWriter, *http.Request, r.Principal) error) {
		mux.HandleFunc(pattern, authenticated(authenticate, fn))
	}
	const base = "/api/reporting/v1"
	if len(layoutStores) == 1 {
		store := layoutStores[0]
		describeBinding := func(req *http.Request, p r.Principal, b layouts.Binding) (r.Dataset, error) {
			catalog, e := service.Catalog(req.Context(), p, b.Integration)
			if e != nil {
				return r.Dataset{}, e
			}
			for _, dataset := range catalog {
				if dataset.Ref == b.Dataset {
					return dataset, nil
				}
			}
			return r.Dataset{}, r.ErrDenied
		}
		authorize := func(req *http.Request, p r.Principal, d layouts.Document) error {
			if e := layouts.Validate(d); e != nil {
				return e
			}
			dataset, e := describeBinding(req, p, d.Binding)
			if e != nil {
				return e
			}
			for _, column := range d.Columns {
				found := false
				for _, known := range dataset.Columns {
					if known.ID == column.ID {
						found = true
						break
					}
				}
				if !found {
					return r.ErrDenied
				}
			}
			return nil
		}
		bind("GET "+base+"/layouts", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			all, e := store.List(p.Identity)
			if e != nil {
				return e
			}
			out := []map[string]any{}
			for _, v := range all {
				if e = authorize(req, p, v.Layout); e != nil {
					if errors.Is(e, r.ErrDenied) || errors.Is(e, r.ErrNotFound) {
						continue
					}
					return e
				}
				out = append(out, map[string]any{"id": v.ID, "version": v.Version, "binding": v.Layout.Binding})
			}
			return send(w, 200, out)
		})
		bind("GET "+base+"/layouts/{id}", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			q := req.URL.Query()
			if len(q) != 1 || len(q["version"]) != 1 {
				return r.ErrInvalid
			}
			version, e := strconv.Atoi(q.Get("version"))
			if e != nil {
				return r.ErrInvalid
			}
			v, e := store.Get(p.Identity, req.PathValue("id"), version)
			if e != nil {
				return e
			}
			if e = authorize(req, p, v.Layout); e != nil {
				return e
			}
			return send(w, 200, v)
		})
		bind("GET "+base+"/layouts/{id}/release", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
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
		})
		bind("GET "+base+"/layouts/{id}/published", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			entry, e := store.Published(p.Identity, req.PathValue("id"))
			if e != nil {
				return e
			}
			if e = authorize(req, p, entry.Layout); e != nil {
				return e
			}
			return send(w, 200, entry)
		})
		bind("POST "+base+"/layouts/{id}/release", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
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
		})
		bind("POST "+base+"/layouts", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
			var input struct {
				ID       string           `json:"id"`
				Expected int              `json:"expected_version"`
				Layout   layouts.Document `json:"layout"`
			}
			if e := decode(req, &input); e != nil {
				return e
			}
			if e := authorize(req, p, input.Layout); e != nil {
				return e
			}
			v, e := store.Save(p.Identity, input.ID, input.Expected, input.Layout)
			if e != nil {
				return e
			}
			return send(w, 201, v)
		})
	}
	bind("GET "+base+"/session", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		return send(w, 200, map[string]any{"identity": map[string]string{"tenant": p.Tenant, "issuer": p.Issuer, "subject": p.Subject}, "integrations": slices.Clone(p.Integrations)})
	})
	bind("GET "+base+"/capabilities", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		return send(w, 200, map[string]any{"profile": "S01-B-contract-simulators", "production_approved": false, "query_shapes": "published catalog only", "formats": service.Formats(), "source_interfaces": []string{"DescribeCatalog", "PrepareRead", "ReadPage", "AuthorizeCurrent"}, "issuance": service.CanIssue(), "routes": []string{"GET /catalog", "POST /queries", "POST /snapshot-jobs", "GET /jobs/{id}", "POST /jobs/{id}/cancel", "GET /snapshots/{id}/rows", "POST /exports", "GET /artifacts/{id}/content", "POST /issues", "GET /issues", "GET /issues/{id}"}})
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
			Format     string `json:"format"` // absent means xlsx
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		key, e := idem(req)
		if e != nil {
			return e
		}
		v, e := service.SubmitExportFormat(req.Context(), p, input.SnapshotID, input.Format, key)
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
		media, extension := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"
		switch v.Format {
		case "": // the workbook
		case "pdf":
			media, extension = "application/pdf", "pdf"
		default:
			return r.ErrIntegrity
		}
		name := "report-" + v.ID
		if v.Issue != "" {
			name = "issued-" + v.Issue
		}
		w.Header().Set("Content-Type", media)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.`+extension+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(v.Content)))
		w.Header().Set("X-Content-SHA256", v.SHA256)
		if _, e = w.Write(v.Content); e != nil {
			panic(http.ErrAbortHandler)
		}
		return nil
	})
	bind("GET "+base+"/issues", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		values := req.URL.Query()
		limit := 25
		if len(values) > 1 || (len(values) == 1 && len(values["limit"]) != 1) {
			return r.ErrInvalid
		}
		if len(values["limit"]) == 1 {
			n, e := strconv.Atoi(values.Get("limit"))
			if e != nil || n < 1 || n > r.MaxIssueListing {
				return r.ErrInvalid
			}
			limit = n
		}
		v, e := service.Issues(req.Context(), p, limit)
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("GET "+base+"/issues/{id}", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		v, e := service.Issue(req.Context(), p, req.PathValue("id"))
		if e != nil {
			return e
		}
		return send(w, 200, v)
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
	}{{r.ErrInvalid, 400}, {r.ErrUnsupported, 422}, {r.ErrDenied, 403}, {r.ErrNotFound, 404}, {r.ErrConflict, 409}, {r.ErrIntegrity, 409}, {r.ErrBudget, 413}, {r.ErrUnavailable, 503}, {r.ErrLease, 409}, {r.ErrUnpublished, 409}} {
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

func authenticated(authenticate Authenticate, fn func(http.ResponseWriter, *http.Request, r.Principal) error) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
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

	}
}
