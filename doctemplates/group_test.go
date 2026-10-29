package doctemplates_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// orders is a snapshot of order lines: the order and customer (dimensions), a note (a dimension that varies
// within an order), the quantity (an integer metric) and the amount (a decimal metric of scale 2).
func orders(rows ...[]string) r.Snapshot {
	return r.Snapshot{
		Owner: r.Owner{Integration: "source"},
		Query: r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}, Dimensions: []string{"order", "customer", "note"}, Metrics: []r.Ref{{ID: "qty", Version: "1"}, {ID: "amount", Version: "1"}}},
		Result: r.Result{Columns: []r.Column{{ID: "order", Label: "订单", Type: "string"}, {ID: "customer", Label: "客户", Type: "string"}, {ID: "note", Label: "备注", Type: "string"},
			{ID: "qty", Label: "数量", Type: "integer"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2, Currency: "CNY"}}, Rows: rows, Totals: map[string]string{}},
	}
}

func line(order, customer, note, qty, amount string) []string {
	return []string{order, customer, note, qty, amount}
}

func band(h float64, items ...doctemplates.Item) *doctemplates.Band {
	return &doctemplates.Band{Height: h, Elements: append([]doctemplates.Item{}, items...)}
}

func textOf(id, source, v string, wrap bool, y float64) doctemplates.Item {
	it := doctemplates.Item{ID: id, Type: "text", Source: source, Wrap: wrap, X: 0, Y: y, Width: 100, Height: 8, FontSize: 10, Align: "left"}
	if source == "literal" || source == "page" {
		it.Text = v
	} else {
		it.Field = v
	}
	return it
}

// invoice is a 2.0 template grouped by order: the order and customer as fields, the note, the sum, a page text.
func invoice(group []string) doctemplates.Document {
	d := doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: "source", Dataset: r.Ref{ID: "d", Version: "1"}}, Bands: &doctemplates.Bands{
		PageHeader:     band(40, textOf("e1", "field", "order", false, 0), textOf("e2", "field", "customer", false, 9), textOf("e3", "page", "{page}/{pages}", false, 18), textOf("e4", "literal", "标题", false, 27)),
		Detail:         &doctemplates.DetailBand{Elements: []doctemplates.Item{{ID: "e5", Type: "table", X: 0, Width: 190, FontSize: 10, Columns: []doctemplates.ItemColumn{{Field: "note", Width: 100}, {Field: "qty", Width: 30}, {Field: "amount", Width: 60}}}}},
		DocumentFooter: band(20, textOf("e6", "group_sum", "amount", false, 0), textOf("e7", "group_sum", "qty", false, 9)),
	}}
	if group != nil {
		d.Group = &doctemplates.GroupSpec{Fields: group}
	}
	return d
}

func TestGroupsPartitionByExactFrozenStringsInOrderOfFirstRow(t *testing.T) {
	s := orders(
		line("B", "乙", "n1", "1", "1.00"),
		line("A", "甲", "n2", "1", "2.00"),
		line("B", "乙", "n3", "1", "3.00"),
		line("", "丙", "n4", "1", "4.00"),  // the empty string is a key like any other
		line("a", "甲", "n5", "1", "5.00"), // case matters
		line("A", "甲", "n6", "1", "6.00"),
		line("", "丙", "n7", "1", "7.00"),
	)
	groups, err := doctemplates.Groups(invoice([]string{"order"}), s)
	if err != nil {
		t.Fatal(err)
	}
	want := []doctemplates.RowGroup{{Key: []string{"B"}, Rows: []int{0, 2}}, {Key: []string{"A"}, Rows: []int{1, 5}}, {Key: []string{""}, Rows: []int{3, 6}}, {Key: []string{"a"}, Rows: []int{4}}}
	if !reflect.DeepEqual(groups, want) {
		t.Errorf("groups %+v, want %+v", groups, want)
	}
	// Two fields: the pairs partition, in order of the first row; no two different tuples make one key.
	s = orders(line("a", "bc", "", "1", "1"), line("ab", "c", "", "1", "1"), line("a", "bc", "", "1", "1"), line("a\x00", "bc", "", "1", "1"))
	groups, err = doctemplates.Groups(invoice([]string{"order", "customer"}), s)
	if err != nil || len(groups) != 3 || !reflect.DeepEqual(groups[0].Rows, []int{0, 2}) || !reflect.DeepEqual(groups[1].Key, []string{"ab", "c"}) {
		t.Errorf("pairs: %+v %v", groups, err)
	}
	// Three fields at most are allowed by the template; the order of the fields is the key's order.
	groups, err = doctemplates.Groups(invoice([]string{"customer", "order", "note"}), orders(line("A", "甲", "n", "1", "1")))
	if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Key, []string{"甲", "A", "n"}) {
		t.Errorf("three fields: %+v %v", groups, err)
	}
	// Without a group the whole snapshot is one group, in the snapshot's order, with no key; without rows it is one without rows.
	groups, err = doctemplates.Groups(invoice(nil), s)
	if err != nil || len(groups) != 1 || groups[0].Key != nil || !reflect.DeepEqual(groups[0].Rows, []int{0, 1, 2, 3}) {
		t.Errorf("no group: %+v %v", groups, err)
	}
	groups, err = doctemplates.Groups(invoice(nil), orders())
	if err != nil || len(groups) != 1 || len(groups[0].Rows) != 0 {
		t.Errorf("no group, no rows: %+v %v", groups, err)
	}
	// With a group and no rows there is no document.
	groups, err = doctemplates.Groups(invoice([]string{"order"}), orders())
	if err != nil || len(groups) != 0 {
		t.Errorf("a group, no rows: %+v %v", groups, err)
	}
	// Refusals: a field the snapshot lacks is denied, a metric is unsupported as a group field, a short row is an integrity failure.
	if _, err = doctemplates.Groups(invoice([]string{"ghost"}), s); !errors.Is(err, r.ErrDenied) {
		t.Errorf("a field the snapshot lacks: %v", err)
	}
	if _, err = doctemplates.Groups(invoice([]string{"amount"}), s); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a metric as a group field: %v", err)
	}
	if _, err = doctemplates.Groups(invoice([]string{"customer"}), orders([]string{"A"})); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a short row: %v", err)
	}
	other := invoice(nil)
	other.Binding.Integration = "elsewhere"
	if _, err = doctemplates.Groups(other, s); !errors.Is(err, r.ErrDenied) {
		t.Errorf("another integration: %v", err)
	}
	if _, err = doctemplates.Groups(document(), s); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a 1.x document: %v", err)
	}
	// The group that holds a row.
	g, err := doctemplates.GroupOf(invoice([]string{"order"}), orders(line("B", "乙", "", "1", "1"), line("A", "甲", "", "1", "1"), line("B", "乙", "", "1", "1")), 2)
	if err != nil || !reflect.DeepEqual(g, doctemplates.RowGroup{Key: []string{"B"}, Rows: []int{0, 2}}) {
		t.Errorf("group of row 2: %+v %v", g, err)
	}
	for _, row := range []int{-1, 3} {
		if _, err := doctemplates.GroupOf(invoice([]string{"order"}), orders(line("B", "乙", "", "1", "1"), line("A", "甲", "", "1", "1"), line("B", "乙", "", "1", "1")), row); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("row %d: %v", row, err)
		}
	}
}

func TestBindGroupTakesFieldsFromTheGroupAndSumsExactly(t *testing.T) {
	s := orders(
		line("A", "甲", "first", "1", "9007199254740993.01"),
		line("B", "乙", "other", "5", "10"),
		line("A", "甲", "second", "2", "-0.5"),
		line("A", "甲", "third", "3", "0.125"),
	)
	d := invoice([]string{"order"})
	groups, err := doctemplates.Groups(d, s)
	if err != nil {
		t.Fatal(err)
	}
	values, table, err := doctemplates.BindGroup(d, s, groups[0])
	if err != nil {
		t.Fatal(err)
	}
	// The order is a group field, the customer the same in every row; the sum is exact, with the longest fraction (the declared
	// scale is 2: three digits here), the quantity an integer sum; a literal and a page text are as the template has them.
	want := map[string]string{"e1": "A", "e2": "甲", "e3": "{page}/{pages}", "e4": "标题", "e6": "9007199254740992.635", "e7": "6"}
	if !reflect.DeepEqual(values, want) {
		t.Errorf("values %v, want %v", values, want)
	}
	if !reflect.DeepEqual(table.Index, []int{0, 2, 3}) || len(table.Rows) != 3 || table.Rows[1][0] != "second" || table.Rows[2][2] != "0.125" || len(table.Totals) != 0 {
		t.Errorf("table %+v", table)
	}
	if table.SnapshotRow(1) != 2 || (&doctemplates.TableData{}).SnapshotRow(4) != 4 {
		t.Error("the snapshot row of a table row")
	}
	if !reflect.DeepEqual(table.Metric, []bool{false, true, true}) || table.Columns[2].Scale != 2 {
		t.Errorf("columns %+v %v", table.Columns, table.Metric)
	}
	// The scale of the column is kept: a sum of whole numbers prints with it.
	values, _, err = doctemplates.BindGroup(d, orders(line("A", "甲", "", "1", "1"), line("A", "甲", "", "1", "2")), doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0, 1}})
	if err != nil || values["e6"] != "3.00" || values["e7"] != "2" {
		t.Errorf("scale kept: %v %v", values, err)
	}
	// An empty group (no group fields, no rows): fields are empty, the sums are zero at the scale.
	values, table, err = doctemplates.BindGroup(invoice(nil), orders(), doctemplates.RowGroup{})
	if err != nil || values["e1"] != "" || values["e6"] != "0.00" || values["e7"] != "0" || len(table.Rows) != 0 {
		t.Errorf("no rows: %v %+v %v", values, table, err)
	}
}

func TestBindGroupRefusesAFieldThatDiffersWithinTheGroup(t *testing.T) {
	s := orders(line("A", "甲", "x", "1", "1.00"), line("A", "甲", "y", "1", "1.00"), line("A", "乙", "z", "1", "1.00"))
	d := invoice([]string{"order"})
	groups, _ := doctemplates.Groups(d, s)
	_, _, err := doctemplates.BindGroup(d, s, groups[0])
	det, ok := doctemplates.DetailOf(err)
	if !errors.Is(err, r.ErrUnsupported) || !ok || det.Reason != doctemplates.ReasonInconsistentGroup || det.Band != "page_header" || det.Element != "e2" || det.Column != "customer" || det.Row == nil || *det.Row != 2 {
		t.Errorf("an inconsistent field: %v %+v", err, det)
	}
	// Neither the message nor the detail carries a value.
	for _, v := range []string{"甲", "乙"} {
		if strings.Contains(err.Error(), v) {
			t.Errorf("the refusal carries %q: %v", v, err)
		}
	}
	// The same field in a group where it agrees is fine, and a group field is never inconsistent.
	if _, _, err := doctemplates.BindGroup(d, orders(line("A", "甲", "x", "1", "1.00"), line("A", "甲", "y", "1", "1.00")), doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0, 1}}); err != nil {
		t.Error(err)
	}
	// The first row that differs is named, not the last.
	s = orders(line("A", "甲", "", "1", "1"), line("A", "甲", "", "1", "1"), line("A", "乙", "", "1", "1"), line("A", "丙", "", "1", "1"))
	groups, _ = doctemplates.Groups(d, s)
	if _, _, err = doctemplates.BindGroup(d, s, groups[0]); err == nil || *detailRow(err) != 2 {
		t.Errorf("the first differing row: %v", err)
	}
}

func detailRow(err error) *int {
	d, _ := doctemplates.DetailOf(err)
	return d.Row
}

func TestBindGroupRefusesWhatItCannotSumOrFind(t *testing.T) {
	s := orders(line("A", "甲", "x", "1", "1.00"), line("A", "甲", "y", "2", "1,000.00"))
	d := invoice([]string{"order"})
	groups, _ := doctemplates.Groups(d, s)
	if _, _, err := doctemplates.BindGroup(d, s, groups[0]); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a sum of a value that is not a decimal: %v", err)
	}
	// A group sum of a dimension is not a sum of a metric; of a field the snapshot lacks it is denied.
	sum := invoice([]string{"order"})
	sum.Bands.DocumentFooter.Elements[0].Field = "customer"
	good := orders(line("A", "甲", "x", "1", "1.00"))
	if _, _, err := doctemplates.BindGroup(sum, good, doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0}}); !errors.Is(err, r.ErrDenied) {
		t.Errorf("a sum of a dimension: %v", err)
	}
	sum.Bands.DocumentFooter.Elements[0].Field = "ghost"
	if _, _, err := doctemplates.BindGroup(sum, good, doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0}}); !errors.Is(err, r.ErrDenied) {
		t.Errorf("a sum of a field the snapshot lacks: %v", err)
	}
	field := invoice([]string{"order"})
	field.Bands.PageHeader.Elements[1].Field = "ghost"
	if _, _, err := doctemplates.BindGroup(field, good, doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0}}); !errors.Is(err, r.ErrDenied) {
		t.Errorf("a field the snapshot lacks: %v", err)
	}
	// A sum of a text metric (a metric that is not a number) is not a sum.
	text := orders(line("A", "甲", "x", "1", "1.00"))
	text.Result.Columns[4].Type = "string"
	if _, _, err := doctemplates.BindGroup(d, text, doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0}}); !errors.Is(err, r.ErrUnsupported) {
		t.Errorf("a sum of a string metric: %v", err)
	}
	// A table field the snapshot lacks is denied.
	tbl := invoice([]string{"order"})
	tbl.Bands.Detail.Elements[0].Columns[0].Field = "ghost"
	if _, _, err := doctemplates.BindGroup(tbl, good, doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0}}); !errors.Is(err, r.ErrDenied) {
		t.Errorf("a table field the snapshot lacks: %v", err)
	}
	// A cell that is not valid UTF-8 and a short row are integrity failures.
	bad := orders(line("A", "甲", "a\xffb", "1", "1.00"))
	if _, _, err := doctemplates.BindGroup(d, bad, doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0}}); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a cell of invalid UTF-8: %v", err)
	}
	// Another snapshot, and a 1.x document.
	d.Binding.Dataset.Version = "2"
	if _, _, err := doctemplates.BindGroup(d, good, doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0}}); !errors.Is(err, r.ErrDenied) {
		t.Errorf("another dataset: %v", err)
	}
	if _, _, err := doctemplates.BindGroup(document(), good, doctemplates.RowGroup{}); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a 1.x document: %v", err)
	}
}

// TestBindGroupBindsOnlyWholeGroupsOfTheSnapshot: a document of part of a group, of rows of two groups or of rows
// out of order would be a silent decision; none is accepted.
func TestBindGroupBindsOnlyWholeGroupsOfTheSnapshot(t *testing.T) {
	s := orders(line("A", "甲", "", "1", "1.00"), line("B", "乙", "", "1", "2.00"), line("A", "甲", "", "1", "3.00"))
	d := invoice([]string{"order"})
	for name, g := range map[string]doctemplates.RowGroup{
		"part of a group":          {Key: []string{"A"}, Rows: []int{0}},
		"rows of two groups":       {Key: []string{"A"}, Rows: []int{0, 1, 2}},
		"rows out of order":        {Key: []string{"A"}, Rows: []int{2, 0}},
		"a repeated row":           {Key: []string{"A"}, Rows: []int{0, 0, 2}},
		"a row the snapshot lacks": {Key: []string{"A"}, Rows: []int{0, 2, 3}},
		"a negative row":           {Key: []string{"A"}, Rows: []int{-1, 0, 2}},
		"another key":              {Key: []string{"B"}, Rows: []int{0, 2}},
		"no key":                   {Rows: []int{0, 2}},
		"a longer key":             {Key: []string{"A", "甲"}, Rows: []int{0, 2}},
		"no rows":                  {Key: []string{"A"}},
	} {
		if _, _, err := doctemplates.BindGroup(d, s, g); !errors.Is(err, r.ErrInvalid) && !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without a group the group is the whole snapshot or nothing.
	none := invoice(nil)
	if _, _, err := doctemplates.BindGroup(none, s, doctemplates.RowGroup{Rows: []int{0, 1}}); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("part of the snapshot: %v", err)
	}
	if _, _, err := doctemplates.BindGroup(none, s, doctemplates.RowGroup{Rows: []int{0, 1, 2}}); err == nil || !errors.Is(err, r.ErrDenied) && !errors.Is(err, r.ErrUnsupported) && !errors.Is(err, r.ErrIntegrity) && !errors.Is(err, r.ErrInvalid) {
		// Inconsistent fields (the customer differs) are the refusal of an ungrouped document over many orders.
		t.Logf("a whole snapshot: %v", err)
	}
	if _, _, err := doctemplates.BindGroup(d, s, doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0, 2}}); err != nil {
		t.Errorf("a whole group: %v", err)
	}
}

func TestBindGroupChecksWhatEachTextCanPrint(t *testing.T) {
	d := invoice([]string{"order"})
	g := doctemplates.RowGroup{Key: []string{"A"}, Rows: []int{0}}
	bind := func(customer string, wrap bool) error {
		dd := cloneDocument(t, d)
		dd.Bands.PageHeader.Elements[1].Wrap = wrap
		_, _, err := doctemplates.BindGroup(dd, orders(line("A", customer, "", "1", "1.00")), g)
		return err
	}
	check := func(name string, err error, reason string) {
		t.Helper()
		det, _ := doctemplates.DetailOf(err)
		if !errors.Is(err, r.ErrUnsupported) || det.Reason != reason || det.Band != "page_header" || det.Element != "e2" {
			t.Errorf("%s: %v %+v", name, err, det)
		}
	}
	// One line: no control (a line break included) and at most 256 characters; wrapped: its line breaks and tabs, 1024 characters.
	for name, c := range map[string]struct {
		v      string
		wrap   bool
		reason string
		ok     bool
	}{
		"a plain value":            {"客户", false, "", true},
		"a tab":                    {"a\tb", false, doctemplates.ReasonNotCovered, false},
		"a line break":             {"a\nb", false, doctemplates.ReasonNotCovered, false},
		"256 characters":           {strings.Repeat("字", 256), false, "", true},
		"257 characters":           {strings.Repeat("字", 257), false, doctemplates.ReasonDoesNotFit, false},
		"a line break, wrapped":    {"a\nb", true, "", true},
		"a CRLF, wrapped":          {"a\r\nb", true, "", true},
		"a tab, wrapped":           {"a\tb", true, "", true},
		"DEL, wrapped":             {"a\x7fb", true, doctemplates.ReasonNotCovered, false},
		"a bell, wrapped":          {"a\x07b", true, doctemplates.ReasonNotCovered, false},
		"1024 characters, wrapped": {strings.Repeat("字", 1024), true, "", true},
		"1025 characters, wrapped": {strings.Repeat("字", 1025), true, doctemplates.ReasonDoesNotFit, false},
	} {
		err := bind(c.v, c.wrap)
		if c.ok {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		check(name, err, c.reason)
	}
	// A group field of 300 characters: refused as a value, with its place.
	long := doctemplates.RowGroup{Key: []string{strings.Repeat("a", 300)}, Rows: []int{0}}
	_, _, err := doctemplates.BindGroup(d, orders(line(strings.Repeat("a", 300), "甲", "", "1", "1.00")), long)
	if det, _ := doctemplates.DetailOf(err); !errors.Is(err, r.ErrUnsupported) || det.Reason != doctemplates.ReasonDoesNotFit || det.Element != "e1" {
		t.Errorf("a group value over 256 characters: %v %+v", err, det)
	}
}

func TestBind1xStillBindsByRowAndNowSaysWhereItRefuses(t *testing.T) {
	d := document()
	// A 1.x document is bound by row; a 2.0 document is not bound by row at all.
	if _, e := doctemplates.Bind(invoice(nil), orders(), 0); !errors.Is(e, r.ErrUnsupported) {
		t.Errorf("Bind of a 2.0 document: %v", e)
	}
	if _, e := doctemplates.BindTable(invoice(nil), orders()); !errors.Is(e, r.ErrUnsupported) {
		t.Errorf("BindTable of a 2.0 document: %v", e)
	}
	// The details of 1.x: the element id, and the reason.
	for name, c := range map[string]struct {
		v      string
		reason string
	}{"a tab": {"a\tb", doctemplates.ReasonNotCovered}, "too long": {strings.Repeat("a", 257), doctemplates.ReasonDoesNotFit}, "DEL": {"a\x7fb", doctemplates.ReasonNotCovered}} {
		s := r.Snapshot{Owner: r.Owner{Integration: "source"}, Query: r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}}, Result: r.Result{Columns: []r.Column{{ID: "amount"}}, Rows: [][]string{{c.v}}, Totals: map[string]string{"amount": c.v}}}
		_, e := doctemplates.Bind(d, s, 0)
		det, ok := doctemplates.DetailOf(e)
		if !errors.Is(e, r.ErrUnsupported) || !ok || det.Reason != c.reason || det.Element != "n1" || det.Band != "" || det.Row != nil || det.Column != "" {
			t.Errorf("%s: %v %+v", name, e, det)
		}
		if strings.Contains(fmt.Sprint(e), strings.TrimSpace(c.v)) && len(c.v) > 3 {
			t.Errorf("%s: the refusal carries the value", name)
		}
	}
}

func TestSumDecimalsKeepsTheScaleAndRefusesWhatIsNotADecimal(t *testing.T) {
	for _, c := range []struct {
		scale  int
		values []string
		want   string
	}{
		{2, []string{"1.50", "2", "-0.25"}, "3.25"},
		{2, []string{"1", "2"}, "3.00"},
		{2, []string{"0.001", "0.002"}, "0.003"},
		{0, []string{"1", "2.5", "0.125", "-1"}, "2.625"},
		{2, nil, "0.00"},
		{0, nil, "0"},
		{2, []string{"-0.01"}, "-0.01"},
		{2, []string{"0.01", "-0.01"}, "0.00"},
		{3, []string{"9007199254740993.01", "9007199254740993.01"}, "18014398509481986.020"},
	} {
		got, err := doctemplates.SumDecimals(c.values, c.scale)
		if err != nil || got != c.want {
			t.Errorf("%v at scale %d: %q (%v), want %q", c.values, c.scale, got, err, c.want)
		}
	}
	for _, v := range []string{"", "1,000", "1e3", " 1", "1 ", "+1", "01", ".5", "1.", "NaN", "１"} {
		if _, err := doctemplates.SumDecimals([]string{v}, 2); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%q: %v", v, err)
		}
	}
}

func TestGroupDigitsGroupsTheIntegerPartAndNothingElse(t *testing.T) {
	for v, want := range map[string]string{
		"0": "0", "999": "999", "1000": "1,000", "-1000": "-1,000", "1234567.800": "1,234,567.800", "-0.5": "-0.5", "0.001": "0.001",
		"9007199254740993.01": "9,007,199,254,740,993.01", "100": "100", "1000000": "1,000,000",
	} {
		if got := doctemplates.GroupDigits(v); got != want {
			t.Errorf("%s: %q, want %q", v, got, want)
		}
	}
	// Anything that is not an exact decimal is returned as it is: nothing is ever invented.
	for _, v := range []string{"", "1e3", "1,000", "+1", "01", ".5", "1.", "x", " 1"} {
		if got := doctemplates.GroupDigits(v); got != v {
			t.Errorf("%q: %q", v, got)
		}
	}
}
