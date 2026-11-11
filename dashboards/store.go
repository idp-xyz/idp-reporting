// Package dashboards validates metadata-only single-dataset dashboard documents.
package dashboards

import (
	"bytes"
	"encoding/json"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"slices"
	"unicode"
	"unicode/utf8"
)

type Binding struct {
	Integration string `json:"integration_id"`
	Dataset     r.Ref  `json:"dataset"`
}

// Widget is one component of a dashboard: a metric (a server total of a field), a table (columns of the current page), or a chart over
// the current page's rows (bar, line or pie: a category column and a numeric field). S21-A: every widget may carry a title, shown in
// place of its default heading (1..64 characters, none of them a control character), and a width ("half", the grid's column, or
// "full", both columns); absent means half.
type Widget struct {
	Type     string   `json:"type"`
	Field    *string  `json:"field,omitempty"`
	Columns  []string `json:"columns,omitempty"`
	Category *string  `json:"category,omitempty"`
	Title    *string  `json:"title,omitempty"`
	Width    *string  `json:"width,omitempty"`
}
type Document struct {
	Schema  string   `json:"schema_version"`
	Kind    string   `json:"kind"`
	Binding Binding  `json:"binding"`
	Widgets []Widget `json:"widgets"`
}
type Store = privatedocs.Store[Document]
type Entry = privatedocs.Entry[Document]

// widgetFields is the required field set of each widget type; title and width are optional on every type.
var widgetFields = map[string][]string{
	"metric": {"type", "field"},
	"bar":    {"type", "category", "field"},
	"line":   {"type", "category", "field"},
	"pie":    {"type", "category", "field"},
	"table":  {"type", "columns"},
}

// MaxTitle is the length of a widget title in characters.
const MaxTitle = 64

func Validate(d Document) error {
	data, e := json.Marshal(d)
	if e != nil || len(data) > 65536 {
		return r.ErrBudget
	}
	if d.Schema != "1.0" || d.Kind != "dashboard" || d.Binding.Integration == "" || len(d.Binding.Integration) > 128 || d.Binding.Dataset.ID == "" || len(d.Binding.Dataset.ID) > 128 || d.Binding.Dataset.Version == "" || len(d.Binding.Dataset.Version) > 128 || d.Widgets == nil || len(d.Widgets) > 12 {
		return r.ErrInvalid
	}
	for _, w := range d.Widgets {
		switch w.Type {
		case "metric":
			if w.Field == nil || *w.Field == "" || len(*w.Field) > 128 || w.Columns != nil || w.Category != nil {
				return r.ErrInvalid
			}
		case "bar", "line", "pie":
			if w.Field == nil || *w.Field == "" || len(*w.Field) > 128 || w.Category == nil || *w.Category == "" || len(*w.Category) > 128 || w.Columns != nil {
				return r.ErrInvalid
			}
		case "table":
			if w.Field != nil || w.Category != nil || len(w.Columns) == 0 || len(w.Columns) > 100 {
				return r.ErrInvalid
			}
			seen := map[string]bool{}
			for _, id := range w.Columns {
				if id == "" || len(id) > 128 || seen[id] {
					return r.ErrInvalid
				}
				seen[id] = true
			}
		default:
			return r.ErrInvalid
		}
		if w.Title != nil && !ValidTitle(*w.Title) {
			return r.ErrInvalid
		}
		if w.Width != nil && *w.Width != "half" && *w.Width != "full" {
			return r.ErrInvalid
		}
	}
	return nil
}

// ValidTitle says whether s may be a widget's title: 1 to MaxTitle characters, valid UTF-8, none of them a control character.
func ValidTitle(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	n := utf8.RuneCountInString(s)
	if n == 0 || n > MaxTitle {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func Open(dir string) (*Store, error) {
	return privatedocs.Open(dir, Validate, bindingKey)
}

// OpenBackend opens the dashboards over a backend (S11-B: the platform's database, shared by every server).
func OpenBackend(b privatedocs.Backend) (*Store, error) {
	return privatedocs.OpenBackend(b, privatedocs.DefaultCap, Validate, bindingKey)
}

func bindingKey(d Document) string { b, _ := json.Marshal(d.Binding); return string(b) }

// UnmarshalJSON enforces the exact field set for each widget variant: the required fields of the type, and optionally title and width,
// each present as a value (never null) and nothing else.
func (w *Widget) UnmarshalJSON(data []byte) error {
	type wire Widget
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return r.ErrInvalid
	}
	required, known := widgetFields[value.Type]
	if !known {
		return r.ErrInvalid
	}
	for _, name := range required {
		if fields[name] == nil {
			return r.ErrInvalid
		}
	}
	for name := range fields {
		if name != "title" && name != "width" && !slices.Contains(required, name) {
			return r.ErrInvalid
		}
	}
	if (fields["title"] != nil && value.Title == nil) || (fields["width"] != nil && value.Width == nil) {
		return r.ErrInvalid
	}
	*w = Widget(value)
	return nil
}
