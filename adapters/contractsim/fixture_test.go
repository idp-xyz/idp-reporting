package contractsim_test

import (
	"context"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"testing"
)

func TestPersistableFixtureProjectsOriginalScope(t *testing.T) {
	seed := contractsim.Financial().Fixture()
	q := contractsim.FinancialQuery()
	g := contractsim.DefaultGrant()
	p := r.PreparedRead{Ref: "frozen_ref", QueryHash: r.QueryHash(q), Watermark: "original"}
	page, e := contractsim.ReadFrozenPage(context.Background(), seed, q, g, p, "", 1)
	if e != nil || len(page.Rows) != 1 || page.Rows[0][2] != "100.25" || page.Totals["ar.open_balance"] != "9007199254740993.01" || page.NextCursor == "" {
		t.Fatal(page, e)
	}
	if len(seed.Records) != 5 {
		t.Fatal("seed excludes physical unauthorized fixture rows")
	}
}
