package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type backgroundRenderer func(context.Context, doctemplates.Document, map[string]string, []byte) ([]byte, error)

func (f backgroundRenderer) Render(ctx context.Context, d doctemplates.Document, v map[string]string, b []byte) ([]byte, error) {
	return f(ctx, d, v, b)
}

func TestBackgroundHTTPPrivateAssetsImmutableTemplatesAndRender(t *testing.T) {
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	source.Grant(owner, contractsim.DefaultGrant())
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: source}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	job, e := service.SubmitSnapshot(t.Context(), p, contractsim.FinancialQuery(), "bg-test")
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
	assetDir := filepath.Join(t.TempDir(), "assets")
	assets, e := doctemplates.OpenAssets(assetDir)
	if e != nil {
		t.Fatal(e)
	}
	defer assets.Close()
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	demo, _ := httpapi.DemoBearer(secret, p)
	other := false
	auth := func(req *http.Request) (r.Principal, error) {
		v, e := demo(req)
		if other {
			v.Subject = "other"
		}
		return v, e
	}
	raw, e := os.ReadFile("../../doctemplates/testdata/a4-background.png")
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	wantImage := true
	corrupt := false
	renderer := backgroundRenderer(func(_ context.Context, d doctemplates.Document, values map[string]string, data []byte) ([]byte, error) {
		calls++
		if (len(data) > 0) != wantImage || values["n1"] != "9007199254740893.00" {
			t.Fatal("wrong resolved input", len(data), values)
		}
		if corrupt {
			files, _ := filepath.Glob(filepath.Join(assetDir, "*.json"))
			b, _ := os.ReadFile(files[0])
			b = bytes.Replace(b, []byte(`"width":630`), []byte(`"width":631`), 1)
			if e := os.WriteFile(files[0], b, 0600); e != nil {
				t.Fatal(e)
			}
		}
		return []byte("%PDF-1.7\npreview\n%%EOF"), nil
	})
	fallback, _ := httpapi.Handler(service, auth)
	h, e := httpapi.WithDocumentAssets(fallback, service, auth, assets)
	if e != nil {
		t.Fatal(e)
	}
	h, e = httpapi.WithDocumentPreviews(h, service, auth, renderer, assets)
	if e != nil {
		t.Fatal(e)
	}
	h, e = httpapi.WithDocumentTemplates(h, service, auth, store, renderer, assets)
	if e != nil {
		t.Fatal(e)
	}
	post := func(path string, input any, want int) []byte {
		t.Helper()
		b, _ := json.Marshal(input)
		w := request(h, "POST", "/api/reporting/v1/"+path, b, true)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	config := savedTemplate("ar.open_balance")
	binding := config.Binding
	input := map[string]any{"binding": binding, "png": base64.StdEncoding.EncodeToString(raw)}
	b, _ := json.Marshal(input)
	if request(h, "POST", "/api/reporting/v1/document-backgrounds", b, false).Code != 401 {
		t.Fatal("anonymous")
	}
	var first, second doctemplates.BackgroundRef
	json.Unmarshal(post("document-backgrounds", input, 201), &first)
	json.Unmarshal(post("document-backgrounds", input, 201), &second)
	if first.ID == second.ID || first.SHA256 != second.SHA256 {
		t.Fatal("immutable refs")
	}
	for _, value := range []any{map[string]any{"binding": binding, "png": "bad base64!"}, map[string]any{"binding": binding, "png": base64.StdEncoding.EncodeToString([]byte("%PDF-1.7"))}, map[string]any{"binding": binding, "png": input["png"], "url": "https://example.test"}} {
		post("document-backgrounds", value, 400)
	}
	config.Schema = "1.2"
	config.Print = &doctemplates.PrintProfile{Mode: "full"}
	config.Background = &first
	var entry struct {
		ID      string                `json:"id"`
		Version int                   `json:"version"`
		Config  doctemplates.Document `json:"config"`
	}
	json.Unmarshal(post("document-templates", map[string]any{"id": "", "expected_version": 0, "config": config}, 201), &entry)
	id := entry.ID
	if entry.Config.Background == nil || *entry.Config.Background != first {
		t.Fatal("saved ref")
	}
	config.Background = &second
	post("document-templates", map[string]any{"id": id, "expected_version": 1, "config": config}, 201)
	w := request(h, "GET", "/api/reporting/v1/document-templates/"+id+"?version=1", nil, true)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &entry)
	if *entry.Config.Background != first {
		t.Fatal("v1 replaced")
	}
	post("document-templates/"+id+"/release", map[string]any{"action": "publish", "version": 1, "expected_revision": 0}, 200)
	post("document-templates/"+id+"/previews", map[string]any{"version": 1, "snapshot_id": job.SnapshotID, "row_index": 2}, 200)
	config.Print.Mode = "variables"
	wantImage = false
	post("document-previews", map[string]any{"config": config, "snapshot_id": job.SnapshotID, "row_index": 2}, 200)
	other = true
	w = request(h, "GET", "/api/reporting/v1/document-backgrounds", nil, true)
	if w.Code != 200 || w.Body.String() != "[]\n" {
		t.Fatal("asset list leak", w.Code, w.Body.String())
	}
	post("document-templates", map[string]any{"id": "", "expected_version": 0, "config": config}, 404)
	other = false
	bad := first
	bad.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	config.Background = &bad
	post("document-templates", map[string]any{"id": "", "expected_version": 0, "config": config}, 409)
	post("document-previews", map[string]any{"config": config, "snapshot_id": job.SnapshotID, "row_index": 2}, 409)
	config.Background = &first
	config.Print.Mode = "full"
	wantImage = true
	corrupt = true
	post("document-previews", map[string]any{"config": config, "snapshot_id": job.SnapshotID, "row_index": 2}, 409)
	before := calls
	post("document-templates/"+id+"/release", map[string]any{"action": "publish", "version": 1, "expected_revision": 1}, 409)
	if calls != before {
		t.Fatal("publication rendered")
	}
	// Withdrawal remains possible even if a referenced asset is damaged.
	post("document-templates/"+id+"/release", map[string]any{"action": "withdraw", "version": 0, "expected_revision": 1}, 200)
}
