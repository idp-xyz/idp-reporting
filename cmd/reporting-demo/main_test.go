package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

// The -quota-* flags (S04-D, and -quota-queries of S04-G): zero is the default, a value out of its bounds stops the start and names the flag.
func TestQuotaFlagsStopTheStartOutsideTheirBounds(t *testing.T) {
	q, err := quotaConfig(0, 0, 0, 0)
	if err != nil || q != (r.Quotas{JobsInFlight: 8, StoredResults: 64, AdmissionsPerHour: 120, QueriesInFlight: 4}) {
		t.Fatalf("%+v %v", q, err)
	}
	if q, err = quotaConfig(1000, 100000, 1, 64); err != nil || q != (r.Quotas{JobsInFlight: 1000, StoredResults: 100000, AdmissionsPerHour: 1, QueriesInFlight: 64}) {
		t.Fatalf("%+v %v", q, err)
	}
	for _, c := range []struct {
		jobs, results, admissions, queries int
		flag                               string
	}{{-1, 0, 0, 0, "-quota-jobs"}, {1001, 0, 0, 0, "-quota-jobs"}, {0, -1, 0, 0, "-quota-results"}, {0, 100001, 0, 0, "-quota-results"}, {0, 0, -1, 0, "-quota-admissions"}, {0, 0, 100001, 0, "-quota-admissions"},
		{0, 0, 0, -1, "-quota-queries"}, {0, 0, 0, 65, "-quota-queries"}} {
		if _, err := quotaConfig(c.jobs, c.results, c.admissions, c.queries); err == nil || !strings.Contains(err.Error(), c.flag) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
}

// -max-in-flight (S04-G): zero is the default, 128; 1 and 4096 are the ends; a value outside them stops the start and names the flag.
func TestMaxInFlightFlagStopsTheStartOutsideItsBounds(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 128}, {1, 1}, {4096, 4096}, {200, 200}} {
		if got, err := maxInFlightConfig(c.in); err != nil || got != c.want {
			t.Fatalf("%d: %d %v", c.in, got, err)
		}
	}
	for _, v := range []int{-1, 4097, 1 << 30} {
		if _, err := maxInFlightConfig(v); err == nil || !strings.Contains(err.Error(), "-max-in-flight") {
			t.Fatalf("%d: %v", v, err)
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

// The demo's cursor key ring (S04-H): one token derives the key it always did; previous tokens are the older keys, newest first, at most 3;
// a refusal names the variable and the entry, never a token.
func TestDemoCursorKeysAreDerivedFromTheTokensAndNeverNamedInARefusal(t *testing.T) {
	const current = "current-demo-token-0123456789abcdef-CURRENT"
	const older1, older2, older3, older4 = "older-demo-token-1-0123456789abcdef-OLDER1", "older-demo-token-2-0123456789abcdef-OLDER2", "older-demo-token-3-0123456789abcdef-OLDER3", "older-demo-token-4-0123456789abcdef-OLDER4"
	// A single token: the key of S01..S04-G, byte for byte (cursors issued by the previous binary stay valid).
	sum := sha256.Sum256([]byte("reporting-demo-cursor-v1:" + current))
	ring, err := demoCursorKeys(current, "")
	if err != nil || len(ring) != 1 || !bytes.Equal(ring[0], sum[:]) {
		t.Fatalf("the single-token key changed: %v", err)
	}
	ring, err = demoCursorKeys(current, older1+","+older2+","+older3)
	if err != nil || len(ring) != 4 || !bytes.Equal(ring[0], sum[:]) {
		t.Fatalf("%d keys, %v", len(ring), err)
	}
	for i, token := range []string{older1, older2, older3} {
		want := sha256.Sum256([]byte("reporting-demo-cursor-v1:" + token))
		if !bytes.Equal(ring[i+1], want[:]) {
			t.Errorf("previous token %d is not at position %d, newest first", i+1, i+2)
		}
	}
	for name, c := range map[string]struct{ current, previous, want string }{
		"no current token":        {"", "", "REPORTING_DEMO_TOKEN must contain at least 32 characters"},
		"a short current token":   {"short", "", "REPORTING_DEMO_TOKEN must contain at least 32 characters"},
		"four previous tokens":    {current, older1 + "," + older2 + "," + older3 + "," + older4, "REPORTING_DEMO_PREVIOUS_TOKENS holds at most 3 tokens"},
		"a short previous token":  {current, older1 + ",short-SECRET", "REPORTING_DEMO_PREVIOUS_TOKENS entry 2 must contain at least 32 characters"},
		"an empty entry":          {current, older1 + ",", "REPORTING_DEMO_PREVIOUS_TOKENS entry 2 must contain at least 32 characters"},
		"the current token again": {current, older1 + "," + current, "REPORTING_DEMO_PREVIOUS_TOKENS entry 2 repeats another token"},
		"a repeated previous":     {current, older1 + "," + older1, "REPORTING_DEMO_PREVIOUS_TOKENS entry 2 repeats another token"},
	} {
		_, err := demoCursorKeys(c.current, c.previous)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", name, err, c.want)
			continue
		}
		for _, secret := range []string{current, older1, older2, older3, older4, "short-SECRET"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the refusal carries a token: %q", name, err)
			}
		}
	}
}

// lockedBuffer is written by the child's copier goroutine while the test polls it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// demoHelperEnv makes this test binary run the demo's main, so that its real log output can be read: the child process of
// TestTheDemosLogCarriesNoTokenAndNoKey.
const demoHelperEnv = "REPORTING_DEMO_TEST_AS_MAIN"

// TestMain makes the test binary run the demo's main when the child process of TestTheDemosLogCarriesNoTokenAndNoKey asks for it.
func TestMain(m *testing.M) {
	if os.Getenv(demoHelperEnv) == "1" {
		os.Args = []string{"reporting-demo", "-profile=contract-simulator", "-memory", "-listen=127.0.0.1:0", "-sweep-interval=0"}
		main() // exits 1 on a refusal; returns on the signal
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// S04-H H5: the demo's output, started with a known token and a known previous token, carries neither, nor a key derived from them; a
// start with a refused previous-token list says why without them. The log says how many cursor keys the ring holds.
func TestTheDemosLogCarriesNoTokenAndNoKey(t *testing.T) {
	const current, previous = "child-demo-token-CURRENT-0123456789abcdef", "child-demo-token-PREVIOUS-0123456789abcdef"
	run := func(previousTokens string, stop bool) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.count=1")
		cmd.Env = append(os.Environ(), demoHelperEnv+"=1", "REPORTING_DEMO_TOKEN="+current, previousTokensEnv+"="+previousTokens)
		out := &lockedBuffer{}
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		if stop {
			deadline := time.After(20 * time.Second)
			for !strings.Contains(out.String(), "listening at") {
				select {
				case err := <-done:
					return out.String(), err
				case <-deadline:
					_ = cmd.Process.Kill()
					t.Fatalf("the demo did not start: %s", out.String())
				case <-time.After(20 * time.Millisecond):
				}
			}
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		err := <-done
		return out.String(), err
	}
	currentKey, previousKey := sha256.Sum256([]byte("reporting-demo-cursor-v1:"+current)), sha256.Sum256([]byte("reporting-demo-cursor-v1:"+previous))
	forbidden := []string{current, previous, string(currentKey[:]), string(previousKey[:]), hex.EncodeToString(currentKey[:]), hex.EncodeToString(previousKey[:]),
		base64.StdEncoding.EncodeToString(currentKey[:]), base64.RawURLEncoding.EncodeToString(currentKey[:]), base64.StdEncoding.EncodeToString(previousKey[:]), base64.RawURLEncoding.EncodeToString(previousKey[:])}
	output, err := run(previous, true)
	if err != nil || !strings.Contains(output, "cursor-keys=2;") {
		t.Fatalf("the demo with a previous token: %v\n%s", err, output)
	}
	if only, err := run("", true); err != nil || !strings.Contains(only, "cursor-keys=1;") {
		t.Fatalf("the demo with one token: %v\n%s", err, only)
	} else {
		output += only
	}
	for _, bad := range []string{"short", previous + "," + previous, previous + ",b,c,d", previous + ",child-demo-token-FOURTH-0123456789abcdef,child-demo-token-FIFTH-0123456789abcdefg,child-demo-token-SIXTH-0123456789abcdef"} {
		out, err := run(bad, false)
		if err == nil || !strings.Contains(out, previousTokensEnv) {
			t.Errorf("a refused list %q started or does not say why: %v\n%s", bad, err, out)
		}
		output += out
	}
	for _, leak := range forbidden {
		if strings.Contains(output, leak) {
			t.Errorf("the demo's output carries a secret (%d bytes of it)", len(leak))
		}
	}
}

// S12-D: the colleague token is a second person of the same tenant, refused when it repeats the demo token or is short, and absent by default.
func TestTheColleagueTokenIsASecondPersonOfTheDemosTenant(t *testing.T) {
	p := r.Principal{Identity: r.Identity{Tenant: "platform_demo", Issuer: "urn:idp:contract-demo", Subject: "user_a"}, Integrations: []string{"finance"}}
	first, second := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, bad := range []string{first, "short"} {
		if _, e := demoAuthenticate(first, bad, p); e == nil || strings.Contains(e.Error(), bad) {
			t.Fatalf("colleague token %q was not refused without naming it: %v", bad, e)
		}
	}
	ask := func(auth func(*http.Request) (r.Principal, error), token string) (r.Principal, error) {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return auth(req)
	}
	alone, e := demoAuthenticate(first, "", p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := ask(alone, second); e == nil {
		t.Fatal("without a colleague token a second token was accepted")
	}
	auth, e := demoAuthenticate(first, second, p)
	if e != nil {
		t.Fatal(e)
	}
	a, e := ask(auth, first)
	if e != nil || a.Subject != "user_a" {
		t.Fatalf("demo token: %v %v", a.Identity, e)
	}
	b, e := ask(auth, second)
	if e != nil || b.Subject != colleagueSubject || b.Tenant != p.Tenant || b.Issuer != p.Issuer || len(b.Integrations) != 1 {
		t.Fatalf("colleague token: %v %v", b, e)
	}
	if _, e := ask(auth, strings.Repeat("c", 32)); e == nil {
		t.Fatal("an unknown token was accepted")
	}
}
