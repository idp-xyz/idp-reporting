# S04-B retention and cleanup

2026-10-29. Today nothing the platform stores is ever deleted: an expired frozen snapshot keeps its business rows,
an expired artifact keeps its file (an issued PDF included), a terminal job keeps its payload. The platform may
keep authorization projections and results only for their lifecycle (docs/s00/ARCHITECTURE.md: "平台可以保存有生命周期
的授权投影/结果"); keeping business data after its authorization has expired is a data-minimisation defect, and
the store grows without bound. This slice deletes what has expired, keeps what must be kept, and proves both.

B1 · What is kept and what goes. A snapshot asset (business rows) is deleted once its expiry (the source read's
expiry, which every download and page already enforces) is older than the grace period. An artifact (XLSX, PDF,
issued PDF) is deleted with or after its snapshot, by the same rule. An issue record (asset kind `issue`) is
never deleted: it stays the permanent record of what was issued, and its view says the file is no longer held.
A job keeps its row (state, codes, ids, times: the audit of what happened); a terminal job's payload is reduced
to what its view and the audit need once its snapshot is gone (no frozen query values beyond the request hash,
no row values; the frozen template of an issue job stays, it carries no business data). A queued or running
job, and every asset a non-terminal job still references, is never touched. The private stores on disk
(layouts, dashboards, templates, backgrounds, images) are configuration, not results, and are out of scope.

B2 · Schema. Migration 005 adds `expires_at timestamptz` to assets (NULL for kinds that never expire), backfilled
from the payload in the up step, with an index on (expires_at) for the sweep; the payload's digest and the
store's decoding stay consistent; the down step drops the column (no data loss: the payload still has the
expiry). The memory store follows the same rules.

B3 · The sweep. `Service.Sweep(ctx, SweepOptions{Grace, Batch, DryRun}) (SweepResult, error)` deletes in bounded
batches (each batch one transaction, `FOR UPDATE SKIP LOCKED`, the database clock decides expiry), in the order
artifacts, then snapshots, then job payload reduction, and returns counts only. It is idempotent and safe to run
concurrently with workers, downloads, other sweeps and migrations' advisory lock (it takes none of them; a
row being read either is read before the delete or is gone, and a download of a deleted artifact answers what a
download of an expired one answers today). Defaults: grace 24 h, batch 500. A sweep never deletes anything whose
expiry is in the future by the database's clock, whatever the platform host's clock says.

B4 · Running it. The demo's worker loop runs a sweep every 10 minutes (flag `-sweep-interval`, 0 to disable);
`cmd/reporting-migrate` gains `sweep [--grace D] [--batch N] [--dry-run]` for operators (counts printed, exit 0 /
non-zero); OPERATIONS.md documents both, the grace period, what is kept, and how to verify (a query per kind).
A sweep's counts are written to a small ledger table `sweeps` (time, counts, dry-run flag) for audit; no ids,
no values.

B5 · Views after cleanup. A job whose snapshot or artifact is gone answers its view with the ids and a state that
says so (no 500, no integrity failure); `GET /issues` and an issue's view say the file has expired; the console
shows "已过期并清除" where it showed a download. Downloads of a swept artifact answer as an expired one does.

B6 · Tests. Repository contract tests on both stores (each kind at, before and after its expiry plus grace by
the database clock; referenced assets of running jobs kept; issue records kept; idempotence; dry run deletes
nothing and counts the same; concurrent sweeps and a worker publishing meanwhile; batches); migration 005
round trip on the migration qualification (catalog equality up/down, backfill correct, refused never); the
operator command; the views after cleanup over HTTP; DOM for the console label. PostgreSQL-only top-level tests
go into the CI skip lists or, better, become subtests of the existing gated ones.

Not in this slice: object storage for artifacts, quotas, row-level security, backups; deleting private
configuration stores; legal holds (a later decision if a product needs them).
