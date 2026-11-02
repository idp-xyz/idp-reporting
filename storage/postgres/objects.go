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
