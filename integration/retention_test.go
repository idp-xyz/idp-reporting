package integration_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The retention contract of S04-B (docs/S04B_SCOPE.md B6), run on both stores: what a sweep deletes, what it keeps, and the views of what is
// left. A store is driven by a harness that lets time pass for it: the memory store's clock moves; the durable store's clock is the
// database's and cannot, so the harness moves every stored expiry into the past by the same amount, which is the same thing for a store that
// keeps expiries (the column, the payload and the digest of each row are rewritten together).
type retention struct {
	t       *testing.T
	repo    r.Repository
	s       *r.Service
	p       r.Principal
	issuer  *stampIssuer
	age     func(time.Duration)
	service func(now func() time.Time) *r.Service // another service over the same store, whose clock is its own
}

// readLife is how long the simulators' source reads live (ten minutes): the expiry of every snapshot, artifact and job of a test.
const readLife = 10 * time.Minute

func (h *retention) freeze(key string) r.JobView {
	h.t.Helper()
	ctx := context.Background()
	j, e := h.s.SubmitSnapshot(ctx, h.p, contractsim.FinancialQuery(), key)
	if e != nil {
		h.t.Fatal(e)
	}
	h.run()
	if j, e = h.s.Job(ctx, h.p, j.ID); e != nil || j.State != "succeeded" || j.SnapshotID == "" {
		h.t.Fatal(j, e)
	}
	return j
}
func (h *retention) run() {
	h.t.Helper()
	if _, e := h.s.RunOne(context.Background()); e != nil {
		h.t.Fatal(e)
	}
}
func (h *retention) export(snapshot, key string) r.JobView {
	h.t.Helper()
	ctx := context.Background()
	j, e := h.s.SubmitExport(ctx, h.p, snapshot, key)
	if e != nil {
		h.t.Fatal(e)
	}
	h.run()
	if j, e = h.s.Job(ctx, h.p, j.ID); e != nil || j.State != "succeeded" || j.ArtifactID == "" {
		h.t.Fatal(j, e)
	}
	return j
}

// issue issues row 0 of the snapshot and returns the job's view (succeeded, with the product's number).
func (h *retention) issue(snapshot, key string) r.JobView {
	h.t.Helper()
	ctx := context.Background()
	j, e := h.s.SubmitIssue(ctx, h.p, issueInput(snapshot, 0), key)
	if e != nil {
		h.t.Fatal(e)
	}
	h.run()
	if j, e = h.s.Job(ctx, h.p, j.ID); e != nil || j.State != "succeeded" || j.ArtifactID == "" || j.Issue == nil || j.Issue.Number == "" {
		h.t.Fatal(j, e)
	}
	return j
}

// held says whether the store still holds the snapshot, artifact or issue record with the id.
func (h *retention) held(kind, id string) bool {
	h.t.Helper()
	ctx := context.Background()
	var e error
	switch kind {
	case "snapshot":
		_, e = h.repo.Snapshot(ctx, h.p.Identity, id)
	case "artifact":
		_, e = h.repo.Artifact(ctx, h.p.Identity, id)
	case "issue":
		_, e = h.repo.Issue(ctx, h.p.Identity, id)
	}
	if e != nil && !errors.Is(e, r.ErrNotFound) {
		h.t.Fatalf("reading %s: %v", kind, e)
	}
	return e == nil
}
func (h *retention) sweep(o r.SweepOptions) r.SweepResult {
	h.t.Helper()
	res, e := h.s.Sweep(context.Background(), o)
	if e != nil {
		h.t.Fatal(e)
	}
	return res
}
func (h *retention) job(id string) r.Job {
	h.t.Helper()
	j, e := h.repo.Job(context.Background(), h.p.Identity, id)
	if e != nil {
		h.t.Fatal(e)
	}
	return j
}

func counts(artifacts, snapshots, jobs int) r.SweepResult {
	return r.SweepResult{Artifacts: artifacts, Snapshots: snapshots, Jobs: jobs}
}

// sameCounts compares what a sweep removed, not how many transactions it took.
func sameCounts(got, want r.SweepResult) bool {
	return got.Artifacts == want.Artifacts && got.Snapshots == want.Snapshots && got.Jobs == want.Jobs
}

// retentionClock is the one clock of a memory harness: the store, the service and the simulators follow it.
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

func newRetentionMemory(t *testing.T) *retention {
	t.Helper()
	clock := &retentionClock{t: time.Date(2026, 10, 29, 6, 0, 0, 123456789, time.UTC)}
	p := contractsim.DemoPrincipal()
	finance := contractsim.Financial()
	finance.SetClock(clock.Now)
	finance.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(clock.Now)
	issuer := &stampIssuer{}
	service := func(now func() time.Time) *r.Service {
		s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}}, CursorKey: []byte("01234567890123456789012345678901"),
			Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: issuer, Now: now})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	return &retention{t: t, repo: repo, s: service(clock.Now), p: p, issuer: issuer, age: clock.Advance, service: service}
}

func newRetentionDurable(t *testing.T) *retention {
	t.Helper()
	v := newDurable(t)
	issuer := &stampIssuer{}
	connections := func(now func() time.Time) *r.Service {
		cs := []r.Connection{}
		for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
			cs = append(cs, r.Connection{Tenant: v.p.Tenant, Integration: id, SourceTenant: "tenant_a", Adapter: v.source})
		}
		s, e := r.NewService(r.Config{Repository: v.repo, Connections: cs, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: issuer, Now: now})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	admin := fixtureAdmin(t)
	age := func(d time.Duration) { ageDurable(t, admin, d) }
	return &retention{t: t, repo: v.repo, s: connections(nil), p: v.p, issuer: issuer, age: age, service: connections}
}

func TestMemoryRetentionContract(t *testing.T) { runRetentionContract(t, newRetentionMemory) }

func runRetentionContract(t *testing.T, newHarness func(*testing.T) *retention) {
	ctx := context.Background()
	grace := time.Hour
	t.Run("every_kind_at_before_and_after_its_expiry_plus_grace", func(t *testing.T) {
		h := newHarness(t)
		snap := h.freeze("snap")
		export := h.export(snap.SnapshotID, "export")
		issue := h.issue(snap.SnapshotID, "issue")
		// A job that never produced anything (cancelled while queued) is finished and expires with the others: it holds no snapshot, so
		// only its own expiry plus the grace keeps its payload.
		cancelled, e := h.s.SubmitSnapshot(ctx, h.p, contractsim.FinancialQuery(), "cancelled")
		if e != nil {
			t.Fatal(e)
		}
		if v, e := h.s.Cancel(ctx, h.p, cancelled.ID); e != nil || v.State != "cancelled" {
			t.Fatal(v, e)
		}
		held := func(want bool) {
			t.Helper()
			for _, a := range []struct{ what, kind, id string }{{"snapshot", "snapshot", snap.SnapshotID}, {"workbook", "artifact", export.ArtifactID}, {"issued PDF", "artifact", issue.ArtifactID}} {
				if h.held(a.kind, a.id) != want {
					t.Fatalf("the %s is held: %v, want %v", a.what, !want, want)
				}
			}
			if !h.held("issue", issue.ID) {
				t.Fatal("the issue record is gone")
			}
		}
		// Valid: nothing goes. Expired but inside the grace (one minute short of expiry plus grace): nothing goes.
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatal("a sweep of valid assets", res)
		}
		h.age(readLife + grace - time.Minute)
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatal("a sweep inside the grace period", res)
		}
		held(true)
		// A longer grace holds what a shorter one would release.
		h.age(2 * time.Minute)
		if res := h.sweep(r.SweepOptions{Grace: 2 * grace}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatal("a sweep with a longer grace", res)
		}
		held(true)
		// Past expiry plus grace: the two artifacts, the snapshot, and the payloads of the four jobs; the record stays.
		res := h.sweep(r.SweepOptions{Grace: grace})
		if !sameCounts(res, counts(2, 1, 4)) {
			t.Fatal("the sweep past the grace", res)
		}
		held(false)
		for _, id := range []string{snap.ID, export.ID, issue.ID, cancelled.ID} {
			if j := h.job(id); !j.Reduced || (j.State != "succeeded") != (id == cancelled.ID) {
				t.Fatalf("job %s after the sweep: %+v", id, j)
			}
		}
		if v, e := h.s.Job(ctx, h.p, cancelled.ID); e != nil || v.State != "cancelled" || v.Expired || v.ErrorCode != "" {
			t.Fatal("a cancelled job's view after the sweep", v, e)
		}
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatal("a second sweep is not idempotent", res)
		}
	})
	t.Run("the_stores_clock_decides_whatever_the_hosts_says", func(t *testing.T) {
		h := newHarness(t)
		snap := h.freeze("snap")
		export := h.export(snap.SnapshotID, "export")
		// A service whose clock is a century ahead sweeps assets that are valid by the store's clock: nothing goes.
		far := func() time.Time { return time.Now().Add(100 * 365 * 24 * time.Hour) }
		if res, e := h.service(far).Sweep(ctx, r.SweepOptions{Grace: time.Second}); e != nil || !sameCounts(res, counts(0, 0, 0)) {
			t.Fatal("a host clock a century ahead made valid assets sweepable", res, e)
		}
		if !h.held("snapshot", snap.SnapshotID) || !h.held("artifact", export.ArtifactID) {
			t.Fatal("valid assets were deleted")
		}
		// And a clock a century behind does not keep what the store's clock says is over.
		h.age(readLife + 2*time.Second)
		past := func() time.Time { return time.Now().Add(-100 * 365 * 24 * time.Hour) }
		if res, e := h.service(past).Sweep(ctx, r.SweepOptions{Grace: time.Second}); e != nil || !sameCounts(res, counts(1, 1, 2)) {
			t.Fatal("a host clock a century behind kept expired assets", res, e)
		}
	})
	t.Run("an_issue_record_is_never_deleted", func(t *testing.T) {
		h := newHarness(t)
		snap := h.freeze("snap")
		issue := h.issue(snap.SnapshotID, "issue")
		h.age(10 * 365 * 24 * time.Hour)
		for range 2 {
			if res := h.sweep(r.SweepOptions{Grace: time.Second, Batch: 1}); res.Artifacts > 1 || res.Snapshots > 1 {
				t.Fatal(res)
			}
		}
		if h.held("artifact", issue.ArtifactID) || h.held("snapshot", snap.SnapshotID) {
			t.Fatal("the issued PDF and its snapshot are past their grace and must be gone")
		}
		rec, e := h.repo.Issue(ctx, h.p.Identity, issue.ID)
		if e != nil || rec.Number != issue.Issue.Number || rec.ArtifactID != issue.ArtifactID || rec.SHA256 == "" || rec.Pages != 3 || rec.ConfirmationRef == "" {
			t.Fatalf("the record after ten years of sweeps: %+v %v", rec, e)
		}
		got, e := h.s.Issue(ctx, h.p, issue.ID)
		if e != nil || !got.Expired || got.Number != rec.Number || got.ArtifactID != rec.ArtifactID || got.SHA256 != rec.SHA256 || got.State != "succeeded" {
			t.Fatalf("the issue view after the sweep: %+v %v", got, e)
		}
		if res := h.sweep(r.SweepOptions{Grace: time.Second, DryRun: true}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatal("a dry run still finds something to delete", res)
		}
	})
	t.Run("what_a_queued_or_running_job_names_is_never_deleted", func(t *testing.T) {
		h := newHarness(t)
		snap := h.freeze("snap")
		first := h.export(snap.SnapshotID, "first")
		// Four jobs wait while the read runs out and the grace passes: a running snapshot job and a running export of the snapshot, a queued
		// export of the snapshot and a queued snapshot job. The two snapshot jobs name nothing; the exports name the snapshot.
		claimOldest := func(want string) {
			t.Helper()
			if c, e := h.repo.Claim(ctx, 5*time.Minute); e != nil || c == nil || c.Job.ID != want {
				t.Fatal(c, e)
			}
		}
		runningSnapshot, e := h.s.SubmitSnapshot(ctx, h.p, contractsim.FinancialQuery(), "running-snapshot")
		if e != nil {
			t.Fatal(e)
		}
		claimOldest(runningSnapshot.ID)
		runningExport, e := h.s.SubmitExport(ctx, h.p, snap.SnapshotID, "running-export")
		if e != nil {
			t.Fatal(e)
		}
		claimOldest(runningExport.ID)
		queuedExport, e := h.s.SubmitExport(ctx, h.p, snap.SnapshotID, "queued-export")
		if e != nil {
			t.Fatal(e)
		}
		queuedSnapshot, e := h.s.SubmitSnapshot(ctx, h.p, contractsim.FinancialQuery(), "queued-snapshot")
		if e != nil {
			t.Fatal(e)
		}
		jobs := []string{runningSnapshot.ID, runningExport.ID, queuedExport.ID, queuedSnapshot.ID}
		h.age(readLife + 3*time.Hour)
		// Everything is past its grace and nothing goes: not the snapshot or the artifact of its first export, not the payload of a job
		// that is queued or running (those of the snapshot jobs name nothing and would otherwise be free to go), not the finished jobs
		// whose snapshot is still stored.
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatalf("a sweep with queued and running jobs did %+v", res)
		}
		for _, a := range []struct{ kind, id string }{{"snapshot", snap.SnapshotID}, {"artifact", first.ArtifactID}} {
			if !h.held(a.kind, a.id) {
				t.Fatalf("the %s was deleted although a queued or running job names its snapshot", a.kind)
			}
		}
		if res := h.sweep(r.SweepOptions{Grace: grace, DryRun: true}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatalf("the dry run says %+v", res)
		}
		for _, id := range jobs {
			if j := h.job(id); j.Reduced || (j.State != "queued" && j.State != "running") {
				t.Fatalf("job %s: %+v", id, j)
			}
		}
		cancel := func(id string) {
			t.Helper()
			if v, e := h.s.Cancel(ctx, h.p, id); e != nil || v.State != "cancelled" {
				t.Fatal(v, e)
			}
		}
		// The two snapshot jobs end: they name nothing and are past their grace, so their payloads go at once.
		cancel(runningSnapshot.ID)
		cancel(queuedSnapshot.ID)
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 2)) || !h.held("snapshot", snap.SnapshotID) {
			t.Fatalf("with the snapshot jobs ended: %+v", res)
		}
		// The queued export ends, the running one still names the snapshot.
		cancel(queuedExport.ID)
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 0)) || !h.held("snapshot", snap.SnapshotID) || !h.held("artifact", first.ArtifactID) {
			t.Fatalf("with one export still running: %+v", res)
		}
		// Nothing names the snapshot now: it goes with the artifact of its first export, and the four jobs of the snapshot are reduced.
		cancel(runningExport.ID)
		dry := h.sweep(r.SweepOptions{Grace: grace, DryRun: true})
		res := h.sweep(r.SweepOptions{Grace: grace})
		if !sameCounts(res, counts(1, 1, 4)) || !sameCounts(dry, res) || h.held("snapshot", snap.SnapshotID) || h.held("artifact", first.ArtifactID) {
			t.Fatalf("the sweep after the jobs ended did %+v (dry run %+v)", res, dry)
		}
	})
	t.Run("a_dry_run_changes_nothing_and_counts_what_the_sweep_does_in_batches", func(t *testing.T) {
		h := newHarness(t)
		var snaps, arts []string
		for i := range 7 {
			s := h.freeze(fmt.Sprint("snap", i))
			snaps = append(snaps, s.SnapshotID)
			arts = append(arts, h.export(s.SnapshotID, fmt.Sprint("export", i)).ArtifactID)
		}
		// Valid, and inside the grace, a dry run counts nothing.
		if res := h.sweep(r.SweepOptions{Grace: grace, DryRun: true}); !res.DryRun || !sameCounts(res, counts(0, 0, 0)) || res.Batches != 0 {
			t.Fatal(res)
		}
		h.age(readLife + grace + time.Minute)
		dry := h.sweep(r.SweepOptions{Grace: grace, Batch: 3, DryRun: true})
		if !dry.DryRun || !sameCounts(dry, counts(7, 7, 14)) || dry.Batches != 0 {
			t.Fatal("the dry run counts", dry)
		}
		for i := range snaps {
			if !h.held("snapshot", snaps[i]) || !h.held("artifact", arts[i]) {
				t.Fatal("a dry run deleted something")
			}
		}
		real := h.sweep(r.SweepOptions{Grace: grace, Batch: 3})
		if real.DryRun || !sameCounts(real, counts(7, 7, 14)) || !sameCounts(real, dry) {
			t.Fatalf("the sweep did %+v, the dry run said %+v", real, dry)
		}
		// 7 artifacts in batches of 3 (3, 3, 1), 7 snapshots (3, 3, 1), 14 job payloads (3, 3, 3, 3, 2): bounded batches, 11 of them.
		if real.Batches != 11 {
			t.Fatalf("%d batches, want 11", real.Batches)
		}
		for i := range snaps {
			if h.held("snapshot", snaps[i]) || h.held("artifact", arts[i]) {
				t.Fatal("something that expired is still held")
			}
		}
		if again := h.sweep(r.SweepOptions{Grace: grace, Batch: 3}); !sameCounts(again, counts(0, 0, 0)) || again.Batches != 0 {
			t.Fatal("a second sweep is not idempotent", again)
		}
		if again := h.sweep(r.SweepOptions{Grace: grace, DryRun: true}); !sameCounts(again, counts(0, 0, 0)) {
			t.Fatal("a second dry run", again)
		}
	})
	t.Run("concurrent_sweeps_delete_every_row_once", func(t *testing.T) {
		h := newHarness(t)
		for i := range 12 {
			s := h.freeze(fmt.Sprint("snap", i))
			h.export(s.SnapshotID, fmt.Sprint("export", i))
		}
		h.age(readLife + grace + time.Minute)
		const sweepers = 4
		results := make([]r.SweepResult, sweepers)
		errs := make([]error, sweepers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range sweepers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results[i], errs[i] = h.s.Sweep(ctx, r.SweepOptions{Grace: grace, Batch: 2})
			}()
		}
		close(start)
		wg.Wait()
		var total r.SweepResult
		for i, e := range errs {
			if e != nil {
				t.Fatalf("sweeper %d: %v", i, e)
			}
			total.Artifacts += results[i].Artifacts
			total.Snapshots += results[i].Snapshots
			total.Jobs += results[i].Jobs
		}
		// Whoever took a row deleted it: together they did exactly what one sweep does, and nothing is left.
		if !sameCounts(total, counts(12, 12, 24)) {
			t.Fatalf("the sweeps together did %+v (%+v)", total, results)
		}
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatal("something was left for another sweep", res)
		}
	})
	t.Run("a_worker_publishing_during_sweeps_loses_nothing", func(t *testing.T) {
		h := newHarness(t)
		for i := range 10 {
			s := h.freeze(fmt.Sprint("old", i))
			h.export(s.SnapshotID, fmt.Sprint("old-export", i))
		}
		h.age(readLife + grace + time.Minute)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		var sweepErr error
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					if _, e := h.s.Sweep(ctx, r.SweepOptions{Grace: grace, Batch: 2}); e != nil {
						sweepErr = e
						return
					}
				}
			}()
		}
		// Meanwhile the worker freezes, exports and issues, each from a read that is valid now.
		type made struct{ snapshot, artifact, issuedPDF, record string }
		var fresh []made
		for i := range 8 {
			s := h.freeze(fmt.Sprint("new", i))
			e := h.export(s.SnapshotID, fmt.Sprint("new-export", i))
			is := h.issue(s.SnapshotID, fmt.Sprint("new-issue", i))
			fresh = append(fresh, made{s.SnapshotID, e.ArtifactID, is.ArtifactID, is.ID})
		}
		close(stop)
		wg.Wait()
		if sweepErr != nil {
			t.Fatal(sweepErr)
		}
		for _, m := range fresh {
			if !h.held("snapshot", m.snapshot) || !h.held("artifact", m.artifact) || !h.held("artifact", m.issuedPDF) || !h.held("issue", m.record) {
				t.Fatalf("a sweep deleted what a worker published meanwhile: %+v", m)
			}
		}
		// And the old ones are gone, those swept by the loops and those a last sweep takes.
		h.sweep(r.SweepOptions{Grace: grace})
		if res := h.sweep(r.SweepOptions{Grace: grace, DryRun: true}); !sameCounts(res, counts(0, 0, 0)) {
			t.Fatal(res)
		}
		for _, m := range fresh {
			if _, e := h.s.Rows(ctx, h.p, m.snapshot, 10, ""); e != nil {
				t.Fatal("a fresh snapshot cannot be read:", e)
			}
			if _, e := h.s.Download(ctx, h.p, m.issuedPDF); e != nil {
				t.Fatal("a fresh issued PDF cannot be downloaded:", e)
			}
		}
	})
	t.Run("views_after_cleanup_say_so_and_never_fail", func(t *testing.T) {
		h := newHarness(t)
		snap := h.freeze("snap")
		export := h.export(snap.SnapshotID, "export")
		issue := h.issue(snap.SnapshotID, "issue")
		// Valid: no view says expired.
		for _, id := range []string{snap.ID, export.ID, issue.ID} {
			if v, e := h.s.Job(ctx, h.p, id); e != nil || v.Expired {
				t.Fatal("a valid job reads as expired", v, e)
			}
		}
		list, e := h.s.Issues(ctx, h.p, 10)
		if e != nil || len(list) != 1 || list[0].Expired {
			t.Fatal(list, e)
		}
		// Expired, inside the grace: the files are still stored and refused as they were, the views already say so.
		h.age(readLife + time.Minute)
		_, e = h.s.Download(ctx, h.p, export.ArtifactID)
		mustError(t, e, r.ErrIntegrity)
		for _, id := range []string{snap.ID, export.ID, issue.ID} {
			if v, e := h.s.Job(ctx, h.p, id); e != nil || !v.Expired || v.State != "succeeded" {
				t.Fatal("an expired job's view", v, e)
			}
		}
		// Past the grace and swept: the same views, the same ids, and the refusals of what is gone.
		h.age(grace)
		before := map[string]r.Job{}
		for _, id := range []string{snap.ID, export.ID, issue.ID} {
			before[id] = h.job(id)
		}
		h.sweep(r.SweepOptions{Grace: grace})
		// Both stores reduce a job to the same thing (r.ReduceJob): the job as it was, without its frozen read, marked.
		for id, was := range before {
			if got := h.job(id); !reflect.DeepEqual(got, r.ReduceJob(was)) {
				t.Fatalf("the reduced job is not ReduceJob of the job:\n got %+v\nwant %+v", got, r.ReduceJob(was))
			}
		}
		views := map[string]r.JobView{}
		for _, id := range []string{snap.ID, export.ID, issue.ID} {
			v, e := h.s.Job(ctx, h.p, id)
			if e != nil || !v.Expired || v.State != "succeeded" || v.ErrorCode != "" {
				t.Fatal("a swept job's view", v, e)
			}
			views[id] = v
		}
		if views[snap.ID].SnapshotID != snap.SnapshotID || views[export.ID].ArtifactID != export.ArtifactID || views[export.ID].SnapshotID != snap.SnapshotID || views[issue.ID].ArtifactID != issue.ArtifactID || views[issue.ID].Issue == nil || views[issue.ID].Issue.Number != issue.Issue.Number || views[issue.ID].Issue.TemplateID == "" {
			t.Fatalf("the ids and the number must stay: %+v", views)
		}
		list, e = h.s.Issues(ctx, h.p, 10)
		if e != nil || len(list) != 1 || !list[0].Expired || list[0].Number != issue.Issue.Number || list[0].ArtifactID != issue.ArtifactID || list[0].SHA256 == "" || list[0].Pages != 3 || list[0].ConfirmationRef == "" {
			t.Fatalf("the issue listing after the sweep: %+v %v", list, e)
		}
		if one, e := h.s.Issue(ctx, h.p, issue.ID); e != nil || !one.Expired || one.Number != list[0].Number {
			t.Fatal(one, e)
		}
		// What is gone is refused cleanly: not found, never an integrity failure, never a failure of the store.
		_, e = h.s.Rows(ctx, h.p, snap.SnapshotID, 1, "")
		mustError(t, e, r.ErrNotFound)
		_, e = h.s.Download(ctx, h.p, export.ArtifactID)
		mustError(t, e, r.ErrNotFound)
		_, e = h.s.Download(ctx, h.p, issue.ArtifactID)
		mustError(t, e, r.ErrNotFound)
		_, e = h.s.SubmitExport(ctx, h.p, snap.SnapshotID, "again")
		mustError(t, e, r.ErrNotFound)
		_, e = h.s.SubmitIssue(ctx, h.p, issueInput(snap.SnapshotID, 0), "again")
		mustError(t, e, r.ErrNotFound)
		// The replay of an admission whose job was swept is denied as an expired job's always was.
		_, e = h.s.SubmitSnapshot(ctx, h.p, contractsim.FinancialQuery(), "snap")
		mustError(t, e, r.ErrDenied)
		// A cancel changes nothing and answers; the job is as it was.
		if v, e := h.s.Cancel(ctx, h.p, snap.ID); e != nil || v.State != "succeeded" || v.SnapshotID != "" {
			t.Fatal(v, e)
		}
		if v, e := h.s.Cancel(ctx, h.p, issue.ID); e != nil || v.State != "succeeded" || v.Issue == nil || v.Issue.Number != issue.Issue.Number {
			t.Fatal(v, e)
		}
		// What a reduced job keeps: its identity, its kind, its key and request hash, its ids and, for an issue, the frozen template
		// and row; what it lost: the query (its values), the source read and the columns.
		j := h.job(snap.ID)
		if !j.Reduced || j.Owner.Identity != h.p.Identity || j.Kind != "snapshot" || j.Key != "snap" || j.RequestHash == "" || j.SnapshotID != snap.SnapshotID ||
			len(j.Query.Companies) != 0 || j.Query.Integration != "" || j.Prepared.Ref != "" || j.Prepared.ScopeHandle != "" || len(j.Columns) != 0 {
			t.Fatalf("the reduced snapshot job: %+v", j)
		}
		ij := h.job(issue.ID)
		if !ij.Reduced || ij.Template != frozenTemplate || ij.TemplateID == "" || ij.TemplateSHA256 == "" || ij.Query.Integration != "" || ij.ArtifactID != issue.ArtifactID || ij.RowIndex != 0 {
			t.Fatalf("the reduced issue job: %+v", ij)
		}
	})
}

// ageDurable lets time pass for the durable store, whose clock is the database's and cannot move: every stored expiry goes into the past by d,
// the column, the payload and the digest of each row rewritten together.
func ageDurable(t *testing.T, admin *pgxpool.Pool, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	const iso = `'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'`
	for _, sql := range []string{
		`UPDATE reporting_s01.assets SET expires_at = expires_at - $1::interval WHERE expires_at IS NOT NULL`,
		`UPDATE reporting_s01.assets SET data = jsonb_set(data, '{ExpiresAt}', to_jsonb(to_char(expires_at AT TIME ZONE 'UTC', ` + iso + `))) WHERE expires_at IS NOT NULL`,
		`UPDATE reporting_s01.assets SET data_sha256 = encode(sha256(convert_to(data::text,'UTF8')),'hex')`,
		`UPDATE reporting_s01.jobs SET source_expires_at = source_expires_at - $1::interval`,
		`UPDATE reporting_s01.jobs SET data = jsonb_set(data, '{Prepared,ExpiresAt}', to_jsonb(to_char(source_expires_at AT TIME ZONE 'UTC', ` + iso + `))) WHERE data ? 'Prepared'`,
		`UPDATE reporting_s01.jobs SET data_sha256 = encode(sha256(convert_to(data::text,'UTF8')),'hex')`,
	} {
		var e error
		if strings.Contains(sql, "$1") {
			_, e = admin.Exec(ctx, sql, fmt.Sprintf("%d microseconds", d.Microseconds()))
		} else {
			_, e = admin.Exec(ctx, sql)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
}
