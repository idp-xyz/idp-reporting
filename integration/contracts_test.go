package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/postgres"
	"os"
	"slices"
	"strings"
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

// via is how the service reaches a contract simulator: in process (direct), or through the reporting source protocol (overHTTP: the
// reference source in front of the simulator and the platform's adapter httpsource in front of that, S19-A). The contract runs both ways
// and must not tell them apart.
type via func(testing.TB, *contractsim.Simulator) r.Adapter

func direct(_ testing.TB, s *contractsim.Simulator) r.Adapter { return s }
func overHTTP(tb testing.TB, s *contractsim.Simulator) r.Adapter {
	return sourcetest.Remote(tb, s)
}

func env(t *testing.T, repo r.Repository, extra ...r.Connection) environment {
	t.Helper()
	return envVia(t, direct, repo, extra...)
}
func envVia(t *testing.T, through via, repo r.Repository, extra ...r.Connection) environment {
	t.Helper()
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	a, b := contractsim.Financial(), contractsim.Shipment()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	g := contractsim.DefaultGrant()
	g.CurrencyPolicy = nil
	b.Grant(contractsim.DemoOwner(p, contractsim.ShipmentIntegration), g)
	connections := []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: through(t, a)}, {Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: through(t, b)}}
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

func runContract(t *testing.T, through via, factory func(*testing.T) r.Repository) {
	ctx := context.Background()
	t.Run("two_domains_full_chain", func(t *testing.T) {
		v := envVia(t, through, factory(t))
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
	t.Run("the_evidence_says_the_adapters_data_class", func(t *testing.T) {
		// S08-A (ADR 0012 decision 6): the evidence says where the data comes from in the adapter's word. The simulators say
		// contract-simulator, in process and behind the reference source alike; a preview, the snapshot as the store keeps it and a
		// page of it all say that word, in either store.
		v := envVia(t, through, factory(t))
		for i, q := range []r.QuerySpec{contractsim.FinancialQuery(), contractsim.ShipmentQuery()} {
			if class, e := v.s.DataClass(v.p, q.Integration); e != nil || class != "contract-simulator" {
				t.Fatal(q.Integration, class, e)
			}
			res, e := v.s.Query(ctx, v.p, q)
			if e != nil || res.Evidence.DataClass != "contract-simulator" {
				t.Fatal(q.Integration, res.Evidence, e)
			}
			j := freeze(t, v, q, fmt.Sprint(i))
			stored, e := v.repo.Snapshot(ctx, v.p.Identity, j.SnapshotID)
			if e != nil || stored.Result.Evidence.DataClass != "contract-simulator" {
				t.Fatal(q.Integration, stored.Result.Evidence, e)
			}
			page, e := v.s.Rows(ctx, v.p, j.SnapshotID, 1, "")
			if e != nil || page.Evidence.DataClass != "contract-simulator" {
				t.Fatal(q.Integration, page.Evidence, e)
			}
		}
	})
	t.Run("admission_does_not_read", func(t *testing.T) {
		v := envVia(t, through, factory(t))
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "same")
		if e != nil || j.State != "queued" || v.a.ReadCalls() != 0 {
			t.Fatal(j, e, v.a.ReadCalls())
		}
	})
	t.Run("idempotency_conflict_and_owner_axes", func(t *testing.T) {
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, repo, r.Connection{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: through(t, a)})
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
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
		q := contractsim.FinancialQuery()
		q.Companies = []string{"company_other"}
		_, e := v.s.Query(ctx, v.p, q)
		mustError(t, e, r.ErrDenied)
	})
	t.Run("company_directory_lists_the_grant", func(t *testing.T) {
		// ADR 0015 (S19-B): each product lists the companies of the principal's grant with names; the list is advisory and a company
		// outside the grant stays refused; a product the principal does not hold is refused as the catalog refuses it.
		v := envVia(t, through, factory(t))
		for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
			got, e := v.s.ListCompanies(ctx, v.p, id)
			if e != nil || !got.Supported || got.Partial || !slices.Equal(got.Companies, []r.Company{{ID: "company_demo", Name: "演示公司"}}) {
				t.Fatalf("%s: %+v %v", id, got, e)
			}
		}
		q := contractsim.FinancialQuery()
		q.Companies = []string{"company_other"}
		_, e := v.s.Query(ctx, v.p, q)
		mustError(t, e, r.ErrDenied)
		p := v.p
		p.Integrations = []string{contractsim.FinanceIntegration}
		_, e = v.s.ListCompanies(ctx, p, contractsim.ShipmentIntegration)
		mustError(t, e, r.ErrDenied)
		v.a.SetUnavailable(true)
		_, e = v.s.ListCompanies(ctx, v.p, contractsim.FinanceIntegration)
		mustError(t, e, r.ErrUnavailable)
	})
	t.Run("frozen_source_not_live_refresh", func(t *testing.T) {
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
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
			v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
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
		v := envVia(t, through, factory(t))
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
	// Fail is fenced by the claim: the job must be running, at the claim's generation, with a lease that has
	// not run out by the store's clock. Whatever else is ErrLease and changes nothing.
	t.Run("fail_is_fenced_by_the_claim_and_the_lease", func(t *testing.T) {
		v := envVia(t, through, factory(t))
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		old, e := v.repo.Claim(ctx, time.Second)
		if e != nil || old == nil {
			t.Fatal(old, e)
		}
		time.Sleep(1100 * time.Millisecond)
		// The lease ran out: the claim cannot fail the job, which stays as it was.
		mustError(t, v.repo.Fail(ctx, *old, "EXECUTION_FAILED"), r.ErrLease)
		if st, e := v.repo.Job(ctx, v.p.Identity, j.ID); e != nil || st.State != "running" || st.ErrorCode != "" || st.Generation != 1 {
			t.Fatalf("a failure whose lease had run out changed the job: %+v %v", st, e)
		}
		next, e := v.repo.Claim(ctx, time.Minute)
		if e != nil || next == nil || next.Job.Generation != 2 {
			t.Fatal(next, e)
		}
		// The old claim is of another generation, and the new one has the lease.
		mustError(t, v.repo.Fail(ctx, *old, "EXECUTION_FAILED"), r.ErrLease)
		forged := *next
		forged.Job.Owner.Subject = "someone-else"
		mustError(t, v.repo.Fail(ctx, forged, "EXECUTION_FAILED"), r.ErrLease)
		if e = v.repo.Fail(ctx, *next, "BUDGET_EXCEEDED"); e != nil {
			t.Fatal(e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, j.ID); e != nil || st.State != "failed" || st.ErrorCode != "BUDGET_EXCEEDED" || !st.LeaseUntil.IsZero() {
			t.Fatalf("%+v %v", st, e)
		}
		// A failed job is not failed again.
		mustError(t, v.repo.Fail(ctx, *next, "EXECUTION_FAILED"), r.ErrLease)
		if st, e := v.repo.Job(ctx, v.p.Identity, j.ID); e != nil || st.ErrorCode != "BUDGET_EXCEEDED" {
			t.Fatalf("%+v %v", st, e)
		}
	})
	// ResolveFailure replaces ISSUE_UNRECONCILED, and nothing else, with the job's own code (S03-H): only for
	// an issue job that is failed, at the claim's generation, with that code; no lease is needed.
	t.Run("resolve_failure_is_fenced_by_the_claim_the_state_and_the_code", func(t *testing.T) {
		v := envVia(t, through, factory(t))
		snap := freeze(t, v, contractsim.FinancialQuery(), "snap")
		claimed := func(key string) r.Claim {
			t.Helper()
			stored, e := v.repo.Snapshot(ctx, v.p.Identity, snap.SnapshotID)
			if e != nil {
				t.Fatal(e)
			}
			sum := sha256.Sum256([]byte(frozenTemplate))
			templateID, digest := strings.Repeat("a", 32), hex.EncodeToString(sum[:])
			if _, e = v.repo.Admit(ctx, r.Job{ID: r.NewID(), Owner: stored.Owner, Kind: "issue", State: "queued", Key: key, RequestHash: r.IssueRequestHash(templateID, 1, digest, stored.ID, 0), Query: stored.Query, Prepared: stored.Prepared,
				SnapshotID: stored.ID, Format: "pdf", TemplateID: templateID, TemplateVersion: 1, TemplateSHA256: digest, Template: frozenTemplate}, r.Quotas{}); e != nil {
				t.Fatal(e)
			}
			c, e := v.repo.Claim(ctx, time.Minute)
			if e != nil || c == nil || c.Job.Kind != "issue" || c.Job.Generation != 1 {
				t.Fatal(c, e)
			}
			return *c
		}
		code := func(c r.Claim) (string, string) {
			t.Helper()
			st, e := v.repo.Job(ctx, v.p.Identity, c.Job.ID)
			if e != nil {
				t.Fatal(e)
			}
			return st.State, st.ErrorCode
		}
		// A job that is running is not resolved; a code that is none, or the unreconciled one, is invalid.
		first := claimed("first")
		mustError(t, v.repo.ResolveFailure(ctx, first, "TEMPLATE_NOT_PUBLISHED"), r.ErrLease)
		mustError(t, v.repo.ResolveFailure(ctx, first, ""), r.ErrInvalid)
		mustError(t, v.repo.ResolveFailure(ctx, first, "ISSUE_UNRECONCILED"), r.ErrInvalid)
		if st, c := code(first); st != "running" || c != "" {
			t.Fatalf("%s %q", st, c)
		}
		// Failed as unreconciled, it is resolved once, under the claim's generation, without a lease.
		if e := v.repo.Fail(ctx, first, "ISSUE_UNRECONCILED"); e != nil {
			t.Fatal(e)
		}
		wrongGeneration, wrongOwner := first, first
		wrongGeneration.Job.Generation = 2
		wrongOwner.Job.Owner.Subject = "someone-else"
		mustError(t, v.repo.ResolveFailure(ctx, wrongGeneration, "TEMPLATE_NOT_PUBLISHED"), r.ErrLease)
		mustError(t, v.repo.ResolveFailure(ctx, wrongOwner, "TEMPLATE_NOT_PUBLISHED"), r.ErrLease)
		if st, c := code(first); st != "failed" || c != "ISSUE_UNRECONCILED" {
			t.Fatalf("a refused resolution changed the job: %s %q", st, c)
		}
		if e := v.repo.ResolveFailure(ctx, first, "TEMPLATE_NOT_PUBLISHED"); e != nil {
			t.Fatal(e)
		}
		if st, c := code(first); st != "failed" || c != "TEMPLATE_NOT_PUBLISHED" {
			t.Fatalf("%s %q", st, c)
		}
		if got, e := v.repo.Job(ctx, v.p.Identity, first.Job.ID); e != nil || got.Generation != 1 || !got.LeaseUntil.IsZero() {
			t.Fatalf("%+v %v", got, e)
		}
		// Resolved once: the code is no longer the unreconciled one.
		mustError(t, v.repo.ResolveFailure(ctx, first, "EXECUTION_FAILED"), r.ErrLease)
		if st, c := code(first); st != "failed" || c != "TEMPLATE_NOT_PUBLISHED" {
			t.Fatalf("%s %q", st, c)
		}
		// A job that failed with a code of its own is not touched.
		second := claimed("second")
		if e := v.repo.Fail(ctx, second, "INTEGRITY_FAILURE"); e != nil {
			t.Fatal(e)
		}
		mustError(t, v.repo.ResolveFailure(ctx, second, "TEMPLATE_NOT_PUBLISHED"), r.ErrLease)
		if st, c := code(second); st != "failed" || c != "INTEGRITY_FAILURE" {
			t.Fatalf("%s %q", st, c)
		}
		// Only an issue job is resolved: a snapshot job failed with that code is not.
		other, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.ShipmentQuery(), "other")
		if e != nil {
			t.Fatal(e)
		}
		c, e := v.repo.Claim(ctx, time.Minute)
		if e != nil || c == nil || c.Job.ID != other.ID {
			t.Fatal(c, e)
		}
		if e = v.repo.Fail(ctx, *c, "ISSUE_UNRECONCILED"); e != nil {
			t.Fatal(e)
		}
		mustError(t, v.repo.ResolveFailure(ctx, *c, "EXECUTION_FAILED"), r.ErrLease)
		if st, e := v.repo.Job(ctx, v.p.Identity, other.ID); e != nil || st.ErrorCode != "ISSUE_UNRECONCILED" {
			t.Fatalf("%+v %v", st, e)
		}
	})
	// A group issue job (S03-K) is stored with its group in place of a row and publishes a record that keeps it: both
	// stores hand back the group they were given, and refuse a record whose group is not the job's.
	t.Run("jobs_listing_newest_first_under_the_identity_and_its_integrations", func(t *testing.T) {
		// S07-C: Jobs is Issues for every kind: the identity's jobs under the given integrations, newest first, at most limit of them.
		v := envVia(t, through, factory(t))
		first := freeze(t, v, contractsim.FinancialQuery(), "a")
		second := freeze(t, v, contractsim.ShipmentQuery(), "b")
		third := export(t, v, first.SnapshotID, "c")
		fourth, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "d")
		if e != nil || fourth.State != "queued" {
			t.Fatal(fourth, e)
		}
		ids := func(jobs []r.Job) []string {
			out := []string{}
			for _, j := range jobs {
				out = append(out, j.ID)
			}
			return out
		}
		both := []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration}
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 10}); e != nil || !slices.Equal(ids(jobs), []string{fourth.ID, third.ID, second.ID, first.ID}) {
			t.Fatal(ids(jobs), e)
		}
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: []string{contractsim.FinanceIntegration}, Limit: 10}); e != nil || !slices.Equal(ids(jobs), []string{fourth.ID, third.ID, first.ID}) {
			t.Fatal(ids(jobs), e)
		}
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 2}); e != nil || !slices.Equal(ids(jobs), []string{fourth.ID, third.ID}) {
			t.Fatal(ids(jobs), e)
		}
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: r.MaxJobListing}); e != nil || len(jobs) != 4 || jobs[0].State != "queued" || jobs[1].Kind != "export" || jobs[1].ArtifactID != third.ArtifactID || jobs[3].SnapshotID != first.SnapshotID {
			t.Fatal(jobs, e)
		}
		for _, c := range []struct {
			i            r.Identity
			integrations []string
			limit        int
		}{{v.p.Identity, both, 0}, {v.p.Identity, both, r.MaxJobListing + 1}, {v.p.Identity, nil, 10}, {r.Identity{}, both, 10}} {
			_, e := v.repo.Jobs(ctx, c.i, r.JobListing{Integrations: c.integrations, Limit: c.limit})
			mustError(t, e, r.ErrInvalid)
		}
		stranger := v.p.Identity
		stranger.Subject = "someone_else"
		if jobs, e := v.repo.Jobs(ctx, stranger, r.JobListing{Integrations: both, Limit: 10}); e != nil || len(jobs) != 0 {
			t.Fatal(jobs, e)
		}
		// Through the service: the views, in the same order, each what its poll shows; a principal without integrations gets an empty page.
		views, e := v.s.Jobs(ctx, v.p, r.JobListing{Limit: 10})
		if e != nil || len(views) != 4 || views[0].ID != fourth.ID || views[0].State != "queued" || views[1].ID != third.ID || views[1].ArtifactID != third.ArtifactID || views[2].ID != second.ID || views[2].SnapshotID != second.SnapshotID || views[3].ID != first.ID || views[3].Expired {
			t.Fatalf("%+v %v", views, e)
		}
		if _, e = v.s.Jobs(ctx, v.p, r.JobListing{Limit: 0}); !errors.Is(e, r.ErrInvalid) {
			t.Fatal("limit 0 accepted")
		}
		if _, e = v.s.Jobs(ctx, v.p, r.JobListing{Limit: r.MaxJobListing + 1}); !errors.Is(e, r.ErrInvalid) {
			t.Fatal("limit above the maximum accepted")
		}
		// S07-E: kind, state and the cursor, on the repository and through the service.
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 10, Kind: "snapshot"}); e != nil || !slices.Equal(ids(jobs), []string{fourth.ID, second.ID, first.ID}) {
			t.Fatal("kind", ids(jobs), e)
		}
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 10, State: "succeeded"}); e != nil || !slices.Equal(ids(jobs), []string{third.ID, second.ID, first.ID}) {
			t.Fatal("state", ids(jobs), e)
		}
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 10, Kind: "export", State: "queued"}); e != nil || len(jobs) != 0 {
			t.Fatal("kind and state", ids(jobs), e)
		}
		// Pages of two: the second starts after the first's last job, the third is empty; together they are the whole list.
		page1, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 2})
		if e != nil {
			t.Fatal(e)
		}
		page2, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 2, Before: page1[1].ID})
		if e != nil || !slices.Equal(ids(page2), []string{second.ID, first.ID}) {
			t.Fatal("second page", ids(page2), e)
		}
		if page3, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 2, Before: page2[1].ID}); e != nil || len(page3) != 0 {
			t.Fatal("third page", ids(page3), e)
		}
		// The cursor with a filter: snapshots older than the export (third) are second and first.
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 10, Kind: "snapshot", Before: third.ID}); e != nil || !slices.Equal(ids(jobs), []string{second.ID, first.ID}) {
			t.Fatal("cursor and kind", ids(jobs), e)
		}
		// A cursor that is not the identity's job, or not under these integrations, or no job at all, gives an empty page.
		if jobs, e := v.repo.Jobs(ctx, stranger, r.JobListing{Integrations: both, Limit: 10, Before: third.ID}); e != nil || len(jobs) != 0 {
			t.Fatal("another identity's cursor", ids(jobs), e)
		}
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: []string{contractsim.FinanceIntegration}, Limit: 10, Before: second.ID}); e != nil || len(jobs) != 0 {
			t.Fatal("a cursor under another integration", ids(jobs), e)
		}
		if jobs, e := v.repo.Jobs(ctx, v.p.Identity, r.JobListing{Integrations: both, Limit: 10, Before: strings.Repeat("0", 32)}); e != nil || len(jobs) != 0 {
			t.Fatal("an unknown cursor", ids(jobs), e)
		}
		for _, l := range []r.JobListing{{Integrations: both, Limit: 10, Kind: "other"}, {Integrations: both, Limit: 10, State: "done"}, {Integrations: both, Limit: 10, Before: "not an id"}} {
			_, e := v.repo.Jobs(ctx, v.p.Identity, l)
			mustError(t, e, r.ErrInvalid)
		}
		if views, e := v.s.Jobs(ctx, v.p, r.JobListing{Limit: 10, Kind: "export", Before: fourth.ID}); e != nil || len(views) != 1 || views[0].ID != third.ID {
			t.Fatalf("service filter: %+v %v", views, e)
		}
		if _, e := v.s.Jobs(ctx, v.p, r.JobListing{Limit: 10, Integrations: both}); !errors.Is(e, r.ErrInvalid) {
			t.Fatal("the caller chose the integrations")
		}
		none := v.p
		none.Integrations = nil
		if views, e := v.s.Jobs(ctx, none, r.JobListing{Limit: 10}); e != nil || len(views) != 0 {
			t.Fatal(views, e)
		}
		other := v.p
		other.Subject = "someone_else"
		if views, e := v.s.Jobs(ctx, other, r.JobListing{Limit: 10}); e != nil || len(views) != 0 {
			t.Fatal(views, e)
		}
	})
	t.Run("a_group_issue_job_and_its_record_round_trip", func(t *testing.T) {
		v := envVia(t, through, factory(t))
		snap := freeze(t, v, contractsim.FinancialQuery(), "snap")
		stored, e := v.repo.Snapshot(ctx, v.p.Identity, snap.SnapshotID)
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256([]byte(frozenTemplate))
		templateID, digest := strings.Repeat("a", 32), hex.EncodeToString(sum[:])
		rows := stored.Result.Rows
		group := r.IssueGroup{Fields: []string{}, Key: []string{}, Rows: []int{0, 1, 2}, RowsSHA256: r.RowsDigest(rows)}
		job := func(key string, g r.IssueGroup) r.Job {
			return r.Job{ID: r.NewID(), Owner: stored.Owner, Kind: "issue", State: "queued", Key: key, RequestHash: r.IssueGroupRequestHash(templateID, 1, digest, stored.ID, g), Query: stored.Query, Prepared: stored.Prepared,
				SnapshotID: stored.ID, Format: "pdf", TemplateID: templateID, TemplateVersion: 1, TemplateSHA256: digest, Template: frozenTemplate, Group: &g}
		}
		j := job("group", group)
		admitted, e := v.repo.Admit(ctx, j, r.Quotas{})
		if e != nil || admitted.Group == nil || !admitted.Group.Equal(&group) || admitted.RowIndex != 0 {
			t.Fatalf("%+v %v", admitted, e)
		}
		// The same key and group again is the same job; another group under the key is a conflict; a job whose hash is
		// not its group's, or that has a row too, is not admitted.
		if again, e := v.repo.Admit(ctx, job("group", group), r.Quotas{}); e != nil || again.ID != j.ID {
			t.Fatal(again, e)
		}
		other := group.Clone()
		other.Rows = []int{0, 1}
		mustError(t, func() error { _, e := v.repo.Admit(ctx, job("group", other), r.Quotas{}); return e }(), r.ErrConflict)
		wrongHash := job("wrong hash", group)
		wrongHash.RequestHash = r.IssueRequestHash(templateID, 1, digest, stored.ID, 0)
		mustError(t, func() error { _, e := v.repo.Admit(ctx, wrongHash, r.Quotas{}); return e }(), r.ErrInvalid)
		withRow := job("with a row", group)
		withRow.RowIndex = 1
		mustError(t, func() error { _, e := v.repo.Admit(ctx, withRow, r.Quotas{}); return e }(), r.ErrInvalid)
		claim, e := v.repo.Claim(ctx, time.Minute)
		if e != nil || claim == nil || claim.Job.ID != j.ID || claim.Job.Group == nil || !claim.Job.Group.Equal(&group) {
			t.Fatal(claim, e)
		}
		content := []byte("%PDF-1.7 group")
		fileSum := sha256.Sum256(content)
		artifact := r.Artifact{ID: r.NewID(), Owner: stored.Owner, SnapshotID: stored.ID, Format: "pdf", ExpiresAt: stored.Prepared.ExpiresAt, Content: content, SHA256: hex.EncodeToString(fileSum[:]), Issue: j.ID}
		at := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
		rec := r.Issue{ID: j.ID, Owner: stored.Owner, Number: "AR-000001", TemplateID: templateID, TemplateVersion: 1, TemplateSHA256: digest, SnapshotID: stored.ID, SnapshotSHA256: stored.SHA256, Group: &group, ArtifactID: artifact.ID, SHA256: artifact.SHA256, Pages: 1,
			IssuedAt: at, ReservationRef: "res-1", ConfirmationRef: "con-1", ConfirmedAt: at.Add(time.Second)}
		rec.Stamp = r.IssueStamp(rec.Number, at, rec.ID)
		// A record whose group is not the job's is refused and publishes nothing.
		wrong := rec
		wg := group.Clone()
		wg.Key = []string{"x"}
		wg.Fields = []string{"x"}
		wrong.Group = &wg
		mustError(t, v.repo.Publish(ctx, *claim, r.Publication{Artifact: &artifact, Issue: &wrong}), r.ErrIntegrity)
		noGroup := rec
		noGroup.Group = nil
		mustError(t, v.repo.Publish(ctx, *claim, r.Publication{Artifact: &artifact, Issue: &noGroup}), r.ErrIntegrity)
		if st, e := v.repo.Job(ctx, v.p.Identity, j.ID); e != nil || st.State != "running" {
			t.Fatalf("a refused publication changed the job: %+v %v", st, e)
		}
		if e = v.repo.Publish(ctx, *claim, r.Publication{Artifact: &artifact, Issue: &rec}); e != nil {
			t.Fatal(e)
		}
		got, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
		if e != nil || got.Group == nil || !got.Group.Equal(&group) || got.RowIndex != 0 || got.Number != "AR-000001" {
			t.Fatalf("%+v %v", got, e)
		}
		done, e := v.repo.Job(ctx, v.p.Identity, j.ID)
		if e != nil || done.State != "succeeded" || done.Group == nil || !done.Group.Equal(&group) {
			t.Fatalf("%+v %v", done, e)
		}
	})
	// S24-A (ADR 0025 decision 3): the cancellation of an issued document is a row of its own beside the record, kept once with its audit
	// event, its owner's alone, and read back with the record (cancellation_test.go).
	t.Run("an_issue_cancellation_is_kept_once_beside_its_record_with_its_audit_event", func(t *testing.T) {
		cancellationContract(t, envVia(t, through, factory(t)))
	})
}
func TestMemoryContract(t *testing.T) {
	runContract(t, direct, func(*testing.T) r.Repository { return memory.New(time.Now) })
}

// TestMemoryContractOverHTTP is the same contract with every simulator reached through the reporting source protocol (S19-A).
func TestMemoryContractOverHTTP(t *testing.T) {
	runContract(t, overHTTP, func(*testing.T) r.Repository { return memory.New(time.Now) })
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
	reset := func(t *testing.T) r.Repository {
		if e := store.ResetFixture(ctx); e != nil {
			t.Fatal(e)
		}
		return store
	}
	t.Run("direct", func(t *testing.T) { runContract(t, direct, reset) })
	t.Run("over_http", func(t *testing.T) { runContract(t, overHTTP, reset) })
}
