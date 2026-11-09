-- 012 audit (S12-B, docs/S12B_SCOPE.md, docs/ADR_0018_ROLES_APPROVAL_AUDIT.md): an append-only record of people's actions. The application role
-- inserts and reads, never updates or deletes; the sweep never touches it. Row-level security: a connection sees and writes the rows of the
-- identity it says it works for (the identity policy of 007), and reads, never writes, every row of its tenant and issuer when it has also said
-- reporting.audit = 'tenant' (the server says so only for a principal with the administrator role). No worker policy: no worker reads it.
CREATE TABLE reporting_s01.audit_events (
  id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
  tenant text NOT NULL,
  issuer text NOT NULL,
  subject text NOT NULL,
  at timestamptz NOT NULL DEFAULT clock_timestamp(),
  action text NOT NULL CHECK (action IN ('design.change','design.release','issue.submit','export.submit','artifact.download','role.refused')),
  object_kind text NOT NULL CHECK (object_kind IN ('layout','dashboard','template','background','image','issue','export','artifact','route')),
  object_id text NOT NULL DEFAULT '' CHECK (length(object_id) <= 64),
  outcome text NOT NULL CHECK (outcome IN ('requested','succeeded','refused','failed')),
  code text NOT NULL DEFAULT '' CHECK (length(code) <= 64)
);
CREATE INDEX audit_events_tenant_at ON reporting_s01.audit_events (tenant, issuer, at DESC, id DESC);
CREATE INDEX audit_events_identity_at ON reporting_s01.audit_events (tenant, issuer, subject, at DESC, id DESC);
ALTER TABLE reporting_s01.audit_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY identity ON reporting_s01.audit_events AS PERMISSIVE FOR ALL TO PUBLIC
  USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))
  WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''));
CREATE POLICY tenant_audit ON reporting_s01.audit_events AS PERMISSIVE FOR SELECT TO PUBLIC
  USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND current_setting('reporting.audit', true) = 'tenant');
