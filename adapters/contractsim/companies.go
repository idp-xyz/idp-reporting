package contractsim

import (
	"context"

	r "idp.local/reporting/reporting"
)

// The company directory of ADR 0015 (S19-B): a simulator lists the companies of the principal's current grant, each with the name a
// person reads. The names are the fixtures': both simulators use the same two company ids. company_other has a name but is not in
// DefaultGrant, so PrepareRead keeps refusing it and the console's refusal path (the manual's figure 10) stays testable through the
// typed line. A company the fixtures do not name is listed under its id.
var companyNames = map[string]string{"company_demo": "演示公司", "company_other": "其他公司"}

// CompanyName is the name the simulators list a company under: the fixtures' name, or the id itself for a company they do not name.
func CompanyName(id string) string {
	if name, ok := companyNames[id]; ok {
		return name
	}
	return id
}

// Companies is the directory of a grant: its companies in its order, each once, each with its name; never partial (a grant holds at
// most 100 companies, docs/REAL_SOURCE_ONBOARDING.md). The durable simulator (pgsource) lists its stored grant through it too.
func Companies(g Grant) r.CompanyList {
	out := r.CompanyList{Companies: make([]r.Company, 0, len(g.Companies))}
	seen := map[string]bool{}
	for _, id := range g.Companies {
		if seen[id] {
			continue
		}
		seen[id] = true
		out.Companies = append(out.Companies, r.Company{ID: id, Name: CompanyName(id)})
	}
	return out
}

// ListCompanies lists the companies of the principal's current grant (ADR 0015), under the same authority as the catalog: an unknown
// principal or another source tenant is denied, an outage is unavailable. The list is the grant's at this instant; a grant changed
// afterwards refuses a read as before, whatever was listed.
func (s *Simulator) ListCompanies(ctx context.Context, sc r.SourceContext) (r.CompanyList, error) {
	if e := ctx.Err(); e != nil {
		return r.CompanyList{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, e := s.catalog(sc)
	if e != nil {
		return r.CompanyList{}, e
	}
	return Companies(g), nil
}

var _ r.CompanyDirectory = (*Simulator)(nil)
