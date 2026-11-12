// Package capacity seeds and measures the platform's PostgreSQL store (S04-G, docs/S04G_SCOPE.md G2, docs/CAPACITY.md). It is internal: a
// qualification tool and the seed of the plan test, not an API. It writes only to the guarded fixture database (the same guard as the other
// fixture tests, and never port 55432), through SQL, and removes every row it seeded: all of them are in tenants whose name starts with TenantPrefix.
package capacity

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"time"
)

// TenantPrefix starts the tenant of every row the tool seeds or admits; Remove deletes by it.
const TenantPrefix = "cap-t"

const (
	// Issuer is the issuer of every seeded identity.
	Issuer = "cap-iss"
	// Hot is the index of the identity that holds HotPercent of the rows: the one with a long history.
	Hot = 0
)

// Profile sizes a seed.
type Profile struct {
	Jobs       int // job rows of the bulk history
	Assets     int // asset rows of the bulk history
	Identities int // at least 2; identity 0 is the hot one
	Tenants    int // the identities are spread over this many tenants (k mod Tenants)
	HotPercent int // share of the jobs and of the assets held by identity 0
	Days       int // the history's span: created_at is spread over this many days up to now
}

// Valid says whether the profile can be seeded.
func (p Profile) Valid() bool {
	return p.Jobs >= 1 && p.Assets >= 1 && p.Identities >= 2 && p.Tenants >= 1 && p.HotPercent >= 0 && p.HotPercent <= 90 && p.Days >= 1
}

// Identity is identity k of the profile.
func (p Profile) Identity(k int) r.Identity {
	return r.Identity{Tenant: fmt.Sprintf("%s%d", TenantPrefix, k%p.Tenants), Issuer: Issuer, Subject: fmt.Sprintf("cap-u%d", k)}
}

// Owner is Identity(k) with the integration of the seeded jobs.
func (p Profile) Owner(k int) r.Owner {
	return r.Owner{Identity: p.Identity(k), Integration: contractsim.FinanceIntegration}
}

// Template is a job as the service admits it (a snapshot of the financial fixture), taken from the in-memory store: the payload every seeded
// job carries and the shape every measured admission has.
func Template(ctx context.Context) (r.Job, error) {
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(time.Now)
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901")})
	if e != nil {
		return r.Job{}, e
	}
	v, e := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "template")
	if e != nil {
		return r.Job{}, e
	}
	return repo.Job(ctx, p.Identity, v.ID)
}

const seedJobsSQL = `WITH g AS (SELECT g, CASE WHEN g <= $4 THEN 0 ELSE 1 + ((g - $4) % ($2 - 1)) END AS k FROM generate_series(1, $1) g),
x AS (SELECT g, k, '` + TenantPrefix + `' || (k % $3) AS tenant, 'cap-u' || k AS subject, md5('cap-job-' || g) AS id, 'k' || g AS key,
  CASE WHEN g % 500 = 0 AND (k <> 0 OR g % 3000 = 0) THEN 'queued' WHEN g % 500 = 1 AND (k <> 0 OR g % 3000 = 1) THEN 'running' WHEN g % 7 = 0 THEN 'failed' WHEN g % 11 = 0 THEN 'cancelled' ELSE 'succeeded' END AS state,
  CASE WHEN g % 5 = 2 THEN 'issue' ELSE 'snapshot' END AS kind,
  now() - ((g::bigint * 1000003) % $1)::float8 / $1 * $5 * interval '1 day' AS created FROM g),
o AS (SELECT x.*, jsonb_build_object('Tenant', tenant, 'Issuer', '` + Issuer + `', 'Subject', subject, 'Integration', $7::text) AS owner FROM x),
p AS (SELECT o.*, jsonb_set(jsonb_set(jsonb_set(jsonb_set($6::jsonb, '{ID}', to_jsonb(id)), '{Key}', to_jsonb(key)), '{Owner}', owner), '{Prepared,Context,Owner}', owner) AS d FROM o)
INSERT INTO reporting_s01.jobs(id, tenant, integration, issuer, subject, kind, state, idem_key, request_hash, lease_until, source_expires_at, data, data_sha256, created_at)
SELECT id, tenant, $7, '` + Issuer + `', subject, kind, state, key, $8, CASE WHEN state = 'running' THEN now() + interval '1 hour' END, now() + interval '365 days', d,
  encode(sha256(convert_to(d::text, 'UTF8')), 'hex'), created FROM p`

// Of the assets, one in ten is an issue record, half are snapshots and the rest artifacts. Two in fifteen of the assets are the files of
// issued documents (an artifact whose payload names its issue), kept for nine years, as the store holds them once a source vouches for the
// scope of its reads (ADR 0026): the rows the stored-results count must not read (S25-B1). The other artifacts are exports.
const seedAssetsSQL = `WITH g AS (SELECT g, CASE WHEN g <= $4 THEN 0 ELSE 1 + ((g - $4) % ($2 - 1)) END AS k FROM generate_series(1, $1) g),
x AS (SELECT g, k, '` + TenantPrefix + `' || (k % $3) AS tenant, 'cap-u' || k AS subject, md5('cap-asset-' || g) AS id,
  CASE WHEN g % 10 = 0 THEN 'issue' WHEN g % 2 = 0 THEN 'artifact' ELSE 'snapshot' END AS kind,
  g % 10 <> 0 AND g % 6 = 0 AS issued,
  CASE WHEN g % 10 = 0 THEN NULL WHEN g % 6 = 0 THEN date_trunc('microseconds', now() + interval '9 years') WHEN g % 50 = 1 THEN date_trunc('microseconds', now() + interval '6 hours')
    ELSE date_trunc('microseconds', now() - ((1 + g % 19) * interval '1 hour')) END AS expires FROM g),
p AS (SELECT x.*, jsonb_build_object('ID', id, 'Owner', jsonb_build_object('Tenant', tenant, 'Issuer', '` + Issuer + `', 'Subject', subject, 'Integration', $5::text),
  'SnapshotID', md5('cap-snapshot-' || g), 'ExpiresAt', to_char(expires AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), 'SHA256', md5(id) || md5(id || 'x'),
  'Content', encode(decode(md5(id || '1') || md5(id || '2') || md5(id || '3') || md5(id || '4') || md5(id || '5') || md5(id || '6') || md5(id || '7') || md5(id || '8'), 'hex'), 'base64'))
  || CASE WHEN issued THEN jsonb_build_object('Issue', md5('cap-issued-' || g)) ELSE '{}'::jsonb END AS d FROM x)
INSERT INTO reporting_s01.assets(id, tenant, integration, issuer, subject, kind, data, data_sha256, expires_at)
SELECT id, tenant, $5, '` + Issuer + `', subject, kind, d, encode(sha256(convert_to(d::text, 'UTF8')), 'hex'), expires FROM p`

// Seed writes the bulk history. The rows are shaped to be inert to the retention sweep (the jobs' source reads expire in a year, the assets
// that have expired did so within the grace, the issued files have years left), so that the sweep step measures exactly the rows SeedSweep
// adds.
func Seed(ctx context.Context, admin *pgxpool.Pool, p Profile, tmpl r.Job) error {
	if !p.Valid() {
		return r.ErrInvalid
	}
	b, e := json.Marshal(tmpl)
	if e != nil {
		return e
	}
	hotJobs, hotAssets := p.Jobs*p.HotPercent/100, p.Assets*p.HotPercent/100
	if _, e = admin.Exec(ctx, seedJobsSQL, p.Jobs, p.Identities, p.Tenants, hotJobs, p.Days, string(b), contractsim.FinanceIntegration, tmpl.RequestHash); e != nil {
		return fmt.Errorf("seeding jobs: %w", e)
	}
	if _, e = admin.Exec(ctx, seedAssetsSQL, p.Assets, p.Identities, p.Tenants, hotAssets, contractsim.FinanceIntegration); e != nil {
		return fmt.Errorf("seeding assets: %w", e)
	}
	_, e = admin.Exec(ctx, "ANALYZE reporting_s01.jobs; ANALYZE reporting_s01.assets")
	return e
}

// SeedSweep adds n finished jobs and their expired artifacts, three days past their expiry: what one sweep deletes (the artifacts) and then
// reduces (the jobs).
func SeedSweep(ctx context.Context, admin *pgxpool.Pool, p Profile, tmpl r.Job, n int) error {
	b, e := json.Marshal(tmpl)
	if e != nil {
		return e
	}
	_, e = admin.Exec(ctx, `WITH x AS (SELECT g, 1 + (g % ($2 - 1)) AS k FROM generate_series(1, $1) g),
o AS (SELECT x.*, '`+TenantPrefix+`' || (k % $3) AS tenant, 'cap-u' || k AS subject, md5('cap-swj-' || g) AS id, md5('cap-swa-' || g) AS aid, date_trunc('microseconds', now() - interval '3 days') AS exp FROM x),
w AS (SELECT o.*, jsonb_build_object('Tenant', tenant, 'Issuer', '`+Issuer+`', 'Subject', subject, 'Integration', $5::text) AS owner FROM o),
p AS (SELECT w.*, jsonb_set(jsonb_set(jsonb_set(jsonb_set($4::jsonb, '{ID}', to_jsonb(id)), '{Key}', to_jsonb('s' || g)), '{Owner}', owner), '{Prepared,Context,Owner}', owner) AS d FROM w),
ins_assets AS (INSERT INTO reporting_s01.assets(id, tenant, integration, issuer, subject, kind, data, data_sha256, expires_at)
  SELECT aid, tenant, $5, '`+Issuer+`', subject, 'artifact', a.d, encode(sha256(convert_to(a.d::text, 'UTF8')), 'hex'), exp
  FROM p, LATERAL (SELECT jsonb_build_object('ID', aid, 'Owner', owner, 'SnapshotID', md5('cap-sws-' || g), 'ExpiresAt', to_char(exp AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), 'SHA256', md5(aid) || md5(aid || 'x'), 'Content', encode(decode(md5(aid), 'hex'), 'base64')) AS d) a)
INSERT INTO reporting_s01.jobs(id, tenant, integration, issuer, subject, kind, state, idem_key, request_hash, source_expires_at, data, data_sha256, created_at, artifact_id)
SELECT id, tenant, $5, '`+Issuer+`', subject, 'snapshot', 'succeeded', 's' || g, $6, exp, d, encode(sha256(convert_to(d::text, 'UTF8')), 'hex'), exp, aid FROM p`,
		n, p.Identities, p.Tenants, string(b), contractsim.FinanceIntegration, tmpl.RequestHash)
	return e
}

// Remove deletes every row the tool seeded or admitted (by tenant prefix).
func Remove(ctx context.Context, admin *pgxpool.Pool) error {
	for _, t := range []string{"jobs", "assets"} {
		if _, e := admin.Exec(ctx, "DELETE FROM reporting_s01."+t+" WHERE tenant LIKE '"+TenantPrefix+"%'"); e != nil {
			return e
		}
	}
	return nil
}
