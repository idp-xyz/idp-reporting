package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/privatedocs"
	"idp.local/reporting/privatedocs/backendtest"
	"idp.local/reporting/storage/postgres"
)

// S11-A: the designs of the private stores in the platform's database keep the backend contract, and two stores on the same database (two
// servers) see each other's designs and never interleave their changes of one owner's document.
func TestPostgresDesignBackend(t *testing.T) {
	dsn := os.Getenv("REPORTING_S01_TEST_DSN")
	if dsn == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required real PostgreSQL DSN missing")
		}
		t.Skip("real PostgreSQL gate not run; use scripts/run_pg16_vm.py or REPORTING_S01_TEST_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	first, e := postgres.Open(ctx, dsn, true)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := postgres.Open(ctx, dsn, true)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	for _, store := range postgres.DesignStores {
		if _, e := first.Designs(store); e != nil {
			t.Fatal(store, e)
		}
	}
	if _, e := first.Designs("reports"); e == nil {
		t.Fatal("an unknown store")
	}
	backendtest.Run(t, func(t *testing.T) privatedocs.Backend {
		if e := first.ResetFixture(ctx); e != nil {
			t.Fatal(e)
		}
		b, _ := first.Designs("templates")
		return b
	}, backendtest.Options{Serializes: true, Second: func(*testing.T) privatedocs.Backend { b, _ := second.Designs("templates"); return b }})
	// One owner's document of one store is not another store's.
	if e := first.ResetFixture(ctx); e != nil {
		t.Fatal(e)
	}
	owner := contractsim.DemoPrincipal().Identity
	layouts, _ := first.Designs("layouts")
	tx, e := layouts.Begin(owner)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Store([]byte(`{"x":1}`)); e != nil || tx.Commit() != nil {
		t.Fatal(e)
	}
	dashboards, _ := second.Designs("dashboards")
	if data, e := dashboards.Load(owner); e != nil || data != nil {
		t.Fatalf("the dashboards see the layouts' document: %q %v", data, e)
	}
}
