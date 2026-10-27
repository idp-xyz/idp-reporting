// Package dashboards validates metadata-only single-dataset dashboard documents.
package dashboards

import (
	"bytes"
	"encoding/json"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

type Binding struct {
	Integration string `json:"integration_id"`
	Dataset     r.Ref  `json:"dataset"`
}
type Widget struct {
	Type     string   `json:"type"`
	Field    *string  `json:"field,omitempty"`
	Columns  []string `json:"columns,omitempty"`
	Category *string  `json:"category,omitempty"`
}
type Document struct {
	Schema  string   `json:"schema_version"`
	Kind    string   `json:"kind"`
	Binding Binding  `json:"binding"`
	Widgets []Widget `json:"widgets"`
}
type Store = privatedocs.Store[Document]
type Entry = privatedocs.Entry[Document]

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
		case "bar":
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
	}
	return nil
}
func Open(dir string) (*Store, error) {
	return privatedocs.Open(dir, Validate, func(d Document) string { b, _ := json.Marshal(d.Binding); return string(b) })
}

// UnmarshalJSON enforces the exact field set for each widget variant.
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
	if value.Type == "metric" {
		if len(fields) != 2 || fields["type"] == nil || fields["field"] == nil {
			return r.ErrInvalid
		}
	} else if value.Type == "bar" {
		if len(fields) != 3 || fields["type"] == nil || fields["category"] == nil || fields["field"] == nil {
			return r.ErrInvalid
		}
	} else if value.Type == "table" {
		if len(fields) != 2 || fields["type"] == nil || fields["columns"] == nil {
			return r.ErrInvalid
		}
	} else {
		return r.ErrInvalid
	}
	*w = Widget(value)
	return nil
}
