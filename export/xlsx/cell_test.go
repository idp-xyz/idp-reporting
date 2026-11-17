package xlsx_test

import (
	"context"
	"errors"
	"testing"

	"idp.local/reporting/export/present"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
)

// The workbook writer fails closed on a cell or a total without a value (S26-A, ADR 0028 decision 1): no snapshot holds one until a
// column can declare that it may be absent, and a workbook is not written from a store that does not agree with itself. S26-B writes
// an empty cell there.
func TestTheWorkbookIsNotWrittenFromACellWithoutAValue(t *testing.T) {
	ctx := context.Background()
	for j, c := range ledger().Result.Columns {
		s := ledger()
		s.Result.Rows[2][j] = r.NoValue()
		if _, e := (xlsx.Writer{}).Write(ctx, s); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("a %s cell without a value: %v", c.Type, e)
		}
		if _, e := (xlsx.Writer{}).WritePresented(ctx, s, present.Whole(s)); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("a %s cell without a value, presented: %v", c.Type, e)
		}
	}
	for _, metric := range []string{"amount", "count"} {
		s := ledger()
		s.Result.Totals[metric] = r.NoValue()
		if _, e := (xlsx.Writer{}).Write(ctx, s); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("the total of %s without a value: %v", metric, e)
		}
	}
	if _, e := (xlsx.Writer{}).Write(ctx, ledger()); e != nil {
		t.Fatal(e)
	}
}
