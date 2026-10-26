// Package xlsx renders a generic typed snapshot without product field names.
package xlsx

import (
	"context"
	"github.com/xuri/excelize/v2"
	r "idp.local/reporting/reporting"
	"strconv"
)

type Writer struct{}

func (Writer) Write(ctx context.Context, s r.Snapshot) ([]byte, error) {
	if len(s.Result.Rows) > r.MaxRows {
		return nil, r.ErrBudget
	}
	f := excelize.NewFile()
	defer f.Close()
	if e := f.SetSheetName("Sheet1", "Data"); e != nil {
		return nil, e
	}
	for _, name := range []string{"Totals", "Metadata"} {
		if _, e := f.NewSheet(name); e != nil {
			return nil, e
		}
	}
	put := func(sheet string, row int, values []string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		for i, v := range values {
			cell, e := excelize.CoordinatesToCellName(i+1, row)
			if e != nil {
				return e
			}
			if e = f.SetCellStr(sheet, cell, v); e != nil {
				return e
			}
		}
		return nil
	}
	head := []string{}
	for _, c := range s.Result.Columns {
		head = append(head, c.Label)
	}
	if e := put("Data", 1, head); e != nil {
		return nil, e
	}
	for i, row := range s.Result.Rows {
		if e := put("Data", i+2, row); e != nil {
			return nil, e
		}
	}
	if e := put("Totals", 1, []string{"metric", "exact_value", "currency", "unit"}); e != nil {
		return nil, e
	}
	for i, m := range s.Query.Metrics {
		var col r.Column
		for _, c := range s.Result.Columns {
			if c.ID == m.ID {
				col = c
			}
		}
		if e := put("Totals", i+2, []string{m.ID, s.Result.Totals[m.ID], col.Currency, col.Unit}); e != nil {
			return nil, e
		}
	}
	metadata := [][]string{{"data_class", "CONTRACT SIMULATOR / NOT REAL BUSINESS DATA"}, {"snapshot_id", s.ID}, {"integration_id", s.Owner.Integration}, {"dataset", s.Query.Dataset.ID + "@" + s.Query.Dataset.Version}, {"as_of", s.Query.AsOf}, {"timezone", s.Query.Timezone}, {"query_hash", s.Prepared.QueryHash}, {"source_watermark", s.Prepared.Watermark}, {"source_snapshot_ref", s.Prepared.Ref}, {"expires_at", s.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")}, {"precision", "All values are exact text cells. Use approved typed imports for arithmetic; no formulas generated."}, {"field", "type", "scale", "currency", "unit"}}
	for _, c := range s.Result.Columns {
		metadata = append(metadata, []string{c.ID, c.Type, strconv.Itoa(c.Scale), c.Currency, c.Unit})
	}
	for i, row := range metadata {
		if e := put("Metadata", i+1, row); e != nil {
			return nil, e
		}
	}
	style, e := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"174A8B"}}})
	if e != nil {
		return nil, e
	}
	end, e := excelize.CoordinatesToCellName(len(head), 1)
	if e != nil {
		return nil, e
	}
	if e = f.SetCellStyle("Data", "A1", end, style); e != nil {
		return nil, e
	}
	for _, name := range []string{"Data", "Totals", "Metadata"} {
		if e = f.SetColWidth(name, "A", "F", 28); e != nil {
			return nil, e
		}
	}
	if e = f.SetPanes("Data", &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	buffer, e := f.WriteToBuffer()
	if e != nil {
		return nil, e
	}
	if buffer.Len() > r.MaxArtifactBytes {
		return nil, r.ErrBudget
	}
	return buffer.Bytes(), nil
}

var _ r.WorkbookWriter = Writer{}
