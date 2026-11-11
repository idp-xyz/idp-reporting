// Package xlsx renders a generic typed snapshot without product field names.
//
// Since S23-A (docs/ADR_0024_TABLE_REPORT_FORMATTING.md decision 3) every workbook, with or without a layout, writes a decimal or integer
// column as numeric cells with an Excel number format, each followed by a text column `<label>（精确）` that holds the exact frozen string.
// A numeric cell is written only when the exact string survives a float64 and comes back as the same digits at the column's scale; a value
// that does not is written as text in the numeric column too, and the Metadata sheet counts those cells. The workbook stays a record: the
// subtotal rows of a layout are values, never formulas, and no formula is ever generated.
package xlsx

import (
	"context"
	"strconv"

	"github.com/xuri/excelize/v2"
	"idp.local/reporting/export/present"
	r "idp.local/reporting/reporting"
)

// The suffix of the exact text column beside each numeric column, and the precision note of the Metadata sheet.
const (
	ExactSuffix   = "（精确）"
	PrecisionNote = "Decimal and integer columns are numeric cells for arithmetic when their exact digits survive a float64 (else text, counted in inexact_cells); the （精确） text columns beside them hold the exact frozen strings for reconciliation. Subtotal rows are values; no formulas generated."
)

// Writer renders a snapshot as a workbook of three sheets: Data, Totals and Metadata.
type Writer struct{}

// Write renders the whole snapshot: every column in catalogue order under the dataset's label, as the exports have always shown it.
func (w Writer) Write(ctx context.Context, s r.Snapshot) ([]byte, error) {
	return w.write(ctx, s, present.Whole(s))
}

// WritePresented renders the snapshot through a presentation (S23-A): the layout's columns in its order with its labels and formats, each
// subtotal group followed by its subtotal row in bold, the marks as cell fills, and the layout, its title and its footnote in Metadata. A
// presentation that does not fit the snapshot is refused whole.
func (w Writer) WritePresented(ctx context.Context, s r.Snapshot, p r.Presentation) ([]byte, error) {
	return w.write(ctx, s, p)
}

// styles makes and keeps the cell styles a workbook uses: bold or not, a number format or none, a mark fill or none.
type styles struct {
	f     *excelize.File
	cache map[styleKey]int
}

type styleKey struct {
	bold   bool
	numFmt string
	fill   string
}

func (s *styles) of(k styleKey) (int, error) {
	if id, ok := s.cache[k]; ok {
		return id, nil
	}
	st := &excelize.Style{}
	if k.bold {
		st.Font = &excelize.Font{Bold: true}
	}
	if k.numFmt != "" {
		custom := k.numFmt
		st.CustomNumFmt = &custom
	}
	if k.fill != "" {
		st.Fill = excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{k.fill}}
	}
	id, e := s.f.NewStyle(st)
	if e != nil {
		return 0, e
	}
	s.cache[k] = id
	return id, nil
}

func (Writer) write(ctx context.Context, s r.Snapshot, p r.Presentation) ([]byte, error) {
	if len(s.Result.Rows) > r.MaxRows {
		return nil, r.ErrBudget
	}
	table, e := present.Build(s, p)
	if e != nil {
		return nil, e
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
	st := &styles{f: f, cache: map[styleKey]int{}}
	inexact := 0
	// number writes an exact decimal string at the cell: a numeric cell when it round-trips through a float64 at the scale, else its
	// formatted text (counted as inexact); the style carries the number format, the weight and the fill.
	number := func(sheet, cell, exact string, scale int, format r.NumberFormat, bold bool, fill string) error {
		key := styleKey{bold: bold, fill: fill}
		if present.RoundTrips(exact, scale) {
			v, e := strconv.ParseFloat(exact, 64)
			if e != nil {
				return e
			}
			if e = f.SetCellFloat(sheet, cell, v, scale, 64); e != nil {
				return e
			}
			key.numFmt = present.ExcelNumberFormat(scale, format)
		} else {
			inexact++
			if e := f.SetCellStr(sheet, cell, present.Format(exact, format)); e != nil {
				return e
			}
		}
		if key == (styleKey{}) {
			return nil
		}
		id, e := st.of(key)
		if e != nil {
			return e
		}
		return f.SetCellStyle(sheet, cell, cell, id)
	}
	text := func(sheet, cell, v string, bold bool, fill string) error {
		if e := f.SetCellStr(sheet, cell, v); e != nil {
			return e
		}
		if !bold && fill == "" {
			return nil
		}
		id, e := st.of(styleKey{bold: bold, fill: fill})
		if e != nil {
			return e
		}
		return f.SetCellStyle(sheet, cell, cell, id)
	}
	putText := func(sheet string, row int, values []string) error {
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
	// The Data sheet: the shown columns, a numeric column followed by its exact text column.
	var head []string
	for _, c := range table.Columns {
		head = append(head, c.Header())
		if c.Numeric() {
			head = append(head, c.Header()+ExactSuffix)
		}
	}
	if e := putText("Data", 1, head); e != nil {
		return nil, e
	}
	for i, row := range table.Rows {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		col := 1
		for k, c := range table.Columns {
			cell := row.Cells[k]
			name, e := excelize.CoordinatesToCellName(col, i+2)
			if e != nil {
				return nil, e
			}
			fill := present.Palette[cell.Mark]
			switch {
			case !c.Numeric():
				if e = text("Data", name, cell.Text, row.Subtotal, fill); e != nil {
					return nil, e
				}
				col++
			case cell.Exact == "":
				// The label cell of a subtotal row in a numeric column.
				if e = text("Data", name, cell.Text, row.Subtotal, fill); e != nil {
					return nil, e
				}
				exactName, e := excelize.CoordinatesToCellName(col+1, i+2)
				if e != nil {
					return nil, e
				}
				if e = text("Data", exactName, "", row.Subtotal, ""); e != nil {
					return nil, e
				}
				col += 2
			default:
				if e = number("Data", name, cell.Exact, c.Source.Scale, c.Format, row.Subtotal, fill); e != nil {
					return nil, e
				}
				exactName, e := excelize.CoordinatesToCellName(col+1, i+2)
				if e != nil {
					return nil, e
				}
				if e = text("Data", exactName, cell.Exact, row.Subtotal, ""); e != nil {
					return nil, e
				}
				col += 2
			}
		}
	}
	// The Totals sheet: the source's total of every metric of the query, as a number under the same rule and as its exact string.
	if e := putText("Totals", 1, []string{"metric", "value", "exact_value", "currency", "unit"}); e != nil {
		return nil, e
	}
	formats := map[string]r.NumberFormat{}
	for _, c := range table.Columns {
		formats[c.Source.ID] = c.Format
	}
	for i, m := range s.Query.Metrics {
		var col r.Column
		for _, c := range s.Result.Columns {
			if c.ID == m.ID {
				col = c
			}
		}
		exact := s.Result.Totals[m.ID]
		if e := putText("Totals", i+2, []string{m.ID, "", exact, col.Currency, col.Unit}); e != nil {
			return nil, e
		}
		if r.NumericColumnType(col.Type) {
			format, ok := formats[m.ID]
			if !ok {
				format = r.DefaultNumberFormat
			}
			if e := number("Totals", "B"+strconv.Itoa(i+2), exact, col.Scale, format, false, ""); e != nil {
				return nil, e
			}
		}
	}
	metadata := [][]string{
		{"data_class", "CONTRACT SIMULATOR / NOT REAL BUSINESS DATA"},
		{"snapshot_id", s.ID},
		{"integration_id", s.Owner.Integration},
		{"dataset", s.Query.Dataset.ID + "@" + s.Query.Dataset.Version},
		{"as_of", s.Query.AsOf},
		{"timezone", s.Query.Timezone},
		{"query_hash", s.Prepared.QueryHash},
		{"source_watermark", s.Prepared.Watermark},
		{"source_snapshot_ref", s.Prepared.Ref},
		{"expires_at", s.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")},
		{"precision", PrecisionNote},
		{"inexact_cells", strconv.Itoa(inexact)},
		{"layout_id", table.LayoutID},
		{"layout_version", layoutVersion(table.LayoutVersion)},
		{"layout_title", table.Title},
		{"layout_footnote", table.Footnote},
		{"subtotal_by", p.SubtotalBy},
		{"field", "type", "scale", "currency", "unit"},
	}
	for _, c := range s.Result.Columns {
		metadata = append(metadata, []string{c.ID, c.Type, strconv.Itoa(c.Scale), c.Currency, c.Unit})
	}
	for i, row := range metadata {
		if e := putText("Metadata", i+1, row); e != nil {
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
	last, e := excelize.ColumnNumberToName(max(len(head), 6))
	if e != nil {
		return nil, e
	}
	if e = f.SetColWidth("Data", "A", last, 28); e != nil {
		return nil, e
	}
	for _, name := range []string{"Totals", "Metadata"} {
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

// layoutVersion is the version as the Metadata sheet writes it: empty without a layout.
func layoutVersion(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}

var _ r.PresentedWriter = Writer{}
