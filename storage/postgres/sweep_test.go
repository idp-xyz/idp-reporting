package postgres

import (
	"os"
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
