package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	r "idp.local/reporting/reporting"
)

// The cancellations of issued documents (S24-A, migration 014, docs/ADR_0025_ISSUE_OPERATIONS.md decision 3). A cancellation is a row of its
// own beside the issue record: the record's row in assets is never written again, the application role inserts a cancellation once and
// never updates or deletes it, and its audit event is inserted in the same transaction. Every statement runs in the identity's context and
// carries the identity's predicate, as the asset reads do: the predicate is the service's own isolation, the policies of 014 the database's.
const (
	// issueWithCancellationSQL is the issue record of an identity with the cancellation of its document beside it (NULLs when there is
	// none): assetByID's columns, then the cancellation's.
	issueWithCancellationSQL = `SELECT a.data::text,a.data_sha256,a.id,a.kind,a.tenant,a.integration,a.issuer,a.subject,a.expires_at,c.ref,c.cancelled_at,c.reason,c.recorded_at
FROM reporting_s01.assets a LEFT JOIN reporting_s01.issue_cancellations c ON c.issue_id=a.id AND c.tenant=a.tenant AND c.issuer=a.issuer AND c.subject=a.subject
WHERE a.id=$1 AND a.tenant=$2 AND a.issuer=$3 AND a.subject=$4 AND a.kind='issue'`
	// cancellationInsertSQL keeps a cancellation once: a second insert for the issue inserts nothing and returns no row. The time the row
	// was kept is the database's clock.
	cancellationInsertSQL = `INSERT INTO reporting_s01.issue_cancellations (issue_id, tenant, issuer, subject, ref, cancelled_at, reason) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (issue_id) DO NOTHING RETURNING recorded_at`
)

// readIssue reads the identity's issue record and the cancellation of its document in the transaction: the record decoded under its
// digest and held to its row as every asset is, the cancellation attached as it was kept. ErrNotFound when the identity has no such
// record.
func readIssue(ctx context.Context, tx pgx.Tx, i r.Identity, id string) (r.Issue, error) {
	var row assetRow
	var ref, reason *string
	var cancelled, recorded *time.Time
	e := tx.QueryRow(ctx, issueWithCancellationSQL, id, i.Tenant, i.Issuer, i.Subject).Scan(append(row.columns(), &ref, &cancelled, &reason, &recorded)...)
	if errors.Is(e, pgx.ErrNoRows) {
		return r.Issue{}, r.ErrNotFound
	}
	if e != nil {
		return r.Issue{}, e
	}
	var v r.Issue
	if e = row.decode(&v); e != nil {
		return r.Issue{}, e
	}
	// The four columns are NOT NULL: any of them present means a cancellation is there, and one that is not whole is not served as none.
	if ref != nil || cancelled != nil || reason != nil || recorded != nil {
		if ref == nil || cancelled == nil || reason == nil || recorded == nil {
			return r.Issue{}, r.ErrIntegrity
		}
		v.Cancellation = &r.IssueCancellationRecord{IssueID: v.ID, Ref: *ref, CancelledAt: cancelled.UTC(), Reason: *reason, RecordedAt: recorded.UTC()}
	}
	return v, nil
}

// RecordCancellation implements reporting.Repository: in one transaction as the identity, the issue record is read (it must be the
// identity's), the cancellation is held to it, inserted once, and its audit event inserted with the time the row was kept. A row that is
// there already (kept by an earlier call, or by one that ran beside this one and committed first: the unique index makes the second
// insert wait for it) answers as it was kept when it carries the same receipt, and is a conflict otherwise; nothing is written then. When
// the audit event cannot be inserted the transaction is rolled back, the cancellation with it, and the error is ErrAuditUnavailable.
func (s *Store) RecordCancellation(ctx context.Context, i r.Identity, c r.IssueCancellationRecord) (r.IssueCancellationRecord, error) {
	var out r.IssueCancellationRecord
	e := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) error {
		rec, e := readIssue(ctx, tx, i, c.IssueID)
		if e != nil {
			return e
		}
		if e = r.ValidateCancellation(rec, c); e != nil {
			return e
		}
		same := func(kept r.IssueCancellationRecord) error {
			if kept.Ref != c.Ref || !kept.CancelledAt.Equal(c.CancelledAt) {
				return r.ErrConflict
			}
			out = kept
			return nil
		}
		if rec.Cancellation != nil {
			return same(*rec.Cancellation)
		}
		var recorded time.Time
		e = tx.QueryRow(ctx, cancellationInsertSQL, c.IssueID, i.Tenant, i.Issuer, i.Subject, c.Ref, c.CancelledAt.UTC(), c.Reason).Scan(&recorded)
		if errors.Is(e, pgx.ErrNoRows) {
			// Another transaction kept one between the read and the insert: it has committed (the insert waited for it), and this
			// statement sees it.
			if rec, e = readIssue(ctx, tx, i, c.IssueID); e != nil {
				return e
			}
			if rec.Cancellation == nil {
				return r.ErrConflict
			}
			return same(*rec.Cancellation)
		}
		if e != nil {
			return e
		}
		event := r.CancellationAuditEvent(i, c.IssueID)
		if _, e = tx.Exec(ctx, auditInsertSQL, event.ID, i.Tenant, i.Issuer, i.Subject, &recorded, event.Action, event.ObjectKind, event.ObjectID, event.Outcome, event.Code); e != nil {
			return fmt.Errorf("%w: the audit event of a cancellation could not be written", r.ErrAuditUnavailable)
		}
		out = r.IssueCancellationRecord{IssueID: c.IssueID, Ref: c.Ref, CancelledAt: c.CancelledAt.UTC(), Reason: c.Reason, RecordedAt: recorded.UTC()}
		return nil
	})
	if e != nil {
		return r.IssueCancellationRecord{}, e
	}
	return out, nil
}
