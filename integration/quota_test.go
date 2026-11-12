package integration_test

import (
	"context"
	"errors"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"testing"
	"time"
)

// The quota contract of S04-D (docs/S04D_SCOPE.md D4), run on both stores: what is counted, by whose clock, what frees a slot, and what a
// refusal leaves. A world lets time pass for its store (the memory store's clock moves; the durable store's is the database's and cannot, so
// every stored expiry and every admission time moves into the past instead, which is the same thing).
type quotaWorld struct {
	t     *testing.T
	repo  r.Repository
	p     r.Principal
	svc   func(r.Quotas) *r.Service // a service over the store with these quotas
	grant func(r.Principal)         // lets another principal read the two integrations
	pass  func(time.Duration)
}

func newQuotaMemory(t *testing.T) *quotaWorld {
	t.Helper()
	clock := &retentionClock{t: time.Date(2026, 10, 29, 6, 0, 0, 123456789, time.UTC)}
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	finance, shipment := contractsim.Financial(), contractsim.Shipment()
	finance.SetClock(clock.Now)
	shipment.SetClock(clock.Now)
	grant := func(p r.Principal) {
		finance.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
		g := contractsim.DefaultGrant()
		g.CurrencyPolicy = nil
		shipment.Grant(contractsim.DemoOwner(p, contractsim.ShipmentIntegration), g)
	}
	grant(p)
	repo := memory.New(clock.Now)
	svc := func(q r.Quotas) *r.Service {
		s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}, {Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: shipment}},
			CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: &stampIssuer{}, Now: clock.Now, Quotas: q})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	return &quotaWorld{t: t, repo: repo, p: p, svc: svc, grant: grant, pass: clock.Advance}
}

func newQuotaDurable(t *testing.T) *quotaWorld {
	t.Helper()
	v := newDurable(t)
	admin := fixtureAdmin(t)
	svc := func(q r.Quotas) *r.Service {
		cs := []r.Connection{}
		for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
			cs = append(cs, r.Connection{Tenant: v.p.Tenant, Integration: id, SourceTenant: "tenant_a", Adapter: v.source})
		}
		s, e := r.NewService(r.Config{Repository: v.repo, Connections: cs, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: &stampIssuer{}, Quotas: q})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	grant := func(p r.Principal) {
		for i, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
			g := contractsim.DefaultGrant()
			if i == 1 {
				g.CurrencyPolicy = nil
			}
			if e := v.source.EnsureGrant(context.Background(), sourceContext(p, id), g); e != nil {
				t.Fatal(e)
			}
		}
	}
	pass := func(d time.Duration) {
		t.Helper()
		ageDurable(t, admin, d)
		if _, e := admin.Exec(context.Background(), `UPDATE reporting_s01.jobs SET created_at = created_at - $1::interval`, fmt.Sprintf("%d microseconds", d.Microseconds())); e != nil {
			t.Fatal(e)
		}
	}
	return &quotaWorld{t: t, repo: v.repo, p: v.p, svc: svc, grant: grant, pass: pass}
}

func (w *quotaWorld) other(subject, issuer string) r.Principal {
	p := w.p
	if subject != "" {
		p.Subject = subject
	}
	if issuer != "" {
		p.Issuer = issuer
	}
	w.grant(p)
	return p
}
func (w *quotaWorld) submit(s *r.Service, p r.Principal, key string) (r.JobView, error) {
	return s.SubmitSnapshot(context.Background(), p, contractsim.FinancialQuery(), key)
}
func (w *quotaWorld) mustSubmit(s *r.Service, p r.Principal, key string) r.JobView {
	w.t.Helper()
	j, e := w.submit(s, p, key)
	if e != nil {
		w.t.Fatalf("admitting %q: %v", key, e)
	}
	return j
}
func (w *quotaWorld) run(s *r.Service) {
	w.t.Helper()
	if ran, e := s.RunOne(context.Background()); e != nil || !ran {
		w.t.Fatal(ran, e)
	}
}

// refused: the error is the quota's, names it in its text and nothing else, and is not any other sentinel.
func refused(t *testing.T, e error, quota string) {
	t.Helper()
	if !errors.Is(e, r.ErrQuota) || r.QuotaOf(e) != quota || e.Error() != "quota exceeded: "+quota {
		t.Fatalf("want the %s quota, got %v", quota, e)
	}
	for _, other := range []error{r.ErrConflict, r.ErrInvalid, r.ErrBudget, r.ErrDenied} {
		if errors.Is(e, other) {
			t.Fatalf("a quota refusal is also %v", other)
		}
	}
}

// queued is how many jobs wait to be claimed, which the test then runs to the end: a refusal that had admitted something would show here.
func (w *quotaWorld) drain(s *r.Service) int {
	w.t.Helper()
	n := 0
	for {
		ran, e := s.RunOne(context.Background())
		if e != nil {
			w.t.Fatal(e)
		}
		if !ran {
			return n
		}
		n++
	}
}

func runQuotaContract(t *testing.T, build func(*testing.T) *quotaWorld) {
	ctx := context.Background()
	t.Run("jobs_in_flight_at_the_limit_and_one_over_and_what_frees_a_slot", func(t *testing.T) {
		w := build(t)
		s := w.svc(r.Quotas{JobsInFlight: 2})
		j1, j2 := w.mustSubmit(s, w.p, "k1"), w.mustSubmit(s, w.p, "k2")
		_, e := w.submit(s, w.p, "k3")
		refused(t, e, r.QuotaJobsInFlight)
		// A cancelled job frees its slot.
		if c, e := s.Cancel(ctx, w.p, j1.ID); e != nil || c.State != "cancelled" {
			t.Fatal(c, e)
		}
		w.mustSubmit(s, w.p, "k3")
		_, e = w.submit(s, w.p, "k4")
		refused(t, e, r.QuotaJobsInFlight)
		// A running job still holds its slot; a failed one frees it.
		claim, e := w.repo.Claim(ctx, time.Minute)
		if e != nil || claim == nil || claim.Job.ID != j2.ID {
			t.Fatal(claim, e)
		}
		_, e = w.submit(s, w.p, "k4")
		refused(t, e, r.QuotaJobsInFlight)
		if e = w.repo.Fail(ctx, *claim, "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		w.mustSubmit(s, w.p, "k4")
		_, e = w.submit(s, w.p, "k5")
		refused(t, e, r.QuotaJobsInFlight)
		// A job that succeeded frees its slot.
		w.run(s)
		w.mustSubmit(s, w.p, "k5")
		_, e = w.submit(s, w.p, "k6")
		refused(t, e, r.QuotaJobsInFlight)
		// Nothing was admitted by any refusal: what waits is exactly what was admitted and not finished (k4 and k5).
		if n := w.drain(s); n != 2 {
			t.Fatalf("%d jobs were left to run, want the 2 admitted", n)
		}
	})
	t.Run("every_kind_of_job_counts_and_is_refused", func(t *testing.T) {
		w := build(t)
		s := w.svc(r.Quotas{JobsInFlight: 2})
		snap := w.mustSubmit(s, w.p, "snap")
		w.run(s)
		snap, e := s.Job(ctx, w.p, snap.ID)
		if e != nil || snap.State != "succeeded" {
			t.Fatal(snap, e)
		}
		if _, e = s.SubmitExport(ctx, w.p, snap.SnapshotID, "export-1"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.SubmitIssue(ctx, w.p, issueInput(snap.SnapshotID, 0), "issue-1"); e != nil {
			t.Fatal(e)
		}
		// The two queued jobs are the two slots; a job of any kind over them is refused.
		_, e = w.submit(s, w.p, "snap-2")
		refused(t, e, r.QuotaJobsInFlight)
		_, e = s.SubmitExport(ctx, w.p, snap.SnapshotID, "export-2")
		refused(t, e, r.QuotaJobsInFlight)
		_, e = s.SubmitIssue(ctx, w.p, issueInput(snap.SnapshotID, 1), "issue-2")
		refused(t, e, r.QuotaJobsInFlight)
		// The same identity's other integration shares the quota.
		_, e = s.SubmitSnapshot(ctx, w.p, contractsim.ShipmentQuery(), "shipment")
		refused(t, e, r.QuotaJobsInFlight)
	})
	t.Run("stored_results_at_the_limit_and_one_over_and_expiry_by_the_clock", func(t *testing.T) {
		w := build(t)
		s := w.svc(r.Quotas{StoredResults: 2})
		first := w.mustSubmit(s, w.p, "k1")
		w.run(s) // a snapshot
		first, e := s.Job(ctx, w.p, first.ID)
		if e != nil || first.State != "succeeded" {
			t.Fatal(first, e)
		}
		ex, e := s.SubmitExport(ctx, w.p, first.SnapshotID, "export")
		if e != nil {
			t.Fatal(e)
		}
		w.run(s) // an artifact
		if ex, e = s.Job(ctx, w.p, ex.ID); e != nil || ex.State != "succeeded" {
			t.Fatal(ex, e)
		}
		// A snapshot and an artifact are two stored results: the limit. One more job that would make one is refused...
		_, e = w.submit(s, w.p, "k2")
		refused(t, e, r.QuotaStoredResults)
		_, e = s.SubmitExport(ctx, w.p, first.SnapshotID, "export-2")
		refused(t, e, r.QuotaStoredResults)
		// ...and an issue is not, since S25-B1: the file it makes is not a stored result (ADR 0026 decision 7), so this quota has nothing
		// to refuse it for. (Until then this line was a refusal: the issued file counted.) It is cancelled, so that the snapshot it names
		// is the sweep's to take below.
		issue, e := s.SubmitIssue(ctx, w.p, issueInput(first.SnapshotID, 0), "issue")
		if e != nil {
			t.Fatal("an issue was refused at the limit of the stored results:", e)
		}
		if c, e := s.Cancel(ctx, w.p, issue.ID); e != nil || c.State != "cancelled" {
			t.Fatal(c, e)
		}
		// ...and the replay of a key whose job exists is not either.
		if again, e := w.submit(s, w.p, "k1"); e != nil || again.ID != first.ID {
			t.Fatal(again, e)
		}
		if again, e := s.SubmitExport(ctx, w.p, first.SnapshotID, "export"); e != nil || again.ID != ex.ID {
			t.Fatal(again, e)
		}
		// Results that have expired by the store's clock no longer count, swept or not.
		w.pass(readLife + time.Minute)
		w.mustSubmit(s, w.p, "k2")
		w.mustSubmit(s, w.p, "k3")
		// The sweep deleting what expired changes nothing about the count.
		if res, e := s.Sweep(ctx, r.SweepOptions{Grace: time.Second}); e != nil || res.Snapshots != 1 || res.Artifacts != 1 {
			t.Fatal(res, e)
		}
		w.mustSubmit(s, w.p, "k4")
	})
	// Until S25-B1 the file of an issued document was a stored result, and this subtest held the limit to "the snapshot, the issued file and
	// one more". ADR 0026 decision 7 changed the rule: the file is kept for years where the source vouches for the scope, so neither it nor
	// its record is a stored result, and the admissions per hour bound how many an identity issues.
	t.Run("issued_files_and_issue_records_never_count_and_expired_results_leave_them_alone", func(t *testing.T) {
		w := build(t)
		s := w.svc(r.Quotas{StoredResults: 3})
		snap := w.mustSubmit(s, w.p, "snap")
		w.run(s)
		snap, e := s.Job(ctx, w.p, snap.ID)
		if e != nil || snap.State != "succeeded" {
			t.Fatal(snap, e)
		}
		// Two documents are issued from the snapshot: two files and two records.
		var issue r.JobView
		for row, key := range []string{"issue", "issue-2"} {
			if issue, e = s.SubmitIssue(ctx, w.p, issueInput(snap.SnapshotID, row), key); e != nil {
				t.Fatal(e)
			}
			w.run(s)
			if issue, e = s.Job(ctx, w.p, issue.ID); e != nil || issue.State != "succeeded" {
				t.Fatal(issue, e)
			}
			file, e := w.repo.Artifact(ctx, w.p.Identity, issue.ArtifactID)
			if e != nil || file.Issue != issue.ID || !file.ExpiresAt.After(time.Time{}) {
				t.Fatalf("the issued file is not stored under its issue: %+v %v", file, e)
			}
			if _, e = w.repo.Issue(ctx, w.p.Identity, issue.ID); e != nil {
				t.Fatal("the issue record is not stored", e)
			}
		}
		// One result is held, the snapshot. Were the issued files counted, three would be held and the export would be refused; were
		// the records counted too, five.
		ex, e := s.SubmitExport(ctx, w.p, snap.SnapshotID, "export")
		if e != nil {
			t.Fatal("an issued file or its record holds a slot of the stored results:", e)
		}
		w.run(s)
		if ex, e = s.Job(ctx, w.p, ex.ID); e != nil || ex.State != "succeeded" {
			t.Fatal(ex, e)
		}
		// An export file is a stored result: two are held, and a second snapshot makes three.
		next := w.mustSubmit(s, w.p, "snap-2")
		w.run(s)
		// The limit is exact: a freeze and an export are refused at it. An issue is admitted and runs, because it adds nothing to what
		// the quota counts; the jobs in flight and the admissions per hour are what bound it.
		_, e = w.submit(s, w.p, "snap-3")
		refused(t, e, r.QuotaStoredResults)
		_, e = s.SubmitExport(ctx, w.p, snap.SnapshotID, "export-2")
		refused(t, e, r.QuotaStoredResults)
		third, e := s.SubmitIssue(ctx, w.p, issueInput(snap.SnapshotID, 0), "issue-3")
		if e != nil {
			t.Fatal("an issue was refused at the limit of the stored results:", e)
		}
		w.run(s)
		if third, e = s.Job(ctx, w.p, third.ID); e != nil || third.State != "succeeded" {
			t.Fatal(third, e)
		}
		// And the limit still holds after it.
		_, e = w.submit(s, w.p, "snap-3")
		refused(t, e, r.QuotaStoredResults)
		if next.ID == "" {
			t.Fatal(next)
		}
		// Everything expires; the records are permanent and the count is zero.
		w.pass(readLife + time.Minute)
		if _, e = w.repo.Issue(ctx, w.p.Identity, issue.ID); e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 3; i++ {
			j := w.mustSubmit(s, w.p, "after-"+string(rune('a'+i)))
			w.run(s)
			if j, e = s.Job(ctx, w.p, j.ID); e != nil || j.State != "succeeded" {
				t.Fatal(j, e)
			}
		}
		_, e = w.submit(s, w.p, "after-d")
		refused(t, e, r.QuotaStoredResults)
	})
	t.Run("admissions_per_hour_by_the_clock_and_finished_jobs_still_count", func(t *testing.T) {
		w := build(t)
		s := w.svc(r.Quotas{AdmissionsPerHour: 3})
		a, b := w.mustSubmit(s, w.p, "a"), w.mustSubmit(s, w.p, "b")
		w.mustSubmit(s, w.p, "c")
		// A cancelled job and a finished one were admitted within the hour all the same.
		if _, e := s.Cancel(ctx, w.p, b.ID); e != nil {
			t.Fatal(e)
		}
		w.run(s)
		_, e := w.submit(s, w.p, "d")
		refused(t, e, r.QuotaAdmissionsPerHour)
		// The replay of a key is not refused.
		if again, e := w.submit(s, w.p, "a"); e != nil || again.ID != a.ID {
			t.Fatal(again, e)
		}
		// 59 minutes later the three still count; 61 minutes later they do not.
		w.pass(59 * time.Minute)
		_, e = w.submit(s, w.p, "d")
		refused(t, e, r.QuotaAdmissionsPerHour)
		w.pass(2 * time.Minute)
		w.mustSubmit(s, w.p, "d")
		w.mustSubmit(s, w.p, "e")
		w.mustSubmit(s, w.p, "f")
		_, e = w.submit(s, w.p, "g")
		refused(t, e, r.QuotaAdmissionsPerHour)
	})
	t.Run("a_replay_at_the_limit_is_the_job_and_a_changed_request_is_a_conflict", func(t *testing.T) {
		w := build(t)
		for _, limits := range []r.Quotas{{JobsInFlight: 1}, {AdmissionsPerHour: 1}} {
			s := w.svc(limits)
			key := "replay-" + string(rune('a'+limits.JobsInFlight)) + string(rune('a'+limits.AdmissionsPerHour))
			j := w.mustSubmit(s, w.p, key)
			if again, e := w.submit(s, w.p, key); e != nil || again.ID != j.ID || again.State != "queued" {
				t.Fatalf("%+v: %v", again, e)
			}
			changed := contractsim.FinancialQuery()
			changed.AsOf = "2026-10-25"
			if _, e := s.SubmitSnapshot(ctx, w.p, changed, key); !errors.Is(e, r.ErrConflict) || errors.Is(e, r.ErrQuota) {
				t.Fatalf("a changed request under a used key at the limit: %v", e)
			}
			_, e := w.submit(s, w.p, key+"-new")
			if !errors.Is(e, r.ErrQuota) {
				t.Fatal(e)
			}
			w.drain(s)
			w.pass(2 * time.Hour)
		}
	})
	t.Run("another_identity_is_not_affected", func(t *testing.T) {
		w := build(t)
		s := w.svc(r.Quotas{JobsInFlight: 1, AdmissionsPerHour: 1})
		w.mustSubmit(s, w.p, "mine")
		_, e := w.submit(s, w.p, "mine-2")
		refused(t, e, r.QuotaJobsInFlight)
		// Same tenant and issuer, another subject; same tenant and subject, another issuer.
		w.mustSubmit(s, w.other("someone-else", ""), "theirs")
		w.mustSubmit(s, w.other("", "urn:other-issuer"), "theirs")
		// Each identity is refused by its own count only.
		_, e = w.submit(s, w.other("someone-else", ""), "theirs-2")
		refused(t, e, r.QuotaJobsInFlight)
		// The results an identity holds are its own too: with one result held by me, the others are not at the stored-results limit.
		w2 := build(t)
		results := w2.svc(r.Quotas{StoredResults: 1})
		w2.mustSubmit(results, w2.p, "mine")
		w2.run(results)
		_, e = w2.submit(results, w2.p, "mine-2")
		refused(t, e, r.QuotaStoredResults)
		w2.mustSubmit(results, w2.other("someone-else", ""), "theirs")
		w2.mustSubmit(results, w2.other("", "urn:other-issuer"), "theirs")
	})
	t.Run("a_refusal_admits_nothing", func(t *testing.T) {
		w := build(t)
		s := w.svc(r.Quotas{JobsInFlight: 1})
		w.mustSubmit(s, w.p, "one")
		for i := 0; i < 3; i++ {
			_, e := w.submit(s, w.p, "refused-"+string(rune('a'+i)))
			refused(t, e, r.QuotaJobsInFlight)
		}
		if n := w.drain(s); n != 1 {
			t.Fatalf("%d jobs ran, want the one admitted", n)
		}
		// After it finished, the slot is free and a refused key is a new key.
		w.mustSubmit(s, w.p, "refused-a")
	})
	t.Run("the_store_applies_the_quotas_it_is_given_defaults_and_bounds", func(t *testing.T) {
		w := build(t)
		s := w.svc(r.Quotas{})
		if got := s.Quotas(); got != (r.Quotas{JobsInFlight: 8, StoredResults: 512, AdmissionsPerHour: 120, QueriesInFlight: 4}) {
			t.Fatalf("%+v", got)
		}
		base := w.mustSubmit(s, w.p, "base")
		stored, e := w.repo.Job(ctx, w.p.Identity, base.ID)
		if e != nil {
			t.Fatal(e)
		}
		copyOf := func(key string) r.Job { j := stored; j.ID = r.NewID(); j.Key = key; return j }
		for _, bad := range []r.Quotas{{JobsInFlight: -1}, {JobsInFlight: 1001}, {StoredResults: 100001}, {StoredResults: -5}, {AdmissionsPerHour: 100001}, {AdmissionsPerHour: -1}} {
			if _, e := w.repo.Admit(ctx, copyOf("bad"), bad); !errors.Is(e, r.ErrInvalid) {
				t.Fatalf("%+v: %v", bad, e)
			}
		}
		// Zero is the default: 8 jobs in flight.
		for i := 0; i < 7; i++ {
			if _, e := w.repo.Admit(ctx, copyOf("c"+string(rune('a'+i))), r.Quotas{}); e != nil {
				t.Fatal(i, e)
			}
		}
		_, e = w.repo.Admit(ctx, copyOf("c-over"), r.Quotas{})
		refused(t, e, r.QuotaJobsInFlight)
		// The same store with larger limits admits it: the limits are the caller's, not the store's memory of an earlier call.
		if _, e = w.repo.Admit(ctx, copyOf("c-over"), r.Quotas{JobsInFlight: 9}); e != nil {
			t.Fatal(e)
		}
	})
}

func TestMemoryQuotaContract(t *testing.T) { runQuotaContract(t, newQuotaMemory) }

// The durable counterpart runs inside TestDurablePostgresContract (it needs the platform and source databases).
func durableQuotaContract(t *testing.T) { runQuotaContract(t, newQuotaDurable) }
