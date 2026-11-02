package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/internal/pgauthproxy"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"net/url"
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
		// The keys of the object store are the environment's alone (S04-E), refused before the store is even opened.
		"secret key as a flag": {[]string{"sweep", "--object-store-secret-key=SECRETVALUE"}, "REPORTING_OBJECT_STORE_SECRET_KEY"},
		"access key as a flag": {[]string{"sweep", "--object-store-access-key", "ACCESSVALUE"}, "REPORTING_OBJECT_STORE_ACCESS_KEY"},
		"endpoint alone":       {[]string{"sweep", "--object-store-endpoint=https://s3.example.com"}, "-object-store-endpoint and -object-store-bucket together"},
		"keys missing":         {[]string{"sweep", "--object-store-endpoint=https://s3.example.com", "--object-store-bucket=reports-bucket"}, "REPORTING_OBJECT_STORE_ACCESS_KEY"},
		// verify-objects has nothing to compare without the object store (S04-F), and takes the same flags and the same refusals as sweep.
		"verify without the store": {[]string{"verify-objects"}, "verify-objects compares the artifact rows with an object store"},
		"verify deep without it":   {[]string{"verify-objects", "--deep", "--ids"}, "--object-store-endpoint"},
		"verify secret as a flag":  {[]string{"verify-objects", "--object-store-secret-key=SECRETVALUE"}, "REPORTING_OBJECT_STORE_SECRET_KEY"},
		"verify keys missing":      {[]string{"verify-objects", "--object-store-endpoint=https://s3.example.com", "--object-store-bucket=reports-bucket"}, "REPORTING_OBJECT_STORE_ACCESS_KEY"},
		"http off loopback":        {[]string{"sweep", "--object-store-endpoint=http://s3.example.com", "--object-store-bucket=reports-bucket", "--object-store-loopback-http"}, "loopback"},
	} {
		keys := "AK0123456789"
		if name == "keys missing" || name == "verify keys missing" {
			keys = ""
		}
		t.Setenv("REPORTING_OBJECT_STORE_ACCESS_KEY", keys)
		t.Setenv("REPORTING_OBJECT_STORE_SECRET_KEY", keys)
		_, err := capture(t, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		} else if strings.Contains(err.Error(), "SECRETVALUE") || strings.Contains(err.Error(), "ACCESSVALUE") {
			t.Errorf("%s: the refusal repeats a key: %v", name, err)
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
	out := expect([]string{"status"}, false, "pending:  001 base", "pending:  003 issues", "pending:  004 source_confirmed_issue", "pending:  005 retention", "pending:  006 sweeps", "pending:  007 row_level_security", "pending:  008 object_storage", "pending:  009 capacity_indexes")
	if strings.Contains(out, "applied:") {
		t.Fatal(out)
	}
	expect([]string{"plan"}, false, "up   001 base", "up   003 issues", "up   004 source_confirmed_issue", "up   005 retention", "up   006 sweeps", "up   007 row_level_security", "up   008 object_storage", "up   009 capacity_indexes")
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
	expect([]string{"up"}, false, "at version 009")
	out = expect([]string{"status"}, false, "applied:  001 base", "applied:  003 issues", "applied:  004 source_confirmed_issue", "applied:  005 retention", "applied:  006 sweeps", "applied:  007 row_level_security", "applied:  008 object_storage", "applied:  009 capacity_indexes", "the connected role is not the application role", "the connected role is not the sweep role")
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
	expect([]string{"plan", "--to", "1"}, false, "down 009 capacity_indexes", "down 008 object_storage", "down 007 row_level_security", "down 006 sweeps", "down 005 retention", "down 004 source_confirmed_issue", "down 003 issues", "down 002 payload_digests")
	expect([]string{"down"}, true, "explicit --to")
	expect([]string{"down", "--to", "10"}, true, "outside")
	expect([]string{"up", "--to", "1"}, true, "wrong side")
	expect([]string{"down", "--to", "2"}, false, "at version 002")
	expect([]string{"plan"}, false, "up   003 issues", "up   004 source_confirmed_issue", "up   005 retention", "up   006 sweeps", "up   007 row_level_security", "up   008 object_storage", "up   009 capacity_indexes")
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
	expect([]string{"up"}, false, "at version 009")
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
	expect([]string{"up"}, false, "at version 009")
	expect([]string{"init-profile", "contract-simulator-only"}, false)
	retention(t, admin, dsn, expect)
	migratorCredentials(t, admin, dsn, expect)
}

// migratorCredentials proves the rotation of the migrator role's password (S04-H H3) for the command: run with the DSN of the role, it
// works; after ALTER ROLE ... PASSWORD it is refused with the old DSN, without a password in what it prints, and works with the new one.
// The throwaway server trusts every password, so the connections go through internal/pgauthproxy, which checks the password against the
// verifier the server stores (see its documentation: the check is the proxy's, the verifier and the role are the server's).
func migratorCredentials(t *testing.T, admin *pgxpool.Pool, dsn string, expect func([]string, bool, ...string) string) {
	t.Helper()
	ctx := context.Background()
	const role, oldPassword, newPassword = "reporting_cli_mig_t", "migrator-OLD-password-6c2e91d0a4", "migrator-NEW-password-d7013fb85e"
	expect([]string{"up"}, false, "at version 009")
	expect([]string{"roles", "--migrator-role", role, "--app-role", "reporting_cli_app_t"}, false, "owned by "+role)
	set := func(password string) {
		t.Helper()
		if _, err := admin.Exec(ctx, "ALTER ROLE "+role+" LOGIN PASSWORD '"+password+"'"); err != nil {
			t.Fatal(err)
		}
	}
	upstream, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	proxy := pgauthproxy.Start(t, upstream.Host, pgauthproxy.Admin{Pool: admin})
	as := func(password string) {
		t.Helper()
		u := url.URL{Scheme: "postgresql", User: url.UserPassword(role, password), Host: proxy.Addr, Path: upstream.Path, RawQuery: "sslmode=disable"}
		t.Setenv("REPORTING_STORE_DSN", u.String())
	}
	check := func(what string, out string, err error) {
		t.Helper()
		all := out
		if err != nil {
			all += err.Error()
		}
		for _, leak := range []string{oldPassword, newPassword, "SCRAM-SHA-256$", "postgresql://"} {
			if strings.Contains(all, leak) {
				t.Errorf("%s: the output carries %q: %q", what, leak, all)
			}
		}
	}
	set(oldPassword)
	as(oldPassword)
	out, err := capture(t, "status")
	check("status with the password", out, err)
	if err != nil || !strings.Contains(out, "applied:  009 capacity_indexes") {
		t.Fatalf("the migrator cannot run the command with its password: %v %s", err, out)
	}
	set(newPassword)
	refused := proxy.Refused.Load()
	out, err = capture(t, "status")
	check("status with the old password", out, err)
	if err == nil || proxy.Refused.Load() <= refused {
		t.Fatalf("the command ran with the old password: %v %s", err, out)
	}
	for _, args := range [][]string{{"plan"}, {"up"}, {"roles", "--migrator-role", role, "--app-role", "reporting_cli_app_t"}} {
		out, err = capture(t, args...)
		check(args[0]+" with the old password", out, err)
		if err == nil {
			t.Errorf("%v ran with the old password", args)
		}
	}
	as(newPassword)
	out, err = capture(t, "status")
	check("status with the new password", out, err)
	if err != nil || !strings.Contains(out, "applied:  009 capacity_indexes") {
		t.Fatalf("the migrator cannot run the command with its new password: %v %s", err, out)
	}
	expect([]string{"up"}, false, "at version 009")
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
	expect([]string{"up"}, false, "at version 009")
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
	objectSweep(t, admin, expect)
	verifyObjects(t, admin, expect)
}

// objectSweep drives the operator's sweep with and without the object store flags (S04-E) against the throwaway S3-compatible server: rows whose files are
// objects, the counts the command prints, the objects gone or left, and a bucket that cannot be reached stopping the sweep before it deletes a row.
func objectSweep(t *testing.T, admin *pgxpool.Pool, expect func([]string, bool, ...string) string) {
	t.Helper()
	ctx := context.Background()
	endpoint, access, secret := os.Getenv("REPORTING_OBJECT_STORE_TEST_ENDPOINT"), os.Getenv("REPORTING_OBJECT_STORE_TEST_ACCESS_KEY"), os.Getenv("REPORTING_OBJECT_STORE_TEST_SECRET_KEY")
	if endpoint == "" || access == "" || secret == "" {
		if os.Getenv("REPORTING_OBJECT_STORE_REQUIRE") == "1" {
			t.Fatal("required object store endpoint and keys missing")
		}
		t.Log("object store gate not run: set REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY and _SECRET_KEY")
		return
	}
	const bucket = "reporting-test-s04e"
	client, err := objects.NewS3(objects.S3{Endpoint: endpoint, Bucket: bucket, Region: "us-east-1", AccessKey: access, SecretKey: secret, LoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = objects.CreateBucketForTests(ctx, client); err != nil {
		t.Fatal(err)
	}
	t.Setenv(objects.AccessKeyEnv, access)
	t.Setenv(objects.SecretKeyEnv, secret)
	flags := []string{"--object-store-endpoint=" + endpoint, "--object-store-bucket=" + bucket, "--object-store-loopback-http"}
	with := func(args ...string) []string { return append(append([]string{"sweep"}, args...), flags...) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, args ...any) (n int) {
		t.Helper()
		if err := admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Two expired artifacts whose files are objects, and a young object that no row names.
	a, b, stray := r.NewID(), r.NewID(), r.NewID()
	body := []byte("PK the file")
	sum := sha256.Sum256(body)
	for _, id := range []string{a, b, stray} {
		if err = client.Put(ctx, r.ObjectKey(id), body, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, id := range []string{a, b, stray} {
			_ = client.Delete(context.Background(), r.ObjectKey(id))
		}
	})
	for _, id := range []string{a, b} {
		data := `{"ID":"` + id + `","SnapshotID":"gone","Object":"` + r.ObjectKey(id) + `"}`
		exec(`INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
VALUES($1,'t','i','x','y','artifact',$2::jsonb,encode(sha256(convert_to($2::jsonb::text,'UTF8')),'hex'),clock_timestamp()-interval '48 hours')`, id, data)
	}
	held := func(id string) bool {
		_, err := client.Get(ctx, r.ObjectKey(id), hex.EncodeToString(sum[:]), len(body))
		return err == nil
	}
	// A bucket that is not there stops the sweep before a row is deleted, with the bucket's name and the status.
	out, err := capture(t, "sweep", "--object-store-endpoint="+endpoint, "--object-store-bucket=reporting-test-s04e-absent", "--object-store-loopback-http")
	if err == nil || !strings.Contains(err.Error(), `"reporting-test-s04e-absent"`) || !strings.Contains(err.Error(), "status 404") || strings.Contains(err.Error(), endpoint) || strings.Contains(err.Error(), secret) || count("SELECT count(*) FROM reporting_s01.assets WHERE id IN ($1,$2)", a, b) != 2 {
		t.Fatalf("a bucket that cannot be reached: %v %q", err, out)
	}
	// A dry run counts the objects and deletes nothing; the young stray is not an orphan.
	expect(with("--dry-run"), false, "dry run, would sweep: 2 artifacts", "objects: 2 would be deleted, 0 orphans would be deleted, 0 left for the next reconciliation")
	if count("SELECT count(*) FROM reporting_s01.assets WHERE id IN ($1,$2)", a, b) != 2 || !held(a) || !held(b) || !held(stray) {
		t.Fatal("a dry run deleted a row or an object")
	}
	// Without the flags the rows go and the objects wait, and the command says so.
	exec(`UPDATE reporting_s01.assets SET expires_at = clock_timestamp()-interval '49 hours' WHERE id=$1`, a)
	exec(`UPDATE reporting_s01.assets SET expires_at = clock_timestamp()+interval '1 hour' WHERE id=$1`, b)
	expect([]string{"sweep"}, false, "swept: 1 artifacts", "objects: no object store configured, rows only; the files of 1 deleted artifacts that were in an object store, and any orphan, wait for a reconciliation")
	if count("SELECT count(*) FROM reporting_s01.assets WHERE id=$1", a) != 0 || !held(a) {
		t.Fatal("without the object store flags the row must go and the object stay")
	}
	// With them the row goes and then its object; the young stray and the unexpired artifact's object stay.
	exec(`UPDATE reporting_s01.assets SET expires_at = clock_timestamp()-interval '48 hours' WHERE id=$1`, b)
	expect(with(), false, "swept: 1 artifacts", "objects: 1 deleted, 0 orphans deleted, 0 left for the next reconciliation")
	if count("SELECT count(*) FROM reporting_s01.assets WHERE id=$1", b) != 0 || held(b) || !held(stray) {
		t.Fatalf("with the object store flags: row of b gone %v, object of b held %v, stray held %v", count("SELECT count(*) FROM reporting_s01.assets WHERE id=$1", b) == 0, held(b), held(stray))
	}
	// The ledger holds the counts and nothing that names an object.
	if n := count("SELECT count(*) FROM reporting_s01.sweeps WHERE NOT dry_run AND objects=1 AND orphans=0"); n != 1 {
		t.Fatal("the ledger row of the sweep with the object store", n)
	}
}

// verifyObjects drives the read-only verification (S04-F) against the throwaway S3-compatible server: the counts it prints, the exit status, the
// ids only when asked, a missing object, another file under a key (found by --deep only), the object put back, and that it wrote nothing.
func verifyObjects(t *testing.T, admin *pgxpool.Pool, expect func([]string, bool, ...string) string) {
	t.Helper()
	ctx := context.Background()
	endpoint, access, secret := os.Getenv("REPORTING_OBJECT_STORE_TEST_ENDPOINT"), os.Getenv("REPORTING_OBJECT_STORE_TEST_ACCESS_KEY"), os.Getenv("REPORTING_OBJECT_STORE_TEST_SECRET_KEY")
	if endpoint == "" || access == "" || secret == "" {
		return // objectSweep has said so, or failed
	}
	client, err := objects.NewS3(objects.S3{Endpoint: endpoint, Bucket: "reporting-test-s04e", Region: "us-east-1", AccessKey: access, SecretKey: secret, LoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	flags := []string{"--object-store-endpoint=" + endpoint, "--object-store-bucket=reporting-test-s04e", "--object-store-loopback-http"}
	verify := func(args ...string) []string { return append(append([]string{"verify-objects"}, args...), flags...) }
	// An empty world: no rows, nothing under artifacts/.
	if _, err = admin.Exec(ctx, "DELETE FROM reporting_s01.assets"); err != nil {
		t.Fatal(err)
	}
	for {
		page, _, err := client.List(ctx, r.ObjectPrefix, "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, o := range page {
			if err = client.Delete(ctx, o.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	body, other := []byte("PK the file"), []byte("PK another file")
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	x, y, z, orphan := r.NewID(), r.NewID(), r.NewID(), r.NewID()
	t.Cleanup(func() {
		for _, id := range []string{x, y, z, orphan} {
			_ = client.Delete(context.Background(), r.ObjectKey(id))
		}
	})
	put := func(id string, b []byte) {
		t.Helper()
		if err := client.Put(ctx, r.ObjectKey(id), b, sum(b)); err != nil {
			t.Fatal(err)
		}
	}
	put(x, body)
	put(y, other) // the row says body
	put(orphan, body)
	for _, id := range []string{x, y, z} {
		if _, err = admin.Exec(ctx, `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
VALUES($1,'t','i','x','y','artifact',jsonb_build_object('ID',$1::text,'Object',$2::text,'Size',$3::int,'SHA256',$4::text),'',clock_timestamp()+interval '1 hour')`, id, r.ObjectKey(id), len(body), sum(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = admin.Exec(ctx, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex')`); err != nil {
		t.Fatal(err)
	}
	var before string
	snapshot := func() (d string) {
		if err := admin.QueryRow(ctx, `SELECT md5(coalesce((SELECT string_agg(a::text,'|' ORDER BY id) FROM reporting_s01.assets a),'') || coalesce((SELECT string_agg(j::text,'|' ORDER BY id) FROM reporting_s01.jobs j),'') || (SELECT count(*) FROM reporting_s01.sweeps)::text)`).Scan(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	before = snapshot()
	t.Setenv(objects.AccessKeyEnv, access)
	t.Setenv(objects.SecretKeyEnv, secret)
	// Presence only: z is missing, y is there (its bytes are not looked at), the orphan is informational. Counts, an error that is counts, no id.
	out := expect(verify(), true, "rows:       3 artifact rows name an object", "objects:    3 under artifacts/", "missing:    1 rows whose object is absent", "orphans:    1 objects no row names", "invalid:    0", "mismatched: not checked", "verify-objects: 1 missing, 0 mismatched, 0 invalid")
	for _, id := range []string{x, y, z, orphan} {
		if strings.Contains(out, id) || strings.Contains(out, r.ObjectKey(id)) {
			t.Fatalf("an identifier was printed without --ids: %s", out)
		}
	}
	if _, err = capture(t, verify()...); err == nil || strings.Contains(err.Error(), z) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), endpoint) {
		t.Fatalf("the failure carries more than counts: %v", err)
	}
	// Deep: y is another file.
	out = expect(verify("--deep"), true, "missing:    1", "mismatched: 1 objects whose length or SHA-256 is not the row's", "verify-objects: 1 missing, 1 mismatched, 0 invalid")
	if strings.Contains(out, y) || strings.Contains(out, z) {
		t.Fatalf("--deep printed an identifier: %s", out)
	}
	// With --ids, and only then, the ids of exactly the rows that fail.
	out = expect(verify("--deep", "--ids"), true, "missing id: "+z, "mismatched id: "+y)
	if strings.Contains(out, x) || strings.Contains(out, orphan) || strings.Count(out, " id: ") != 2 {
		t.Fatalf("--ids lists more than the rows that fail: %s", out)
	}
	// The operator puts the objects right: clean, exit 0, the orphan still only informational.
	put(z, body)
	put(y, body)
	expect(verify("--deep"), false, "rows:       3", "objects:    4", "missing:    0", "orphans:    1", "mismatched: 0")
	expect(verify(), false, "missing:    0", "mismatched: not checked")
	if snapshot() != before {
		t.Fatal("verify-objects wrote to the database")
	}
	// A bucket that is not there stops it before anything is read, with the bucket's name and the status.
	_, err = capture(t, "verify-objects", "--object-store-endpoint="+endpoint, "--object-store-bucket=reporting-test-s04e-absent", "--object-store-loopback-http")
	if err == nil || !strings.Contains(err.Error(), `"reporting-test-s04e-absent"`) || strings.Contains(err.Error(), secret) {
		t.Fatalf("a bucket that cannot be reached: %v", err)
	}
}

// S04-H H3: a REPORTING_STORE_DSN the command cannot use is refused without the string and without the password in it, whether the string
// cannot be parsed or the server cannot be reached; the credential flags of the object store are refused as before.
func TestMigrateCommandRefusalsCarryNoPassword(t *testing.T) {
	const password = "S04H-migrator-pw-0123456789-NEVER-SHOWN"
	for name, dsn := range map[string]string{
		"not a connection string": "::" + password,
		"a bad sslmode":           "postgresql://migrator:" + password + "@127.0.0.1:1/db?sslmode=bogus",
		"connection refused":      "postgresql://migrator:" + password + "@127.0.0.1:1/db?sslmode=disable",
	} {
		t.Setenv("REPORTING_STORE_DSN", dsn)
		out, err := capture(t, "status", "--timeout", "5s")
		if err == nil {
			t.Errorf("%s: status ran", name)
			continue
		}
		if all := out + err.Error(); strings.Contains(all, password) || strings.Contains(all, "postgresql://") {
			t.Errorf("%s: the refusal carries the connection string: %q", name, all)
		}
	}
	t.Setenv("REPORTING_STORE_DSN", "::"+password)
	if _, err := capture(t, "status"); !errors.Is(err, r.ErrInvalid) || !strings.Contains(err.Error(), "REPORTING_STORE_DSN") {
		t.Errorf("an unparseable string: %v", err)
	}
}
