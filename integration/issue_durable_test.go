package integration_test

import (
	"context"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePDFWriter struct{}

func (fakePDFWriter) Write(context.Context, r.Snapshot) ([]byte, error) {
	return []byte("%PDF-1.7 x"), nil
}

// stampIssuer answers with a PDF carrying the stamp and the issue time, from its request alone as
// the real issuer does (the same request gives the same bytes); it never touches a template store.
type stampIssuer struct {
	mu    sync.Mutex
	calls int
	err   error
	hook  func() // runs inside the call: the world changes while the document renders
}

func (f *stampIssuer) Issue(_ context.Context, req r.IssueRequest) (r.IssueResult, error) {
	f.mu.Lock()
	f.calls++
	err, hook := f.err, f.hook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return r.IssueResult{}, err
	}
	return r.IssueResult{PDF: []byte("%PDF-1.7\n" + req.Stamp + "\n" + req.At.Format(time.RFC3339) + "\n%%EOF"), Pages: 3}, nil
}

func issueDurableService(t *testing.T, v durableEnv, issuer r.Issuer) *r.Service {
	t.Helper()
	return issueDurableServiceOn(t, v, v.repo, issuer)
}

// issueDurableServiceOn is the service over another repository, one that wraps the store; the
// durable source implements the document authority of both integrations.
func issueDurableServiceOn(t *testing.T, v durableEnv, repo r.Repository, issuer r.Issuer) *r.Service {
	t.Helper()
	connections := []r.Connection{}
	for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
		connections = append(connections, r.Connection{Tenant: v.p.Tenant, Integration: id, SourceTenant: "tenant_a", Adapter: v.source})
	}
	s, e := r.NewService(r.Config{Repository: repo, Connections: connections, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: issuer})
	if e != nil {
		t.Fatal(e)
	}
	return s
}

const frozenTemplate = `{"schema_version":"1.0","kind":"document","binding":{"integration_id":"i","dataset":{"id":"d","version":"1"}},"elements":[]}`

func issueInput(snapshot string, row int) r.IssueInput {
	return r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(frozenTemplate), SnapshotID: snapshot, RowIndex: row}
}

// shortLease claims with a lease of two seconds whatever the worker asks for, and makes its first
// publications outlast it: a worker that lost its lease for real, to the store's own clock.
type shortLease struct {
	r.Repository
	mu   sync.Mutex
	slow int
}

func (w *shortLease) Claim(ctx context.Context, _ time.Duration) (*r.Claim, error) {
	return w.Repository.Claim(ctx, 2*time.Second)
}
func (w *shortLease) Publish(ctx context.Context, c r.Claim, p r.Publication) error {
	w.mu.Lock()
	slow := w.slow > 0
	if slow {
		w.slow--
	}
	w.mu.Unlock()
	if slow {
		time.Sleep(2200 * time.Millisecond)
	}
	return w.Repository.Publish(ctx, c, p)
}

// productRecord is what the durable source holds for a job, by the job id.
func productRecord(t *testing.T, v durableEnv, id string) (contractsim.IssueRecord, bool) {
	t.Helper()
	rec, ok, e := v.source.IssueRecord(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	return rec, ok
}

// TestDurablePostgresIssues covers the issue kind on the PostgreSQL fixture under the source
// product's authority (S03-H), with the durable simulator as the product: its numbers per
// integration, the record with its two references, the void of a failed job and what happens when
// it cannot be delivered, a lost lease, cancellation, the listing, the v2→v4 fixture upgrade and
// tampering.
func TestDurablePostgresIssues(t *testing.T) {
	if os.Getenv("REPORTING_S01_TEST_DSN") == "" || os.Getenv("REPORTING_S01_SOURCE_DSN") == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required platform/source PostgreSQL DSNs missing")
		}
		t.Skip("durable PostgreSQL gate not run: two isolated DB DSNs required")
	}
	ctx := context.Background()
	t.Run("numbers_records_and_listing", func(t *testing.T) {
		v := newDurable(t)
		issuer := &stampIssuer{}
		s := issueDurableService(t, v, issuer)
		snap := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.FinancialQuery(), "snap")
		first, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		// The job has no number: the product gives it when the worker prepares the document.
		if e != nil || first.Issue == nil || first.Issue.Number != "" {
			t.Fatal(first, e)
		}
		if _, held := productRecord(t, v, first.ID); held {
			t.Fatal("admission reached the product")
		}
		// A replay returns the same job; another input under the same key conflicts.
		if again, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one"); e != nil || again.ID != first.ID {
			t.Fatal(again, e)
		}
		if _, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 1), "one"); !errors.Is(e, r.ErrConflict) {
			t.Fatal("conflicting replay admitted", e)
		}
		second, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 1), "two")
		if e != nil {
			t.Fatal(e)
		}
		// A queued job is cancelled; it never reached the product and holds no number.
		if c, e := s.Cancel(ctx, v.p, second.ID); e != nil || c.State != "cancelled" || c.Issue.Number != "" {
			t.Fatal(c, e)
		}
		third, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 2), "three")
		if e != nil {
			t.Fatal(e)
		}
		// The worker issues the first job: the product's number, the file confirmed by its digest,
		// artifact and record in one transaction.
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		done, e := s.Job(ctx, v.p, first.ID)
		if e != nil || done.State != "succeeded" || done.ArtifactID == "" || done.Issue.Number != "AR-000001" {
			t.Fatal(done, e)
		}
		rec, e := v.repo.Issue(ctx, v.p.Identity, first.ID)
		if e != nil || rec.Number != "AR-000001" || rec.ArtifactID != done.ArtifactID || rec.Pages != 3 || rec.Stamp != r.IssueStamp("AR-000001", rec.IssuedAt, first.ID) || rec.IssuedAt.IsZero() || rec.ReservationRef == "" || rec.ConfirmationRef == "" || rec.ConfirmedAt.Before(rec.IssuedAt) {
			t.Fatalf("%+v %v", rec, e)
		}
		a, e := s.Download(ctx, v.p, done.ArtifactID)
		if e != nil || a.Issue != first.ID || a.SHA256 != rec.SHA256 || !strings.Contains(string(a.Content), rec.Stamp) {
			t.Fatal(a.Issue, e)
		}
		theirs, held := productRecord(t, v, first.ID)
		if !held || theirs.State != contractsim.IssueConfirmed || theirs.Number != "AR-000001" || theirs.SHA256 != a.SHA256 || theirs.Pages != 3 || theirs.Ref != rec.ReservationRef || theirs.ConfirmationRef != rec.ConfirmationRef || !theirs.ConfirmedAt.Equal(rec.ConfirmedAt) || !theirs.IssuedAt.Equal(rec.IssuedAt) {
			t.Fatalf("the product's record %+v against ours %+v", theirs, rec)
		}
		view, e := s.Issue(ctx, v.p, first.ID)
		if e != nil || view.Number != "AR-000001" || view.SHA256 != rec.SHA256 || view.Pages != 3 || view.IssuedAt != rec.IssuedAt.UTC().Format(time.RFC3339) || view.ReservationRef != rec.ReservationRef || view.ConfirmationRef != rec.ConfirmationRef || view.ConfirmedAt != rec.ConfirmedAt.UTC().Format(time.RFC3339) {
			t.Fatalf("%+v %v", view, e)
		}
		// The third job fails on the issuer's word, after the product reserved AR-000002 for it: the
		// reservation is voided under the job's error code, the job shows no number.
		issuer.mu.Lock()
		issuer.err = r.ErrUnpublished
		issuer.mu.Unlock()
		if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrUnpublished) {
			t.Fatal(e)
		}
		voided, held := productRecord(t, v, third.ID)
		if !held || voided.State != contractsim.IssueVoid || voided.Reason != "TEMPLATE_NOT_PUBLISHED" || voided.Number != "AR-000002" {
			t.Fatalf("the product's record of the failed job: %+v %v", voided, held)
		}
		if _, held := productRecord(t, v, second.ID); held {
			t.Fatal("the cancelled job reached the product")
		}
		// A voided number stays voided: the next document is AR-000003. Another integration is
		// another product with its own numbers: SH-000001.
		issuer.mu.Lock()
		issuer.err = nil
		issuer.mu.Unlock()
		shipment := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.ShipmentQuery(), "ship")
		fourth, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "four")
		if e != nil {
			t.Fatal(e)
		}
		shipped, e := s.SubmitIssue(ctx, v.p, issueInput(shipment.SnapshotID, 0), "ship-issue")
		if e != nil {
			t.Fatal(e)
		}
		for range 2 {
			if _, e = s.RunOne(ctx); e != nil {
				t.Fatal(e)
			}
		}
		if got, e := s.Job(ctx, v.p, fourth.ID); e != nil || got.State != "succeeded" || got.Issue.Number != "AR-000003" {
			t.Fatalf("%+v %v", got, e)
		}
		if got, e := s.Job(ctx, v.p, shipped.ID); e != nil || got.State != "succeeded" || got.Issue.Number != "SH-000001" {
			t.Fatalf("%+v %v", got, e)
		}
		list, e := s.Issues(ctx, v.p, 10)
		if e != nil || len(list) != 5 || list[0].ID != shipped.ID || list[0].Number != "SH-000001" || list[1].ID != fourth.ID || list[1].Number != "AR-000003" || list[2].ID != third.ID || list[2].State != "failed" || list[2].ErrorCode != "TEMPLATE_NOT_PUBLISHED" || list[2].Number != "" || list[3].ID != second.ID || list[3].State != "cancelled" || list[3].Number != "" || list[4].ID != first.ID || list[4].Number != "AR-000001" || list[4].SHA256 != rec.SHA256 {
			t.Fatalf("%+v %v", list, e)
		}
		if list, e = s.Issues(ctx, v.p, 2); e != nil || len(list) != 2 || list[0].ID != shipped.ID {
			t.Fatalf("%+v %v", list, e)
		}
		// Another identity sees none of it.
		stranger := v.p
		stranger.Subject = "stranger"
		if list, e = s.Issues(ctx, stranger, 10); e != nil || len(list) != 0 {
			t.Fatal(list, e)
		}
		if _, e = v.repo.Issue(ctx, stranger.Identity, first.ID); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("record visible to another subject", e)
		}
		// The listing is filtered by the principal's integrations before the limit: a shipment
		// issue does not push a finance issue off a page of one.
		finance := v.p
		finance.Integrations = []string{contractsim.FinanceIntegration}
		if list, e = s.Issues(ctx, finance, 1); e != nil || len(list) != 1 || list[0].ID != fourth.ID {
			t.Fatalf("%+v %v", list, e)
		}
		if list, e = s.Issues(ctx, v.p, 1); e != nil || len(list) != 1 || list[0].ID != shipped.ID || list[0].State != "succeeded" || list[0].Number != "SH-000001" {
			t.Fatalf("%+v %v", list, e)
		}
		// An export artifact is not an issue and never gets a number.
		ex := durableExport(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, snap.SnapshotID, "export")
		if ex.Issue != nil {
			t.Fatal(ex)
		}
		if _, e = s.Issue(ctx, v.p, ex.ID); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("export read as an issue", e)
		}
	})
	t.Run("concurrent_workers_take_distinct_consecutive_numbers_from_the_product", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		snap := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.FinancialQuery(), "snap")
		const n = 6
		ids := make([]string, n)
		for i := range n {
			j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, i%3), "key-"+string(rune('a'+i)))
			if e != nil {
				t.Fatal(e)
			}
			ids[i] = j.ID
		}
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					ran, e := s.RunOne(ctx)
					if e != nil {
						errs[i] = e
						return
					}
					if !ran {
						return
					}
				}
			}()
		}
		wg.Wait()
		seen := map[string]bool{}
		for i := range n {
			if errs[i] != nil {
				t.Fatalf("worker %d: %v", i, errs[i])
			}
			got, e := s.Job(ctx, v.p, ids[i])
			if e != nil || got.State != "succeeded" || got.Issue.Number < "AR-000001" || got.Issue.Number > "AR-000006" || seen[got.Issue.Number] {
				t.Fatalf("job %d: %+v %v (seen %v)", i, got, e, seen)
			}
			seen[got.Issue.Number] = true
			if rec, held := productRecord(t, v, ids[i]); !held || rec.State != contractsim.IssueConfirmed || rec.Number != got.Issue.Number {
				t.Fatalf("the product's record of job %d: %+v %v", i, rec, held)
			}
		}
	})
	// A failed job voids at the product when the worker reached it (or an earlier attempt may
	// have), and calls nothing when it fails before; a void that cannot be delivered is
	// ISSUE_UNRECONCILED, with the reservation left at the product to be found by the job id.
	t.Run("failure_voids_at_the_product_and_unreconciled_when_it_cannot", func(t *testing.T) {
		v := newDurable(t)
		issuer := &stampIssuer{}
		s := issueDurableService(t, v, issuer)
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		sc := sourceContext(v.p, contractsim.FinanceIntegration)
		// Before the product: the grant is revoked, the first attempt fails at the source check and
		// calls nothing at the product, which holds nothing for the job.
		before, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "before")
		if e != nil {
			t.Fatal(e)
		}
		if e = v.source.Revoke(ctx, sc); e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrDenied) {
			t.Fatal(e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, before.ID); e != nil || st.State != "failed" || st.ErrorCode != "AUTH_SCOPE_DENIED" || st.Generation != 1 {
			t.Fatalf("%+v %v", st, e)
		}
		if rec, held := productRecord(t, v, before.ID); held {
			t.Fatalf("the product holds %+v for a job that failed before reaching it", rec)
		}
		if e = v.source.SetGrant(ctx, sc, contractsim.DefaultGrant()); e != nil { // EnsureGrant would leave it revoked
			t.Fatal(e)
		}
		// The product refuses (its approval switch): AUTH_SCOPE_DENIED, nothing reserved, no number
		// used.
		if e = v.source.SetIssueRefused(ctx, contractsim.FinanceIntegration, "tenant_a", true); e != nil {
			t.Fatal(e)
		}
		refused, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "refused")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrDenied) {
			t.Fatal(e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, refused.ID); e != nil || st.State != "failed" || st.ErrorCode != "AUTH_SCOPE_DENIED" {
			t.Fatalf("%+v %v", st, e)
		}
		if rec, held := productRecord(t, v, refused.ID); held && rec.State != contractsim.IssueVoid {
			t.Fatalf("the product holds %+v for a refused job", rec)
		}
		if e = v.source.SetIssueRefused(ctx, contractsim.FinanceIntegration, "tenant_a", false); e != nil {
			t.Fatal(e)
		}
		// After the product: the render fails; the product voids what it reserved (AR-000001, the
		// first number given out) with the code as the reason.
		issuer.mu.Lock()
		issuer.err = r.ErrUnpublished
		issuer.mu.Unlock()
		after, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 1), "after")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrUnpublished) {
			t.Fatal(e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, after.ID); e != nil || st.State != "failed" || st.ErrorCode != "TEMPLATE_NOT_PUBLISHED" {
			t.Fatalf("%+v %v", st, e)
		}
		if rec, held := productRecord(t, v, after.ID); !held || rec.State != contractsim.IssueVoid || rec.Reason != "TEMPLATE_NOT_PUBLISHED" || rec.Number != "AR-000001" {
			t.Fatalf("the product's record of the failed job: %+v %v", rec, held)
		}
		// The source goes unavailable while the document renders: the confirmation fails and so does
		// the void, so the job is ISSUE_UNRECONCILED and the product still holds the reservation.
		issuer.mu.Lock()
		issuer.err = nil
		issuer.hook = func() {
			if e := v.source.SetUnavailable(ctx, contractsim.FinanceIntegration, "tenant_a", true); e != nil {
				t.Error(e)
			}
		}
		issuer.mu.Unlock()
		stuck, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 2), "stuck")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrUnreconciled) {
			t.Fatal(e)
		}
		issuer.mu.Lock()
		issuer.hook = nil
		issuer.mu.Unlock()
		if e = v.source.SetUnavailable(ctx, contractsim.FinanceIntegration, "tenant_a", false); e != nil {
			t.Fatal(e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, stuck.ID); e != nil || st.State != "failed" || st.ErrorCode != "ISSUE_UNRECONCILED" {
			t.Fatalf("%+v %v", st, e)
		}
		if rec, held := productRecord(t, v, stuck.ID); !held || rec.State != contractsim.IssueReserved || rec.Number != "AR-000002" {
			t.Fatalf("the reservation the product keeps for the unreconciled job: %+v %v", rec, held)
		}
		if got, e := s.Issue(ctx, v.p, stuck.ID); e != nil || got.State != "failed" || got.ErrorCode != "ISSUE_UNRECONCILED" || got.Number != "" {
			t.Fatalf("%+v %v", got, e)
		}
		// A second-generation claim voids even when it fails before reaching the product: a worker
		// took the job and vanished, an earlier attempt may have reserved. The source is unavailable
		// then, so this void cannot be delivered either.
		late, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "late")
		if e != nil {
			t.Fatal(e)
		}
		crashed, e := v.repo.Claim(ctx, time.Second)
		if e != nil || crashed == nil || crashed.Job.ID != late.ID || crashed.Job.Generation != 1 {
			t.Fatal(crashed, e)
		}
		time.Sleep(1100 * time.Millisecond)
		if e = v.source.SetUnavailable(ctx, contractsim.FinanceIntegration, "tenant_a", true); e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrUnreconciled) || !errors.Is(e, r.ErrUnavailable) {
			t.Fatal(e)
		}
		if e = v.source.SetUnavailable(ctx, contractsim.FinanceIntegration, "tenant_a", false); e != nil {
			t.Fatal(e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, late.ID); e != nil || st.State != "failed" || st.ErrorCode != "ISSUE_UNRECONCILED" || st.Generation != 2 {
			t.Fatalf("%+v %v", st, e)
		}
	})
	// A worker whose lease ends for real (the store's clock) while it publishes loses the job without
	// voiding anything: the next claim reuses the product's reservation, renders the same bytes,
	// has them confirmed again (idempotently) and publishes.
	t.Run("lost_lease_does_not_void_and_the_next_claim_confirms_again", func(t *testing.T) {
		v := newDurable(t)
		issuer := &stampIssuer{}
		wrapper := &shortLease{Repository: v.repo}
		s := issueDurableServiceOn(t, v, wrapper, issuer)
		snap := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.FinancialQuery(), "snap")
		j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 1), "one")
		if e != nil {
			t.Fatal(e)
		}
		wrapper.slow = 1
		if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, r.ErrLease) {
			t.Fatal(ran, e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, j.ID); e != nil || st.State != "running" || st.Generation != 1 {
			t.Fatalf("a lost lease finished or failed the job: %+v %v", st, e)
		}
		confirmed, held := productRecord(t, v, j.ID)
		if !held || confirmed.State != contractsim.IssueConfirmed || confirmed.Number != "AR-000001" {
			t.Fatalf("the first worker's file was confirmed and must stay so: %+v %v", confirmed, held)
		}
		if ran, e := s.RunOne(ctx); !ran || e != nil {
			t.Fatal(ran, e)
		}
		st, e := v.repo.Job(ctx, v.p.Identity, j.ID)
		if e != nil || st.State != "succeeded" || st.Generation != 2 {
			t.Fatalf("%+v %v", st, e)
		}
		rec, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
		after, held := productRecord(t, v, j.ID)
		if e != nil || !held || after.State != contractsim.IssueConfirmed || after.Number != "AR-000001" || after.Ref != confirmed.Ref || after.ConfirmationRef != confirmed.ConfirmationRef || rec.Number != "AR-000001" || rec.ReservationRef != confirmed.Ref || rec.ConfirmationRef != confirmed.ConfirmationRef || !rec.ConfirmedAt.Equal(confirmed.ConfirmedAt) {
			t.Fatalf("%+v %+v %v", rec, after, e)
		}
		a, e := s.Download(ctx, v.p, st.ArtifactID)
		if e != nil || a.SHA256 != after.SHA256 {
			t.Fatal("the file confirmed is not the file published", e)
		}
		// No number was spent on the second claim.
		next, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 2), "two")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if got, e := s.Job(ctx, v.p, next.ID); e != nil || got.Issue.Number != "AR-000002" {
			t.Fatalf("%+v %v", got, e)
		}
	})
	// An issue job is cancelled only while queued; a running one is returned unchanged, with its
	// lease. Another kind is cancelled while running, as before.
	t.Run("a_running_issue_job_is_not_cancelled", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		snap := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.FinancialQuery(), "snap")
		queued, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "queued")
		if e != nil {
			t.Fatal(e)
		}
		if c, e := s.Cancel(ctx, v.p, queued.ID); e != nil || c.State != "cancelled" {
			t.Fatal(c, e)
		}
		running, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 1), "running")
		if e != nil {
			t.Fatal(e)
		}
		claim, e := v.repo.Claim(ctx, time.Minute)
		if e != nil || claim == nil || claim.Job.ID != running.ID {
			t.Fatal(claim, e)
		}
		c, e := s.Cancel(ctx, v.p, running.ID)
		if e != nil || c.State != "running" || c.Kind != "issue" {
			t.Fatal("a running issue job was cancelled", c, e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, running.ID); e != nil || st.State != "running" || st.Generation != 1 || st.LeaseUntil.IsZero() {
			t.Fatalf("%+v %v", st, e)
		}
		other, e := s.SubmitSnapshot(ctx, v.p, contractsim.ShipmentQuery(), "other")
		if e != nil {
			t.Fatal(e)
		}
		if claim, e = v.repo.Claim(ctx, time.Minute); e != nil || claim == nil || claim.Job.ID != other.ID {
			t.Fatal(claim, e)
		}
		if c, e = s.Cancel(ctx, v.p, other.ID); e != nil || c.State != "cancelled" {
			t.Fatal("a running snapshot job was not cancelled", c, e)
		}
	})
	// A ledger at version 2 (an S04-A database taken down to 2): the upgrade keeps an export
	// artifact and adds the issue kind. The pre-ledger v2 database is the migrate test's legacy case.
	t.Run("fixture_v2_upgrade_adds_issues_and_keeps_assets", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		ex := durableExport(t, env, snap.SnapshotID, "export")
		before, e := s.Download(ctx, v.p, ex.ArtifactID)
		if e != nil {
			t.Fatal(e)
		}
		v.repo.Close()
		p := fixtureAdmin(t)
		if _, e = p.Exec(ctx, `DELETE FROM reporting_s01.schema_migrations WHERE version>2;
ALTER TABLE reporting_s01.jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE reporting_s01.jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('snapshot','export'));
ALTER TABLE reporting_s01.assets DROP CONSTRAINT assets_kind_check;
ALTER TABLE reporting_s01.assets ADD CONSTRAINT assets_kind_check CHECK(kind IN ('snapshot','artifact'));
UPDATE reporting_s01.profile SET version=2 WHERE singleton;`); e != nil {
			t.Fatal(e)
		}
		bad, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false)
		if bad != nil {
			bad.Close()
		}
		mustError(t, e, r.ErrDenied)
		reopened, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), true)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		v.repo = reopened
		s = issueDurableService(t, v, &stampIssuer{})
		after, e := s.Download(ctx, v.p, ex.ArtifactID)
		if e != nil || after.SHA256 != before.SHA256 {
			t.Fatal(e)
		}
		j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if rec, e := reopened.Issue(ctx, v.p.Identity, j.ID); e != nil || rec.Number != "AR-000001" {
			t.Fatal(rec, e)
		}
	})
	t.Run("tampered_record_or_job_is_integrity_failure", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Issue(ctx, v.p, j.ID); e != nil {
			t.Fatal(e)
		}
		p := fixtureAdmin(t)
		// The record's number changed with its digest recomputed: the stamp no longer matches.
		for _, sql := range []string{`UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Number}','"AR-000007"') WHERE id=$1 AND kind='issue'`, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1 AND kind='issue'`} {
			if _, e = p.Exec(ctx, sql, j.ID); e != nil {
				t.Fatal(e)
			}
		}
		_, e = s.Issue(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
		// The record restored but naming another row: consistent with its own stamp, yet not
		// with the job, which the service cross-checks.
		for _, sql := range []string{`UPDATE reporting_s01.assets SET data=jsonb_set(jsonb_set(data,'{Number}','"AR-000001"'),'{RowIndex}','5') WHERE id=$1 AND kind='issue'`, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1 AND kind='issue'`} {
			if _, e = p.Exec(ctx, sql, j.ID); e != nil {
				t.Fatal(e)
			}
		}
		_, e = s.Issue(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
		// The job's frozen template changed without its digest: the stored job is corrupt.
		if _, e = p.Exec(ctx, `UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{RowIndex}','2') WHERE id=$1`, j.ID); e != nil {
			t.Fatal(e)
		}
		_, e = s.Job(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
		_, e = s.Issues(ctx, v.p, 10)
		mustError(t, e, r.ErrIntegrity)
	})
	t.Run("download_requires_the_record_to_name_the_artifact", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		done, e := s.Job(ctx, v.p, j.ID)
		if e != nil {
			t.Fatal(e)
		}
		// The artifact rewritten consistently (content, digest and row digest): it is no longer
		// the PDF the record names, so it is not served as issued.
		p := fixtureAdmin(t)
		for _, sql := range []string{`UPDATE reporting_s01.assets SET data=jsonb_set(jsonb_set(data,'{Content}',to_jsonb(encode('%PDF-1.7 other','base64'))),'{SHA256}',to_jsonb(encode(sha256('%PDF-1.7 other'::bytea),'hex'))) WHERE id=$1 AND kind='artifact'`, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1 AND kind='artifact'`} {
			if _, e = p.Exec(ctx, sql, done.ArtifactID); e != nil {
				t.Fatal(e)
			}
		}
		_, e = s.Download(ctx, v.p, done.ArtifactID)
		mustError(t, e, r.ErrIntegrity)
		_, e = s.Job(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
		// The artifact rewritten consistently and no longer claiming the record: it downloads
		// as an export would, but the issue job's view, which checks both directions, refuses.
		for _, sql := range []string{`UPDATE reporting_s01.assets SET data=data - 'Issue' WHERE id=$1 AND kind='artifact'`, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1 AND kind='artifact'`} {
			if _, e = p.Exec(ctx, sql, done.ArtifactID); e != nil {
				t.Fatal(e)
			}
		}
		_, e = s.Job(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
	})
	t.Run("missing_record_of_a_succeeded_job_is_integrity_failure", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		done, e := s.Job(ctx, v.p, j.ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = fixtureAdmin(t).Exec(ctx, `DELETE FROM reporting_s01.assets WHERE id=$1 AND kind='issue'`, j.ID); e != nil {
			t.Fatal(e)
		}
		for name, read := range map[string]func() error{
			"record":   func() error { _, e := s.Issue(ctx, v.p, j.ID); return e },
			"listing":  func() error { _, e := s.Issues(ctx, v.p, 10); return e },
			"job":      func() error { _, e := s.Job(ctx, v.p, j.ID); return e },
			"download": func() error { _, e := s.Download(ctx, v.p, done.ArtifactID); return e },
		} {
			if e := read(); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("%s with the record missing: %v; want integrity failure", name, e)
			}
		}
	})
}
