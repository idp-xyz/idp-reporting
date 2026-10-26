// Package layouts stores private metadata-only screen layouts in a single-process local directory.
package layouts

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	r "idp.local/reporting/reporting"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
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
type ReleaseEvent struct {
	Revision int    `json:"revision"`
	Action   string `json:"action"`
	Version  int    `json:"version"`
	At       string `json:"at"`
}
type ReleaseState struct {
	Revision         int            `json:"revision"`
	PublishedVersion int            `json:"published_version"`
	History          []ReleaseEvent `json:"history"`
}
type record struct {
	Versions []Entry        `json:"versions"`
	Releases []ReleaseEvent `json:"releases"`
}
type database struct {
	Schema  int      `json:"schema_version"`
	Records []record `json:"records"`
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
func (s *Store) read(owner r.Identity) ([]record, error) {
	if s.closed {
		return nil, r.ErrUnavailable
	}
	if !owner.Valid() {
		return nil, r.ErrDenied
	}
	path := s.path(owner)
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return []record{}, nil
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
	data, e := io.ReadAll(io.LimitReader(f, maxFile+1))
	if e != nil || len(data) > maxFile {
		return nil, r.ErrIntegrity
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, r.ErrIntegrity
	}
	decode := func(out any) error {
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(out) != nil {
			return r.ErrIntegrity
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return r.ErrIntegrity
		}
		return nil
	}
	var records []record
	if data[0] == '[' {
		var old [][]Entry
		if decode(&old) != nil || len(old) == 0 {
			return nil, r.ErrIntegrity
		}
		for _, versions := range old {
			records = append(records, record{Versions: versions, Releases: []ReleaseEvent{}})
		}
	} else {
		var db database
		if decode(&db) != nil || db.Schema != 2 {
			return nil, r.ErrIntegrity
		}
		records = db.Records
	}
	if len(records) == 0 || len(records) > 16 {
		return nil, r.ErrIntegrity
	}
	ids := map[string]bool{}
	for _, item := range records {
		versions := item.Versions
		if len(versions) == 0 || len(versions) > 20 || item.Releases == nil || len(item.Releases) > 64 {
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
		published := 0
		for i, event := range item.Releases {
			if event.Revision != i+1 {
				return nil, r.ErrIntegrity
			}
			if _, e := time.Parse(time.RFC3339Nano, event.At); e != nil {
				return nil, r.ErrIntegrity
			}
			switch event.Action {
			case "publish":
				if event.Version < 1 || event.Version > len(versions) {
					return nil, r.ErrIntegrity
				}
				published = event.Version
			case "withdraw":
				if event.Version != 0 || published == 0 {
					return nil, r.ErrIntegrity
				}
				published = 0
			default:
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
	for _, item := range all {
		versions := item.Versions
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
	for _, item := range all {
		versions := item.Versions
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
		all = append(all, record{Versions: []Entry{}, Releases: []ReleaseEvent{}})
		index = len(all) - 1
	} else {
		if !ValidID(id) {
			return Entry{}, r.ErrInvalid
		}
		for i, v := range all {
			if v.Versions[0].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return Entry{}, r.ErrNotFound
		}
		if expected != len(all[index].Versions) {
			return Entry{}, r.ErrConflict
		}
		if len(all[index].Versions) >= 20 {
			return Entry{}, r.ErrBudget
		}
		if d.Binding != all[index].Versions[0].Layout.Binding {
			return Entry{}, r.ErrInvalid
		}
	}
	entry := Entry{ID: id, Version: len(all[index].Versions) + 1, Layout: d}
	all[index].Versions = append(all[index].Versions, entry)
	if e = s.write(owner, all); e != nil {
		return Entry{}, e
	}
	return entry, nil
}
func (s *Store) write(owner r.Identity, all []record) error {
	data, e := json.Marshal(database{Schema: 2, Records: all})
	if e != nil {
		return e
	}
	if len(data) > maxFile {
		return r.ErrBudget
	}
	f, e := os.CreateTemp(s.dir, ".layout-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), s.path(owner)); e != nil {
		return e
	}
	dir, e := os.Open(s.dir)
	if e != nil {
		return e
	}
	e = dir.Sync()
	dir.Close()
	return e
}
func releaseState(events []ReleaseEvent) ReleaseState {
	out := ReleaseState{Revision: len(events), History: append([]ReleaseEvent{}, events...)}
	if len(events) > 0 && events[len(events)-1].Action == "publish" {
		out.PublishedVersion = events[len(events)-1].Version
	}
	return out
}
func (s *Store) Release(owner r.Identity, id string) (ReleaseState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return ReleaseState{}, r.ErrInvalid
	}
	all, e := s.read(owner)
	if e != nil {
		return ReleaseState{}, e
	}
	for _, item := range all {
		if item.Versions[0].ID == id {
			return releaseState(item.Releases), nil
		}
	}
	return ReleaseState{}, r.ErrNotFound
}
func (s *Store) Publish(owner r.Identity, id, action string, version, expected int) (ReleaseState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) || expected < 0 || (action != "publish" && action != "withdraw") {
		return ReleaseState{}, r.ErrInvalid
	}
	all, e := s.read(owner)
	if e != nil {
		return ReleaseState{}, e
	}
	for i, item := range all {
		if item.Versions[0].ID != id {
			continue
		}
		state := releaseState(item.Releases)
		if expected != state.Revision {
			return ReleaseState{}, r.ErrConflict
		}
		if len(item.Releases) >= 64 {
			return ReleaseState{}, r.ErrBudget
		}
		if action == "publish" {
			if version < 1 || version > len(item.Versions) {
				return ReleaseState{}, r.ErrInvalid
			}
		} else if version != 0 || state.PublishedVersion == 0 {
			return ReleaseState{}, r.ErrInvalid
		}
		event := ReleaseEvent{Revision: expected + 1, Action: action, Version: version, At: time.Now().UTC().Format(time.RFC3339Nano)}
		all[i].Releases = append(item.Releases, event)
		if e = s.write(owner, all); e != nil {
			return ReleaseState{}, e
		}
		return releaseState(all[i].Releases), nil
	}
	return ReleaseState{}, r.ErrNotFound
}
func (s *Store) Published(owner r.Identity, id string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return Entry{}, r.ErrInvalid
	}
	all, e := s.read(owner)
	if e != nil {
		return Entry{}, e
	}
	for _, item := range all {
		if item.Versions[0].ID == id {
			state := releaseState(item.Releases)
			if state.PublishedVersion == 0 {
				return Entry{}, r.ErrNotFound
			}
			return item.Versions[state.PublishedVersion-1], nil
		}
	}
	return Entry{}, r.ErrNotFound
}
