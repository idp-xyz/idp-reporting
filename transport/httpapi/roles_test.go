package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// The route classes of ADR 0018 (S12-A): what each pattern needs.
func TestRequiredRoleOfEachRouteClass(t *testing.T) {
	for _, c := range []struct{ method, pattern, want string }{
		{"GET", "GET /api/reporting/v1/layouts", r.RoleViewer},
		{"GET", "GET /api/reporting/v1/dashboards/{id}", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/queries", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/snapshot-jobs", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/exports", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/jobs/{id}/cancel", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/document-previews", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/document-checks", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/document-templates/{id}/previews", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/document-templates/samples/{kind}/previews", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/layouts", r.RoleDesigner},
		{"POST", "POST /api/reporting/v1/dashboards", r.RoleDesigner},
		{"POST", "POST /api/reporting/v1/document-templates", r.RoleDesigner},
		{"PUT", "PUT /api/reporting/v1/document-templates/{id}/catalog", r.RoleDesigner},
		{"POST", "POST /api/reporting/v1/document-templates/samples/{kind}/copy", r.RoleDesigner},
		{"POST", "POST /api/reporting/v1/document-backgrounds", r.RoleDesigner},
		{"POST", "POST /api/reporting/v1/document-images", r.RoleDesigner},
		{"POST", "POST /api/reporting/v1/layouts/{id}/release", r.RolePublisher},
		{"POST", "POST /api/reporting/v1/dashboards/{id}/release", r.RolePublisher},
		{"POST", "POST /api/reporting/v1/document-templates/{id}/release", r.RolePublisher},
		{"POST", "POST /api/reporting/v1/issues", r.RoleIssuer},
		// S16-A: a schedule's routes are a viewer's, the role of the jobs its runs submit.
		{"POST", "POST /api/reporting/v1/schedules", r.RoleViewer},
		{"GET", "GET /api/reporting/v1/schedules/{id}/runs", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/schedules/{id}/pause", r.RoleViewer},
		{"POST", "POST /api/reporting/v1/schedules/{id}/resume", r.RoleViewer},
		{"DELETE", "DELETE /api/reporting/v1/schedules/{id}", r.RoleViewer},
		// S18-B: the administrator's view, a read, is an administrator's.
		{"GET", "GET /api/reporting/v1/admin/configuration", r.RoleAdministrator},
	} {
		if got := httpapi.RequiredRole(c.method, c.pattern); got != c.want {
			t.Errorf("%s: %s, want %s", c.pattern, got, c.want)
		}
	}
}

// A principal's roles decide what the API does: a viewer reads and queries but does not design, a designer does not publish, nobody but an
// issuer issues; the refusal is 403 AUTH_ROLE_DENIED with nothing else; a principal whose binding names no role keeps the default rights.
func TestRolesAreEnforcedOnTheRoutes(t *testing.T) {
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := layouts.Open(t.TempDir() + "/layouts")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	var roles []string
	demo, _ := httpapi.DemoBearer(secret, p)
	auth := func(req *http.Request) (r.Principal, error) {
		q, e := demo(req)
		q.Roles = roles
		return q, e
	}
	h, e := httpapi.Handler(service, auth, store)
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := service.Catalog(t.Context(), p, contractsim.FinanceIntegration)
	if e != nil {
		t.Fatal(e)
	}
	q := contractsim.FinancialQuery()
	d := layouts.Document{Schema: "1.0", Binding: layouts.Binding{Integration: q.Integration, Dataset: q.Dataset}, Columns: []layouts.Column{{ID: catalog[0].Columns[0].ID, Align: "right"}}}
	save, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "layout": d})
	query, _ := json.Marshal(q)
	denied := func(name, method, path string, body []byte) {
		t.Helper()
		w := request(h, method, path, body, true)
		if w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"AUTH_ROLE_DENIED"}` {
			t.Errorf("%s with %v: %d %s", name, roles, w.Code, w.Body.String())
		}
	}
	// A viewer.
	roles = []string{r.RoleViewer}
	if w := request(h, "POST", "/api/reporting/v1/queries", query, true); w.Code != 200 {
		t.Fatal("a viewer's query", w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/api/reporting/v1/layouts", nil, true); w.Code != 200 {
		t.Fatal("a viewer's listing", w.Code)
	}
	denied("a viewer's save", "POST", "/api/reporting/v1/layouts", save)
	// A designer saves, but does not publish.
	roles = []string{r.RoleViewer, r.RoleDesigner}
	w := request(h, "POST", "/api/reporting/v1/layouts", save, true)
	var entry layouts.Entry
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &entry) != nil {
		t.Fatal("a designer's save", w.Code, w.Body.String())
	}
	release := []byte(`{"action":"publish","version":1,"expected_revision":0}`)
	denied("a designer's publication", "POST", "/api/reporting/v1/layouts/"+entry.ID+"/release", release)
	// A publisher publishes.
	roles = []string{r.RolePublisher}
	if w := request(h, "POST", "/api/reporting/v1/layouts/"+entry.ID+"/release", release, true); w.Code != 200 {
		t.Fatal("a publisher's publication", w.Code, w.Body.String())
	}
	// Only an issuer issues (the route answers before any template is looked at).
	roles = []string{r.RoleViewer, r.RoleDesigner, r.RolePublisher}
	denied("an issue without the issuer role", "POST", "/api/reporting/v1/issues", []byte(`{}`))
	// An administrator alone is not a viewer.
	roles = []string{r.RoleAdministrator}
	denied("an administrator's query", "POST", "/api/reporting/v1/queries", query)
	// No role named: the default rights; the session says which.
	roles = nil
	if w := request(h, "POST", "/api/reporting/v1/layouts", save, true); w.Code != 201 {
		t.Fatal("the default rights", w.Code, w.Body.String())
	}
	roles = []string{r.RoleViewer, r.RoleIssuer}
	w = request(h, "GET", "/api/reporting/v1/session", nil, true)
	var session struct {
		Roles []string `json:"roles"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil || strings.Join(session.Roles, ",") != "viewer,issuer" {
		t.Fatal(w.Code, w.Body.String())
	}
}
