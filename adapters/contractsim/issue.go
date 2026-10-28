package contractsim

import (
	"context"
	"fmt"
	r "idp.local/reporting/reporting"
	"strings"
	"time"
	"unicode/utf8"
)

// The states of an issue record. A reservation is reserved until the platform confirms the
// file it printed or voids the job; a confirmed document can still be voided.
const (
	IssueReserved  = "reserved"
	IssueConfirmed = "confirmed"
	IssueVoid      = "void"
)

const (
	// DefaultNumberPrefix prefixes the numbers of a fixture that names no prefix of its own.
	DefaultNumberPrefix = "DOC"
	// ReservationTTL is how long an unconfirmed reservation can still be confirmed, unless the
	// source read it was made from expires first.
	ReservationTTL = 10 * time.Minute
	// MaxVoidReason is the most bytes of a void reason a simulator keeps.
	MaxVoidReason = 200
)

// IssueRecord is what a simulator holds for one issue job (S03-H): its reservation and, as the
// job goes on, its confirmation or its void. The in-memory and the durable simulator keep this
// same record and decide every call with the methods below, so that their rules cannot drift.
// It is a lookup for tests, not part of the reporting.DocumentAuthority contract. Times are UTC;
// the fields of a step that did not happen are zero.
type IssueRecord struct {
	Integration     string
	SourceTenant    string
	JobID           string
	State           string // IssueReserved, IssueConfirmed or IssueVoid
	Ref             string // the reservation reference
	Number          string
	IssuedAt        time.Time // whole second
	ExpiresAt       time.Time
	IntentSHA256    string // Digest of the intent the job was reserved under, normalized by IntentDigest
	SHA256          string // the confirmed file; kept when a confirmed job is voided later
	Pages           int
	ConfirmationRef string
	ConfirmedAt     time.Time // whole second
	Reason          string    // why the job was voided, at most MaxVoidReason bytes
	VoidedAt        time.Time // whole second
}

// hexOf: exactly n hexadecimal characters, lowercase unless upper also allows A-F.
func hexOf(s string, n int, upper bool) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || upper && c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ValidJobID: the platform's job id, 32 lowercase hexadecimal characters.
func ValidJobID(id string) bool { return hexOf(id, 32, false) }

// ValidNumberPrefix: 1..8 characters of A-Z and 0-9, starting with a letter, so that
// "<prefix>-<digits>" is always a valid issue number.
func ValidNumberPrefix(p string) bool {
	if len(p) < 1 || len(p) > 8 || p[0] < 'A' || p[0] > 'Z' {
		return false
	}
	for i := 1; i < len(p); i++ {
		if c := p[i]; !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// ValidateIntent refuses an intent that is not well formed with ErrInvalid: the job id and the
// row digest are lowercase hexadecimal, the template id and digest hexadecimal, the row index is
// not negative and the template version at least 1.
func ValidateIntent(in r.IssueIntent) error {
	if !ValidJobID(in.JobID) || in.RowIndex < 0 || !hexOf(in.RowSHA256, 64, false) || !hexOf(in.TemplateID, 32, true) || in.TemplateVersion < 1 || !hexOf(in.TemplateSHA256, 64, true) {
		return r.ErrInvalid
	}
	return nil
}

// ValidateConfirmation refuses a confirmation that is not well formed with ErrInvalid: a job id
// of 32 and a digest of 64 lowercase hexadecimal characters and 1..MaxIssuePages pages.
func ValidateConfirmation(c r.IssueConfirmation) error {
	if !ValidJobID(c.JobID) || !hexOf(c.SHA256, 64, false) || c.Pages < 1 || c.Pages > r.MaxIssuePages {
		return r.ErrInvalid
	}
	return nil
}

// IntentDigest identifies an intent: the same job asking for the same thing again has the same
// digest, however the time of its source read is spelled.
func IntentDigest(in r.IssueIntent) string {
	in.Prepared.ExpiresAt = in.Prepared.ExpiresAt.UTC()
	return r.Digest(in)
}

// FrozenRows re-reads the whole frozen source read, page after page through ReadFrozenPage, and
// returns its rows in order: the rows the platform's snapshot holds. Like ReadFrozenPage it is
// not an authorization endpoint; the simulators authorize before calling it.
func FrozenRows(ctx context.Context, seed Fixture, q r.QuerySpec, g Grant, p r.PreparedRead) ([][]string, error) {
	rows := [][]string{}
	cursor := ""
	for {
		page, e := ReadFrozenPage(ctx, seed, q, g, p, cursor, 100)
		if e != nil {
			return nil, e
		}
		rows = append(rows, page.Rows...)
		if page.NextCursor == "" {
			return rows, nil
		}
		if page.NextCursor == cursor {
			return nil, r.ErrIntegrity
		}
		cursor = page.NextCursor
	}
}

// CheckRow: the intent's row must be one of the frozen read's rows with the digest the platform
// froze; otherwise it is not what the source snapshot produced (ErrDenied).
func CheckRow(rows [][]string, in r.IssueIntent) error {
	if in.RowIndex < 0 || in.RowIndex >= len(rows) || r.Digest(rows[in.RowIndex]) != in.RowSHA256 {
		return r.ErrDenied
	}
	return nil
}

// IssueNumber is the number of the seq-th document of a source: <PREFIX>-<6 digits>, more
// digits after the millionth. It is always a valid issue number.
func IssueNumber(prefix string, seq int64) string { return fmt.Sprintf("%s-%06d", prefix, seq) }

// ReservationTimes: a reservation is issued at the clock's whole second and expires
// ReservationTTL later, or with the source read it was made from, whichever is first. The expiry
// is kept to the microsecond, which is what the durable source stores.
func ReservationTimes(now, readExpires time.Time) (issuedAt, expiresAt time.Time) {
	issuedAt = now.UTC().Truncate(time.Second)
	expiresAt = issuedAt.Add(ReservationTTL)
	if e := readExpires.UTC().Truncate(time.Microsecond); e.Before(expiresAt) {
		expiresAt = e
	}
	return issuedAt, expiresAt
}

// CutReason makes the platform's reason fit what a simulator keeps: invalid UTF-8 becomes U+FFFD,
// NUL is dropped (the database cannot hold it) and at most MaxVoidReason bytes remain, cut at a
// rune boundary.
func CutReason(reason string) string {
	reason = strings.ToValidUTF8(strings.ReplaceAll(reason, "\x00", ""), "�")
	if len(reason) <= MaxVoidReason {
		return reason
	}
	n := MaxVoidReason
	for n > 0 && !utf8.RuneStart(reason[n]) {
		n--
	}
	return reason[:n]
}

// NewIssueRecord reserves number for the job of the intent, made at now.
func NewIssueRecord(sc r.SourceContext, in r.IssueIntent, number string, now time.Time) IssueRecord {
	issuedAt, expiresAt := ReservationTimes(now, in.Prepared.ExpiresAt)
	return IssueRecord{Integration: sc.Owner.Integration, SourceTenant: sc.SourceTenant, JobID: in.JobID, State: IssueReserved, Ref: r.NewID(), Number: number, IssuedAt: issuedAt, ExpiresAt: expiresAt, IntentSHA256: IntentDigest(in)}
}

// Reservation is the product's answer to PrepareDocument for the record.
func (rec IssueRecord) Reservation() r.IssueReservation {
	return r.IssueReservation{Ref: rec.Ref, JobID: rec.JobID, Number: rec.Number, IssuedAt: rec.IssuedAt, ExpiresAt: rec.ExpiresAt}
}

// Receipt is the product's answer to ConfirmIssue for a confirmed record.
func (rec IssueRecord) Receipt() r.IssueReceipt {
	return r.IssueReceipt{Ref: rec.ConfirmationRef, ConfirmedAt: rec.ConfirmedAt}
}

// Again answers PrepareDocument for a job that already has this record, once the caller has
// checked the principal, the row and the approval: another intent is ErrConflict, so is a voided
// reservation, a reserved one that has expired is ErrDenied, and a reserved or confirmed one is
// returned as it was reserved (a confirmed one even after its expiry).
func (rec IssueRecord) Again(intentSHA string, now time.Time) (r.IssueReservation, error) {
	switch {
	case rec.IntentSHA256 != intentSHA, rec.State == IssueVoid:
		return r.IssueReservation{}, r.ErrConflict
	case rec.State == IssueReserved && !now.Before(rec.ExpiresAt):
		return r.IssueReservation{}, r.ErrDenied
	}
	return rec.Reservation(), nil
}

// Confirm answers ConfirmIssue at now. It returns the record to keep and the receipt; the record
// is the receiver's when the confirmation had already happened. The reference must be the
// reservation's (else ErrConflict); a voided one is ErrConflict; a confirmed one answers the same
// receipt for the same digest and pages and is ErrConflict for others; a reserved one that has
// expired is ErrDenied. The time is never before the issue time.
func (rec IssueRecord) Confirm(c r.IssueConfirmation, now time.Time) (IssueRecord, r.IssueReceipt, error) {
	if c.ReservationRef != rec.Ref {
		return rec, r.IssueReceipt{}, r.ErrConflict
	}
	switch rec.State {
	case IssueVoid:
		return rec, r.IssueReceipt{}, r.ErrConflict
	case IssueConfirmed:
		if rec.SHA256 != c.SHA256 || rec.Pages != c.Pages {
			return rec, r.IssueReceipt{}, r.ErrConflict
		}
		return rec, rec.Receipt(), nil
	}
	if !now.Before(rec.ExpiresAt) {
		return rec, r.IssueReceipt{}, r.ErrDenied
	}
	at := now.UTC().Truncate(time.Second)
	if at.Before(rec.IssuedAt) {
		at = rec.IssuedAt
	}
	rec.State, rec.SHA256, rec.Pages, rec.ConfirmationRef, rec.ConfirmedAt = IssueConfirmed, c.SHA256, c.Pages, r.NewID(), at
	return rec, rec.Receipt(), nil
}

// Void answers VoidIssue at now: a reserved or confirmed record becomes void with the cut reason
// (changed is true); a void one stays as it is, with its first reason.
func (rec IssueRecord) Void(reason string, now time.Time) (next IssueRecord, changed bool) {
	if rec.State == IssueVoid {
		return rec, false
	}
	rec.State, rec.Reason, rec.VoidedAt = IssueVoid, CutReason(reason), now.UTC().Truncate(time.Second)
	return rec, true
}
