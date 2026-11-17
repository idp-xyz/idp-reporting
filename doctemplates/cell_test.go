package doctemplates_test

import (
	"errors"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// The document templates and a cell without a value (S26-A, ADR 0028 decision 1). No snapshot holds one until a column can declare
// that it may be absent; a template is bound to a snapshot's cells and fails closed (reporting.ErrIntegrity) wherever it would print
// one, encode it, sum it, put it in a table or make a group's key of it. S26-B gives each its rule: a field prints nothing, a code
// is refused as an empty one is, a sum skips it, and the rows without a value in a group field are one group.
func TestATemplateIsNotBoundToACellWithoutAValue(t *testing.T) {
	// 1.x: an element bound to a row's value and to a total, and a table.
	one := func() r.Snapshot {
		return r.Snapshot{Owner: r.Owner{Integration: "source"}, Query: r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}, Dimensions: []string{"customer"}, Metrics: []r.Ref{{ID: "amount"}}},
			Result: r.Result{Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2}}, Rows: r.CellRows([][]string{{"a", "1.00"}, {"b", "2.50"}}), Totals: r.CellTotals(map[string]string{"amount": "3.50"})}}
	}
	d := document()
	if v, e := doctemplates.Bind(d, one(), 1); e != nil || v["n1"] != "2.50" {
		t.Fatal(v, e)
	}
	s := one()
	s.Result.Rows[1][1] = r.NoValue()
	if _, e := doctemplates.Bind(d, s, 1); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a row's value without a value: %v", e)
	}
	if _, e := doctemplates.Bind(d, s, 0); e != nil {
		t.Errorf("another row of the same snapshot: %v", e)
	}
	total := document()
	total.Elements[0].Source = "total"
	if v, e := doctemplates.Bind(total, one(), 0); e != nil || v["n1"] != "3.50" {
		t.Fatal(v, e)
	}
	s = one()
	s.Result.Totals["amount"] = r.NoValue()
	if _, e := doctemplates.Bind(total, s, 0); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a total without a value: %v", e)
	}
	for j := range 2 {
		s := one()
		s.Result.Rows[0][j] = r.NoValue()
		if _, e := doctemplates.BindTable(tableDocument(), s); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("a table's cell without a value, column %d: %v", j, e)
		}
	}
	s = one()
	s.Result.Totals["amount"] = r.NoValue()
	if _, e := doctemplates.BindTable(tableDocument(), s); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a table's total without a value: %v", e)
	}
	// 2.x: the group's key, a field of the group, a summed value, a table's cell.
	rows := func() r.Snapshot {
		return orders(line("A", "甲", "n1", "1", "1.00"), line("A", "甲", "n2", "2", "2.00"), line("B", "乙", "n3", "3", "3.00"))
	}
	byOrder := invoice([]string{"order"})
	groups, e := doctemplates.Groups(byOrder, rows())
	if e != nil || len(groups) != 2 {
		t.Fatal(groups, e)
	}
	if _, _, e := doctemplates.BindGroup(byOrder, rows(), groups[0]); e != nil {
		t.Fatal(e)
	}
	s = rows()
	s.Result.Rows[2][0] = r.NoValue()
	if _, e := doctemplates.Groups(byOrder, s); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a group field without a value: %v", e)
	}
	if _, e := doctemplates.GroupOf(byOrder, s, 0); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("the group of a row, in a snapshot with a group field without a value: %v", e)
	}
	if _, e := doctemplates.GroupByKey(byOrder, s, r.Cells("A")); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a group by its key, in a snapshot with a group field without a value: %v", e)
	}
	// A key that holds a cell without a value is no group of a snapshot whose cells all have one.
	if _, e := doctemplates.GroupByKey(byOrder, rows(), []r.Cell{r.NoValue()}); !errors.Is(e, r.ErrNotFound) {
		t.Errorf("a key without a value: %v", e)
	}
	if _, _, e := doctemplates.BindGroup(byOrder, rows(), doctemplates.RowGroup{Key: []r.Cell{r.NoValue()}, Rows: []int{0, 1}}); !errors.Is(e, r.ErrInvalid) {
		t.Errorf("a group whose key is without a value: %v", e)
	}
	// A group that is the rows without a value, handed in with its key as the snapshot holds it: it is whole (the cells are equal), and
	// its document is still not bound, because the key is printed.
	if _, _, e := doctemplates.BindGroup(byOrder, s, doctemplates.RowGroup{Key: []r.Cell{r.NoValue()}, Rows: []int{2}}); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("the group of the rows without a value: %v", e)
	}
	for name, at := range map[string][2]int{"a field of the group (the customer)": {1, 1}, "a table's cell (the note)": {0, 2}, "a summed value (the amount)": {1, 4}, "a summed value (the quantity)": {0, 3}} {
		s := rows()
		s.Result.Rows[at[0]][at[1]] = r.NoValue()
		if _, _, e := doctemplates.BindGroup(byOrder, s, groups[0]); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("%s without a value: %v", name, e)
		}
		// The other order's document does not read the cell.
		if _, _, e := doctemplates.BindGroup(byOrder, s, groups[1]); e != nil {
			t.Errorf("%s without a value, the other group: %v", name, e)
		}
	}
}
