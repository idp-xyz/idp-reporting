package pdf

import (
	"errors"
	"testing"

	"idp.local/reporting/export/present"
	r "idp.local/reporting/reporting"
)

// The PDF's table fails closed on a cell or a total without a value (S26-A, ADR 0028 decision 1), in the plain table and through a
// presentation: no snapshot holds one until a column can declare that it may be absent. S26-B prints an empty cell there.
func TestTheTableIsNotLaidOutFromACellWithoutAValue(t *testing.T) {
	rows := [][]string{{"甲", "乙", "100.25"}, {"丙", "丁", "-0.24"}}
	for j := range 3 {
		s := table(2, 1, rows...)
		s.Result.Rows[1][j] = r.NoValue()
		if _, err := plainTable(s); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("column %d without a value, the plain table: %v", j, err)
		}
		if _, err := presentedTable(s, present.Whole(s)); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("column %d without a value, presented: %v", j, err)
		}
		if _, err := build(s, fakeMetrics(), created); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("column %d without a value, the document: %v", j, err)
		}
	}
	s := table(2, 1, rows...)
	s.Result.Totals["m0"] = r.NoValue()
	if _, err := plainTable(s); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a total without a value, the plain table: %v", err)
	}
	if _, err := presentedTable(s, present.Whole(s)); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a total without a value, presented: %v", err)
	}
	if _, err := plainTable(table(2, 1, rows...)); err != nil {
		t.Fatal(err)
	}
}
