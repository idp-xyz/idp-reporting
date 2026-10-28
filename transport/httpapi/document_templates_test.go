package httpapi_test

import (
	"context"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func savedTemplate(field string) doctemplates.Document {
	return doctemplates.Document{Schema: "1.0", Kind: "document", Binding: doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset}, Elements: []doctemplates.Element{{ID: "n1", Source: "row", Field: field, X: 10, Y: 25, Width: 190, Height: 10, FontSize: 12, Align: "left"}}}
}
func TestTemplateMetadataCurrentTargetRightsAndOwner(t *testing.T) {
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	source.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	adapter := &changingLayoutCatalog{Adapter: source}
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: adapter}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	demo, _ := httpapi.DemoBearer(secret, p)
	other, revoked := false, false
	auth := func(req *http.Request) (r.Principal, error) {
		v, e := demo(req)
		if other {
			v.Subject = "other"
		}
		if revoked {
			v.Integrations = nil
		}
		return v, e
	}
	fallback, _ := httpapi.Handler(service, auth)
	h, e := httpapi.WithDocumentTemplates(fallback, service, auth, store, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	save := func(id string, version int, d doctemplates.Document) string {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id": id, "expected_version": version, "config": d})
		w := request(h, "POST", "/api/reporting/v1/document-templates", body, true)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct{ ID string }
		json.Unmarshal(w.Body.Bytes(), &result)
		return result.ID
	}
	id := save("", 0, savedTemplate("customer.id"))
	save(id, 1, savedTemplate("ar.open_balance"))
	path := "/api/reporting/v1/document-templates/" + id
	if request(h, "GET", path+"?version=1", nil, false).Code != 401 {
		t.Fatal("anonymous")
	}
	other = true
	for _, suffix := range []string{"?version=1", "/release", "/published"} {
		if request(h, "GET", path+suffix, nil, true).Code != 404 {
			t.Fatal("owner leak", suffix)
		}
	}
	other = false
	adapter.allowed = "customer.id"
	if request(h, "POST", path+"/release", []byte(`{"action":"publish","version":2,"expected_revision":0}`), true).Code != 403 {
		t.Fatal("wrong target authorized")
	}
	if request(h, "POST", path+"/release", []byte(`{"action":"publish","version":1,"expected_revision":0}`), true).Code != 200 {
		t.Fatal("valid target")
	}
	if request(h, "POST", path+"/release", []byte(`{"action":"publish","version":1,"expected_revision":0}`), true).Code != 409 {
		t.Fatal("release CAS")
	}
	adapter.allowed = "ar.open_balance"
	if request(h, "GET", path+"/published", nil, true).Code != 403 {
		t.Fatal("removed published field")
	}
	if request(h, "GET", path+"/release", nil, true).Code != 200 {
		t.Fatal("history blocked by old field")
	}
	if request(h, "POST", path+"/release", []byte(`{"action":"withdraw","version":0,"expected_revision":1}`), true).Code != 200 {
		t.Fatal("cannot withdraw")
	}
	if request(h, "POST", path+"/release", []byte(`{"action":"publish","version":2,"expected_revision":2}`), true).Code != 200 {
		t.Fatal("new target blocked by v1")
	}
	revoked = true
	if request(h, "GET", path+"/release", nil, true).Code != 403 {
		t.Fatal("revoked binding")
	}
	if request(h, "POST", path+"/previews", []byte(`{}`), true).Code != 404 {
		t.Fatal("PDF metadata-only runtime")
	}
}
func TestSavedTemplatePreviewUsesServerVersionWithoutCallerConfig(t *testing.T) {
	service, fallback := setup(t)
	p := contractsim.DemoPrincipal()
	auth, _ := httpapi.DemoBearer(secret, p)
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	first, e := store.Save(p.Identity, "", 0, savedTemplate("ar.open_balance"))
	if e != nil {
		t.Fatal(e)
	}
	draft := savedTemplate("customer.id")
	if _, e = store.Save(p.Identity, first.ID, 1, draft); e != nil {
		t.Fatal(e)
	}
	job, e := service.SubmitSnapshot(t.Context(), p, contractsim.FinancialQuery(), "saved-pdf")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.RunOne(t.Context()); e != nil {
		t.Fatal(e)
	}
	job, e = service.Job(t.Context(), p, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	renderer := documentRenderer(func(_ context.Context, d doctemplates.Document, values map[string]string) ([]byte, error) {
		calls++
		if d.Elements[0].Field != "ar.open_balance" || values["n1"] != "9007199254740893.00" {
			t.Fatal("wrong version or values", d, values)
		}
		return []byte("%PDF-1.7\nserver saved preview\n%%EOF"), nil
	})
	h, e := httpapi.WithDocumentTemplates(fallback, service, auth, store, renderer, nil)
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/reporting/v1/document-templates/" + first.ID + "/previews"
	input := map[string]any{"version": 1, "snapshot_id": job.SnapshotID, "row_index": 2}
	body, _ := json.Marshal(input)
	w := request(h, "POST", path, body, true)
	if w.Code != 200 || w.Header().Get("X-Template-Version") != "1" || w.Header().Get("X-Template-ID") != first.ID || calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	input["config"] = draft
	body, _ = json.Marshal(input)
	if request(h, "POST", path, body, true).Code != 400 || calls != 1 {
		t.Fatal("accepted caller template")
	}
	body, _ = json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": map[string]any{"schema_version": "1.0", "kind": "dashboard", "widgets": []any{}}})
	if request(h, "POST", "/api/reporting/v1/document-templates", body, true).Code != 400 {
		t.Fatal("wrong document type")
	}
}
