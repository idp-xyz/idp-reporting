package httpapi_test

import (
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/dashboards"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"path/filepath"
	"testing"
)

func TestDashboardRoutesPrivateCurrentCatalogAndSeparateFromLayouts(t *testing.T) {
	service, fallback := setup(t)
	store, e := dashboards.Open(filepath.Join(t.TempDir(), "boards"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	demo, _ := httpapi.DemoBearer(secret, contractsim.DemoPrincipal())
	revoked, other := false, false
	auth := func(req *http.Request) (r.Principal, error) {
		p, e := demo(req)
		if revoked {
			p.Integrations = nil
		}
		if other {
			p.Subject = "other"
		}
		return p, e
	}
	h, e := httpapi.WithDashboards(fallback, service, auth, store)
	if e != nil {
		t.Fatal(e)
	}
	reportStore, e := layouts.Open(filepath.Join(t.TempDir(), "reports"))
	if e != nil {
		t.Fatal(e)
	}
	defer reportStore.Close()
	reportHandler, e := httpapi.Handler(service, auth, reportStore)
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := service.Catalog(t.Context(), contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	if e != nil {
		t.Fatal(e)
	}
	var field string
	for _, c := range catalog[0].Columns {
		if c.Type == "decimal" || c.Type == "integer" {
			field = c.ID
			break
		}
	}
	if field == "" {
		t.Fatal("no numeric source column")
	}
	config := dashboards.Document{Schema: "1.0", Kind: "dashboard", Binding: dashboards.Binding{Integration: contractsim.FinanceIntegration, Dataset: catalog[0].Ref}, Widgets: []dashboards.Widget{{Type: "metric", Field: &field}}}
	body, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": config})
	if request(h, "POST", "/api/reporting/v1/dashboards", body, false).Code != 401 {
		t.Fatal("anonymous")
	}
	w := request(h, "POST", "/api/reporting/v1/dashboards", body, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if request(reportHandler, "POST", "/api/reporting/v1/layouts", body, true).Code != 400 {
		t.Fatal("report API accepted dashboard body")
	}
	reportBody, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "layout": map[string]any{"schema_version": "1.0", "binding": config.Binding, "columns": []map[string]string{{"id": field, "align": "right"}}}})
	if request(h, "POST", "/api/reporting/v1/dashboards", reportBody, true).Code != 400 {
		t.Fatal("dashboard API accepted report body")
	}
	var entry struct {
		ID      string
		Version int
	}
	json.Unmarshal(w.Body.Bytes(), &entry)
	path := "/api/reporting/v1/dashboards/" + entry.ID + "?version=1"
	if request(h, "GET", path, nil, true).Code != 200 {
		t.Fatal("get")
	}
	other = true
	if request(h, "GET", path, nil, true).Code != 404 {
		t.Fatal("owner leak")
	}
	other = false
	stale, _ := json.Marshal(map[string]any{"id": entry.ID, "expected_version": 0, "config": config})
	if request(h, "POST", "/api/reporting/v1/dashboards", stale, true).Code != 409 {
		t.Fatal("CAS")
	}
	invalid := config
	bad := "not-published"
	invalid.Widgets = []dashboards.Widget{{Type: "metric", Field: &bad}}
	badBody, _ := json.Marshal(map[string]any{"config": invalid})
	if request(h, "POST", "/api/reporting/v1/dashboards", badBody, true).Code != 403 {
		t.Fatal("unknown metric")
	}
	if request(h, "POST", "/api/reporting/v1/layouts", body, true).Code != 404 {
		t.Fatal("dashboard routed to disabled report layout store")
	}
	if request(fallback, "GET", "/api/reporting/v1/dashboards", nil, true).Code != 404 {
		t.Fatal("dashboard not opt-in")
	}
	revoked = true
	if request(h, "GET", path, nil, true).Code != 403 {
		t.Fatal("current rights bypass")
	}
	list := request(h, "GET", "/api/reporting/v1/dashboards", nil, true)
	if list.Code != 200 || list.Body.String() != "[]\n" {
		t.Fatal("revoked listing")
	}
}
