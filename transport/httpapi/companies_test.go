package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// withoutDirectory is an adapter without the company directory: the interface's four methods and nothing else.
type withoutDirectory struct{ r.Adapter }

// directoryAnswering is an adapter whose directory answers the given list.
type directoryAnswering struct {
	r.Adapter
	list r.CompanyList
}

func (d directoryAnswering) ListCompanies(context.Context, r.SourceContext) (r.CompanyList, error) {
	return d.list, nil
}

// companiesHandler is the core API over the given adapter, with the principal's roles as the test sets them.
func companiesHandler(t *testing.T, a r.Adapter, roles *[]string) http.Handler {
	t.Helper()
	p := contractsim.DemoPrincipal()
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	demo, e := httpapi.DemoBearer(secret, p)
	if e != nil {
		t.Fatal(e)
	}
	h, e := httpapi.Handler(s, func(req *http.Request) (r.Principal, error) {
		q, e := demo(req)
		if roles != nil {
			q.Roles = *roles
		}
		return q, e
	})
	if e != nil {
		t.Fatal(e)
	}
	return h
}

// GET /companies (ADR 0015 decision 3, S19-B): exactly {supported, partial, companies}, under the catalog's authority and refusals; a
// viewer's route; never cached; the source's failure is the catalogue's 503; a list that does not verify is never shown.
func TestTheCompaniesRouteAnswersTheThreeShapes(t *testing.T) {
	const route = "/api/reporting/v1/companies"
	granted := func() *contractsim.Simulator {
		sim := contractsim.Financial()
		sim.Grant(contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), contractsim.DefaultGrant())
		return sim
	}
	body := func(t *testing.T, h http.Handler, path string, status int) string {
		t.Helper()
		w := request(h, "GET", path, nil, true)
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("%s: %v", path, w.Header())
		}
		return strings.TrimSpace(w.Body.String())
	}
	sim := granted()
	h := companiesHandler(t, sim, nil)
	// Supported: the source's list, by name, in its order.
	if got := body(t, h, route+"?integration_id="+contractsim.FinanceIntegration, 200); got != `{"supported":true,"partial":false,"companies":[{"id":"company_demo","name":"演示公司"}]}` {
		t.Fatal(got)
	}
	// Unsupported: 200, supported false, an empty list, never null.
	if got := body(t, companiesHandler(t, withoutDirectory{granted()}, nil), route+"?integration_id="+contractsim.FinanceIntegration, 200); got != `{"supported":false,"partial":false,"companies":[]}` {
		t.Fatal(got)
	}
	// Partial: the source cut the list and says so.
	partial := directoryAnswering{granted(), r.CompanyList{Companies: []r.Company{{ID: "a", Name: "甲"}, {ID: "b", Name: "乙"}}, Partial: true}}
	if got := body(t, companiesHandler(t, partial, nil), route+"?integration_id="+contractsim.FinanceIntegration, 200); got != `{"supported":true,"partial":true,"companies":[{"id":"a","name":"甲"},{"id":"b","name":"乙"}]}` {
		t.Fatal(got)
	}
	// The query: integration_id exactly once and nothing else, as the catalog's.
	for _, path := range []string{route, route + "?integration_id=a&integration_id=b", route + "?integration_id=" + contractsim.FinanceIntegration + "&limit=1", route + "?integration=" + contractsim.FinanceIntegration} {
		if got := body(t, h, path, 400); got != `{"error":"INVALID_REQUEST"}` {
			t.Fatal(path, got)
		}
	}
	// A product the principal does not hold, or none: the catalog's refusal.
	for _, path := range []string{route + "?integration_id=" + contractsim.ShipmentIntegration, route + "?integration_id="} {
		if got := body(t, h, path, 403); got != `{"error":"AUTH_SCOPE_DENIED"}` {
			t.Fatal(path, got)
		}
	}
	// The source's failures: an outage is the catalogue's 503, a list that does not verify is an integrity failure and nothing of it is shown.
	sim.SetUnavailable(true)
	if got := body(t, h, route+"?integration_id="+contractsim.FinanceIntegration, 503); got != `{"error":"AUTHORITY_UNAVAILABLE"}` {
		t.Fatal(got)
	}
	sim.SetUnavailable(false)
	many := make([]r.Company, 1001)
	for i := range many {
		many[i] = r.Company{ID: fmt.Sprintf("c%04d", i), Name: "SECRET-NAME"}
	}
	for name, list := range map[string]r.CompanyList{"1001 companies": {Companies: many}, "an id twice": {Companies: []r.Company{{ID: "a", Name: "SECRET-NAME"}, {ID: "a", Name: "SECRET-NAME"}}}} {
		w := request(companiesHandler(t, directoryAnswering{granted(), list}, nil), "GET", route+"?integration_id="+contractsim.FinanceIntegration, nil, true)
		if w.Code != 409 || strings.TrimSpace(w.Body.String()) != `{"error":"INTEGRITY_FAILURE"}` || strings.Contains(w.Body.String(), "SECRET-NAME") {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// Unauthenticated: 401, nothing of the list.
	if w := request(h, "GET", route+"?integration_id="+contractsim.FinanceIntegration, nil, false); w.Code != 401 || strings.Contains(w.Body.String(), "company_demo") {
		t.Fatal(w.Code, w.Body.String())
	}
}

// The route is a viewer's (S12-A): a principal without the viewer role is refused with the role's code before the source is asked.
func TestTheCompaniesRouteIsAViewers(t *testing.T) {
	if got := httpapi.RequiredRole("GET", "GET /api/reporting/v1/companies"); got != r.RoleViewer {
		t.Fatal(got)
	}
	if action, _, _ := httpapi.AuditAction("GET", "GET /api/reporting/v1/companies"); action != "" {
		t.Fatal("a read is recorded:", action)
	}
	sim := contractsim.Financial()
	sim.Grant(contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), contractsim.DefaultGrant())
	roles := []string{r.RoleDesigner, r.RolePublisher, r.RoleIssuer, r.RoleAdministrator}
	h := companiesHandler(t, sim, &roles)
	w := request(h, "GET", "/api/reporting/v1/companies?integration_id="+contractsim.FinanceIntegration, nil, true)
	if w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"AUTH_ROLE_DENIED"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	roles = []string{r.RoleViewer}
	if w = request(h, "GET", "/api/reporting/v1/companies?integration_id="+contractsim.FinanceIntegration, nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"supported":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
