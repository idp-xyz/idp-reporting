// Package postgres stores only Reporting assets in a guarded S01 test database.
// It does not access source product tables. Production roles/migrations are S04.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
	"time"
)

//go:embed fixture.sql
var fixtureSQL string

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string, initialize bool) (*Store, error) {
	p, e := fixturedb.Open(ctx, dsn, "reporting_test_s01", "reporting_test_s01_race")
	if e != nil {
		return nil, e
	}
	s := &Store{pool: p}
	fail := func(err error) (*Store, error) { p.Close(); return nil, err }
	if initialize {
		if _, e = p.Exec(ctx, fixtureSQL); e != nil {
			return fail(e)
		}
		if e = upgradeFixture(ctx, p); e != nil {
			return fail(e)
		}
	}
	var profile string
	var version int
	if e = p.QueryRow(ctx, "SELECT profile,version FROM reporting_s01.profile WHERE singleton").Scan(&profile, &version); e != nil {
		return fail(e)
	}
	if profile != "contract-simulator-only" || version != fixtureVersion {
		return fail(r.ErrDenied)
	}
	return s, nil
}
func (s *Store) Close() { s.pool.Close() }

// ResetFixture is destructive only inside the database/profile protected by Open.
func (s *Store) ResetFixture(ctx context.Context) error {
	_, e := s.pool.Exec(ctx, "TRUNCATE reporting_s01.assets, reporting_s01.jobs, reporting_s01.issue_numbers")
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

func (s *Store) Admit(ctx context.Context, j r.Job) (r.Job, error) {
	if e := r.ValidateAdmission(j); e != nil {
		return r.Job{}, e
	}
	if j.Kind != "issue" {
		return admit(ctx, s.pool, j)
	}
	// An issue job takes the next number of its tenant and integration in the transaction
	// that admits it: the counter row serializes concurrent admissions, and a replay of an
	// earlier admission (the insert returns that job) rolls the increment back.
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return r.Job{}, e
	}
	defer func() {
		cleanup, cancel := fixturedb.RollbackContext(ctx)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if e = tx.QueryRow(ctx, `INSERT INTO reporting_s01.issue_numbers(tenant,integration,last) VALUES($1,$2,1)
ON CONFLICT(tenant,integration) DO UPDATE SET last=reporting_s01.issue_numbers.last+1 RETURNING last`, j.Owner.Tenant, j.Owner.Integration).Scan(&j.Number); e != nil {
		return r.Job{}, e
	}
	v, e := admit(ctx, tx, j)
	if e != nil {
		return r.Job{}, e
	}
	if v.ID != j.ID {
		return v, nil // the earlier admission; the rollback gives the number back
	}
	if e = tx.Commit(ctx); e != nil {
		return r.Job{}, e
	}
	return v, nil
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
func (s *Store) Cancel(ctx context.Context, i r.Identity, id string) (r.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `UPDATE reporting_s01.jobs SET
state=CASE WHEN state IN ('queued','running') THEN 'cancelled' ELSE state END,
lease_until=CASE WHEN state IN ('queued','running') THEN NULL ELSE lease_until END
WHERE id=$1 AND tenant=$2 AND issuer=$3 AND subject=$4 RETURNING `+jobFields, id, i.Tenant, i.Issuer, i.Subject))
}
func (s *Store) Claim(ctx context.Context, lease time.Duration) (*r.Claim, error) {
	if lease < time.Second || lease > 5*time.Minute {
		return nil, r.ErrInvalid
	}
	j, e := scanJob(s.pool.QueryRow(ctx, `UPDATE reporting_s01.jobs SET state='running',generation=generation+1,lease_until=clock_timestamp()+$1*interval '1 microsecond'
WHERE id=(SELECT id FROM reporting_s01.jobs WHERE state='queued' OR (state='running' AND lease_until<=clock_timestamp()) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+jobFields, lease.Microseconds()))
	if errors.Is(e, r.ErrNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &r.Claim{Job: j}, nil
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
		return r.ErrLease
	}
	return tx.Commit(ctx)
}
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
func (s *Store) Issues(ctx context.Context, i r.Identity, limit int) ([]r.Job, error) {
	if limit < 1 || limit > r.MaxIssueListing || !i.Valid() {
		return nil, r.ErrInvalid
	}
	rows, e := s.pool.Query(ctx, "SELECT "+jobFields+" FROM reporting_s01.jobs WHERE kind='issue' AND tenant=$1 AND issuer=$2 AND subject=$3 ORDER BY created_at DESC, id DESC LIMIT $4", i.Tenant, i.Issuer, i.Subject, limit)
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
