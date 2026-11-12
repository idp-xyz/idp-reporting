package integration_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/objects"
)

// The lifetime contract of ADR 0026 (docs/S25A_SCOPE.md), run on both stores: a result outlives the read it was frozen from when the
// source vouches for the read's scope, every disclosure of it still asks the source, and a source that vouches for nothing is served as
// before. A world lets time pass for its store, its services and its simulator together. The memory world's clock moves, so its reads last
// ten minutes and its scope three hours; the durable world's clock is the database's, so its reads last six seconds and its scope
// fourteen, and the world really waits them out.
//
// What the durable world does while a read lasts (a few jobs and a download, tenths of a second) and between a read's grace and the
// scope's end has more than five seconds to spare, and nothing else in it depends on being fast: every other step waits for a time to be
// past. A machine that stalls for longer in one of those stretches, or whose wall clock is stepped forward by as much (a virtual machine
// under time correction: steps of up to three and a half seconds were seen where this was written), fails a subtest without the code
// being wrong; the world says so when one fails (newLifetimeDurable).
type lifetimeWorld struct {
	t        *testing.T
	repo     r.Repository
	sim      *askCounter
	through  r.Adapter     // how the services reach the simulator; nil is the simulator itself, in process
	objects  r.ObjectStore // where the services put their files; nil keeps them inline in their rows
	p        r.Principal
	owner    r.Owner
	clock    func() time.Time    // the services' clock; nil is the real one
	now      func() time.Time    // the world's time
	pass     func(time.Duration) // lets this much time pass
	readLife time.Duration       // how long this world's reads last
	scope    time.Duration       // how long, from a read's preparation, the simulator vouches for its scope once vouch is called
	margin   time.Duration       // what the world waits beyond a time to be past it
	hours    bool                // the world's results' time is shorter than its scope: its clock moves by hours
	rendered atomic.Int64        // the workbooks the world's services have rendered
	// stored is a job's row as the store keeps it, where the store has one to show (PostgreSQL), read without its payload; unreadable
	// makes a stored job one this binary cannot read, consistent with its own digest.
	stored     func(jobID string) storedJob
	unreadable func(jobID string)
}

// storedJob is what the durable store keeps of a job beside its payload: its state and code, when it was admitted, and its deadline
// (jobs.source_expires_at).
type storedJob struct {
	State, Code       string
	Created, Deadline time.Time
}

// lifetimeResults is Retention.Results of the worlds' services; issued documents keep the default, ten years.
const lifetimeResults = time.Hour

// askCounter is the simulator, counting how often the platform asks it whether a person's current rights still cover a read, and how
// often it asks it for a document.
type askCounter struct {
	*contractsim.Simulator
	asked     atomic.Int64
	documents atomic.Int64
}

func (a *askCounter) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, p r.PreparedRead) error {
	a.asked.Add(1)
	return a.Simulator.AuthorizeCurrent(ctx, sc, p)
}
func (a *askCounter) PrepareDocument(ctx context.Context, sc r.SourceContext, in r.IssueIntent) (r.IssueReservation, error) {
	a.documents.Add(1)
	return a.Simulator.PrepareDocument(ctx, sc, in)
}

// renderCounter is the workbook writer, counting the files it renders.
type renderCounter struct {
	xlsx.Writer
	rendered *atomic.Int64
}

func (c renderCounter) Write(ctx context.Context, v r.Snapshot) ([]byte, error) {
	c.rendered.Add(1)
	return c.Writer.Write(ctx, v)
}

func (w *lifetimeWorld) svcWith(q r.Quotas, keep r.Retention) *r.Service {
	w.t.Helper()
	var source r.Adapter = w.sim
	if w.through != nil {
		source = w.through
	}
	s, e := r.NewService(r.Config{Repository: w.repo, Connections: []r.Connection{{Tenant: w.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: source}},
		CursorKey: []byte("01234567890123456789012345678901"), Writer: renderCounter{rendered: &w.rendered}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: &stampIssuer{}, Now: w.clock, Quotas: q, Retention: keep, Objects: w.objects})
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

// newLifetimeOverTheProtocol is the memory world with the simulator behind the reporting source protocol (S25-B2): the reference source
// in front of it, and in front of that the platform's adapter `http` of a connection that declares kept results, on the world's clock.
// The scope the simulator vouches for travels as scope_expires_at, and every question the services ask about a read is a request that
// carries the read back as the source answered it. The contract does not tell the two worlds apart.
func newLifetimeOverTheProtocol(t *testing.T) *lifetimeWorld {
	t.Helper()
	w := newLifetimeMemory(t)
	w.through = sourcetest.Remote(t, w.sim, func(c *httpsource.Config) { c.Now = w.clock })
	return w
}

// newLifetimeDurable is the durable store under the simulator in memory, both on the real clock.
func newLifetimeDurable(t *testing.T) *lifetimeWorld {
	t.Helper()
	v := newDurable(t)
	admin := fixtureAdmin(t)
	w := &lifetimeWorld{t: t, sim: &askCounter{Simulator: contractsim.Financial()}, p: v.p, repo: v.repo, now: time.Now, pass: time.Sleep,
		readLife: 6 * time.Second, scope: 14 * time.Second, margin: 500 * time.Millisecond}
	w.owner = contractsim.DemoOwner(w.p, contractsim.FinanceIntegration)
	w.sim.SetReadLifetime(w.readLife)
	w.sim.Grant(w.owner, contractsim.DefaultGrant())
	started := time.Now()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("this subtest runs on the real clock, with reads of %s and a scope of %s, and took %s: a machine that stalls for seconds while a read or a scope lasts fails it without the code being wrong; the same contract on a clock the test moves is TestMemoryLifetimeContract",
				w.readLife, w.scope, time.Since(started).Round(time.Millisecond))
		}
	})
	w.stored = func(id string) storedJob {
		var v storedJob
		if e := admin.QueryRow(context.Background(), `SELECT state,error_code,created_at,source_expires_at FROM reporting_s01.jobs WHERE id=$1`, id).Scan(&v.State, &v.Code, &v.Created, &v.Deadline); e != nil {
			t.Fatal(e)
		}
		return v
	}
	// What a binary that knows more writes and this one cannot read: a payload consistent with its own digest, of a kind this one does not know.
	w.unreadable = func(id string) {
		for _, statement := range []string{
			`UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{Kind}','"kind-of-a-later-binary"') WHERE id=$1`,
			`UPDATE reporting_s01.jobs SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1`,
		} {
			if _, e := admin.Exec(context.Background(), statement, id); e != nil {
				t.Fatal(e)
			}
		}
	}
	return w
}

// vouch makes the simulator vouch for the scope of the reads it prepares from now on.
func (w *lifetimeWorld) vouch() { w.sim.SetScopeLifetime(w.scope) }

// until lets time pass until just after at. It looks at the clock again after it has waited: the wall clock of a virtual machine is
// corrected in steps, and one that steps back during the wait would otherwise leave the world before the time it waited for.
func (w *lifetimeWorld) until(at time.Time) {
	for d := at.Sub(w.now()) + w.margin; d > 0; d = at.Sub(w.now()) + w.margin {
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
	if w.stored == nil {
		return
	}
	if got := w.stored(id).Deadline; !got.Equal(at.Truncate(time.Microsecond)) {
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

// The same contract with the source behind the protocol (S25-B2): what the simulator vouches for reaches the services through
// scope_expires_at, and every disclosure of a kept result asks the source over the wire.
func TestMemoryLifetimeContractOverTheProtocol(t *testing.T) {
	runLifetimeContract(t, newLifetimeOverTheProtocol)
}

func runLifetimeContract(t *testing.T, newWorld func(*testing.T) *lifetimeWorld) {
	ctx := context.Background()
	const grace = time.Second
	// sweep is one retention sweep, after a dry run that must have counted what the sweep then does: with results of several lifetimes
	// in the store, too.
	sweep := func(t *testing.T, s *r.Service) r.SweepResult {
		t.Helper()
		dry, e := s.Sweep(ctx, r.SweepOptions{Grace: grace, DryRun: true})
		if e != nil {
			t.Fatal(e)
		}
		res, e := s.Sweep(ctx, r.SweepOptions{Grace: grace})
		if e != nil {
			t.Fatal(e)
		}
		if dry.Artifacts != res.Artifacts || dry.Snapshots != res.Snapshots || dry.Jobs != res.Jobs {
			t.Fatalf("the dry run counted %+v and the sweep did %+v", dry, res)
		}
		return res
	}
	t.Run("a_source_that_vouches_for_no_scope_is_served_as_before", func(t *testing.T) {
		w := newWorld(t)
		s := w.svc()
		snap := w.freeze(s, "snap")
		ex := w.export(s, snap.SnapshotID, "export")
		is := w.issue(s, snap.SnapshotID, "issue")
		if e := w.download(s, ex.ArtifactID); e != nil {
			t.Fatal("the file cannot be downloaded while its read lasts:", e)
		}
		// So far the read had to last; what follows reads the store. Nothing is kept beyond the read, and nothing says otherwise: no job
		// freezes a time, no file carries a basis of its own, and the store's deadline of every job is the read's expiry.
		read := w.job(snap.ID).Prepared
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
		// A cancel of the job that has succeeded changes nothing: the file is kept, and so is the store's deadline of its job.
		if v, e := s.Cancel(ctx, w.p, ex.ID); e != nil || v.State != "succeeded" {
			t.Fatal(v, e)
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
		w.refusedByTheSource("the poll of the kept file's job", func() error { _, e := s.Job(ctx, w.p, ex.ID); return e })
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
		queued, e := s.SubmitIssue(ctx, w.p, issueInput(snap.SnapshotID, 0), "issue")
		if e != nil {
			t.Fatal(e)
		}
		// An issue needs the read itself: until the job has published, its deadline in the store is the read's expiry, although its file
		// will be kept until the scope ends.
		w.deadlineIs(queued.ID, w.job(snap.ID).Prepared.ExpiresAt)
		is := w.done(s, queued, nil)
		job, a := w.job(is.ID), w.artifact(is.ArtifactID)
		read := job.Prepared
		// Ten years of the class and the scope of this world: the scope is the earlier, and nothing is kept beyond it.
		scope := read.ScopeExpiresAt
		if scope.IsZero() || !job.KeepUntil.Equal(scope) || !a.ExpiresAt.Equal(scope) || a.Issue != is.ID || a.Query == nil || a.Prepared == nil {
			t.Fatalf("the issued file is kept until %s (scope %s): %+v", job.KeepUntil, scope, a)
		}
		// Having succeeded, the job's deadline in the store is the time its file is kept until.
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
	// S24-A under ADR 0026 ("the refusal of a cancelled file ... does not change"): the file of an issued document that is kept beyond
	// its read is no longer handed out once the product has cancelled the document, for as long as the file is kept, and the source
	// is not asked for it; a kept file that is not cancelled is served as before; the cancellation stays with the record when the
	// scope has ended and the file is gone.
	t.Run("a_kept_issued_document_that_is_cancelled_is_no_longer_handed_out", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svc()
		snap := w.freeze(s, "snap")
		withdrawn, standing := w.issue(s, snap.SnapshotID, "to cancel"), w.issue(s, snap.SnapshotID, "to keep")
		read := w.job(withdrawn.ID).Prepared
		scope := read.ScopeExpiresAt
		if a := w.artifact(withdrawn.ArtifactID); scope.IsZero() || !a.ExpiresAt.Equal(scope) || a.Prepared == nil {
			t.Fatalf("the issued file is not kept until the scope %s: %+v", scope, a)
		}
		// After the read both files stand without their snapshot, and the document is cancelled then: a cancellation needs no read.
		w.until(read.ExpiresAt)
		for _, id := range []string{withdrawn.ArtifactID, standing.ArtifactID} {
			if e := w.download(s, id); e != nil {
				t.Fatal("a kept issued file is refused after its read:", e)
			}
		}
		view, e := s.CancelIssue(ctx, w.p, withdrawn.ID, "客户退回：金额有误")
		if e != nil || view.Cancellation == nil || view.Expired || view.ArtifactID != withdrawn.ArtifactID {
			t.Fatalf("the cancellation of a kept document: %+v %v", view, e)
		}
		// The file is refused as cancelled, and the source is not asked to authorize what will not be handed out.
		asked := w.sim.asked.Load()
		mustError(t, w.download(s, withdrawn.ArtifactID), r.ErrIssueCancelled)
		w.sim.SetUnavailable(true)
		mustError(t, w.download(s, withdrawn.ArtifactID), r.ErrIssueCancelled)
		w.sim.SetUnavailable(false)
		if n := w.sim.asked.Load(); n != asked {
			t.Fatalf("the source was asked %d times about the file of a cancelled document", n-asked)
		}
		if e := w.download(s, standing.ArtifactID); e != nil {
			t.Fatal("the kept file of a document that was not cancelled:", e)
		}
		// A sweep while the files are kept takes the working snapshot and leaves both files; the job's poll still answers, with the
		// cancellation, and the file stays refused.
		w.until(read.ExpiresAt.Add(grace))
		if res := sweep(t, s); res.Snapshots != 1 || res.Artifacts != 0 {
			t.Fatalf("the sweep while the issued files are kept: %+v", res)
		}
		if v, e := s.Job(ctx, w.p, withdrawn.ID); e != nil || v.Expired || v.Issue == nil || v.Issue.Cancellation == nil || *v.Issue.Cancellation != *view.Cancellation {
			t.Fatal("the cancelled issue job's view while its file is kept:", v, e)
		}
		mustError(t, w.download(s, withdrawn.ArtifactID), r.ErrIssueCancelled)
		w.refusedByTheSource("the kept file of the document that was not cancelled", func() error { return w.download(s, standing.ArtifactID) })
		// The scope ends. The cancelled file is still refused as cancelled while its row is there; the other as a file whose time has
		// passed; after the sweep neither is found, and the record keeps the cancellation.
		w.until(scope)
		mustError(t, w.download(s, withdrawn.ArtifactID), r.ErrIssueCancelled)
		mustError(t, w.download(s, standing.ArtifactID), r.ErrIntegrity)
		w.until(scope.Add(grace))
		if res := sweep(t, s); res.Artifacts != 2 {
			t.Fatalf("the sweep after the scope: %+v", res)
		}
		for _, id := range []string{withdrawn.ArtifactID, standing.ArtifactID} {
			mustError(t, w.download(s, id), r.ErrNotFound)
		}
		rec, e := w.repo.Issue(ctx, w.p.Identity, withdrawn.ID)
		if e != nil || rec.Cancellation == nil || rec.Cancellation.Ref != view.Cancellation.Ref {
			t.Fatalf("the record after the scope: %+v %v", rec.Cancellation, e)
		}
		if v, e := s.Issue(ctx, w.p, withdrawn.ID); e != nil || !v.Expired || v.Cancellation == nil || *v.Cancellation != *view.Cancellation || v.Number != view.Number {
			t.Fatalf("the cancelled issue's view after the scope: %+v %v", v, e)
		}
		if v, e := s.Issue(ctx, w.p, standing.ID); e != nil || !v.Expired || v.Cancellation != nil {
			t.Fatalf("the other issue's view after the scope: %+v %v", v, e)
		}
	})
	t.Run("a_kept_snapshot_is_read_and_exported_after_its_read_and_not_issued_from", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svc()
		queued, e := s.SubmitKeptSnapshot(ctx, w.p, contractsim.FinancialQuery(), "run")
		if e != nil {
			t.Fatal(e)
		}
		// A freeze needs the read itself, a kept one too: until the job has published, its deadline in the store is the read's expiry.
		w.deadlineIs(queued.ID, w.job(queued.ID).Prepared.ExpiresAt)
		run := w.done(s, queued, nil)
		job := w.job(run.ID)
		read := job.Prepared
		want := w.resultsEnd(read)
		snap, e := w.repo.Snapshot(ctx, w.p.Identity, run.SnapshotID)
		if e != nil || !job.KeepUntil.Equal(want) || !snap.ExpiresAt.Equal(want) {
			t.Fatalf("the kept snapshot expires at %s, its job keeps until %s, want %s (%v)", snap.ExpiresAt, job.KeepUntil, want, e)
		}
		// Having succeeded, the job's deadline in the store is the time its snapshot is kept until.
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
		w.deadlineIs(dropped.ID, w.resultsEnd(read))
		// Cancelled, it keeps nothing, and its deadline comes back to the read's expiry; cancelled again, nothing changes.
		for range 2 {
			if v, e := s.Cancel(ctx, w.p, dropped.ID); e != nil || v.State != "cancelled" {
				t.Fatal(v, e)
			}
			w.deadlineIs(dropped.ID, read.ExpiresAt)
		}
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
		// A cancel repeated after the payload is gone finds no read to name, and leaves the store's deadline what it was.
		if v, e := s.Cancel(ctx, w.p, dropped.ID); e != nil || v.State != "cancelled" {
			t.Fatal(v, e)
		}
		w.deadlineIs(dropped.ID, read.ExpiresAt)
	})
	t.Run("after_the_scope_the_source_is_not_asked", func(t *testing.T) {
		w := newWorld(t)
		if !w.hours {
			t.Skip("the service decides this before a store is asked; the durable world would only wait its scope out once more")
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
			t.Skip("the durable world's scope is shorter than the shortest results' time, so no file of it can be over before its snapshot")
		}
		w.vouch()
		long := w.svc()
		run := w.keptFreeze(long, "run")
		read := w.job(run.ID).Prepared
		// The snapshot is kept for an hour after its read. A server whose results' time is a minute admits an export of it while the read
		// lasts: the file would live a minute after the read.
		short := w.svcWith(r.Quotas{}, r.Retention{Results: time.Minute})
		waiting, e := short.SubmitExport(ctx, w.p, run.SnapshotID, "waiting")
		if e != nil || !w.job(waiting.ID).KeepUntil.Equal(read.ExpiresAt.Add(time.Minute)) {
			t.Fatal(waiting, e)
		}
		// No worker reaches it until five minutes after the read. Its snapshot is still served and its scope goes on, and the file's own
		// time is over: the job is denied before anything is rendered.
		w.until(read.ExpiresAt.Add(4 * time.Minute))
		rendered := w.rendered.Load()
		if ran, e := short.RunOne(ctx); !ran || !errors.Is(e, r.ErrDenied) {
			t.Fatal(ran, e)
		}
		if j := w.job(waiting.ID); j.State != "failed" || j.ErrorCode != r.ErrDenied.Error() || w.rendered.Load() != rendered {
			t.Fatalf("the export that waited too long: %s %s, %d files rendered", j.State, j.ErrorCode, w.rendered.Load()-rendered)
		}
		// Asked now, the same server admits nothing: the file would have been over for four minutes.
		_, e = short.SubmitExport(ctx, w.p, run.SnapshotID, "late")
		mustError(t, e, r.ErrDenied)
		if jobs, e := short.Jobs(ctx, w.p, r.JobListing{Limit: 10}); e != nil || len(jobs) != 2 {
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
		// A scope before its read is refused for itself, whatever the job keeps, and so is one in a zone no encoding can write.
		mustError(t, again(func(j *r.Job) {
			j.KeepUntil, j.Prepared.ScopeExpiresAt = time.Time{}, j.Prepared.ExpiresAt.Add(-time.Second)
		}), r.ErrInvalid)
		mustError(t, again(func(j *r.Job) {
			j.Prepared.ScopeExpiresAt = j.Prepared.ScopeExpiresAt.In(time.FixedZone("a day away", 24*3600))
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
	t.Run("a_store_fences_a_publication_by_the_jobs_own_deadline", func(t *testing.T) {
		w := newWorld(t)
		if !w.hours {
			t.Skip("the durable store's fence is its column against its clock, as before; what the column holds while a job can publish is checked where each kind of job is made")
		}
		w.vouch()
		s := w.svc()
		// claimedNear claims the oldest queued job ten seconds before a time, with a lease that outlasts it, and lets twenty seconds pass:
		// the claim still holds the job, and the time is past.
		claimedNear := func(at time.Time) r.Claim {
			t.Helper()
			w.pass(at.Add(-10 * time.Second).Sub(w.now()))
			c, e := w.repo.Claim(ctx, time.Minute)
			if e != nil || c == nil {
				t.Fatal(c, e)
			}
			w.pass(20 * time.Second)
			return *c
		}
		// A kept freeze needs the read itself: its snapshot would be kept for the hour, and its publication is refused once the read is
		// over. The publication is one the store would take (ValidatePublication): only the time refuses it.
		kept, e := s.SubmitKeptSnapshot(ctx, w.p, contractsim.FinancialQuery(), "kept")
		if e != nil {
			t.Fatal(e)
		}
		read := w.job(kept.ID).Prepared
		c := claimedNear(read.ExpiresAt)
		if c.Job.ID != kept.ID || !c.Job.KeepUntil.Equal(read.ExpiresAt.Add(lifetimeResults)) {
			t.Fatalf("%+v", c.Job)
		}
		pub := publication(c)
		pub.Snapshot.ExpiresAt = c.Job.KeepUntil
		if e := r.ValidatePublication(c.Job, pub); e != nil {
			t.Fatal(e)
		}
		mustError(t, w.repo.Publish(ctx, c, pub), r.ErrDenied)
		if e := w.repo.Fail(ctx, c, r.ErrDenied.Error()); e != nil {
			t.Fatal(e)
		}
		// An export needs its snapshot and the scope: it may publish after the read, until the time its file is kept until, which is
		// before the scope's end here; then its publication is refused too.
		run := w.keptFreeze(s, "run")
		queued, e := s.SubmitExport(ctx, w.p, run.SnapshotID, "export")
		if e != nil {
			t.Fatal(e)
		}
		until := w.job(queued.ID).KeepUntil
		if scope := w.job(queued.ID).Prepared.ScopeExpiresAt; until.IsZero() || !until.Before(scope.Add(-time.Hour)) {
			t.Fatalf("the export keeps until %s under a scope until %s", until, scope)
		}
		c = claimedNear(until)
		if c.Job.ID != queued.ID {
			t.Fatalf("%+v", c.Job)
		}
		q, p := c.Job.Query, c.Job.Prepared
		file := r.Publication{Artifact: &r.Artifact{ID: r.NewID(), Owner: c.Job.Owner, SnapshotID: c.Job.SnapshotID, ExpiresAt: until, SHA256: sha([]byte("x")), Content: []byte("x"), Query: &q, Prepared: &p}}
		if e := r.ValidatePublication(c.Job, file); e != nil {
			t.Fatal(e)
		}
		mustError(t, w.repo.Publish(ctx, c, file), r.ErrDenied)
	})
	// S25-B1, ADR 0026 decision 7: what is kept and what the stored-results quota counts are not the same thing. An export that is kept
	// holds its slot for as long as it is kept, beyond its read; the file of an issued document, kept for longer still, holds none.
	t.Run("a_kept_export_holds_its_slot_of_the_stored_results_and_an_issued_file_holds_none", func(t *testing.T) {
		w := newWorld(t)
		w.vouch()
		s := w.svcWith(r.Quotas{StoredResults: 2}, r.Retention{Results: lifetimeResults})
		submit := func(key string) error {
			_, e := s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), key)
			return e
		}
		snap := w.freeze(s, "snap")                  // one stored result
		is := w.issue(s, snap.SnapshotID, "issue")   // its file is kept until the scope ends, and is none
		ex := w.export(s, snap.SnapshotID, "export") // two: the limit
		refused(t, submit("over"), r.QuotaStoredResults)
		read := w.job(snap.ID).Prepared
		if file := w.artifact(is.ArtifactID); !file.ExpiresAt.After(read.ExpiresAt) || file.Issue != is.ID {
			t.Fatalf("the issued file is not kept beyond its read: %+v", file)
		}
		// The read ends. The working snapshot is over and its slot is free; the export is kept and still holds its own; the issued file
		// is kept too and never held one. So exactly one freeze is admitted, and the next is refused.
		w.until(read.ExpiresAt)
		w.freeze(s, "next")
		refused(t, submit("over again"), r.QuotaStoredResults)
		// What holds a slot and what holds none are both still served.
		for name, id := range map[string]string{"export": ex.ArtifactID, "issued file": is.ArtifactID} {
			if e := w.download(s, id); e != nil {
				t.Fatalf("the kept %s is not served: %v", name, e)
			}
		}
	})
	t.Run("an_export_the_store_cannot_read_keeps_nothing", func(t *testing.T) {
		w := newWorld(t)
		if w.unreadable == nil {
			t.Skip("the memory store knows a failed job's read (Job.ReduceAfter); only the durable store has to do without the payload")
		}
		s := w.svc()
		queued := func(j r.JobView, e error) string {
			t.Helper()
			if e != nil || j.State != "queued" {
				t.Fatal(j, e)
			}
			return j.ID
		}
		// Two snapshots: one of a read without a scope, one of a read whose scope lasts two hours.
		plain := w.freeze(s, "plain")
		plainRead := w.job(plain.ID).Prepared
		w.sim.SetScopeLifetime(2 * time.Hour)
		snap := w.freeze(s, "snap")
		read := w.job(snap.ID).Prepared
		// Three jobs that no worker has reached. Without a scope an export froze no time: its deadline is its read's expiry. With the
		// scope an export froze its file's time, an hour after its read: that is its deadline while it can publish, far beyond the longest
		// a read lasts. A kept freeze needs the read itself: its deadline is its read's expiry.
		a := queued(s.SubmitExport(ctx, w.p, plain.SnapshotID, "a"))
		b := queued(s.SubmitExport(ctx, w.p, snap.SnapshotID, "b"))
		c := queued(s.SubmitKeptSnapshot(ctx, w.p, contractsim.FinancialQuery(), "c"))
		keptRead := w.job(c).Prepared
		if j := w.job(b); !j.KeepUntil.Equal(read.ExpiresAt.Add(lifetimeResults)) {
			t.Fatalf("the export keeps until %s, want an hour after %s", j.KeepUntil, read.ExpiresAt)
		}
		w.deadlineIs(a, plainRead.ExpiresAt)
		w.deadlineIs(b, read.ExpiresAt.Add(lifetimeResults))
		w.deadlineIs(c, keptRead.ExpiresAt)
		for _, id := range []string{a, b, c} {
			w.unreadable(id)
		}
		// The worker's claim fails all three, and finds nothing to run.
		if ran, e := s.RunOne(ctx); ran || e != nil {
			t.Fatal(ran, e)
		}
		for _, id := range []string{a, b, c} {
			if v := w.stored(id); v.State != "failed" || v.Code != r.ErrIntegrity.Error() {
				t.Fatalf("job %s: %+v", id, v)
			}
		}
		// Nothing of a payload that cannot be read is trusted, so the store cannot bring the export's deadline back to its read's expiry
		// as Fail does. It brings it back to the latest the read can have expired: the longest a read lasts, after the job's admission.
		// A job whose deadline was its read's expiry already keeps it, to the microsecond.
		if v := w.stored(b); !v.Deadline.Equal(v.Created.Add(r.MaxReadLifetime)) {
			t.Fatalf("the deadline of the export that cannot be read is %s, admitted %s", v.Deadline.UTC(), v.Created.UTC())
		}
		w.deadlineIs(a, plainRead.ExpiresAt)
		w.deadlineIs(c, keptRead.ExpiresAt)
	})
}

// alteredStore is a store that serves a snapshot or an artifact other than the one it holds: what a store that does not agree with itself
// is to the service. What it holds is untouched; the change is made to what a read of it returns.
type alteredStore struct {
	r.Repository
	snapshot func(*r.Snapshot)
	artifact func(*r.Artifact)
}

func (s *alteredStore) Snapshot(ctx context.Context, i r.Identity, id string) (r.Snapshot, error) {
	v, e := s.Repository.Snapshot(ctx, i, id)
	if e == nil && s.snapshot != nil {
		s.snapshot(&v)
	}
	return v, e
}
func (s *alteredStore) Artifact(ctx context.Context, i r.Identity, id string) (r.Artifact, error) {
	v, e := s.Repository.Artifact(ctx, i, id)
	if e == nil && s.artifact != nil {
		s.artifact(&v)
	}
	return v, e
}

// newAlteredMemory is the memory world over a store whose reads a test can alter.
func newAlteredMemory(t *testing.T) (*lifetimeWorld, *alteredStore) {
	t.Helper()
	w := newLifetimeMemory(t)
	store := &alteredStore{Repository: w.repo}
	w.repo = store
	return w, store
}

// A file kept beyond its read is authorized, once its snapshot is gone, by the query and the read its job froze and by nothing else
// (ADR 0026 decision 5), so it is made only from the snapshot of that read. A store whose row of the snapshot says another read does not
// agree with itself: the export and the issue fail, nothing is rendered, and the product is not asked for a document.
func TestAKeptFileIsMadeOnlyFromTheSnapshotOfTheReadItsJobFroze(t *testing.T) {
	ctx := context.Background()
	w, store := newAlteredMemory(t)
	w.vouch()
	s := w.svc()
	first, second := w.freeze(s, "first"), w.freeze(s, "second")
	export, e := s.SubmitExport(ctx, w.p, first.SnapshotID, "export")
	if e != nil {
		t.Fatal(e)
	}
	issue, e := s.SubmitIssue(ctx, w.p, issueInput(first.SnapshotID, 0), "issue")
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{export.ID, issue.ID} {
		if j := w.job(id); j.KeepUntil.IsZero() || j.Prepared.Ref != w.job(first.ID).Prepared.Ref {
			t.Fatalf("the job does not keep its file by the first read: %+v", j)
		}
	}
	// From here on the store serves the second freeze, its rows and its read, under the first one's id. The second snapshot is whole and
	// its owner's own, so it passes every check a snapshot passes by itself.
	other, e := store.Repository.Snapshot(ctx, w.p.Identity, second.SnapshotID)
	if e != nil {
		t.Fatal(e)
	}
	store.snapshot = func(v *r.Snapshot) {
		if v.ID == first.SnapshotID {
			*v = other
			v.ID = first.SnapshotID
		}
	}
	rendered := w.rendered.Load()
	for _, id := range []string{export.ID, issue.ID} {
		if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, r.ErrIntegrity) {
			t.Fatal(ran, e)
		}
		if j := w.job(id); j.State != "failed" || j.ErrorCode != r.ErrIntegrity.Error() || j.ArtifactID != "" {
			t.Fatalf("the %s job of a snapshot that is another read's: %+v", j.Kind, j)
		}
	}
	if n := w.rendered.Load() - rendered; n != 0 {
		t.Fatalf("%d files were rendered from a snapshot that is another read's", n)
	}
	if _, asked := w.sim.IssueRecord(issue.ID); asked {
		t.Fatal("the product was asked for a document of a snapshot that is another read's")
	}
}

// A kept file is served by its own basis, and only when its row agrees with itself (ADR 0026 decision 5): both halves of the basis, the
// owner's, a time of its own that is after the read's and within the read's scope, and a read that is the query's. A row that says
// otherwise is refused whole, before the source is asked. A kept file whose basis is gone altogether falls under the rule of the files
// that have none, never later than its snapshot, and is refused by it.
func TestAKeptFileWhoseRowDoesNotAgreeWithItsBasisIsNotServed(t *testing.T) {
	w, store := newAlteredMemory(t)
	w.vouch()
	s := w.svc()
	snap := w.freeze(s, "snap")
	ex := w.export(s, snap.SnapshotID, "export")
	if e := w.download(s, ex.ArtifactID); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*r.Artifact){
		"a query without a read":         func(a *r.Artifact) { a.Prepared = nil },
		"a read without a query":         func(a *r.Artifact) { a.Query = nil },
		"the basis of another owner":     func(a *r.Artifact) { p := *a.Prepared; p.Context.Owner.Subject += "-another"; a.Prepared = &p },
		"a time that is the read's own":  func(a *r.Artifact) { a.ExpiresAt = a.Prepared.ExpiresAt },
		"a time beyond the read's scope": func(a *r.Artifact) { a.ExpiresAt = a.Prepared.ScopeExpiresAt.Add(time.Nanosecond) },
		"a read that is not the query's": func(a *r.Artifact) { q := *a.Query; q.AsOf = "2001-01-01"; a.Query = &q },
		"a scope that ends before its read": func(a *r.Artifact) {
			p := *a.Prepared
			p.ScopeExpiresAt = p.ExpiresAt.Add(-time.Second)
			a.Prepared = &p
		},
	} {
		store.artifact = change
		asked := w.sim.asked.Load()
		if e := w.download(s, ex.ArtifactID); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("%s: the download answered %v", name, e)
		}
		if n := w.sim.asked.Load() - asked; n != 0 {
			t.Errorf("%s: the source was asked %d times", name, n)
		}
	}
	store.artifact = func(a *r.Artifact) { a.Query, a.Prepared = nil, nil }
	if e := w.download(s, ex.ArtifactID); !errors.Is(e, r.ErrIntegrity) {
		t.Error("a kept file without its basis, later than its snapshot: the download answered", e)
	}
	store.artifact = nil
	if e := w.download(s, ex.ArtifactID); e != nil {
		t.Fatal("the file as the store holds it:", e)
	}
}

// Without a scope a file takes its snapshot's expiry, as it always did, and the store holds it to the job's read: a snapshot whose expiry
// is not its read's (a row changed outside the platform) is not exported from. The file's expiry is the job's own only where the job
// keeps the file beyond the read (ADR 0026), so this is the refusal it was before.
func TestAFileOfASnapshotWhoseExpiryIsNotItsReadsIsRefusedAsBefore(t *testing.T) {
	ctx := context.Background()
	w, store := newAlteredMemory(t)
	s := w.svc()
	snap := w.freeze(s, "snap")
	store.snapshot = func(v *r.Snapshot) { v.ExpiresAt = v.ExpiresAt.Add(-time.Minute) }
	export, e := s.SubmitExport(ctx, w.p, snap.SnapshotID, "export")
	if e != nil {
		t.Fatal(e)
	}
	if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, r.ErrIntegrity) {
		t.Fatal(ran, e)
	}
	if j := w.job(export.ID); j.State != "failed" || j.ErrorCode != r.ErrIntegrity.Error() || j.ArtifactID != "" || !j.KeepUntil.IsZero() {
		t.Fatalf("%+v", j)
	}
}

// A snapshot job and an issue job need the read itself, also where the source goes on vouching for the read's scope (ADR 0026 decision
// 6). Once the read is over, a job of either kind that no worker has reached is denied, and so is its poll; no page is read and the
// product is not asked for a document, although the snapshot the issue is for is still kept and its scope still answers.
func TestAFreezeAndAnIssueNeedTheReadItselfAlsoUnderAScope(t *testing.T) {
	ctx := context.Background()
	w := newLifetimeMemory(t)
	w.vouch()
	s := w.svc()
	run := w.keptFreeze(s, "run")
	queued := func(j r.JobView, e error) string {
		t.Helper()
		if e != nil || j.State != "queued" {
			t.Fatal(j, e)
		}
		return j.ID
	}
	jobs := []string{
		queued(s.SubmitIssue(ctx, w.p, issueInput(run.SnapshotID, 0), "issue")),
		queued(s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "freeze")),
		queued(s.SubmitKeptSnapshot(ctx, w.p, contractsim.FinancialQuery(), "kept")),
	}
	last := w.job(jobs[2]).Prepared
	w.until(last.ExpiresAt)
	// The snapshot is kept and its scope answers: its rows are served. What the three jobs lack is the read.
	if _, e := s.Rows(ctx, w.p, run.SnapshotID, 10, ""); e != nil {
		t.Fatal("the kept snapshot's rows after its read:", e)
	}
	pages, documents := w.sim.ReadCalls(), w.sim.documents.Load()
	for _, id := range jobs {
		if _, e := s.Job(ctx, w.p, id); !errors.Is(e, r.ErrDenied) {
			t.Fatalf("the poll of a queued %s job whose read is over: %v", w.job(id).Kind, e)
		}
	}
	for _, id := range jobs {
		if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, r.ErrDenied) {
			t.Fatal(ran, e)
		}
		if j := w.job(id); j.State != "failed" || j.ErrorCode != r.ErrDenied.Error() || j.ArtifactID != "" {
			t.Fatalf("%+v", j)
		}
	}
	if n := w.sim.ReadCalls() - pages; n != 0 {
		t.Fatalf("%d pages were read after the read", n)
	}
	if n := w.sim.documents.Load() - documents; n != 0 {
		t.Fatalf("the product was asked for %d documents after the read", n)
	}
}

// A kept file's object outlives the sweep that takes its snapshot, is read back for every download, and goes with its row at the file's
// own time (S04-E, ADR 0026): with an object store, too, the file stands without its snapshot.
func TestAKeptFilesObjectOutlivesItsSnapshotAndGoesWithItsRow(t *testing.T) {
	ctx := context.Background()
	const grace = time.Second
	w := newLifetimeMemory(t)
	bucket := objects.NewMemory(w.clock)
	w.objects = bucket
	w.repo = memory.New(w.clock).UseObjects(bucket)
	w.vouch()
	s := w.svc()
	keys := func() int {
		t.Helper()
		page, next, e := bucket.List(ctx, r.ObjectPrefix, "", 1000)
		if e != nil || next != "" {
			t.Fatal(next, e)
		}
		return len(page)
	}
	sweep := func() r.SweepResult {
		t.Helper()
		res, e := s.Sweep(ctx, r.SweepOptions{Grace: grace})
		if e != nil {
			t.Fatal(e)
		}
		return res
	}
	snap := w.freeze(s, "snap")
	ex := w.export(s, snap.SnapshotID, "export")
	job := w.job(ex.ID)
	if a := w.artifact(ex.ArtifactID); a.Object != r.ObjectKey(a.ID) || len(a.Content) != 0 || a.Query == nil || a.Prepared == nil || !a.ExpiresAt.Equal(job.KeepUntil) || keys() != 1 {
		t.Fatalf("the kept file's row: %+v, %d objects", a, keys())
	}
	// The sweep after the read takes the working snapshot and neither the file's row nor its object; the file is read back from the
	// object, by the source's answer.
	w.until(job.Prepared.ExpiresAt.Add(grace))
	if res := sweep(); res.Snapshots != 1 || res.Artifacts != 0 || res.Objects != 0 || keys() != 1 {
		t.Fatalf("the sweep after the read: %+v, %d objects", res, keys())
	}
	w.refusedByTheSource("the kept file in the object store", func() error { return w.download(s, ex.ArtifactID) })
	// At the file's own time the sweep takes the row and then the object.
	w.until(job.KeepUntil.Add(grace))
	if res := sweep(); res.Artifacts != 1 || res.Objects != 1 || res.ObjectsLeft != 0 || keys() != 0 {
		t.Fatalf("the sweep after the file's time: %+v, %d objects", res, keys())
	}
	mustError(t, w.download(s, ex.ArtifactID), r.ErrNotFound)
}

// crossing is a service's clock that passes a time between two looks: once armed, the first look answers the instant before the time
// and every later one the time itself; unarmed it is the world's clock.
type crossing struct {
	world func() time.Time
	mu    sync.Mutex
	armed bool
	at    time.Time
	looks int
}

func (c *crossing) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.armed {
		return c.world()
	}
	if c.looks++; c.looks == 1 {
		return c.at.Add(-time.Nanosecond)
	}
	return c.at
}
func (c *crossing) arm(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed, c.at, c.looks = true, at, 0
}

// A poll looks at the clock more than once, and the instant a result ends may lie between two looks: the first says that the result
// lasts, a later one refuses it as a result that has ended. Under a source that vouches for the read's scope such a poll answers what
// a poll a moment later answers, the job with its result over (S25-B2, found when a server test met the instant on a real clock):
// before, a working freeze polled at its read's expiry answered an integrity failure of the store, and a result kept to the end of
// its scope a refusal. Under a source that vouches for nothing the poll answers what it always did.
func TestAPollAtTheInstantAResultEndsSaysThatItIsOver(t *testing.T) {
	ctx := context.Background()
	// The service that is polled: its clock crosses the armed time between its first look and its second.
	polled := func(w *lifetimeWorld, keep r.Retention) (*r.Service, *crossing) {
		c := &crossing{world: w.clock}
		world := w.clock
		w.clock = c.now
		s := w.svcWith(r.Quotas{}, keep)
		w.clock = world
		return s, c
	}
	over := func(name string, s *r.Service, w *lifetimeWorld, id string) {
		t.Helper()
		v, e := s.Job(ctx, w.p, id)
		if e != nil || !v.Expired || v.State != "succeeded" || v.ID != id {
			t.Fatalf("%s: %+v %v", name, v, e)
		}
	}

	t.Run("a working freeze at its read's expiry", func(t *testing.T) {
		w := newLifetimeMemory(t)
		w.vouch()
		s, clock := polled(w, r.Retention{Results: lifetimeResults})
		snap := w.freeze(s, "snap")
		if v, e := s.Job(ctx, w.p, snap.ID); e != nil || v.Expired {
			t.Fatal(v, e)
		}
		asked := w.sim.asked.Load()
		clock.arm(w.job(snap.ID).Prepared.ExpiresAt)
		over("the freeze", s, w, snap.ID)
		// The source was asked, by the scope, and said yes: what refused was the snapshot's own time.
		if n := w.sim.asked.Load() - asked; n != 1 {
			t.Fatalf("the source was asked %d times", n)
		}
	})
	t.Run("a kept export at its own time", func(t *testing.T) {
		w := newLifetimeMemory(t)
		w.vouch()
		s, clock := polled(w, r.Retention{Results: lifetimeResults})
		snap := w.freeze(s, "snap")
		ex := w.export(s, snap.SnapshotID, "export")
		job := w.job(ex.ID)
		if !job.KeepUntil.Before(job.Prepared.ScopeExpiresAt) {
			t.Fatal("the export's time is not within its scope: the next subtest is this one")
		}
		clock.arm(job.KeepUntil)
		over("the export", s, w, ex.ID)
	})
	t.Run("a kept export whose time is the end of its scope", func(t *testing.T) {
		w := newLifetimeMemory(t)
		w.vouch()
		s, clock := polled(w, r.Retention{Results: 24 * time.Hour}) // longer than the world's scope
		snap := w.freeze(s, "snap")
		ex := w.export(s, snap.SnapshotID, "export")
		job := w.job(ex.ID)
		if !job.KeepUntil.Equal(job.Prepared.ScopeExpiresAt) {
			t.Fatalf("the export is kept until %s, its scope ends at %s", job.KeepUntil, job.Prepared.ScopeExpiresAt)
		}
		asked := w.sim.asked.Load()
		clock.arm(job.KeepUntil)
		over("the export", s, w, ex.ID)
		// After the scope the source is not asked.
		if n := w.sim.asked.Load() - asked; n != 0 {
			t.Fatalf("the source was asked %d times at the end of the scope", n)
		}
	})
	t.Run("a source that vouches for nothing is answered as before", func(t *testing.T) {
		w := newLifetimeMemory(t)
		s, clock := polled(w, r.Retention{Results: lifetimeResults})
		snap := w.freeze(s, "snap")
		clock.arm(w.job(snap.ID).Prepared.ExpiresAt)
		if v, e := s.Job(ctx, w.p, snap.ID); !errors.Is(e, r.ErrDenied) {
			t.Fatalf("the poll at the read's expiry: %+v %v", v, e)
		}
		// A moment later, with every look past the time, it is the job with its result over, as it always was.
		clock.arm(w.job(snap.ID).Prepared.ExpiresAt.Add(time.Nanosecond))
		over("the freeze a moment later", s, w, snap.ID)
	})
	// A job that is not finished has no result to be over: its poll after the read is denied under a scope too, which
	// TestAFreezeAndAnIssueNeedTheReadItselfAlsoUnderAScope holds.
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
// years a product's time may name, or a time that cannot be encoded as it is (a year after 9999 in its own zone, a zone a day from UTC).
// Nothing is admitted and nothing is read.
func TestAScopeThatIsNotWellFormedIsAnIntegrityFailure(t *testing.T) {
	ctx := context.Background()
	east := time.FixedZone("east", 8*3600)
	for name, scope := range map[string]func(time.Time) time.Time{
		"a scope that ends before its read":                    func(read time.Time) time.Time { return read.Add(-time.Second) },
		"a scope beyond the years a product may name":          func(time.Time) time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"a scope whose year cannot be encoded in its own zone": func(time.Time) time.Time { return time.Date(9999, 12, 31, 20, 0, 0, 0, time.UTC).In(east) },
		"a scope in a zone no encoding can write":              func(read time.Time) time.Time { return read.Add(time.Hour).In(time.FixedZone("a day away", 24*3600)) },
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
