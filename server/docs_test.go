package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
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
	// Every field of the schema is explained in the reference table.
	for _, field := range []string{"listen", "metrics_listen", "tls_cert_file", "tls_key_file", "allow_plain_loopback", "allow_simulator", "read_header_timeout", "read_timeout", "write_timeout", "idle_timeout", "max_header_bytes", "max_in_flight", "workers", "shutdown_grace", "store.pool_max_conns", "object_store.endpoint", "issuer.issuer", "issuer.max_token_lifetime", "issuer.jwks_file", "cursor_key_files", "connections", "quotas.*", "retention.sweep_interval", "pdf.typst", "layout_directory", "dashboard_directory", "template_directory", "background_directory", "embed_origins", "profile_label"} {
		if !strings.Contains(string(doc), "`"+field+"`") && !strings.Contains(string(doc), "`"+field+"`,") {
			t.Errorf("the reference does not list %s", field)
		}
	}
}
