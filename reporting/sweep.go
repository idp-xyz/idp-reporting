package reporting

import (
	"fmt"
	"reflect"
	"time"
)

// Retention (S04-B, docs/S04B_SCOPE.md). What the platform stores of a result lives until the result's own expiry (the one every download
// and page already enforces: the expiry of the source read it was made from, or, where the source vouches for the read's scope, the later
// time its class keeps it until, ADR 0026, lifetime.go) plus a grace period; then it is deleted. What is kept for good: the issue records,
// and the rows of the jobs, whose payload is reduced to what their view and the audit need.
const (
	// DefaultSweepGrace is how long after its expiry an asset is still held, so that a download in flight, a worker whose
	// clock lags and an operator who looks for it the next morning find it.
	DefaultSweepGrace = 24 * time.Hour
	// DefaultSweepBatch is how many rows one transaction of a sweep deletes or reduces at most.
	DefaultSweepBatch = 500
	// MaxSweepGrace and MaxSweepBatch bound what an operator can ask for: a grace is a retention period, not a place to overflow
	// an interval, and a batch is a transaction that holds its rows.
	MaxSweepGrace = 10 * 365 * 24 * time.Hour
	MaxSweepBatch = 10000
	// DefaultReconcileInterval is how long a reconciliation settles the object store for the sweeps that run on a schedule (the operator's
	// command, the server's loop) unless they are told otherwise (S25-B1): an hour short of a day, so that a sweep scheduled once a day
	// reconciles every day. With exactly a day, whether today's run finds yesterday's reconciliation a day old would be decided by which
	// of the two took a second longer to get to it, and the bucket would be reconciled every other day. MaxReconcileInterval bounds what
	// they can be told: the reconciliation is what removes a file whose row is gone, so it cannot be put off for good.
	DefaultReconcileInterval = 23 * time.Hour
	MaxReconcileInterval     = 30 * 24 * time.Hour
)

// SweepOptions is what one sweep is asked to do. A zero Grace or Batch is the default (24 hours, 500): there is no way to ask for no
// grace, and a negative or oversized value is ErrInvalid, so that a typo can only keep data longer, never delete it sooner. With
// DryRun nothing is deleted or changed (the ledger of the sweeps apart) and the counts are what the sweep would have done.
//
// Reconcile is how long a reconciliation of the object store settles the bucket for (S25-B1, docs/S25B1_SCOPE.md). The reconciliation
// lists the whole bucket, which holds every file that is kept (ADR 0026: an issued document's for ten years), so a sweep that runs every
// few minutes must not do it every time. With an interval the sweep leaves the bucket alone while it is settled. Two kinds of sweep
// concern the bucket, dry runs apart: one that left objects behind or did not complete, from the moment it ended, and a reconciliation
// that ran to its end and left none, from the moment its listing began. The bucket is settled while the newest of them is such a
// reconciliation and its listing began less than the interval ago, by the store's clock. So a sweep that leaves objects (a delete that
// failed, a sweep given no object store), or that is cut short, is followed by a reconciliation at the next sweep, as it always was;
// what waits for the interval is the orphan of a publication that failed, which no sweep can know of without listing. Zero reconciles
// at every sweep, as every sweep with an object store did before; a negative value or one above MaxReconcileInterval is ErrInvalid.
//
// A reconciliation takes an object no row names only when the object is older than the sweep's grace and than MinOrphanAge. A sweep
// whose grace is under MinOrphanAge can therefore leave an object that its own next reconciliation finds too young, and a
// reconciliation of such a sweep settles nothing: with a grace that short every sweep reconciles, as before.
type SweepOptions struct {
	Grace     time.Duration
	Batch     int
	DryRun    bool
	Reconcile time.Duration
}

// Resolved returns the options with their defaults, or ErrInvalid.
func (o SweepOptions) Resolved() (SweepOptions, error) {
	if o.Grace < 0 || o.Grace > MaxSweepGrace || o.Batch < 0 || o.Batch > MaxSweepBatch {
		return SweepOptions{}, fmt.Errorf("a sweep's grace is 0 (the default) up to %s and its batch 0 (the default) up to %d: %w", MaxSweepGrace, MaxSweepBatch, ErrInvalid)
	}
	if o.Reconcile < 0 || o.Reconcile > MaxReconcileInterval {
		return SweepOptions{}, fmt.Errorf("a sweep reconciles the object store every time (0) or at most every %s: %w", MaxReconcileInterval, ErrInvalid)
	}
	if o.Grace == 0 {
		o.Grace = DefaultSweepGrace
	}
	if o.Batch == 0 {
		o.Batch = DefaultSweepBatch
	}
	return o, nil
}

// SweepResult is what a sweep did, counts only: artifacts and snapshots deleted, job payloads reduced, and the transactions that
// changed something. With DryRun they are what it would have done. A sweep that failed part way returns what it had done.
//
// With an object store (S04-E, docs/S04E_SCOPE.md E3) Objects is the objects deleted after the rows that named them, Orphans the objects the
// reconciliation deleted (no asset row names them and they are older than the grace), and ObjectsLeft the objects of deleted rows that this
// sweep did not delete, a delete that failed or no object store being configured: they wait for a reconciliation. Of a dry run, Orphans
// is how many the reconciliation would delete and Objects how many the rows it would delete name. Reconciled says whether this sweep
// reconciled the object store, to the end of its listing (S25-B1): false without an object store, while the bucket is settled
// (SweepOptions.Reconcile), and when the listing failed. While the bucket is settled Orphans is zero because nothing was looked for,
// not because nothing is there; after a listing that failed it is what was deleted before the failure.
type SweepResult struct {
	DryRun      bool
	Artifacts   int
	Snapshots   int
	Jobs        int
	Batches     int
	Objects     int
	Orphans     int
	ObjectsLeft int
	Reconciled  bool
}

// MinOrphanAge is the least age the reconciliation of the object store accepts for an object that no row names (S04-E): an object is put
// before the row that names it is published, a worker that takes a minute for the two is slow but alive, and deleting that object would leave
// a published row that promises nothing. A sweep's grace below it is raised to it for the reconciliation alone.
const MinOrphanAge = 15 * time.Minute

// Terminal says whether a job state is final.
func Terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

// ReduceJob is the payload a terminal job keeps once its results are gone: the query, the prepared source read and the columns are
// cleared, the request hash and the rest (owner, kind, key, ids, format, template, row or group, an export's presentation) stay. Both
// stores reduce a job to this shape, and ValidateReduced is what reading one checks.
func ReduceJob(j Job) Job {
	j.Query, j.Prepared, j.Columns, j.Reduced = QuerySpec{}, PreparedRead{}, nil, true
	return j
}

// ValidateReduced is ValidateAdmission for a reduced job: the same identity checks, no query or source read to bind, and the request hash
// of an export or an issue job is still the one its frozen fields make.
func ValidateReduced(j Job) error {
	if !j.Reduced || !validID(j.ID) || !j.Owner.Identity.Valid() || j.Owner.Integration == "" || !validKey(j.Key) || j.State != "queued" || j.Generation != 0 || j.RequestHash == "" ||
		!reflect.DeepEqual(j.Query, QuerySpec{}) || !reflect.DeepEqual(j.Prepared, PreparedRead{}) || j.Columns != nil {
		return ErrInvalid
	}
	switch j.Kind {
	case "snapshot":
		if j.SnapshotID != "" || j.Format != "" {
			return ErrInvalid
		}
	case "export":
		if !validID(j.SnapshotID) || !validFormat(j.Format) || !exportFields(j) {
			return ErrInvalid
		}
	case "issue":
		if !validID(j.SnapshotID) || j.Format != "pdf" || !issueFields(j) || j.RequestHash != issueRequestHash(j) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if j.Kind != "issue" && (j.TemplateID != "" || j.TemplateVersion != 0 || j.TemplateSHA256 != "" || j.Template != "" || j.RowIndex != 0 || j.Group != nil) {
		return ErrInvalid
	}
	if j.Kind != "export" && j.Presentation != nil {
		return ErrInvalid
	}
	return nil
}
