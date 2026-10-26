package reporting_test

import (
	"context"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

func TestFinancialSnapshot(t *testing.T) {
	ctx := context.Background()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "queued" || a.ReadCalls() != 0 {
		t.Fatal("admission executed data query", job)
	}
	if ran, err := s.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	job, err = s.Job(ctx, p, job.ID)
	if err != nil || job.State != "succeeded" {
		t.Fatal(job, err)
	}
	page, err := s.Rows(ctx, p, job.SnapshotID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0][2] != "100.25" || page.Totals["ar.open_balance"] != "9007199254740993.01" {
		t.Fatalf("unexpected financial result: %+v", page)
	}
}
