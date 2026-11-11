package present_test

import (
	"errors"
	"strings"
	"testing"

	"idp.local/reporting/export/present"
	r "idp.local/reporting/reporting"
)

// ledger is a snapshot of two dimensions (a customer, an integer year) and two metrics (an amount in fen-exact CNY, a count), with the
// values that exercise every format: negatives, zeros, a number beyond float64's integer precision.
func ledger() r.Snapshot {
	return r.Snapshot{
		ID:    "0123456789abcdef0123456789abcdef",
		Query: r.QuerySpec{Dataset: r.Ref{ID: "finance.ledger", Version: "1"}, Dimensions: []string{"customer", "year"}, Metrics: []r.Ref{{ID: "amount", Version: "1"}, {ID: "count", Version: "1"}}},
		Result: r.Result{
			Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "year", Label: "年份", Type: "integer"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2, Currency: "CNY"}, {ID: "count", Label: "笔数", Type: "integer", Unit: "笔"}},
			Rows: [][]string{
				{"甲", "2026", "1234567.89", "3"},
				{"乙", "2026", "-0.50", "0"},
				{"甲", "2025", "0.00", "9007199254740993"},
				{"", "2025", "100.00", "1"},
			},
			Totals: map[string]string{"amount": "1234667.39", "count": "9007199254740997"},
		},
	}
}

func TestFormatChangesNoDigit(t *testing.T) {
	grouped, plain := r.DefaultNumberFormat, r.PlainNumberFormat
	parens := r.NumberFormat{Grouping: true, Negative: r.NegativeParentheses, Zero: r.ZeroDigits}
	dash := r.NumberFormat{Grouping: true, Negative: r.NegativeMinus, Zero: r.ZeroDash}
	for _, c := range []struct {
		in   string
		f    r.NumberFormat
		want string
	}{
		{"1234567.89", grouped, "1,234,567.89"},
		{"1234567.89", plain, "1234567.89"},
		{"-1234567.89", grouped, "-1,234,567.89"},
		{"-1234567.89", parens, "(1,234,567.89)"},
		{"-0.50", parens, "(0.50)"},
		{"0.00", dash, "—"},
		{"0", dash, "—"},
		{"-0.00", dash, "—"},
		{"0.00", grouped, "0.00"},
		{"100.00", dash, "100.00"},
		{"9007199254740993", grouped, "9,007,199,254,740,993"},
		{"999999.000000000000000001", plain, "999999.000000000000000001"},
		{"not a number", grouped, "not a number"},
	} {
		if got := present.Format(c.in, c.f); got != c.want {
			t.Errorf("Format(%q, %+v) = %q, want %q", c.in, c.f, got, c.want)
		}
		if c.want != "—" && strings.NewReplacer(",", "", "(", "-", ")", "").Replace(present.Format(c.in, c.f)) != c.in {
			t.Errorf("Format(%q, %+v) changed a digit", c.in, c.f)
		}
	}
}

func TestExcelNumberFormatFollowsScaleAndFormat(t *testing.T) {
	for _, c := range []struct {
		scale int
		f     r.NumberFormat
		want  string
	}{
		{2, r.DefaultNumberFormat, "#,##0.00;-#,##0.00"},
		{0, r.DefaultNumberFormat, "#,##0;-#,##0"},
		{2, r.PlainNumberFormat, "0.00;-0.00"},
		{3, r.NumberFormat{Grouping: true, Negative: r.NegativeParentheses, Zero: r.ZeroDigits}, "#,##0.000;(#,##0.000)"},
		{2, r.NumberFormat{Grouping: false, Negative: r.NegativeParentheses, Zero: r.ZeroDash}, `0.00;(0.00);"—"`},
	} {
		if got := present.ExcelNumberFormat(c.scale, c.f); got != c.want {
			t.Errorf("ExcelNumberFormat(%d, %+v) = %q, want %q", c.scale, c.f, got, c.want)
		}
	}
}

func TestRoundTripsThroughFloat64AtTheScale(t *testing.T) {
	for _, c := range []struct {
		v     string
		scale int
		want  bool
	}{
		{"1234567.89", 2, true},
		{"-0.50", 2, true},
		{"0.00", 2, true},
		{"9007199254740893.00", 2, true},  // below 2^53: exact
		{"9007199254740993.01", 2, false}, // above 2^53: another number comes back
		{"9007199254740993", 0, false},
		{"9007199254740992", 0, true},
		{"123456789012345678.99", 2, false},
		{"0.1", 1, true},
		{"0.10", 2, true},
		{"1", 0, true},
		{"1.0", 0, false}, // not the column's scale
		{"1,000", 0, false},
		{"", 0, false},
	} {
		if got := present.RoundTrips(c.v, c.scale); got != c.want {
			t.Errorf("RoundTrips(%q, %d) = %v, want %v", c.v, c.scale, got, c.want)
		}
	}
}

func TestCompareAndHoldsAreExact(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.0", "1.00", 0}, {"-0.01", "0", -1}, {"9007199254740993", "9007199254740992", 1}, {"0.000000000000000001", "0", 1}, {"-5", "-4.99", -1}} {
		if got, err := present.Compare(c.a, c.b); err != nil || got != c.want {
			t.Errorf("Compare(%q, %q) = %d (%v), want %d", c.a, c.b, got, err, c.want)
		}
	}
	if _, err := present.Compare("1", "x"); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a comparison with a non-number: %v", err)
	}
	numeric := func(when, value, v string) bool {
		holds, err := present.Holds(r.Mark{Column: "c", When: when, Value: value, Mark: r.MarkBad}, v, true)
		if err != nil {
			t.Fatal(when, value, v, err)
		}
		return holds
	}
	if !numeric(r.WhenLess, "0", "-0.50") || numeric(r.WhenLess, "0", "0.00") || !numeric(r.WhenLessEqual, "0", "0.00") || !numeric(r.WhenGreater, "100", "100.01") || numeric(r.WhenGreaterEqual, "100", "99.99") || !numeric(r.WhenEqual, "1", "1.00") || numeric(r.WhenNotEqual, "1", "1.00") {
		t.Error("numeric relations")
	}
	if _, err := present.Holds(r.Mark{When: r.WhenBlank}, "1", true); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("blank on a numeric column: %v", err)
	}
	text := func(when, value, v string) bool {
		holds, err := present.Holds(r.Mark{Column: "c", When: when, Value: value, Mark: r.MarkGood}, v, false)
		if err != nil {
			t.Fatal(when, value, v, err)
		}
		return holds
	}
	if !text(r.WhenEqual, "current", "current") || text(r.WhenEqual, "current", "Current") || !text(r.WhenNotEqual, "current", "1_30") || !text(r.WhenBlank, "", "") || text(r.WhenBlank, "", " ") {
		t.Error("string relations are exact")
	}
	if _, err := present.Holds(r.Mark{When: r.WhenLess, Value: "1"}, "x", false); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("an ordered relation on a string column: %v", err)
	}
}

func TestWholeIsTheSnapshotAsItAlwaysWas(t *testing.T) {
	s := ledger()
	table, err := present.Build(s, present.Whole(s))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Columns) != 4 || table.Title != "" || table.Footnote != "" || table.LayoutID != "" || len(table.Rows) != 4 {
		t.Fatalf("%+v", table)
	}
	for k, want := range []string{"客户", "年份", "金额", "笔数"} {
		if table.Columns[k].Header() != want || table.Columns[k].Label != "" {
			t.Errorf("column %d header %q", k, table.Columns[k].Header())
		}
	}
	for k, want := range []string{r.AlignLeft, r.AlignRight, r.AlignRight, r.AlignRight} {
		if table.Columns[k].Align != want {
			t.Errorf("column %d align %q", k, table.Columns[k].Align)
		}
	}
	// A metric is grouped, a numeric dimension is left as it is, a string is itself.
	if got := texts(table.Rows[0]); !equal(got, []string{"甲", "2026", "1,234,567.89", "3"}) {
		t.Errorf("row 0 = %q", got)
	}
	if got := texts(table.Rows[2]); !equal(got, []string{"甲", "2025", "0.00", "9,007,199,254,740,993"}) {
		t.Errorf("row 2 = %q", got)
	}
	for _, row := range table.Rows {
		if row.Subtotal {
			t.Error("a subtotal row without subtotals")
		}
		for _, c := range row.Cells {
			if c.Mark != "" {
				t.Error("a mark without marks")
			}
		}
	}
	if table.TotalLabelAt != 0 || table.Totals == nil || !equal(cellTexts(table.Totals), []string{"合计", "", "1,234,667.39", "9,007,199,254,740,997"}) {
		t.Errorf("totals %v at %d", cellTexts(table.Totals), table.TotalLabelAt)
	}
	if table.Totals[2].Exact != "1234667.39" || table.Rows[0].Cells[2].Exact != "1234567.89" || !table.Columns[2].Metric || table.Columns[1].Metric {
		t.Error("exact strings and metric flags")
	}
}

func TestBuildPresentsColumnsGroupsAndMarks(t *testing.T) {
	s := ledger()
	parens := r.NumberFormat{Grouping: true, Negative: r.NegativeParentheses, Zero: r.ZeroDash}
	p := r.Presentation{
		LayoutID: "fedcba9876543210fedcba9876543210", LayoutVersion: 3, Title: "客户账龄", Footnote: "金额为含税金额。",
		Columns: []r.PresentedColumn{
			{ID: "amount", Label: "应收", Align: r.AlignRight, Format: &parens},
			{ID: "customer", Align: r.AlignCenter},
			{ID: "count", Align: r.AlignRight},
		},
		SubtotalBy: "customer",
		Marks: []r.Mark{
			{Column: "amount", When: r.WhenLess, Value: "0", Mark: r.MarkBad},
			{Column: "amount", When: r.WhenGreaterEqual, Value: "100", Mark: r.MarkGood},
			{Column: "amount", When: r.WhenGreater, Value: "1000000", Mark: r.MarkWarn}, // after good: never reached for 1234567.89
			{Column: "customer", When: r.WhenBlank, Mark: r.MarkNote},
			{Column: "count", When: r.WhenEqual, Value: "0", Mark: r.MarkWarn},
		},
	}
	table, err := present.Build(s, p)
	if err != nil {
		t.Fatal(err)
	}
	if table.Title != "客户账龄" || table.Footnote != "金额为含税金额。" || table.LayoutID != p.LayoutID || table.LayoutVersion != 3 {
		t.Errorf("texts and identity: %+v", table)
	}
	if len(table.Columns) != 3 || table.Columns[0].Header() != "应收" || table.Columns[0].Index != 2 || table.Columns[1].Header() != "客户" || table.Columns[1].Align != r.AlignCenter || table.Columns[2].Source.Unit != "笔" {
		t.Errorf("columns %+v", table.Columns)
	}
	// Groups by the exact customer strings, in first-row order (甲, 乙, ""), the rows in snapshot order inside, a subtotal row after each.
	want := [][]string{
		{"1,234,567.89", "甲", "3"},
		{"—", "甲", "9,007,199,254,740,993"},
		{"1,234,567.89", "小计", "9,007,199,254,740,996"},
		{"(0.50)", "乙", "0"},
		{"(0.50)", "小计", "0"},
		{"100.00", "", "1"},
		{"100.00", "小计", "1"},
	}
	if len(table.Rows) != len(want) {
		t.Fatalf("%d rows: %v", len(table.Rows), table.Rows)
	}
	for i, row := range table.Rows {
		if got := texts(row); !equal(got, want[i]) {
			t.Errorf("row %d = %q, want %q", i, got, want[i])
		}
		if row.Subtotal != (i == 2 || i == 4 || i == 6) {
			t.Errorf("row %d subtotal %v", i, row.Subtotal)
		}
	}
	if table.Rows[0].Index != 0 || table.Rows[1].Index != 2 || table.Rows[2].Index != -1 || table.Rows[3].Index != 1 || table.Rows[5].Index != 3 {
		t.Error("snapshot row indices")
	}
	// The subtotal sums are exact strings at the column's scale, the label cell has no exact value.
	if sub := table.Rows[2]; sub.Cells[0].Exact != "1234567.89" || sub.Cells[1].Exact != "" || sub.Cells[2].Exact != "9007199254740996" || sub.Group != "甲" {
		t.Errorf("subtotal of 甲: %+v", sub)
	}
	if sub := table.Rows[4]; sub.Cells[0].Exact != "-0.50" || sub.Cells[2].Exact != "0" {
		t.Errorf("subtotal of 乙: %+v", sub)
	}
	// Marks: the first rule that holds wins; a subtotal row is never marked.
	marks := func(i int) []string {
		out := []string{}
		for _, c := range table.Rows[i].Cells {
			out = append(out, c.Mark)
		}
		return out
	}
	for i, want := range [][]string{{r.MarkGood, "", ""}, {"", "", ""}, {"", "", ""}, {r.MarkBad, "", r.MarkWarn}, {"", "", ""}, {r.MarkGood, r.MarkNote, ""}, {"", "", ""}} {
		if got := marks(i); !equal(got, want) {
			t.Errorf("row %d marks %q, want %q", i, got, want)
		}
	}
	// The source's totals, formatted as the columns say, the label in the first column that is not a summed metric.
	if table.TotalLabelAt != 1 || !equal(cellTexts(table.Totals), []string{"1,234,667.39", "合计", "9,007,199,254,740,997"}) {
		t.Errorf("totals %v at %d", cellTexts(table.Totals), table.TotalLabelAt)
	}
}

func TestBuildListsTotalsWhenEveryShownColumnIsAMetric(t *testing.T) {
	s := ledger()
	table, err := present.Build(s, r.Presentation{Columns: []r.PresentedColumn{{ID: "count", Align: r.AlignRight}, {ID: "amount", Align: r.AlignRight}}})
	if err != nil {
		t.Fatal(err)
	}
	if table.TotalLabelAt != -1 || !equal(cellTexts(table.Totals), []string{"9,007,199,254,740,997", "1,234,667.39"}) {
		t.Errorf("totals %v at %d", cellTexts(table.Totals), table.TotalLabelAt)
	}
	// Without a shown metric there is nothing to total.
	table, err = present.Build(s, r.Presentation{Columns: []r.PresentedColumn{{ID: "customer", Align: r.AlignLeft}}})
	if err != nil || table.Totals != nil || table.TotalLabelAt != -1 {
		t.Errorf("no metric: totals %v at %d (%v)", table.Totals, table.TotalLabelAt, err)
	}
	// A metric that is not numeric is shown and totalled verbatim, never summed.
	s.Query.Metrics = append(s.Query.Metrics, r.Ref{ID: "status", Version: "1"})
	s.Result.Columns = append(s.Result.Columns, r.Column{ID: "status", Label: "状态", Type: "string"})
	for i := range s.Result.Rows {
		s.Result.Rows[i] = append(s.Result.Rows[i], "open")
	}
	s.Result.Totals["status"] = "mixed"
	table, err = present.Build(s, r.Presentation{Columns: []r.PresentedColumn{{ID: "customer", Align: r.AlignLeft}, {ID: "status", Align: r.AlignLeft}, {ID: "amount", Align: r.AlignRight}}, SubtotalBy: "customer"})
	if err != nil || table.TotalLabelAt != 0 || !equal(cellTexts(table.Totals), []string{"合计", "mixed", "1,234,667.39"}) || table.Columns[1].Metric {
		t.Errorf("a string metric: totals %v at %d, metric %v (%v)", cellTexts(table.Totals), table.TotalLabelAt, table.Columns[1].Metric, err)
	}
	if sub := table.Rows[2]; !sub.Subtotal || !equal(texts(sub), []string{"小计", "", "1,234,567.89"}) {
		t.Errorf("the subtotal of a group with a string metric: %v", texts(sub))
	}
}

func TestBuildRefusesWhatDoesNotFit(t *testing.T) {
	s := ledger()
	f := r.DefaultNumberFormat
	for name, p := range map[string]r.Presentation{
		"unknown column":           {Columns: []r.PresentedColumn{{ID: "nope", Align: r.AlignLeft}}},
		"format on a string":       {Columns: []r.PresentedColumn{{ID: "customer", Align: r.AlignLeft, Format: &f}}},
		"subtotal by a metric":     {Columns: []r.PresentedColumn{{ID: "amount", Align: r.AlignRight}}, SubtotalBy: "amount"},
		"blank on a number":        {Columns: []r.PresentedColumn{{ID: "amount", Align: r.AlignRight}}, Marks: []r.Mark{{Column: "amount", When: r.WhenBlank, Mark: r.MarkBad}}},
		"order on a string":        {Columns: []r.PresentedColumn{{ID: "customer", Align: r.AlignLeft}}, Marks: []r.Mark{{Column: "customer", When: r.WhenLess, Value: "1", Mark: r.MarkBad}}},
		"string value on a number": {Columns: []r.PresentedColumn{{ID: "amount", Align: r.AlignRight}}, Marks: []r.Mark{{Column: "amount", When: r.WhenEqual, Value: "none", Mark: r.MarkBad}}},
	} {
		if _, err := present.Build(s, p); !errors.Is(err, r.ErrIntegrity) {
			t.Errorf("%s: %v, want ErrIntegrity", name, err)
		}
	}
	if _, err := present.Build(s, r.Presentation{}); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("an empty presentation: %v", err)
	}
	// A numeric cell that is not an exact number is a snapshot that cannot be trusted.
	bad := ledger()
	bad.Result.Rows[1][2] = "1,000"
	if _, err := present.Build(bad, present.Whole(bad)); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a grouped value in the snapshot: %v", err)
	}
	short := ledger()
	short.Result.Rows[0] = short.Result.Rows[0][:3]
	if _, err := present.Build(short, present.Whole(short)); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a short row: %v", err)
	}
}

func TestSumIsExactAndNeverRounds(t *testing.T) {
	c := present.NewSum(2)
	for _, v := range []string{"1.50", "2", "-0.25", "9007199254740993.01"} {
		if err := c.Add(v); err != nil {
			t.Fatal(v, err)
		}
	}
	if got := c.String(); got != "9007199254740996.26" || !c.Equals("9007199254740996.260") || c.Equals("9007199254740996.27") {
		t.Errorf("sum %s", got)
	}
	copied := c.Copy()
	_ = copied.Add("0.01")
	if c.String() != "9007199254740996.26" || copied.String() != "9007199254740996.27" || c.Zero().String() != "0.00" || c.Frac() != 2 {
		t.Error("copy, zero and frac")
	}
	c = present.NewSum(0)
	for _, v := range []string{"1", "2.5", "0.125", "-1"} {
		_ = c.Add(v)
	}
	if got := c.String(); got != "2.625" {
		t.Errorf("sum %s", got)
	}
	if err := c.Add("1,000"); !errors.Is(err, r.ErrIntegrity) {
		t.Errorf("a grouped number: %v", err)
	}
	if present.GroupDigits("-1234567.891") != "-1,234,567.891" || present.GroupDigits("999") != "999" {
		t.Error("GroupDigits")
	}
}

func TestPaletteIsClosedAndLight(t *testing.T) {
	if len(present.Palette) != 4 {
		t.Fatal(present.Palette)
	}
	for _, mark := range []string{r.MarkGood, r.MarkWarn, r.MarkBad, r.MarkNote} {
		c, ok := present.Palette[mark]
		if !ok || len(c) != 6 || strings.ToUpper(c) != c {
			t.Errorf("%s: %q", mark, c)
		}
	}
}

func texts(row present.Row) []string {
	return cellTexts(row.Cells)
}

func cellTexts(cells []present.Cell) []string {
	out := []string{}
	for _, c := range cells {
		out = append(out, c.Text)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
