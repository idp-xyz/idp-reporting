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
	for name, args := range map[string][]string{
		"down without --to":    {"down"},
		"roles without roles":  {"roles"},
		"init-profile fixture": {"init-profile", "fixture"},
		"init-profile none":    {"init-profile"},
		"unknown":              {"frobnicate"},
	} {
		if _, err := capture(t, args...); err == nil || errors.Is(err, r.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
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
	defer admin.Close()
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
	out := expect([]string{"status"}, false, "pending:  001 base", "pending:  003 issues")
	if strings.Contains(out, "applied:") {
		t.Fatal(out)
	}
	expect([]string{"plan"}, false, "up   001 base", "up   003 issues")
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
	expect([]string{"up"}, false, "at version 003")
	out = expect([]string{"status"}, false, "applied:  001 base", "applied:  003 issues", "the connected role is not the application role")
	if strings.Contains(out, "pending:") {
		t.Fatal(out)
	}
	expect([]string{"plan", "--to", "1"}, false, "down 003 issues", "down 002 payload_digests")
	expect([]string{"down"}, true, "explicit --to")
	expect([]string{"down", "--to", "4"}, true, "outside")
	expect([]string{"up", "--to", "1"}, true, "wrong side")
	expect([]string{"down", "--to", "2"}, false, "at version 002")
	expect([]string{"plan"}, false, "up   003 issues")
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
	expect([]string{"up"}, false, "at version 003")
	expect([]string{"init-profile", "contract-simulator-only"}, false)
}
