-- 015 kept_results, down: the ledger of the sweeps forgets which sweeps reconciled the object store, since when the bucket was settled and
-- how many objects each sweep left (refused while a row says any of it, unless the loss is accepted: the counts of what each sweep deleted
-- stay), and the index of the stored-results count without the issued files goes. No row of a job or an asset is lost: an index is derived
-- from the rows. The index of 009 is in place at this step (the step after this one is what drops it), so the version this goes back to
-- counts as it always did: a binary older than this step reconciles at every sweep and counts an issued document's file as a stored result.
DROP INDEX IF EXISTS reporting_s01.sweeps_reconciliation;
ALTER TABLE reporting_s01.sweeps DROP COLUMN settled_from;
ALTER TABLE reporting_s01.sweeps DROP COLUMN objects_left;
ALTER TABLE reporting_s01.sweeps DROP COLUMN reconciled;
DROP INDEX IF EXISTS reporting_s01.assets_identity_results;
