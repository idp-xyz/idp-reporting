package integration_test

import (
	"context"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"testing"
)

func records(n int) ([]contractsim.Record, contractsim.Grant) {
	g := contractsim.DefaultGrant()
	g.Rows = []string{}
	data := []contractsim.Record{}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("row_%04d", i)
		g.Rows = append(g.Rows, key)
		data = append(data, contractsim.Record{Company: "company_demo", Key: key, Values: []string{key, "current", "1.00"}})
	}
	return data, g
}
func TestSourceMultiplePagesUseSameFrozenVersion(t *testing.T) {
	data, g := records(60)
	a := contractsim.Financial()
	a.ReplaceRecords(data)
	p := contractsim.DemoPrincipal()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), g)
	wrapped := intercept{Adapter: a, afterRead: func() {
		a.ReplaceRecords([]contractsim.Record{{Company: "company_demo", Key: "row_0000", Values: []string{"changed", "current", "999.00"}}})
	}}
	s, p, _ := intercepted(t, wrapped)
	q := contractsim.FinancialQuery()
	q.Page.Limit = 100
	result, e := s.Query(context.Background(), p, q)
	if e != nil || len(result.Rows) != 60 || result.Rows[59][0] != "row_0059" || result.Totals["ar.open_balance"] != "60.00" || a.ReadCalls() != 2 {
		t.Fatal(len(result.Rows), result.Totals, a.ReadCalls(), e)
	}
}
func TestSourceBudgetRejectsWholeResult(t *testing.T) {
	data, g := records(1001)
	a := contractsim.Financial()
	a.ReplaceRecords(data)
	p := contractsim.DemoPrincipal()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), g)
	s, p, _ := intercepted(t, a)
	_, e := s.Query(context.Background(), p, contractsim.FinancialQuery())
	mustError(t, e, r.ErrBudget)
}
func TestInvalidTypedSourceCellRejected(t *testing.T) {
	a := contractsim.Financial()
	p := contractsim.DemoPrincipal()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	wrapped := intercept{Adapter: a, mutate: func(v r.SourcePage) r.SourcePage { v.Rows[0][2] = "1e99"; return v }}
	s, p, _ := intercepted(t, wrapped)
	_, e := s.Query(context.Background(), p, contractsim.FinancialQuery())
	mustError(t, e, r.ErrIntegrity)
}
