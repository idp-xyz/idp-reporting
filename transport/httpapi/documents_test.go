package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"strings"
	"testing"
	"time"
)

type documentRenderer func(context.Context, doctemplates.Document, map[string]string) ([]byte, error)

func (f documentRenderer) Render(ctx context.Context, d doctemplates.Document, v map[string]string, _ []byte, _ *doctemplates.TableData) ([]byte, error) {
	return f(ctx, d, v)
}

// tableRenderer records the table rows the route binds.
type tableRenderer struct {
	documentRenderer
	bound **doctemplates.TableData
}

func (f tableRenderer) Render(ctx context.Context, d doctemplates.Document, v map[string]string, _ []byte, table *doctemplates.TableData) ([]byte, error) {
	*f.bound = table
	return f.documentRenderer(ctx, d, v)
}
func TestDocumentPreviewPrivateFrozenAndReauthorizedAfterRender(t *testing.T) {
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	source.Grant(owner, contractsim.DefaultGrant())
	adapter := &changingLayoutCatalog{Adapter: source}
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: adapter}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	job, e := service.SubmitSnapshot(t.Context(), p, contractsim.FinancialQuery(), "pdf-test")
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
	auth, _ := httpapi.DemoBearer(secret, p)
	other := false
	wrapped := func(req *http.Request) (r.Principal, error) {
		v, e := auth(req)
		if other {
			v.Subject = "other"
		}
		return v, e
	}
	fallback, _ := httpapi.Handler(service, wrapped)
	mode := ""
	calls := 0
	pdf := []byte("%PDF-1.7\nprivate preview\n%%EOF")
	renderer := documentRenderer(func(_ context.Context, _ doctemplates.Document, values map[string]string) ([]byte, error) {
		calls++
		if values["n1"] != "9007199254740893.00" {
			t.Fatal("caller values or rounding", values)
		}
		if mode == "revoke" {
			source.Revoke(owner)
		}
		if mode == "fields" {
			adapter.allowed = "customer.id"
		}
		return pdf, nil
	})
	h, e := httpapi.WithDocumentPreviews(fallback, service, wrapped, renderer, nil)
	if e != nil {
		t.Fatal(e)
	}
	config := doctemplates.Document{Schema: "1.0", Kind: "document", Binding: doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset}, Elements: []doctemplates.Element{{ID: "n1", Source: "row", Field: "ar.open_balance", X: 10, Y: 25, Width: 190, Height: 10, FontSize: 12, Align: "right"}}}
	input := map[string]any{"config": config, "snapshot_id": job.SnapshotID, "row_index": 2}
	post := func() int {
		t.Helper()
		body, _ := json.Marshal(input)
		return request(h, "POST", "/api/reporting/v1/document-previews", body, true).Code
	}
	body, _ := json.Marshal(input)
	if request(h, "POST", "/api/reporting/v1/document-previews", body, false).Code != 401 {
		t.Fatal("anonymous")
	}
	w := request(h, "POST", "/api/reporting/v1/document-previews", body, true)
	hash := sha256.Sum256(pdf)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || w.Header().Get("X-Content-SHA256") != hex.EncodeToString(hash[:]) || w.Body.String() != string(pdf) {
		t.Fatal(w.Code, w.Body.String())
	}
	// A schema 1.3 template binds its table's rows from the snapshot in the template's column
	// order; a column the dataset does not have is a denial.
	withTable := config
	withTable.Schema, withTable.Print = "1.3", &doctemplates.PrintProfile{Mode: "full"}
	withTable.Table = &doctemplates.Table{X: 10, Y: 60, Width: 190, Height: 100, FontSize: 9, Columns: []doctemplates.TableColumn{{Field: "ar.open_balance", Width: 90}, {Field: "customer.id", Width: 100}}}
	input["config"] = withTable
	var bound *doctemplates.TableData
	renderer2 := documentRenderer(func(_ context.Context, _ doctemplates.Document, _ map[string]string) ([]byte, error) { return pdf, nil })
	h2, _ := httpapi.WithDocumentPreviews(fallback, service, wrapped, tableRenderer{renderer2, &bound}, nil)
	body, _ = json.Marshal(input)
	if w := request(h2, "POST", "/api/reporting/v1/document-previews", body, true); w.Code != 200 || bound == nil || len(bound.Columns) != 2 || bound.Columns[0].ID != "ar.open_balance" || len(bound.Rows) == 0 || bound.Rows[0][0] == "" || !bound.Metric[0] || bound.Metric[1] {
		t.Fatalf("table preview: %d %+v", w.Code, bound)
	}
	withTable.Table.Columns[1].Field = "nonexistent"
	input["config"] = withTable
	body, _ = json.Marshal(input)
	if w := request(h2, "POST", "/api/reporting/v1/document-previews", body, true); w.Code != 403 {
		t.Fatalf("table with an unknown field: %d", w.Code)
	}
	input["config"] = config
	// A template naming a background on a server without a background store is unsupported.
	withBackground := config
	withBackground.Schema, withBackground.Print = "1.2", &doctemplates.PrintProfile{Mode: "full"}
	withBackground.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64)}
	input["config"] = withBackground
	if post() != 422 {
		t.Fatal("background without a store")
	}
	input["config"] = config
	other = true
	if post() != 404 {
		t.Fatal("owner leak")
	}
	other = false
	delete(input, "row_index")
	if post() != 400 {
		t.Fatal("missing row")
	}
	input["row_index"] = 2
	input["values"] = map[string]string{"n1": "evil"}
	if post() != 400 {
		t.Fatal("caller values")
	}
	delete(input, "values")
	wrong := config
	wrong.Binding.Integration = "other"
	input["config"] = wrong
	if post() != 403 {
		t.Fatal("wrong binding")
	}
	input["config"] = config
	before := calls
	mode = "fields"
	if post() != 403 || calls != before+1 {
		t.Fatal("field revoke during render")
	}
	adapter.allowed = ""
	mode = "revoke"
	if post() != 403 {
		t.Fatal("current authority not rechecked")
	}
	if request(fallback, "POST", "/api/reporting/v1/document-previews", body, true).Code != 404 {
		t.Fatal("route not opt-in")
	}
}
