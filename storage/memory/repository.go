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
	order     []string              // job ids in admission order, for the issue listing
	admitted  map[string]time.Time  // when each job was admitted, by this store's clock (the quota of jobs per hour, S04-D)
	objects   r.ObjectStore         // only for the sweep's deletes and reconciliation (UseObjects); nil: none
	audit     []r.AuditEvent        // S12-B, in the order recorded
	schedules map[string]r.Schedule // S16-A, by id; made by the first schedule
	runs      map[string]*storedRun // S16-A, by id
}

// UseObjects tells the store which object store the artifacts it is given have their files in (S04-E), so that its sweep deletes the objects
// of the artifact rows it deletes and reconciles the bucket, as the durable store's does. The store never writes an object and never reads one: the
// service that publishes to it puts the files there (Config.Objects), and the store keeps the rows, with an Object or with the content inline as
// they come. The store's clock is the platform's clock for the age of an object.
func (s *Store) UseObjects(o r.ObjectStore) *Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects = o
	return s
}

func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{now: now, jobs: map[string]r.Job{}, keys: map[string]string{}, snapshots: map[string]r.Snapshot{}, artifacts: map[string]r.Artifact{}, issues: map[string]r.Issue{}, admitted: map[string]time.Time{}}
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

// Admit counts under the store's mutex what PostgreSQL counts under the identity's advisory lock (docs/S04D_SCOPE.md D2), by the same rules:
// after the replay of the key is settled and before the insert, so that a replay is never refused by a quota and a refusal stores nothing.
func (s *Store) Admit(ctx context.Context, j r.Job, quotas r.Quotas) (r.Job, error) {
	if e := ctx.Err(); e != nil {
		return r.Job{}, e
	}
	if e := r.ValidateAdmission(j); e != nil {
		return r.Job{}, e
	}
	quotas, e := quotas.Resolved()
	if e != nil {
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
	if q := s.overQuota(j.Owner.Identity, quotas); q != "" {
		return r.Job{}, &r.QuotaError{Quota: q}
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
	s.admitted[j.ID] = s.now()
	s.order = append(s.order, j.ID)
	return clone(j)
}

// overQuota names the first quota (jobs in flight, stored results, admissions per hour) that the identity has reached, or "". Only the
// identity's own rows count: queued and running jobs of any kind; snapshots and artifacts whose expiry is after the store's clock (an
// expired one waits for the sweep and does not count; an issue record is not among them); jobs admitted within the last hour. Called
// under the mutex.
func (s *Store) overQuota(i r.Identity, q r.Quotas) string {
	now := s.now()
	inFlight, results, lastHour := 0, 0, 0
	for id, j := range s.jobs {
		if j.Owner.Identity != i {
			continue
		}
		if j.State == "queued" || j.State == "running" {
			inFlight++
		}
		if s.admitted[id].After(now.Add(-time.Hour)) {
			lastHour++
		}
	}
	for _, v := range s.snapshots {
		if v.Owner.Identity == i && v.ExpiresAt.After(now) {
			results++
		}
	}
	for _, v := range s.artifacts {
		if v.Owner.Identity == i && v.ExpiresAt.After(now) {
			results++
		}
	}
	switch {
	case inFlight >= q.JobsInFlight:
		return r.QuotaJobsInFlight
	case results >= q.StoredResults:
		return r.QuotaStoredResults
	case lastHour >= q.AdmissionsPerHour:
		return r.QuotaAdmissionsPerHour
	}
	return ""
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
	return s.listing(ctx, i, integrations, limit, r.MaxIssueListing, "issue", "", "")
}
func (s *Store) Jobs(ctx context.Context, i r.Identity, l r.JobListing) ([]r.Job, error) {
	if !l.Valid() {
		return nil, r.ErrInvalid
	}
	return s.listing(ctx, i, l.Integrations, l.Limit, r.MaxJobListing, l.Kind, l.State, l.Before)
}

// listing is an identity's jobs under the given integrations, newest first (the order of admission, reversed), at most limit of them;
// kind and state "" mean any; before "" means from the newest, else from the job admitted before that one (which must be the identity's,
// under these integrations, or the page is empty).
func (s *Store) listing(ctx context.Context, i r.Identity, integrations []string, limit, most int, kind, state, before string) ([]r.Job, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if limit < 1 || limit > most || !i.Valid() || len(integrations) == 0 {
		return nil, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []r.Job{}
	from := len(s.order) - 1
	if before != "" {
		at := slices.Index(s.order, before)
		if b, ok := s.jobs[before]; at < 0 || !ok || b.Owner.Identity != i || !slices.Contains(integrations, b.Owner.Integration) {
			return out, nil
		}
		from = at - 1
	}
	for n := from; n >= 0 && len(out) < limit; n-- {
		j := s.jobs[s.order[n]]
		if (kind == "" || j.Kind == kind) && (state == "" || j.State == state) && j.Owner.Identity == i && slices.Contains(integrations, j.Owner.Integration) {
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
	s.mu.Lock()
	objects := s.objects
	s.mu.Unlock()
	if o.DryRun {
		s.dryRun(o, &res)
		if objects != nil {
			e = s.reconcile(ctx, objects, o, nil, &res)
		}
		return res, e
	}
	// One kind of row at a time, in batches until a batch is short: each takes the lock, picks at most Batch rows by the store's
	// clock as it is then, changes them and lets go. The objects the rows named are deleted after the lock is let go (the row first, then
	// the object: ADR 0007 decision 4); one that cannot be deleted is left for the reconciliation.
	left := map[string]bool{} // the keys this sweep meant to delete and could not: the reconciliation does not count them twice
	batches := func(count *int, pick func(*retained) []string, apply func(string) string) error {
		for {
			if e := ctx.Err(); e != nil {
				return e
			}
			s.mu.Lock()
			ids := pick(s.retention(o.Grace))
			if len(ids) > o.Batch {
				ids = ids[:o.Batch]
			}
			var keys []string
			for _, id := range ids {
				if key := apply(id); key != "" {
					keys = append(keys, key)
				}
			}
			s.mu.Unlock()
			*count += len(ids)
			if len(ids) > 0 {
				res.Batches++
			}
			for _, key := range keys {
				if objects == nil || objects.Delete(ctx, key) != nil {
					res.ObjectsLeft++
					left[key] = true
				} else {
					res.Objects++
				}
			}
			if len(ids) < o.Batch {
				return nil
			}
		}
	}
	if e = batches(&res.Artifacts, func(c *retained) []string { return c.artifacts }, func(id string) string {
		key := s.artifacts[id].Object
		delete(s.artifacts, id)
		if key == r.ObjectKey(id) {
			return key
		}
		return ""
	}); e != nil {
		return res, e
	}
	if e = batches(&res.Snapshots, func(c *retained) []string { return c.snapshots }, func(id string) string { delete(s.snapshots, id); return "" }); e != nil {
		return res, e
	}
	if e = batches(&res.Jobs, func(c *retained) []string {
		return c.reducible(func(j r.Job) bool { return !s.holdsSnapshot(j.SnapshotID) && !s.holdsArtifact(j.ArtifactID) })
	}, func(id string) string { s.jobs[id] = r.ReduceJob(s.jobs[id]); return "" }); e != nil {
		return res, e
	}
	if objects != nil {
		e = s.reconcile(ctx, objects, o, left, &res)
	}
	return res, e
}

// dryRun counts what the three steps would do in turn, under the lock.
func (s *Store) dryRun(o r.SweepOptions, res *r.SweepResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.retention(o.Grace)
	res.Artifacts, res.Snapshots = len(c.artifacts), len(c.snapshots)
	named := 0
	for _, id := range c.artifacts {
		if s.artifacts[id].Object != "" {
			named++
		}
	}
	if s.objects != nil {
		res.Objects = named
	} else {
		res.ObjectsLeft = named
	}
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
}

// reconcile deletes the objects of the bucket's artifact prefix that no row names (a snapshot, an artifact or an issue record, by id) and that
// are older than the grace, at least r.MinOrphanAge, by the store's clock (docs/S04E_SCOPE.md E3).
func (s *Store) reconcile(ctx context.Context, objects r.ObjectStore, o r.SweepOptions, left map[string]bool, res *r.SweepResult) error {
	age := max(o.Grace, r.MinOrphanAge)
	after := ""
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		page, next, e := objects.List(ctx, r.ObjectPrefix, after, 1000)
		if e != nil {
			return e
		}
		cutoff := s.now().Add(-age)
		s.mu.Lock()
		var orphans []string
		for _, obj := range page {
			id, ok := r.ObjectID(obj.Key)
			if !ok || !obj.LastModified.Before(cutoff) || left[obj.Key] {
				continue
			}
			_, a := s.artifacts[id]
			_, sn := s.snapshots[id]
			_, is := s.issues[id]
			if !a && !sn && !is {
				orphans = append(orphans, obj.Key)
			}
		}
		s.mu.Unlock()
		for _, key := range orphans {
			switch {
			case o.DryRun:
				res.Orphans++
			case objects.Delete(ctx, key) != nil:
				res.ObjectsLeft++
			default:
				res.Orphans++
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
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
