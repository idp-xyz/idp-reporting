package pgsource

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"time"
)

// The durable source keeps the document-authority capability (S03-H) in two tables of its own
// schema: issue_numbers, one locked row per source that gives out the numbers, and issues, one row
// per job. Every call is one transaction at READ COMMITTED, which lets callers that wait for the
// number row see what the call before them committed. The rules are the in-memory simulator's:
// both decide with the methods of contractsim.IssueRecord and differ only in where the record
// lives and in whose clock is read, here the database's.

const issueColumns = `integration,source_tenant,job_id,ref,seq,number,state,issued_at,expires_at,intent_sha,COALESCE(sha256,''),COALESCE(pages,0),COALESCE(confirmation_ref,''),confirmed_at,COALESCE(reason,''),voided_at`

func scanIssue(row pgx.Row) (contractsim.IssueRecord, int64, error) {
	var rec contractsim.IssueRecord
	var seq int64
	var issued, expires time.Time
	var confirmed, voided *time.Time
	e := row.Scan(&rec.Integration, &rec.SourceTenant, &rec.JobID, &rec.Ref, &seq, &rec.Number, &rec.State, &issued, &expires, &rec.IntentSHA256, &rec.SHA256, &rec.Pages, &rec.ConfirmationRef, &confirmed, &rec.Reason, &voided)
	if e != nil {
		return rec, 0, e
	}
	rec.IssuedAt, rec.ExpiresAt = issued.UTC(), expires.UTC()
	if confirmed != nil {
		rec.ConfirmedAt = confirmed.UTC()
	}
	if voided != nil {
		rec.VoidedAt = voided.UTC()
	}
	return rec, seq, nil
}
func (s *Source) beginIssue(ctx context.Context) (pgx.Tx, error) {
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	if e != nil {
		return nil, r.ErrUnavailable
	}
	return tx, nil
}

// clockNow reads the database clock; the callers read it after taking their locks.
func clockNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if e := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); e != nil {
		return now, r.ErrUnavailable
	}
	return now.UTC(), nil
}

// lockIssue finds the job's record in the source of sc and locks it. A source that is not
// available answers ErrUnavailable; found is false when the source holds nothing for the job,
// which includes a source that does not exist.
func lockIssue(ctx context.Context, tx pgx.Tx, sc r.SourceContext, jobID string) (rec contractsim.IssueRecord, found bool, err error) {
	var unavailable bool
	e := tx.QueryRow(ctx, "SELECT unavailable FROM reporting_source_s01.heads WHERE integration=$1 AND source_tenant=$2", sc.Owner.Integration, sc.SourceTenant).Scan(&unavailable)
	if errors.Is(e, pgx.ErrNoRows) {
		return rec, false, nil
	}
	if e != nil || unavailable {
		return rec, false, r.ErrUnavailable
	}
	rec, _, e = scanIssue(tx.QueryRow(ctx, "SELECT "+issueColumns+" FROM reporting_source_s01.issues WHERE integration=$1 AND source_tenant=$2 AND job_id=$3 FOR UPDATE", sc.Owner.Integration, sc.SourceTenant, jobID))
	if errors.Is(e, pgx.ErrNoRows) {
		return rec, false, nil
	}
	if e != nil {
		return rec, false, r.ErrUnavailable
	}
	return rec, true, nil
}

// SetIssueRefused makes the source refuse every document as not approved by its own rules until
// it is set back; it is a Go-only fixture control like SetUnavailable.
func (s *Source) SetIssueRefused(ctx context.Context, integration, sourceTenant string, refused bool) error {
	result, e := s.pool.Exec(ctx, "UPDATE reporting_source_s01.issue_numbers SET refused=$3 WHERE integration=$1 AND source_tenant=$2", integration, sourceTenant, refused)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return r.ErrNotFound
	}
	return nil
}

// IssueRecord is the test lookup of what the source holds for a job id. Should several sources
// hold the same job id, it returns the earliest issued.
func (s *Source) IssueRecord(ctx context.Context, jobID string) (contractsim.IssueRecord, bool, error) {
	rec, _, e := scanIssue(s.pool.QueryRow(ctx, "SELECT "+issueColumns+" FROM reporting_source_s01.issues WHERE job_id=$1 ORDER BY issued_at,integration,source_tenant LIMIT 1", jobID))
	if errors.Is(e, pgx.ErrNoRows) {
		return rec, false, nil
	}
	if e != nil {
		return rec, false, r.ErrUnavailable
	}
	return rec, true, nil
}

// PrepareDocument makes the refusals of the in-memory simulator in the same order: an intent that
// is not well formed or made for another context than sc, an unavailable source, a principal whose
// current grant may not issue documents, a source read that is not the source's own, unexpired and
// still authorized, a row without the digest the platform froze (re-read, page after page, from
// the immutable version the read was frozen on) and an approval switch that is on. Only then does
// it lock the source's number row, which serializes every reservation of the source, and answer
// from the job's record or give the job the next number.
func (s *Source) PrepareDocument(ctx context.Context, sc r.SourceContext, in r.IssueIntent) (r.IssueReservation, error) {
	if e := contractsim.ValidateIntent(in); e != nil {
		return r.IssueReservation{}, e
	}
	if in.Prepared.Context != sc {
		return r.IssueReservation{}, r.ErrDenied
	}
	tx, e := s.beginIssue(ctx)
	if e != nil {
		return r.IssueReservation{}, e
	}
	defer rollback(ctx, tx)
	v, e := loadSource(ctx, tx, sc, true)
	if e != nil {
		return r.IssueReservation{}, e
	}
	if !v.Grant.IssueDocuments {
		return r.IssueReservation{}, r.ErrDenied
	}
	saved, e := authorize(ctx, tx, sc, in.Prepared)
	if e != nil {
		return r.IssueReservation{}, e
	}
	seed, e := loadSeed(ctx, tx, sc, saved.Version)
	if e != nil {
		return r.IssueReservation{}, e
	}
	rows, e := contractsim.FrozenRows(ctx, seed, saved.Query, saved.Grant, in.Prepared)
	if e != nil {
		return r.IssueReservation{}, e
	}
	if e = contractsim.CheckRow(rows, in); e != nil {
		return r.IssueReservation{}, e
	}
	var prefix string
	var refused bool
	e = tx.QueryRow(ctx, "SELECT prefix,refused FROM reporting_source_s01.issue_numbers WHERE integration=$1 AND source_tenant=$2 FOR UPDATE", sc.Owner.Integration, sc.SourceTenant).Scan(&prefix, &refused)
	if errors.Is(e, pgx.ErrNoRows) {
		return r.IssueReservation{}, r.ErrIntegrity // a source that was never ensured since the sequences exist
	}
	if e != nil {
		return r.IssueReservation{}, r.ErrUnavailable
	}
	if refused {
		return r.IssueReservation{}, r.ErrDenied
	}
	now, e := clockNow(ctx, tx)
	if e != nil {
		return r.IssueReservation{}, e
	}
	rec, _, e := scanIssue(tx.QueryRow(ctx, "SELECT "+issueColumns+" FROM reporting_source_s01.issues WHERE integration=$1 AND source_tenant=$2 AND job_id=$3", sc.Owner.Integration, sc.SourceTenant, in.JobID))
	if e == nil {
		return rec.Again(contractsim.IntentDigest(in), now)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return r.IssueReservation{}, r.ErrUnavailable
	}
	// The read may have run out while the call waited for the number row.
	if !now.Before(in.Prepared.ExpiresAt) {
		return r.IssueReservation{}, r.ErrDenied
	}
	var seq int64
	if e = tx.QueryRow(ctx, "UPDATE reporting_source_s01.issue_numbers SET last_number=last_number+1 WHERE integration=$1 AND source_tenant=$2 RETURNING last_number", sc.Owner.Integration, sc.SourceTenant).Scan(&seq); e != nil {
		return r.IssueReservation{}, r.ErrUnavailable
	}
	rec = contractsim.NewIssueRecord(sc, in, contractsim.IssueNumber(prefix, seq), now)
	_, e = tx.Exec(ctx, "INSERT INTO reporting_source_s01.issues(integration,source_tenant,job_id,ref,seq,number,state,issued_at,expires_at,intent_sha) VALUES($1,$2,$3,$4,$5,$6,'reserved',$7,$8,$9)", rec.Integration, rec.SourceTenant, rec.JobID, rec.Ref, seq, rec.Number, rec.IssuedAt, rec.ExpiresAt, rec.IntentSHA256)
	if e != nil {
		return r.IssueReservation{}, r.ErrUnavailable
	}
	if e = tx.Commit(ctx); e != nil {
		return r.IssueReservation{}, r.ErrUnavailable
	}
	return rec.Reservation(), nil
}

// ConfirmIssue binds the reserved number to the file's digest and page count; the rules are
// contractsim.IssueRecord.Confirm's. It does not check the principal's grant, but an unavailable
// source answers ErrUnavailable and a job the source does not hold ErrNotFound.
func (s *Source) ConfirmIssue(ctx context.Context, sc r.SourceContext, c r.IssueConfirmation) (r.IssueReceipt, error) {
	if e := contractsim.ValidateConfirmation(c); e != nil {
		return r.IssueReceipt{}, e
	}
	tx, e := s.beginIssue(ctx)
	if e != nil {
		return r.IssueReceipt{}, e
	}
	defer rollback(ctx, tx)
	rec, found, e := lockIssue(ctx, tx, sc, c.JobID)
	if e != nil {
		return r.IssueReceipt{}, e
	}
	if !found {
		return r.IssueReceipt{}, r.ErrNotFound
	}
	now, e := clockNow(ctx, tx)
	if e != nil {
		return r.IssueReceipt{}, e
	}
	next, receipt, e := rec.Confirm(c, now)
	if e != nil {
		return r.IssueReceipt{}, e
	}
	if rec.State == contractsim.IssueConfirmed {
		return receipt, nil // the same confirmation again changes nothing
	}
	if _, e = tx.Exec(ctx, "UPDATE reporting_source_s01.issues SET state='confirmed',sha256=$4,pages=$5,confirmation_ref=$6,confirmed_at=$7 WHERE integration=$1 AND source_tenant=$2 AND job_id=$3", sc.Owner.Integration, sc.SourceTenant, c.JobID, next.SHA256, next.Pages, next.ConfirmationRef, next.ConfirmedAt); e != nil {
		return r.IssueReceipt{}, r.ErrUnavailable
	}
	if e = tx.Commit(ctx); e != nil {
		return r.IssueReceipt{}, r.ErrUnavailable
	}
	return receipt, nil
}

// VoidIssue voids what the source holds for the job in sc's source, whoever reserved it and
// whatever the principal's grants now are; holding nothing, or a void job, is not an error.
func (s *Source) VoidIssue(ctx context.Context, sc r.SourceContext, jobID, reason string) error {
	if !contractsim.ValidJobID(jobID) {
		return r.ErrInvalid
	}
	tx, e := s.beginIssue(ctx)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	rec, found, e := lockIssue(ctx, tx, sc, jobID)
	if e != nil || !found {
		return e
	}
	now, e := clockNow(ctx, tx)
	if e != nil {
		return e
	}
	next, changed := rec.Void(reason, now)
	if !changed {
		return nil
	}
	if _, e = tx.Exec(ctx, "UPDATE reporting_source_s01.issues SET state='void',reason=$4,voided_at=$5 WHERE integration=$1 AND source_tenant=$2 AND job_id=$3", sc.Owner.Integration, sc.SourceTenant, jobID, next.Reason, next.VoidedAt); e != nil {
		return r.ErrUnavailable
	}
	if e = tx.Commit(ctx); e != nil {
		return r.ErrUnavailable
	}
	return nil
}

var _ r.DocumentAuthority = (*Source)(nil)
