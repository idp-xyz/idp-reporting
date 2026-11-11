package contractsim_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// The simulators list the companies of the principal's grant with the fixtures' names (ADR 0015 decision 5), under the catalog's
// authority; company_other has a name but stays outside the default grant, so a read naming it is refused as before.
func TestTheSimulatorListsTheGrantsCompaniesWithNames(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		sim   *contractsim.Simulator
		query r.QuerySpec
	}{{contractsim.Financial(), contractsim.FinancialQuery()}, {contractsim.Shipment(), contractsim.ShipmentQuery()}} {
		sim, q := c.sim, c.query
		owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), q.Integration)
		grant := contractsim.DefaultGrant()
		grant.CurrencyPolicy = q.CurrencyPolicy
		sim.Grant(owner, grant)
		sc := r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
		list, err := sim.ListCompanies(ctx, sc)
		if err != nil || !reflect.DeepEqual(list, r.CompanyList{Companies: []r.Company{{ID: "company_demo", Name: "演示公司"}}}) {
			t.Fatalf("%+v %v", list, err)
		}
		if err = r.ValidateCompanyList(list); err != nil {
			t.Fatal(err)
		}
		// The list is advisory and the grant decides: the company the fixtures hold but the grant does not is refused by a read.
		q.Companies = []string{"company_other"}
		if _, err = sim.PrepareRead(ctx, sc, q, r.QueryHash(q)); !errors.Is(err, r.ErrDenied) {
			t.Fatal("company_other was not refused:", err)
		}
		// A wider grant lists more, each once, in the grant's order; a company the fixtures do not name is listed under its id.
		wider := grant
		wider.Companies = []string{"company_other", "company_demo", "company_other", "acme-7"}
		sim.Grant(owner, wider)
		list, err = sim.ListCompanies(ctx, sc)
		want := []r.Company{{ID: "company_other", Name: "其他公司"}, {ID: "company_demo", Name: "演示公司"}, {ID: "acme-7", Name: "acme-7"}}
		if err != nil || list.Partial || !reflect.DeepEqual(list.Companies, want) {
			t.Fatalf("%+v %v", list, err)
		}
		// The catalog's refusals: an unknown principal, another source tenant, an outage, a revoked grant, a context that ended.
		stranger := sc
		stranger.Owner.Subject = "user_z"
		if _, err = sim.ListCompanies(ctx, stranger); !errors.Is(err, r.ErrDenied) {
			t.Fatal("an unknown principal:", err)
		}
		other := sc
		other.SourceTenant = "tenant_b"
		if _, err = sim.ListCompanies(ctx, other); !errors.Is(err, r.ErrDenied) {
			t.Fatal("another source tenant:", err)
		}
		sim.SetUnavailable(true)
		if _, err = sim.ListCompanies(ctx, sc); !errors.Is(err, r.ErrUnavailable) {
			t.Fatal("an outage:", err)
		}
		sim.SetUnavailable(false)
		sim.Revoke(owner)
		if _, err = sim.ListCompanies(ctx, sc); !errors.Is(err, r.ErrDenied) {
			t.Fatal("a revoked grant:", err)
		}
		ended, cancel := context.WithCancel(ctx)
		cancel()
		if _, err = sim.ListCompanies(ended, sc); !errors.Is(err, context.Canceled) {
			t.Fatal("a context that ended:", err)
		}
	}
	if contractsim.CompanyName("company_demo") != "演示公司" || contractsim.CompanyName("company_other") != "其他公司" || contractsim.CompanyName("x") != "x" {
		t.Fatal("the fixtures' names")
	}
}
