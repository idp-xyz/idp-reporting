package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
	if !strings.Contains(admitLockSQL, "pg_advisory_xact_lock(") || strings.Contains(strings.ToLower(admitLockSQL), "pg_try_advisory") || strings.Contains(strings.ToLower(admitLockSQL), "pg_advisory_lock(") {
		t.Error("the admission lock is not a transaction-scoped advisory lock")
	}
	lock, replay, count := strings.Index(code, "tx.Exec(ctx, admitLockSQL"), strings.Index(code, "admitReplaySQL, o.Tenant"), strings.Index(code, "admitCountSQL, o.Tenant")
	insert := strings.Index(code, "v, err = admit(ctx, tx, j)")
	if !(lock > 0 && lock < replay && replay < count && count < insert) {
		t.Errorf("Admit does not take the lock, settle the replay, count and insert in that order (%d %d %d %d)", lock, replay, count, insert)
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
