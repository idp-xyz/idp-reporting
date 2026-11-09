package postgres

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	r "idp.local/reporting/reporting"
)

const (
	auditInsertSQL = `INSERT INTO reporting_s01.audit_events (id, tenant, issuer, subject, at, action, object_kind, object_id, outcome, code) VALUES ($1,$2,$3,$4,coalesce($5, clock_timestamp()),$6,$7,$8,$9,$10)`
	// auditListSQL is one page, newest first, after the event $5 (the same rows the connection may see: its own, or its tenant's when it said so).
	auditListSQL = `SELECT id, subject, at, action, object_kind, object_id, outcome, code FROM reporting_s01.audit_events
WHERE tenant=$1 AND issuer=$2 AND ($3 OR subject=$4)
AND ($5='' OR (at, id) < (SELECT b.at, b.id FROM reporting_s01.audit_events b WHERE b.id=$5))
ORDER BY at DESC, id DESC LIMIT $6`
)

// Record implements reporting.AuditLog (S12-B): one row, as the event's identity.
func (s *Store) Record(ctx context.Context, e r.AuditEvent) error {
	if !e.Identity.Valid() || len(e.ID) != 32 || !slices.Contains(r.AuditActions, e.Action) || !slices.Contains(r.AuditObjectKinds, e.ObjectKind) || !slices.Contains(r.AuditOutcomes, e.Outcome) {
		return r.ErrInvalid
	}
	var at *time.Time
	if !e.At.IsZero() {
		at = &e.At
	}
	return asIdentity(ctx, s.pool, e.Identity, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, auditInsertSQL, e.ID, e.Identity.Tenant, e.Identity.Issuer, e.Identity.Subject, at, e.Action, e.ObjectKind, e.ObjectID, e.Outcome, e.Code)
		return err
	})
}

// Events implements reporting.AuditLog. A tenant listing says reporting.audit = 'tenant' in its transaction, which the tenant_audit policy
// requires; the subject of each event is then in the answer.
func (s *Store) Events(ctx context.Context, i r.Identity, tenant bool, limit int, before string) ([]r.AuditEvent, error) {
	if !i.Valid() || limit < 1 || limit > r.MaxAuditListing {
		return nil, r.ErrInvalid
	}
	out := []r.AuditEvent{}
	err := asIdentity(ctx, s.pool, i, func(tx pgx.Tx) error {
		if tenant {
			if err := setAuditTenant(ctx, tx); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, auditListSQL, i.Tenant, i.Issuer, tenant, i.Subject, before, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e r.AuditEvent
			var subject string
			if err := rows.Scan(&e.ID, &subject, &e.At, &e.Action, &e.ObjectKind, &e.ObjectID, &e.Outcome, &e.Code); err != nil {
				return err
			}
			if tenant {
				e.Subject = subject
			}
			e.At = e.At.UTC()
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

var _ r.AuditLog = (*Store)(nil)
