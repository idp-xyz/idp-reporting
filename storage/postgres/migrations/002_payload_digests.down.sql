-- 002 down: the digests are derived from the payloads, so nothing is lost.
ALTER TABLE reporting_s01.jobs DROP COLUMN data_sha256;
ALTER TABLE reporting_s01.assets DROP COLUMN data_sha256;
