package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
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

// failingLog is an audit log that cannot write (S12-B: a publication must not run unrecorded).
type failingLog struct{ r.AuditLog }

func (failingLog) Record(context.Context, r.AuditEvent) error { return errors.New("down") }

// What each route records (ADR 0018 decision 4).
func TestAuditActionOfEachRouteClass(t *testing.T) {
	for _, c := range []struct {
		method, pattern, action, kind string
		mandatory                     bool
	}{
		{"POST", "POST /api/reporting/v1/layouts", "design.change", "layout", false},
		{"PUT", "PUT /api/reporting/v1/document-templates/{id}/catalog", "design.change", "template", false},
		{"POST", "POST /api/reporting/v1/document-images", "design.change", "image", false},
		{"POST", "POST /api/reporting/v1/dashboards/{id}/release", "design.release", "dashboard", true},
		{"POST", "POST /api/reporting/v1/issues", "issue.submit", "issue", true},
		{"POST", "POST /api/reporting/v1/exports", "export.submit", "export", false},
		{"GET", "GET /api/reporting/v1/artifacts/{id}/content", "artifact.download", "artifact", false},
		{"GET", "GET /api/reporting/v1/layouts", "", "", false},
		{"POST", "POST /api/reporting/v1/queries", "", "", false},
		{"POST", "POST /api/reporting/v1/document-templates/{id}/previews", "", "", false},
	} {
		a, k, m := httpapi.AuditAction(c.method, c.pattern)
		if a != c.action || k != c.kind || m != c.mandatory {
			t.Errorf("%s: %q %q %t", c.pattern, a, k, m)
		}
	}
}

// People's actions are recorded with their outcome; a publication is recorded before it runs and refused when that fails, a save is not;
// a role refusal is recorded; the caller reads its own events, an administrator the tenant's with their subjects.
func TestAuditRecordsActionsAndListsThem(t *testing.T) {
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	colleague := p
	colleague.Subject = "colleague"
	a.Grant(contractsim.DemoOwner(colleague, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(time.Now)
	service, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := layouts.Open(t.TempDir() + "/layouts")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	var roles []string
	subject := p.Subject
	demo, _ := httpapi.DemoBearer(secret, p)
	auth := func(req *http.Request) (r.Principal, error) {
		q, e := demo(req)
		q.Roles, q.Subject = roles, subject
		return q, e
	}
	api, e := httpapi.Handler(service, auth, store)
	if e != nil {
		t.Fatal(e)
	}
	h := httpapi.WithAudit(api, repo)
	catalog, _ := service.Catalog(t.Context(), p, contractsim.FinanceIntegration)
	q := contractsim.FinancialQuery()
	doc := layouts.Document{Schema: "1.0", Binding: layouts.Binding{Integration: q.Integration, Dataset: q.Dataset}, Columns: []layouts.Column{{ID: catalog[0].Columns[0].ID, Align: "right"}}}
	save, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "layout": doc})
	w := request(h, "POST", "/api/reporting/v1/layouts", save, true)
	var entry layouts.Entry
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &entry) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	release := []byte(`{"action":"publish","version":1,"expected_revision":0}`)
	if w := request(h, "POST", "/api/reporting/v1/layouts/"+entry.ID+"/release", release, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	roles = []string{r.RoleViewer}
	if w := request(h, "POST", "/api/reporting/v1/layouts", save, true); w.Code != 403 {
		t.Fatal(w.Code)
	}
	list := func(path string, want int) []r.AuditEvent {
		t.Helper()
		w := request(h, "GET", path, nil, true)
		var out []r.AuditEvent
		if w.Code != want || (want == 200 && json.Unmarshal(w.Body.Bytes(), &out) != nil) {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return out
	}
	events := list("/api/reporting/v1/audit", 200)
	got := []string{}
	for _, ev := range events {
		got = append(got, ev.Action+":"+ev.ObjectKind+":"+ev.Outcome)
		if ev.Subject != "" {
			t.Errorf("an own listing names the subject: %+v", ev)
		}
	}
	want := "role.refused:route:refused design.release:layout:succeeded design.release:layout:requested design.change:layout:succeeded"
	if strings.Join(got, " ") != want {
		t.Fatalf("%s\nwant %s", strings.Join(got, " "), want)
	}
	if events[1].ObjectID != entry.ID || events[3].ObjectID != "" {
		t.Fatalf("object ids: %+v", events)
	}
	// Pages: the second starts after the first's last event.
	page := list("/api/reporting/v1/audit?limit=2", 200)
	rest := list("/api/reporting/v1/audit?limit=10&before="+page[1].ID, 200)
	if len(page) != 2 || len(rest) != 2 || rest[0].ID != events[2].ID {
		t.Fatalf("%+v %+v", page, rest)
	}
	// Another subject of the tenant does something; it sees only its own; the tenant listing is an administrator's.
	subject, roles = "colleague", nil
	if w := request(h, "POST", "/api/reporting/v1/layouts", save, true); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if own := list("/api/reporting/v1/audit", 200); len(own) != 1 || own[0].Action != "design.change" {
		t.Fatalf("%+v", own)
	}
	list("/api/reporting/v1/audit?scope=tenant", 403)
	roles = []string{r.RoleViewer, r.RoleAdministrator}
	tenant := list("/api/reporting/v1/audit?scope=tenant", 200)
	if len(tenant) != 5 || tenant[0].Subject != "colleague" || tenant[1].Subject != p.Subject {
		t.Fatalf("%+v", tenant)
	}
	for _, bad := range []string{"?limit=0", "?limit=201", "?scope=all", "?before=x", "?other=1", "?limit=1&limit=2"} {
		list("/api/reporting/v1/audit"+bad, 400)
	}
	// An audit that cannot write: a publication is refused before it runs; a save still runs.
	roles, subject = nil, p.Subject
	broken := httpapi.WithAudit(api, failingLog{repo})
	if w := request(broken, "POST", "/api/reporting/v1/layouts", save, true); w.Code != 201 {
		t.Fatal("a save with the audit down", w.Code, w.Body.String())
	}
	w = request(broken, "POST", "/api/reporting/v1/layouts/"+entry.ID+"/release", []byte(`{"action":"withdraw","version":0,"expected_revision":1}`), true)
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"AUDIT_UNAVAILABLE"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if state, _ := store.Release(p.Identity, entry.ID); state.PublishedVersion != 1 {
		t.Fatal("the withdrawal ran unrecorded", state)
	}
	// Without an audit the route is not there.
	if w := request(api, "GET", "/api/reporting/v1/audit", nil, true); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
