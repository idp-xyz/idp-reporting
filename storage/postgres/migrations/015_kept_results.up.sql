-- 015 kept_results (S25-B1, docs/S25B1_SCOPE.md): what the store needs before a result may outlive the read it was made from (ADR 0026): the
-- file of an issued document is then kept for years and an export for days, where both ended within a quarter of an hour.
--
-- 1. The stored-results count of an admission leaves the issued documents' files out (ADR 0026 decision 7): they are kept for years, their
--    number grows with an identity's history, and what bounds them is the admissions per hour. assets_identity_results holds, per identity, the
--    snapshots and the export files only, so the count is answered from the index and does not look into the payload of an issued file to
--    tell it from an export (the payload of an artifact stored without an object store is the file itself). It takes the place of
--    assets_identity_kind_expires (009), which served that count alone and held every issued file; the next step drops that one, in a
--    transaction of its own: dropping it here, after the build, would ask for the table's exclusive lock while this transaction holds the
--    lock of the build, and a transaction that has read assets and waits to write would then be in a deadlock with the migration.
--    As in 009, CREATE INDEX blocks writes to the table while it builds, inside the migration's transaction, and reads go on;
--    docs/OPERATIONS.md "Quotas" gives the alternative (CREATE INDEX CONCURRENTLY with this name and definition before `up`). IF NOT
--    EXISTS accepts an index made that way, and the check refuses one that is not what this step would have made. The check compares the
--    definition as PostgreSQL prints it, letter for letter: the kinds and the payload's key are text, and an index whose predicate names
--    `issue` where the count names `Issue` would be kept and never used, so that every admission read the payloads after all.
CREATE INDEX IF NOT EXISTS assets_identity_results ON reporting_s01.assets (tenant, issuer, subject, expires_at) WHERE kind IN ('snapshot','artifact') AND NOT (data ? 'Issue');
DO $kept$
DECLARE
  want text := 'CREATE INDEX assets_identity_results ON reporting_s01.assets USING btree (tenant, issuer, subject, expires_at) WHERE ((kind = ANY (ARRAY[''snapshot''::text, ''artifact''::text])) AND (NOT (data ? ''Issue''::text)))';
  made record;
BEGIN
  SELECT x.indisvalid AS valid, regexp_replace(pg_get_indexdef(x.indexrelid), '[[:space:]]+', ' ', 'g') AS def INTO made
    FROM pg_index x JOIN pg_class c ON c.oid = x.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'reporting_s01' AND c.relname = 'assets_identity_results';
  IF made.def IS NULL THEN
    RAISE EXCEPTION 'assets_identity_results exists and is not an index of the schema: drop or rename it and run up again';
  END IF;
  IF NOT made.valid THEN
    RAISE EXCEPTION 'index assets_identity_results exists and is invalid (a CREATE INDEX CONCURRENTLY that failed?): drop it and run up again';
  END IF;
  IF made.def IS DISTINCT FROM want THEN
    RAISE EXCEPTION 'index assets_identity_results exists with another definition (%): drop it, or create it as this step does', made.def;
  END IF;
END
$kept$;
-- 2. The ledger of the sweeps says which sweeps reconciled the object store, and how many objects each left behind: the objects of rows it
--    deleted that it did not delete, because a delete failed or because it was given no object store. Until now only the sweep's output
--    said the second. The reconciliation lists the whole bucket, which now holds every file that is kept, so a sweep no longer does it
--    every time: it reads here whether the bucket is due (r.SweepOptions.Reconcile).
--
--    settled_from is the word of a reconciliation that ran to its end and left nothing behind (never a dry run's): the database's time
--    at which its listing began. As of that time the bucket holds no object without a row that the sweep could find. The bucket is
--    settled while the newest entry of the ledger, among the sweeps that left objects or did not complete (by the time they ended, at)
--    and the reconciliations that left none (by the time their listing began, settled_from), is such a reconciliation, within the
--    interval. So a sweep that leaves objects makes the next one reconcile, as it always did; so does a sweep that was cut short or
--    failed, which cannot say what it left (a batch whose commit was not answered may have deleted its rows); and a reconciliation
--    settles only what was left before it began to list: one that ends after another sweep left an object, at a key it had already
--    passed, does not hide it.
--
--    Defaults false, 0 and NULL, so that the rows of 006 and 008 stay valid: they do not say, and the first sweep after this step
--    reconciles. The index holds the sweeps the rule reads, in its order, however many sweeps the ledger records.
ALTER TABLE reporting_s01.sweeps ADD COLUMN reconciled boolean NOT NULL DEFAULT false;
ALTER TABLE reporting_s01.sweeps ADD COLUMN objects_left bigint NOT NULL DEFAULT 0 CHECK (objects_left >= 0);
ALTER TABLE reporting_s01.sweeps ADD COLUMN settled_from timestamptz CONSTRAINT sweeps_settled_from_check CHECK (settled_from IS NULL OR (reconciled AND complete AND objects_left = 0 AND NOT dry_run));
CREATE INDEX sweeps_reconciliation ON reporting_s01.sweeps ((COALESCE(settled_from, at)), id) WHERE NOT dry_run AND (settled_from IS NOT NULL OR objects_left > 0 OR NOT complete);
