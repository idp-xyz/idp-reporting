package accessjwt_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"idp.local/reporting/identity/accessjwt"
	r "idp.local/reporting/reporting"
)

const issuer = "https://issuer.example.invalid/realm"

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func config(t *testing.T, k *rsa.PrivateKey) accessjwt.Config {
	t.Helper()
	jwks, err := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "RSA", "kid": "key-1", "alg": "RS256", "use": "sig", "key_ops": []string{"verify"},
		"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return accessjwt.Config{Issuer: issuer, Audience: "https://reporting.example.invalid", RequiredScope: "reporting.read", JWKS: jwks, MaxTokenLifetime: 15 * time.Minute,
		Bindings: []accessjwt.Binding{{Subject: "subject-a", ClientID: "web-a", Tenant: "platform-a", Integrations: []string{"finance-a"}}}}
}

func claims() map[string]any {
	now := time.Now().Unix()
	return map[string]any{"iss": issuer, "sub": "subject-a", "client_id": "web-a", "aud": "https://reporting.example.invalid", "iat": now - 60, "exp": now + 300, "jti": "test-token", "scope": "reporting.read"}
}

func signRaw(t *testing.T, k *rsa.PrivateKey, header, payload []byte) string {
	t.Helper()
	data := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(data))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return data + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func sign(t *testing.T, k *rsa.PrivateKey, c map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return signRaw(t, k, []byte(`{"alg":"RS256","typ":"at+jwt","kid":"key-1"}`), payload)
}

func authenticate(v *accessjwt.Verifier, token string) (r.Principal, error) {
	req := httptest.NewRequest("GET", "https://reporting.example.invalid/api/reporting/v1/catalog", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tenant-ID", "attacker")
	req.Header.Set("X-User-ID", "administrator")
	return v.Authenticate(req)
}

func TestSignedAccessTokenProducesOnlyConfiguredPrincipal(t *testing.T) {
	k := key(t)
	v, err := accessjwt.New(config(t, k))
	if err != nil {
		t.Fatal(err)
	}
	c := claims()
	c["tenant"], c["roles"], c["integrations"], c["email"] = "attacker", []string{"admin"}, []string{"all"}, "admin@example.invalid"
	p, err := authenticate(v, sign(t, k, c))
	want := r.Principal{Identity: r.Identity{Tenant: "platform-a", Issuer: issuer, Subject: "subject-a"}, Integrations: []string{"finance-a"}}
	if err != nil || !reflect.DeepEqual(p, want) {
		t.Fatalf("principal=%#v error=%v", p, err)
	}
	p.Integrations[0] = "mutated"
	p, err = authenticate(v, sign(t, k, c))
	if err != nil || !reflect.DeepEqual(p, want) {
		t.Fatalf("principal alias=%#v error=%v", p, err)
	}
}

func TestIssuerWithEmptyFragmentIsNotTrustable(t *testing.T) {
	c := config(t, key(t))
	c.Issuer += "#"
	if _, err := accessjwt.New(c); err == nil {
		t.Fatal("issuer URL fragment delimiter accepted")
	}
}

func TestTokenWithoutReportingScopeIsDenied(t *testing.T) {
	k := key(t)
	v, err := accessjwt.New(config(t, k))
	if err != nil {
		t.Fatal(err)
	}
	c := claims()
	delete(c, "scope")
	requireDenied(t, v, sign(t, k, c))
}
