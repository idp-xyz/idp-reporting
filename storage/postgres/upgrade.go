package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
)

// upgradeFixture is an explicit protected test-fixture v1→v2 transition. It does
// not qualify production upgrades. Existing source/asset identities are retained.
func upgradeFixture(ctx context.Context, pool *pgxpool.Pool) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { cleanup, cancel := fixturedb.RollbackContext(ctx); defer cancel(); _ = tx.Rollback(cleanup) }()
	var name string
	var version int
	if e = tx.QueryRow(ctx, "SELECT profile,version FROM reporting_s01.profile WHERE singleton FOR UPDATE").Scan(&name, &version); e != nil {
		return e
	}
	if name != "contract-simulator-only" {
		return r.ErrDenied
	}
	if version == 2 {
		return tx.Commit(ctx)
	}
	if version != 1 {
		return r.ErrDenied
	}
	_, e = tx.Exec(ctx, `ALTER TABLE reporting_s01.jobs ADD COLUMN IF NOT EXISTS data_sha256 text;
ALTER TABLE reporting_s01.assets ADD COLUMN IF NOT EXISTS data_sha256 text;
UPDATE reporting_s01.jobs SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex');
UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex');
ALTER TABLE reporting_s01.jobs ALTER COLUMN data_sha256 SET NOT NULL;
ALTER TABLE reporting_s01.assets ALTER COLUMN data_sha256 SET NOT NULL;
UPDATE reporting_s01.profile SET version=2 WHERE singleton;`)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
