// Package memory is a bounded, process-local contract model, not durable storage.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	r "idp.local/reporting/reporting"
	"slices"
	"strings"
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
	issues    map[string]r.Issue
	order     []string // job ids in admission order, for the issue listing
}

func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{now: now, jobs: map[string]r.Job{}, keys: map[string]string{}, snapshots: map[string]r.Snapshot{}, artifacts: map[string]r.Artifact{}, issues: map[string]r.Issue{}}
}

// clone copies a value through JSON, as the durable store keeps it. A value that cannot be encoded
// (a time outside the years 0..9999, say) is an error, never a panic: a worker must not die with a job
// running because of what it was asked to store.
func clone[T any](v T) (T, error) {
	var out T
	b, e := json.Marshal(v)
	if e != nil {
		return out, fmt.Errorf("memory store: a value cannot be copied: %w", e)
	}
	if e = json.Unmarshal(b, &out); e != nil {
		return out, fmt.Errorf("memory store: a value cannot be copied: %w", e)
	}
	return out, nil
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
		return clone(v)
	}
	if _, ok := s.jobs[j.ID]; ok {
		return r.Job{}, r.ErrConflict
	}
	if len(s.jobs) >= 10000 {
		return r.Job{}, r.ErrBudget
	}
	stored, e := clone(j)
	if e != nil {
		return r.Job{}, e
	}
	s.keys[key] = j.ID
	s.jobs[j.ID] = stored
	s.order = append(s.order, j.ID)
	return clone(j)
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
	return clone(v)
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
	// A running issue job is not cancelled (S03-H): the source product may already hold a
	// reservation for it, so it finishes, or fails and voids; it is returned unchanged.
	if v.State == "queued" || (v.State == "running" && v.Kind != "issue") {
		v.State = "cancelled"
		v.LeaseUntil = time.Time{}
		s.jobs[id] = v
	}
	return clone(v)
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
	// In admission order, as the durable store claims by creation time.
	for _, id := range s.order {
		j := s.jobs[id]
		if j.State == "queued" || (j.State == "running" && !j.LeaseUntil.After(s.now())) {
			// A job that cannot be copied as it is stored is what the durable store cannot decode: it is failed at its claim, terminally,
			// and the next job is claimed, so that it neither blocks the jobs behind it nor is claimed again. Kind and generation are
			// its own plain fields. What cannot be copied after the claim (a lease beyond the years JSON holds, from the store's clock) is
			// not the job's: the job is left as it was.
			if _, e := clone(j); e != nil {
				j.State, j.ErrorCode, j.LeaseUntil = "failed", r.UnreadableJobCode(j.Kind, j.Generation), time.Time{}
				j.Generation++
				s.jobs[id] = j
				continue
			}
			j.State = "running"
			j.Generation++
			j.LeaseUntil = s.now().Add(lease)
			claimed, e := clone(j)
			if e != nil {
				return nil, e
			}
			s.jobs[id] = j
			return &r.Claim{Job: claimed}, nil
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
	p, e := clone(p)
	if e != nil {
		return e
	}
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
		if p.Issue != nil {
			if _, exists := s.issues[p.Issue.ID]; exists {
				return r.ErrConflict
			}
			s.issues[p.Issue.ID] = *p.Issue
		}
		s.artifacts[p.Artifact.ID] = *p.Artifact
		j.ArtifactID = p.Artifact.ID
	}
	j.State = "succeeded"
	j.LeaseUntil = time.Time{}
	s.jobs[j.ID] = j
	return nil
}

// Fail fails a job that is running, at the claim's generation, with a lease that has not run out by
// the store's clock; anything else is ErrLease.
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

// ResolveFailure replaces the code ISSUE_UNRECONCILED of a failed issue job with the job's own failure
// code once the source product has voided what it held (S03-H). It applies only to an issue job that is
// failed, at the claim's generation, with that code: anything else is ErrLease. It needs no lease.
func (s *Store) ResolveFailure(ctx context.Context, c r.Claim, code string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if code == "" || code == r.ErrUnreconciled.Error() {
		return r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[c.Job.ID]
	if !ok || j.Owner != c.Job.Owner || j.Kind != "issue" || j.State != "failed" || j.Generation != c.Job.Generation || j.ErrorCode != r.ErrUnreconciled.Error() {
		return r.ErrLease
	}
	j.ErrorCode = code
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
	return clone(v)
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
	return clone(v)
}

func (s *Store) Issue(ctx context.Context, i r.Identity, id string) (r.Issue, error) {
	if e := ctx.Err(); e != nil {
		return r.Issue{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.issues[id]
	if !ok || v.Owner.Identity != i {
		return r.Issue{}, r.ErrNotFound
	}
	return clone(v)
}
func (s *Store) Issues(ctx context.Context, i r.Identity, integrations []string, limit int) ([]r.Job, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if limit < 1 || limit > r.MaxIssueListing || !i.Valid() || len(integrations) == 0 {
		return nil, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []r.Job{}
	for n := len(s.order) - 1; n >= 0 && len(out) < limit; n-- {
		j := s.jobs[s.order[n]]
		if j.Kind == "issue" && j.Owner.Identity == i && slices.Contains(integrations, j.Owner.Integration) {
			c, e := clone(j)
			if e != nil {
				return nil, e
			}
			out = append(out, c)
		}
	}
	return out, nil
}

var _ r.Repository = (*Store)(nil)

// Sweep is the retention of S04-B on the model: the same rules as the durable store's, by the store's own clock (the clock New was
// given stands for the database's). A batch is the time the lock is held; the lock is released between batches, so that a worker, a
// download or another sweep goes on meanwhile.
//
//   - an artifact is deleted once its expiry plus the grace is past, unless a queued or running job names it or its snapshot, or its
//     snapshot is still held for a later expiry;
//   - a snapshot, once its expiry plus the grace is past, unless a queued or running job names it;
//   - an issue record never;
//   - the payload of a finished job is reduced once its expiry plus the grace is past and neither its snapshot nor its artifact is held.
func (s *Store) Sweep(ctx context.Context, o r.SweepOptions) (r.SweepResult, error) {
	o, e := o.Resolved()
	res := r.SweepResult{DryRun: o.DryRun}
	if e != nil {
		return res, e
	}
	if e = ctx.Err(); e != nil {
		return res, e
	}
	if o.DryRun {
		s.mu.Lock()
		defer s.mu.Unlock()
		c := s.retention(o.Grace)
		res.Artifacts, res.Snapshots = len(c.artifacts), len(c.snapshots)
		// What the sweep would delete counts as gone for the jobs, as it would be when the jobs are reached.
		goneArtifacts, goneSnapshots := map[string]bool{}, map[string]bool{}
		for _, id := range c.artifacts {
			goneArtifacts[id] = true
		}
		for _, id := range c.snapshots {
			goneSnapshots[id] = true
		}
		res.Jobs = len(c.reducible(func(j r.Job) bool {
			return (!s.holdsSnapshot(j.SnapshotID) || goneSnapshots[j.SnapshotID]) && (!s.holdsArtifact(j.ArtifactID) || goneArtifacts[j.ArtifactID])
		}))
		return res, nil
	}
	// One kind of row at a time, in batches until a batch is short: each takes the lock, picks at most Batch rows by the store's
	// clock as it is then, changes them and lets go.
	batches := func(count *int, pick func(*retained) []string, apply func(string)) error {
		for {
			if e := ctx.Err(); e != nil {
				return e
			}
			s.mu.Lock()
			ids := pick(s.retention(o.Grace))
			if len(ids) > o.Batch {
				ids = ids[:o.Batch]
			}
			for _, id := range ids {
				apply(id)
			}
			s.mu.Unlock()
			*count += len(ids)
			if len(ids) > 0 {
				res.Batches++
			}
			if len(ids) < o.Batch {
				return nil
			}
		}
	}
	if e = batches(&res.Artifacts, func(c *retained) []string { return c.artifacts }, func(id string) { delete(s.artifacts, id) }); e != nil {
		return res, e
	}
	if e = batches(&res.Snapshots, func(c *retained) []string { return c.snapshots }, func(id string) { delete(s.snapshots, id) }); e != nil {
		return res, e
	}
	e = batches(&res.Jobs, func(c *retained) []string {
		return c.reducible(func(j r.Job) bool { return !s.holdsSnapshot(j.SnapshotID) && !s.holdsArtifact(j.ArtifactID) })
	}, func(id string) { s.jobs[id] = r.ReduceJob(s.jobs[id]) })
	return res, e
}

// holdsSnapshot and holdsArtifact say whether the store still holds the asset a job names; a job that names none holds none.
func (s *Store) holdsSnapshot(id string) bool { _, ok := s.snapshots[id]; return id != "" && ok }
func (s *Store) holdsArtifact(id string) bool { _, ok := s.artifacts[id]; return id != "" && ok }

// retained is what a sweep finds at one instant of the store's clock, computed under the lock: the artifacts and the snapshots that may
// go (oldest expiry first, the order they are deleted in).
type retained struct {
	s         *Store
	cutoff    time.Time // an expiry before it is past its grace
	artifacts []string
	snapshots []string
}

func (s *Store) retention(grace time.Duration) *retained {
	c := &retained{s: s, cutoff: s.now().Add(-grace)}
	pastGrace := func(t time.Time) bool { return t.Before(c.cutoff) }
	// What a queued or running job names is never touched: the snapshot it reads from, and so the artifacts of that snapshot.
	named := map[string]bool{}
	for _, j := range s.jobs {
		if !r.Terminal(j.State) {
			named[j.SnapshotID], named[j.ArtifactID] = true, true
		}
	}
	for id, a := range s.artifacts {
		if snap, ok := s.snapshots[a.SnapshotID]; ok && !pastGrace(snap.ExpiresAt) {
			continue // its snapshot is held for a later expiry: the artifact is never deleted before it
		}
		if pastGrace(a.ExpiresAt) && !named[id] && !named[a.SnapshotID] {
			c.artifacts = append(c.artifacts, id)
		}
	}
	for id, v := range s.snapshots {
		if pastGrace(v.ExpiresAt) && !named[id] {
			c.snapshots = append(c.snapshots, id)
		}
	}
	byExpiry := func(expiry func(string) time.Time) func(a, b string) int {
		return func(a, b string) int {
			if c := expiry(a).Compare(expiry(b)); c != 0 {
				return c
			}
			return strings.Compare(a, b)
		}
	}
	slices.SortFunc(c.artifacts, byExpiry(func(id string) time.Time { return s.artifacts[id].ExpiresAt }))
	slices.SortFunc(c.snapshots, byExpiry(func(id string) time.Time { return s.snapshots[id].ExpiresAt }))
	return c
}

// reducible lists, in admission order, the finished jobs not yet reduced whose expiry plus the grace is past and whose results are
// gone by the test given.
func (c *retained) reducible(gone func(r.Job) bool) []string {
	var out []string
	for _, id := range c.s.order {
		j := c.s.jobs[id]
		if r.Terminal(j.State) && !j.Reduced && j.Prepared.ExpiresAt.Before(c.cutoff) && gone(j) {
			out = append(out, id)
		}
	}
	return out
}
