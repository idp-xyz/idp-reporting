package privatedocs

import (
	"strings"
	"time"
	"unicode/utf8"

	r "idp.local/reporting/reporting"
)

// The template centre's per-template metadata (S06-B), kept with the record on the identity's own file: nothing here is shared between
// identities. It is for people (a business domain and flow, a favourite, an archive flag, named collections) and nothing that renders
// or issues a document reads it.
const (
	MaxCollections    = 16 // collections one template may be in
	MaxCatalogRunes   = 40 // a domain, a flow or a collection name
	maxCatalogRevison = 1 << 30
)

// Catalog is the metadata of one record. Revision counts the changes people made (not the opening of a version): a change is
// made against the revision it read, as a release is, and a stale one is a conflict. LastOpened is set by the store when a version is
// read (Opened); a change of the rest keeps it.
type Catalog struct {
	Domain      string   `json:"domain"`
	Flow        string   `json:"flow"`
	Favourite   bool     `json:"favourite"`
	Archived    bool     `json:"archived"`
	Collections []string `json:"collections"`
	LastOpened  string   `json:"last_opened,omitempty"`
	Revision    int      `json:"revision"`
}

// catalogText says whether s is a name: valid UTF-8 of at most MaxCatalogRunes characters on one line, with no control character and
// no space around it. The empty string is a name only where allowed (domain and flow).
func catalogText(s string, allowEmpty bool) bool {
	if s == "" {
		return allowEmpty
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxCatalogRunes || strings.TrimSpace(s) != s {
		return false
	}
	for _, c := range s {
		if c < 32 || c == 0x7F {
			return false
		}
	}
	return true
}

func validCatalog(c Catalog) error {
	if !catalogText(c.Domain, true) || !catalogText(c.Flow, true) || c.Collections == nil || len(c.Collections) > MaxCollections || c.Revision < 0 || c.Revision > maxCatalogRevison {
		return r.ErrInvalid
	}
	seen := map[string]bool{}
	for _, name := range c.Collections {
		if !catalogText(name, false) || seen[name] {
			return r.ErrInvalid
		}
		seen[name] = true
	}
	if c.LastOpened != "" {
		if _, e := time.Parse(time.RFC3339Nano, c.LastOpened); e != nil {
			return r.ErrInvalid
		}
	}
	return nil
}

// ValidCatalog says whether a catalog could be stored (its revision and last-opened time aside, which the store sets).
func ValidCatalog(c Catalog) error {
	c.Revision, c.LastOpened = 0, ""
	return validCatalog(c)
}

// empty is the catalog of a record that never had one.
func emptyCatalog() Catalog { return Catalog{Collections: []string{}} }

func (item record[T]) catalog() Catalog {
	if item.Catalog == nil {
		return emptyCatalog()
	}
	c := *item.Catalog
	c.Collections = append([]string{}, c.Collections...)
	return c
}

// Catalog returns the metadata of a record of the owner; a record that never had any has the empty one (no domain, not a favourite,
// not archived, in no collection, revision 0).
func (s *Store[T]) Catalog(owner r.Identity, id string) (Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return Catalog{}, r.ErrInvalid
	}
	all, e := s.read(owner)
	if e != nil {
		return Catalog{}, e
	}
	for _, item := range all {
		if item.Versions[0].ID == id {
			return item.catalog(), nil
		}
	}
	return Catalog{}, r.ErrNotFound
}

// Catalogs returns the metadata of every record of the owner that has any, by record id.
func (s *Store[T]) Catalogs(owner r.Identity) (map[string]Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, e := s.read(owner)
	if e != nil {
		return nil, e
	}
	out := map[string]Catalog{}
	for _, item := range all {
		out[item.Versions[0].ID] = item.catalog()
	}
	return out, nil
}

// SetCatalog replaces the people-made metadata of a record (the whole object) if expected is its revision, else ErrConflict, and
// answers the stored catalog, one revision on. The time the record was last opened is the store's and is kept.
func (s *Store[T]) SetCatalog(owner r.Identity, id string, expected int, c Catalog) (Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) || expected < 0 {
		return Catalog{}, r.ErrInvalid
	}
	if e := ValidCatalog(c); e != nil {
		return Catalog{}, e
	}
	all, e := s.read(owner)
	if e != nil {
		return Catalog{}, e
	}
	i := s.find(all, id)
	if i < 0 {
		return Catalog{}, r.ErrNotFound
	}
	cur := all[i].catalog()
	if expected != cur.Revision {
		return Catalog{}, r.ErrConflict
	}
	if cur.Revision >= maxCatalogRevison {
		return Catalog{}, r.ErrBudget
	}
	next := Catalog{Domain: c.Domain, Flow: c.Flow, Favourite: c.Favourite, Archived: c.Archived, Collections: append([]string{}, c.Collections...), LastOpened: cur.LastOpened, Revision: cur.Revision + 1}
	all[i].Catalog = &next
	if e = s.write(owner, all); e != nil {
		return Catalog{}, e
	}
	return next, nil
}

// Opened is Get for a version that is being opened: it also records the time (at) as the record's last-opened time, without a
// new revision of the metadata. When the time cannot be recorded (a full file, a failing disk) the version is still returned: opening
// must not depend on it, and the second result says whether it was recorded.
func (s *Store[T]) Opened(owner r.Identity, id string, version int, at time.Time) (Entry[T], bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) || version < 1 {
		return Entry[T]{}, false, r.ErrInvalid
	}
	all, e := s.read(owner)
	if e != nil {
		return Entry[T]{}, false, e
	}
	i := s.find(all, id)
	if i < 0 || version > len(all[i].Versions) {
		return Entry[T]{}, false, r.ErrNotFound
	}
	entry := all[i].Versions[version-1]
	c := all[i].catalog()
	c.LastOpened = at.UTC().Format(time.RFC3339Nano)
	all[i].Catalog = &c
	return entry, s.write(owner, all) == nil, nil
}

func (s *Store[T]) find(all []record[T], id string) int {
	for i, item := range all {
		if item.Versions[0].ID == id {
			return i
		}
	}
	return -1
}
