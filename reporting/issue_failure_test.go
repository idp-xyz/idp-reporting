package reporting_test

// How a failed issue job is recorded and voided (S03-H, the order of docs/API.md): the failure is
// recorded first, fenced by the claim, as ISSUE_UNRECONCILED; only then is the product told to void;
// when that succeeded the job's own code replaces the unreconciled one. A panic anywhere in the
// execution is a failure with the code EXECUTION_FAILED, not the end of the worker.

import (
	"context"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"runtime"
	"strings"
	"testing"
	"time"
)

// runInGoroutine runs the worker once in a goroutine of its own and waits for it to end, which a
// worker that dies (runtime.Goexit stands for a crash) does without returning: ok is false then.
func runInGoroutine(v *issueEnv) (ok bool, e error) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, e = v.s.RunOne(context.Background())
		ok = true
	}()
	<-done
	return ok, e
}

// The failure is recorded before the void is asked for: when the product is told to void, the job is
// already failed as ISSUE_UNRECONCILED, at its generation, and no other worker can claim it. Once the
// product has voided, the job's own code replaces it. A failure that needs no void is failed once, with
// its own code, and resolves nothing.
func TestIssueFailureIsRecordedBeforeTheVoidAndResolvedAfter(t *testing.T) {
	v := newIssueEnv(t, envOpts{})
	v.issuer.err = r.ErrUnpublished
	j := v.submit(1, "k")
	var atVoid r.Job
	var claimable bool
	v.finance.voidHook = func() {
		v.finance.voidHook = nil // once: a worker that claims the job meanwhile must not recurse into this
		atVoid = v.stored(j.ID)
		v.clock.Advance(r.LeaseDuration + time.Second) // even when the lease runs out meanwhile
		ran, e := v.run()
		claimable = ran || e != nil
	}
	if _, e := v.run(); !errors.Is(e, r.ErrUnpublished) || errors.Is(e, r.ErrUnreconciled) {
		t.Fatal(e)
	}
	if atVoid.State != "failed" || atVoid.ErrorCode != "ISSUE_UNRECONCILED" || atVoid.Generation != 1 {
		t.Fatalf("when the product was told to void, the job was %s with the code %q, generation %d; want failed, ISSUE_UNRECONCILED, generation 1", atVoid.State, atVoid.ErrorCode, atVoid.Generation)
	}
	if claimable {
		t.Fatal("another worker could claim the job while its void was being delivered")
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "TEMPLATE_NOT_PUBLISHED" || st.Generation != 1 {
		t.Fatalf("the void succeeded and the job is %+v; want failed with its own code", st)
	}
	if len(v.store.fails) != 1 || v.store.fails[0] != "ISSUE_UNRECONCILED" || len(v.store.resolves) != 1 || v.store.resolves[0] != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatalf("failed with %v, resolved to %v", v.store.fails, v.store.resolves)
	}
	assertVoided(t, v, j.ID, "TEMPLATE_NOT_PUBLISHED")
	// A job that was refused before the product was asked is failed once with its own code: nothing to
	// void, nothing to resolve.
	w := newIssueEnv(t, envOpts{})
	w.issuer.checkErr = r.ErrUnpublished
	k := w.submit(1, "k")
	if _, e := w.run(); !errors.Is(e, r.ErrUnpublished) {
		t.Fatal(e)
	}
	if st := w.stored(k.ID); st.State != "failed" || st.ErrorCode != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatalf("%+v", st)
	}
	if len(w.store.fails) != 1 || w.store.fails[0] != "TEMPLATE_NOT_PUBLISHED" || len(w.store.resolves) != 0 || len(w.finance.voids) != 0 {
		t.Fatalf("failed with %v, resolved to %v, voids %v", w.store.fails, w.store.resolves, w.finance.voids)
	}
}

// The reviewer's scenario. Worker 1 asks the product to confirm the file, which it does, but the answer
// is lost. Its failure is recorded, and its void sent, only after the lease has run out (it was slow):
// worker 2 claims the job, has the same reservation answered again, renders the same bytes, has them
// confirmed again and publishes. Worker 1's failure is fenced by the lease and does not take, so no
// void is sent for the number that was published.
func TestIssueFailureDelayedPastTheLeaseNeverVoidsAPublishedNumber(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	j := v.submit(1, "k")
	v.finance.confirmL = r.ErrUnavailable
	v.store.failHook = func() {
		v.clock.Advance(r.LeaseDuration + time.Second)
		v.store.failHook = nil
	}
	if ran, e := v.run(); !ran || !errors.Is(e, r.ErrUnavailable) || errors.Is(e, r.ErrUnreconciled) {
		t.Fatal(ran, e)
	}
	if len(v.finance.voids) != 0 {
		t.Fatalf("a failure that was not recorded was voided: %+v", v.finance.voids)
	}
	if st := v.stored(j.ID); st.State != "running" || st.Generation != 1 || st.ErrorCode != "" {
		t.Fatalf("the job after worker 1: %+v; want it left to its lease", st)
	}
	// Worker 2.
	v.finance.confirmL = nil
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	st := v.stored(j.ID)
	if st.State != "succeeded" || st.Generation != 2 {
		t.Fatalf("%+v", st)
	}
	got := v.job(j.ID)
	if got.Issue == nil || got.Issue.Number != "AR-000001" {
		t.Fatalf("%+v", got)
	}
	if len(v.finance.voids) != 0 {
		t.Fatalf("a published number was voided: %+v", v.finance.voids)
	}
	rec, held := held(v, j.ID)
	a, e := v.s.Download(ctx, v.p, st.ArtifactID)
	if e != nil || !held || rec.State != contractsim.IssueConfirmed || rec.Number != "AR-000001" || rec.SHA256 != a.SHA256 {
		t.Fatalf("the product's record %+v (held %v), the file %v", rec, held, e)
	}
}

// The void itself held up while the lease runs out: the job is failed (and so claimed by nobody),
// worker 2 finds nothing to take, the void is delivered at last and the job ends with its own code and
// no record or artifact.
func TestIssueVoidHeldPastTheLeaseFindsTheJobFailedAndUnclaimable(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	v.finance.confirmL = r.ErrUnavailable
	j := v.submit(1, "k")
	var ran2 bool
	var e2 error
	v.finance.voidHook = func() {
		v.finance.voidHook = nil // once
		v.clock.Advance(r.LeaseDuration + time.Second)
		v.finance.confirmL = nil
		ran2, e2 = v.run() // worker 2
	}
	if _, e := v.run(); !errors.Is(e, r.ErrUnavailable) {
		t.Fatal(e)
	}
	if ran2 || e2 != nil {
		t.Fatalf("worker 2 claimed the job (%v, %v) while its void was held", ran2, e2)
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "AUTHORITY_UNAVAILABLE" || st.Generation != 1 || st.ArtifactID != "" {
		t.Fatalf("%+v", st)
	}
	if _, e := v.repo.Issue(ctx, v.p.Identity, j.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("a record exists for a failed job", e)
	}
	if len(v.finance.voids) != 1 {
		t.Fatalf("voids: %+v", v.finance.voids)
	}
	if rec := assertVoided(t, v, j.ID, "AUTHORITY_UNAVAILABLE"); rec.SHA256 == "" {
		t.Fatalf("the product had confirmed the file: %+v", rec)
	}
}

// A crash between the two steps (the process is gone once the failure is recorded) leaves the job
// ISSUE_UNRECONCILED, which is true: the product holds a reservation. Nobody claims the job again.
func TestIssueCrashBetweenRecordingAndVoidingLeavesTheJobUnreconciled(t *testing.T) {
	v := newIssueEnv(t, envOpts{})
	v.issuer.err = r.ErrUnpublished
	j := v.submit(0, "k")
	v.finance.voidHook = runtime.Goexit // the worker vanishes as the void is about to be delivered
	if ok, _ := runInGoroutine(v); ok {
		t.Fatal("the worker survived the crash")
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "ISSUE_UNRECONCILED" || st.Generation != 1 {
		t.Fatalf("%+v; want failed, ISSUE_UNRECONCILED", st)
	}
	if rec, ok := held(v, j.ID); !ok || rec.State != contractsim.IssueReserved || rec.Number != "AR-000001" {
		t.Fatalf("the reservation the product keeps for the job: %+v %v", rec, ok)
	}
	v.clock.Advance(r.LeaseDuration + time.Second)
	v.finance.voidHook = nil
	if ran, e := v.run(); ran || e != nil {
		t.Fatalf("a failed job was claimed again: %v %v", ran, e)
	}
	if got := v.job(j.ID); got.State != "failed" || got.ErrorCode != "ISSUE_UNRECONCILED" {
		t.Fatalf("%+v", got)
	}
}

// A void that fails, by an error or by a panic of the adapter, leaves the job ISSUE_UNRECONCILED and
// resolves nothing; so does a void that succeeded but could not be recorded (the store went away).
func TestIssueFailedVoidLeavesTheJobUnreconciledAndResolvesNothing(t *testing.T) {
	for name, c := range map[string]struct {
		setup   func(v *issueEnv)
		causes  []error
		voided  bool // the product voided the job
		resolve bool // ResolveFailure was called
	}{
		"the product refuses the void":    {setup: func(v *issueEnv) { v.finance.voidE = r.ErrUnavailable }, causes: []error{r.ErrUnreconciled, r.ErrUnpublished, r.ErrUnavailable}},
		"the adapter panics in the void":  {setup: func(v *issueEnv) { v.finance.voidPn = true }, causes: []error{r.ErrUnreconciled, r.ErrUnpublished}},
		"the store cannot record the end": {setup: func(v *issueEnv) { v.store.resolveE = errors.New("the database is down") }, causes: []error{r.ErrUnreconciled, r.ErrUnpublished}, voided: true, resolve: true},
	} {
		t.Run(name, func(t *testing.T) {
			v := newIssueEnv(t, envOpts{})
			v.issuer.err = r.ErrUnpublished
			c.setup(v)
			j := v.submit(0, "k")
			ran, e := v.run()
			if !ran || e == nil {
				t.Fatal(ran, e)
			}
			for _, want := range c.causes {
				if !errors.Is(e, want) {
					t.Errorf("the worker's error %v does not name %v", e, want)
				}
			}
			if strings.Contains(e.Error(), "secret-row-value") {
				t.Errorf("the error carries the panic's value: %v", e)
			}
			if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "ISSUE_UNRECONCILED" || st.Generation != 1 {
				t.Fatalf("%+v", st)
			}
			if len(v.store.fails) != 1 || v.store.fails[0] != "ISSUE_UNRECONCILED" || (len(v.store.resolves) > 0) != c.resolve {
				t.Fatalf("failed with %v, resolved to %v", v.store.fails, v.store.resolves)
			}
			rec, ok := held(v, j.ID)
			if (ok && rec.State == contractsim.IssueVoid) != c.voided {
				t.Fatalf("the product's record: %+v %v; want voided %v", rec, ok, c.voided)
			}
			// Nobody claims a failed job: the product keeps what it holds, found by the job id.
			v.clock.Advance(r.LeaseDuration + time.Second)
			if ran, e := v.run(); ran || e != nil {
				t.Fatal("a failed job was claimed again", ran, e)
			}
		})
	}
}

// When the failure cannot be recorded, nothing is voided: the lease decides. The job stays running, its
// lease runs out, and the next claim (a later generation) voids it, with its own failure.
func TestIssueFailureThatCannotBeRecordedIsLeftToTheLeaseAndTheNextClaimVoids(t *testing.T) {
	v := newIssueEnv(t, envOpts{})
	v.issuer.err = r.ErrUnpublished
	j := v.submit(0, "k")
	v.store.failE = errors.New("the database is down")
	if ran, e := v.run(); !ran || !errors.Is(e, r.ErrUnpublished) {
		t.Fatal(ran, e)
	}
	if len(v.finance.voids) != 0 {
		t.Fatalf("a failure that was not recorded was voided: %+v", v.finance.voids)
	}
	if st := v.stored(j.ID); st.State != "running" || st.Generation != 1 {
		t.Fatalf("%+v", st)
	}
	v.store.failE = nil
	v.clock.Advance(r.LeaseDuration + time.Second)
	if ran, e := v.run(); !ran || !errors.Is(e, r.ErrUnpublished) {
		t.Fatal(ran, e)
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "TEMPLATE_NOT_PUBLISHED" || st.Generation != 2 {
		t.Fatalf("%+v", st)
	}
	if len(v.finance.voids) != 1 || v.finance.voids[0] != (voidCall{j.ID, "TEMPLATE_NOT_PUBLISHED"}) {
		t.Fatalf("voids: %+v", v.finance.voids)
	}
	assertVoided(t, v, j.ID, "TEMPLATE_NOT_PUBLISHED")
}

// T-1(a): the worker's own guard. A stored issue job whose row is beyond its snapshot (a hand-made
// job with its request hash recomputed, which admission through the service would never make) fails
// INTEGRITY_FAILURE and the product is never called.
func TestIssueJobWithARowBeyondItsSnapshotFailsWithoutCallingTheProduct(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	good := v.stored(v.submit(0, "good").ID)
	crafted := good
	crafted.ID, crafted.Key, crafted.RowIndex = r.NewID(), "crafted", 3 // the snapshot has rows 0..2
	crafted.RequestHash = r.IssueRequestHash(crafted.TemplateID, crafted.TemplateVersion, crafted.TemplateSHA256, crafted.SnapshotID, crafted.RowIndex)
	if e := r.ValidateAdmission(crafted); e != nil {
		t.Fatalf("the crafted job is not a valid admission: %v", e)
	}
	if _, e := v.repo.Admit(ctx, crafted); e != nil {
		t.Fatal(e)
	}
	// Run the good job first (it is older), then the crafted one.
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	calls := len(v.finance.intents)
	if ran, e := v.run(); !ran || !errors.Is(e, r.ErrIntegrity) {
		t.Fatal(ran, e)
	}
	if st := v.stored(crafted.ID); st.State != "failed" || st.ErrorCode != "INTEGRITY_FAILURE" || st.Generation != 1 {
		t.Fatalf("%+v", st)
	}
	if len(v.finance.intents) != calls || len(v.finance.voids) != 0 || v.issuer.checks != 1 {
		t.Fatalf("the product was asked about a row that does not exist: intents %d (was %d), voids %+v, checks %d", len(v.finance.intents), calls, v.finance.voids, v.issuer.checks)
	}
	if _, ok := held(v, crafted.ID); ok {
		t.Fatal("the product holds something for the crafted job")
	}
}

// T-1(b): a panic in a job's execution, in the issuer or in an adapter, is a failure with the code
// EXECUTION_FAILED and the worker lives on. It carries no value. The void rule applies as for any error:
// a panic after the product was asked is voided; one before it calls nothing.
func TestIssueExecutionPanicIsAFailureNotTheEndOfTheWorker(t *testing.T) {
	for name, c := range map[string]struct {
		setup  func(v *issueEnv)
		reason bool // the product was asked, so it is voided
	}{
		"the renderer panics":          {setup: func(v *issueEnv) { v.issuer.panics = true }, reason: true},
		"the adapter panics preparing": {setup: func(v *issueEnv) { v.finance.preparePn = true }, reason: true},
		"the issuer's check panics":    {setup: func(v *issueEnv) { v.issuer.checkPans = true }},
	} {
		t.Run(name, func(t *testing.T) {
			v := newIssueEnv(t, envOpts{})
			c.setup(v)
			j := v.submit(1, "k")
			ran, e := v.run() // a panic that escaped would fail the test here
			if !ran || e == nil || errors.Is(e, r.ErrUnreconciled) || strings.Contains(e.Error(), "secret-row-value") {
				t.Fatal(ran, e)
			}
			if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "EXECUTION_FAILED" || st.Generation != 1 {
				t.Fatalf("%+v", st)
			}
			if c.reason {
				if len(v.finance.voids) != 1 || v.finance.voids[0] != (voidCall{j.ID, "EXECUTION_FAILED"}) {
					t.Fatalf("voids: %+v", v.finance.voids)
				}
				assertVoided(t, v, j.ID, "EXECUTION_FAILED")
			} else if len(v.finance.intents) != 0 || len(v.finance.voids) != 0 {
				t.Fatalf("a panic before the product was asked called it: intents %d, voids %+v", len(v.finance.intents), v.finance.voids)
			}
			// The worker is fine: the next job is issued.
			v.finance.preparePn, v.issuer.panics, v.issuer.checkPans = false, false, false
			next := v.submit(2, "next")
			if ran, e := v.run(); !ran || e != nil {
				t.Fatal(ran, e)
			}
			if got := v.job(next.ID); got.State != "succeeded" {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// panickingReads is an adapter whose reads panic: the snapshot job it serves fails, the worker lives.
type panickingReads struct{ r.Adapter }

func (panickingReads) ReadPage(context.Context, r.SourceContext, r.PreparedRead, string, int) (r.SourcePage, error) {
	panic("the adapter broke: secret-row-value")
}

func TestExecutionPanicInAnAdapterFailsAnySnapshotJob(t *testing.T) {
	v := newIssueEnv(t, envOpts{})
	// The same service over the same store, with the finance adapter reading through the panicking one.
	s, e := r.NewService(r.Config{Repository: v.store, Connections: []r.Connection{{Tenant: v.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: panickingReads{v.finance}}}, CursorKey: []byte("01234567890123456789012345678901"), Now: v.clock.Now})
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.SubmitSnapshot(context.Background(), v.p, contractsim.FinancialQuery(), "panicking")
	if e != nil {
		t.Fatal(e)
	}
	ran, e := s.RunOne(context.Background())
	if !ran || e == nil || strings.Contains(e.Error(), "secret-row-value") {
		t.Fatal(ran, e)
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "EXECUTION_FAILED" {
		t.Fatalf("%+v", st)
	}
	if len(v.store.fails) != 1 || v.store.fails[0] != "EXECUTION_FAILED" || len(v.store.resolves) != 0 {
		t.Fatalf("failed with %v, resolved to %v", v.store.fails, v.store.resolves)
	}
}
