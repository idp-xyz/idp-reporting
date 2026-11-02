package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The database context of S04-C is transaction-local and nothing else: a setting that outlived its transaction would carry one call's identity
// into the next call of a pooled connection, which is the leak the policies exist to prevent. No test can see every future statement, so the
// source is held to the rule, as TestSweepNeverDecidesByTheHostsClock holds the sweep to the database's clock.

var comments = regexp.MustCompile(`(?m)//.*$`)

// contextProblems says what a source file does that the rule forbids.
func contextProblems(name, src string) []string {
	// "SET ROLE in effect" is the name of a problem the role check reports, not a statement.
	code := strings.ReplaceAll(comments.ReplaceAllString(src, ""), "SET ROLE in effect", "")
	var out []string
	calls := regexp.MustCompile(`(?i)set_config\(`).FindAllString(code, -1)
	good := regexp.MustCompile(`(?i)set_config\('reporting\.(tenant|issuer|subject|scope)', \$[1-9], true\)`).FindAllString(code, -1)
	if len(calls) != len(good) {
		out = append(out, name+": a set_config that is not set_config('reporting.x', $n, true): the value is a parameter and the setting transaction-local")
	}
	for _, m := range regexp.MustCompile(`(?i)(^|["`+"`"+`;]\s*)(SET\s+(SESSION|ROLE|SCHEMA|search_path|reporting)|RESET\b|SET\s+(?:[a-z_.]+)\s*(=|TO)\b)`).FindAllString(code, -1) {
		if !regexp.MustCompile(`(?i)SET\s+LOCAL`).MatchString(m) {
			out = append(out, name+": a session-level setting: "+strings.TrimSpace(m))
		}
	}
	if regexp.MustCompile(`(?i)set_config\([^)]*false`).MatchString(code) {
		out = append(out, name+": set_config(..., false) lasts the session")
	}
	if name != "context.go" && name != "production.go" && regexp.MustCompile(`reporting\.(tenant|issuer|subject|scope)`).MatchString(code) {
		out = append(out, name+": names a context setting outside context.go (and the policies the role check expects, in production.go)")
	}
	return out
}

func TestTheDatabaseContextIsTransactionLocalAndNothingElse(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	source := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		source[f] = string(b)
		for _, p := range contextProblems(f, string(b)) {
			t.Error(p)
		}
	}
	// The rule has teeth: the shapes it forbids are found.
	for name, bad := range map[string]string{
		"session-level set_config":  `tx.Exec(ctx, "SELECT set_config('reporting.tenant', $1, false)", t)`,
		"interpolated value":        `tx.Exec(ctx, "SELECT set_config('reporting.tenant', '" + t + "', true)")`,
		"SET":                       `tx.Exec(ctx, "SET reporting.tenant = 'x'")`,
		"SET SESSION":               `tx.Exec(ctx, "SET SESSION reporting.tenant = 'x'")`,
		"RESET":                     `tx.Exec(ctx, "RESET reporting.tenant")`,
		"SET ROLE":                  `tx.Exec(ctx, "SET ROLE reporting_migrator")`,
		"a setting named elsewhere": `const x = "reporting.tenant"`,
	} {
		if len(contextProblems("elsewhere.go", bad)) == 0 {
			t.Errorf("%s is not found by the source rule", name)
		}
	}
	if p := contextProblems("context.go", `tx.Exec(ctx, "SET LOCAL lock_timeout = '10s'")`+"\n"+`const s = "SELECT set_config('reporting.scope', $1, true)"`); len(p) != 0 {
		t.Errorf("the allowed forms are refused: %v", p)
	}
	// Every statement of the store runs in a transaction that has set its context: repository.go reaches the pool only through
	// beginAs, beginWorker, asIdentity and asWorker, except the fixture's reset; sweep.go only for its ledger and its role check (neither
	// reads a business row).
	direct := regexp.MustCompile(`\bpool\.(Exec|Query|QueryRow|Begin|BeginTx|SendBatch|CopyFrom|Acquire)\(`)
	if n := len(direct.FindAllString(comments.ReplaceAllString(source["repository.go"], ""), -1)); n != 1 {
		t.Errorf("repository.go reaches the pool directly %d times, want 1 (ResetFixture): a statement outside a context transaction", n)
	}
	if n := len(direct.FindAllString(comments.ReplaceAllString(source["sweep.go"], ""), -1)); n != 2 {
		t.Errorf("sweep.go reaches the pool directly %d times, want 2 (the role check and the ledger row): a batch outside a context transaction", n)
	}
}
