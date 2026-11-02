package accessjwt_test

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/identity/accessjwt"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// Issuer key rotation (S04-H, docs/S04H_SCOPE.md H2): the overlap procedure of docs/ACCESS_JWT.md, with real RS256 tokens.

type issuerKey struct {
	kid string
	key *rsa.PrivateKey
}

func newIssuerKey(t *testing.T, kid string) issuerKey { return issuerKey{kid, key(t)} }

func (k issuerKey) jwk() map[string]any {
	return map[string]any{"kty": "RSA", "kid": k.kid, "alg": "RS256", "use": "sig", "key_ops": []string{"verify"},
		"n": base64.RawURLEncoding.EncodeToString(k.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.key.E)).Bytes())}
}

// trust is the configuration that trusts these keys.
func trust(t *testing.T, keys ...issuerKey) accessjwt.Config {
	t.Helper()
	c := config(t, keys[0].key)
	var entries []any
	for _, k := range keys {
		entries = append(entries, k.jwk())
	}
	jwks, err := json.Marshal(map[string]any{"keys": entries})
	if err != nil {
		t.Fatal(err)
	}
	c.JWKS = jwks
	c.Bindings[0].Integrations = []string{contractsim.FinanceIntegration}
	return c
}

// token signs the standard claims with this key, naming its kid.
func (k issuerKey) token(t *testing.T) string {
	t.Helper()
	payload, err := json.Marshal(claims())
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "at+jwt", "kid": k.kid})
	if err != nil {
		t.Fatal(err)
	}
	return signRaw(t, k.key, header, payload)
}

type rotationAPI struct {
	v       *accessjwt.Verifier
	handler http.Handler
}

// newRotationAPI serves the session route behind the verifier, as the server does.
func newRotationAPI(t *testing.T, c accessjwt.Config) *rotationAPI {
	t.Helper()
	v, err := accessjwt.New(c)
	if err != nil {
		t.Fatal(err)
	}
	a := contractsim.Financial()
	p := r.Principal{Identity: r.Identity{Tenant: "platform-a", Issuer: issuer, Subject: "subject-a"}, Integrations: []string{contractsim.FinanceIntegration}}
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: "platform-a", Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("rotation-test-cursor-key-0123456789")})
	if err != nil {
		t.Fatal(err)
	}
	h, err := httpapi.Handler(s, v.Authenticate)
	if err != nil {
		t.Fatal(err)
	}
	return &rotationAPI{v, h}
}

// get is the response to a request bearing the token.
func (a *rotationAPI) get(token string) (int, string, string) {
	req := httptest.NewRequest("GET", "https://reporting.example.invalid/api/reporting/v1/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Body)
	return w.Code, w.Header().Get("WWW-Authenticate"), string(body)
}

const invalidToken = `Bearer realm="reporting", error="invalid_token"`

func (a *rotationAPI) accepts(t *testing.T, name, token string) {
	t.Helper()
	if code, _, body := a.get(token); code != 200 {
		t.Errorf("%s: status %d, want 200: %.120s", name, code, body)
	}
}

func (a *rotationAPI) refuses(t *testing.T, name, token string, leaks ...string) {
	t.Helper()
	code, challenge, body := a.get(token)
	if code != 401 || challenge != invalidToken {
		t.Errorf("%s: status %d challenge %q, want 401 invalid_token", name, code, challenge)
	}
	for _, leak := range leaks {
		if strings.Contains(body, leak) || strings.Contains(challenge, leak) {
			t.Errorf("%s: the refusal names %q: %q", name, leak, body)
		}
	}
}

// The rotation by overlap, step by step, at the HTTP boundary: the removed key's tokens are 401 invalid_token from the next request on,
// without a restart, and KeyIDs shows what is trusted at each step.
func TestIssuerKeyRotationByOverlap(t *testing.T) {
	oldKey, newKey := newIssuerKey(t, "issuer-2026-01"), newIssuerKey(t, "issuer-2026-07")
	api := newRotationAPI(t, trust(t, oldKey))
	oldToken, newToken := oldKey.token(t), newKey.token(t)
	step := func(name string, want []string) {
		t.Helper()
		if got := api.v.KeyIDs(); !slices.Equal(got, want) {
			t.Errorf("%s: KeyIDs %v, want %v", name, got, want)
		}
	}
	step("before", []string{"issuer-2026-01"})
	api.accepts(t, "the old key before the rotation", oldToken)
	api.refuses(t, "the new key before it is published", newToken, "issuer-2026-07")

	// Step 1: publish the new key beside the old; both signers are accepted (the issuer may switch at any moment).
	if err := api.v.Replace(trust(t, oldKey, newKey)); err != nil {
		t.Fatal(err)
	}
	step("overlap", []string{"issuer-2026-01", "issuer-2026-07"})
	api.accepts(t, "the old key in the overlap", oldToken)
	api.accepts(t, "the new key in the overlap", newToken)

	// Step 2: remove the old key. Its tokens are refused from the next request; the new key's are accepted.
	if err := api.v.Replace(trust(t, newKey)); err != nil {
		t.Fatal(err)
	}
	step("after", []string{"issuer-2026-07"})
	api.refuses(t, "a token of the removed key", oldToken, "issuer-2026-01", "issuer-2026-07")
	api.accepts(t, "a token of the new key", newToken)

	// The removal does not depend on the order of the keys in the document.
	if err := api.v.Replace(trust(t, newKey, oldKey)); err != nil {
		t.Fatal(err)
	}
	step("reordered overlap", []string{"issuer-2026-01", "issuer-2026-07"})
}

// KeyIDs is the sorted kids and nothing else.
func TestKeyIDsAreTheSortedKidsAndNothingElse(t *testing.T) {
	a, b, c := newIssuerKey(t, "mmm"), newIssuerKey(t, "aaa"), newIssuerKey(t, "zzz")
	v, err := accessjwt.New(trust(t, a, b, c))
	if err != nil {
		t.Fatal(err)
	}
	got := v.KeyIDs()
	if !slices.Equal(got, []string{"aaa", "mmm", "zzz"}) {
		t.Fatalf("KeyIDs %v", got)
	}
	// A returned slice is the caller's: changing it changes nothing.
	got[0] = "tampered"
	if !slices.Equal(v.KeyIDs(), []string{"aaa", "mmm", "zzz"}) {
		t.Error("the caller changed the trusted set through KeyIDs")
	}
	encoded, _ := json.Marshal(v.KeyIDs())
	for _, k := range []issuerKey{a, b, c} {
		if bytes.Contains(encoded, []byte(base64.RawURLEncoding.EncodeToString(k.key.N.Bytes()[:24]))) {
			t.Error("KeyIDs carries a modulus")
		}
	}
	var zero accessjwt.Verifier
	if len(zero.KeyIDs()) != 0 || len((*accessjwt.Verifier)(nil).KeyIDs()) != 0 {
		t.Error("a verifier with no configuration trusts a key")
	}
}

// A rotation that fails half-way changes nothing: the whole new configuration is validated before any of it is published, and the error
// says nothing of the keys.
func TestFailedReplaceLeavesThePreviousConfigurationInForce(t *testing.T) {
	oldKey, newKey := newIssuerKey(t, "issuer-old"), newIssuerKey(t, "issuer-new")
	api := newRotationAPI(t, trust(t, oldKey))
	oldToken, newToken := oldKey.token(t), newKey.token(t)
	good := trust(t, oldKey, newKey)

	duplicate := trust(t, oldKey, newKey)
	duplicate.JWKS = bytes.Replace(duplicate.JWKS, []byte(`"kid":"issuer-new"`), []byte(`"kid":"issuer-old"`), 1)
	short := trust(t, oldKey, newKey)
	short.JWKS = bytes.Replace(short.JWKS, []byte(base64.RawURLEncoding.EncodeToString(newKey.key.N.Bytes())), []byte("AQAB"), 1)
	var seventeen []any
	for i := range 17 {
		seventeen = append(seventeen, newIssuerKey(t, "k"+string(rune('a'+i))).jwk())
	}
	tooMany := good
	tooMany.JWKS, _ = json.Marshal(map[string]any{"keys": seventeen})
	badBinding := good
	badBinding.Bindings = append(slices.Clone(good.Bindings), good.Bindings[0]) // the same subject and client twice
	badLifetime := good
	badLifetime.MaxTokenLifetime = 2 * time.Hour
	badIssuer := good
	badIssuer.Issuer = "http://issuer.example.invalid"
	empty := good
	empty.JWKS = []byte(`{"keys":[]}`)
	for name, c := range map[string]accessjwt.Config{"a duplicate kid": duplicate, "a short modulus": short, "17 keys": tooMany, "a duplicate binding": badBinding, "a lifetime over the bound": badLifetime, "an http issuer": badIssuer, "no keys": empty, "a zero configuration": {}} {
		err := api.v.Replace(c)
		if !errors.Is(err, accessjwt.ErrInvalidConfig) {
			t.Errorf("%s: Replace returned %v", name, err)
			continue
		}
		if err.Error() != "INVALID_ACCESS_JWT_CONFIG" {
			t.Errorf("%s: the error text is %q", name, err)
		}
		if got := api.v.KeyIDs(); !slices.Equal(got, []string{"issuer-old"}) {
			t.Errorf("%s: the trusted set is now %v", name, got)
		}
		api.accepts(t, name+": the old key", oldToken)
		api.refuses(t, name+": the new key", newToken)
	}
	// And the same Replace, valid, is then accepted: the failures were about the input.
	if err := api.v.Replace(good); err != nil {
		t.Fatal(err)
	}
	api.accepts(t, "the new key once the valid replacement is in", newToken)
}

// An authentication that has started holds the configuration it started with and completes under it: a Replace that removes its key
// during the call does not fail it, and the next authentication is judged by the new configuration. This is the documented semantics, and
// the reason the exposure of a leaked signing key ends "at the next authentication", not at the instant of Replace.
func TestAnAuthenticationInFlightCompletesUnderTheOldConfiguration(t *testing.T) {
	oldKey, newKey := newIssuerKey(t, "issuer-old"), newIssuerKey(t, "issuer-new")
	v, err := accessjwt.New(trust(t, oldKey))
	if err != nil {
		t.Fatal(err)
	}
	oldToken, newToken := oldKey.token(t), newKey.token(t)
	req := func(token string) *http.Request {
		q := httptest.NewRequest("GET", "https://reporting.example.invalid/api/reporting/v1/catalog", nil)
		q.Header.Set("Authorization", "Bearer "+token)
		return q.WithContext(context.Background())
	}
	started := v.Pin() // the authentication has read its configuration and has not finished
	if err := v.Replace(trust(t, newKey)); err != nil {
		t.Fatal(err)
	}
	if p, err := started.Authenticate(req(oldToken)); err != nil || !p.Identity.Valid() {
		t.Errorf("the authentication in flight did not complete under the old configuration: %v", err)
	}
	if _, err := started.Authenticate(req(newToken)); !errors.Is(err, r.ErrDenied) {
		t.Errorf("the authentication in flight saw the new configuration: %v", err)
	}
	if _, err := v.Authenticate(req(oldToken)); !errors.Is(err, r.ErrDenied) {
		t.Errorf("the next authentication still trusts the removed key: %v", err)
	}
	if p, err := v.Authenticate(req(newToken)); err != nil || !p.Identity.Valid() {
		t.Errorf("the next authentication does not trust the new key: %v", err)
	}
}

// A token that names a kid that is not trusted is refused, and neither the error nor the HTTP answer names a key: not the kid it named, not
// a trusted one. A token that names a trusted kid but was signed by another key is refused alike.
func TestAnUntrustedKidIsRefusedWithoutNamingAKey(t *testing.T) {
	trusted, stranger := newIssuerKey(t, "issuer-trusted"), newIssuerKey(t, "issuer-stranger")
	api := newRotationAPI(t, trust(t, trusted))
	forged := issuerKey{"issuer-trusted", stranger.key} // the trusted kid, another private key
	for name, token := range map[string]string{"an unknown kid": stranger.token(t), "a trusted kid, another key": forged.token(t)} {
		api.refuses(t, name, token, "issuer-trusted", "issuer-stranger")
		_, err := authenticate(api.v, token)
		if !errors.Is(err, r.ErrDenied) || strings.Contains(err.Error(), "issuer-") || strings.Contains(err.Error(), "kid") {
			t.Errorf("%s: the error names a key: %v", name, err)
		}
	}
}

// The two exposure windows (docs/ACCESS_JWT.md). A stolen token works until it expires, at most MaxTokenLifetime, and as often as its holder
// presents it: there is no revocation of a single token, no jti denylist. Removing the signing key is the control for a leaked key.
func TestAStolenTokenIsBoundedByItsLifetimeAndNotRevocable(t *testing.T) {
	k := newIssuerKey(t, "issuer-1")
	c := trust(t, k)
	c.MaxTokenLifetime = 60 * time.Second
	v, err := accessjwt.New(c)
	if err != nil {
		t.Fatal(err)
	}
	with := func(iat, exp time.Time) string {
		cl := claims()
		cl["iat"], cl["exp"] = iat.Unix(), exp.Unix()
		payload, _ := json.Marshal(cl)
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "at+jwt", "kid": k.kid})
		return signRaw(t, k.key, header, payload)
	}
	now := time.Now()
	if _, err := authenticate(v, with(now.Add(-10*time.Second), now.Add(50*time.Second))); err != nil {
		t.Errorf("a token whose lifetime is exactly the bound: %v", err)
	}
	if _, err := authenticate(v, with(now.Add(-10*time.Second), now.Add(51*time.Second))); !errors.Is(err, r.ErrDenied) {
		t.Errorf("a token one second over the bound: %v", err)
	}
	if _, err := authenticate(v, with(now.Add(-120*time.Second), now.Add(-time.Second))); !errors.Is(err, r.ErrDenied) {
		t.Errorf("an expired token: %v", err)
	}
	// The same token, presented over and over: nothing remembers a jti.
	stolen := with(now.Add(-10*time.Second), now.Add(50*time.Second))
	for range 3 {
		if _, err := authenticate(v, stolen); err != nil {
			t.Fatalf("a replay within the lifetime: %v", err)
		}
	}
	// What ends it before its expiry is the removal of the signing key.
	if err := v.Replace(trust(t, newIssuerKey(t, "issuer-2"))); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(v, stolen); !errors.Is(err, r.ErrDenied) {
		t.Errorf("a token of a removed key: %v", err)
	}
}
