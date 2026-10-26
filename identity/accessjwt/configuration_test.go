package accessjwt_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/identity/accessjwt"
)

func editKey(t *testing.T, c *accessjwt.Config, fn func(map[string]any)) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(c.JWKS, &root); err != nil {
		t.Fatal(err)
	}
	fn(root["keys"].([]any)[0].(map[string]any))
	var err error
	c.JWKS, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidTrustConfiguration(t *testing.T) {
	k := key(t)
	cases := []struct {
		name   string
		change func(*accessjwt.Config)
	}{
		{"http_issuer", func(c *accessjwt.Config) { c.Issuer = "http://issuer.example.invalid" }},
		{"issuer_query", func(c *accessjwt.Config) { c.Issuer += "?issuer=other" }},
		{"issuer_empty_query", func(c *accessjwt.Config) { c.Issuer += "?" }},
		{"issuer_userinfo", func(c *accessjwt.Config) { c.Issuer = "https://user@issuer.example.invalid" }},
		{"issuer_fragment", func(c *accessjwt.Config) { c.Issuer += "#other" }},
		{"empty_audience", func(c *accessjwt.Config) { c.Audience = "" }},
		{"empty_scope", func(c *accessjwt.Config) { c.RequiredScope = "" }},
		{"invalid_scope", func(c *accessjwt.Config) { c.RequiredScope = "reporting read" }},
		{"zero_max_lifetime", func(c *accessjwt.Config) { c.MaxTokenLifetime = 0 }},
		{"excessive_max_lifetime", func(c *accessjwt.Config) { c.MaxTokenLifetime = 2 * time.Hour }},
		{"fractional_max_lifetime", func(c *accessjwt.Config) { c.MaxTokenLifetime = time.Second + time.Millisecond }},
		{"empty_subject", func(c *accessjwt.Config) { c.Bindings[0].Subject = "" }},
		{"empty_client", func(c *accessjwt.Config) { c.Bindings[0].ClientID = "" }},
		{"empty_tenant", func(c *accessjwt.Config) { c.Bindings[0].Tenant = "" }},
		{"empty_products", func(c *accessjwt.Config) { c.Bindings[0].Integrations = nil }},
		{"wildcard_products", func(c *accessjwt.Config) { c.Bindings[0].Integrations = []string{"*"} }},
		{"duplicate_products", func(c *accessjwt.Config) { c.Bindings[0].Integrations = []string{"finance-a", "finance-a"} }},
		{"duplicate_binding", func(c *accessjwt.Config) { c.Bindings = append(c.Bindings, c.Bindings[0]) }},
		{"excessive_bindings", func(c *accessjwt.Config) { c.Bindings = make([]accessjwt.Binding, 4097) }},
		{"empty_jwks", func(c *accessjwt.Config) { c.JWKS = nil }},
		{"oversized_jwks", func(c *accessjwt.Config) { c.JWKS = make([]byte, 65537) }},
		{"no_keys", func(c *accessjwt.Config) { c.JWKS = []byte(`{"keys":[]}`) }},
		{"unknown_root", func(c *accessjwt.Config) { c.JWKS = []byte(`{"keys":[],"other":1}`) }},
		{"non_rsa_key", func(c *accessjwt.Config) { editKey(t, c, func(j map[string]any) { j["kty"] = "EC" }) }},
		{"key_for_encryption", func(c *accessjwt.Config) { editKey(t, c, func(j map[string]any) { j["use"] = "enc" }) }},
		{"wrong_key_algorithm", func(c *accessjwt.Config) { editKey(t, c, func(j map[string]any) { j["alg"] = "HS256" }) }},
		{"key_for_signing", func(c *accessjwt.Config) { editKey(t, c, func(j map[string]any) { j["key_ops"] = []string{"sign"} }) }},
		{"null_key_operations", func(c *accessjwt.Config) { editKey(t, c, func(j map[string]any) { j["key_ops"] = nil }) }},
		{"weak_rsa_modulus", func(c *accessjwt.Config) {
			editKey(t, c, func(j map[string]any) { j["n"] = base64.RawURLEncoding.EncodeToString([]byte{1, 1}) })
		}},
		{"other_exponent", func(c *accessjwt.Config) { editKey(t, c, func(j map[string]any) { j["e"] = "Aw" }) }},
		{"private_key_material", func(c *accessjwt.Config) { editKey(t, c, func(j map[string]any) { j["d"] = "not-a-real-secret" }) }},
		{"token_key_url", func(c *accessjwt.Config) {
			editKey(t, c, func(j map[string]any) { j["jku"] = "https://attacker.invalid" })
		}},
		{"duplicate_key_id", func(c *accessjwt.Config) {
			var root map[string][]json.RawMessage
			_ = json.Unmarshal(c.JWKS, &root)
			root["keys"] = append(root["keys"], root["keys"][0])
			c.JWKS, _ = json.Marshal(root)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := config(t, k)
			tc.change(&c)
			v, err := accessjwt.New(c)
			if !errors.Is(err, accessjwt.ErrInvalidConfig) || v != nil {
				t.Fatalf("bad trust accepted: %v", err)
			}
		})
	}
}

func TestReplaceTrustKeysAndRevokeIdentity(t *testing.T) {
	k1, k2 := key(t), key(t)
	c1, c2 := config(t, k1), config(t, k2)
	v, err := accessjwt.New(c1)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, newToken := sign(t, k1, claims()), sign(t, k2, claims())
	if _, err := authenticate(v, oldToken); err != nil {
		t.Fatal(err)
	}
	invalid := c2
	invalid.Audience = ""
	if err := v.Replace(invalid); !errors.Is(err, accessjwt.ErrInvalidConfig) {
		t.Fatal("invalid replacement accepted")
	}
	if _, err := authenticate(v, oldToken); err != nil {
		t.Fatal("failed replacement changed active trust")
	}
	if err := v.Replace(c2); err != nil {
		t.Fatal(err)
	}
	requireDenied(t, v, oldToken)
	if _, err := authenticate(v, newToken); err != nil {
		t.Fatal("new provisioned key denied")
	}
	c2.Bindings = nil
	if err := v.Replace(c2); err != nil {
		t.Fatal(err)
	}
	requireDenied(t, v, newToken)
}

func TestConfigurationDoesNotAliasCallerMemory(t *testing.T) {
	k := key(t)
	c := config(t, k)
	v, err := accessjwt.New(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := range c.JWKS {
		c.JWKS[i] = 'x'
	}
	c.Bindings[0].Subject = "other"
	c.Bindings[0].Integrations[0] = "all"
	p, err := authenticate(v, sign(t, k, claims()))
	if err != nil || p.Subject != "subject-a" || len(p.Integrations) != 1 || p.Integrations[0] != "finance-a" {
		t.Fatal("caller changed active trust")
	}
}

func TestConcurrentAuthenticationAndConfigurationReplacement(t *testing.T) {
	k := key(t)
	a := config(t, k)
	b := config(t, k)
	b.Bindings[0].Tenant = "platform-b"
	b.Bindings[0].Integrations = []string{"shipment-b"}
	v, err := accessjwt.New(a)
	if err != nil {
		t.Fatal(err)
	}
	token := sign(t, k, claims())
	var wg sync.WaitGroup
	errorsFound := make(chan string, 16)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				p, err := authenticate(v, token)
				if err != nil || len(p.Integrations) != 1 || !((p.Tenant == "platform-a" && p.Integrations[0] == "finance-a") || (p.Tenant == "platform-b" && p.Integrations[0] == "shipment-b")) {
					errorsFound <- "mixed configuration or invalid identity"
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			if v.Replace(b) != nil || v.Replace(a) != nil {
				errorsFound <- "replacement failed"
				return
			}
		}
	}()
	wg.Wait()
	close(errorsFound)
	for message := range errorsFound {
		t.Error(message)
	}
}
