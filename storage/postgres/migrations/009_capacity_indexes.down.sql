-- 009 down: the four indexes go. Nothing else changes and no row is lost (an index is derived from the rows).
DROP INDEX IF EXISTS reporting_s01.jobs_reducible;
DROP INDEX IF EXISTS reporting_s01.assets_identity_kind_expires;
DROP INDEX IF EXISTS reporting_s01.jobs_identity_in_flight;
DROP INDEX IF EXISTS reporting_s01.jobs_identity_created;
