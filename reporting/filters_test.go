package reporting_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// typed is a dataset with a column of every type: the dimensions k (string), d (date) and b (boolean), the metrics n (integer) and m
// (decimal, scale 2), and a declaration that lets every operator be tried on the type it fits.
func typed() r.Dataset {
	return r.Dataset{
		Ref:        r.Ref{ID: "typed", Version: "1"},
		Dimensions: []string{"k", "d", "b"},
		Metrics:    []r.Ref{{ID: "n", Version: "1"}, {ID: "m", Version: "1"}},
		Timezone:   "UTC",
		Columns: []r.Column{{ID: "k", Label: "K", Type: "string"}, {ID: "d", Label: "D", Type: "date"}, {ID: "b", Label: "B", Type: "boolean"},
			{ID: "n", Label: "N", Type: "integer"}, {ID: "m", Label: "M", Type: "decimal", Scale: 2}},
		Filters: []r.FilterSpec{
			{Field: "k", Operators: []string{"eq", "ne", "lt", "le", "gt", "ge", "in", "between", "prefix", "contains"}},
			{Field: "d", Operators: []string{"eq", "ne", "lt", "le", "gt", "ge", "in", "between"}},
			{Field: "b", Operators: []string{"eq", "ne"}},
			{Field: "n", Operators: []string{"eq", "ne", "lt", "le", "gt", "ge", "in", "between"}},
			{Field: "m", Operators: []string{"eq", "ne", "lt", "le", "gt", "ge", "in", "between"}},
		},
		Sorts: []r.SortSpec{{Field: "k", Directions: []string{"asc", "desc"}}, {Field: "m", Directions: []string{"desc"}}, {Field: "d", Directions: []string{"asc"}}},
	}
}

// queryOf is a well-formed query of the dataset without a filter or a sort.
func queryOf(d r.Dataset) r.QuerySpec {
	return r.QuerySpec{SchemaVersion: "1.0", Integration: "it", Dataset: d.Ref, Companies: []string{"c1"}, Metrics: d.Metrics, Dimensions: d.Dimensions, Filters: []r.Filter{}, Sort: []r.Sort{}, AsOf: "2026-10-26", Timezone: d.Timezone, CurrencyPolicy: d.CurrencyPolicy, Page: r.PageSpec{Limit: 50}}
}

func filtered(d r.Dataset, filters ...r.Filter) r.QuerySpec {
	q := queryOf(d)
	q.Filters = filters
	return q
}

func sorted(d r.Dataset, keys ...r.Sort) r.QuerySpec {
	q := queryOf(d)
	q.Sort = keys
	return q
}

func wantErr(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) && !(got == nil && want == nil) {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func TestTheDeclarationIsValidatedLikeAColumn(t *testing.T) {
	good := typed()
	if e := r.ValidateQuery(queryOf(good), good); e != nil {
		t.Fatal(e)
	}
	// A dataset without the lists is what it always was: valid, and a query with a filter or a sort is unsupported.
	plain := typed()
	plain.Filters, plain.Sorts = nil, nil
	wantErr(t, "plain", r.ValidateQuery(queryOf(plain), plain), nil)
	wantErr(t, "plain + filter", r.ValidateQuery(filtered(plain, r.Filter{Field: "k", Operator: "eq", Value: "a"}), plain), r.ErrUnsupported)
	wantErr(t, "plain + sort", r.ValidateQuery(sorted(plain, r.Sort{Field: "k", Direction: "asc"}), plain), r.ErrUnsupported)

	many := func(n int, field string, ops []string) []r.FilterSpec {
		out := []r.FilterSpec{}
		for i := 0; i < n; i++ {
			out = append(out, r.FilterSpec{Field: fmt.Sprint(field, i), Operators: ops})
		}
		return out
	}
	manySorts := func(n int) []r.SortSpec {
		out := []r.SortSpec{}
		for i := 0; i < n; i++ {
			out = append(out, r.SortSpec{Field: fmt.Sprint("x", i), Directions: []string{"asc", "desc"}})
		}
		return out
	}
	wide := typed()
	for i := 0; i < 17; i++ {
		wide.Columns = append(wide.Columns, r.Column{ID: fmt.Sprint("x", i), Label: "X", Type: "integer"})
		wide.Metrics = append(wide.Metrics, r.Ref{ID: fmt.Sprint("x", i), Version: "1"})
	}
	bad := map[string]func(d *r.Dataset){
		"17 filters":                         func(d *r.Dataset) { d.Filters = many(17, "x", []string{"eq"}) },
		"9 sorts":                            func(d *r.Dataset) { d.Sorts = manySorts(9) },
		"a filter on a column that is not":   func(d *r.Dataset) { d.Filters[0].Field = "nope" },
		"a filter field twice":               func(d *r.Dataset) { d.Filters[1].Field = "k" },
		"no operator":                        func(d *r.Dataset) { d.Filters[0].Operators = []string{} },
		"an operator outside the set":        func(d *r.Dataset) { d.Filters[0].Operators = []string{"like"} },
		"an operator twice":                  func(d *r.Dataset) { d.Filters[0].Operators = []string{"eq", "eq"} },
		"prefix on a date":                   func(d *r.Dataset) { d.Filters[1].Operators = []string{"prefix"} },
		"contains on an integer":             func(d *r.Dataset) { d.Filters[3].Operators = []string{"contains"} },
		"lt on a boolean":                    func(d *r.Dataset) { d.Filters[2].Operators = []string{"lt"} },
		"between on a boolean":               func(d *r.Dataset) { d.Filters[2].Operators = []string{"between"} },
		"in on a boolean":                    func(d *r.Dataset) { d.Filters[2].Operators = []string{"in"} },
		"65 closed values":                   func(d *r.Dataset) { d.Filters[0].Values = values(65) },
		"a closed value twice":               func(d *r.Dataset) { d.Filters[0].Values = []string{"a", "a"} },
		"an empty closed value":              func(d *r.Dataset) { d.Filters[0].Values = []string{""} },
		"a closed value of 129 characters":   func(d *r.Dataset) { d.Filters[0].Values = []string{strings.Repeat("字", 129)} },
		"a closed value with a control char": func(d *r.Dataset) { d.Filters[0].Values = []string{"a\tb"} },
		"a closed value with a C1 control":   func(d *r.Dataset) { d.Filters[0].Values = []string{"a\u0085b"} },
		"a closed value that is not a date":  func(d *r.Dataset) { d.Filters[1].Values = []string{"2026-1-5"} },
		"a closed value of another scale":    func(d *r.Dataset) { d.Filters[4].Values = []string{"1.5"} },
		"a closed value that is not a bool":  func(d *r.Dataset) { d.Filters[2].Values = []string{"yes"} },
		"a sort on a column that is not":     func(d *r.Dataset) { d.Sorts[0].Field = "nope" },
		"a sort field twice":                 func(d *r.Dataset) { d.Sorts[1].Field = "k" },
		"no direction":                       func(d *r.Dataset) { d.Sorts[0].Directions = nil },
		"a direction that is not":            func(d *r.Dataset) { d.Sorts[0].Directions = []string{"up"} },
		"a direction twice":                  func(d *r.Dataset) { d.Sorts[0].Directions = []string{"asc", "asc"} },
		"a sixth filter on a column that is not": func(d *r.Dataset) {
			d.Filters = append(d.Filters, r.FilterSpec{Field: "zz", Operators: []string{"eq"}, Required: true})
		},
	}
	for name, change := range bad {
		d := wide
		d.Filters, d.Sorts = slices.Clone(wide.Filters), slices.Clone(wide.Sorts)
		for i := range d.Filters {
			d.Filters[i].Operators = slices.Clone(wide.Filters[i].Operators)
		}
		for i := range d.Sorts {
			d.Sorts[i].Directions = slices.Clone(wide.Sorts[i].Directions)
		}
		change(&d)
		if e := r.ValidateQuery(queryOf(d), d); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("%s: got %v, want INTEGRITY_FAILURE", name, e)
		}
	}
	// At the bounds the declaration is accepted: 16 filters, 8 sorts, 64 closed values of 128 characters.
	full := wide
	full.Filters = many(16, "x", []string{"eq"})
	full.Filters[0].Values = values(64)
	full.Sorts = manySorts(8)
	long := typed()
	long.Filters[0].Values = []string{strings.Repeat("字", 128)}
	for name, d := range map[string]r.Dataset{"full": full, "long": long} {
		if e := r.ValidateQuery(queryOf(d), d); e != nil {
			t.Errorf("%s: %v", name, e)
		}
	}
}

func values(n int) []string {
	out := []string{}
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprint(i))
	}
	return out
}

func TestEveryOperatorAndTypeTakesWellFormedValuesOnly(t *testing.T) {
	d := typed()
	good := map[string][]r.Filter{
		"k": {{Field: "k", Operator: "eq", Value: "a"}, {Field: "k", Operator: "ne", Value: strings.Repeat("字", 128)}, {Field: "k", Operator: "lt", Value: "b"}, {Field: "k", Operator: "le", Value: "b"}, {Field: "k", Operator: "gt", Value: "b"}, {Field: "k", Operator: "ge", Value: "b"},
			{Field: "k", Operator: "in", Value: `["a","b"]`}, {Field: "k", Operator: "in", Value: `["a"]`}, {Field: "k", Operator: "in", Value: inOf(32)}, {Field: "k", Operator: "between", Value: `["a","b"]`}, {Field: "k", Operator: "between", Value: `["a","a"]`},
			{Field: "k", Operator: "prefix", Value: "cust"}, {Field: "k", Operator: "contains", Value: "ü"}},
		"d": {{Field: "d", Operator: "eq", Value: "2026-10-26"}, {Field: "d", Operator: "lt", Value: "2026-01-01"}, {Field: "d", Operator: "in", Value: `["2026-01-01","2026-02-01"]`}, {Field: "d", Operator: "between", Value: `["2026-01-01","2026-12-31"]`}},
		"b": {{Field: "b", Operator: "eq", Value: "true"}, {Field: "b", Operator: "ne", Value: "false"}},
		"n": {{Field: "n", Operator: "eq", Value: "0"}, {Field: "n", Operator: "ge", Value: "-9007199254740993"}, {Field: "n", Operator: "in", Value: `["1","2","3"]`}, {Field: "n", Operator: "between", Value: `["-5","10"]`}, {Field: "n", Operator: "between", Value: `["9","10"]`}, {Field: "n", Operator: "lt", Value: strings.Repeat("9", 128)}},
		"m": {{Field: "m", Operator: "eq", Value: "100.25"}, {Field: "m", Operator: "lt", Value: "-0.24"}, {Field: "m", Operator: "between", Value: `["-0.24","9007199254740893.00"]`}, {Field: "m", Operator: "in", Value: `["0.00"]`}},
	}
	for field, filters := range good {
		for _, f := range filters {
			if e := r.ValidateQuery(filtered(d, f), d); e != nil {
				t.Errorf("%s %s %q: %v", field, f.Operator, f.Value, e)
			}
		}
	}
	bad := []r.Filter{
		{Field: "k", Operator: "eq", Value: ""}, {Field: "k", Operator: "eq", Value: strings.Repeat("字", 129)}, {Field: "k", Operator: "eq", Value: "a\nb"}, {Field: "k", Operator: "eq", Value: "a\x00"}, {Field: "k", Operator: "prefix", Value: "\x7f"}, {Field: "k", Operator: "eq", Value: "\xff"},
		{Field: "k", Operator: "in", Value: `[]`}, {Field: "k", Operator: "in", Value: inOf(33)}, {Field: "k", Operator: "in", Value: `"a"`}, {Field: "k", Operator: "in", Value: `a`}, {Field: "k", Operator: "in", Value: `[1]`}, {Field: "k", Operator: "in", Value: `[null]`}, {Field: "k", Operator: "in", Value: `["a",["b"]]`}, {Field: "k", Operator: "in", Value: `{"a":"b"}`}, {Field: "k", Operator: "in", Value: `null`}, {Field: "k", Operator: "in", Value: `["a"] x`}, {Field: "k", Operator: "in", Value: `[""]`},
		{Field: "k", Operator: "between", Value: `["a"]`}, {Field: "k", Operator: "between", Value: `["a","b","c"]`}, {Field: "k", Operator: "between", Value: `["b","a"]`}, {Field: "k", Operator: "between", Value: `a,b`},
		{Field: "d", Operator: "eq", Value: "2026-1-5"}, {Field: "d", Operator: "eq", Value: "2026-13-01"}, {Field: "d", Operator: "eq", Value: "20261026"}, {Field: "d", Operator: "between", Value: `["2026-12-31","2026-01-01"]`}, {Field: "d", Operator: "eq", Value: ""},
		{Field: "b", Operator: "eq", Value: "yes"}, {Field: "b", Operator: "eq", Value: "True"}, {Field: "b", Operator: "eq", Value: "1"},
		{Field: "n", Operator: "eq", Value: "1.0"}, {Field: "n", Operator: "eq", Value: "01"}, {Field: "n", Operator: "eq", Value: "1e3"}, {Field: "n", Operator: "eq", Value: " 1"}, {Field: "n", Operator: "eq", Value: strings.Repeat("9", 129)}, {Field: "n", Operator: "between", Value: `["10","9"]`}, {Field: "n", Operator: "between", Value: `["10","-9"]`},
		{Field: "m", Operator: "eq", Value: "100"}, {Field: "m", Operator: "eq", Value: "100.5"}, {Field: "m", Operator: "eq", Value: "100.255"}, {Field: "m", Operator: "eq", Value: "abc"}, {Field: "m", Operator: "between", Value: `["0.01","0.00"]`}, {Field: "m", Operator: "between", Value: `["0.00","-0.01"]`},
	}
	for _, f := range bad {
		if e := r.ValidateQuery(filtered(d, f), d); !errors.Is(e, r.ErrUnsupported) {
			t.Errorf("%s %s %q: got %v, want UNSUPPORTED_COMBINATION", f.Field, f.Operator, f.Value, e)
		}
	}
	// A string column's 128 characters are characters, not bytes: 128 CJK characters pass, 129 do not; a 129th byte of ASCII does not either.
	if e := r.ValidateQuery(filtered(d, r.Filter{Field: "k", Operator: "eq", Value: strings.Repeat("a", 129)}), d); !errors.Is(e, r.ErrUnsupported) {
		t.Error(e)
	}
}

func inOf(n int) string {
	return `["` + strings.Join(values(n), `","`) + `"]`
}

func TestAQueryIsHeldToTheDeclaration(t *testing.T) {
	d := typed()
	cases := map[string]struct {
		q    r.QuerySpec
		want error
	}{
		"16 filters":                  {filtered(d, repeatFilter(16, r.Filter{Field: "k", Operator: "eq", Value: "a"})...), nil},
		"17 filters":                  {filtered(d, repeatFilter(17, r.Filter{Field: "k", Operator: "eq", Value: "a"})...), r.ErrUnsupported},
		"an undeclared field":         {filtered(d, r.Filter{Field: "zz", Operator: "eq", Value: "a"}), r.ErrUnsupported},
		"an undeclared operator":      {filtered(d, r.Filter{Field: "b", Operator: "lt", Value: "true"}), r.ErrUnsupported},
		"an operator outside the set": {filtered(d, r.Filter{Field: "k", Operator: "like", Value: "a%"}), r.ErrUnsupported},
		"an empty operator":           {filtered(d, r.Filter{Field: "k", Operator: "", Value: "a"}), r.ErrUnsupported},
		"a filter twice":              {filtered(d, r.Filter{Field: "k", Operator: "eq", Value: "a"}, r.Filter{Field: "k", Operator: "eq", Value: "a"}), nil},
		"two filters on one field":    {filtered(d, r.Filter{Field: "m", Operator: "ge", Value: "0.00"}, r.Filter{Field: "m", Operator: "lt", Value: "100.00"}), nil},
		"three sort keys":             {sorted(d, r.Sort{Field: "k", Direction: "desc"}, r.Sort{Field: "m", Direction: "desc"}, r.Sort{Field: "d", Direction: "asc"}), nil},
		"four sort keys":              {sorted(d, r.Sort{Field: "k", Direction: "desc"}, r.Sort{Field: "m", Direction: "desc"}, r.Sort{Field: "d", Direction: "asc"}, r.Sort{Field: "k", Direction: "asc"}), r.ErrUnsupported},
		"a sort field twice":          {sorted(d, r.Sort{Field: "k", Direction: "asc"}, r.Sort{Field: "k", Direction: "desc"}), r.ErrUnsupported},
		"an undeclared sort field":    {sorted(d, r.Sort{Field: "n", Direction: "asc"}), r.ErrUnsupported},
		"an undeclared direction":     {sorted(d, r.Sort{Field: "m", Direction: "asc"}), r.ErrUnsupported},
		"a direction that is not":     {sorted(d, r.Sort{Field: "k", Direction: "up"}), r.ErrUnsupported},
		"a filter and a sort":         {withSort(filtered(d, r.Filter{Field: "k", Operator: "prefix", Value: "cu"}), r.Sort{Field: "m", Direction: "desc"}), nil},
	}
	for name, c := range cases {
		wantErr(t, name, r.ValidateQuery(c.q, d), c.want)
	}
	// A column the version declares no filter on takes none, whatever its type.
	partial := noFilterOn(d, "b")
	wantErr(t, "a column without a declaration", r.ValidateQuery(filtered(partial, r.Filter{Field: "b", Operator: "eq", Value: "true"}), partial), r.ErrUnsupported)

	// Closed values: eq, ne and in take one of them; a bound does not have to.
	closed := typed()
	closed.Filters[0].Values = []string{"current", "1_30", "31_60"}
	for name, c := range map[string]struct {
		f    r.Filter
		want error
	}{
		"eq a closed value":         {r.Filter{Field: "k", Operator: "eq", Value: "1_30"}, nil},
		"eq another value":          {r.Filter{Field: "k", Operator: "eq", Value: "61_90"}, r.ErrUnsupported},
		"ne another value":          {r.Filter{Field: "k", Operator: "ne", Value: "61_90"}, r.ErrUnsupported},
		"in closed values":          {r.Filter{Field: "k", Operator: "in", Value: `["current","31_60"]`}, nil},
		"in with one outside":       {r.Filter{Field: "k", Operator: "in", Value: `["current","61_90"]`}, r.ErrUnsupported},
		"a bound outside the list":  {r.Filter{Field: "k", Operator: "lt", Value: "zzz"}, nil},
		"a prefix outside the list": {r.Filter{Field: "k", Operator: "prefix", Value: "cu"}, nil},
	} {
		wantErr(t, name, r.ValidateQuery(filtered(closed, c.f), closed), c.want)
	}

	// A required filter is present exactly once.
	required := typed()
	required.Filters[1].Required = true
	period := r.Filter{Field: "d", Operator: "between", Value: `["2026-01-01","2026-01-31"]`}
	wantErr(t, "required present", r.ValidateQuery(filtered(required, period), required), nil)
	wantErr(t, "required missing", r.ValidateQuery(queryOf(required), required), r.ErrUnsupported)
	wantErr(t, "required twice", r.ValidateQuery(filtered(required, period, r.Filter{Field: "d", Operator: "eq", Value: "2026-01-05"}), required), r.ErrUnsupported)
	wantErr(t, "required missing, another present", r.ValidateQuery(filtered(required, r.Filter{Field: "k", Operator: "eq", Value: "a"}), required), r.ErrUnsupported)
}

func repeatFilter(n int, f r.Filter) []r.Filter {
	out := []r.Filter{}
	for i := 0; i < n; i++ {
		out = append(out, f)
	}
	return out
}

func noFilterOn(d r.Dataset, field string) r.Dataset {
	d.Filters = slices.DeleteFunc(slices.Clone(d.Filters), func(f r.FilterSpec) bool { return f.Field == field })
	return d
}

func withSort(q r.QuerySpec, keys ...r.Sort) r.QuerySpec {
	q.Sort = keys
	return q
}

func TestNormalizeOrdersTheFiltersAndKeepsTheSortOrder(t *testing.T) {
	q := queryOf(typed())
	q.Filters = []r.Filter{{Field: "m", Operator: "lt", Value: "9.00"}, {Field: "k", Operator: "in", Value: `["b"]`}, {Field: "k", Operator: "eq", Value: "b"}, {Field: "k", Operator: "eq", Value: "a"}, {Field: "d", Operator: "ge", Value: "2026-01-01"}}
	q.Sort = []r.Sort{{Field: "m", Direction: "desc"}, {Field: "k", Direction: "asc"}}
	n := r.Normalize(q)
	want := []r.Filter{{Field: "d", Operator: "ge", Value: "2026-01-01"}, {Field: "k", Operator: "eq", Value: "a"}, {Field: "k", Operator: "eq", Value: "b"}, {Field: "k", Operator: "in", Value: `["b"]`}, {Field: "m", Operator: "lt", Value: "9.00"}}
	if !slices.Equal(n.Filters, want) {
		t.Fatalf("%+v", n.Filters)
	}
	if !slices.Equal(n.Sort, q.Sort) || n.Filters[0] == q.Filters[0] {
		t.Fatal("the sort keys were reordered, or the caller's filters were not left alone")
	}
	if q.Filters[0].Field != "m" {
		t.Fatal("Normalize changed the caller's slice")
	}
	// The order of the filters does not change the hash; the order of the sort keys does.
	other := q
	other.Filters = slices.Clone(want)
	if r.QueryHash(other) != r.QueryHash(q) {
		t.Fatal("the hash depends on the order of the filters")
	}
	other.Sort = []r.Sort{{Field: "k", Direction: "asc"}, {Field: "m", Direction: "desc"}}
	if r.QueryHash(other) == r.QueryHash(q) {
		t.Fatal("the hash does not depend on the order of the sort keys")
	}
	empty := r.Normalize(r.QuerySpec{})
	if empty.Filters == nil || empty.Sort == nil || len(empty.Filters) != 0 {
		t.Fatal("nil lists stay nil")
	}
}

func TestTheQueryHashCoversTheFiltersAndIsUnchangedWithoutThem(t *testing.T) {
	// The hash of the unfiltered demo query is what it was before filters existed (the value the protocol example quotes).
	if h := r.QueryHash(contractsim.FinancialQuery()); h != "57ef45fcb414e8b9d4a75b7a83a6046a8c61216d35f8e1a0bca4dbafe9f33d25" {
		t.Fatal(h)
	}
	if h := r.QueryHash(contractsim.ShipmentQuery()); h != "cc14612827be9740252c036baed6ae88e7b436c741e91271b9b8edc22ab9a660" {
		t.Fatal(h)
	}
	nilLists := contractsim.FinancialQuery()
	nilLists.Filters, nilLists.Sort = nil, nil
	if r.QueryHash(nilLists) != r.QueryHash(contractsim.FinancialQuery()) {
		t.Fatal("nil and empty lists hash differently")
	}
	a := contractsim.FinancialQuery()
	a.Filters = []r.Filter{{Field: "customer.id", Operator: "eq", Value: "customer_a"}}
	b := contractsim.FinancialQuery()
	b.Filters = []r.Filter{{Field: "customer.id", Operator: "eq", Value: "customer_b"}}
	c := contractsim.FinancialQuery()
	c.Sort = []r.Sort{{Field: "ar.open_balance", Direction: "desc"}}
	hashes := map[string]bool{r.QueryHash(contractsim.FinancialQuery()): true, r.QueryHash(a): true, r.QueryHash(b): true, r.QueryHash(c): true}
	if len(hashes) != 4 {
		t.Fatal("two queries differing only in a filter or a sort share a hash")
	}
	// The page is not in the hash, as before.
	a.Page.Limit = 1
	if r.QueryHash(a) != r.QueryHash(func() r.QuerySpec { x := a; x.Page.Limit = 100; return x }()) {
		t.Fatal("the page is in the hash")
	}
}

func TestFilterValuesReadsTheOneFormThePlatformValidates(t *testing.T) {
	for _, c := range []struct {
		f    r.Filter
		want []string
	}{
		{r.Filter{Operator: "eq", Value: "a"}, []string{"a"}},
		{r.Filter{Operator: "prefix", Value: `["not","decoded"]`}, []string{`["not","decoded"]`}},
		{r.Filter{Operator: "in", Value: `["a","b"]`}, []string{"a", "b"}},
		{r.Filter{Operator: "in", Value: ` [ "a" , "b" ] `}, []string{"a", "b"}},
		{r.Filter{Operator: "in", Value: `["\u5b57","\"q\""]`}, []string{"字", `"q"`}},
		{r.Filter{Operator: "between", Value: `["1","2"]`}, []string{"1", "2"}},
	} {
		got, e := r.FilterValues(c.f)
		if e != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s %q: %q %v", c.f.Operator, c.f.Value, got, e)
		}
	}
	for _, v := range []string{``, `a`, `"a"`, `[]`, `[1]`, `[null]`, `null`, `{}`, `[["a"]]`, `["a",]`, `["a"]]`, `["a"] ["b"]`, inOf(33), `["a","b","c"]`} {
		op := "in"
		if v == `["a","b","c"]` {
			op = "between"
		}
		if _, e := r.FilterValues(r.Filter{Operator: op, Value: v}); !errors.Is(e, r.ErrUnsupported) {
			t.Errorf("%s %q: %v", op, v, e)
		}
	}
	if _, e := r.FilterValues(r.Filter{Operator: "in", Value: `["` + strings.Repeat("a", r.MaxFilterValueBytes) + `"]`}); !errors.Is(e, r.ErrUnsupported) {
		t.Error("a Value over the byte bound was decoded")
	}
}

// TestTheServiceRunsAFilteredAndSortedQuery: the service validates a query's filters and sort against the catalogue, the source applies
// them, and the result's rows and totals are the narrowed scope's; a frozen snapshot carries them in its query and hash; what the
// catalogue does not declare is unsupported before the source is asked.
func TestTheServiceRunsAFilteredAndSortedQuery(t *testing.T) {
	ctx := context.Background()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	q := contractsim.FinancialQuery()
	q.Filters = []r.Filter{{Field: "ar.open_balance", Operator: "ge", Value: "0.00"}}
	q.Sort = []r.Sort{{Field: "ar.open_balance", Direction: "desc"}}
	res, err := s.Query(ctx, p, q)
	if err != nil || len(res.Rows) != 2 || res.Rows[0][0] != "customer_c" || res.Rows[1][0] != "customer_a" || res.Totals["ar.open_balance"] != "9007199254740993.25" || res.Evidence.QueryHash != r.QueryHash(q) || res.Evidence.QueryHash == r.QueryHash(contractsim.FinancialQuery()) {
		t.Fatalf("%+v %v", res, err)
	}
	job, err := s.SubmitSnapshot(ctx, p, q, "narrowed")
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := s.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	job, err = s.Job(ctx, p, job.ID)
	if err != nil || job.State != "succeeded" {
		t.Fatal(job, err)
	}
	page, err := s.Rows(ctx, p, job.SnapshotID, 10, "")
	if err != nil || page.TotalRows != 2 || page.Rows[0][2] != "9007199254740893.00" || page.Rows[1][2] != "100.25" || page.Totals["ar.open_balance"] != "9007199254740993.25" || page.Evidence.QueryHash != r.QueryHash(q) {
		t.Fatalf("%+v %v", page, err)
	}
	snap, err := s.AuthorizedSnapshot(ctx, p, job.SnapshotID)
	if err != nil || !slices.Equal(snap.Query.Filters, q.Filters) || !slices.Equal(snap.Query.Sort, q.Sort) {
		t.Fatalf("%+v %v", snap.Query, err)
	}
	// What the catalogue does not declare never reaches the source.
	reads := a.ReadCalls()
	for name, change := range map[string]func(q *r.QuerySpec){
		"an undeclared field": func(q *r.QuerySpec) {
			q.Filters = []r.Filter{{Field: "aging.bucket", Operator: "prefix", Value: "cur"}}
		},
		"a value outside the list": func(q *r.QuerySpec) { q.Filters = []r.Filter{{Field: "aging.bucket", Operator: "eq", Value: "91+"}} },
		"a malformed decimal":      func(q *r.QuerySpec) { q.Filters = []r.Filter{{Field: "ar.open_balance", Operator: "ge", Value: "0"}} },
		"an undeclared sort":       func(q *r.QuerySpec) { q.Sort = []r.Sort{{Field: "aging.bucket", Direction: "asc"}} },
	} {
		bad := contractsim.FinancialQuery()
		change(&bad)
		if _, err := s.Query(ctx, p, bad); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := s.SubmitSnapshot(ctx, p, bad, "k-"+name); !errors.Is(err, r.ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if a.ReadCalls() != reads {
		t.Fatal("a refused query read the source")
	}
}

func TestCompareValuesIsExactForNumbersAndTextualOtherwise(t *testing.T) {
	dec := r.Column{Type: "decimal", Scale: 2}
	integer := r.Column{Type: "integer"}
	for _, c := range []struct {
		col  r.Column
		a, b string
		want int
	}{
		{dec, "9007199254740893.00", "9007199254740892.99", 1}, {dec, "-0.24", "0.00", -1}, {dec, "100.25", "100.25", 0}, {dec, "-1.00", "-2.00", 1}, {dec, "9.99", "10.00", -1},
		{integer, "9007199254740993", "9007199254740992", 1}, {integer, "-5", "3", -1}, {integer, "10", "9", 1}, {integer, "0", "-0", 0},
		{r.Column{Type: "date"}, "2026-01-31", "2026-02-01", -1}, {r.Column{Type: "string"}, "b", "a", 1}, {r.Column{Type: "string"}, "a", "a", 0},
		{r.Column{Type: "boolean"}, "false", "true", -1}, {r.Column{Type: "boolean"}, "true", "true", 0},
	} {
		if got := r.CompareValues(c.col, c.a, c.b); (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("%s %q %q: %d, want %d", c.col.Type, c.a, c.b, got, c.want)
		}
	}
	if !r.OperatorFits("between", "date") || r.OperatorFits("between", "boolean") || r.OperatorFits("prefix", "integer") || !r.OperatorFits("eq", "boolean") || r.OperatorFits("in", "boolean") || r.OperatorFits("eq", "float") || r.OperatorFits("like", "string") {
		t.Fatal("OperatorFits")
	}
}
