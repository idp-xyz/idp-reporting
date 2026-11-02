package httpapi_test

import (
	"context"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// retentionClock is the one clock of the test: the store, the service and the simulator follow it.
type retentionClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *retentionClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *retentionClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// stampIssuer is the issuer of the test: a PDF carrying the stamp, from its request alone.
type stampIssuer struct{}

func (stampIssuer) Check(context.Context, r.Job, r.Snapshot) error { return nil }
func (stampIssuer) Issue(_ context.Context, req r.IssueRequest) (r.IssueResult, error) {
	return r.IssueResult{PDF: []byte("%PDF-1.7\n" + req.Stamp + "\n%%EOF"), Pages: 2}, nil
}

// TestViewsAfterCleanupOverHTTP follows a snapshot, an export and an issue through their expiry, the grace period and the sweep, over HTTP:
// what each view says at each stage (S04-B, B5), and that nothing ever answers with a server error or an integrity failure that the
// sweep caused.
func TestViewsAfterCleanupOverHTTP(t *testing.T) {
	ctx := context.Background()
	clock := &retentionClock{t: time.Date(2026, 10, 29, 6, 0, 0, 123456000, time.UTC)}
	p := contractsim.DemoPrincipal()
	finance := contractsim.Financial()
	finance.SetClock(clock.Now)
	finance.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	service, e := r.NewService(r.Config{Repository: memory.New(clock.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}}, CursorKey: []byte(secret),
		Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: stampIssuer{}, Now: clock.Now})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	h, e := httpapi.Handler(service, auth)
	if e != nil {
		t.Fatal(e)
	}
	const base = "/api/reporting/v1"
	get := func(path string) (int, map[string]any) {
		t.Helper()
		w := request(h, "GET", base+path, nil, true)
		var body map[string]any
		if json.Unmarshal(w.Body.Bytes(), &body) != nil {
			body = map[string]any{"raw": w.Body.String()}
		}
		return w.Code, body
	}
	// A snapshot, its export and an issue of its first row.
	run := func() {
		t.Helper()
		if _, e := service.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
	}
	snap, e := service.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	run()
	if snap, e = service.Job(ctx, p, snap.ID); e != nil || snap.SnapshotID == "" {
		t.Fatal(snap, e)
	}
	export, e := service.SubmitExport(ctx, p, snap.SnapshotID, "export")
	if e != nil {
		t.Fatal(e)
	}
	run()
	if export, e = service.Job(ctx, p, export.ID); e != nil || export.ArtifactID == "" {
		t.Fatal(export, e)
	}
	issue, e := service.SubmitIssue(ctx, p, r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(`{"a":1}`), SnapshotID: snap.SnapshotID}, "issue")
	if e != nil {
		t.Fatal(e)
	}
	run()
	if issue, e = service.Job(ctx, p, issue.ID); e != nil || issue.ArtifactID == "" || issue.Issue.Number == "" {
		t.Fatal(issue, e)
	}
	expired := func(body map[string]any) bool { v, _ := body["expired"].(bool); return v }
	// While the read is valid no view says expired, and the file is served.
	for _, id := range []string{snap.ID, export.ID, issue.ID} {
		if code, body := get("/jobs/" + id); code != 200 || body["state"] != "succeeded" || expired(body) || body["expired"] != nil {
			t.Fatal("a valid job", id, code, body)
		}
	}
	if code, body := get("/issues/" + issue.ID); code != 200 || expired(body) || body["expired"] != nil || body["number"] != issue.Issue.Number {
		t.Fatal(code, body)
	}
	if w := request(h, "GET", base+"/artifacts/"+export.ArtifactID+"/content", nil, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Past the read's expiry, inside the grace period: the views say expired; what was stored is still there but refused, as it always was.
	clock.Advance(11 * time.Minute)
	for _, id := range []string{snap.ID, export.ID, issue.ID} {
		if code, body := get("/jobs/" + id); code != 200 || body["state"] != "succeeded" || !expired(body) {
			t.Fatal("an expired job", id, code, body)
		}
	}
	for path, want := range map[string]string{"/artifacts/" + export.ArtifactID + "/content": "INTEGRITY_FAILURE", "/snapshots/" + snap.SnapshotID + "/rows?limit=1": "INTEGRITY_FAILURE"} {
		if code, body := get(path); code != 409 || body["error"] != want {
			t.Fatal("an expired file", path, code, body)
		}
	}
	// Past the grace, swept: the same views with the same ids, the permanent record unchanged, and what is gone refused as not found.
	clock.Advance(r.DefaultSweepGrace)
	res, e := service.Sweep(ctx, r.SweepOptions{})
	if e != nil || res.Artifacts != 2 || res.Snapshots != 1 || res.Jobs != 3 {
		t.Fatal(res, e)
	}
	code, body := get("/jobs/" + snap.ID)
	if code != 200 || body["state"] != "succeeded" || !expired(body) || body["snapshot_id"] != snap.SnapshotID || body["job_id"] != snap.ID || body["kind"] != "snapshot" {
		t.Fatal("a swept snapshot job", code, body)
	}
	code, body = get("/jobs/" + export.ID)
	if code != 200 || !expired(body) || body["artifact_id"] != export.ArtifactID || body["snapshot_id"] != snap.SnapshotID || body["kind"] != "export" {
		t.Fatal("a swept export job", code, body)
	}
	code, body = get("/jobs/" + issue.ID)
	view, _ := body["issue"].(map[string]any)
	if code != 200 || !expired(body) || body["artifact_id"] != issue.ArtifactID || view == nil || view["number"] != issue.Issue.Number || view["template_id"] != strings.Repeat("a", 32) {
		t.Fatal("a swept issue job", code, body)
	}
	code, body = get("/issues/" + issue.ID)
	if code != 200 || !expired(body) || body["state"] != "succeeded" || body["number"] != issue.Issue.Number || body["artifact_id"] != issue.ArtifactID || body["sha256"] == "" || body["pages"] != float64(2) || body["confirmation_ref"] == "" {
		t.Fatal("a swept issue's view", code, body)
	}
	w := request(h, "GET", base+"/issues?limit=10", nil, true)
	var list []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 || !expired(list[0]) || list[0]["number"] != issue.Issue.Number {
		t.Fatal("the issue listing after the sweep", w.Code, w.Body.String())
	}
	for _, path := range []string{"/artifacts/" + export.ArtifactID + "/content", "/artifacts/" + issue.ArtifactID + "/content", "/snapshots/" + snap.SnapshotID + "/rows?limit=1"} {
		if code, body := get(path); code != 404 || body["error"] != "NOT_FOUND" {
			t.Fatal("what was swept", path, code, body)
		}
	}
	// A client that asks again with the key of a job that was swept is denied, as for an expired job; a cancel answers and changes nothing.
	b, _ := json.Marshal(contractsim.FinancialQuery())
	if w := requestWithKey(h, "POST", base+"/snapshot-jobs", b, "snap"); w.Code != 403 || !strings.Contains(w.Body.String(), "AUTH_SCOPE_DENIED") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "POST", base+"/jobs/"+snap.ID+"/cancel", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"succeeded"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Only counts leave the sweep, and the views carry no query value: the swept job's own answer has none of the frozen read.
	raw := request(h, "GET", base+"/jobs/"+snap.ID, nil, true).Body.String()
	for _, secretValue := range []string{"company_demo", "tenant_a", "customer_a", "9007199254740993"} {
		if strings.Contains(raw, secretValue) {
			t.Fatalf("a swept job's view carries %q: %s", secretValue, raw)
		}
	}
	var _ http.Handler = h
}
