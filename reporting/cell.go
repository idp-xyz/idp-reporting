package reporting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Cell is one value of a dataset: a cell of a row, the total of a metric, a value of a group's key (ADR 0028 decision 1,
// docs/S26A_SCOPE.md). It is a text of its column's type, or no value. Rows were lists of strings from S01-A to S25; a cell is a type
// of its own so that the absent value cannot be read as a text by code that was not written for it: the text is reached through Value,
// Required or Text, and every such place is one that says what it does with a cell that has no value.
//
// The zero Cell is the empty text, as the zero string was. A cell that has a value is encoded as its JSON string, exactly as the string
// was, so that every stored snapshot, every digest of a row or a group and every issue record is what it was; a cell without a value is
// JSON null. Two cells are equal (==) when both hold the same text or both hold no value: a cell without a value equals no text. No
// column can declare that it may be absent yet (S26-B): validCell refuses a cell without a value in every column, so no such cell is
// ever stored, served or digested, and nothing a person or a product sees has changed.
type Cell struct {
	text string
	null bool
}

// Text is the cell that holds the text.
func Text(s string) Cell { return Cell{text: s} }

// NoValue is the cell that holds no value: JSON null.
func NoValue() Cell { return Cell{null: true} }

// IsNull says whether the cell holds no value.
func (c Cell) IsNull() bool { return c.null }

// Value is the cell's text and true, or "" and false for a cell without a value.
func (c Cell) Value() (string, bool) { return c.text, !c.null }

// Required is the text of a cell that must have a value, and ErrIntegrity for one that has none. Until a column can declare that it
// may be absent (S26-B) no stored row, total or key holds such a cell, so a reader that meets one is reading a store that does not
// agree with itself and fails closed; it is what the exports, the templates and the document checks call wherever they compute with
// or print a value, and each such call is a place S26-B decides what an absent value does there.
func (c Cell) Required() (string, error) {
	if c.null {
		return "", fmt.Errorf("a cell without a value where no column may be without one: %w", ErrIntegrity)
	}
	return c.text, nil
}

// Text is the cell's text, and the empty text for a cell without a value: what a person is shown. A caller that computes with the
// text, compares it or digests it uses Value, or the cell itself, so that an absent value is not taken for an empty one.
func (c Cell) Text() string { return c.text }

// String is the cell as JSON writes it, the text in its quotes or null, for the message of a test: a row prints as ["a" "" null], so
// that an empty text and no value are told apart. It is not the cell's text (Text, Value): a cell formatted where its text was meant
// shows its quotes.
func (c Cell) String() string {
	b, err := c.MarshalJSON()
	if err != nil {
		return "(a cell that cannot be written)"
	}
	return string(b)
}

// MarshalJSON writes the text as the JSON string it always was, and null for no value. The string is written without the escapes
// for HTML: the encoder that asked adds them when it is set to (encoding/json compacts and escapes what a Marshaler returns by its own
// setting), so that the text is byte for byte what the same encoder wrote for the string, whichever its setting is
// (TestACellIsEncodedAsItsStringWas holds the two settings to it).
func (c Cell) MarshalJSON() ([]byte, error) {
	if c.null {
		return []byte("null"), nil
	}
	if plainJSONText(c.text) {
		b := make([]byte, 0, len(c.text)+2)
		b = append(b, '"')
		b = append(b, c.text...)
		return append(b, '"'), nil
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(c.text); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// plainJSONText: the text between two quotes is what MarshalJSON may return for it: valid UTF-8 without a quote, a backslash or a
// control character (which a JSON string escapes), and without the two line separators, which the encoder escapes in a string under
// either setting but in a Marshaler's answer only under one. The three characters of HTML need no care here: the encoder escapes
// them in a Marshaler's answer exactly when it escapes them in a string.
func plainJSONText(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x20, c == '"', c == '\\':
			return false
		case c == 0xE2 && i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xA8 || s[i+2] == 0xA9):
			return false
		}
	}
	return utf8.ValidString(s)
}

// UnmarshalJSON reads a JSON string or null and nothing else: a number, a boolean, an array or an object is not a cell, and neither
// is what is no JSON value at all. encoding/json hands this method a value it has already found well formed; a caller of its own gets
// the same answer for the same bytes, and ErrInvalid for the rest.
func (c *Cell) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, " \t\r\n")
	if bytes.Equal(b, []byte("null")) {
		*c = Cell{null: true}
		return nil
	}
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return ErrInvalid
	}
	if inner := b[1 : len(b)-1]; plainJSONInner(inner) {
		*c = Cell{text: string(inner)}
		return nil
	}
	var s string
	if e := json.Unmarshal(b, &s); e != nil {
		return ErrInvalid
	}
	*c = Cell{text: s}
	return nil
}

// plainJSONInner: what stands between the two quotes of a JSON string is the text itself: no escape in it (no backslash), nothing a
// JSON string may not hold as it is (a quote, a control character), and valid UTF-8 (the decoder replaces what is not).
func plainJSONInner(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c == '"' || c == '\\' {
			return false
		}
	}
	return utf8.Valid(b)
}

// The conversions between cells and texts. They stand where a list of texts meets the core: the wire of the source protocol and a
// request's group key, which are texts until a column can declare that it may be absent (S26-B), the fixtures of the simulators, and
// the tests. From texts every cell has a value; to texts, a cell without a value is an error (Required*) or the empty text (Texts).

// Cells is the row of the texts: every cell has a value.
func Cells(texts ...string) []Cell {
	out := make([]Cell, len(texts))
	for i, s := range texts {
		out[i] = Cell{text: s}
	}
	return out
}

// CellRows is the rows of the texts, nil for nil: every cell has a value.
func CellRows(rows [][]string) [][]Cell {
	if rows == nil {
		return nil
	}
	out := make([][]Cell, len(rows))
	for i, row := range rows {
		out[i] = Cells(row...)
	}
	return out
}

// CellTotals is the totals of the texts, nil for nil: every total has a value.
func CellTotals(totals map[string]string) map[string]Cell {
	if totals == nil {
		return nil
	}
	out := make(map[string]Cell, len(totals))
	for k, v := range totals {
		out[k] = Cell{text: v}
	}
	return out
}

// Texts is the texts of the cells, the empty text for a cell without a value (Cell.Text).
func Texts(cells []Cell) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = c.text
	}
	return out
}

// RequiredTexts is Required for every cell of a row.
func RequiredTexts(cells []Cell) ([]string, error) {
	out := make([]string, len(cells))
	for i, c := range cells {
		v, e := c.Required()
		if e != nil {
			return nil, e
		}
		out[i] = v
	}
	return out, nil
}

// RequiredRows is RequiredTexts for every row; no rows is an empty list.
func RequiredRows(rows [][]Cell) ([][]string, error) {
	out := make([][]string, len(rows))
	for i, row := range rows {
		texts, e := RequiredTexts(row)
		if e != nil {
			return nil, e
		}
		out[i] = texts
	}
	return out, nil
}

// RequiredTotals is Required for every total; no totals is an empty map.
func RequiredTotals(totals map[string]Cell) (map[string]string, error) {
	out := make(map[string]string, len(totals))
	for k, c := range totals {
		v, e := c.Required()
		if e != nil {
			return nil, e
		}
		out[k] = v
	}
	return out, nil
}
