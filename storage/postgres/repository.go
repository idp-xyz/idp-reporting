// Package postgres stores only the platform's own assets: a guarded test fixture (Open) or a
// production store (OpenProduction, S04-A), migrated through the embedded migrations. It never
// accesses source product tables.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// objects is where the content of an artifact lives (S04-E, docs/ADR_0007_OBJECT_STORAGE.md): with one, Publish takes an artifact that is an
	// object already put and refuses one that still carries its content, and the sweep deletes the objects of the rows it deletes and reconciles
	// the bucket. objectNow is the platform's clock for the age of an object (see SweepWithObjects).
	objects   r.ObjectStore
	objectNow func() time.Time
}

// Option configures a store as it is opened.
type Option func(*Store)

// WithObjects gives the store the object store the artifacts' content lives in (S04-E). The service that publishes to the store must be
// configured with the same one (Config.Objects).
func WithObjects(o r.ObjectStore) Option { return func(s *Store) { s.objects = o } }

// WithObjectClock sets the clock by which the sweep judges the age of an object (default time.Now): the one place the platform host's clock
// decides anything, because the clock that stamped the object is the object store's and not the database's. Tests move it.
func WithObjectClock(now func() time.Time) Option { return func(s *Store) { s.objectNow = now } }

func (s *Store) apply(opts []Option) *Store {
	for _, o := range opts {
		o(s)
	}
	return s
}

// Open opens the guarded test fixture: one of the fixture database names on a loopback address,
// the contract-simulator profile, the schema at the binary's version. With initialize, the
// migrations are applied first and the profile row inserted; a database at an older version
// (made before the ledger existed included) is upgraded. Production stores open through
// OpenProduction.
func Open(ctx context.Context, dsn string, initialize bool, opts ...Option) (*Store, error) {
	p, e := fixturedb.Open(ctx, dsn, fixtureDatabases...)
	if e != nil {
		return nil, e
	}
	s := (&Store{pool: p, fixture: true}).apply(opts)
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
	_, e := s.pool.Exec(ctx, "TRUNCATE reporting_s01.assets, reporting_s01.jobs, reporting_s01.sweeps, reporting_s01.designs")
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
	// A reduced job (S04-B) has no query or source read left to bind: it is held to its own shape.
	valid := r.ValidateAdmission
	if j.Reduced {
		valid = r.ValidateReduced
	}
	if j.ID != id || j.Owner != owner || j.Kind != kind || j.Key != key || j.RequestHash != requestHash || valid(j) != nil {
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

// The lock and the counts of admission (S04-D, docs/S04D_SCOPE.md, docs/OPERATIONS.md "Quotas"). Admit takes a transaction-scoped advisory
// lock keyed on the identity, so that two admissions of one identity count and insert one after the other (the second counts what the
// first committed), while two identities never wait on each other; the lock ends with the transaction, committed or not. The key is the
// three parts of the identity, each length-prefixed so that no two identities share one text, under a prefix of this use.
const (
	admitLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	// admitReplaySQL finds the job a key already names: a replay is settled before anything is counted.
	admitReplaySQL = `SELECT EXISTS (SELECT 1 FROM reporting_s01.jobs WHERE tenant=$1 AND integration=$2 AND issuer=$3 AND subject=$4 AND kind=$5 AND idem_key=$6)`
	// The three counts of the identity's own rows, by the database's clock: the queued and running jobs; the snapshots and artifacts whose
	// expiry is still in the future (an issue record is another kind and never counts; an expired one waits for the sweep and does not
	// count; a NULL expiry is not in the future); the jobs admitted in the last hour.
	admitInFlightSQL = `SELECT count(*) FROM reporting_s01.jobs WHERE tenant=$1 AND issuer=$2 AND subject=$3 AND state IN ('queued','running')`
	admitResultsSQL  = `SELECT count(*) FROM reporting_s01.assets WHERE tenant=$1 AND issuer=$2 AND subject=$3 AND kind IN ('snapshot','artifact') AND expires_at > clock_timestamp()`
	admitHourSQL     = `SELECT count(*) FROM reporting_s01.jobs WHERE tenant=$1 AND issuer=$2 AND subject=$3 AND created_at > clock_timestamp() - interval '1 hour'`
	// admitCountSQL is the three counts in one round trip; the indexes of migration 009 (S04-G) serve them: jobs_identity_in_flight,
	// assets_identity_kind_expires and jobs_identity_created, in this order.
	admitCountSQL = `SELECT (` + admitInFlightSQL + `), (` + admitResultsSQL + `), (` + admitHourSQL + `)`
	// issueListingSQL is the listing of an identity's issue jobs, newest first (Store.Issues); jobs_identity_created serves it.
	issueListingSQL = "SELECT " + jobFields + " FROM reporting_s01.jobs WHERE kind='issue' AND tenant=$1 AND issuer=$2 AND subject=$3 AND integration=ANY($4) ORDER BY created_at DESC, id DESC LIMIT $5"
	// jobListingSQL is the listing of an identity's jobs of every kind, newest first (Store.Jobs, S07-C); jobs_identity_created serves it.
	// S07-E: of one kind ($6) and one state ($7) when they are not empty, and strictly older than the identity's job $8 when it is not empty
	// (a job that is not the identity's, or not under $4, makes the comparison null and the page empty).
	jobListingSQL = "SELECT " + jobFields + " FROM reporting_s01.jobs WHERE tenant=$1 AND issuer=$2 AND subject=$3 AND integration=ANY($4)" +
		" AND ($6='' OR kind=$6) AND ($7='' OR state=$7)" +
		" AND ($8='' OR (created_at,id) < (SELECT b.created_at,b.id FROM reporting_s01.jobs b WHERE b.id=$8 AND b.tenant=$1 AND b.issuer=$2 AND b.subject=$3 AND b.integration=ANY($4)))" +
		" ORDER BY created_at DESC, id DESC LIMIT $5"
)

// CapacityStatement is one of the statements whose plan the capacity qualification holds to the indexes of migration 009 (S04-G): the three
// counts of an admission, taken one at a time, and the issue listing. Args makes its arguments for an identity.
type CapacityStatement struct {
	Name string
	SQL  string
	Args func(i r.Identity, integrations []string) []any
}

// CapacityStatementsForTests returns the statements the platform runs on its hot paths, exactly as it runs them, for the plan test and the
// measurement tool (internal/capacity). Nothing in the platform runs them through this.
func CapacityStatementsForTests() []CapacityStatement {
	identity := func(i r.Identity, _ []string) []any { return []any{i.Tenant, i.Issuer, i.Subject} }
	return []CapacityStatement{
		{"jobs in flight", admitInFlightSQL, identity},
		{"stored results", admitResultsSQL, identity},
		{"admissions in the last hour", admitHourSQL, identity},
		{"issue listing", issueListingSQL, func(i r.Identity, integrations []string) []any {
			return []any{i.Tenant, i.Issuer, i.Subject, integrations, 25}
		}},
		{"job listing", jobListingSQL, func(i r.Identity, integrations []string) []any {
			return []any{i.Tenant, i.Issuer, i.Subject, integrations, 50, "", "", ""}
		}},
	}
}

// AdmissionLockForTests returns the statement that takes an identity's admission lock and the key it takes it on, for the qualification that
// holds one identity's lock and watches another's admission pass it by. Nothing in the platform runs it.
func AdmissionLockForTests(i r.Identity) (sql, key string) { return admitLockSQL, admitLockKey(i) }

func admitLockKey(i r.Identity) string {
	return fmt.Sprintf("reporting.admit:%d:%s:%d:%s:%d:%s", len(i.Tenant), i.Tenant, len(i.Issuer), i.Issuer, len(i.Subject), i.Subject)
}

// Admit stores the job as it is: every kind goes through the same insert. An issue job has no
// number (S03-H): the source product gives it when the worker prepares the document. Inside the one
// transaction of the identity's context it takes the identity's lock, settles a replay of the key (which no quota refuses), counts, and
// only then inserts; a refusal rolls back and changes nothing.
func (s *Store) Admit(ctx context.Context, j r.Job, quotas r.Quotas) (r.Job, error) {
	if e := r.ValidateAdmission(j); e != nil {
		return r.Job{}, e
	}
	quotas, e := quotas.Resolved()
	if e != nil {
		return r.Job{}, e
	}
	var v r.Job
	e = asIdentity(ctx, s.pool, j.Owner.Identity, func(tx pgx.Tx) (err error) {
		if _, err = tx.Exec(ctx, admitLockSQL, admitLockKey(j.Owner.Identity)); err != nil {
			return err
		}
		var replay bool
		o := j.Owner
		if err = tx.QueryRow(ctx, admitReplaySQL, o.Tenant, o.Integration, o.Issuer, o.Subject, j.Kind, j.Key).Scan(&replay); err != nil {
			return err
		}
		if !replay {
			var inFlight, results, lastHour int
			if err = tx.QueryRow(ctx, admitCountSQL, o.Tenant, o.Issuer, o.Subject).Scan(&inFlight, &results, &lastHour); err != nil {
				return err
			}
			switch {
			case inFlight >= quotas.JobsInFlight:
				return &r.QuotaError{Quota: r.QuotaJobsInFlight}
			case results >= quotas.StoredResults:
				return &r.QuotaError{Quota: r.QuotaStoredResults}
			case lastHour >= quotas.AdmissionsPerHour:
				return &r.QuotaError{Quota: r.QuotaAdmissionsPerHour}
			}
		}
		v, err = admit(ctx, tx, j)
		return err
	})
	if e != nil {
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

// The identity predicate of every identity-scoped statement. It is the service's own isolation; the row-level policies of migration 007
// are the database's, and either holds without the other. The statements without the predicate exist only for the test that proves that
// (UnscopedQueriesForTests).
const (
	jobByID       = "SELECT " + jobFields + " FROM reporting_s01.jobs WHERE id=$1"
	jobIdentity   = " AND tenant=$2 AND issuer=$3 AND subject=$4"
	assetByID     = "SELECT data::text,data_sha256,id,kind,tenant,integration,issuer,subject,expires_at FROM reporting_s01.assets WHERE id=$1"
	assetIdentity = " AND tenant=$2 AND issuer=$3 AND subject=$4 AND kind=$5"
)

// UnscopedQueriesForTests returns the statements of Job and of the asset reads (Snapshot, Artifact, Issue) with their identity predicates
// removed, each taking the id alone, for the qualification that a missing predicate leaks nothing to a connection with a context. Nothing in
// the platform runs them.
func UnscopedQueriesForTests() (job, asset string) { return jobByID, assetByID }

func (s *Store) Job(ctx context.Context, i r.Identity, id string) (r.Job, error) {
	var j r.Job
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) (err error) {
		j, err = scanJob(tx.QueryRow(ctx, jobByID+jobIdentity, id, i.Tenant, i.Issuer, i.Subject))
		return err
	})
	return j, e
}

// Cancel cancels a queued job, and a running one of any kind but issue: the source product may
// hold a reservation for a running issue job (S03-H), which therefore finishes, or fails and voids,
// and is returned unchanged.
func (s *Store) Cancel(ctx context.Context, i r.Identity, id string) (r.Job, error) {
	var j r.Job
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) (err error) {
		j, err = scanJob(tx.QueryRow(ctx, `UPDATE reporting_s01.jobs SET
state=CASE WHEN state='queued' OR (state='running' AND kind<>'issue') THEN 'cancelled' ELSE state END,
lease_until=CASE WHEN state='queued' OR (state='running' AND kind<>'issue') THEN NULL ELSE lease_until END
WHERE id=$1 AND tenant=$2 AND issuer=$3 AND subject=$4 RETURNING `+jobFields, id, i.Tenant, i.Issuer, i.Subject))
		return err
	})
	return j, e
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
	tx, e := beginWorker(ctx, s.pool)
	if e != nil {
		return nil, false, e
	}
	defer rollback(ctx, tx)
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
	tx, e := beginWorker(ctx, s.pool)
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
	// Where the content lives is the store's: with an object store the artifact must be an object already put and carry no content, without
	// one it must carry its content and name no object (S04-E). The row never promises what the store cannot serve.
	if p.Artifact != nil && (s.objects != nil) != (p.Artifact.Object != "") {
		return r.ErrIntegrity
	}
	// expires is the asset's expiry as the column (S04-B) keeps it: the snapshot's and the artifact's, never the issue record's, which
	// does not expire and which the sweep never deletes.
	type asset struct {
		id, kind string
		value    any
		expires  *time.Time
	}
	var assets []asset
	snapshotID, artifactID := j.SnapshotID, j.ArtifactID
	if p.Snapshot != nil {
		snapshotID = p.Snapshot.ID
		assets = append(assets, asset{p.Snapshot.ID, "snapshot", p.Snapshot, &p.Snapshot.ExpiresAt})
	} else {
		artifactID = p.Artifact.ID
		assets = append(assets, asset{p.Artifact.ID, "artifact", p.Artifact, &p.Artifact.ExpiresAt})
		if p.Issue != nil {
			assets = append(assets, asset{p.Issue.ID, "issue", p.Issue, nil})
		}
	}
	for _, a := range assets {
		b, e := json.Marshal(a.value)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,encode(sha256(convert_to($7::jsonb::text,'UTF8')),'hex'),$8)", a.id, j.Owner.Tenant, j.Owner.Integration, j.Owner.Issuer, j.Owner.Subject, a.kind, b, a.expires); e != nil {
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
	return s.workerUpdate(ctx, `UPDATE reporting_s01.jobs SET state='failed',lease_until=NULL,error_code=$1 WHERE id=$2 AND tenant=$3 AND integration=$4 AND issuer=$5 AND subject=$6 AND state='running' AND generation=$7 AND lease_until>clock_timestamp()`, code, c.Job.ID, c.Job.Owner.Tenant, c.Job.Owner.Integration, c.Job.Owner.Issuer, c.Job.Owner.Subject, c.Job.Generation)
}

// workerUpdate runs one fenced update in a transaction of the worker scope: exactly one row changes, or it is ErrLease and none does.
func (s *Store) workerUpdate(ctx context.Context, sql string, args ...any) error {
	return asWorker(ctx, s.pool, func(tx pgx.Tx) error {
		result, e := tx.Exec(ctx, sql, args...)
		if e != nil {
			return e
		}
		if result.RowsAffected() != 1 {
			return r.ErrLease
		}
		return nil
	})
}

// ResolveFailure replaces the code ISSUE_UNRECONCILED of a failed issue job with the job's own failure
// code once the source product has voided what it held (S03-H). It applies only to an issue job that is
// failed, at the claim's generation, with that code: anything else is ErrLease and changes nothing. It
// needs no lease, which a failed job has not.
func (s *Store) ResolveFailure(ctx context.Context, c r.Claim, code string) error {
	if code == "" || code == r.ErrUnreconciled.Error() {
		return r.ErrInvalid
	}
	return s.workerUpdate(ctx, `UPDATE reporting_s01.jobs SET error_code=$1 WHERE id=$2 AND tenant=$3 AND integration=$4 AND issuer=$5 AND subject=$6 AND kind='issue' AND state='failed' AND generation=$7 AND error_code=$8`, code, c.Job.ID, c.Job.Owner.Tenant, c.Job.Owner.Integration, c.Job.Owner.Issuer, c.Job.Owner.Subject, c.Job.Generation, r.ErrUnreconciled.Error())
}
func (s *Store) asset(ctx context.Context, i r.Identity, id, kind string, out any) error {
	var b []byte
	var digest, resourceID, resourceKind string
	var owner r.Owner
	var expires *time.Time
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, assetByID+assetIdentity, id, i.Tenant, i.Issuer, i.Subject, kind).Scan(&b, &digest, &resourceID, &resourceKind, &owner.Tenant, &owner.Integration, &owner.Issuer, &owner.Subject, &expires)
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return r.ErrNotFound
	}
	if e != nil {
		return e
	}
	if e = fixturedb.Decode(b, digest, out); e != nil {
		return e
	}
	// The expiry column is what the sweep deletes by (S04-B): it must be the payload's expiry, as the store wrote it (to the microsecond),
	// or the asset is not served; an issue record has none.
	sameExpiry := func(payload time.Time) bool {
		return expires != nil && expires.Equal(payload.Truncate(time.Microsecond))
	}
	switch v := out.(type) {
	case *r.Snapshot:
		if v.ID != resourceID || v.Owner != owner || resourceKind != "snapshot" || v.Prepared.Context.Owner != v.Owner || v.Prepared.QueryHash != r.QueryHash(v.Query) || !sameExpiry(v.ExpiresAt) {
			return r.ErrIntegrity
		}
	case *r.Artifact:
		// The content is in the row or in the object store, never both and never neither; an object is the key of the artifact's own id, with a
		// length and a digest (S04-E).
		if v.ID != resourceID || v.Owner != owner || resourceKind != "artifact" || !sameExpiry(v.ExpiresAt) || (v.Object != "") == (len(v.Content) > 0) || (v.Object != "" && !r.ValidArtifactBody(*v)) {
			return r.ErrIntegrity
		}
	case *r.Issue:
		if v.ID != resourceID || v.Owner != owner || resourceKind != "issue" || v.Stamp != r.IssueStamp(v.Number, v.IssuedAt, v.ID) || expires != nil {
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
	return s.listing(ctx, i, integrations, limit, r.MaxIssueListing, issueListingSQL)
}
func (s *Store) Jobs(ctx context.Context, i r.Identity, l r.JobListing) ([]r.Job, error) {
	if !l.Valid() {
		return nil, r.ErrInvalid
	}
	return s.listing(ctx, i, l.Integrations, l.Limit, r.MaxJobListing, jobListingSQL, l.Kind, l.State, l.Before)
}

// listing runs one of the two listing statements as the identity, newest first, at most limit (1..most) jobs; extra are the job
// listing's kind, state and cursor.
func (s *Store) listing(ctx context.Context, i r.Identity, integrations []string, limit, most int, sql string, extra ...any) ([]r.Job, error) {
	if limit < 1 || limit > most || !i.Valid() || len(integrations) == 0 {
		return nil, r.ErrInvalid
	}
	out := []r.Job{}
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, sql, append([]any{i.Tenant, i.Issuer, i.Subject, integrations, limit}, extra...)...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			j, e := scanJob(rows)
			if e != nil {
				return e
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

var _ r.Repository = (*Store)(nil)

// OnPoolForTests is a store over a pool the test made (one of a single connection, say, to show that no context outlives its call). It checks
// nothing and is not a way to open a store: production opens through OpenProduction, the fixture through Open.
func OnPoolForTests(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }
