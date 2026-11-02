-- 008 object_storage (S04-E, docs/S04E_SCOPE.md E3): the ledger of the sweeps learns what the object store did. Two counts, default 0 so that
-- the rows of 006 and 007 stay valid: `objects`, the objects deleted after the artifact rows that named them, and `orphans`, the objects the
-- reconciliation deleted because no asset row named them. Counts only, as every sweep: no key, no id.
-- Nothing else changes: the content of an artifact moves to the object store inside the asset's payload (Artifact.Object), which is JSON, so no
-- column and no backfill is needed, and an artifact stored before stays inline until it expires and is swept.
ALTER TABLE reporting_s01.sweeps ADD COLUMN objects bigint NOT NULL DEFAULT 0 CHECK (objects >= 0);
ALTER TABLE reporting_s01.sweeps ADD COLUMN orphans bigint NOT NULL DEFAULT 0 CHECK (orphans >= 0);
