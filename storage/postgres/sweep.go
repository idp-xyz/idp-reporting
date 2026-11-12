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

// artifactFrom is the FROM list of a statement that tests artifactGoes on the asset aliased a: the asset, the cutoff CTE c and, as a column of
// its own, the snapshot id the artifact's payload names. The id is computed in a subquery that PostgreSQL cannot pull up (OFFSET 0), so that the
// probe of the snapshot is a comparison of two plain columns, which the planner may answer from the primary key. As an expression on the payload
// (a.data->>'SnapshotID', a function that is not leakproof) it cannot be used as an index condition on assets under the row-level policies of
// S04-C, and the probe was a scan of every snapshot not yet past its grace for every candidate artifact (S04-G, docs/CAPACITY.md).
func artifactFrom(a string) string {
	return strings.ReplaceAll(`reporting_s01.assets @, c, LATERAL (SELECT @.data->>'SnapshotID' AS sid OFFSET 0) @_snap`, "@", a)
}

// artifactGoes is the condition under which the artifact aliased a (a row of artifactFrom(a)) is deleted.
func artifactGoes(a string) string {
	return strings.ReplaceAll(`@.kind = 'artifact' AND @.expires_at IS NOT NULL AND @.expires_at < c.cutoff
AND jsonb_typeof(@.data) = 'object' AND @_snap.sid IS NOT NULL
AND NOT EXISTS (SELECT 1 FROM reporting_s01.jobs j WHERE j.state IN ('queued','running') AND (j.artifact_id = @.id OR j.snapshot_id = @_snap.sid))
AND NOT EXISTS (SELECT 1 FROM reporting_s01.assets s WHERE s.kind = 'snapshot' AND s.id = @_snap.sid AND (s.expires_at IS NULL OR s.expires_at >= c.cutoff))`, "@", a)
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
SELECT a.id FROM ` + artifactFrom("a") + ` WHERE ` + artifactGoes("a") + `
ORDER BY a.expires_at, a.id FOR UPDATE OF a SKIP LOCKED LIMIT $2)
DELETE FROM reporting_s01.assets d USING picked WHERE d.id = picked.id AND d.kind = 'artifact'
RETURNING d.id, coalesce(d.data->>'Object', '')`

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
SELECT (SELECT count(*) FROM ` + artifactFrom("a") + ` WHERE ` + artifactGoes("a") + `),
(SELECT count(*) FROM ` + artifactFrom("a") + ` WHERE ` + artifactGoes("a") + ` AND a.data ? 'Object'),
(SELECT count(*) FROM reporting_s01.assets a, c WHERE ` + snapshotGoes("a") + `),
(SELECT count(*) FROM reporting_s01.jobs j, c WHERE ` + jobReducible + `
AND (j.snapshot_id = '' OR NOT EXISTS (SELECT 1 FROM reporting_s01.assets s, c WHERE s.id = j.snapshot_id AND NOT COALESCE(` + snapshotGoes("s") + `, false)))
AND (j.artifact_id = '' OR NOT EXISTS (SELECT 1 FROM ` + artifactFrom("a") + ` WHERE a.id = j.artifact_id AND NOT COALESCE(` + artifactGoes("a") + `, false))))`
)

const sweepLockTimeout = "10s"

// Sweep runs one retention sweep on the store (see the comment of this file and r.SweepOptions) on its rows alone: the objects the deleted
// artifacts named are not deleted (SweepWithObjects does that); they wait for a reconciliation, which the result counts as ObjectsLeft. The
// connected role needs DELETE on and UPDATE on assets (the row locks), UPDATE on jobs and INSERT on the sweeps ledger (the sweep role of
// `reporting-migrate roles`, or the migrator); for a dry run, INSERT on the ledger alone. It refuses a store that is not at the binary's schema
// version. Whatever happens, a ledger row records the counts of what was done, and the result carries them (also with the error of a sweep that
// failed part way).
func Sweep(ctx context.Context, pool *pgxpool.Pool, o r.SweepOptions) (r.SweepResult, error) {
	return sweep(ctx, pool, o, nil, nil)
}

// sweep is Sweep with the object store (S04-E): after each batch of artifact rows commits, the objects those rows named are deleted, and a
// fourth step reconciles the bucket against the rows (objects.go). now is the clock by which an object's age is judged.
func sweep(ctx context.Context, pool *pgxpool.Pool, o r.SweepOptions, objects r.ObjectStore, now func() time.Time) (r.SweepResult, error) {
	o, e := o.Resolved()
	res := r.SweepResult{DryRun: o.DryRun}
	if e != nil {
		return res, e
	}
	if e = sweepable(ctx, pool, o.DryRun, objects != nil && o.Reconcile > 0); e != nil {
		return res, e
	}
	grace := o.Grace.Microseconds()
	complete := false
	left := map[string]bool{} // the keys this sweep meant to delete and could not: the reconciliation does not count them twice
	var began time.Time       // when this sweep's listing of the bucket began, by the database's clock, if it reconciled
	if o.DryRun {
		// A dry run reads the rows of every identity, so it too is a transaction of the worker scope (S04-C).
		var named int
		e = asWorker(ctx, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, countSweep, grace).Scan(&res.Artifacts, &named, &res.Snapshots, &res.Jobs)
		})
		if objects != nil {
			res.Objects = named
		} else {
			res.ObjectsLeft = named
		}
		if e == nil && objects != nil {
			_, e = reconcileWhenDue(ctx, pool, objects, now, o, left, &res)
		}
		complete = e == nil
	} else {
		if e = artifactBatches(ctx, pool, grace, o.Batch, objects, left, &res); e == nil {
			for _, step := range []struct {
				sql   string
				count *int
			}{{deleteSnapshots, &res.Snapshots}, {reduceJobs, &res.Jobs}} {
				if e = batches(ctx, pool, step.sql, grace, o.Batch, step.count, &res.Batches); e != nil {
					break
				}
			}
		}
		if e == nil && objects != nil {
			began, e = reconcileWhenDue(ctx, pool, objects, now, o, left, &res)
		}
		complete = e == nil
	}
	// The ledger row is written even when the sweep was cut short (a cancelled context, a failed batch): what it already deleted is audit.
	// It says whether the sweep reconciled the object store and how many objects it left behind (S25-B1), and a reconciliation that left
	// none says since when the bucket is settled: the next sweeps read it to know whether the bucket is due (bucketSettled).
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, le := pool.Exec(record, `INSERT INTO reporting_s01.sweeps(dry_run, complete, grace_seconds, batch, artifacts, snapshots, jobs, batches, objects, orphans, reconciled, objects_left, settled_from) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		o.DryRun, complete, int64(o.Grace/time.Second), o.Batch, res.Artifacts, res.Snapshots, res.Jobs, res.Batches, res.Objects, res.Orphans, res.Reconciled, res.ObjectsLeft, settledFrom(res, complete, o.Grace, began)); le != nil {
		e = unrecorded(e, le)
	}
	return res, e
}

// unrecorded is the error of a sweep whose ledger row could not be written (unwritten is why), after the sweep itself ended with swept.
// Such a sweep has told the next sweeps nothing of what it left behind, so the caller hears of it also when the sweep had failed already:
// the error is then still the sweep's own, with the ledger's added to what it says.
func unrecorded(swept, unwritten error) error {
	if swept == nil {
		return fmt.Errorf("the sweep ran but its ledger row could not be written: %w", unwritten)
	}
	return fmt.Errorf("%w (and its ledger row could not be written: %v)", swept, unwritten)
}

// settledFrom is what a sweep's ledger row says of the bucket being settled: of a sweep that ran to its end, reconciled the bucket and left
// no object behind, the time its listing began; of every other sweep, and of every dry run, nothing. Nor does a sweep whose grace is
// under r.MinOrphanAge say it: the reconciliation takes an object no row names only when it is older than that floor, so an object such
// a sweep left may have been too young for it, and with so short a grace every sweep reconciles, as before S25-B1.
func settledFrom(res r.SweepResult, complete bool, grace time.Duration, began time.Time) *time.Time {
	if res.DryRun || !complete || !res.Reconciled || res.ObjectsLeft != 0 || grace < r.MinOrphanAge {
		return nil
	}
	return &began
}

// artifactBatches deletes the expired artifacts in batches, as batches does, and after each batch commits deletes the objects the rows named
// (ADR 0007 decision 4: the row first, then the object). The keys come from the batch's RETURNING; one that is not the key of its row's own id
// is not ours to delete and is left alone. A delete that fails leaves the object for the reconciliation, and is counted, not relayed. Without an
// object store the objects are left, and counted so.
func artifactBatches(ctx context.Context, pool *pgxpool.Pool, grace int64, limit int, objects r.ObjectStore, left map[string]bool, res *r.SweepResult) error {
	for {
		n, keys, e := artifactBatch(ctx, pool, grace, limit)
		if e != nil {
			return e
		}
		res.Artifacts += n
		if n > 0 {
			res.Batches++
		}
		for _, key := range keys {
			switch {
			case objects == nil:
				res.ObjectsLeft++
			case objects.Delete(ctx, key) != nil:
				res.ObjectsLeft++
				left[key] = true
			default:
				res.Objects++
			}
		}
		if n < limit {
			return nil
		}
	}
}

// artifactBatch is one transaction of the artifact step: the rows deleted, and the keys of the objects they named.
func artifactBatch(ctx context.Context, pool *pgxpool.Pool, grace int64, limit int) (int, []string, error) {
	tx, e := beginWorker(ctx, pool)
	if e != nil {
		return 0, nil, e
	}
	defer rollback(ctx, tx)
	if _, e = tx.Exec(ctx, "SET LOCAL lock_timeout = '"+sweepLockTimeout+"'"); e != nil {
		return 0, nil, e
	}
	rows, e := tx.Query(ctx, deleteArtifacts, grace, limit)
	if e != nil {
		return 0, nil, e
	}
	n := 0
	var keys []string
	for rows.Next() {
		var id, key string
		if e = rows.Scan(&id, &key); e != nil {
			rows.Close()
			return 0, nil, e
		}
		n++
		if key != "" && key == r.ObjectKey(id) {
			keys = append(keys, key)
		}
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return 0, nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, nil, e
	}
	return n, keys, nil
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

// sweepable refuses a store the sweep cannot run on, and a role that lacks what the sweep needs, before anything is changed. readsLedger
// says that the sweep will read the ledger of the sweeps to know when the object store was last reconciled (S25-B1).
func sweepable(ctx context.Context, pool *pgxpool.Pool, dryRun, readsLedger bool) error {
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
	var del, lock, upd, ins, sel bool
	if e = pool.QueryRow(ctx, `SELECT has_table_privilege(current_user,'reporting_s01.assets','DELETE'), has_table_privilege(current_user,'reporting_s01.assets','UPDATE'),
has_table_privilege(current_user,'reporting_s01.jobs','UPDATE'), has_table_privilege(current_user,'reporting_s01.sweeps','INSERT'),
has_table_privilege(current_user,'reporting_s01.sweeps','SELECT')`).Scan(&del, &lock, &upd, &ins, &sel); e != nil {
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
	if readsLedger && !sel {
		missing = append(missing, "SELECT on sweeps (when the object store was last reconciled)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("the connected role cannot sweep, it lacks %s (the application role never deletes: run the sweep as the sweep role or the migrator, see reporting-migrate roles --sweep-role): %w", strings.Join(missing, ", "), r.ErrDenied)
	}
	return nil
}

// Sweep is r.Repository's retention on the durable store.
func (s *Store) Sweep(ctx context.Context, o r.SweepOptions) (r.SweepResult, error) {
	return sweep(ctx, s.pool, o, s.objects, s.objectNow)
}

// SweepStatement is one of the statements of the retention sweep, as it is run: Args are its arguments for a grace in microseconds and a batch.
type SweepStatement struct {
	Name string
	SQL  string
	Args func(graceMicros int64, batch int) []any
}

// SweepStatementsForTests returns the sweep's statements for the plan test and the measurement tool (S04-G). Nothing in the platform runs them
// through this.
func SweepStatementsForTests() []SweepStatement {
	two := func(g int64, b int) []any { return []any{g, b} }
	return []SweepStatement{
		{"delete artifacts", deleteArtifacts, two}, {"delete snapshots", deleteSnapshots, two}, {"reduce jobs", reduceJobs, two},
		{"dry run count", countSweep, func(g int64, _ int) []any { return []any{g} }},
	}
}
