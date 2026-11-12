-- 014 issue_cancellations, down: the cancellations go with their table (the step is refused while there are any unless the loss is accepted:
-- the register would show the cancelled documents as issued again and hand out their files while they last), and so do the audit's events of
-- cancellations, which 013's check does not admit (export the audit first if it is to be kept). The issue records are not touched.
DROP TABLE reporting_s01.issue_cancellations;
DELETE FROM reporting_s01.audit_events WHERE action = 'issue.cancel';
ALTER TABLE reporting_s01.audit_events DROP CONSTRAINT audit_events_action_check;
ALTER TABLE reporting_s01.audit_events ADD CONSTRAINT audit_events_action_check
  CHECK (action IN ('design.change','design.release','issue.submit','export.submit','artifact.download','role.refused','schedule.change','schedule.run'));
