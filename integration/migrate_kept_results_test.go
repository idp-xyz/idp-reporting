package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
)

// The two steps of S25-B1 (docs/S25B1_SCOPE.md) are found by their names, and their catalogs by their numbers: a step renumbered before it
// is merged needs its catalog file renamed and nothing here.

// stepNamed is the version of the embedded step of that name.
func stepNamed(t *testing.T, name string) (int, []postgres.Migration) {
	t.Helper()
	known, e := postgres.Migrations()
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range known {
		if m.Name == name {
			return m.Version, known
		}
	}
	t.Fatalf("no step is named %s", name)
	return 0, nil
}

// committedCatalog is the catalog committed for a version; with REPORTING_WRITE_CATALOG=1 (after a reviewed migration change only) it is
// first written from what the store has.
func committedCatalog(t *testing.T, version int, have string, write bool) string {
	t.Helper()
	name := fmt.Sprintf("testdata/catalog_%03d.txt", version)
	if write && os.Getenv("REPORTING_WRITE_CATALOG") == "1" {
		if e := os.WriteFile(name, []byte(have), 0o644); e != nil {
			t.Fatal(e)
		}
	}
	b, e := os.ReadFile(name)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

// restoreFixture leaves the fixture as the other tests expect it, at the latest version with its profile, however the test that changed
// the schema ended.
func restoreFixture(t *testing.T, admin *pgxpool.Pool) {
	t.Cleanup(func() {
		ctx := context.Background()
		if _, e := admin.Exec(ctx, "DROP SCHEMA IF EXISTS reporting_s01 CASCADE"); e != nil {
			t.Errorf("the fixture's schema could not be dropped: %v", e)
			return
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Errorf("the fixture could not be migrated again: %v", e)
			return
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Errorf("the fixture's profile could not be written again: %v", e)
		}
	})
}

// migrationKeptResults qualifies the step kept_results: a store at the version before it, with rows, upgrades keeping them; the index of
// the stored-results count holds the snapshots and the export files and no issued file, beside the index of 009, which the next step
// drops; the ledger of the sweeps gains its word on which sweeps reconciled, on the objects each left and on since when the bucket is
// settled: false, 0 and nothing for the rows written before; the catalog is the committed one. The down step gives back the catalog of the
// version before exactly and loses no row, and is refused while the ledger says either of a sweep, unless the loss is accepted. As 009,
// the step accepts its index made beforehand with CREATE INDEX CONCURRENTLY under the same name and definition and refuses anything else
// under the name, leaving the store as it was.
func migrationKeptResults(t *testing.T, admin *pgxpool.Pool) {
	ctx := context.Background()
	v, known := stepNamed(t, "kept_results")
	if v < 2 {
		t.Fatal("kept_results is the first step")
	}
	restoreFixture(t, admin)
	toBefore := func() {
		t.Helper()
		dropSchema(t, admin)
		if e := postgres.Up(ctx, admin, v-1, "test"); e != nil {
			t.Fatal(e)
		}
		if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
			t.Fatal(e)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := admin.Exec(ctx, sql, args...); e != nil {
			t.Fatal(sql, e)
		}
	}
	current := func() int { t.Helper(); return must(postgres.Status(ctx, admin)).Current() }
	const results, issued = "assets_identity_results", "assets_identity_kind_expires"
	hasIndex := func(name string) bool {
		t.Helper()
		return count(t, admin, "SELECT count(*) FROM pg_indexes WHERE schemaname='reporting_s01' AND indexname=$1", name) == 1
	}
	toBefore()
	before := catalog(t, admin)
	if before != committedCatalog(t, v-1, before, false) {
		t.Fatalf("the store at version %d does not have its committed catalog", v-1)
	}
	// Rows of one identity: a snapshot, an export file, the file of an issued document (its payload names its issue), the issue record; a job;
	// and two rows of the sweeps ledger, one of which counted orphans (it reconciled, and the ledger had no word for it yet).
	asset := func(id, kind, data, expires string) {
		t.Helper()
		var at any
		if expires != "" {
			at = expires
		}
		exec(`INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
VALUES($1,'t','i','x','y',$2,$3::jsonb,encode(sha256(convert_to($3::jsonb::text,'UTF8')),'hex'),clock_timestamp()+$4::interval)`, id, kind, data, at)
	}
	asset("a-snapshot", "snapshot", `{"ID":"a-snapshot"}`, "5 days")
	asset("an-export", "artifact", `{"ID":"an-export","SnapshotID":"a-snapshot"}`, "5 days")
	asset("an-issued-file", "artifact", `{"ID":"an-issued-file","SnapshotID":"a-snapshot","Issue":"an-issue"}`, "3650 days")
	asset("an-issue", "issue", `{"ID":"an-issue"}`, "")
	exec(rlsInsertJob, "a-job", "t", "x", "y")
	exec("INSERT INTO reporting_s01.sweeps(dry_run,complete,grace_seconds,batch,artifacts,snapshots,jobs,batches,objects,orphans) VALUES(false,true,86400,500,1,2,3,4,1,2),(true,true,86400,500,0,0,0,0,0,0)")
	st := must(postgres.Status(ctx, admin))
	if st.Current() != v-1 || len(st.Pending) != postgres.LatestVersion()-(v-1) || st.Pending[0] != v {
		t.Fatalf("%+v", st)
	}
	if plan, e := postgres.Plan(st, v, postgres.Upward); e != nil || len(plan) != 1 || plan[0] != fmt.Sprintf("up   %03d kept_results", v) {
		t.Fatal(plan, e)
	}
	// The step alone.
	if e := postgres.Up(ctx, admin, v, "test"); e != nil {
		t.Fatal(e)
	}
	st = must(postgres.Status(ctx, admin))
	if st.Current() != v || len(st.Pending) != postgres.LatestVersion()-v || len(st.Problems) != 0 || st.Applied[v-1].Checksum != known[v-1].Checksum || st.Applied[v-1].Name != "kept_results" {
		t.Fatalf("%+v", st)
	}
	at := catalog(t, admin)
	if at != committedCatalog(t, v, at, true) {
		t.Fatalf("the catalog at version %d differs from its committed catalog:\n%s", v, at)
	}
	for _, want := range []string{
		"index assets assets_identity_results CREATE INDEX assets_identity_results ON reporting_s01.assets USING btree (tenant, issuer, subject, expires_at) WHERE ((kind = ANY (ARRAY['snapshot'::text, 'artifact'::text])) AND (NOT (data ? 'Issue'::text)))",
		// The index of 009 is still there: the next step drops it, in a transaction of its own.
		"index assets assets_identity_kind_expires CREATE INDEX assets_identity_kind_expires ON reporting_s01.assets USING btree (tenant, issuer, subject, kind, expires_at)",
		"column sweeps.reconciled 13 boolean null=NO default=false",
		"column sweeps.objects_left 14 bigint null=NO default=0",
		"column sweeps.settled_from 15 timestamp with time zone null=YES default=\n",
		"constraint reporting_s01.sweeps sweeps_objects_left_check CHECK ((objects_left >= 0))",
		"constraint reporting_s01.sweeps sweeps_settled_from_check CHECK (((settled_from IS NULL) OR (reconciled AND complete AND (objects_left = 0) AND (NOT dry_run))))",
		"index sweeps sweeps_reconciliation CREATE INDEX sweeps_reconciliation ON reporting_s01.sweeps USING btree (COALESCE(settled_from, at), id) WHERE ((NOT dry_run) AND ((settled_from IS NOT NULL) OR (objects_left > 0) OR (NOT complete)))",
	} {
		if !strings.Contains(at, want) {
			t.Errorf("the catalog does not say %q:\n%s", want, at)
		}
	}
	// Nothing is lost, and the rows of the ledger written before the step say neither a reconciliation nor an object left: by them the
	// bucket is due, so the first sweep after the step reconciles, whatever interval it is given.
	if count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 4 || count(t, admin, "SELECT count(*) FROM reporting_s01.jobs") != 1 {
		t.Fatal("the upgrade lost rows")
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps WHERE NOT reconciled AND objects_left = 0 AND settled_from IS NULL") != 2 || count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps WHERE artifacts=1 AND orphans=2") != 1 {
		t.Fatal("the ledger rows written before the step are not carried over as they were, without a reconciliation, without an object left and without a word on the bucket being settled")
	}
	var settled bool
	if e := admin.QueryRow(ctx, postgres.BucketSettledForTests(), int64(30*24*3600)*1e6).Scan(&settled); e != nil || settled {
		t.Fatalf("by the ledger rows written before the step the bucket is settled (%v)", e)
	}
	if _, e := admin.Exec(ctx, "INSERT INTO reporting_s01.sweeps(dry_run,complete,grace_seconds,batch,artifacts,snapshots,jobs,batches,objects_left) VALUES(false,true,1,1,0,0,0,0,-1)"); e == nil || !strings.Contains(e.Error(), "sweeps_objects_left_check") {
		t.Fatal("a negative count of objects left was accepted:", e)
	}
	// What the index holds: the snapshot and the export, not the issued file, not the record. The predicate is read from the catalog,
	// as the store has it, and asked of the four rows.
	var predicate, held string
	if e := admin.QueryRow(ctx, `SELECT pg_get_expr(x.indpred, x.indrelid) FROM pg_index x JOIN pg_class c ON c.oid=x.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='reporting_s01' AND c.relname='assets_identity_results'`).Scan(&predicate); e != nil || predicate == "" {
		t.Fatal("the index of the stored results has no predicate:", e)
	}
	if e := admin.QueryRow(ctx, `SELECT coalesce(string_agg(id, ',' ORDER BY id COLLATE "C"), '') FROM reporting_s01.assets WHERE `+predicate).Scan(&held); e != nil || held != "a-snapshot,an-export" {
		t.Fatalf("the index of the stored results holds %q, want the snapshot and the export (%v)", held, e)
	}
	// And what the store counts for the identity, by its own statement: the same two.
	counted := false
	for _, statement := range postgres.CapacityStatementsForTests() {
		if statement.Name != "stored results" {
			continue
		}
		counted = true
		if n := count(t, admin, statement.SQL, statement.Args(r.Identity{Tenant: "t", Issuer: "x", Subject: "y"}, []string{"i"})...); n != 2 {
			t.Fatalf("the store counts %d stored results for the identity, want the snapshot and the export", n)
		}
	}
	if !counted {
		t.Fatal("the statement of the stored-results count was not found")
	}
	// Down with nothing the step would lose: no acceptance, the catalog of the version before exactly, the rows still there; up again, the
	// same as before.
	if e := postgres.Down(ctx, admin, v-1, "test", false); e != nil {
		t.Fatal("down with no reconciliation on record needs no acceptance:", e)
	}
	if catalog(t, admin) != before {
		t.Fatal("down does not give back the catalog of the version before")
	}
	if !hasIndex(issued) || hasIndex(results) {
		t.Fatal("down did not leave the index of 009 and take the step's own")
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 4 || count(t, admin, "SELECT count(*) FROM reporting_s01.jobs") != 1 || count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps") != 2 {
		t.Fatal("down lost rows")
	}
	if e := postgres.Up(ctx, admin, v, "test"); e != nil {
		t.Fatal(e)
	}
	if catalog(t, admin) != at {
		t.Fatal("the catalog differs after down and up")
	}
	// A ledger row that says a sweep reconciled, or that one left objects, is what the down step would lose: refused for either, and the
	// ledger of the schema stays.
	for _, c := range []struct{ name, row string }{
		{"a reconciliation on record", "INSERT INTO reporting_s01.sweeps(dry_run,complete,grace_seconds,batch,artifacts,snapshots,jobs,batches,reconciled) VALUES(false,true,86400,500,0,0,0,0,true)"},
		{"objects left on record", "INSERT INTO reporting_s01.sweeps(dry_run,complete,grace_seconds,batch,artifacts,snapshots,jobs,batches,objects_left) VALUES(false,true,86400,500,2,0,0,1,2)"},
	} {
		exec("DELETE FROM reporting_s01.sweeps WHERE reconciled OR objects_left > 0")
		exec(c.row)
		if e := postgres.Down(ctx, admin, v-1, "test", false); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "1 rows of the sweeps ledger say one or the other") || !strings.Contains(e.Error(), "--accept-data-loss") {
			t.Fatalf("down with %s accepted: %v", c.name, e)
		}
		if current() != v || catalog(t, admin) != at {
			t.Fatalf("with %s, a refused step changed the store", c.name)
		}
	}
	// Accepted: the catalog of the version before, and every row of the ledger with the counts it had.
	if e := postgres.Down(ctx, admin, v-1, "test", true); e != nil {
		t.Fatal(e)
	}
	if catalog(t, admin) != before || count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps") != 3 || count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps WHERE artifacts=1 AND orphans=2") != 1 || count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps WHERE artifacts=2 AND batches=1") != 1 {
		t.Fatal("the accepted down did not give back the catalog of the version before with the ledger's rows")
	}
	// The index made beforehand, as OPERATIONS.md "Quotas" says (outside any transaction, so CONCURRENTLY is allowed): the step finds it in
	// place, builds nothing, and the catalog is the same as when the step built it.
	exec("CREATE INDEX CONCURRENTLY assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE kind IN ('snapshot','artifact') AND NOT (data ? 'Issue')")
	oid := func() int {
		t.Helper()
		return count(t, admin, "SELECT c.oid::int FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='reporting_s01' AND c.relname='assets_identity_results'")
	}
	built := oid()
	if e := postgres.Up(ctx, admin, v, "test"); e != nil {
		t.Fatal("the step with its index made beforehand:", e)
	}
	if oid() != built {
		t.Fatal("the step rebuilt an index that was in place")
	}
	if catalog(t, admin) != at {
		t.Fatal("the catalog after the index made beforehand differs from the one the step builds")
	}
	// Not what the step would have made: refused with the name, and the store is as it was before the step was tried, the catalog and
	// the ledger of the schema alike (the ledger's new columns are not there either), so that the binary of the version before goes on.
	refused := func(name, ddl, message string) {
		t.Helper()
		toBefore()
		exec(ddl)
		tried := catalog(t, admin)
		e := postgres.Up(ctx, admin, v, "test")
		if e == nil || !strings.Contains(e.Error(), message) {
			t.Fatalf("%s: %v", name, e)
		}
		if current() != v-1 || catalog(t, admin) != tried || !hasIndex(issued) {
			t.Fatalf("%s: a refused step changed the store (version %d)", name, current())
		}
	}
	for name, ddl := range map[string]string{
		"not partial":              "CREATE INDEX assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at)",
		"the issued files left in": "CREATE INDEX assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE kind IN ('snapshot','artifact')",
		"the records left in":      "CREATE INDEX assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE NOT (data ? 'Issue')",
		// The payload's key and the kinds are text: in another case the index would never serve the count, or would hold nothing.
		"the key in another case": "CREATE INDEX assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE kind IN ('snapshot','artifact') AND NOT (data ? 'issue')",
		"a kind in another case":  "CREATE INDEX assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE kind IN ('Snapshot','artifact') AND NOT (data ? 'Issue')",
		"other columns":           "CREATE INDEX assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, kind, expires_at) WHERE kind IN ('snapshot','artifact') AND NOT (data ? 'Issue')",
		"unique":                  "CREATE UNIQUE INDEX assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE kind IN ('snapshot','artifact') AND NOT (data ? 'Issue')",
		"another order":           "CREATE INDEX assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at DESC) WHERE kind IN ('snapshot','artifact') AND NOT (data ? 'Issue')",
		"another table":           "CREATE INDEX assets_identity_results ON reporting_s01.jobs (tenant, issuer, subject)",
	} {
		refused(name, ddl, "assets_identity_results exists with another definition")
	}
	// The name taken by something of the schema that is no index: IF NOT EXISTS skips it too.
	refused("a table under the name", "CREATE TABLE reporting_s01.assets_identity_results (id text)", "assets_identity_results exists and is not an index of the schema")
	// An index a failed CONCURRENTLY build left invalid (a uniqueness violation makes one) is refused too: IF NOT EXISTS alone would keep it.
	toBefore()
	asset("one", "snapshot", `{"ID":"one"}`, "1 day")
	asset("two", "snapshot", `{"ID":"two"}`, "1 day")
	if _, e := admin.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY assets_identity_results ON reporting_s01.assets (tenant, issuer, subject)"); e == nil {
		t.Fatal("the build that should fail succeeded")
	}
	if !hasIndex(results) {
		t.Fatal("no invalid index was left")
	}
	if e := postgres.Up(ctx, admin, v, "test"); e == nil || !strings.Contains(e.Error(), "assets_identity_results exists and is invalid") {
		t.Fatal("an invalid index was kept:", e)
	}
	if current() != v-1 {
		t.Fatal("a refused step moved the ledger")
	}
	// That index also had another definition. An invalid index with exactly the step's definition is what a build that was interrupted
	// leaves: the definition says nothing against it, and PostgreSQL does not read an invalid index, so the count would go back to reading
	// payloads. The fixture's administrator marks the index as such a build would have left it.
	toBefore()
	exec("CREATE INDEX CONCURRENTLY assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE kind IN ('snapshot','artifact') AND NOT (data ? 'Issue')")
	exec("UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'reporting_s01.assets_identity_results'::regclass")
	tried := catalog(t, admin)
	if e := postgres.Up(ctx, admin, v, "test"); e == nil || !strings.Contains(e.Error(), "assets_identity_results exists and is invalid") {
		t.Fatal("an invalid index with the step's own definition was kept:", e)
	}
	if current() != v-1 || catalog(t, admin) != tried {
		t.Fatal("a refused step changed the store")
	}
}

// migrationResultsIndexCleanup qualifies the step stored_results_index_cleanup: it drops the index of 009 that the stored-results count
// read until kept_results, keeps every row and changes nothing else; its down builds the index again exactly as 009 made it, and fails,
// changing nothing, when something else stands under that name.
func migrationResultsIndexCleanup(t *testing.T, admin *pgxpool.Pool) {
	ctx := context.Background()
	w, known := stepNamed(t, "stored_results_index_cleanup")
	if kept, _ := stepNamed(t, "kept_results"); w != kept+1 {
		t.Fatalf("the index the count no longer reads is dropped by step %d, not by the step after kept_results (%d)", w, kept)
	}
	restoreFixture(t, admin)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := admin.Exec(ctx, sql, args...); e != nil {
			t.Fatal(sql, e)
		}
	}
	const results, issued = "assets_identity_results", "assets_identity_kind_expires"
	hasIndex := func(name string) bool {
		t.Helper()
		return count(t, admin, "SELECT count(*) FROM pg_indexes WHERE schemaname='reporting_s01' AND indexname=$1", name) == 1
	}
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, w-1, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
	before := catalog(t, admin)
	exec(`INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
VALUES('a-snapshot','t','i','x','y','snapshot','{"ID":"a-snapshot"}'::jsonb,encode(sha256(convert_to('{"ID":"a-snapshot"}'::jsonb::text,'UTF8')),'hex'),clock_timestamp()+interval '5 days')`)
	if !hasIndex(issued) || !hasIndex(results) {
		t.Fatal("before the step the store does not have both indexes")
	}
	if e := postgres.Up(ctx, admin, w, "test"); e != nil {
		t.Fatal(e)
	}
	st := must(postgres.Status(ctx, admin))
	if st.Current() != w || len(st.Problems) != 0 || st.Applied[w-1].Checksum != known[w-1].Checksum || st.Applied[w-1].Name != "stored_results_index_cleanup" {
		t.Fatalf("%+v", st)
	}
	at := catalog(t, admin)
	if at != committedCatalog(t, w, at, true) {
		t.Fatalf("the catalog at version %d differs from its committed catalog:\n%s", w, at)
	}
	// The one difference between the two catalogs is the index of 009.
	if hasIndex(issued) || !hasIndex(results) || strings.Contains(at, issued) {
		t.Fatal("the step did not drop the index of 009, or dropped the index of the count")
	}
	gone := ""
	for _, line := range strings.Split(before, "\n") {
		if !strings.Contains(at, line+"\n") {
			gone += line + "\n"
		}
	}
	if want := "index assets assets_identity_kind_expires CREATE INDEX assets_identity_kind_expires ON reporting_s01.assets USING btree (tenant, issuer, subject, kind, expires_at)\n"; gone != want || len(at) != len(before)-len(want) {
		t.Fatalf("the step changed more than the index of 009; lines gone:\n%s", gone)
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 1 {
		t.Fatal("the step lost a row")
	}
	// Dropped once more by hand (an operator who had dropped it already): the step does not mind. Down first, so that there is a step to apply.
	if e := postgres.Down(ctx, admin, w-1, "test", false); e != nil {
		t.Fatal("the down needs no acceptance, an index is derived from the rows:", e)
	}
	if catalog(t, admin) != before || count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 1 {
		t.Fatal("down does not give back the catalog of the version before, with the row")
	}
	exec("DROP INDEX reporting_s01.assets_identity_kind_expires")
	if e := postgres.Up(ctx, admin, w, "test"); e != nil {
		t.Fatal("the step on a store whose index of 009 was dropped already:", e)
	}
	if catalog(t, admin) != at {
		t.Fatal("the catalog differs when the index was dropped beforehand")
	}
	// Something else stands under the name of 009's index: the down fails and changes nothing, so the version it goes back to never has
	// another index under that name. Once it is out of the way, the down builds 009's own.
	exec("CREATE INDEX assets_identity_kind_expires ON reporting_s01.assets (tenant)")
	foreign := catalog(t, admin)
	if e := postgres.Down(ctx, admin, w-1, "test", false); e == nil || !strings.Contains(e.Error(), "already exists") {
		t.Fatal("the down kept or replaced an index that is not the one 009 made:", e)
	}
	if must(postgres.Status(ctx, admin)).Current() != w || catalog(t, admin) != foreign {
		t.Fatal("the down that failed changed the store")
	}
	exec("DROP INDEX reporting_s01.assets_identity_kind_expires")
	if e := postgres.Down(ctx, admin, w-1, "test", false); e != nil {
		t.Fatal(e)
	}
	if catalog(t, admin) != before {
		t.Fatal("the down does not build the index as 009 made it")
	}
}
