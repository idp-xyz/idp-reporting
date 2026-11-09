package signin_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/identity/signin"
)

// The known values of the tests: none of them may appear in an error's text or an outcome word.
const (
	clientID    = "reporting-console"
	redirectURI = "https://reporting.example.test/console/"
	providerMsg = "PROVIDER-TEXT-Zq7-never-shown"
	accessToken = "eyJhbGciOiJSUzI1NiJ9.ACCESS-Zq7.sig"
	goodNonce   = "nonce-0123456789abcdefghij"
)

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func verifierOf(c byte) string { return strings.Repeat(string(c), 43) }

// provider is a fake OpenID provider's token endpoint: it knows the codes it issued, each with its S256 challenge and the redirect URI it was
// issued for, redeems a code once, and answers as a provider does. answer, when set, replaces its answer.
type provider struct {
	*httptest.Server
	mu       sync.Mutex
	codes    map[string]issued
	requests atomic.Int64
	last     http.Request
	form     map[string][]string
	answer   func(w http.ResponseWriter, req *http.Request) bool
	ca       string
}

type issued struct {
	challenge, redirect, nonce string
	used                       bool
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{codes: map[string]issued{}}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	p.ca = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// issue makes a code for this verifier, as the authorization endpoint would.
func (p *provider) issue(code, verifier, nonce string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes[code] = issued{challenge: challenge(verifier), redirect: redirectURI, nonce: nonce}
}

func refusal(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": providerMsg})
}

func idToken(nonce string) string {
	payload, _ := json.Marshal(map[string]any{"iss": "https://idp.example.test", "aud": clientID, "sub": "someone", "nonce": nonce})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln"
}

func (p *provider) serve(w http.ResponseWriter, req *http.Request) {
	p.requests.Add(1)
	if req.URL.Path != "/oauth2/token" || req.Method != http.MethodPost {
		http.NotFound(w, req)
		return
	}
	if err := req.ParseForm(); err != nil {
		refusal(w, 400, "invalid_request")
		return
	}
	p.mu.Lock()
	p.last = *req
	p.form = req.PostForm
	answer := p.answer
	p.mu.Unlock()
	if answer != nil && answer(w, req) {
		return
	}
	f := req.PostForm
	if f.Get("grant_type") != "authorization_code" || f.Get("client_id") != clientID {
		refusal(w, 401, "invalid_client")
		return
	}
	p.mu.Lock()
	code, known := p.codes[f.Get("code")]
	ok := known && !code.used && code.redirect == f.Get("redirect_uri") && code.challenge == challenge(f.Get("code_verifier"))
	if known {
		code.used = true // RFC 6749 §4.1.2: a code is used once; a failed attempt spends it too
		p.codes[f.Get("code")] = code
	}
	p.mu.Unlock()
	if !ok {
		refusal(w, 400, "invalid_grant")
		return
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 900, "id_token": idToken(code.nonce), "refresh_token": "REFRESH-Zq7", "scope": "openid"})
}

func config(p *provider) signin.Config {
	return signin.Config{AuthorizationEndpoint: p.URL + "/oauth2/authorize", TokenEndpoint: p.URL + "/oauth2/token", ClientID: clientID, RedirectURI: redirectURI, Scope: "openid reporting.read", CA: p.ca}
}

// client builds a client on the fake provider and records the outcomes it reports.
func client(t *testing.T, c signin.Config) (*signin.Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	outcomes := &[]string{}
	c.Outcome = func(o string) { mu.Lock(); *outcomes = append(*outcomes, o); mu.Unlock() }
	cl, err := signin.New(c)
	if err != nil {
		t.Fatal(err)
	}
	return cl, outcomes
}

func clean(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, v := range []string{providerMsg, accessToken, "REFRESH-Zq7", "invalid_grant", "invalid_client", "code-", verifierOf('a')} {
		if strings.Contains(err.Error(), v) {
			t.Errorf("the error %q carries %q", err, v)
		}
	}
}

func TestAGoodCodeIsExchangedOnce(t *testing.T) {
	p := newProvider(t)
	c, outcomes := client(t, config(p))
	p.issue("code-good", verifierOf('a'), goodNonce)
	token, err := c.Exchange(context.Background(), "code-good", verifierOf('a'), goodNonce)
	if err != nil || token.AccessToken != accessToken || token.ExpiresIn != 900 {
		t.Fatalf("%+v %v", token, err)
	}
	// What the provider was sent: the form of RFC 6749 §4.1.3 with the verifier, the configured redirect URI, a public client's id.
	want := map[string]string{"grant_type": "authorization_code", "code": "code-good", "redirect_uri": redirectURI, "client_id": clientID, "code_verifier": verifierOf('a')}
	if len(p.form) != len(want) {
		t.Errorf("the form has %d fields: %v", len(p.form), p.form)
	}
	for k, v := range want {
		if got := p.form[k]; len(got) != 1 || got[0] != v {
			t.Errorf("%s = %v, want %q", k, got, v)
		}
	}
	if p.last.Header.Get("Authorization") != "" || p.last.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("headers %v", p.last.Header)
	}
	// The same code again is refused here, without asking the provider.
	before := p.requests.Load()
	if _, err := c.Exchange(context.Background(), "code-good", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrReplayed) {
		t.Fatalf("replay: %v", err)
	}
	if p.requests.Load() != before {
		t.Error("a replay reached the provider")
	}
	if strings.Join(*outcomes, ",") != "ok,replayed" {
		t.Errorf("outcomes %v", *outcomes)
	}
}

func TestRefusalsOfTheProvider(t *testing.T) {
	p := newProvider(t)
	c, outcomes := client(t, config(p))
	// A wrong verifier: the challenge does not match.
	p.issue("code-1", verifierOf('a'), goodNonce)
	_, err := c.Exchange(context.Background(), "code-1", verifierOf('b'), goodNonce)
	if !errors.Is(err, signin.ErrRefused) {
		t.Fatalf("wrong verifier: %v", err)
	}
	clean(t, err)
	// The provider spent the code: the right verifier afterwards is refused by the provider (not as a replay here: a refused code is forgotten).
	if _, err = c.Exchange(context.Background(), "code-1", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrRefused) {
		t.Fatalf("after a failed attempt: %v", err)
	}
	// An unknown code.
	if _, err = c.Exchange(context.Background(), "code-unknown", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrRefused) {
		t.Fatalf("unknown code: %v", err)
	}
	// A code issued for another redirect URI: the configured one is sent, and the provider refuses it.
	p.mu.Lock()
	p.codes["code-2"] = issued{challenge: challenge(verifierOf('a')), redirect: "https://evil.example.test/console/", nonce: goodNonce}
	p.mu.Unlock()
	if _, err = c.Exchange(context.Background(), "code-2", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrRefused) {
		t.Fatalf("wrong redirect: %v", err)
	}
	clean(t, err)
	if strings.Join(*outcomes, ",") != "provider_refused,provider_refused,provider_refused,provider_refused" {
		t.Errorf("outcomes %v", *outcomes)
	}
	// A client the provider does not know.
	other := config(p)
	other.ClientID = "someone-else"
	c2, outcomes2 := client(t, other)
	p.issue("code-3", verifierOf('a'), goodNonce)
	if _, err = c2.Exchange(context.Background(), "code-3", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrRefused) {
		t.Fatalf("unknown client: %v", err)
	}
	clean(t, err)
	if strings.Join(*outcomes2, ",") != "provider_client_refused" {
		t.Errorf("outcomes %v", *outcomes2)
	}
}

func TestAnswersThatAreNotATokenAnswer(t *testing.T) {
	cases := map[string]struct {
		answer  func(w http.ResponseWriter)
		err     error
		outcome string
	}{
		"a server error": {func(w http.ResponseWriter) { refusal(w, 500, "server_error") }, signin.ErrUnavailable, signin.OutcomeProviderStatus},
		"a 503":          {func(w http.ResponseWriter) { w.WriteHeader(503); _, _ = w.Write([]byte(providerMsg)) }, signin.ErrUnavailable, signin.OutcomeProviderStatus},
		"a redirect": {func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://elsewhere.example.test/token")
			w.WriteHeader(307)
		}, signin.ErrUnavailable, signin.OutcomeProviderStatus},
		"an oversized answer": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"` + strings.Repeat("a", signin.MaxAnswerBytes) + `","token_type":"Bearer"}`))
		}, signin.ErrUnavailable, signin.OutcomeOversized},
		"not JSON": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(providerMsg))
		}, signin.ErrUnavailable, signin.OutcomeMalformed},
		"HTML": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer"}`))
		}, signin.ErrUnavailable, signin.OutcomeMalformed},
		"no access token": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token_type":"Bearer"}`))
		}, signin.ErrUnavailable, signin.OutcomeMalformed},
		"not a Bearer token": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"x","token_type":"MAC"}`))
		}, signin.ErrUnavailable, signin.OutcomeMalformed},
		"a token with a space": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"a b","token_type":"Bearer"}`))
		}, signin.ErrUnavailable, signin.OutcomeMalformed},
		"a lifetime that is not a whole number": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer","expires_in":"900"}`))
		}, signin.ErrUnavailable, signin.OutcomeMalformed},
		"two documents": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer"} {}`))
		}, signin.ErrUnavailable, signin.OutcomeMalformed},
		"an ID token of another nonce": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "bearer", "id_token": idToken("nonce-of-another-request-0123")})
		}, signin.ErrRefused, signin.OutcomeNonce},
		"an ID token without a nonce": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "Bearer", "id_token": "e30.e30.c2ln"})
		}, signin.ErrRefused, signin.OutcomeNonce},
		"an ID token that is not a JWT": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "Bearer", "id_token": providerMsg})
		}, signin.ErrRefused, signin.OutcomeNonce},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := newProvider(t)
			p.answer = func(w http.ResponseWriter, _ *http.Request) bool { tc.answer(w); return true }
			c, outcomes := client(t, config(p))
			_, err := c.Exchange(context.Background(), "code-x", verifierOf('a'), goodNonce)
			if !errors.Is(err, tc.err) {
				t.Fatalf("%v, want %v", err, tc.err)
			}
			clean(t, err)
			if len(*outcomes) != 1 || (*outcomes)[0] != tc.outcome {
				t.Errorf("outcomes %v, want %s", *outcomes, tc.outcome)
			}
		})
	}
	// Without an ID token (a provider that is OAuth only), there is no nonce to compare.
	p := newProvider(t)
	p.answer = func(w http.ResponseWriter, _ *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","token_type":"Bearer"}`))
		return true
	}
	c, _ := client(t, config(p))
	if token, err := c.Exchange(context.Background(), "code-y", verifierOf('a'), goodNonce); err != nil || token.AccessToken != accessToken || token.ExpiresIn != 0 {
		t.Fatalf("%+v %v", token, err)
	}
}

func TestTimeoutsAndUnreachableProviders(t *testing.T) {
	p := newProvider(t)
	release := make(chan struct{})
	defer close(release)
	p.answer = func(w http.ResponseWriter, req *http.Request) bool {
		select {
		case <-release:
		case <-req.Context().Done():
		}
		return true
	}
	cfg := config(p)
	cfg.Timeout = 150 * time.Millisecond
	c, outcomes := client(t, cfg)
	start := time.Now()
	_, err := c.Exchange(context.Background(), "code-slow", verifierOf('a'), goodNonce)
	if !errors.Is(err, signin.ErrUnavailable) || time.Since(start) > 5*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
	if (*outcomes)[0] != signin.OutcomeTimeout {
		t.Errorf("outcomes %v", *outcomes)
	}
	// The caller's deadline is honoured too.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cfg.Timeout = 0
	c2, _ := client(t, cfg)
	if _, err = c2.Exchange(ctx, "code-slow-2", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrUnavailable) {
		t.Fatalf("caller's deadline: %v", err)
	}
	// A provider whose certificate is not the configured CA's: the system's roots do not know the test server.
	untrusted := config(p)
	untrusted.CA = ""
	c3, outcomes3 := client(t, untrusted)
	if _, err = c3.Exchange(context.Background(), "code-z", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrUnavailable) || (*outcomes3)[0] != signin.OutcomeUnreachable {
		t.Fatalf("untrusted: %v %v", err, *outcomes3)
	}
	// Nobody listening.
	gone := newProvider(t)
	cfg = config(gone)
	gone.Close()
	c4, outcomes4 := client(t, cfg)
	if _, err = c4.Exchange(context.Background(), "code-z", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrUnavailable) || (*outcomes4)[0] != signin.OutcomeUnreachable {
		t.Fatalf("gone: %v %v", err, *outcomes4)
	}
}

func TestAConcurrentReplayAndTheBoundOnExchanges(t *testing.T) {
	p := newProvider(t)
	entered := make(chan struct{}, signin.MaxInFlight+1)
	release := make(chan struct{})
	p.answer = func(w http.ResponseWriter, req *http.Request) bool {
		entered <- struct{}{}
		<-release
		refusal(w, 400, "invalid_grant")
		return true
	}
	c, _ := client(t, config(p))
	var wg sync.WaitGroup
	errs := make(chan error, signin.MaxInFlight)
	for i := 0; i < signin.MaxInFlight; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := c.Exchange(context.Background(), "code-busy-"+string(rune('a'+i)), verifierOf('a'), goodNonce)
			errs <- err
		}(i)
	}
	for i := 0; i < signin.MaxInFlight; i++ {
		<-entered
	}
	// Every slot is taken: one more is refused at once, without a request.
	if _, err := c.Exchange(context.Background(), "code-one-more", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrBusy) {
		t.Fatalf("over the bound: %v", err)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, signin.ErrRefused) {
			t.Errorf("%v", err)
		}
	}

	// A code presented twice at once: the second is a replay while the first is in flight.
	p2 := newProvider(t)
	hold := make(chan struct{})
	in := make(chan struct{})
	p2.answer = func(w http.ResponseWriter, req *http.Request) bool {
		close(in)
		<-hold
		return false
	}
	p2.issue("code-twice", verifierOf('a'), goodNonce)
	c2, _ := client(t, config(p2))
	done := make(chan error)
	go func() {
		_, err := c2.Exchange(context.Background(), "code-twice", verifierOf('a'), goodNonce)
		done <- err
	}()
	<-in
	if _, err := c2.Exchange(context.Background(), "code-twice", verifierOf('a'), goodNonce); !errors.Is(err, signin.ErrReplayed) {
		t.Fatalf("concurrent replay: %v", err)
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatalf("the first: %v", err)
	}
	if p2.requests.Load() != 1 {
		t.Errorf("%d requests", p2.requests.Load())
	}
}

func TestAConfidentialClientUsesBasicAuthentication(t *testing.T) {
	p := newProvider(t)
	var user, pass string
	var hasID bool
	p.answer = func(w http.ResponseWriter, req *http.Request) bool {
		user, pass, _ = req.BasicAuth()
		_, hasID = req.PostForm["client_id"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","token_type":"Bearer"}`))
		return true
	}
	cfg := config(p)
	cfg.ClientSecret = "s3cret:with/odd+chars"
	if strings.Contains(cfg.String(), "s3cret") || strings.Contains(cfg.GoString(), "s3cret") {
		t.Fatal("the secret is printed")
	}
	c, _ := client(t, cfg)
	if _, err := c.Exchange(context.Background(), "code-c", verifierOf('a'), goodNonce); err != nil {
		t.Fatal(err)
	}
	if user != clientID || pass != "s3cret%3Awith%2Fodd%2Bchars" || hasID {
		t.Errorf("basic %q %q, client_id in the body %v", user, pass, hasID)
	}
}

func TestInputsAreBoundedBeforeAnyRequest(t *testing.T) {
	p := newProvider(t)
	c, _ := client(t, config(p))
	for name, in := range map[string][3]string{
		"an empty code":        {"", verifierOf('a'), goodNonce},
		"a code too long":      {strings.Repeat("c", signin.MaxCode+1), verifierOf('a'), goodNonce},
		"a code with a NUL":    {"co\x00de", verifierOf('a'), goodNonce},
		"a code with a space":  {" code", verifierOf('a'), goodNonce},
		"a short verifier":     {"code", strings.Repeat("a", 42), goodNonce},
		"a long verifier":      {"code", strings.Repeat("a", 129), goodNonce},
		"a verifier with a +":  {"code", strings.Repeat("a", 42) + "+", goodNonce},
		"a short nonce":        {"code", verifierOf('a'), "short"},
		"a nonce with a slash": {"code", verifierOf('a'), goodNonce + "/"},
	} {
		if _, err := c.Exchange(context.Background(), in[0], in[1], in[2]); !errors.Is(err, signin.ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if p.requests.Load() != 0 {
		t.Errorf("%d requests reached the provider", p.requests.Load())
	}
}

func TestConfigurationRules(t *testing.T) {
	good := signin.Config{AuthorizationEndpoint: "https://idp.example.test/authorize", TokenEndpoint: "https://idp.example.test:8443/token", ClientID: clientID, RedirectURI: redirectURI, Scope: "openid reporting.read"}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	loop := good
	loop.AuthorizationEndpoint, loop.TokenEndpoint, loop.RedirectURI, loop.LoopbackHTTP = "http://127.0.0.1:9/authorize", "http://[::1]:9/token", "http://127.0.0.1:8/console/", true
	if err := loop.Check(); err != nil {
		t.Fatalf("loopback with the option: %v", err)
	}
	const secret = "VALUE-Zq7"
	cases := map[string]func(*signin.Config){
		"http without the option":             func(c *signin.Config) { c.TokenEndpoint = "http://127.0.0.1:9/token" },
		"http on a name":                      func(c *signin.Config) { c.TokenEndpoint, c.LoopbackHTTP = "http://localhost:9/token", true },
		"http on a public address":            func(c *signin.Config) { c.AuthorizationEndpoint, c.LoopbackHTTP = "http://192.0.2.1/authorize", true },
		"a redirect URI over http":            func(c *signin.Config) { c.RedirectURI = "http://reporting.example.test/console/" },
		"another scheme":                      func(c *signin.Config) { c.AuthorizationEndpoint = "javascript:alert(1)//" + secret },
		"a relative URL":                      func(c *signin.Config) { c.TokenEndpoint = "/token" },
		"a user in the URL":                   func(c *signin.Config) { c.TokenEndpoint = "https://u:" + secret + "@idp.example.test/token" },
		"a query":                             func(c *signin.Config) { c.AuthorizationEndpoint = "https://idp.example.test/authorize?x=" + secret },
		"a fragment":                          func(c *signin.Config) { c.AuthorizationEndpoint = "https://idp.example.test/authorize#" + secret },
		"an empty fragment":                   func(c *signin.Config) { c.TokenEndpoint = "https://idp.example.test/token#" },
		"no path":                             func(c *signin.Config) { c.TokenEndpoint = "https://idp.example.test" },
		"a bad port":                          func(c *signin.Config) { c.TokenEndpoint = "https://idp.example.test:99999/token" },
		"a redirect URI that is not /console": func(c *signin.Config) { c.RedirectURI = "https://reporting.example.test/elsewhere" },
		"a redirect URI with a query":         func(c *signin.Config) { c.RedirectURI = redirectURI + "?next=https://evil.example.test" },
		"a redirect URI without the slash":    func(c *signin.Config) { c.RedirectURI = "https://reporting.example.test/console" },
		"no client id":                        func(c *signin.Config) { c.ClientID = "" },
		"a client id with a space":            func(c *signin.Config) { c.ClientID = "a " + secret },
		"no scope":                            func(c *signin.Config) { c.Scope = "" },
		"a scope with two spaces":             func(c *signin.Config) { c.Scope = "openid  " + secret },
		"a scope with a quote":                func(c *signin.Config) { c.Scope = `openid "` + secret },
		"a negative timeout":                  func(c *signin.Config) { c.Timeout = -time.Second },
		"a timeout over a minute":             func(c *signin.Config) { c.Timeout = 61 * time.Second },
		"a secret with a space":               func(c *signin.Config) { c.ClientSecret = "a " + secret },
	}
	for name, change := range cases {
		c := good
		change(&c)
		err := c.Check()
		if !errors.Is(err, signin.ErrConfig) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "example.test") {
			t.Errorf("%s: the refusal carries a value: %q", name, err)
		}
		if _, err := signin.New(c); !errors.Is(err, signin.ErrConfig) {
			t.Errorf("%s: New accepts it", name)
		}
	}
	// The CA file is read by New: missing, or holding no certificate.
	missing := good
	missing.CA = filepath.Join(t.TempDir(), "absent.pem")
	if _, err := signin.New(missing); !errors.Is(err, signin.ErrConfig) {
		t.Errorf("a missing CA file: %v", err)
	}
	empty := good
	empty.CA = filepath.Join(t.TempDir(), "empty.pem")
	_ = os.WriteFile(empty.CA, []byte("not a certificate"), 0o644)
	if _, err := signin.New(empty); !errors.Is(err, signin.ErrConfig) {
		t.Errorf("a CA file without a certificate: %v", err)
	}
	// What the console learns: nothing of the token endpoint and nothing secret.
	good.ClientSecret = "SECRET-" + secret
	c, err := signin.New(good)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c.Public())
	if string(b) != `{"enabled":true,"authorization_endpoint":"https://idp.example.test/authorize","client_id":"reporting-console","redirect_uri":"https://reporting.example.test/console/","scope":"openid reporting.read","code_challenge_method":"S256","paste_fallback":false}` {
		t.Errorf("public %s", b)
	}
}
