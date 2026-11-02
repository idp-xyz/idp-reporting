package objects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"

	r "idp.local/reporting/reporting"
)

// Memory is an object store in a map with the contract of reporting.ObjectStore, for the tests that exercise the service path and the stores
// without a server (docs/S04E_SCOPE.md E1). It answers as S3 does: a missing object, another length or another digest is r.ErrIntegrity,
// an injected outage is a *r.ObjectStoreError. It is not a way to keep anything: its objects die with the process.
type Memory struct {
	mu      sync.Mutex
	now     func() time.Time
	objects map[string]stored
	fault   func(op, key string) bool
	calls   map[string]int
}

type stored struct {
	content []byte
	at      time.Time
}

// NewMemory makes an empty store whose objects are stamped by now (nil is time.Now): the store's clock is the one that stamps an object, as
// an object store's is.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, objects: map[string]stored{}, calls: map[string]int{}}
}

func (m *Memory) enter(op, key string) error {
	m.calls[op]++
	if m.fault != nil && m.fault(op, key) {
		return &r.ObjectStoreError{Status: 503}
	}
	return nil
}

func (m *Memory) Put(ctx context.Context, key string, content []byte, sha string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter("put", key); err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	if !validKey(key) || len(content) == 0 || len(content) > r.MaxArtifactBytes || hex.EncodeToString(sum[:]) != sha {
		return r.ErrInvalid
	}
	m.objects[key] = stored{content: slices.Clone(content), at: m.now()}
	return nil
}

func (m *Memory) Get(ctx context.Context, key, sha string, length int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter("get", key); err != nil {
		return nil, err
	}
	if !validKey(key) || length < 1 || length > r.MaxArtifactBytes || !validDigest(sha) {
		return nil, r.ErrInvalid
	}
	o, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("the object is missing: %w", r.ErrIntegrity)
	}
	if len(o.content) != length {
		return nil, fmt.Errorf("the object's length is not the row's: %w", r.ErrIntegrity)
	}
	if sum := sha256.Sum256(o.content); hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("the object's digest is not the row's: %w", r.ErrIntegrity)
	}
	return slices.Clone(o.content), nil
}

func (m *Memory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter("delete", key); err != nil {
		return err
	}
	if !validKey(key) {
		return r.ErrInvalid
	}
	delete(m.objects, key)
	return nil
}

// List pages by key: the continuation token is the last key of the previous page.
func (m *Memory) List(ctx context.Context, prefix, after string, limit int) ([]r.Object, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter("list", prefix); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > MaxListLimit {
		return nil, "", r.ErrInvalid
	}
	keys := []string{}
	for k := range m.objects {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix && k > after {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	next := ""
	if len(keys) > limit {
		keys = keys[:limit]
		next = keys[limit-1]
	}
	out := make([]r.Object, len(keys))
	for i, k := range keys {
		out[i] = r.Object{Key: k, LastModified: m.objects[k].at.UTC()}
	}
	return out, next, nil
}

func (m *Memory) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enter("check", "")
}

// The rest is for tests: it is how a test breaks the store the way a real one breaks.

// Fail makes every call for which fail(op, key) is true an outage (a *r.ObjectStoreError); op is "put", "get", "delete", "list" or "check".
// A nil function ends the outage.
func (m *Memory) Fail(fail func(op, key string) bool) { m.mu.Lock(); m.fault = fail; m.mu.Unlock() }

// Calls says how many times an operation was called, the failed ones included.
func (m *Memory) Calls(op string) int { m.mu.Lock(); defer m.mu.Unlock(); return m.calls[op] }

// Keys lists the keys held, sorted.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objects))
	for k := range m.objects {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Replace changes the bytes of an object without anything else changing: a store that corrupted what it held.
func (m *Memory) Replace(key string, content []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if ok {
		m.objects[key] = stored{content: slices.Clone(content), at: o.at}
	}
	return ok
}

// Remove deletes an object behind the platform's back: a store that lost what it held.
func (m *Memory) Remove(key string) { m.mu.Lock(); delete(m.objects, key); m.mu.Unlock() }

// SetLastModified changes the time an object is stamped with: an object put long ago.
func (m *Memory) SetLastModified(key string, at time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if ok {
		o.at = at
		m.objects[key] = o
	}
	return ok
}

// Plant stores an object under a key the platform did not make, as a stray object of another process would be.
func (m *Memory) Plant(key string, content []byte, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = stored{content: slices.Clone(content), at: at}
}

var _ r.ObjectStore = (*Memory)(nil)
