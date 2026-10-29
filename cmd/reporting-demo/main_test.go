package main

import (
	"errors"
	"fmt"
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
)

// The demo says why a fixture store cannot be opened when the reason is the platform's own refusal,
// and nothing but the generic text otherwise.
func TestOpenFailureKeepsThePlatformsRefusalsAndHidesTheRest(t *testing.T) {
	const generic = "guarded fixture could not be opened"
	precondition := fmt.Errorf("migration 004 up: 3 issue jobs and records exist; run reporting-migrate down --to 2 --accept-data-loss first: %w", r.ErrDenied)
	for name, e := range map[string]error{
		"a denial with its remedy":  precondition,
		"a denial of the database":  r.ErrDenied,
		"an integrity failure":      fmt.Errorf("schema ledger: version 2 was applied from a different file: %w", r.ErrIntegrity),
		"an invalid request":        fmt.Errorf("a DSN that is not one: %w", r.ErrInvalid),
		"a conflict":                fmt.Errorf("the ledger is gone: another migrator reverted the schema, rerun: %w", r.ErrConflict),
		"a refusal wrapped further": fmt.Errorf("opening: %w", precondition),
	} {
		got := openFailure(generic, e)
		if !strings.HasPrefix(got.Error(), generic+": ") || !strings.Contains(got.Error(), e.Error()) || !errors.Is(got, e) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := openFailure(generic, precondition); !strings.Contains(got.Error(), "reporting-migrate down --to 2 --accept-data-loss") {
		t.Errorf("the remedy is not in the message: %q", got)
	}
	// Anything else is the generic text alone: a connection failure may carry the host, the user and
	// the database of the DSN.
	for name, e := range map[string]error{
		"a connection failure":    errors.New("failed to connect to `host=127.0.0.1 user=postgres database=reporting_test_s01`: dial error"),
		"a plain error":           errors.New("boom"),
		"a wrapped login failure": fmt.Errorf("wrapped: %w", errors.New("password authentication failed for user postgres")),
	} {
		if got := openFailure(generic, e); got.Error() != generic || strings.Contains(got.Error(), "127.0.0.1") || strings.Contains(got.Error(), "password") {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// -embed-origin is read strictly and at startup: exact origins only, loopback http only here, and only for the integrations the demo has.
func TestEmbedConfigStopsTheStartOnAnythingMalformed(t *testing.T) {
	integrations := []string{"brant-contract-demo", "shipment-contract-demo"}
	config, err := embedConfig([]string{"brant-contract-demo=http://127.0.0.1:7811", "brant-contract-demo=https://app.brant.example", "shipment-contract-demo=https://ship.example:8443"}, integrations)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Allows("brant-contract-demo", "http://127.0.0.1:7811") || !config.Allows("shipment-contract-demo", "https://ship.example:8443") || config.Allows("shipment-contract-demo", "http://127.0.0.1:7811") {
		t.Error("the configuration is not what was given")
	}
	if config, err := embedConfig(nil, integrations); err != nil || len(config.Integrations()) != 0 {
		t.Errorf("no flag is an empty configuration: %v", err)
	}
	for name, value := range map[string]string{
		"no equals":            "brant-contract-demo",
		"a path":               "brant-contract-demo=https://app.brant.example/app",
		"a trailing slash":     "brant-contract-demo=https://app.brant.example/",
		"a wildcard":           "brant-contract-demo=https://*.brant.example",
		"plain http, public":   "brant-contract-demo=http://app.brant.example",
		"http, not loopback":   "brant-contract-demo=http://192.168.1.2:8080",
		"upper case":           "brant-contract-demo=https://APP.brant.example",
		"another integration":  "unknown-integration=https://app.brant.example",
		"the default port":     "brant-contract-demo=https://app.brant.example:443",
		"a malformed integers": "=https://app.brant.example",
	} {
		if _, err := embedConfig([]string{value}, integrations); err == nil || !strings.Contains(err.Error(), "-embed-origin") {
			t.Errorf("%s: %q accepted (%v)", name, value, err)
		}
	}
}
