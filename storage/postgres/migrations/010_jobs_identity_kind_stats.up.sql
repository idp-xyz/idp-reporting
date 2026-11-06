-- 010 jobs_identity_kind_stats (docs/CAPACITY.md "Issue listing"): extended statistics on the columns the issue listing filters by.
-- The listing (Store.Issues) asks for an identity's newest issue jobs: kind='issue' AND tenant, issuer and subject, newest first, 25 rows. The
-- planner took the four columns for independent ones, so for an identity with a long issue history it estimated about a hundred rows where there
-- are tens of thousands, kept the unique key index and sorted every issue job of the identity (about 100 ms at 1,000,000 job rows with 20,000
-- issue jobs). With statistics on the four columns together it knows how many rows match, reads jobs_identity_created (migration 009) newest first
-- and stops at 25.
-- CREATE STATISTICS is a catalog entry only: it takes no lock that blocks reads or writes. The ANALYZE fills it at once, reading a sample of the
-- table under a lock that blocks neither; without it the statistics stay empty until autovacuum's next analyze of the table.
CREATE STATISTICS IF NOT EXISTS reporting_s01.jobs_identity_kind_stats (dependencies, ndistinct) ON tenant, issuer, subject, kind FROM reporting_s01.jobs;
ANALYZE reporting_s01.jobs;
DO $kindstats$
DECLARE
  found record;
BEGIN
  -- IF NOT EXISTS would keep a same-named object of another definition silently; this step refuses it, as 009 refuses another index.
  SELECT s.stxkind::text AS kinds, (SELECT string_agg(a.attname, ',' ORDER BY k.n) FROM unnest(s.stxkeys) WITH ORDINALITY AS k(attnum, n)
         JOIN pg_attribute a ON a.attrelid = s.stxrelid AND a.attnum = k.attnum) AS cols, s.stxrelid::regclass::text AS rel
    INTO found
    FROM pg_statistic_ext s JOIN pg_namespace n ON n.oid = s.stxnamespace
   WHERE n.nspname = 'reporting_s01' AND s.stxname = 'jobs_identity_kind_stats';
  IF found.rel IS DISTINCT FROM 'reporting_s01.jobs' OR found.cols IS DISTINCT FROM 'tenant,issuer,subject,kind' OR found.kinds IS DISTINCT FROM '{d,f}' THEN
    RAISE EXCEPTION 'statistics jobs_identity_kind_stats exist with another definition (% on % kinds %): drop them, or create them as this step does', found.cols, found.rel, found.kinds;
  END IF;
END
$kindstats$;
