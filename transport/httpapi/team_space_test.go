package httpapi_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// S12-C (ADR 0018): the tenant's shared template space. A colleague reads what another saved; the person who saved a version cannot
// publish it, another publisher can; the space is not anyone's own space and not another tenant's; the audit records the approval.
func TestTeamSpaceIsSharedWithinTheTenantAndPublicationIsAnApproval(t *testing.T) {
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	for _, subject := range []string{p.Subject, "bob"} {
		q := p
		q.Subject = subject
		source.Grant(contractsim.DemoOwner(q, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	}
	repo := memory.New(time.Now)
	service, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: source}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	subject, tenant := p.Subject, p.Tenant
	demo, _ := httpapi.DemoBearer(secret, p)
	auth := func(req *http.Request) (r.Principal, error) {
		v, e := demo(req)
		v.Subject, v.Tenant = subject, tenant
		return v, e
	}
	fallback, _ := httpapi.Handler(service, auth)
	templates, e := httpapi.WithDocumentTemplates(fallback, service, auth, store, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	h := httpapi.WithAudit(templates, repo)
	const team = "/api/reporting/v1/team/document-templates"
	body, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": savedTemplate("customer.id")})
	w := request(h, "POST", team, body, true)
	var saved struct{ ID string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	// The saver's own space does not have it; the team listing does, and says who saved it to no one.
	if w := request(h, "GET", "/api/reporting/v1/document-templates", nil, true); w.Code != 200 || strings.Contains(w.Body.String(), saved.ID) {
		t.Fatal("the team template is in the saver's own space", w.Body.String())
	}
	w = request(h, "GET", team+"/"+saved.ID+"?version=1", nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"saved_by_me":true`) || strings.Contains(w.Body.String(), "saved_by\"") {
		t.Fatal(w.Code, w.Body.String())
	}
	publish := []byte(`{"action":"publish","version":1,"expected_revision":0}`)
	if w := request(h, "POST", team+"/"+saved.ID+"/release", publish, true); w.Code != 409 || strings.TrimSpace(w.Body.String()) != `{"error":"APPROVAL_REQUIRED"}` {
		t.Fatal("the saver published its own version", w.Code, w.Body.String())
	}
	// A colleague sees it, did not save it, and approves it by publishing it.
	subject = "bob"
	w = request(h, "GET", team+"/"+saved.ID+"?version=1", nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"saved_by_me":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "POST", team+"/"+saved.ID+"/release", publish, true); w.Code != 200 {
		t.Fatal("the colleague's approval", w.Code, w.Body.String())
	}
	if w := request(h, "GET", team+"/"+saved.ID+"/published", nil, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Another tenant has its own team space: not this one.
	tenant = "another_tenant"
	if w := request(h, "GET", team+"/"+saved.ID+"/published", nil, true); w.Code == 200 {
		t.Fatal("another tenant reads this tenant's team template")
	}
	tenant = p.Tenant
	// The audit: the save and the approval, as the team template's.
	subject = p.Subject
	events, _ := repo.Events(t.Context(), r.Identity{Tenant: p.Tenant, Issuer: p.Issuer, Subject: "bob"}, false, 10, "")
	got := []string{}
	for _, ev := range events {
		got = append(got, ev.Action+":"+ev.ObjectKind+":"+ev.Outcome)
	}
	if strings.Join(got, " ") != "design.release:template:succeeded design.release:template:requested" {
		t.Fatal(got)
	}
	// The role rules read the team routes as the same routes.
	for c, want := range map[string]string{"POST " + team + "/{id}/release": r.RolePublisher, "POST " + team: r.RoleDesigner, "POST /api/reporting/v1/team/issues": r.RoleIssuer, "GET " + team: r.RoleViewer} {
		if got := httpapi.RequiredRole(strings.Fields(c)[0], c); got != want {
			t.Errorf("%s: %s", c, got)
		}
	}
	// No binding may be the team.
	if !(r.Identity{Tenant: "t", Issuer: "i", Subject: r.TeamSubject}).IsTeam() {
		t.Fatal("the reserved subject")
	}
}
