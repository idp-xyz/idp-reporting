package integration_test

import (
	"context"
	"errors"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/postgres"
	"os"
	"sync"
	"testing"
	"time"
)

type environment struct {
	s    *r.Service
	repo r.Repository
	p    r.Principal
	a, b *contractsim.Simulator
}

func env(t *testing.T, repo r.Repository, extra ...r.Connection) environment {
	t.Helper()
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	a, b := contractsim.Financial(), contractsim.Shipment()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	g := contractsim.DefaultGrant()
	g.CurrencyPolicy = nil
	b.Grant(contractsim.DemoOwner(p, contractsim.ShipmentIntegration), g)
	connections := []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}, {Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: b}}
	connections = append(connections, extra...)
	s, e := r.NewService(r.Config{Repository: repo, Connections: connections, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	return environment{s, repo, p, a, b}
}
func freeze(t *testing.T, v environment, q r.QuerySpec, key string) r.JobView {
	t.Helper()
	ctx := context.Background()
	j, e := v.s.SubmitSnapshot(ctx, v.p, q, key)
	if e != nil {
		t.Fatal(e)
	}
	if ran, e := v.s.RunOne(ctx); !ran || e != nil {
		t.Fatal(ran, e)
	}
	j, e = v.s.Job(ctx, v.p, j.ID)
	if e != nil || j.State != "succeeded" {
		t.Fatal(j, e)
	}
	return j
}
func export(t *testing.T, v environment, snapshot, key string) r.JobView {
	t.Helper()
	ctx := context.Background()
	j, e := v.s.SubmitExport(ctx, v.p, snapshot, key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	j, e = v.s.Job(ctx, v.p, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func mustError(t *testing.T, e, want error) {
	t.Helper()
	if !errors.Is(e, want) {
		t.Fatalf("want %v, got %v", want, e)
	}
}

func runContract(t *testing.T, factory func(*testing.T) r.Repository) {
	ctx := context.Background()
	t.Run("two_domains_full_chain", func(t *testing.T) {
		v := env(t, factory(t))
		for i, q := range []r.QuerySpec{contractsim.FinancialQuery(), contractsim.ShipmentQuery()} {
			j := freeze(t, v, q, fmt.Sprint(i))
			ex := export(t, v, j.SnapshotID, fmt.Sprint(i))
			a, e := v.s.Download(ctx, v.p, ex.ArtifactID)
			if e != nil || len(a.Content) < 1000 {
				t.Fatal(e, len(a.Content))
			}
			if folder := os.Getenv("REPORTING_S01_EVIDENCE_DIR"); folder != "" {
				if e = os.WriteFile(folder+fmt.Sprintf("/sample_%d.xlsx", i+1), a.Content, 0600); e != nil {
					t.Fatal(e)
				}
			}
		}
	})
	t.Run("admission_does_not_read", func(t *testing.T) {
		v := env(t, factory(t))
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "same")
		if e != nil || j.State != "queued" || v.a.ReadCalls() != 0 {
			t.Fatal(j, e, v.a.ReadCalls())
		}
	})
	t.Run("idempotency_conflict_and_owner_axes", func(t *testing.T) {
		v := env(t, factory(t))
		q := contractsim.FinancialQuery()
		j, e := v.s.SubmitSnapshot(ctx, v.p, q, "same")
		if e != nil {
			t.Fatal(e)
		}
		again, e := v.s.SubmitSnapshot(ctx, v.p, q, "same")
		if e != nil || again.ID != j.ID {
			t.Fatal(again, e)
		}
		q.AsOf = "2026-10-25"
		_, e = v.s.SubmitSnapshot(ctx, v.p, q, "same")
		mustError(t, e, r.ErrConflict)
		other, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.ShipmentQuery(), "same")
		if e != nil || other.ID == j.ID {
			t.Fatal(other, e)
		}
	})
	t.Run("same_user_id_different_issuer", func(t *testing.T) {
		v := env(t, factory(t))
		j := freeze(t, v, contractsim.FinancialQuery(), "one")
		ex := export(t, v, j.SnapshotID, "one")
		p := v.p
		p.Issuer = "urn:other-issuer"
		_, e := v.s.Job(ctx, p, j.ID)
		mustError(t, e, r.ErrNotFound)
		_, e = v.s.Rows(ctx, p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrNotFound)
		_, e = v.s.Download(ctx, p, ex.ArtifactID)
		mustError(t, e, r.ErrNotFound)
	})
	t.Run("cross_tenant_source_ids_not_equivalent", func(t *testing.T) {
		repo := factory(t)
		a := contractsim.Financial()
		p := contractsim.DemoPrincipal()
		p.Tenant = "other_platform_tenant"
		a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
		v := env(t, repo, r.Connection{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a})
		j := freeze(t, v, contractsim.FinancialQuery(), "same")
		other, e := v.s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "same")
		if e != nil || other.ID == j.ID {
			t.Fatal(other, e)
		}
		_, e = v.s.Rows(ctx, p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrNotFound)
		_, e = v.s.Job(ctx, p, j.ID)
		mustError(t, e, r.ErrNotFound)
	})
	t.Run("cross_product_requires_explicit_grant", func(t *testing.T) {
		v := env(t, factory(t))
		j := freeze(t, v, contractsim.ShipmentQuery(), "one")
		ex := export(t, v, j.SnapshotID, "one")
		p := v.p
		p.Integrations = []string{contractsim.FinanceIntegration}
		_, e := v.s.Query(ctx, p, contractsim.ShipmentQuery())
		mustError(t, e, r.ErrDenied)
		_, e = v.s.Job(ctx, p, j.ID)
		mustError(t, e, r.ErrDenied)
		_, e = v.s.Rows(ctx, p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrDenied)
		_, e = v.s.Download(ctx, p, ex.ArtifactID)
		mustError(t, e, r.ErrDenied)
		_, e = v.s.Cancel(ctx, p, j.ID)
		mustError(t, e, r.ErrDenied)
	})
	t.Run("company_selector_is_not_authority", func(t *testing.T) {
		v := env(t, factory(t))
		q := contractsim.FinancialQuery()
		q.Companies = []string{"company_other"}
		_, e := v.s.Query(ctx, v.p, q)
		mustError(t, e, r.ErrDenied)
	})
	t.Run("frozen_source_not_live_refresh", func(t *testing.T) {
		v := env(t, factory(t))
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		v.a.ReplaceRecords([]contractsim.Record{{Company: "company_demo", Key: "r1", Values: []string{"updated", "current", "999.99"}}})
		if _, e = v.s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		j, e = v.s.Job(ctx, v.p, j.ID)
		if e != nil {
			t.Fatal(e)
		}
		rows, e := v.s.Rows(ctx, v.p, j.SnapshotID, 1, "")
		if e != nil || rows.Rows[0][0] != "customer_a" || rows.Totals["ar.open_balance"] != "9007199254740993.01" {
			t.Fatal(rows, e)
		}
		next, e := v.s.Rows(ctx, v.p, j.SnapshotID, 1, rows.NextCursor)
		if e != nil || next.Rows[0][0] != "customer_b" {
			t.Fatal(next, e)
		}
		_, e = v.s.Rows(ctx, v.p, j.SnapshotID, 1, rows.NextCursor+"tampered")
		mustError(t, e, r.ErrInvalid)
	})
	t.Run("scope_expansion_does_not_widen", func(t *testing.T) {
		v := env(t, factory(t))
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		g := contractsim.DefaultGrant()
		g.Rows = append(g.Rows, "secret")
		g.Companies = append(g.Companies, "company_other")
		v.a.Grant(contractsim.DemoOwner(v.p, contractsim.FinanceIntegration), g)
		if _, e = v.s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		j, e = v.s.Job(ctx, v.p, j.ID)
		if e != nil {
			t.Fatal(e)
		}
		rows, e := v.s.Rows(ctx, v.p, j.SnapshotID, 100, "")
		if e != nil || len(rows.Rows) != 3 || rows.Totals["ar.open_balance"] != "9007199254740993.01" {
			t.Fatal(rows, e)
		}
	})
	t.Run("queued_revocation_and_cleanup", func(t *testing.T) {
		v := env(t, factory(t))
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		v.a.Revoke(contractsim.DemoOwner(v.p, contractsim.FinanceIntegration))
		_, e = v.s.RunOne(ctx)
		mustError(t, e, r.ErrDenied)
		internal, e := v.repo.Job(ctx, v.p.Identity, j.ID)
		if e != nil || internal.State != "failed" || internal.SnapshotID != "" {
			t.Fatal(internal, e)
		}
		cancel, e := v.s.Cancel(ctx, v.p, j.ID)
		if e != nil || cancel.SnapshotID != "" || cancel.ArtifactID != "" {
			t.Fatal(cancel, e)
		}
	})
	t.Run("completed_revocation_all_disclosures", func(t *testing.T) {
		v := env(t, factory(t))
		j := freeze(t, v, contractsim.FinancialQuery(), "one")
		ex := export(t, v, j.SnapshotID, "one")
		g := contractsim.DefaultGrant()
		g.Rows = []string{"r1", "r2"}
		v.a.Grant(contractsim.DemoOwner(v.p, contractsim.FinanceIntegration), g)
		_, e := v.s.Job(ctx, v.p, j.ID)
		mustError(t, e, r.ErrDenied)
		_, e = v.s.Job(ctx, v.p, ex.ID)
		mustError(t, e, r.ErrDenied)
		_, e = v.s.Rows(ctx, v.p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrDenied)
		_, e = v.s.SubmitExport(ctx, v.p, j.SnapshotID, "two")
		mustError(t, e, r.ErrDenied)
		_, e = v.s.Download(ctx, v.p, ex.ArtifactID)
		mustError(t, e, r.ErrDenied)
		cancel, e := v.s.Cancel(ctx, v.p, ex.ID)
		if e != nil || cancel.ArtifactID != "" || cancel.SnapshotID != "" {
			t.Fatal(cancel, e)
		}
	})
	for _, policy := range []string{"currency", "timezone"} {
		t.Run("completed_"+policy+"_change", func(t *testing.T) {
			v := env(t, factory(t))
			j := freeze(t, v, contractsim.FinancialQuery(), "one")
			ex := export(t, v, j.SnapshotID, "one")
			g := contractsim.DefaultGrant()
			if policy == "currency" {
				g.CurrencyPolicy = &r.Ref{ID: "ledger_base", Version: "2.0.0"}
			} else {
				g.Timezone = "UTC"
			}
			v.a.Grant(contractsim.DemoOwner(v.p, contractsim.FinanceIntegration), g)
			_, e := v.s.Rows(ctx, v.p, j.SnapshotID, 1, "")
			mustError(t, e, r.ErrDenied)
			_, e = v.s.Download(ctx, v.p, ex.ArtifactID)
			mustError(t, e, r.ErrDenied)
			_, e = v.s.SubmitExport(ctx, v.p, j.SnapshotID, "two")
			mustError(t, e, r.ErrDenied)
		})
	}
	t.Run("authority_outage_is_distinct_and_closed", func(t *testing.T) {
		v := env(t, factory(t))
		j := freeze(t, v, contractsim.FinancialQuery(), "one")
		ex := export(t, v, j.SnapshotID, "one")
		v.a.SetUnavailable(true)
		_, e := v.s.Rows(ctx, v.p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrUnavailable)
		_, e = v.s.Download(ctx, v.p, ex.ArtifactID)
		mustError(t, e, r.ErrUnavailable)
		_, e = v.s.Job(ctx, v.p, ex.ID)
		mustError(t, e, r.ErrUnavailable)
	})
	t.Run("cancelled_claim_cannot_publish", func(t *testing.T) {
		v := env(t, factory(t))
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		claim, e := v.repo.Claim(ctx, time.Minute)
		if e != nil || claim == nil {
			t.Fatal(claim, e)
		}
		if _, e = v.s.Cancel(ctx, v.p, j.ID); e != nil {
			t.Fatal(e)
		}
		e = v.repo.Publish(ctx, *claim, r.Publication{})
		mustError(t, e, r.ErrLease)
		if ran, e := v.s.RunOne(ctx); ran || e != nil {
			t.Fatal(ran, e)
		}
	})
	t.Run("concurrent_admission_and_claim", func(t *testing.T) {
		v := env(t, factory(t))
		var wg sync.WaitGroup
		ids := make(chan string, 8)
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
				if e != nil {
					errs <- e
				} else {
					ids <- j.ID
				}
			}()
		}
		wg.Wait()
		close(ids)
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		first := ""
		for id := range ids {
			if first != "" && id != first {
				t.Fatal("duplicate admission", first, id)
			}
			first = id
		}
		claims := make(chan *r.Claim, 8)
		errs = make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, e := v.repo.Claim(ctx, time.Minute)
				if e != nil {
					errs <- e
				} else if c != nil {
					claims <- c
				}
			}()
		}
		wg.Wait()
		close(claims)
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		if len(claims) != 1 {
			t.Fatal("claim count", len(claims))
		}
	})
	t.Run("lease_expiry_generation_and_atomicity", func(t *testing.T) {
		v := env(t, factory(t))
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		old, e := v.repo.Claim(ctx, time.Second)
		if e != nil || old == nil {
			t.Fatal(old, e)
		}
		time.Sleep(1100 * time.Millisecond)
		next, e := v.repo.Claim(ctx, time.Minute)
		if e != nil || next == nil || next.Job.Generation != old.Job.Generation+1 {
			t.Fatal(next, e)
		}
		stale := publication(*old)
		mustError(t, v.repo.Publish(ctx, *old, stale), r.ErrLease)
		_, e = v.repo.Snapshot(ctx, v.p.Identity, stale.Snapshot.ID)
		mustError(t, e, r.ErrNotFound)
		good := publication(*next)
		if e = v.repo.Publish(ctx, *next, good); e != nil {
			t.Fatal(e)
		}
		state, e := v.repo.Job(ctx, v.p.Identity, j.ID)
		if e != nil || state.State != "succeeded" || state.SnapshotID != good.Snapshot.ID {
			t.Fatal(state, e)
		}
		if _, e = v.repo.Snapshot(ctx, v.p.Identity, good.Snapshot.ID); e != nil {
			t.Fatal(e)
		}
	})
}
func TestMemoryContract(t *testing.T) {
	runContract(t, func(*testing.T) r.Repository { return memory.New(time.Now) })
}
func TestPostgresContract(t *testing.T) {
	dsn := os.Getenv("REPORTING_S01_TEST_DSN")
	if dsn == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required real PostgreSQL DSN missing")
		}
		t.Skip("real PostgreSQL gate not run; use scripts/run_pg16_vm.py or REPORTING_S01_TEST_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store, e := postgres.Open(ctx, dsn, true)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	runContract(t, func(t *testing.T) r.Repository {
		if e := store.ResetFixture(ctx); e != nil {
			t.Fatal(e)
		}
		return store
	})
}
