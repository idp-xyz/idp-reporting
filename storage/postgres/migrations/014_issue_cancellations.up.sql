-- 014 issue_cancellations (S24-A, docs/S24A_SCOPE.md, docs/ADR_0025_ISSUE_OPERATIONS.md decision 3): the cancellations the source products made
-- of issued documents, as the platform recorded them. A cancellation is a row of its own beside the issue record (assets, kind 'issue'), which
-- keeps every field it had: the issue, the identity that owns it (the columns the policies read), the product's reference and time, the
-- person's reason and when the platform recorded it. One row per issue, inserted once: the application role inserts and reads and never updates
-- or deletes (an append-only table, like the audit of 012), and the sweep never touches it. Row-level security, under the issue's own: a
-- connection reads the cancellations of the identity it says it works for, and inserts one only for that identity and only for an issue record
-- it can see, which the policies of 007 on assets make that identity's own record: another identity can neither read nor insert a cancellation
-- of the issue, whatever owner it claims for the row. No policy admits UPDATE or DELETE, and there is no worker policy: nothing rewrites a
-- cancellation and no worker reads one.
CREATE TABLE reporting_s01.issue_cancellations (
  issue_id text PRIMARY KEY CHECK (issue_id ~ '^[0-9a-f]{32}$'),
  tenant text NOT NULL,
  issuer text NOT NULL,
  subject text NOT NULL,
  ref text NOT NULL CHECK (ref ~ '^[\x21-\x7E]{1,128}$'),
  cancelled_at timestamptz NOT NULL,
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 200),
  recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE reporting_s01.issue_cancellations ENABLE ROW LEVEL SECURITY;
CREATE POLICY identity ON reporting_s01.issue_cancellations AS PERMISSIVE FOR SELECT TO PUBLIC
  USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''));
CREATE POLICY issue_owner ON reporting_s01.issue_cancellations AS PERMISSIVE FOR INSERT TO PUBLIC
  WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), '')
    AND EXISTS (SELECT 1 FROM reporting_s01.assets a WHERE a.id = issue_cancellations.issue_id AND a.kind = 'issue' AND a.tenant = issue_cancellations.tenant AND a.issuer = issue_cancellations.issuer AND a.subject = issue_cancellations.subject));
-- The audit (012) records the cancellation, in the transaction that inserts it.
ALTER TABLE reporting_s01.audit_events DROP CONSTRAINT audit_events_action_check;
ALTER TABLE reporting_s01.audit_events ADD CONSTRAINT audit_events_action_check
  CHECK (action IN ('design.change','design.release','issue.submit','export.submit','artifact.download','role.refused','schedule.change','schedule.run','issue.cancel'));
