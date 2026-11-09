package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/postgres"
)

// S12-B: the audit keeps the same contract on the memory and the PostgreSQL stores: newest first, pages, one's own or the tenant's.
func runAuditContract(t *testing.T, log r.AuditLog) {
	ctx := context.Background()
	a := r.Identity{Tenant: "t", Issuer: "i", Subject: "a"}
	b := r.Identity{Tenant: "t", Issuer: "i", Subject: "b"}
	other := r.Identity{Tenant: "u", Issuer: "i", Subject: "a"}
	base := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	var ids []string
	for n, who := range []r.Identity{a, b, a, other} {
		e := r.AuditEvent{ID: r.NewID(), Identity: who, At: base.Add(time.Duration(n) * time.Minute), Action: "design.change", ObjectKind: "layout", Outcome: "succeeded"}
		if err := log.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	if err := log.Record(ctx, r.AuditEvent{ID: r.NewID(), Identity: a, Action: "deleted.everything", ObjectKind: "layout", Outcome: "succeeded"}); err == nil {
		t.Fatal("an unknown action was recorded")
	}
	own, err := log.Events(ctx, a, false, 10, "")
	if err != nil || len(own) != 2 || own[0].ID != ids[2] || own[1].ID != ids[0] || own[0].Subject != "" {
		t.Fatalf("%+v %v", own, err)
	}
	tenant, err := log.Events(ctx, a, true, 10, "")
	if err != nil || len(tenant) != 3 || tenant[0].ID != ids[2] || tenant[1].Subject != "b" {
		t.Fatalf("%+v %v", tenant, err)
	}
	page, err := log.Events(ctx, a, true, 1, ids[2])
	if err != nil || len(page) != 1 || page[0].ID != ids[1] {
		t.Fatalf("%+v %v", page, err)
	}
	if _, err := log.Events(ctx, a, false, 0, ""); err == nil {
		t.Fatal("limit 0")
	}
}

func TestMemoryAuditContract(t *testing.T) { runAuditContract(t, memory.New(time.Now)) }

func TestPostgresAuditContract(t *testing.T) {
	dsn := os.Getenv("REPORTING_S01_TEST_DSN")
	if dsn == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required real PostgreSQL DSN missing")
		}
		t.Skip("real PostgreSQL gate not run; use scripts/run_pg16_vm.py or REPORTING_S01_TEST_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store, err := postgres.Open(ctx, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ResetFixture(ctx); err != nil {
		t.Fatal(err)
	}
	runAuditContract(t, store)
}
