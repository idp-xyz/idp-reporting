-- 016 stored_results_index_cleanup (S25-B1, docs/S25B1_SCOPE.md): assets_identity_kind_expires (009) goes. It served the stored-results count
-- alone, the count reads assets_identity_results since the step kept_results, and it held every issued document's file for as long as the
-- file is kept. A step of its own, because dropping an index takes the table's exclusive lock: here that lock is the first and only one
-- the transaction asks for on assets, and it is held for an instant. In kept_results it would have followed the lock of an index build,
-- and a transaction that had read assets and was waiting to write would have been in a deadlock with the migration.
DROP INDEX IF EXISTS reporting_s01.assets_identity_kind_expires;
