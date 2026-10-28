# Operating the platform store (S04-A)

The platform store is a PostgreSQL database that holds only the platform's own assets: jobs, snapshots,
artifacts, issue records and numbers, the schema ledger and the profile row. Source product data never
lives in it. This page is the operator's reference for the schema lifecycle and the roles; it is not a
production admission (docs/s00/MIGRATION.md: that discussion follows S04).

## Schema lifecycle

The schema is a sequence of embedded migrations, `storage/postgres/migrations/NNN_name.{up,down}.sql`:

| Version | Name | Adds | Down discards |
|---|---|---|---|
| 001 | base | the schema, `profile`, `jobs`, `assets` (S01-A) | every row (refused unless accepted) |
| 002 | payload_digests | `data_sha256` on jobs and assets (S01-B) | nothing (the digests are derived) |
| 003 | issues | the issue kind on jobs and assets, `issue_numbers` (S03-G) | issue jobs and records (refused unless accepted) |

The ledger `reporting_s01.schema_migrations` records every applied step with the SHA-256 of its up file,
the time and who applied it. The `profile` row names the deployment kind (`production` or
`contract-simulator-only`) and carries the highest applied version. A server opens the store only when
the ledger is at the binary's latest version with the embedded checksums, and the profile row matches.

`reporting-migrate` (built from `cmd/reporting-migrate`) is the operator tool. It reads the store from
`REPORTING_STORE_DSN`; `up`, `down`, `roles` and `init-profile` must run as the migrator role (or an owner):

```bash
export REPORTING_STORE_DSN='postgresql://reporting_migrator@db.example:5432/reporting?sslmode=verify-full'
reporting-migrate status             # embedded steps, applied steps, pending steps, problems, role check
reporting-migrate plan               # what up would do (--to N for a target)
reporting-migrate up                 # apply the pending steps, one transaction each
reporting-migrate init-profile production   # once, after the first up
reporting-migrate roles --migrator-role reporting_migrator --app-role reporting_app
reporting-migrate down --to 2        # revert 003; refused while issue rows, issued PDFs or issue counters exist
reporting-migrate down --to 2 --accept-data-loss
```

Each step runs in its own transaction under a transaction-scoped advisory lock, so concurrent migrators
serialize and a failed step leaves the previous version in place. A ledger that names a version the binary
does not know, a checksum that differs from the embedded file, a gap, or a profile row at another version is a problem: `status` reports it,
`up` and `down` refuse, and the server refuses to open. A database made before the ledger existed is
bootstrapped from its profile version on the first `up` (those rows carry the checksum `legacy`).

Rollback is `down --to N`, newest step first; a target above the current version is refused. A down that
would discard rows is refused unless `--accept-data-loss` is passed; the message names what would go: 003
discards the issue jobs, records, issued PDFs and the issue counters (after which the numbering of every
tenant and integration restarts at 1, reusing numbers already printed), 001 discards everything including
the profile row (`init-profile` is needed after the next `up`). Qualification: a fresh database taken up
to the latest, down to zero and up again has a catalog (columns, types, defaults, nullability, constraints,
indexes) identical to the first (integration/migrate_test.go).

## Roles

Two roles, created by the DBA with their own login and password policy:

- the migrator role owns the schema and every table and runs `up`, `down` and `roles`; it needs
  `GRANT CREATE ON DATABASE <db> TO <migrator>` for the very first `up` (it creates the schema), nothing after;
- the application role is what the server connects as: USAGE on the schema, SELECT on every table, INSERT
  and UPDATE on the data tables (`jobs`, `assets`, `issue_numbers`), nothing else: no DELETE, no TRUNCATE,
  no DDL, no CREATE on the schema, no ownership, not a superuser, no role or database creation, no RLS
  bypass. The ledger and the profile it only reads.

`reporting-migrate roles` sets ownership and grants (idempotent) on the tables that exist; the migrator's
default privileges give the application role SELECT on tables created later, never INSERT or UPDATE. So
run `roles` after every `up` or `down` that creates a data table, whoever ran it: until then the server's
role check names the table and refuses to start (`status` shows the same). `roles` refuses a schema that
holds a table it does not know, and every identifier it executes is quoted.

Trust boundary: the ledger is written by the migrator role (or an owner, or a superuser). Such a role can
also rewrite it, drop it or mark rows `legacy`; the checksums detect a changed migration file or a
mismatched database, not a privileged writer. A production store is never bootstrapped: a `legacy` row
there is refused. A rollback that discards rows is final; `001 down` leaves the empty schema in place.
`sslmode=verify-full` verifies the server against the system trust store unless `sslrootcert` names a CA.

The production opening path (`postgres.OpenProduction`) verifies at startup: `sslmode=verify-full`, a
database that is not a fixture one, the `production` profile row, the ledger at the binary's version, and
the connected role against every rule above; any failure is a refusal to start. The guarded fixture path
(`postgres.Open`, fixture database names on a loopback address, the contract-simulator profile) keeps the
superuser connections the destructive test resets need; a production store cannot be reset.

## Demo and tests

`reporting-demo -initialize-fixture` applies the migrations and inserts the fixture profile through the
same migrator. The CI postgres job and the local throwaway server run `TestPostgresMigrations` with two
roles it creates (`reporting_app_t`, `reporting_migrator_t`): the application role runs the platform's
whole contract through the production path (with the test-only option that lifts TLS and the fixture
name), and every forbidden statement is attempted and refused.

Not covered yet: row-level security (one application role for all tenants; isolation is the service's),
retention and cleanup, object storage, backups and restore, capacity, key lifecycle.
