-- 013 schedules (S16-A, docs/S16A_SCOPE.md, docs/ADR_0020_SCHEDULED_REPORTS.md): scheduled reports and their runs. A schedule is one identity's
-- definition (data: the name, the frozen query, the export format, the as-of rule and the recurrence, as reporting.Schedule encodes them, with
-- their digest) and its state; next_run_at is the occurrence the scheduler starts next, set only while the schedule is active. A deleted schedule
-- stays as a tombstone (the application role never deletes). A run is one occurrence: unique per schedule and occurrence, so that no occurrence
-- runs twice, with the query frozen when it started (data), its jobs and results, its delivery, and the lease (generation, lease_until) of the
-- scheduler that holds it. Row-level security as on jobs and assets (007): a connection sees and writes the rows of the identity it says it works
-- for, and the scheduler's transactions (reporting.scope = 'worker') those of every identity.
CREATE TABLE reporting_s01.schedules (
  id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
  tenant text NOT NULL,
  integration text NOT NULL,
  issuer text NOT NULL,
  subject text NOT NULL,
  idem_key text NOT NULL CHECK (length(idem_key) BETWEEN 1 AND 128),
  request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  state text NOT NULL CHECK (state IN ('active','paused','deleted')),
  data jsonb NOT NULL,
  data_sha256 text NOT NULL CHECK (data_sha256 ~ '^[0-9a-f]{64}$'),
  next_run_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK ((state = 'active') = (next_run_at IS NOT NULL)),
  UNIQUE (tenant, issuer, subject, idem_key)
);
CREATE INDEX schedules_due ON reporting_s01.schedules (next_run_at) WHERE state = 'active';
CREATE INDEX schedules_identity_created ON reporting_s01.schedules (tenant, issuer, subject, created_at DESC, id DESC);
CREATE TABLE reporting_s01.schedule_runs (
  id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
  schedule_id text NOT NULL REFERENCES reporting_s01.schedules (id),
  tenant text NOT NULL,
  integration text NOT NULL,
  issuer text NOT NULL,
  subject text NOT NULL,
  due_at timestamptz NOT NULL,
  state text NOT NULL CHECK (state IN ('due','snapshot','export','succeeded','failed','refused','cancelled')),
  data jsonb NOT NULL,
  data_sha256 text NOT NULL CHECK (data_sha256 ~ '^[0-9a-f]{64}$'),
  snapshot_job text NOT NULL DEFAULT '' CHECK (snapshot_job = '' OR snapshot_job ~ '^[0-9a-f]{32}$'),
  export_job text NOT NULL DEFAULT '' CHECK (export_job = '' OR export_job ~ '^[0-9a-f]{32}$'),
  snapshot_id text NOT NULL DEFAULT '' CHECK (snapshot_id = '' OR snapshot_id ~ '^[0-9a-f]{32}$'),
  artifact_id text NOT NULL DEFAULT '' CHECK (artifact_id = '' OR artifact_id ~ '^[0-9a-f]{32}$'),
  error_code text NOT NULL DEFAULT '' CHECK (length(error_code) <= 64),
  delivery text NOT NULL DEFAULT 'none' CHECK (delivery IN ('none','pending','delivered','failed')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 100),
  check_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  generation bigint NOT NULL DEFAULT 0,
  lease_until timestamptz,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  finished_at timestamptz,
  UNIQUE (schedule_id, due_at)
);
CREATE INDEX schedule_runs_open ON reporting_s01.schedule_runs (check_at) WHERE state IN ('due','snapshot','export') OR delivery = 'pending';
ALTER TABLE reporting_s01.schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_s01.schedule_runs ENABLE ROW LEVEL SECURITY;
CREATE POLICY identity ON reporting_s01.schedules AS PERMISSIVE FOR ALL TO PUBLIC
  USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))
  WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''));
CREATE POLICY worker ON reporting_s01.schedules AS PERMISSIVE FOR ALL TO PUBLIC
  USING (current_setting('reporting.scope', true) = 'worker')
  WITH CHECK (current_setting('reporting.scope', true) = 'worker');
CREATE POLICY identity ON reporting_s01.schedule_runs AS PERMISSIVE FOR ALL TO PUBLIC
  USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))
  WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''));
CREATE POLICY worker ON reporting_s01.schedule_runs AS PERMISSIVE FOR ALL TO PUBLIC
  USING (current_setting('reporting.scope', true) = 'worker')
  WITH CHECK (current_setting('reporting.scope', true) = 'worker');
-- The audit (012) records the schedules' changes and runs.
ALTER TABLE reporting_s01.audit_events DROP CONSTRAINT audit_events_action_check;
ALTER TABLE reporting_s01.audit_events ADD CONSTRAINT audit_events_action_check
  CHECK (action IN ('design.change','design.release','issue.submit','export.submit','artifact.download','role.refused','schedule.change','schedule.run'));
ALTER TABLE reporting_s01.audit_events DROP CONSTRAINT audit_events_object_kind_check;
ALTER TABLE reporting_s01.audit_events ADD CONSTRAINT audit_events_object_kind_check
  CHECK (object_kind IN ('layout','dashboard','template','background','image','issue','export','artifact','route','schedule'));
