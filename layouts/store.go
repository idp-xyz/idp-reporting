// Package layouts validates private screen report column layouts.
package layouts

import (
	"encoding/json"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

type Binding struct {
	Integration string `json:"integration_id"`
	Dataset     r.Ref  `json:"dataset"`
}
type Column struct {
	ID    string `json:"id"`
	Align string `json:"align"`
}
type Document struct {
	Schema  string   `json:"schema_version"`
	Binding Binding  `json:"binding"`
	Columns []Column `json:"columns"`
}

func ValidID(id string) bool { return privatedocs.ValidID(id) }
func Validate(d Document) error {
	b, e := json.Marshal(d)
	if e != nil || len(b) > 65536 {
		return r.ErrBudget
	}
	if d.Schema != "1.0" || d.Binding.Integration == "" || len(d.Binding.Integration) > 128 || d.Binding.Dataset.ID == "" || len(d.Binding.Dataset.ID) > 128 || d.Binding.Dataset.Version == "" || len(d.Binding.Dataset.Version) > 128 || len(d.Columns) == 0 || len(d.Columns) > 100 {
		return r.ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range d.Columns {
		if c.ID == "" || len(c.ID) > 128 || seen[c.ID] || (c.Align != "left" && c.Align != "right") {
			return r.ErrInvalid
		}
		seen[c.ID] = true
	}
	return nil
}

type Entry = privatedocs.Entry[Document]
type ReleaseEvent = privatedocs.ReleaseEvent
type ReleaseState = privatedocs.ReleaseState
type Store = privatedocs.Store[Document]

func Open(dir string) (*Store, error) {
	return privatedocs.Open(dir, Validate, func(d Document) string { b, _ := json.Marshal(d.Binding); return string(b) })
}
