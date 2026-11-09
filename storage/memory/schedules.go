package memory

import (
	"context"
	"slices"
	"time"

	r "idp.local/reporting/reporting"
)

// The schedules of S16-A, under the store's mutex and by its clock, by the rules PostgreSQL keeps under its row locks (013).

type storedRun struct {
	run     r.ScheduleRun
	checkAt time.Time
	lease   time.Time
}

func (s *Store) scheduleState() {
	if s.schedules == nil {
		s.schedules = map[string]r.Schedule{}
		s.runs = map[string]*storedRun{}
	}
}

// CreateSchedule implements reporting.ScheduleStore.
func (s *Store) CreateSchedule(ctx context.Context, v r.Schedule, limit int) (r.Schedule, error) {
	if e := ctx.Err(); e != nil {
		return r.Schedule{}, e
	}
	if e := r.ValidateSchedule(v); e != nil || limit < 1 || limit > r.MaxSchedulesPerIdentity {
		return r.Schedule{}, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleState()
	held := 0
	for _, o := range s.schedules {
		if o.Owner.Identity != v.Owner.Identity {
			continue
		}
		if o.Key == v.Key {
			if o.RequestHash != v.RequestHash {
				return r.Schedule{}, r.ErrConflict
			}
			if o.State == r.ScheduleDeleted {
				return r.Schedule{}, r.ErrNotFound
			}
			return cloneSchedule(o)
		}
		if o.State != r.ScheduleDeleted {
			held++
		}
	}
	if _, taken := s.schedules[v.ID]; taken {
		return r.Schedule{}, r.ErrConflict
	}
	if held >= limit {
		return r.Schedule{}, &r.QuotaError{Quota: r.QuotaSchedules}
	}
	now := s.now()
	next, e := v.Recurrence.Next(now)
	if e != nil {
		return r.Schedule{}, e
	}
	v.State, v.NextRunAt, v.CreatedAt, v.UpdatedAt = r.ScheduleActive, next, now.UTC(), now.UTC()
	stored, e := clone(v)
	if e != nil {
		return r.Schedule{}, e
	}
	s.schedules[v.ID] = withHidden(stored, v)
	return cloneSchedule(v)
}

// withHidden copies the fields that JSON does not carry (the row's columns in PostgreSQL) from v onto a clone.
func withHidden(c, v r.Schedule) r.Schedule {
	c.State, c.NextRunAt, c.CreatedAt, c.UpdatedAt = v.State, v.NextRunAt, v.CreatedAt, v.UpdatedAt
	return c
}

func cloneSchedule(v r.Schedule) (r.Schedule, error) {
	c, e := clone(v)
	return withHidden(c, v), e
}

func cloneRun(v r.ScheduleRun) r.ScheduleRun {
	c, e := clone(v)
	if e != nil {
		// A run was cloned once already when it was made: what JSON carried then it carries now.
		panic(e)
	}
	c.State, c.SnapshotJob, c.ExportJob, c.SnapshotID, c.ArtifactID, c.ErrorCode = v.State, v.SnapshotJob, v.ExportJob, v.SnapshotID, v.ArtifactID, v.ErrorCode
	c.Delivery, c.Attempts, c.Generation, c.CreatedAt, c.FinishedAt = v.Delivery, v.Attempts, v.Generation, v.CreatedAt, v.FinishedAt
	return c
}

// Schedule implements reporting.ScheduleStore.
func (s *Store) Schedule(ctx context.Context, i r.Identity, id string) (r.Schedule, error) {
	if e := ctx.Err(); e != nil {
		return r.Schedule{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.schedules[id]
	if !ok || v.Owner.Identity != i || v.State == r.ScheduleDeleted {
		return r.Schedule{}, r.ErrNotFound
	}
	return cloneSchedule(v)
}

// Schedules implements reporting.ScheduleStore: newest first, strictly older than Before.
func (s *Store) Schedules(ctx context.Context, i r.Identity, l r.ScheduleListing) ([]r.Schedule, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !i.Valid() || !l.Valid(r.MaxScheduleListing) {
		return nil, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var mine []r.Schedule
	for _, v := range s.schedules {
		if v.Owner.Identity == i && v.State != r.ScheduleDeleted {
			mine = append(mine, v)
		}
	}
	newest := func(a, b r.Schedule) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return compareText(b.ID, a.ID)
	}
	slices.SortFunc(mine, newest)
	out := []r.Schedule{}
	if l.Before != "" {
		at := slices.IndexFunc(mine, func(v r.Schedule) bool { return v.ID == l.Before })
		if at < 0 {
			return out, nil
		}
		mine = mine[at+1:]
	}
	for _, v := range mine {
		if len(out) == l.Limit {
			break
		}
		c, e := cloneSchedule(v)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, nil
}

func compareText(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// SetScheduleState implements reporting.ScheduleStore.
func (s *Store) SetScheduleState(ctx context.Context, i r.Identity, id, state string) (r.Schedule, error) {
	if e := ctx.Err(); e != nil {
		return r.Schedule{}, e
	}
	if state != r.ScheduleActive && state != r.SchedulePaused && state != r.ScheduleDeleted {
		return r.Schedule{}, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.schedules[id]
	if !ok || v.Owner.Identity != i || v.State == r.ScheduleDeleted {
		return r.Schedule{}, r.ErrNotFound
	}
	if v.State == state {
		return cloneSchedule(v)
	}
	now := s.now()
	v.State, v.NextRunAt, v.UpdatedAt = state, time.Time{}, now.UTC()
	if state == r.ScheduleActive {
		next, e := v.Recurrence.Next(now)
		if e != nil {
			return r.Schedule{}, e
		}
		v.NextRunAt = next
	}
	s.schedules[id] = v
	return cloneSchedule(v)
}

// ScheduleRuns implements reporting.ScheduleStore: newest occurrence first, strictly older than Before.
func (s *Store) ScheduleRuns(ctx context.Context, i r.Identity, scheduleID string, l r.ScheduleListing) ([]r.ScheduleRun, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !i.Valid() || !l.Valid(r.MaxRunListing) {
		return nil, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var mine []r.ScheduleRun
	for _, v := range s.runs {
		if v.run.ScheduleID == scheduleID && v.run.Owner.Identity == i {
			mine = append(mine, v.run)
		}
	}
	slices.SortFunc(mine, func(a, b r.ScheduleRun) int {
		if c := b.DueAt.Compare(a.DueAt); c != 0 {
			return c
		}
		return compareText(b.ID, a.ID)
	})
	out := []r.ScheduleRun{}
	if l.Before != "" {
		at := slices.IndexFunc(mine, func(v r.ScheduleRun) bool { return v.ID == l.Before })
		if at < 0 {
			return out, nil
		}
		mine = mine[at+1:]
	}
	for _, v := range mine {
		if len(out) == l.Limit {
			break
		}
		out = append(out, cloneRun(v))
	}
	return out, nil
}

// StartDueRuns implements reporting.ScheduleStore.
func (s *Store) StartDueRuns(ctx context.Context, limit int) (int, error) {
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	if limit < 1 || limit > 1000 {
		return 0, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleState()
	now := s.now()
	open := map[string]bool{}
	for _, v := range s.runs {
		if r.RunOpen(v.run.State) {
			open[v.run.ScheduleID] = true
		}
	}
	var due []r.Schedule
	for _, v := range s.schedules {
		if v.State == r.ScheduleActive && !v.NextRunAt.After(now) && !open[v.ID] {
			due = append(due, v)
		}
	}
	slices.SortFunc(due, func(a, b r.Schedule) int {
		if c := a.NextRunAt.Compare(b.NextRunAt); c != 0 {
			return c
		}
		return compareText(a.ID, b.ID)
	})
	started := 0
	for n, v := range due {
		if n == limit {
			break
		}
		run, e := r.StartRun(v, v.NextRunAt)
		if e != nil {
			return started, e
		}
		next, e := v.Recurrence.Next(now)
		if e != nil {
			return started, e
		}
		// An occurrence that already has its run gets no second one: the schedule only moves on.
		exists := false
		for _, o := range s.runs {
			exists = exists || o.run.ScheduleID == v.ID && o.run.DueAt.Equal(run.DueAt)
		}
		if !exists {
			run.CreatedAt = now.UTC()
			s.runs[run.ID] = &storedRun{run: run, checkAt: now}
			started++
		}
		v.NextRunAt, v.UpdatedAt = next, now.UTC()
		s.schedules[v.ID] = v
	}
	return started, nil
}

// ClaimRuns implements reporting.ScheduleStore: the oldest occurrences first.
func (s *Store) ClaimRuns(ctx context.Context, lease time.Duration, limit int) ([]r.RunClaim, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if lease < time.Second || lease > 10*time.Minute || limit < 1 || limit > 1000 {
		return nil, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var ready []*storedRun
	for _, v := range s.runs {
		claimable := r.RunOpen(v.run.State) || v.run.Delivery == r.DeliveryPending
		if claimable && !v.checkAt.After(now) && !v.lease.After(now) {
			ready = append(ready, v)
		}
	}
	slices.SortFunc(ready, func(a, b *storedRun) int {
		if c := a.run.DueAt.Compare(b.run.DueAt); c != 0 {
			return c
		}
		return compareText(a.run.ID, b.run.ID)
	})
	out := []r.RunClaim{}
	for _, v := range ready {
		if len(out) == limit {
			break
		}
		v.run.Generation++
		v.lease = now.Add(lease)
		out = append(out, r.RunClaim{Run: cloneRun(v.run), ScheduleState: s.schedules[v.run.ScheduleID].State})
	}
	return out, nil
}

// UpdateRun implements reporting.ScheduleStore, fenced by the claim's generation and lease.
func (s *Store) UpdateRun(ctx context.Context, c r.RunClaim, run r.ScheduleRun, recheck time.Duration) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := r.ValidRunUpdate(c, run); e != nil || recheck < 0 || recheck > 24*time.Hour {
		return r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	v, ok := s.runs[run.ID]
	if !ok || v.run.Generation != c.Run.Generation || !v.lease.After(now) {
		return r.ErrLease
	}
	kept := v.run
	kept.State, kept.SnapshotJob, kept.ExportJob, kept.SnapshotID, kept.ArtifactID, kept.ErrorCode = run.State, run.SnapshotJob, run.ExportJob, run.SnapshotID, run.ArtifactID, run.ErrorCode
	kept.Delivery, kept.Attempts = run.Delivery, run.Attempts
	if !r.RunOpen(run.State) && kept.FinishedAt.IsZero() {
		kept.FinishedAt = run.FinishedAt.UTC()
		if kept.FinishedAt.IsZero() {
			kept.FinishedAt = now.UTC()
		}
	}
	v.run, v.lease, v.checkAt = kept, time.Time{}, now.Add(recheck)
	return nil
}

var _ r.ScheduleStore = (*Store)(nil)
