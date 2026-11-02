package httpapi_test

import (
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Quotas over HTTP (S04-D, docs/S04D_SCOPE.md D3): a submission over a quota is 429 {"error":"QUOTA_EXCEEDED","quota":<name>} with Retry-After: 60,
// nothing admitted and no Location; the capabilities carry the numbers in force.
func quotaSetup(t *testing.T, q r.Quotas) (*r.Service, http.Handler) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}, Quotas: q})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	h, e := httpapi.Handler(s, auth)
	if e != nil {
		t.Fatal(e)
	}
	return s, h
}

func quotaRefusal(t *testing.T, w *httptest.ResponseRecorder, quota string) {
	t.Helper()
	var body map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e, w.Body.String())
	}
	if w.Code != 429 || !reflect.DeepEqual(body, map[string]any{"error": "QUOTA_EXCEEDED", "quota": quota}) {
		t.Fatalf("want 429 for %s, got %d %s", quota, w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") != "60" || w.Header().Get("Location") != "" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("headers of the refusal: %v", w.Header())
	}
}

func TestQuotaRefusesSnapshotSubmissionsWithoutAdmittingAnything(t *testing.T) {
	s, h := quotaSetup(t, r.Quotas{JobsInFlight: 1})
	b, _ := json.Marshal(contractsim.FinancialQuery())
	first := requestWithKey(h, "POST", "/api/reporting/v1/snapshot-jobs", b, "one")
	if first.Code != 202 || first.Header().Get("Location") == "" {
		t.Fatal(first.Code, first.Body.String())
	}
	quotaRefusal(t, requestWithKey(h, "POST", "/api/reporting/v1/snapshot-jobs", b, "two"), r.QuotaJobsInFlight)
	// The replay of the first key is the first job, not a refusal.
	again := requestWithKey(h, "POST", "/api/reporting/v1/snapshot-jobs", b, "one")
	if again.Code != 202 || again.Header().Get("Location") != first.Header().Get("Location") || again.Header().Get("Retry-After") != "" {
		t.Fatal(again.Code, again.Header(), again.Body.String())
	}
	// One job is waiting, and only one.
	for i, want := range []bool{true, false} {
		if ran, e := s.RunOne(t.Context()); e != nil || ran != want {
			t.Fatalf("run %d: ran=%t %v, want %t", i, ran, e, want)
		}
	}
}

func TestQuotaRefusesExportSubmissionsForEachQuota(t *testing.T) {
	for _, c := range []struct {
		quota  string
		limits r.Quotas
	}{{r.QuotaJobsInFlight, r.Quotas{JobsInFlight: 1}}, {r.QuotaStoredResults, r.Quotas{StoredResults: 1}}, {r.QuotaAdmissionsPerHour, r.Quotas{AdmissionsPerHour: 1}}} {
		s, h := quotaSetup(t, c.limits)
		ctx := t.Context()
		p := contractsim.DemoPrincipal()
		snap, e := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
		if e != nil {
			t.Fatal(e)
		}
		if ran, e := s.RunOne(ctx); e != nil || !ran {
			t.Fatal(ran, e)
		}
		snap, e = s.Job(ctx, p, snap.ID)
		if e != nil || snap.SnapshotID == "" {
			t.Fatal(snap, e)
		}
		body := []byte(`{"snapshot_id":"` + snap.SnapshotID + `"}`)
		if c.quota == r.QuotaJobsInFlight {
			// The snapshot job finished, so its slot is free: an export takes it, and the next is over.
			if w := requestWithKey(h, "POST", "/api/reporting/v1/exports", body, "e1"); w.Code != 202 {
				t.Fatal(w.Code, w.Body.String())
			}
			body = []byte(`{"snapshot_id":"` + snap.SnapshotID + `","format":"xlsx"}`)
		}
		quotaRefusal(t, requestWithKey(h, "POST", "/api/reporting/v1/exports", body, "e2"), c.quota)
	}
}

func TestQuotaRefusesIssueSubmissions(t *testing.T) {
	v := newIssueHTTP(t, nil)
	v.publish()
	// The default is 8 jobs in flight: eight issue jobs wait, the ninth is over.
	for i := 0; i < 8; i++ {
		if w := v.postRaw(v.input(2), "issue-"+strconv.Itoa(i)); w.Code != 202 {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	quotaRefusal(t, v.postRaw(v.input(2), "issue-8"), r.QuotaJobsInFlight)
	// A replay of an admitted key is its job.
	if w := v.postRaw(v.input(2), "issue-3"); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestCapabilitiesCarryTheQuotasInForce(t *testing.T) {
	for _, c := range []struct {
		limits r.Quotas
		want   map[string]any
	}{
		{r.Quotas{}, map[string]any{"jobs_in_flight": 8.0, "stored_results": 64.0, "admissions_per_hour": 120.0, "queries_in_flight": 4.0}},
		{r.Quotas{JobsInFlight: 3, StoredResults: 7, AdmissionsPerHour: 11, QueriesInFlight: 2}, map[string]any{"jobs_in_flight": 3.0, "stored_results": 7.0, "admissions_per_hour": 11.0, "queries_in_flight": 2.0}},
	} {
		_, h := quotaSetup(t, c.limits)
		w := request(h, "GET", "/api/reporting/v1/capabilities", nil, true)
		var caps map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &caps); e != nil || w.Code != 200 || !reflect.DeepEqual(caps["quotas"], c.want) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}
