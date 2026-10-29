package doctemplates

import (
	"errors"
	"strconv"
	"strings"
)

// The reasons a document is refused where it is bound or laid out (ADR 0004, decision 8). A reason
// and the place it applies to are names and indices, never values: they travel to the client as the
// `detail` of a problem answer and as the detail of a document check's row.
const (
	// ReasonDoesNotFit: a value is wider or taller than its box or column, a carried row's label or
	// sum does not fit its column, or a table row is taller than a page holds.
	ReasonDoesNotFit = "DOES_NOT_FIT"
	// ReasonNotCovered: a value the platform cannot print as it is, a character the fonts do not
	// cover, a control character, or text that is only meant for the preview banner.
	ReasonNotCovered = "NOT_COVERED"
	// ReasonInconsistentGroup: a field that is not a group field differs between the rows of a group.
	ReasonInconsistentGroup = "INCONSISTENT_GROUP"
	// ReasonTooManyPages: the document needs more pages than the platform lays out.
	ReasonTooManyPages = "TOO_MANY_PAGES"
	// ReasonBandOverflow: a band, or the table's header and total rows, is taller than the space
	// its page has for it.
	ReasonBandOverflow = "BAND_OVERFLOW"
)

// Detail says why and where a document was refused: the reason and, where they apply, the band,
// the element (a 1.x box id such as n3, a 2.0 element id such as e7), the snapshot row index and the
// field id of a table column. Never a value.
type Detail struct {
	Reason  string `json:"reason"`
	Band    string `json:"band,omitempty"`
	Element string `json:"element,omitempty"`
	Row     *int   `json:"row,omitempty"`
	Column  string `json:"column,omitempty"`
}

// At returns the detail with a snapshot row index.
func (d Detail) At(row int) Detail {
	d.Row = &row
	return d
}

// Refusal is a refusal with its detail. It wraps the sentinel the refusal has always been (an
// unsupported combination, 422), so that errors.Is and the code and status of the API do not change.
type Refusal struct {
	Detail Detail
	err    error
}

// Refuse gives err (a sentinel, possibly wrapped) the detail. A nil err stays nil.
func Refuse(err error, d Detail) error {
	if err == nil {
		return nil
	}
	return &Refusal{Detail: d, err: err}
}

func (e *Refusal) Unwrap() error { return e.err }

// Error names the reason and the place: names and indices only, so it is as safe as the sentinel.
func (e *Refusal) Error() string {
	var b strings.Builder
	b.WriteString(e.Detail.Reason)
	if e.Detail.Band != "" {
		b.WriteString(" band " + e.Detail.Band)
	}
	if e.Detail.Element != "" {
		b.WriteString(" element " + e.Detail.Element)
	}
	if e.Detail.Row != nil {
		b.WriteString(" row " + strconv.Itoa(*e.Detail.Row))
	}
	if e.Detail.Column != "" {
		b.WriteString(" column " + e.Detail.Column)
	}
	return b.String() + ": " + e.err.Error()
}

// DetailOf returns the detail of the first refusal in err's chain.
func DetailOf(err error) (Detail, bool) {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Detail, true
	}
	return Detail{}, false
}
