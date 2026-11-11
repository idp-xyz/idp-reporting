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
	"strings"
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

	releasePath := "/api/reporting/v1/dashboards/" + entry.ID + "/release"
	publishedPath := "/api/reporting/v1/dashboards/" + entry.ID + "/published"
	publish := []byte(`{"action":"publish","version":1,"expected_revision":0}`)
	if request(h, "POST", releasePath, publish, false).Code != 401 {
		t.Fatal("anonymous publish")
	}
	other = true
	for _, path := range []string{releasePath, publishedPath} {
		if request(h, "GET", path, nil, true).Code != 404 {
			t.Fatal("release owner leak")
		}
	}
	if request(h, "POST", releasePath, publish, true).Code != 404 {
		t.Fatal("cross-owner publish")
	}
	other = false
	for _, invalid := range []string{`{"action":"share","version":1,"expected_revision":0}`, `{"action":"publish","version":1,"expected_revision":0,"owner":"x"}`, `null`} {
		if request(h, "POST", releasePath, []byte(invalid), true).Code != 400 {
			t.Fatal("invalid release body")
		}
	}
	if request(h, "POST", releasePath, publish, true).Code != 200 {
		t.Fatal("publish")
	}
	if request(h, "POST", releasePath, publish, true).Code != 409 {
		t.Fatal("release CAS")
	}
	if request(h, "GET", publishedPath, nil, true).Code != 200 {
		t.Fatal("published read")
	}
	revoked = true
	for _, path := range []string{releasePath, publishedPath} {
		if request(h, "GET", path, nil, true).Code != 403 {
			t.Fatal("revoked release disclosure")
		}
	}
	if request(h, "POST", releasePath, []byte(`{"action":"withdraw","version":0,"expected_revision":1}`), true).Code != 403 {
		t.Fatal("revoked integration mutation")
	}
	if request(h, "GET", path, nil, true).Code != 403 {
		t.Fatal("current rights bypass")
	}
	list := request(h, "GET", "/api/reporting/v1/dashboards", nil, true)
	if list.Code != 200 || list.Body.String() != "[]\n" {
		t.Fatal("revoked listing")
	}
}

// S21-A: a line and a pie are authorized as the bar is (a numeric field and a column of the bound dataset, 403 otherwise); a title and a
// width are stored as written and a bad one is 400 before anything is looked up.
func TestLineAndPieChartsAreAuthorizedAsTheBarAndKeepTitleAndWidth(t *testing.T) {
	service, fallback := setup(t)
	store, e := dashboards.Open(filepath.Join(t.TempDir(), "boards"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	demo, _ := httpapi.DemoBearer(secret, contractsim.DemoPrincipal())
	h, e := httpapi.WithDashboards(fallback, service, demo, store)
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := service.Catalog(t.Context(), contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	if e != nil {
		t.Fatal(e)
	}
	var field, text string
	for _, c := range catalog[0].Columns {
		if (c.Type == "decimal" || c.Type == "integer") && field == "" {
			field = c.ID
		} else if c.Type == "string" && text == "" {
			text = c.ID
		}
	}
	if field == "" || text == "" {
		t.Fatal("the dataset needs a numeric and a text column", catalog[0].Columns)
	}
	title, full := "账龄走势 · 2026", "full"
	document := func(widgets ...dashboards.Widget) []byte {
		config := dashboards.Document{Schema: "1.0", Kind: "dashboard", Binding: dashboards.Binding{Integration: contractsim.FinanceIntegration, Dataset: catalog[0].Ref}, Widgets: widgets}
		body, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": config})
		return body
	}
	w := request(h, "POST", "/api/reporting/v1/dashboards", document(
		dashboards.Widget{Type: "line", Category: &text, Field: &field, Title: &title, Width: &full},
		dashboards.Widget{Type: "pie", Category: &text, Field: &field},
		dashboards.Widget{Type: "table", Columns: []string{text}, Title: &title},
	), true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var entry struct {
		ID     string
		Config dashboards.Document
	}
	json.Unmarshal(w.Body.Bytes(), &entry)
	if len(entry.Config.Widgets) != 3 || entry.Config.Widgets[0].Title == nil || *entry.Config.Widgets[0].Title != title || entry.Config.Widgets[0].Width == nil || *entry.Config.Widgets[0].Width != "full" || entry.Config.Widgets[1].Title != nil || entry.Config.Widgets[1].Width != nil || entry.Config.Widgets[2].Title == nil {
		t.Fatal("the answer is not the document as written", w.Body.String())
	}
	got := request(h, "GET", "/api/reporting/v1/dashboards/"+entry.ID+"?version=1", nil, true)
	if got.Code != 200 || got.Body.String() != w.Body.String() {
		t.Fatal("reopened differently", got.Code, got.Body.String())
	}
	unknown, numberAsCategory := "not-a-column", field
	for name, widget := range map[string]dashboards.Widget{
		"line of an unknown field":    {Type: "line", Category: &text, Field: &unknown},
		"pie of an unknown field":     {Type: "pie", Category: &text, Field: &unknown},
		"line of a text field":        {Type: "line", Category: &text, Field: &text},
		"pie of a text field":         {Type: "pie", Category: &text, Field: &text},
		"line of an unknown category": {Type: "line", Category: &unknown, Field: &field},
		"pie of an unknown category":  {Type: "pie", Category: &unknown, Field: &field},
	} {
		if code := request(h, "POST", "/api/reporting/v1/dashboards", document(widget), true).Code; code != 403 {
			t.Fatal(name, code)
		}
	}
	// A numeric column is a column too: a chart may take it as its category.
	if code := request(h, "POST", "/api/reporting/v1/dashboards", document(dashboards.Widget{Type: "pie", Category: &numberAsCategory, Field: &field}), true).Code; code != 201 {
		t.Fatal("numeric category", code)
	}
	empty, long, control, third := "", strings.Repeat("标", 65), "a\tb", "third"
	for name, widget := range map[string]dashboards.Widget{
		"empty title":   {Type: "line", Category: &text, Field: &field, Title: &empty},
		"65 characters": {Type: "pie", Category: &text, Field: &field, Title: &long},
		"control":       {Type: "metric", Field: &field, Title: &control},
		"third":         {Type: "table", Columns: []string{text}, Width: &third},
	} {
		if code := request(h, "POST", "/api/reporting/v1/dashboards", document(widget), true).Code; code != 400 {
			t.Fatal(name, code)
		}
	}
	for _, raw := range []string{
		`{"type":"line","category":"` + text + `","field":"` + field + `","title":null}`,
		`{"type":"pie","category":"` + text + `","field":"` + field + `","width":"Full"}`,
		`{"type":"area","category":"` + text + `","field":"` + field + `"}`,
		`{"type":"line","field":"` + field + `"}`,
		`{"type":"pie","category":"` + text + `","field":"` + field + `","columns":["` + text + `"]}`,
	} {
		body := []byte(`{"id":"","expected_version":0,"config":{"schema_version":"1.0","kind":"dashboard","binding":{"integration_id":"` + contractsim.FinanceIntegration + `","dataset":{"id":"` + catalog[0].Ref.ID + `","version":"` + catalog[0].Ref.Version + `"}},"widgets":[` + raw + `]}}`)
		if code := request(h, "POST", "/api/reporting/v1/dashboards", body, true).Code; code != 400 {
			t.Fatal(raw, code)
		}
	}
}
