// Package layouts stores private metadata-only screen layouts in a single-process local directory.
package layouts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	r "idp.local/reporting/reporting"
	"io"
	"os"
	"path/filepath"
	"sync"
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
type Entry struct {
	ID      string   `json:"id"`
	Version int      `json:"version"`
	Layout  Document `json:"layout"`
}
type Store struct {
	mu     sync.Mutex
	dir    string
	closed bool
}

const maxFile = 8 << 20

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, r.ErrInvalid
	}
	abs, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		return nil, e
	}
	info, e := os.Lstat(abs)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, r.ErrInvalid
	}
	f, e := os.OpenFile(filepath.Join(abs, ".lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, r.ErrConflict
	}
	if e = f.Close(); e != nil {
		return nil, e
	}
	return &Store{dir: abs}, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(filepath.Join(s.dir, ".lock"))
}
func ValidID(id string) bool {
	b, e := hex.DecodeString(id)
	return e == nil && len(b) == 16 && hex.EncodeToString(b) == id
}
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
func (s *Store) path(owner r.Identity) string {
	b, _ := json.Marshal(owner)
	h := sha256.Sum256(b)
	return filepath.Join(s.dir, hex.EncodeToString(h[:])+".json")
}
func (s *Store) read(owner r.Identity) ([][]Entry, error) {
	if s.closed {
		return nil, r.ErrUnavailable
	}
	if !owner.Valid() {
		return nil, r.ErrDenied
	}
	path := s.path(owner)
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return [][]Entry{}, nil
	}
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > maxFile {
		return nil, r.ErrIntegrity
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, maxFile+1))
	decoder.DisallowUnknownFields()
	var records [][]Entry
	if decoder.Decode(&records) != nil || len(records) > 16 {
		return nil, r.ErrIntegrity
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return nil, r.ErrIntegrity
	}
	ids := map[string]bool{}
	for _, versions := range records {
		if len(versions) == 0 || len(versions) > 20 {
			return nil, r.ErrIntegrity
		}
		id := versions[0].ID
		if !ValidID(id) || ids[id] {
			return nil, r.ErrIntegrity
		}
		ids[id] = true
		for i, v := range versions {
			if v.ID != id || v.Version != i+1 || Validate(v.Layout) != nil || v.Layout.Binding != versions[0].Layout.Binding {
				return nil, r.ErrIntegrity
			}
		}
	}
	return records, nil
}
func (s *Store) List(owner r.Identity) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, e := s.read(owner)
	if e != nil {
		return nil, e
	}
	out := []Entry{}
	for _, versions := range all {
		out = append(out, versions[len(versions)-1])
	}
	return out, nil
}
func (s *Store) Get(owner r.Identity, id string, version int) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) || version < 1 {
		return Entry{}, r.ErrInvalid
	}
	all, e := s.read(owner)
	if e != nil {
		return Entry{}, e
	}
	for _, versions := range all {
		if versions[0].ID == id && version <= len(versions) {
			return versions[version-1], nil
		}
	}
	return Entry{}, r.ErrNotFound
}
func (s *Store) Save(owner r.Identity, id string, expected int, d Document) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := Validate(d); e != nil {
		return Entry{}, e
	}
	all, e := s.read(owner)
	if e != nil {
		return Entry{}, e
	}
	index := -1
	if id == "" {
		if expected != 0 {
			return Entry{}, r.ErrInvalid
		}
		if len(all) >= 16 {
			return Entry{}, r.ErrBudget
		}
		var b [16]byte
		if _, e = rand.Read(b[:]); e != nil {
			return Entry{}, e
		}
		id = hex.EncodeToString(b[:])
		all = append(all, []Entry{})
		index = len(all) - 1
	} else {
		if !ValidID(id) {
			return Entry{}, r.ErrInvalid
		}
		for i, v := range all {
			if v[0].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return Entry{}, r.ErrNotFound
		}
		if expected != len(all[index]) {
			return Entry{}, r.ErrConflict
		}
		if len(all[index]) >= 20 {
			return Entry{}, r.ErrBudget
		}
		if d.Binding != all[index][0].Layout.Binding {
			return Entry{}, r.ErrInvalid
		}
	}
	entry := Entry{ID: id, Version: len(all[index]) + 1, Layout: d}
	all[index] = append(all[index], entry)
	data, e := json.Marshal(all)
	if e != nil {
		return Entry{}, e
	}
	if len(data) > maxFile {
		return Entry{}, r.ErrBudget
	}
	f, e := os.CreateTemp(s.dir, ".layout-")
	if e != nil {
		return Entry{}, e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(data); e != nil {
		f.Close()
		return Entry{}, e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return Entry{}, e
	}
	if e = f.Close(); e != nil {
		return Entry{}, e
	}
	if e = os.Rename(f.Name(), s.path(owner)); e != nil {
		return Entry{}, e
	}
	dir, e := os.Open(s.dir)
	if e != nil {
		return Entry{}, e
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return Entry{}, e
	}
	return entry, nil
}
