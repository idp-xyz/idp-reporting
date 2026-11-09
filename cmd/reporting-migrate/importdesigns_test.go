package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"idp.local/reporting/layouts"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
)

// S11-C: import-designs copies the designs of the bound identities from the private directories into the store, keeps what is already there
// unless --replace, counts the files of no bound identity, and imports nothing when one document breaks its store's rules.
func TestImportDesigns(t *testing.T) {
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
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS reporting_s01 CASCADE"); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Up(ctx, admin, 0, "test"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REPORTING_STORE_DSN", dsn)
	const issuer = "https://issuer.example.test"
	a := r.Identity{Tenant: "acme", Issuer: issuer, Subject: "alice"}
	stranger := r.Identity{Tenant: "acme", Issuer: issuer, Subject: "nobody-binds-me"}
	dir := t.TempDir()
	layoutDir := filepath.Join(dir, "layouts")
	saveLayout := func(owner r.Identity, column string) {
		t.Helper()
		s, err := layouts.Open(layoutDir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		d := layouts.Document{Schema: "1.0", Binding: layouts.Binding{Integration: "p", Dataset: r.Ref{ID: "d", Version: "1"}}, Columns: []layouts.Column{{ID: column, Align: "left"}}}
		if _, err := s.Save(owner, "", 0, d); err != nil {
			t.Fatal(err)
		}
	}
	saveLayout(a, "amount")
	saveLayout(stranger, "other") // a file whose owner no binding names
	bindings := filepath.Join(dir, "bindings.json")
	if err := os.WriteFile(bindings, []byte(`[{"subject":"alice","client_id":"c1","tenant":"acme","integrations":["p"]},{"subject":"alice","client_id":"c2","tenant":"acme","integrations":["p"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"import-designs", "--issuer", issuer, "--bindings", bindings, "--layout-directory", layoutDir}
	rows := func() int {
		var n int
		if err := admin.QueryRow(ctx, "SELECT count(*) FROM reporting_s01.designs WHERE store='layouts'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	out, err := capture(t, args...)
	if err != nil || !strings.Contains(out, "layouts: 1 imported, 0 kept") || !strings.Contains(out, "1 files of no bound identity") || rows() != 1 {
		t.Fatalf("%v %q %d", err, out, rows())
	}
	if strings.Contains(out, "alice") || strings.Contains(out, "acme") {
		t.Fatalf("the output names an identity: %q", out)
	}
	// The document in the store is the file's, read as the owner.
	file, _ := os.ReadFile(filepath.Join(layoutDir, privatedocs.DirectoryFileName(a)))
	var stored []byte
	if err := admin.QueryRow(ctx, "SELECT data FROM reporting_s01.designs WHERE store='layouts' AND tenant=$1 AND issuer=$2 AND subject=$3", a.Tenant, a.Issuer, a.Subject).Scan(&stored); err != nil || string(stored) != string(file) {
		t.Fatalf("%v %q", err, stored)
	}
	// Again: kept; with --replace: written again.
	if out, err = capture(t, args...); err != nil || !strings.Contains(out, "layouts: 0 imported, 1 kept") {
		t.Fatalf("%v %q", err, out)
	}
	if out, err = capture(t, append(args, "--replace")...); err != nil || !strings.Contains(out, "layouts: 1 imported, 0 kept") {
		t.Fatalf("%v %q", err, out)
	}
	// A document that breaks the rules: nothing is imported, and the message names neither the file nor the identity.
	if _, err := admin.Exec(ctx, "TRUNCATE reporting_s01.designs"); err != nil {
		t.Fatal(err)
	}
	broken := r.Identity{Tenant: "acme", Issuer: issuer, Subject: "bob"}
	if err := os.WriteFile(filepath.Join(layoutDir, privatedocs.DirectoryFileName(broken)), []byte(`{"schema_version":2,"records":[{"versions":[]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bindings, []byte(`[{"subject":"alice","client_id":"c1","tenant":"acme","integrations":["p"]},{"subject":"bob","client_id":"c1","tenant":"acme","integrations":["p"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = capture(t, args...)
	if err == nil || !strings.Contains(err.Error(), "nothing was imported") || strings.Contains(err.Error(), "bob") || rows() != 0 {
		t.Fatalf("%v %d", err, rows())
	}
	// The arguments.
	for name, bad := range map[string][]string{
		"no issuer":           {"import-designs", "--bindings", bindings, "--layout-directory", layoutDir},
		"an http issuer":      {"import-designs", "--issuer", "http://issuer", "--bindings", bindings, "--layout-directory", layoutDir},
		"no directory":        {"import-designs", "--issuer", issuer, "--bindings", bindings},
		"no bindings":         {"import-designs", "--issuer", issuer, "--layout-directory", layoutDir},
		"a missing directory": {"import-designs", "--issuer", issuer, "--bindings", bindings, "--dashboard-directory", filepath.Join(dir, "none")},
	} {
		if _, err := capture(t, bad...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
