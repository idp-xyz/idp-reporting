package server

import (
	"context"
	"sync"
	"time"

	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// The options below exist for the repository's tests and for the test host of the qualification (internal/servertesthost). The shipped command
// (cmd/reporting-server) calls server.Main, which passes none, and its configuration has no field that reaches any of them
// (TestShippedCommandCannotReachTheTestOnlyOptions).

// WithStoreForTests replaces the production opener with a store the test made.
func WithStoreForTests(s Store) Option { return func(o *options) { o.store = s } }

// WithInsecureStoreForTests lifts OpenProduction's TLS and database-name rules (postgres.ProductionConfig.InsecureTestOnly), for the throwaway
// test server. The role, the schema and the profile checks stay.
func WithInsecureStoreForTests() Option { return func(o *options) { o.insecureStore = true } }

// WithClockForTests replaces the clock of the log, the readiness cache and the certificate check.
func WithClockForTests(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithGraceTimerForTests replaces the timer of shutdown_grace: it returns the channel that fires when the grace is used up, and a stop function.
func WithGraceTimerForTests(f func(time.Duration) (<-chan time.Time, func())) Option {
	return func(o *options) { o.graceTimer = f }
}

// WithPollIntervalForTests sets how long an idle worker waits before it asks for a job again.
func WithPollIntervalForTests(d time.Duration) Option { return func(o *options) { o.poll = d } }

// WithSweepTickerForTests replaces the ticker of the sweep loop.
func WithSweepTickerForTests(f func(time.Duration) (<-chan time.Time, func())) Option {
	return func(o *options) { o.sweepTicker = f }
}

// WithHooksForTests gives the test its signals.
func WithHooksForTests(h *Hooks) Option { return func(o *options) { o.hooks = h } }

// MemoryStore is the memory store with the readiness of a Store, for tests that need no database: SetNotReady makes Ready answer a reason.
type MemoryStore struct {
	*memory.Store
	designs map[string]*privatedocs.MemoryBackend
	mu      sync.Mutex
	reason  string
	closed  bool
	sweep   error
}

// NewMemoryStoreForTests makes one; now is the store's clock (leases and expiries).
func NewMemoryStoreForTests(now func() time.Time) *MemoryStore {
	return &MemoryStore{Store: memory.New(now)}
}

// Designs implements Store: one memory backend per private store, shared by every server started on this MemoryStore (S11-B).
func (m *MemoryStore) Designs(store string) (privatedocs.Backend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.designs == nil {
		m.designs = map[string]*privatedocs.MemoryBackend{}
	}
	switch store {
	case "layouts", "dashboards", "templates", "backgrounds", "images":
	default:
		return nil, r.ErrInvalid
	}
	if m.designs[store] == nil {
		m.designs[store] = privatedocs.NewMemoryBackend()
	}
	return m.designs[store], nil
}

// SetNotReady makes Ready answer reason ("store", "schema", "roles"); "" makes it ready again.
func (m *MemoryStore) SetNotReady(reason string) { m.mu.Lock(); m.reason = reason; m.mu.Unlock() }

// SetSweepError makes Sweep fail with err; nil makes it work again.
func (m *MemoryStore) SetSweepError(err error) { m.mu.Lock(); m.sweep = err; m.mu.Unlock() }

// Sweep implements the repository's sweep, failing when SetSweepError says so.
func (m *MemoryStore) Sweep(ctx context.Context, o r.SweepOptions) (r.SweepResult, error) {
	m.mu.Lock()
	err := m.sweep
	m.mu.Unlock()
	if err != nil {
		return r.SweepResult{}, err
	}
	return m.Store.Sweep(ctx, o)
}

// Ready implements Store.
func (m *MemoryStore) Ready(context.Context) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reason
}

// LedgerVersion implements Store.
func (m *MemoryStore) LedgerVersion(context.Context) (int, error) { return 0, nil }

// Close implements Store.
func (m *MemoryStore) Close() { m.mu.Lock(); m.closed = true; m.mu.Unlock() }

// Closed says whether Close was called.
func (m *MemoryStore) Closed() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.closed }

var _ Store = (*MemoryStore)(nil)
var _ r.Repository = (*MemoryStore)(nil)
