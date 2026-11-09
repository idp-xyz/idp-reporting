package httpapi_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// ADR 0016: GET /session lists the principal's products that the service serves, in the principal's order; a product the principal is
// bound to without a connection is never offered (the server also refuses such a binding at start and reload).
func TestSessionListsOnlyServedProducts(t *testing.T) {
	p := contractsim.DemoPrincipal()
	p.Integrations = []string{"unserved-before", contractsim.FinanceIntegration, "unserved-after"}
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	h, e := httpapi.Handler(s, auth)
	if e != nil {
		t.Fatal(e)
	}
	w := request(h, "GET", "/api/reporting/v1/session", nil, true)
	var body struct {
		Integrations []string `json:"integrations"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || !slices.Equal(body.Integrations, []string{contractsim.FinanceIntegration}) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Another tenant's principal with the same product name has no connection: an empty list, not an error.
	q := p
	q.Tenant = "another-tenant"
	other, _ := httpapi.DemoBearer(secret, q)
	h2, _ := httpapi.Handler(s, other)
	w = request(h2, "GET", "/api/reporting/v1/session", nil, true)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Integrations == nil || len(body.Integrations) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
