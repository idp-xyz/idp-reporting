package privatedocs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	r "idp.local/reporting/reporting"
)

// Backend keeps one opaque document per owner: the JSON of all the owner's records in one store (S11-A, docs/S11A_SCOPE.md). The Store
// decodes, validates and bounds what it reads, and encodes what it writes; a backend only keeps bytes, atomically.
//
// Load reads the owner's document; nil (and no error) means the owner has none. Begin starts a change of the owner's document: the Txn
// reads it, replaces it and commits; until Commit nothing is visible, and between Begin and Commit no other change of the same owner's
// document can commit (the file backend is one process's, which the Store's own lock serializes; the database backend locks the row).
// A Txn that is neither committed nor rolled back holds that lock: the Store always ends it.
type Backend interface {
	Load(owner r.Identity) ([]byte, error)
	Begin(owner r.Identity) (Txn, error)
	Close() error
}

// Txn is one change of an owner's document. Load answers what Begin found (nil for none); Store replaces it in the transaction; Commit
// makes it the document; Rollback (also after Commit, where it does nothing) leaves the document as it was.
type Txn interface {
	Load() ([]byte, error)
	Store(data []byte) error
	Commit() error
	Rollback() error
}

// MaxDocument is the bound of one owner's document in every backend (8 MiB, the file's since S03-B).
const MaxDocument = maxFile

// ---- the private directory (one process) -------------------------------------------------------------------------------------------

// fileBackend is the store of a private directory: one file per owner, named by the SHA-256 of the owner, written to a temporary file and
// renamed over the old one. The directory's .lock file keeps a second process out (Open refuses a directory another process holds).
type fileBackend struct {
	dir string
}

func openFileBackend(dir string) (*fileBackend, error) {
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
	return &fileBackend{dir: abs}, nil
}

func (b *fileBackend) path(owner r.Identity) string {
	raw, _ := json.Marshal(owner)
	h := sha256.Sum256(raw)
	return filepath.Join(b.dir, hex.EncodeToString(h[:])+".json")
}

func (b *fileBackend) Load(owner r.Identity) ([]byte, error) {
	path := b.path(owner)
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil, nil
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
	return data, nil
}

// Begin of the file backend: the Store holds its lock for the whole change, and the directory is one process's.
func (b *fileBackend) Begin(owner r.Identity) (Txn, error) {
	data, e := b.Load(owner)
	if e != nil {
		return nil, e
	}
	return &fileTxn{b: b, owner: owner, data: data}, nil
}

func (b *fileBackend) Close() error { return os.Remove(filepath.Join(b.dir, ".lock")) }

type fileTxn struct {
	b       *fileBackend
	owner   r.Identity
	data    []byte
	pending []byte
	done    bool
}

func (t *fileTxn) Load() ([]byte, error) { return t.data, nil }
func (t *fileTxn) Rollback() error       { t.done = true; return nil }

// Store keeps the document until Commit, which replaces the file by a rename: it is never half written.
func (t *fileTxn) Store(data []byte) error {
	if len(data) > maxFile {
		return r.ErrBudget
	}
	t.pending = bytes.Clone(data)
	return nil
}

func (t *fileTxn) Commit() error {
	if t.done {
		return r.ErrConflict
	}
	t.done = true
	if t.pending == nil {
		return nil
	}
	data := t.pending
	f, e := os.CreateTemp(t.b.dir, ".layout-")
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
	if e = os.Rename(f.Name(), t.b.path(t.owner)); e != nil {
		return e
	}
	dir, e := os.Open(t.b.dir)
	if e != nil {
		return e
	}
	e = dir.Sync()
	dir.Close()
	return e
}

// ---- memory (tests and the demo's -memory) ---------------------------------------------------------------------------------------

// MemoryBackend keeps the documents in the process: for tests and for a demo whose designs need not outlive it. Its changes are
// serialized by one lock held from Begin to Commit or Rollback, as the database backend's row lock does for one owner.
type MemoryBackend struct {
	mu     sync.Mutex
	change sync.Mutex
	docs   map[string][]byte
}

// NewMemoryBackend answers an empty memory backend.
func NewMemoryBackend() *MemoryBackend { return &MemoryBackend{docs: map[string][]byte{}} }

func memoryKey(owner r.Identity) string { b, _ := json.Marshal(owner); return string(b) }

func (b *MemoryBackend) Load(owner r.Identity) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.docs[memoryKey(owner)]), nil
}

func (b *MemoryBackend) Begin(owner r.Identity) (Txn, error) {
	b.change.Lock()
	data, _ := b.Load(owner)
	return &memoryTxn{b: b, key: memoryKey(owner), data: data, open: true}, nil
}

func (b *MemoryBackend) Close() error { return nil }

type memoryTxn struct {
	b       *MemoryBackend
	key     string
	data    []byte
	written bool
	open    bool
}

func (t *memoryTxn) Load() ([]byte, error) { return t.data, nil }
func (t *memoryTxn) Store(data []byte) error {
	if len(data) > maxFile {
		return r.ErrBudget
	}
	t.data, t.written = bytes.Clone(data), true
	return nil
}
func (t *memoryTxn) Commit() error {
	if !t.open {
		return r.ErrConflict
	}
	if t.written {
		t.b.mu.Lock()
		t.b.docs[t.key] = t.data
		t.b.mu.Unlock()
	}
	t.open = false
	t.b.change.Unlock()
	return nil
}
func (t *memoryTxn) Rollback() error {
	if t.open {
		t.open = false
		t.b.change.Unlock()
	}
	return nil
}
