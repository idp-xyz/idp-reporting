package accessjwt_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/identity/accessjwt"
	r "idp.local/reporting/reporting"
)

func requireDenied(t *testing.T, v *accessjwt.Verifier, token string) {
	t.Helper()
	p, err := authenticate(v, token)
	if !errors.Is(err, r.ErrDenied) || p.Identity.Valid() || len(p.Integrations) != 0 {
		t.Fatalf("expected empty denied principal, got %#v %v", p, err)
	}
}

func TestRejectInvalidAccessTokenClaims(t *testing.T) {
	k := key(t)
	v, err := accessjwt.New(config(t, k))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"expired", func(c map[string]any) { c["exp"] = now - 1 }},
		{"future_issued", func(c map[string]any) { c["iat"] = now + 60 }},
		{"future_not_before", func(c map[string]any) { c["nbf"] = now + 60 }},
		{"not_before_at_expiry", func(c map[string]any) { c["nbf"] = c["exp"] }},
		{"null_not_before", func(c map[string]any) { c["nbf"] = nil }},
		{"excessive_lifetime", func(c map[string]any) { c["exp"] = now + 3600 }},
		{"fractional_issue_time", func(c map[string]any) { c["iat"] = float64(now) - 0.5 }},
		{"string_expiry", func(c map[string]any) { c["exp"] = "9999999999" }},
		{"null_expiry", func(c map[string]any) { c["exp"] = nil }},
		{"null_issue_time", func(c map[string]any) { c["iat"] = nil }},
		{"expiry_before_issue", func(c map[string]any) { c["iat"] = now - 10; c["exp"] = now - 20 }},
		{"wrong_issuer", func(c map[string]any) { c["iss"] = "https://other-issuer.example.invalid/realm" }},
		{"issuer_trailing_slash", func(c map[string]any) { c["iss"] = issuer + "/" }},
		{"wrong_audience", func(c map[string]any) { c["aud"] = "https://different.example.invalid" }},
		{"null_audience", func(c map[string]any) { c["aud"] = nil }},
		{"mixed_audience", func(c map[string]any) { c["aud"] = []any{"https://reporting.example.invalid", true} }},
		{"null_audience_member", func(c map[string]any) { c["aud"] = []any{"https://reporting.example.invalid", nil} }},
		{"duplicate_audience", func(c map[string]any) {
			c["aud"] = []string{"https://reporting.example.invalid", "https://reporting.example.invalid"}
		}},
		{"unknown_subject_same_email", func(c map[string]any) { c["sub"] = "other-subject"; c["email"] = "admin@example.invalid" }},
		{"unknown_client", func(c map[string]any) { c["client_id"] = "service-account" }},
		{"null_client", func(c map[string]any) { c["client_id"] = nil }},
		{"null_token_id", func(c map[string]any) { c["jti"] = nil }},
		{"wrong_scope", func(c map[string]any) { c["scope"] = "openid reporting.reader" }},
		{"case_sensitive_scope", func(c map[string]any) { c["scope"] = "REPORTING.READ" }},
		{"null_scope", func(c map[string]any) { c["scope"] = nil }},
		{"array_scope", func(c map[string]any) { c["scope"] = []string{"reporting.read"} }},
		{"ambiguous_scope_spacing", func(c map[string]any) { c["scope"] = "openid  reporting.read" }},
		{"duplicate_scope", func(c map[string]any) { c["scope"] = "reporting.read reporting.read" }},
		{"empty_subject", func(c map[string]any) { c["sub"] = "" }},
		{"unsupported_delegation", func(c map[string]any) { c["act"] = map[string]string{"sub": "service-account"} }},
		{"null_delegation", func(c map[string]any) { c["act"] = nil }},
		{"proof_bound_token", func(c map[string]any) { c["cnf"] = map[string]string{"jkt": "proof-required"} }},
		{"integer_overflow", func(c map[string]any) { c["exp"] = json.Number("9223372036854775808") }},
	}
	for _, name := range []string{"iss", "sub", "aud", "exp", "iat", "jti", "client_id"} {
		field := name
		cases = append(cases, struct {
			name   string
			change func(map[string]any)
		}{"missing_" + field, func(c map[string]any) { delete(c, field) }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { c := claims(); tc.change(c); requireDenied(t, v, sign(t, k, c)) })
	}
}

func TestRejectJOSEAndCompactTokenAttacks(t *testing.T) {
	k := key(t)
	other := key(t)
	v, err := accessjwt.New(config(t, k))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(claims())
	headers := map[string]string{
		"id_token":                    `{"alg":"RS256","typ":"JWT","kid":"key-1"}`,
		"missing_purpose":             `{"alg":"RS256","kid":"key-1"}`,
		"hmac_confusion":              `{"alg":"HS256","typ":"at+jwt","kid":"key-1"}`,
		"unsigned_algorithm":          `{"alg":"none","typ":"at+jwt","kid":"key-1"}`,
		"unknown_key":                 `{"alg":"RS256","typ":"at+jwt","kid":"missing"}`,
		"key_path":                    `{"alg":"RS256","typ":"at+jwt","kid":"../../private"}`,
		"token_key_url":               `{"alg":"RS256","typ":"at+jwt","kid":"key-1","jku":"https://attacker.invalid/keys"}`,
		"token_public_key":            `{"alg":"RS256","typ":"at+jwt","kid":"key-1","jwk":{}}`,
		"critical_extension":          `{"alg":"RS256","typ":"at+jwt","kid":"key-1","crit":["unknown"]}`,
		"unencoded_payload":           `{"alg":"RS256","typ":"at+jwt","kid":"key-1","b64":false}`,
		"duplicate_algorithm":         `{"alg":"none","alg":"RS256","typ":"at+jwt","kid":"key-1"}`,
		"escaped_duplicate_algorithm": `{"alg":"none","\u0061lg":"RS256","typ":"at+jwt","kid":"key-1"}`,
	}
	for name, header := range headers {
		t.Run(name, func(t *testing.T) { requireDenied(t, v, signRaw(t, k, []byte(header), payload)) })
	}
	goodHeader := []byte(`{"alg":"RS256","typ":"at+jwt","kid":"key-1"}`)
	for name, raw := range map[string][]byte{
		"duplicate_issuer":         []byte(strings.TrimSuffix(string(payload), "}") + `,"iss":"` + issuer + `"}`),
		"escaped_duplicate_issuer": []byte(strings.TrimSuffix(string(payload), "}") + `,"\u0069ss":"` + issuer + `"}`),
		"extra_json_root":          append(append([]byte{}, payload...), []byte(` {}`)...),
		"payload_array":            []byte(`[]`),
		"invalid_utf8":             append(append([]byte{}, payload...), 0xff),
	} {
		t.Run(name, func(t *testing.T) { requireDenied(t, v, signRaw(t, k, goodHeader, raw)) })
	}
	t.Run("different_signing_key", func(t *testing.T) { requireDenied(t, v, signRaw(t, other, goodHeader, payload)) })
	valid := sign(t, k, claims())
	parts := strings.Split(valid, ".")
	t.Run("altered_signature", func(t *testing.T) {
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sig[0] ^= 1
		requireDenied(t, v, parts[0]+"."+parts[1]+"."+base64.RawURLEncoding.EncodeToString(sig))
	})
	for name, token := range map[string]string{"empty_signature": parts[0] + "." + parts[1] + ".", "padded_base64": parts[0] + "=." + parts[1] + "." + parts[2], "extra_segment": valid + ".x", "oversized": strings.Repeat("x", 32769), "newline_base64": parts[0] + "\n." + parts[1] + "." + parts[2]} {
		t.Run(name, func(t *testing.T) { requireDenied(t, v, token) })
	}
}

func TestOnlyAuthorizationHeaderProvidesCredentials(t *testing.T) {
	k := key(t)
	v, err := accessjwt.New(config(t, k))
	if err != nil {
		t.Fatal(err)
	}
	token := sign(t, k, claims())
	for _, name := range []string{"query_token", "cookie_token", "forwarded_identity", "duplicate_authorization", "basic_scheme", "extra_space", "cancelled_context"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "https://reporting.example.invalid/", nil)
			switch name {
			case "query_token":
				req.URL.RawQuery = "access_token=" + token
			case "cookie_token":
				req.Header.Set("Cookie", "access_token="+token)
			case "forwarded_identity":
				req.Header.Set("X-User-ID", "subject-a")
			case "duplicate_authorization":
				req.Header.Add("Authorization", "Bearer "+token)
				req.Header.Add("Authorization", "Bearer "+token)
			case "basic_scheme":
				req.Header.Set("Authorization", "Basic "+token)
			case "extra_space":
				req.Header.Set("Authorization", "Bearer  "+token)
			case "cancelled_context":
				req.Header.Set("Authorization", "Bearer "+token)
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			p, err := v.Authenticate(req)
			if !errors.Is(err, r.ErrDenied) || p.Identity.Valid() {
				t.Fatalf("credential bypass: %#v %v", p, err)
			}
		})
	}
}

func TestSupportedAccessTokenRepresentations(t *testing.T) {
	k := key(t)
	v, err := accessjwt.New(config(t, k))
	if err != nil {
		t.Fatal(err)
	}
	for name, typ := range map[string]string{"short": "at+jwt", "full_media_type": "application/at+jwt", "media_type_case": "at+JWT"} {
		t.Run(name, func(t *testing.T) {
			c := claims()
			c["aud"] = []string{"other-resource", "https://reporting.example.invalid"}
			c["nbf"] = time.Now().Unix() - 120
			payload, _ := json.Marshal(c)
			header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "key-1", "typ": typ})
			if p, err := authenticate(v, signRaw(t, k, header, payload)); err != nil || p.Tenant != "platform-a" {
				t.Fatalf("supported token rejected: %v", err)
			}
		})
	}
}
