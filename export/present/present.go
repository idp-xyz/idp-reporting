// Package present is the presentation step the exports share (S23-A, docs/ADR_0024_TABLE_REPORT_FORMATTING.md): from a frozen snapshot
// and a presentation it picks and orders the columns, labels them, formats the numeric values, partitions the rows into subtotal groups
// with their exact sums, and evaluates the marks. The PDF and the workbook writers lay out what it returns and compute nothing themselves.
//
// It is standard library only and decides no business value: a format changes how the digits of a frozen value are shown, never which
// digits (no rounding, scaling or unit conversion); a subtotal is a presentation sum of the printed values by the rule the document tables
// use for their totals (Sum: exact decimal strings added as scaled integers); a mark colours a cell and hides, changes or adds nothing;
// the source's totals are shown as they came and never recomputed or reconciled. A presentation that does not fit the snapshot is refused
// whole (reporting.ErrIntegrity: the frozen presentation was checked against the frozen snapshot at admission, so a mismatch here is a
// store that does not agree with itself), never applied in part.
package present

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	r "idp.local/reporting/reporting"
)

// The labels of the rows the presentation adds and the dash a zero may be shown as. They are data to the writers: the PDF checks them
// against its fonts like every value.
const (
	SubtotalLabel = "小计"
	TotalLabel    = "合计"
	ZeroDash      = "—" // U+2014, the dash of a nil amount in a Chinese ledger
)

// Palette is the closed set of mark colours (ADR 0024 decision 1), as RGB hex without the number sign: light, print-safe tints under which
// black text stays legible; the same on screen (the console copies them), in the PDF and in the workbook.
var Palette = map[string]string{
	r.MarkGood: "D9EAD3",
	r.MarkWarn: "FFF2CC",
	r.MarkBad:  "F4CCCC",
	r.MarkNote: "CFE2F3",
}

// Table is a snapshot as a presentation shows it: the shown columns in order, the rows in print order (the snapshot's rows in their order,
// each subtotal group followed by its subtotal row), the source's totals for the shown columns, and the texts around the table.
type Table struct {
	Title, Footnote string
	LayoutID        string
	LayoutVersion   int
	Columns         []Column
	Rows            []Row
	// Totals is the source's total row for the shown columns: the total of each shown metric that has one (formatted when the column is
	// numeric, verbatim otherwise), an empty cell elsewhere; nil when no shown column has a total. TotalLabelAt is the index of the shown
	// column that takes the 合计 label, the first that is not a metric with a total, or -1 when every shown column is one (the writer then
	// lists the totals instead of a row).
	Totals       []Cell
	TotalLabelAt int
}

// Column is one shown column: the snapshot column it shows (Source, with its index in the snapshot), the presentation's label (Label, ""
// keeps the dataset's), its alignment, and, for a numeric column, its resolved format. Metric says whether the column is one of the
// query's metrics with a numeric type: the columns a subtotal row sums and the total row carries.
type Column struct {
	Index  int
	Source r.Column
	Label  string
	Align  string
	Format r.NumberFormat
	Metric bool
}

// Header is the column's header label without a currency or unit suffix: the presentation's or the dataset's.
func (c Column) Header() string {
	if c.Label != "" {
		return c.Label
	}
	return c.Source.Label
}

// Numeric says whether the column holds exact decimal strings.
func (c Column) Numeric() bool { return r.NumericColumnType(c.Source.Type) }

// Row is one printed row: a row of the snapshot (Index is its index there) or the subtotal row of a group (Subtotal, Index -1, Group the
// exact key the group shares).
type Row struct {
	Index    int
	Subtotal bool
	Group    string
	Cells    []Cell
}

// Cell is one printed cell: the exact string it stands for (the frozen value, or the exact sum of a subtotal; "" where the row has none,
// as in the label cell of a subtotal row), the text as printed, and the mark the rules gave it ("" for none).
type Cell struct {
	Exact string
	Text  string
	Mark  string
}

// exactNumber is the form of the integer and decimal values a published snapshot holds.
var exactNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// Whole is the presentation of the whole snapshot as the exports have always shown it: every column in catalogue order under the
// dataset's label, numeric columns right-aligned and the rest left, metrics grouped, no subtotals, no marks, no title and no footnote.
// Build with it is what a writer renders without a layout.
func Whole(s r.Snapshot) r.Presentation {
	p := r.Presentation{}
	for _, c := range s.Result.Columns {
		align := r.AlignLeft
		if r.NumericColumnType(c.Type) {
			align = r.AlignRight
		}
		p.Columns = append(p.Columns, r.PresentedColumn{ID: c.ID, Align: align})
	}
	return p
}

// Build presents a snapshot through a presentation. The presentation must be whole (reporting.Presentation.Validate) and fit the snapshot
// (Fits); a value that is not the exact number its column promises is reporting.ErrIntegrity. Build never computes, rounds or changes a
// value: the texts are the frozen strings, grouped and signed as the formats say, and the subtotals are exact sums of the frozen strings.
func Build(s r.Snapshot, p r.Presentation) (*Table, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	cols, rows := s.Result.Columns, s.Result.Rows
	if e := p.Fits(cols, s.Query.Dimensions); e != nil {
		return nil, fmt.Errorf("the frozen presentation does not fit the frozen snapshot: %w", r.ErrIntegrity)
	}
	metrics := map[string]bool{}
	for _, m := range s.Query.Metrics {
		metrics[m.ID] = true
	}
	index := map[string]int{}
	for j, c := range cols {
		index[c.ID] = j
	}
	t := &Table{Title: p.Title, Footnote: p.Footnote, LayoutID: p.LayoutID, LayoutVersion: p.LayoutVersion, TotalLabelAt: -1}
	for _, pc := range p.Columns {
		j := index[pc.ID]
		c := Column{Index: j, Source: cols[j], Label: pc.Label, Align: pc.Align, Format: r.PlainNumberFormat, Metric: metrics[pc.ID] && r.NumericColumnType(cols[j].Type)}
		// Without a format of its own a metric is grouped and a numeric dimension is left as it is, as the exports always printed them.
		if c.Metric {
			c.Format = r.DefaultNumberFormat
		}
		if pc.Format != nil {
			c.Format = *pc.Format
		}
		t.Columns = append(t.Columns, c)
	}
	// The marks, by shown column, in their order: the first rule that holds colours the cell.
	marks := map[int][]r.Mark{}
	for _, m := range p.Marks {
		for k, c := range t.Columns {
			if c.Source.ID == m.Column {
				marks[k] = append(marks[k], m)
			}
		}
	}
	// The rows, each group's subtotal row after its last row. The groups are the exact frozen strings of the subtotal column, ordered by
	// their first row (ADR 0004 decision 2); the rows keep the snapshot's order inside them. Without subtotals every row is one group of
	// its own and no subtotal row is added.
	by := -1
	if p.SubtotalBy != "" {
		by = index[p.SubtotalBy]
	}
	type group struct {
		key  string
		rows []int
	}
	var groups []*group
	position := map[string]*group{}
	for i, row := range rows {
		if len(row) != len(cols) {
			return nil, r.ErrIntegrity
		}
		if by < 0 {
			groups = append(groups, &group{rows: []int{i}})
			continue
		}
		key := row[by]
		g, ok := position[key]
		if !ok {
			g = &group{key: key}
			position[key] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, i)
	}
	for _, g := range groups {
		var sums []*Sum
		if by >= 0 {
			sums = make([]*Sum, len(t.Columns))
			for k, c := range t.Columns {
				if c.Metric {
					sums[k] = NewSum(c.Source.Scale)
				}
			}
		}
		for _, i := range g.rows {
			out := Row{Index: i, Cells: make([]Cell, len(t.Columns))}
			for k, c := range t.Columns {
				v := rows[i][c.Index]
				cell, e := c.cell(v)
				if e != nil {
					return nil, e
				}
				if cell.Mark, e = c.mark(v, marks[k]); e != nil {
					return nil, e
				}
				if sums != nil && sums[k] != nil {
					if e := sums[k].Add(v); e != nil {
						return nil, e
					}
				}
				out.Cells[k] = cell
			}
			t.Rows = append(t.Rows, out)
		}
		if by >= 0 {
			sub := Row{Index: -1, Subtotal: true, Group: g.key, Cells: make([]Cell, len(t.Columns))}
			labelled := false
			for k, c := range t.Columns {
				switch {
				case sums[k] != nil:
					v := sums[k].String()
					sub.Cells[k] = Cell{Exact: v, Text: Format(v, c.Format)}
				case !labelled:
					sub.Cells[k] = Cell{Text: SubtotalLabel}
					labelled = true
				}
			}
			t.Rows = append(t.Rows, sub)
		}
	}
	// The source's totals, as they came, for the shown metrics: a numeric one formatted as its column says, any other verbatim.
	totals := make([]Cell, len(t.Columns))
	some := false
	for k, c := range t.Columns {
		v, ok := s.Result.Totals[c.Source.ID]
		if !metrics[c.Source.ID] || !ok {
			if t.TotalLabelAt < 0 {
				t.TotalLabelAt = k
			}
			continue
		}
		cell, e := c.cell(v)
		if e != nil {
			return nil, e
		}
		totals[k] = cell
		some = true
	}
	if some {
		t.Totals = totals
		if t.TotalLabelAt >= 0 {
			t.Totals[t.TotalLabelAt] = Cell{Text: TotalLabel}
		}
	} else {
		t.TotalLabelAt = -1
	}
	return t, nil
}

// cell is the printed cell of a frozen value in the column: a numeric value formatted, any other value as it is.
func (c Column) cell(v string) (Cell, error) {
	if !c.Numeric() {
		return Cell{Exact: v, Text: v}, nil
	}
	if !exactNumber.MatchString(v) {
		return Cell{}, fmt.Errorf("a value of a numeric column is not an exact number: %w", r.ErrIntegrity)
	}
	return Cell{Exact: v, Text: Format(v, c.Format)}, nil
}

// mark is the mark of the first rule in rules that holds for the value, or "".
func (c Column) mark(v string, rules []r.Mark) (string, error) {
	for _, m := range rules {
		holds, e := Holds(m, v, c.Numeric())
		if e != nil {
			return "", e
		}
		if holds {
			return m.Mark, nil
		}
	}
	return "", nil
}

// Holds says whether the mark's relation holds for a value: an exact decimal comparison on a numeric column, an exact string comparison
// (eq, ne) or emptiness (blank) on any other. A numeric value that is not an exact number is reporting.ErrIntegrity.
func Holds(m r.Mark, v string, numeric bool) (bool, error) {
	if numeric {
		if m.When == r.WhenBlank {
			return false, fmt.Errorf("a numeric column has no blank: %w", r.ErrIntegrity)
		}
		c, e := Compare(v, m.Value)
		if e != nil {
			return false, e
		}
		switch m.When {
		case r.WhenLess:
			return c < 0, nil
		case r.WhenLessEqual:
			return c <= 0, nil
		case r.WhenGreater:
			return c > 0, nil
		case r.WhenGreaterEqual:
			return c >= 0, nil
		case r.WhenEqual:
			return c == 0, nil
		case r.WhenNotEqual:
			return c != 0, nil
		}
		return false, fmt.Errorf("an unknown relation: %w", r.ErrIntegrity)
	}
	switch m.When {
	case r.WhenEqual:
		return v == m.Value, nil
	case r.WhenNotEqual:
		return v != m.Value, nil
	case r.WhenBlank:
		return v == "", nil
	}
	return false, fmt.Errorf("a relation that does not fit the column: %w", r.ErrIntegrity)
}

// Format shows an exact decimal or integer string as the format says: thousands separators in the integer part when Grouping, a negative
// with a minus sign or in parentheses, a zero as its digits or as a dash. It changes no digit and adds none; a string that is not an exact
// number is returned as it is.
func Format(v string, f r.NumberFormat) string {
	if !exactNumber.MatchString(v) {
		return v
	}
	sign, rest := "", v
	if strings.HasPrefix(rest, "-") {
		sign, rest = "-", rest[1:]
	}
	if f.Zero == r.ZeroDash && strings.Trim(rest, "0.") == "" {
		return ZeroDash
	}
	if f.Grouping {
		rest = GroupDigits(rest)
	}
	if sign != "" && f.Negative == r.NegativeParentheses {
		return "(" + rest + ")"
	}
	return sign + rest
}

// GroupDigits inserts thousands separators into the integer part of an exact decimal string, the sign included or not. It changes no digit.
func GroupDigits(v string) string {
	sign, rest := "", v
	if strings.HasPrefix(rest, "-") {
		sign, rest = "-", rest[1:]
	}
	whole, frac, dot := strings.Cut(rest, ".")
	var b strings.Builder
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if dot {
		return sign + b.String() + "." + frac
	}
	return sign + b.String()
}

// ExcelNumberFormat is the number format of a workbook cell of the column's scale shown as f says: a positive section, a negative section
// (a minus sign or parentheses) and, when a zero is a dash, a zero section (ADR 0024 decision 3). It is a display format: the cell keeps
// its exact digits.
func ExcelNumberFormat(scale int, f r.NumberFormat) string {
	digits := "0"
	if f.Grouping {
		digits = "#,##0"
	}
	if scale > 0 {
		digits += "." + strings.Repeat("0", scale)
	}
	out := digits + ";-" + digits
	if f.Negative == r.NegativeParentheses {
		out = digits + ";(" + digits + ")"
	}
	if f.Zero == r.ZeroDash {
		out += `;"` + ZeroDash + `"`
	}
	return out
}

// RoundTrips says whether an exact decimal or integer string survives a float64 and comes back as the same digits at the column's scale:
// the rule under which a workbook writes a numeric cell (ADR 0024 decision 3). A value that does not is written as text.
func RoundTrips(v string, scale int) bool {
	if !exactNumber.MatchString(v) || len(v) > 128 {
		return false
	}
	f, e := strconv.ParseFloat(v, 64)
	if e != nil {
		return false
	}
	return strconv.FormatFloat(f, 'f', scale, 64) == v
}

// Sum accumulates exact decimal strings, as the document tables' totals and carries do: the sum is an integer scaled to the longest
// fraction seen (or the column's declared scale), so no digit is ever rounded. It is the one decimal arithmetic of the exports.
type Sum struct {
	scale int // the column's declared scale, if any
	frac  int // the longest fraction seen; sum is scaled by 10^frac
	sum   big.Int
}

// NewSum is an empty sum for a column of the given scale.
func NewSum(scale int) *Sum { return &Sum{scale: scale} }

// Add adds an exact decimal string; anything else is reporting.ErrIntegrity.
func (c *Sum) Add(v string) error {
	if !exactNumber.MatchString(v) {
		return r.ErrIntegrity
	}
	_, f, _ := strings.Cut(v, ".")
	if len(f) > c.frac {
		c.sum.Mul(&c.sum, pow10(len(f)-c.frac))
		c.frac = len(f)
	}
	c.sum.Add(&c.sum, scaled(v, c.frac))
	return nil
}

// Frac is the number of fraction digits the sum is scaled by.
func (c *Sum) Frac() int { return c.frac }

// Copy is an independent sum with the same scale, fraction and value.
func (c *Sum) Copy() *Sum {
	out := &Sum{scale: c.scale, frac: c.frac}
	out.sum.Set(&c.sum)
	return out
}

// Zero is the empty sum of the same scale and fraction: what a sum shows before anything was added.
func (c *Sum) Zero() *Sum { return &Sum{scale: c.scale, frac: c.frac} }

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// scaled is v as an integer with frac fraction digits.
func scaled(v string, frac int) *big.Int {
	whole, f, _ := strings.Cut(v, ".")
	f += strings.Repeat("0", frac-len(f))
	n, _ := new(big.Int).SetString(whole+f, 10)
	return n
}

// String is the sum with the column's fraction digits.
func (c *Sum) String() string {
	frac := max(c.frac, c.scale)
	digits := new(big.Int).Abs(&c.sum)
	if frac > c.frac {
		digits.Mul(digits, pow10(frac-c.frac))
	}
	s := digits.String()
	if frac > 0 {
		for len(s) <= frac {
			s = "0" + s
		}
		s = s[:len(s)-frac] + "." + s[len(s)-frac:]
	}
	if c.sum.Sign() < 0 {
		s = "-" + s
	}
	return s
}

// Equals compares the sum with another exact decimal string numerically.
func (c *Sum) Equals(v string) bool {
	if !exactNumber.MatchString(v) {
		return false
	}
	_, f, _ := strings.Cut(v, ".")
	frac := max(c.frac, c.scale, len(f))
	return scaled(c.String(), frac).Cmp(scaled(v, frac)) == 0
}

// Compare compares two exact decimal strings numerically (-1, 0, 1); a string that is not an exact number is reporting.ErrIntegrity.
func Compare(a, b string) (int, error) {
	if !exactNumber.MatchString(a) || !exactNumber.MatchString(b) {
		return 0, fmt.Errorf("a comparison of something that is not an exact number: %w", r.ErrIntegrity)
	}
	_, fa, _ := strings.Cut(a, ".")
	_, fb, _ := strings.Cut(b, ".")
	frac := max(len(fa), len(fb))
	return scaled(a, frac).Cmp(scaled(b, frac)), nil
}
