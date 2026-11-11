// Package layouts validates private screen report column layouts.
//
// Schema 1.0 picks, orders and aligns the columns of a dataset. Schema 1.1 (S23-A, docs/ADR_0024_TABLE_REPORT_FORMATTING.md decision 1)
// adds, all optional, what the exports print: a title and a footnote, a column's label, a centre alignment, a number format on a decimal or
// integer column, one level of subtotals and up to eight mark rules. A 1.0 document stays valid and means what it meant; a 1.0 document
// with a 1.1 key is refused, so that a layout says which schema it speaks.
package layouts

import (
	"encoding/json"
	"fmt"
	"slices"

	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

type Binding struct {
	Integration string `json:"integration_id"`
	Dataset     r.Ref  `json:"dataset"`
}

// Column is one shown column. Label and Format are 1.1: a label of 1..64 characters replaces the dataset's label (and its currency and
// unit) in the header; a format is allowed on a decimal or integer column only.
type Column struct {
	ID     string  `json:"id"`
	Align  string  `json:"align"` // left or right; 1.1 also center
	Label  string  `json:"label,omitempty"`
	Format *Format `json:"format,omitempty"`
}

// Format is how a numeric column shows its digits (1.1): grouping (default true), the negative form (minus or parentheses, default minus)
// and the zero form (digits or dash, default digits). A key that is absent takes its default. It changes which digits are shown never.
type Format struct {
	Grouping *bool  `json:"grouping,omitempty"`
	Negative string `json:"negative,omitempty"`
	Zero     string `json:"zero,omitempty"`
}

// Subtotals is the one level of subtotals (1.1): the rows are partitioned by the exact strings of the column By, a dimension of the
// dataset shown by the layout, and each group is followed by a 小计 row with the exact sum of each shown metric.
type Subtotals struct {
	By string `json:"by"`
}

// Mark is one conditional rule (1.1): the cells of Column whose value stands in the relation When (lt le gt ge eq ne on a numeric column,
// with an exact decimal Value; eq ne blank on any other, with a string Value of at most 128 characters, absent for blank) get the mark
// (good warn bad note).
type Mark struct {
	Column string `json:"column"`
	When   string `json:"when"`
	Value  string `json:"value,omitempty"`
	Mark   string `json:"mark"`
}

type Document struct {
	Schema    string     `json:"schema_version"`
	Binding   Binding    `json:"binding"`
	Title     string     `json:"title,omitempty"`
	Footnote  string     `json:"footnote,omitempty"`
	Columns   []Column   `json:"columns"`
	Subtotals *Subtotals `json:"subtotals,omitempty"`
	Marks     []Mark     `json:"marks,omitempty"`
}

// The bounds of a layout: as JSON, columns, and the 1.1 texts and marks (reporting's bounds of a presentation).
const (
	MaxBytes   = 65536
	MaxColumns = r.MaxPresentedColumns
	MaxMarks   = r.MaxMarks
)

// Schemas are the schema versions a layout may speak.
const (
	Schema10 = "1.0"
	Schema11 = "1.1"
)

func ValidID(id string) bool { return privatedocs.ValidID(id) }

// Validate holds a document to its shape (r.ErrInvalid otherwise; r.ErrBudget over 64 KiB): what can be checked without the dataset. The
// rules that need the dataset (a format on a numeric column, a subtotal column that is a dimension, a relation that fits the column's
// type) are Check, which the HTTP layer applies against the dataset the layout is bound to.
func Validate(d Document) error {
	b, e := json.Marshal(d)
	if e != nil || len(b) > MaxBytes {
		return r.ErrBudget
	}
	if (d.Schema != Schema10 && d.Schema != Schema11) || d.Binding.Integration == "" || len(d.Binding.Integration) > 128 || d.Binding.Dataset.ID == "" || len(d.Binding.Dataset.ID) > 128 || d.Binding.Dataset.Version == "" || len(d.Binding.Dataset.Version) > 128 || len(d.Columns) == 0 || len(d.Columns) > MaxColumns {
		return r.ErrInvalid
	}
	if d.Schema == Schema10 {
		// A 1.0 document is exactly what 1.0 defined.
		if d.Title != "" || d.Footnote != "" || d.Subtotals != nil || d.Marks != nil {
			return fmt.Errorf("a 1.0 layout with a 1.1 key: %w", r.ErrInvalid)
		}
		for _, c := range d.Columns {
			if c.Label != "" || c.Format != nil || c.Align == r.AlignCenter {
				return fmt.Errorf("a 1.0 layout with a 1.1 key: %w", r.ErrInvalid)
			}
		}
	}
	seen := map[string]bool{}
	for _, c := range d.Columns {
		if c.ID == "" || len(c.ID) > 128 || seen[c.ID] || (c.Align != r.AlignLeft && c.Align != r.AlignRight && c.Align != r.AlignCenter) {
			return r.ErrInvalid
		}
		seen[c.ID] = true
		if c.Label != "" && !r.Literal(c.Label, r.MaxColumnLabelRunes) {
			return fmt.Errorf("a column label is empty, too long or not printable: %w", r.ErrInvalid)
		}
		if c.Format != nil && ((c.Format.Negative != "" && c.Format.Negative != r.NegativeMinus && c.Format.Negative != r.NegativeParentheses) || (c.Format.Zero != "" && c.Format.Zero != r.ZeroDigits && c.Format.Zero != r.ZeroDash)) {
			return fmt.Errorf("a format names an unknown negative or zero form: %w", r.ErrInvalid)
		}
	}
	if d.Title != "" && !r.Literal(d.Title, r.MaxTitleRunes) || d.Footnote != "" && !r.Literal(d.Footnote, r.MaxFootnoteRunes) {
		return fmt.Errorf("the title or the footnote is too long or not printable: %w", r.ErrInvalid)
	}
	if d.Subtotals != nil && !seen[d.Subtotals.By] {
		return fmt.Errorf("the subtotal column is not a column of the layout: %w", r.ErrInvalid)
	}
	if d.Marks != nil && (len(d.Marks) == 0 || len(d.Marks) > MaxMarks) {
		return fmt.Errorf("marks must be 1 to %d rules: %w", MaxMarks, r.ErrInvalid)
	}
	// The shape of the marks, and of everything else the exports need, is the presentation's own shape: the same rules, once.
	if e := Presentation(d, "", 0).Validate(); e != nil {
		return e
	}
	return nil
}

// Check holds a valid document to the dataset it is bound to: every column is one of columns (r.ErrDenied otherwise, as a column the
// caller may not see), a format sits on a decimal or integer column only, the subtotal column is one of dimensions, and a mark compares
// as its column's type allows (r.ErrInvalid otherwise; ADR 0024 decision 1). The HTTP layer applies it where it already checks a layout
// against the dataset's catalog entry, and against the frozen snapshot an export is made from.
func Check(d Document, columns []r.Column, dimensions []string) error {
	for _, column := range d.Columns {
		if !slices.ContainsFunc(columns, func(c r.Column) bool { return c.ID == column.ID }) {
			return r.ErrDenied
		}
	}
	if e := Presentation(d, "", 0).Fits(columns, dimensions); e != nil {
		return fmt.Errorf("%v: %w", e, r.ErrInvalid)
	}
	return nil
}

// Presentation is what the exports render a layout as (reporting.Presentation): the layout's columns with their labels and alignments,
// each format with its defaults filled in, the subtotal column, the marks, the title and the footnote, naming the layout version it is
// taken from (id "" and version 0 for none).
func Presentation(d Document, id string, version int) r.Presentation {
	p := r.Presentation{LayoutID: id, LayoutVersion: version, Title: d.Title, Footnote: d.Footnote}
	for _, c := range d.Columns {
		pc := r.PresentedColumn{ID: c.ID, Label: c.Label, Align: c.Align}
		if c.Format != nil {
			f := r.DefaultNumberFormat
			if c.Format.Grouping != nil {
				f.Grouping = *c.Format.Grouping
			}
			if c.Format.Negative != "" {
				f.Negative = c.Format.Negative
			}
			if c.Format.Zero != "" {
				f.Zero = c.Format.Zero
			}
			pc.Format = &f
		}
		p.Columns = append(p.Columns, pc)
	}
	if d.Subtotals != nil {
		p.SubtotalBy = d.Subtotals.By
	}
	for _, m := range d.Marks {
		p.Marks = append(p.Marks, r.Mark{Column: m.Column, When: m.When, Value: m.Value, Mark: m.Mark})
	}
	return p
}

type Entry = privatedocs.Entry[Document]
type ReleaseEvent = privatedocs.ReleaseEvent
type ReleaseState = privatedocs.ReleaseState
type Store = privatedocs.Store[Document]

func Open(dir string) (*Store, error) {
	return privatedocs.Open(dir, Validate, bindingKey)
}

// OpenBackend opens the layouts over a backend (S11-B: the platform's database, shared by every server).
func OpenBackend(b privatedocs.Backend) (*Store, error) {
	return privatedocs.OpenBackend(b, privatedocs.DefaultCap, Validate, bindingKey)
}

func bindingKey(d Document) string { b, _ := json.Marshal(d.Binding); return string(b) }
