package contractsim

import (
	"context"
	r "idp.local/reporting/reporting"
	"slices"
	"strconv"
	"strings"
)

// Fixture is synthetic source configuration. It contains no grants or prepared
// handles. Real source adapters do not use this seeding API.
type Fixture struct {
	Dataset r.Dataset
	Records []Record
}

func (s *Simulator) Fixture() Fixture {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Fixture{Dataset: copyValue(s.dataset), Records: copyValue(s.records)}
}

// ReadFrozenPage projects an already authorized immutable fixture version. Both
// simulator adapters authorize at their four-operation boundary before calling
// this helper; the helper is not an authorization endpoint.
func ReadFrozenPage(ctx context.Context, seed Fixture, q r.QuerySpec, g Grant, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	if e := r.ValidateQuery(q, seed.Dataset); e != nil {
		return r.SourcePage{}, e
	}
	if limit < 1 || limit > 100 {
		return r.SourcePage{}, r.ErrInvalid
	}
	start := 0
	if cursor != "" {
		parts := strings.Split(cursor, ":")
		if len(parts) != 2 || parts[0] != p.Ref {
			return r.SourcePage{}, r.ErrInvalid
		}
		v, e := strconv.Atoi(parts[1])
		if e != nil || v < 0 {
			return r.SourcePage{}, r.ErrInvalid
		}
		start = v
	}
	rows := [][]string{}
	totals := map[string]string{}
	metricCols := seed.Dataset.Columns[len(q.Dimensions):]
	for _, record := range seed.Records {
		if e := ctx.Err(); e != nil {
			return r.SourcePage{}, e
		}
		if slices.Contains(q.Companies, record.Company) && slices.Contains(g.Rows, record.Key) {
			rows = append(rows, slices.Clone(record.Values))
		}
	}
	for i, m := range q.Metrics {
		values := []string{}
		for _, row := range rows {
			if len(row) != len(seed.Dataset.Columns) {
				return r.SourcePage{}, r.ErrIntegrity
			}
			values = append(values, row[len(q.Dimensions)+i])
		}
		v, e := sum(values, metricCols[i].Scale)
		if e != nil {
			return r.SourcePage{}, e
		}
		totals[m.ID] = v
	}
	if start > len(rows) {
		return r.SourcePage{}, r.ErrInvalid
	}
	end := min(start+limit, len(rows))
	page := r.SourcePage{Ref: p.Ref, QueryHash: p.QueryHash, Watermark: p.Watermark, Rows: rows[start:end], Totals: totals}
	if end < len(rows) {
		page.NextCursor = p.Ref + ":" + strconv.Itoa(end)
	}
	return page, nil
}
