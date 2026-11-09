// Package httpapi exposes the contract-simulator profile, not production SSO.
package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/layouts"
	"idp.local/reporting/privatedocs"
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
			if e := decode(w, req, &input, bodySmall); e != nil {
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
			if e := decode(w, req, &input, bodyDocument); e != nil {
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
	// S12-B: the audit, newest first: the caller's own events, or with scope=tenant (an administrator's) the tenant's, each with its subject.
	bind("GET "+base+"/audit", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		log := auditLogOf(req.Context())
		if log == nil {
			return r.ErrNotFound
		}
		limit, before, tenant := 50, "", false
		for key, v := range req.URL.Query() {
			if len(v) != 1 || v[0] == "" {
				return r.ErrInvalid
			}
			switch key {
			case "limit":
				n, e := strconv.Atoi(v[0])
				if e != nil || n < 1 || n > r.MaxAuditListing {
					return r.ErrInvalid
				}
				limit = n
			case "before":
				if len(v[0]) != 32 {
					return r.ErrInvalid
				}
				before = v[0]
			case "scope":
				if v[0] != "tenant" {
					return r.ErrInvalid
				}
				tenant = true
			default:
				return r.ErrInvalid
			}
		}
		if tenant && !p.Has(r.RoleAdministrator) {
			return r.ErrRoleDenied
		}
		events, e := log.Events(req.Context(), p.Identity, tenant, limit, before)
		if e != nil {
			return e
		}
		return send(w, 200, events)
	})
	// S15-A (ADR 0019): before anyone signs in, the console asks whether a browser sign-in is configured; WithSignIn answers it when one is.
	// Unauthenticated, and nothing in it: a deployment without a sign-in says so instead of a 404 on every page load.
	mux.HandleFunc("GET "+base+"/sign-in", func(w http.ResponseWriter, req *http.Request) {
		if len(req.URL.Query()) != 0 {
			problem(w, r.ErrInvalid, 0)
			return
		}
		_ = send(w, 200, map[string]bool{"enabled": false})
	})
	bind("GET "+base+"/session", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		// Only the products the service serves for the principal (ADR 0016): a binding's product without a connection is never offered.
		return send(w, 200, map[string]any{"identity": map[string]string{"tenant": p.Tenant, "issuer": p.Issuer, "subject": p.Subject}, "integrations": service.Served(p), "roles": p.EffectiveRoles()})
	})
	bind("GET "+base+"/capabilities", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		return send(w, 200, capabilities(service, false))
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
		if e := decode(w, req, &q, bodyQuery); e != nil {
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
		if e := decode(w, req, &q, bodyQuery); e != nil {
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
		if e := decode(w, req, &input, bodySmall); e != nil {
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
	bind("GET "+base+"/jobs", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		f, e := jobListing(req)
		if e != nil {
			return e
		}
		v, e := service.Jobs(req.Context(), p, f)
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("GET "+base+"/issues", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		limit, e := listingLimit(req, 25, r.MaxIssueListing)
		if e != nil {
			return e
		}
		v, e := service.Issues(req.Context(), p, limit)
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	bind("POST "+base+"/issues", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		// Mounted for its answer only: the template handler (S03-G) overrides it when templates exist.
		return r.ErrUnsupported
	})
	bind("GET "+base+"/issues/{id}", func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		v, e := service.Issue(req.Context(), p, req.PathValue("id"))
		if e != nil {
			return e
		}
		return send(w, 200, v)
	})
	return secured(mux), nil
}

// listingLimit is the one query parameter a listing takes: limit, once, 1..most, or absent (fallback). Any other query is ErrInvalid.
func listingLimit(req *http.Request, fallback, most int) (int, error) {
	values := req.URL.Query()
	if len(values) > 1 || (len(values) == 1 && len(values["limit"]) != 1) {
		return 0, r.ErrInvalid
	}
	if len(values["limit"]) == 1 {
		n, e := strconv.Atoi(values.Get("limit"))
		if e != nil || n < 1 || n > most {
			return 0, r.ErrInvalid
		}
		return n, nil
	}
	return fallback, nil
}

// jobListing reads GET /jobs's query (S07-C, S07-E): limit (1..MaxJobListing, default 50), kind, state and before, each at most once
// and not empty; any other key is ErrInvalid, and so is a value the service would refuse.
func jobListing(req *http.Request) (r.JobListing, error) {
	values := req.URL.Query()
	f := r.JobListing{Limit: 50}
	for key, v := range values {
		if len(v) != 1 || v[0] == "" {
			return r.JobListing{}, r.ErrInvalid
		}
		switch key {
		case "limit":
			n, e := strconv.Atoi(v[0])
			if e != nil {
				return r.JobListing{}, r.ErrInvalid
			}
			f.Limit = n
		case "kind":
			f.Kind = v[0]
		case "state":
			f.State = v[0]
		case "before":
			f.Before = v[0]
		default:
			return r.JobListing{}, r.ErrInvalid
		}
	}
	return f, nil
}

// capabilities is the one capability document. embedding is whether embed pages are configured for the caller's integrations
// (WithEmbedding computes it; the core API alone says false); it never carries the origins.
func capabilities(service *r.Service, embedding bool) map[string]any {
	return capabilitiesLabelled(service, embedding, "S01-B-contract-simulators")
}

// capabilitiesLabelled is the capability document with the given profile label. production_approved is never a parameter: it is false in
// this code until a code change after the admission decision says otherwise.
func capabilitiesLabelled(service *r.Service, embedding bool, profile string) map[string]any {
	view := map[string]any{"profile": profile, "production_approved": false, "query_shapes": "published catalog only", "formats": service.Formats(), "source_interfaces": []string{"DescribeCatalog", "PrepareRead", "ReadPage", "AuthorizeCurrent"}, "issuance": service.CanIssue(), "routes": []string{"GET /catalog", "POST /queries", "POST /snapshot-jobs", "GET /jobs/{id}", "POST /jobs/{id}/cancel", "GET /snapshots/{id}/rows", "POST /exports", "GET /artifacts/{id}/content", "GET /issues", "GET /issues/{id}"}, "template_routes": []string{"POST /issues", "GET, POST /document-templates", "GET /document-templates/{id}", "GET, POST /document-templates/{id}/release", "GET /document-templates/{id}/published", "POST /document-templates/{id}/previews"}}
	view["embedding"] = embedding
	q := service.Quotas()
	view["quotas"] = map[string]int{r.QuotaJobsInFlight: q.JobsInFlight, r.QuotaStoredResults: q.StoredResults, r.QuotaAdmissionsPerHour: q.AdmissionsPerHour, r.QuotaQueriesInFlight: q.QueriesInFlight}
	return view
}

// secured is the one wrapper of every answer of this API, a success and a refusal alike: never cached, never sniffed, and marked
// with the class of the data it carries, and unframeable. Handler applies it to the core API and each With* mount applies it to the routes it
// adds, so that no route answers without these headers whichever mounts a deployment composes; a route sets only what is its own
// (a content type, a disposition, a digest, a policy of its own) and never repeats these.
func secured(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Reporting-Data-Class", "contract-simulator")
		// No answer of the API can be shown in a frame (ADR 0006): only the embed pages of the console package can, and only for an
		// origin the deployment lists. A route that sets a policy of its own includes the same frame-ancestors source in it.
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		next.ServeHTTP(w, req)
	})
}

// The bound of the body of each route that reads one (S04-G, docs/API.md "Limits"), in bytes. A body over it is refused with 413
// REQUEST_TOO_LARGE before any of it is parsed, and the connection is closed rather than the rest read. The documents (a layout, a
// template, a dashboard, a preview or a check request, a background or an image, which travels as base64) keep the bound of every
// document, MaxResultBytes; the small requests are bounded where they are small.
const (
	bodySmall    = 4 << 10          // exports, issues, the release and publish actions
	bodyQuery    = 64 << 10         // POST /queries and /snapshot-jobs
	bodyDocument = r.MaxResultBytes // the documents and the uploads
)

// errTooLarge is a body over its route's bound.
var errTooLarge = errors.New("REQUEST_TOO_LARGE")

func decode(w http.ResponseWriter, req *http.Request, out any, limit int64) error {
	kind, _, e := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if e != nil || kind != "application/json" {
		return r.ErrInvalid
	}
	if req.ContentLength > limit {
		return errTooLarge
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, req.Body, limit))
	var tooLarge *http.MaxBytesError
	if errors.As(e, &tooLarge) {
		return errTooLarge
	}
	if e != nil {
		return r.ErrInvalid
	}
	return r.DecodeStrict(bytes.NewReader(b), out)
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

// classify is the one table of error to code and status: the answer of problem, and the code of a
// row of a document check, which is the code a preview of that row would answer. ok is false for
// an error the table does not know (EXECUTION_FAILED, 500).
func classify(e error) (code string, status int, ok bool) {
	if errors.Is(e, r.ErrQuota) {
		return "QUOTA_EXCEEDED", 429, true
	}
	if errors.Is(e, errTooLarge) {
		return errTooLarge.Error(), 413, true
	}
	for _, candidate := range []struct {
		err    error
		status int
	}{{r.ErrInvalid, 400}, {r.ErrUnsupported, 422}, {r.ErrDenied, 403}, {r.ErrRoleDenied, 403}, {r.ErrAuditUnavailable, 503}, {privatedocs.ErrApprovalRequired, 409}, {r.ErrNotFound, 404}, {r.ErrConflict, 409}, {r.ErrIntegrity, 409}, {r.ErrBudget, 413}, {r.ErrUnavailable, 503}, {r.ErrLease, 409}, {r.ErrUnpublished, 409}} {
		if errors.Is(e, candidate.err) {
			return candidate.err.Error(), candidate.status, true
		}
	}
	return "EXECUTION_FAILED", 500, false
}

func problem(w http.ResponseWriter, e error, status int) {
	if status == 0 {
		status = 500
	}
	code, mapped, ok := classify(e)
	if ok && status != 401 {
		status = mapped
	}
	body := map[string]any{"error": code}
	// A quota refusal (S04-D) names the quota and nothing else, and says when to try again: admission looks at the last hour and at the
	// jobs in flight, so a minute is a fair wait, not a promise.
	if q := r.QuotaOf(e); ok && q != "" {
		body["quota"] = q
		w.Header().Set("Retry-After", "60")
		if q == r.QuotaQueriesInFlight {
			// The identity's own reads in this process: a read finishing frees the slot, so a second is a fair wait (S04-G).
			w.Header().Set("Retry-After", "1")
		}
	}
	// The object store being unreachable (S04-E) is an outage of the platform's own, not of the source product: it is the same 503, and it says
	// when to try again. Nothing of the store is in the answer: not its status, not its text.
	if r.IsObjectStoreError(e) && ok {
		w.Header().Set("Retry-After", "10")
	}
	// A layout or binding refusal says why and where (S03-J): a reason and names and indices, never a value.
	if detail, found := doctemplates.DetailOf(e); found && ok {
		body["detail"] = detail
	}
	_ = send(w, status, body)
}

// auditKey carries the audit log of a handler composed WithAudit.
type auditKey struct{}

// WithAudit records people's actions in log (S12-B, ADR 0018): every route composed under h records what AuditAction names, and answers
// GET /api/reporting/v1/audit. Without it nothing is recorded and the audit route answers 404.
func WithAudit(h http.Handler, log r.AuditLog) http.Handler {
	if log == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		inner := req.WithContext(context.WithValue(req.Context(), auditKey{}, log))
		// The mux under h writes the route it matched on the request it was given; the access line and the metrics above read it here,
		// also when a handler panics.
		defer func() { req.Pattern = inner.Pattern }()
		h.ServeHTTP(w, inner)
	})
}

func auditLogOf(ctx context.Context) r.AuditLog {
	log, _ := ctx.Value(auditKey{}).(r.AuditLog)
	return log
}

// AuditAction is what a route records (S12-B): the action, the kind of object, and whether the action must not run unrecorded (a
// publication or withdrawal, an official issue). "" for a route that records nothing (reading, querying, previews).
func AuditAction(method, pattern string) (action, kind string, mandatory bool) {
	path := routePath(pattern)
	designKind := func() string {
		for prefix, k := range map[string]string{"/layouts": "layout", "/dashboards": "dashboard", "/document-templates": "template", "/document-backgrounds": "background", "/document-images": "image"} {
			if strings.Contains(path, "/api/reporting/v1"+prefix) {
				return k
			}
		}
		return ""
	}
	switch {
	case method == http.MethodGet && strings.HasSuffix(path, "/artifacts/{id}/content"):
		return "artifact.download", "artifact", false
	case method == http.MethodGet || method == http.MethodHead:
		return "", "", false
	case strings.HasSuffix(path, "/release"):
		return "design.release", designKind(), true
	case strings.HasSuffix(path, "/api/reporting/v1/issues"):
		return "issue.submit", "issue", true
	case strings.HasSuffix(path, "/api/reporting/v1/exports"):
		return "export.submit", "export", false
	case strings.Contains(path, "/api/reporting/v1/schedules"):
		// S16-A: a schedule's creation, pause, resumption and deletion; its runs are recorded by the scheduler (schedule.run).
		return "schedule.change", "schedule", false
	}
	if RequiredRole(method, pattern) == r.RoleDesigner {
		if k := designKind(); k != "" {
			return "design.change", k, false
		}
	}
	return "", "", false
}

// statusRecorder keeps the status a handler wrote, for the outcome of its audit event.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the writer (flushing, deadlines).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func outcomeOf(status int) string {
	switch {
	case status == 0 || status < 400:
		return "succeeded"
	case status < 500:
		return "refused"
	}
	return "failed"
}

// routePath is a pattern's path, the tenant's shared space's (S12-C, /team/...) read as the same route as one's own.
func routePath(pattern string) string {
	_, path, _ := strings.Cut(pattern, " ")
	if path == "" {
		path = pattern
	}
	return strings.Replace(path, "/api/reporting/v1/team/", "/api/reporting/v1/", 1)
}

// designPaths are the routes of the private design stores (layouts, dashboards, templates, page backgrounds, images): a change of any of them
// is a designer's (S12-A).
var designPaths = []string{"/layouts", "/dashboards", "/document-templates", "/document-backgrounds", "/document-images"}

// RequiredRole is the role a route needs (S12-A, ADR 0018), from its method and the pattern the mux matched: a publication or withdrawal
// (".../release") is a publisher's, an official issue an issuer's, any other change of a design a designer's; reading, querying, freezing,
// exporting, previewing and checking are a viewer's, and so is a schedule's every route (S16-A): a schedule submits a viewer's jobs, as its
// owner, at each run. A route added later falls under the same rules.
func RequiredRole(method, pattern string) string {
	path := routePath(pattern)
	if method == http.MethodGet || method == http.MethodHead {
		return r.RoleViewer
	}
	switch {
	case strings.HasSuffix(path, "/release"):
		return r.RolePublisher
	case strings.HasSuffix(path, "/api/reporting/v1/issues"):
		return r.RoleIssuer
	case strings.HasSuffix(path, "/previews"):
		return r.RoleViewer
	}
	for _, d := range designPaths {
		if strings.Contains(path, "/api/reporting/v1"+d) {
			return r.RoleDesigner
		}
	}
	return r.RoleViewer
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
		// S12-A (ADR 0018): the route's role, from the pattern the mux matched; a refusal names the code only.
		audit := auditLogOf(req.Context())
		if role := RequiredRole(req.Method, req.Pattern); !p.Has(role) {
			if audit != nil {
				_ = audit.Record(req.Context(), r.AuditEvent{ID: r.NewID(), Identity: p.Identity, Action: "role.refused", ObjectKind: "route", Outcome: "refused", Code: r.ErrRoleDenied.Error()})
			}
			problem(w, r.ErrRoleDenied, 403)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), r.WorkTimeout)
		defer cancel()
		req = req.WithContext(ctx)
		// S12-B: the action is recorded; a publication and an issue are recorded before they run and refused when that fails (ADR 0018 Q4).
		if action, kind, mandatory := AuditAction(req.Method, req.Pattern); audit != nil && action != "" {
			event := r.AuditEvent{ID: r.NewID(), Identity: p.Identity, Action: action, ObjectKind: kind, ObjectID: req.PathValue("id"), Outcome: "requested"}
			if len(event.ObjectID) > 64 {
				event.ObjectID = ""
			}
			if mandatory && audit.Record(ctx, event) != nil {
				problem(w, r.ErrAuditUnavailable, 503)
				return
			}
			rec := &statusRecorder{ResponseWriter: w}
			w = rec
			defer func() {
				event.ID, event.Outcome = r.NewID(), outcomeOf(rec.status)
				_ = audit.Record(context.WithoutCancel(req.Context()), event)
			}()
		}
		if e = fn(w, req, p); e != nil {
			problem(w, e, 0)
		}

	}
}
