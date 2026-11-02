-- 009 capacity_indexes (S04-G, docs/S04G_SCOPE.md G1): the indexes that the hot paths lacked. The admission counts of S04-D, the issue
-- listing and the stored-results count filter by (tenant, issuer, subject), which no index led with, so each of them scanned (the results count
-- scanned the assets of every tenant). They are:
--   jobs_identity_created      the hour count and the issue listing (an identity's jobs, newest first);
--   jobs_identity_in_flight    the jobs in flight: partial, so it holds the queued and running rows only and stays tiny whatever the history;
--   assets_identity_kind_expires  the stored-results count (and any read of an identity's assets).
--   jobs_reducible             the retention sweep's job reduction: the finished jobs whose payload is not yet reduced, by the expiry of their own
--                              source read; partial, so a job leaves it when it is reduced and the history already reduced is never walked.
-- CREATE INDEX takes a lock that blocks writes to the table while it builds, and this step builds inside the migration's transaction (docs/
-- OPERATIONS.md "Capacity" gives the alternative: CREATE INDEX CONCURRENTLY with these names and definitions before `up`). IF NOT EXISTS lets the
-- step accept an index made that way; the check after it refuses one that is not what this step would have made (another definition under the
-- same name, or an index a failed CONCURRENTLY build left invalid), because IF NOT EXISTS alone would keep it silently.
CREATE INDEX IF NOT EXISTS jobs_identity_created ON reporting_s01.jobs (tenant, issuer, subject, created_at DESC);
CREATE INDEX IF NOT EXISTS jobs_identity_in_flight ON reporting_s01.jobs (tenant, issuer, subject) WHERE state IN ('queued','running');
CREATE INDEX IF NOT EXISTS assets_identity_kind_expires ON reporting_s01.assets (tenant, issuer, subject, kind, expires_at);
CREATE INDEX IF NOT EXISTS jobs_reducible ON reporting_s01.jobs (source_expires_at) WHERE state IN ('succeeded','failed','cancelled') AND NOT (data ? 'Reduced');
DO $capacity$
DECLARE
  want text[][] := ARRAY[
    ['jobs_identity_created', 'create index jobs_identity_created on reporting_s01.jobs using btree (tenant, issuer, subject, created_at desc)'],
    ['jobs_identity_in_flight', 'create index jobs_identity_in_flight on reporting_s01.jobs using btree (tenant, issuer, subject) where (state = any (array[''queued''::text, ''running''::text]))'],
    ['assets_identity_kind_expires', 'create index assets_identity_kind_expires on reporting_s01.assets using btree (tenant, issuer, subject, kind, expires_at)'],
    ['jobs_reducible', 'create index jobs_reducible on reporting_s01.jobs using btree (source_expires_at) where ((state = any (array[''succeeded''::text, ''failed''::text, ''cancelled''::text])) and (not (data ? ''reduced''::text)))']];
  i integer;
  found record;
BEGIN
  FOR i IN 1..array_length(want, 1) LOOP
    SELECT x.indisvalid AS valid, regexp_replace(lower(pg_get_indexdef(x.indexrelid)), '\s+', ' ', 'g') AS def INTO found
      FROM pg_index x JOIN pg_class c ON c.oid = x.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'reporting_s01' AND c.relname = want[i][1];
    IF NOT found.valid THEN
      RAISE EXCEPTION 'index % exists and is invalid (a CREATE INDEX CONCURRENTLY that failed?): drop it and run up again', want[i][1];
    END IF;
    IF found.def IS DISTINCT FROM want[i][2] THEN
      RAISE EXCEPTION 'index % exists with another definition (%): drop it, or create it as this step does', want[i][1], found.def;
    END IF;
  END LOOP;
END
$capacity$;
