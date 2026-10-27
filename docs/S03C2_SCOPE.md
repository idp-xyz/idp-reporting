# S03-C2 private dashboard server versions

2026-10-27. Baseline cc54990c09f85f6dd2b95194ab3fad72d6022e87.
Add opt-in -dashboard-directory, separate from report -layout-directory, with
owner-private dashboard save/list/get historical version routes at /dashboards.
Use trusted tenant/issuer/subject ownership, current source catalog authorization
on save/list/get, fixed integration/dataset per history, expected version CAS and
immutable versions. Numeric metric cards must reference published numeric metric
columns; tables only known columns. Strict exact widget variants, 12widgets/64KiB,
16documents/owner,20versions/document,8MiB owner file. Empty dashboards valid.
Responses contain metadata only. No data/companies/date/identity/token in config.
Report column layout APIs and files must not accept dashboard documents.

Extract reusable typed repository privatedocs from existing layouts implementation;
retain report Store/Entry/publication API compatibility and identical schema2/legacy
file behavior, quotas, locking/fsync, corruption rejection and private file modes.
Type-specific validators and binding functions supplied by layout/dashboard modules.
Do not clone the full repository. Extract common HTTP authentication middleware
without weakening Bearer scope/timeout/error semantics. No core/source changes.
Dashboard directory explicit,0700 single writer/0600 files; normal close/reopen
preserves history. No production database/crash recovery/downgrade guarantee.

UI save/new/list/explicit version open binds current authorized result, with view
staleness guards and errors scoped away from unrelated result. Imports with reordered
JSON keys must save successfully. Wrong binding/unsupported fields reject atomically.
Old version save conflicts. Lost save response prompts list reconciliation, no retry.
Logout clears server refs/list; query invalidation clears data. Same-binding metadata
can survive until a fresh authorized result. Don't claim dashboard publication just
because reusable repository internally supports release primitives; no dashboard
release HTTP routes/buttons in this slice. Existing report publication unaffected.

Public Store/HTTP and actual-loopback public DOM tests. Browser/visual/mobile/download
and PG rerun not qualified. Charts/multi-dataset/shared dashboards/PDF/production
remain future. Real source/enterprise identity integration last.
