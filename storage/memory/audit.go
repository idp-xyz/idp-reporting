package memory

import (
	"context"
	"slices"

	r "idp.local/reporting/reporting"
)

// Record implements reporting.AuditLog (S12-B): the events in the order recorded.
func (s *Store) Record(ctx context.Context, e r.AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !e.Identity.Valid() || e.ID == "" || !slices.Contains(r.AuditActions, e.Action) || !slices.Contains(r.AuditObjectKinds, e.ObjectKind) || !slices.Contains(r.AuditOutcomes, e.Outcome) {
		return r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.At.IsZero() {
		e.At = s.now()
	}
	e.Subject = ""
	s.audit = append(s.audit, e)
	return nil
}

// Events implements reporting.AuditLog: newest first, the identity's own or its tenant's, after before.
func (s *Store) Events(ctx context.Context, i r.Identity, tenant bool, limit int, before string) ([]r.AuditEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !i.Valid() || limit < 1 || limit > r.MaxAuditListing {
		return nil, r.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []r.AuditEvent{}
	from := len(s.audit) - 1
	if before != "" {
		at := slices.IndexFunc(s.audit, func(e r.AuditEvent) bool { return e.ID == before })
		if at < 0 {
			return out, nil
		}
		from = at - 1
	}
	for n := from; n >= 0 && len(out) < limit; n-- {
		e := s.audit[n]
		mine := e.Identity == i
		ours := e.Identity.Tenant == i.Tenant && e.Identity.Issuer == i.Issuer
		if mine || tenant && ours {
			if tenant {
				e.Subject = e.Identity.Subject
			}
			out = append(out, e)
		}
	}
	return out, nil
}
