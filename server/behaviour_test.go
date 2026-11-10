package server

import (
	"bytes"
	"context"
	"fmt"
	"idp.local/reporting/storage/postgres"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// The sweep loop logs counts, or the code of its failure, and nothing of the error.
func TestSweepLoopLogsCountsAndCodes(t *testing.T) {
	w := newWorld(t)
	w.cfg.Retention.SweepInterval = 3600
	store := NewMemoryStoreForTests(time.Now)
	tick := make(chan time.Time)
	hooks := &Hooks{Swept: make(chan struct{}, 4)}
	s := startWorld(t, w, WithStoreForTests(store), WithHooksForTests(hooks), WithSweepTickerForTests(func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} }))
	tick <- time.Time{}
	<-hooks.Swept
	store.SetSweepError(fmt.Errorf("%w: connection postgresql://u:%s@h", r.ErrDenied, testDSNPass))
	tick <- time.Time{}
	<-hooks.Swept
	s.cancel()
	<-s.Done()
	sweeps, failed := s.log.events(t, "sweep"), s.log.events(t, "sweep_failed")
	if len(sweeps) != 1 || sweeps[0]["artifacts"] != float64(0) || len(failed) != 1 || failed[0]["code"] != "AUTH_SCOPE_DENIED" {
		t.Fatalf("%v %v", sweeps, failed)
	}
	if strings.Contains(s.log.String(), testDSNPass) {
		t.Error("the sweep's error reached the log")
	}
}

// A sweep loop that is off by default does not run, and the interval's bounds are the configuration's.
func TestNoSweepLoopWithoutAnInterval(t *testing.T) {
	w := newWorld(t)
	called := false
	s := startWorld(t, w, WithSweepTickerForTests(func(time.Duration) (<-chan time.Time, func()) { called = true; return nil, func() {} }))
	s.cancel()
	<-s.Done()
	if called {
		t.Error("a sweep ticker was made with retention.sweep_interval at zero")
	}
}

// A worker whose job fails logs the platform's code and not the error, which may carry what the source product said.
func TestWorkerFailuresAreLoggedByCodeOnly(t *testing.T) {
	w, g := gatedWorld(t)
	g.fail = fmt.Errorf("%w: the source said postgresql://u:%s@h", r.ErrUnavailable, testDSNPass)
	hooks := newHooks()
	s := startGated(t, w, g, hooks, WithStoreForTests(NewMemoryStoreForTests(time.Now)))
	token := mint(t, w.keys[0], claims{})
	id := submitSnapshot(t, s, token, "one")
	if j := s.waitJob(t, token, id); j.State != "failed" {
		t.Fatalf("%+v", j)
	}
	s.cancel()
	<-s.Done()
	failures := s.log.events(t, "worker_failed")
	if len(failures) == 0 || failures[0]["code"] != "AUTHORITY_UNAVAILABLE" {
		t.Fatalf("%v", failures)
	}
	if strings.Contains(s.log.String(), testDSNPass) {
		t.Error("the error reached the log")
	}
}

// A panic in a handler is the net/http server's to recover; what it would print (the panic's value, the client's address) is not logged.
func TestAPanicInAHandlerIsLoggedWithoutItsValue(t *testing.T) {
	w, g := gatedWorld(t)
	g.panicWith = "PANIC-VALUE-Zq7 " + testDSNPass
	hooks := newHooks()
	s := startGated(t, w, g, hooks, WithStoreForTests(NewMemoryStoreForTests(time.Now)))
	token := mint(t, w.keys[0], claims{})
	req, _ := http.NewRequest("POST", s.base+api+"/queries", strings.NewReader(mustJSON(contractsim.FinancialQuery())))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := s.client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("the request was answered %d", resp.StatusCode)
	}
	if s.inflight() != 0 || s.requests.count() != 0 {
		t.Errorf("slots held after a panic: %d %d", s.inflight(), s.requests.count())
	}
	s.cancel()
	<-s.Done()
	text := s.log.String()
	if strings.Contains(text, "PANIC-VALUE") || strings.Contains(text, testDSNPass) || strings.Contains(text, "127.0.0.1:") && strings.Contains(text, "panic serving") {
		t.Errorf("the panic reached the log: %s", text)
	}
	if len(s.log.events(t, "http_server_error")) == 0 {
		t.Errorf("no http_server_error line: %s", text)
	}
	var seen500 bool
	for _, a := range s.log.events(t, "access") {
		if a["status"] == float64(500) && a["route"] == "POST "+api+"/queries" {
			seen500 = true
		}
	}
	if !seen500 {
		t.Error("the aborted request has no access line with a 500")
	}
}

func TestCheckConfigPrintsCountsAndNoValue(t *testing.T) {
	w := newWorld(t)
	path := writeConfigFile(t, w)
	var out, errs bytes.Buffer
	code := Execute(context.Background(), []string{"reporting-server", "check-config", "-config", path}, Registry{}, w.getenv, &out, &errs, nil)
	if code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	text := out.String()
	for _, want := range []string{"configuration: ok", "connections: 1", "cursor keys: 1", "trusted kids: 1", "bindings: 1", "environment: REPORTING_STORE_DSN set", "certificate: valid"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
	for _, secret := range append(secretsOf(w, ""), "kid-a") {
		if secret != "" && secret != "Bearer " && strings.Contains(strings.ToLower(text+errs.String()), strings.ToLower(secret)) {
			t.Errorf("check-config prints %q", secret)
		}
	}
	// The first problem, and only a description of it.
	writeFile(t, w.cursorFile, []byte(cursorKeyHex), 0o644)
	out.Reset()
	errs.Reset()
	code = Execute(context.Background(), []string{"reporting-server", "check-config", "-config", path}, Registry{}, w.getenv, &out, &errs, nil)
	if code != 1 || !strings.Contains(errs.String(), "cursor_key_files[0]: the file's mode must be 0600 or 0400") || out.Len() != 0 {
		t.Fatalf("%d %q %q", code, errs.String(), out.String())
	}
	if strings.Contains(errs.String(), cursorKeyHex) || strings.Contains(errs.String(), w.dir) {
		t.Errorf("%q", errs.String())
	}
	writeFile(t, w.cursorFile, []byte(cursorKeyHex), 0o600)
	// An adapter name the binary does not register is a problem when the binary registers some.
	out.Reset()
	errs.Reset()
	code = Execute(context.Background(), []string{"reporting-server", "check-config", "-config", path}, Registry{"other": w.registry()["sim"]}, w.getenv, &out, &errs, nil)
	if code != 1 || !strings.Contains(errs.String(), "connections[0].adapter") {
		t.Fatalf("%d %q", code, errs.String())
	}
	// A directory that is not private.
	w.cfg.LayoutDir = t.TempDir()
	if err := os.Chmod(w.cfg.LayoutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = writeConfigFile(t, w)
	errs.Reset()
	if code = Execute(context.Background(), []string{"reporting-server", "check-config", "-config", path}, Registry{}, w.getenv, &out, &errs, nil); code != 1 || !strings.Contains(errs.String(), "layout_directory") {
		t.Fatalf("%d %q", code, errs.String())
	}
}

func TestCommandUsageAndVersion(t *testing.T) {
	var out, errs bytes.Buffer
	if c := Execute(context.Background(), []string{"reporting-server", "version"}, nil, nil, &out, &errs, nil); c != 0 || !strings.HasPrefix(out.String(), "reporting-server ") || !strings.Contains(out.String(), fmt.Sprintf(" schema %d", postgres.LatestVersion())) {
		t.Fatalf("%d %q %q", c, out.String(), errs.String())
	}
	for name, args := range map[string][]string{
		"no command":       {"reporting-server"},
		"unknown":          {"reporting-server", "serve"},
		"no config":        {"reporting-server", "run"},
		"extra argument":   {"reporting-server", "run", "-config", "/x", "extra"},
		"a flag for a dsn": {"reporting-server", "run", "-config", "/x", "-dsn", testDSN},
		"a missing file":   {"reporting-server", "run", "-config", "/nonexistent/config.json"},
	} {
		out.Reset()
		errs.Reset()
		if c := Execute(context.Background(), args, Registry{}, nil, &out, &errs, nil); c != 1 {
			t.Errorf("%s: exit %d", name, c)
		}
		if strings.Contains(out.String()+errs.String(), testDSNPass) {
			t.Errorf("%s: the output carries the secret: %q", name, errs.String())
		}
	}
}

// The shipped command registers the platform's generic source adapter and nothing else (S19-A): no simulator, no product's code.
func TestShippedCommandRegistersOnlyTheHTTPAdapter(t *testing.T) {
	src, err := os.ReadFile("../cmd/reporting-server/main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "server.Main(os.Args, server.DefaultRegistry())") || strings.Contains(text, "adapters/") || strings.Contains(text, "contractsim") {
		t.Errorf("the shipped command registers something else:\n%s", text)
	}
	reg, err := os.ReadFile("registry.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reg), "contractsim") || len(DefaultRegistry()) != 1 || DefaultRegistry()[HTTPAdapter] == nil {
		t.Error("the shipped registry is not the http adapter alone")
	}
}
