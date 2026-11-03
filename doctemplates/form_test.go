package doctemplates_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// A form (S06-B) is a 2.x template without a detail band: its page header, document header, document footer and page footer, no table.

func formOf(group []string) doctemplates.Document {
	d := doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: "source", Dataset: r.Ref{ID: "d", Version: "1"}}, Bands: &doctemplates.Bands{
		PageHeader: band(30, textOf("e1", "field", "order", false, 0), textOf("e2", "field", "customer", false, 9), textOf("e3", "literal", "表单", false, 18)),
		PageFooter: band(10, textOf("e4", "page", "{page}/{pages}", false, 0)),
	}}
	if group != nil {
		d.Group = &doctemplates.GroupSpec{Fields: group}
	}
	return d
}

func TestAFormIsAValid2xTemplateWithoutADetailBandAndIsRefusedIn1x(t *testing.T) {
	d := formOf([]string{"order"})
	if err := doctemplates.Validate(d); err != nil {
		t.Fatal(err)
	}
	if !d.Form() || d.TableItem() != nil || len(d.Items()) != 4 {
		t.Fatal("a form has no table")
	}
	b, err := doctemplates.Canonical(d)
	if err != nil || strings.Contains(string(b), `"detail"`) {
		t.Fatalf("the canonical form of a form has no detail key: %s %v", b, err)
	}
	var back doctemplates.Document
	if err := json.Unmarshal(b, &back); err != nil || !back.Form() || doctemplates.Validate(back) != nil {
		t.Fatalf("read back: %v", err)
	}
	// "detail": null is no detail band and no absent one.
	null := strings.Replace(string(b), `"bands":{`, `"bands":{"detail":null,`, 1)
	if err := json.Unmarshal([]byte(null), &back); err == nil {
		t.Fatal("a null detail band was read")
	}
	// A 1.x document has no bands, with or without a detail.
	one := document()
	one.Bands = d.Bands
	if err := doctemplates.Validate(one); !errors.Is(err, r.ErrInvalid) {
		t.Fatalf("a 1.x document with the bands of a form: %v", err)
	}
	// Nothing to draw is no form.
	empty := formOf(nil)
	empty.Bands = &doctemplates.Bands{}
	if err := doctemplates.Validate(empty); !errors.Is(err, r.ErrInvalid) {
		t.Fatalf("a form without a band: %v", err)
	}
	// A form carries no table element in a band, and the 128 limit counts its elements alone.
	withTable := formOf(nil)
	withTable.Bands.PageHeader.Elements = append(withTable.Bands.PageHeader.Elements, doctemplates.Item{ID: "e9", Type: "table", X: 0, Width: 20, FontSize: 8, Columns: []doctemplates.ItemColumn{{Field: "note", Width: 20}}})
	if err := doctemplates.Validate(withTable); !errors.Is(err, r.ErrInvalid) {
		t.Fatalf("a table in a band: %v", err)
	}
}

func TestAFormsGroupIsItsGroupFieldsRowsOrOneRowWithoutGroupFields(t *testing.T) {
	snap := orders(line("A", "x", "n1", "1", "1.00"), line("A", "x", "n2", "2", "2.00"), line("B", "y", "n3", "3", "3.00"))
	// With group fields the rows of a key are one document, as they are with a table.
	d := formOf([]string{"order"})
	groups, err := doctemplates.Groups(d, snap)
	if err != nil || len(groups) != 2 || len(groups[0].Rows) != 2 || len(groups[1].Rows) != 1 {
		t.Fatalf("%v %v", groups, err)
	}
	values, table, err := doctemplates.BindGroup(d, snap, groups[1])
	if err != nil || table != nil || values["e1"] != "B" || values["e2"] != "y" {
		t.Fatalf("a form binds values and no table: %v %v %v", values, table, err)
	}
	// A field that is not a group field must agree within the group, as ever.
	if _, _, err := doctemplates.BindGroup(formOf([]string{"order"}), orders(line("A", "x", "n", "1", "1"), line("A", "z", "n", "1", "1")), doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0, 1}}); !errors.Is(err, r.ErrUnsupported) {
		t.Fatalf("inconsistent group: %v", err)
	}
	// Without group fields it is one document per row (as 1.x row fields): each row is its own group, a group of two rows is no group.
	d = formOf(nil)
	groups, err = doctemplates.Groups(d, snap)
	if err != nil || len(groups) != 3 || groups[2].Rows[0] != 2 || len(groups[2].Key) != 0 {
		t.Fatalf("%v %v", groups, err)
	}
	if g, err := doctemplates.GroupOf(d, snap, 1); err != nil || len(g.Rows) != 1 || g.Rows[0] != 1 {
		t.Fatalf("the group of row 1: %v %v", g, err)
	}
	if values, table, err = doctemplates.BindGroup(d, snap, groups[1]); err != nil || table != nil || values["e1"] != "A" {
		t.Fatalf("%v %v %v", values, table, err)
	}
	if _, _, err = doctemplates.BindGroup(d, snap, doctemplates.RowGroup{Rows: []int{0, 1}}); !errors.Is(err, r.ErrInvalid) {
		t.Fatalf("two rows are no document of a form without group fields: %v", err)
	}
	if _, _, err = doctemplates.BindGroup(d, snap, doctemplates.RowGroup{Rows: []int{7}}); !errors.Is(err, r.ErrInvalid) {
		t.Fatalf("a row the snapshot lacks: %v", err)
	}
	// A template with a table and no group fields is still one document of all the rows.
	whole := invoice(nil)
	if groups, err = doctemplates.Groups(whole, snap); err != nil || len(groups) != 1 || len(groups[0].Rows) != 3 {
		t.Fatalf("%v %v", groups, err)
	}
	// No rows, no documents of a form.
	if groups, err = doctemplates.Groups(formOf(nil), orders()); err != nil || len(groups) != 0 {
		t.Fatalf("%v %v", groups, err)
	}
}
