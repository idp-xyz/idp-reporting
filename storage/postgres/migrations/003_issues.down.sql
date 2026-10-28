-- 003 down: refused while issue jobs or issue records exist unless data loss is accepted; then
-- those rows are removed before the constraints are restored.
DELETE FROM reporting_s01.assets WHERE kind='issue';
DELETE FROM reporting_s01.jobs WHERE kind='issue';
DROP TABLE reporting_s01.issue_numbers;
ALTER TABLE reporting_s01.jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE reporting_s01.jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('snapshot','export'));
ALTER TABLE reporting_s01.assets DROP CONSTRAINT assets_kind_check;
ALTER TABLE reporting_s01.assets ADD CONSTRAINT assets_kind_check CHECK(kind IN ('snapshot','artifact'));
