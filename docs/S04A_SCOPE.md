# S04-A production storage foundation: versioned migrations, rollback, least privilege

2026-10-28. First slice of S04 (docs/s00/MIGRATION.md: production migration/upgrade, least privilege, cleanup,
quotas, capacity, backup and recovery; production admission is discussed only after S04). Until now the
platform store was a guarded test fixture: `fixture.sql` plus an ad-hoc v1→v2→v3 upgrade, superuser
connections, loopback and fixture database names only, `TRUNCATE` for resets. This slice gives the store a
real schema lifecycle and a production opening path without claiming production admission: the demo server
keeps the contract-simulator profile; what is delivered is the mechanism and its qualification.

A1 · Versioned migrations with a ledger. The schema lives in embedded, numbered migration pairs
`storage/postgres/migrations/NNN_name.{up,down}.sql`: 001 the base schema of S01-A (jobs, assets, profile),
002 the payload digests of S01-B, 003 the issue kind, issue records and issue numbers of S03-G; they
reproduce the schema the fixture reached, column for column and constraint for constraint. A ledger
`reporting_s01.schema_migrations(version, name, checksum, applied_at, applied_by)` records every applied
step with the SHA-256 of its up-file; the `profile` row's version stays equal to the highest applied
migration, so every existing check keeps working. The migrator (`storage/postgres` package: Status, Plan,
Up, Down) applies one step per transaction under a transaction-scoped advisory lock, so that concurrent
migrators serialize and a failed step leaves the previous version intact; it refuses a database whose ledger
names a version the binary does not know or whose recorded checksum differs from the embedded file; a
database that predates the ledger (a `profile` row only) is bootstrapped from the profile's version. Opening
the store requires the ledger at the binary's latest version with matching checksums.

A2 · Rollback. Every migration has a down step that is the exact inverse of its up step, qualified by a
round trip: up to the latest, snapshot the catalog (tables, columns, types, defaults, nullability,
constraints, indexes), down to zero, up again, and the catalog is identical (001 down keeps the empty
schema, whose ownership is the DBA's). A down step that would discard
rows (003 with issue jobs, records, issued PDFs or issue counters present, as the numbering would restart;
001 with any row, the profile row included) is refused unless the operator passes
`--accept-data-loss`; 002's down only drops derivable digest columns and is always allowed. Down runs one
step per transaction, to an explicit target version.

A3 · Least privilege. An operator step (`reporting-migrate roles`) sets the ownership and the grants of two
roles the DBA created: the migrator role that owns the schema and runs the migrations (it needs CREATE on
the database for the very first `up`, which creates the schema), and the application role that can only read and
write rows (USAGE on the schema; SELECT on every table, INSERT and UPDATE on the data tables; no DELETE, no
TRUNCATE, no DDL, no CREATE on the schema, no ownership, not a superuser, no role or database creation, no
row-security bypass, no replication, a member of no role, no SET ROLE in effect, not the database's owner
and without CREATE on it, no column-level grants). The production opening path
(`postgres.OpenProduction`) verifies the connected role against these rules at startup and refuses to run
otherwise; the guarded fixture path keeps its superuser connections for the destructive test resets, which
exist only there. Every write the server performs (freeze, export, download, issue, cancel) runs as the
application role on the fixture through the production path, and the role's missing privileges are proven
by attempting them; the full contract suites keep running through the fixture path.

A4 · Production opening path and the migrate command. `OpenProduction` accepts any database name except the
fixture names, requires `sslmode=verify-full` (a test-only option disables that check explicitly and is named
as such), requires the `production` profile row, the ledger at the binary's version and the role rules
above. `cmd/reporting-migrate` is the operator tool: `status`, `plan`, `up [--to N]`, `down --to N
[--accept-data-loss]`, `roles --app-role NAME --migrator-role NAME`, `init-profile production|contract-simulator-only`, on
the DSN in `REPORTING_STORE_DSN`; it never touches source product databases and never runs inside the
server. The demo server's `-initialize-fixture` applies the migrations through the same migrator.

Not in this slice: row-level security (the application role is one role for all tenants; tenant isolation
stays in the service and is listed for a later S04 slice with the per-request session variable it needs),
object storage for artifacts, retention and cleanup of expired assets, quotas, backup and restore
qualification, capacity measurements, key lifecycle, production admission.
