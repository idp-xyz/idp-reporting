package httpapi

import (
	"errors"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// The rows and the groups a document check picks are picked by the width of what a cell prints (S26-A, ADR 0028 decision 1). A
// snapshot holds no cell without a value until a column can declare one; the pickers read the snapshot's cells and fail closed on one
// (reporting.ErrIntegrity), in a field's column, in a summed column and in a table's column, so that a cell that is absent is not
// measured as the narrowest text.
func TestTheBoundaryPicksFailClosedOnACellWithoutAValue(t *testing.T) {
	// 1.x: a row field's box.
	snap := snapshotOf([]string{"a", "b"}, []string{"mid", "x"}, []string{"abcde", "yy"}, []string{"end", "z"})
	if _, e := boundaryRows(boxes("a", "b", ""), snap); e != nil {
		t.Fatal(e)
	}
	holed := snapshotOf([]string{"a", "b"}, []string{"mid", "x"}, []string{"abcde", "yy"}, []string{"end", "z"})
	holed.Result.Rows[1][1] = r.NoValue()
	if _, e := boundaryRows(boxes("a", "b", ""), holed); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a row field's cell without a value: %v", e)
	}
	// A column no box is bound to is not measured.
	if _, e := boundaryRows(boxes("a", ""), holed); e != nil {
		t.Errorf("a cell without a value in a column the template does not print: %v", e)
	}
	// 2.x: a field text (read from the group's first row), a table's column.
	d, s := bandedWide(6, 60)
	groups, e := doctemplates.Groups(d, s)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := boundaryGroups(d, s, groups); e != nil {
		t.Fatal(e)
	}
	// A field text is read from its group's first row, in a column the table does not print (c20); a table's cell from every row, here
	// from one that is not its group's first, which no field text reads.
	if len(groups[2].Rows) < 2 {
		t.Fatal("the third group has one row")
	}
	for name, at := range map[string][2]int{"a field text's column": {groups[2].Rows[0], 20}, "a table's column": {groups[2].Rows[1], 5}} {
		_, s := bandedWide(6, 60)
		s.Result.Rows[at[0]][at[1]] = r.NoValue()
		if _, e := boundaryGroups(d, s, groups); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("%s without a value: %v", name, e)
		}
	}
	// A group sum: the values that are added.
	sum, summed := bandedWide(6, 60)
	summed.Result.Columns[7].Type, summed.Result.Columns[7].Scale = "decimal", 2
	for i := range summed.Result.Rows {
		summed.Result.Rows[i][7] = r.Text("1.25")
	}
	sum.Bands.DocumentFooter.Elements[0] = doctemplates.Item{ID: "e25", Type: "text", Source: "group_sum", Field: "c7", X: 0, Y: 0, Width: 100, Height: 5, FontSize: 8, Align: "right"}
	if _, e := boundaryGroups(sum, summed, groups); e != nil {
		t.Fatal(e)
	}
	summed.Result.Rows[groups[1].Rows[len(groups[1].Rows)-1]][7] = r.NoValue()
	if _, e := boundaryGroups(sum, summed, groups); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a summed cell without a value: %v", e)
	}
}
