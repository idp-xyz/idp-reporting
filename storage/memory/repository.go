// Package memory is a bounded, process-local contract model, not durable storage.
package memory

import (
	"context"
	"encoding/json"
	r "idp.local/reporting/reporting"
	"sync"
	"time"
)

type Store struct {
	mu        sync.Mutex
	now       func() time.Time
	jobs      map[string]r.Job
	keys      map[string]string
	snapshots map[string]r.Snapshot
	artifacts map[string]r.Artifact
}

func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{now: now, jobs: map[string]r.Job{}, keys: map[string]string{}, snapshots: map[string]r.Snapshot{}, artifacts: map[string]r.Artifact{}}
}
func clone[T any](v T) T {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	var out T
	if e = json.Unmarshal(b, &out); e != nil {
		panic(e)
	}
	return out
}
func (s *Store) Admit(ctx context.Context, j r.Job) (r.Job, error) {
	if e := ctx.Err(); e != nil {
		return r.Job{}, e
	}
	if e := r.ValidateAdmission(j); e != nil {
		return r.Job{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.Digest(struct {
		Owner     r.Owner
		Kind, Key string
	}{j.Owner, j.Kind, j.Key})
	if id, ok := s.keys[key]; ok {
		v := s.jobs[id]
		if v.RequestHash != j.RequestHash {
			return r.Job{}, r.ErrConflict
		}
		return clone(v), nil
	}
	if _, ok := s.jobs[j.ID]; ok {
		return r.Job{}, r.ErrConflict
	}
	if len(s.jobs) >= 10000 {
		return r.Job{}, r.ErrBudget
	}
	s.keys[key] = j.ID
	s.jobs[j.ID] = clone(j)
	return clone(j), nil
}
func (s *Store) Job(ctx context.Context, i r.Identity, id string) (r.Job, error) {
	if e := ctx.Err(); e != nil {
		return r.Job{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.jobs[id]
	if !ok || v.Owner.Identity != i {
		return r.Job{}, r.ErrNotFound
	}
	return clone(v), nil
}
func (s *Store) Cancel(ctx context.Context, i r.Identity, id string) (r.Job, error) {
	if e := ctx.Err(); e != nil {
		return r.Job{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.jobs[id]
	if !ok || v.Owner.Identity != i {
		return r.Job{}, r.ErrNotFound
	}
	if v.State == "queued" || v.State == "running" {
		v.State = "cancelled"
		v.LeaseUntil = time.Time{}
		s.jobs[id] = v
	}
	return clone(v), nil
}
func (s *Store) Claim(ctx context.Context, lease time.Duration) (*r.Claim, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if lease < time.Second || lease > 5*time.Minute {
		return nil, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		if j.State == "queued" || (j.State == "running" && !j.LeaseUntil.After(s.now())) {
			j.State = "running"
			j.Generation++
			j.LeaseUntil = s.now().Add(lease)
			s.jobs[id] = j
			return &r.Claim{Job: clone(j)}, nil
		}
	}
	return nil, nil
}
func (s *Store) Publish(ctx context.Context, c r.Claim, p r.Publication) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[c.Job.ID]
	if !ok || !r.ClaimValid(j, c, s.now()) {
		return r.ErrLease
	}
	if e := r.ValidatePublication(j, p); e != nil {
		return e
	}
	p = clone(p)
	if e := ctx.Err(); e != nil {
		return e
	}
	// Materialization can consume the remaining lease. Fence at the mutation
	// point as well as admission to this critical section.
	if !r.ClaimValid(j, c, s.now()) {
		return r.ErrLease
	}
	if !s.now().Before(j.Prepared.ExpiresAt) {
		return r.ErrDenied
	}
	if p.Snapshot != nil {
		if _, exists := s.snapshots[p.Snapshot.ID]; exists {
			return r.ErrConflict
		}
		s.snapshots[p.Snapshot.ID] = *p.Snapshot
		j.SnapshotID = p.Snapshot.ID
	}
	if p.Artifact != nil {
		if _, exists := s.artifacts[p.Artifact.ID]; exists {
			return r.ErrConflict
		}
		s.artifacts[p.Artifact.ID] = *p.Artifact
		j.ArtifactID = p.Artifact.ID
	}
	j.State = "succeeded"
	j.LeaseUntil = time.Time{}
	s.jobs[j.ID] = j
	return nil
}
func (s *Store) Fail(ctx context.Context, c r.Claim, code string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[c.Job.ID]
	if !ok || !r.ClaimValid(j, c, s.now()) {
		return r.ErrLease
	}
	j.State = "failed"
	j.ErrorCode = code
	j.LeaseUntil = time.Time{}
	s.jobs[j.ID] = j
	return nil
}
func (s *Store) Snapshot(ctx context.Context, i r.Identity, id string) (r.Snapshot, error) {
	if e := ctx.Err(); e != nil {
		return r.Snapshot{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.snapshots[id]
	if !ok || v.Owner.Identity != i {
		return r.Snapshot{}, r.ErrNotFound
	}
	return clone(v), nil
}
func (s *Store) Artifact(ctx context.Context, i r.Identity, id string) (r.Artifact, error) {
	if e := ctx.Err(); e != nil {
		return r.Artifact{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.artifacts[id]
	if !ok || v.Owner.Identity != i {
		return r.Artifact{}, r.ErrNotFound
	}
	return clone(v), nil
}

var _ r.Repository = (*Store)(nil)
