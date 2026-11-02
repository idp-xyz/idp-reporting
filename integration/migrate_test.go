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

// catalog is a textual snapshot of the platform schema: tables, columns, constraints, indexes. A column's position is its rank among the
// columns the table has, not its attribute number: a column dropped by a down step (005's) leaves a hole in the numbers, which an up step
// does not fill, and the schema is still the same.
func catalog(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var out strings.Builder
	rows, e := pool.Query(ctx, `SELECT c.relname, a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod), CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END,
coalesce(pg_get_expr(d.adbin, d.adrelid), ''), row_number() OVER (PARTITION BY c.relname ORDER BY a.attnum)
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
	// Row-level security (007): a table's flags are printed only where one is set, and a policy where one exists, so that the catalogs of
	// 003 to 006, which have neither, are what they were.
	rows, e = pool.Query(ctx, `SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='reporting_s01' AND c.relkind='r' AND (c.relrowsecurity OR c.relforcerowsecurity) ORDER BY 1`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var table string
		var enabled, forced bool
		if e := rows.Scan(&table, &enabled, &forced); e != nil {
			t.Fatal(e)
		}
		fmt.Fprintf(&out, "rowsecurity %s enabled=%v forced=%v\n", table, enabled, forced)
	}
	rows.Close()
	rows, e = pool.Query(ctx, `SELECT tablename, policyname, cmd, permissive, roles::text, coalesce(qual,''), coalesce(with_check,'') FROM pg_policies WHERE schemaname='reporting_s01' ORDER BY 1,2`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var table, name, cmd, permissive, roles, qual, check string
		if e := rows.Scan(&table, &name, &cmd, &permissive, &roles, &qual, &check); e != nil {
			t.Fatal(e)
		}
		fmt.Fprintf(&out, "policy %s %s %s permissive=%s roles=%s using=%s check=%s\n", table, name, cmd, permissive, roles, qual, check)
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
	if e != nil || len(known) != 9 || known[0].Name != "base" || known[2].Name != "issues" || known[3].Name != "source_confirmed_issue" || known[4].Name != "retention" || known[5].Name != "sweeps" || known[6].Name != "row_level_security" || known[7].Name != "object_storage" || known[8].Name != "capacity_indexes" {
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
		if !strings.Contains(first, "constraint reporting_s01.jobs jobs_kind_check CHECK ((kind = ANY (ARRAY['snapshot'::text, 'export'::text, 'issue'::text])))") || strings.Contains(first, "issue_numbers") || !strings.Contains(first, "column schema_migrations.checksum") {
			t.Fatalf("unexpected catalog:\n%s", first)
		}
		// The catalogs of the latest version (9), of 8, 7, 6, 5, 4 and 3 are committed: a later edit of an
		// earlier migration file (which the checksums would also flag) changes a fresh database and
		// fails here.
		if os.Getenv("REPORTING_WRITE_CATALOG") == "1" { // after a reviewed migration change only
			if e := os.WriteFile("testdata/catalog_009.txt", []byte(first), 0o644); e != nil {
				t.Fatal(e)
			}
		}
		want, e := os.ReadFile("testdata/catalog_009.txt")
		if e != nil {
			t.Fatal(e)
		}
		if string(want) != first {
			t.Fatalf("the catalog differs from testdata/catalog_009.txt:\n%s", first)
		}
		if !strings.Contains(first, "column assets.expires_at") || !strings.Contains(first, "index assets assets_expires_at") || !strings.Contains(first, "column sweeps.artifacts") {
			t.Fatalf("the retention schema (005, 006) is not in the catalog:\n%s", first)
		}
		st, e := postgres.Status(ctx, admin)
		if e != nil || st.Current() != 9 || len(st.Pending) != 0 || len(st.Problems) != 0 || st.Legacy {
			t.Fatalf("%+v %v", st, e)
		}
		for _, a := range st.Applied {
			if a.Checksum != known[a.Version-1].Checksum || a.AppliedBy != "test" || a.AppliedAt.IsZero() {
				t.Fatalf("ledger row %+v", a)
			}
		}
		// Down to zero and up again: the catalog is byte for byte the same, the profile is gone
		// and back at 4. The profile row counts as data: 001 down needs the acceptance.
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
		// Versions 7, 6, 5 and 4 are committed too: each down gives back exactly the catalog it left, 008 the schema without the two counts of
		// the object store (007's), 007 the schema without the policies (006's), 006 the schema without the ledger of the sweeps, 005 the
		// schema without the expiry column and its index.
		for _, v := range []int{7, 6, 5, 4} {
			if e := postgres.Down(ctx, admin, v, "test", false); e != nil {
				t.Fatal(v, "down with no rows to lose needs no acceptance:", e)
			}
			name := fmt.Sprintf("testdata/catalog_%03d.txt", v)
			at := catalog(t, admin)
			if os.Getenv("REPORTING_WRITE_CATALOG") == "1" {
				if e := os.WriteFile(name, []byte(at), 0o644); e != nil {
					t.Fatal(e)
				}
			}
			if want, e := os.ReadFile(name); e != nil || string(want) != at {
				t.Fatalf("the catalog at version %d differs from %s (%v):\n%s", v, name, e, at)
			}
		}
		// Version 3 is where S03-G left the schema, and its catalog is committed as well: 004 down
		// must give it back exactly, the counters table as 003 created it, empty, and 004 up must
		// take it away again.
		if e := postgres.Down(ctx, admin, 3, "test", false); e != nil {
			t.Fatal("004 down with no issue rows needs no acceptance:", e)
		}
		at3 := catalog(t, admin)
		if os.Getenv("REPORTING_WRITE_CATALOG") == "1" {
			if e := os.WriteFile("testdata/catalog_003.txt", []byte(at3), 0o644); e != nil {
				t.Fatal(e)
			}
		}
		want3, e := os.ReadFile("testdata/catalog_003.txt")
		if e != nil || string(want3) != at3 || !strings.Contains(at3, "column issue_numbers.last") {
			t.Fatalf("the catalog at version 3 differs from testdata/catalog_003.txt (%v):\n%s", e, at3)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if catalog(t, admin) != first {
			t.Fatal("up from 3 to 4 did not give the catalog of 4")
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
		if plan, e = postgres.Plan(must(postgres.Status(ctx, admin)), 4, postgres.Upward); e != nil || len(plan) != 2 || plan[1] != "up   004 source_confirmed_issue" {
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
	})
	t.Run("refusals_checksum_newer_binary_gap_and_data_loss", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		if _, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		// Down over 004 would discard the issue job and its record (an S03-G binary could not read
		// them: the number is the product's string) and leave the issued PDF in the store without its
		// record, which makes it undownloadable: refused with all three named; down to 2 meets that
		// refusal first and stays at 4.
		e := postgres.Down(ctx, admin, 3, "test", false)
		if !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "2 issue jobs and records") || !strings.Contains(e.Error(), "S03-G binary could not read them") || !strings.Contains(e.Error(), "1 issued PDFs would lose their records: they would stay in the store but could no longer be downloaded, and would expire with their snapshots") || strings.Contains(e.Error(), "plain artifact") {
			t.Fatal("down with issue rows accepted or counted wrongly", e)
		}
		if e := postgres.Down(ctx, admin, 2, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "migration 004 down") {
			t.Fatal("down over 004 with issue rows accepted", e)
		}
		if st := must(postgres.Status(ctx, admin)); st.Current() != 4 {
			t.Fatal(st)
		}
		// A record alone (its job gone) is data as well.
		if _, e := admin.Exec(ctx, "DELETE FROM reporting_s01.jobs WHERE kind='issue'"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, admin, 3, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "1 issue jobs and records") {
			t.Fatal("down with a record alone accepted", e)
		}
		if e := postgres.Down(ctx, admin, 3, "test", true); e != nil {
			t.Fatal(e)
		}
		var n, issued, issueRows int
		if e := admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM reporting_s01.jobs), (SELECT count(*) FROM reporting_s01.assets WHERE kind='artifact' AND data ? 'Issue'), (SELECT count(*) FROM reporting_s01.jobs WHERE kind='issue')+(SELECT count(*) FROM reporting_s01.assets WHERE kind='issue')").Scan(&n, &issued, &issueRows); e != nil || n < 1 || issued != 1 || issueRows != 0 {
			t.Fatal("the snapshot job and the issued PDF must survive 004 down, the issue job and record must not", n, issued, issueRows, e)
		}
		// Version 3 has the counters table again, empty. S03-G's counters are data there: a counter
		// alone is enough to refuse 003 down, because the numbering would restart.
		if _, e := admin.Exec(ctx, "INSERT INTO reporting_s01.issue_numbers(tenant,integration,last) VALUES('platform_demo','brant-contract-demo',3)"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, admin, 2, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "1 issue counters") || !strings.Contains(e.Error(), "restart at 1") || !strings.Contains(e.Error(), "1 issued PDFs would lose their records") {
			t.Fatal("down with a counter alone accepted", e)
		}
		if st := must(postgres.Status(ctx, admin)); st.Current() != 3 {
			t.Fatal(st)
		}
		if e := postgres.Down(ctx, admin, 2, "test", true); e != nil {
			t.Fatal(e)
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
			{"newer binary", "INSERT INTO reporting_s01.schema_migrations(version,name,checksum,applied_by) VALUES(10,'future','0','x')", "DELETE FROM reporting_s01.schema_migrations WHERE version=10", "version 10 was applied by a newer binary"},
			{"gap", "DELETE FROM reporting_s01.schema_migrations WHERE version=2", "INSERT INTO reporting_s01.schema_migrations(version,name,checksum,applied_by) VALUES(2,'payload_digests','" + known[1].Checksum + "','test')", "gap before version 3"},
			{"other name", "UPDATE reporting_s01.schema_migrations SET name='other' WHERE version=3", "UPDATE reporting_s01.schema_migrations SET name='issues' WHERE version=3", "under another name"},
			{"fake legacy", "UPDATE reporting_s01.schema_migrations SET checksum='legacy' WHERE version=3", "UPDATE reporting_s01.schema_migrations SET checksum='" + known[2].Checksum + "' WHERE version=3", "claims to be a legacy row"},
			{"profile stale", "UPDATE reporting_s01.profile SET version=2", "UPDATE reporting_s01.profile SET version=9", "profile says version 2"},
			{"other name of 004", "UPDATE reporting_s01.schema_migrations SET name='other' WHERE version=4", "UPDATE reporting_s01.schema_migrations SET name='source_confirmed_issue' WHERE version=4", "under another name"},
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
	// S03-H, migration 004: the platform's issue counters go. S03-G's documents were numbered by the
	// platform and never confirmed by a source product, so while any exists 004 up is refused, whatever
	// the operator accepts; the remedy it names is the down that accepts losing them.
	t.Run("source_confirmed_issue_step_refuses_platform_numbered_documents", func(t *testing.T) {
		dropSchema(t, admin)
		if e := postgres.Up(ctx, admin, 3, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		at3 := catalog(t, admin)
		// A step whose DDL fails leaves version 3 as it was: the counters table cannot be dropped
		// while something depends on it.
		if _, e := admin.Exec(ctx, "CREATE VIEW reporting_s01.counters AS SELECT * FROM reporting_s01.issue_numbers"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e == nil || !strings.Contains(e.Error(), "depend") {
			t.Fatal("a table with a dependent did not fail 004", e)
		}
		var profileVersion int
		if e := admin.QueryRow(ctx, "SELECT version FROM reporting_s01.profile").Scan(&profileVersion); e != nil || profileVersion != 3 || must(postgres.Status(ctx, admin)).Current() != 3 || catalog(t, admin) != at3 {
			t.Fatal("a failed 004 changed something", profileVersion, e)
		}
		if _, e := admin.Exec(ctx, "DROP VIEW reporting_s01.counters"); e != nil {
			t.Fatal(e)
		}
		// S03-G's documents as that binary stored them, numbered by the platform: an issue job, an
		// issue record, and the counter that numbered them.
		const id = "11111111111111111111111111111111"
		job := `INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,data,data_sha256) VALUES('` + id + `','t','i','x','y','issue','succeeded','k','h',now(),'{"Number":1}','0')`
		record := `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256) VALUES('` + id + `','t','i','x','y','issue','{"Number":1}','0')`
		refused := func(what string, count int) {
			t.Helper()
			e := postgres.Up(ctx, admin, 0, "test")
			if !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "migration 004 up") || !strings.Contains(e.Error(), fmt.Sprintf("%d issue jobs and records exist", count)) || !strings.Contains(e.Error(), "reporting-migrate down --to 2 --accept-data-loss") {
				t.Fatalf("004 up with %s: %v", what, e)
			}
			var version int
			if e := admin.QueryRow(ctx, "SELECT version FROM reporting_s01.profile").Scan(&version); e != nil || version != 3 || must(postgres.Status(ctx, admin)).Current() != 3 || catalog(t, admin) != at3 {
				t.Fatalf("a refused 004 changed something with %s: %d %v", what, version, e)
			}
		}
		if _, e := admin.Exec(ctx, job); e != nil {
			t.Fatal(e)
		}
		refused("a job", 1)
		if _, e := admin.Exec(ctx, "DELETE FROM reporting_s01.jobs"); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, record); e != nil {
			t.Fatal(e)
		}
		refused("a record", 1)
		if _, e := admin.Exec(ctx, job); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, "INSERT INTO reporting_s01.issue_numbers(tenant,integration,last) VALUES('t','i',1)"); e != nil {
			t.Fatal(e)
		}
		refused("a job and a record", 2)
		// The remedy it names: the down that accepts the loss (refused without the acceptance, with
		// the rows and the counters named), then the up.
		if e := postgres.Down(ctx, admin, 2, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "2 issue jobs and records and 1 issue counters") {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, admin, 2, "test", true); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal("004 up after the documents were removed:", e)
		}
		var rows int
		if e := admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM reporting_s01.jobs WHERE kind='issue')+(SELECT count(*) FROM reporting_s01.assets WHERE kind='issue')").Scan(&rows); e != nil || rows != 0 {
			t.Fatal(rows, e)
		}
		want8, e := os.ReadFile("testdata/catalog_009.txt")
		if e != nil || string(want8) != catalog(t, admin) {
			t.Fatalf("the catalog after the remedy differs from testdata/catalog_009.txt (%v)", e)
		}
		// Documents numbered by a source product (S03-H) do not block it: they are what the
		// migration leaves in place; and with them the migrator refuses nothing on the way up again.
		if e := postgres.Verify(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
	})
	// S04-B, migrations 005 and 006: the expiry column is backfilled from the payload and never refuses to run; the down step keeps every
	// row as it was; the ledger of the sweeps is audit, and its down step is refused while it has rows.
	t.Run("retention_steps_backfill_the_expiry_never_refuse_and_roll_back_without_loss", func(t *testing.T) {
		dropSchema(t, admin)
		if e := postgres.Up(ctx, admin, 4, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		at4 := catalog(t, admin)
		// The expiries as the store wrote them (Go's RFC 3339 text, with up to nine fractional digits and any zone) and what a payload can
		// be besides: no expiry, text that is not a time, a time that looks like one and is not, a number, and a payload that is no object.
		type row struct{ id, kind, data, want string }
		good := []row{
			{"s1", "snapshot", `{"ExpiresAt":"2026-10-28T06:13:33.123456789Z"}`, "2026-10-28 06:13:33.123456+00"},
			{"s2", "snapshot", `{"ExpiresAt":"2026-10-28T06:13:33Z"}`, "2026-10-28 06:13:33+00"},
			{"a1", "artifact", `{"SnapshotID":"s1","ExpiresAt":"2026-10-28T14:13:33.5+08:00"}`, "2026-10-28 06:13:33.5+00"},
			{"a2", "artifact", `{"SnapshotID":"s2","ExpiresAt":"2026-10-28T06:13:33.9999999Z"}`, "2026-10-28 06:13:33.999999+00"}, // cut, not rounded: the store's driver truncates
			{"s0", "snapshot", `{"ExpiresAt":"0001-01-01T00:00:00Z"}`, "0001-01-01 00:00:00+00"},
		}
		bad := []row{
			{"b1", "snapshot", `{"ID":"b1"}`, ""},
			{"b2", "snapshot", `{"ExpiresAt":"not a time"}`, ""},
			{"b3", "artifact", `{"ExpiresAt":17}`, ""},
			{"b4", "snapshot", `[]`, ""},
			{"b5", "issue", `{"ExpiresAt":"2026-10-28T06:13:33Z"}`, ""}, // a record never expires, whatever its payload says
		}
		insert := func(rows ...row) {
			t.Helper()
			for _, v := range rows {
				if _, e := admin.Exec(ctx, `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256) VALUES($1,'t','i','x','y',$2,$3::jsonb,encode(sha256(convert_to($3::jsonb::text,'UTF8')),'hex'))`, v.id, v.kind, v.data); e != nil {
					t.Fatal(e)
				}
			}
		}
		payloads := func() string {
			t.Helper()
			var out string
			if e := admin.QueryRow(ctx, `SELECT coalesce(string_agg(id||data::text||data_sha256||created_at::text, '|' ORDER BY id),'') FROM reporting_s01.assets`).Scan(&out); e != nil {
				t.Fatal(e)
			}
			return out
		}
		check := func(rows ...row) {
			t.Helper()
			for _, v := range rows {
				var got *string
				if e := admin.QueryRow(ctx, `SELECT expires_at::text FROM reporting_s01.assets WHERE id=$1`, v.id).Scan(&got); e != nil {
					t.Fatal(e)
				}
				if v.want == "" && got != nil || v.want != "" && (got == nil || *got != v.want) {
					t.Fatalf("%s %s: expires_at %v, want %q", v.id, v.data, got, v.want)
				}
			}
		}
		// Valid expiries alone: the set-based step does it. With a time that looks like one and is not, the step falls back to the rows one
		// by one: the others are still backfilled, the bad one stays NULL, and neither refuses the migration.
		for round, extra := range [][]row{nil, {{"b6", "snapshot", `{"ExpiresAt":"2026-02-30T00:00:00Z"}`, ""}, {"b7", "artifact", `{"ExpiresAt":"2026-13-01T00:00:00Z"}`, ""}}} {
			if round == 1 {
				if e := postgres.Down(ctx, admin, 4, "test", false); e != nil {
					t.Fatal("005 down loses nothing and needs no acceptance:", e)
				}
			}
			insert(good...)
			insert(bad...)
			insert(extra...)
			before := payloads()
			if e := postgres.Up(ctx, admin, 5, "test"); e != nil {
				t.Fatalf("005 up was refused or failed with rows it cannot read (round %d): %v", round, e)
			}
			check(good...)
			check(bad...)
			check(extra...)
			if payloads() != before {
				t.Fatal("005 up changed a payload, a digest or a row's time of creation")
			}
			if e := postgres.Down(ctx, admin, 4, "test", false); e != nil {
				t.Fatal("005 down loses nothing and needs no acceptance:", e)
			}
			if payloads() != before || catalog(t, admin) != at4 {
				t.Fatal("005 down changed the rows or did not give back the catalog of version 4")
			}
			if _, e := admin.Exec(ctx, "TRUNCATE reporting_s01.assets"); e != nil {
				t.Fatal(e)
			}
		}
		// The index is there for the sweep, and the sweeps ledger (006) is audit: its down step is refused while it has rows.
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		var indexed bool
		if e := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='reporting_s01' AND tablename='assets' AND indexdef LIKE '%(expires_at)')`).Scan(&indexed); e != nil || !indexed {
			t.Fatal("no index on assets(expires_at)", e)
		}
		if _, e := admin.Exec(ctx, "INSERT INTO reporting_s01.sweeps(dry_run,complete,grace_seconds,batch,artifacts,snapshots,jobs,batches) VALUES(false,true,86400,500,1,2,3,4)"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.Down(ctx, admin, 5, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "1 rows of the sweeps ledger") || !strings.Contains(e.Error(), "--accept-data-loss") {
			t.Fatal("006 down with a ledger row accepted", e)
		}
		if st := must(postgres.Status(ctx, admin)); st.Current() != 6 {
			t.Fatal("a refused step moved the ledger", st.Current())
		}
		if e := postgres.Down(ctx, admin, 5, "test", true); e != nil {
			t.Fatal(e)
		}
		if want5, e := os.ReadFile("testdata/catalog_005.txt"); e != nil || string(want5) != catalog(t, admin) {
			t.Fatalf("006 down did not give back the catalog of version 5 (%v)", e)
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if want8, e := os.ReadFile("testdata/catalog_009.txt"); e != nil || string(want8) != catalog(t, admin) {
			t.Fatalf("the catalog after the round trip differs from testdata/catalog_009.txt (%v)", e)
		}
	})
	t.Run("object_storage_step_adds_the_two_counts_and_refuses_to_lose_what_they_say", func(t *testing.T) { migration008(t, admin) })
	t.Run("capacity_indexes_step_adds_three_indexes_and_accepts_the_ones_made_beforehand", func(t *testing.T) { migration009(t, admin) })
	t.Run("capacity_plans_use_the_new_indexes_under_row_level_security", func(t *testing.T) { capacityPlans(t, admin) })
	t.Run("concurrent_migrators_serialize_on_the_lock", func(t *testing.T) {
		// Several rounds on a pool whose connections have seen the schema absent (a negative
		// catalog cache entry): every migrator must end at version 9 with one ledger.
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
			if st.Current() != 9 || len(st.Applied) != 9 || len(st.Problems) != 0 {
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
		// A database at profile version 2 made before S04-A: no ledger, no issue tables (the
		// counters table that 003 made is not there at the latest version either).
		if _, e := admin.Exec(ctx, `DROP POLICY identity ON reporting_s01.jobs; DROP POLICY worker ON reporting_s01.jobs; DROP POLICY identity ON reporting_s01.assets; DROP POLICY worker ON reporting_s01.assets;
ALTER TABLE reporting_s01.jobs DISABLE ROW LEVEL SECURITY; ALTER TABLE reporting_s01.assets DISABLE ROW LEVEL SECURITY;
DROP TABLE reporting_s01.sweeps; DROP INDEX reporting_s01.assets_expires_at; ALTER TABLE reporting_s01.assets DROP COLUMN expires_at;
DROP TABLE reporting_s01.schema_migrations;
ALTER TABLE reporting_s01.jobs DROP CONSTRAINT jobs_kind_check; ALTER TABLE reporting_s01.jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('snapshot','export'));
ALTER TABLE reporting_s01.assets DROP CONSTRAINT assets_kind_check; ALTER TABLE reporting_s01.assets ADD CONSTRAINT assets_kind_check CHECK(kind IN ('snapshot','artifact'));
UPDATE reporting_s01.profile SET version=2`); e != nil {
			t.Fatal(e)
		}
		st := must(postgres.Status(ctx, admin))
		if !st.Legacy || st.Current() != 2 || len(st.Pending) != 7 || len(st.Problems) != 0 {
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
		if st.Legacy || st.Current() != 9 || st.Applied[0].Checksum != "legacy" || st.Applied[1].Checksum != "legacy" || st.Applied[2].Checksum != known[2].Checksum || st.Applied[3].Checksum != known[3].Checksum || st.Applied[5].Checksum != known[5].Checksum || st.Applied[6].Checksum != known[6].Checksum || st.Applied[7].Checksum != known[7].Checksum {
			t.Fatalf("%+v", st)
		}
		// The realistic pre-S04-A database: the old fixture at version 3 (S03-G's, with its counters
		// table) without a ledger. The server refuses it for the right reason; initialising records
		// three legacy rows and applies 004.
		if _, e := admin.Exec(ctx, `DROP POLICY identity ON reporting_s01.jobs; DROP POLICY worker ON reporting_s01.jobs; DROP POLICY identity ON reporting_s01.assets; DROP POLICY worker ON reporting_s01.assets;
ALTER TABLE reporting_s01.jobs DISABLE ROW LEVEL SECURITY; ALTER TABLE reporting_s01.assets DISABLE ROW LEVEL SECURITY;
DROP TABLE reporting_s01.sweeps; DROP INDEX reporting_s01.assets_expires_at; ALTER TABLE reporting_s01.assets DROP COLUMN expires_at;
DROP TABLE reporting_s01.schema_migrations;
CREATE TABLE reporting_s01.issue_numbers (
  tenant text NOT NULL, integration text NOT NULL, last bigint NOT NULL CHECK(last >= 0),
  PRIMARY KEY(tenant,integration)
);
UPDATE reporting_s01.profile SET version=3`); e != nil {
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
		if st.Legacy || st.Current() != 9 || st.Applied[2].Checksum != "legacy" || !strings.HasPrefix(st.Applied[2].AppliedBy, "legacy:") || st.Applied[3].Checksum != known[3].Checksum || st.Applied[3].AppliedBy != "fixture" {
			t.Fatalf("%+v", st)
		}
		if want8, e := os.ReadFile("testdata/catalog_009.txt"); e != nil || string(want8) != catalog(t, admin) {
			t.Fatalf("the bootstrapped database does not have the catalog of version 9 (%v)", e)
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
		// At version 3 (S03-G's) the counters table is a known table and a data table: roles accepts
		// it; 004 then drops it, as the migrator role that owns it.
		if e := postgres.Up(ctx, migPool, 3, "migrator"); e != nil {
			t.Fatal("the migrator role cannot migrate to 3", e)
		}
		if e := postgres.Grants(ctx, migPool, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal("roles at version 3", e)
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
		// A table created by a migration the migrator role did not run (here the superuser: version
		// 3 makes S03-G's counters table) is not covered by the migrator's default privileges: the
		// role check says so, and the roles step repairs it, the counters table being a data table
		// where it exists.
		if e := postgres.Up(ctx, admin, 3, "test"); e != nil {
			t.Fatal(e)
		}
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) == 0 {
			t.Fatal("a table outside the grants passed", problems, e)
		}
		if e := postgres.Grants(ctx, admin, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) != 0 {
			t.Fatal("roles at version 3 left problems", problems, e)
		}
		var counters bool
		if e := admin.QueryRow(ctx, "SELECT has_table_privilege('reporting_app_t','reporting_s01.issue_numbers','INSERT') AND has_table_privilege('reporting_app_t','reporting_s01.issue_numbers','UPDATE') AND NOT has_table_privilege('reporting_app_t','reporting_s01.issue_numbers','DELETE')").Scan(&counters); e != nil || !counters {
			t.Fatal("the application role cannot write the counters table at version 3, or can delete from it", counters, e)
		}
		// At the latest version the table is gone, and the role check does not miss it. The sweeps ledger (006) was created here by
		// the superuser, not by the migrator whose default privileges would have given the application role SELECT: the role check says so
		// until roles is run, which is the documented rule for every up that creates a table.
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) == 0 || !strings.Contains(strings.Join(problems, ";"), "no SELECT on sweeps") {
			t.Fatal("a sweeps ledger outside the grants passed", problems, e)
		}
		if e := postgres.Grants(ctx, admin, "reporting_migrator_t", "reporting_app_t"); e != nil {
			t.Fatal(e)
		}
		if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) != 0 {
			t.Fatal("a store at the latest version reports problems for the missing counters table", problems, e)
		}
		retentionRoles(t, admin, appPool, appDSN)
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
		if _, e := admin.Exec(ctx, "TRUNCATE reporting_s01.assets, reporting_s01.jobs"); e != nil {
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
		if e != nil || j.Issue.Number != "" {
			t.Fatal(j, e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if rec, e := s.Issue(ctx, v.p, j.ID); e != nil || rec.State != "succeeded" || rec.Pages != 3 || rec.Number != "AR-000001" {
			t.Fatalf("%+v %v", rec, e)
		}
		if c, e := s.Cancel(ctx, v.p, j.ID); e != nil || c.State != "succeeded" {
			t.Fatal(c, e)
		}
		// The application role never deletes (A3): the sweep it is asked for is refused before anything is touched, with the way out,
		// and a dry run too, which needs to write the ledger; the data is as it was.
		var before int
		if e := admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM reporting_s01.assets)+(SELECT count(*) FROM reporting_s01.jobs)+(SELECT count(*) FROM reporting_s01.sweeps)").Scan(&before); e != nil {
			t.Fatal(e)
		}
		for _, dry := range []bool{false, true} {
			if res, e := s.Sweep(ctx, r.SweepOptions{DryRun: dry}); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "the application role never deletes") || res.Artifacts+res.Snapshots+res.Jobs != 0 {
				t.Fatal("the application role swept:", dry, res, e)
			}
		}
		var after int
		if e := admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM reporting_s01.assets)+(SELECT count(*) FROM reporting_s01.jobs)+(SELECT count(*) FROM reporting_s01.sweeps)").Scan(&after); e != nil || after != before {
			t.Fatal("a refused sweep changed the store", before, after, e)
		}
		// The passwords of the roles are rotated (S04-H H3): the production opener and the sweep, with the proofs of what stays up.
		credentialRotation(t, admin, v, snap.ID)
		// With the fixture profile row back, the production path refuses the database.
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
		if _, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: appDSN, InsecureTestOnly: true}); !errors.Is(e, r.ErrDenied) {
			t.Fatal("production store opened on the fixture profile", e)
		}
	})
	t.Run("row_level_security", func(t *testing.T) { rowLevelSecurity(t, admin) })
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

// retentionRoles qualifies the sweep role of S04-B on the roles of the migration qualification: it is the one role that deletes, and exactly
// that; the application role keeps no DELETE; the migrator can sweep too.
func retentionRoles(t *testing.T, admin, appPool *pgxpool.Pool, appDSN string) {
	t.Helper()
	ctx := context.Background()
	const role = "reporting_sweep_t"
	if _, e := admin.Exec(ctx, "DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role); e != nil && !strings.Contains(e.Error(), "does not exist") {
		t.Fatal(e)
	}
	if _, e := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role)
	})
	for _, bad := range [][3]string{{"reporting_migrator_t", "reporting_app_t", "reporting_app_t"}, {"reporting_migrator_t", "reporting_app_t", "reporting_migrator_t"}, {"reporting_migrator_t", "reporting_app_t", "Robert'; DROP"}, {"reporting_migrator_t", "reporting_app_t", ""}} {
		if e := postgres.GrantSweeper(ctx, admin, bad[0], bad[1], bad[2]); !errors.Is(e, r.ErrInvalid) {
			t.Fatal("the sweep role must be a third role with a plain name:", bad, e)
		}
	}
	if e := postgres.GrantSweeper(ctx, admin, "reporting_migrator_t", "reporting_app_t", role); e != nil {
		t.Fatal(e)
	}
	if e := postgres.GrantSweeper(ctx, admin, "reporting_migrator_t", "reporting_app_t", role); e != nil {
		t.Fatal("the sweep role's grants are not idempotent", e)
	}
	sweepDSN := strings.Replace(os.Getenv("REPORTING_S01_TEST_DSN"), "postgres@", role+"@", 1)
	sweepPool, e := pgxpool.New(ctx, sweepDSN)
	if e != nil {
		t.Fatal(e)
	}
	defer sweepPool.Close()
	if problems, e := postgres.SweeperProblems(ctx, sweepPool); e != nil || len(problems) != 0 {
		t.Fatalf("the sweep role has problems: %v %v", problems, e)
	}
	// Each role is judged by its own rules: the sweep role is not the application role (it deletes, it cannot write jobs), the application
	// role is not the sweep role, the superuser is neither, and granting the sweep role changed nothing for the application role.
	if problems, e := postgres.RoleProblems(ctx, sweepPool); e != nil || !strings.Contains(strings.Join(problems, ";"), "DELETE or TRUNCATE on assets") || !strings.Contains(strings.Join(problems, ";"), "no INSERT and UPDATE on jobs") {
		t.Fatal("the sweep role passes as the application role", problems, e)
	}
	if problems, e := postgres.SweeperProblems(ctx, appPool); e != nil || !strings.Contains(strings.Join(problems, ";"), "no DELETE on assets") || !strings.Contains(strings.Join(problems, ";"), "no INSERT on sweeps") || !strings.Contains(strings.Join(problems, ";"), "INSERT or UPDATE on assets") || !strings.Contains(strings.Join(problems, ";"), "INSERT or UPDATE on jobs") {
		t.Fatal("the application role passes as the sweep role", problems, e)
	}
	if problems, e := postgres.SweeperProblems(ctx, admin); e != nil || !strings.Contains(strings.Join(problems, ";"), "superuser") {
		t.Fatal("the superuser passes as the sweep role", problems, e)
	}
	if problems, e := postgres.RoleProblems(ctx, appPool); e != nil || len(problems) != 0 {
		t.Fatal("granting the sweep role changed the application role", problems, e)
	}
	var appDeletes bool
	if e := admin.QueryRow(ctx, "SELECT has_table_privilege('reporting_app_t','reporting_s01.assets','DELETE') OR has_table_privilege('reporting_app_t','reporting_s01.jobs','DELETE') OR has_table_privilege('reporting_app_t','reporting_s01.sweeps','INSERT')").Scan(&appDeletes); e != nil || appDeletes {
		t.Fatal("the application role can delete or write the ledger", e)
	}
	// What the sweep role can do, and what it cannot, tried: it deletes assets, updates jobs and writes the ledger; it cannot insert an
	// asset or a job, delete a job or a ledger row, truncate, alter, or rewrite the ledger of the migrations. (It can delete an issue
	// record at the privilege level: the table-level DELETE does not see the kind. What keeps the sweep from it is the sweep's own SQL,
	// which names the kinds, and the tests of it.)
	for name, c := range map[string]struct {
		sql string
		ok  bool
	}{
		"delete an asset":            {"DELETE FROM reporting_s01.assets WHERE id='nothing'", true},
		"update a job":               {"UPDATE reporting_s01.jobs SET error_code=error_code WHERE id='nothing'", true},
		"write the ledger":           {"INSERT INTO reporting_s01.sweeps(dry_run,complete,grace_seconds,batch,artifacts,snapshots,jobs,batches) VALUES(true,true,1,1,0,0,0,0)", true},
		"read the ledger":            {"SELECT count(*) FROM reporting_s01.sweeps", true},
		"insert an asset":            {"INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256) VALUES('x','x','x','x','x','snapshot','{}','x')", false},
		"update an asset":            {"UPDATE reporting_s01.assets SET kind=kind WHERE id='nothing'", true}, // PostgreSQL requires it for the row locks of FOR UPDATE SKIP LOCKED
		"insert a job":               {"INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,data,data_sha256) VALUES('x','x','x','x','x','snapshot','queued','x','x',now(),'{}','x')", false},
		"delete a job":               {"DELETE FROM reporting_s01.jobs WHERE id='nothing'", false},
		"delete a ledger row":        {"DELETE FROM reporting_s01.sweeps", false},
		"update the ledger":          {"UPDATE reporting_s01.sweeps SET batches=0", false},
		"truncate":                   {"TRUNCATE reporting_s01.assets", false},
		"drop":                       {"DROP TABLE reporting_s01.assets", false},
		"alter":                      {"ALTER TABLE reporting_s01.assets ADD COLUMN x int", false},
		"write the migration ledger": {"DELETE FROM reporting_s01.schema_migrations", false},
		"write the profile":          {"UPDATE reporting_s01.profile SET version=9", false},
	} {
		_, e := sweepPool.Exec(ctx, c.sql)
		denied := e != nil && (strings.Contains(e.Error(), "permission denied") || strings.Contains(e.Error(), "must be owner"))
		if c.ok && e != nil || !c.ok && !denied {
			t.Errorf("the sweep role, %s: %v (allowed: %v)", name, e, c.ok)
		}
	}
	// A sweep on this role needs no more, and a sweep as the sweep role against an empty store counts nothing and leaves its ledger row.
	res, e := postgres.Sweep(ctx, sweepPool, r.SweepOptions{})
	if e != nil || res.Artifacts+res.Snapshots+res.Jobs != 0 {
		t.Fatal("the sweep role cannot sweep", res, e)
	}
	// The migrator, which owns the tables, sweeps without the role.
	migPool, e := pgxpool.New(ctx, strings.Replace(os.Getenv("REPORTING_S01_TEST_DSN"), "postgres@", "reporting_migrator_t@", 1))
	if e != nil {
		t.Fatal(e)
	}
	defer migPool.Close()
	if _, e := postgres.Sweep(ctx, migPool, r.SweepOptions{DryRun: true}); e != nil {
		t.Fatal("the migrator cannot sweep", e)
	}
	// The application role cannot, even on a dry run, which writes the ledger.
	for _, dry := range []bool{false, true} {
		if _, e := postgres.Sweep(ctx, appPool, r.SweepOptions{DryRun: dry}); !errors.Is(e, r.ErrDenied) {
			t.Fatal("the application role swept", dry, e)
		}
	}
	// roles repairs a sweep role whose table was created later: a ledger it cannot write is a problem until roles is run again.
	if _, e := admin.Exec(ctx, "REVOKE INSERT ON reporting_s01.sweeps FROM "+role); e != nil {
		t.Fatal(e)
	}
	if problems, e := postgres.SweeperProblems(ctx, sweepPool); e != nil || len(problems) != 1 || problems[0] != "no INSERT on sweeps" {
		t.Fatal(problems, e)
	}
	if _, e := postgres.Sweep(ctx, sweepPool, r.SweepOptions{}); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "INSERT on sweeps") {
		t.Fatal("a sweep without the ledger ran", e)
	}
	if e := postgres.GrantSweeper(ctx, admin, "reporting_migrator_t", "reporting_app_t", role); e != nil {
		t.Fatal(e)
	}
	if problems, e := postgres.SweeperProblems(ctx, sweepPool); e != nil || len(problems) != 0 {
		t.Fatal("grants did not repair the sweep role", problems, e)
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

// migration008 qualifies the step of S04-E (docs/S04E_SCOPE.md E3): a store at version 7 with rows upgrades keeping them, the two counts of the
// sweeps ledger default to 0, the catalog is the committed one, and the down step gives back the catalog of 007 exactly; it is refused while a
// ledger row says objects were deleted or an artifact's file is an object (an older binary could not serve it), unless the loss is accepted.
func migration008(t *testing.T, admin *pgxpool.Pool) {
	ctx := context.Background()
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 7, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
	if want, e := os.ReadFile("testdata/catalog_007.txt"); e != nil || string(want) != catalog(t, admin) {
		t.Fatalf("the store at version 7 does not have the catalog of 007 (%v)", e)
	}
	if _, e := admin.Exec(ctx, "INSERT INTO reporting_s01.sweeps(dry_run,complete,grace_seconds,batch,artifacts,snapshots,jobs,batches) VALUES(false,true,86400,500,1,2,3,4)"); e != nil {
		t.Fatal(e)
	}
	st := must(postgres.Status(ctx, admin))
	if st.Current() != 7 || len(st.Pending) != 2 || st.Pending[0] != 8 || st.Pending[1] != 9 {
		t.Fatalf("%+v", st)
	}
	if plan, e := postgres.Plan(st, 8, postgres.Upward); e != nil || len(plan) != 1 || plan[0] != "up   008 object_storage" {
		t.Fatal(plan, e)
	}
	// The step alone: 009 (S04-G) is applied on top in migration009.
	if e := postgres.Up(ctx, admin, 8, "test"); e != nil {
		t.Fatal(e)
	}
	known, _ := postgres.Migrations()
	st = must(postgres.Status(ctx, admin))
	if st.Current() != 8 || len(st.Pending) != 1 || len(st.Problems) != 0 || st.Applied[7].Checksum != known[7].Checksum || st.Applied[7].Name != "object_storage" {
		t.Fatalf("%+v", st)
	}
	at8 := catalog(t, admin)
	if want, e := os.ReadFile("testdata/catalog_008.txt"); e != nil || string(want) != at8 {
		t.Fatalf("the catalog at version 8 differs from testdata/catalog_008.txt (%v):\n%s", e, at8)
	}
	for _, want := range []string{"column sweeps.objects 11 bigint null=NO default=0", "column sweeps.orphans 12 bigint null=NO default=0", "constraint reporting_s01.sweeps sweeps_objects_check CHECK ((objects >= 0))", "constraint reporting_s01.sweeps sweeps_orphans_check CHECK ((orphans >= 0))"} {
		if !strings.Contains(at8, want) {
			t.Errorf("the catalog does not say %q:\n%s", want, at8)
		}
	}
	// The row written before the step has no objects and no orphans, and is otherwise as it was.
	if count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps WHERE artifacts=1 AND snapshots=2 AND jobs=3 AND batches=4 AND objects=0 AND orphans=0") != 1 {
		t.Fatal("the ledger row of version 7 is not carried over with 0 objects and 0 orphans")
	}
	if _, e := admin.Exec(ctx, "INSERT INTO reporting_s01.sweeps(dry_run,complete,grace_seconds,batch,artifacts,snapshots,jobs,batches,objects) VALUES(false,true,1,1,0,0,0,0,-1)"); e == nil {
		t.Fatal("a negative count was accepted")
	}
	// Down with nothing the step would lose: no acceptance, the catalog of 007 exactly, the ledger row still there; up again, the same as before.
	if e := postgres.Down(ctx, admin, 7, "test", false); e != nil {
		t.Fatal("008 down with nothing to lose needs no acceptance:", e)
	}
	if want, e := os.ReadFile("testdata/catalog_007.txt"); e != nil || string(want) != catalog(t, admin) {
		t.Fatalf("down from 008 does not give the catalog of 007 (%v)", e)
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps") != 1 {
		t.Fatal("008 down lost the ledger row")
	}
	if e := postgres.Up(ctx, admin, 8, "test"); e != nil {
		t.Fatal(e)
	}
	if catalog(t, admin) != at8 {
		t.Fatal("the catalog differs after down and up")
	}
	// A ledger row that counts objects deleted is audit the step would lose.
	if _, e := admin.Exec(ctx, "UPDATE reporting_s01.sweeps SET objects=2, orphans=1"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.Down(ctx, admin, 7, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "1 rows of the sweeps ledger that count objects deleted") || !strings.Contains(e.Error(), "--accept-data-loss") {
		t.Fatal("008 down with a ledger row that counts objects accepted:", e)
	}
	if st := must(postgres.Status(ctx, admin)); st.Current() != 8 {
		t.Fatal("a refused step moved the ledger", st.Current())
	}
	if _, e := admin.Exec(ctx, "UPDATE reporting_s01.sweeps SET objects=0, orphans=0"); e != nil {
		t.Fatal(e)
	}
	// An artifact whose file is an object cannot be read by an older binary: the step is refused while one exists, and the row stays when the
	// loss is accepted (it expires and is swept; its object waits for a reconciliation).
	if _, e := admin.Exec(ctx, `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at) VALUES('0123456789abcdef0123456789abcdef','t','i','x','y','artifact','{"Object":"artifacts/0123456789abcdef0123456789abcdef"}','x',now())`); e != nil {
		t.Fatal(e)
	}
	if e := postgres.Down(ctx, admin, 7, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "1 artifacts whose files are in the object store") {
		t.Fatal("008 down with an artifact whose file is an object accepted:", e)
	}
	if e := postgres.Down(ctx, admin, 7, "test", true); e != nil {
		t.Fatal(e)
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 1 {
		t.Fatal("008 down deleted the artifact")
	}
	if want, e := os.ReadFile("testdata/catalog_007.txt"); e != nil || string(want) != catalog(t, admin) {
		t.Fatalf("down from 008 does not give the catalog of 007 (%v)", e)
	}
	if _, e := admin.Exec(ctx, "TRUNCATE reporting_s01.assets"); e != nil {
		t.Fatal(e)
	}
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
}

// migration009 qualifies the step of S04-G (docs/S04G_SCOPE.md G1): a store at version 8 with rows upgrades keeping them, the catalog is the
// committed one, the down step gives back the catalog of 008 exactly and loses no row; and the step accepts the indexes an operator made
// beforehand with CREATE INDEX CONCURRENTLY under the same names and definitions (IF NOT EXISTS), and refuses one that is not what it would
// have made: another definition under the name, or an index left invalid by a build that failed.
func migration009(t *testing.T, admin *pgxpool.Pool) {
	ctx := context.Background()
	names := []string{"assets_identity_kind_expires", "jobs_identity_created", "jobs_identity_in_flight", "jobs_reducible"}
	indexes := func() []string {
		rows, e := admin.Query(ctx, "SELECT indexname FROM pg_indexes WHERE schemaname='reporting_s01' AND indexname IN ('assets_identity_kind_expires','jobs_identity_created','jobs_identity_in_flight','jobs_reducible') ORDER BY 1")
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			if e := rows.Scan(&n); e != nil {
				t.Fatal(e)
			}
			out = append(out, n)
		}
		return out
	}
	toEight := func() {
		dropSchema(t, admin)
		if e := postgres.Up(ctx, admin, 8, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
	}
	toEight()
	if want, e := os.ReadFile("testdata/catalog_008.txt"); e != nil || string(want) != catalog(t, admin) {
		t.Fatalf("the store at version 8 does not have the catalog of 008 (%v)", e)
	}
	if _, e := admin.Exec(ctx, rlsInsertJob, "old-job", "t", "x", "y"); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(ctx, rlsInsertAsset, "old-asset", "t", "x", "y"); e != nil {
		t.Fatal(e)
	}
	st := must(postgres.Status(ctx, admin))
	if st.Current() != 8 || len(st.Pending) != 1 || st.Pending[0] != 9 {
		t.Fatalf("%+v", st)
	}
	if plan, e := postgres.Plan(st, 9, postgres.Upward); e != nil || len(plan) != 1 || plan[0] != "up   009 capacity_indexes" {
		t.Fatal(plan, e)
	}
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	known, _ := postgres.Migrations()
	st = must(postgres.Status(ctx, admin))
	if st.Current() != 9 || len(st.Pending) != 0 || len(st.Problems) != 0 || st.Applied[8].Checksum != known[8].Checksum || st.Applied[8].Name != "capacity_indexes" {
		t.Fatalf("%+v", st)
	}
	at9 := catalog(t, admin)
	if want, e := os.ReadFile("testdata/catalog_009.txt"); e != nil || string(want) != at9 {
		t.Fatalf("the catalog at version 9 differs from testdata/catalog_009.txt (%v):\n%s", e, at9)
	}
	if got := indexes(); strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatal(got)
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.jobs") != 1 || count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 1 {
		t.Fatal("the upgrade lost rows")
	}
	// Down: the catalog of 008 exactly, the rows still there, no acceptance (an index is derived); up again, the same as before.
	if e := postgres.Down(ctx, admin, 8, "test", false); e != nil {
		t.Fatal("009 down needs no acceptance:", e)
	}
	if want, e := os.ReadFile("testdata/catalog_008.txt"); e != nil || string(want) != catalog(t, admin) {
		t.Fatalf("down from 009 does not give the catalog of 008 (%v)", e)
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.jobs") != 1 || count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 1 {
		t.Fatal("009 down lost rows")
	}
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	if catalog(t, admin) != at9 {
		t.Fatal("the catalog differs after down and up")
	}
	// The indexes made beforehand, as OPERATIONS.md "Capacity" says (outside any transaction, so CONCURRENTLY is allowed): the step finds them
	// in place, builds nothing, and the catalog is the same as when the step built them.
	toEight()
	for _, ddl := range []string{
		"CREATE INDEX CONCURRENTLY jobs_identity_created ON reporting_s01.jobs (tenant, issuer, subject, created_at DESC)",
		"CREATE INDEX CONCURRENTLY jobs_identity_in_flight ON reporting_s01.jobs (tenant, issuer, subject) WHERE state IN ('queued','running')",
		"CREATE INDEX CONCURRENTLY assets_identity_kind_expires ON reporting_s01.assets (tenant, issuer, subject, kind, expires_at)",
		"CREATE INDEX CONCURRENTLY jobs_reducible ON reporting_s01.jobs (source_expires_at) WHERE state IN ('succeeded','failed','cancelled') AND NOT (data ? 'Reduced')",
	} {
		if _, e := admin.Exec(ctx, ddl); e != nil {
			t.Fatal(ddl, e)
		}
	}
	oids := func() string {
		var out string
		if e := admin.QueryRow(ctx, "SELECT string_agg(c.oid::text, ',' ORDER BY c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='reporting_s01' AND c.relname IN ('assets_identity_kind_expires','jobs_identity_created','jobs_identity_in_flight','jobs_reducible')").Scan(&out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	before := oids()
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal("009 with the indexes made beforehand:", e)
	}
	if oids() != before {
		t.Fatal("the step rebuilt an index that was in place")
	}
	if catalog(t, admin) != at9 {
		t.Fatal("the catalog after the out-of-band indexes differs from the one the step builds")
	}
	// Not what the step would have made: refused, with the index named, and the ledger stays at 8.
	for name, c := range map[string]struct{ ddl, want string }{
		"another definition":    {"CREATE INDEX jobs_identity_created ON reporting_s01.jobs (tenant, issuer, subject, created_at)", "jobs_identity_created exists with another definition"},
		"another table":         {"CREATE INDEX assets_identity_kind_expires ON reporting_s01.jobs (tenant, issuer, subject)", "assets_identity_kind_expires exists with another definition"},
		"reducible not partial": {"CREATE INDEX jobs_reducible ON reporting_s01.jobs (source_expires_at)", "jobs_reducible exists with another definition"},
		"not partial":           {"CREATE INDEX jobs_identity_in_flight ON reporting_s01.jobs (tenant, issuer, subject)", "jobs_identity_in_flight exists with another definition"},
	} {
		toEight()
		if _, e := admin.Exec(ctx, c.ddl); e != nil {
			t.Fatal(name, e)
		}
		e := postgres.Up(ctx, admin, 0, "test")
		if e == nil || !strings.Contains(e.Error(), c.want) {
			t.Fatalf("%s: %v", name, e)
		}
		if st := must(postgres.Status(ctx, admin)); st.Current() != 8 {
			t.Fatalf("%s: a refused step moved the ledger to %d", name, st.Current())
		}
		if got := indexes(); len(got) != 1 {
			t.Fatalf("%s: the refused step left indexes: %v", name, got)
		}
	}
	// An index a failed CONCURRENTLY build left invalid (a uniqueness violation makes one) is refused too: IF NOT EXISTS alone would keep it.
	toEight()
	if _, e := admin.Exec(ctx, `INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,data,data_sha256)
SELECT 'dup-'||g,'t','i','x','y','snapshot','queued','k'||g,'h',now(),'{}','x' FROM generate_series(1,2) g`); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY jobs_identity_created ON reporting_s01.jobs (tenant, issuer, subject)"); e == nil {
		t.Fatal("the build that should fail succeeded")
	}
	if got := indexes(); strings.Join(got, ",") != "jobs_identity_created" {
		t.Fatal("no invalid index was left:", got)
	}
	if e := postgres.Up(ctx, admin, 0, "test"); e == nil || !strings.Contains(e.Error(), "jobs_identity_created exists and is invalid") {
		t.Fatal("an invalid index was kept:", e)
	}
	// Leave the fixture as the other tests expect it.
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
}
