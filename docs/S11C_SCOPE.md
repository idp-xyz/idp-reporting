# S11-C importing a deployment's private directories into the store

2026-11-09. ADR 0017, after S11-B.

C1 · `reporting-migrate import-designs --issuer URL --bindings FILE [--layout-directory D] [--dashboard-directory D] [--template-directory D]
[--background-directory D] [--replace]`, the servers stopped. A private directory's file is named by the SHA-256 of its owner
(`privatedocs.DirectoryFileName`), so the identities come from the bindings file (each identity once, whatever its clients). The command opens
each directory as the server would (its lock), reads the document of every bound identity, holds it to its store's rules
(`privatedocs.CheckDocument` for every store; the layouts', dashboards' and templates' typed validation through a memory backend), and only
when every document passes writes them, one transaction per document as its owner (`postgres.ImportDesign`). A document already in the store is
kept unless `--replace`. The output counts imported, kept and files of no bound identity per store; it names no identity. The images are the
backgrounds directory's `images/`.

Tests: `cmd/reporting-migrate/importdesigns_test.go` (a first import, the stored bytes are the file's, a second import keeps, `--replace` writes,
a file of no bound identity is counted, a broken document stops the import with nothing written and is not named, the argument refusals).
Docs: OPERATIONS "Designs in the store", DEPLOYMENT "Several servers".
