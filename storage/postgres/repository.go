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
	r "idp.local/reporting/reporting"
	"net"
	"time"
)

//go:embed fixture.sql
var fixtureSQL string

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string, initialize bool) (*Store, error) {
	c, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, r.ErrInvalid
	}
	if (c.ConnConfig.Database != "reporting_test_s01" && c.ConnConfig.Database != "reporting_test_s01_race") || net.ParseIP(c.ConnConfig.Host) == nil || !net.ParseIP(c.ConnConfig.Host).IsLoopback() {
		return nil, r.ErrDenied
	}
	for _, fallback := range c.ConnConfig.Fallbacks {
		if net.ParseIP(fallback.Host) == nil || !net.ParseIP(fallback.Host).IsLoopback() {
			return nil, r.ErrDenied
		}
	}
	c.MaxConns = 8
	c.MinConns = 0
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	p, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	s := &Store{pool: p}
	fail := func(err error) (*Store, error) { p.Close(); return nil, err }
	if initialize {
		if _, e = p.Exec(ctx, fixtureSQL); e != nil {
			return fail(e)
		}
	}
	var profile string
	var version int
	if e = p.QueryRow(ctx, "SELECT profile,version FROM reporting_s01.profile WHERE singleton").Scan(&profile, &version); e != nil {
		return fail(e)
	}
	if profile != "contract-simulator-only" || version != 1 {
		return fail(r.ErrDenied)
	}
	return s, nil
}
func (s *Store) Close() { s.pool.Close() }

// ResetFixture is destructive only inside the database/profile protected by Open.
func (s *Store) ResetFixture(ctx context.Context) error {
	_, e := s.pool.Exec(ctx, "TRUNCATE reporting_s01.assets, reporting_s01.jobs")
	return e
}

type scanner interface{ Scan(...any) error }

const jobFields = "data,state,generation,lease_until,snapshot_id,artifact_id,error_code"

func scanJob(row scanner) (r.Job, error) {
	var j r.Job
	var b []byte
	var state, snapshot, artifact, code string
	var gen int64
	var lease *time.Time
	if e := row.Scan(&b, &state, &gen, &lease, &snapshot, &artifact, &code); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return j, r.ErrNotFound
		}
		return j, e
	}
	if e := json.Unmarshal(b, &j); e != nil {
		return j, r.ErrIntegrity
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
func (s *Store) Admit(ctx context.Context, j r.Job) (r.Job, error) {
	if e := r.ValidateAdmission(j); e != nil {
		return r.Job{}, e
	}
	b, e := json.Marshal(j)
	if e != nil {
		return r.Job{}, e
	}
	v, e := scanJob(s.pool.QueryRow(ctx, `INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,data,snapshot_id)
VALUES($1,$2,$3,$4,$5,$6,'queued',$7,$8,$9,$10,$11)
ON CONFLICT(tenant,integration,issuer,subject,kind,idem_key) DO UPDATE SET request_hash=reporting_s01.jobs.request_hash
RETURNING `+jobFields, j.ID, j.Owner.Tenant, j.Owner.Integration, j.Owner.Issuer, j.Owner.Subject, j.Kind, j.Key, j.RequestHash, j.Prepared.ExpiresAt, b, j.SnapshotID))
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
	var id, kind string
	var b []byte
	snapshotID, artifactID := j.SnapshotID, j.ArtifactID
	if p.Snapshot != nil {
		id = p.Snapshot.ID
		kind = "snapshot"
		snapshotID = id
		b, e = json.Marshal(p.Snapshot)
	} else {
		id = p.Artifact.ID
		kind = "artifact"
		artifactID = id
		b, e = json.Marshal(p.Artifact)
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data) VALUES($1,$2,$3,$4,$5,$6,$7)", id, j.Owner.Tenant, j.Owner.Integration, j.Owner.Issuer, j.Owner.Subject, kind, b); e != nil {
		return e
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
	e := s.pool.QueryRow(ctx, "SELECT data FROM reporting_s01.assets WHERE id=$1 AND tenant=$2 AND issuer=$3 AND subject=$4 AND kind=$5", id, i.Tenant, i.Issuer, i.Subject, kind).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return r.ErrNotFound
	}
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, out); e != nil {
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

var _ r.Repository = (*Store)(nil)
