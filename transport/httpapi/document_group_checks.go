package httpapi

import (
	"errors"
	"net/http"
	"sort"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// groupResult is what one group of a 2.0 document answers to the check, for each of its rows.
type groupResult struct {
	err error
}

// checkGroupRows is the check of a 2.0 template (S03-J): each row asked for, or sampled, stands for
// the group that holds it, bound whole and checked once; every row of the answer carries the result of
// its group, a refused one with the same code and detail a preview of the group would answer. A
// refusal of the group is that group's; any other error is the whole request's, as for 1.x rows.
func checkGroupRows(w http.ResponseWriter, req *http.Request, s *r.Service, p r.Principal, checker DocumentChecker, config doctemplates.Document, snap r.Snapshot, snapshotID string, explicit []int, boundary bool, assets *doctemplates.Assets) error {
	ctx := req.Context()
	groups, e := doctemplates.Groups(config, snap)
	if e != nil {
		return e
	}
	groupOf := map[int]int{}
	for gi, g := range groups {
		for _, i := range g.Rows {
			groupOf[i] = gi
		}
	}
	var picks []checkRowPick
	if boundary {
		if picks, e = boundaryGroups(config, snap, groups); e != nil {
			return e
		}
	} else {
		for _, v := range explicit {
			if _, ok := groupOf[v]; !ok {
				return r.ErrInvalid
			}
			picks = append(picks, checkRowPick{index: v})
		}
	}
	// Bind each group once, as a preview does: what the layout could not print is the group's
	// refusal, any other refusal the request's.
	type bound struct {
		values  map[string]string
		table   *doctemplates.TableData
		refused error
	}
	binds := map[int]*bound{}
	for _, v := range picks {
		gi := groupOf[v.index]
		if binds[gi] != nil {
			continue
		}
		values, table, e := doctemplates.BindGroup(config, snap, groups[gi])
		switch {
		case errors.Is(e, r.ErrUnsupported):
			binds[gi] = &bound{refused: e}
		case e != nil:
			return e
		default:
			binds[gi] = &bound{values: values, table: table}
		}
	}
	if e = authorizeDocument(req, s, p, config, assets); e != nil {
		return e
	}
	results := map[int]*groupResult{}
	answer := make([]checkRow, 0, len(picks))
	for _, v := range picks {
		if e = ctx.Err(); e != nil {
			return e
		}
		gi := groupOf[v.index]
		result := results[gi]
		if result == nil {
			b := binds[gi]
			result = &groupResult{err: b.refused}
			if result.err == nil {
				result.err = checker.CheckIssue(ctx, config, b.values, nil, b.table)
			}
			results[gi] = result
		}
		row := checkRow{RowIndex: v.index, OK: result.err == nil, Reasons: v.reasons}
		if result.err != nil {
			if !errors.Is(result.err, r.ErrUnsupported) || ctx.Err() != nil {
				return result.err
			}
			row.Code, _, _ = classify(result.err)
			if detail, found := doctemplates.DetailOf(result.err); found {
				row.Detail = &detail
			}
		}
		answer = append(answer, row)
	}
	if _, e = s.AuthorizedSnapshot(ctx, p, snapshotID); e != nil {
		return e
	}
	if e = authorizeDocument(req, s, p, config, assets); e != nil {
		return e
	}
	w.Header().Set("Cache-Control", "no-store")
	return send(w, 200, map[string]any{"rows": answer})
}

// boundaryGroups picks the groups a 2.0 template is most likely to refuse, each by its first row, in row
// order: the first and the last group, the one with the most rows and the one with the fewest; for each
// field and group-sum text, the groups with the longest and the shortest value by display width; for
// each table column, the group that holds the longest cell. Ties go to the lowest group. At most
// MaxDocumentCheckRows groups, taken in that order.
func boundaryGroups(config doctemplates.Document, snap r.Snapshot, groups []doctemplates.RowGroup) ([]checkRowPick, error) {
	if len(snap.Result.Rows) == 0 || len(groups) == 0 {
		return nil, nil
	}
	chosen := map[int]*checkRowPick{}
	var order []int
	add := func(group int, reason string) {
		if pick := chosen[group]; pick != nil {
			pick.reasons = append(pick.reasons, reason)
			return
		}
		if len(order) < MaxDocumentCheckRows {
			chosen[group] = &checkRowPick{index: groups[group].Rows[0], reasons: []string{reason}}
			order = append(order, group)
		}
	}
	most, fewest := 0, 0
	for i, g := range groups {
		if len(g.Rows) > len(groups[most].Rows) {
			most = i
		}
		if len(g.Rows) < len(groups[fewest].Rows) {
			fewest = i
		}
	}
	add(0, "first")
	add(len(groups)-1, "last")
	add(most, "most_rows")
	add(fewest, "fewest_rows")
	columns := map[string]int{}
	for i, c := range snap.Result.Columns {
		columns[c.ID] = i
	}
	for _, bi := range config.Items() {
		it := bi.Item
		switch {
		case it.Type == doctemplates.TypeText && (it.Source == doctemplates.SourceField || it.Source == doctemplates.SourceGroupSum):
			column, ok := columns[it.Field]
			if !ok {
				return nil, r.ErrDenied
			}
			longest, shortest, most, least := 0, 0, -1, int(^uint(0)>>1)
			for gi, g := range groups {
				value := ""
				if it.Source == doctemplates.SourceField {
					if row := snap.Result.Rows[g.Rows[0]]; column >= len(row) {
						return nil, r.ErrIntegrity
					} else {
						value = row[column]
					}
				} else {
					values := make([]string, 0, len(g.Rows))
					for _, i := range g.Rows {
						if column >= len(snap.Result.Rows[i]) {
							return nil, r.ErrIntegrity
						}
						values = append(values, snap.Result.Rows[i][column])
					}
					sum, e := doctemplates.SumDecimals(values, snap.Result.Columns[column].Scale)
					if e != nil {
						return nil, e
					}
					value = doctemplates.GroupDigits(sum) // as it prints
				}
				// Strict comparisons keep the lowest group on a tie.
				if w := displayWidth(value); w > most {
					longest, most = gi, w
				}
				if w := displayWidth(value); w < least {
					shortest, least = gi, w
				}
			}
			add(longest, "longest:"+it.ID)
			add(shortest, "shortest:"+it.ID)
		case it.Type == doctemplates.TypeTable:
			for _, c := range it.Columns {
				column, ok := columns[c.Field]
				if !ok {
					return nil, r.ErrDenied
				}
				widest, most := 0, -1
				for gi, g := range groups {
					for _, i := range g.Rows {
						if column >= len(snap.Result.Rows[i]) {
							return nil, r.ErrIntegrity
						}
						if w := displayWidth(snap.Result.Rows[i][column]); w > most {
							widest, most = gi, w
						}
					}
				}
				add(widest, "cell:"+c.Field)
			}
		}
	}
	picks := make([]checkRowPick, 0, len(order))
	for _, group := range order {
		picks = append(picks, *chosen[group])
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].index < picks[j].index })
	return picks, nil
}
