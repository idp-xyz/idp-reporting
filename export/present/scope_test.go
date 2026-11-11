package present_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"idp.local/reporting/export/present"
	r "idp.local/reporting/reporting"
)

// typedColumns is a column of every type a dataset may have.
func typedColumns() []r.Column {
	return []r.Column{
		{ID: "customer", Label: "客户", Type: "string"},
		{ID: "due", Label: "到期日", Type: "date"},
		{ID: "overdue", Label: "是否逾期", Type: "boolean"},
		{ID: "count", Label: "笔数", Type: "integer"},
		{ID: "balance", Label: "应收余额", Type: "decimal", Scale: 2, Currency: "CNY"},
	}
}

// scoped is a snapshot of the typed columns whose frozen query carries the filters and the sort.
func scoped(filters []r.Filter, sort []r.Sort) r.Snapshot {
	return r.Snapshot{Query: r.QuerySpec{Filters: filters, Sort: sort}, Result: r.Result{Columns: typedColumns()}}
}

// TestEveryOperatorOfEveryTypeHasItsWords: the Chinese sentence the PDF prints and the English one the workbook writes, for every
// operator a column of each type may be filtered by; the values are the query's exact strings in both.
func TestEveryOperatorOfEveryTypeHasItsWords(t *testing.T) {
	cases := []struct {
		field, operator, value string
		values                 []string
		chinese, english       string
	}{
		{"customer", "eq", "甲方", []string{"甲方"}, "客户 等于 甲方", "客户 equals 甲方"},
		{"customer", "ne", "甲方", []string{"甲方"}, "客户 不等于 甲方", "客户 does not equal 甲方"},
		{"customer", "lt", "M", []string{"M"}, "客户 小于 M", "客户 is less than M"},
		{"customer", "le", "M", []string{"M"}, "客户 不大于 M", "客户 is less than or equal to M"},
		{"customer", "gt", "M", []string{"M"}, "客户 大于 M", "客户 is greater than M"},
		{"customer", "ge", "M", []string{"M"}, "客户 不小于 M", "客户 is greater than or equal to M"},
		{"customer", "in", `["甲方","乙方","丙方"]`, []string{"甲方", "乙方", "丙方"}, "客户 属于 甲方、乙方、丙方", "客户 is one of 甲方, 乙方, 丙方"},
		{"customer", "between", `["A","M"]`, []string{"A", "M"}, "客户 介于 A 和 M 之间", "客户 is between A and M"},
		{"customer", "prefix", "华东", []string{"华东"}, "客户 开头为 华东", "客户 starts with 华东"},
		{"customer", "contains", "贸易", []string{"贸易"}, "客户 包含 贸易", "客户 contains 贸易"},

		{"due", "eq", "2026-10-26", []string{"2026-10-26"}, "到期日 等于 2026-10-26", "到期日 equals 2026-10-26"},
		{"due", "ne", "2026-10-26", []string{"2026-10-26"}, "到期日 不等于 2026-10-26", "到期日 does not equal 2026-10-26"},
		{"due", "lt", "2026-10-26", []string{"2026-10-26"}, "到期日 小于 2026-10-26", "到期日 is less than 2026-10-26"},
		{"due", "le", "2026-10-26", []string{"2026-10-26"}, "到期日 不大于 2026-10-26", "到期日 is less than or equal to 2026-10-26"},
		{"due", "gt", "2026-10-26", []string{"2026-10-26"}, "到期日 大于 2026-10-26", "到期日 is greater than 2026-10-26"},
		{"due", "ge", "2026-10-26", []string{"2026-10-26"}, "到期日 不小于 2026-10-26", "到期日 is greater than or equal to 2026-10-26"},
		{"due", "in", `["2026-10-26","2026-11-26"]`, []string{"2026-10-26", "2026-11-26"}, "到期日 属于 2026-10-26、2026-11-26", "到期日 is one of 2026-10-26, 2026-11-26"},
		{"due", "between", `["2026-01-01","2026-03-31"]`, []string{"2026-01-01", "2026-03-31"}, "到期日 介于 2026-01-01 和 2026-03-31 之间", "到期日 is between 2026-01-01 and 2026-03-31"},

		{"overdue", "eq", "true", []string{"true"}, "是否逾期 等于 true", "是否逾期 equals true"},
		{"overdue", "ne", "false", []string{"false"}, "是否逾期 不等于 false", "是否逾期 does not equal false"},

		// A number is the query's exact string: never grouped, however many digits it has.
		{"count", "eq", "0", []string{"0"}, "笔数 等于 0", "笔数 equals 0"},
		{"count", "ne", "-3", []string{"-3"}, "笔数 不等于 -3", "笔数 does not equal -3"},
		{"count", "lt", "1000000", []string{"1000000"}, "笔数 小于 1000000", "笔数 is less than 1000000"},
		{"count", "le", "1000000", []string{"1000000"}, "笔数 不大于 1000000", "笔数 is less than or equal to 1000000"},
		{"count", "gt", "9007199254740993", []string{"9007199254740993"}, "笔数 大于 9007199254740993", "笔数 is greater than 9007199254740993"},
		{"count", "ge", "9007199254740993", []string{"9007199254740993"}, "笔数 不小于 9007199254740993", "笔数 is greater than or equal to 9007199254740993"},
		{"count", "in", `["0","-3","1000000"]`, []string{"0", "-3", "1000000"}, "笔数 属于 0、-3、1000000", "笔数 is one of 0, -3, 1000000"},
		{"count", "between", `["-3","1000000"]`, []string{"-3", "1000000"}, "笔数 介于 -3 和 1000000 之间", "笔数 is between -3 and 1000000"},

		{"balance", "eq", "0.00", []string{"0.00"}, "应收余额 等于 0.00", "应收余额 equals 0.00"},
		{"balance", "ne", "0.00", []string{"0.00"}, "应收余额 不等于 0.00", "应收余额 does not equal 0.00"},
		{"balance", "lt", "-0.24", []string{"-0.24"}, "应收余额 小于 -0.24", "应收余额 is less than -0.24"},
		{"balance", "le", "-0.24", []string{"-0.24"}, "应收余额 不大于 -0.24", "应收余额 is less than or equal to -0.24"},
		{"balance", "gt", "1000.00", []string{"1000.00"}, "应收余额 大于 1000.00", "应收余额 is greater than 1000.00"},
		{"balance", "ge", "1000.00", []string{"1000.00"}, "应收余额 不小于 1000.00", "应收余额 is greater than or equal to 1000.00"},
		{"balance", "in", `["0.00","9007199254740993.01"]`, []string{"0.00", "9007199254740993.01"}, "应收余额 属于 0.00、9007199254740993.01", "应收余额 is one of 0.00, 9007199254740993.01"},
		{"balance", "between", `["-1000000.00","1000000.00"]`, []string{"-1000000.00", "1000000.00"}, "应收余额 介于 -1000000.00 和 1000000.00 之间", "应收余额 is between -1000000.00 and 1000000.00"},
	}
	covered := map[[2]string]bool{}
	for _, c := range cases {
		scope, e := present.ScopeOf(scoped([]r.Filter{{Field: c.field, Operator: c.operator, Value: c.value}}, nil))
		if e != nil || len(scope.Filters) != 1 || len(scope.Sort) != 0 {
			t.Errorf("%s %s %s: %+v (%v)", c.field, c.operator, c.value, scope, e)
			continue
		}
		f := scope.Filters[0]
		// The field, the operator and the value are the query's, to the character; the values are the core's decoding of the value.
		if f.Field != c.field || f.Operator != c.operator || f.Value != c.value || !slices.Equal(f.Values, c.values) {
			t.Errorf("%s %s %s: read as %+v", c.field, c.operator, c.value, f)
		}
		if got := f.Chinese(); got != c.chinese {
			t.Errorf("%s %s: Chinese %q, want %q", c.field, c.operator, got, c.chinese)
		}
		if got := f.English(); got != c.english {
			t.Errorf("%s %s: English %q, want %q", c.field, c.operator, got, c.english)
		}
		for _, column := range typedColumns() {
			if column.ID == c.field {
				covered[[2]string{column.Type, c.operator}] = true
			}
		}
	}
	// The table above is every operator of the closed set on every type it fits (ADR 0023 decision 1), and nothing fits that is not there.
	for _, column := range typedColumns() {
		for _, op := range r.FilterOperators {
			if r.OperatorFits(op, column.Type) != covered[[2]string{column.Type, op}] {
				t.Errorf("%s on a %s column: fits %v, has words in this test %v", op, column.Type, r.OperatorFits(op, column.Type), covered[[2]string{column.Type, op}])
			}
		}
	}
	// The sort keys, in the order they apply, in both directions.
	scope, e := present.ScopeOf(scoped(nil, []r.Sort{{Field: "balance", Direction: "desc"}, {Field: "customer", Direction: "asc"}}))
	if e != nil || len(scope.Filters) != 0 || len(scope.Sort) != 2 {
		t.Fatalf("%+v (%v)", scope, e)
	}
	if k := scope.Sort[0]; k.Field != "balance" || k.Direction != "desc" || k.Chinese() != "应收余额 降序" || k.English() != "应收余额 descending" {
		t.Errorf("the first sort key: %+v %q %q", k, k.Chinese(), k.English())
	}
	if k := scope.Sort[1]; k.Field != "customer" || k.Direction != "asc" || k.Chinese() != "客户 升序" || k.English() != "客户 ascending" {
		t.Errorf("the second sort key: %+v %q %q", k, k.Chinese(), k.English())
	}
	for _, direction := range r.SortDirections {
		scope, e := present.ScopeOf(scoped(nil, []r.Sort{{Field: "due", Direction: direction}}))
		if e != nil || len(scope.Sort) != 1 || strings.HasSuffix(scope.Sort[0].Chinese(), " ") || strings.HasSuffix(scope.Sort[0].English(), " ") {
			t.Errorf("direction %s has no words: %+v (%v)", direction, scope, e)
		}
	}
}

// TestAValueThatCouldBeMisreadIsQuotedInThePDFsWords: the page has no cell that holds the exact value beside the words, so a value that
// begins or ends with white space or holds a mark the Chinese sentence uses is set between “ and ”, unchanged; any other value, and
// every value of the workbook's words (whose row holds the exact value), is as the query has it.
func TestAValueThatCouldBeMisreadIsQuotedInThePDFsWords(t *testing.T) {
	for _, c := range []struct {
		operator, value  string
		chinese, english string
	}{
		{"eq", " 甲方", "客户 等于 “ 甲方”", "客户 equals  甲方"},
		{"eq", "甲方 ", "客户 等于 “甲方 ”", "客户 equals 甲方 "},
		{"eq", "甲方\u3000", "客户 等于 “甲方\u3000”", "客户 equals 甲方\u3000"},
		{"eq", "甲、乙", "客户 等于 “甲、乙”", "客户 equals 甲、乙"},
		{"ne", "甲；乙", "客户 不等于 “甲；乙”", "客户 does not equal 甲；乙"},
		{"contains", "“甲”", "客户 包含 ““甲””", "客户 contains “甲”"},
		{"in", `["甲、乙","丙"]`, "客户 属于 “甲、乙”、丙", "客户 is one of 甲、乙, 丙"},
		// 和 stands between the two values of a `between` only: a value that holds it is quoted there and nowhere else.
		{"between", `["和平","和记"]`, "客户 介于 “和平” 和 “和记” 之间", "客户 is between 和平 and 和记"},
		{"eq", "和平", "客户 等于 和平", "客户 equals 和平"},
		// A space inside a value, a comma, a semicolon and quotation marks of another script are not the sentence's marks.
		{"eq", "ACME Trading, Ltd; \"east\"", "客户 等于 ACME Trading, Ltd; \"east\"", "客户 equals ACME Trading, Ltd; \"east\""},
		{"prefix", "1_30", "客户 开头为 1_30", "客户 starts with 1_30"},
	} {
		scope, e := present.ScopeOf(scoped([]r.Filter{{Field: "customer", Operator: c.operator, Value: c.value}}, nil))
		if e != nil || len(scope.Filters) != 1 {
			t.Errorf("%s %q: %v", c.operator, c.value, e)
			continue
		}
		if got := scope.Filters[0].Chinese(); got != c.chinese {
			t.Errorf("%s %q: Chinese %q, want %q", c.operator, c.value, got, c.chinese)
		}
		if got := scope.Filters[0].English(); got != c.english {
			t.Errorf("%s %q: English %q, want %q", c.operator, c.value, got, c.english)
		}
	}
}

// TestScopeOfReadsTheQueryInItsNormalizedOrder: the filters by field, operator and value whatever order the frozen query holds them in,
// the sort keys in their own order (order is meaning), the snapshot's query left as it was; a query without either has the empty scope.
func TestScopeOfReadsTheQueryInItsNormalizedOrder(t *testing.T) {
	filters := []r.Filter{
		{Field: "due", Operator: "ge", Value: "2026-01-01"},
		{Field: "customer", Operator: "in", Value: `["乙方","甲方"]`},
		{Field: "balance", Operator: "gt", Value: "1000.00"},
		{Field: "customer", Operator: "eq", Value: "甲方"},
		{Field: "balance", Operator: "between", Value: `["0.00","5.00"]`},
	}
	sort := []r.Sort{{Field: "due", Direction: "asc"}, {Field: "balance", Direction: "desc"}, {Field: "customer", Direction: "asc"}}
	s := scoped(slices.Clone(filters), slices.Clone(sort))
	scope, e := present.ScopeOf(s)
	if e != nil {
		t.Fatal(e)
	}
	var got []string
	for _, f := range scope.Filters {
		got = append(got, f.Chinese())
	}
	want := []string{"应收余额 介于 0.00 和 5.00 之间", "应收余额 大于 1000.00", "客户 等于 甲方", "客户 属于 乙方、甲方", "到期日 不小于 2026-01-01"}
	if !slices.Equal(got, want) {
		t.Errorf("filters %q, want %q", got, want)
	}
	// The values of an `in` keep the order the query gave them: they are the value, not a list the platform arranges.
	if f := scope.Filters[3]; f.Value != `["乙方","甲方"]` || !slices.Equal(f.Values, []string{"乙方", "甲方"}) {
		t.Errorf("the in filter: %+v", f)
	}
	got = nil
	for _, k := range scope.Sort {
		got = append(got, k.Chinese())
	}
	if want := []string{"到期日 升序", "应收余额 降序", "客户 升序"}; !slices.Equal(got, want) {
		t.Errorf("sort %q, want %q", got, want)
	}
	if !slices.Equal(s.Query.Filters, filters) || !slices.Equal(s.Query.Sort, sort) {
		t.Error("ScopeOf changed the snapshot's query")
	}
	// Reading it again gives the same scope.
	again, e := present.ScopeOf(s)
	if e != nil || len(again.Filters) != len(scope.Filters) || again.Filters[0].Chinese() != scope.Filters[0].Chinese() {
		t.Errorf("a second reading: %+v (%v)", again, e)
	}
	for name, s := range map[string]r.Snapshot{"nil lists": scoped(nil, nil), "empty lists": scoped([]r.Filter{}, []r.Sort{}), "no columns": {}} {
		if scope, e := present.ScopeOf(s); e != nil || scope.Filters != nil || scope.Sort != nil {
			t.Errorf("%s: %+v (%v), want the empty scope", name, scope, e)
		}
	}
	// The bounds of a query are a scope the exports say whole: 16 filters of 32 values of 128 characters, 3 sort keys.
	value := `["` + strings.Join(slices.Repeat([]string{strings.Repeat("值", r.MaxFilterValueChars)}, r.MaxInValues), `","`) + `"]`
	full := scoped(slices.Repeat([]r.Filter{{Field: "customer", Operator: "in", Value: value}}, r.MaxQueryFilters), sort)
	if scope, e := present.ScopeOf(full); e != nil || len(scope.Filters) != r.MaxQueryFilters || len(scope.Filters[15].Values) != r.MaxInValues || len(scope.Sort) != r.MaxQuerySortKeys {
		t.Errorf("a query at its bounds: %d filters, %d sort keys (%v)", len(scope.Filters), len(scope.Sort), e)
	}
}

// TestScopeOfRefusesWhatAFrozenQueryCannotHold: the query was validated when the snapshot was admitted, so a frozen query that does not
// fit the frozen columns by the core's rules is an integrity failure and no export says any of it; the error carries no field and no
// value.
func TestScopeOfRefusesWhatAFrozenQueryCannotHold(t *testing.T) {
	filter := func(field, operator, value string) r.Snapshot {
		return scoped([]r.Filter{{Field: field, Operator: operator, Value: value}}, nil)
	}
	order := func(keys ...r.Sort) r.Snapshot { return scoped(nil, keys) }
	unlabelled := filter("customer", "eq", "secret-value")
	unlabelled.Result.Columns[0].Label = ""
	unlabelledSort := order(r.Sort{Field: "customer", Direction: "asc"})
	unlabelledSort.Result.Columns[0].Label = ""
	for name, s := range map[string]r.Snapshot{
		"a field that is no column":         filter("secret-field", "eq", "secret-value"),
		"an operator outside the set":       filter("customer", "like", "secret-value"),
		"an empty operator":                 filter("customer", "", "secret-value"),
		"prefix on a decimal":               filter("balance", "prefix", "1"),
		"contains on a date":                filter("due", "contains", "2026"),
		"an order on a boolean":             filter("overdue", "lt", "true"),
		"in on a boolean":                   filter("overdue", "in", `["true"]`),
		"an in that is not a list":          filter("customer", "in", "secret-value"),
		"an in of no values":                filter("customer", "in", `[]`),
		"an in of 33 values":                filter("customer", "in", `["`+strings.Join(slices.Repeat([]string{"v"}, r.MaxInValues+1), `","`)+`"]`),
		"an in of numbers":                  filter("count", "in", `[1,2]`),
		"a between of three":                filter("count", "between", `["1","2","3"]`),
		"a between of one":                  filter("count", "between", `["1"]`),
		"a between out of order":            filter("count", "between", `["10","9"]`),
		"a decimal of another scale":        filter("balance", "gt", "1000"),
		"a grouped number":                  filter("balance", "gt", "1,000.00"),
		"an integer with a fraction":        filter("count", "eq", "1.0"),
		"a date that is not one":            filter("due", "eq", "2026-13-01"),
		"a boolean that is not one":         filter("overdue", "eq", "yes"),
		"an empty string":                   filter("customer", "eq", ""),
		"a string over the bound":           filter("customer", "eq", strings.Repeat("长", r.MaxFilterValueChars+1)),
		"a string with a line break":        filter("customer", "eq", "secret\nvalue"),
		"a malformed value in a list":       filter("balance", "in", `["0.00","secret-value"]`),
		"a column without a label":          unlabelled,
		"17 filters":                        scoped(slices.Repeat([]r.Filter{{Field: "customer", Operator: "eq", Value: "v"}}, r.MaxQueryFilters+1), nil),
		"a sort field that is no column":    order(r.Sort{Field: "secret-field", Direction: "asc"}),
		"a direction outside the set":       order(r.Sort{Field: "customer", Direction: "up"}),
		"an empty direction":                order(r.Sort{Field: "customer"}),
		"a sort field twice":                order(r.Sort{Field: "customer", Direction: "asc"}, r.Sort{Field: "customer", Direction: "desc"}),
		"four sort keys":                    order(r.Sort{Field: "customer", Direction: "asc"}, r.Sort{Field: "due", Direction: "asc"}, r.Sort{Field: "count", Direction: "asc"}, r.Sort{Field: "balance", Direction: "asc"}),
		"a sort on a column without label":  unlabelledSort,
		"a filter that fits beside one bad": scoped([]r.Filter{{Field: "customer", Operator: "eq", Value: "v"}, {Field: "balance", Operator: "gt", Value: "secret-value"}}, nil),
	} {
		scope, e := present.ScopeOf(s)
		if !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("%s: %v, want an integrity failure", name, e)
			continue
		}
		if scope.Filters != nil || scope.Sort != nil {
			t.Errorf("%s: a part of the scope was returned: %+v", name, scope)
		}
		if strings.Contains(e.Error(), "secret") {
			t.Errorf("%s: the error carries a field or a value: %v", name, e)
		}
	}
}
