// Package postgres stores only the platform's own assets: a guarded test fixture (Open) or a
// production store (OpenProduction, S04-A), migrated through the embedded migrations. It never
// accesses source product tables.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
	"time"
)

type Store struct {
	pool    *pgxpool.Pool
	fixture bool // opened through the guarded fixture path: resets are allowed
}

// Open opens the guarded test fixture: one of the fixture database names on a loopback address,
// the contract-simulator profile, the schema at the binary's version. With initialize, the
// migrations are applied first and the profile row inserted; a database at an older version
// (made before the ledger existed included) is upgraded. Production stores open through
// OpenProduction.
func Open(ctx context.Context, dsn string, initialize bool) (*Store, error) {
	p, e := fixturedb.Open(ctx, dsn, fixtureDatabases...)
	if e != nil {
		return nil, e
	}
	s := &Store{pool: p, fixture: true}
	fail := func(err error) (*Store, error) { p.Close(); return nil, err }
	if initialize {
		if e = Up(ctx, p, 0, "fixture"); e != nil {
			return fail(e)
		}
		if e = InitProfile(ctx, p, ProfileFixture); e != nil {
			return fail(e)
		}
	}
	if e = Verify(ctx, p, ProfileFixture); e != nil {
		return fail(e)
	}
	return s, nil
}
func (s *Store) Close() { s.pool.Close() }

// ResetFixture is destructive only inside the database/profile protected by Open.
func (s *Store) ResetFixture(ctx context.Context) error {
	if !s.fixture {
		return r.ErrDenied
	}
	_, e := s.pool.Exec(ctx, "TRUNCATE reporting_s01.assets, reporting_s01.jobs")
	return e
}

type scanner interface{ Scan(...any) error }

const jobFields = "id,tenant,integration,issuer,subject,kind,idem_key,request_hash,data::text,data_sha256,state,generation,lease_until,snapshot_id,artifact_id,error_code"

func scanJob(row scanner) (r.Job, error) {
	var j r.Job
	var b []byte
	var state, snapshot, artifact, code, digest, id, kind, key, requestHash string
	var owner r.Owner
	var gen int64
	var lease *time.Time
	if e := row.Scan(&id, &owner.Tenant, &owner.Integration, &owner.Issuer, &owner.Subject, &kind, &key, &requestHash, &b, &digest, &state, &gen, &lease, &snapshot, &artifact, &code); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return j, r.ErrNotFound
		}
		return j, e
	}
	if e := fixturedb.Decode(b, digest, &j); e != nil {
		return j, r.ErrIntegrity
	}
	if j.ID != id || j.Owner != owner || j.Kind != kind || j.Key != key || j.RequestHash != requestHash || r.ValidateAdmission(j) != nil {
		return r.Job{}, r.ErrIntegrity
	}
	j.State = state
	j.Generation = gen
	j.SnapshotID = snapshot
	j.ArtifactID = artifact
	j.ErrorCode = code
	j.LeaseUntil = time.Time{}
	if lease != nil {
		j.LeaseUntil = *lease
	}
	return j, nil
}

const admitSQL = `INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,data,snapshot_id,data_sha256)
VALUES($1,$2,$3,$4,$5,$6,'queued',$7,$8,$9,$10,$11,encode(sha256(convert_to($10::jsonb::text,'UTF8')),'hex'))
ON CONFLICT(tenant,integration,issuer,subject,kind,idem_key) DO UPDATE SET request_hash=reporting_s01.jobs.request_hash
RETURNING ` + jobFields

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Admit stores the job as it is: every kind goes through the same insert. An issue job has no
// number (S03-H): the source product gives it when the worker prepares the document.
func (s *Store) Admit(ctx context.Context, j r.Job) (r.Job, error) {
	if e := r.ValidateAdmission(j); e != nil {
		return r.Job{}, e
	}
	return admit(ctx, s.pool, j)
}
func admit(ctx context.Context, q querier, j r.Job) (r.Job, error) {
	b, e := json.Marshal(j)
	if e != nil {
		return r.Job{}, e
	}
	v, e := scanJob(q.QueryRow(ctx, admitSQL, j.ID, j.Owner.Tenant, j.Owner.Integration, j.Owner.Issuer, j.Owner.Subject, j.Kind, j.Key, j.RequestHash, j.Prepared.ExpiresAt, b, j.SnapshotID))
	var pgerr *pgconn.PgError
	if errors.As(e, &pgerr) && pgerr.Code == "23505" {
		return r.Job{}, r.ErrConflict
	}
	if e != nil {
		return r.Job{}, e
	}
	if v.RequestHash != j.RequestHash {
		return r.Job{}, r.ErrConflict
	}
	return v, nil
}
func (s *Store) Job(ctx context.Context, i r.Identity, id string) (r.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, "SELECT "+jobFields+" FROM reporting_s01.jobs WHERE id=$1 AND tenant=$2 AND issuer=$3 AND subject=$4", id, i.Tenant, i.Issuer, i.Subject))
}

// Cancel cancels a queued job, and a running one of any kind but issue: the source product may
// hold a reservation for a running issue job (S03-H), which therefore finishes, or fails and voids,
// and is returned unchanged.
func (s *Store) Cancel(ctx context.Context, i r.Identity, id string) (r.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `UPDATE reporting_s01.jobs SET
state=CASE WHEN state='queued' OR (state='running' AND kind<>'issue') THEN 'cancelled' ELSE state END,
lease_until=CASE WHEN state='queued' OR (state='running' AND kind<>'issue') THEN NULL ELSE lease_until END
WHERE id=$1 AND tenant=$2 AND issuer=$3 AND subject=$4 RETURNING `+jobFields, id, i.Tenant, i.Issuer, i.Subject))
}

// Claim claims the oldest job that is queued, or running with a lease that has run out, by the database's clock. A job whose stored
// payload cannot be decoded (corrupt, or written by a binary this one cannot read) is failed in the transaction of its claim, terminally
// (r.UnreadableJobCode: INTEGRITY_FAILURE, or ISSUE_UNRECONCILED for an issue job that had been claimed before), and Claim goes on to
// the next job, so that nil still means that nothing is claimable. Its kind and its generation come from their own columns, never from
// the payload; nothing is logged.
func (s *Store) Claim(ctx context.Context, lease time.Duration) (*r.Claim, error) {
	if lease < time.Second || lease > 5*time.Minute {
		return nil, r.ErrInvalid
	}
	for {
		claim, failed, e := s.claimNext(ctx, lease)
		if e != nil || !failed {
			return claim, e
		}
	}
}

// claimNext claims one job, or fails one that cannot be decoded (the bool is true then and the caller claims again); an error of any
// other kind rolls the transaction back, so that no job is left claimed by a claim that was never answered.
func (s *Store) claimNext(ctx context.Context, lease time.Duration) (*r.Claim, bool, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, false, e
	}
	defer func() {
		cleanup, cancel := fixturedb.RollbackContext(ctx)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var id, kind string
	var prior int64
	e = tx.QueryRow(ctx, `SELECT id,kind,generation FROM reporting_s01.jobs WHERE state='queued' OR (state='running' AND lease_until<=clock_timestamp()) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &kind, &prior)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	j, e := scanJob(tx.QueryRow(ctx, `UPDATE reporting_s01.jobs SET state='running',generation=generation+1,lease_until=clock_timestamp()+$1*interval '1 microsecond' WHERE id=$2 RETURNING `+jobFields, lease.Microseconds(), id))
	if e == nil {
		if e = tx.Commit(ctx); e != nil {
			return nil, false, e
		}
		return &r.Claim{Job: j}, false, nil
	}
	if !errors.Is(e, r.ErrIntegrity) {
		return nil, false, e
	}
	if _, e = tx.Exec(ctx, `UPDATE reporting_s01.jobs SET state='failed',lease_until=NULL,error_code=$1 WHERE id=$2`, r.UnreadableJobCode(kind, prior), id); e != nil {
		return nil, false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, false, e
	}
	return nil, true, nil
}
func (s *Store) Publish(ctx context.Context, c r.Claim, p r.Publication) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	j, e := scanJob(tx.QueryRow(ctx, "SELECT "+jobFields+" FROM reporting_s01.jobs WHERE id=$1 FOR UPDATE", c.Job.ID))
	if e != nil {
		return e
	}
	var now time.Time
	if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); e != nil {
		return e
	}
	if !r.ClaimValid(j, c, now) {
		return r.ErrLease
	}
	if e = r.ValidatePublication(j, p); e != nil {
		return e
	}
	type asset struct {
		id, kind string
		value    any
	}
	var assets []asset
	snapshotID, artifactID := j.SnapshotID, j.ArtifactID
	if p.Snapshot != nil {
		snapshotID = p.Snapshot.ID
		assets = append(assets, asset{p.Snapshot.ID, "snapshot", p.Snapshot})
	} else {
		artifactID = p.Artifact.ID
		assets = append(assets, asset{p.Artifact.ID, "artifact", p.Artifact})
		if p.Issue != nil {
			assets = append(assets, asset{p.Issue.ID, "issue", p.Issue})
		}
	}
	for _, a := range assets {
		b, e := json.Marshal(a.value)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256) VALUES($1,$2,$3,$4,$5,$6,$7,encode(sha256(convert_to($7::jsonb::text,'UTF8')),'hex'))", a.id, j.Owner.Tenant, j.Owner.Integration, j.Owner.Issuer, j.Owner.Subject, a.kind, b); e != nil {
			return e
		}
	}
	result, e := tx.Exec(ctx, `UPDATE reporting_s01.jobs SET state='succeeded',lease_until=NULL,snapshot_id=$1,artifact_id=$2
WHERE id=$3 AND state='running' AND generation=$4 AND lease_until>clock_timestamp() AND source_expires_at>clock_timestamp()`, snapshotID, artifactID, j.ID, c.Job.Generation)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		// Two things refuse the update, and they are not the same. A claim that still holds the
		// job (running, its generation, its lease not run out) whose source read has expired by the
		// store's clock has not lost anything: it is denied, as in the memory store, so that the
		// worker voids what the product confirmed. Whatever else refuses it, the lease is lost.
		var expired bool
		e = tx.QueryRow(ctx, `SELECT source_expires_at<=clock_timestamp() FROM reporting_s01.jobs
WHERE id=$1 AND state='running' AND generation=$2 AND lease_until>clock_timestamp()`, j.ID, c.Job.Generation).Scan(&expired)
		if e == nil && expired {
			return r.ErrDenied
		}
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		return r.ErrLease
	}
	return tx.Commit(ctx)
}

// Fail fails a job that is running, at the claim's generation, with a lease that has not run out by
// the store's clock (clock_timestamp()); anything else is ErrLease and changes nothing.
func (s *Store) Fail(ctx context.Context, c r.Claim, code string) error {
	result, e := s.pool.Exec(ctx, `UPDATE reporting_s01.jobs SET state='failed',lease_until=NULL,error_code=$1 WHERE id=$2 AND tenant=$3 AND integration=$4 AND issuer=$5 AND subject=$6 AND state='running' AND generation=$7 AND lease_until>clock_timestamp()`, code, c.Job.ID, c.Job.Owner.Tenant, c.Job.Owner.Integration, c.Job.Owner.Issuer, c.Job.Owner.Subject, c.Job.Generation)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return r.ErrLease
	}
	return nil
}

// ResolveFailure replaces the code ISSUE_UNRECONCILED of a failed issue job with the job's own failure
// code once the source product has voided what it held (S03-H). It applies only to an issue job that is
// failed, at the claim's generation, with that code: anything else is ErrLease and changes nothing. It
// needs no lease, which a failed job has not.
func (s *Store) ResolveFailure(ctx context.Context, c r.Claim, code string) error {
	if code == "" || code == r.ErrUnreconciled.Error() {
		return r.ErrInvalid
	}
	result, e := s.pool.Exec(ctx, `UPDATE reporting_s01.jobs SET error_code=$1 WHERE id=$2 AND tenant=$3 AND integration=$4 AND issuer=$5 AND subject=$6 AND kind='issue' AND state='failed' AND generation=$7 AND error_code=$8`, code, c.Job.ID, c.Job.Owner.Tenant, c.Job.Owner.Integration, c.Job.Owner.Issuer, c.Job.Owner.Subject, c.Job.Generation, r.ErrUnreconciled.Error())
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return r.ErrLease
	}
	return nil
}
func (s *Store) asset(ctx context.Context, i r.Identity, id, kind string, out any) error {
	var b []byte
	var digest, resourceID, resourceKind string
	var owner r.Owner
	e := s.pool.QueryRow(ctx, "SELECT data::text,data_sha256,id,kind,tenant,integration,issuer,subject FROM reporting_s01.assets WHERE id=$1 AND tenant=$2 AND issuer=$3 AND subject=$4 AND kind=$5", id, i.Tenant, i.Issuer, i.Subject, kind).Scan(&b, &digest, &resourceID, &resourceKind, &owner.Tenant, &owner.Integration, &owner.Issuer, &owner.Subject)
	if errors.Is(e, pgx.ErrNoRows) {
		return r.ErrNotFound
	}
	if e != nil {
		return e
	}
	if e = fixturedb.Decode(b, digest, out); e != nil {
		return e
	}
	switch v := out.(type) {
	case *r.Snapshot:
		if v.ID != resourceID || v.Owner != owner || resourceKind != "snapshot" || v.Prepared.Context.Owner != v.Owner || v.Prepared.QueryHash != r.QueryHash(v.Query) {
			return r.ErrIntegrity
		}
	case *r.Artifact:
		if v.ID != resourceID || v.Owner != owner || resourceKind != "artifact" {
			return r.ErrIntegrity
		}
	case *r.Issue:
		if v.ID != resourceID || v.Owner != owner || resourceKind != "issue" || v.Stamp != r.IssueStamp(v.Number, v.IssuedAt, v.ID) {
			return r.ErrIntegrity
		}
	default:
		return r.ErrIntegrity
	}
	return nil
}
func (s *Store) Snapshot(ctx context.Context, i r.Identity, id string) (r.Snapshot, error) {
	var v r.Snapshot
	e := s.asset(ctx, i, id, "snapshot", &v)
	return v, e
}
func (s *Store) Artifact(ctx context.Context, i r.Identity, id string) (r.Artifact, error) {
	var v r.Artifact
	e := s.asset(ctx, i, id, "artifact", &v)
	return v, e
}
func (s *Store) Issue(ctx context.Context, i r.Identity, id string) (r.Issue, error) {
	var v r.Issue
	e := s.asset(ctx, i, id, "issue", &v)
	return v, e
}
func (s *Store) Issues(ctx context.Context, i r.Identity, integrations []string, limit int) ([]r.Job, error) {
	if limit < 1 || limit > r.MaxIssueListing || !i.Valid() || len(integrations) == 0 {
		return nil, r.ErrInvalid
	}
	rows, e := s.pool.Query(ctx, "SELECT "+jobFields+" FROM reporting_s01.jobs WHERE kind='issue' AND tenant=$1 AND issuer=$2 AND subject=$3 AND integration=ANY($4) ORDER BY created_at DESC, id DESC LIMIT $5", i.Tenant, i.Issuer, i.Subject, integrations, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []r.Job{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return out, nil
}

var _ r.Repository = (*Store)(nil)
