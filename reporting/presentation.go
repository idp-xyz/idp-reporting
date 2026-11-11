package reporting

import (
	"fmt"
	"regexp"
	"slices"
	"unicode/utf8"
)

// Presentation is how an export shows a snapshot (S23-A, docs/ADR_0024_TABLE_REPORT_FORMATTING.md): which columns in which order, under
// which labels, aligned and formatted how, the rows partitioned into subtotal groups by one column, cells marked by rules, a title above
// the table and a footnote under its total row. It changes how the frozen values are shown and never which values: no digit is rounded,
// scaled or converted, no row is hidden, and the subtotals are presentation sums of the printed values (ADR 0004 decision 6). An export
// job freezes it at admission, as an issue job freezes its template, and names where it came from: LayoutID and LayoutVersion identify
// the saved layout version, so that the file is reproducible and the view can say which layout it printed. The core knows nothing of
// layouts: a Presentation is what the HTTP layer made of one.
type Presentation struct {
	LayoutID      string `json:",omitempty"`
	LayoutVersion int    `json:",omitempty"`
	Title         string `json:",omitempty"`
	Footnote      string `json:",omitempty"`
	Columns       []PresentedColumn
	// SubtotalBy is the id of the column whose exact frozen strings partition the rows into groups, each followed by a subtotal row; "" for
	// no subtotals. It is one of Columns and, of the snapshot, a dimension (Fits).
	SubtotalBy string `json:",omitempty"`
	Marks      []Mark `json:",omitempty"`
}

// PresentedColumn is one shown column: the snapshot column it shows, the label that replaces the dataset's ("" keeps it), its alignment
// (left, center or right) and, for a decimal or integer column, how its digits are shown.
type PresentedColumn struct {
	ID     string
	Label  string `json:",omitempty"`
	Align  string
	Format *NumberFormat `json:",omitempty"`
}

// NumberFormat says how an exact decimal or integer string is shown: with or without thousands grouping, a negative with a minus sign or in
// parentheses, a zero as its digits or as a dash. It changes no digit.
type NumberFormat struct {
	Grouping bool
	Negative string // NegativeMinus or NegativeParentheses
	Zero     string // ZeroDigits or ZeroDash
}

// DefaultNumberFormat is how a numeric metric is shown when its presentation says nothing: grouped, a minus sign, the digits of a zero
// (how the exports have always printed a metric). PlainNumberFormat is how a numeric column that is not a metric (a year, a count used as
// a dimension) is shown when its presentation says nothing: its digits as they are, as the exports have always printed a dimension.
var (
	DefaultNumberFormat = NumberFormat{Grouping: true, Negative: NegativeMinus, Zero: ZeroDigits}
	PlainNumberFormat   = NumberFormat{Grouping: false, Negative: NegativeMinus, Zero: ZeroDigits}
)

// Mark is one conditional rule: the cells of Column whose value stands in the relation When to Value get the mark. On a numeric column the
// comparison is an exact decimal one (Value an exact decimal); on any other column When is eq, ne or blank, comparing the exact frozen string
// (blank has no Value). A mark colours a cell and nothing else.
type Mark struct {
	Column string
	When   string
	Value  string `json:",omitempty"`
	Mark   string
}

// The words of a presentation.
const (
	AlignLeft   = "left"
	AlignCenter = "center"
	AlignRight  = "right"

	NegativeMinus       = "minus"
	NegativeParentheses = "parentheses"
	ZeroDigits          = "digits"
	ZeroDash            = "dash"

	WhenLess         = "lt"
	WhenLessEqual    = "le"
	WhenGreater      = "gt"
	WhenGreaterEqual = "ge"
	WhenEqual        = "eq"
	WhenNotEqual     = "ne"
	WhenBlank        = "blank"

	MarkGood = "good"
	MarkWarn = "warn"
	MarkBad  = "bad"
	MarkNote = "note"
)

// The bounds of a presentation (ADR 0024 decision 1).
const (
	MaxPresentedColumns = 100
	MaxMarks            = 8
	MaxTitleRunes       = 120
	MaxFootnoteRunes    = 400
	MaxColumnLabelRunes = 64
	MaxMarkValueRunes   = 128
)

var (
	alignments    = []string{AlignLeft, AlignCenter, AlignRight}
	negatives     = []string{NegativeMinus, NegativeParentheses}
	zeros         = []string{ZeroDigits, ZeroDash}
	orderedWhens  = []string{WhenLess, WhenLessEqual, WhenGreater, WhenGreaterEqual}
	equalityWhens = []string{WhenEqual, WhenNotEqual}
	markNames     = []string{MarkGood, MarkWarn, MarkBad, MarkNote}
	// exactDecimal is the form of an integer or decimal value a snapshot holds, and so of the value a numeric mark compares with.
	exactDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
)

// NumericColumnType says whether a column of the type holds exact decimal strings (decimal or integer): the columns that may carry a
// NumberFormat, that are summed in a subtotal row and that a mark compares numerically.
func NumericColumnType(t string) bool { return t == "decimal" || t == "integer" }

// Literal says whether s is a text a presentation may print: valid UTF-8 of at most max characters without control characters.
func Literal(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, ch := range s {
		if ch < 32 || ch == 0x7F || ch == 0xFFFE || ch == 0xFFFF {
			return false
		}
	}
	return true
}

// Valid says whether the format names its negative and zero forms.
func (f NumberFormat) Valid() bool {
	return slices.Contains(negatives, f.Negative) && slices.Contains(zeros, f.Zero)
}

// Validate holds a presentation to its own shape (ErrInvalid otherwise): a layout identity that is whole or absent, a title and a footnote
// within their bounds, 1 to 100 distinct columns with a known alignment, a label within its bound and a whole format, a subtotal column
// among the columns, and at most 8 marks, each on a column among the columns with a known relation and mark, a value absent for blank and
// an exact decimal for an ordered relation. What needs the snapshot (a format on a numeric column, a dimension to subtotal by, a relation
// that fits the column's type) is Fits.
func (p Presentation) Validate() error {
	if (p.LayoutID == "") != (p.LayoutVersion == 0) || (p.LayoutID != "" && (!validID(p.LayoutID) || p.LayoutVersion < 1)) {
		return fmt.Errorf("the presentation names half a layout version: %w", ErrInvalid)
	}
	if !Literal(p.Title, MaxTitleRunes) || !Literal(p.Footnote, MaxFootnoteRunes) {
		return fmt.Errorf("the presentation's title or footnote is not a printable text within its bound: %w", ErrInvalid)
	}
	if len(p.Columns) == 0 || len(p.Columns) > MaxPresentedColumns {
		return fmt.Errorf("the presentation shows no column or more than %d: %w", MaxPresentedColumns, ErrInvalid)
	}
	seen := map[string]bool{}
	for i, c := range p.Columns {
		if c.ID == "" || len(c.ID) > 128 || seen[c.ID] || !slices.Contains(alignments, c.Align) || !Literal(c.Label, MaxColumnLabelRunes) || (c.Format != nil && !c.Format.Valid()) {
			return fmt.Errorf("presented column %d is not whole: %w", i, ErrInvalid)
		}
		seen[c.ID] = true
	}
	if p.SubtotalBy != "" && !seen[p.SubtotalBy] {
		return fmt.Errorf("the subtotal column is not a presented column: %w", ErrInvalid)
	}
	if len(p.Marks) > MaxMarks {
		return fmt.Errorf("more than %d marks: %w", MaxMarks, ErrInvalid)
	}
	for i, m := range p.Marks {
		ordered, equality := slices.Contains(orderedWhens, m.When), slices.Contains(equalityWhens, m.When)
		if !seen[m.Column] || !slices.Contains(markNames, m.Mark) || !(ordered || equality || m.When == WhenBlank) || !Literal(m.Value, MaxMarkValueRunes) {
			return fmt.Errorf("mark %d is not whole: %w", i, ErrInvalid)
		}
		if (m.When == WhenBlank && m.Value != "") || (ordered && !exactDecimal.MatchString(m.Value)) {
			return fmt.Errorf("mark %d compares with a value its relation cannot take: %w", i, ErrInvalid)
		}
	}
	return nil
}

// Fits says whether a valid presentation can be applied to a result of the given columns, of which dimensions are the dimension columns
// (ErrUnsupported otherwise): every presented column is one of them, a format sits on a decimal or integer column only, the subtotal column
// is a dimension, and a mark compares as its column's type allows: an ordered or equality relation with an exact decimal on a numeric column,
// eq, ne or blank on any other. Nothing is dropped to make a presentation fit: the whole presentation is refused.
func (p Presentation) Fits(columns []Column, dimensions []string) error {
	byID := map[string]Column{}
	for _, c := range columns {
		byID[c.ID] = c
	}
	for _, pc := range p.Columns {
		c, ok := byID[pc.ID]
		if !ok {
			return fmt.Errorf("the presentation shows a column the result does not have: %w", ErrUnsupported)
		}
		if pc.Format != nil && !NumericColumnType(c.Type) {
			return fmt.Errorf("a number format on a column that is not numeric: %w", ErrUnsupported)
		}
	}
	if p.SubtotalBy != "" && !slices.Contains(dimensions, p.SubtotalBy) {
		return fmt.Errorf("the subtotal column is not a dimension: %w", ErrUnsupported)
	}
	for _, m := range p.Marks {
		c, ok := byID[m.Column]
		if !ok {
			return fmt.Errorf("a mark on a column the result does not have: %w", ErrUnsupported)
		}
		if NumericColumnType(c.Type) {
			if m.When == WhenBlank || !exactDecimal.MatchString(m.Value) {
				return fmt.Errorf("a mark compares a numeric column with something that is not an exact decimal: %w", ErrUnsupported)
			}
		} else if slices.Contains(orderedWhens, m.When) {
			return fmt.Errorf("a mark orders a column that is not numeric: %w", ErrUnsupported)
		}
	}
	return nil
}

// Clone is a deep copy, so that a frozen presentation shares nothing with its origin.
func (p Presentation) Clone() Presentation {
	out := p
	out.Columns = make([]PresentedColumn, len(p.Columns))
	for i, c := range p.Columns {
		out.Columns[i] = c
		if c.Format != nil {
			f := *c.Format
			out.Columns[i].Format = &f
		}
	}
	if p.Marks != nil {
		out.Marks = slices.Clone(p.Marks)
	}
	return out
}

// LayoutRef names a layout version in a view (S23-A).
type LayoutRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// layoutOf is the layout version an export job's presentation names, or nothing.
func layoutOf(p *Presentation) (string, int) {
	if p == nil {
		return "", 0
	}
	return p.LayoutID, p.LayoutVersion
}
