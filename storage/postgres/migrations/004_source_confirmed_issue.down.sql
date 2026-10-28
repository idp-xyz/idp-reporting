-- 004 down: refused while issue jobs or issue records exist unless data loss is accepted: an
-- S03-G binary could not read them (their numbers are the product's strings, not platform counters).
-- Those rows are removed (the issued PDFs stay as plain artifacts) and the counters table that 003
-- created comes back empty.
DELETE FROM reporting_s01.assets WHERE kind='issue';
DELETE FROM reporting_s01.jobs WHERE kind='issue';
CREATE TABLE reporting_s01.issue_numbers (
  tenant text NOT NULL, integration text NOT NULL, last bigint NOT NULL CHECK(last >= 0),
  PRIMARY KEY(tenant,integration)
);
