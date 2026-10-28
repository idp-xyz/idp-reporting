package contractsim

import (
	"context"
	r "idp.local/reporting/reporting"
	"time"
)

// source is what the numbers of a product belong to: its integration and source tenant.
type source struct{ integration, sourceTenant string }
type issueKey struct {
	source
	jobID string
}

func sourceOf(sc r.SourceContext) source { return source{sc.Owner.Integration, sc.SourceTenant} }

// SetClock replaces the clock of the simulator, for tests: the issue, confirmation and void
// times, the expiry of reservations and the expiry of source reads all follow it. A nil clock
// restores the real one. The clock must not call back into the simulator.
func (s *Simulator) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = now
}
func (s *Simulator) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// SetIssueRefused makes PrepareDocument refuse every document as not approved by the product's
// own rules, until it is set back. It is a Go-only fixture control like SetUnavailable.
func (s *Simulator) SetIssueRefused(v bool) { s.mu.Lock(); defer s.mu.Unlock(); s.issueRefused = v }

// IssueRecord is the test lookup of what the simulator holds for a job id. Should several
// sources hold the same job id, it returns the earliest issued.
func (s *Simulator) IssueRecord(jobID string) (IssueRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found IssueRecord
	ok := false
	for _, rec := range s.issues {
		if rec.JobID == jobID && (!ok || rec.IssuedAt.Before(found.IssuedAt) || rec.IssuedAt.Equal(found.IssuedAt) && rec.Integration+"\x00"+rec.SourceTenant < found.Integration+"\x00"+found.SourceTenant) {
			found, ok = rec, true
		}
	}
	return found, ok
}

// PrepareDocument refuses, in this order: an intent that is not well formed (ErrInvalid); an
// intent whose source read was made for another context than sc (ErrDenied); an unavailable
// source (ErrUnavailable); and with ErrDenied a principal whose current grant may not issue
// documents, a source read that is not the simulator's own, unexpired and still authorized (as
// AuthorizeCurrent says), a read whose row at RowIndex lacks the digest the platform froze, and
// an approval switch that is on. Only then does it answer from the record of the job, or reserve
// the next number of the source for it. The checks apply to every call, so a principal revoked
// meanwhile is refused even for a job it already reserved.
func (s *Simulator) PrepareDocument(ctx context.Context, sc r.SourceContext, in r.IssueIntent) (r.IssueReservation, error) {
	if e := ctx.Err(); e != nil {
		return r.IssueReservation{}, e
	}
	if e := ValidateIntent(in); e != nil {
		return r.IssueReservation{}, e
	}
	if in.Prepared.Context != sc {
		return r.IssueReservation{}, r.ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, e := s.catalog(sc)
	if e != nil {
		return r.IssueReservation{}, e
	}
	if !g.IssueDocuments {
		return r.IssueReservation{}, r.ErrDenied
	}
	now := s.now()
	f, e := s.authorizeAt(now, sc, in.Prepared)
	if e != nil {
		return r.IssueReservation{}, e
	}
	// The whole frozen read again, from the records it was frozen on and under the grant it was
	// made with; the counter of data reads is the platform's, not the product's own check.
	rows, e := FrozenRows(ctx, Fixture{Dataset: s.dataset, Records: f.records}, f.query, f.grant, in.Prepared)
	if e != nil {
		return r.IssueReservation{}, e
	}
	if e = CheckRow(rows, in); e != nil {
		return r.IssueReservation{}, e
	}
	if s.issueRefused {
		return r.IssueReservation{}, r.ErrDenied
	}
	key := issueKey{sourceOf(sc), in.JobID}
	if rec, ok := s.issues[key]; ok {
		return rec.Again(IntentDigest(in), now)
	}
	s.numbers[key.source]++
	rec := NewIssueRecord(sc, in, IssueNumber(s.prefix, s.numbers[key.source]), now)
	s.issues[key] = rec
	return rec.Reservation(), nil
}

// ConfirmIssue binds the reserved number to the file's digest and page count; the rules are
// IssueRecord.Confirm's. It does not check the principal's grant, but an unavailable source
// answers ErrUnavailable and a job the source does not hold ErrNotFound.
func (s *Simulator) ConfirmIssue(ctx context.Context, sc r.SourceContext, c r.IssueConfirmation) (r.IssueReceipt, error) {
	if e := ctx.Err(); e != nil {
		return r.IssueReceipt{}, e
	}
	if e := ValidateConfirmation(c); e != nil {
		return r.IssueReceipt{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return r.IssueReceipt{}, r.ErrUnavailable
	}
	key := issueKey{sourceOf(sc), c.JobID}
	rec, ok := s.issues[key]
	if !ok {
		return r.IssueReceipt{}, r.ErrNotFound
	}
	next, receipt, e := rec.Confirm(c, s.now())
	if e != nil {
		return r.IssueReceipt{}, e
	}
	s.issues[key] = next
	return receipt, nil
}

// VoidIssue voids what the simulator holds for the job in sc's source, whoever reserved it and
// whatever the principal's grants now are; holding nothing, or a void job, is not an error.
func (s *Simulator) VoidIssue(ctx context.Context, sc r.SourceContext, jobID, reason string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if !ValidJobID(jobID) {
		return r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return r.ErrUnavailable
	}
	key := issueKey{sourceOf(sc), jobID}
	if rec, ok := s.issues[key]; ok {
		if next, changed := rec.Void(reason, s.now()); changed {
			s.issues[key] = next
		}
	}
	return nil
}

var _ r.DocumentAuthority = (*Simulator)(nil)
