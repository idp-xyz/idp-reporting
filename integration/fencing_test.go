package integration_test

import (
	"context"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"sync"
	"testing"
	"time"
)

func publication(c r.Claim) r.Publication {
	j := c.Job
	result := r.Result{Columns: j.Columns, Rows: [][]string{{"customer_a", "current", "100.25"}, {"customer_b", "1_30", "-0.24"}, {"customer_c", "31_60", "9007199254740893.00"}}, Totals: map[string]string{"ar.open_balance": "9007199254740993.01"}, Completeness: "complete", Issues: []string{}, Evidence: r.Evidence{QueryHash: j.Prepared.QueryHash, SourceRef: j.Prepared.Ref, Watermark: j.Prepared.Watermark, DataClass: "contract-simulator"}}
	return r.Publication{Snapshot: &r.Snapshot{ID: r.NewID(), Owner: j.Owner, Query: j.Query, Prepared: j.Prepared, Result: result, ExpiresAt: j.Prepared.ExpiresAt, SHA256: r.Digest(result)}}
}
func TestLeaseGenerationAndAtomicPublication(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	repo := memory.New(func() time.Time { return now })
	v := env(t, repo)
	_, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
	if e != nil {
		t.Fatal(e)
	}
	old, e := repo.Claim(ctx, time.Second)
	if e != nil || old == nil {
		t.Fatal(old, e)
	}
	now = now.Add(2 * time.Second)
	next, e := repo.Claim(ctx, time.Second)
	if e != nil || next == nil || next.Job.Generation != old.Job.Generation+1 {
		t.Fatal(next, e)
	}
	pub := publication(*old)
	mustError(t, repo.Publish(ctx, *old, pub), r.ErrLease)
	_, e = repo.Snapshot(ctx, v.p.Identity, pub.Snapshot.ID)
	mustError(t, e, r.ErrNotFound)
	good := publication(*next)
	if e = repo.Publish(ctx, *next, good); e != nil {
		t.Fatal(e)
	}
	job, e := repo.Job(ctx, v.p.Identity, next.Job.ID)
	if e != nil || job.State != "succeeded" || job.SnapshotID != good.Snapshot.ID {
		t.Fatal(job, e)
	}
	if _, e = repo.Snapshot(ctx, v.p.Identity, job.SnapshotID); e != nil {
		t.Fatal(e)
	}
	mustError(t, repo.Publish(ctx, *next, good), r.ErrLease)
}
func TestCancelPublicationRaceHasOneOutcome(t *testing.T) {
	ctx := context.Background()
	repo := memory.New(time.Now)
	v := env(t, repo)
	j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
	if e != nil {
		t.Fatal(e)
	}
	claim, e := repo.Claim(ctx, time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	pub := publication(*claim)
	var wg sync.WaitGroup
	wg.Add(2)
	var publishErr, cancelErr error
	go func() { defer wg.Done(); publishErr = repo.Publish(ctx, *claim, pub) }()
	go func() { defer wg.Done(); _, cancelErr = repo.Cancel(ctx, v.p.Identity, j.ID) }()
	wg.Wait()
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	jstate, e := repo.Job(ctx, v.p.Identity, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, assetErr := repo.Snapshot(ctx, v.p.Identity, pub.Snapshot.ID)
	if jstate.State == "cancelled" {
		mustError(t, publishErr, r.ErrLease)
		mustError(t, assetErr, r.ErrNotFound)
	} else if jstate.State == "succeeded" {
		if publishErr != nil || assetErr != nil {
			t.Fatal(publishErr, assetErr)
		}
	} else {
		t.Fatal(jstate)
	}
}

type intercept struct {
	r.Adapter
	afterRead func()
	mutate    func(r.SourcePage) r.SourcePage
}

func (a intercept) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	v, e := a.Adapter.ReadPage(ctx, sc, p, cursor, limit)
	if e == nil && a.afterRead != nil {
		a.afterRead()
	}
	if e == nil && a.mutate != nil {
		v = a.mutate(v)
	}
	return v, e
}
func intercepted(t *testing.T, a r.Adapter) (*r.Service, r.Principal, *memory.Store) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	repo := memory.New(time.Now)
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901")})
	if e != nil {
		t.Fatal(e)
	}
	return s, p, repo
}
func TestRevocationAtFinalReadDoesNotPublish(t *testing.T) {
	a := contractsim.Financial()
	p := contractsim.DemoPrincipal()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	wrapped := intercept{Adapter: a, afterRead: func() { a.Revoke(contractsim.DemoOwner(p, contractsim.FinanceIntegration)) }}
	s, p, repo := intercepted(t, wrapped)
	ctx := context.Background()
	j, e := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "one")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.RunOne(ctx)
	mustError(t, e, r.ErrDenied)
	state, e := repo.Job(ctx, p.Identity, j.ID)
	if e != nil || state.State != "failed" || state.SnapshotID != "" {
		t.Fatal(state, e)
	}
}
func TestSourceBindingMismatchRejected(t *testing.T) {
	a := contractsim.Financial()
	p := contractsim.DemoPrincipal()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	wrapped := intercept{Adapter: a, mutate: func(v r.SourcePage) r.SourcePage { v.Watermark = "unrelated"; return v }}
	s, p, _ := intercepted(t, wrapped)
	_, e := s.Query(context.Background(), p, contractsim.FinancialQuery())
	mustError(t, e, r.ErrIntegrity)
}
func TestExpiredAssetsCannotDisclose(t *testing.T) {
	ctx := context.Background()
	repo := memory.New(time.Now)
	v := env(t, repo)
	j := freeze(t, v, contractsim.FinancialQuery(), "one")
	ex := export(t, v, j.SnapshotID, "one")
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: v.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: v.a}}, CursorKey: []byte("01234567890123456789012345678901"), Now: func() time.Time { return time.Now().Add(time.Hour) }})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Rows(ctx, v.p, j.SnapshotID, 1, "")
	mustError(t, e, r.ErrIntegrity)
	_, e = s.Download(ctx, v.p, ex.ArtifactID)
	if !errors.Is(e, r.ErrIntegrity) {
		t.Fatal(e)
	}
}
