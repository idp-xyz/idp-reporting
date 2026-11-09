// Package signin is the server side of the console's browser sign-in (S15-A, ADR 0019): OpenID Connect's authorization code flow with PKCE
// (S256) for a public client, or a confidential one whose secret is the environment's. The browser goes to the provider's authorization
// endpoint and comes back to the console with a code; the console gives the code, its PKCE verifier and its nonce to the platform, and this
// package exchanges them at the configured token endpoint, server to server, because the console's Content-Security-Policy (connect-src
// 'self') does not let the page call the provider. The access token it answers is used exactly like a pasted one: every request is still
// verified by identity/accessjwt. The ID token is never trusted for authorization; when the provider returns one, only its nonce is compared.
//
// The package is generic because the real provider's contract is not known yet (docs/DEPLOYMENT.md, go-live checklist item 16): the
// endpoints are configured, not discovered. Standard library only.
package signin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ClientSecretEnv is the environment variable of a confidential client's secret. It is never a field of a configuration file.
const ClientSecretEnv = "REPORTING_SIGN_IN_CLIENT_SECRET"

// RedirectPath is the path of every redirect URI: the console's own page, which reads the code and removes it from the address at once.
const RedirectPath = "/console/"

// Bounds.
const (
	DefaultTimeout  = 10 * time.Second
	MaxTimeout      = 60 * time.Second
	MaxAnswerBytes  = 64 << 10 // the token endpoint's answer
	MaxAccessToken  = 16 << 10
	MaxCode         = 2048
	MaxInFlight     = 16               // exchanges at once in this process; beyond it ErrBusy
	MaxRemembered   = 4096             // codes in flight or redeemed, remembered against a replay; beyond it ErrBusy
	RememberCodeFor = 10 * time.Minute // longer than any authorization code lives (RFC 6749 §4.1.2 recommends at most 10 minutes)
	maxEndpoint     = 2048
	maxClientID     = 256
	maxScope        = 512
	maxSecret       = 512
)

// The refusals of Exchange. Each is a fixed code; the provider's own text never travels beyond this package.
var (
	// ErrRefused: the provider refused the code (unknown, expired, already used, a verifier that does not match, a client it does not
	// know) or its ID token does not carry the nonce the browser sent.
	ErrRefused = errors.New("SIGN_IN_REFUSED")
	// ErrReplayed: this code was already presented to this process.
	ErrReplayed = errors.New("SIGN_IN_REPLAYED")
	// ErrUnavailable: the provider did not answer in time, could not be reached, answered a server error, or an answer that is not a token
	// answer (not JSON, too large, no access token, not a Bearer token).
	ErrUnavailable = errors.New("SIGN_IN_UNAVAILABLE")
	// ErrBusy: too many exchanges at once, or too many codes remembered.
	ErrBusy = errors.New("SIGN_IN_BUSY")
	// ErrConfig: the configuration is refused. The wrapping text names the rule, never a value.
	ErrConfig = errors.New("sign-in configuration refused")
)

// The outcomes Exchange reports to Config.Outcome, for the log and nothing else: fixed words, never a value of the request or the answer.
const (
	OutcomeOK              = "ok"
	OutcomeReplayed        = "replayed"
	OutcomeBusy            = "busy"
	OutcomeProviderRefused = "provider_refused"        // 400: invalid_grant and its kin
	OutcomeClientRefused   = "provider_client_refused" // 401: the provider does not know the client or its secret (a configuration error)
	OutcomeProviderStatus  = "provider_status"         // any other status
	OutcomeTimeout         = "timeout"
	OutcomeUnreachable     = "unreachable"
	OutcomeOversized       = "oversized"
	OutcomeMalformed       = "malformed"
	OutcomeNonce           = "nonce_mismatch"
)

// Config is the client's configuration. Every field but ClientSecret may come from a configuration file.
type Config struct {
	// AuthorizationEndpoint is where the browser is sent: https://host[:port]/path, no user, query or fragment.
	AuthorizationEndpoint string
	// TokenEndpoint is where the server exchanges the code: the same form.
	TokenEndpoint string
	// ClientID is the platform's client at the provider: 1 to 256 printable ASCII characters without a space.
	ClientID string
	// RedirectURI is the one redirect URI registered at the provider, sent unchanged in both requests: https://host[:port]/console/.
	RedirectURI string
	// Scope is what the browser asks for: 1 to 512 characters, scope tokens separated by one space (RFC 6749 §3.3).
	Scope string
	// PasteFallback keeps the console's paste-a-token form visible under the sign-in button (ADR 0019: off by default).
	PasteFallback bool
	// CA is a PEM file; with it the token endpoint is verified against these certificates alone, else against the system's roots.
	CA string
	// LoopbackHTTP allows http:// for a URL whose host is a loopback IP address: for the demo and the tests, never for a deployment.
	LoopbackHTTP bool
	// Timeout bounds one exchange (connection, TLS, request and answer): 0 is DefaultTimeout, at most MaxTimeout.
	Timeout time.Duration
	// ClientSecret makes the client confidential (client_secret_basic). It is read from ClientSecretEnv by the caller, never from a file.
	ClientSecret string
	// Outcome, when set, is told the outcome of each exchange (one of the Outcome* words).
	Outcome func(string)
}

// String keeps the secret out of any log line that prints the value.
func (c Config) String() string   { return "signin.Config{client " + c.ClientID + "}" }
func (c Config) GoString() string { return c.String() }

// configError is a refusal of the configuration: its text is the rule, and it is ErrConfig.
type configError struct{ why string }

func (e configError) Error() string { return e.why }
func (e configError) Unwrap() error { return ErrConfig }

func refuse(why string) error { return configError{why} }

// Check validates what the configuration alone decides; it reads no file. New calls it.
func (c Config) Check() error {
	for _, f := range []struct{ name, value string }{{"authorization_endpoint", c.AuthorizationEndpoint}, {"token_endpoint", c.TokenEndpoint}} {
		if err := checkURL(f.value, c.LoopbackHTTP, false); err != nil {
			return refuse(f.name + " " + err.Error())
		}
	}
	if err := checkURL(c.RedirectURI, c.LoopbackHTTP, true); err != nil {
		return refuse("redirect_uri " + err.Error())
	}
	if !printable(c.ClientID, maxClientID) {
		return refuse("client_id is 1 to 256 printable ASCII characters without a space")
	}
	if !validScope(c.Scope) {
		return refuse("scope is 1 to 512 characters: scope tokens separated by one space")
	}
	if c.Timeout < 0 || c.Timeout > MaxTimeout {
		return refuse("the timeout is 0 (the default, 10 seconds) up to 60 seconds")
	}
	if c.ClientSecret != "" && !printable(c.ClientSecret, maxSecret) {
		return refuse("the client secret (" + ClientSecretEnv + ") is 1 to 512 printable ASCII characters without a space")
	}
	return nil
}

// checkURL holds an endpoint (or the redirect URI, whose path is RedirectPath) to https://host[:port]/path with no user, query or fragment;
// http:// only on a loopback IP address and only with loopback.
func checkURL(raw string, loopback, redirect bool) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > maxEndpoint || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.Opaque != "" {
		return errors.New("must be an absolute https:// URL with no user, query or fragment")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return errors.New("has an invalid port")
		}
	}
	switch u.Scheme {
	case "https":
	case "http":
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return errors.New("must be https:// (http:// is allowed only on a loopback IP address)")
		}
		if !loopback {
			return errors.New("is http://, which needs loopback_http (for the demo and the tests only)")
		}
	default:
		return errors.New("must be an https:// URL")
	}
	if redirect && u.EscapedPath() != RedirectPath {
		return errors.New("must be the console's address, ending in " + RedirectPath)
	}
	if !redirect && (u.Path == "" || strings.Contains(u.EscapedPath(), "..")) {
		return errors.New("must have a path")
	}
	return nil
}

func printable(s string, most int) bool {
	if s == "" || len(s) > most {
		return false
	}
	for _, c := range []byte(s) {
		if c <= 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// validScope is RFC 6749 §3.3: scope-token *( SP scope-token ), scope-token = 1*( %x21 / %x23-5B / %x5D-7E ).
func validScope(s string) bool {
	if s == "" || len(s) > maxScope {
		return false
	}
	for _, token := range strings.Split(s, " ") {
		if token == "" {
			return false
		}
		for _, c := range []byte(token) {
			if c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
				return false
			}
		}
	}
	return true
}

// ValidCode is an authorization code as RFC 6749 allows it (VSCHAR, %x20-7E), 1 to MaxCode characters, no space at either end.
func ValidCode(s string) bool {
	if s == "" || len(s) > MaxCode || s[0] == ' ' || s[len(s)-1] == ' ' {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// ValidVerifier is a PKCE code verifier (RFC 7636 §4.1): 43 to 128 of [A-Za-z0-9-._~].
func ValidVerifier(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	return unreserved(s)
}

// ValidNonce is the console's nonce: 22 to 128 of the same characters (at least 128 bits in base64url).
func ValidNonce(s string) bool { return len(s) >= 22 && len(s) <= 128 && unreserved(s) }

func unreserved(s string) bool {
	for _, c := range []byte(s) {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~') {
			return false
		}
	}
	return true
}

// Public is what the console learns before anyone signs in (GET /sign-in): nothing secret, nothing of the token endpoint.
type Public struct {
	// Enabled is true: the core API answers {"enabled": false} for a deployment without a sign-in.
	Enabled               bool   `json:"enabled"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	ClientID              string `json:"client_id"`
	RedirectURI           string `json:"redirect_uri"`
	Scope                 string `json:"scope"`
	CodeChallengeMethod   string `json:"code_challenge_method"`
	PasteFallback         bool   `json:"paste_fallback"`
}

// Token is what an exchange answers the browser: the access token and, when the provider said it, its lifetime in seconds.
type Token struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in,omitempty"`
}

// Client exchanges codes. It is safe for concurrent use.
type Client struct {
	cfg     Config
	token   string // the token endpoint, parsed and re-serialised
	http    *http.Client
	slots   chan struct{}
	now     func() time.Time
	mu      sync.Mutex
	redeems map[[32]byte]time.Time // the SHA-256 of each code in flight or redeemed, and when it was first presented
}

// New validates the configuration, reads the CA file when one is named, and answers the client. It contacts nothing.
func New(c Config) (*Client, error) {
	if err := c.Check(); err != nil {
		return nil, err
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.MaxResponseHeaderBytes = 16 << 10
	transport.ResponseHeaderTimeout = c.Timeout
	if c.CA != "" {
		pem, err := os.ReadFile(c.CA)
		if err != nil {
			return nil, refuse("ca_file cannot be read")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, refuse("ca_file holds no certificate")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	u, _ := url.Parse(c.TokenEndpoint)
	return &Client{
		cfg:   c,
		token: u.String(),
		http: &http.Client{
			Transport: transport,
			Timeout:   c.Timeout,
			// A redirect is not followed: the code and the verifier are for this endpoint and no other.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		slots:   make(chan struct{}, MaxInFlight),
		now:     time.Now,
		redeems: map[[32]byte]time.Time{},
	}, nil
}

// Public answers what the console may know.
func (c *Client) Public() Public {
	return Public{Enabled: true, AuthorizationEndpoint: c.cfg.AuthorizationEndpoint, ClientID: c.cfg.ClientID, RedirectURI: c.cfg.RedirectURI, Scope: c.cfg.Scope, CodeChallengeMethod: "S256", PasteFallback: c.cfg.PasteFallback}
}

func (c *Client) outcome(word string) {
	if c.cfg.Outcome != nil {
		c.cfg.Outcome(word)
	}
}

// remember records a code as presented. It refuses a code presented before (ErrReplayed) and a full memory (ErrBusy); entries older than
// RememberCodeFor are forgotten first.
func (c *Client) remember(code string) ([32]byte, error) {
	key := sha256.Sum256([]byte(code))
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if at, seen := c.redeems[key]; seen && now.Sub(at) < RememberCodeFor {
		return key, ErrReplayed
	}
	if len(c.redeems) >= MaxRemembered {
		for k, at := range c.redeems {
			if now.Sub(at) >= RememberCodeFor {
				delete(c.redeems, k)
			}
		}
		if len(c.redeems) >= MaxRemembered {
			return key, ErrBusy
		}
	}
	c.redeems[key] = now
	return key, nil
}

// forget releases a code the provider refused: the provider will refuse it again, and a memory of refused codes would let anyone fill it.
func (c *Client) forget(key [32]byte) {
	c.mu.Lock()
	delete(c.redeems, key)
	c.mu.Unlock()
}

// Exchange redeems one code with its verifier at the token endpoint and answers the access token. The inputs must be valid (ValidCode,
// ValidVerifier, ValidNonce): anything else is ErrRefused without a request. A code is redeemed at most once in this process: a second
// presentation is ErrReplayed, whether the first succeeded or is still in flight. The answer must be 200, JSON, at most MaxAnswerBytes,
// with a Bearer access token; when it carries an ID token, the ID token's nonce must be the one given (TLS to the configured endpoint is
// what vouches for the answer, as OpenID Connect Core §3.1.3.7 allows for the code flow; the ID token is not used for anything else).
func (c *Client) Exchange(ctx context.Context, code, verifier, nonce string) (Token, error) {
	if !ValidCode(code) || !ValidVerifier(verifier) || !ValidNonce(nonce) {
		return Token{}, ErrRefused
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		c.outcome(OutcomeBusy)
		return Token{}, ErrBusy
	}
	key, err := c.remember(code)
	if err != nil {
		if errors.Is(err, ErrReplayed) {
			c.outcome(OutcomeReplayed)
		} else {
			c.outcome(OutcomeBusy)
		}
		return Token{}, err
	}
	token, word, err := c.exchange(ctx, code, verifier, nonce)
	if word == OutcomeProviderRefused || word == OutcomeClientRefused {
		c.forget(key)
	}
	c.outcome(word)
	return token, err
}

func (c *Client) exchange(ctx context.Context, code, verifier, nonce string) (Token, string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.cfg.RedirectURI}, "code_verifier": {verifier}}
	if c.cfg.ClientSecret == "" {
		form.Set("client_id", c.cfg.ClientID) // a public client names itself in the body (RFC 6749 §4.1.3)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.token, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, OutcomeUnreachable, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.cfg.ClientSecret != "" {
		// client_secret_basic: both parts form-encoded first (RFC 6749 §2.3.1).
		req.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var timeout interface{ Timeout() bool }
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			return Token{}, OutcomeTimeout, ErrUnavailable
		}
		return Token{}, OutcomeUnreachable, ErrUnavailable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswerBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return Token{}, OutcomeTimeout, ErrUnavailable
		}
		return Token{}, OutcomeUnreachable, ErrUnavailable
	}
	if len(body) > MaxAnswerBytes {
		return Token{}, OutcomeOversized, ErrUnavailable
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusBadRequest:
		// invalid_grant, invalid_request and their kin: the code, the verifier or the redirect URI is not what the provider issued.
		return Token{}, OutcomeProviderRefused, ErrRefused
	case resp.StatusCode == http.StatusUnauthorized:
		return Token{}, OutcomeClientRefused, ErrRefused
	default:
		return Token{}, OutcomeProviderStatus, ErrUnavailable
	}
	if kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || kind != "application/json" {
		return Token{}, OutcomeMalformed, ErrUnavailable
	}
	var answer struct {
		AccessToken string          `json:"access_token"`
		TokenType   string          `json:"token_type"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
		IDToken     string          `json:"id_token"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&answer); err != nil || dec.More() {
		return Token{}, OutcomeMalformed, ErrUnavailable
	}
	if !printable(answer.AccessToken, MaxAccessToken) || !strings.EqualFold(answer.TokenType, "Bearer") {
		return Token{}, OutcomeMalformed, ErrUnavailable
	}
	out := Token{AccessToken: answer.AccessToken}
	if len(answer.ExpiresIn) > 0 {
		n, err := strconv.ParseInt(string(answer.ExpiresIn), 10, 64)
		if err != nil || n < 1 || n > 86400 {
			return Token{}, OutcomeMalformed, ErrUnavailable
		}
		out.ExpiresIn = n
	}
	if answer.IDToken != "" && !nonceMatches(answer.IDToken, nonce) {
		return Token{}, OutcomeNonce, ErrRefused
	}
	return out, OutcomeOK, nil
}

// nonceMatches reads the nonce claim of an ID token's payload and compares it with want. The signature is not checked: the token came from
// the configured endpoint over TLS, and nothing of it but this comparison is used.
func nonceMatches(idToken, want string) bool {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 || len(parts[1]) > MaxAnswerBytes {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Nonce *string `json:"nonce"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Nonce == nil {
		return false
	}
	return *claims.Nonce == want
}
