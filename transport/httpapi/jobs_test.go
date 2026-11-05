package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// GET /jobs lists the caller's jobs of every kind under the integrations the caller holds, newest first, at most limit of them
// (S07-C, docs/S07C_SCOPE.md): the job view of each and nothing else; another identity sees none; the query is limit alone.
func TestHTTPJobsListingPerIdentity(t *testing.T) {
	ctx := context.Background()
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	a, b := contractsim.Financial(), contractsim.Shipment()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	g := contractsim.DefaultGrant()
	g.CurrencyPolicy = nil
	b.Grant(contractsim.DemoOwner(p, contractsim.ShipmentIntegration), g)
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{
		{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a},
		{Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: b}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	demo, _ := httpapi.DemoBearer(secret, p)
	other := false
	auth := func(req *http.Request) (r.Principal, error) {
		p, e := demo(req)
		if other {
			p.Subject = "other"
		}
		return p, e
	}
	h, e := httpapi.Handler(s, auth)
	if e != nil {
		t.Fatal(e)
	}
	const base = "/api/reporting/v1"
	submit := func(path string, body []byte, key string) r.JobView {
		t.Helper()
		w := requestWithKey(h, "POST", base+path, body, key)
		var v r.JobView
		if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return v
	}
	run := func() {
		t.Helper()
		if ran, e := s.RunOne(ctx); !ran || e != nil {
			t.Fatal(ran, e)
		}
	}
	finance, _ := json.Marshal(contractsim.FinancialQuery())
	shipment, _ := json.Marshal(contractsim.ShipmentQuery())
	first := submit("/snapshot-jobs", finance, "a")
	run()
	second := submit("/snapshot-jobs", shipment, "b")
	run()
	snap := request(h, "GET", base+"/jobs/"+first.ID, nil, true)
	var done r.JobView
	if snap.Code != 200 || json.Unmarshal(snap.Body.Bytes(), &done) != nil || done.State != "succeeded" {
		t.Fatal(snap.Code, snap.Body.String())
	}
	third := submit("/exports", []byte(`{"snapshot_id":"`+done.SnapshotID+`"}`), "c")
	run()
	fourth := submit("/snapshot-jobs", finance, "d") // queued: no worker runs it

	list := func(path string, want int) []map[string]any {
		t.Helper()
		w := request(h, "GET", base+path, nil, true)
		var out []map[string]any
		if w.Code != want || (want == 200 && json.Unmarshal(w.Body.Bytes(), &out) != nil) {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return out
	}
	// Newest first, both integrations, every kind; each entry is the job view: no query, no key, no owner.
	got := list("/jobs", 200)
	ids := []string{}
	for _, j := range got {
		ids = append(ids, j["job_id"].(string))
		for k := range j {
			switch k {
			case "job_id", "kind", "state", "snapshot_id", "format", "artifact_id", "error_code", "issue", "expired":
			default:
				t.Errorf("a listed job carries %q: %v", k, j)
			}
		}
	}
	if len(got) != 4 || ids[0] != fourth.ID || ids[1] != third.ID || ids[2] != second.ID || ids[3] != first.ID {
		t.Fatal(ids, fourth.ID, third.ID, second.ID, first.ID)
	}
	if got[0]["kind"] != "snapshot" || got[0]["state"] != "queued" || got[0]["snapshot_id"] != nil ||
		got[1]["kind"] != "export" || got[1]["state"] != "succeeded" || got[1]["artifact_id"] == nil || got[1]["snapshot_id"] != done.SnapshotID ||
		got[2]["kind"] != "snapshot" || got[2]["state"] != "succeeded" || got[2]["snapshot_id"] == nil ||
		got[3]["snapshot_id"] != done.SnapshotID {
		t.Fatalf("%v", got)
	}
	// The limit cuts the newest; its bounds and any other query are refused; the default is 50.
	if page := list("/jobs?limit=2", 200); len(page) != 2 || page[0]["job_id"] != fourth.ID || page[1]["job_id"] != third.ID {
		t.Fatal(page)
	}
	if page := list("/jobs?limit=100", 200); len(page) != 4 {
		t.Fatal(page)
	}
	// S07-E: kind, state and the cursor narrow and page the listing.
	idsOf := func(page []map[string]any) []string {
		out := []string{}
		for _, j := range page {
			out = append(out, j["job_id"].(string))
		}
		return out
	}
	if page := list("/jobs?kind=snapshot", 200); !slices.Equal(idsOf(page), []string{fourth.ID, second.ID, first.ID}) {
		t.Fatal("kind", idsOf(page))
	}
	if page := list("/jobs?kind=export&state=succeeded", 200); !slices.Equal(idsOf(page), []string{third.ID}) {
		t.Fatal("kind and state", idsOf(page))
	}
	if page := list("/jobs?state=queued", 200); !slices.Equal(idsOf(page), []string{fourth.ID}) {
		t.Fatal("state", idsOf(page))
	}
	if page := list("/jobs?limit=2&before="+third.ID, 200); !slices.Equal(idsOf(page), []string{second.ID, first.ID}) {
		t.Fatal("the page after the export", idsOf(page))
	}
	if page := list("/jobs?before="+first.ID, 200); len(page) != 0 {
		t.Fatal("after the oldest", idsOf(page))
	}
	if page := list("/jobs?kind=snapshot&state=succeeded&limit=1&before="+second.ID, 200); !slices.Equal(idsOf(page), []string{first.ID}) {
		t.Fatal("all four", idsOf(page))
	}
	for _, path := range []string{"/jobs?limit=0", "/jobs?limit=101", "/jobs?limit=x", "/jobs?limit=", "/jobs?other=1", "/jobs?limit=1&limit=2", "/jobs?limit=1&other=1",
		"/jobs?kind=", "/jobs?kind=report", "/jobs?state=done", "/jobs?state=", "/jobs?before=", "/jobs?before=x", "/jobs?kind=export&kind=issue"} {
		if w := request(h, "GET", base+path, nil, true); w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	// The listing does not take the place of the poll, and needs the bearer like everything else.
	if w := request(h, "GET", base+"/jobs/"+third.ID, nil, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "GET", base+"/jobs", nil, false); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Another subject of the same tenant and issuer sees an empty page, not an error and not these jobs; this identity's cursor gives it nothing.
	other = true
	if page := list("/jobs", 200); len(page) != 0 {
		t.Fatal(page)
	}
	if page := list("/jobs?before="+third.ID, 200); len(page) != 0 {
		t.Fatal(page)
	}
	other = false
	// A cancel shows in the listing as in the poll.
	if w := request(h, "POST", base+"/jobs/"+fourth.ID+"/cancel", nil, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if page := list("/jobs?limit=1", 200); len(page) != 1 || page[0]["job_id"] != fourth.ID || page[0]["state"] != "cancelled" {
		t.Fatal(page)
	}
}
