package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"os"
	"strings"
	"testing"
)

func capture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, &out)
	return out.String(), err
}

// Without a database: the usage, the DSN requirement and the argument checks.
func TestMigrateCommandArguments(t *testing.T) {
	t.Setenv("REPORTING_STORE_DSN", "")
	if _, err := capture(t); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatal(err)
	}
	if _, err := capture(t, "status"); err == nil || !strings.Contains(err.Error(), "REPORTING_STORE_DSN") {
		t.Fatal(err)
	}
	t.Setenv("REPORTING_STORE_DSN", "postgresql://nobody@127.0.0.1:1/none?sslmode=disable")
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"down without --to":    {[]string{"down"}, "explicit --to"},
		"roles without roles":  {[]string{"roles"}, "--migrator-role"},
		"roles bad name":       {[]string{"roles", "--migrator-role", "Robert'; DROP", "--app-role", "x"}, "INVALID_REQUEST"},
		"init-profile fixture": {[]string{"init-profile", "fixture"}, "init-profile takes"},
		"init-profile none":    {[]string{"init-profile"}, "init-profile takes"},
		"unknown":              {[]string{"frobnicate"}, "usage"},
		"timeout zero":         {[]string{"status", "--timeout", "0"}, "--timeout must be positive"},
		"sweep without grace":  {[]string{"sweep", "--grace", "0"}, "--grace and --batch must be positive"},
		"sweep negative grace": {[]string{"sweep", "--grace", "-1h"}, "--grace and --batch must be positive"},
		"sweep without batch":  {[]string{"sweep", "--batch", "0"}, "--grace and --batch must be positive"},
		"sweep role as app":    {[]string{"roles", "--migrator-role", "a", "--app-role", "b", "--sweep-role", "b"}, "--sweep-role must be"},
		"sweep role as owner":  {[]string{"roles", "--migrator-role", "a", "--app-role", "b", "--sweep-role", "a"}, "--sweep-role must be"},
		"sweep role bad name":  {[]string{"roles", "--migrator-role", "a", "--app-role", "b", "--sweep-role", "Robert'; DROP"}, "--sweep-role must be"},
	} {
		if _, err := capture(t, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	t.Setenv("REPORTING_STORE_DSN", "::")
	if _, err := capture(t, "status"); !errors.Is(err, r.ErrInvalid) {
		t.Fatal(err)
	}
}

// TestMigrateCommand drives every subcommand against the guarded fixture database.
func TestMigrateCommand(t *testing.T) {
	dsn := os.Getenv("REPORTING_S01_TEST_DSN")
	if dsn == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required PostgreSQL DSN missing")
		}
		t.Skip("PostgreSQL gate not run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close) // registered first: the role cleanups below run before it
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS reporting_s01 CASCADE"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPORTING_STORE_DSN", dsn)
	expect := func(args []string, wantErr bool, contains ...string) string {
		t.Helper()
		out, err := capture(t, args...)
		if (err != nil) != wantErr {
			t.Fatalf("%v: err=%v out=%s", args, err, out)
		}
		for _, c := range contains {
			if !strings.Contains(out, c) && (err == nil || !strings.Contains(err.Error(), c)) {
				t.Fatalf("%v: %q not in output %q / error %v", args, c, out, err)
			}
		}
		return out
	}
	out := expect([]string{"status"}, false, "pending:  001 base", "pending:  003 issues", "pending:  004 source_confirmed_issue", "pending:  005 retention", "pending:  006 sweeps", "pending:  007 row_level_security")
	if strings.Contains(out, "applied:") {
		t.Fatal(out)
	}
	expect([]string{"plan"}, false, "up   001 base", "up   003 issues", "up   004 source_confirmed_issue", "up   005 retention", "up   006 sweeps", "up   007 row_level_security")
	expect([]string{"up", "--to", "2"}, false, "at version 002")
	expect([]string{"init-profile", "contract-simulator-only"}, false, "profile contract-simulator-only")
	expect([]string{"init-profile", "production"}, true, "already")
	// roles at version 2: only the tables that exist are granted.
	for _, role := range []string{"reporting_cli_app_t", "reporting_cli_mig_t"} {
		if _, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+role+"; CREATE ROLE "+role); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(context.Background(), "REASSIGN OWNED BY "+role+" TO postgres; DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role)
		})
	}
	expect([]string{"roles", "--migrator-role", "reporting_cli_mig_t", "--app-role", "reporting_cli_app_t"}, false, "owned by reporting_cli_mig_t")
	expect([]string{"up"}, false, "at version 007")
	out = expect([]string{"status"}, false, "applied:  001 base", "applied:  003 issues", "applied:  004 source_confirmed_issue", "applied:  005 retention", "applied:  006 sweeps", "applied:  007 row_level_security", "the connected role is not the application role", "the connected role is not the sweep role")
	if strings.Contains(out, "pending:") {
		t.Fatal(out)
	}
	// Row-level security (S04-C) is part of what status checks about the connected role: an administrator's DISABLE ROW LEVEL SECURITY or a
	// dropped policy leaves the ledger intact, and status names the table.
	for _, c := range []struct{ break_, fix, want string }{
		{"ALTER TABLE reporting_s01.jobs DISABLE ROW LEVEL SECURITY", "ALTER TABLE reporting_s01.jobs ENABLE ROW LEVEL SECURITY", "row-level security is not enabled on jobs"},
		{"DROP POLICY identity ON reporting_s01.assets", `CREATE POLICY identity ON reporting_s01.assets AS PERMISSIVE FOR ALL TO PUBLIC USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), '')) WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))`, "policy identity is missing on assets"},
	} {
		if _, err := admin.Exec(ctx, c.break_); err != nil {
			t.Fatal(err)
		}
		expect([]string{"status"}, false, c.want)
		if _, err := admin.Exec(ctx, c.fix); err != nil {
			t.Fatal(err)
		}
		if out := expect([]string{"status"}, false, "the connected role is not the application role"); strings.Contains(out, "row-level security") || strings.Contains(out, "policy ") {
			t.Fatal("a restored store still reports row-level problems:", out)
		}
	}
	expect([]string{"plan", "--to", "1"}, false, "down 007 row_level_security", "down 006 sweeps", "down 005 retention", "down 004 source_confirmed_issue", "down 003 issues", "down 002 payload_digests")
	expect([]string{"down"}, true, "explicit --to")
	expect([]string{"down", "--to", "8"}, true, "outside")
	expect([]string{"up", "--to", "1"}, true, "wrong side")
	expect([]string{"down", "--to", "2"}, false, "at version 002")
	expect([]string{"plan"}, false, "up   003 issues", "up   004 source_confirmed_issue", "up   005 retention", "up   006 sweeps", "up   007 row_level_security")
	// 004 up is refused while S03-G's platform-numbered documents exist, with the remedy; the
	// operator removes them with the down that accepts the loss and goes up.
	expect([]string{"up", "--to", "3"}, false, "at version 003")
	if _, err := admin.Exec(ctx, `INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,data,data_sha256) VALUES('11111111111111111111111111111111','t','i','x','y','issue','succeeded','k','h',now(),'{"Number":1}','0')`); err != nil {
		t.Fatal(err)
	}
	expect([]string{"up"}, true, "migration 004 up", "1 issue jobs and records exist", "reporting-migrate down --to 2 --accept-data-loss")
	expect([]string{"status"}, false, "applied:  003 issues", "pending:  004 source_confirmed_issue")
	expect([]string{"down", "--to", "2"}, true, "--accept-data-loss")
	expect([]string{"down", "--to", "2", "--accept-data-loss"}, false, "at version 002")
	expect([]string{"up"}, false, "at version 007")
	if _, err := admin.Exec(ctx, "UPDATE reporting_s01.schema_migrations SET checksum='0' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(t, "status"); !errors.Is(err, r.ErrIntegrity) {
		t.Fatal("a tampered ledger passed status", err)
	}
	expect([]string{"up"}, true, "not usable")
	if _, err := admin.Exec(ctx, "DROP SCHEMA reporting_s01 CASCADE"); err != nil {
		t.Fatal(err)
	}
	expect([]string{"up"}, false, "at version 007")
	expect([]string{"init-profile", "contract-simulator-only"}, false)
	retention(t, admin, dsn, expect)
}

// retention drives the operator's sweep (S04-B) on rows seeded by SQL: a dry run that counts and changes nothing, the sweep in batches of
// one, its counts, the same sweep again (nothing left), what it kept, the ledger, and the roles: the sweep role runs it, the application role
// is refused, a store behind the binary's version is refused.
func retention(t *testing.T, admin *pgxpool.Pool, dsn string, expect func([]string, bool, ...string) string) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A store behind the binary is refused before anything is touched.
	expect([]string{"down", "--to", "5"}, false, "at version 005")
	expect([]string{"sweep"}, true, "reporting-migrate up")
	expect([]string{"up"}, false, "at version 007")
	for _, role := range []string{"reporting_cli_sweep_t"} {
		exec("DROP ROLE IF EXISTS " + role + "; CREATE ROLE " + role + " LOGIN")
		t.Cleanup(func() {
			_, _ = admin.Exec(context.Background(), "REASSIGN OWNED BY "+role+" TO postgres; DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role)
		})
	}
	expect([]string{"roles", "--migrator-role", "reporting_cli_mig_t", "--app-role", "reporting_cli_app_t", "--sweep-role", "reporting_cli_sweep_t"}, false, "reporting_cli_sweep_t may read every table, delete expired assets, reduce finished jobs and write the sweeps ledger, nothing else")
	// Rows: a snapshot, an artifact and the jobs of a finished run 48 hours past their expiry; an issue record; a snapshot 1 hour past its
	// expiry (inside the grace), one still valid, one with no readable expiry, one past its grace but named by a running job together
	// with an artifact of it; a corrupt job payload past its grace.
	asset := func(id, kind, data, offset string) {
		t.Helper()
		var at any
		if offset != "" {
			at = offset
		}
		exec(`INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
VALUES($1,'t','i','x','y',$2,$3::jsonb,encode(sha256(convert_to($3::jsonb::text,'UTF8')),'hex'),clock_timestamp()+$4::interval)`, id, kind, data, at)
	}
	job := func(id, state, snapshot, artifact, offset string) {
		t.Helper()
		exec(`INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,snapshot_id,artifact_id,data,data_sha256)
VALUES($1,'t','i','x','y','snapshot',$2,$1,'h',clock_timestamp()+$3::interval,$4,$5,'{"ID":"kept","Query":{"filters":"secret"},"Prepared":{"Ref":"r"},"Columns":[{"id":"c"}],"Template":"t"}',
encode(sha256(convert_to('{"ID":"kept","Query":{"filters":"secret"},"Prepared":{"Ref":"r"},"Columns":[{"id":"c"}],"Template":"t"}'::jsonb::text,'UTF8')),'hex'))`, id, state, offset, snapshot, artifact)
	}
	asset("s-old", "snapshot", `{"ID":"s-old"}`, "-48 hours")
	asset("a-old", "artifact", `{"SnapshotID":"s-old"}`, "-48 hours")
	asset("r-issue", "issue", `{"ID":"r-issue"}`, "")
	asset("s-grace", "snapshot", `{"ID":"s-grace"}`, "-1 hour")
	asset("s-valid", "snapshot", `{"ID":"s-valid"}`, "1 hour")
	asset("s-null", "snapshot", `{"ID":"s-null"}`, "")
	asset("s-named", "snapshot", `{"ID":"s-named"}`, "-48 hours")
	asset("a-named", "artifact", `{"SnapshotID":"s-named"}`, "-48 hours")
	job("j-done", "succeeded", "s-old", "", "-48 hours")
	job("j-failed", "failed", "", "", "-48 hours")
	job("j-valid", "succeeded", "s-valid", "", "1 hour")
	job("j-running", "running", "s-named", "", "-48 hours")
	job("j-corrupt", "failed", "", "", "-48 hours")
	exec(`UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{ID}','"tampered"') WHERE id='j-corrupt'`)
	count := func(sql string) (n int) {
		t.Helper()
		if err := admin.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	held := func() int {
		return count("SELECT (SELECT count(*) FROM reporting_s01.assets)*1000 + (SELECT count(*) FROM reporting_s01.jobs WHERE data ? 'Reduced')")
	}
	before := held()
	out := expect([]string{"sweep", "--dry-run"}, false, "dry run, would sweep: 1 artifacts, 1 snapshots, 2 job payloads reduced (grace 24h0m0s, batch 500, 0 transactions)")
	if held() != before {
		t.Fatal("a dry run changed the store", out)
	}
	if n := count("SELECT count(*) FROM reporting_s01.sweeps WHERE dry_run AND complete AND artifacts=1 AND snapshots=1 AND jobs=2 AND batches=0 AND grace_seconds=86400 AND batch=500"); n != 1 {
		t.Fatal("the dry run left no ledger row", n)
	}
	expect([]string{"sweep", "--batch", "1"}, false, "swept: 1 artifacts, 1 snapshots, 2 job payloads reduced (grace 24h0m0s, batch 1, 4 transactions)")
	if n := count("SELECT count(*) FROM reporting_s01.assets WHERE id IN ('s-old','a-old')"); n != 0 {
		t.Fatal("the expired snapshot or artifact is still there", n)
	}
	if n := count("SELECT count(*) FROM reporting_s01.assets WHERE id IN ('r-issue','s-grace','s-valid','s-null','s-named','a-named')"); n != 6 {
		t.Fatal("something that must be kept was deleted", n)
	}
	if n := count("SELECT count(*) FROM reporting_s01.jobs WHERE data ? 'Reduced' AND NOT data ? 'Query' AND NOT data ? 'Prepared' AND NOT data ? 'Columns' AND data->>'ID'='kept' AND data->>'Template'='t' AND data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') AND id IN ('j-done','j-failed')"); n != 2 {
		t.Fatal("the finished jobs are not reduced as they should be", n)
	}
	if n := count("SELECT count(*) FROM reporting_s01.jobs WHERE NOT data ? 'Reduced' AND id IN ('j-valid','j-running','j-corrupt')"); n != 3 {
		t.Fatal("a valid, a running or a corrupt job was reduced", n)
	}
	// Again: nothing is left to do, and the ledger says so.
	expect([]string{"sweep"}, false, "swept: 0 artifacts, 0 snapshots, 0 job payloads reduced (grace 24h0m0s, batch 500, 0 transactions)")
	expect([]string{"sweep", "--dry-run"}, false, "dry run, would sweep: 0 artifacts, 0 snapshots, 0 job payloads reduced")
	if n := count("SELECT count(*) FROM reporting_s01.sweeps"); n != 4 {
		t.Fatal("the ledger has one row per sweep, dry runs included", n)
	}
	// The sweep role runs it and the application role is refused; neither sees more than counts.
	as := func(role string) string { return strings.Replace(dsn, "postgres@", role+"@", 1) }
	exec("ALTER ROLE reporting_cli_app_t LOGIN")
	t.Setenv("REPORTING_STORE_DSN", as("reporting_cli_sweep_t"))
	expect([]string{"status"}, false, "sweeper:  the connected role satisfies the sweep role's rules")
	expect([]string{"sweep"}, false, "swept: 0 artifacts")
	t.Setenv("REPORTING_STORE_DSN", as("reporting_cli_app_t"))
	expect([]string{"sweep"}, true, "the application role never deletes")
	t.Setenv("REPORTING_STORE_DSN", dsn)
}
