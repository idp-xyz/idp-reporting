package embedding_test

import (
	"slices"
	"strings"
	"testing"

	"idp.local/reporting/embedding"
)

func TestAcceptsOnlyExactSerializedOrigins(t *testing.T) {
	for _, origin := range []string{
		"https://brant.example", "https://app.brant.example:8443", "https://xn--fiqs8s.example", "https://203.0.113.7", "https://203.0.113.7:8443",
		"https://[2001:db8::1]", "https://[2001:db8::1]:8443", "https://localhost:8443", "https://a-b.c-d.example", "https://a1.example",
	} {
		if _, err := embedding.New(map[string][]string{"brant": {origin}}, embedding.Options{}); err != nil {
			t.Errorf("%q refused: %v", origin, err)
		}
	}
	for name, origin := range map[string]string{
		"empty":                   "",
		"no scheme":               "brant.example",
		"scheme only":             "https://",
		"trailing slash":          "https://brant.example/",
		"path":                    "https://brant.example/app",
		"query":                   "https://brant.example?x=1",
		"query after slash":       "https://brant.example/?x=1",
		"fragment":                "https://brant.example#x",
		"user information":        "https://user@brant.example",
		"password":                "https://user:pw@brant.example",
		"upper-case host":         "https://Brant.example",
		"upper-case scheme":       "HTTPS://brant.example",
		"default port":            "https://brant.example:443",
		"empty port":              "https://brant.example:",
		"leading zero port":       "https://brant.example:0443",
		"port zero":               "https://brant.example:0",
		"port too large":          "https://brant.example:65536",
		"port with sign":          "https://brant.example:+8443",
		"port with letters":       "https://brant.example:80a",
		"wildcard":                "https://*.brant.example",
		"bare wildcard":           "*",
		"wildcard scheme":         "*://brant.example",
		"null":                    "null",
		"javascript":              "javascript:alert(1)",
		"data":                    "data:text/html,x",
		"ftp":                     "ftp://brant.example",
		"http for a public host":  "http://brant.example",
		"http for a lan address":  "http://192.168.1.2:8080",
		"http loopback not asked": "http://127.0.0.1:8080",
		"backslash":               "https://brant.example\\@evil.example",
		"percent":                 "https://brant%2eexample",
		"space":                   "https://brant.example ",
		"comma list":              "https://brant.example,https://evil.example",
		"semicolon":               "https://brant.example;frame-ancestors *",
		"quote":                   "https://brant.example'",
		"newline":                 "https://brant.example\n",
		"non-ASCII host":          "https://底.example",
		"trailing dot":            "https://brant.example.",
		"empty label":             "https://brant..example",
		"leading hyphen label":    "https://-brant.example",
		"numeric last label":      "https://brant.123",
		"hex IPv4":                "https://0x7f.1",
		"leading zero IPv4":       "https://01.2.3.4",
		"short IPv4":              "https://127.1",
		"IPv6 not canonical":      "https://[2001:0db8::1]",
		"IPv6 upper case":         "https://[2001:DB8::1]",
		"IPv6 zone":               "https://[fe80::1%25eth0]",
		"IPv4-mapped IPv6":        "https://[::ffff:203.0.113.7]",
		"IPv6 without brackets":   "https://2001:db8::1",
		"IPv6 text after":         "https://[2001:db8::1]x",
		"IPv6 unterminated":       "https://[2001:db8::1",
		"two origins":             "https://brant.example https://evil.example",
		"too long":                "https://" + strings.Repeat("a", 250) + ".example",
	} {
		if _, err := embedding.New(map[string][]string{"brant": {origin}}, embedding.Options{}); err == nil {
			t.Errorf("%s: %q accepted", name, origin)
		}
	}
}

func TestLoopbackHTTPOnlyWhenAskedAndOnlyForLoopbackAddresses(t *testing.T) {
	asked := embedding.Options{LoopbackHTTP: true}
	for _, origin := range []string{"http://127.0.0.1:7811", "http://127.0.0.1", "http://127.1.2.3:9", "http://[::1]:7811"} {
		if _, err := embedding.New(map[string][]string{"brant": {origin}}, asked); err != nil {
			t.Errorf("%q refused with loopback http asked: %v", origin, err)
		}
		if _, err := embedding.New(map[string][]string{"brant": {origin}}, embedding.Options{}); err == nil {
			t.Errorf("%q accepted without loopback http asked", origin)
		}
	}
	for _, origin := range []string{"http://localhost:7811", "http://10.0.0.1:7811", "http://brant.example", "http://0.0.0.0:7811", "http://127.0.0.1:80", "http://[::]:7811", "http://[::ffff:127.0.0.1]:7811"} {
		if _, err := embedding.New(map[string][]string{"brant": {origin}}, asked); err == nil {
			t.Errorf("%q accepted for loopback http", origin)
		}
	}
}

func TestIntegrationsAndLimits(t *testing.T) {
	good := []string{"https://a.example"}
	for name, id := range map[string]string{"empty": "", "space": "a b", "slash": "a/b", "equals": "a=b", "leading dot": ".a", "leading hyphen": "-a", "unicode": "产品", "too long": strings.Repeat("a", 129)} {
		if _, err := embedding.New(map[string][]string{id: good}, embedding.Options{}); err == nil {
			t.Errorf("integration %s accepted", name)
		}
	}
	if _, err := embedding.New(map[string][]string{"brant": nil}, embedding.Options{}); err == nil {
		t.Error("an integration without origins accepted")
	}
	if _, err := embedding.New(map[string][]string{"brant": {"https://a.example", "https://a.example"}}, embedding.Options{}); err == nil {
		t.Error("a duplicate origin accepted")
	}
	many := map[string][]string{}
	for i := 0; i < embedding.MaxIntegrations+1; i++ {
		many["i"+string(rune('a'+i%26))+strings.Repeat("x", i/26)] = good
	}
	if _, err := embedding.New(many, embedding.Options{}); err == nil {
		t.Error("too many integrations accepted")
	}
	var origins []string
	for i := 0; i < embedding.MaxOriginsPerIntegration+1; i++ {
		origins = append(origins, "https://h"+strings.Repeat("x", i)+".example")
	}
	if _, err := embedding.New(map[string][]string{"brant": origins}, embedding.Options{}); err == nil {
		t.Error("too many origins accepted")
	}
	if _, err := embedding.New(map[string][]string{"brant": origins[:embedding.MaxOriginsPerIntegration]}, embedding.Options{}); err != nil {
		t.Errorf("the limit itself refused: %v", err)
	}
	// The configuration is a copy: changing the caller's map afterwards changes nothing.
	source := map[string][]string{"brant": {"https://a.example"}}
	config, err := embedding.New(source, embedding.Options{})
	if err != nil {
		t.Fatal(err)
	}
	source["brant"][0] = "https://evil.example"
	source["other"] = good
	if config.Allows("brant", "https://evil.example") || !config.Allows("brant", "https://a.example") || config.Allows("other", "https://a.example") {
		t.Error("the configuration shares the caller's map")
	}
}

func TestAllowsIsExactPerIntegration(t *testing.T) {
	config, err := embedding.Parse([]string{"brant=https://app.brant.example", "brant=https://admin.brant.example:8443", "second=https://second.example"}, embedding.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		integration, origin string
		want                bool
	}{
		{"brant", "https://app.brant.example", true},
		{"brant", "https://admin.brant.example:8443", true},
		{"second", "https://second.example", true},
		{"brant", "https://second.example", false},
		{"second", "https://app.brant.example", false},
		{"unknown", "https://app.brant.example", false},
		{"", "https://app.brant.example", false},
		{"brant", "", false},
		{"brant", "https://app.brant.example/", false},
		{"brant", "https://APP.brant.example", false},
		{"brant", "https://app.brant.example:443", false},
		{"brant", "https://admin.brant.example", false},
		{"brant", "https://app.brant.example.evil.example", false},
		{"brant", "https://evil.example?https://app.brant.example", false},
		{"brant", "null", false},
		{"brant", "*", false},
		{"brant", "https://app.brant.example https://evil.example", false},
	} {
		if got := config.Allows(c.integration, c.origin); got != c.want {
			t.Errorf("Allows(%q, %q) = %t", c.integration, c.origin, got)
		}
	}
	if !config.Configured([]string{"x", "second"}) || config.Configured([]string{"x"}) || config.Configured(nil) {
		t.Error("Configured is wrong")
	}
	if got := config.Integrations(); !slices.Equal(got, []string{"brant", "second"}) {
		t.Errorf("Integrations = %v", got)
	}
}

func TestNilAndEmptyAllowNothing(t *testing.T) {
	var none *embedding.Config
	empty, err := embedding.Parse(nil, embedding.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []*embedding.Config{none, empty, {}} {
		if config.Allows("brant", "https://a.example") || config.Configured([]string{"brant"}) || len(config.Integrations()) != 0 {
			t.Error("an empty configuration allows or reports something")
		}
	}
}

func TestParseRefusesMalformedFlags(t *testing.T) {
	for name, value := range map[string]string{"no equals": "brant", "no integration": "=https://a.example", "no origin": "brant=", "bad origin": "brant=https://a.example/x", "two equals": "brant=https://a.example=x"} {
		if _, err := embedding.Parse([]string{value}, embedding.Options{}); err == nil {
			t.Errorf("%s: %q accepted", name, value)
		}
	}
}
