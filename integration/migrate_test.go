package integration_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// catalog is a textual snapshot of the platform schema: tables, columns, constraints, indexes.
func catalog(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var out strings.Builder
	rows, e := pool.Query(ctx, `SELECT c.relname, a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod), CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END,
coalesce(pg_get_expr(d.adbin, d.adrelid), ''), a.attnum
FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
WHERE n.nspname='reporting_s01' AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped ORDER BY c.relname, a.attnum`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var table, column, typ, nullable, def string
		var pos int
		if e := rows.Scan(&table, &column, &typ, &nullable, &def, &pos); e != nil {
			t.Fatal(e)
		}
		fmt.Fprintf(&out, "column %s.%s %d %s null=%s default=%s\n", table, column, pos, typ, nullable, def)
	}
	rows.Close()
	rows, e = pool.Query(ctx, `SELECT c.conrelid::regclass::text, c.conname, pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='reporting_s01' ORDER BY 1,2`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var table, name, def string
		if e := rows.Scan(&table, &name, &def); e != nil {
			t.Fatal(e)
		}
		fmt.Fprintf(&out, "constraint %s %s %s\n", table, name, def)
	}
	rows.Close()
	rows, e = pool.Query(ctx, `SELECT tablename, indexname, indexdef FROM pg_indexes WHERE schemaname='reporting_s01' ORDER BY 1,2`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var table, name, def string
		if e := rows.Scan(&table, &name, &def); e != nil {
			t.Fatal(e)
		}
		fmt.Fprintf(&out, "index %s %s %s\n", table, name, def)
	}
	rows.Close()
	return out.String()
}

func dropSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, e := pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS reporting_s01 CASCADE"); e != nil {
		t.Fatal(e)
	}
}

// TestPostgresMigrations qualifies the schema lifecycle of S04-A on the guarded fixture: the
// ledger, the plan, up and down round trips, refusals, concurrency, the legacy bootstrap, the
// application role's privileges and the production opening path's rules.
func TestPostgresMigrations(t *testing.T) {
	if os.Getenv("REPORTING_S01_TEST_DSN") == "" || os.Getenv("REPORTING_S01_SOURCE_DSN") == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required platform/source PostgreSQL DSNs missing")
		}
		t.Skip("durable PostgreSQL gate not run: two isolated DB DSNs required")
	}
	ctx := context.Background()
	admin := fixtureAdmin(t)
	known, e := postgres.Migrations()
	if e != nil || len(known) != 3 || known[0].Name != "base" || known[2].Name != "issues" {
		t.Fatalf("%+v %v", known, e)
	}
	t.Run("round_trip_reproduces_the_catalog_exactly", func(t *testing.T) {
		dropSchema(t, admin)
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		first := catalog(t, admin)
		if !strings.Contains(first, "constraint reporting_s01.jobs jobs_kind_check CHECK ((kind = ANY (ARRAY['snapshot'::text, 'export'::text, 'issue'::text])))") || !strings.Contains(first, "column issue_numbers.last") || !strings.Contains(first, "column schema_migrations.checksum") {
			t.Fatalf("unexpected catalog:\n%s", first)
		}
		// The catalog of version 3 is committed: a later edit of an earlier migration file (which
		// the checksums would also flag) changes a fresh database and fails here.
		if os.Getenv("REPORTING_WRITE_CATALOG") == "1" { // after a reviewed migration change only
			if e := os.WriteFile("testdata/catalog_003.txt", []byte(first), 0o644); e != nil {
				t.Fatal(e)
			}
		}
		want, e := os.ReadFile("testdata/catalog_003.txt")
		if e != nil {
			t.Fatal(e)
		}
		if string(want) != first {
			t.Fatalf("the catalog differs from testdata/catalog_003.txt:\n%s", first)
		}
		st, e := postgres.Status(ctx, admin)
		if e != nil || st.Current() != 3 || len(st.Pending) != 0 || len(st.Problems) != 0 || st.Legacy {
			t.Fatalf("%+v %v", st, e)
		}
		for _, a := range st.Applied {
			if a.Checksum != known[a.Version-1].Checksum || a.AppliedBy != "test" || a.AppliedAt.IsZero() {
				t.Fatalf("ledger row %+v", a)
			}
		}
		// Down to zero and up again: the catalog is byte for byte the same, the profile is gone
		// and back at 3. The profile row counts as data: 001 down needs the acceptance.
		if e := postgres.Down(ctx, admin, 0, "test", false); !errors.Is(e, r.ErrDenied) {
			t.Fatal("001 down with the profile row accepted", e)
		}
		if e := postgres.Down(ctx, admin, 0, "test", true); e != nil {
			t.Fatal(e)
		}
		if after := catalog(t, admin); after != "" {
			t.Fatalf("schema not empty after down to 0:\n%s", after)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		if again := catalog(t, admin); again != first {
			t.Fatalf("catalog differs after the round trip:\n%s\n---\n%s", first, again)
		}
		if e := postgres.Verify(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		// Partial targets: down to 1 keeps the base tables without digests, up to 2 adds them.
		if e := postgres.Down(ctx, admin, 1, "test", false); e != nil {
			t.Fatal(e)
		}
		c := catalog(t, admin)
		if strings.Contains(c, "data_sha256") || strings.Contains(c, "issue_numbers") || !strings.Contains(c, "column jobs.id") {
			t.Fatalf("catalog at 1:\n%s", c)
		}
		var version int
		if e := admin.QueryRow(ctx, "SELECT version FROM reporting_s01.profile").Scan(&version); e != nil || version != 1 {
			t.Fatal(version, e)
		}
		if e := postgres.Up(ctx, admin, 2, "test"); e != nil {
			t.Fatal(e)
		}
		if c := catalog(t, admin); !strings.Contains(c, "column jobs.data_sha256") || strings.Contains(c, "issue_numbers") {
			t.Fatalf("catalog at 2:\n%s", c)
		}
		plan, e := postgres.Plan(must(postgres.Status(ctx, admin)), 3, postgres.Upward)
		if e != nil || len(plan) != 1 || plan[0] != "up   003 issues" {
			t.Fatal(plan, e)
		}
		// A target on the wrong side of the current version is refused, not silently ignored.
		if e := postgres.Down(ctx, admin, 3, "test", false); !errors.Is(e, r.ErrInvalid) {
			t.Fatal("down to a higher version accepted", e)
		}
		if e := postgres.Up(ctx, admin, 1, "test"); !errors.Is(e, r.ErrInvalid) {
			t.Fatal("up to a lower version accepted", e)
		}
		if _, e := postgres.Plan(must(postgres.Status(ctx, admin)), 1, postgres.Either); e != nil {
			t.Fatal("plan either way", e)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("refusals_checksum_newer_binary_gap_and_data_loss", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		if _, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one"); e != nil {
			t.Fatal(e)
		}
		// Down over 003 would discard the issue job, the counter and the issued PDF: refused with
		// all three named (the counters restart the numbering); accepted, it goes.
		e := postgres.Down(ctx, admin, 2, "test", false)
		if !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "1 issue jobs and records and 1 issue counters") || !strings.Contains(e.Error(), "restart at 1") || !strings.Contains(e.Error(), "0 issued PDFs would lose their records") {
			t.Fatal("down with issue rows accepted or counted wrongly", e)
		}
		// A counter alone (its rows gone) is still data: the numbering would restart.
		if _, e := admin.Exec(ctx, "DELETE FROM reporting_s01.jobs WHERE kind='issue'"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, admin, 2, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "1 issue counters") {
			t.Fatal("down with a counter alone accepted", e)
		}
		if st := must(postgres.Status(ctx, admin)); st.Current() != 3 {
			t.Fatal(st)
		}
		if e := postgres.Down(ctx, admin, 2, "test", true); e != nil {
			t.Fatal(e)
		}
		var n int
		if e := admin.QueryRow(ctx, "SELECT count(*) FROM reporting_s01.jobs").Scan(&n); e != nil || n < 1 {
			t.Fatal("the snapshot job must survive 003 down", n, e)
		}
		// 002 down loses nothing and needs no acceptance; 001 down with rows is refused, and so
		// is 001 down with the profile row alone.
		if e := postgres.Down(ctx, admin, 1, "test", false); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, admin, 0, "test", false); !errors.Is(e, r.ErrDenied) {
			t.Fatal("001 down with rows accepted", e)
		}
		if _, e := admin.Exec(ctx, "TRUNCATE reporting_s01.jobs, reporting_s01.assets"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, admin, 0, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "profile row") {
			t.Fatal("001 down with the profile row accepted", e)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		// A stale plan: a step applied onto a ledger whose head moved is refused, never applied.
		if e := postgres.Down(ctx, admin, 1, "test", true); e != nil {
			t.Fatal(e)
		}
		if e := postgres.ApplyStepForTests(ctx, admin, 3, true); !errors.Is(e, r.ErrConflict) {
			t.Fatal("a step applied onto the wrong ledger head", e)
		}
		if st := must(postgres.Status(ctx, admin)); st.Current() != 1 || len(st.Problems) != 0 {
			t.Fatalf("the stale step changed the ledger: %+v", st)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		// Legacy rows are refused on a production profile, accepted on the fixture.
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.schema_migrations SET checksum='legacy', applied_by='legacy:test' WHERE version=2"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Verify(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileProduction); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Verify(ctx, admin, postgres.ProfileProduction); !errors.Is(e, r.ErrIntegrity) {
			t.Fatal("a legacy row passed on a production store", e)
		}
		for _, sql := range []string{"UPDATE reporting_s01.profile SET profile='" + postgres.ProfileFixture + "'", "UPDATE reporting_s01.schema_migrations SET checksum='" + known[1].Checksum + "' WHERE version=2"} {
			if _, e := admin.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
		}
		// The store refuses to open: a checksum that is not the embedded file's, a version the
		// binary does not know, a gap, a profile at another version.
		for _, c := range []struct{ name, tamper, restore, reason string }{
			{"checksum", "UPDATE reporting_s01.schema_migrations SET checksum='0' WHERE version=2", "UPDATE reporting_s01.schema_migrations SET checksum='" + known[1].Checksum + "' WHERE version=2", "version 2 was applied from a different file"},
			{"newer binary", "INSERT INTO reporting_s01.schema_migrations(version,name,checksum,applied_by) VALUES(4,'future','0','x')", "DELETE FROM reporting_s01.schema_migrations WHERE version=4", "version 4 was applied by a newer binary"},
			{"gap", "DELETE FROM reporting_s01.schema_migrations WHERE version=2", "INSERT INTO reporting_s01.schema_migrations(version,name,checksum,applied_by) VALUES(2,'payload_digests','" + known[1].Checksum + "','test')", "gap before version 3"},
			{"other name", "UPDATE reporting_s01.schema_migrations SET name='other' WHERE version=3", "UPDATE reporting_s01.schema_migrations SET name='issues' WHERE version=3", "under another name"},
			{"fake legacy", "UPDATE reporting_s01.schema_migrations SET checksum='legacy' WHERE version=3", "UPDATE reporting_s01.schema_migrations SET checksum='" + known[2].Checksum + "' WHERE version=3", "claims to be a legacy row"},
			{"profile stale", "UPDATE reporting_s01.profile SET version=2", "UPDATE reporting_s01.profile SET version=3", "profile says version 2"},
		} {
			before := catalog(t, admin)
			if _, e := admin.Exec(ctx, c.tamper); e != nil {
				t.Fatal(c.name, e)
			}
			st := must(postgres.Status(ctx, admin))
			if len(st.Problems) == 0 || !strings.Contains(strings.Join(st.Problems, ";"), c.reason) {
				t.Fatalf("%s: problems %v, want one with %q", c.name, st.Problems, c.reason)
			}
			_, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false)
			if !errors.Is(e, r.ErrIntegrity) || !strings.Contains(e.Error(), c.reason) {
				t.Fatalf("%s: the store opened or refused for another reason: %v", c.name, e)
			}
			if e := postgres.Up(ctx, admin, 0, "test"); !errors.Is(e, r.ErrIntegrity) {
				t.Fatalf("%s: up ran on an unusable ledger: %v", c.name, e)
			}
			if _, e := admin.Exec(ctx, c.restore); e != nil {
				t.Fatal(c.name, e)
			}
			if after := catalog(t, admin); after != before {
				t.Fatalf("%s changed the catalog", c.name)
			}
		}
		// A step whose DDL fails leaves the ledger, the profile and the catalog as they were.
		if e := postgres.Down(ctx, admin, 2, "test", true); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, "CREATE TABLE reporting_s01.issue_numbers(x int)"); e != nil {
			t.Fatal(e)
		}
		before := catalog(t, admin)
		if e := postgres.Up(ctx, admin, 0, "test"); e == nil || !strings.Contains(e.Error(), "already exists") {
			t.Fatal("a conflicting table did not fail the step", e)
		}
		st := must(postgres.Status(ctx, admin))
		var profileVersion int
		if e := admin.QueryRow(ctx, "SELECT version FROM reporting_s01.profile").Scan(&profileVersion); e != nil {
			t.Fatal(e)
		}
		if st.Current() != 2 || profileVersion != 2 || catalog(t, admin) != before {
			t.Fatalf("a failed step changed something: %d %d", st.Current(), profileVersion)
		}
		if _, e := admin.Exec(ctx, "DROP TABLE reporting_s01.issue_numbers"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Verify(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal("not restored", e)
		}
	})
	t.Run("concurrent_migrators_serialize_on_the_lock", func(t *testing.T) {
		// Several rounds on a pool whose connections have seen the schema absent (a negative
		// catalog cache entry): every migrator must end at version 3 with one ledger.
		const n = 6
		for round := range 5 {
			dropSchema(t, admin)
			var warm sync.WaitGroup
			for range n {
				warm.Add(1)
				go func() { defer warm.Done(); _, _ = postgres.Status(ctx, admin) }()
			}
			warm.Wait()
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[i] = postgres.Up(ctx, admin, 0, fmt.Sprintf("m%d", i))
				}()
			}
			wg.Wait()
			for i, e := range errs {
				if e != nil {
					t.Fatalf("round %d migrator %d: %v", round, i, e)
				}
			}
			st := must(postgres.Status(ctx, admin))
			if st.Current() != 3 || len(st.Applied) != 3 || len(st.Problems) != 0 {
				t.Fatalf("round %d: %+v", round, st)
			}
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("half_initialised_database_is_a_problem_not_version_zero", func(t *testing.T) {
		dropSchema(t, admin)
		if _, e := admin.Exec(ctx, "CREATE SCHEMA reporting_s01; CREATE TABLE reporting_s01.profile(singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton), profile text NOT NULL, version integer NOT NULL)"); e != nil {
			t.Fatal(e)
		}
		st := must(postgres.Status(ctx, admin))
		if st.Legacy || len(st.Problems) != 1 || !strings.Contains(st.Problems[0], "half-initialised") {
			t.Fatalf("%+v", st)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); !errors.Is(e, r.ErrIntegrity) {
			t.Fatal("up ran on a half-initialised database", e)
		}
		var ledger bool
		if e := admin.QueryRow(ctx, "SELECT to_regclass('reporting_s01.schema_migrations') IS NOT NULL").Scan(&ledger); e != nil || ledger {
			t.Fatal("a ledger was left behind", ledger, e)
		}
		dropSchema(t, admin)
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("legacy_database_without_a_ledger_is_bootstrapped", func(t *testing.T) {
		// A database at profile version 2 made before S04-A: no ledger, no issue tables.
		if _, e := admin.Exec(ctx, `DROP TABLE reporting_s01.schema_migrations; DROP TABLE reporting_s01.issue_numbers;
ALTER TABLE reporting_s01.jobs DROP CONSTRAINT jobs_kind_check; ALTER TABLE reporting_s01.jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('snapshot','export'));
ALTER TABLE reporting_s01.assets DROP CONSTRAINT assets_kind_check; ALTER TABLE reporting_s01.assets ADD CONSTRAINT assets_kind_check CHECK(kind IN ('snapshot','artifact'));
UPDATE reporting_s01.profile SET version=2`); e != nil {
			t.Fatal(e)
		}
		st := must(postgres.Status(ctx, admin))
		if !st.Legacy || st.Current() != 2 || len(st.Pending) != 1 || len(st.Problems) != 0 {
			t.Fatalf("%+v", st)
		}
		if _, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false); !errors.Is(e, r.ErrDenied) {
			t.Fatal("a legacy database opened without initialize", e)
		}
		store, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), true)
		if e != nil {
			t.Fatal(e)
		}
		store.Close()
		st = must(postgres.Status(ctx, admin))
		if st.Legacy || st.Current() != 3 || st.Applied[0].Checksum != "legacy" || st.Applied[1].Checksum != "legacy" || st.Applied[2].Checksum != known[2].Checksum {
			t.Fatalf("%+v", st)
		}
		// The realistic pre-S04-A database: the old fixture at version 3 without a ledger. The
		// server refuses it for the right reason; initialising records three legacy rows.
		if _, e := admin.Exec(ctx, "DROP TABLE reporting_s01.schema_migrations"); e != nil {
			t.Fatal(e)
		}
		if _, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "no schema ledger") {
			t.Fatal("a ledger-less v3 database opened or refused for another reason", e)
		}
		store, e = postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), true)
		if e != nil {
			t.Fatal(e)
		}
		store.Close()
		st = must(postgres.Status(ctx, admin))
		if st.Legacy || st.Current() != 3 || st.Applied[2].Checksum != "legacy" || !strings.HasPrefix(st.Applied[2].AppliedBy, "legacy:") {
			t.Fatalf("%+v", st)
		}
		// A production store is never a bootstrap: those rows are refused there.
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.profile SET profile='"+postgres.ProfileProduction+"'"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Verify(ctx, admin, postgres.ProfileProduction); !errors.Is(e, r.ErrIntegrity) || !strings.Contains(e.Error(), "no verifiable checksum") {
			t.Fatal(e)
		}
		dropSchema(t, admin)
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("application_role_reads_and_writes_rows_and_nothing_else", func(t *testing.T) {
		dropSchema(t, admin)
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		v := newDurable(t)
		v.repo.Close()
		for _, role := range []string{"reporting_app_t", "reporting_migrator_t"} {
			if _, e := admin.Exec(ctx, "DROP ROLE IF EXISTS "+role); e != nil {
				t.Fatal(e)
			}
			if _, e := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN"); e != nil {
				t.Fatal(e)
			}
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(context.Background(), "REASSIGN OWNED BY reporting_migrator_t TO postgres; DROP OWNED BY reporting_app_t; DROP OWNED BY reporting_migrator_t; DROP ROLE IF EXISTS reporting_app_t; DROP ROLE IF EXISTS reporting_migrator_t")
		})
		if e := postgres.Grants(ctx, admin, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Grants(ctx, admin, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal("grants are not idempotent", e)
		}
		for _, bad := range [][2]string{{"a", "a"}, {"", "x"}, {"Robert'; DROP", "x"}} {
			if e := postgres.Grants(ctx, admin, bad[0], bad[1]); !errors.Is(e, r.ErrInvalid) {
				t.Fatal(bad, e)
			}
		}
		// The superuser is not the application role; the application role is. Each attribute
		// the rules forbid is refused on its own, on a role that otherwise has the grants.
		if problems, e := postgres.RoleProblems(ctx, admin); e != nil || !strings.Contains(strings.Join(problems, ";"), "superuser") {
			t.Fatal("the superuser passes as the application role", problems, e)
		}
		for attr, want := range map[string]string{"CREATEROLE": "role creation", "CREATEDB": "database creation", "BYPASSRLS": "row security bypass", "REPLICATION": "replication"} {
			role := "reporting_attr_t"
			if _, e := admin.Exec(ctx, "DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role); e != nil && !strings.Contains(e.Error(), "does not exist") {
				t.Fatal(e)
			}
			if _, e := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN "+attr); e != nil {
				t.Fatal(e)
			}
			if e := postgres.Grants(ctx, admin, "reporting_migrator_t", role); e != nil {
				t.Fatal(e)
			}
			pool, e := pgxpool.New(ctx, strings.Replace(os.Getenv("REPORTING_S01_TEST_DSN"), "postgres@", role+"@", 1))
			if e != nil {
				t.Fatal(e)
			}
			problems, e := postgres.RoleProblems(ctx, pool)
			pool.Close()
			if e != nil || len(problems) != 1 || problems[0] != want {
				t.Fatalf("%s: %v %v, want exactly %q", attr, problems, e, want)
			}
			if _, e := admin.Exec(ctx, "DROP OWNED BY "+role+"; DROP ROLE "+role); e != nil {
				t.Fatal(e)
			}
		}
		// The migrator role itself runs the whole lifecycle once it may create the schema: down
		// to zero (accepting the profile row), up, roles at version 2, grants on what exists.
		if _, e := admin.Exec(ctx, "GRANT CREATE ON DATABASE "+currentDatabase(t, admin)+" TO reporting_migrator_t"); e != nil {
			t.Fatal(e)
		}
		migPool, e := pgxpool.New(ctx, strings.Replace(os.Getenv("REPORTING_S01_TEST_DSN"), "postgres@", "reporting_migrator_t@", 1))
		if e != nil {
			t.Fatal(e)
		}
		defer migPool.Close()
		if _, e := admin.Exec(ctx, "ALTER SCHEMA reporting_s01 OWNER TO reporting_migrator_t"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, migPool, 0, "migrator", true); e != nil {
			t.Fatal("the migrator role cannot roll back", e)
		}
		if e := postgres.Up(ctx, migPool, 2, "migrator"); e != nil {
			t.Fatal("the migrator role cannot migrate", e)
		}
		if e := postgres.Grants(ctx, migPool, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal("roles at version 2", e)
		}
		if e := postgres.Up(ctx, migPool, 0, "migrator"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, migPool, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Grants(ctx, migPool, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		// With CREATE on the database revoked, the migrator still rolls back to zero and comes
		// up again: the schema stays and 001 does not create it.
		if _, e := admin.Exec(ctx, "REVOKE CREATE ON DATABASE "+currentDatabase(t, admin)+" FROM reporting_migrator_t"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, migPool, 0, "migrator", true); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Up(ctx, migPool, 0, "migrator"); e != nil {
			t.Fatal("the migrator role needs CREATE on the database after the first run", e)
		}
		if e := postgres.InitProfile(ctx, migPool, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Grants(ctx, migPool, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		appDSN := strings.Replace(os.Getenv("REPORTING_S01_TEST_DSN"), "postgres@", "reporting_app_t@", 1)
		appPool, e := pgxpool.New(ctx, appDSN)
		if e != nil {
			t.Fatal(e)
		}
		defer appPool.Close()
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) != 0 {
			t.Fatalf("the application role has problems: %v %v", problems, e)
		}
		// Memberships make a role more than it looks: a member of the superuser, a NOINHERIT
		// member of the migrator, are refused although their own attributes are clean; so is a
		// leftover CREATE on the schema until roles revokes it.
		for _, c := range []struct{ role, create, want string }{
			{"reporting_member_t", "CREATE ROLE reporting_member_t LOGIN IN ROLE postgres", "member of 1 roles"},
			{"reporting_noinh_t", "CREATE ROLE reporting_noinh_t LOGIN NOINHERIT IN ROLE reporting_migrator_t", "member of 1 roles"},
		} {
			if _, e := admin.Exec(ctx, "DROP ROLE IF EXISTS "+c.role+"; "+c.create); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				_, _ = admin.Exec(context.Background(), "DROP OWNED BY "+c.role+"; DROP ROLE IF EXISTS "+c.role)
			})
			if e := postgres.Grants(ctx, admin, "reporting_migrator_t", c.role); e != nil {
				t.Fatal(e)
			}
			pool, e := pgxpool.New(ctx, strings.Replace(os.Getenv("REPORTING_S01_TEST_DSN"), "postgres@", c.role+"@", 1))
			if e != nil {
				t.Fatal(e)
			}
			problems, e := postgres.RoleProblems(ctx, pool)
			pool.Close()
			if e != nil || !strings.Contains(strings.Join(problems, ";"), c.want) {
				t.Fatalf("%s: %v %v", c.role, problems, e)
			}
		}
		// Power over the database itself, and column-level grants that the table probes cannot
		// see, are refused too; roles revokes the column grants, the DBA the rest.
		for name, c := range map[string]struct{ give, take, want string }{
			"create on the database": {"GRANT CREATE ON DATABASE " + currentDatabase(t, admin) + " TO reporting_app_t", "REVOKE CREATE ON DATABASE " + currentDatabase(t, admin) + " FROM reporting_app_t", "CREATE on the database"},
			"database owner":         {"ALTER DATABASE " + currentDatabase(t, admin) + " OWNER TO reporting_app_t", "ALTER DATABASE " + currentDatabase(t, admin) + " OWNER TO postgres", "owns the database"},
			"column grant":           {"GRANT UPDATE(version) ON reporting_s01.profile TO reporting_app_t", "REVOKE UPDATE(version) ON reporting_s01.profile FROM reporting_app_t", "column-level privileges on profile"},
		} {
			if _, e := admin.Exec(ctx, c.give); e != nil {
				t.Fatal(name, e)
			}
			if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || !strings.Contains(strings.Join(problems, ";"), c.want) {
				t.Fatalf("%s: %v %v, want %q", name, problems, e, c.want)
			}
			if _, e := admin.Exec(ctx, c.take); e != nil {
				t.Fatal(name, e)
			}
			if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) != 0 {
				t.Fatalf("%s not restored: %v %v", name, problems, e)
			}
		}
		if _, e := admin.Exec(ctx, "GRANT UPDATE(checksum) ON reporting_s01.schema_migrations TO reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Grants(ctx, admin, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) != 0 {
			t.Fatal("roles did not revoke the column grant", problems, e)
		}
		if _, e := admin.Exec(ctx, "GRANT CREATE ON SCHEMA reporting_s01 TO reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || !strings.Contains(strings.Join(problems, ";"), "CREATE on the schema") {
			t.Fatal(problems, e)
		}
		if e := postgres.Grants(ctx, admin, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) != 0 {
			t.Fatal("roles did not revoke CREATE on the schema", problems, e)
		}
		// A table planted in the schema under a name that is SQL: roles refuses and runs none of
		// it; the role check names it.
		if _, e := admin.Exec(ctx, `CREATE TABLE reporting_s01."x OWNER TO postgres; ALTER ROLE reporting_app_t SUPERUSER; --" (a int)`); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Grants(ctx, admin, "reporting_migrator_t", "reporting_app_t"); !errors.Is(e, r.ErrIntegrity) {
			t.Fatal("roles ran with a planted table", e)
		}
		var super bool
		if e := admin.QueryRow(ctx, "SELECT rolsuper FROM pg_roles WHERE rolname='reporting_app_t'").Scan(&super); e != nil || super {
			t.Fatal("the planted statement ran", super, e)
		}
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || !strings.Contains(strings.Join(problems, ";"), "unexpected table") {
			t.Fatal(problems, e)
		}
		if _, e := admin.Exec(ctx, `DROP TABLE reporting_s01."x OWNER TO postgres; ALTER ROLE reporting_app_t SUPERUSER; --"`); e != nil {
			t.Fatal(e)
		}
		// What it cannot do, tried: DDL, TRUNCATE, DELETE, writing the ledger or the profile.
		for name, sql := range map[string]string{
			"create table": "CREATE TABLE reporting_s01.x(a int)",
			"alter table":  "ALTER TABLE reporting_s01.jobs ADD COLUMN x int",
			"truncate":     "TRUNCATE reporting_s01.jobs",
			"delete":       "DELETE FROM reporting_s01.jobs",
			"ledger":       "INSERT INTO reporting_s01.schema_migrations(version,name,checksum,applied_by) VALUES(9,'x','x','x')",
			"profile":      "UPDATE reporting_s01.profile SET version=9",
			"drop":         "DROP TABLE reporting_s01.assets",
		} {
			if _, e := appPool.Exec(ctx, sql); e == nil || !strings.Contains(e.Error(), "permission denied") && !strings.Contains(e.Error(), "must be owner") {
				t.Errorf("%s as the application role: %v", name, e)
			}
		}
		// With a step pending, the application role cannot apply it; the migrator (here the
		// superuser, which may alter the owner's tables) can.
		if e := postgres.Down(ctx, admin, 2, "test", false); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Up(ctx, appPool, 0, "app"); e == nil || !strings.Contains(e.Error(), "must be owner") && !strings.Contains(e.Error(), "permission denied") {
			t.Error("the application role could migrate", e)
		}
		if st := must(postgres.Status(ctx, admin)); st.Current() != 2 {
			t.Fatal("a failed step changed the ledger", st.Current())
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		// A table created by a migration the migrator role did not run (here the superuser) is
		// not covered by the migrator's default privileges: the role check says so, and the
		// roles step repairs it.
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) == 0 {
			t.Fatal("a table outside the grants passed", problems, e)
		}
		if e := postgres.Grants(ctx, admin, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		// The whole platform contract passes as the application role on the production path
		// (the test-only option lifts TLS and the fixture name; the profile row is production
		// for the duration).
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileProduction); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(context.Background(), "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileFixture)
		})
		if _, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: appDSN}); !errors.Is(e, r.ErrDenied) {
			t.Fatal("production store opened without TLS on a fixture database", e)
		}
		if _, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: os.Getenv("REPORTING_S01_TEST_DSN"), InsecureTestOnly: true}); !errors.Is(e, r.ErrDenied) {
			t.Fatal("production store opened as the superuser", e)
		}
		store, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: appDSN, InsecureTestOnly: true})
		if e != nil {
			t.Fatal(e)
		}
		defer store.Close()
		if e := store.ResetFixture(ctx); !errors.Is(e, r.ErrDenied) {
			t.Fatal("a production store can reset", e)
		}
		if _, e := admin.Exec(ctx, "TRUNCATE reporting_s01.assets, reporting_s01.jobs, reporting_s01.issue_numbers"); e != nil {
			t.Fatal(e)
		}
		s := issueDurableService(t, durableEnv{repo: store, source: v.source, p: v.p}, &stampIssuer{})
		env := durableEnv{s: s, repo: store, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		ex := durableExport(t, env, snap.SnapshotID, "export")
		if _, e := s.Download(ctx, v.p, ex.ArtifactID); e != nil {
			t.Fatal(e)
		}
		j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		if e != nil || j.Issue.Number != 1 {
			t.Fatal(j, e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if rec, e := s.Issue(ctx, v.p, j.ID); e != nil || rec.State != "succeeded" || rec.Pages != 3 {
			t.Fatalf("%+v %v", rec, e)
		}
		if c, e := s.Cancel(ctx, v.p, j.ID); e != nil || c.State != "succeeded" {
			t.Fatal(c, e)
		}
		// With the fixture profile row back, the production path refuses the database.
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		if _, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: appDSN, InsecureTestOnly: true}); !errors.Is(e, r.ErrDenied) {
			t.Fatal("production store opened on the fixture profile", e)
		}
	})
	t.Run("production_dsn_rules", func(t *testing.T) {
		for name, dsn := range map[string]string{
			"plaintext":    "postgresql://app@db.example/reporting?sslmode=disable",
			"prefer":       "postgresql://app@db.example/reporting?sslmode=prefer",
			"require":      "postgresql://app@db.example/reporting?sslmode=require",
			"verify-ca":    "postgresql://app@db.example/reporting?sslmode=verify-ca",
			"fixture name": "postgresql://app@db.example/reporting_test_s01?sslmode=verify-full",
			"fixture race": "postgresql://app@db.example/reporting_test_s01_race?sslmode=verify-full",
			"not a dsn":    "::",
		} {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: dsn})
			cancel()
			if !errors.Is(e, r.ErrDenied) && !errors.Is(e, r.ErrInvalid) {
				t.Errorf("%s: %v", name, e)
			}
		}
	})
	// The fixture is left as the other tests expect it.
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
}

func must(st postgres.State, e error) postgres.State {
	if e != nil {
		panic(e)
	}
	return st
}

func currentDatabase(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var name string
	if e := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&name); e != nil {
		t.Fatal(e)
	}
	return name
}
