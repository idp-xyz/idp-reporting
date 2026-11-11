package present

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	r "idp.local/reporting/reporting"
)

// Scope is what a snapshot's query narrowed and ordered its rows by (S20-C, docs/ADR_0023_PUBLISHED_FILTERS_AND_SORTS.md decision 6): the
// filters of the frozen query, a conjunction, in the query's normalized order (by field, operator and value), and its sort keys in the
// order they apply. A snapshot made with filters is a narrowed scope and its totals are that scope's, so the exports say it: the workbook
// in a row per filter and sort key of its Metadata sheet, the PDF in words under its meta line. The scope is the query's, whatever layout
// the export is rendered through, and the exports only say it: the source applied it (ADR 0023 decision 4), the platform evaluates nothing.
type Scope struct {
	Filters []ScopeFilter
	Sort    []ScopeSort
}

// ScopeFilter is one filter of the query: the field, the operator and the value exactly as the query has them (Value is the JSON array of
// strings of an `in` or a `between`, as text), the values that Value carries (reporting.FilterValues, the one decoding of it: one for a
// scalar operator, 1..32 for `in`, two for `between`) and the label of the snapshot's column the field names.
type ScopeFilter struct {
	Field    string
	Operator string
	Value    string
	Values   []string
	Label    string
}

// ScopeSort is one sort key of the query: the field and the direction as the query has them and the label of the snapshot's column.
type ScopeSort struct {
	Field     string
	Direction string
	Label     string
}

// ScopeOf reads the scope of a snapshot from its frozen query and holds it to the frozen columns by the core's own rules: at most
// reporting.MaxQueryFilters filters and reporting.MaxQuerySortKeys sort keys; every filter on a column of the snapshot that has a label,
// with an operator of the closed set that fits the column's type and a value the core's one decoding reads, each of its values well
// formed for the column and the two of a `between` in ascending order; every sort key on such a column in a direction of the closed
// set, no column twice. (The catalogue's declaration, which says which of these a dataset publishes, is not frozen with a snapshot and
// is not asked again.) The query was validated against that declaration when the snapshot was admitted, so anything else here is a
// store that does not agree with itself: reporting.ErrIntegrity, and no export says any of it. An error names no field and carries no
// value. A query without filters and without a sort has the empty scope.
func ScopeOf(s r.Snapshot) (Scope, error) {
	q := r.Normalize(s.Query)
	if len(q.Filters) > r.MaxQueryFilters || len(q.Sort) > r.MaxQuerySortKeys {
		return Scope{}, fmt.Errorf("the frozen query has more filters or sort keys than a query may carry: %w", r.ErrIntegrity)
	}
	columns := map[string]r.Column{}
	for _, c := range s.Result.Columns {
		columns[c.ID] = c
	}
	var out Scope
	for _, f := range q.Filters {
		c, ok := columns[f.Field]
		if !ok || c.Label == "" || !r.OperatorFits(f.Operator, c.Type) {
			return Scope{}, fmt.Errorf("a filter of the frozen query does not fit the frozen columns: %w", r.ErrIntegrity)
		}
		values, e := r.FilterValues(f)
		if e != nil {
			return Scope{}, fmt.Errorf("a filter of the frozen query carries a value the core cannot read: %w", r.ErrIntegrity)
		}
		for _, v := range values {
			if !r.ValidFilterValue(v, c) {
				return Scope{}, fmt.Errorf("a filter of the frozen query carries a value that is not one of its column: %w", r.ErrIntegrity)
			}
		}
		if f.Operator == r.OpBetween && r.CompareValues(c, values[0], values[1]) > 0 {
			return Scope{}, fmt.Errorf("a between of the frozen query is not in ascending order: %w", r.ErrIntegrity)
		}
		out.Filters = append(out.Filters, ScopeFilter{Field: f.Field, Operator: f.Operator, Value: f.Value, Values: values, Label: c.Label})
	}
	seen := map[string]bool{}
	for _, k := range q.Sort {
		c, ok := columns[k.Field]
		if !ok || c.Label == "" || seen[k.Field] || !slices.Contains(r.SortDirections, k.Direction) {
			return Scope{}, fmt.Errorf("a sort key of the frozen query does not fit the frozen columns: %w", r.ErrIntegrity)
		}
		seen[k.Field] = true
		out.Sort = append(out.Sort, ScopeSort{Field: k.Field, Direction: k.Direction, Label: c.Label})
	}
	return out, nil
}

// The words of the operators and the directions: Chinese as the PDF prints them, English as the workbook's Metadata sheet writes them
// (the sheet's keys and notes are English). Closed lists beside reporting.FilterOperators and reporting.SortDirections; `between` has a
// sentence of its own. They are data to the writers: the PDF checks every character of them against its fonts like a value.
var (
	operatorChinese  = map[string]string{r.OpEq: "等于", r.OpNe: "不等于", r.OpLt: "小于", r.OpLe: "不大于", r.OpGt: "大于", r.OpGe: "不小于", r.OpIn: "属于", r.OpPrefix: "开头为", r.OpContains: "包含"}
	operatorEnglish  = map[string]string{r.OpEq: "equals", r.OpNe: "does not equal", r.OpLt: "is less than", r.OpLe: "is less than or equal to", r.OpGt: "is greater than", r.OpGe: "is greater than or equal to", r.OpIn: "is one of", r.OpPrefix: "starts with", r.OpContains: "contains"}
	directionChinese = map[string]string{"asc": "升序", "desc": "降序"}
	directionEnglish = map[string]string{"asc": "ascending", "desc": "descending"}
)

// Chinese is the filter in the PDF's words: the column's label, the operator in a word and the value or values, as
// `账龄区间 属于 1_30、31_60`, `应收余额 大于 1000.00` and `到期日 介于 2026-01-01 和 2026-03-31 之间`. A value is the exact string of the query:
// a number is never grouped, rounded or translated (it is the person's input, shown as given). The page has no cell beside the words that
// holds the exact value, so a value that could be misread in the sentence is set between “ and ”, unchanged: one that begins or ends with
// white space, or that holds a mark the sentence itself uses (、 ； “ ”, and 和 between the two values of a `between`).
func (f ScopeFilter) Chinese() string {
	values := make([]string, len(f.Values))
	for i, v := range f.Values {
		values[i] = quoted(v, f.Operator == r.OpBetween)
	}
	switch f.Operator {
	case r.OpBetween:
		return f.Label + " 介于 " + values[0] + " 和 " + values[1] + " 之间"
	case r.OpIn:
		return f.Label + " " + operatorChinese[f.Operator] + " " + strings.Join(values, "、")
	}
	return f.Label + " " + operatorChinese[f.Operator] + " " + values[0]
}

// English is the filter in the workbook's words, as `账龄区间 is one of 1_30, 31_60`: the label, the operator in words and the values as
// the query has them. The row that carries it holds the exact field, operator and value in the cells beside it, so nothing is quoted.
func (f ScopeFilter) English() string {
	switch f.Operator {
	case r.OpBetween:
		return f.Label + " is between " + f.Values[0] + " and " + f.Values[1]
	case r.OpIn:
		return f.Label + " " + operatorEnglish[f.Operator] + " " + strings.Join(f.Values, ", ")
	}
	return f.Label + " " + operatorEnglish[f.Operator] + " " + f.Values[0]
}

// Chinese is the sort key in the PDF's words, as `应收余额 降序`.
func (k ScopeSort) Chinese() string { return k.Label + " " + directionChinese[k.Direction] }

// English is the sort key in the workbook's words, as `应收余额 descending`.
func (k ScopeSort) English() string { return k.Label + " " + directionEnglish[k.Direction] }

// quoted sets a value between “ and ” when the Chinese sentence could be misread without them, and returns it as it is otherwise.
func quoted(v string, between bool) string {
	first, _ := utf8.DecodeRuneInString(v)
	last, _ := utf8.DecodeLastRuneInString(v)
	if unicode.IsSpace(first) || unicode.IsSpace(last) || strings.ContainsAny(v, "、；“”") || between && strings.Contains(v, "和") {
		return "“" + v + "”"
	}
	return v
}
