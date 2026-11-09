package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// The routes of the scheduled reports (S16-A): a person makes, reads, lists, pauses, resumes and deletes their own schedules and lists a
// schedule's runs; every change is in the audit; the routes are a viewer's; a request that is not exactly the documented one is refused.
func TestScheduleRoutes(t *testing.T) {
	ctx := context.Background()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	store := memory.New(time.Now)
	service, e := r.NewService(r.Config{Repository: store, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	schedules, e := r.NewSchedules(service, r.ScheduleOptions{Store: store, Resolve: func(i r.Identity) (r.Principal, bool) { return p, i == p.Identity }, Limit: 2})
	if e != nil {
		t.Fatal(e)
	}
	var roles []string
	demo, _ := httpapi.DemoBearer(secret, p)
	auth := func(req *http.Request) (r.Principal, error) {
		q, e := demo(req)
		q.Roles = roles
		return q, e
	}
	core, e := httpapi.Handler(service, auth)
	if e != nil {
		t.Fatal(e)
	}
	if w := request(httpapi.WithAudit(core, store), "GET", "/api/reporting/v1/schedules", nil, true); w.Code != 404 {
		t.Fatalf("the routes answer without WithSchedules: %d", w.Code)
	}
	mounted, e := httpapi.WithSchedules(core, service, auth, schedules)
	if e != nil {
		t.Fatal(e)
	}
	h := httpapi.WithAudit(mounted, store)
	in := map[string]any{"name": "应收周报", "query": contractsim.FinancialQuery(), "export_format": "xlsx", "as_of": "previous_day",
		"recurrence": map[string]any{"frequency": "weekly", "at": "07:30", "weekday": 1, "timezone": "Asia/Shanghai"}}
	body, _ := json.Marshal(in)
	w := requestWithKey(h, "POST", "/api/reporting/v1/schedules", body, "s1")
	var v r.ScheduleView
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.ID == "" || w.Header().Get("Location") != "/api/reporting/v1/schedules/"+v.ID || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	if v.State != "active" || v.Format != "xlsx" || v.AsOf != "previous_day" || v.Recurrence.Weekday != 1 || v.NextRunAt == "" || v.Name != "应收周报" {
		t.Fatalf("%+v", v)
	}
	if w := requestWithKey(h, "POST", "/api/reporting/v1/schedules", body, "s1"); w.Code != 201 || !strings.Contains(w.Body.String(), v.ID) {
		t.Fatal("a replay", w.Code, w.Body.String())
	}
	// Refusals: no key, an unknown field, a recurrence out of its forms, a format the service does not write, over the limit.
	if w := request(h, "POST", "/api/reporting/v1/schedules", body, true); w.Code != 201 {
		t.Fatal("the request helper's key", w.Code, w.Body.String())
	}
	noKey := requestWithKey(h, "POST", "/api/reporting/v1/schedules", body, "")
	if noKey.Code != 400 {
		t.Fatal("no key", noKey.Code)
	}
	for name, change := range map[string]func(map[string]any){
		"unknown field": func(m map[string]any) { m["cron"] = "* * * * *" },
		"recurrence": func(m map[string]any) {
			m["recurrence"] = map[string]any{"frequency": "daily", "at": "7:30", "timezone": "Asia/Shanghai"}
		},
		"zone": func(m map[string]any) {
			m["recurrence"] = map[string]any{"frequency": "daily", "at": "07:30", "timezone": "Local"}
		},
		"format": func(m map[string]any) { m["export_format"] = "pdf" },
	} {
		m := map[string]any{}
		for k, x := range in {
			m[k] = x
		}
		change(m)
		b, _ := json.Marshal(m)
		want := 400
		if name == "format" {
			want = 422
		}
		if w := requestWithKey(h, "POST", "/api/reporting/v1/schedules", b, "x-"+name); w.Code != want {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := requestWithKey(h, "POST", "/api/reporting/v1/schedules", body, "s3"); w.Code != 429 || !strings.Contains(w.Body.String(), `"quota":"schedules"`) || w.Header().Get("Retry-After") == "" {
		t.Fatal("over the limit", w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/api/reporting/v1/schedules/"+v.ID, nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"schedule_id":"`+v.ID+`"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var list []r.ScheduleView
	if w := request(h, "GET", "/api/reporting/v1/schedules?limit=1", nil, true); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/api/reporting/v1/schedules?limit=10&before="+list[0].ID, nil, true); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=x", "?before=XYZ", "?other=1", "?limit=1&limit=2", "?before="} {
		if w := request(h, "GET", "/api/reporting/v1/schedules"+q, nil, true); w.Code != 400 {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	if w := request(h, "GET", "/api/reporting/v1/schedules/"+strings.Repeat("0", 32), nil, true); w.Code != 404 {
		t.Fatal("an unknown schedule", w.Code)
	}
	if w := request(h, "POST", "/api/reporting/v1/schedules/"+v.ID+"/pause", []byte("{}"), true); w.Code != 400 {
		t.Fatal("a pause with a body", w.Code)
	}
	if w := request(h, "POST", "/api/reporting/v1/schedules/"+v.ID+"/pause", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"paused"`) || strings.Contains(w.Body.String(), "next_run_at") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "POST", "/api/reporting/v1/schedules/"+v.ID+"/resume", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"active"`) || !strings.Contains(w.Body.String(), "next_run_at") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/api/reporting/v1/schedules/"+v.ID+"/runs", nil, true); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body.String())
	}
	// Roles: an administrator alone is not a viewer, so neither reads nor changes a schedule.
	roles = []string{r.RoleAdministrator}
	for _, c := range [][2]string{{"GET", "/api/reporting/v1/schedules"}, {"POST", "/api/reporting/v1/schedules/" + v.ID + "/pause"}, {"DELETE", "/api/reporting/v1/schedules/" + v.ID}} {
		if w := request(h, c[0], c[1], nil, true); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"AUTH_ROLE_DENIED"}` {
			t.Errorf("%v: %d %s", c, w.Code, w.Body.String())
		}
	}
	roles = nil
	if w := request(h, "DELETE", "/api/reporting/v1/schedules/"+v.ID, nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"deleted"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, c := range [][2]string{{"GET", "/api/reporting/v1/schedules/" + v.ID}, {"GET", "/api/reporting/v1/schedules/" + v.ID + "/runs"}, {"DELETE", "/api/reporting/v1/schedules/" + v.ID}} {
		if w := request(h, c[0], c[1], nil, true); w.Code != 404 {
			t.Errorf("a deleted schedule: %v %d", c, w.Code)
		}
	}
	if w := request(h, "GET", "/api/reporting/v1/schedules", nil, false); w.Code != 401 {
		t.Fatal("without a token", w.Code)
	}
	// The audit: each change with its outcome and the schedule's id; a read is not recorded.
	events, e := store.Events(ctx, p.Identity, false, 100, "")
	if e != nil {
		t.Fatal(e)
	}
	count := map[string]int{}
	for _, ev := range events {
		if ev.Action == "schedule.change" {
			if ev.ObjectKind != "schedule" {
				t.Fatalf("%+v", ev)
			}
			count[ev.Outcome]++
		}
		if ev.Action == "role.refused" {
			count["role"]++
		}
	}
	// Changes: 2 creations and a replay, the refused ones (no key, 4 bad bodies, over the limit, a pause with a body), a pause, a
	// resumption, a deletion and the deletion of a deleted schedule.
	if count["succeeded"] != 6 || count["refused"] != 8 || count["role"] != 3 {
		t.Fatalf("%v", count)
	}
}
