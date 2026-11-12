package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"time"
)

// Reconciliation of the object store (S04-E, docs/ADR_0007_OBJECT_STORAGE.md decision 3). The worker puts an artifact's object before the row
// that names it is published, so an object whose transaction failed is an orphan: nothing names it. The sweep lists the bucket's `artifacts/`
// prefix and deletes the objects that no row of `assets` (any kind) names and that are older than the grace.
//
// Age is judged by the platform host's clock against the time the object store stamped on the object. It is the one place the host's clock decides
// anything in the sweep, because the clock that stamped the object is the object store's, not the database's; the rows' expiries stay the
// database's (sweep.go reads no clock). A host whose clock is far ahead of the object store's would take young objects for old, which is why
// r.MinOrphanAge floors the age: an object younger than that is never deleted, whatever grace is asked for.
//
// The lookup is by id with `= ANY($1)` in a transaction of the worker scope (S04-C), so row-level security shows it every identity's rows.

// SweepWithObjects is Sweep with the object store: after each batch of artifact rows commits it deletes the objects those rows named, and it ends
// with the reconciliation. now is the clock by which the age of an object is judged (nil is time.Now). The result counts Objects, Orphans and
// ObjectsLeft as r.SweepResult says, and the sweeps ledger records the first two.
func SweepWithObjects(ctx context.Context, pool *pgxpool.Pool, o r.SweepOptions, objects r.ObjectStore, now func() time.Time) (r.SweepResult, error) {
	if objects == nil {
		return Sweep(ctx, pool, o)
	}
	return sweep(ctx, pool, o, objects, now)
}

// listPage is how many keys one listing asks for, and so how many ids one lookup names.
const listPage = 1000

// bucketSettled says whether the object store need not be reconciled now (S25-B1). The ledger of the sweeps holds two kinds of entry that
// concern the bucket, dry runs apart. A sweep that left objects behind, or that did not complete, counts from the time it ended (at): a
// sweep that was cut short or failed cannot say what it left, because a batch whose commit was not answered may have deleted its rows.
// A reconciliation that ran to its end and left nothing counts from the time its listing began (settled_from): a listing knows the bucket
// as it was when it began. The bucket is settled when the newest of these entries is such a reconciliation and it began within the last
// $1 microseconds, by the database's clock, which wrote both times. So a sweep that left objects (a delete that failed, or a sweep given
// no object store) makes the next sweep reconcile whatever the interval, as every sweep did before, also when a reconciliation that began
// before it ended after it; and so does a ledger that records no reconciliation at all. The partial index sweeps_reconciliation
// (migration kept_results) holds those entries alone, in this order, so the answer costs the same however many sweeps the ledger records.
const bucketSettled = `SELECT COALESCE((SELECT settled_from > clock_timestamp() - $1::bigint * interval '1 microsecond'
FROM reporting_s01.sweeps WHERE NOT dry_run AND (settled_from IS NOT NULL OR objects_left > 0 OR NOT complete) ORDER BY COALESCE(settled_from, at) DESC, id DESC LIMIT 1), false)`

// BucketSettledForTests returns the statement by which a sweep reads whether the object store is due for a reconciliation, exactly as it
// runs it, for the test that holds its plan to its index. Nothing in the platform runs it through this.
func BucketSettledForTests() string { return bucketSettled }

// reconcileWhenDue reconciles the object store unless the sweep's options put it off (r.SweepOptions.Reconcile, S25-B1). The listing of the
// bucket is the one step of a sweep whose cost is what the bucket holds and not what the sweep handles, and with kept results (ADR 0026)
// the bucket holds the issued documents of ten years. With an interval, the sweep reconciles only when the ledger does not say the
// bucket is settled (bucketSettled); every process that sweeps the store reads the same ledger, the operator's command included, so how
// often the bucket is listed does not grow with how many of them run or how often. A sweep that finds the bucket due while another is
// still listing it lists it too (the ledger says nothing of a reconciliation before it ends): that is what every sweep did before, and
// it deletes nothing twice. A reconciliation that ran to the end of its listing is said in the result, and began is then the database's
// time at which the listing began; one that failed is not said, and the next sweep tries again.
func reconcileWhenDue(ctx context.Context, pool *pgxpool.Pool, objects r.ObjectStore, now func() time.Time, o r.SweepOptions, left map[string]bool, res *r.SweepResult) (began time.Time, err error) {
	if o.Reconcile > 0 {
		var settled bool
		if e := pool.QueryRow(ctx, bucketSettled, o.Reconcile.Microseconds()).Scan(&settled); e != nil {
			return began, e
		}
		if settled {
			return began, nil
		}
	}
	// The clock that stamps the ledger says when the listing began: what another sweep leaves from now on, this listing may have passed.
	if e := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&began); e != nil {
		return began, e
	}
	if e := reconcile(ctx, pool, objects, now, o, left, res); e != nil {
		return began, e
	}
	res.Reconciled = true
	return began, nil
}

func reconcile(ctx context.Context, pool *pgxpool.Pool, objects r.ObjectStore, now func() time.Time, o r.SweepOptions, left map[string]bool, res *r.SweepResult) error {
	if now == nil {
		now = time.Now
	}
	age := max(o.Grace, r.MinOrphanAge)
	cutoff := now().Add(-age)
	after := ""
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		page, next, e := objects.List(ctx, r.ObjectPrefix, after, listPage)
		if e != nil {
			return e
		}
		// Only what is ours and old enough can be an orphan: a key that is not ObjectKey of an id is another process's, and an object stamped
		// at or after the cutoff may be one whose row is about to be published. An object this sweep has just failed to delete is already
		// counted as left, and is not tried (and counted) again.
		old := map[string]string{}
		var ids []string
		for _, obj := range page {
			if id, ok := r.ObjectID(obj.Key); ok && obj.LastModified.Before(cutoff) && !left[obj.Key] {
				old[id] = obj.Key
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			named := map[string]bool{}
			e = asWorker(ctx, pool, func(tx pgx.Tx) error {
				rows, err := tx.Query(ctx, `SELECT id FROM reporting_s01.assets WHERE id = ANY($1)`, ids)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var id string
					if err = rows.Scan(&id); err != nil {
						return err
					}
					named[id] = true
				}
				return rows.Err()
			})
			if e != nil {
				return e
			}
			for _, id := range ids {
				switch {
				case named[id]:
				case o.DryRun:
					res.Orphans++
				case objects.Delete(ctx, old[id]) != nil:
					res.ObjectsLeft++
				default:
					res.Orphans++
				}
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}
