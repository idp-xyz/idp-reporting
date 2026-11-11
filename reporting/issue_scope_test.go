package reporting_test

import (
	"context"
	"slices"
	"testing"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// TestAnIssueFromAFilteredSnapshotRecordsThatSnapshot (S20-C, ADR 0023 decision 6): a document template is per row or group and does
// not say the query's scope; the issue record keeps the snapshot's id and digest, as it does for any snapshot, and that snapshot's
// frozen query holds the filters and the sort. The row that is issued is a row of the narrowed, ordered snapshot, and the source
// product checks it against its own read of the same narrowed scope.
func TestAnIssueFromAFilteredSnapshotRecordsThatSnapshot(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	q := contractsim.FinancialQuery()
	q.Filters = []r.Filter{{Field: "customer.id", Operator: "ne", Value: "customer_a"}, {Field: "aging.bucket", Operator: "in", Value: `["1_30","31_60"]`}}
	q.Sort = []r.Sort{{Field: "ar.open_balance", Direction: "desc"}}
	filtered := v.freeze(q, "snap-filtered")
	if filtered == v.snapshot {
		t.Fatal("the filtered query froze the snapshot of the whole dataset")
	}
	snap, e := v.repo.Snapshot(ctx, v.p.Identity, filtered)
	if e != nil {
		t.Fatal(e)
	}
	// The snapshot is the narrowed scope in the order asked (the larger balance first), and its frozen query holds the filters in
	// the normalized order and the sort.
	if rows := snap.Result.Rows; len(rows) != 2 || rows[0][0] != "customer_c" || rows[1][0] != "customer_b" || snap.Result.Totals["ar.open_balance"] != "9007199254740892.76" {
		t.Fatalf("the filtered snapshot: %v, totals %v", rows, snap.Result.Totals)
	}
	wantFilters := []r.Filter{{Field: "aging.bucket", Operator: "in", Value: `["1_30","31_60"]`}, {Field: "customer.id", Operator: "ne", Value: "customer_a"}}
	if !slices.Equal(snap.Query.Filters, wantFilters) || !slices.Equal(snap.Query.Sort, q.Sort) || snap.Prepared.QueryHash != r.QueryHash(q) {
		t.Fatalf("the frozen query: filters %v, sort %v", snap.Query.Filters, snap.Query.Sort)
	}
	// Row 0 of the filtered snapshot is customer_c; row 0 of the whole dataset is customer_a.
	in := v.input(0)
	in.SnapshotID = filtered
	j, e := v.s.SubmitIssue(ctx, v.p, in, "issue-filtered")
	if e != nil || j.SnapshotID != filtered {
		t.Fatal(j, e)
	}
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	if j = v.job(j.ID); j.State != "succeeded" || j.SnapshotID != filtered || j.Issue == nil || j.Issue.Number != "AR-000001" {
		t.Fatalf("the issue job: %+v", j)
	}
	// The product was asked about that row of its narrowed read, and the document was rendered from the filtered snapshot.
	if len(v.finance.intents) != 1 {
		t.Fatalf("%d calls to prepare", len(v.finance.intents))
	}
	if intent := v.finance.intents[0]; intent.RowIndex != 0 || intent.RowSHA256 != r.Digest([]string{"customer_c", "31_60", "9007199254740893.00"}) || intent.Prepared.Ref != snap.Prepared.Ref || intent.Prepared.QueryHash != r.QueryHash(q) {
		t.Fatalf("the intent: %+v", intent)
	}
	if v.issuer.last.Snapshot.ID != filtered || len(v.issuer.last.Snapshot.Query.Filters) != 2 {
		t.Fatalf("the issuer rendered from snapshot %s", v.issuer.last.Snapshot.ID)
	}
	// The record names the filtered snapshot and its digest, as a record names any snapshot; nothing else of it knows of filters.
	rec, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if rec.SnapshotID != filtered || rec.SnapshotSHA256 != snap.SHA256 || rec.RowIndex != 0 || rec.Number != "AR-000001" || rec.Group != nil {
		t.Fatalf("the record: %+v", rec)
	}
	view, e := v.s.Issue(ctx, v.p, j.ID)
	if e != nil || view.SnapshotID != filtered || view.SnapshotSHA256 != snap.SHA256 {
		t.Fatalf("the view: %+v (%v)", view, e)
	}
	// The whole dataset's snapshot is another snapshot: the same row index there is another row, issued under its own record.
	whole := v.submit(0, "issue-whole")
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	other, e := v.repo.Issue(ctx, v.p.Identity, whole.ID)
	if e != nil || other.SnapshotID != v.snapshot || other.SnapshotID == rec.SnapshotID || other.SnapshotSHA256 == rec.SnapshotSHA256 {
		t.Fatalf("the record of the whole dataset's issue: %+v (%v)", other, e)
	}
	if intent := v.finance.intents[1]; intent.RowSHA256 != r.Digest([]string{"customer_a", "current", "100.25"}) {
		t.Fatalf("the second intent: %+v", intent)
	}
}
