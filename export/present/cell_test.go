package present_test

import (
	"errors"
	"testing"

	"idp.local/reporting/export/present"
	r "idp.local/reporting/reporting"
)

// A cell without a value (S26-A, ADR 0028 decision 1). No snapshot holds one: no column can declare that it may be absent, and the
// service and the stores refuse a row or a total that has one. The presentation reads a snapshot's rows as cells and fails closed
// (reporting.ErrIntegrity) wherever it would print, mark, sum or group by a cell that has none, and on a total that has none, so that
// a store which does not agree with itself is not printed as if the value were the empty text or a zero. S26-B replaces each of these
// refusals with what the exports do with an absent value.
func TestBuildFailsClosedOnACellWithoutAValue(t *testing.T) {
	grouped := r.Presentation{Columns: []r.PresentedColumn{{ID: "customer", Align: r.AlignLeft}, {ID: "amount", Align: r.AlignRight}}, SubtotalBy: "customer"}
	marked := r.Presentation{Columns: []r.PresentedColumn{{ID: "customer", Align: r.AlignLeft}, {ID: "amount", Align: r.AlignRight}}, Marks: []r.Mark{{Column: "customer", When: r.WhenBlank, Mark: r.MarkNote}}}
	only := r.Presentation{Columns: []r.PresentedColumn{{ID: "year", Align: r.AlignRight}}}
	for j, c := range ledger().Result.Columns {
		s := ledger()
		s.Result.Rows[1][j] = r.NoValue()
		if _, err := present.Build(s, present.Whole(s)); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("a %s cell without a value, the whole snapshot: %v", c.Type, err)
		}
	}
	for name, p := range map[string]r.Presentation{"the subtotal column": grouped, "a marked column": marked} {
		s := ledger()
		s.Result.Rows[3][0] = r.NoValue()
		if _, err := present.Build(s, p); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("a cell without a value in %s: %v", name, err)
		}
	}
	s := ledger()
	s.Result.Rows[2][2] = r.NoValue()
	if _, err := present.Build(s, grouped); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a cell without a value in a summed column: %v", err)
	}
	for _, metric := range []string{"amount", "count"} {
		s := ledger()
		s.Result.Totals[metric] = r.NoValue()
		if _, err := present.Build(s, present.Whole(s)); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("the total of %s without a value: %v", metric, err)
		}
	}
	// A metric that is not a number has its total printed as it came: without a value it is refused too, not printed as nothing.
	texts := ledger()
	texts.Query.Metrics = []r.Ref{{ID: "amount", Version: "1"}, {ID: "count", Version: "1"}, {ID: "grade", Version: "1"}}
	texts.Result.Columns = append(texts.Result.Columns, r.Column{ID: "grade", Label: "评级", Type: "string"})
	for i := range texts.Result.Rows {
		texts.Result.Rows[i] = append(texts.Result.Rows[i], r.Text("A"))
	}
	texts.Result.Totals["grade"] = r.Text("A")
	if _, err := present.Build(texts, present.Whole(texts)); err != nil {
		t.Fatal(err)
	}
	texts.Result.Totals["grade"] = r.NoValue()
	if _, err := present.Build(texts, present.Whole(texts)); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("the total of a text metric without a value: %v", err)
	}
	// What is not shown is not read: a presentation of another column prints what it printed.
	s = ledger()
	s.Result.Rows[0][0], s.Result.Totals["amount"] = r.NoValue(), r.NoValue()
	if table, err := present.Build(s, only); err != nil || len(table.Rows) != 4 || table.Rows[0].Cells[0].Exact != "2026" {
		t.Errorf("a presentation that does not show the cell: %v", err)
	}
	// The empty text is a value, as it always was: the fourth row's customer.
	if table, err := present.Build(ledger(), grouped); err != nil || len(table.Rows) != 7 {
		t.Errorf("the empty text as a group's key: %v", err)
	}
}
