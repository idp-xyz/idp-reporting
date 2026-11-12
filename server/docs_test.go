package server

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"idp.local/reporting/adapters/httpsource"
	r "idp.local/reporting/reporting"
)

// The example in docs/DEPLOYMENT.md is parsed by the production loader, and says nothing the shipped example must not: no simulator allowance.
func TestDeploymentExampleIsAValidConfiguration(t *testing.T) {
	doc, err := os.ReadFile("../docs/DEPLOYMENT.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)```json\n(.*?)\n```").FindSubmatch(doc)
	if m == nil {
		t.Fatal("no JSON example in docs/DEPLOYMENT.md")
	}
	c, err := ParseConfig(m[1])
	if err != nil {
		t.Fatalf("the example is refused: %v", err)
	}
	if c.AllowSimulator || strings.Contains(string(m[1]), "allow_simulator") {
		t.Error("the shipped example allows simulators")
	}
	// The example's connections use the shipped adapter, with options it reads (S19-A), every one of which the reference lists.
	for _, conn := range c.Connections {
		var o httpsource.Options
		if conn.Adapter != HTTPAdapter || r.DecodeStrict(bytes.NewReader(conn.Options), &o) != nil || !strings.HasPrefix(o.BaseURL, "https://") {
			t.Errorf("the example's connection %q is not an http connection the adapter reads", conn.Integration)
		}
	}
	for _, option := range []string{"base_url", "loopback_http", "timeout", "ca_file", "client_cert_file", "client_key_file", "auth_env", "max_response_bytes", "issuance", "companies"} {
		if !strings.Contains(string(doc), "`"+option+"`") {
			t.Errorf("the http options table does not list %s", option)
		}
	}
	// Every field of the schema is explained in the reference table.
	for _, field := range []string{"listen", "metrics_listen", "tls_cert_file", "tls_key_file", "allow_plain_loopback", "allow_simulator", "read_header_timeout", "read_timeout", "write_timeout", "idle_timeout", "max_header_bytes", "max_in_flight", "workers", "shutdown_grace", "store.pool_max_conns", "object_store.endpoint", "issuer.issuer", "issuer.max_token_lifetime", "issuer.jwks_file", "cursor_key_files", "connections", "quotas.*", "retention.sweep_interval", "retention.results", "pdf.typst", "layout_directory", "dashboard_directory", "template_directory", "background_directory", "embed_origins", "profile_label", "sign_in.authorization_endpoint", "sign_in.token_endpoint", "sign_in.client_id", "sign_in.redirect_uri", "sign_in.scope", "sign_in.paste_fallback", "sign_in.ca_file", "sign_in.loopback_http", "sign_in.timeout",
		"schedules", "schedules.poll_interval", "schedules.webhook.url", "schedules.email.host", "schedules.email.username", "schedules.email.starttls", "schedules.email.timeout", "schedules.email.console_url"} {
		if !strings.Contains(string(doc), "`"+field+"`") && !strings.Contains(string(doc), "`"+field+"`,") {
			t.Errorf("the reference does not list %s", field)
		}
	}
}
