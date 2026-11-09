-- 011 designs (S11-A, docs/S11A_SCOPE.md, docs/ADR_0017_ENTERPRISE_ROADMAP.md): the designs of the platform's private stores (report layouts,
-- dashboards, document templates, page backgrounds, images) in the platform's database, so that several servers serve the same designs. One row
-- per store and owner holds the owner's whole document of that store, as the private directory's file held it (privatedocs encodes, validates
-- and bounds it; the database keeps bytes). An empty document is no document: a change inserts the row empty, locks it and fills it in one
-- transaction. Row-level security as on jobs and assets (007): a connection sees and writes only the rows of the identity it says it works for.
CREATE TABLE reporting_s01.designs (
  store text NOT NULL CHECK (store IN ('layouts','dashboards','templates','backgrounds','images')),
  tenant text NOT NULL,
  issuer text NOT NULL,
  subject text NOT NULL,
  data bytea NOT NULL CHECK (octet_length(data) <= 8388608),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY (store, tenant, issuer, subject)
);
ALTER TABLE reporting_s01.designs ENABLE ROW LEVEL SECURITY;
CREATE POLICY identity ON reporting_s01.designs AS PERMISSIVE FOR ALL TO PUBLIC
  USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))
  WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''));
CREATE POLICY worker ON reporting_s01.designs AS PERMISSIVE FOR ALL TO PUBLIC
  USING (current_setting('reporting.scope', true) = 'worker')
  WITH CHECK (current_setting('reporting.scope', true) = 'worker');
