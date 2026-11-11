package reporting_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// plain is an adapter without the company directory: embedding the interface promotes the four contract methods and nothing else.
type plain struct{ r.Adapter }

// directoryOf is an adapter whose directory answers what the test says.
type directoryOf struct {
	r.Adapter
	list r.CompanyList
	err  error
}

func (d directoryOf) ListCompanies(context.Context, r.SourceContext) (r.CompanyList, error) {
	return d.list, d.err
}

func companiesService(t *testing.T, a r.Adapter) (*r.Service, r.Principal) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	s, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func manyCompanies(n int) []r.Company {
	out := make([]r.Company, n)
	for i := range out {
		out[i] = r.Company{ID: fmt.Sprintf("c%04d", i), Name: fmt.Sprintf("公司 %d", i)}
	}
	return out
}

// The bounds of ADR 0015, fail closed: at most 1000 entries, each id and name 1..128 bytes of text without a control character, no id
// twice; anything else is an integrity failure.
func TestACompanyListIsVerifiedFailClosed(t *testing.T) {
	long := strings.Repeat("a", 128)
	for name, list := range map[string]r.CompanyList{
		"the empty list":           {Companies: []r.Company{}},
		"a nil list":               {},
		"one company":              {Companies: []r.Company{{ID: "company_demo", Name: "演示公司"}}},
		"1000 companies, partial":  {Companies: manyCompanies(1000), Partial: true},
		"ids and names of 128":     {Companies: []r.Company{{ID: long, Name: long}}},
		"a name with inner spaces": {Companies: []r.Company{{ID: "a", Name: "Acme Trading Co."}}},
		"two ids, one name":        {Companies: []r.Company{{ID: "a", Name: "同名"}, {ID: "b", Name: "同名"}}},
	} {
		if err := r.ValidateCompanyList(list); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, list := range map[string]r.CompanyList{
		"1001 companies":          {Companies: manyCompanies(1001), Partial: true},
		"an id twice":             {Companies: []r.Company{{ID: "a", Name: "A"}, {ID: "a", Name: "B"}}},
		"an empty id":             {Companies: []r.Company{{ID: "", Name: "A"}}},
		"an empty name":           {Companies: []r.Company{{ID: "a", Name: ""}}},
		"a blank name":            {Companies: []r.Company{{ID: "a", Name: " \t "}}},
		"an id of 129 bytes":      {Companies: []r.Company{{ID: long + "a", Name: "A"}}},
		"a name of 129 bytes":     {Companies: []r.Company{{ID: "a", Name: long + "a"}}},
		"a name of 43 CJK runes":  {Companies: []r.Company{{ID: "a", Name: strings.Repeat("公", 43)}}},
		"a NUL in a name":         {Companies: []r.Company{{ID: "a", Name: "A\x00B"}}},
		"a newline in a name":     {Companies: []r.Company{{ID: "a", Name: "A\nB"}}},
		"a tab in an id":          {Companies: []r.Company{{ID: "a\tb", Name: "A"}}},
		"DEL in a name":           {Companies: []r.Company{{ID: "a", Name: "A\x7fB"}}},
		"an escape in an id":      {Companies: []r.Company{{ID: "a\x1b[31m", Name: "A"}}},
		"U+FFFE in a name":        {Companies: []r.Company{{ID: "a", Name: "A\uFFFE"}}},
		"invalid UTF-8 in a name": {Companies: []r.Company{{ID: "a", Name: "A\xffB"}}},
		"a bad entry among good":  {Companies: append(manyCompanies(10), r.Company{ID: "c0003", Name: "again"})},
	} {
		if err := r.ValidateCompanyList(list); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%s: %v, want INTEGRITY_FAILURE", name, err)
		}
	}
}

// The service answers the directory of the product as ADR 0015 decision 3 says: supported with the list for an adapter that has the
// capability, supported false with an empty list for one that has not, the catalog's refusals for a product the principal does not hold, the
// source's failure as it is, and an integrity failure for a list that does not verify. Nothing of a refused list is passed on.
func TestListCompaniesAnswersTheDirectoryOfTheProduct(t *testing.T) {
	ctx := context.Background()
	sim := contractsim.Financial()
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	s, p := companiesService(t, sim)
	got, err := s.ListCompanies(ctx, p, contractsim.FinanceIntegration)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(got); string(b) != `{"supported":true,"partial":false,"companies":[{"id":"company_demo","name":"演示公司"}]}` {
		t.Fatalf("%s", b)
	}
	// A product the principal does not hold, and one the service has no connection for: the catalog's refusals.
	if _, err = s.ListCompanies(ctx, p, contractsim.ShipmentIntegration); !errors.Is(err, r.ErrDenied) {
		t.Fatal("a product the principal does not hold:", err)
	}
	stranger := p
	stranger.Integrations = []string{contractsim.FinanceIntegration, "another-product"}
	if _, err = s.ListCompanies(ctx, stranger, "another-product"); !errors.Is(err, r.ErrDenied) {
		t.Fatal("a product without a connection:", err)
	}
	if _, err = s.ListCompanies(ctx, r.Principal{Integrations: []string{contractsim.FinanceIntegration}}, contractsim.FinanceIntegration); !errors.Is(err, r.ErrDenied) {
		t.Fatal("an invalid identity:", err)
	}
	// The source's own refusals travel: a principal the source does not know, an outage.
	unknown := p
	unknown.Subject = "user_z"
	if _, err = s.ListCompanies(ctx, unknown, contractsim.FinanceIntegration); !errors.Is(err, r.ErrDenied) {
		t.Fatal("a subject the source does not know:", err)
	}
	sim.SetUnavailable(true)
	if _, err = s.ListCompanies(ctx, p, contractsim.FinanceIntegration); !errors.Is(err, r.ErrUnavailable) {
		t.Fatal("an unavailable source:", err)
	}
	sim.SetUnavailable(false)

	// An adapter without the capability: supported false, an empty list, not an error; the list is `[]`, never null.
	s, p = companiesService(t, plain{sim})
	got, err = s.ListCompanies(ctx, p, contractsim.FinanceIntegration)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(got); string(b) != `{"supported":false,"partial":false,"companies":[]}` {
		t.Fatalf("%s", b)
	}

	// A directory whose answer does not verify is an integrity failure, whatever else it says; a partial list is passed on as partial.
	for name, list := range map[string]r.CompanyList{
		"1001 companies":      {Companies: manyCompanies(1001), Partial: true},
		"an id twice":         {Companies: []r.Company{{ID: "a", Name: "A"}, {ID: "a", Name: "B"}}},
		"an empty name":       {Companies: []r.Company{{ID: "a", Name: ""}}},
		"a control character": {Companies: []r.Company{{ID: "a", Name: "A\x00"}}},
	} {
		s, p = companiesService(t, directoryOf{Adapter: sim, list: list})
		if _, err = s.ListCompanies(ctx, p, contractsim.FinanceIntegration); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%s: %v, want INTEGRITY_FAILURE", name, err)
		}
	}
	s, p = companiesService(t, directoryOf{Adapter: sim, list: r.CompanyList{Companies: manyCompanies(1000), Partial: true}})
	got, err = s.ListCompanies(ctx, p, contractsim.FinanceIntegration)
	if err != nil || !got.Supported || !got.Partial || len(got.Companies) != 1000 || got.Companies[999].ID != "c0999" {
		t.Fatalf("%v %v", err, got.Supported)
	}
	s, p = companiesService(t, directoryOf{Adapter: sim, list: r.CompanyList{Companies: nil}})
	got, err = s.ListCompanies(ctx, p, contractsim.FinanceIntegration)
	if b, _ := json.Marshal(got); err != nil || string(b) != `{"supported":true,"partial":false,"companies":[]}` {
		t.Fatalf("a nil list: %v %s", err, b)
	}
	// The directory's own error is the answer.
	s, p = companiesService(t, directoryOf{Adapter: sim, err: r.ErrBudget})
	if _, err = s.ListCompanies(ctx, p, contractsim.FinanceIntegration); !errors.Is(err, r.ErrBudget) {
		t.Fatal(err)
	}
}
