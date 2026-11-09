package doctemplates

import (
	"encoding/json"
	"idp.local/reporting/privatedocs"
)

type Store = privatedocs.Store[Document]
type Entry = privatedocs.Entry[Document]
type Catalog = privatedocs.Catalog

// MaxTemplates is how many templates an identity may keep (S06-B; 16 before it, which the 37 samples a user may copy exceed). The file an
// identity's templates are kept in is still bounded (8 MiB, the shared store's): the count is not the only limit.
const MaxTemplates = 64

func Open(dir string) (*Store, error) {
	return privatedocs.OpenCap(dir, MaxTemplates, Validate, bindingKey)
}

// OpenBackend opens the templates over a backend (S11-B: the platform's database, shared by every server).
func OpenBackend(b privatedocs.Backend) (*Store, error) {
	return privatedocs.OpenBackend(b, MaxTemplates, Validate, bindingKey)
}

func bindingKey(d Document) string { b, _ := json.Marshal(d.Binding); return string(b) }
