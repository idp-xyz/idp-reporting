-- 005 down: the column is derived from the payload, which keeps the expiry, so nothing is lost.
DROP INDEX reporting_s01.assets_expires_at;
ALTER TABLE reporting_s01.assets DROP COLUMN expires_at;
