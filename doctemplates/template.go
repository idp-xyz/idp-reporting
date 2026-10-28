// Package doctemplates defines the bounded document preview contract.
package doctemplates

import (
	"bytes"
	"encoding/json"
	r "idp.local/reporting/reporting"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Binding struct {
	Integration string `json:"integration_id"`
	Dataset     r.Ref  `json:"dataset"`
}
type Element struct {
	ID       string  `json:"id"`
	Source   string  `json:"source"`
	Field    string  `json:"field"`
	Text     string  `json:"text"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	FontSize int     `json:"font_size"`
	Align    string  `json:"align"`
}
type PrintProfile struct {
	Mode    string  `json:"mode"`
	OffsetX float64 `json:"offset_x"`
	OffsetY float64 `json:"offset_y"`
}
type Document struct {
	Schema     string         `json:"schema_version"`
	Kind       string         `json:"kind"`
	Binding    Binding        `json:"binding"`
	Elements   []Element      `json:"elements"`
	Print      *PrintProfile  `json:"print,omitempty"`
	Background *BackgroundRef `json:"background,omitempty"`
}

func Validate(d Document) error {
	b, e := json.Marshal(d)
	if e != nil || len(b) > 65536 {
		return r.ErrBudget
	}
	if (d.Schema != "1.0" && d.Schema != "1.1" && d.Schema != "1.2") || d.Kind != "document" || d.Binding.Integration == "" || len(d.Binding.Integration) > 128 || d.Binding.Dataset.ID == "" || len(d.Binding.Dataset.ID) > 128 || d.Binding.Dataset.Version == "" || len(d.Binding.Dataset.Version) > 128 || len(d.Elements) < 1 || len(d.Elements) > 24 {
		return r.ErrInvalid
	}
	if d.Schema == "1.0" && d.Print != nil {
		return r.ErrInvalid
	}
	if d.Schema != "1.0" && (d.Print == nil || (d.Print.Mode != "full" && d.Print.Mode != "variables") || !grid(d.Print.OffsetX) || !grid(d.Print.OffsetY) || math.Abs(d.Print.OffsetX) > 10 || math.Abs(d.Print.OffsetY) > 10) {
		return r.ErrInvalid
	}
	if (d.Schema == "1.2" && (d.Background == nil || !validBackgroundRef(*d.Background))) || (d.Schema != "1.2" && d.Background != nil) {
		return r.ErrInvalid
	}
	bound := false
	ids := map[string]bool{}
	for i, v := range d.Elements {
		number, parseErr := strconv.Atoi(strings.TrimPrefix(v.ID, "n"))
		if parseErr != nil || v.ID != "n"+strconv.Itoa(number) || number < 1 || number > 24 || len(v.ID) < 1 || len(v.ID) > 32 || ids[v.ID] || v.X < 10 || v.X > 190 || v.Y < 25 || v.Y > 265 || v.Width < 10 || v.Width > 190 || v.Height < 5 || v.Height > 245 || tenths(v.X)+tenths(v.Width) > 2000 || tenths(v.Y)+tenths(v.Height) > 2700 || v.FontSize < 8 || v.FontSize > 28 || tenths(v.Height)*72 < v.FontSize*400 || (v.Align != "left" && v.Align != "center" && v.Align != "right") {
			return r.ErrInvalid
		}
		for _, n := range []float64{v.X, v.Y, v.Width, v.Height} {
			if !grid(n) || (d.Schema == "1.0" && n != math.Trunc(n)) {
				return r.ErrInvalid
			}
		}
		if v.Source != "literal" {
			bound = true
		}
		if d.Print != nil && (d.Print.Mode == "full" || v.Source != "literal") {
			x, y := tenths(v.X)+tenths(d.Print.OffsetX), tenths(v.Y)+tenths(d.Print.OffsetY)
			if x < 100 || y < 250 || x+tenths(v.Width) > 2000 || y+tenths(v.Height) > 2700 {
				return r.ErrInvalid
			}
		}
		ids[v.ID] = true
		if v.Source == "literal" {
			if v.Field != "" || !utf8.ValidString(v.Text) || len([]rune(v.Text)) > 256 || hasControls(v.Text) {
				return r.ErrInvalid
			}
		} else if v.Source == "row" || v.Source == "total" {
			if v.Field == "" || len(v.Field) > 128 || v.Text != "" {
				return r.ErrInvalid
			}
		} else {
			return r.ErrInvalid
		}
		for _, prior := range d.Elements[:i] {
			if tenths(v.X) < tenths(prior.X)+tenths(prior.Width) && tenths(prior.X) < tenths(v.X)+tenths(v.Width) && tenths(v.Y) < tenths(prior.Y)+tenths(prior.Height) && tenths(prior.Y) < tenths(v.Y)+tenths(v.Height) {
				return r.ErrInvalid
			}
		}
	}
	if d.Print != nil && d.Print.Mode == "variables" && !bound {
		return r.ErrInvalid
	}
	return nil
}

// Bind produces literal strings from an already authorized frozen snapshot, never caller values.
func Bind(d Document, s r.Snapshot, index int) (map[string]string, error) {
	if e := Validate(d); e != nil {
		return nil, e
	}
	if d.Binding.Integration != s.Owner.Integration || d.Binding.Dataset != s.Query.Dataset {
		return nil, r.ErrDenied
	}
	if index < 0 || index >= len(s.Result.Rows) {
		return nil, r.ErrInvalid
	}
	cols := map[string]int{}
	for i, c := range s.Result.Columns {
		cols[c.ID] = i
	}
	out := map[string]string{}
	for _, v := range d.Elements {
		text := v.Text
		if v.Source != "literal" {
			col, ok := cols[v.Field]
			if !ok {
				return nil, r.ErrDenied
			}
			if v.Source == "row" {
				if col >= len(s.Result.Rows[index]) {
					return nil, r.ErrIntegrity
				}
				text = s.Result.Rows[index][col]
			} else {
				var ok bool
				text, ok = s.Result.Totals[v.Field]
				if !ok {
					return nil, r.ErrDenied
				}
			}
		}
		// A value the preview cannot print on one line as it is.
		if !utf8.ValidString(text) || len([]rune(text)) > 256 || hasControls(text) {
			return nil, r.ErrUnsupported
		}
		out[v.ID] = text
	}
	return out, nil
}

func hasControls(s string) bool {
	for _, c := range s {
		if c < 32 || c == 0x7F {
			return true
		}
	}
	return false
}

func grid(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && math.Abs(n*10-math.Round(n*10)) < 1e-7
}
func tenths(n float64) int { return int(math.Round(n * 10)) }

// UnmarshalJSON keeps schema1.0 print-profile-free, including explicit null.
func (d *Document) UnmarshalJSON(data []byte) error {
	type wire Document
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		return e
	}
	if (value.Schema == "1.0" && fields["print"] != nil) || (value.Schema != "1.2" && fields["background"] != nil) {
		return r.ErrInvalid
	}
	*d = Document(value)
	return nil
}

func (p *PrintProfile) UnmarshalJSON(data []byte) error {
	type wire PrintProfile
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		return e
	}
	if len(fields) != 3 || fields["mode"] == nil || fields["offset_x"] == nil || fields["offset_y"] == nil {
		return r.ErrInvalid
	}
	*p = PrintProfile(value)
	return nil
}

func (b *BackgroundRef) UnmarshalJSON(data []byte) error {
	type wire BackgroundRef
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	if !validBackgroundRef(BackgroundRef(value)) {
		return r.ErrInvalid
	}
	*b = BackgroundRef(value)
	return nil
}
