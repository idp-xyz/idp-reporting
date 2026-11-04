package reporting_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// The queries in flight of an identity (S04-G, docs/S04G_SCOPE.md G3): POST /queries and the row reads share one per-identity bound in the
// service. These tests hold reads inside the source adapter, so that "in flight" is observed, not assumed.

// holdAdapter wraps the contract simulator and holds, for the identity named in who, every PrepareRead (a query) or every AuthorizeCurrent
// (the check a row read makes of the snapshot's source) until the test lets it go, the context ends, or it fails or panics as told.
type holdAdapter struct {
	*contractsim.Simulator
	who      string // the subject whose calls are held; others pass
	hold     atomic.Int32
	entered  chan struct{}
	release  chan struct{}
	failWith error
	panicOn  bool
	prepares atomic.Int32 // PrepareRead calls that reached the adapter: a refused read must not add one
}

const (
	holdNone      = 0
	holdPrepare   = 1
	holdAuthorize = 2
)

func (a *holdAdapter) wait(ctx context.Context, o r.Owner, which int32) error {
	if o.Subject != a.who || a.hold.Load() != which {
		return nil
	}
	a.entered <- struct{}{}
	if a.panicOn {
		panic("the source adapter panicked")
	}
	if a.failWith != nil {
		return a.failWith
	}
	select {
	case <-a.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *holdAdapter) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, key string) (r.PreparedRead, error) {
	a.prepares.Add(1)
	if e := a.wait(ctx, sc.Owner, holdPrepare); e != nil {
		return r.PreparedRead{}, e
	}
	return a.Simulator.PrepareRead(ctx, sc, q, key)
}

func (a *holdAdapter) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, p r.PreparedRead) error {
	if e := a.wait(ctx, sc.Owner, holdAuthorize); e != nil {
		return e
	}
	return a.Simulator.AuthorizeCurrent(ctx, sc, p)
}

type gateWorld struct {
	s     *r.Service
	a     *holdAdapter
	p, p2 r.Principal // two identities of one tenant
}

func newGateWorld(t *testing.T, limit int) *gateWorld {
	t.Helper()
	p := contractsim.DemoPrincipal()
	p2 := p
	p2.Subject = "someone-else"
	sim := contractsim.Financial()
	for _, who := range []r.Principal{p, p2} {
		sim.Grant(contractsim.DemoOwner(who, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	}
	a := &holdAdapter{Simulator: sim, who: p.Subject, entered: make(chan struct{}, 64), release: make(chan struct{})}
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901"), Quotas: r.Quotas{QueriesInFlight: limit}})
	if e != nil {
		t.Fatal(e)
	}
	return &gateWorld{s: s, a: a, p: p, p2: p2}
}

// start runs a query as the principal in the background and waits until it is inside the adapter.
func (w *gateWorld) startQuery(t *testing.T, p r.Principal) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, e := w.s.Query(context.Background(), p, contractsim.FinancialQuery()); done <- e }()
	select {
	case <-w.a.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the query never reached the source")
	}
	return done
}

func wantQuota(t *testing.T, e error) {
	t.Helper()
	if !errors.Is(e, r.ErrQuota) || r.QuotaOf(e) != r.QuotaQueriesInFlight {
		t.Fatalf("want the queries_in_flight quota, got %v", e)
	}
}

func TestQueriesInFlightBoundOneIdentityAndLeaveAnotherAlone(t *testing.T) {
	w := newGateWorld(t, 2)
	w.a.hold.Store(holdPrepare)
	one, two := w.startQuery(t, w.p), w.startQuery(t, w.p)
	// The third of the same identity is over the bound, and nothing of it ran: the adapter was not asked.
	asked := w.a.prepares.Load()
	_, e := w.s.Query(context.Background(), w.p, contractsim.FinancialQuery())
	wantQuota(t, e)
	if w.a.prepares.Load() != asked {
		t.Fatal("a refused query reached the source")
	}
	// Another identity of the same tenant is not affected (the adapter passes it).
	if _, e := w.s.Query(context.Background(), w.p2, contractsim.FinancialQuery()); e != nil {
		t.Fatal("another identity was held by the first:", e)
	}
	// A query finishing frees its slot. The release wakes whichever of the two held queries the runtime queued on the
	// channel first; which one that is depends on scheduling (under the race detector on a slow machine it was the second,
	// and the first then sat held until its work deadline). The test takes whichever finished and keeps the other for the end.
	w.a.release <- struct{}{}
	var other chan error
	select {
	case e := <-one:
		if e != nil {
			t.Fatal(e)
		}
		other = two
	case e := <-two:
		if e != nil {
			t.Fatal(e)
		}
		other = one
	}
	w.a.hold.Store(holdNone)
	if _, e := w.s.Query(context.Background(), w.p, contractsim.FinancialQuery()); e != nil {
		t.Fatal("the slot was not given back when a query finished:", e)
	}
	w.a.release <- struct{}{}
	if e := <-other; e != nil {
		t.Fatal(e)
	}
}

func TestQueriesInFlightAreGivenBackOnEveryWayOut(t *testing.T) {
	w := newGateWorld(t, 1)
	// An error: three failures in a row are three failures, never the quota.
	w.a.hold.Store(holdPrepare)
	w.a.failWith = errors.New("the source failed")
	for i := range 3 {
		w.a.entered = make(chan struct{}, 64)
		if _, e := w.s.Query(context.Background(), w.p, contractsim.FinancialQuery()); e == nil || errors.Is(e, r.ErrQuota) {
			t.Fatalf("query %d: %v", i, e)
		}
	}
	// A panic: the caller recovers it, and the slot is free.
	w.a.failWith, w.a.panicOn = nil, true
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the adapter's panic was swallowed")
			}
		}()
		_, _ = w.s.Query(context.Background(), w.p, contractsim.FinancialQuery())
	}()
	w.a.panicOn = false
	w.a.entered = make(chan struct{}, 64) // the panic and the failures left their tokens
	// A caller that goes away: its context ends while the query is in the source.
	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan error, 1)
	go func() { _, e := w.s.Query(ctx, w.p, contractsim.FinancialQuery()); gone <- e }()
	<-w.a.entered
	_, e := w.s.Query(context.Background(), w.p, contractsim.FinancialQuery())
	wantQuota(t, e)
	cancel()
	if e := <-gone; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	w.a.hold.Store(holdNone)
	if _, e := w.s.Query(context.Background(), w.p, contractsim.FinancialQuery()); e != nil {
		t.Fatal("the slot was not given back after the error, the panic and the cancellation:", e)
	}
}

func TestRowReadsShareTheBoundWithQueries(t *testing.T) {
	w := newGateWorld(t, 1)
	job, e := w.s.SubmitSnapshot(context.Background(), w.p, contractsim.FinancialQuery(), "k")
	if e != nil {
		t.Fatal(e)
	}
	if ran, e := w.s.RunOne(context.Background()); !ran || e != nil {
		t.Fatal(ran, e)
	}
	job, _ = w.s.Job(context.Background(), w.p, job.ID)
	// A row read held in the source's check of the snapshot holds the identity's one slot...
	w.a.hold.Store(holdAuthorize)
	done := make(chan error, 1)
	go func() { _, e := w.s.Rows(context.Background(), w.p, job.SnapshotID, 1, ""); done <- e }()
	<-w.a.entered
	asked := w.a.prepares.Load()
	_, e = w.s.Query(context.Background(), w.p, contractsim.FinancialQuery())
	wantQuota(t, e)
	_, e = w.s.Rows(context.Background(), w.p, job.SnapshotID, 1, "")
	wantQuota(t, e)
	if w.a.prepares.Load() != asked {
		t.Fatal("a refused read reached the source")
	}
	// ... another identity's read of its own is unaffected by it (it has no snapshot: not found, and not the quota) ...
	if _, e = w.s.Rows(context.Background(), w.p2, job.SnapshotID, 1, ""); errors.Is(e, r.ErrQuota) {
		t.Fatal("another identity was held by the first")
	}
	// ... and the bad request of a held identity is still a bad request: the limit is checked before any slot is wanted.
	if _, e = w.s.Rows(context.Background(), w.p, job.SnapshotID, 0, ""); !errors.Is(e, r.ErrInvalid) {
		t.Fatal(e)
	}
	w.a.release <- struct{}{}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	w.a.hold.Store(holdNone)
	if _, e = w.s.Rows(context.Background(), w.p, job.SnapshotID, 1, ""); e != nil {
		t.Fatal("the slot was not given back when the row read finished:", e)
	}
	// An invalid principal takes no slot and is refused as before.
	if _, e = w.s.Query(context.Background(), r.Principal{}, contractsim.FinancialQuery()); !errors.Is(e, r.ErrDenied) {
		t.Fatal(e)
	}
}

// Many identities come and go: the gate keeps no entry for one that is not running anything.
func TestQueryGateKeepsNoEntryForAnIdentityThatIsDone(t *testing.T) {
	w := newGateWorld(t, 1)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := w.p2
			p.Subject = "visitor-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			_, _ = w.s.Query(context.Background(), p, contractsim.FinancialQuery()) // denied by the source (no grant); the slot still comes back
		}()
	}
	wg.Wait()
	if n := r.QueryGateSizeForTests(w.s); n != 0 {
		t.Fatalf("the gate holds %d entries with nothing running", n)
	}
}
