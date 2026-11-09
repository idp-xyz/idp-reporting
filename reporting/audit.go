package reporting

import (
	"context"
	"time"
)

// AuditEvent is one action of a person (S12-B, ADR 0018 decision 4): who, when, what, on which object, and how it ended. It never carries a
// value of the object (no template, no rows, no file name); ObjectID is the platform's id of the design, job or artifact.
type AuditEvent struct {
	ID         string    `json:"id"`
	Identity   Identity  `json:"-"`
	Subject    string    `json:"subject,omitempty"` // filled only in a tenant listing, for the administrator
	At         time.Time `json:"at"`
	Action     string    `json:"action"`
	ObjectKind string    `json:"object_kind"`
	ObjectID   string    `json:"object_id,omitempty"`
	Outcome    string    `json:"outcome"`
	Code       string    `json:"code,omitempty"`
}

// The actions, object kinds and outcomes an event may have (migration 012 checks them too).
var (
	AuditActions     = []string{"design.change", "design.release", "issue.submit", "export.submit", "artifact.download", "role.refused"}
	AuditObjectKinds = []string{"layout", "dashboard", "template", "background", "image", "issue", "export", "artifact", "route"}
	AuditOutcomes    = []string{"requested", "succeeded", "refused", "failed"}
)

// MaxAuditListing bounds one page of the audit.
const MaxAuditListing = 200

// AuditLog keeps the audit (S12-B). Record appends one event as its identity. Events lists, newest first, the identity's own events, or the
// events of the identity's tenant and issuer when tenant is set (the caller has checked the administrator role); before is the id of the
// last event of the previous page ("" for the first).
type AuditLog interface {
	Record(ctx context.Context, e AuditEvent) error
	Events(ctx context.Context, i Identity, tenant bool, limit int, before string) ([]AuditEvent, error)
}
