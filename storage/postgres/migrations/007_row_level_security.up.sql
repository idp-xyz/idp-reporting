-- 007 row_level_security (S04-C, docs/S04C_SCOPE.md C1): the database refuses the rows of another identity to a connection that has not said
-- whose rows it works on. Row-level security is ENABLED, not FORCED, on the two tables that carry business rows: the owner (the migrator) and a
-- superuser bypass it, so that migrations and their data checks run as before; the application role and the sweep role are subject to it.
-- The store says whose rows it works on with transaction-local settings (set_config(..., true)): reporting.tenant, reporting.issuer and
-- reporting.subject for a call made for an identity, reporting.scope = 'worker' for the workers' and the sweep's transactions. A setting that is
-- missing or empty (a connection that carries one from an earlier transaction shows an empty string, not NULL) makes the comparison NULL: no row
-- is visible and none may be written. The ledgers, the profile and issue_numbers carry no business rows and get no policy.
ALTER TABLE reporting_s01.jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_s01.assets ENABLE ROW LEVEL SECURITY;
CREATE POLICY identity ON reporting_s01.jobs AS PERMISSIVE FOR ALL TO PUBLIC
  USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))
  WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''));
CREATE POLICY worker ON reporting_s01.jobs AS PERMISSIVE FOR ALL TO PUBLIC
  USING (current_setting('reporting.scope', true) = 'worker')
  WITH CHECK (current_setting('reporting.scope', true) = 'worker');
CREATE POLICY identity ON reporting_s01.assets AS PERMISSIVE FOR ALL TO PUBLIC
  USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))
  WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''));
CREATE POLICY worker ON reporting_s01.assets AS PERMISSIVE FOR ALL TO PUBLIC
  USING (current_setting('reporting.scope', true) = 'worker')
  WITH CHECK (current_setting('reporting.scope', true) = 'worker');
