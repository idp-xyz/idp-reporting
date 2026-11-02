package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func (s *started) status(t *testing.T, token string) int {
	t.Helper()
	status, _, _ := s.do(t, "GET", api+"/session", token, nil)
	return status
}

// A rotation of the issuer's keys and a change of the bindings take effect without a restart: the overlap accepts both keys, the removal
// refuses the old one, a new binding is accepted and a removed one refused. A broken file leaves the previous configuration in force and is
// logged by its code.
func TestReloadAppliesRotatedKeysAndBindingsAndKeepsTheOldOnesOnFailure(t *testing.T) {
	snap := goroutines()
	w := newWorld(t)
	s := startWorld(t, w)
	k1, k2 := w.keys[0], w.keys[1]
	old, next := mint(t, k1, claims{}), mint(t, k2, claims{})
	if s.status(t, old) != 200 || s.status(t, next) != 401 {
		t.Fatal("the starting configuration trusts the first key only")
	}
	// Nothing changes until Reload: the file is rewritten and the server still trusts what it read.
	replaceFile(t, w.jwks, jwksOf(k1, k2), 0o644)
	if s.status(t, next) != 401 {
		t.Fatal("a file that is rewritten takes effect without a reload")
	}
	// The overlap: both keys.
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if s.status(t, old) != 200 || s.status(t, next) != 200 {
		t.Fatal("the overlap does not accept both keys")
	}
	// The removal: the old key is refused from the next authentication.
	replaceFile(t, w.jwks, jwksOf(k2), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if s.status(t, old) != 401 || s.status(t, next) != 200 {
		t.Fatal("the removed key is still trusted, or the new one is not")
	}
	// Bindings: a second subject is added, the first removed.
	other := mint(t, k2, claims{Sub: "second-subject"})
	if s.status(t, other) != 401 {
		t.Fatal("an unbound subject")
	}
	replaceFile(t, w.binds, bindingsJSON(bindingJSON{"second-subject", testClient, testTenant, []string{w.finance}}), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if s.status(t, other) != 200 || s.status(t, next) != 401 {
		t.Fatal("the bindings were not replaced")
	}
	// Failures: each keeps what is in force, returns its code, and logs the code and nothing of the file.
	const planted = "PLANTED-CONTENT-Zq7"
	for name, c := range map[string]struct {
		file    string
		content string
		mode    os.FileMode
		code    string
	}{
		"a jwks the profile refuses":      {w.jwks, `{"keys":[{"kty":"RSA","kid":"` + planted + `"}]}`, 0o644, CodeIssuerConfigRefused},
		"a jwks that is not JSON":         {w.jwks, planted, 0o644, CodeIssuerConfigRefused},
		"a jwks anyone can write":         {w.jwks, string(jwksOf(k2)), 0o666, CodeIssuerFileUnreadable},
		"bindings that are not a list":    {w.binds, `{"subject":"` + planted + `"}`, 0o644, CodeIssuerFileInvalid},
		"bindings with an unknown field":  {w.binds, `[{"subject":"x","client_id":"c","tenant":"t","integrations":["i"],"` + planted + `":1}]`, 0o644, CodeIssuerFileInvalid},
		"bindings that repeat a binding":  {w.binds, string(bindingsJSON(bindingJSON{"x", "c", "t", []string{"i"}}, bindingJSON{"x", "c", "t2", []string{"i"}})), 0o644, CodeIssuerConfigRefused},
		"bindings of another user's mode": {w.binds, `[]`, 0o662, CodeIssuerFileUnreadable},
	} {
		goodJWKS, goodBinds := mustRead(t, w.jwks), mustRead(t, w.binds)
		replaceFile(t, c.file, []byte(c.content), c.mode)
		err := s.Reload()
		var re *ReloadError
		if !errors.As(err, &re) || re.Code != c.code {
			t.Errorf("%s: %v, want code %s", name, err, c.code)
		}
		if err != nil && strings.Contains(err.Error(), planted) {
			t.Errorf("%s: the error carries the file", name)
		}
		if s.status(t, other) != 200 || s.status(t, old) != 401 {
			t.Errorf("%s: the previous configuration is no longer in force", name)
		}
		replaceFile(t, w.jwks, goodJWKS, 0o644)
		replaceFile(t, w.binds, goodBinds, 0o644)
	}
	// A missing file too.
	if err := os.Remove(w.jwks); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err == nil || s.status(t, other) != 200 {
		t.Errorf("a missing file: %v", err)
	}
	replaceFile(t, w.jwks, jwksOf(k2), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	// The log: outcomes by code, the kids after a success, nothing of the files.
	s.cancel()
	<-s.Done()
	var ok, refused int
	for _, line := range s.log.events(t, "reload") {
		switch line["outcome"] {
		case "ok":
			ok++
			if kids, _ := line["trusted_kids"].([]any); len(kids) == 0 {
				t.Errorf("a successful reload does not name the kids: %v", line)
			}
		case "refused":
			refused++
			if line["code"] == nil || line["level"] != "warn" {
				t.Errorf("%v", line)
			}
		}
	}
	if ok != 4 || refused != 8 {
		t.Errorf("%d successful and %d refused reloads logged", ok, refused)
	}
	if strings.Contains(s.log.String(), planted) {
		t.Error("the log carries a file's content")
	}
	if kids := s.log.events(t, "reload")[0]["trusted_kids"]; kids == nil {
		t.Error("no kids")
	}
	noNewGoroutines(t, snap)
}

// SIGHUP is the command's trigger for the reload: the channel Execute is given reloads, and the next authentication sees the change.
func TestSIGHUPReloadsThroughTheCommand(t *testing.T) {
	snap := goroutines()
	w := newWorld(t)
	cfgPath := writeConfigFile(t, w)
	hup := make(chan os.Signal)
	ctx, cancel := context.WithCancel(context.Background())
	lb := &logBuffer{}
	hooks := &Hooks{Started: make(chan *Server, 1), Reloaded: make(chan struct{}, 4)}
	code := make(chan int, 1)
	go func() {
		code <- Execute(ctx, []string{"reporting-server", "run", "-config", cfgPath}, w.registry(), w.getenv, io.Discard, lb, hup, WithStoreForTests(NewMemoryStoreForTests(time.Now)), WithHooksForTests(hooks))
	}()
	srv := <-hooks.Started
	st := &started{Server: srv, client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: w.cert}}}, base: "https://" + srv.Addr()}
	next := mint(t, w.keys[1], claims{})
	if st.status(t, next) != 401 {
		t.Fatal("trusted before the reload")
	}
	replaceFile(t, w.jwks, jwksOf(w.keys[0], w.keys[1]), 0o644)
	hup <- syscall.SIGHUP
	<-hooks.Reloaded
	if st.status(t, next) != 200 {
		t.Fatal("not trusted after SIGHUP")
	}
	// A broken file on the next SIGHUP changes nothing.
	replaceFile(t, w.jwks, []byte("{"), 0o644)
	hup <- syscall.SIGHUP
	<-hooks.Reloaded
	if st.status(t, next) != 200 {
		t.Fatal("a broken file replaced the configuration")
	}
	cancel()
	if c := <-code; c != 0 {
		t.Fatalf("exit code %d\n%s", c, lb.String())
	}
	st.client.CloseIdleConnections()
	noNewGoroutines(t, snap)
}

// writeConfigFile writes the world's configuration as the JSON file an operator writes.
func writeConfigFile(t testing.TB, w *world) string {
	t.Helper()
	b, err := json.Marshal(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var prune func(any) any
	prune = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if c == nil {
					delete(x, k)
				} else {
					x[k] = prune(c)
				}
			}
		case []any:
			for i := range x {
				x[i] = prune(x[i])
			}
		}
		return v
	}
	prune(doc)
	b, _ = json.Marshal(doc)
	path := filepath.Join(w.dir, "config.json")
	writeFile(t, path, b, 0o644)
	return path
}
