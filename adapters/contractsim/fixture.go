package contractsim

import (
	"context"
	r "idp.local/reporting/reporting"
	"slices"
	"strconv"
	"strings"
)

// Fixture is synthetic source configuration. It contains no grants or prepared
// handles. Real source adapters do not use this seeding API. NumberPrefix is the prefix of
// the document numbers the source issues (S03-H), such as AR; empty means DefaultNumberPrefix.
type Fixture struct {
	Dataset      r.Dataset
	Records      []Record
	NumberPrefix string
}

func (s *Simulator) Fixture() Fixture {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Fixture{Dataset: copyValue(s.dataset), Records: copyValue(s.records), NumberPrefix: s.prefix}
}

// ReadFrozenPage projects an already authorized immutable fixture version. Both
// simulator adapters authorize at their four-operation boundary before calling
// this helper; the helper is not an authorization endpoint.
//
// The rows are the frozen query's scope (S20-A, ADR 0023): the records of the query's companies the grant covers, narrowed by the
// query's filters and ordered by its sort as the dataset version declares them (narrow), and the totals are the narrowed rows'. The
// query is validated against the version's declaration here as in PrepareRead, so a read whose filters the version does not publish
// is unsupported however it was stored.
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
			if len(record.Values) != len(seed.Dataset.Columns) {
				return r.SourcePage{}, r.ErrIntegrity
			}
			rows = append(rows, slices.Clone(record.Values))
		}
	}
	rows, e := narrow(seed.Dataset, q, rows)
	if e != nil {
		return r.SourcePage{}, e
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
