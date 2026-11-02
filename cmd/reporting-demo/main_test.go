package main

import (
	"context"
	"errors"
	"fmt"
	r "idp.local/reporting/reporting"
	"strings"
	"sync"
	"testing"
	"time"
)

// The demo says why a fixture store cannot be opened when the reason is the platform's own refusal,
// and nothing but the generic text otherwise.
func TestOpenFailureKeepsThePlatformsRefusalsAndHidesTheRest(t *testing.T) {
	const generic = "guarded fixture could not be opened"
	precondition := fmt.Errorf("migration 004 up: 3 issue jobs and records exist; run reporting-migrate down --to 2 --accept-data-loss first: %w", r.ErrDenied)
	for name, e := range map[string]error{
		"a denial with its remedy":  precondition,
		"a denial of the database":  r.ErrDenied,
		"an integrity failure":      fmt.Errorf("schema ledger: version 2 was applied from a different file: %w", r.ErrIntegrity),
		"an invalid request":        fmt.Errorf("a DSN that is not one: %w", r.ErrInvalid),
		"a conflict":                fmt.Errorf("the ledger is gone: another migrator reverted the schema, rerun: %w", r.ErrConflict),
		"a refusal wrapped further": fmt.Errorf("opening: %w", precondition),
	} {
		got := openFailure(generic, e)
		if !strings.HasPrefix(got.Error(), generic+": ") || !strings.Contains(got.Error(), e.Error()) || !errors.Is(got, e) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := openFailure(generic, precondition); !strings.Contains(got.Error(), "reporting-migrate down --to 2 --accept-data-loss") {
		t.Errorf("the remedy is not in the message: %q", got)
	}
	// Anything else is the generic text alone: a connection failure may carry the host, the user and
	// the database of the DSN.
	for name, e := range map[string]error{
		"a connection failure":    errors.New("failed to connect to `host=127.0.0.1 user=postgres database=reporting_test_s01`: dial error"),
		"a plain error":           errors.New("boom"),
		"a wrapped login failure": fmt.Errorf("wrapped: %w", errors.New("password authentication failed for user postgres")),
	} {
		if got := openFailure(generic, e); got.Error() != generic || strings.Contains(got.Error(), "127.0.0.1") || strings.Contains(got.Error(), "password") {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// -embed-origin is read strictly and at startup: exact origins only, loopback http only here, and only for the integrations the demo has.
func TestEmbedConfigStopsTheStartOnAnythingMalformed(t *testing.T) {
	integrations := []string{"brant-contract-demo", "shipment-contract-demo"}
	config, err := embedConfig([]string{"brant-contract-demo=http://127.0.0.1:7811", "brant-contract-demo=https://app.brant.example", "shipment-contract-demo=https://ship.example:8443"}, integrations)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Allows("brant-contract-demo", "http://127.0.0.1:7811") || !config.Allows("shipment-contract-demo", "https://ship.example:8443") || config.Allows("shipment-contract-demo", "http://127.0.0.1:7811") {
		t.Error("the configuration is not what was given")
	}
	if config, err := embedConfig(nil, integrations); err != nil || len(config.Integrations()) != 0 {
		t.Errorf("no flag is an empty configuration: %v", err)
	}
	for name, value := range map[string]string{
		"no equals":            "brant-contract-demo",
		"a path":               "brant-contract-demo=https://app.brant.example/app",
		"a trailing slash":     "brant-contract-demo=https://app.brant.example/",
		"a wildcard":           "brant-contract-demo=https://*.brant.example",
		"plain http, public":   "brant-contract-demo=http://app.brant.example",
		"http, not loopback":   "brant-contract-demo=http://192.168.1.2:8080",
		"upper case":           "brant-contract-demo=https://APP.brant.example",
		"another integration":  "unknown-integration=https://app.brant.example",
		"the default port":     "brant-contract-demo=https://app.brant.example:443",
		"a malformed integers": "=https://app.brant.example",
	} {
		if _, err := embedConfig([]string{value}, integrations); err == nil || !strings.Contains(err.Error(), "-embed-origin") {
			t.Errorf("%s: %q accepted (%v)", name, value, err)
		}
	}
}

// A sweeper that answers from a script and keeps what it was asked.
type scriptedSweeper struct {
	mu      sync.Mutex
	opts    []r.SweepOptions
	results []r.SweepResult
	errs    []error
}

func (s *scriptedSweeper) Sweep(_ context.Context, o r.SweepOptions) (r.SweepResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.opts)
	s.opts = append(s.opts, o)
	if n < len(s.results) {
		return s.results[n], s.errs[n]
	}
	return r.SweepResult{}, nil
}
func (s *scriptedSweeper) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.opts) }

// The worker loop's sweep (S04-B) runs on every tick with the default grace and batch, logs counts only (a quiet sweep says nothing, a
// failed one says that it stopped and what it had done, never the error, which may carry the connection's details), goes on after a failure
// and stops with its context.
func TestSweepLoopSweepsEveryIntervalLogsCountsOnlyAndStops(t *testing.T) {
	sweeper := &scriptedSweeper{
		results: []r.SweepResult{{Artifacts: 2, Snapshots: 1, Jobs: 3}, {Artifacts: 1}, {}},
		errs:    []error{nil, errors.New("failed to connect to `host=db.internal user=reporting_sweep database=reports`: password authentication failed"), nil},
	}
	var mu sync.Mutex
	var logged []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sweepLoop(ctx, sweeper, 5*time.Millisecond, logf) }()
	for end := time.Now().Add(5 * time.Second); sweeper.calls() < 5 && time.Now().Before(end); time.Sleep(time.Millisecond) {
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop with its context")
	}
	calls := sweeper.calls()
	if calls < 5 {
		t.Fatalf("%d sweeps", calls)
	}
	for _, o := range sweeper.opts {
		if o != (r.SweepOptions{}) {
			t.Fatalf("the loop asked for %+v, not the defaults", o)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 2 || logged[0] != "retention sweep: 2 artifacts and 1 snapshots deleted, 3 job payloads reduced" || logged[1] != "retention sweep stopped; it had deleted 1 artifacts and 0 snapshots and reduced 0 job payloads" {
		t.Fatalf("logged %q", logged)
	}
	for _, line := range logged {
		for _, leak := range []string{"db.internal", "reporting_sweep", "password", "host="} {
			if strings.Contains(line, leak) {
				t.Fatalf("the log carries %q: %s", leak, line)
			}
		}
	}
}

// The -quota-* flags (S04-D): zero is the default, a value out of its bounds stops the start and names the flag.
func TestQuotaFlagsStopTheStartOutsideTheirBounds(t *testing.T) {
	q, err := quotaConfig(0, 0, 0)
	if err != nil || q != (r.Quotas{JobsInFlight: 8, StoredResults: 64, AdmissionsPerHour: 120}) {
		t.Fatalf("%+v %v", q, err)
	}
	if q, err = quotaConfig(1000, 100000, 1); err != nil || q != (r.Quotas{JobsInFlight: 1000, StoredResults: 100000, AdmissionsPerHour: 1}) {
		t.Fatalf("%+v %v", q, err)
	}
	for _, c := range []struct {
		jobs, results, admissions int
		flag                      string
	}{{-1, 0, 0, "-quota-jobs"}, {1001, 0, 0, "-quota-jobs"}, {0, -1, 0, "-quota-results"}, {0, 100001, 0, "-quota-results"}, {0, 0, -1, "-quota-admissions"}, {0, 0, 100001, "-quota-admissions"}} {
		if _, err := quotaConfig(c.jobs, c.results, c.admissions); err == nil || !strings.Contains(err.Error(), c.flag) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
}
