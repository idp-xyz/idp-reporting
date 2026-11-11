package contractsim

import (
	r "idp.local/reporting/reporting"
	"slices"
	"strings"
)

// narrow applies a query's published filters and sort to the authorized rows of a frozen fixture version, in memory, as a source product
// applies them in its own transaction (S20-A, ADR 0023 decision 4): it keeps the rows that satisfy every filter (a conjunction) and orders
// them by the sort keys, stably, so that the order is the same on every page of a read and ties keep the fixture's order. A value is data
// compared by the column's type (reporting.CompareValues: exact for integers and decimals, text for dates and strings), never a text
// that is interpreted; the query has been validated against the dataset's declaration (reporting.ValidateQuery) before this is reached,
// so an operator this does not know is a declaration the platform would have refused: it is answered as unsupported, not applied.
func narrow(d r.Dataset, q r.QuerySpec, rows [][]string) ([][]string, error) {
	index := map[string]int{}
	for i, c := range d.Columns {
		index[c.ID] = i
	}
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		keep := true
		for _, f := range q.Filters {
			i, ok := index[f.Field]
			if !ok || i >= len(row) {
				return nil, r.ErrUnsupported
			}
			matched, e := matches(d.Columns[i], f, row[i])
			if e != nil {
				return nil, e
			}
			if !matched {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, row)
		}
	}
	for _, s := range q.Sort {
		if _, ok := index[s.Field]; !ok || (s.Direction != "asc" && s.Direction != "desc") {
			return nil, r.ErrUnsupported
		}
	}
	if len(q.Sort) > 0 {
		slices.SortStableFunc(out, func(a, b []string) int {
			for _, s := range q.Sort {
				i := index[s.Field]
				c := r.CompareValues(d.Columns[i], a[i], b[i])
				if s.Direction == "desc" {
					c = -c
				}
				if c != 0 {
					return c
				}
			}
			return 0
		})
	}
	return out, nil
}

// matches says whether one cell satisfies one filter.
func matches(c r.Column, f r.Filter, cell string) (bool, error) {
	values, e := r.FilterValues(f)
	if e != nil {
		return false, e
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
	return false, r.ErrUnsupported
}
