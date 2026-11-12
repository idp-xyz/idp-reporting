package integration_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// The lifetime contract of ADR 0026 (docs/S25A_SCOPE.md), run on both stores: a result outlives the read it was frozen from when the
// source vouches for the read's scope, every disclosure of it still asks the source, and a source that vouches for nothing is served as
// before. A world lets time pass for its store, its services and its simulator together. The memory world's clock moves, so its reads last
// ten minutes and its scope three hours; the durable world's clock is the database's, so its reads last four seconds and its scope ten,
// and the world really waits them out.
type lifetimeWorld struct {
	t        *testing.T
	repo     r.Repository
	sim      *askCounter
	p        r.Principal
	owner    r.Owner
	clock    func() time.Time    // the services' clock; nil is the real one
	now      func() time.Time    // the world's time
	pass     func(time.Duration) // lets this much time pass
	readLife time.Duration       // how long this world's reads last
	scope    time.Duration       // how long, from a read's preparation, the simulator vouches for its scope once vouch is called
	margin   time.Duration       // what the world waits beyond a time to be past it
	hours    bool                // the world's results' time is shorter than its scope: its clock moves by hours
	// deadline is the store's own deadline of a job, where the store has one to show: PostgreSQL's jobs.source_expires_at.
	deadline func(jobID string) time.Time
}

// lifetimeResults is Retention.Results of the worlds' services; issued documents keep the default, ten years.
const lifetimeResults = time.Hour

// askCounter is the simulator, counting how often the platform asks it whether a person's current rights still cover a read.
type askCounter struct {
	*contractsim.Simulator
	asked atomic.Int64
}

func (a *askCounter) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, p r.PreparedRead) error {
	a.asked.Add(1)
	return a.Simulator.AuthorizeCurrent(ctx, sc, p)
}

func (w *lifetimeWorld) svcWith(q r.Quotas, keep r.Retention) *r.Service {
	w.t.Helper()
	s, e := r.NewService(r.Config{Repository: w.repo, Connections: []r.Connection{{Tenant: w.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: w.sim}},
		CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: &stampIssuer{}, Now: w.clock, Quotas: q, Retention: keep})
	if e != nil {
		w.t.Fatal(e)
	}
	return s
}
func (w *lifetimeWorld) svc() *r.Service {
	return w.svcWith(r.Quotas{}, r.Retention{Results: lifetimeResults})
}

func newLifetimeMemory(t *testing.T) *lifetimeWorld {
	t.Helper()
	clock := &retentionClock{t: time.Date(2026, 11, 11, 7, 30, 0, 123456789, time.UTC)}
	w := &lifetimeWorld{t: t, sim: &askCounter{Simulator: contractsim.Financial()}, p: contractsim.DemoPrincipal(), clock: clock.Now, now: clock.Now, pass: clock.Advance,
		readLife: contractsim.DefaultReadLifetime, scope: 3 * time.Hour, margin: time.Minute, hours: true}
	w.owner = contractsim.DemoOwner(w.p, contractsim.FinanceIntegration)
	w.sim.SetClock(clock.Now)
	w.sim.Grant(w.owner, contractsim.DefaultGrant())
	w.repo = memory.New(clock.Now)
	return w
}

// newLifetimeDurable is the durable store under the simulator in memory, both on the real clock.
func newLifetimeDurable(t *testing.T) *lifetimeWorld {
	t.Helper()
	v := newDurable(t)
	admin := fixtureAdmin(t)
	w := &lifetimeWorld{t: t, sim: &askCounter{Simulator: contractsim.Financial()}, p: v.p, repo: v.repo, now: time.Now, pass: time.Sleep,
		readLife: 4 * time.Second, scope: 10 * time.Second, margin: 500 * time.Millisecond}
	w.owner = contractsim.DemoOwner(w.p, contractsim.FinanceIntegration)
	w.sim.SetReadLifetime(w.readLife)
	w.sim.Grant(w.owner, contractsim.DefaultGrant())
	w.deadline = func(id string) time.Time {
		var at time.Time
		if e := admin.QueryRow(context.Background(), `SELECT source_expires_at FROM reporting_s01.jobs WHERE id=$1`, id).Scan(&at); e != nil {
			t.Fatal(e)
		}
		return at
	}
	return w
}

// vouch makes the simulator vouch for the scope of the reads it prepares from now on.
func (w *lifetimeWorld) vouch() { w.sim.SetScopeLifetime(w.scope) }

// until lets time pass until just after at.
func (w *lifetimeWorld) until(at time.Time) {
	if d := at.Sub(w.now()) + w.margin; d > 0 {
		w.pass(d)
	}
}

// resultsEnd is when a result of the results' class made from the read ends: the read's expiry and the results' time, or the scope's end
// when that is earlier. Which of the two it is belongs to the world (hours), and is checked against it.
func (w *lifetimeWorld) resultsEnd(read r.PreparedRead) time.Time {
	w.t.Helper()
	end := read.ExpiresAt.Add(lifetimeResults)
	if read.ScopeExpiresAt.Before(end) {
		end = read.ScopeExpiresAt
	}
	if w.hours != end.Equal(read.ExpiresAt.Add(lifetimeResults)) {
		w.t.Fatalf("the results' end %s is not the one this world was built for (read %s, scope %s)", end, read.ExpiresAt, read.ScopeExpiresAt)
	}
	return end
}

// deadlineIs holds the store's own deadline of a job to a time, to the microsecond the store keeps; the memory store has none to show.
func (w *lifetimeWorld) deadlineIs(id string, at time.Time) {
	w.t.Helper()
	if w.deadline == nil {
		return
	}
	if got := w.deadline(id); !got.Equal(at.Truncate(time.Microsecond)) {
		w.t.Fatalf("the store's deadline of job %s is %s, want %s", id, got.UTC(), at.UTC())
	}
}

func (w *lifetimeWorld) done(s *r.Service, j r.JobView, e error) r.JobView {
	w.t.Helper()
	ctx := context.Background()
	if e != nil {
		w.t.Fatal(e)
	}
	if ran, e := s.RunOne(ctx); e != nil || !ran {
		w.t.Fatal(ran, e)
	}
	if j, e = s.Job(ctx, w.p, j.ID); e != nil || j.State != "succeeded" {
		w.t.Fatal(j, e)
	}
	return j
}
func (w *lifetimeWorld) freeze(s *r.Service, key string) r.JobView {
	w.t.Helper()
	j, e := s.SubmitSnapshot(context.Background(), w.p, contractsim.FinancialQuery(), key)
	return w.done(s, j, e)
}
func (w *lifetimeWorld) keptFreeze(s *r.Service, key string) r.JobView {
	w.t.Helper()
	j, e := s.SubmitKeptSnapshot(context.Background(), w.p, contractsim.FinancialQuery(), key)
	return w.done(s, j, e)
}
func (w *lifetimeWorld) export(s *r.Service, snapshot, key string) r.JobView {
	w.t.Helper()
	j, e := s.SubmitExport(context.Background(), w.p, snapshot, key)
	return w.done(s, j, e)
}
func (w *lifetimeWorld) issue(s *r.Service, snapshot, key string) r.JobView {
	w.t.Helper()
	j, e := s.SubmitIssue(context.Background(), w.p, issueInput(snapshot, 0), key)
	return w.done(s, j, e)
}
func (w *lifetimeWorld) job(id string) r.Job {
	w.t.Helper()
	j, e := w.repo.Job(context.Background(), w.p.Identity, id)
	if e != nil {
		w.t.Fatal(e)
	}
	return j
}
func (w *lifetimeWorld) artifact(id string) r.Artifact {
	w.t.Helper()
	a, e := w.repo.Artifact(context.Background(), w.p.Identity, id)
	if e != nil {
		w.t.Fatal(e)
	}
	return a
}
func (w *lifetimeWorld) download(s *r.Service, id string) error {
	a, e := s.Download(context.Background(), w.p, id)
	if e == nil && len(a.Content) == 0 {
		return errors.New("the download has no content")
	}
	return e
}

// refusedByTheSource holds a disclosure to the source's answer of the moment: unavailable, revoked, narrowed, and served again under the
// rights the read was frozen on. A source that cannot be asked is not a yes, and neither is the owner alone.
func (w *lifetimeWorld) refusedByTheSource(what string, disclose func() error) {
	w.t.Helper()
	w.sim.SetUnavailable(true)
	if e := disclose(); !errors.Is(e, r.ErrUnavailable) {
		w.t.Fatalf("%s with the source unavailable: %v", what, e)
	}
	w.sim.SetUnavailable(false)
	w.sim.Revoke(w.owner)
	if e := disclose(); !errors.Is(e, r.ErrDenied) {
		w.t.Fatalf("%s after a revocation: %v", what, e)
	}
	narrowed := contractsim.DefaultGrant()
	narrowed.Companies = []string{"company_other"}
	w.sim.Grant(w.owner, narrowed)
	if e := disclose(); !errors.Is(e, r.ErrDenied) {
		w.t.Fatalf("%s under narrowed rights: %v", what, e)
	}
	w.sim.Grant(w.owner, contractsim.DefaultGrant())
	if e := disclose(); e != nil {
		w.t.Fatalf("%s under the rights it was frozen on: %v", what, e)
	}
}

func TestMemoryLifetimeContract(t *testing.T) { runLifetimeContract(t, newLifetimeMemory) }

func runLifetimeContract(t *testing.T, newWorld func(*testing.T) *lifetimeWorld) {
	ctx := context.Background()
	const grace = time.Second
	sweep := func(t *testing.T, s *r.Service) r.SweepResult {
		t.Helper()
		res, e := s.Sweep(ctx, r.SweepOptions{Grace: grace})
		if e != nil {
			t.Fatal(e)
		}
		return res
	}
	t.Run("a_source_that_vouches_for_no_scope_is_served_as_before", func(t *testing.T) {
		w := newWorld(t)
		s := w.svc()
		snap := w.freeze(s, "snap")
		ex := w.export(s, snap.SnapshotID, "export")
		is := w.issue(s, snap.SnapshotID, "issue")
		read := w.job(snap.ID).Prepared
		// Nothing is kept beyond the read, and nothing says otherwise: no job freezes a time, no file carries a basis of its own, and the
		// store's deadline of every job is the read's expiry.
		for _, id := range []string{snap.ID, ex.ID, is.ID} {
			if j := w.job(id); !j.KeepUntil.IsZero() || !j.Prepared.ScopeExpiresAt.IsZero() || !j.ResultEnd().Equal(j.Prepared.ExpiresAt) {
				t.Fatalf("job %s keeps something: %+v", j.Kind, j)
			}
			w.deadlineIs(id, read.ExpiresAt)
		}
		for _, v := range []r.JobView{ex, is} {
			if a := w.artifact(v.ArtifactID); a.Query != nil || a.Prepared != nil || !a.ExpiresAt.Equal(read.ExpiresAt) {
				t.Fatalf("the file of the %s job: %+v", v.Kind, a)
			}
		}
		if e := w.download(s, ex.ArtifactID); e != nil {
			t.Fatal("the file cannot be downloaded while its read lasts:", e)
		}
		w.until(read.ExpiresAt)
		// The read is over and so is everything made from it, exactly as before ADR 0026.
		mustError(t, w.download(s, ex.ArtifactID), r.ErrIntegrity)
		mustError(t, w.download(s, is.ArtifactID), r.ErrIntegrity)
		_, e := s.Rows(ctx, w.p, snap.SnapshotID, 10, "")
		mustError(t, e, r.ErrIntegrity)
		for _, id := range []string{snap.ID, ex.ID, is.ID} {
			if v, e := s.Job(ctx, w.p, id); e != nil || !v.Expired {
				t.Fatal("a job whose read is over does not read as expired", v, e)
			}
		}
		_, e = s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "snap")
		mustError(t, e, r.ErrDenied)
		// The sweep takes all of it once the grace is past, and reduces the three jobs.
		w.until(read.ExpiresAt.Add(grace))
		if res := sweep(t, s); res.Snapshots != 1 || res.Artifacts != 2 || res.Jobs != 3 {
			t.Fatalf("the sweep after the read: %+v", res)
		}
	})
	t.Run("an_export_outlives_its_read_and_every_download_asks_the_source", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svc()
		snap := w.freeze(s, "snap")
		ex := w.export(s, snap.SnapshotID, "export")
		job := w.job(ex.ID)
		read := job.Prepared
		if got := read.ScopeExpiresAt.Sub(read.ExpiresAt); read.ScopeExpiresAt.IsZero() || got != w.scope-w.readLife {
			t.Fatalf("the simulator vouches for %s beyond the read, want %s", got, w.scope-w.readLife)
		}
		// The export freezes its file's expiry. The file has that expiry and its own basis; the store's deadline of the job is that time.
		want := w.resultsEnd(read)
		if !job.KeepUntil.Equal(want) {
			t.Fatalf("the export keeps until %s, want %s", job.KeepUntil, want)
		}
		a := w.artifact(ex.ArtifactID)
		if !a.ExpiresAt.Equal(want) || a.Query == nil || a.Prepared == nil || a.Prepared.Ref != read.Ref || r.Digest(*a.Query) != r.Digest(job.Query) {
			t.Fatalf("the kept file: %+v", a)
		}
		w.deadlineIs(ex.ID, want)
		// A person's freeze is working state: its job keeps nothing beyond the read.
		if sj := w.job(snap.ID); !sj.KeepUntil.IsZero() {
			t.Fatalf("the working snapshot keeps until %s", sj.KeepUntil)
		}
		w.deadlineIs(snap.ID, read.ExpiresAt)
		if e := w.download(s, ex.ArtifactID); e != nil {
			t.Fatal(e)
		}
		w.until(read.ExpiresAt)
		// The read is over: the working snapshot with it, and its job says so. The file is served, the source is asked for it once, and
		// its job does not say expired.
		_, e := s.Rows(ctx, w.p, snap.SnapshotID, 10, "")
		mustError(t, e, r.ErrIntegrity)
		if v, e := s.Job(ctx, w.p, snap.ID); e != nil || !v.Expired {
			t.Fatal(v, e)
		}
		asked := w.sim.asked.Load()
		if e := w.download(s, ex.ArtifactID); e != nil || w.sim.asked.Load() != asked+1 {
			t.Fatalf("the kept file after its read: %v, the source asked %d times", e, w.sim.asked.Load()-asked)
		}
		if v, e := s.Job(ctx, w.p, ex.ID); e != nil || v.Expired || v.ArtifactID != ex.ArtifactID {
			t.Fatal("the export's view after its read:", v, e)
		}
		w.refusedByTheSource("the kept file", func() error { return w.download(s, ex.ArtifactID) })
		// The snapshot goes when its own time and the grace are past, and its job's payload with it. The file and its job stay, and the
		// file is served without its snapshot, by the source's answer still.
		w.until(read.ExpiresAt.Add(grace))
		if res := sweep(t, s); res.Snapshots != 1 || res.Artifacts != 0 || res.Jobs != 1 {
			t.Fatalf("the sweep after the read: %+v", res)
		}
		if _, e := w.repo.Snapshot(ctx, w.p.Identity, snap.SnapshotID); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("the working snapshot is still stored:", e)
		}
		if !w.job(snap.ID).Reduced || w.job(ex.ID).Reduced {
			t.Fatal("the snapshot's job is reduced with its read and the kept file's job is not")
		}
		w.refusedByTheSource("the kept file without its snapshot", func() error { return w.download(s, ex.ArtifactID) })
		// The file's own time passes: it is refused without the source being asked, its job says expired, and the sweep takes it.
		w.until(want)
		asked = w.sim.asked.Load()
		mustError(t, w.download(s, ex.ArtifactID), r.ErrIntegrity)
		if v, e := s.Job(ctx, w.p, ex.ID); e != nil || !v.Expired || v.State != "succeeded" {
			t.Fatal(v, e)
		}
		if n := w.sim.asked.Load() - asked; n != 0 {
			t.Fatalf("the source was asked %d times about a file that is over", n)
		}
		w.until(want.Add(grace))
		if res := sweep(t, s); res.Artifacts != 1 || res.Snapshots != 0 || res.Jobs != 1 {
			t.Fatalf("the sweep after the file's time: %+v", res)
		}
		mustError(t, w.download(s, ex.ArtifactID), r.ErrNotFound)
		if !w.job(ex.ID).Reduced {
			t.Fatal("the export's job is not reduced once its file is gone")
		}
	})
	t.Run("an_issued_document_is_kept_within_the_scope_and_served_under_its_record", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svc()
		snap := w.freeze(s, "snap")
		is := w.issue(s, snap.SnapshotID, "issue")
		job, a := w.job(is.ID), w.artifact(is.ArtifactID)
		read := job.Prepared
		// Ten years of the class and the scope of this world: the scope is the earlier, and nothing is kept beyond it.
		scope := read.ScopeExpiresAt
		if scope.IsZero() || !job.KeepUntil.Equal(scope) || !a.ExpiresAt.Equal(scope) || a.Issue != is.ID || a.Query == nil || a.Prepared == nil {
			t.Fatalf("the issued file is kept until %s (scope %s): %+v", job.KeepUntil, scope, a)
		}
		// The job needed the read to publish; having succeeded, its deadline in the store is the time its file is kept until.
		w.deadlineIs(is.ID, scope)
		w.until(read.ExpiresAt)
		// The formal file is there after its read, under its record.
		if e := w.download(s, is.ArtifactID); e != nil {
			t.Fatal("the issued file is refused after its read:", e)
		}
		if v, e := s.Issue(ctx, w.p, is.ID); e != nil || v.Expired || v.Number == "" || v.ArtifactID != is.ArtifactID {
			t.Fatal(v, e)
		}
		if v, e := s.Job(ctx, w.p, is.ID); e != nil || v.Expired || v.Issue == nil || v.Issue.Number == "" {
			t.Fatal("the issue job's view after its read:", v, e)
		}
		rec, e := w.repo.Issue(ctx, w.p.Identity, is.ID)
		if e != nil || rec.ArtifactID != is.ArtifactID || rec.SHA256 != a.SHA256 {
			t.Fatal(rec, e)
		}
		// A sweep while the file is kept takes the working snapshot it was issued from and that snapshot's job, and nothing of the issue:
		// its job keeps its payload, its view does not say expired, and the file is served without its snapshot.
		w.until(read.ExpiresAt.Add(grace))
		if res := sweep(t, s); res.Snapshots != 1 || res.Artifacts != 0 || res.Jobs != 1 {
			t.Fatalf("the sweep while the issued file is kept: %+v", res)
		}
		if w.job(is.ID).Reduced {
			t.Fatal("the payload of a job whose file is kept was reduced")
		}
		if v, e := s.Job(ctx, w.p, is.ID); e != nil || v.Expired {
			t.Fatal(v, e)
		}
		w.refusedByTheSource("the issued file", func() error { return w.download(s, is.ArtifactID) })
		// The scope ends, and the file with it: refused, said to be over, swept. The record stays.
		w.until(scope)
		mustError(t, w.download(s, is.ArtifactID), r.ErrIntegrity)
		if v, e := s.Issue(ctx, w.p, is.ID); e != nil || !v.Expired || v.Number != rec.Number {
			t.Fatal(v, e)
		}
		w.until(scope.Add(grace))
		if res := sweep(t, s); res.Artifacts != 1 || res.Jobs != 1 {
			t.Fatalf("the sweep after the scope: %+v", res)
		}
		if kept, e := w.repo.Issue(ctx, w.p.Identity, is.ID); e != nil || kept.Number != rec.Number {
			t.Fatal("the issue record is not kept:", e)
		}
	})
	t.Run("a_kept_snapshot_is_read_and_exported_after_its_read_and_not_issued_from", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svc()
		run := w.keptFreeze(s, "run")
		job := w.job(run.ID)
		read := job.Prepared
		want := w.resultsEnd(read)
		snap, e := w.repo.Snapshot(ctx, w.p.Identity, run.SnapshotID)
		if e != nil || !job.KeepUntil.Equal(want) || !snap.ExpiresAt.Equal(want) {
			t.Fatalf("the kept snapshot expires at %s, its job keeps until %s, want %s (%v)", snap.ExpiresAt, job.KeepUntil, want, e)
		}
		w.deadlineIs(run.ID, want)
		w.until(read.ExpiresAt)
		// Its rows are served after the read, by the source's answer, and say until when.
		page, e := s.Rows(ctx, w.p, run.SnapshotID, 10, "")
		if e != nil || len(page.Rows) == 0 || !page.ExpiresAt.Equal(want) {
			t.Fatal("the kept snapshot's rows after its read:", page.ExpiresAt, e)
		}
		if v, e := s.Job(ctx, w.p, run.ID); e != nil || v.Expired {
			t.Fatal(v, e)
		}
		// A replay of the freeze answers the job: its result is not over.
		if again, e := s.SubmitKeptSnapshot(ctx, w.p, contractsim.FinancialQuery(), "run"); e != nil || again.ID != run.ID {
			t.Fatal("the replay of a kept freeze:", again, e)
		}
		// An export needs the snapshot and the scope, not the read: it is admitted, runs and is served, and lives as long as the snapshot.
		late := w.export(s, run.SnapshotID, "late")
		if e := w.download(s, late.ArtifactID); e != nil {
			t.Fatal(e)
		}
		if j := w.job(late.ID); !j.KeepUntil.Equal(want) {
			t.Fatalf("the late export keeps until %s, want %s", j.KeepUntil, want)
		}
		// An issue needs the read itself: the product checks the row against the rows it froze.
		_, e = s.SubmitIssue(ctx, w.p, issueInput(run.SnapshotID, 0), "issue")
		mustError(t, e, r.ErrDenied)
		// A sweep while the snapshot is kept takes nothing and reduces nothing.
		w.until(read.ExpiresAt.Add(grace))
		if res := sweep(t, s); res.Snapshots != 0 || res.Artifacts != 0 || res.Jobs != 0 {
			t.Fatalf("the sweep while the snapshot is kept: %+v", res)
		}
		if w.job(run.ID).Reduced || w.job(late.ID).Reduced {
			t.Fatal("the payload of a job whose result is kept was reduced")
		}
		// And the source decides every page.
		w.refusedByTheSource("the kept snapshot's rows", func() error { _, e := s.Rows(ctx, w.p, run.SnapshotID, 10, ""); return e })
	})
	t.Run("an_export_that_ends_without_a_file_keeps_nothing", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svc()
		snap := w.freeze(s, "snap")
		read := w.job(snap.ID).Prepared
		late, e := s.SubmitExport(ctx, w.p, snap.SnapshotID, "late")
		if e != nil {
			t.Fatal(e)
		}
		dropped, e := s.SubmitExport(ctx, w.p, snap.SnapshotID, "dropped")
		if e != nil {
			t.Fatal(e)
		}
		// A queued export may publish for as long as its file would be served: that is its deadline in the store.
		w.deadlineIs(late.ID, w.resultsEnd(read))
		// Cancelled, it keeps nothing, and its deadline comes back to the read's expiry.
		if v, e := s.Cancel(ctx, w.p, dropped.ID); e != nil || v.State != "cancelled" {
			t.Fatal(v, e)
		}
		w.deadlineIs(dropped.ID, read.ExpiresAt)
		// The worker reaches the other after the read: the scope goes on, the working snapshot has ended. The job is denied, as an export
		// whose read had expired always was, and is not an integrity failure of the store. Failed, it keeps nothing either.
		w.until(read.ExpiresAt)
		if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, r.ErrDenied) || errors.Is(e, r.ErrIntegrity) {
			t.Fatal(ran, e)
		}
		if j := w.job(late.ID); j.State != "failed" || j.ErrorCode != r.ErrDenied.Error() || j.ArtifactID != "" {
			t.Fatalf("%+v", j)
		}
		w.deadlineIs(late.ID, read.ExpiresAt)
		// A new export of the ended snapshot is refused at admission, as it always was, and admits nothing.
		again, e := s.SubmitExport(ctx, w.p, snap.SnapshotID, "again")
		mustError(t, e, r.ErrIntegrity)
		if again.ID != "" {
			t.Fatal(again)
		}
		// The frozen query of the two exports goes with the read's grace, not a results' time later: three payloads are reduced.
		w.until(read.ExpiresAt.Add(grace))
		if res := sweep(t, s); res.Snapshots != 1 || res.Artifacts != 0 || res.Jobs != 3 {
			t.Fatalf("the sweep after the read: %+v", res)
		}
		for _, id := range []string{snap.ID, late.ID, dropped.ID} {
			if !w.job(id).Reduced {
				t.Fatalf("job %s keeps its payload", id)
			}
		}
	})
	t.Run("after_the_scope_the_source_is_not_asked", func(t *testing.T) {
		w := newWorld(t)
		if !w.hours {
			return // the service decides this before a store is asked; the durable world would only wait its scope out once more
		}
		w.vouch()
		s := w.svc()
		snap := w.freeze(s, "snap")
		queued, e := s.SubmitExport(ctx, w.p, snap.SnapshotID, "queued")
		if e != nil {
			t.Fatal(e)
		}
		// No worker reaches the export until the scope has ended. It is denied then, and the source is not asked about a read it no
		// longer vouches for: not by the worker, and not by the poll of the job.
		w.until(w.job(queued.ID).Prepared.ScopeExpiresAt)
		asked := w.sim.asked.Load()
		if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, r.ErrDenied) {
			t.Fatal(ran, e)
		}
		if v, e := s.Job(ctx, w.p, queued.ID); e != nil || v.State != "failed" || v.ErrorCode != r.ErrDenied.Error() {
			t.Fatal(v, e)
		}
		if n := w.sim.asked.Load() - asked; n != 0 {
			t.Fatalf("the source was asked %d times after the scope it vouched for", n)
		}
	})
	t.Run("a_replay_of_a_finished_freeze_is_denied_once_its_snapshot_is_over", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svc()
		snap := w.freeze(s, "snap")
		read := w.job(snap.ID).Prepared
		if again, e := s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "snap"); e != nil || again.ID != snap.ID {
			t.Fatal("the replay while the snapshot lasts:", again, e)
		}
		// The working snapshot ends with its read although the source goes on vouching for the scope: the replay is denied from then on,
		// the same before the sweep has cleared the job's payload and after.
		w.until(read.ExpiresAt)
		_, e := s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "snap")
		mustError(t, e, r.ErrDenied)
		w.until(read.ExpiresAt.Add(grace))
		if res := sweep(t, s); res.Snapshots != 1 || res.Jobs != 1 {
			t.Fatalf("%+v", res)
		}
		_, e = s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "snap")
		mustError(t, e, r.ErrDenied)
	})
	t.Run("an_export_whose_file_would_be_over_is_refused_at_admission", func(t *testing.T) {
		w := newWorld(t)
		if !w.hours {
			return // the durable world's scope is shorter than the shortest results' time, so no file of it can be over before its snapshot
		}
		w.vouch()
		long := w.svc()
		run := w.keptFreeze(long, "run")
		read := w.job(run.ID).Prepared
		// The snapshot is kept for an hour after its read. A server whose results' time is a minute is asked for an export of it five
		// minutes after the read: the file would have been over for four minutes. Nothing is admitted.
		w.until(read.ExpiresAt.Add(4 * time.Minute))
		short := w.svcWith(r.Quotas{}, r.Retention{Results: time.Minute})
		_, e := short.SubmitExport(ctx, w.p, run.SnapshotID, "late")
		mustError(t, e, r.ErrDenied)
		if jobs, e := short.Jobs(ctx, w.p, r.JobListing{Limit: 10}); e != nil || len(jobs) != 1 {
			t.Fatal("an export was admitted:", len(jobs), e)
		}
		// Under the results' time the snapshot was kept with, the same export is made.
		if e := w.download(long, w.export(long, run.SnapshotID, "late").ArtifactID); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("a_job_keeps_within_its_scope_and_publishes_what_it_froze", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svc()
		snap := w.freeze(s, "snap")
		ex := w.export(s, snap.SnapshotID, "export")
		// Another admission of the same export, changed in one respect each time: the store refuses what is outside the three lifetimes.
		again := func(change func(*r.Job)) error {
			j := w.job(ex.ID)
			j.ID, j.Key, j.State, j.Generation, j.LeaseUntil, j.ArtifactID, j.ErrorCode = r.NewID(), r.NewID(), "queued", 0, time.Time{}, "", ""
			change(&j)
			_, e := w.repo.Admit(ctx, j, r.Quotas{})
			return e
		}
		mustError(t, again(func(j *r.Job) { j.KeepUntil = j.Prepared.ScopeExpiresAt.Add(time.Nanosecond) }), r.ErrInvalid)
		mustError(t, again(func(j *r.Job) { j.KeepUntil = j.Prepared.ExpiresAt }), r.ErrInvalid)
		mustError(t, again(func(j *r.Job) { j.Prepared.ScopeExpiresAt = time.Time{} }), r.ErrInvalid) // what it keeps is then beyond the read, its only scope
		// A scope before its read is refused for itself, whatever the job keeps.
		mustError(t, again(func(j *r.Job) {
			j.KeepUntil, j.Prepared.ScopeExpiresAt = time.Time{}, j.Prepared.ExpiresAt.Add(-time.Second)
		}), r.ErrInvalid)
		// An export that keeps nothing under a scope is admitted: it is what a job admitted before this field existed is. It is taken out
		// of the queue again; then the unchanged job is admitted.
		if e := again(func(j *r.Job) { j.KeepUntil = time.Time{} }); e != nil {
			t.Fatal("an export that keeps nothing is not admitted:", e)
		}
		if c, e := w.repo.Claim(ctx, 30*time.Second); e != nil || c == nil || !c.Job.KeepUntil.IsZero() {
			t.Fatal(c, e)
		} else if _, e := w.repo.Cancel(ctx, w.p.Identity, c.Job.ID); e != nil {
			t.Fatal(e)
		}
		if e := again(func(*r.Job) {}); e != nil {
			t.Fatal("the unchanged job is not admitted:", e)
		}
		// The job admitted last is claimed, and what it publishes is held to what it froze.
		c, e := w.repo.Claim(ctx, 30*time.Second)
		if e != nil || c == nil || c.Job.Kind != "export" || c.Job.KeepUntil.IsZero() {
			t.Fatal(c, e)
		}
		file := func(change func(*r.Artifact)) error {
			q, p := c.Job.Query, c.Job.Prepared
			a := r.Artifact{ID: r.NewID(), Owner: c.Job.Owner, SnapshotID: c.Job.SnapshotID, ExpiresAt: c.Job.KeepUntil, SHA256: sha([]byte("x")), Content: []byte("x"), Query: &q, Prepared: &p}
			change(&a)
			return w.repo.Publish(ctx, *c, r.Publication{Artifact: &a})
		}
		mustError(t, file(func(a *r.Artifact) { a.ExpiresAt = c.Job.Prepared.ExpiresAt }), r.ErrIntegrity)
		mustError(t, file(func(a *r.Artifact) { a.ExpiresAt = c.Job.KeepUntil.Add(time.Second) }), r.ErrIntegrity)
		mustError(t, file(func(a *r.Artifact) { a.Query, a.Prepared = nil, nil }), r.ErrIntegrity)
		mustError(t, file(func(a *r.Artifact) { a.Prepared = nil }), r.ErrIntegrity)
		mustError(t, file(func(a *r.Artifact) { q := *a.Query; q.AsOf = "2001-01-01"; a.Query = &q }), r.ErrIntegrity)
		mustError(t, file(func(a *r.Artifact) { p := *a.Prepared; p.ScopeHandle = "another"; a.Prepared = &p }), r.ErrIntegrity)
		// A basis that cannot be encoded is not the job's either, and is refused rather than making the store panic.
		mustError(t, file(func(a *r.Artifact) {
			p := *a.Prepared
			p.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			a.Prepared = &p
		}), r.ErrIntegrity)
		if e := file(func(*r.Artifact) {}); e != nil {
			t.Fatal("what the job froze is not published:", e)
		}
	})
}

// scopeForger is the simulator declaring a scope it makes up: what an adapter that answers with a malformed scope is to the service.
type scopeForger struct {
	*contractsim.Simulator
	scope func(read time.Time) time.Time
}

func (f scopeForger) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, hash string) (r.PreparedRead, error) {
	p, e := f.Simulator.PrepareRead(ctx, sc, q, hash)
	if e == nil {
		p.ScopeExpiresAt = f.scope(p.ExpiresAt)
	}
	return p, e
}

// A scope that is not well formed is an answer the service does not pass on (ADR 0026 decision 2): before the read's own expiry, beyond the
// years a product's time may name, or in a year that cannot be encoded in the time's own zone. Nothing is admitted and nothing is read.
func TestAScopeThatIsNotWellFormedIsAnIntegrityFailure(t *testing.T) {
	ctx := context.Background()
	east := time.FixedZone("east", 8*3600)
	for name, scope := range map[string]func(time.Time) time.Time{
		"a scope that ends before its read":                    func(read time.Time) time.Time { return read.Add(-time.Second) },
		"a scope beyond the years a product may name":          func(time.Time) time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"a scope whose year cannot be encoded in its own zone": func(time.Time) time.Time { return time.Date(9999, 12, 31, 20, 0, 0, 0, time.UTC).In(east) },
	} {
		clock := &retentionClock{t: time.Date(2026, 11, 11, 7, 30, 0, 0, time.UTC)}
		sim := contractsim.Financial()
		sim.SetClock(clock.Now)
		p := contractsim.DemoPrincipal()
		sim.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
		repo := memory.New(clock.Now)
		s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: scopeForger{sim, scope}}},
			CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Now: clock.Now})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "k"); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("%s: a freeze answered %v", name, e)
		}
		if _, e = s.Query(ctx, p, contractsim.FinancialQuery()); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("%s: a preview answered %v", name, e)
		}
		if c, e := repo.Claim(ctx, 30*time.Second); e != nil || c != nil {
			t.Errorf("%s: a job was admitted (%v)", name, e)
		}
		if sim.ReadCalls() != 0 {
			t.Errorf("%s: %d pages were read", name, sim.ReadCalls())
		}
	}
}
