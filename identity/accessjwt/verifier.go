// Package accessjwt verifies the provisioned RS256 access-token profile at the
// reporting HTTP identity boundary. It does not implement login or discovery.
package accessjwt

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	r "idp.local/reporting/reporting"
)

const maxTokenBytes = 32 << 10

// ErrInvalidConfig deliberately contains no key, subject or configuration data.
var ErrInvalidConfig = errors.New("INVALID_ACCESS_JWT_CONFIG")

// ErrInsufficientScope identifies a verified, mapped token lacking the
// provisioned scope. Transports may recognize InsufficientScope() for RFC 6750.
var ErrInsufficientScope error = scopeDenied{}

type scopeDenied struct{}

func (scopeDenied) Error() string           { return "INSUFFICIENT_TOKEN_SCOPE" }
func (scopeDenied) Unwrap() error           { return r.ErrDenied }
func (scopeDenied) InsufficientScope() bool { return true }

// Binding is provisioned by a trusted operator, never populated from a token's
// tenant, email, role or product claims. It does not grant source business rights.
type Binding struct {
	Subject, ClientID, Tenant string
	Integrations              []string
}

// Config is one issuer's explicit trust and identity map. JWKS contains public
// keys obtained out of band from that issuer. No URL in a JWT is ever fetched.
type Config struct {
	Issuer, Audience, RequiredScope string
	JWKS                            []byte
	MaxTokenLifetime                time.Duration
	Bindings                        []Binding
}

type subjectClient struct{ subject, clientID string }
type configuration struct {
	issuer, audience, requiredScope string
	maxLifetime                     int64
	keys                            map[string]*rsa.PublicKey
	bindings                        map[subjectClient]r.Principal
}

// Verifier has an immutable configuration snapshot per authentication. Its zero
// value denies all requests. Configuration replacement is atomic and local.
type Verifier struct{ current atomic.Pointer[configuration] }

func New(c Config) (*Verifier, error) {
	v := &Verifier{}
	if err := v.Replace(c); err != nil {
		return nil, err
	}
	return v, nil
}

// Replace validates the whole configuration before publishing it. Failure keeps
// the previous snapshot. Already-started authentications may finish on that
// snapshot; subsequent calls use the new keys and bindings. Empty bindings deny
// all identities. Production key acquisition and operator authorization are the
// caller's responsibility; this method is not exposed as an HTTP endpoint.
func (v *Verifier) Replace(c Config) error {
	if v == nil {
		return ErrInvalidConfig
	}
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(c.Issuer, "#") ||
		!text(c.Issuer, 512) || !text(c.Audience, 512) || !scopeToken(c.RequiredScope) || c.MaxTokenLifetime < time.Second || c.MaxTokenLifetime > time.Hour || c.MaxTokenLifetime%time.Second != 0 || len(c.Bindings) > 4096 {
		return ErrInvalidConfig
	}
	keys, err := publicKeys(c.JWKS)
	if err != nil {
		return ErrInvalidConfig
	}
	next := &configuration{issuer: c.Issuer, audience: c.Audience, requiredScope: c.RequiredScope, maxLifetime: int64(c.MaxTokenLifetime / time.Second), keys: keys, bindings: make(map[subjectClient]r.Principal)}
	for _, b := range c.Bindings {
		p := r.Principal{Identity: r.Identity{Tenant: b.Tenant, Issuer: c.Issuer, Subject: b.Subject}, Integrations: slices.Clone(b.Integrations)}
		id := subjectClient{b.Subject, b.ClientID}
		if _, exists := next.bindings[id]; exists {
			return ErrInvalidConfig
		}
		if !text(b.Subject, 256) || !text(b.ClientID, 256) || !text(b.Tenant, 256) || !p.Identity.Valid() || len(b.Integrations) == 0 || len(b.Integrations) > 32 {
			return ErrInvalidConfig
		}
		seen := make(map[string]bool)
		for _, product := range b.Integrations {
			if !text(product, 128) || product == "*" || seen[product] {
				return ErrInvalidConfig
			}
			seen[product] = true
		}
		next.bindings[id] = p
	}
	v.current.Store(next)
	return nil
}

// Authenticate implements transport/httpapi.Authenticate without importing the
// transport. Only one Authorization Bearer header is read. Returned identities
// are copied; neither claims nor forwarded headers can expand their bindings.
func (v *Verifier) Authenticate(req *http.Request) (r.Principal, error) {
	denied := func() (r.Principal, error) { return r.Principal{}, r.ErrDenied }
	if v == nil || req == nil || req.Context().Err() != nil {
		return denied()
	}
	c := v.current.Load()
	if c == nil || len(req.Header.Values("Authorization")) != 1 {
		return denied()
	}
	header := req.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(token) == 0 || len(token) > maxTokenBytes {
		return denied()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return denied()
	}
	head, err := segment(parts[0], 2048)
	if err != nil {
		return denied()
	}
	h, err := object(head, 3)
	if err != nil || len(h) != 3 || stringValue(h["alg"]) != "RS256" {
		return denied()
	}
	typ := stringValue(h["typ"])
	if !strings.EqualFold(typ, "at+jwt") && !strings.EqualFold(typ, "application/at+jwt") {
		return denied()
	}
	public := c.keys[stringValue(h["kid"])]
	if public == nil {
		return denied()
	}
	sig, err := segment(parts[2], 512)
	if err != nil {
		return denied()
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], sig) != nil {
		return denied()
	}
	payload, err := segment(parts[1], 24<<10)
	if err != nil {
		return denied()
	}
	claims, err := object(payload, 64)
	if err != nil {
		return denied()
	}
	if _, exists := claims["act"]; exists {
		return denied()
	}
	if _, exists := claims["cnf"]; exists {
		return denied()
	}
	if stringValue(claims["iss"]) != c.issuer || !audience(claims["aud"], c.audience) || !text(stringValue(claims["jti"]), 512) {
		return denied()
	}
	exp, err := date(claims["exp"])
	if err != nil {
		return denied()
	}
	iat, err := date(claims["iat"])
	now := time.Now().Unix()
	if err != nil || iat == 0 || exp <= now || iat > now || exp <= iat || exp-iat > c.maxLifetime {
		return denied()
	}
	if raw, exists := claims["nbf"]; exists {
		nbf, err := date(raw)
		if err != nil || nbf > now || nbf >= exp {
			return denied()
		}
	}
	p, exists := c.bindings[subjectClient{stringValue(claims["sub"]), stringValue(claims["client_id"])}]
	if !exists {
		return denied()
	}
	valid, allowed := scope(claims["scope"], c.requiredScope)
	if !valid {
		return denied()
	}
	if !allowed {
		return r.Principal{}, ErrInsufficientScope
	}
	p.Integrations = slices.Clone(p.Integrations)
	return p, nil
}

func text(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, ch := range s {
		if unicode.IsControl(ch) || ch == utf8.RuneError {
			return false
		}
	}
	return true
}

func segment(encoded string, max int) ([]byte, error) {
	if encoded == "" || len(encoded) > base64.RawURLEncoding.EncodedLen(max) {
		return nil, r.ErrDenied
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(b) > max || base64.RawURLEncoding.EncodeToString(b) != encoded {
		return nil, r.ErrDenied
	}
	return b, nil
}

// object rejects duplicate decoded member names, nonobjects and extra JSON roots.
// Nested extension claims are bounded by input size and never authorize access.
func object(data []byte, maxMembers int) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, r.ErrDenied
	}
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, r.ErrDenied
	}
	out := make(map[string]json.RawMessage)
	for d.More() {
		t, err := d.Token()
		name, ok := t.(string)
		if err != nil || !ok || len(out) >= maxMembers {
			return nil, r.ErrDenied
		}
		if _, exists := out[name]; exists {
			return nil, r.ErrDenied
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, r.ErrDenied
		}
		out[name] = raw
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return nil, r.ErrDenied
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, r.ErrDenied
	}
	return out, nil
}

func stringValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func date(raw json.RawMessage) (int64, error) {
	var n int64
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || json.Unmarshal(raw, &n) != nil || n < 0 {
		return 0, r.ErrDenied
	}
	return n, nil
}

func audience(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	if raw[0] == '"' {
		return stringValue(raw) == want
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 || len(values) > 16 {
		return false
	}
	seen := make(map[string]bool)
	for _, a := range values {
		if !text(a, 512) || seen[a] {
			return false
		}
		seen[a] = true
	}
	return seen[want]
}

func scopeToken(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, b := range []byte(s) {
		if b < 0x21 || b > 0x7e || b == '"' || b == '\\' {
			return false
		}
	}
	return true
}

func scope(raw json.RawMessage, want string) (valid, allowed bool) {
	if len(raw) == 0 {
		return true, false
	}
	var s string
	if string(raw) == "null" || json.Unmarshal(raw, &s) != nil || len(s) > 4096 {
		return false, false
	}
	if s == "" {
		return true, false
	}
	values := strings.Split(s, " ")
	if len(values) > 64 {
		return false, false
	}
	seen := make(map[string]bool)
	for _, v := range values {
		if !scopeToken(v) || seen[v] {
			return false, false
		}
		seen[v] = true
	}
	return true, seen[want]
}

func publicKeys(data []byte) (map[string]*rsa.PublicKey, error) {
	if len(data) == 0 || len(data) > 64<<10 {
		return nil, r.ErrDenied
	}
	root, err := object(data, 1)
	if err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	if json.Unmarshal(root["keys"], &entries) != nil || len(entries) == 0 || len(entries) > 16 {
		return nil, r.ErrDenied
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, entry := range entries {
		j, err := object(entry, 16)
		if err != nil {
			return nil, err
		}
		for field := range j {
			switch field {
			case "kty", "kid", "n", "e", "use", "alg", "key_ops", "x5c", "x5t", "x5t#S256":
			default:
				return nil, r.ErrDenied
			}
		}
		id := stringValue(j["kid"])
		if stringValue(j["kty"]) != "RSA" || !text(id, 128) || keys[id] != nil {
			return nil, r.ErrDenied
		}
		for name, want := range map[string]string{"use": "sig", "alg": "RS256"} {
			if raw, exists := j[name]; exists && stringValue(raw) != want {
				return nil, r.ErrDenied
			}
		}
		if raw, exists := j["key_ops"]; exists {
			var ops []string
			if json.Unmarshal(raw, &ops) != nil || len(ops) != 1 || ops[0] != "verify" {
				return nil, r.ErrDenied
			}
		}
		n, err := segment(stringValue(j["n"]), 512)
		if err != nil || len(n) == 0 || n[0] == 0 {
			return nil, r.ErrDenied
		}
		e, err := segment(stringValue(j["e"]), 3)
		if err != nil || !bytes.Equal(e, []byte{1, 0, 1}) {
			return nil, r.ErrDenied
		}
		modulus := new(big.Int).SetBytes(n)
		if modulus.BitLen() < 2048 || modulus.BitLen() > 4096 || modulus.Bit(0) != 1 {
			return nil, r.ErrDenied
		}
		keys[id] = &rsa.PublicKey{N: modulus, E: 65537}
	}
	return keys, nil
}
