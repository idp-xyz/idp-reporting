package reporting

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// The cancellation of an issued document (S24-A, docs/ADR_0025_ISSUE_OPERATIONS.md decisions 1 to 4). A document that was issued correctly
// and should not stand is withdrawn by asking the source product, which decides: the platform never marks a document cancelled without the
// product's receipt, the number stays used, and the platform's record of the issue keeps every field it had: the cancellation is a row of
// its own, added once and never changed.

// DocumentCanceller is the optional capability of an adapter whose source product can be asked to cancel a formal document it confirmed,
// beside DocumentAuthority (ADR 0025 decision 1). An adapter without it cannot be asked: Service.CancelIssue answers ErrUnsupported, as
// issuing does for an adapter without DocumentAuthority.
type DocumentCanceller interface {
	// CancelDocument cancels the confirmed document the product holds for the job of the request, for the principal of the context.
	// The request names the document by the platform's job id, the product's reservation and confirmation references and its number
	// (IssueCancellation), and carries the person's reason. The product refuses with ErrDenied when the principal may not cancel or
	// its own rules say no (the document was settled, a period was closed); with ErrConflict for a document that is not a confirmed
	// one (a reservation it never confirmed, a job it voided) or that the request does not name as the product holds it (another
	// reference or another number); with ErrNotFound for a job it never reserved. It is idempotent by the job id: asking again for a
	// document it has cancelled returns the same receipt, whatever the reason says the second time. The number stays used: it is
	// never given out again. VoidIssue keeps its meaning (the platform cleaning up after a job that failed), and a void of a job
	// whose document was cancelled is not an error and leaves the cancellation as it is.
	CancelDocument(context.Context, SourceContext, IssueCancellation) (IssueCancellationReceipt, error)
}

// IssueCancellation is what the platform asks the product to cancel: the issue by the platform's job id and by what the product gave
// for it (the references of its reservation and of its confirmation, and its number), with the reason of the person who asks.
type IssueCancellation struct {
	JobID           string
	ReservationRef  string
	ConfirmationRef string
	Number          string
	Reason          string // ValidCancellationReason
}

// Valid says whether a cancellation is one the contract carries: the platform's job id, the product's two references and its number as
// the product gave them (ValidProductRef, ValidIssueNumber) and a reason (ValidCancellationReason). An adapter that reaches its product
// over a network sends nothing else, and a product may refuse anything else as not well formed.
func (c IssueCancellation) Valid() bool {
	return validID(c.JobID) && ValidProductRef(c.ReservationRef) && ValidProductRef(c.ConfirmationRef) && ValidIssueNumber(c.Number) && ValidCancellationReason(c.Reason)
}

// IssueCancellationReceipt is the product's record of the cancellation.
type IssueCancellationReceipt struct {
	Ref         string    // the product's cancellation id: 1..128 printable ASCII characters (0x21..0x7E), no space
	CancelledAt time.Time // the product's time: a whole second between the confirmation time and 9999-12-31T23:59:59Z
}

// Valid says whether a receipt is of the contract's form: the product's reference (ValidProductRef) and a time that is a whole second
// between 1970 and 9999, judged by value (ValidProductTime). That the time is not before the confirmation it cancels is judged by
// whoever holds the confirmation: Service.CancelIssue, and a repository that keeps the cancellation (ValidateCancellation).
func (r IssueCancellationReceipt) Valid() bool {
	return ValidProductRef(r.Ref) && ValidProductTime(r.CancelledAt)
}

// MaxCancellationReason is the most characters a cancellation's reason has.
const MaxCancellationReason = 200

// ValidCancellationReason: the reason a person gives for a cancellation is 1..MaxCancellationReason characters of text a person reads
// on one line: valid UTF-8, not blank, without control characters (plainText, the rule of every text a person writes), counted in
// characters and not in bytes, so that a reason in Chinese has the room one in Latin letters has. It is the only free text of a
// cancellation: it is stored with the cancellation and shown to the issue's owner, and it never reaches an error, a log line or a
// metric.
func ValidCancellationReason(s string) bool {
	return plainText(s, MaxCancellationReason*utf8.UTFMax) && utf8.RuneCountInString(s) <= MaxCancellationReason
}

// IssueCancellationRecord is the platform's record of a cancellation the source product made (ADR 0025 decision 3): which issue, the
// product's reference and time, the person's reason, and when the platform recorded it. It is immutable and belongs to the identity
// that owns the issue record.
type IssueCancellationRecord struct {
	IssueID     string
	Ref         string    // the product's cancellation reference (ValidProductRef)
	CancelledAt time.Time // the product's time, UTC, whole second, not before the record's confirmation time
	Reason      string    // ValidCancellationReason
	RecordedAt  time.Time // the store's clock when it kept the row
}

// AuditIssueCancel is the audit action of a cancellation (ADR 0018 decision 4, ADR 0025 decision 2): `requested` when a person asks,
// `succeeded` with the cancellation's record, in its transaction.
const AuditIssueCancel = "issue.cancel"

// CancellationAuditEvent is the event a repository appends with a cancellation it keeps, in the same atomic step (Repository.
// RecordCancellation): the issue's owner cancelled the issue, and it succeeded. Its time is the store's, the cancellation's RecordedAt;
// it carries no value of the cancellation: not the reason, not the number, not the product's reference.
func CancellationAuditEvent(i Identity, issueID string) AuditEvent {
	return AuditEvent{ID: NewID(), Identity: i, Action: AuditIssueCancel, ObjectKind: "issue", ObjectID: issueID, Outcome: "succeeded"}
}

// cancelRecordTimeout bounds the write that records a cancellation the product has made. It runs in a context of its own: once the
// product has cancelled, a caller that went away must not keep the platform from recording it (a retry would complete the record, but
// a retry needs the person).
const cancelRecordTimeout = 5 * time.Second

// CanCancel says, for each product the service serves for the principal (Served), whether a cancellation of an issued document can be
// asked of it: whether the product's adapter is a DocumentCanceller. It asks nothing of a product; whether the product then lets this
// principal cancel a given document is the product's answer to the request.
func (s *Service) CanCancel(p Principal) map[string]bool {
	out := map[string]bool{}
	for _, id := range s.held(p) {
		c, _, _ := s.source(p, id)
		_, ok := c.Adapter.(DocumentCanceller)
		out[id] = ok
	}
	return out
}

// CancelIssue asks the source product to cancel a document the caller issued and records the product's answer (ADR 0025 decision 2),
// in this order:
//
//   - the issue must be the caller's own (ErrNotFound for another identity's, as for any job that is not an issue), under a product
//     the caller holds (ErrDenied), and its job must have succeeded: a queued, running, failed or cancelled job has no issued document
//     to cancel (ErrConflict). The reason must be one (ValidCancellationReason: ErrInvalid).
//   - a cancellation the platform has recorded already answers the issue as it stands, and the product is not asked again;
//   - the product's adapter must be a DocumentCanceller (ErrUnsupported);
//   - the product is asked, once, within the caller's context. Its refusal is the answer and nothing is written: ErrDenied (the
//     principal may not, or the product's rules say no), ErrConflict (the product does not hold the document as a confirmed one),
//     ErrUnavailable (it cannot be reached). A product that says it never reserved a job whose confirmation the platform holds, or
//     that answers with one of the platform's own refusals, has given an answer the platform cannot trust: ErrIntegrity. So has one
//     whose receipt is not of the contract's form or is dated before the confirmation it cancels;
//   - the cancellation and its audit event are recorded in one step (Repository.RecordCancellation), in a context of the write's own,
//     and the issue is answered as the listing shows it, with its cancellation.
//
// Nothing is atomic across the two systems, and every outcome is reconcilable by the job id (ADR 0003 decision 4): when the platform
// fails after the product cancelled (the record or its audit event cannot be written: ErrAuditUnavailable, or the store's error), the
// caller asks again, the product answers the same receipt whatever the reason says the second time, and the record is completed. The
// errors carry nothing of the reason, of the number or of the product's references.
//
// It needs neither the snapshot nor the file: an issue is cancelled long after the source read it was made from has expired and
// retention has removed the file, and the cancellation stays with the record.
func (s *Service) CancelIssue(ctx context.Context, p Principal, issueID, reason string) (IssueView, error) {
	if !p.Identity.Valid() || !validID(issueID) {
		return IssueView{}, ErrNotFound
	}
	if !ValidCancellationReason(reason) {
		return IssueView{}, fmt.Errorf("the reason of a cancellation is 1 to %d characters of text on one line: %w", MaxCancellationReason, ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, WorkTimeout)
	defer cancel()
	j, e := s.repo.Job(ctx, p.Identity, issueID)
	if e != nil {
		return IssueView{}, e
	}
	if j.Kind != "issue" {
		return IssueView{}, ErrNotFound
	}
	c, sc, e := s.source(p, j.Owner.Integration)
	if e != nil {
		return IssueView{}, e
	}
	if j.State != "succeeded" {
		return IssueView{}, fmt.Errorf("an issue job that has not succeeded has no issued document to cancel: %w", ErrConflict)
	}
	v, rec, e := s.issueRecord(ctx, p, j)
	if e != nil {
		return IssueView{}, e
	}
	if rec.Cancellation != nil {
		return v, nil
	}
	canceller, ok := c.Adapter.(DocumentCanceller)
	if !ok {
		return IssueView{}, fmt.Errorf("the product of the issue cannot be asked for a cancellation: %w", ErrUnsupported)
	}
	receipt, e := cancelDocument(ctx, canceller, sc, IssueCancellation{JobID: j.ID, ReservationRef: rec.ReservationRef, ConfirmationRef: rec.ConfirmationRef, Number: rec.Number, Reason: reason})
	if e != nil {
		return IssueView{}, fromCanceller(e)
	}
	// The time is judged by value, as every time the product gives; the record keeps the same instant in UTC.
	if !receipt.Valid() || receipt.CancelledAt.Before(rec.ConfirmedAt) {
		return IssueView{}, fmt.Errorf("the receipt of a cancellation is not of the contract's form or is dated before the confirmation: %w", ErrIntegrity)
	}
	record, stop := context.WithTimeout(context.WithoutCancel(ctx), cancelRecordTimeout)
	defer stop()
	_, e = s.repo.RecordCancellation(record, p.Identity, IssueCancellationRecord{IssueID: j.ID, Ref: receipt.Ref, CancelledAt: receipt.CancelledAt.UTC(), Reason: reason})
	if errors.Is(e, ErrConflict) {
		// The platform holds another receipt for this issue (a request that ran beside this one recorded it): a product that is
		// idempotent by the job id gives one receipt, so this one cannot be trusted. The recorded one stands.
		return IssueView{}, fmt.Errorf("%w: the source product gave another receipt than the one recorded for the issue", ErrIntegrity)
	}
	if e != nil {
		return IssueView{}, e
	}
	return s.issueView(ctx, p, j)
}

// cancelDocument is the one call to the product, with a panic in the adapter turned into an error that carries no value (the panic's
// value may hold data), as the worker treats a panic of the document authority.
func cancelDocument(ctx context.Context, canceller DocumentCanceller, sc SourceContext, c IssueCancellation) (receipt IssueCancellationReceipt, err error) {
	defer func() {
		if recover() != nil {
			receipt, err = IssueCancellationReceipt{}, errExecutionPanicked
		}
	}()
	return canceller.CancelDocument(ctx, sc, c)
}

// fromCanceller is the product's refusal of a cancellation as CancelIssue answers it. What a product may say passes as it is: its
// denial, its conflict, its outage, a request it finds malformed, a capability it lacks after all. A job it says it never reserved is
// not "not found" to the caller, who can see the issue in the register: the platform holds the product's own confirmation of that job,
// so the two systems disagree. And the platform's own refusals (a lost lease, a role, the audit, a quota, a cancelled issue) are not
// the product's to speak: an adapter that answers with one would have the platform say something false about itself. Both are
// INTEGRITY_FAILURE, with a fixed text: nothing of the adapter's error is kept.
func fromCanceller(e error) error {
	for _, own := range []error{ErrNotFound, ErrLease, ErrRoleDenied, ErrAuditUnavailable, ErrQuota, ErrIssueCancelled} {
		if errors.Is(e, own) {
			return fmt.Errorf("%w: the source product answered a cancellation with %s, which it cannot say of an issue the platform holds its confirmation of", ErrIntegrity, own.Error())
		}
	}
	return e
}
