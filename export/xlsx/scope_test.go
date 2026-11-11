package xlsx_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
)

// narrowed is the ledger fixture as a snapshot whose frozen query carries three filters, in an order that is not the normalized one,
// and two sort keys. The rows are the fixture's: a writer says the scope and never applies it.
func narrowed() r.Snapshot {
	s := ledger()
	s.Query.Filters = []r.Filter{
		{Field: "year", Operator: "between", Value: `["2025","2026"]`},
		{Field: "customer", Operator: "in", Value: `["甲","乙","=SUM(A1)"]`},
		{Field: "amount", Operator: "gt", Value: "-1000000.00"},
	}
	s.Query.Sort = []r.Sort{{Field: "amount", Direction: "desc"}, {Field: "customer", Direction: "asc"}}
	return s
}

// scopeRows is the Metadata sheet from the scope row on, each row's cells joined by "|".
func scopeRows(t *testing.T, f *excelize.File) []string {
	t.Helper()
	rows, e := f.GetRows("Metadata", excelize.Options{RawCellValue: true})
	if e != nil {
		t.Fatal(e)
	}
	for i, row := range rows {
		if len(row) > 1 && row[0] == xlsx.ScopeKey && row[1] == xlsx.ScopeFilters {
			var out []string
			for _, row := range rows[i:] {
				out = append(out, strings.Join(row, "|"))
			}
			return out
		}
	}
	t.Fatal("the Metadata sheet has no scope row")
	return nil
}

func TestTheMetadataSheetSaysTheScopeOfTheQuery(t *testing.T) {
	s := narrowed()
	b, e := xlsx.Writer{}.Write(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	f := open(t, b)
	// The scope row counts the filters and the sort keys; then a row per filter in the query's normalized order (by field, operator,
	// value) and a row per sort key in the order they apply: the words with the column's label, and beside them the field, the operator
	// and the value exactly as the query has them (the values of an `in` and a `between` as the JSON array the query carries).
	want := []string{
		`query_scope|filters|3|sort_keys|2`,
		`filter_1|金额 is greater than -1000000.00|amount|gt|-1000000.00`,
		`filter_2|客户 is one of 甲, 乙, =SUM(A1)|customer|in|["甲","乙","=SUM(A1)"]`,
		`filter_3|年份 is between 2025 and 2026|year|between|["2025","2026"]`,
		`sort_1|金额 descending|amount|desc`,
		`sort_2|客户 ascending|customer|asc`,
	}
	if got := scopeRows(t, f); !slices.Equal(got, want) {
		t.Errorf("the scope rows:\n %s\nwant\n %s", strings.Join(got, "\n "), strings.Join(want, "\n "))
	}
	// They follow the last row the sheet had before (the fixture's four columns end at row 22), every cell of them text: a count is
	// not a number cell, a value that looks like a number is not one, and a value that starts with "=" is no formula.
	if got := strings.Join(row(t, f, "Metadata", 23), "|"); got != want[0] {
		t.Errorf("row 23 = %q", got)
	}
	if got := row(t, f, "Metadata", 29); got != nil {
		t.Errorf("a row after the scope: %q", got)
	}
	for _, ref := range []string{"A23", "B23", "C23", "D23", "E23", "A24", "B24", "C24", "D24", "E24", "B25", "E25", "E26", "A27", "B27", "C27", "D27", "D28"} {
		if v, kind, st := cell(t, f, "Metadata", ref); v == "" || kind != excelize.CellTypeSharedString || numFmt(st) != "" {
			t.Errorf("Metadata!%s = %q, type %v: not a text cell", ref, v, kind)
		}
	}
	if xml := xmlOf(t, b); strings.Contains(xml, "<f>") || strings.Contains(xml, "<f ") {
		t.Error("a formula in the workbook")
	}
	// A writer says the scope and applies nothing: every sheet, width and cell before the scope row is the workbook of the same
	// snapshot without filters, fact for fact.
	whole, e := xlsx.Writer{}.Write(context.Background(), ledger())
	if e != nil {
		t.Fatal(e)
	}
	plain, scoped := cells(t, whole), cells(t, b)
	before := len(plain) - 5 // the unfiltered workbook's own scope row is its last five cells
	if len(scoped) <= before || !slices.Equal(scoped[:before], plain[:before]) {
		t.Error("the filters or the sort changed a cell outside the scope rows")
	}
	if bytes.Equal(whole, b) {
		t.Error("the workbook of a query with filters is the workbook of one without")
	}
	// The same input gives the same bytes.
	if again, e := (xlsx.Writer{}).Write(context.Background(), s); e != nil || !bytes.Equal(again, b) {
		t.Errorf("two renders differ (%v)", e)
	}
	// A presented export carries the same scope rows: the filters are the query's, whatever the layout shows (here it leaves out the
	// year the query filters by and relabels the amount; the words keep the dataset's label).
	presented, e := xlsx.Writer{}.WritePresented(context.Background(), s, ledgerPresentation())
	if e != nil {
		t.Fatal(e)
	}
	if got := scopeRows(t, open(t, presented)); !slices.Equal(got, want) {
		t.Errorf("the scope rows of the presented workbook:\n %s", strings.Join(got, "\n "))
	}
}

func TestAWorkbookOfAQueryWithoutFiltersCountsNone(t *testing.T) {
	b, e := xlsx.Writer{}.Write(context.Background(), ledger())
	if e != nil {
		t.Fatal(e)
	}
	f := open(t, b)
	// The scope row is in every workbook: "0" and "0" say that the query had no filter and no sort, which a workbook written before
	// S20-C (no such row) cannot say. Nothing follows it.
	if got := scopeRows(t, f); !slices.Equal(got, []string{`query_scope|filters|0|sort_keys|0`}) {
		t.Errorf("the scope rows: %q", got)
	}
	// One filter and no sort, no filter and one sort: each list on its own.
	s := ledger()
	s.Query.Filters = []r.Filter{{Field: "customer", Operator: "prefix", Value: "甲"}}
	if b, e = (xlsx.Writer{}).Write(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	if got := scopeRows(t, open(t, b)); !slices.Equal(got, []string{`query_scope|filters|1|sort_keys|0`, `filter_1|客户 starts with 甲|customer|prefix|甲`}) {
		t.Errorf("one filter: %q", got)
	}
	s = ledger()
	s.Query.Sort = []r.Sort{{Field: "year", Direction: "asc"}}
	if b, e = (xlsx.Writer{}).Write(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	if got := scopeRows(t, open(t, b)); !slices.Equal(got, []string{`query_scope|filters|0|sort_keys|1`, `sort_1|年份 ascending|year|asc`}) {
		t.Errorf("one sort key: %q", got)
	}
}

func TestAScopeTheWorkbookCannotSayIsRefused(t *testing.T) {
	// A frozen query that does not fit the frozen columns is an integrity failure: no workbook, with or without a presentation.
	for name, change := range map[string]func(*r.Snapshot){
		"a filter on no column": func(s *r.Snapshot) { s.Query.Filters = []r.Filter{{Field: "nope", Operator: "eq", Value: "x"}} },
		"an unknown operator":   func(s *r.Snapshot) { s.Query.Filters = []r.Filter{{Field: "customer", Operator: "like", Value: "x"}} },
		"a malformed value": func(s *r.Snapshot) {
			s.Query.Filters = []r.Filter{{Field: "amount", Operator: "gt", Value: "1,000.00"}}
		},
		"an unknown direction":   func(s *r.Snapshot) { s.Query.Sort = []r.Sort{{Field: "customer", Direction: "up"}} },
		"a sort key on no field": func(s *r.Snapshot) { s.Query.Sort = []r.Sort{{Field: "nope", Direction: "asc"}} },
	} {
		s := ledger()
		change(&s)
		if b, e := (xlsx.Writer{}).Write(context.Background(), s); !errors.Is(e, r.ErrIntegrity) || b != nil {
			t.Errorf("%s: %v, want an integrity failure and no file", name, e)
		}
		if b, e := (xlsx.Writer{}).WritePresented(context.Background(), s, ledgerPresentation()); !errors.Is(e, r.ErrIntegrity) || b != nil {
			t.Errorf("%s, presented: %v, want an integrity failure and no file", name, e)
		}
	}
	// The values of a query at its bounds fit their cells: 32 values of 128 characters as a list of words and as the JSON array, the
	// array even when every character is written as an escape (the longest text a value may be).
	long := `["` + strings.Join(slices.Repeat([]string{strings.Repeat("值", r.MaxFilterValueChars)}, r.MaxInValues), `","`) + `"]`
	escaped := `["` + strings.Join(slices.Repeat([]string{strings.Repeat(`\u503c`, r.MaxFilterValueChars)}, r.MaxInValues), `","`) + `"]`
	if len(escaped) != 24673 || r.MaxFilterValueBytes != 24674 {
		t.Fatalf("the escaped value is %d bytes, the bound %d", len(escaped), r.MaxFilterValueBytes)
	}
	s := ledger()
	s.Query.Filters = []r.Filter{{Field: "customer", Operator: "in", Value: long}, {Field: "customer", Operator: "in", Value: escaped}}
	b, e := xlsx.Writer{}.Write(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	rows := scopeRows(t, open(t, b))
	// The words are the same 32 values for both; the exact cell is each query's own text (the escaped one sorts first: the normalized
	// order compares the values as the query has them).
	words := "客户 is one of " + strings.Join(slices.Repeat([]string{strings.Repeat("值", r.MaxFilterValueChars)}, r.MaxInValues), ", ")
	if len(rows) != 3 || rows[1] != "filter_1|"+words+"|customer|in|"+escaped || rows[2] != "filter_2|"+words+"|customer|in|"+long {
		t.Errorf("a filter at the bounds of a query is not written whole (%d scope rows)", len(rows))
	}
	// A text longer than a cell holds is refused, never cut: only a column label beyond every sensible length makes one.
	s = ledger()
	s.Result.Columns[0].Label = strings.Repeat("长", excelize.TotalCellChars)
	s.Query.Filters = []r.Filter{{Field: "customer", Operator: "eq", Value: "甲"}}
	if b, e := (xlsx.Writer{}).Write(context.Background(), s); !errors.Is(e, r.ErrUnsupported) || b != nil {
		t.Errorf("words longer than a cell: %v, want unsupported and no file", e)
	}
	s.Query.Filters, s.Query.Sort = nil, []r.Sort{{Field: "customer", Direction: "asc"}}
	if b, e := (xlsx.Writer{}).Write(context.Background(), s); !errors.Is(e, r.ErrUnsupported) || b != nil {
		t.Errorf("sort words longer than a cell: %v, want unsupported and no file", e)
	}
}
