-- 013 schedules, down: the schedules and their runs go with their tables, and so do the audit's events of schedules, which 012's checks do not
-- admit (export the audit first if it is to be kept). The jobs the runs submitted stay: they are their owners' jobs.
DROP TABLE reporting_s01.schedule_runs;
DROP TABLE reporting_s01.schedules;
DELETE FROM reporting_s01.audit_events WHERE action IN ('schedule.change','schedule.run') OR object_kind = 'schedule';
ALTER TABLE reporting_s01.audit_events DROP CONSTRAINT audit_events_action_check;
ALTER TABLE reporting_s01.audit_events ADD CONSTRAINT audit_events_action_check
  CHECK (action IN ('design.change','design.release','issue.submit','export.submit','artifact.download','role.refused'));
ALTER TABLE reporting_s01.audit_events DROP CONSTRAINT audit_events_object_kind_check;
ALTER TABLE reporting_s01.audit_events ADD CONSTRAINT audit_events_object_kind_check
  CHECK (object_kind IN ('layout','dashboard','template','background','image','issue','export','artifact','route'));
