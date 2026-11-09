package server

import (
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/identity/signin"
)

func signInDoc() map[string]any {
	return map[string]any{"authorization_endpoint": "https://idp.example/oauth2/authorize", "token_endpoint": "https://idp.example/oauth2/token", "client_id": "reporting-console", "redirect_uri": "https://reporting.example/console/", "scope": "openid reporting.read"}
}

func TestConfigSignInSection(t *testing.T) {
	d := validDoc()
	d["sign_in"] = signInDoc()
	c, err := ParseConfig(encode(t, d))
	if err != nil {
		t.Fatal(err)
	}
	if c.SignIn == nil || c.SignIn.ClientID != "reporting-console" || c.SignIn.PasteFallback || c.SignIn.Timeout != 0 {
		t.Fatalf("%+v", c.SignIn)
	}
	// Loopback HTTP, a CA file, a timeout and the paste form, each where it is allowed.
	s := signInDoc()
	s["token_endpoint"], s["redirect_uri"], s["loopback_http"], s["timeout"], s["paste_fallback"] = "http://127.0.0.1:9000/token", "http://127.0.0.1:8080/console/", true, 30, true
	d["sign_in"] = s
	if _, err := ParseConfig(encode(t, d)); err != nil {
		t.Fatal(err)
	}
	const secret = "TOPSECRET-value-Zq7"
	with := func(f func(map[string]any)) []byte {
		d := validDoc()
		s := signInDoc()
		f(s)
		d["sign_in"] = s
		return encode(t, d)
	}
	for name, doc := range map[string][]byte{
		"a client secret in the file":     with(func(s map[string]any) { s["client_secret"] = secret }),
		"a camelCase secret":              with(func(s map[string]any) { s["clientSecret"] = secret }),
		"an unknown field":                with(func(s map[string]any) { s["issuer"] = secret }),
		"http without loopback_http":      with(func(s map[string]any) { s["token_endpoint"] = "http://127.0.0.1:9/" + secret }),
		"http on a public address":        with(func(s map[string]any) { s["token_endpoint"], s["loopback_http"] = "http://192.0.2.1/"+secret, true }),
		"a redirect URI elsewhere":        with(func(s map[string]any) { s["redirect_uri"] = "https://evil.example/" + secret }),
		"a redirect URI with a query":     with(func(s map[string]any) { s["redirect_uri"] = "https://reporting.example/console/?next=" + secret }),
		"an authorization endpoint query": with(func(s map[string]any) { s["authorization_endpoint"] = "https://idp.example/a?" + secret }),
		"no client id":                    with(func(s map[string]any) { delete(s, "client_id") }),
		"no scope":                        with(func(s map[string]any) { delete(s, "scope") }),
		"a timeout over a minute":         with(func(s map[string]any) { s["timeout"] = 61 }),
		"a negative timeout":              with(func(s map[string]any) { s["timeout"] = -1 }),
		"a relative CA file":              with(func(s map[string]any) { s["ca_file"] = "ca.pem" }),
		"a CA file for http": with(func(s map[string]any) {
			s["token_endpoint"], s["loopback_http"], s["ca_file"] = "http://127.0.0.1:9/token", true, "/etc/reporting/ca.pem"
		}),
	} {
		_, err := ParseConfig(doc)
		var ce *ConfigError
		if !errors.As(err, &ce) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "example") {
			t.Errorf("%s: the refusal carries a value: %q", name, err)
		}
		if strings.Contains(name, "secret") && !strings.Contains(err.Error(), "named like a secret") {
			t.Errorf("%s: %q", name, err)
		}
	}
}

// A server with sign_in: the console learns the public part, the code is exchanged at a fake provider over TLS (verified against the
// configured CA file), the token it answers is the world's own and works as a pasted one, a token for another audience is never handed out,
// and the log has the outcomes and nothing of the code, the verifier or the token.
func TestServerSignInWithAFakeProvider(t *testing.T) {
	w := newWorld(t)
	good := mint(t, w.keys[0], claims{})
	foreign := mint(t, w.keys[0], claims{Aud: "another-api"})
	var answer atomic.Value
	answer.Store(good)
	var basic atomic.Value
	basic.Store("")
	provider := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		_, pass, _ := req.BasicAuth()
		basic.Store(pass)
		if req.PostForm.Get("code") == "code-refused" {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(400)
			_, _ = rw.Write([]byte(`{"error":"invalid_grant","error_description":"PROVIDER-TEXT-Zq7"}`))
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"access_token": answer.Load(), "token_type": "Bearer", "expires_in": 300})
	}))
	defer provider.Close()
	ca := filepath.Join(w.dir, "provider-ca.pem")
	writeFile(t, ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0o644)
	w.cfg.SignIn = &SignInConfig{AuthorizationEndpoint: provider.URL + "/authorize", TokenEndpoint: provider.URL + "/token", ClientID: testClient, RedirectURI: "https://reporting.example/console/", Scope: "openid " + testScope, CAFile: ca}
	s := startWorld(t, w)

	status, h, body := s.do(t, "GET", "/api/reporting/v1/sign-in", "", nil)
	if status != 200 || h.Get("Cache-Control") != "no-store" || strings.Contains(string(body), "/token") {
		t.Fatalf("%d %s", status, body)
	}
	var public signin.Public
	if err := json.Unmarshal(body, &public); err != nil || public.AuthorizationEndpoint != provider.URL+"/authorize" || public.CodeChallengeMethod != "S256" {
		t.Fatalf("%+v %v", public, err)
	}
	const verifier = "verifier-0123456789-0123456789-0123456789-abcdef"
	const nonce = "nonce-0123456789-abcdefgh"
	in := func(code string) map[string]string {
		return map[string]string{"code": code, "code_verifier": verifier, "nonce": nonce}
	}
	status, _, body = s.do(t, "POST", "/api/reporting/v1/sign-in/token", "", in("code-good-Zq7"))
	var token signin.Token
	if status != 200 || json.Unmarshal(body, &token) != nil || token.AccessToken != good || token.ExpiresIn != 300 {
		t.Fatalf("%d %s", status, body)
	}
	if status, _, _ = s.do(t, "GET", "/api/reporting/v1/session", token.AccessToken, nil); status != 200 {
		t.Fatalf("the token does not open a session: %d", status)
	}
	if status, _, body = s.do(t, "POST", "/api/reporting/v1/sign-in/token", "", in("code-good-Zq7")); status != 409 || !strings.Contains(string(body), "SIGN_IN_REPLAYED") {
		t.Fatalf("replay: %d %s", status, body)
	}
	if status, _, body = s.do(t, "POST", "/api/reporting/v1/sign-in/token", "", in("code-refused")); status != 400 || strings.TrimSpace(string(body)) != `{"error":"SIGN_IN_REFUSED"}` {
		t.Fatalf("refused: %d %s", status, body)
	}
	answer.Store(foreign)
	if status, _, body = s.do(t, "POST", "/api/reporting/v1/sign-in/token", "", in("code-foreign")); status != 403 || strings.TrimSpace(string(body)) != `{"error":"SIGN_IN_TOKEN_REFUSED"}` {
		t.Fatalf("foreign: %d %s", status, body)
	}
	if basic.Load() != "" {
		t.Error("a public client sent a secret")
	}
	// The log: the outcomes, the routes, and none of the values.
	var outcomes []string
	for _, e := range s.log.events(t, "sign_in") {
		outcomes = append(outcomes, e["outcome"].(string))
		if len(e) != 4 {
			t.Errorf("a sign_in line has other fields: %v", e)
		}
	}
	if strings.Join(outcomes, ",") != "ok,replayed,provider_refused,ok" {
		t.Errorf("outcomes %v", outcomes)
	}
	if start := s.log.events(t, "start"); len(start) != 1 || start[0]["sign_in"] != true {
		t.Errorf("start %v", start)
	}
	log := s.log.String()
	for _, v := range []string{good, foreign, verifier, nonce, "code-good-Zq7", "PROVIDER-TEXT-Zq7", "invalid_grant"} {
		if strings.Contains(log, v) {
			t.Errorf("the log carries %q", v)
		}
	}
}

// A confidential client's secret is the environment's, sent as HTTP Basic; a secret the client refuses stops the start without its value.
func TestServerSignInConfidentialClient(t *testing.T) {
	w := newWorld(t)
	w.cfg.SignIn = &SignInConfig{AuthorizationEndpoint: "https://idp.example/authorize", TokenEndpoint: "https://idp.example/token", ClientID: testClient, RedirectURI: "https://reporting.example/console/", Scope: "openid"}
	const bad = "has a space Zq7"
	_, err := Start(t.Context(), w.cfg, w.registry(), func(name string) string {
		if name == signin.ClientSecretEnv {
			return bad
		}
		return w.getenv(name)
	}, &logBuffer{}, WithStoreForTests(NewMemoryStoreForTests(time.Now)))
	var se *StartError
	if !errors.As(err, &se) || se.Step != StepSignIn || strings.Contains(err.Error(), bad) {
		t.Fatalf("%v", err)
	}
}
