package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"strings"
	"time"
)

// Retention (S04-B, docs/S04B_SCOPE.md, docs/OPERATIONS.md "Retention"). The sweep deletes what has expired, keeps what must be kept, and
// is exact about both. Every rule below is in the SQL of the batch that acts on it, evaluated by the database's clock
// (clock_timestamp()), never the platform host's:
//
//   - an artifact or a snapshot is deleted only once its expires_at is before now minus the grace; a row whose expires_at is NULL (no
//     readable expiry) is never deleted, and neither is an issue record, whatever its columns say: the delete itself names the kind;
//   - nothing a queued or running job names is deleted: not the snapshot it reads from (jobs.snapshot_id), not an artifact of that
//     snapshot, not an artifact it names;
//   - an artifact goes only with its snapshot or after it is past the grace too, never while its snapshot is held for a later expiry;
//   - a finished job's payload is reduced only once its own expiry plus the grace is past and neither its snapshot nor its artifact is still
//     stored, and only a payload that is a JSON object whose digest matches (a corrupt payload is not rewritten into a valid-looking one).
//
// Each batch is one statement in one transaction with FOR UPDATE SKIP LOCKED, so a failed batch changes nothing, concurrent sweeps take
// different rows, and a row a worker or a cancel holds is left for the next sweep. It takes no advisory lock.

// cutoff is the first CTE of every statement: the instant before which an expiry is past its grace, by the database's clock. $1 is the grace
// in microseconds.
const cutoff = `c AS MATERIALIZED (SELECT clock_timestamp() - $1::bigint * interval '1 microsecond' AS cutoff)`

// artifactGoes is the condition under which the artifact aliased a (a row of assets, with the CTE c in scope) is deleted.
func artifactGoes(a string) string {
	return strings.ReplaceAll(`@.kind = 'artifact' AND @.expires_at IS NOT NULL AND @.expires_at < c.cutoff
AND jsonb_typeof(@.data) = 'object' AND @.data->>'SnapshotID' IS NOT NULL
AND NOT EXISTS (SELECT 1 FROM reporting_s01.jobs j WHERE j.state IN ('queued','running') AND (j.artifact_id = @.id OR j.snapshot_id = @.data->>'SnapshotID'))
AND NOT EXISTS (SELECT 1 FROM reporting_s01.assets s WHERE s.kind = 'snapshot' AND s.id = @.data->>'SnapshotID' AND (s.expires_at IS NULL OR s.expires_at >= c.cutoff))`, "@", a)
}

// snapshotGoes is the condition under which the snapshot aliased a is deleted.
func snapshotGoes(a string) string {
	return strings.ReplaceAll(`@.kind = 'snapshot' AND @.expires_at IS NOT NULL AND @.expires_at < c.cutoff
AND NOT EXISTS (SELECT 1 FROM reporting_s01.jobs j WHERE j.state IN ('queued','running') AND j.snapshot_id = @.id)`, "@", a)
}

// jobReducible is what a job aliased j must be to have its payload reduced, apart from its results being gone.
const jobReducible = `j.state IN ('succeeded','failed','cancelled') AND j.source_expires_at < c.cutoff
AND jsonb_typeof(j.data) = 'object' AND NOT (j.data ? 'Reduced')
AND j.data_sha256 = encode(sha256(convert_to(j.data::text,'UTF8')),'hex')`

var (
	deleteArtifacts = `WITH ` + cutoff + `, picked AS (
SELECT a.id FROM reporting_s01.assets a, c WHERE ` + artifactGoes("a") + `
ORDER BY a.expires_at, a.id FOR UPDATE OF a SKIP LOCKED LIMIT $2)
DELETE FROM reporting_s01.assets d USING picked WHERE d.id = picked.id AND d.kind = 'artifact'`

	deleteSnapshots = `WITH ` + cutoff + `, picked AS (
SELECT a.id FROM reporting_s01.assets a, c WHERE ` + snapshotGoes("a") + `
ORDER BY a.expires_at, a.id FOR UPDATE OF a SKIP LOCKED LIMIT $2)
DELETE FROM reporting_s01.assets d USING picked WHERE d.id = picked.id AND d.kind = 'snapshot'`

	// The reduction keeps every key of the payload but the frozen read (Query, Prepared, Columns) and marks it; the digest is that of the
	// new canonical text, as every writer of the store makes it.
	reduceJobs = `WITH ` + cutoff + `, picked AS (
SELECT j.id FROM reporting_s01.jobs j, c WHERE ` + jobReducible + `
AND (j.snapshot_id = '' OR NOT EXISTS (SELECT 1 FROM reporting_s01.assets s WHERE s.id = j.snapshot_id))
AND (j.artifact_id = '' OR NOT EXISTS (SELECT 1 FROM reporting_s01.assets a WHERE a.id = j.artifact_id))
ORDER BY j.created_at, j.id FOR UPDATE OF j SKIP LOCKED LIMIT $2),
reduced AS (SELECT j.id, (j.data - 'Query' - 'Prepared' - 'Columns') || '{"Reduced": true}'::jsonb AS d FROM reporting_s01.jobs j JOIN picked ON picked.id = j.id)
UPDATE reporting_s01.jobs u SET data = reduced.d, data_sha256 = encode(sha256(convert_to(reduced.d::text,'UTF8')),'hex') FROM reduced WHERE u.id = reduced.id`

	// A dry run counts what the three steps would do in turn: the jobs whose assets are still stored count as reduced when those assets
	// are among the ones the sweep deletes.
	countSweep = `WITH ` + cutoff + `
SELECT (SELECT count(*) FROM reporting_s01.assets a, c WHERE ` + artifactGoes("a") + `),
(SELECT count(*) FROM reporting_s01.assets a, c WHERE ` + snapshotGoes("a") + `),
(SELECT count(*) FROM reporting_s01.jobs j, c WHERE ` + jobReducible + `
AND (j.snapshot_id = '' OR NOT EXISTS (SELECT 1 FROM reporting_s01.assets s, c WHERE s.id = j.snapshot_id AND NOT COALESCE(` + snapshotGoes("s") + `, false)))
AND (j.artifact_id = '' OR NOT EXISTS (SELECT 1 FROM reporting_s01.assets a, c WHERE a.id = j.artifact_id AND NOT COALESCE(` + artifactGoes("a") + `, false))))`
)

const sweepLockTimeout = "10s"

// Sweep runs one retention sweep on the store (see the comment of this file and r.SweepOptions). The connected role needs DELETE on
// and UPDATE on assets (the row locks), UPDATE on jobs and INSERT on the sweeps ledger (the sweep role of `reporting-migrate roles`, or the
// migrator); for a dry run, INSERT on the ledger alone. It refuses a store that is not at the binary's schema version. Whatever happens, a ledger row records the counts of
// what was done, and the result carries them (also with the error of a sweep that failed part way).
func Sweep(ctx context.Context, pool *pgxpool.Pool, o r.SweepOptions) (r.SweepResult, error) {
	o, e := o.Resolved()
	res := r.SweepResult{DryRun: o.DryRun}
	if e != nil {
		return res, e
	}
	if e = sweepable(ctx, pool, o.DryRun); e != nil {
		return res, e
	}
	grace := o.Grace.Microseconds()
	complete := false
	if o.DryRun {
		// A dry run reads the rows of every identity, so it too is a transaction of the worker scope (S04-C).
		e = asWorker(ctx, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, countSweep, grace).Scan(&res.Artifacts, &res.Snapshots, &res.Jobs)
		})
		complete = e == nil
	} else {
		for _, step := range []struct {
			sql   string
			count *int
		}{{deleteArtifacts, &res.Artifacts}, {deleteSnapshots, &res.Snapshots}, {reduceJobs, &res.Jobs}} {
			if e = batches(ctx, pool, step.sql, grace, o.Batch, step.count, &res.Batches); e != nil {
				break
			}
		}
		complete = e == nil
	}
	// The ledger row is written even when the sweep was cut short (a cancelled context, a failed batch): what it already deleted is audit.
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, le := pool.Exec(record, `INSERT INTO reporting_s01.sweeps(dry_run, complete, grace_seconds, batch, artifacts, snapshots, jobs, batches) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		o.DryRun, complete, int64(o.Grace/time.Second), o.Batch, res.Artifacts, res.Snapshots, res.Jobs, res.Batches); le != nil && e == nil {
		e = fmt.Errorf("the sweep ran but its ledger row could not be written: %w", le)
	}
	return res, e
}

// batches runs one statement in transactions of at most limit rows each until a batch is short, adding the rows it changed to count.
func batches(ctx context.Context, pool *pgxpool.Pool, sql string, grace int64, limit int, count, batchCount *int) error {
	for {
		n, e := batch(ctx, pool, sql, grace, limit)
		if e != nil {
			return e
		}
		*count += n
		if n > 0 {
			*batchCount++
		}
		if n < limit {
			return nil
		}
	}
}

// batch is one transaction: the statement acts on at most limit rows, or on none.
func batch(ctx context.Context, pool *pgxpool.Pool, sql string, grace int64, limit int) (int, error) {
	// The batch works on the rows of every identity: it is a transaction of the worker scope (S04-C), which row-level security needs.
	tx, e := beginWorker(ctx, pool)
	if e != nil {
		return 0, e
	}
	defer rollback(ctx, tx)
	// A sweep never waits long on a lock (a migration holding the table, say): it fails and the next one goes on.
	if _, e = tx.Exec(ctx, "SET LOCAL lock_timeout = '"+sweepLockTimeout+"'"); e != nil {
		return 0, e
	}
	tag, e := tx.Exec(ctx, sql, grace, limit)
	if e != nil {
		return 0, e
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	return int(tag.RowsAffected()), nil
}

// sweepable refuses a store the sweep cannot run on, and a role that lacks what the sweep needs, before anything is changed.
func sweepable(ctx context.Context, pool *pgxpool.Pool, dryRun bool) error {
	st, e := Status(ctx, pool)
	if e != nil {
		return e
	}
	if len(st.Problems) > 0 {
		return fmt.Errorf("schema ledger: %s: %w", strings.Join(st.Problems, "; "), r.ErrIntegrity)
	}
	if st.Legacy || st.Current() != st.Latest() {
		return fmt.Errorf("schema at version %d, the binary needs %d (run reporting-migrate up): %w", st.Current(), st.Latest(), r.ErrDenied)
	}
	var del, lock, upd, ins bool
	if e = pool.QueryRow(ctx, `SELECT has_table_privilege(current_user,'reporting_s01.assets','DELETE'), has_table_privilege(current_user,'reporting_s01.assets','UPDATE'),
has_table_privilege(current_user,'reporting_s01.jobs','UPDATE'), has_table_privilege(current_user,'reporting_s01.sweeps','INSERT')`).Scan(&del, &lock, &upd, &ins); e != nil {
		return e
	}
	var missing []string
	if !dryRun && !del {
		missing = append(missing, "DELETE on assets")
	}
	if !dryRun && !lock {
		missing = append(missing, "UPDATE on assets (row locks)")
	}
	if !dryRun && !upd {
		missing = append(missing, "UPDATE on jobs")
	}
	if !ins {
		missing = append(missing, "INSERT on sweeps")
	}
	if len(missing) > 0 {
		return fmt.Errorf("the connected role cannot sweep, it lacks %s (the application role never deletes: run the sweep as the sweep role or the migrator, see reporting-migrate roles --sweep-role): %w", strings.Join(missing, ", "), r.ErrDenied)
	}
	return nil
}

// Sweep is r.Repository's retention on the durable store.
func (s *Store) Sweep(ctx context.Context, o r.SweepOptions) (r.SweepResult, error) {
	return Sweep(ctx, s.pool, o)
}
