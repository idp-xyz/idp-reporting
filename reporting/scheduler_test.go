package reporting_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// fakeDeliverer answers each attempt with the next outcome of its script (the last one repeats) and keeps what it was given.
type fakeDeliverer struct {
	mu       sync.Mutex
	script   []string
	received []r.Notification
}

func (d *fakeDeliverer) Deliver(_ context.Context, n r.Notification) r.DeliveryResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.received = append(d.received, n)
	outcome := r.DeliveryOutcomeDelivered
	if len(d.script) > 0 {
		outcome = d.script[0]
		if len(d.script) > 1 {
			d.script = d.script[1:]
		}
	}
	return r.DeliveryResult{Outcome: outcome, Status: map[string]int{r.DeliveryOutcomeDelivered: 204, r.DeliveryOutcomeServerError: 503, r.DeliveryOutcomeRejected: 400, r.DeliveryOutcomeRedirected: 302}[outcome]}
}

func (d *fakeDeliverer) all() []r.Notification {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]r.Notification(nil), d.received...)
}

// scheduleWorld is one service on a memory store, a simulator and a clock that all three follow, and the scheduled reports over them. bound
// is what the resolver answers for the demo identity: its binding as it is now.
type scheduleWorld struct {
	clock     *testClock
	store     *memory.Store
	source    *contractsim.Simulator
	svc       *r.Service
	schedules *r.Schedules
	deliver   *fakeDeliverer
	p         r.Principal
	mu        sync.Mutex
	bound     *r.Principal
}

func newScheduleWorld(t *testing.T, limit int) *scheduleWorld {
	t.Helper()
	w := &scheduleWorld{clock: &testClock{t: time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)}, deliver: &fakeDeliverer{}, p: contractsim.DemoPrincipal()}
	w.store = memory.New(w.clock.Now)
	w.source = contractsim.Financial()
	w.source.SetClock(w.clock.Now)
	w.source.Grant(contractsim.DemoOwner(w.p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	var e error
	w.svc, e = r.NewService(r.Config{Repository: w.store, Connections: []r.Connection{{Tenant: w.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: w.source}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Now: w.clock.Now})
	if e != nil {
		t.Fatal(e)
	}
	bound := w.p
	w.bound = &bound
	w.schedules = w.another(t, limit)
	return w
}

// another is a second scheduler on the same store and service: another server.
func (w *scheduleWorld) another(t *testing.T, limit int) *r.Schedules {
	t.Helper()
	s, e := r.NewSchedules(w.svc, r.ScheduleOptions{Store: w.store, Limit: limit, Deliver: w.deliver, Audit: w.store, Resolve: func(i r.Identity) (r.Principal, bool) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.bound == nil || i != w.bound.Identity {
			return r.Principal{}, false
		}
		return *w.bound, true
	}})
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func (w *scheduleWorld) bind(p *r.Principal) { w.mu.Lock(); w.bound = p; w.mu.Unlock() }

func dailyAt(at string) r.ScheduleInput {
	return r.ScheduleInput{Name: "应收日报", Query: contractsim.FinancialQuery(), Recurrence: r.Recurrence{Frequency: "daily", At: at, Timezone: "Asia/Shanghai"}}
}

func (w *scheduleWorld) tick(t *testing.T) r.TickResult {
	t.Helper()
	res, e := w.schedules.Tick(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return res
}

// work runs every queued job, as the workers do.
func (w *scheduleWorld) work(t *testing.T) {
	t.Helper()
	for {
		ran, e := w.svc.RunOne(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if !ran {
			return
		}
	}
}

func (w *scheduleWorld) runs(t *testing.T, id string) []r.RunView {
	t.Helper()
	v, e := w.schedules.Runs(context.Background(), w.p, id, r.ScheduleListing{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	return v
}

// A person's schedules: made under an Idempotency-Key against the published catalog, listed newest first, paused, resumed and deleted, bounded
// per identity, and nobody else's.
func TestSchedulesAreMadeListedAndChangedByTheirOwner(t *testing.T) {
	ctx := context.Background()
	w := newScheduleWorld(t, 2)
	v, e := w.schedules.Create(ctx, w.p, dailyAt("08:00"), "k1")
	if e != nil || v.State != r.ScheduleActive || v.AsOf != r.AsOfRunDate || v.NextRunAt != "2026-11-10T00:00:00Z" || v.Integration != contractsim.FinanceIntegration || v.Query.AsOf != "2026-10-26" {
		t.Fatalf("%+v %v", v, e)
	}
	if again, e := w.schedules.Create(ctx, w.p, dailyAt("08:00"), "k1"); e != nil || again.ID != v.ID {
		t.Fatalf("a replay made another schedule: %+v %v", again, e)
	}
	if _, e := w.schedules.Create(ctx, w.p, dailyAt("09:00"), "k1"); !errors.Is(e, r.ErrConflict) {
		t.Fatalf("another request under the key: %v", e)
	}
	refusals := map[string]struct {
		in   func(r.ScheduleInput) r.ScheduleInput
		want error
	}{
		"name":       {func(in r.ScheduleInput) r.ScheduleInput { in.Name = " "; return in }, r.ErrInvalid},
		"recurrence": {func(in r.ScheduleInput) r.ScheduleInput { in.Recurrence.Frequency = "hourly"; return in }, r.ErrInvalid},
		"as_of":      {func(in r.ScheduleInput) r.ScheduleInput { in.AsOf = "tomorrow"; return in }, r.ErrInvalid},
		"format":     {func(in r.ScheduleInput) r.ScheduleInput { in.Format = "pdf"; return in }, r.ErrUnsupported},
		"dataset": {func(in r.ScheduleInput) r.ScheduleInput {
			in.Query.Dataset.Version = "9.9.9"
			return in
		}, r.ErrUnsupported},
		"product": {func(in r.ScheduleInput) r.ScheduleInput {
			in.Query.Integration = contractsim.ShipmentIntegration
			return in
		}, r.ErrDenied},
		"query": {func(in r.ScheduleInput) r.ScheduleInput { in.Query.Page.Limit = 0; return in }, r.ErrInvalid},
	}
	for name, c := range refusals {
		if _, e := w.schedules.Create(ctx, w.p, c.in(dailyAt("08:00")), "k-"+name); !errors.Is(e, c.want) {
			t.Errorf("%s: %v, want %v", name, e, c.want)
		}
	}
	if _, e := w.schedules.Create(ctx, w.p, dailyAt("08:00"), ""); !errors.Is(e, r.ErrInvalid) {
		t.Fatalf("no key: %v", e)
	}
	w.clock.Advance(time.Second)
	in := dailyAt("10:00")
	in.Format, in.AsOf = "xlsx", r.AsOfPreviousDay
	second, e := w.schedules.Create(ctx, w.p, in, "k2")
	if e != nil || second.Format != "xlsx" || second.AsOf != r.AsOfPreviousDay {
		t.Fatalf("%+v %v", second, e)
	}
	// The limit counts the schedules that are not deleted; a replay is never refused by it.
	if _, e := w.schedules.Create(ctx, w.p, dailyAt("11:00"), "k3"); r.QuotaOf(e) != r.QuotaSchedules {
		t.Fatalf("over the limit: %v", e)
	}
	if again, e := w.schedules.Create(ctx, w.p, in, "k2"); e != nil || again.ID != second.ID {
		t.Fatalf("a replay at the limit: %v", e)
	}
	list, e := w.schedules.List(ctx, w.p, r.ScheduleListing{Limit: 10})
	if e != nil || len(list) != 2 || list[0].ID != second.ID || list[1].ID != v.ID {
		t.Fatalf("%+v %v", list, e)
	}
	if page, e := w.schedules.List(ctx, w.p, r.ScheduleListing{Limit: 10, Before: second.ID}); e != nil || len(page) != 1 || page[0].ID != v.ID {
		t.Fatalf("the second page: %+v %v", page, e)
	}
	// Somebody else sees none of them and changes none of them.
	other := w.p
	other.Subject = "user_b"
	if _, e := w.schedules.Get(ctx, other, v.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatalf("another identity read a schedule: %v", e)
	}
	if _, e := w.schedules.SetState(ctx, other, v.ID, r.SchedulePaused); !errors.Is(e, r.ErrNotFound) {
		t.Fatalf("another identity paused a schedule: %v", e)
	}
	if list, _ := w.schedules.List(ctx, other, r.ScheduleListing{Limit: 10}); len(list) != 0 {
		t.Fatal("another identity lists schedules")
	}
	// A person who no longer holds the product neither reads nor lists the schedule.
	none := w.p
	none.Integrations = []string{"another-product"}
	if _, e := w.schedules.Get(ctx, none, v.ID); !errors.Is(e, r.ErrDenied) {
		t.Fatalf("without the product: %v", e)
	}
	if list, _ := w.schedules.List(ctx, none, r.ScheduleListing{Limit: 10}); len(list) != 0 {
		t.Fatal("listed without the product")
	}
	paused, e := w.schedules.SetState(ctx, w.p, v.ID, r.SchedulePaused)
	if e != nil || paused.State != r.SchedulePaused || paused.NextRunAt != "" {
		t.Fatalf("%+v %v", paused, e)
	}
	// Resumed two days later, it runs at its next occurrence from then: what was missed while it was paused is not run.
	w.clock.Advance(48 * time.Hour)
	resumed, e := w.schedules.SetState(ctx, w.p, v.ID, r.ScheduleActive)
	if e != nil || resumed.State != r.ScheduleActive || resumed.NextRunAt != "2026-11-12T00:00:00Z" {
		t.Fatalf("%+v %v", resumed, e)
	}
	deleted, e := w.schedules.SetState(ctx, w.p, v.ID, r.ScheduleDeleted)
	if e != nil || deleted.State != r.ScheduleDeleted {
		t.Fatalf("%+v %v", deleted, e)
	}
	for _, f := range []func() error{
		func() error { _, e := w.schedules.Get(ctx, w.p, v.ID); return e },
		func() error { _, e := w.schedules.SetState(ctx, w.p, v.ID, r.ScheduleActive); return e },
		func() error { _, e := w.schedules.Runs(ctx, w.p, v.ID, r.ScheduleListing{Limit: 10}); return e },
		func() error { _, e := w.schedules.Create(ctx, w.p, dailyAt("08:00"), "k1"); return e },
	} {
		if e := f(); !errors.Is(e, r.ErrNotFound) {
			t.Fatalf("a deleted schedule: %v", e)
		}
	}
	if _, e := w.schedules.Create(ctx, w.p, dailyAt("11:00"), "k3"); e != nil {
		t.Fatalf("a deleted schedule still counts: %v", e)
	}
}

// A run freezes its snapshot as its owner, exports it, ends succeeded with the ids of its results, is delivered once, without a value of the
// report, and is recorded in the owner's audit; the next run waits for the next occurrence.
func TestAScheduledRunFreezesExportsAndIsDelivered(t *testing.T) {
	ctx := context.Background()
	w := newScheduleWorld(t, 0)
	in := dailyAt("08:00")
	in.Format = "xlsx"
	v, e := w.schedules.Create(ctx, w.p, in, "k1")
	if e != nil {
		t.Fatal(e)
	}
	if res := w.tick(t); res.Started != 0 || res.Advanced != 0 {
		t.Fatalf("a run before its occurrence: %+v", res)
	}
	w.clock.Advance(23 * time.Hour)
	if res := w.tick(t); res.Started != 1 || res.Advanced != 1 {
		t.Fatalf("%+v", res)
	}
	runs := w.runs(t, v.ID)
	if len(runs) != 1 || runs[0].State != r.RunSnapshot || runs[0].DueAt != "2026-11-10T00:00:00Z" || runs[0].AsOf != "2026-11-10" || runs[0].SnapshotJob == "" {
		t.Fatalf("%+v", runs)
	}
	// The run's job is the owner's own, as if the owner had submitted it: listed, read and cancellable by the owner.
	job, e := w.svc.Job(ctx, w.p, runs[0].SnapshotJob)
	if e != nil || job.Kind != "snapshot" || job.State != "queued" {
		t.Fatalf("%+v %v", job, e)
	}
	// Until the job has run, the run waits.
	w.clock.Advance(r.RunRecheck)
	if res := w.tick(t); res.Advanced != 1 || w.runs(t, v.ID)[0].State != r.RunSnapshot {
		t.Fatalf("%+v", res)
	}
	w.work(t)
	w.clock.Advance(r.RunRecheck)
	w.tick(t)
	runs = w.runs(t, v.ID)
	if runs[0].State != r.RunExport || runs[0].SnapshotID == "" || runs[0].ExportJob == "" {
		t.Fatalf("%+v", runs)
	}
	w.work(t)
	w.clock.Advance(r.RunRecheck)
	res := w.tick(t)
	runs = w.runs(t, v.ID)
	run := runs[0]
	if run.State != r.RunSucceeded || run.ArtifactID == "" || run.Delivery != r.DeliveryDelivered || run.FinishedAt == "" || run.Format != "xlsx" || run.ErrorCode != "" {
		t.Fatalf("%+v", run)
	}
	if len(res.Deliveries) != 1 || res.Deliveries[0].Outcome != r.DeliveryOutcomeDelivered || res.Deliveries[0].Attempt != 1 || res.Deliveries[0].Delivery != r.DeliveryDelivered {
		t.Fatalf("%+v", res.Deliveries)
	}
	if a, e := w.svc.Download(ctx, w.p, run.ArtifactID); e != nil || len(a.Content) == 0 {
		t.Fatalf("the owner cannot download the run's file: %v", e)
	}
	sent := w.deliver.all()
	if len(sent) != 1 {
		t.Fatalf("%d deliveries", len(sent))
	}
	n := sent[0]
	if n.Event != r.NotificationEvent || n.RunID != run.ID || n.ScheduleID != v.ID || n.State != r.RunSucceeded || n.SnapshotID != run.SnapshotID || n.ArtifactID != run.ArtifactID ||
		n.Tenant != w.p.Tenant || n.Subject != w.p.Subject || n.Integration != contractsim.FinanceIntegration || n.Attempt != 1 || n.DueAt != run.DueAt {
		t.Fatalf("%+v", n)
	}
	body, _ := json.Marshal(n)
	for _, value := range []string{"customer_a", "receivables", "应收", "company_demo"} {
		if strings.Contains(string(body), value) {
			t.Fatalf("the notification carries %q: %s", value, body)
		}
	}
	events, e := w.store.Events(ctx, w.p.Identity, false, 10, "")
	if e != nil || len(events) != 1 || events[0].Action != "schedule.run" || events[0].ObjectKind != "schedule" || events[0].ObjectID != v.ID || events[0].Outcome != "succeeded" {
		t.Fatalf("%+v %v", events, e)
	}
	// Nothing more until the next occurrence; then the next run.
	w.clock.Advance(time.Hour)
	if res := w.tick(t); res.Started != 0 || res.Advanced != 0 || len(w.deliver.all()) != 1 {
		t.Fatalf("%+v", res)
	}
	w.clock.Advance(24 * time.Hour)
	if res := w.tick(t); res.Started != 1 {
		t.Fatalf("%+v", res)
	}
	if runs := w.runs(t, v.ID); len(runs) != 2 || runs[0].DueAt != "2026-11-11T00:00:00Z" {
		t.Fatalf("%+v", runs)
	}
}

// The file of a scheduled run is there when its mail is read (ADR 0026): with a source that vouches for the scope of its reads, the run's
// snapshot and its export are kept results, served two hours after the run and for the results' time (seven days by default); with a
// source that vouches for nothing they end with the read, as they always did.
func TestAScheduledRunsResultsAreKeptWhenTheSourceVouchesForTheScope(t *testing.T) {
	ctx := context.Background()
	for _, vouch := range []bool{false, true} {
		w := newScheduleWorld(t, 0)
		if vouch {
			w.source.SetScopeLifetime(30 * 24 * time.Hour)
		}
		in := dailyAt("08:00")
		in.Format = "xlsx"
		v, e := w.schedules.Create(ctx, w.p, in, "k1")
		if e != nil {
			t.Fatal(e)
		}
		w.clock.Advance(23 * time.Hour)
		for range 3 { // the run starts, its snapshot is frozen, its export is made
			w.tick(t)
			w.work(t)
			w.clock.Advance(r.RunRecheck)
		}
		w.tick(t)
		run := w.runs(t, v.ID)[0]
		if run.State != r.RunSucceeded || run.SnapshotID == "" || run.ArtifactID == "" {
			t.Fatalf("vouch %v: %+v", vouch, run)
		}
		read := func() (rows, file error) {
			_, rows = w.svc.Rows(ctx, w.p, run.SnapshotID, 10, "")
			_, file = w.svc.Download(ctx, w.p, run.ArtifactID)
			return rows, file
		}
		// Two hours later the owner follows the mail's link.
		w.clock.Advance(2 * time.Hour)
		rows, file := read()
		if !vouch {
			if !errors.Is(rows, r.ErrIntegrity) || !errors.Is(file, r.ErrIntegrity) {
				t.Fatalf("without a scope the run's results end with the read: %v, %v", rows, file)
			}
			continue
		}
		if rows != nil || file != nil {
			t.Fatalf("the run's snapshot and file are refused two hours after the run: %v, %v", rows, file)
		}
		// The source still decides: a revocation reaches what is kept.
		owner := contractsim.DemoOwner(w.p, contractsim.FinanceIntegration)
		w.source.Revoke(owner)
		if rows, file = read(); !errors.Is(rows, r.ErrDenied) || !errors.Is(file, r.ErrDenied) {
			t.Fatalf("a revocation does not reach the kept results: %v, %v", rows, file)
		}
		w.source.Grant(owner, contractsim.DefaultGrant())
		// And the results' time ends them.
		w.clock.Advance(8 * 24 * time.Hour)
		if rows, file = read(); !errors.Is(rows, r.ErrIntegrity) || !errors.Is(file, r.ErrIntegrity) {
			t.Fatalf("eight days later the results are still served: %v, %v", rows, file)
		}
	}
}

// After a downtime the missed occurrences are caught up once, by the oldest, and the schedule goes on from the next occurrence after now.
func TestAScheduleCatchesUpOnceAfterADowntime(t *testing.T) {
	ctx := context.Background()
	w := newScheduleWorld(t, 0)
	v, e := w.schedules.Create(ctx, w.p, dailyAt("08:00"), "k1")
	if e != nil {
		t.Fatal(e)
	}
	w.clock.Advance(5*24*time.Hour + time.Hour)
	if res := w.tick(t); res.Started != 1 {
		t.Fatalf("%+v", res)
	}
	runs := w.runs(t, v.ID)
	if len(runs) != 1 || runs[0].DueAt != "2026-11-10T00:00:00Z" {
		t.Fatalf("%+v", runs)
	}
	got, _ := w.schedules.Get(ctx, w.p, v.ID)
	if got.NextRunAt != "2026-11-15T00:00:00Z" {
		t.Fatalf("next %s", got.NextRunAt)
	}
	w.work(t)
	for i := 0; i < 5; i++ {
		w.clock.Advance(r.RunRecheck)
		w.tick(t)
	}
	if runs := w.runs(t, v.ID); len(runs) != 1 || runs[0].State != r.RunSucceeded {
		t.Fatalf("%+v", runs)
	}
}

// Several schedulers on one store start an occurrence once and submit its job once.
func TestSchedulersOnOneStoreRunAnOccurrenceOnce(t *testing.T) {
	ctx := context.Background()
	w := newScheduleWorld(t, 0)
	v, e := w.schedules.Create(ctx, w.p, dailyAt("08:00"), "k1")
	if e != nil {
		t.Fatal(e)
	}
	w.clock.Advance(24 * time.Hour)
	servers := []*r.Schedules{w.schedules, w.another(t, 0), w.another(t, 0), w.another(t, 0)}
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, e := s.Tick(ctx); e != nil {
					t.Error(e)
				}
			}
		}()
	}
	wg.Wait()
	runs := w.runs(t, v.ID)
	if len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	jobs, e := w.svc.Jobs(ctx, w.p, r.JobListing{Limit: 100})
	if e != nil || len(jobs) != 1 || jobs[0].ID != runs[0].SnapshotJob {
		t.Fatalf("%+v %v", jobs, e)
	}
}

// A run acts as its owner as the owner is bound now: an identity no binding names, a binding without the viewer role, and a source product that
// refuses the read all refuse the run, and no job is submitted; an outage of the source product is tried again, then fails.
func TestAScheduledRunActsAsItsOwnerNow(t *testing.T) {
	ctx := context.Background()
	start := func(t *testing.T, w *scheduleWorld, key string) string {
		t.Helper()
		v, e := w.schedules.Create(ctx, w.p, dailyAt("08:00"), key)
		if e != nil {
			t.Fatal(e)
		}
		w.clock.Advance(24 * time.Hour)
		return v.ID
	}
	only := func(t *testing.T, w *scheduleWorld, id, state, code string) {
		t.Helper()
		runs := w.runs(t, id)
		if len(runs) != 1 || runs[0].State != state || runs[0].ErrorCode != code || runs[0].SnapshotJob != "" {
			t.Fatalf("%+v, want %s %s", runs, state, code)
		}
		if jobs, _ := w.svc.Jobs(ctx, w.p, r.JobListing{Limit: 100}); len(jobs) != 0 {
			t.Fatalf("a job was submitted: %+v", jobs)
		}
	}
	t.Run("unbound", func(t *testing.T) {
		w := newScheduleWorld(t, 0)
		id := start(t, w, "k")
		w.bind(nil)
		w.tick(t)
		only(t, w, id, r.RunRefused, "AUTH_SCOPE_DENIED")
		if n := w.deliver.all(); len(n) != 1 || n[0].State != r.RunRefused || n[0].ErrorCode != "AUTH_SCOPE_DENIED" {
			t.Fatalf("%+v", n)
		}
		events, _ := w.store.Events(ctx, w.p.Identity, false, 10, "")
		if len(events) != 1 || events[0].Outcome != "refused" || events[0].Code != "AUTH_SCOPE_DENIED" {
			t.Fatalf("%+v", events)
		}
	})
	t.Run("no_viewer_role", func(t *testing.T) {
		w := newScheduleWorld(t, 0)
		id := start(t, w, "k")
		designer := w.p
		designer.Roles = []string{r.RoleDesigner}
		w.bind(&designer)
		w.tick(t)
		only(t, w, id, r.RunRefused, "AUTH_ROLE_DENIED")
	})
	t.Run("source_revoked", func(t *testing.T) {
		w := newScheduleWorld(t, 0)
		id := start(t, w, "k")
		w.source.Revoke(contractsim.DemoOwner(w.p, contractsim.FinanceIntegration))
		w.tick(t)
		only(t, w, id, r.RunRefused, "AUTH_SCOPE_DENIED")
	})
	t.Run("product_no_longer_bound", func(t *testing.T) {
		w := newScheduleWorld(t, 0)
		id := start(t, w, "k")
		moved := w.p
		moved.Integrations = []string{contractsim.ShipmentIntegration}
		w.bind(&moved)
		w.tick(t)
		only(t, w, id, r.RunRefused, "AUTH_SCOPE_DENIED")
	})
	t.Run("source_unavailable", func(t *testing.T) {
		w := newScheduleWorld(t, 0)
		id := start(t, w, "k")
		w.source.SetUnavailable(true)
		w.tick(t)
		if runs := w.runs(t, id); runs[0].State != r.RunDue {
			t.Fatalf("%+v", runs)
		}
		// Not looked at again before RunRetry; then tried again until RunMaxWait after the occurrence.
		w.clock.Advance(r.RunRecheck)
		if res := w.tick(t); res.Advanced != 0 {
			t.Fatalf("%+v", res)
		}
		w.clock.Advance(r.RunRetry)
		if res := w.tick(t); res.Advanced != 1 || w.runs(t, id)[0].State != r.RunDue {
			t.Fatalf("%+v", res)
		}
		w.clock.Advance(r.RunMaxWait)
		w.tick(t)
		only(t, w, id, r.RunFailed, "AUTHORITY_UNAVAILABLE")
	})
	t.Run("recovered_before_the_wait_ends", func(t *testing.T) {
		w := newScheduleWorld(t, 0)
		id := start(t, w, "k")
		w.source.SetUnavailable(true)
		w.tick(t)
		w.source.SetUnavailable(false)
		w.clock.Advance(r.RunRetry)
		w.tick(t)
		if runs := w.runs(t, id); runs[0].State != r.RunSnapshot {
			t.Fatalf("%+v", runs)
		}
	})
}

// A run that was due when its schedule was paused or deleted is cancelled, not run; a run whose job its owner cancelled ends cancelled; a run
// whose job never ends fails RUN_TIMEOUT.
func TestARunEndsWithoutItsJob(t *testing.T) {
	ctx := context.Background()
	for _, state := range []string{r.SchedulePaused, r.ScheduleDeleted} {
		w := newScheduleWorld(t, 0)
		v, _ := w.schedules.Create(ctx, w.p, dailyAt("08:00"), "k")
		w.clock.Advance(24 * time.Hour)
		if n, e := w.store.StartDueRuns(ctx, 10); n != 1 || e != nil {
			t.Fatal(n, e)
		}
		if _, e := w.schedules.SetState(ctx, w.p, v.ID, state); e != nil {
			t.Fatal(e)
		}
		w.tick(t)
		runs, e := w.store.ScheduleRuns(ctx, w.p.Identity, v.ID, r.ScheduleListing{Limit: 10})
		if e != nil || len(runs) != 1 || runs[0].State != r.RunCancelled || runs[0].ErrorCode != "SCHEDULE_"+strings.ToUpper(state) || runs[0].SnapshotJob != "" {
			t.Fatalf("%s: %+v %v", state, runs, e)
		}
	}
	w := newScheduleWorld(t, 0)
	v, _ := w.schedules.Create(ctx, w.p, dailyAt("08:00"), "k")
	w.clock.Advance(24 * time.Hour)
	w.tick(t)
	run := w.runs(t, v.ID)[0]
	if _, e := w.svc.Cancel(ctx, w.p, run.SnapshotJob); e != nil {
		t.Fatal(e)
	}
	w.clock.Advance(r.RunRecheck)
	w.tick(t)
	if run := w.runs(t, v.ID)[0]; run.State != r.RunCancelled || run.ErrorCode != r.JobCancelledCode {
		t.Fatalf("%+v", run)
	}
	w2 := newScheduleWorld(t, 0)
	v2, _ := w2.schedules.Create(ctx, w2.p, dailyAt("08:00"), "k")
	w2.clock.Advance(24 * time.Hour)
	w2.tick(t)
	w2.clock.Advance(r.RunMaxDuration)
	w2.tick(t)
	if run := w2.runs(t, v2.ID)[0]; run.State != r.RunFailed || run.ErrorCode != r.RunTimeoutCode {
		t.Fatalf("%+v", run)
	}
}

// A delivery the receiver cannot take now is tried again, waiting twice as long each time, at most MaxDeliveryAttempts times; one it refuses,
// or one that redirects, is not tried again.
func TestADeliveryIsRetriedWithBackoffAndThenGivenUp(t *testing.T) {
	ctx := context.Background()
	finish := func(t *testing.T, script ...string) (*scheduleWorld, string) {
		t.Helper()
		w := newScheduleWorld(t, 0)
		w.deliver.script = script
		v, _ := w.schedules.Create(ctx, w.p, dailyAt("08:00"), "k")
		w.clock.Advance(24 * time.Hour)
		w.bind(nil) // the run ends refused at once: it is the delivery that is under test
		w.tick(t)
		return w, v.ID
	}
	w, id := finish(t, r.DeliveryOutcomeServerError)
	waits := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for n, wait := range waits {
		if run := w.runs(t, id)[0]; run.Delivery != r.DeliveryPending || len(w.deliver.all()) != n+1 {
			t.Fatalf("attempt %d: %+v, %d attempts", n+1, run, len(w.deliver.all()))
		}
		w.clock.Advance(wait - time.Second)
		if res := w.tick(t); len(res.Deliveries) != 0 {
			t.Fatalf("attempt %d came %v early", n+2, time.Second)
		}
		w.clock.Advance(time.Second)
		res := w.tick(t)
		if len(res.Deliveries) != 1 || res.Deliveries[0].Attempt != n+2 || res.Deliveries[0].Outcome != r.DeliveryOutcomeServerError || res.Deliveries[0].Status != 503 {
			t.Fatalf("%+v", res.Deliveries)
		}
	}
	if run := w.runs(t, id)[0]; run.Delivery != r.DeliveryFailed || len(w.deliver.all()) != r.MaxDeliveryAttempts {
		t.Fatalf("%+v %d", run, len(w.deliver.all()))
	}
	w.clock.Advance(time.Hour)
	if res := w.tick(t); len(res.Deliveries) != 0 {
		t.Fatal("a failed delivery was tried again")
	}
	for _, outcome := range []string{r.DeliveryOutcomeRejected, r.DeliveryOutcomeRedirected} {
		w, id := finish(t, outcome)
		w.clock.Advance(time.Hour)
		w.tick(t)
		if run := w.runs(t, id)[0]; run.Delivery != r.DeliveryFailed || len(w.deliver.all()) != 1 {
			t.Fatalf("%s: %+v", outcome, run)
		}
	}
	// Taken on the second attempt: delivered, and the attempt number is in the notification.
	w, id = finish(t, r.DeliveryOutcomeServerError, r.DeliveryOutcomeDelivered)
	w.clock.Advance(30 * time.Second)
	w.tick(t)
	if run := w.runs(t, id)[0]; run.Delivery != r.DeliveryDelivered {
		t.Fatalf("%+v", run)
	}
	if n := w.deliver.all(); len(n) != 2 || n[0].Attempt != 1 || n[1].Attempt != 2 || n[0].RunID != n[1].RunID {
		t.Fatalf("%+v", n)
	}
}

// Without a deliverer a run ends with delivery none; a claim that lost its lease records nothing.
func TestRunsWithoutDeliveryAndLeases(t *testing.T) {
	ctx := context.Background()
	w := newScheduleWorld(t, 0)
	quiet, e := r.NewSchedules(w.svc, r.ScheduleOptions{Store: w.store, Resolve: func(r.Identity) (r.Principal, bool) { return r.Principal{}, false }})
	if e != nil || quiet.Delivers() || quiet.Limit() != r.DefaultSchedulesPerIdentity {
		t.Fatal(e)
	}
	v, _ := quiet.Create(ctx, w.p, dailyAt("08:00"), "k")
	w.clock.Advance(24 * time.Hour)
	if _, e := quiet.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if run := w.runs(t, v.ID)[0]; run.State != r.RunRefused || run.Delivery != r.DeliveryNone {
		t.Fatalf("%+v", run)
	}
	for _, o := range []r.ScheduleOptions{{Resolve: quietResolve}, {Store: w.store}, {Store: w.store, Resolve: quietResolve, Limit: -1}, {Store: w.store, Resolve: quietResolve, Limit: r.MaxSchedulesPerIdentity + 1}} {
		if _, e := r.NewSchedules(w.svc, o); !errors.Is(e, r.ErrInvalid) {
			t.Fatalf("%+v: %v", o, e)
		}
	}
	if _, e := r.NewSchedules(nil, r.ScheduleOptions{Store: w.store, Resolve: quietResolve}); !errors.Is(e, r.ErrInvalid) {
		t.Fatal(e)
	}
}

func quietResolve(r.Identity) (r.Principal, bool) { return r.Principal{}, false }
