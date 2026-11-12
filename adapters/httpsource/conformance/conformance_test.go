package conformance_test

import (
	"context"
	"crypto/x509"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/conformance"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
)

// What the check declares for the connection it makes is the target's own word (Target.KeptResults), never the configuration's: a
// configuration that declares kept results does not make the check take a read that vouches, and one that declares nothing does not
// stop the check that was asked to. The command never sets the configuration's field; a caller of the package may.
func TestTheTargetDecidesWhetherTheCheckDeclaresKeptResults(t *testing.T) {
	const token = "CONFORMANCE-TOKEN-0123456789-never-shown"
	sim := contractsim.Financial()
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	sim.SetScopeLifetime(7 * 24 * time.Hour)
	srv := httptest.NewTLSServer(sourcetest.New(sim, sourcetest.Options{Token: token}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	target := func(configured, asked bool) conformance.Target {
		return conformance.Target{
			Config:      httpsource.Config{BaseURL: srv.URL, RootCAs: pool, Token: token, KeptResults: configured},
			Context:     r.SourceContext{Owner: owner, SourceTenant: "tenant_a"},
			Query:       contractsim.FinancialQuery(),
			KeptResults: asked,
		}
	}
	line := func(results []conformance.Result, id string) string {
		for _, x := range results {
			if x.ID == id {
				return x.Line()
			}
		}
		return ""
	}
	// The configuration declares, the target does not: the read that vouches does not verify, and the check ends there.
	results := conformance.Run(context.Background(), target(true, false))
	if got := line(results, "R1"); !strings.HasPrefix(got, "FAIL R1 ") || !strings.Contains(got, "the connection does not declare kept_results") || line(results, "K1") != "" {
		t.Fatalf("a configuration that declares, for a target that does not: %q, then %q", got, line(results, "K1"))
	}
	// The target declares, the configuration does not: the read is taken and the two rules are asked.
	results = conformance.Run(context.Background(), target(false, true))
	if !strings.HasPrefix(line(results, "R1"), "PASS R1 ") || !strings.HasPrefix(line(results, "K1"), "PASS K1 ") || !strings.HasPrefix(line(results, "K2"), "PASS K2 ") {
		t.Fatalf("a target that declares, over a configuration that does not: %q %q %q", line(results, "R1"), line(results, "K1"), line(results, "K2"))
	}
}
