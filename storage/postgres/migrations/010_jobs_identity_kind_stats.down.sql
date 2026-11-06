-- 010 down: the extended statistics go. Nothing else changes and no row is lost (statistics are derived from the rows); the issue listing of an
-- identity with a long issue history goes back to the plan of version 9.
DROP STATISTICS IF EXISTS reporting_s01.jobs_identity_kind_stats;
