// Package xlsx renders a generic typed snapshot without product field names.
//
// Since S23-A (docs/ADR_0024_TABLE_REPORT_FORMATTING.md decision 3) every workbook, with or without a layout, writes a decimal or integer
// column as numeric cells with an Excel number format, each followed by a text column `<label>（精确）` that holds the exact frozen string.
// A numeric cell is written only when the exact string survives a float64 and comes back as the same digits at the column's scale; a value
// that does not is written as text in the numeric column too, and the Metadata sheet counts those cells. The workbook stays a record: the
// subtotal rows of a layout are values, never formulas, and no formula is ever generated.
//
// Since S20-C (docs/ADR_0023_PUBLISHED_FILTERS_AND_SORTS.md decision 6) the Metadata sheet ends with the scope of the snapshot's query:
// the `query_scope` row, which every workbook has and which counts the query's filters and sort keys (0 and 0 for a query without them,
// so that a reader can tell a workbook of the whole dataset from one written before the row existed), then a row per filter and a row
// per sort key, in the query's normalized order, each with the words and the exact field, operator and value beside them.
package xlsx

import (
	"context"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"idp.local/reporting/export/present"
	r "idp.local/reporting/reporting"
)

// The suffix of the exact text column beside each numeric column, and the precision note of the Metadata sheet.
const (
	ExactSuffix   = "（精确）"
	PrecisionNote = "Decimal and integer columns are numeric cells for arithmetic when their exact digits survive a float64 (else text, counted in inexact_cells); the （精确） text columns beside them hold the exact frozen strings for reconciliation. Subtotal rows are values; no formulas generated."
)

// DataClassNotes is what the `data_class` row of the Metadata sheet says for the snapshot's data class (S08-A, ADR 0012 decision 6): where
// the data comes from, in the word of the adapter it was read through, one note for each of reporting.DataClasses. The simulator's note
// is what every workbook said before the class was the adapter's word. A source product's data is called that and nothing more: the
// platform cannot know more of it. A stored word outside the set is an integrity failure, never written as it is.
var DataClassNotes = map[string]string{
	r.DataClassSimulator: "CONTRACT SIMULATOR / NOT REAL BUSINESS DATA",
	r.DataClassManual:    "MANUAL ENTRY / NOT FROM A SOURCE PRODUCT",
	r.DataClassBusiness:  "SOURCE PRODUCT DATA",
}

// The rows of the query's scope at the end of the Metadata sheet (S20-C). The scope row is `query_scope | filters | <n> | sort_keys | <m>`:
// its third cell counts the query's filters and its fifth the sort keys, as text like every cell of the sheet. The n rows after it are
// the filters, `filter_<i> | <words> | <field> | <operator> | <value>`, and the m rows after those the sort keys,
// `sort_<i> | <words> | <field> | <direction>`, i from 1 in the query's normalized order: the words are English, with the column's label
// (present.ScopeFilter.English), and the three cells beside them are exactly what the query has, the value of an `in` or a `between` the
// JSON array of strings it carries.
const (
	ScopeKey      = "query_scope"
	ScopeFilters  = "filters"
	ScopeSortKeys = "sort_keys"
	FilterKey     = "filter_"
	SortKey       = "sort_"
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
	// The data class the Metadata sheet says (S08-A): the snapshot's own word, which must be one of the three.
	dataClass, known := DataClassNotes[s.Result.Evidence.DataClass]
	if !known {
		return nil, fmt.Errorf("xlsx: the snapshot's data class is not one the platform knows: %w", r.ErrIntegrity)
	}
	table, e := present.Build(s, p)
	if e != nil {
		return nil, e
	}
	// The scope of the query (S20-C), the query's own whatever the presentation shows, as the rows that end the Metadata sheet: the
	// scope row, always, then the filters and the sort keys. A cell holds at most excelize.TotalCellChars characters and the library
	// cuts a longer text without saying so; a scope text that long (only a column label beyond every sensible length makes one) is
	// refused, never cut.
	scope, e := present.ScopeOf(s)
	if e != nil {
		return nil, e
	}
	scopeRows := [][]string{{ScopeKey, ScopeFilters, strconv.Itoa(len(scope.Filters)), ScopeSortKeys, strconv.Itoa(len(scope.Sort))}}
	for i, filter := range scope.Filters {
		scopeRows = append(scopeRows, []string{FilterKey + strconv.Itoa(i+1), filter.English(), filter.Field, filter.Operator, filter.Value})
	}
	for i, key := range scope.Sort {
		scopeRows = append(scopeRows, []string{SortKey + strconv.Itoa(i+1), key.English(), key.Field, key.Direction})
	}
	for _, row := range scopeRows {
		for _, v := range row {
			if utf8.RuneCountInString(v) > excelize.TotalCellChars {
				return nil, fmt.Errorf("xlsx: a filter or a sort key of the query is longer than a cell holds: %w", r.ErrUnsupported)
			}
		}
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
		{"data_class", dataClass},
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
	// The scope of the query, after every row the sheet had before S20-C, so that no cell of those moves.
	metadata = append(metadata, scopeRows...)
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
