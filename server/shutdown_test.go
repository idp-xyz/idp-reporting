package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// gate is an adapter that stops inside ReadPage while armed, so that a test can hold a request or a worker exactly where it is working.
// entered receives once for each call that stopped; a stopped call returns when release is closed or its context ends.
type gate struct {
	r.Adapter
	fail      error  // ReadPage fails with it
	panicWith string // ReadPage panics with it
	armed     atomic.Bool
	entered   chan struct{}
	release   chan struct{}
}

func newGate(a r.Adapter) *gate {
	return &gate{Adapter: a, entered: make(chan struct{}, 16), release: make(chan struct{})}
}

func (g *gate) IsSimulator() bool { return true }
func (g *gate) DataClass() string { return r.DataClassSimulator } // the wrapper hides the simulator's word (S08-A) and says it itself
func (g *gate) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	if g.panicWith != "" {
		panic(g.panicWith)
	}
	if g.fail != nil {
		return r.SourcePage{}, g.fail
	}
	if g.armed.Load() {
		g.entered <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			return r.SourcePage{}, ctx.Err()
		}
	}
	return g.Adapter.ReadPage(ctx, sc, p, cursor, limit)
}

func gatedWorld(t testing.TB) (*world, *gate) {
	w := newWorld(t)
	g := newGate(w.sim)
	return w, g
}

func registryOf(g *gate) Registry {
	return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return g, nil }}
}

// startGated starts a server whose adapter is the gate.
func startGated(t *testing.T, w *world, g *gate, hooks *Hooks, opts ...Option) *started {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lb := &logBuffer{}
	opts = append([]Option{WithPollIntervalForTests(time.Millisecond), WithHooksForTests(hooks)}, opts...)
	s, err := Start(ctx, w.cfg, registryOf(g), w.getenv, lb, opts...)
	if err != nil {
		cancel()
		t.Fatalf("start: %v", err)
	}
	st := &started{Server: s, log: lb, cancel: cancel, w: w}
	tr := newTLSTransport(w)
	st.client = &http.Client{Transport: tr, Timeout: 20 * time.Second}
	st.base = "https://" + s.Addr()
	t.Cleanup(func() { cancel(); <-s.Done(); tr.CloseIdleConnections() })
	return st
}

func newHooks() *Hooks {
	return &Hooks{DrainBegan: make(chan struct{}), ListenerClosed: make(chan struct{}), Started: make(chan *Server, 1), Reloaded: make(chan struct{}, 4)}
}

// waitBusyZero waits for the workers that were left running to come out of their call.
func waitBusyZero(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.busy.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a worker is still inside a call")
		}
		runtime.Gosched()
	}
}

// A drain in the middle of a request: readiness says draining at once, the listener closes and a new connection is refused, the request in
// flight completes with its answer, and the server then returns cleanly with every slot given back and nothing left running.
func TestDrainLetsAnInFlightRequestFinishAndRefusesNewConnections(t *testing.T) {
	snap := goroutines()
	w, g := gatedWorld(t)
	store := NewMemoryStoreForTests(time.Now)
	hooks := newHooks()
	s := startGated(t, w, g, hooks, WithStoreForTests(store))
	token := mint(t, w.keys[0], claims{})
	g.armed.Store(true)
	type answer struct {
		status int
		body   []byte
	}
	answered := make(chan answer, 1)
	go func() {
		status, _, body := s.do(t, "POST", api+"/queries", token, contractsim.FinancialQuery())
		answered <- answer{status, body}
	}()
	<-g.entered // the request is inside the adapter
	if s.inflight() != 1 || s.requests.count() != 1 {
		t.Fatalf("slots: %d %d", s.inflight(), s.requests.count())
	}
	s.cancel()
	<-hooks.DrainBegan
	// Readiness says draining at once, health stays 200, the answers are the API's.
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 || strings.TrimSpace(rec.Body.String()) != `{"status":"not_ready","reason":"draining"}` || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("readyz while draining: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("healthz while draining: %d", rec.Code)
	}
	<-hooks.ListenerClosed
	if c, err := net.DialTimeout("tcp", s.Addr(), 5*time.Second); err == nil {
		c.Close()
		t.Fatal("a new connection was accepted after the listener closed")
	}
	select {
	case <-s.Done():
		t.Fatal("the drain ended with a request in flight")
	default:
	}
	close(g.release)
	a := <-answered
	var result r.Result
	if a.status != 200 || json.Unmarshal(a.body, &result) != nil || len(result.Rows) == 0 {
		t.Fatalf("the request in flight: %d %s", a.status, a.body)
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if s.inflight() != 0 || s.requests.count() != 0 || !store.Closed() {
		t.Errorf("after the drain: %d slots, %d requests, store closed %v", s.inflight(), s.requests.count(), store.Closed())
	}
	events := s.log.events(t, "drain_begin")
	ends := s.log.events(t, "drain_end")
	if len(events) != 1 || events[0]["in_flight"] != float64(1) || len(ends) != 1 || ends[0]["clean"] != true {
		t.Errorf("drain log: %v %v", events, ends)
	}
	s.client.CloseIdleConnections()
	noNewGoroutines(t, snap)
}

func submitSnapshot(t *testing.T, s *started, token, key string) string {
	t.Helper()
	status, _, body := s.do(t, "POST", api+"/snapshot-jobs", token, contractsim.FinancialQuery(), "Idempotency-Key", key)
	var j r.JobView
	if status != 202 || json.Unmarshal(body, &j) != nil {
		t.Fatalf("snapshot: %d %s", status, body)
	}
	return j.ID
}

// A worker in the middle of a job when the drain begins stops claiming, finishes the job inside the grace and the job is succeeded.
func TestDrainLetsAWorkerFinishItsJob(t *testing.T) {
	snap := goroutines()
	w, g := gatedWorld(t)
	store := NewMemoryStoreForTests(time.Now)
	hooks := newHooks()
	s := startGated(t, w, g, hooks, WithStoreForTests(store))
	token := mint(t, w.keys[0], claims{})
	g.armed.Store(true)
	id := submitSnapshot(t, s, token, "one")
	<-g.entered // the worker is inside the job
	// A second job is queued behind it: the worker must not claim it once the drain has begun.
	second := submitSnapshot(t, s, token, "two")
	s.cancel()
	<-hooks.DrainBegan
	<-hooks.ListenerClosed
	select {
	case <-s.Done():
		t.Fatal("the drain ended with a job running")
	default:
	}
	g.armed.Store(false)
	close(g.release)
	if err := s.Wait(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	identity := r.Identity{Tenant: testTenant, Issuer: testIssuer, Subject: testSubject}
	if j, err := store.Job(context.Background(), identity, id); err != nil || j.State != "succeeded" {
		t.Fatalf("the job the worker held: %+v %v", j, err)
	}
	if j, err := store.Job(context.Background(), identity, second); err != nil || j.State != "queued" {
		t.Fatalf("the job behind it was claimed during the drain: %+v %v", j, err)
	}
	if ends := s.log.events(t, "drain_end"); len(ends) != 1 || ends[0]["clean"] != true || ends[0]["jobs_left"] != float64(0) {
		t.Errorf("%v", ends)
	}
	s.client.CloseIdleConnections()
	noNewGoroutines(t, snap)
}

// graceTimer is a shutdown_grace the test ends by hand.
type graceTimer struct{ fire chan time.Time }

func newGraceTimer() *graceTimer { return &graceTimer{fire: make(chan time.Time, 1)} }
func (g *graceTimer) option() Option {
	return WithGraceTimerForTests(func(time.Duration) (<-chan time.Time, func()) { return g.fire, func() {} })
}

// A drain whose grace runs out with a worker mid-job: the exit is "out of grace", the job keeps its lease (it is still running, not failed) and
// another process claims it once the lease has run out, by the store's clock; the log says how many jobs were left.
func TestDrainOutOfGraceLeavesTheJobToItsLease(t *testing.T) {
	w, g := gatedWorld(t)
	clock := newFakeClock()
	store := NewMemoryStoreForTests(clock.Now)
	hooks := newHooks()
	timer := newGraceTimer()
	s := startGated(t, w, g, hooks, WithStoreForTests(store), timer.option())
	token := mint(t, w.keys[0], claims{})
	g.armed.Store(true)
	id := submitSnapshot(t, s, token, "one")
	<-g.entered
	s.cancel()
	<-hooks.DrainBegan
	timer.fire <- time.Time{}
	if err := s.Wait(); err != ErrDrainTimeout {
		t.Fatalf("drain: %v", err)
	}
	if store.Closed() {
		t.Error("the store was closed under a worker that is still inside a call")
	}
	identity := r.Identity{Tenant: testTenant, Issuer: testIssuer, Subject: testSubject}
	j, err := store.Job(context.Background(), identity, id)
	if err != nil || j.State != "running" {
		t.Fatalf("the job is not left running under its lease: %+v %v", j, err)
	}
	// Before the lease runs out nobody else can have it; after, another process claims the same job.
	if c, err := store.Claim(context.Background(), r.LeaseDuration); err != nil || c != nil {
		t.Fatalf("claimed before the lease ran out: %v %v", c, err)
	}
	clock.Advance(r.LeaseDuration + time.Second)
	c, err := store.Claim(context.Background(), r.LeaseDuration)
	if err != nil || c == nil || c.Job.ID != id {
		t.Fatalf("the job was not claimable after its lease: %v %v", c, err)
	}
	ends := s.log.events(t, "drain_end")
	if len(ends) != 1 || ends[0]["clean"] != false || ends[0]["jobs_left"] != float64(1) {
		t.Errorf("drain log: %v", ends)
	}
	// The worker is let go so that the test leaves nothing behind.
	g.armed.Store(false)
	close(g.release)
	waitBusyZero(t, s.Server)
	s.client.CloseIdleConnections()
}

// A drain whose grace runs out with a request in flight closes the connection, the handler comes back, and every slot is given back.
func TestDrainOutOfGraceClosesRequestsAndGivesBackTheSlots(t *testing.T) {
	snap := goroutines()
	w, g := gatedWorld(t)
	store := NewMemoryStoreForTests(time.Now)
	hooks := newHooks()
	timer := newGraceTimer()
	s := startGated(t, w, g, hooks, WithStoreForTests(store), timer.option())
	token := mint(t, w.keys[0], claims{})
	g.armed.Store(true)
	failed := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("POST", s.base+api+"/queries", strings.NewReader(mustJSON(contractsim.FinancialQuery())))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.client.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		failed <- err
	}()
	<-g.entered
	s.cancel()
	<-hooks.DrainBegan
	timer.fire <- time.Time{}
	if err := s.Wait(); err != ErrDrainTimeout {
		t.Fatalf("drain: %v", err)
	}
	if err := <-failed; err == nil {
		t.Error("the request in flight was answered after the grace ran out")
	}
	if s.inflight() != 0 || s.requests.count() != 0 {
		t.Errorf("slots held: %d %d", s.inflight(), s.requests.count())
	}
	if !store.Closed() {
		t.Error("nothing was running, so the store should have been closed")
	}
	s.client.CloseIdleConnections()
	noNewGoroutines(t, snap)
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// The command's exit codes: 0 for a clean drain, 1 for a refused start, 2 for a drain that ran out of grace.
func TestExitCodes(t *testing.T) {
	run := func(t *testing.T, w *world, reg Registry, interrupt func(*Server, context.CancelFunc, *Hooks), opts ...Option) (int, string) {
		t.Helper()
		path := writeConfigFile(t, w)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hooks := newHooks()
		errs := &logBuffer{}
		code := make(chan int, 1)
		opts = append(opts, WithHooksForTests(hooks), WithPollIntervalForTests(time.Millisecond))
		go func() {
			code <- Execute(ctx, []string{"reporting-server", "run", "-config", path}, reg, w.getenv, io.Discard, errs, make(chan os.Signal), opts...)
		}()
		select {
		case s := <-hooks.Started:
			interrupt(s, cancel, hooks)
		case c := <-code:
			return c, errs.String()
		}
		c := <-code
		return c, errs.String()
	}
	t.Run("0 for a clean drain", func(t *testing.T) {
		w := newWorld(t)
		c, _ := run(t, w, w.registry(), func(s *Server, cancel context.CancelFunc, h *Hooks) { cancel() }, WithStoreForTests(NewMemoryStoreForTests(time.Now)))
		if c != 0 {
			t.Fatal(c)
		}
	})
	t.Run("1 for a refused start", func(t *testing.T) {
		w := newWorld(t)
		c, out := run(t, w, Registry{}, nil, WithStoreForTests(NewMemoryStoreForTests(time.Now)))
		if c != 1 || !strings.Contains(out, "no adapter is registered for integration") {
			t.Fatal(c, out)
		}
	})
	t.Run("2 for a drain that ran out of grace", func(t *testing.T) {
		w, g := gatedWorld(t)
		timer := newGraceTimer()
		g.armed.Store(true)
		var srv *Server
		c, out := run(t, w, registryOf(g), func(s *Server, cancel context.CancelFunc, h *Hooks) {
			srv = s
			// A job the worker will hold, then the interrupt, then the end of the grace.
			status := submitRaw(t, s, w, g)
			_ = status
			<-g.entered
			cancel()
			<-h.DrainBegan
			timer.fire <- time.Time{}
		}, WithStoreForTests(NewMemoryStoreForTests(time.Now)), timer.option())
		if c != 2 || !strings.Contains(out, "ran out of shutdown_grace") {
			t.Fatal(c, out)
		}
		g.armed.Store(false)
		close(g.release)
		waitBusyZero(t, srv)
	})
}

// submitRaw admits a snapshot job as the test caller.
func submitRaw(t *testing.T, s *Server, w *world, g *gate) int {
	t.Helper()
	st := &started{Server: s, client: &http.Client{Transport: newTLSTransport(w)}, base: "https://" + s.Addr(), w: w}
	defer st.client.CloseIdleConnections()
	status, _, _ := st.do(t, "POST", api+"/snapshot-jobs", mint(t, w.keys[0], claims{}), contractsim.FinancialQuery(), "Idempotency-Key", "raw")
	return status
}
