package reporting_test

import (
	"context"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"reflect"
	"testing"
	"time"
)

// SweepOptions: zero is the default, never "none"; a negative or oversized value is refused, so that a slip can only keep data longer.
func TestSweepOptionsDefaultsAndBounds(t *testing.T) {
	// The interval of the reconciliation (S25-B1) has no default of its own: zero reconciles at every sweep, as before it existed.
	got, e := r.SweepOptions{}.Resolved()
	if e != nil || got.Grace != 24*time.Hour || got.Batch != 500 || got.DryRun || got.Reconcile != 0 {
		t.Fatal(got, e)
	}
	got, e = r.SweepOptions{Grace: time.Minute, Batch: 3, DryRun: true, Reconcile: time.Hour}.Resolved()
	if e != nil || got.Grace != time.Minute || got.Batch != 3 || !got.DryRun || got.Reconcile != time.Hour {
		t.Fatal(got, e)
	}
	for name, o := range map[string]r.SweepOptions{
		"negative grace": {Grace: -1}, "negative batch": {Batch: -1}, "oversized grace": {Grace: r.MaxSweepGrace + 1}, "oversized batch": {Batch: r.MaxSweepBatch + 1},
		// The reconciliation removes the files whose rows are gone: it cannot be put off for good.
		"a negative interval": {Reconcile: -1}, "an interval beyond thirty days": {Reconcile: r.MaxReconcileInterval + 1},
	} {
		if _, e := o.Resolved(); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s: %v", name, e)
		}
	}
	if _, e := (r.SweepOptions{Grace: r.MaxSweepGrace, Batch: r.MaxSweepBatch, Reconcile: r.MaxReconcileInterval}).Resolved(); e != nil {
		t.Fatal("the bounds themselves are allowed", e)
	}
	// An hour short of a day, so that a sweep scheduled once a day reconciles every day and not every other day.
	if r.DefaultReconcileInterval != 23*time.Hour || r.DefaultReconcileInterval >= 24*time.Hour || r.MaxReconcileInterval != 30*24*time.Hour {
		t.Fatal("the sweeps that run on a schedule reconcile once a day unless told otherwise, and at least every thirty days")
	}
	// A service refuses the same options before it asks the store.
	v := newIssueEnv(t, envOpts{})
	if _, e := v.s.Sweep(context.Background(), r.SweepOptions{Grace: -time.Hour}); !errors.Is(e, r.ErrInvalid) {
		t.Fatal(e)
	}
}

// A reduced job is held to its own shape: the job as admitted without its frozen read, marked, still bound to its request hash where the
// hash can be recomputed. Anything else, and a job that is marked reduced at admission, is refused.
func TestReducedJobsAreValidatedAndAdmissionRefusesThem(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	snapJob, e := v.s.SubmitSnapshot(ctx, v.p, snapshotQuery2(), "second")
	if e != nil {
		t.Fatal(e)
	}
	exportJob, e := v.s.SubmitExport(ctx, v.p, v.snapshot, "export")
	if e != nil {
		t.Fatal(e)
	}
	issueJob := v.submit(1, "issue")
	for name, id := range map[string]string{"snapshot": snapJob.ID, "export": exportJob.ID, "issue": issueJob.ID} {
		stored := v.stored(id)
		if e := r.ValidateAdmission(stored); e != nil {
			t.Fatal(name, "as admitted", e)
		}
		reduced := r.ReduceJob(stored)
		if !reduced.Reduced || !reflect.DeepEqual(reduced.Query, r.QuerySpec{}) || !reflect.DeepEqual(reduced.Prepared, r.PreparedRead{}) || reduced.Columns != nil || reduced.RequestHash != stored.RequestHash || reduced.Template != stored.Template || reduced.RowIndex != stored.RowIndex {
			t.Fatalf("%s reduced: %+v", name, reduced)
		}
		if e := r.ValidateReduced(reduced); e != nil {
			t.Fatal(name, "reduced is not valid:", e)
		}
		if e := r.ValidateAdmission(reduced); !errors.Is(e, r.ErrInvalid) {
			t.Fatal(name, "a reduced job was admitted", e)
		}
		if e := r.ValidateReduced(stored); !errors.Is(e, r.ErrInvalid) {
			t.Fatal(name, "an unreduced job passed as reduced", e)
		}
		for what, change := range map[string]func(*r.Job){
			"a query kept":       func(j *r.Job) { j.Query.Companies = []string{"company_demo"} },
			"a source read kept": func(j *r.Job) { j.Prepared.Ref = "ref" },
			"columns kept":       func(j *r.Job) { j.Columns = []r.Column{{ID: "c"}} },
			"no hash":            func(j *r.Job) { j.RequestHash = "" },
			"another kind":       func(j *r.Job) { j.Kind = "other" },
			"a claimed job":      func(j *r.Job) { j.Generation = 1 },
			"another state":      func(j *r.Job) { j.State = "succeeded" },
		} {
			bad := reduced
			change(&bad)
			if e := r.ValidateReduced(bad); !errors.Is(e, r.ErrInvalid) {
				t.Errorf("%s, %s: %v", name, what, e)
			}
		}
		switch name {
		case "export", "issue":
			bad := reduced
			bad.SnapshotID = r.NewID()
			if e := r.ValidateReduced(bad); !errors.Is(e, r.ErrInvalid) {
				t.Errorf("%s with another snapshot: the request hash does not bind it any more (%v)", name, e)
			}
		}
		if name == "issue" {
			bad := reduced
			bad.RowIndex = 2
			if e := r.ValidateReduced(bad); !errors.Is(e, r.ErrInvalid) {
				t.Errorf("an issue job with another row: %v", e)
			}
		}
	}
}

// snapshotQuery2 is another valid query, so that its key is another job.
func snapshotQuery2() r.QuerySpec {
	q := contractsim.FinancialQuery()
	q.AsOf = "2026-10-25"
	return q
}
