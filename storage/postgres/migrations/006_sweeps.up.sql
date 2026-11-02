-- 006 sweeps (S04-B, B4): the ledger of the retention sweeps, one row per sweep, dry runs included: when, with which grace and batch, and
-- the counts of what it deleted or reduced. Counts only: no id of a business resource, no value.
CREATE TABLE reporting_s01.sweeps (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  at timestamptz NOT NULL DEFAULT clock_timestamp(),
  dry_run boolean NOT NULL,
  complete boolean NOT NULL,
  grace_seconds bigint NOT NULL CHECK (grace_seconds >= 0),
  batch integer NOT NULL CHECK (batch > 0),
  artifacts bigint NOT NULL CHECK (artifacts >= 0),
  snapshots bigint NOT NULL CHECK (snapshots >= 0),
  jobs bigint NOT NULL CHECK (jobs >= 0),
  batches bigint NOT NULL CHECK (batches >= 0)
);
