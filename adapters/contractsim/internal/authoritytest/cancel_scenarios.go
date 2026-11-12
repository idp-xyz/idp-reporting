package authoritytest

import (
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
)

// The cancellation of a confirmed document, asked of the product for a person (S24-A, docs/ADR_0025_ISSUE_OPERATIONS.md
// decision 1): the rules of reporting.DocumentCanceller that both contract simulators implement, as scenarios run against
// either of them and, through the reference source, over the reporting source protocol.

const (
	firstReason  = "发错客户：订单 7 已取消"
	secondReason = "asked again, with other words"
)

func sameCancellation(t *testing.T, got, want r.IssueCancellationReceipt) {
	t.Helper()
	if got.Ref != want.Ref || !got.CancelledAt.Equal(want.CancelledAt) {
		t.Fatalf("the cancellation's receipt changed: got %+v, want %+v", got, want)
	}
}

// notCancelled fails unless the simulator holds the n-th job in the state, under the number, without a cancellation.
func notCancelled(t *testing.T, g *Rig, n int, state, number string) {
	t.Helper()
	if rec := g.Holds(n, state, number); rec.Cancelled() || rec.CancellationRef != "" || !rec.CancelledAt.IsZero() || rec.CancelReason != "" {
		t.Fatalf("job %d carries a cancellation: %+v", n, rec)
	}
}

var cancelScenarios = []ruleScenario{
	{"cancelling_a_confirmed_document_answers_a_receipt_and_keeps_the_confirmation", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		before := g.Holds(1, contractsim.IssueConfirmed, "AR-000001")
		receipt := g.Cancel(res, rc, firstReason)
		// The platform holds a receipt to the contract's form (reporting.IssueCancellationReceipt.Valid) and requires a time
		// that is not before the confirmation: a simulator must give what the platform accepts.
		if !receipt.Valid() || receipt.Ref == res.Ref || receipt.Ref == rc.Ref || receipt.CancelledAt.Before(rc.ConfirmedAt) {
			t.Fatalf("%+v", receipt)
		}
		utcSecond(t, receipt.CancelledAt, "cancellation time")
		near(t, g, receipt.CancelledAt, "cancellation time")
		// The record is the confirmed document it was, with its cancellation beside it.
		rec := g.Holds(1, contractsim.IssueConfirmed, "AR-000001")
		if !rec.Cancelled() || rec.CancellationRef != receipt.Ref || !rec.CancelledAt.Equal(receipt.CancelledAt) || rec.CancelReason != firstReason {
			t.Fatalf("the cancellation of the record: %+v", rec)
		}
		rec.CancellationRef, rec.CancelledAt, rec.CancelReason = "", before.CancelledAt, ""
		if rec != before {
			t.Fatalf("the cancellation changed the rest of the record:\n%+v\n%+v", rec, before)
		}
	}},
	{"cancelling_again_returns_the_same_receipt_whatever_the_reason", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		first := g.Cancel(res, rc, firstReason)
		for range 2 {
			sameCancellation(t, g.Cancel(res, rc, secondReason), first)
		}
		sameCancellation(t, g.Cancel(res, rc, firstReason), first)
		// The product keeps the reason of the request that cancelled the document.
		if rec := g.Record(1); rec.CancelReason != firstReason || rec.CancellationRef != first.Ref {
			t.Fatalf("%+v", rec)
		}
	}},
	{"a_cancelled_number_is_not_given_out_again", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		g.Cancel(res, rc, firstReason)
		for i, want := range []string{"AR-000002", "AR-000003"} {
			if next := g.Reserve(i + 2); next.Number != want || next.Number == res.Number {
				t.Fatalf("after a cancellation job %d got %s, want %s", i+2, next.Number, want)
			}
		}
		// The job keeps its document: asked again, the product answers the reservation and the confirmation it gave, and
		// the cancellation stands.
		again, e := g.Prepare(1, 0)
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, res)
		if same := g.Confirm(res); same.Ref != rc.Ref || !same.ConfirmedAt.Equal(rc.ConfirmedAt) {
			t.Fatalf("the confirmation of a cancelled document changed: %+v then %+v", rc, same)
		}
		if rec := g.Holds(1, contractsim.IssueConfirmed, "AR-000001"); !rec.Cancelled() {
			t.Fatalf("%+v", rec)
		}
	}},
	{"only_a_confirmed_document_is_cancelled", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		// A confirmation the product never gave: what a request about an unconfirmed reservation can name.
		none := r.IssueReceipt{Ref: strings.Repeat("a", 32)}
		reserved := g.Reserve(1)
		_, e := g.Src.CancelDocument(ctx, g.SC, g.Cancellation(reserved, none, firstReason))
		is(t, e, r.ErrConflict, "a reservation that was not confirmed")
		notCancelled(t, g, 1, contractsim.IssueReserved, "AR-000001")
		expired := g.Reserve(2)
		g.Env.Expire(Job(2))
		_, e = g.Src.CancelDocument(ctx, g.SC, g.Cancellation(expired, none, firstReason))
		is(t, e, r.ErrConflict, "a reservation that expired unconfirmed")
		notCancelled(t, g, 2, contractsim.IssueReserved, "AR-000002")
		voided := g.Reserve(3)
		if e = g.Src.VoidIssue(ctx, g.SC, Job(3), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		_, e = g.Src.CancelDocument(ctx, g.SC, g.Cancellation(voided, none, firstReason))
		is(t, e, r.ErrConflict, "a voided reservation")
		notCancelled(t, g, 3, contractsim.IssueVoid, "AR-000003")
		// A confirmed document the platform voided (its job failed after the confirmation) is void, not a document to cancel.
		res, rc := g.Issued(4)
		if e = g.Src.VoidIssue(ctx, g.SC, Job(4), "ISSUE_UNRECONCILED"); e != nil {
			t.Fatal(e)
		}
		_, e = g.Src.CancelDocument(ctx, g.SC, g.Cancellation(res, rc, firstReason))
		is(t, e, r.ErrConflict, "a confirmed document that was voided")
		notCancelled(t, g, 4, contractsim.IssueVoid, "AR-000004")
		// The void of a job nothing was reserved for: the source holds the job, as a tombstone, and no document.
		if e = g.Src.VoidIssue(ctx, g.SC, Job(5), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		tombstone := r.IssueReservation{JobID: Job(5), Ref: strings.Repeat("b", 32), Number: "AR-000005"}
		_, e = g.Src.CancelDocument(ctx, g.SC, g.Cancellation(tombstone, none, firstReason))
		is(t, e, r.ErrConflict, "a job voided before anything was reserved")
		notCancelled(t, g, 5, contractsim.IssueVoid, "")
		// None of the refusals took a number.
		if next := g.Reserve(6); next.Number != "AR-000005" {
			t.Fatal(next.Number)
		}
	}},
	{"cancelling_an_unknown_job_is_not_found", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		c := g.Cancellation(res, rc, firstReason)
		c.JobID = Job(2)
		_, e := g.Src.CancelDocument(ctx, g.SC, c)
		is(t, e, r.ErrNotFound, "a job the source never reserved")
		if _, ok := g.Env.Record(Job(2)); ok {
			t.Fatal("the refusal left a record")
		}
		notCancelled(t, g, 1, contractsim.IssueConfirmed, "AR-000001")
	}},
	{"the_request_must_name_the_document_as_the_product_holds_it", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		other, otherReceipt := g.Issued(2)
		for name, change := range map[string]func(*r.IssueCancellation){
			"another reservation reference":     func(c *r.IssueCancellation) { c.ReservationRef = strings.Repeat("f", 32) },
			"the other document's reservation":  func(c *r.IssueCancellation) { c.ReservationRef = other.Ref },
			"another confirmation reference":    func(c *r.IssueCancellation) { c.ConfirmationRef = strings.Repeat("f", 32) },
			"the other document's confirmation": func(c *r.IssueCancellation) { c.ConfirmationRef = otherReceipt.Ref },
			"the references the other way round": func(c *r.IssueCancellation) {
				c.ReservationRef, c.ConfirmationRef = c.ConfirmationRef, c.ReservationRef
			},
			"another number":                      func(c *r.IssueCancellation) { c.Number = "AR-000009" },
			"the other document's number":         func(c *r.IssueCancellation) { c.Number = other.Number },
			"the number in lower case":            func(c *r.IssueCancellation) { c.Number = strings.ToLower(c.Number) },
			"the reservation reference with more": func(c *r.IssueCancellation) { c.ReservationRef += "0" },
		} {
			c := g.Cancellation(res, rc, firstReason)
			change(&c)
			_, e := g.Src.CancelDocument(ctx, g.SC, c)
			is(t, e, r.ErrConflict, name)
		}
		notCancelled(t, g, 1, contractsim.IssueConfirmed, "AR-000001")
		notCancelled(t, g, 2, contractsim.IssueConfirmed, "AR-000002")
		// Once cancelled, the document is still named by what the product gave: a repeat that names it otherwise conflicts.
		first := g.Cancel(res, rc, firstReason)
		wrong := g.Cancellation(res, rc, firstReason)
		wrong.Number = other.Number
		_, e := g.Src.CancelDocument(ctx, g.SC, wrong)
		is(t, e, r.ErrConflict, "a repeat under another number")
		sameCancellation(t, g.Cancel(res, rc, secondReason), first)
		notCancelled(t, g, 2, contractsim.IssueConfirmed, "AR-000002")
	}},
	{"a_principal_that_may_not_issue_may_not_cancel", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		c := g.Cancellation(res, rc, firstReason)
		noIssue := g.P.Grant
		noIssue.IssueDocuments = false
		g.Env.SetGrant(g.SC, noIssue)
		_, e := g.Src.CancelDocument(ctx, g.SC, c)
		is(t, e, r.ErrDenied, "a grant without the right to issue")
		g.Env.Revoke(g.SC)
		_, e = g.Src.CancelDocument(ctx, g.SC, c)
		is(t, e, r.ErrDenied, "a revoked principal")
		g.Env.SetGrant(g.SC, g.P.Grant)
		// A subject the product does not know, and the principal in a source tenant that is not the product's.
		stranger := g.SC
		stranger.Owner.Subject = "user_nobody_knows"
		_, e = g.Src.CancelDocument(ctx, stranger, c)
		is(t, e, r.ErrDenied, "a subject the product does not know")
		elsewhere := g.SC
		elsewhere.SourceTenant = "tenant_b"
		_, e = g.Src.CancelDocument(ctx, elsewhere, c)
		is(t, e, r.ErrDenied, "another source tenant")
		notCancelled(t, g, 1, contractsim.IssueConfirmed, "AR-000001")
		// The checks apply to every call: once the document is cancelled, a principal who has lost the right is refused its
		// receipt too, and gets it again with the right.
		first := g.Cancel(res, rc, firstReason)
		g.Env.SetGrant(g.SC, noIssue)
		_, e = g.Src.CancelDocument(ctx, g.SC, c)
		is(t, e, r.ErrDenied, "the receipt of a cancelled document, without the right")
		_, e = g.Src.CancelDocument(ctx, stranger, c)
		is(t, e, r.ErrDenied, "the receipt of a cancelled document, to a subject the product does not know")
		g.Env.SetGrant(g.SC, g.P.Grant)
		sameCancellation(t, g.Cancel(res, rc, firstReason), first)
		// In these products the right to issue is the right to cancel: a colleague who holds it cancels a document of the
		// source. (The platform asks only for the identity that issued the document; who else may is the product's rule.)
		other, otherReceipt := g.Issued(2)
		colleague := g.SC
		colleague.Owner.Subject = "user_b"
		g.Env.SetGrant(colleague, g.P.Grant)
		if _, e = g.Src.CancelDocument(ctx, colleague, g.Cancellation(other, otherReceipt, firstReason)); e != nil {
			t.Fatal("a colleague with the right to issue:", e)
		}
	}},
	{"the_products_own_rules_refuse_a_cancellation", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		g.Env.SetRefused(true)
		_, e := g.Src.CancelDocument(ctx, g.SC, g.Cancellation(res, rc, firstReason))
		is(t, e, r.ErrDenied, "the product's own rules say no")
		notCancelled(t, g, 1, contractsim.IssueConfirmed, "AR-000001")
		g.Env.SetRefused(false)
		first := g.Cancel(res, rc, firstReason)
		// The rules apply to every call, so to a document that is cancelled already.
		g.Env.SetRefused(true)
		_, e = g.Src.CancelDocument(ctx, g.SC, g.Cancellation(res, rc, firstReason))
		is(t, e, r.ErrDenied, "the product's own rules say no, cancelled document")
		g.Env.SetRefused(false)
		sameCancellation(t, g.Cancel(res, rc, firstReason), first)
	}},
	{"a_cancellation_that_is_not_well_formed_is_invalid", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		for name, change := range map[string]func(*r.IssueCancellation){
			"job id empty":                      func(c *r.IssueCancellation) { c.JobID = "" },
			"job id too short":                  func(c *r.IssueCancellation) { c.JobID = c.JobID[:31] },
			"job id uppercase":                  func(c *r.IssueCancellation) { c.JobID = strings.ToUpper(strings.Repeat("ab", 16)) },
			"job id not hexadecimal":            func(c *r.IssueCancellation) { c.JobID = strings.Repeat("g", 32) },
			"no reservation reference":          func(c *r.IssueCancellation) { c.ReservationRef = "" },
			"a reservation reference of 129":    func(c *r.IssueCancellation) { c.ReservationRef = strings.Repeat("r", 129) },
			"a reservation reference in spaces": func(c *r.IssueCancellation) { c.ReservationRef = " " + c.ReservationRef },
			"no confirmation reference":         func(c *r.IssueCancellation) { c.ConfirmationRef = "" },
			"a confirmation reference not text": func(c *r.IssueCancellation) { c.ConfirmationRef = "confirm\x00ation" },
			"no number":                         func(c *r.IssueCancellation) { c.Number = "" },
			"a number with a space":             func(c *r.IssueCancellation) { c.Number = "AR 000001" },
			"a number of 33 characters":         func(c *r.IssueCancellation) { c.Number = strings.Repeat("N", 33) },
			"no reason":                         func(c *r.IssueCancellation) { c.Reason = "" },
			"a blank reason":                    func(c *r.IssueCancellation) { c.Reason = " \u3000" },
			"a reason of 201 characters":        func(c *r.IssueCancellation) { c.Reason = strings.Repeat("因", 201) },
			"a reason of two lines":             func(c *r.IssueCancellation) { c.Reason = "wrong customer\nsee the order" },
			"a reason with a tab":               func(c *r.IssueCancellation) { c.Reason = "wrong\tcustomer" },
			"a reason that is not UTF-8":        func(c *r.IssueCancellation) { c.Reason = "wrong \xff customer" },
		} {
			c := g.Cancellation(res, rc, firstReason)
			change(&c)
			_, e := g.Src.CancelDocument(ctx, g.SC, c)
			is(t, e, r.ErrInvalid, name)
		}
		notCancelled(t, g, 1, contractsim.IssueConfirmed, "AR-000001")
		// The limits themselves are valid: a reason of one character, and of 200 characters of four and of three bytes each.
		for i, reason := range []string{"x", strings.Repeat("😀", 200), strings.Repeat("因", 200)} {
			n := i + 2
			res, rc := g.Issued(n)
			g.Cancel(res, rc, reason)
			if rec := g.Record(n); rec.CancelReason != reason {
				t.Fatalf("job %d kept a reason of %d bytes, want %d", n, len(rec.CancelReason), len(reason))
			}
		}
	}},
	// VoidIssue keeps its meaning, the platform cleaning up after a failed job: a void of a job whose document was
	// cancelled is not an error, and the document stays cancelled.
	{"voiding_a_cancelled_document_is_not_an_error_and_leaves_it_cancelled", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		first := g.Cancel(res, rc, firstReason)
		for _, reason := range []string{"EXECUTION_FAILED", "ISSUE_UNRECONCILED"} {
			if e := g.Src.VoidIssue(ctx, g.SC, Job(1), reason); e != nil {
				t.Fatal("a void of a cancelled document:", e)
			}
		}
		rec := g.Holds(1, contractsim.IssueConfirmed, "AR-000001")
		if !rec.Cancelled() || rec.CancellationRef != first.Ref || rec.Reason != "" || !rec.VoidedAt.IsZero() {
			t.Fatalf("the void changed a cancelled document: %+v", rec)
		}
		sameCancellation(t, g.Cancel(res, rc, secondReason), first)
		if next := g.Reserve(2); next.Number != "AR-000002" {
			t.Fatal(next.Number)
		}
	}},
	{"cancelling_is_scoped_to_the_source", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance(), OtherIntegration)
		res, rc := g.Issued(1)
		c := g.Cancellation(res, rc, firstReason)
		// The same job id in another integration is another job, which that source never reserved.
		_, e := g.Src.CancelDocument(ctx, g.Context(OtherIntegration), c)
		is(t, e, r.ErrNotFound, "the other integration")
		notCancelled(t, g, 1, contractsim.IssueConfirmed, "AR-000001")
		g.Cancel(res, rc, firstReason)
		_, e = g.Src.CancelDocument(ctx, g.Context(OtherIntegration), c)
		is(t, e, r.ErrNotFound, "the other integration, once the document is cancelled")
	}},
	{"an_unavailable_source_refuses_a_cancellation", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res, rc := g.Issued(1)
		g.Env.SetUnavailable(true)
		_, e := g.Src.CancelDocument(ctx, g.SC, g.Cancellation(res, rc, firstReason))
		is(t, e, r.ErrUnavailable, "an outage")
		g.Env.SetUnavailable(false)
		notCancelled(t, g, 1, contractsim.IssueConfirmed, "AR-000001")
		first := g.Cancel(res, rc, firstReason)
		g.Env.SetUnavailable(true)
		_, e = g.Src.CancelDocument(ctx, g.SC, g.Cancellation(res, rc, firstReason))
		is(t, e, r.ErrUnavailable, "an outage, cancelled document")
		g.Env.SetUnavailable(false)
		sameCancellation(t, g.Cancel(res, rc, firstReason), first)
	}},
	{"a_document_of_a_group_is_cancelled_like_a_rows", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		res := g.ReserveGroup(1, byCustomerA())
		rc := g.Confirm(res)
		first := g.Cancel(res, rc, firstReason)
		sameCancellation(t, g.Cancel(res, rc, secondReason), first)
		if rec := g.Holds(1, contractsim.IssueConfirmed, "AR-000001"); !rec.Cancelled() {
			t.Fatalf("%+v", rec)
		}
	}},
}

// cancelConcurrentScenarios call one simulator from several goroutines at once about one document (RunConcurrent).
var cancelConcurrentScenarios = []ruleScenario{
	{"cancellations_of_one_document_return_one_receipt", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for round := 1; round <= rounds; round++ {
			res, rc := g.Issued(round)
			got := make([]r.IssueCancellationReceipt, workers)
			errs := make([]error, workers)
			together(workers, func(i int) {
				got[i], errs[i] = g.Src.CancelDocument(ctx, g.SC, g.Cancellation(res, rc, fmt.Sprintf("reason %d", i)))
			})
			for i := range got {
				if errs[i] != nil {
					t.Fatalf("round %d call %d: %v", round, i, errs[i])
				}
				sameCancellation(t, got[i], got[0])
			}
			// one of the requests was first, and its reason stays
			rec := g.Holds(round, contractsim.IssueConfirmed, fmt.Sprintf("AR-%06d", round))
			if rec.CancellationRef != got[0].Ref || !strings.HasPrefix(rec.CancelReason, "reason ") {
				t.Fatalf("round %d: %+v", round, rec)
			}
		}
	}},
	// A cancellation and a void that race on a confirmed document settle on one of two outcomes: the void came first, the
	// job is void and no cancellation was made; or the cancellation came first, and the void changed nothing. Never a void
	// document that is also cancelled.
	{"cancellations_and_voids_of_one_document_settle_on_one_outcome", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for round := 1; round <= rounds; round++ {
			res, rc := g.Issued(round)
			got := make([]r.IssueCancellationReceipt, workers)
			errs := make([]error, workers)
			together(workers, func(i int) {
				if i%2 == 0 {
					got[i], errs[i] = g.Src.CancelDocument(ctx, g.SC, g.Cancellation(res, rc, firstReason))
				} else {
					errs[i] = g.Src.VoidIssue(ctx, g.SC, Job(round), "EXECUTION_FAILED")
				}
			})
			rec := g.Record(round)
			if rec.Number != fmt.Sprintf("AR-%06d", round) || (rec.State == contractsim.IssueVoid) == rec.Cancelled() {
				t.Fatalf("round %d: the document is neither void nor cancelled, or both: %+v", round, rec)
			}
			for i := range got {
				switch {
				case i%2 == 1:
					if errs[i] != nil {
						t.Fatalf("round %d: void %d: %v", round, i, errs[i])
					}
				case rec.Cancelled():
					if errs[i] != nil || got[i].Ref != rec.CancellationRef || !got[i].CancelledAt.Equal(rec.CancelledAt) {
						t.Fatalf("round %d: cancellation %d of a cancelled document: %+v %v, the source holds %+v", round, i, got[i], errs[i], rec)
					}
				default:
					is(t, errs[i], r.ErrConflict, fmt.Sprintf("round %d: cancellation %d after the void", round, i))
				}
			}
		}
	}},
}
