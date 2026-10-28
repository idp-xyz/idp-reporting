-- 002 payload digests (S01-B): every stored JSON document carries the SHA-256 of its canonical JSONB text.
ALTER TABLE reporting_s01.jobs ADD COLUMN data_sha256 text;
ALTER TABLE reporting_s01.assets ADD COLUMN data_sha256 text;
UPDATE reporting_s01.jobs SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex');
UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex');
ALTER TABLE reporting_s01.jobs ALTER COLUMN data_sha256 SET NOT NULL;
ALTER TABLE reporting_s01.assets ALTER COLUMN data_sha256 SET NOT NULL;
