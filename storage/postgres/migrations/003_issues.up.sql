-- 003 issues (S03-G): issue jobs, issue records and the issue numbers per tenant and integration.
ALTER TABLE reporting_s01.jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE reporting_s01.jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('snapshot','export','issue'));
ALTER TABLE reporting_s01.assets DROP CONSTRAINT assets_kind_check;
ALTER TABLE reporting_s01.assets ADD CONSTRAINT assets_kind_check CHECK(kind IN ('snapshot','artifact','issue'));
CREATE TABLE reporting_s01.issue_numbers (
  tenant text NOT NULL, integration text NOT NULL, last bigint NOT NULL CHECK(last >= 0),
  PRIMARY KEY(tenant,integration)
);
