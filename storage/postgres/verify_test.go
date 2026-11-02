package postgres

import (
	"os"
	"regexp"
	"testing"
)

// The verification of the objects after a restore (S04-F) reads and never writes. The behaviour is proved by the integration tests (the rows and the
// bucket are unchanged, and it runs as a role that has SELECT on assets only); this holds the source to the structure that makes it so, as the
// context rule holds the rest of the package: every transaction of verify.go is the read-only worker one, and nothing in it is a statement that
// writes.

var writing = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|TRUNCATE|ALTER|CREATE|DROP|GRANT|REVOKE|COPY|LOCK|VACUUM|NOTIFY)\b`)

func verifyProblems(src string) []string {
	code := comments.ReplaceAllString(src, "")
	var out []string
	if m := regexp.MustCompile("(?s)`[^`]*`|\"[^\"\\n]*\"").FindAllString(code, -1); len(m) > 0 {
		for _, lit := range m {
			if writing.MatchString(lit) {
				out = append(out, "a literal that reads like a write: "+lit)
			}
		}
	}
	if regexp.MustCompile(`\.(Exec|SendBatch|CopyFrom)\(`).MatchString(code) {
		out = append(out, "Exec, SendBatch or CopyFrom")
	}
	if regexp.MustCompile(`\bpool\.(Begin|BeginTx|Exec|Query|QueryRow|Acquire)\(`).MatchString(code) {
		out = append(out, "reaches the pool directly, outside the read-only worker transaction")
	}
	if !regexp.MustCompile(`beginWorkerReadOnly\(`).MatchString(code) || regexp.MustCompile(`\b(beginWorker|beginAs|asWorker|asIdentity)\(`).MatchString(code) {
		out = append(out, "does not use only beginWorkerReadOnly")
	}
	return out
}

func TestVerificationOfObjectsOnlyReads(t *testing.T) {
	b, err := os.ReadFile("verify.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range verifyProblems(string(b)) {
		t.Error(p)
	}
	for name, bad := range map[string]string{
		"an update":            "tx.Query(ctx, `UPDATE reporting_s01.assets SET data='{}'`)\nbeginWorkerReadOnly(ctx, pool)",
		"an exec":              "tx.Exec(ctx, `SELECT 1`)\nbeginWorkerReadOnly(ctx, pool)",
		"the pool":             "pool.Query(ctx, `SELECT 1`)\nbeginWorkerReadOnly(ctx, pool)",
		"a writable worker tx": "beginWorker(ctx, pool)",
		"no transaction":       "return nil",
	} {
		if len(verifyProblems(bad)) == 0 {
			t.Errorf("%s is not found by the source rule", name)
		}
	}
	if p := verifyProblems("tx.Query(ctx, `SELECT id FROM reporting_s01.assets WHERE id > $1`)\nbeginWorkerReadOnly(ctx, s.pool)"); len(p) != 0 {
		t.Errorf("the allowed form is refused: %v", p)
	}
}
