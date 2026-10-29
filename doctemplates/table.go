package doctemplates

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	r "idp.local/reporting/reporting"
)

// Table is the detail table of a schema 1.3 document (S03-F): a region in millimetres that
// lists every row of the frozen snapshot over as many pages as it needs, and the fields it shows.
type Table struct {
	X        float64       `json:"x"`
	Y        float64       `json:"y"`
	Width    float64       `json:"width"`
	Height   float64       `json:"height"`
	FontSize int           `json:"font_size"`
	Columns  []TableColumn `json:"columns"`
}

type TableColumn struct {
	Field string  `json:"field"`
	Width float64 `json:"width"` // millimetres; the columns add up to the table's width
}

// Table limits: columns, their least width, the least region height, the type sizes.
const (
	MaxTableColumns  = 12
	MinColumnWidth   = 8.0
	MinTableHeight   = 30.0
	MinTableFontSize = 8
	MaxTableFontSize = 12
)

func (t *Table) UnmarshalJSON(data []byte) error {
	type wire Table
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		return e
	}
	for _, k := range []string{"x", "y", "width", "height", "font_size", "columns"} {
		if fields[k] == nil {
			return r.ErrInvalid
		}
	}
	*t = Table(value)
	return nil
}

func (c *TableColumn) UnmarshalJSON(data []byte) error {
	type wire TableColumn
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		return e
	}
	if len(fields) != 2 || fields["field"] == nil || fields["width"] == nil {
		return r.ErrInvalid
	}
	*c = TableColumn(value)
	return nil
}

// validateTable checks the region (inside the safe region, with the print offsets too, and
// overlapping no text box), the type size and the columns (1..12 distinct fields, each at
// least 8 mm, adding up to the table's width).
func validateTable(t Table, print *PrintProfile, elements []Element) error {
	for _, n := range []float64{t.X, t.Y, t.Width, t.Height} {
		if !grid(n) {
			return r.ErrInvalid
		}
	}
	if t.X < 10 || t.Y < 25 || t.Width < MinColumnWidth || t.Height < MinTableHeight || tenths(t.X)+tenths(t.Width) > 2000 || tenths(t.Y)+tenths(t.Height) > 2700 || t.FontSize < MinTableFontSize || t.FontSize > MaxTableFontSize || len(t.Columns) < 1 || len(t.Columns) > MaxTableColumns {
		return r.ErrInvalid
	}
	if print != nil {
		x, y := tenths(t.X)+tenths(print.OffsetX), tenths(t.Y)+tenths(print.OffsetY)
		if x < 100 || y < 250 || x+tenths(t.Width) > 2000 || y+tenths(t.Height) > 2700 {
			return r.ErrInvalid
		}
	}
	total := 0
	seen := map[string]bool{}
	for _, c := range t.Columns {
		if c.Field == "" || len(c.Field) > 128 || seen[c.Field] || !grid(c.Width) || c.Width < MinColumnWidth {
			return r.ErrInvalid
		}
		seen[c.Field] = true
		total += tenths(c.Width)
	}
	if total != tenths(t.Width) {
		return r.ErrInvalid
	}
	for _, e := range elements {
		if tenths(t.X) < tenths(e.X)+tenths(e.Width) && tenths(e.X) < tenths(t.X)+tenths(t.Width) && tenths(t.Y) < tenths(e.Y)+tenths(e.Height) && tenths(e.Y) < tenths(t.Y)+tenths(t.Height) {
			return r.ErrInvalid
		}
	}
	return nil
}

// TableData is what the renderer lays out: the chosen columns of the snapshot, every row's
// values for them in snapshot order, and the snapshot's whole-scope totals of those that are
// metrics, all exact strings from the frozen snapshot.
type TableData struct {
	Columns []r.Column
	Metric  []bool // whether the column is one of the query's metrics
	Rows    [][]string
	Totals  map[string]string
	// Index is the snapshot row index of each row of Rows, for the refusal details of a layout. A
	// 1.x table lists every row of the snapshot, and then it is nil: row i is snapshot row i. The
	// rows of a 2.0 document's group are not consecutive, and their indices are here.
	Index []int
}

// SnapshotRow returns the snapshot row index of row i of the table.
func (t *TableData) SnapshotRow(i int) int {
	if i >= 0 && i < len(t.Index) {
		return t.Index[i]
	}
	return i
}

// BindTable takes the table's columns and rows from an already authorized frozen snapshot,
// never caller values. A field the snapshot lacks is a denial, as in Bind; a cell the
// single-line preview could not print is accepted here, as cells wrap.
func BindTable(d Document, s r.Snapshot) (*TableData, error) {
	if d.Banded() {
		return nil, fmt.Errorf("doctemplates: the table of a 2.x document is bound by row group: %w", r.ErrUnsupported)
	}
	if d.Table == nil {
		return nil, nil
	}
	if d.Binding.Integration != s.Owner.Integration || d.Binding.Dataset != s.Query.Dataset {
		return nil, r.ErrDenied
	}
	index := map[string]int{}
	for i, c := range s.Result.Columns {
		index[c.ID] = i
	}
	metrics := map[string]bool{}
	for _, m := range s.Query.Metrics {
		metrics[m.ID] = true
	}
	out := &TableData{Totals: map[string]string{}}
	picked := make([]int, len(d.Table.Columns))
	for i, c := range d.Table.Columns {
		j, ok := index[c.Field]
		if !ok {
			return nil, r.ErrDenied
		}
		picked[i] = j
		out.Columns = append(out.Columns, s.Result.Columns[j])
		out.Metric = append(out.Metric, metrics[c.Field])
		if v, ok := s.Result.Totals[c.Field]; ok {
			out.Totals[c.Field] = v
		}
	}
	out.Rows = make([][]string, len(s.Result.Rows))
	for i, row := range s.Result.Rows {
		out.Rows[i] = make([]string, len(picked))
		for k, j := range picked {
			if j >= len(row) || !utf8.ValidString(row[j]) {
				return nil, r.ErrIntegrity
			}
			out.Rows[i][k] = row[j]
		}
	}
	return out, nil
}
