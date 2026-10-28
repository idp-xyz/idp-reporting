// Package pgsource is a durable synthetic source, never a real product adapter.
package pgsource

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
	"reflect"
	"slices"
	"sync/atomic"
	"time"
)

//go:embed fixture.sql
var schema string

type Source struct {
	pool  *pgxpool.Pool
	reads atomic.Int64
}
type savedRead struct {
	Prepared r.PreparedRead
	Query    r.QuerySpec
	Grant    contractsim.Grant
	Version  string
}
type sourceView struct {
	Version string
	Dataset r.Dataset
	Grant   contractsim.Grant
	Now     time.Time
}

func Open(ctx context.Context, dsn string, initialize bool) (*Source, error) {
	p, e := fixturedb.Open(ctx, dsn, "reporting_source_test_s01", "reporting_source_test_s01_race")
	if e != nil {
		return nil, e
	}
	fail := func(err error) (*Source, error) { p.Close(); return nil, err }
	if initialize {
		if e = migrate(ctx, p); e != nil {
			return fail(e)
		}
	}
	var name string
	var version int
	if e = p.QueryRow(ctx, "SELECT profile,version FROM reporting_source_s01.profile WHERE singleton").Scan(&name, &version); e != nil {
		return fail(e)
	}
	if name != "durable-contract-simulator-only" || version != profileVersion {
		return fail(r.ErrDenied)
	}
	return &Source{pool: p}, nil
}

// profileVersion is the schema this code requires: 2 since S03-H added the number sequences and
// the issue reservations; version 1 databases are upgraded by Open(initialize=true).
const profileVersion = 2

// migrate creates the schema, or upgrades a version 1 one, in a single transaction. The advisory
// lock keeps two initializing processes from creating the same tables at once.
func migrate(ctx context.Context, p *pgxpool.Pool) error {
	tx, e := p.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "reporting_source_s01.schema"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, schema); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Source) Close()         { s.pool.Close() }
func (s *Source) ReadCalls() int { return int(s.reads.Load()) }
func (s *Source) ResetFixture(ctx context.Context) error {
	_, e := s.pool.Exec(ctx, "TRUNCATE reporting_source_s01.prepared,reporting_source_s01.grants,reporting_source_s01.heads,reporting_source_s01.versions,reporting_source_s01.issue_numbers,reporting_source_s01.issues")
	return e
}
func rollback(ctx context.Context, tx pgx.Tx) {
	cleanup, cancel := fixturedb.RollbackContext(ctx)
	defer cancel()
	_ = tx.Rollback(cleanup)
}
func validFixture(id, tenant string, f contractsim.Fixture) error {
	q := r.QuerySpec{SchemaVersion: "1.0", Integration: id, Dataset: f.Dataset.Ref, Companies: []string{"fixture"}, Dimensions: f.Dataset.Dimensions, Metrics: f.Dataset.Metrics, CurrencyPolicy: f.Dataset.CurrencyPolicy, Timezone: f.Dataset.Timezone, AsOf: "2026-10-26", Page: r.PageSpec{Limit: 50}}
	if id == "" || tenant == "" || len(id) > 128 || len(tenant) > 128 || r.ValidateQuery(q, f.Dataset) != nil || len(f.Records) > 5000 {
		return r.ErrInvalid
	}
	if f.NumberPrefix != "" && !contractsim.ValidNumberPrefix(f.NumberPrefix) {
		return r.ErrInvalid
	}
	b, e := json.Marshal(f.Records)
	if e != nil || len(b) > 2<<20 {
		return r.ErrBudget
	}
	for _, record := range f.Records {
		if record.Company == "" || record.Key == "" || len(record.Values) != len(f.Dataset.Columns) {
			return r.ErrInvalid
		}
	}
	return nil
}
func putVersion(ctx context.Context, tx pgx.Tx, id, tenant string, f contractsim.Fixture) (string, error) {
	version := r.NewID()
	catalog, e := json.Marshal(f.Dataset)
	if e != nil {
		return "", e
	}
	records, e := json.Marshal(f.Records)
	if e != nil {
		return "", e
	}
	_, e = tx.Exec(ctx, `INSERT INTO reporting_source_s01.versions(integration,source_tenant,id,catalog,catalog_sha,records,records_sha) VALUES($1,$2,$3,$4,encode(sha256(convert_to($4::jsonb::text,'UTF8')),'hex'),$5,encode(sha256(convert_to($5::jsonb::text,'UTF8')),'hex'))`, id, tenant, version, catalog, records)
	return version, e
}

// EnsureFixture never updates an existing source version or clears outages. It records the
// fixture's number prefix for the source, and creates the number sequence of a source whose
// fixture predates the capability; an existing sequence, its prefix and its switch stay.
func (s *Source) EnsureFixture(ctx context.Context, id, tenant string, f contractsim.Fixture) error {
	if e := validFixture(id, tenant, f); e != nil {
		return e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", r.Digest([]string{id, tenant})); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM reporting_source_s01.heads WHERE integration=$1 AND source_tenant=$2)", id, tenant).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		v, e := putVersion(ctx, tx, id, tenant, f)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO reporting_source_s01.heads(integration,source_tenant,version_id) VALUES($1,$2,$3)", id, tenant, v); e != nil {
			return e
		}
	}
	prefix := f.NumberPrefix
	if prefix == "" {
		prefix = contractsim.DefaultNumberPrefix
	}
	if _, e = tx.Exec(ctx, "INSERT INTO reporting_source_s01.issue_numbers(integration,source_tenant,prefix) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", id, tenant, prefix); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Source) ReplaceFixture(ctx context.Context, id, tenant string, f contractsim.Fixture) error {
	if e := validFixture(id, tenant, f); e != nil {
		return e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	var old string
	if e = tx.QueryRow(ctx, "SELECT version_id FROM reporting_source_s01.heads WHERE integration=$1 AND source_tenant=$2 FOR UPDATE", id, tenant).Scan(&old); e != nil {
		return e
	}
	v, e := putVersion(ctx, tx, id, tenant, f)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE reporting_source_s01.heads SET version_id=$3 WHERE integration=$1 AND source_tenant=$2", id, tenant, v); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func validGrant(sc r.SourceContext, g contractsim.Grant) error {
	if !sc.Owner.Identity.Valid() || sc.Owner.Integration == "" || sc.SourceTenant == "" || len(g.Companies) == 0 || len(g.Companies) > 100 || len(g.Rows) > 5000 {
		return r.ErrInvalid
	}
	if _, e := time.LoadLocation(g.Timezone); e != nil {
		return r.ErrInvalid
	}
	return nil
}
func (s *Source) writeGrant(ctx context.Context, sc r.SourceContext, g contractsim.Grant, ensure bool) error {
	if e := validGrant(sc, g); e != nil {
		return e
	}
	b, e := json.Marshal(g)
	if e != nil || len(b) > 65536 {
		return r.ErrBudget
	}
	conflict := "DO UPDATE SET active=true,data=excluded.data,data_sha=excluded.data_sha"
	if ensure {
		conflict = "DO NOTHING"
	}
	_, e = s.pool.Exec(ctx, `INSERT INTO reporting_source_s01.grants(tenant,integration,issuer,subject,source_tenant,active,data,data_sha) VALUES($1,$2,$3,$4,$5,true,$6,encode(sha256(convert_to($6::jsonb::text,'UTF8')),'hex')) ON CONFLICT(tenant,integration,issuer,subject,source_tenant) `+conflict, sc.Owner.Tenant, sc.Owner.Integration, sc.Owner.Issuer, sc.Owner.Subject, sc.SourceTenant, b)
	return e
}
func (s *Source) EnsureGrant(ctx context.Context, sc r.SourceContext, g contractsim.Grant) error {
	return s.writeGrant(ctx, sc, g, true)
}
func (s *Source) SetGrant(ctx context.Context, sc r.SourceContext, g contractsim.Grant) error {
	return s.writeGrant(ctx, sc, g, false)
}
func (s *Source) Revoke(ctx context.Context, sc r.SourceContext) error {
	if !sc.Owner.Identity.Valid() || sc.Owner.Integration == "" || sc.SourceTenant == "" {
		return r.ErrInvalid
	}
	_, e := s.pool.Exec(ctx, `INSERT INTO reporting_source_s01.grants(tenant,integration,issuer,subject,source_tenant,active,data,data_sha) VALUES($1,$2,$3,$4,$5,false,'{}',encode(sha256(convert_to('{}'::jsonb::text,'UTF8')),'hex')) ON CONFLICT(tenant,integration,issuer,subject,source_tenant) DO UPDATE SET active=false`, sc.Owner.Tenant, sc.Owner.Integration, sc.Owner.Issuer, sc.Owner.Subject, sc.SourceTenant)
	return e
}
func (s *Source) SetUnavailable(ctx context.Context, id, tenant string, v bool) error {
	result, e := s.pool.Exec(ctx, "UPDATE reporting_source_s01.heads SET unavailable=$3 WHERE integration=$1 AND source_tenant=$2", id, tenant, v)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return r.ErrNotFound
	}
	return nil
}
func loadSource(ctx context.Context, tx pgx.Tx, sc r.SourceContext, lock bool) (sourceView, error) {
	var v sourceView
	var c, g []byte
	var ch, gh string
	var unavailable bool
	sql := `SELECT h.version_id,h.unavailable,v.catalog::text,v.catalog_sha,g.data::text,g.data_sha,clock_timestamp() FROM reporting_source_s01.heads h JOIN reporting_source_s01.versions v ON (v.integration,v.source_tenant,v.id)=(h.integration,h.source_tenant,h.version_id) JOIN reporting_source_s01.grants g ON (g.integration,g.source_tenant)=(h.integration,h.source_tenant) WHERE h.integration=$1 AND h.source_tenant=$2 AND g.tenant=$3 AND g.issuer=$4 AND g.subject=$5 AND g.active`
	if lock {
		sql += " FOR SHARE OF h,g"
	}
	e := tx.QueryRow(ctx, sql, sc.Owner.Integration, sc.SourceTenant, sc.Owner.Tenant, sc.Owner.Issuer, sc.Owner.Subject).Scan(&v.Version, &unavailable, &c, &ch, &g, &gh, &v.Now)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, r.ErrDenied
	}
	if e != nil {
		return v, r.ErrUnavailable
	}
	if unavailable {
		return v, r.ErrUnavailable
	}
	if e = fixturedb.Decode(c, ch, &v.Dataset); e != nil {
		return v, e
	}
	if e = fixturedb.Decode(g, gh, &v.Grant); e != nil {
		return v, e
	}
	return v, nil
}
func (s *Source) begin(ctx context.Context, readOnly bool) (pgx.Tx, error) {
	mode := pgx.ReadWrite
	if readOnly {
		mode = pgx.ReadOnly
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: mode})
	if e != nil {
		return nil, r.ErrUnavailable
	}
	return tx, nil
}
func (s *Source) DescribeCatalog(ctx context.Context, sc r.SourceContext) ([]r.Dataset, error) {
	tx, e := s.begin(ctx, true)
	if e != nil {
		return nil, e
	}
	defer rollback(ctx, tx)
	v, e := loadSource(ctx, tx, sc, false)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, r.ErrUnavailable
	}
	return []r.Dataset{v.Dataset}, nil
}
func (s *Source) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, hash string) (r.PreparedRead, error) {
	tx, e := s.begin(ctx, false)
	if e != nil {
		return r.PreparedRead{}, e
	}
	defer rollback(ctx, tx)
	v, e := loadSource(ctx, tx, sc, true)
	if e != nil {
		return r.PreparedRead{}, e
	}
	if e = r.ValidateQuery(q, v.Dataset); e != nil {
		return r.PreparedRead{}, e
	}
	if q.Integration != sc.Owner.Integration || hash != r.QueryHash(q) {
		return r.PreparedRead{}, r.ErrIntegrity
	}
	for _, c := range q.Companies {
		if !slices.Contains(v.Grant.Companies, c) {
			return r.PreparedRead{}, r.ErrDenied
		}
	}
	if v.Grant.Timezone != q.Timezone || !reflect.DeepEqual(v.Grant.CurrencyPolicy, q.CurrencyPolicy) {
		return r.PreparedRead{}, r.ErrDenied
	}
	p := r.PreparedRead{Context: sc, Ref: r.NewID(), ScopeHandle: r.NewID(), QueryHash: hash, Dataset: q.Dataset, Companies: slices.Clone(q.Companies), Timezone: q.Timezone, CurrencyPolicy: q.CurrencyPolicy, Watermark: "fixture-" + v.Version, ExpiresAt: v.Now.UTC().Truncate(time.Microsecond).Add(10 * time.Minute)}
	state := savedRead{Prepared: p, Query: q, Grant: v.Grant, Version: v.Version}
	b, e := json.Marshal(state)
	if e != nil {
		return r.PreparedRead{}, r.ErrInvalid
	}
	_, e = tx.Exec(ctx, `INSERT INTO reporting_source_s01.prepared(id,tenant,integration,issuer,subject,source_tenant,version_id,expires_at,data,data_sha) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,encode(sha256(convert_to($9::jsonb::text,'UTF8')),'hex'))`, p.Ref, sc.Owner.Tenant, sc.Owner.Integration, sc.Owner.Issuer, sc.Owner.Subject, sc.SourceTenant, v.Version, p.ExpiresAt, b)
	if e != nil {
		return r.PreparedRead{}, r.ErrUnavailable
	}
	if e = tx.Commit(ctx); e != nil {
		return r.PreparedRead{}, r.ErrUnavailable
	}
	return p, nil
}
func authorize(ctx context.Context, tx pgx.Tx, sc r.SourceContext, p r.PreparedRead) (savedRead, error) {
	var saved savedRead
	var raw []byte
	var hash, version string
	var expires time.Time
	e := tx.QueryRow(ctx, `SELECT data::text,data_sha,version_id,expires_at FROM reporting_source_s01.prepared WHERE id=$1 AND tenant=$2 AND integration=$3 AND issuer=$4 AND subject=$5 AND source_tenant=$6`, p.Ref, sc.Owner.Tenant, sc.Owner.Integration, sc.Owner.Issuer, sc.Owner.Subject, sc.SourceTenant).Scan(&raw, &hash, &version, &expires)
	if errors.Is(e, pgx.ErrNoRows) {
		return saved, r.ErrDenied
	}
	if e != nil {
		return saved, r.ErrUnavailable
	}
	if e = fixturedb.Decode(raw, hash, &saved); e != nil {
		return saved, e
	}
	if r.Digest(saved.Prepared) != r.Digest(p) || p.Context != sc || saved.Query.Integration != sc.Owner.Integration || saved.Version != version || p.Watermark != "fixture-"+version || saved.Prepared.QueryHash != r.QueryHash(saved.Query) || !expires.Equal(p.ExpiresAt) {
		return saved, r.ErrIntegrity
	}
	v, e := loadSource(ctx, tx, sc, false)
	if e != nil {
		return saved, e
	}
	if !v.Now.Before(expires) {
		return saved, r.ErrDenied
	}
	if v.Grant.Timezone != saved.Grant.Timezone || !reflect.DeepEqual(v.Grant.CurrencyPolicy, saved.Grant.CurrencyPolicy) {
		return saved, r.ErrDenied
	}
	for _, c := range p.Companies {
		if !slices.Contains(v.Grant.Companies, c) {
			return saved, r.ErrDenied
		}
	}
	for _, row := range saved.Grant.Rows {
		if !slices.Contains(v.Grant.Rows, row) {
			return saved, r.ErrDenied
		}
	}
	return saved, nil
}
func (s *Source) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, p r.PreparedRead) error {
	tx, e := s.begin(ctx, true)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	if _, e = authorize(ctx, tx, sc, p); e != nil {
		return e
	}
	if tx.Commit(ctx) != nil {
		return r.ErrUnavailable
	}
	return nil
}

// loadSeed reads the immutable source version a frozen read was made from.
func loadSeed(ctx context.Context, tx pgx.Tx, sc r.SourceContext, version string) (contractsim.Fixture, error) {
	var seed contractsim.Fixture
	var catalog, records []byte
	var ch, rh string
	e := tx.QueryRow(ctx, "SELECT catalog::text,catalog_sha,records::text,records_sha FROM reporting_source_s01.versions WHERE integration=$1 AND source_tenant=$2 AND id=$3", sc.Owner.Integration, sc.SourceTenant, version).Scan(&catalog, &ch, &records, &rh)
	if errors.Is(e, pgx.ErrNoRows) {
		return seed, r.ErrIntegrity
	}
	if e != nil {
		return seed, r.ErrUnavailable
	}
	if e = fixturedb.Decode(catalog, ch, &seed.Dataset); e != nil {
		return seed, e
	}
	if e = fixturedb.Decode(records, rh, &seed.Records); e != nil {
		return seed, e
	}
	return seed, nil
}
func (s *Source) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	tx, e := s.begin(ctx, true)
	if e != nil {
		return r.SourcePage{}, e
	}
	defer rollback(ctx, tx)
	saved, e := authorize(ctx, tx, sc, p)
	if e != nil {
		return r.SourcePage{}, e
	}
	seed, e := loadSeed(ctx, tx, sc, saved.Version)
	if e != nil {
		return r.SourcePage{}, e
	}
	s.reads.Add(1)
	page, e := contractsim.ReadFrozenPage(ctx, seed, saved.Query, saved.Grant, p, cursor, limit)
	if e != nil {
		return r.SourcePage{}, e
	}
	if tx.Commit(ctx) != nil {
		return r.SourcePage{}, r.ErrUnavailable
	}
	return page, nil
}

var _ r.Adapter = (*Source)(nil)
