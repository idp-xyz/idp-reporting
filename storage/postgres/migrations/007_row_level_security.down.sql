-- 007 down: the policies go and row-level security is disabled; no row is touched.
DROP POLICY identity ON reporting_s01.jobs;
DROP POLICY worker ON reporting_s01.jobs;
DROP POLICY identity ON reporting_s01.assets;
DROP POLICY worker ON reporting_s01.assets;
ALTER TABLE reporting_s01.jobs DISABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_s01.assets DISABLE ROW LEVEL SECURITY;
