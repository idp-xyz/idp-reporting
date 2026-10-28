package doctemplates

import (
	"encoding/json"
	"idp.local/reporting/privatedocs"
)

type Store = privatedocs.Store[Document]
type Entry = privatedocs.Entry[Document]

func Open(dir string) (*Store, error) {
	return privatedocs.Open(dir, Validate, func(d Document) string { b, _ := json.Marshal(d.Binding); return string(b) })
}
