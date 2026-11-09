# S11-A the private stores over a backend; the designs in the platform's database

2026-11-09. ADR 0017. The report layouts, dashboards, document templates, page backgrounds and images are kept by `privatedocs.Store`: one
JSON document per owner and store, until now one file of a private directory that one process locks. S11-A puts the store over a
`privatedocs.Backend` so that the document can live elsewhere, and gives the platform's database a backend. Nothing is wired to it yet (S11-B);
the behaviour of the directory store is unchanged.

A1 · `privatedocs.Backend`: `Load(owner)` (nil: no document), `Begin(owner)` → `Txn` (`Load`, `Store`, `Commit`, `Rollback`); one document is
at most 8 MiB (`MaxDocument`). The Store reads through `Load` and makes every change (`Save`, `Publish`, `SetCatalog`, `Opened`) one
transaction: begin, decode and validate, change, encode, store, commit. Three backends: the private directory (as before: one file per
owner, written to a temporary file and renamed, now at commit), memory (`NewMemoryBackend`, for tests and the demo), and the database.
`OpenBackend` opens a store over any of them; `Open` and `OpenCap` still open a directory.

A2 · The database: migration 011 `designs (store, tenant, issuer, subject, data bytea ≤ 8 MiB, updated_at)`, one row per store and owner,
row-level security as on jobs and assets (the identity policy and the worker policy, 007). `postgres.Store.Designs(store)` answers the
backend of one of the five stores: every read and change is a transaction of the owner's identity; a change inserts the row empty if there is
none and locks it (`FOR UPDATE`), so that two servers never interleave their changes of one owner's document. The application role inserts
and updates designs and never deletes them; the role check knows the table where it exists, the policy check from version 11. The fixture
reset empties it.

Tests: `privatedocs/backendtest`, the contract every backend keeps (no document, a committed change, a rolled back one, owners apart, the
bound, a whole store over the backend read again by another, and for memory and the database two writers that never lose a change), run on
the directory, memory and the database (`integration/designs_test.go`, with two stores on one database as two servers); the designs case of
the row-level security test (as the application role: no context no rows, only one's own rows, another identity's insert refused, no delete);
`integration/testdata/catalog_011.txt`, which differs from 010 by the migration alone; the tests that build a pre-ledger database drop the
table too.
