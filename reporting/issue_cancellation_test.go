package reporting_test

import (
	"context"
	"errors"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// canceller is the cancelling half of a test's source product (S24-A): a reporting.DocumentCanceller written here from the contract's
// words, over what the product's simulator holds for a job, with the calls it received and the means to refuse, to lose its answer and
// to answer wrongly. It is a fake authority: what the contract simulators do is tested with them.
type canceller struct {
	mu             sync.Mutex
	cancelAsked    []r.IssueCancellation                 // every request, in the order received
	cancelContexts []r.SourceContext                     // and the context of each
	cancelled      map[string]r.IssueCancellationReceipt // by job id: the documents the product has cancelled
	cancelReasons  map[string]string                     // the reason the product kept for each: the first
	cancelRefuse   error                                 // the product refuses, and cancels nothing
	cancelLose     error                                 // the product cancels and its answer is lost on the way back
	cancelAs       func(*r.IssueCancellationReceipt)     // changes the receipt on its way back
	cancelPanics   bool                                  // the adapter panics
	cancelDuring   func()                                // runs inside the call, before the product decides
	cancelNow      func() time.Time                      // the product's clock
}

// CancelDocument answers as ADR 0025 decision 1 says a product does: a job it never reserved is not found; a document that is not a
// confirmed one, or that the request names by another reference or number than the product's, is a conflict; a confirmed one is
// cancelled at the product's whole second, once, and the same receipt comes back whatever the reason says the next time.
func (p *product) CancelDocument(_ context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	k := &p.canceller
	k.mu.Lock()
	k.cancelAsked, k.cancelContexts = append(k.cancelAsked, c), append(k.cancelContexts, sc)
	during, panics := k.cancelDuring, k.cancelPanics
	k.mu.Unlock()
	if panics {
		panic("the adapter broke: secret-reason-value")
	}
	if during != nil {
		during()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cancelRefuse != nil {
		return r.IssueCancellationReceipt{}, k.cancelRefuse
	}
	theirs, ok := p.Simulator.IssueRecord(c.JobID)
	if !ok {
		return r.IssueCancellationReceipt{}, r.ErrNotFound
	}
	if theirs.State != contractsim.IssueConfirmed || theirs.Ref != c.ReservationRef || theirs.ConfirmationRef != c.ConfirmationRef || theirs.Number != c.Number {
		return r.IssueCancellationReceipt{}, r.ErrConflict
	}
	receipt, done := k.cancelled[c.JobID]
	if !done {
		if k.cancelled == nil {
			k.cancelled, k.cancelReasons = map[string]r.IssueCancellationReceipt{}, map[string]string{}
		}
		receipt = r.IssueCancellationReceipt{Ref: fmt.Sprintf("cancel-%d", len(k.cancelled)+1), CancelledAt: k.cancelNow().UTC().Truncate(time.Second)}
		k.cancelled[c.JobID], k.cancelReasons[c.JobID] = receipt, c.Reason
	}
	if k.cancelLose != nil {
		return r.IssueCancellationReceipt{}, k.cancelLose
	}
	if k.cancelAs != nil {
		k.cancelAs(&receipt)
	}
	return receipt, nil
}

// asked is how many times the product was asked to cancel.
func (p *product) asked() int {
	p.canceller.mu.Lock()
	defer p.canceller.mu.Unlock()
	return len(p.cancelAsked)
}

// newCancelEnv is newIssueEnv with the products' cancellation clocks set: the finance product's is ahead of the service's by
// productSkew, as its issue clock is.
func newCancelEnv(t *testing.T, o envOpts) *issueEnv {
	t.Helper()
	v := newIssueEnv(t, o)
	v.finance.cancelNow = func() time.Time { return v.clock.Now().Add(productSkew) }
	v.shipment.cancelNow = v.clock.Now
	return v
}

// issueRow issues the finance snapshot's row and returns the issue as the register shows it.
func issueRow(t *testing.T, v *issueEnv, row int, key string) r.IssueView {
	t.Helper()
	j := v.submit(row, key)
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	view, e := v.s.Issue(context.Background(), v.p, j.ID)
	if e != nil || view.State != "succeeded" || view.Cancellation != nil {
		t.Fatalf("the issue: %+v %v", view, e)
	}
	return view
}

// cancelEvents are the audit's events of cancellations the store holds for the test's identity, newest first.
func cancelEvents(t *testing.T, v *issueEnv) []r.AuditEvent {
	t.Helper()
	all, e := v.repo.Events(context.Background(), v.p.Identity, false, r.MaxAuditListing, "")
	if e != nil {
		t.Fatal(e)
	}
	out := []r.AuditEvent{}
	for _, event := range all {
		if event.Action == r.AuditIssueCancel {
			out = append(out, event)
		}
	}
	return out
}

// notCancelled fails unless the platform holds no cancellation of the issue: the record has none, the view has none, no audit event says
// one, and the file is still handed out.
func notCancelled(t *testing.T, v *issueEnv, issue r.IssueView, what string) {
	t.Helper()
	ctx := context.Background()
	rec, e := v.repo.Issue(ctx, v.p.Identity, issue.ID)
	if e != nil || rec.Cancellation != nil {
		t.Fatalf("%s: the record carries a cancellation: %+v %v", what, rec.Cancellation, e)
	}
	if view, e := v.s.Issue(ctx, v.p, issue.ID); e != nil || !reflect.DeepEqual(view, issue) {
		t.Fatalf("%s: the view changed: %+v %v", what, view, e)
	}
	if events := cancelEvents(t, v); len(events) != 0 {
		t.Fatalf("%s: the audit says %+v", what, events)
	}
	if a, e := v.s.Download(ctx, v.p, issue.ArtifactID); e != nil || a.Issue != issue.ID {
		t.Fatalf("%s: the file is not handed out: %v", what, e)
	}
}

// saysNothingOf fails when an error says anything of the cancellation: the reason, the number or one of the product's references.
func saysNothingOf(t *testing.T, e error, issue r.IssueView, reason, what string) {
	t.Helper()
	if e == nil {
		return
	}
	for _, secret := range []string{reason, issue.Number, issue.ReservationRef, issue.ConfirmationRef} {
		if secret != "" && strings.Contains(e.Error(), secret) {
			t.Fatalf("%s: the error carries %q: %v", what, secret, e)
		}
	}
}

const (
	reasonA = "客户选错了：订单 7 已取消"
	reasonB = "another reason, typed the second time"
)

// The whole path of ADR 0025 decision 2: the product is asked once, with the job id, its own references, its number and the reason, as
// the caller; its receipt is recorded beside the record, which keeps every field it had, with the audit event in the same step; every
// view of the issue shows the cancellation and the state word stays the job's; the file is no longer handed out; a repeat answers the
// same view without asking the product again.
func TestCancelIssueAsksTheProductThenRecordsTheReceipt(t *testing.T) {
	ctx := context.Background()
	v := newCancelEnv(t, envOpts{})
	issue := issueRow(t, v, 1, "first")
	other := issueRow(t, v, 2, "second")
	if issue.Number != "AR-000001" || other.Number != "AR-000002" || issue.Integration != contractsim.FinanceIntegration {
		t.Fatalf("%+v %+v", issue, other)
	}
	before, e := v.repo.Issue(ctx, v.p.Identity, issue.ID)
	if e != nil {
		t.Fatal(e)
	}
	if can := v.s.CanCancel(v.p); !reflect.DeepEqual(can, map[string]bool{contractsim.FinanceIntegration: true, contractsim.ShipmentIntegration: true}) {
		t.Fatalf("what can be cancelled: %v", can)
	}
	// Two minutes later, by both clocks: the product's is 90 s ahead of the service's.
	v.clock.Advance(2 * time.Minute)
	got, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA)
	if e != nil {
		t.Fatal(e)
	}
	wantCancellation := &r.IssueCancellationView{Ref: "cancel-1", CancelledAt: "2026-10-28T06:15:33Z", Reason: reasonA}
	if !reflect.DeepEqual(got.Cancellation, wantCancellation) {
		t.Fatalf("the cancellation in the view: %+v", got.Cancellation)
	}
	// Nothing else of the view changed: the state word is the job's, the number, the digest and the page count are what was issued.
	unchanged := got
	unchanged.Cancellation = nil
	if !reflect.DeepEqual(unchanged, issue) || got.State != "succeeded" || got.Number != "AR-000001" || got.SHA256 != issue.SHA256 || got.Pages != 2 {
		t.Fatalf("the view changed beyond its cancellation:\n%+v\n%+v", got, issue)
	}
	// The product was asked once, as the caller, with exactly what names the document and the reason.
	if v.finance.asked() != 1 || v.shipment.asked() != 0 {
		t.Fatalf("the products were asked %d and %d times", v.finance.asked(), v.shipment.asked())
	}
	if asked := v.finance.cancelAsked[0]; asked != (r.IssueCancellation{JobID: issue.ID, ReservationRef: issue.ReservationRef, ConfirmationRef: issue.ConfirmationRef, Number: "AR-000001", Reason: reasonA}) || !asked.Valid() {
		t.Fatalf("the request: %+v", asked)
	}
	if sc := v.finance.cancelContexts[0]; sc != (r.SourceContext{Owner: v.owner, SourceTenant: "tenant_a"}) {
		t.Fatalf("the context of the request: %+v", sc)
	}
	// The record keeps every field it had; the cancellation is beside it, in UTC, with the store's time.
	rec, e := v.repo.Issue(ctx, v.p.Identity, issue.ID)
	if e != nil || rec.Cancellation == nil {
		t.Fatal(rec, e)
	}
	cancelledAt, recordedAt := time.Date(2026, 10, 28, 6, 15, 33, 0, time.UTC), time.Date(2026, 10, 28, 6, 14, 3, 700_000_000, time.UTC)
	if c := *rec.Cancellation; c.IssueID != issue.ID || c.Ref != "cancel-1" || c.Reason != reasonA || !c.CancelledAt.Equal(cancelledAt) || !c.RecordedAt.Equal(recordedAt) || c.CancelledAt.Location() != time.UTC || c.RecordedAt.Location() != time.UTC {
		t.Fatalf("the cancellation's record: %+v", c)
	}
	rec.Cancellation = nil
	if !reflect.DeepEqual(rec, before) {
		t.Fatalf("the issue record changed:\n%+v\n%+v", rec, before)
	}
	// The audit event of the cancellation was written with it, at its time, and carries no value of it.
	events := cancelEvents(t, v)
	if len(events) != 1 || events[0].Action != "issue.cancel" || events[0].ObjectKind != "issue" || events[0].ObjectID != issue.ID || events[0].Outcome != "succeeded" || events[0].Code != "" || !events[0].At.Equal(recordedAt) {
		t.Fatalf("the audit: %+v", events)
	}
	// Every view of the issue shows it: the record, the listing, the poll, the jobs' listing, the cancel of the job, the replay.
	if view, e := v.s.Issue(ctx, v.p, issue.ID); e != nil || !reflect.DeepEqual(view, got) {
		t.Fatalf("the record's view: %+v %v", view, e)
	}
	list, e := v.s.Issues(ctx, v.p, 10)
	if e != nil || len(list) != 2 || list[0].ID != other.ID || list[0].Cancellation != nil || !reflect.DeepEqual(list[1], got) {
		t.Fatalf("the listing: %+v %v", list, e)
	}
	polled := v.job(issue.ID)
	if polled.State != "succeeded" || polled.ArtifactID != issue.ArtifactID || polled.Issue == nil || polled.Issue.Number != "AR-000001" || !reflect.DeepEqual(polled.Issue.Cancellation, wantCancellation) {
		t.Fatalf("the poll: %+v %+v", polled, polled.Issue)
	}
	jobs, e := v.s.Jobs(ctx, v.p, r.JobListing{Limit: 10, Kind: "issue"})
	if e != nil || len(jobs) != 2 || jobs[0].Issue.Cancellation != nil || !reflect.DeepEqual(jobs[1].Issue.Cancellation, wantCancellation) {
		t.Fatalf("the jobs' listing: %+v %v", jobs, e)
	}
	if c, e := v.s.Cancel(ctx, v.p, issue.ID); e != nil || c.State != "succeeded" || !reflect.DeepEqual(c.Issue.Cancellation, wantCancellation) {
		t.Fatalf("the cancel of the job: %+v %v", c, e)
	}
	if replay, e := v.s.SubmitIssue(ctx, v.p, v.input(1), "first"); e != nil || replay.ID != issue.ID || !reflect.DeepEqual(replay.Issue.Cancellation, wantCancellation) {
		t.Fatalf("the replay: %+v %v", replay, e)
	}
	// The file is no longer handed out; the other document's is.
	if _, e := v.s.Download(ctx, v.p, issue.ArtifactID); !errors.Is(e, r.ErrIssueCancelled) {
		t.Fatal("the file of a cancelled issue:", e)
	}
	if a, e := v.s.Download(ctx, v.p, other.ArtifactID); e != nil || a.Issue != other.ID {
		t.Fatal("the file of the other issue:", e)
	}
	// A repeat, with another reason: the same view, the product not asked again, nothing written.
	v.clock.Advance(time.Minute)
	again, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonB)
	if e != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("the repeat: %+v %v", again, e)
	}
	if v.finance.asked() != 1 || len(cancelEvents(t, v)) != 1 || v.store.cancels != 1 {
		t.Fatalf("the repeat asked the product (%d), wrote an event (%d) or a record (%d)", v.finance.asked(), len(cancelEvents(t, v)), v.store.cancels)
	}
	// The number stays the cancelled document's: the next issue takes the next one.
	if next := issueRow(t, v, 0, "third"); next.Number != "AR-000003" {
		t.Fatal("the number after a cancellation:", next.Number)
	}
}

// Who and what cannot be cancelled, before the product is asked: another identity's issue and what is no issue are not found, a product
// the caller no longer holds is denied, a job that did not succeed is a conflict, a reason that is none is invalid.
func TestCancelIssueRefusesBeforeTheProductIsAsked(t *testing.T) {
	ctx := context.Background()
	v := newCancelEnv(t, envOpts{})
	issue := issueRow(t, v, 1, "issued")
	stranger := v.p
	stranger.Subject = "stranger"
	elsewhere := v.p
	elsewhere.Tenant = "another-tenant"
	withoutFinance := v.p
	withoutFinance.Integrations = []string{contractsim.ShipmentIntegration}
	export, e := v.s.SubmitExportFormat(ctx, v.p, v.snapshot, "pdf", "export")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.run(); e != nil {
		t.Fatal(e)
	}
	queued := v.submit(0, "queued")
	jobCancelled := v.submit(2, "cancelled")
	if c, e := v.s.Cancel(ctx, v.p, jobCancelled.ID); e != nil || c.State != "cancelled" {
		t.Fatal(c, e)
	}
	for name, c := range map[string]struct {
		p      r.Principal
		id     string
		reason string
		want   error
	}{
		"another subject's issue":            {stranger, issue.ID, reasonA, r.ErrNotFound},
		"another tenant's issue":             {elsewhere, issue.ID, reasonA, r.ErrNotFound},
		"an identity that is none":           {r.Principal{}, issue.ID, reasonA, r.ErrNotFound},
		"an id nothing has":                  {v.p, r.NewID(), reasonA, r.ErrNotFound},
		"the id in upper case":               {v.p, strings.ToUpper(issue.ID), reasonA, r.ErrNotFound},
		"an id that is none":                 {v.p, "issue", reasonA, r.ErrNotFound},
		"an export job":                      {v.p, export.ID, reasonA, r.ErrNotFound},
		"the artifact's id":                  {v.p, issue.ArtifactID, reasonA, r.ErrNotFound},
		"a product the caller does not hold": {withoutFinance, issue.ID, reasonA, r.ErrDenied},
		"a queued issue job":                 {v.p, queued.ID, reasonA, r.ErrConflict},
		"a cancelled issue job":              {v.p, jobCancelled.ID, reasonA, r.ErrConflict},
		"no reason":                          {v.p, issue.ID, "", r.ErrInvalid},
		"a blank reason":                     {v.p, issue.ID, " \u3000 ", r.ErrInvalid},
		"a reason of 201 characters":         {v.p, issue.ID, strings.Repeat("因", 201), r.ErrInvalid},
		"a reason of two lines":              {v.p, issue.ID, "wrong customer\nsee the order", r.ErrInvalid},
		"a reason with a tab":                {v.p, issue.ID, "wrong\tcustomer", r.ErrInvalid},
		"a reason that is not UTF-8":         {v.p, issue.ID, "wrong \xff customer", r.ErrInvalid},
		// The request shape is judged before anything is looked up: a reason that is none is invalid whoever asks and whatever for.
		"another identity's issue and no reason": {stranger, issue.ID, "", r.ErrInvalid},
	} {
		got, e := v.s.CancelIssue(ctx, c.p, c.id, c.reason)
		if !errors.Is(e, c.want) || !reflect.DeepEqual(got, r.IssueView{}) {
			t.Errorf("%s: %+v %v; want %v", name, got, e, c.want)
		}
		saysNothingOf(t, e, issue, c.reason, name)
	}
	// A running issue job, and one that failed: there is no issued document yet, or none at all.
	claim, e := v.repo.Claim(ctx, r.LeaseDuration)
	if e != nil || claim == nil || claim.Job.ID != queued.ID {
		t.Fatal(claim, e)
	}
	if _, e = v.s.CancelIssue(ctx, v.p, queued.ID, reasonA); !errors.Is(e, r.ErrConflict) {
		t.Error("a running issue job:", e)
	}
	if e = v.repo.Fail(ctx, *claim, "EXECUTION_FAILED"); e != nil {
		t.Fatal(e)
	}
	if _, e = v.s.CancelIssue(ctx, v.p, queued.ID, reasonA); !errors.Is(e, r.ErrConflict) {
		t.Error("a failed issue job:", e)
	}
	if v.finance.asked() != 0 || v.store.cancels != 0 {
		t.Fatalf("a refusal reached the product (%d) or the store (%d)", v.finance.asked(), v.store.cancels)
	}
	notCancelled(t, v, issue, "after the refusals")
}

// An adapter without the capability cannot be asked: 422's error, as issuing answers for an adapter without DocumentAuthority, and the
// capabilities say so per product.
func TestCancelIssueNeedsAnAdapterThatCancels(t *testing.T) {
	ctx := context.Background()
	// contractOne forwards the source calls and the three of the document authority and nothing else: it issues, and cannot cancel.
	v := newCancelEnv(t, envOpts{contractOne: true})
	issue := issueRow(t, v, 1, "issued")
	if _, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal(e)
	}
	if v.finance.asked() != 0 || v.store.cancels != 0 {
		t.Fatal("an adapter without the capability was asked")
	}
	notCancelled(t, v, issue, "an adapter that cannot cancel")
	if can := v.s.CanCancel(v.p); !reflect.DeepEqual(can, map[string]bool{contractsim.FinanceIntegration: false, contractsim.ShipmentIntegration: true}) {
		t.Fatalf("what can be cancelled: %v", can)
	}
	// Only the products the service serves for the principal are named; a principal with none gets an empty answer, never nil.
	one := v.p
	one.Integrations = []string{contractsim.ShipmentIntegration, "a-product-without-a-connection"}
	if can := v.s.CanCancel(one); !reflect.DeepEqual(can, map[string]bool{contractsim.ShipmentIntegration: true}) {
		t.Fatalf("%v", can)
	}
	if can := v.s.CanCancel(r.Principal{}); can == nil || len(can) != 0 {
		t.Fatalf("%v", can)
	}
}

// The product decides. Its refusals are the answer, as the issue path maps them, and nothing is written; a job it says it never reserved,
// and the platform's own refusals in its mouth, are answers the platform cannot trust; a panic of the adapter is an error without the
// panic's value. No error says anything of the reason, the number or the references.
func TestCancelIssueRefusedByTheProductWritesNothing(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		refuse error
		want   error
		not    []error
	}{
		"the principal may not, or the product's rules say no": {r.ErrDenied, r.ErrDenied, nil},
		"the document is not a confirmed one":                  {r.ErrConflict, r.ErrConflict, nil},
		"the product cannot be reached":                        {r.ErrUnavailable, r.ErrUnavailable, nil},
		"the product finds the request malformed":              {r.ErrInvalid, r.ErrInvalid, nil},
		"the product lacks the capability after all":           {r.ErrUnsupported, r.ErrUnsupported, nil},
		"the product's own bound":                              {r.ErrBudget, r.ErrBudget, nil},
		"the product's integrity refusal":                      {r.ErrIntegrity, r.ErrIntegrity, nil},
		"a wrapped denial":                                     {fmt.Errorf("the period is closed: %w", r.ErrDenied), r.ErrDenied, nil},
		"a job the product says it never reserved":             {r.ErrNotFound, r.ErrIntegrity, []error{r.ErrNotFound}},
		"the platform's lease error":                           {r.ErrLease, r.ErrIntegrity, []error{r.ErrLease}},
		"the platform's role refusal":                          {r.ErrRoleDenied, r.ErrIntegrity, []error{r.ErrRoleDenied}},
		"the platform's audit refusal":                         {r.ErrAuditUnavailable, r.ErrIntegrity, []error{r.ErrAuditUnavailable}},
		"the platform's quota refusal":                         {&r.QuotaError{Quota: r.QuotaJobsInFlight}, r.ErrIntegrity, []error{r.ErrQuota}},
		"the platform's own word for a cancelled issue":        {r.ErrIssueCancelled, r.ErrIntegrity, []error{r.ErrIssueCancelled}},
	} {
		t.Run(name, func(t *testing.T) {
			v := newCancelEnv(t, envOpts{})
			issue := issueRow(t, v, 1, "issued")
			v.finance.cancelRefuse = c.refuse
			got, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA)
			if !errors.Is(e, c.want) || !reflect.DeepEqual(got, r.IssueView{}) {
				t.Fatalf("%+v %v; want %v", got, e, c.want)
			}
			for _, not := range c.not {
				if errors.Is(e, not) {
					t.Fatalf("the answer is still the product's %v: %v", not, e)
				}
			}
			if r.QuotaOf(e) != "" {
				t.Fatalf("the answer names a quota: %v", e)
			}
			saysNothingOf(t, e, issue, reasonA, name)
			if v.finance.asked() != 1 || v.store.cancels != 0 {
				t.Fatalf("asked %d times, %d records written", v.finance.asked(), v.store.cancels)
			}
			notCancelled(t, v, issue, name)
			// The refusal is not final: when the product agrees, the same request cancels.
			v.finance.cancelRefuse = nil
			if done, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA); e != nil || done.Cancellation == nil || done.Cancellation.Ref != "cancel-1" {
				t.Fatalf("after the refusal: %+v %v", done, e)
			}
		})
	}
	t.Run("the adapter panics", func(t *testing.T) {
		v := newCancelEnv(t, envOpts{})
		issue := issueRow(t, v, 1, "issued")
		v.finance.cancelPanics = true
		_, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA)
		if e == nil || strings.Contains(e.Error(), "secret") {
			t.Fatalf("a panic of the adapter: %v", e)
		}
		for _, known := range []error{r.ErrDenied, r.ErrConflict, r.ErrUnavailable, r.ErrInvalid, r.ErrIntegrity, r.ErrNotFound, r.ErrUnsupported, r.ErrAuditUnavailable} {
			if errors.Is(e, known) {
				t.Fatalf("a panic is answered as %v", known)
			}
		}
		notCancelled(t, v, issue, "a panic")
	})
}

// The receipt is held to the contract before anything is written: the product's reference as every reference it gives, a time that is a
// whole second between the confirmation it cancels and 9999, judged by value and kept in UTC.
func TestCancelIssueHoldsTheReceiptToTheContract(t *testing.T) {
	ctx := context.Background()
	confirmed := time.Date(2026, 10, 28, 6, 13, 33, 0, time.UTC)
	for name, c := range map[string]struct {
		change func(*r.IssueCancellationReceipt)
		want   string // the cancellation time of the view; "" when the receipt is refused
	}{
		"the receipt as the product gave it": {func(*r.IssueCancellationReceipt) {}, "2026-10-28T06:13:33Z"},
		"a reference of one character":       {func(rc *r.IssueCancellationReceipt) { rc.Ref = "x" }, "2026-10-28T06:13:33Z"},
		"a reference of 128 characters":      {func(rc *r.IssueCancellationReceipt) { rc.Ref = strings.Repeat("~", 128) }, "2026-10-28T06:13:33Z"},
		"the second of the confirmation":     {func(rc *r.IssueCancellationReceipt) { rc.CancelledAt = confirmed }, "2026-10-28T06:13:33Z"},
		"a time in another zone": {func(rc *r.IssueCancellationReceipt) {
			rc.CancelledAt = time.Date(2026, 10, 28, 15, 0, 0, 0, time.FixedZone("CST", 8*3600))
		}, "2026-10-28T07:00:00Z"},
		"the last second of 9999": {func(rc *r.IssueCancellationReceipt) {
			rc.CancelledAt = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
		}, "9999-12-31T23:59:59Z"},
		"no reference":                       {func(rc *r.IssueCancellationReceipt) { rc.Ref = "" }, ""},
		"a reference of 129 characters":      {func(rc *r.IssueCancellationReceipt) { rc.Ref = strings.Repeat("r", 129) }, ""},
		"a reference with a space":           {func(rc *r.IssueCancellationReceipt) { rc.Ref = "cancel 1" }, ""},
		"a reference with a newline":         {func(rc *r.IssueCancellationReceipt) { rc.Ref = "cancel-1\n" }, ""},
		"a reference that is not ASCII":      {func(rc *r.IssueCancellationReceipt) { rc.Ref = "作废-1" }, ""},
		"a reference with a bidi mark":       {func(rc *r.IssueCancellationReceipt) { rc.Ref = "cancel\u202e1" }, ""},
		"no time":                            {func(rc *r.IssueCancellationReceipt) { rc.CancelledAt = time.Time{} }, ""},
		"a time with a fraction":             {func(rc *r.IssueCancellationReceipt) { rc.CancelledAt = rc.CancelledAt.Add(time.Millisecond) }, ""},
		"the second before the confirmation": {func(rc *r.IssueCancellationReceipt) { rc.CancelledAt = confirmed.Add(-time.Second) }, ""},
		"a time before 1970": {func(rc *r.IssueCancellationReceipt) {
			rc.CancelledAt = time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC)
		}, ""},
		"a time in the year 10000":                   {func(rc *r.IssueCancellationReceipt) { rc.CancelledAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }, ""},
		"a receipt that is nothing":                  {func(rc *r.IssueCancellationReceipt) { *rc = r.IssueCancellationReceipt{} }, ""},
		"a reference that is the platform's own id":  {func(rc *r.IssueCancellationReceipt) { rc.Ref = r.NewID() }, "2026-10-28T06:13:33Z"},
		"a reference of every printable punctuation": {func(rc *r.IssueCancellationReceipt) { rc.Ref = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~" }, "2026-10-28T06:13:33Z"},
	} {
		t.Run(name, func(t *testing.T) {
			v := newCancelEnv(t, envOpts{})
			issue := issueRow(t, v, 1, "issued")
			if issue.ConfirmedAt != "2026-10-28T06:13:33Z" {
				t.Fatal("the confirmation time of the test:", issue.ConfirmedAt)
			}
			var sent r.IssueCancellationReceipt
			v.finance.cancelAs = func(rc *r.IssueCancellationReceipt) { c.change(rc); sent = *rc }
			got, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA)
			if c.want == "" {
				if !errors.Is(e, r.ErrIntegrity) || !reflect.DeepEqual(got, r.IssueView{}) {
					t.Fatalf("a receipt outside the contract: %+v %v", got, e)
				}
				saysNothingOf(t, e, issue, reasonA, name)
				if v.store.cancels != 0 {
					t.Fatal("a receipt outside the contract reached the store")
				}
				notCancelled(t, v, issue, name)
				return
			}
			if e != nil || got.Cancellation == nil || got.Cancellation.Ref != sent.Ref || got.Cancellation.CancelledAt != c.want || got.Cancellation.Reason != reasonA {
				t.Fatalf("a receipt within the contract: %+v %v", got.Cancellation, e)
			}
			rec, e := v.repo.Issue(ctx, v.p.Identity, issue.ID)
			if e != nil || rec.Cancellation == nil || !rec.Cancellation.CancelledAt.Equal(sent.CancelledAt) || rec.Cancellation.CancelledAt.Location() != time.UTC {
				t.Fatalf("the record: %+v %v", rec.Cancellation, e)
			}
		})
	}
}

// Nothing is atomic across the two systems, and the outcome is reconcilable by the job id: when the platform fails after the product
// cancelled, the person asks again, the product answers the same receipt whatever the reason says the second time, and the record is
// completed. The same when the product's answer was lost on its way back, and when the store kept the record but its answer was lost.
func TestCancelIssueIsCompletedByARetryAfterThePlatformFailed(t *testing.T) {
	ctx := context.Background()
	down := errors.New("the store cannot be reached")
	t.Run("the record cannot be written", func(t *testing.T) {
		v := newCancelEnv(t, envOpts{})
		issue := issueRow(t, v, 1, "issued")
		v.store.cancelE = down
		got, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA)
		if !errors.Is(e, down) || !reflect.DeepEqual(got, r.IssueView{}) {
			t.Fatal(got, e)
		}
		// The product has cancelled; the platform holds nothing and goes on saying issued: this is the state a retry reconciles.
		if receipt, cancelled := v.finance.cancelled[issue.ID]; !cancelled || receipt.Ref != "cancel-1" {
			t.Fatal("the product did not cancel")
		}
		notCancelled(t, v, issue, "the record failed")
		// The retry, a minute later, with another reason: the same receipt (its first time), and the record is completed.
		v.store.cancelE = nil
		v.clock.Advance(time.Minute)
		got, e = v.s.CancelIssue(ctx, v.p, issue.ID, reasonB)
		if e != nil || !reflect.DeepEqual(got.Cancellation, &r.IssueCancellationView{Ref: "cancel-1", CancelledAt: "2026-10-28T06:13:33Z", Reason: reasonB}) {
			t.Fatalf("the retry: %+v %v", got.Cancellation, e)
		}
		// The product was asked twice and cancelled once, and kept its first reason; the platform's record carries the reason of the
		// request that completed it.
		if v.finance.asked() != 2 || len(v.finance.cancelled) != 1 || v.finance.cancelReasons[issue.ID] != reasonA || len(cancelEvents(t, v)) != 1 {
			t.Fatalf("asked %d times, %d cancelled, reason %q, %d events", v.finance.asked(), len(v.finance.cancelled), v.finance.cancelReasons[issue.ID], len(cancelEvents(t, v)))
		}
		if _, e := v.s.Download(ctx, v.p, issue.ArtifactID); !errors.Is(e, r.ErrIssueCancelled) {
			t.Fatal(e)
		}
	})
	t.Run("the product's answer is lost", func(t *testing.T) {
		v := newCancelEnv(t, envOpts{})
		issue := issueRow(t, v, 1, "issued")
		v.finance.cancelLose = r.ErrUnavailable
		if _, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA); !errors.Is(e, r.ErrUnavailable) {
			t.Fatal(e)
		}
		if _, cancelled := v.finance.cancelled[issue.ID]; !cancelled || v.store.cancels != 0 {
			t.Fatal("the product did not cancel, or the platform wrote without a receipt")
		}
		notCancelled(t, v, issue, "the answer was lost")
		v.finance.cancelLose = nil
		got, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA)
		if e != nil || got.Cancellation == nil || got.Cancellation.Ref != "cancel-1" || len(v.finance.cancelled) != 1 {
			t.Fatalf("the retry: %+v %v", got.Cancellation, e)
		}
	})
	t.Run("the store kept the record and its answer is lost", func(t *testing.T) {
		v := newCancelEnv(t, envOpts{})
		issue := issueRow(t, v, 1, "issued")
		v.store.cancelLie = true
		if _, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA); e == nil {
			t.Fatal("a lost acknowledgement is an error")
		}
		// The retry finds the record and answers it: the product is not asked a second time, the first reason stands.
		got, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonB)
		if e != nil || got.Cancellation == nil || got.Cancellation.Reason != reasonA || v.finance.asked() != 1 || len(cancelEvents(t, v)) != 1 {
			t.Fatalf("the retry: %+v %v, asked %d times", got.Cancellation, e, v.finance.asked())
		}
	})
}

// ADR 0018 Q4, ADR 0025 decision 2: the audit event of a cancellation is written with the cancellation or the request fails, and nothing
// of it is kept; the retry completes both.
func TestCancelIssueFailsClosedWhenItsAuditEventCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	v := newCancelEnv(t, envOpts{})
	issue := issueRow(t, v, 1, "issued")
	v.repo.SetAuditFailure(errors.New("the audit cannot be written"))
	got, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA)
	if !errors.Is(e, r.ErrAuditUnavailable) || !reflect.DeepEqual(got, r.IssueView{}) {
		t.Fatal(got, e)
	}
	saysNothingOf(t, e, issue, reasonA, "the audit is down")
	// The store was asked and kept nothing: no cancellation without its event.
	if v.store.cancels != 1 || v.finance.asked() != 1 {
		t.Fatalf("%d records asked, product asked %d times", v.store.cancels, v.finance.asked())
	}
	v.repo.SetAuditFailure(nil)
	notCancelled(t, v, issue, "the audit was down")
	got, e = v.s.CancelIssue(ctx, v.p, issue.ID, reasonA)
	if e != nil || got.Cancellation == nil || got.Cancellation.Ref != "cancel-1" {
		t.Fatalf("the retry: %+v %v", got.Cancellation, e)
	}
	if events := cancelEvents(t, v); len(events) != 1 || events[0].Outcome != "succeeded" || events[0].ObjectID != issue.ID {
		t.Fatalf("the audit after the retry: %+v", events)
	}
}

// Once the product has cancelled, a caller that went away does not keep the platform from recording it: the write runs in a context of
// its own, bounded.
func TestCancelIssueRecordsWhenTheCallerWentAwayWhileTheProductDecided(t *testing.T) {
	v := newCancelEnv(t, envOpts{})
	issue := issueRow(t, v, 1, "issued")
	ctx, hangUp := context.WithCancel(context.Background())
	v.finance.cancelDuring = hangUp
	if _, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA); !errors.Is(e, context.Canceled) {
		t.Fatal("the caller's context ended:", e)
	}
	if c := v.store.cancelCtx; v.store.cancels != 1 || c.err != nil || !c.hasDeadline || c.remaining <= 0 || c.remaining > 5*time.Second {
		t.Fatalf("the record's context: %+v", c)
	}
	rec, e := v.repo.Issue(context.Background(), v.p.Identity, issue.ID)
	if e != nil || rec.Cancellation == nil || rec.Cancellation.Ref != "cancel-1" || len(cancelEvents(t, v)) != 1 {
		t.Fatalf("the record after the caller went away: %+v %v", rec.Cancellation, e)
	}
}

// A product that is idempotent by the job id gives one receipt. When the platform already holds a receipt for the issue and the product
// answers another (two requests side by side and a product that is not idempotent), the recorded one stands and the answer is an
// integrity failure, never the store's conflict.
func TestCancelIssueKeepsTheFirstReceiptWhenTheProductGivesAnother(t *testing.T) {
	ctx := context.Background()
	v := newCancelEnv(t, envOpts{})
	issue := issueRow(t, v, 1, "issued")
	// While the product decides the second request, the first one runs whole and records its receipt; then the product answers the
	// second with another receipt.
	v.finance.cancelDuring = func() {
		v.finance.canceller.mu.Lock()
		v.finance.cancelDuring = nil
		v.finance.canceller.mu.Unlock()
		if first, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA); e != nil || first.Cancellation.Ref != "cancel-1" {
			t.Error("the request that ran beside:", first, e)
		}
		v.finance.canceller.mu.Lock()
		v.finance.cancelAs = func(rc *r.IssueCancellationReceipt) { rc.Ref = "another-receipt" }
		v.finance.canceller.mu.Unlock()
	}
	_, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonB)
	if !errors.Is(e, r.ErrIntegrity) || errors.Is(e, r.ErrConflict) {
		t.Fatal(e)
	}
	saysNothingOf(t, e, issue, reasonB, "another receipt")
	rec, e := v.repo.Issue(ctx, v.p.Identity, issue.ID)
	if e != nil || rec.Cancellation == nil || rec.Cancellation.Ref != "cancel-1" || rec.Cancellation.Reason != reasonA || len(cancelEvents(t, v)) != 1 {
		t.Fatalf("the record: %+v %v", rec.Cancellation, e)
	}
}

// Many requests for one issue at once record one cancellation and one audit event, and every one of them answers the same view.
func TestConcurrentCancellationsOfOneIssueRecordOnce(t *testing.T) {
	ctx := context.Background()
	v := newCancelEnv(t, envOpts{})
	issue := issueRow(t, v, 1, "issued")
	const n = 8
	views, errs := make([]r.IssueView, n), make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			views[i], errs[i] = v.s.CancelIssue(ctx, v.p, issue.ID, fmt.Sprintf("reason %d", i))
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || views[i].Cancellation == nil || views[i].Cancellation.Ref != "cancel-1" || !reflect.DeepEqual(views[i], views[0]) {
			t.Fatalf("request %d: %+v %v", i, views[i].Cancellation, errs[i])
		}
	}
	if len(v.finance.cancelled) != 1 || len(cancelEvents(t, v)) != 1 {
		t.Fatalf("%d cancellations at the product, %d audit events", len(v.finance.cancelled), len(cancelEvents(t, v)))
	}
}

// A cancellation needs neither the snapshot nor the file, and outlives both: an issue is cancelled after its source read expired and
// after retention removed the file, and a cancelled issue keeps its cancellation through retention.
func TestACancellationOutlivesTheSnapshotAndTheFile(t *testing.T) {
	ctx := context.Background()
	v := newCancelEnv(t, envOpts{})
	early := issueRow(t, v, 1, "cancelled while the read lived")
	late := issueRow(t, v, 2, "cancelled after retention")
	if _, e := v.s.CancelIssue(ctx, v.p, early.ID, reasonA); e != nil {
		t.Fatal(e)
	}
	// The source read expires (ten minutes after the product froze it, by the product's clock): the views say expired, the cancellation
	// is still there, and the file of the cancelled issue is refused as cancelled, not as expired.
	v.clock.Advance(12 * time.Minute)
	if view, e := v.s.Issue(ctx, v.p, early.ID); e != nil || !view.Expired || view.Cancellation == nil || view.Cancellation.Reason != reasonA {
		t.Fatalf("%+v %v", view, e)
	}
	if poll := v.job(early.ID); !poll.Expired || poll.Issue.Cancellation == nil || poll.Issue.Number != "AR-000001" {
		t.Fatalf("the poll of an expired, cancelled issue: %+v %+v", poll, poll.Issue)
	}
	if _, e := v.s.Download(ctx, v.p, early.ArtifactID); !errors.Is(e, r.ErrIssueCancelled) {
		t.Fatal("the file of a cancelled issue whose read expired:", e)
	}
	if _, e := v.s.Download(ctx, v.p, late.ArtifactID); !errors.Is(e, r.ErrIntegrity) {
		t.Fatal("the file of an issue whose read expired, as before:", e)
	}
	// Retention removes both files and reduces the jobs; the records and the cancellation stay.
	v.clock.Advance(25 * time.Hour)
	swept, e := v.s.Sweep(ctx, r.SweepOptions{})
	if e != nil || swept.Artifacts != 2 || swept.Snapshots != 1 || swept.Jobs != 3 {
		t.Fatalf("the sweep: %+v %v", swept, e)
	}
	if view, e := v.s.Issue(ctx, v.p, early.ID); e != nil || !view.Expired || view.Cancellation == nil || view.Cancellation.Ref != "cancel-1" || view.Number != "AR-000001" || view.SHA256 != early.SHA256 || view.Pages != 2 {
		t.Fatalf("the cancelled issue after retention: %+v %v", view, e)
	}
	if _, e := v.s.Download(ctx, v.p, early.ArtifactID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("a swept file:", e)
	}
	// The other issue is cancelled now, a day after its file is gone: the product is asked by what the record keeps.
	got, e := v.s.CancelIssue(ctx, v.p, late.ID, reasonB)
	if e != nil || !got.Expired || got.State != "succeeded" || got.Number != "AR-000002" || got.Cancellation == nil || got.Cancellation.Ref != "cancel-2" || got.Cancellation.Reason != reasonB {
		t.Fatalf("a cancellation after retention: %+v %v", got, e)
	}
	if asked := v.finance.cancelAsked[1]; asked.JobID != late.ID || asked.Number != "AR-000002" || asked.ReservationRef != late.ReservationRef || asked.ConfirmationRef != late.ConfirmationRef {
		t.Fatalf("the request after retention: %+v", asked)
	}
	// And a later sweep leaves both cancellations where they are.
	v.clock.Advance(48 * time.Hour)
	if _, e := v.s.Sweep(ctx, r.SweepOptions{}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{early.ID, late.ID} {
		if rec, e := v.repo.Issue(ctx, v.p.Identity, id); e != nil || rec.Cancellation == nil {
			t.Fatalf("a cancellation after two sweeps: %+v %v", rec.Cancellation, e)
		}
	}
}

// The cancellation a repository reads with a record is held to that record wherever the issue is shown: one that is not the record's, or
// not of the contract's form, is INTEGRITY_FAILURE for the record, the listing and the job. The file stays refused: whatever cancellation
// the record carries, it is not handed out.
func TestIssueViewsHoldTheCancellationToItsRecord(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(*r.IssueCancellationRecord){
		"the cancellation of another issue":  func(c *r.IssueCancellationRecord) { c.IssueID = r.NewID() },
		"no issue id":                        func(c *r.IssueCancellationRecord) { c.IssueID = "" },
		"no reference":                       func(c *r.IssueCancellationRecord) { c.Ref = "" },
		"a reference with a space":           func(c *r.IssueCancellationRecord) { c.Ref = "cancel 1" },
		"a time with a fraction":             func(c *r.IssueCancellationRecord) { c.CancelledAt = c.CancelledAt.Add(time.Millisecond) },
		"a time before the confirmation":     func(c *r.IssueCancellationRecord) { c.CancelledAt = c.CancelledAt.Add(-time.Hour) },
		"no time":                            func(c *r.IssueCancellationRecord) { c.CancelledAt = time.Time{} },
		"no reason":                          func(c *r.IssueCancellationRecord) { c.Reason = "" },
		"a reason of two lines":              func(c *r.IssueCancellationRecord) { c.Reason = "first\nsecond" },
		"a reason of 201 characters":         func(c *r.IssueCancellationRecord) { c.Reason = strings.Repeat("x", 201) },
		"a reason that is not valid UTF-8":   func(c *r.IssueCancellationRecord) { c.Reason = "\xff" },
		"a cancellation that is nothing":     func(c *r.IssueCancellationRecord) { *c = r.IssueCancellationRecord{} },
		"a reason of the two noncharacters":  func(c *r.IssueCancellationRecord) { c.Reason = "\uFFFE" },
		"a reference that is 129 characters": func(c *r.IssueCancellationRecord) { c.Ref = strings.Repeat("r", 129) },
	} {
		t.Run(name, func(t *testing.T) {
			v := newCancelEnv(t, envOpts{})
			issue := issueRow(t, v, 1, "issued")
			if _, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA); e != nil {
				t.Fatal(e)
			}
			v.store.recordAs = func(i *r.Issue) {
				c := *i.Cancellation
				change(&c)
				i.Cancellation = &c
			}
			if _, e := v.s.Issue(ctx, v.p, issue.ID); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the record: %v", e)
			}
			if _, e := v.s.Issues(ctx, v.p, 10); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the listing: %v", e)
			}
			if _, e := v.s.Job(ctx, v.p, issue.ID); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the job: %v", e)
			}
			if _, e := v.s.Jobs(ctx, v.p, r.JobListing{Limit: 10}); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the jobs' listing: %v", e)
			}
			if _, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonB); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("a repeat: %v", e)
			}
			if _, e := v.s.Download(ctx, v.p, issue.ArtifactID); !errors.Is(e, r.ErrIssueCancelled) {
				t.Errorf("the file: %v", e)
			}
			v.store.recordAs = nil
			if view, e := v.s.Issue(ctx, v.p, issue.ID); e != nil || view.Cancellation == nil {
				t.Fatal(view, e)
			}
		})
	}
}

// The download refuses the file of a cancelled issue only when the record that carries the cancellation is the artifact's own; every
// other answer about an issued file is what it was (TestIssue... above): a record that does not describe the artifact is an integrity
// failure whether or not it says cancelled.
func TestDownloadRefusesACancelledIssuesFileByItsOwnRecord(t *testing.T) {
	ctx := context.Background()
	v := newCancelEnv(t, envOpts{})
	issue := issueRow(t, v, 1, "issued")
	if _, e := v.s.CancelIssue(ctx, v.p, issue.ID, reasonA); e != nil {
		t.Fatal(e)
	}
	v.store.recordAs = func(i *r.Issue) { i.SHA256 = strings.Repeat("0", 64) }
	if _, e := v.s.Download(ctx, v.p, issue.ArtifactID); !errors.Is(e, r.ErrIntegrity) || errors.Is(e, r.ErrIssueCancelled) {
		t.Fatal("a cancelled record that is not the artifact's:", e)
	}
	v.store.recordAs = nil
	// The poll of the job goes on answering: only the file is refused.
	if poll := v.job(issue.ID); poll.State != "succeeded" || poll.ArtifactID != issue.ArtifactID || poll.Issue.Cancellation == nil {
		t.Fatalf("%+v", poll)
	}
	// A principal who no longer holds the product is refused as before, without a word about the cancellation.
	without := v.p
	without.Integrations = []string{contractsim.ShipmentIntegration}
	if _, e := v.s.Download(ctx, without, issue.ArtifactID); !errors.Is(e, r.ErrDenied) {
		t.Fatal(e)
	}
	// The product is not asked about a file that will not be handed out: an outage does not change the answer.
	v.finance.SetUnavailable(true)
	if _, e := v.s.Download(ctx, v.p, issue.ArtifactID); !errors.Is(e, r.ErrIssueCancelled) {
		t.Fatal("the file of a cancelled issue while the product is down:", e)
	}
}

// The reason's rule: 1..200 characters of text on one line, counted in characters; and the forms of the request and of the receipt.
func TestCancellationReasonRequestAndReceiptForms(t *testing.T) {
	for name, c := range map[string]struct {
		reason string
		ok     bool
	}{
		"one character":                     {"x", true},
		"200 ASCII characters":              {strings.Repeat("x", 200), true},
		"200 CJK characters, 600 bytes":     {strings.Repeat("因", 200), true},
		"200 astral characters, 800 bytes":  {strings.Repeat("😀", 200), true},
		"spaces around text":                {"  客户选错了  ", true},
		"punctuation and digits":            {"订单 #7 (2026-10) 已取消；见工单 42。", true},
		"empty":                             {"", false},
		"only spaces":                       {"   ", false},
		"only an ideographic space":         {"\u3000", false},
		"201 ASCII characters":              {strings.Repeat("x", 201), false},
		"201 CJK characters":                {strings.Repeat("因", 201), false},
		"a newline":                         {"a\nb", false},
		"a carriage return":                 {"a\rb", false},
		"a tab":                             {"a\tb", false},
		"a NUL":                             {"a\x00b", false},
		"an escape":                         {"a\x1bb", false},
		"a DEL":                             {"a\x7fb", false},
		"invalid UTF-8":                     {"a\xffb", false},
		"the noncharacter U+FFFE":           {"a\uFFFEb", false},
		"the noncharacter U+FFFF":           {"a\uFFFFb", false},
		"200 characters and a trailing NUL": {strings.Repeat("x", 199) + "\x00", false},
	} {
		if got := r.ValidCancellationReason(c.reason); got != c.ok {
			t.Errorf("%s: %v, want %v", name, got, c.ok)
		}
	}
	if r.MaxCancellationReason != 200 {
		t.Fatal(r.MaxCancellationReason)
	}
	id := strings.Repeat("ab", 16)
	good := r.IssueCancellation{JobID: id, ReservationRef: "res-1", ConfirmationRef: "con-1", Number: "AR-000001", Reason: "wrong customer"}
	if !good.Valid() {
		t.Fatal("a well formed request is refused")
	}
	for name, change := range map[string]func(*r.IssueCancellation){
		"no job id":                     func(c *r.IssueCancellation) { c.JobID = "" },
		"a job id in upper case":        func(c *r.IssueCancellation) { c.JobID = strings.ToUpper(id) },
		"a job id of 31 characters":     func(c *r.IssueCancellation) { c.JobID = id[:31] },
		"no reservation reference":      func(c *r.IssueCancellation) { c.ReservationRef = "" },
		"a reservation with a space":    func(c *r.IssueCancellation) { c.ReservationRef = "res 1" },
		"no confirmation reference":     func(c *r.IssueCancellation) { c.ConfirmationRef = "" },
		"a confirmation of 129 bytes":   func(c *r.IssueCancellation) { c.ConfirmationRef = strings.Repeat("c", 129) },
		"no number":                     func(c *r.IssueCancellation) { c.Number = "" },
		"a number with a space":         func(c *r.IssueCancellation) { c.Number = "AR 1" },
		"a number of 33 characters":     func(c *r.IssueCancellation) { c.Number = strings.Repeat("N", 33) },
		"no reason":                     func(c *r.IssueCancellation) { c.Reason = "" },
		"a reason with a control":       func(c *r.IssueCancellation) { c.Reason = "a\nb" },
		"a reason of 201 characters":    func(c *r.IssueCancellation) { c.Reason = strings.Repeat("x", 201) },
		"a reason that is only a space": func(c *r.IssueCancellation) { c.Reason = " " },
	} {
		c := good
		change(&c)
		if c.Valid() {
			t.Errorf("%s: a request outside the contract is valid", name)
		}
	}
	at := time.Date(2026, 10, 28, 6, 15, 33, 0, time.UTC)
	for name, c := range map[string]struct {
		receipt r.IssueCancellationReceipt
		ok      bool
	}{
		"a reference and a whole second": {r.IssueCancellationReceipt{Ref: "cancel-1", CancelledAt: at}, true},
		"another zone":                   {r.IssueCancellationReceipt{Ref: "cancel-1", CancelledAt: at.In(time.FixedZone("CST", 8*3600))}, true},
		"the first second of 1970":       {r.IssueCancellationReceipt{Ref: "c", CancelledAt: time.Unix(0, 0)}, true},
		"no reference":                   {r.IssueCancellationReceipt{CancelledAt: at}, false},
		"a reference with a space":       {r.IssueCancellationReceipt{Ref: "cancel 1", CancelledAt: at}, false},
		"no time":                        {r.IssueCancellationReceipt{Ref: "cancel-1"}, false},
		"a fraction of a second":         {r.IssueCancellationReceipt{Ref: "cancel-1", CancelledAt: at.Add(time.Nanosecond)}, false},
		"the year 10000":                 {r.IssueCancellationReceipt{Ref: "cancel-1", CancelledAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, false},
	} {
		if got := c.receipt.Valid(); got != c.ok {
			t.Errorf("%s: %v, want %v", name, got, c.ok)
		}
	}
}

// What a repository holds a cancellation to before it keeps it: the record it names, the product's reference, a time that is a whole
// second not before the record's confirmation, and a reason.
func TestValidateCancellationHoldsItToTheRecord(t *testing.T) {
	confirmed := time.Date(2026, 10, 28, 6, 13, 33, 0, time.UTC)
	rec := r.Issue{ID: strings.Repeat("ab", 16), ConfirmedAt: confirmed}
	good := r.IssueCancellationRecord{IssueID: rec.ID, Ref: "cancel-1", CancelledAt: confirmed.Add(time.Hour), Reason: "wrong customer"}
	for name, change := range map[string]func(*r.IssueCancellationRecord){
		"as it is":                         func(*r.IssueCancellationRecord) {},
		"the second of the confirmation":   func(c *r.IssueCancellationRecord) { c.CancelledAt = confirmed },
		"another zone":                     func(c *r.IssueCancellationRecord) { c.CancelledAt = c.CancelledAt.In(time.FixedZone("CST", 8*3600)) },
		"a time the store is to set":       func(c *r.IssueCancellationRecord) { c.RecordedAt = time.Time{} },
		"a time the caller set":            func(c *r.IssueCancellationRecord) { c.RecordedAt = confirmed.Add(-24 * time.Hour) },
		"a reason of 200 CJK characters":   func(c *r.IssueCancellationRecord) { c.Reason = strings.Repeat("因", 200) },
		"a reference of 128 punctuation":   func(c *r.IssueCancellationRecord) { c.Ref = strings.Repeat("~", 128) },
		"the last second the product has":  func(c *r.IssueCancellationRecord) { c.CancelledAt = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC) },
		"a reason with spaces around text": func(c *r.IssueCancellationRecord) { c.Reason = " wrong customer " },
	} {
		c := good
		change(&c)
		if e := r.ValidateCancellation(rec, c); e != nil {
			t.Errorf("%s: %v", name, e)
		}
	}
	for name, change := range map[string]func(*r.IssueCancellationRecord){
		"another issue":                  func(c *r.IssueCancellationRecord) { c.IssueID = strings.Repeat("cd", 16) },
		"no issue":                       func(c *r.IssueCancellationRecord) { c.IssueID = "" },
		"no reference":                   func(c *r.IssueCancellationRecord) { c.Ref = "" },
		"a reference with a space":       func(c *r.IssueCancellationRecord) { c.Ref = "cancel 1" },
		"a reference of 129 bytes":       func(c *r.IssueCancellationRecord) { c.Ref = strings.Repeat("r", 129) },
		"a reference with an accent":     func(c *r.IssueCancellationRecord) { c.Ref = "annul\u00e9" },
		"no time":                        func(c *r.IssueCancellationRecord) { c.CancelledAt = time.Time{} },
		"a fraction of a second":         func(c *r.IssueCancellationRecord) { c.CancelledAt = c.CancelledAt.Add(time.Microsecond) },
		"a second before the record's":   func(c *r.IssueCancellationRecord) { c.CancelledAt = confirmed.Add(-time.Second) },
		"the year 10000":                 func(c *r.IssueCancellationRecord) { c.CancelledAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"no reason":                      func(c *r.IssueCancellationRecord) { c.Reason = "" },
		"a blank reason":                 func(c *r.IssueCancellationRecord) { c.Reason = "  " },
		"a reason of 201 characters":     func(c *r.IssueCancellationRecord) { c.Reason = strings.Repeat("x", 201) },
		"a reason of two lines":          func(c *r.IssueCancellationRecord) { c.Reason = "a\nb" },
		"a reason that is invalid UTF-8": func(c *r.IssueCancellationRecord) { c.Reason = "\xff" },
	} {
		c := good
		change(&c)
		if e := r.ValidateCancellation(rec, c); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s: %v, want INVALID_REQUEST", name, e)
		}
	}
	// A record whose id is not a platform id cancels nothing, whatever names it.
	odd := r.Issue{ID: "not-an-id", ConfirmedAt: confirmed}
	c := good
	c.IssueID = odd.ID
	if e := r.ValidateCancellation(odd, c); !errors.Is(e, r.ErrInvalid) {
		t.Fatal(e)
	}
	// The event a store appends with a cancellation: the owner's, on the issue, succeeded, with a fresh id and nothing of the cancellation.
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	event := r.CancellationAuditEvent(owner, rec.ID)
	other := r.CancellationAuditEvent(owner, rec.ID)
	if event.Identity != owner || event.Action != "issue.cancel" || event.ObjectKind != "issue" || event.ObjectID != rec.ID || event.Outcome != "succeeded" || event.Code != "" || !event.At.IsZero() || len(event.ID) != 32 || event.ID == other.ID {
		t.Fatalf("%+v", event)
	}
	if r.AuditIssueCancel != "issue.cancel" {
		t.Fatal(r.AuditIssueCancel)
	}
	found := false
	for _, action := range r.AuditActions {
		found = found || action == "issue.cancel"
	}
	if !found {
		t.Fatal("the audit's actions do not list issue.cancel")
	}
}

// The register's entries say which product an issue was made under (the capabilities are per product), for every state.
func TestIssueViewsNameTheirProduct(t *testing.T) {
	ctx := context.Background()
	v := newCancelEnv(t, envOpts{})
	finance := issueRow(t, v, 1, "finance")
	ship := v.freeze(contractsim.ShipmentQuery(), "ship")
	queued := v.submit(2, "queued")
	shipped, e := v.s.SubmitIssue(ctx, v.p, r.IssueInput{TemplateID: templateID, TemplateVersion: 1, Template: []byte(template), SnapshotID: ship}, "shipment")
	if e != nil {
		t.Fatal(e)
	}
	list, e := v.s.Issues(ctx, v.p, 10)
	if e != nil || len(list) != 3 {
		t.Fatal(list, e)
	}
	want := map[string]string{finance.ID: contractsim.FinanceIntegration, queued.ID: contractsim.FinanceIntegration, shipped.ID: contractsim.ShipmentIntegration}
	for _, entry := range list {
		if entry.Integration != want[entry.ID] {
			t.Errorf("issue %s (%s) names the product %q, want %q", entry.ID, entry.State, entry.Integration, want[entry.ID])
		}
	}
}
