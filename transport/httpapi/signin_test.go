package httpapi_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/identity/signin"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// fakeExchanger answers what the test sets and records what it was asked.
type fakeExchanger struct {
	token signin.Token
	err   error
	calls []string
}

func (f *fakeExchanger) Public() signin.Public {
	return signin.Public{Enabled: true, AuthorizationEndpoint: "https://idp.example.test/authorize", ClientID: "console", RedirectURI: "https://reporting.example.test/console/", Scope: "openid", CodeChallengeMethod: "S256"}
}

func (f *fakeExchanger) Exchange(_ context.Context, code, verifier, nonce string) (signin.Token, error) {
	f.calls = append(f.calls, code+"|"+verifier+"|"+nonce)
	return f.token, f.err
}

func signInHandler(t *testing.T, x httpapi.Exchanger) http.Handler {
	t.Helper()
	p := contractsim.DemoPrincipal()
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: contractsim.Financial()}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	h, e := httpapi.Handler(s, auth)
	if e != nil {
		t.Fatal(e)
	}
	if h, e = httpapi.WithSignIn(h, x, auth); e != nil {
		t.Fatal(e)
	}
	return h
}

const (
	goodVerifier = "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv"
	goodNonce    = "nnnnnnnnnnnnnnnnnnnnnnnnnn"
)

func exchange(h http.Handler, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/reporting/v1/sign-in/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestSignInConfigurationIsPublicAndUncached(t *testing.T) {
	h := signInHandler(t, &fakeExchanger{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/reporting/v1/sign-in", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"enabled":true,"authorization_endpoint":"https://idp.example.test/authorize","client_id":"console","redirect_uri":"https://reporting.example.test/console/","scope":"openid","code_challenge_method":"S256","paste_fallback":false}` {
		t.Fatal(w.Code, w.Body.String())
	}
	for k, v := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'", "Content-Type": "application/json; charset=utf-8"} {
		if w.Header().Get(k) != v {
			t.Errorf("%s: %q", k, w.Header().Get(k))
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/reporting/v1/sign-in?x=1", nil))
	if w.Code != 400 {
		t.Errorf("a query: %d", w.Code)
	}
	// Without the mount, the core API says there is no sign-in (not a 404 on every page load of the console), and has no exchange.
	p := contractsim.DemoPrincipal()
	s, _ := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: contractsim.Financial()}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	auth, _ := httpapi.DemoBearer(secret, p)
	core, _ := httpapi.Handler(s, auth)
	w = httptest.NewRecorder()
	core.ServeHTTP(w, httptest.NewRequest("GET", "/api/reporting/v1/sign-in", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"enabled":false}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = exchange(core, `{}`); w.Code != 404 {
		t.Errorf("the exchange without the mount: %d", w.Code)
	}
	// Everything else still needs a token.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/reporting/v1/session", nil))
	if w.Code != 401 {
		t.Errorf("session without a token: %d", w.Code)
	}
}

func TestSignInExchangeAnswersFixedCodes(t *testing.T) {
	body := `{"code":"code-1","code_verifier":"` + goodVerifier + `","nonce":"` + goodNonce + `"}`
	// The token the platform accepts (the demo's secret) is answered, and nothing else of the provider's answer.
	x := &fakeExchanger{token: signin.Token{AccessToken: secret, ExpiresIn: 900}}
	h := signInHandler(t, x)
	w := exchange(h, body)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"access_token":"`+secret+`","expires_in":900}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(x.calls) != 1 || x.calls[0] != "code-1|"+goodVerifier+"|"+goodNonce {
		t.Fatalf("calls %v", x.calls)
	}
	// A token this platform would refuse is not handed to the page.
	h = signInHandler(t, &fakeExchanger{token: signin.Token{AccessToken: "a-token-from-the-provider-for-another-audience"}})
	if w = exchange(h, body); w.Code != 403 || !strings.Contains(w.Body.String(), `"SIGN_IN_TOKEN_REFUSED"`) || strings.Contains(w.Body.String(), "another-audience") {
		t.Fatal(w.Code, w.Body.String())
	}
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		signin.ErrRefused:                    {400, "SIGN_IN_REFUSED"},
		signin.ErrReplayed:                   {409, "SIGN_IN_REPLAYED"},
		signin.ErrUnavailable:                {502, "SIGN_IN_UNAVAILABLE"},
		signin.ErrBusy:                       {503, "SIGN_IN_BUSY"},
		errors.New("provider said: secret!"): {502, "SIGN_IN_UNAVAILABLE"},
	} {
		h := signInHandler(t, &fakeExchanger{err: err})
		w := exchange(h, body)
		if w.Code != want.status || strings.TrimSpace(w.Body.String()) != `{"error":"`+want.code+`"}` {
			t.Errorf("%v: %d %s", err, w.Code, w.Body.String())
		}
		if (want.code == "SIGN_IN_BUSY") != (w.Header().Get("Retry-After") == "1") {
			t.Errorf("%v: Retry-After %q", err, w.Header().Get("Retry-After"))
		}
	}
	// Bodies out of their bounds never reach the exchange.
	x = &fakeExchanger{token: signin.Token{AccessToken: secret}}
	h = signInHandler(t, x)
	for name, b := range map[string]string{
		"not JSON":           `code=1`,
		"an unknown field":   `{"code":"c","code_verifier":"` + goodVerifier + `","nonce":"` + goodNonce + `","redirect_uri":"https://evil.example.test/"}`,
		"no verifier":        `{"code":"c","nonce":"` + goodNonce + `"}`,
		"a short verifier":   `{"code":"c","code_verifier":"short","nonce":"` + goodNonce + `"}`,
		"no nonce":           `{"code":"c","code_verifier":"` + goodVerifier + `"}`,
		"an empty code":      `{"code":"","code_verifier":"` + goodVerifier + `","nonce":"` + goodNonce + `"}`,
		"a code too long":    `{"code":"` + strings.Repeat("c", 2049) + `","code_verifier":"` + goodVerifier + `","nonce":"` + goodNonce + `"}`,
		"a code with a tab":  `{"code":"c\td","code_verifier":"` + goodVerifier + `","nonce":"` + goodNonce + `"}`,
		"a code as a number": `{"code":1,"code_verifier":"` + goodVerifier + `","nonce":"` + goodNonce + `"}`,
		"two documents":      body + body,
	} {
		if w := exchange(h, b); w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"INVALID_REQUEST"}` {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w = exchange(h, `{"code":"`+strings.Repeat("c", 5000)+`"}`); w.Code != 413 {
		t.Errorf("a body over 4 KiB: %d", w.Code)
	}
	req := httptest.NewRequest("POST", "/api/reporting/v1/sign-in/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("text/plain: %d", w.Code)
	}
	if len(x.calls) != 0 {
		t.Errorf("an invalid body reached the exchange: %d", len(x.calls))
	}
	// GET is not the exchange (the mounts' fallback answers it: the mux's 404).
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/reporting/v1/sign-in/token?code=c", nil))
	if w.Code != 404 || len(x.calls) != 0 {
		t.Errorf("GET: %d", w.Code)
	}
}

// The whole chain: the mount, the real client, a fake provider over TLS that answers the demo's secret as its access token.
func TestSignInExchangeEndToEndWithAFakeProvider(t *testing.T) {
	var form map[string][]string
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		form = req.PostForm
		if req.PostForm.Get("code") != "code-e2e" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"PROVIDER-TEXT"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": secret, "token_type": "Bearer", "refresh_token": "REFRESH-never-answered", "id_token": "e30." + "eyJub25jZSI6Im5ubm5ubm5ubm5ubm5ubm5ubm5ubm5ubm5uIn0" + ".c2ln"})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0o644); e != nil {
		t.Fatal(e)
	}
	c, e := signin.New(signin.Config{AuthorizationEndpoint: provider.URL + "/authorize", TokenEndpoint: provider.URL + "/token", ClientID: "console", RedirectURI: "https://reporting.example.test/console/", Scope: "openid", CA: ca})
	if e != nil {
		t.Fatal(e)
	}
	h := signInHandler(t, c)
	body := `{"code":"code-e2e","code_verifier":"` + goodVerifier + `","nonce":"` + goodNonce + `"}`
	w := exchange(h, body)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"access_token":"`+secret+`"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if form["code_verifier"][0] != goodVerifier || form["redirect_uri"][0] != "https://reporting.example.test/console/" {
		t.Errorf("form %v", form)
	}
	// The token answered works as a pasted one would.
	req := httptest.NewRequest("GET", "/api/reporting/v1/session", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if w = exchange(h, body); w.Code != 409 || strings.TrimSpace(w.Body.String()) != `{"error":"SIGN_IN_REPLAYED"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	other := strings.Replace(body, "code-e2e", "code-other", 1)
	if w = exchange(h, other); w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"SIGN_IN_REFUSED"}` {
		t.Fatal(w.Code, w.Body.String())
	}
}
