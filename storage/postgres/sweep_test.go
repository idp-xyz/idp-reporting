package postgres

import (
	"errors"
	"fmt"
	r "idp.local/reporting/reporting"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The sweep's clock is the database's: every comparison of an expiry is made in SQL against clock_timestamp(), and the Go code never reads the
// platform host's clock to decide anything (the only time it uses is a timeout for writing the ledger row of a sweep that was cut short).
// A host whose clock is wrong must not delete what the database says is valid; no test can move the database's clock away from the test
// host's, so the source is held to it.
func TestSweepNeverDecidesByTheHostsClock(t *testing.T) {
	src, err := os.ReadFile("sweep.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(src), "")
	for _, use := range regexp.MustCompile(`time\.(Now|Since|Until)\(`).FindAllString(code, -1) {
		t.Errorf("sweep.go reads the host's clock: %s", use)
	}
	if !strings.Contains(code, "clock_timestamp() - $1::bigint * interval '1 microsecond' AS cutoff") {
		t.Error("the cutoff is not the database's clock minus the grace")
	}
	if strings.Count(code, "clock_timestamp()") != 1 {
		t.Errorf("the sweep compares against %d clocks, want the one cutoff of the statement", strings.Count(code, "clock_timestamp()"))
	}
	// The expiries are compared with the cutoff only: no parameter of the statements is a time.
	for _, sql := range []string{deleteArtifacts, deleteSnapshots, reduceJobs, countSweep} {
		if strings.Contains(sql, "$3") || strings.Contains(sql, "::timestamptz") {
			t.Error("a sweep statement takes a time from its caller")
		}
	}
}

// Admission counts by the database's clock and under the identity's lock (S04-D). No test can move the database's clock away from the test
// host's, so, as for the sweep, the source is held to it: every comparison of an expiry or of the hour is made in SQL against
// clock_timestamp(), the Go code of Admit never reads the host's clock, the lock is taken before the counts and the counts name the identity.
func TestAdmissionCountsByTheDatabasesClockUnderTheIdentitysLock(t *testing.T) {
	src, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(src), "")
	for _, use := range regexp.MustCompile(`time\.(Now|Since|Until)\(`).FindAllString(code, -1) {
		t.Errorf("repository.go reads the host's clock: %s", use)
	}
	if n := strings.Count(admitCountSQL, "clock_timestamp()"); n != 2 {
		t.Errorf("the counts compare against %d database clocks, want 2 (the expiry and the hour)", n)
	}
	if !strings.Contains(admitCountSQL, "expires_at > clock_timestamp()") || !strings.Contains(admitCountSQL, "created_at > clock_timestamp() - interval '1 hour'") {
		t.Error("the expiry and the hour are not compared with the database's clock")
	}
	if strings.Contains(admitCountSQL, "$4") || strings.Contains(admitCountSQL, "::timestamptz") {
		t.Error("a count takes a time from its caller")
	}
	if n := strings.Count(admitCountSQL, "tenant=$1 AND issuer=$2 AND subject=$3"); n != 3 {
		t.Errorf("%d of the 3 counts name the whole identity", n)
	}
	if !strings.Contains(admitCountSQL, "kind IN ('snapshot','artifact')") || strings.Contains(admitCountSQL, "'issue'") {
		t.Error("the results counted are not exactly snapshots and artifacts")
	}
	// The files of issued documents are left out (ADR 0026 decision 7, S25-B1), by the words of the predicate of assets_identity_results
	// (migration kept_results), which is what lets the planner answer from that index without reading a payload; the plan itself is held
	// by the integration test of the capacity plans.
	if !strings.Contains(admitResultsSQL, "kind IN ('snapshot','artifact') AND NOT (data ? 'Issue') AND expires_at > clock_timestamp()") {
		t.Error("the stored results are not counted without the issued files, in the words of the index's predicate")
	}
	var step Migration
	for _, m := range mustMigrations(t) {
		if m.Name == "kept_results" {
			step = m
		}
	}
	if !strings.Contains(step.Up, "ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE kind IN ('snapshot','artifact') AND NOT (data ? 'Issue');") {
		t.Error("the index of the stored results does not have the predicate the count is written with")
	}
	if !strings.Contains(admitLockSQL, "pg_advisory_xact_lock(") || strings.Contains(strings.ToLower(admitLockSQL), "pg_try_advisory") || strings.Contains(strings.ToLower(admitLockSQL), "pg_advisory_lock(") {
		t.Error("the admission lock is not a transaction-scoped advisory lock")
	}
	lock, replay, count := strings.Index(code, "tx.Exec(ctx, admitLockSQL"), strings.Index(code, "admitReplaySQL, o.Tenant"), strings.Index(code, "admitCountSQL, o.Tenant")
	insert := strings.Index(code, "v, err = admit(ctx, tx, j)")
	if !(lock > 0 && lock < replay && replay < count && count < insert) {
		t.Errorf("Admit does not take the lock, settle the replay, count and insert in that order (%d %d %d %d)", lock, replay, count, insert)
	}
}

func mustMigrations(t *testing.T) []Migration {
	t.Helper()
	known, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	return known
}

// Whether the object store is due for a reconciliation is the ledger's word, read against the database's clock, which wrote the ledger's
// times (S25-B1): the host's clock does not decide whether a sweep reconciles. The bucket is settled only by a reconciliation that ran
// to its end and left no object behind, from the time its listing began; a dry run settles nothing, and a sweep that left objects or
// did not complete unsettles it from the time it ended. The statement is written with the predicate and the order of the index
// sweeps_reconciliation (migration kept_results), and reads the newest of the sweeps that index holds.
func TestWhetherTheBucketIsDueIsTheLedgersWordByTheDatabasesClock(t *testing.T) {
	for _, part := range []string{
		"SELECT settled_from > clock_timestamp() - $1::bigint * interval '1 microsecond'",
		"FROM reporting_s01.sweeps WHERE NOT dry_run AND (settled_from IS NOT NULL OR objects_left > 0 OR NOT complete) ORDER BY COALESCE(settled_from, at) DESC, id DESC LIMIT 1",
		"COALESCE((", ", false)", // a ledger that records no such sweep is a bucket that is due, and so is one whose newest entry settles nothing
	} {
		if !strings.Contains(bucketSettled, part) {
			t.Errorf("the statement does not say %q: %q", part, bucketSettled)
		}
	}
	if strings.Contains(bucketSettled, "$2") || strings.Contains(bucketSettled, "::timestamptz") || strings.Count(bucketSettled, "clock_timestamp()") != 1 {
		t.Error("the statement takes a time from its caller, or compares with more than the database's clock")
	}
	var step Migration
	for _, m := range mustMigrations(t) {
		if m.Name == "kept_results" {
			step = m
		}
	}
	if !strings.Contains(step.Up, "ON reporting_s01.sweeps ((COALESCE(settled_from, at)), id) WHERE NOT dry_run AND (settled_from IS NOT NULL OR objects_left > 0 OR NOT complete);") {
		t.Error("the index of the reconciliations does not have the predicate and the order the statement is written with")
	}
	// The ledger takes the word that the bucket is settled only from a sweep that could have settled it.
	if !strings.Contains(step.Up, "CONSTRAINT sweeps_settled_from_check CHECK (settled_from IS NULL OR (reconciled AND complete AND objects_left = 0 AND NOT dry_run))") {
		t.Error("the ledger does not hold a row that says the bucket is settled to a reconciliation that ran to its end and left nothing")
	}
	// The sweep records in its ledger row what the result says, and since when the bucket is settled; that time is the database's, read
	// before the listing begins, and nothing else decides them.
	src, err := os.ReadFile("sweep.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "objects, orphans, reconciled, objects_left, settled_from) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)") ||
		!strings.Contains(string(src), "res.Objects, res.Orphans, res.Reconciled, res.ObjectsLeft, settledFrom(res, complete, o.Grace, began))") {
		t.Error("the ledger row of a sweep does not record whether it reconciled, what it left and since when the bucket is settled")
	}
	obj, err := os.ReadFile("objects.go")
	if err != nil {
		t.Fatal(err)
	}
	clock, listed := strings.Index(string(obj), "pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&began)"), strings.Index(string(obj), "if e := reconcile(ctx, pool, objects, now, o, left, res); e != nil {")
	if clock < 0 || listed < 0 || clock > listed {
		t.Error("the time a reconciliation's listing began is not the database's, read before the listing")
	}
	// What settles: only a sweep that was not a dry run, ran to its end, reconciled and left nothing, with a grace that is not under the
	// least age of an orphan (an object such a sweep left may have been too young for its own reconciliation).
	began := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	clean := r.SweepResult{Reconciled: true}
	for _, grace := range []time.Duration{r.MinOrphanAge, r.DefaultSweepGrace} {
		if got := settledFrom(clean, true, grace, began); got == nil || !got.Equal(began) {
			t.Errorf("a reconciliation that left nothing, with a grace of %s, does not say since when the bucket is settled: %v", grace, got)
		}
	}
	for name, c := range map[string]struct {
		res      r.SweepResult
		complete bool
		grace    time.Duration
	}{
		"a dry run":                              {r.SweepResult{DryRun: true, Reconciled: true}, true, r.DefaultSweepGrace},
		"a sweep that did not reconcile":         {r.SweepResult{}, true, r.DefaultSweepGrace},
		"a reconciliation that left objects":     {r.SweepResult{Reconciled: true, ObjectsLeft: 1}, true, r.DefaultSweepGrace},
		"a sweep that did not complete":          {r.SweepResult{Reconciled: true}, false, r.DefaultSweepGrace},
		"a sweep with a grace under an orphan's": {r.SweepResult{Reconciled: true}, true, r.MinOrphanAge - time.Second},
	} {
		if got := settledFrom(c.res, c.complete, c.grace, began); got != nil {
			t.Errorf("%s says that the bucket is settled since %v", name, got)
		}
	}
}

// A sweep whose ledger row could not be written has left no word for the next sweeps (S25-B1): its caller hears of that whether the sweep
// itself had completed or failed, and a sweep that had failed keeps its own error.
func TestASweepWhoseLedgerRowWasNotWrittenSaysSo(t *testing.T) {
	unwritten := errors.New("connection lost")
	if e := unrecorded(nil, unwritten); e == nil || !errors.Is(e, unwritten) || !strings.Contains(e.Error(), "the sweep ran but its ledger row could not be written") {
		t.Errorf("a sweep that completed and wrote no ledger row: %v", e)
	}
	swept := fmt.Errorf("the bucket could not be listed: %w", r.ErrUnavailable)
	e := unrecorded(swept, unwritten)
	if e == nil || !errors.Is(e, r.ErrUnavailable) || !strings.Contains(e.Error(), "the bucket could not be listed") || !strings.Contains(e.Error(), "its ledger row could not be written: connection lost") {
		t.Errorf("a sweep that failed and wrote no ledger row: %v", e)
	}
	// The sweep hands every failure of the ledger's INSERT to it, and returns what it gives back.
	src, err := os.ReadFile("sweep.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "settledFrom(res, complete, o.Grace, began)); le != nil {\n\t\te = unrecorded(e, le)\n\t}\n\treturn res, e\n") {
		t.Error("the sweep does not report a ledger row that could not be written whatever its own outcome")
	}
}

// The one place the platform host's clock decides anything in the store is the age of an object (S04-E): the clock that stamped an object is
// the object store's, not the database's, so the reconciliation compares its time with the host's; the rows' expiries stay the database's
// (TestSweepNeverDecidesByTheHostsClock). No other file of the package reads the host's clock, and objects.go reads it once, as the default of
// a clock a test can replace (WithObjectClock).
func TestOnlyTheAgeOfAnObjectIsJudgedByTheHostsClock(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	uses := regexp.MustCompile(`time\.(Now|Since|Until)\b`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		code := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(src), "")
		found := uses.FindAllString(code, -1)
		switch {
		case f == "objects.go" && len(found) != 1:
			t.Errorf("objects.go reads the host's clock %d times, want its one default", len(found))
		case f != "objects.go" && len(found) != 0:
			t.Errorf("%s reads the host's clock: %v", f, found)
		}
	}
	// The row is deleted and committed before any object is: the delete of the objects follows the batch's commit in the same function.
	src, err := os.ReadFile("sweep.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(src), "")
	commit, del := strings.Index(code, "func artifactBatch("), strings.Index(code, "objects.Delete(ctx, key)")
	if commit < 0 || del < 0 || strings.Contains(code[commit:strings.Index(code[commit:], "\n}\n")+commit], "Delete(") {
		t.Error("an object is deleted inside the transaction that deletes the rows")
	}
	if !strings.Contains(code, "RETURNING d.id, coalesce(d.data->>'Object', '')") || !strings.Contains(code, "key == r.ObjectKey(id)") {
		t.Error("the keys deleted are not those the batch returned for the rows it deleted, each checked against its row's own id")
	}
}
