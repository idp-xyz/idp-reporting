package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
)

// The schedules of S16-A (migration 013, docs/S16A_SCOPE.md). A person's calls run in the identity's context and carry its predicate, as the
// jobs' do; the scheduler's (StartDueRuns, ClaimRuns, UpdateRun) run in the worker scope. Every time is the database's clock.

const (
	scheduleFields = "id,tenant,integration,issuer,subject,idem_key,request_hash,state,data::text,data_sha256,next_run_at,created_at,updated_at"
	runFields      = "id,schedule_id,tenant,integration,issuer,subject,due_at,state,data::text,data_sha256,snapshot_job,export_job,snapshot_id,artifact_id,error_code,delivery,attempts,generation,created_at,finished_at"
	// scheduleIdentity is the identity predicate of the person's statements ($2..$4).
	scheduleIdentity = " AND tenant=$2 AND issuer=$3 AND subject=$4"
	digestOf         = "encode(sha256(convert_to(%s::jsonb::text,'UTF8')),'hex')"
)

var (
	scheduleInsertSQL = `INSERT INTO reporting_s01.schedules(id,tenant,integration,issuer,subject,idem_key,request_hash,state,data,data_sha256,next_run_at)
VALUES($1,$2,$3,$4,$5,$6,$7,'active',$8,` + fmt.Sprintf(digestOf, "$8") + `,$9) RETURNING ` + scheduleFields
	scheduleReplaySQL = "SELECT " + scheduleFields + " FROM reporting_s01.schedules WHERE idem_key=$1" + scheduleIdentity
	scheduleCountSQL  = "SELECT count(*) FROM reporting_s01.schedules WHERE state<>'deleted' AND tenant=$1 AND issuer=$2 AND subject=$3"
	scheduleByIDSQL   = "SELECT " + scheduleFields + " FROM reporting_s01.schedules WHERE id=$1" + scheduleIdentity + " AND state<>'deleted'"
	// scheduleListingSQL is one page of an identity's schedules, newest first, strictly older than its schedule $5 when that is set;
	// schedules_identity_created serves it.
	scheduleListingSQL = "SELECT " + scheduleFields + " FROM reporting_s01.schedules WHERE state<>'deleted' AND tenant=$1 AND issuer=$2 AND subject=$3" +
		" AND ($4='' OR (created_at,id) < (SELECT b.created_at,b.id FROM reporting_s01.schedules b WHERE b.id=$4 AND b.tenant=$1 AND b.issuer=$2 AND b.subject=$3))" +
		" ORDER BY created_at DESC, id DESC LIMIT $5"
	scheduleStateSQL = "UPDATE reporting_s01.schedules SET state=$5,next_run_at=$6,updated_at=clock_timestamp() WHERE id=$1" + scheduleIdentity + " RETURNING " + scheduleFields
	// runListingSQL is one page of a schedule's runs, newest occurrence first; the unique index on (schedule_id, due_at) serves it.
	runListingSQL = "SELECT " + runFields + " FROM reporting_s01.schedule_runs WHERE schedule_id=$1" + scheduleIdentity +
		" AND ($5='' OR (due_at,id) < (SELECT b.due_at,b.id FROM reporting_s01.schedule_runs b WHERE b.id=$5 AND b.schedule_id=$1 AND b.tenant=$2 AND b.issuer=$3 AND b.subject=$4))" +
		" ORDER BY due_at DESC, id DESC LIMIT $6"
	// dueScheduleSQL takes the oldest due schedule that has no open run and that no other scheduler holds; schedules_due serves it.
	dueScheduleSQL = "SELECT " + scheduleFields + ", clock_timestamp() FROM reporting_s01.schedules s WHERE state='active' AND next_run_at<=clock_timestamp()" +
		" AND NOT EXISTS (SELECT 1 FROM reporting_s01.schedule_runs o WHERE o.schedule_id=s.id AND o.state IN ('due','snapshot','export'))" +
		" ORDER BY next_run_at, id FOR UPDATE SKIP LOCKED LIMIT 1"
	runInsertSQL = `INSERT INTO reporting_s01.schedule_runs(id,schedule_id,tenant,integration,issuer,subject,due_at,state,data,data_sha256,check_at)
VALUES($1,$2,$3,$4,$5,$6,$7,'due',$8,` + fmt.Sprintf(digestOf, "$8") + `,clock_timestamp()) ON CONFLICT (schedule_id, due_at) DO NOTHING`
	scheduleAdvanceSQL = "UPDATE reporting_s01.schedules SET next_run_at=$2,updated_at=clock_timestamp() WHERE id=$1 AND state='active'"
	// claimRunsSQL claims the oldest open (or delivery-pending) runs whose time has come and whose lease has run out; schedule_runs_open serves it.
	claimRunsSQL = `UPDATE reporting_s01.schedule_runs SET generation=generation+1,lease_until=clock_timestamp()+$1*interval '1 microsecond'
WHERE id IN (SELECT id FROM reporting_s01.schedule_runs WHERE (state IN ('due','snapshot','export') OR delivery='pending') AND check_at<=clock_timestamp()
AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY due_at,id FOR UPDATE SKIP LOCKED LIMIT $2)
RETURNING ` + runFields + `, (SELECT s.state FROM reporting_s01.schedules s WHERE s.id=schedule_id)`
	// unreadableRunSQL ends a run whose stored definition cannot be read: it is not claimed again, and it is not delivered.
	unreadableRunSQL = "UPDATE reporting_s01.schedule_runs SET state='failed',error_code='INTEGRITY_FAILURE',delivery='none',lease_until=NULL,finished_at=coalesce(finished_at,clock_timestamp()) WHERE id=$1"
	updateRunSQL     = `UPDATE reporting_s01.schedule_runs SET state=$3,snapshot_job=$4,export_job=$5,snapshot_id=$6,artifact_id=$7,error_code=$8,delivery=$9,attempts=$10,
finished_at=CASE WHEN $3 IN ('due','snapshot','export') THEN NULL ELSE coalesce(finished_at,$11,clock_timestamp()) END,
lease_until=NULL,check_at=clock_timestamp()+$12*interval '1 microsecond'
WHERE id=$1 AND generation=$2 AND lease_until>clock_timestamp()`
)

// scheduleLockKey is the advisory lock of an identity's schedule creations, so that two creations count and insert one after the other.
func scheduleLockKey(i r.Identity) string {
	return fmt.Sprintf("reporting.schedules:%d:%s:%d:%s:%d:%s", len(i.Tenant), i.Tenant, len(i.Issuer), i.Issuer, len(i.Subject), i.Subject)
}

func scanSchedule(row scanner, extra ...any) (r.Schedule, error) {
	var v r.Schedule
	var id, key, hash, state, digest string
	var owner r.Owner
	var b []byte
	var next *time.Time
	var created, updated time.Time
	dest := append([]any{&id, &owner.Tenant, &owner.Integration, &owner.Issuer, &owner.Subject, &key, &hash, &state, &b, &digest, &next, &created, &updated}, extra...)
	if e := row.Scan(dest...); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return v, r.ErrNotFound
		}
		return v, e
	}
	if e := fixturedb.Decode(b, digest, &v); e != nil {
		return r.Schedule{}, r.ErrIntegrity
	}
	if v.ID != id || v.Owner != owner || v.Key != key || v.RequestHash != hash || r.ValidateSchedule(v) != nil {
		return r.Schedule{}, r.ErrIntegrity
	}
	v.State, v.CreatedAt, v.UpdatedAt = state, created.UTC(), updated.UTC()
	if next != nil {
		v.NextRunAt = next.UTC()
	}
	return v, nil
}

func scanRun(row scanner, extra ...any) (r.ScheduleRun, error) {
	var v r.ScheduleRun
	var id, scheduleID, state, digest, snapshotJob, exportJob, snapshotID, artifactID, code, delivery string
	var owner r.Owner
	var due, created time.Time
	var finished *time.Time
	var b []byte
	var attempts int
	var gen int64
	dest := append([]any{&id, &scheduleID, &owner.Tenant, &owner.Integration, &owner.Issuer, &owner.Subject, &due, &state, &b, &digest, &snapshotJob, &exportJob, &snapshotID, &artifactID, &code, &delivery, &attempts, &gen, &created, &finished}, extra...)
	if e := row.Scan(dest...); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return v, r.ErrNotFound
		}
		return v, e
	}
	// The row's own columns are filled before the payload is judged, so that a run that cannot be read can still be named and ended.
	v.ID = id
	var payload r.ScheduleRun
	if e := fixturedb.Decode(b, digest, &payload); e != nil || payload.ID != id || payload.ScheduleID != scheduleID || payload.Owner != owner || !payload.DueAt.Equal(due) {
		return v, r.ErrIntegrity
	}
	v = payload
	v.DueAt = due.UTC()
	v.State, v.SnapshotJob, v.ExportJob, v.SnapshotID, v.ArtifactID, v.ErrorCode, v.Delivery, v.Attempts, v.Generation, v.CreatedAt = state, snapshotJob, exportJob, snapshotID, artifactID, code, delivery, attempts, gen, created.UTC()
	if finished != nil {
		v.FinishedAt = finished.UTC()
	}
	return v, nil
}

// CreateSchedule implements reporting.ScheduleStore: in the identity's context it takes the identity's lock, settles a replay of the key,
// counts the schedules that are not deleted, and inserts, the first occurrence after the database's clock as next_run_at.
func (s *Store) CreateSchedule(ctx context.Context, v r.Schedule, limit int) (r.Schedule, error) {
	if e := r.ValidateSchedule(v); e != nil || limit < 1 || limit > r.MaxSchedulesPerIdentity {
		return r.Schedule{}, r.ErrInvalid
	}
	b, e := json.Marshal(v)
	if e != nil {
		return r.Schedule{}, e
	}
	var out r.Schedule
	i := v.Owner.Identity
	e = asIdentity(ctx, s.pool, i, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, admitLockSQL, scheduleLockKey(i)); err != nil {
			return err
		}
		prior, err := scanSchedule(tx.QueryRow(ctx, scheduleReplaySQL, v.Key, i.Tenant, i.Issuer, i.Subject))
		switch {
		case err == nil && prior.RequestHash != v.RequestHash:
			return r.ErrConflict
		case err == nil && prior.State == r.ScheduleDeleted:
			return r.ErrNotFound
		case err == nil:
			out = prior
			return nil
		case !errors.Is(err, r.ErrNotFound):
			return err
		}
		var held int
		var now time.Time
		if err = tx.QueryRow(ctx, "SELECT ("+scheduleCountSQL+"), clock_timestamp()", i.Tenant, i.Issuer, i.Subject).Scan(&held, &now); err != nil {
			return err
		}
		if held >= limit {
			return &r.QuotaError{Quota: r.QuotaSchedules}
		}
		next, err := v.Recurrence.Next(now)
		if err != nil {
			return err
		}
		out, err = scanSchedule(tx.QueryRow(ctx, scheduleInsertSQL, v.ID, v.Owner.Tenant, v.Owner.Integration, i.Issuer, i.Subject, v.Key, v.RequestHash, b, next))
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return r.ErrConflict
		}
		return err
	})
	if e != nil {
		return r.Schedule{}, e
	}
	return out, nil
}

// Schedule implements reporting.ScheduleStore.
func (s *Store) Schedule(ctx context.Context, i r.Identity, id string) (r.Schedule, error) {
	var v r.Schedule
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) (err error) {
		v, err = scanSchedule(tx.QueryRow(ctx, scheduleByIDSQL, id, i.Tenant, i.Issuer, i.Subject))
		return err
	})
	return v, e
}

// Schedules implements reporting.ScheduleStore.
func (s *Store) Schedules(ctx context.Context, i r.Identity, l r.ScheduleListing) ([]r.Schedule, error) {
	if !i.Valid() || !l.Valid(r.MaxScheduleListing) {
		return nil, r.ErrInvalid
	}
	out := []r.Schedule{}
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, scheduleListingSQL, i.Tenant, i.Issuer, i.Subject, l.Before, l.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanSchedule(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

// SetScheduleState implements reporting.ScheduleStore: the row is locked, then changed, a resumption's next occurrence by the database's clock.
func (s *Store) SetScheduleState(ctx context.Context, i r.Identity, id, state string) (r.Schedule, error) {
	if state != r.ScheduleActive && state != r.SchedulePaused && state != r.ScheduleDeleted {
		return r.Schedule{}, r.ErrInvalid
	}
	var out r.Schedule
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) error {
		var now time.Time
		v, err := scanSchedule(tx.QueryRow(ctx, scheduleByIDSQL+" FOR UPDATE", id, i.Tenant, i.Issuer, i.Subject))
		if err != nil {
			return err
		}
		if v.State == state {
			out = v
			return nil
		}
		if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			return err
		}
		var next *time.Time
		if state == r.ScheduleActive {
			at, err := v.Recurrence.Next(now)
			if err != nil {
				return err
			}
			next = &at
		}
		out, err = scanSchedule(tx.QueryRow(ctx, scheduleStateSQL, id, i.Tenant, i.Issuer, i.Subject, state, next))
		return err
	})
	if e != nil {
		return r.Schedule{}, e
	}
	return out, nil
}

// ScheduleRuns implements reporting.ScheduleStore.
func (s *Store) ScheduleRuns(ctx context.Context, i r.Identity, scheduleID string, l r.ScheduleListing) ([]r.ScheduleRun, error) {
	if !i.Valid() || !l.Valid(r.MaxRunListing) {
		return nil, r.ErrInvalid
	}
	out := []r.ScheduleRun{}
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, runListingSQL, scheduleID, i.Tenant, i.Issuer, i.Subject, l.Before, l.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

// StartDueRuns implements reporting.ScheduleStore: one schedule per transaction, locked with FOR UPDATE SKIP LOCKED, its run inserted and its
// next occurrence set before the lock is released. An occurrence that already has its run (the table's unique (schedule, occurrence)) gets no
// second one: the schedule only moves on.
func (s *Store) StartDueRuns(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, r.ErrInvalid
	}
	started := 0
	for n := 0; n < limit; n++ {
		taken, inserted, e := s.startOne(ctx)
		if inserted {
			started++
		}
		if e != nil || !taken {
			return started, e
		}
	}
	return started, nil
}

// startOne takes one due schedule (taken) and starts its occurrence (inserted, unless the occurrence had its run already).
func (s *Store) startOne(ctx context.Context) (taken, inserted bool, err error) {
	e := asWorker(ctx, s.pool, func(tx pgx.Tx) error {
		var now time.Time
		v, err := scanSchedule(tx.QueryRow(ctx, dueScheduleSQL), &now)
		if errors.Is(err, r.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		run, err := r.StartRun(v, v.NextRunAt)
		if err != nil {
			return err
		}
		next, err := v.Recurrence.Next(now)
		if err != nil {
			return err
		}
		b, err := json.Marshal(run)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, runInsertSQL, run.ID, run.ScheduleID, run.Owner.Tenant, run.Owner.Integration, run.Owner.Issuer, run.Owner.Subject, run.DueAt, b)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, scheduleAdvanceSQL, v.ID, next); err != nil {
			return err
		}
		taken, inserted = true, tag.RowsAffected() == 1
		return nil
	})
	if e != nil {
		return false, false, e
	}
	return taken, inserted, nil
}

// ClaimRuns implements reporting.ScheduleStore in one statement. A run whose stored definition cannot be read is ended in the same transaction
// (failed, INTEGRITY_FAILURE, not delivered) and left out, so that it never holds the scheduler.
func (s *Store) ClaimRuns(ctx context.Context, lease time.Duration, limit int) ([]r.RunClaim, error) {
	if lease < time.Second || lease > 10*time.Minute || limit < 1 || limit > 1000 {
		return nil, r.ErrInvalid
	}
	out := []r.RunClaim{}
	e := asWorker(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, claimRunsSQL, lease.Microseconds(), limit)
		if err != nil {
			return err
		}
		var unreadable []string
		for rows.Next() {
			var state *string
			v, err := scanRun(rows, &state)
			if errors.Is(err, r.ErrIntegrity) && v.ID != "" {
				unreadable = append(unreadable, v.ID)
				continue
			}
			if err != nil {
				rows.Close()
				return err
			}
			c := r.RunClaim{Run: v}
			if state != nil {
				c.ScheduleState = *state
			}
			out = append(out, c)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, id := range unreadable {
			if _, err = tx.Exec(ctx, unreadableRunSQL, id); err != nil {
				return err
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

// UpdateRun implements reporting.ScheduleStore, fenced by the claim's generation and its lease by the database's clock.
func (s *Store) UpdateRun(ctx context.Context, c r.RunClaim, run r.ScheduleRun, recheck time.Duration) error {
	if e := r.ValidRunUpdate(c, run); e != nil || recheck < 0 || recheck > 24*time.Hour {
		return r.ErrInvalid
	}
	var finished *time.Time
	if !run.FinishedAt.IsZero() {
		at := run.FinishedAt.UTC()
		finished = &at
	}
	return asWorker(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, updateRunSQL, run.ID, c.Run.Generation, run.State, run.SnapshotJob, run.ExportJob, run.SnapshotID, run.ArtifactID, run.ErrorCode, run.Delivery, run.Attempts, finished, recheck.Microseconds())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return r.ErrLease
		}
		return nil
	})
}

var _ r.ScheduleStore = (*Store)(nil)
