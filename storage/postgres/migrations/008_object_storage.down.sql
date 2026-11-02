-- 008 down: the two counts go (with their checks). The ledger rows stay; what they said of objects is lost, which the migrator refuses
-- while a row says an object was deleted, unless the operator accepts the loss.
ALTER TABLE reporting_s01.sweeps DROP COLUMN orphans;
ALTER TABLE reporting_s01.sweeps DROP COLUMN objects;
