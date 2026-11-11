package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"

	r "idp.local/reporting/reporting"
)

// The rules of the published filters and sorts (S20-A, ADR 0023 decision 4): for every filter the dataset declares and every operator of
// it, a well-formed filter made from the sample read's own values narrows the rows to those that satisfy it, the rows are rows of the
// whole scope and the totals agree where they are sums; for every sort and direction the rows come back ordered, the same rows with the
// same totals; a filter on an undeclared field, with an undeclared operator or with a malformed value, and a sort the dataset does not
// declare, are refused with unsupported (or invalid).
const (
	ruleFilters          = "every declared filter, with each of its operators, narrows the rows to those that satisfy it (rows of the whole scope; totals agree where they are sums)"
	ruleSorts            = "every declared sort orders the rows in each of its directions, the same rows and totals, stable across pages"
	ruleUndeclaredField  = "a filter on a field the catalog does not declare is refused (unsupported or invalid)"
	ruleUndeclaredOp     = "a filter with an operator the catalog does not declare for its field is refused (unsupported or invalid)"
	ruleMalformedValue   = "a filter with a malformed value is refused (unsupported or invalid)"
	ruleUndeclaredSort   = "a sort on a field or in a direction the catalog does not declare is refused (unsupported or invalid)"
	noFilterDeclared     = "the dataset declares no filter"
	noSortDeclared       = "the dataset declares no sort"
	wholeReadUnavailable = "the whole read could not be paged (R3)"
)

// narrowing runs the F rules. rows and totals are the whole read's (R3), paged says whether it was read whole.
func narrowing(ctx context.Context, a r.Adapter, sc r.SourceContext, q r.QuerySpec, d r.Dataset, rows [][]string, totals map[string]string, paged bool, add func(id, rule string, err error) bool, skip func(id, rule, why string)) {
	index := map[string]int{}
	for i, c := range d.Columns {
		index[c.ID] = i
	}
	switch {
	case len(d.Filters) == 0:
		skip("F1", ruleFilters, noFilterDeclared)
	case !paged:
		skip("F1", ruleFilters, wholeReadUnavailable)
	default:
		add("F1", ruleFilters, filtersObeyed(ctx, a, sc, q, d, index, rows, additiveMetrics(d, index, rows, totals)))
	}
	switch {
	case len(d.Sorts) == 0:
		skip("F2", ruleSorts, noSortDeclared)
	case !paged:
		skip("F2", ruleSorts, wholeReadUnavailable)
	default:
		add("F2", ruleSorts, sortsObeyed(ctx, a, sc, q, d, index, rows, totals))
	}
	add("F3", ruleUndeclaredField, refused(ctx, a, sc, withFilter(q, undeclaredField(d, rows))))
	if len(d.Filters) == 0 {
		skip("F4", ruleUndeclaredOp, noFilterDeclared)
		skip("F5", ruleMalformedValue, noFilterDeclared)
	} else {
		spec := d.Filters[0]
		var err error
		for _, op := range undeclaredOperators(spec) {
			if err = refused(ctx, a, sc, withFilter(q, r.Filter{Field: spec.Field, Operator: op, Value: sampleValue(d, index, rows, spec)})); err != nil {
				err = fmt.Errorf("operator %s: %w", op, err)
				break
			}
		}
		add("F4", ruleUndeclaredOp, err)
		add("F5", ruleMalformedValue, refused(ctx, a, sc, withFilter(q, malformed(d, index, spec))))
	}
	add("F6", ruleUndeclaredSort, refused(ctx, a, sc, withSort(q, undeclaredSort(d))))
}

// refused prepares the query and expects a refusal of what the catalog does not declare.
func refused(ctx context.Context, a r.Adapter, sc r.SourceContext, q r.QuerySpec) error {
	_, err := a.PrepareRead(ctx, sc, q, r.QueryHash(q))
	return expect(err, r.ErrUnsupported, r.ErrInvalid)
}

func withFilter(q r.QuerySpec, f r.Filter) r.QuerySpec {
	q.Filters = []r.Filter{f}
	q.Sort = []r.Sort{}
	return r.Normalize(q)
}

func withSort(q r.QuerySpec, s r.Sort) r.QuerySpec {
	q.Filters = []r.Filter{}
	q.Sort = []r.Sort{s}
	return r.Normalize(q)
}

// undeclaredField is a filter on a column the dataset declares no filter for, or, when every column is declared, on a field that does not
// exist; eq with a value of the first row when there is one.
func undeclaredField(d r.Dataset, rows [][]string) r.Filter {
	declared := map[string]bool{}
	for _, f := range d.Filters {
		declared[f.Field] = true
	}
	for i, c := range d.Columns {
		if !declared[c.ID] {
			value := "reporting-source-check"
			if len(rows) > 0 && i < len(rows[0]) && rows[0][i] != "" {
				value = rows[0][i]
			}
			return r.Filter{Field: c.ID, Operator: r.OpEq, Value: value}
		}
	}
	return r.Filter{Field: "reporting-source-check-" + r.NewID(), Operator: r.OpEq, Value: "reporting-source-check"}
}

// undeclaredOperators are the operators of the closed set the spec does not declare, and one outside the set.
func undeclaredOperators(spec r.FilterSpec) []string {
	var out []string
	for _, op := range r.FilterOperators {
		if !slices.Contains(spec.Operators, op) {
			out = append(out, op)
		}
	}
	return append(out, "like")
}

// sampleValue is a value of the spec's column, from the rows (or its closed list) when there is one, so that a refusal is for the operator
// alone; a scalar, whatever the operator (an undeclared `in` is refused before its value is read).
func sampleValue(d r.Dataset, index map[string]int, rows [][]string, spec r.FilterSpec) string {
	if len(spec.Values) > 0 {
		return spec.Values[0]
	}
	i := index[spec.Field]
	for _, row := range rows {
		if i < len(row) && r.ValidFilterValue(row[i], d.Columns[i]) {
			return row[i]
		}
	}
	switch d.Columns[i].Type {
	case "integer":
		return "1"
	case "decimal":
		return decimalOf(big.NewInt(1), d.Columns[i].Scale)
	case "date":
		return "2000-01-01"
	case "boolean":
		return "true"
	}
	return "reporting-source-check"
}

// malformed is a filter of the spec's first operator whose value is not well formed for the column: not an array for `in` and `between`,
// not a number, a date or a boolean for those types, an empty string for a string column.
func malformed(d r.Dataset, index map[string]int, spec r.FilterSpec) r.Filter {
	op := spec.Operators[0]
	value := ""
	switch {
	case op == r.OpIn || op == r.OpBetween:
		value = "not-a-json-array"
	case d.Columns[index[spec.Field]].Type != "string":
		value = "not-a-" + d.Columns[index[spec.Field]].Type
	}
	return r.Filter{Field: spec.Field, Operator: op, Value: value}
}

// undeclaredSort is a sort on a column the dataset declares no sort for; else a declared field in a direction it does not declare; else a
// field that does not exist.
func undeclaredSort(d r.Dataset) r.Sort {
	declared := map[string]r.SortSpec{}
	for _, s := range d.Sorts {
		declared[s.Field] = s
	}
	for _, c := range d.Columns {
		if _, ok := declared[c.ID]; !ok {
			return r.Sort{Field: c.ID, Direction: "asc"}
		}
	}
	for _, s := range d.Sorts {
		for _, dir := range r.SortDirections {
			if !slices.Contains(s.Directions, dir) {
				return r.Sort{Field: s.Field, Direction: dir}
			}
		}
	}
	return r.Sort{Field: "reporting-source-check-" + r.NewID(), Direction: "asc"}
}

// filtersObeyed exercises every declared filter with each of its operators.
func filtersObeyed(ctx context.Context, a r.Adapter, sc r.SourceContext, q r.QuerySpec, d r.Dataset, index map[string]int, rows [][]string, additive []string) error {
	whole := multiset(rows)
	for _, spec := range d.Filters {
		i := index[spec.Field]
		c := d.Columns[i]
		for _, op := range spec.Operators {
			f, ok := sampleFilter(c, i, spec, op, rows)
			if !ok {
				continue // no value of the rows can make this filter (every cell empty): nothing to exercise
			}
			narrowed := withFilter(q, f)
			p, err := a.PrepareRead(ctx, sc, narrowed, r.QueryHash(narrowed))
			if err != nil {
				return fmt.Errorf("filter %s %s was refused: %w", spec.Field, op, err)
			}
			got, totals, err := readAll(ctx, a, sc, p)
			if err != nil {
				return fmt.Errorf("filter %s %s: %w", spec.Field, op, err)
			}
			for _, row := range got {
				if len(row) <= i {
					return fail(fmt.Sprintf("filter %s %s: a row is narrower than the dataset", spec.Field, op), r.ErrIntegrity)
				}
				if ok, err := satisfies(c, f, row[i]); err != nil || !ok {
					return fail(fmt.Sprintf("filter %s %s: a row does not satisfy the filter", spec.Field, op), r.ErrIntegrity)
				}
				if whole[key(row)] == 0 {
					return fail(fmt.Sprintf("filter %s %s: a row is not a row of the whole scope", spec.Field, op), r.ErrIntegrity)
				}
			}
			if err := totalsAgree(d, index, got, totals, additive); err != nil {
				return fmt.Errorf("filter %s %s: %w", spec.Field, op, err)
			}
		}
	}
	return nil
}

// sampleFilter makes one well-formed filter of the operator from the rows' values of the column (from the closed list when the spec has
// one): eq, ne, lt, le, gt, ge on a value of the rows; in on one or two of them; between on the least and the greatest; prefix on the first
// character of a value; contains on a value. ok is false when no row has a value the filter can take.
func sampleFilter(c r.Column, i int, spec r.FilterSpec, op string, rows [][]string) (r.Filter, bool) {
	var values []string
	for _, row := range rows {
		if i < len(row) && r.ValidFilterValue(row[i], c) && (spec.Values == nil || slices.Contains(spec.Values, row[i])) && !slices.Contains(values, row[i]) {
			values = append(values, row[i])
		}
	}
	if len(values) == 0 && len(spec.Values) > 0 && (op == r.OpEq || op == r.OpNe || op == r.OpIn) {
		values = spec.Values[:1]
	}
	if len(values) == 0 {
		// A bound or a pattern may take any well-formed value: the rows' values of the column, closed list or not.
		for _, row := range rows {
			if i < len(row) && r.ValidFilterValue(row[i], c) && !slices.Contains(values, row[i]) {
				values = append(values, row[i])
			}
		}
	}
	if len(values) == 0 {
		return r.Filter{}, false
	}
	f := r.Filter{Field: spec.Field, Operator: op, Value: values[0]}
	switch op {
	case r.OpIn:
		f.Value = arrayOf(values[:min(2, len(values))])
	case r.OpBetween:
		lo, hi := values[0], values[0]
		for _, v := range values[1:] {
			if r.CompareValues(c, v, lo) < 0 {
				lo = v
			}
			if r.CompareValues(c, v, hi) > 0 {
				hi = v
			}
		}
		f.Value = arrayOf([]string{lo, hi})
	case r.OpPrefix:
		f.Value = string([]rune(values[0])[:1])
	}
	return f, true
}

func arrayOf(values []string) string {
	b, _ := json.Marshal(values)
	return string(b)
}

// satisfies says whether a cell satisfies a filter, by the rules the platform's adapters apply (reporting.CompareValues, reporting.FilterValues).
func satisfies(c r.Column, f r.Filter, cell string) (bool, error) {
	values, err := r.FilterValues(f)
	if err != nil {
		return false, err
	}
	cmp := func(v string) int { return r.CompareValues(c, cell, v) }
	switch f.Operator {
	case r.OpEq:
		return cmp(values[0]) == 0, nil
	case r.OpNe:
		return cmp(values[0]) != 0, nil
	case r.OpLt:
		return cmp(values[0]) < 0, nil
	case r.OpLe:
		return cmp(values[0]) <= 0, nil
	case r.OpGt:
		return cmp(values[0]) > 0, nil
	case r.OpGe:
		return cmp(values[0]) >= 0, nil
	case r.OpIn:
		return slices.ContainsFunc(values, func(v string) bool { return cmp(v) == 0 }), nil
	case r.OpBetween:
		return cmp(values[0]) >= 0 && cmp(values[1]) <= 0, nil
	case r.OpPrefix:
		return strings.HasPrefix(cell, values[0]), nil
	case r.OpContains:
		return strings.Contains(cell, values[0]), nil
	}
	return false, errors.New("an operator the checker does not know")
}

// sortsObeyed exercises every declared sort in each of its directions.
func sortsObeyed(ctx context.Context, a r.Adapter, sc r.SourceContext, q r.QuerySpec, d r.Dataset, index map[string]int, rows [][]string, totals map[string]string) error {
	whole := multiset(rows)
	for _, spec := range d.Sorts {
		i := index[spec.Field]
		c := d.Columns[i]
		for _, dir := range spec.Directions {
			sorted := withSort(q, r.Sort{Field: spec.Field, Direction: dir})
			p, err := a.PrepareRead(ctx, sc, sorted, r.QueryHash(sorted))
			if err != nil {
				return fmt.Errorf("sort %s %s was refused: %w", spec.Field, dir, err)
			}
			got, gotTotals, err := readAll(ctx, a, sc, p)
			if err != nil {
				return fmt.Errorf("sort %s %s: %w", spec.Field, dir, err)
			}
			if len(got) != len(rows) || !maps.Equal(multiset(got), whole) || !maps.Equal(gotTotals, totals) {
				return fail(fmt.Sprintf("sort %s %s: not the same rows and totals as the unsorted read", spec.Field, dir), r.ErrIntegrity)
			}
			for n := 1; n < len(got); n++ {
				if len(got[n-1]) <= i || len(got[n]) <= i {
					return fail(fmt.Sprintf("sort %s %s: a row is narrower than the dataset", spec.Field, dir), r.ErrIntegrity)
				}
				order := r.CompareValues(c, got[n-1][i], got[n][i])
				if dir == "desc" {
					order = -order
				}
				if order > 0 {
					return fail(fmt.Sprintf("sort %s %s: two rows are out of order", spec.Field, dir), r.ErrIntegrity)
				}
			}
		}
	}
	return nil
}

// additiveMetrics are the metrics whose total of the whole read is the sum of its rows: for those, a narrowed read's total must be the sum
// of its rows. A metric whose total is not its rows' sum (an average, a count of something else) is not judged.
func additiveMetrics(d r.Dataset, index map[string]int, rows [][]string, totals map[string]string) []string {
	var out []string
	for _, m := range d.Metrics {
		c := d.Columns[index[m.ID]]
		if sum, ok := sumOf(c, index[m.ID], rows); ok && sum == totals[m.ID] {
			out = append(out, m.ID)
		}
	}
	return out
}

// totalsAgree holds a narrowed read's totals to the sum of its rows for the additive metrics.
func totalsAgree(d r.Dataset, index map[string]int, rows [][]string, totals map[string]string, additive []string) error {
	for _, id := range additive {
		sum, ok := sumOf(d.Columns[index[id]], index[id], rows)
		if !ok || sum != totals[id] {
			return fail("the total of "+id+" is not the sum of the narrowed rows", r.ErrIntegrity)
		}
	}
	return nil
}

// sumOf is the exact sum of a column's values over the rows, written as the column writes a value; ok is false for a column that is not
// an integer or a decimal, or a value that is not of its form.
func sumOf(c r.Column, i int, rows [][]string) (string, bool) {
	if c.Type != "integer" && c.Type != "decimal" {
		return "", false
	}
	total := new(big.Int)
	for _, row := range rows {
		if i >= len(row) {
			return "", false
		}
		n, ok := new(big.Int).SetString(strings.ReplaceAll(row[i], ".", ""), 10)
		if !ok {
			return "", false
		}
		total.Add(total, n)
	}
	return decimalOf(total, c.Scale), true
}

// decimalOf writes an integer of scale digits after the point as a decimal of that scale.
func decimalOf(n *big.Int, scale int) string {
	digits := new(big.Int).Abs(n).String()
	if scale > 0 {
		for len(digits) <= scale {
			digits = "0" + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if n.Sign() < 0 {
		return "-" + digits
	}
	return digits
}

func key(row []string) string { b, _ := json.Marshal(row); return string(b) }

func multiset(rows [][]string) map[string]int {
	out := map[string]int{}
	for _, row := range rows {
		out[key(row)]++
	}
	return out
}
