package httpapi_test

import (
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"path/filepath"
	"testing"
)

func TestLayoutRoutesRecheckCatalogAndVersion(t *testing.T) {
	service, _ := setup(t)
	store, e := layouts.Open(filepath.Join(t.TempDir(), "private"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	demoAuth, _ := httpapi.DemoBearer(secret, contractsim.DemoPrincipal())
	revoked := false
	auth := func(req *http.Request) (r.Principal, error) {
		p, e := demoAuth(req)
		if revoked {
			p.Integrations = nil
		}
		return p, e
	}
	h, e := httpapi.Handler(service, auth, store)
	if e != nil {
		t.Fatal(e)
	}
	q := contractsim.FinancialQuery()
	d := layouts.Document{Schema: "1.0", Binding: layouts.Binding{Integration: q.Integration, Dataset: q.Dataset}, Columns: []layouts.Column{{ID: "open_amount", Align: "right"}}}
	// Use an actual published field id rather than a product assumption.
	catalog, e := service.Catalog(t.Context(), contractsim.DemoPrincipal(), q.Integration)
	if e != nil {
		t.Fatal(e)
	}
	d.Columns[0].ID = catalog[0].Columns[0].ID
	body, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "layout": d})
	if request(h, "POST", "/api/reporting/v1/layouts", body, false).Code != 401 {
		t.Fatal("anonymous save")
	}
	w := request(h, "POST", "/api/reporting/v1/layouts", body, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var first layouts.Entry
	if json.Unmarshal(w.Body.Bytes(), &first) != nil {
		t.Fatal("response")
	}
	if request(h, "GET", "/api/reporting/v1/layouts/"+first.ID+"?version=1", nil, true).Code != 200 {
		t.Fatal("get")
	}
	if request(h, "GET", "/api/reporting/v1/layouts/"+first.ID+"/published", nil, true).Code != 404 {
		t.Fatal("draft published")
	}
	releaseBody := []byte(`{"action":"publish","version":1,"expected_revision":0}`)
	if request(h, "POST", "/api/reporting/v1/layouts/"+first.ID+"/release", releaseBody, true).Code != 200 {
		t.Fatal("publish")
	}
	if request(h, "GET", "/api/reporting/v1/layouts/"+first.ID+"/published", nil, true).Code != 200 {
		t.Fatal("read published")
	}
	if request(h, "POST", "/api/reporting/v1/layouts/"+first.ID+"/release", releaseBody, true).Code != 409 {
		t.Fatal("publication conflict")
	}
	body, _ = json.Marshal(map[string]any{"id": first.ID, "expected_version": 0, "layout": d})
	if request(h, "POST", "/api/reporting/v1/layouts", body, true).Code != 409 {
		t.Fatal("stale save")
	}
	d.Columns[0].ID = "not-published"
	body, _ = json.Marshal(map[string]any{"id": "", "expected_version": 0, "layout": d})
	if request(h, "POST", "/api/reporting/v1/layouts", body, true).Code != 403 {
		t.Fatal("unknown field")
	}
	if request(h, "POST", "/api/reporting/v1/layouts", []byte(`{"tenant":"admin"}`), true).Code != 400 {
		t.Fatal("authority fields")
	}
	revoked = true
	if request(h, "GET", "/api/reporting/v1/layouts/"+first.ID+"/release", nil, true).Code != 403 {
		t.Fatal("release history bypassed current rights")
	}
	if request(h, "GET", "/api/reporting/v1/layouts/"+first.ID+"/published", nil, true).Code != 403 {
		t.Fatal("published bypassed current rights")
	}
	if request(h, "POST", "/api/reporting/v1/layouts/"+first.ID+"/release", []byte(`{"action":"withdraw","version":0,"expected_revision":1}`), true).Code != 403 {
		t.Fatal("withdraw bypassed current rights")
	}

	if request(h, "GET", "/api/reporting/v1/layouts/"+first.ID+"?version=1", nil, true).Code != 403 {
		t.Fatal("get bypassed current integration rights")
	}
	listed := request(h, "GET", "/api/reporting/v1/layouts", nil, true)
	if listed.Code != 200 || listed.Body.String() != "[]\n" {
		t.Fatal("list disclosed revoked binding")
	}
	disabled, _ := httpapi.Handler(service, auth)
	if request(disabled, "GET", "/api/reporting/v1/layouts", nil, true).Code != 404 {
		t.Fatal("not opt-in")
	}
}
