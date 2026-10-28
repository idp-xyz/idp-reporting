package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/issuance"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stampingRenderer stands in for the Typst renderer behind the real issuer: a PDF carrying
// the stamp, two pages; it records what it was asked to draw.
type stampingRenderer struct {
	calls  int
	doc    doctemplates.Document
	values map[string]string
	stamp  string
}

func (f *stampingRenderer) Issue(_ context.Context, d doctemplates.Document, values map[string]string, _ []byte, _ *doctemplates.TableData, stamp string) ([]byte, int, error) {
	f.calls++
	f.doc, f.values, f.stamp = d, values, stamp
	return []byte("%PDF-1.7\n" + stamp + "\n%%EOF"), 2, nil
}

func TestIssueRoutesFreezeThePublishedVersionAndListRecords(t *testing.T) {
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	source.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	adapter := &changingLayoutCatalog{Adapter: source}
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	drawn := &stampingRenderer{}
	issuer, e := issuance.New(store, nil, drawn)
	if e != nil {
		t.Fatal(e)
	}
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: adapter}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: issuer})
	if e != nil {
		t.Fatal(e)
	}
	demo, _ := httpapi.DemoBearer(secret, p)
	other := false
	auth := func(req *http.Request) (r.Principal, error) {
		v, e := demo(req)
		if other {
			v.Subject = "other"
		}
		return v, e
	}
	fallback, _ := httpapi.Handler(service, auth)
	renderer := documentRenderer(func(context.Context, doctemplates.Document, map[string]string) ([]byte, error) {
		return []byte("%PDF-1.7 preview"), nil
	})
	h, e := httpapi.WithDocumentTemplates(fallback, service, auth, store, renderer, nil)
	if e != nil {
		t.Fatal(e)
	}
	ctx := t.Context()
	job, e := service.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if job, e = service.Job(ctx, p, job.ID); e != nil {
		t.Fatal(e)
	}
	v1, e := store.Save(p.Identity, "", 0, savedTemplate("ar.open_balance"))
	if e != nil {
		t.Fatal(e)
	}
	post := func(body map[string]any, key string) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		q := request(h, "POST", "/api/reporting/v1/issues", b, true)
		if key != "" {
			q = requestWithKey(h, "POST", "/api/reporting/v1/issues", b, key)
		}
		var out map[string]any
		_ = json.Unmarshal(q.Body.Bytes(), &out)
		return q.Code, out
	}
	input := map[string]any{"template_id": v1.ID, "version": 1, "snapshot_id": job.SnapshotID, "row_index": 2}
	// Capabilities announce issuance; a draft (unpublished) version is refused as such; a
	// missing version is not found; the caller cannot pass a configuration.
	caps := request(h, "GET", "/api/reporting/v1/capabilities", nil, true)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"issuance":true`) || !strings.Contains(caps.Body.String(), `"POST /issues"`) {
		t.Fatal(caps.Body.String())
	}
	if code, out := post(input, ""); code != 409 || out["error"] != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatal("draft version issued", code, out)
	}
	if code, _ := post(map[string]any{"template_id": v1.ID, "version": 2, "snapshot_id": job.SnapshotID, "row_index": 2}, ""); code != 404 {
		t.Fatal("missing version", code)
	}
	if _, e = store.Publish(p.Identity, v1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	withConfig := map[string]any{"template_id": v1.ID, "version": 1, "snapshot_id": job.SnapshotID, "row_index": 2, "config": savedTemplate("customer.id")}
	if code, _ := post(withConfig, ""); code != 400 {
		t.Fatal("caller configuration accepted", code)
	}
	if code, _ := post(map[string]any{"template_id": v1.ID, "version": 1, "snapshot_id": job.SnapshotID, "row_index": 3}, ""); code != 400 {
		t.Fatal("row beyond the snapshot", code)
	}
	if code, _ := post(map[string]any{"template_id": v1.ID, "version": 1, "snapshot_id": job.SnapshotID}, ""); code != 400 {
		t.Fatal("row missing", code)
	}
	// A field the catalog no longer allows is a denial at admission.
	adapter.allowed = "customer.id"
	if code, _ := post(input, ""); code != 403 {
		t.Fatal("unauthorized field issued", code)
	}
	adapter.allowed = ""
	code, out := post(input, "")
	if code != 202 || out["kind"] != "issue" || out["state"] != "queued" || out["format"] != "pdf" {
		t.Fatal(code, out)
	}
	issue := out["issue"].(map[string]any)
	if issue["number"] != 1.0 || issue["template_id"] != v1.ID || issue["template_version"] != 1.0 || issue["row_index"] != 2.0 || len(issue["template_sha256"].(string)) != 64 {
		t.Fatal(issue)
	}
	id := out["job_id"].(string)
	// The same key replays; another key is the next number.
	if code, again := post(input, ""); code != 202 || again["job_id"] != id {
		t.Fatal("replay", code, again)
	}
	code, second := post(input, "another")
	if code != 202 || second["job_id"] == id || second["issue"].(map[string]any)["number"] != 2.0 {
		t.Fatal(code, second)
	}
	// The worker issues the first; the frozen configuration is what was published.
	if _, e = service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if drawn.calls != 1 || drawn.doc.Elements[0].Field != "ar.open_balance" || drawn.values["n1"] != "9007199254740893.00" || !strings.HasPrefix(drawn.stamp, "ISSUED No. 000001 - ") {
		t.Fatalf("renderer: %+v", drawn)
	}
	// The job view, the record, the listing and the download.
	w := request(h, "GET", "/api/reporting/v1/jobs/"+id, nil, true)
	var view r.JobView
	json.Unmarshal(w.Body.Bytes(), &view)
	if w.Code != 200 || view.State != "succeeded" || view.ArtifactID == "" || view.Issue == nil || view.Issue.Number != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", "/api/reporting/v1/issues/"+id, nil, true)
	var rec r.IssueView
	json.Unmarshal(w.Body.Bytes(), &rec)
	if w.Code != 200 || rec.ID != id || rec.Number != 1 || rec.ArtifactID != view.ArtifactID || rec.Pages != 2 || len(rec.SHA256) != 64 || rec.Stamp != "ISSUED No. 000001 - "+rec.IssuedAt+" - "+id || !strings.HasSuffix(rec.IssuedAt, "Z") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", "/api/reporting/v1/issues?limit=10", nil, true)
	var list []r.IssueView
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list) != 2 || list[0].Number != 2 || list[0].State != "queued" || list[0].SHA256 != "" || list[1].ID != id || list[1].SHA256 != rec.SHA256 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/reporting/v1/issues?limit=0", "/api/reporting/v1/issues?limit=101", "/api/reporting/v1/issues?limit=x", "/api/reporting/v1/issues?other=1"} {
		if request(h, "GET", path, nil, true).Code != 400 {
			t.Fatal(path)
		}
	}
	w = request(h, "GET", "/api/reporting/v1/artifacts/"+view.ArtifactID+"/content", nil, true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || w.Header().Get("Content-Disposition") != `attachment; filename="issued-`+id+`.pdf"` || !strings.Contains(w.Body.String(), rec.Stamp) || w.Header().Get("X-Content-SHA256") != rec.SHA256 {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	// Another subject sees no record, no listing entry and no job.
	other = true
	if request(h, "GET", "/api/reporting/v1/issues/"+id, nil, true).Code != 404 {
		t.Fatal("record visible to another subject")
	}
	w = request(h, "GET", "/api/reporting/v1/issues", nil, true)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
	other = false
	// Withdrawn after admission: the queued second job fails with TEMPLATE_NOT_PUBLISHED and
	// keeps its number.
	if _, e = store.Publish(p.Identity, v1.ID, "withdraw", 0, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = service.RunOne(ctx); e == nil {
		t.Fatal("withdrawn template issued")
	}
	w = request(h, "GET", "/api/reporting/v1/issues/"+second["job_id"].(string), nil, true)
	json.Unmarshal(w.Body.Bytes(), &rec)
	if w.Code != 200 || rec.State != "failed" || rec.ErrorCode != "TEMPLATE_NOT_PUBLISHED" || rec.Number != 2 {
		t.Fatal(w.Body.String())
	}
	// Anonymous and without issuance.
	b, _ := json.Marshal(input)
	if request(h, "POST", "/api/reporting/v1/issues", b, false).Code != 401 {
		t.Fatal("anonymous")
	}
	plain, _ := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: adapter}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	fallback2, _ := httpapi.Handler(plain, auth)
	h2, _ := httpapi.WithDocumentTemplates(fallback2, plain, auth, store, renderer, nil)
	if w := request(h2, "POST", "/api/reporting/v1/issues", b, true); w.Code != 422 {
		t.Fatal("issued without an issuer", w.Code, w.Body.String())
	}
	if w := request(h2, "GET", "/api/reporting/v1/capabilities", nil, true); !strings.Contains(w.Body.String(), `"issuance":false`) {
		t.Fatal(w.Body.String())
	}
}

func requestWithKey(h http.Handler, method, path string, body []byte, key string) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, path, bytes.NewReader(body))
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Idempotency-Key", key)
	q.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, q)
	return w
}
