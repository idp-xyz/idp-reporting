package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// The object store at startup (S04-E, docs/S04E_SCOPE.md E5): the server starts only when HEAD bucket passes, and says which bucket and what
// status when it does not; it never says the endpoint or a key; the memory store refuses the flags; no flag is the demo as it was.
func TestTheObjectStoreMustAnswerBeforeTheServerStarts(t *testing.T) {
	const access, secretKey = "AKIADEMOSTARTUP0001", "demo-startup-secret-0123456789abcdef"
	var heads, others atomic.Int64
	status := atomic.Int64{}
	status.Store(200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "HEAD" || req.URL.Path != "/demo-bucket" || !strings.Contains(req.Header.Get("Authorization"), "Credential="+access+"/") {
			others.Add(1)
			w.WriteHeader(400)
			return
		}
		heads.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	keys := map[string]string{objects.AccessKeyEnv: access, objects.SecretKeyEnv: secretKey}
	getenv := func(k string) string { return keys[k] }
	flags := func(args ...string) *objects.Flags {
		fs := flag.NewFlagSet("demo", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f := objects.Register(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return f
	}
	configured := flags("-object-store-endpoint="+server.URL, "-object-store-bucket=demo-bucket", "-object-store-loopback-http")
	// Unconfigured: the demo as it was, and no request.
	if store, err := openObjectStore(flags(), getenv, false); store != nil || err != nil {
		t.Fatal(store, err)
	}
	if store, err := openObjectStore(flags(), getenv, true); store != nil || err != nil {
		t.Fatal("-memory without the flags was refused", store, err)
	}
	// Reachable: the store, after one HEAD on the bucket.
	store, err := openObjectStore(configured, getenv, false)
	if err != nil || store == nil || heads.Load() != 1 {
		t.Fatal(store, err, heads.Load())
	}
	// The bucket refused or missing, or the server gone: no store, a text with the bucket and the status, and nothing else.
	for _, code := range []int{403, 404, 500} {
		status.Store(int64(code))
		_, err := openObjectStore(configured, getenv, false)
		want := fmt.Sprintf(`the object store's bucket "demo-bucket" is not usable: HEAD bucket answered status %d`, code)
		if err == nil || err.Error() != want {
			t.Errorf("status %d: %v, want %q", code, err, want)
		}
	}
	server.Close()
	_, err = openObjectStore(configured, getenv, false)
	if err == nil || err.Error() != `the object store's bucket "demo-bucket" is not usable: HEAD bucket got no answer` {
		t.Errorf("a server that is gone: %v", err)
	}
	for _, e := range []error{err} {
		for _, leak := range []string{host, "127.0.0.1", access, secretKey, "dial", "refused"} {
			if strings.Contains(e.Error(), leak) {
				t.Errorf("the startup refusal carries %q: %q", leak, e)
			}
		}
	}
	// Refused before any request: the memory store keeps content inline, a key missing, an endpoint off the loopback.
	before := heads.Load()
	if _, err := openObjectStore(configured, getenv, true); err == nil || !strings.Contains(err.Error(), "-memory") {
		t.Errorf("-memory with the object store flags: %v", err)
	}
	if _, err := openObjectStore(configured, func(k string) string { return map[string]string{objects.AccessKeyEnv: access}[k] }, false); err == nil || strings.Contains(err.Error(), access) || !strings.Contains(err.Error(), objects.SecretKeyEnv) {
		t.Errorf("a missing secret key: %v", err)
	}
	if _, err := openObjectStore(flags("-object-store-endpoint=http://10.0.0.5:9000", "-object-store-bucket=demo-bucket", "-object-store-loopback-http"), getenv, false); err == nil {
		t.Error("an http endpoint off the loopback was accepted")
	}
	if heads.Load() != before || others.Load() != 0 {
		t.Errorf("a refused configuration made requests: %d heads, %d others", heads.Load()-before, others.Load())
	}
}

// The sweep loop logs the object store's counts (S04-E) when there are some, as counts, and a quiet sweep with no objects says nothing as before.
func TestSweepLoopLogsTheObjectCountsAndOnlyCounts(t *testing.T) {
	sweeper := &scriptedSweeper{
		results: []r.SweepResult{{Artifacts: 2, Snapshots: 1, Jobs: 3, Objects: 2}, {Orphans: 4}, {ObjectsLeft: 1}, {}},
		errs:    []error{nil, nil, errors.New("dial tcp 10.1.2.3:9000: bucket secret-bucket key artifacts/0123"), nil},
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
	<-done
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"retention sweep: 2 artifacts and 1 snapshots deleted, 3 job payloads reduced; objects: 2 deleted, 0 orphans deleted, 0 left for the next reconciliation",
		"retention sweep: 0 artifacts and 0 snapshots deleted, 0 job payloads reduced; objects: 0 deleted, 4 orphans deleted, 0 left for the next reconciliation",
		"retention sweep stopped; it had deleted 0 artifacts and 0 snapshots and reduced 0 job payloads; objects: 0 deleted, 0 orphans deleted, 1 left for the next reconciliation",
	}
	if fmt.Sprint(logged) != fmt.Sprint(want) {
		t.Fatalf("logged %q, want %q", logged, want)
	}
	for _, line := range logged {
		for _, leak := range []string{"10.1.2.3", "secret-bucket", "artifacts/0123"} {
			if strings.Contains(line, leak) {
				t.Fatalf("the log carries %q: %s", leak, line)
			}
		}
	}
}
