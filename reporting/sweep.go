package reporting

import (
	"fmt"
	"reflect"
	"time"
)

// Retention (S04-B, docs/S04B_SCOPE.md). What the platform stores of a result lives as long as the source read it was made
// from (the expiry every download and page already enforces) plus a grace period; then it is deleted. What is kept for good: the issue
// records, and the rows of the jobs, whose payload is reduced to what their view and the audit need.
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
)

// SweepOptions is what one sweep is asked to do. A zero Grace or Batch is the default (24 hours, 500): there is no way to ask for no
// grace, and a negative or oversized value is ErrInvalid, so that a typo can only keep data longer, never delete it sooner. With
// DryRun nothing is deleted or changed (the ledger of the sweeps apart) and the counts are what the sweep would have done.
type SweepOptions struct {
	Grace  time.Duration
	Batch  int
	DryRun bool
}

// Resolved returns the options with their defaults, or ErrInvalid.
func (o SweepOptions) Resolved() (SweepOptions, error) {
	if o.Grace < 0 || o.Grace > MaxSweepGrace || o.Batch < 0 || o.Batch > MaxSweepBatch {
		return SweepOptions{}, fmt.Errorf("a sweep's grace is 0 (the default) up to %s and its batch 0 (the default) up to %d: %w", MaxSweepGrace, MaxSweepBatch, ErrInvalid)
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
type SweepResult struct {
	DryRun    bool
	Artifacts int
	Snapshots int
	Jobs      int
	Batches   int
}

// Terminal says whether a job state is final.
func Terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

// ReduceJob is the payload a terminal job keeps once its results are gone: the query, the prepared source read and the columns are
// cleared, the request hash and the rest (owner, kind, key, ids, format, template, row or group) stay. Both stores reduce a job to this
// shape, and ValidateReduced is what reading one checks.
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
		if !validID(j.SnapshotID) || !validFormat(j.Format) || j.RequestHash != ExportRequestHash(j.SnapshotID, j.Format) || j.TemplateID != "" || j.Template != "" {
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
	return nil
}
