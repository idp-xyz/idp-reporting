package conformance

import (
	"context"
	"errors"

	r "idp.local/reporting/reporting"
)

// The rules of the cancellation of an issued document (S24-A, ADR 0025 decisions 1 and 5), checked as a connection that declares
// `cancellation` would use the route. They need a document, so they make one of their own: a number is reserved for the sample read's
// first row under a job id of the check's and confirmed with a digest that is no file's, and that document is cancelled. On the way a
// second number is reserved and voided (X2) and a third after the cancellation (X4). The reason sent is the check's own fixed text.
//
//   - X1: the product cancels the confirmed document and answers a receipt the platform accepts (of the job, with the product's
//     reference and a whole second that is not before the confirmation's); asked again, with another reason, it answers the same
//     receipt, which is how the platform reconciles a cancellation whose answer it did not record.
//   - X2: only a confirmed document is cancelled: a reservation that was never confirmed is a conflict, a job the source never
//     reserved is not found.
//   - X3: the product decides who may cancel, from the request's context: a subject it does not know is refused, before the
//     document is cancelled and after (the receipt of a cancellation is not public either).
//   - X4: the number stays used: a job reserved after the cancellation gets another number.
//
// A source that answers the route `unsupported` does not implement it: X1 says so and the other three are skipped. A document the
// rules confirmed and could not cancel is voided before they end, so that nothing is left confirmed in the product.
const (
	ruleCancel          = "a confirmed document is cancelled (a receipt of the job, with a reference and a whole second not before the confirmation), and asking again, with another reason, answers the same receipt"
	ruleCancelConfirmed = "a reservation that was not confirmed is not cancelled (conflict), nor is a job the source never reserved (not_found)"
	ruleCancelSubject   = "a cancellation asked by a subject the product does not know is refused (denied), before the document is cancelled and after"
	ruleNumberStaysUsed = "the number of a cancelled document stays used: a job reserved afterwards gets another number"

	// checkReason is the reason the rules send, and the void reason of what they reserve.
	checkReason = "reporting-source-check"
	// noFile is the digest the rules confirm: 64 zeros, the digest of no file.
	noFile = "0000000000000000000000000000000000000000000000000000000000000000"
)

// skipCancellation skips the four rules for one reason.
func skipCancellation(skip func(id, rule, why string), why string) {
	skip("X1", ruleCancel, why)
	skip("X2", ruleCancelConfirmed, why)
	skip("X3", ruleCancelSubject, why)
	skip("X4", ruleNumberStaysUsed, why)
}

// cancellation runs the X rules. a is the adapter of a connection that declares issuance and cancellation; p is the sample read and row
// its first row; stranger is the sample context under a subject the product does not know.
func cancellation(ctx context.Context, a r.Adapter, sc, stranger r.SourceContext, p r.PreparedRead, row []string, add func(id, rule string, err error) bool, skip func(id, rule, why string)) {
	auth, canceller := a.(r.DocumentAuthority), a.(r.DocumentCanceller)
	intent := func() r.IssueIntent {
		return r.IssueIntent{JobID: r.NewID(), Prepared: p, RowIndex: 0, RowSHA256: r.Digest(row), TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: noFile}
	}
	// The document of the rules: reserved, then confirmed.
	in := intent()
	res, err := auth.PrepareDocument(ctx, sc, in)
	var confirmed r.IssueReceipt
	if err == nil {
		confirmed, err = auth.ConfirmIssue(ctx, sc, r.IssueConfirmation{JobID: in.JobID, ReservationRef: res.Ref, SHA256: noFile, Pages: 1})
	}
	if err != nil {
		_ = auth.VoidIssue(ctx, sc, in.JobID, checkReason)
		add("X1", ruleCancel, fail("a document to cancel could not be reserved and confirmed", err))
		skip("X2", ruleCancelConfirmed, "no confirmed document to cancel (X1)")
		skip("X3", ruleCancelSubject, "no confirmed document to cancel (X1)")
		skip("X4", ruleNumberStaysUsed, "no confirmed document to cancel (X1)")
		return
	}
	doc := r.IssueCancellation{JobID: in.JobID, ReservationRef: res.Ref, ConfirmationRef: confirmed.Ref, Number: res.Number, Reason: checkReason}

	// X3, its first half, comes first: once the document is cancelled, a product that answers anybody would only repeat a receipt.
	_, err = canceller.CancelDocument(ctx, stranger, doc)
	if errors.Is(err, r.ErrUnsupported) {
		_ = auth.VoidIssue(ctx, sc, in.JobID, checkReason)
		add("X1", ruleCancel, fail("the source does not implement the route", err))
		skip("X2", ruleCancelConfirmed, "the source does not cancel (X1)")
		skip("X3", ruleCancelSubject, "the source does not cancel (X1)")
		skip("X4", ruleNumberStaysUsed, "the source does not cancel (X1)")
		return
	}
	subject := expect(err, r.ErrDenied)
	if subject != nil {
		subject = fail("before the document was cancelled", subject)
	}
	onlyConfirmed := cancelsOnlyTheConfirmed(ctx, auth, canceller, sc, intent(), doc)
	cancelled := cancelTwice(ctx, canceller, sc, doc, confirmed)
	if cancelled != nil {
		_ = auth.VoidIssue(ctx, sc, in.JobID, checkReason)
	} else if subject == nil {
		_, err = canceller.CancelDocument(ctx, stranger, doc)
		if e := expect(err, r.ErrDenied); e != nil {
			subject = fail("after the document was cancelled", e)
		}
	}
	add("X1", ruleCancel, cancelled)
	add("X2", ruleCancelConfirmed, onlyConfirmed)
	add("X3", ruleCancelSubject, subject)
	if cancelled != nil {
		skip("X4", ruleNumberStaysUsed, "the document was not cancelled (X1)")
		return
	}
	add("X4", ruleNumberStaysUsed, numberStaysUsed(ctx, auth, sc, intent(), doc))
}

// cancelTwice asks for the cancellation and asks again with another reason: the adapter has verified each receipt (the job, the
// reference, the time's form); the time is not before the confirmation's and the second receipt is the first.
func cancelTwice(ctx context.Context, canceller r.DocumentCanceller, sc r.SourceContext, doc r.IssueCancellation, confirmed r.IssueReceipt) error {
	first, err := canceller.CancelDocument(ctx, sc, doc)
	if err != nil {
		return err
	}
	if first.CancelledAt.Before(confirmed.ConfirmedAt) {
		return fail("the receipt's time is before the confirmation's", r.ErrIntegrity)
	}
	again := doc
	again.Reason = checkReason + ", asked again"
	second, err := canceller.CancelDocument(ctx, sc, again)
	if err != nil {
		return fail("asking again was refused", err)
	}
	if second.Ref != first.Ref || !second.CancelledAt.Equal(first.CancelledAt) {
		return fail("asking again answered another receipt", r.ErrIntegrity)
	}
	return nil
}

// cancelsOnlyTheConfirmed reserves a second number and asks for its cancellation without confirming it, which is a conflict, voids it,
// and asks for the cancellation of a job the source never reserved, which is not found. The request about the reservation names the
// confirmation of the rules' document: a reservation has none of its own, and the request must be of the contract's form to be sent.
func cancelsOnlyTheConfirmed(ctx context.Context, auth r.DocumentAuthority, canceller r.DocumentCanceller, sc r.SourceContext, in r.IssueIntent, doc r.IssueCancellation) error {
	res, err := auth.PrepareDocument(ctx, sc, in)
	if err != nil {
		return fail("a second number could not be reserved", err)
	}
	_, err = canceller.CancelDocument(ctx, sc, r.IssueCancellation{JobID: in.JobID, ReservationRef: res.Ref, ConfirmationRef: doc.ConfirmationRef, Number: res.Number, Reason: checkReason})
	refused := expect(err, r.ErrConflict)
	voided := auth.VoidIssue(ctx, sc, in.JobID, checkReason)
	if refused != nil {
		return fail("a reservation that was not confirmed", refused)
	}
	if voided != nil {
		return fail("the reservation could not be voided", voided)
	}
	unknown := doc
	unknown.JobID = r.NewID()
	_, err = canceller.CancelDocument(ctx, sc, unknown)
	if e := expect(err, r.ErrNotFound); e != nil {
		return fail("a job the source never reserved", e)
	}
	return nil
}

// numberStaysUsed reserves a number for a new job after the cancellation: it is not the cancelled document's. The reservation is
// voided.
func numberStaysUsed(ctx context.Context, auth r.DocumentAuthority, sc r.SourceContext, in r.IssueIntent, doc r.IssueCancellation) error {
	res, err := auth.PrepareDocument(ctx, sc, in)
	if err != nil {
		return fail("a number could not be reserved after the cancellation", err)
	}
	voided := auth.VoidIssue(ctx, sc, in.JobID, checkReason)
	if res.Number == doc.Number {
		return fail("a job reserved after the cancellation was given the cancelled document's number", r.ErrIntegrity)
	}
	if voided != nil {
		return fail("the reservation could not be voided", voided)
	}
	return nil
}
