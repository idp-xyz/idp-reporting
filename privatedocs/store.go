// Package privatedocs stores validated private document versions: one document per owner and store, kept by a Backend (a single-writer
// local directory, memory, or the platform's database: S11-A, docs/S11A_SCOPE.md).
package privatedocs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	r "idp.local/reporting/reporting"
	"io"
	"sync"
	"time"
)

type Entry[T any] struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Layout  T      `json:"layout"`
	// SavedBy is who saved the version in a shared space (S12-C): an actor digest (reporting.ActorDigest), "" in one's own designs. Kept in
	// the document; the routes never answer it.
	SavedBy string `json:"saved_by,omitempty"`
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
type record[T any] struct {
	Versions []Entry[T]     `json:"versions"`
	Releases []ReleaseEvent `json:"releases"`
	// Catalog is the template centre's metadata of the record (S06-B); a record that never had any does not write the key, so
	// a file written before it reads and writes as it did.
	Catalog *Catalog `json:"catalog,omitempty"`
}
type database[T any] struct {
	Schema  int         `json:"schema_version"`
	Records []record[T] `json:"records"`
}
type Store[T any] struct {
	mu       sync.Mutex
	backend  Backend
	closed   bool
	validate func(T) error
	binding  func(T) string
	cap      int // records per identity
}

const maxFile = 8 << 20

// DefaultCap is the number of records one identity may keep in a store unless the store was opened with another (OpenCap).
const DefaultCap = 16

func Open[T any](dir string, validate func(T) error, binding func(T) string) (*Store[T], error) {
	return OpenCap(dir, DefaultCap, validate, binding)
}

// OpenCap is Open for a store that keeps at most cap records (1 to 1024) per identity: the 16 of DefaultCap are the bound of the
// layouts, the dashboards and the images; the document templates keep 64 since S06-B. A store opened with a lower cap than
// the file of an identity holds refuses to read it (integrity), so a cap is only ever raised.
func OpenCap[T any](dir string, cap int, validate func(T) error, binding func(T) string) (*Store[T], error) {
	if dir == "" || validate == nil || binding == nil || cap < 1 || cap > 1024 {
		return nil, r.ErrInvalid
	}
	b, e := openFileBackend(dir)
	if e != nil {
		return nil, e
	}
	return &Store[T]{backend: b, validate: validate, binding: binding, cap: cap}, nil
}

// OpenBackend is OpenCap over another backend: memory, or the platform's database (storage/postgres, S11-A). The Store closes the backend.
func OpenBackend[T any](b Backend, cap int, validate func(T) error, binding func(T) string) (*Store[T], error) {
	if b == nil || validate == nil || binding == nil || cap < 1 || cap > 1024 {
		return nil, r.ErrInvalid
	}
	return &Store[T]{backend: b, validate: validate, binding: binding, cap: cap}, nil
}
func (s *Store[T]) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.backend.Close()
}
func ValidID(id string) bool {
	b, e := hex.DecodeString(id)
	return e == nil && len(b) == 16 && hex.EncodeToString(b) == id
}
func (s *Store[T]) read(owner r.Identity) ([]record[T], error) {
	if s.closed {
		return nil, r.ErrUnavailable
	}
	if !owner.Valid() {
		return nil, r.ErrDenied
	}
	data, e := s.backend.Load(owner)
	if e != nil {
		return nil, e
	}
	return s.decode(data)
}

// begin starts a change of the owner's document: the records as the change found them. The caller ends the change (commit, or Rollback).
func (s *Store[T]) begin(owner r.Identity) (Txn, []record[T], error) {
	if s.closed {
		return nil, nil, r.ErrUnavailable
	}
	if !owner.Valid() {
		return nil, nil, r.ErrDenied
	}
	tx, e := s.backend.Begin(owner)
	if e != nil {
		return nil, nil, e
	}
	data, e := tx.Load()
	if e == nil {
		var all []record[T]
		if all, e = s.decode(data); e == nil {
			return tx, all, nil
		}
	}
	tx.Rollback()
	return nil, nil, e
}

// commit encodes the records, replaces the owner's document with them and commits the change.
func (s *Store[T]) commit(tx Txn, all []record[T]) error {
	data, e := json.Marshal(database[T]{Schema: 2, Records: all})
	if e != nil {
		return e
	}
	if len(data) > maxFile {
		return r.ErrBudget
	}
	if e = tx.Store(data); e != nil {
		return e
	}
	return tx.Commit()
}

// decode reads an owner's document (nil: none) and holds it to every rule a written document follows.
func (s *Store[T]) decode(data []byte) ([]record[T], error) {
	if data == nil {
		return []record[T]{}, nil
	}
	if len(data) > maxFile {
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
	var records []record[T]
	if data[0] == '[' {
		var old [][]Entry[T]
		if decode(&old) != nil || len(old) == 0 {
			return nil, r.ErrIntegrity
		}
		for _, versions := range old {
			records = append(records, record[T]{Versions: versions, Releases: []ReleaseEvent{}})
		}
	} else {
		var db database[T]
		if decode(&db) != nil || db.Schema != 2 {
			return nil, r.ErrIntegrity
		}
		records = db.Records
	}
	if len(records) == 0 || len(records) > s.cap {
		return nil, r.ErrIntegrity
	}
	ids := map[string]bool{}
	for _, item := range records {
		if item.Catalog != nil && validCatalog(*item.Catalog) != nil {
			return nil, r.ErrIntegrity
		}
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
			if v.ID != id || v.Version != i+1 || s.validate(v.Layout) != nil || s.binding(v.Layout) != s.binding(versions[0].Layout) {
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
func (s *Store[T]) List(owner r.Identity) ([]Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, e := s.read(owner)
	if e != nil {
		return nil, e
	}
	out := []Entry[T]{}
	for _, item := range all {
		versions := item.Versions
		out = append(out, versions[len(versions)-1])
	}
	return out, nil
}
func (s *Store[T]) Get(owner r.Identity, id string, version int) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) || version < 1 {
		return Entry[T]{}, r.ErrInvalid
	}
	all, e := s.read(owner)
	if e != nil {
		return Entry[T]{}, e
	}
	for _, item := range all {
		versions := item.Versions
		if versions[0].ID == id && version <= len(versions) {
			return versions[version-1], nil
		}
	}
	return Entry[T]{}, r.ErrNotFound
}
func (s *Store[T]) Save(owner r.Identity, id string, expected int, d T) (Entry[T], error) {
	return s.SaveAs(owner, "", id, expected, d)
}

// SaveAs is Save that records who saved the version (S12-C: an actor digest, for the shared space's approval).
func (s *Store[T]) SaveAs(owner r.Identity, saver, id string, expected int, d T) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.validate(d); e != nil {
		return Entry[T]{}, e
	}
	tx, all, e := s.begin(owner)
	if e != nil {
		return Entry[T]{}, e
	}
	defer tx.Rollback()
	index := -1
	if id == "" {
		if expected != 0 {
			return Entry[T]{}, r.ErrInvalid
		}
		if len(all) >= s.cap {
			return Entry[T]{}, r.ErrBudget
		}
		var b [16]byte
		if _, e = rand.Read(b[:]); e != nil {
			return Entry[T]{}, e
		}
		id = hex.EncodeToString(b[:])
		all = append(all, record[T]{Versions: []Entry[T]{}, Releases: []ReleaseEvent{}})
		index = len(all) - 1
	} else {
		if !ValidID(id) {
			return Entry[T]{}, r.ErrInvalid
		}
		for i, v := range all {
			if v.Versions[0].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return Entry[T]{}, r.ErrNotFound
		}
		if expected != len(all[index].Versions) {
			return Entry[T]{}, r.ErrConflict
		}
		if len(all[index].Versions) >= 20 {
			return Entry[T]{}, r.ErrBudget
		}
		if s.binding(d) != s.binding(all[index].Versions[0].Layout) {
			return Entry[T]{}, r.ErrInvalid
		}
	}
	entry := Entry[T]{ID: id, Version: len(all[index].Versions) + 1, Layout: d, SavedBy: saver}
	all[index].Versions = append(all[index].Versions, entry)
	if e = s.commit(tx, all); e != nil {
		return Entry[T]{}, e
	}
	return entry, nil
}
func releaseState(events []ReleaseEvent) ReleaseState {
	out := ReleaseState{Revision: len(events), History: append([]ReleaseEvent{}, events...)}
	if len(events) > 0 && events[len(events)-1].Action == "publish" {
		out.PublishedVersion = events[len(events)-1].Version
	}
	return out
}
func (s *Store[T]) Release(owner r.Identity, id string) (ReleaseState, error) {
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
func (s *Store[T]) Publish(owner r.Identity, id, action string, version, expected int) (ReleaseState, error) {
	return s.PublishAs(owner, "", id, action, version, expected)
}

// ErrApprovalRequired refuses a publication in a shared space by the person who saved the version (S12-C, ADR 0018 decision 3): another
// publisher approves it by publishing it.
var ErrApprovalRequired = errors.New("APPROVAL_REQUIRED")

// PublishAs is Publish by an approver (an actor digest). When approver is set, publishing a version that it saved, or that records no saver,
// is refused with ErrApprovalRequired; a withdrawal is anyone's with the role.
func (s *Store[T]) PublishAs(owner r.Identity, approver, id, action string, version, expected int) (ReleaseState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) || expected < 0 || (action != "publish" && action != "withdraw") {
		return ReleaseState{}, r.ErrInvalid
	}
	tx, all, e := s.begin(owner)
	if e != nil {
		return ReleaseState{}, e
	}
	defer tx.Rollback()
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
			if saver := item.Versions[version-1].SavedBy; approver != "" && (saver == "" || saver == approver) {
				return ReleaseState{}, ErrApprovalRequired
			}
		} else if version != 0 || state.PublishedVersion == 0 {
			return ReleaseState{}, r.ErrInvalid
		}
		event := ReleaseEvent{Revision: expected + 1, Action: action, Version: version, At: time.Now().UTC().Format(time.RFC3339Nano)}
		all[i].Releases = append(item.Releases, event)
		if e = s.commit(tx, all); e != nil {
			return ReleaseState{}, e
		}
		return releaseState(all[i].Releases), nil
	}
	return ReleaseState{}, r.ErrNotFound
}
func (s *Store[T]) Published(owner r.Identity, id string) (Entry[T], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return Entry[T]{}, r.ErrInvalid
	}
	all, e := s.read(owner)
	if e != nil {
		return Entry[T]{}, e
	}
	for _, item := range all {
		if item.Versions[0].ID == id {
			state := releaseState(item.Releases)
			if state.PublishedVersion == 0 {
				return Entry[T]{}, r.ErrNotFound
			}
			return item.Versions[state.PublishedVersion-1], nil
		}
	}
	return Entry[T]{}, r.ErrNotFound
}
