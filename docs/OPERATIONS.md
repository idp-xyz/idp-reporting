# Operating the platform store (S04-A)

The platform store is a PostgreSQL database that holds only the platform's own assets: jobs, snapshots,
artifacts, issue records (with the numbers and references the source product gave: the platform numbers
nothing since 004), the schema ledger and the profile row. Source product data never lives in it. This page is the operator's reference for the schema lifecycle and the roles; it is not a
production admission (docs/s00/MIGRATION.md: that discussion follows S04).

## Schema lifecycle

The schema is a sequence of embedded migrations, `storage/postgres/migrations/NNN_name.{up,down}.sql`:

| Version | Name | Adds | Down discards |
|---|---|---|---|
| 001 | base | the schema, `profile`, `jobs`, `assets` (S01-A) | every row (refused unless accepted) |
| 002 | payload_digests | `data_sha256` on jobs and assets (S01-B) | nothing (the digests are derived) |
| 003 | issues | the issue kind on jobs and assets, `issue_numbers` (S03-G: the platform's own counters, version 3 only) | issue jobs, records and counters (refused unless accepted) |
| 004 | source_confirmed_issue | drops `issue_numbers`: the source product, not the platform, numbers every document (S03-H, docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md) | issue jobs and records (refused unless accepted: an S03-G binary could not read them), and recreates the empty `issue_numbers`; the up step is refused while issue jobs or records exist |

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
reporting-migrate down --to 3        # revert 004; refused while issue jobs or records exist
reporting-migrate down --to 2        # revert 004 and 003; refused while issue jobs, records or counters exist
reporting-migrate down --to 2 --accept-data-loss
```

Each step runs in its own transaction under a transaction-scoped advisory lock, so concurrent migrators
serialize and a failed step leaves the previous version in place. A ledger that names a version the binary
does not know, a checksum that differs from the embedded file, a gap, or a profile row at another version is a problem: `status` reports it,
`up` and `down` refuse, and the server refuses to open. A database made before the ledger existed is
bootstrapped from its profile version on the first `up` (those rows carry the checksum `legacy`).

Rollback is `down --to N`, newest step first; a target above the current version is refused. A down that
would discard rows is refused unless `--accept-data-loss` is passed; the message names what would go: 004
discards the issue jobs and records (an S03-H issue record carries the source product's string number and
references, which an S03-G binary could not read) and recreates the empty `issue_numbers`; 003 discards the
issue jobs, the records and the issue counters (the counters exist at version 3 only: S03-G's platform
numbering, after which the numbering of every tenant and integration restarts at 1, reusing numbers already
printed). After either, the issued PDFs stay in the store but can no longer be downloaded (a download needs the
record the artifact names, which is gone, and answers 409 INTEGRITY_FAILURE); they expire with their snapshots.
They are not plain artifacts: they keep their `Issue` field. 001 discards everything including the profile row (`init-profile` is needed
after the next `up`). The up step of 004 has the opposite precondition: it is refused, whatever the operator
accepts, while any issue job or issue record exists, because those are S03-G's documents, numbered by the
platform and never confirmed by a source product, and cannot become source-confirmed ones. The message names
the count and the remedy, `reporting-migrate down --to 2 --accept-data-loss`, which deletes them (the issued
PDFs stay in the store but can no longer be downloaded, their records being gone; they expire with their
snapshots); then `up` applies 003 and 004. A store that never issued under S03-G migrates
without ceremony. Qualification: a fresh database taken up to the latest, down to zero and up again has a
catalog (columns, types, defaults, nullability, constraints, indexes) identical to the first, and 004 down gives
back exactly the catalog of version 3 (integration/migrate_test.go, integration/testdata/catalog_00{3,4}.txt).

## Roles

Two roles, created by the DBA with their own login and password policy:

- the migrator role owns the schema and every table and runs `up`, `down` and `roles`; it needs
  `GRANT CREATE ON DATABASE <db> TO <migrator>` for the very first `up` (it creates the schema), nothing after;
- the application role is what the server connects as: USAGE on the schema, SELECT on every table, INSERT
  and UPDATE on the data tables (`jobs`, `assets`, and S03-G's `issue_numbers` where the schema is at
  version 3: 004 drops it, and a store at the latest version is not missing it), nothing else: no DELETE, no TRUNCATE,
  no DDL, no CREATE on the schema, no ownership, not a superuser, no role or database creation, no RLS
  bypass, no replication, a member of no role (a membership is a `SET ROLE` away from more), no `SET ROLE`
  in effect, not the owner of the database and without CREATE on it (an owner can drop the database and set
  its parameters), no column-level grants anywhere in the schema. The ledger and the profile it only reads.
  Create the database as the DBA or the migrator, never as the application role.

`reporting-migrate roles` sets ownership and grants (idempotent) on the tables that exist; the migrator's
default privileges give the application role SELECT on tables created later, never INSERT or UPDATE. So
run `roles` after every `up` or `down` that creates a data table, as whoever ran the step (only the owner
of a table, or a superuser, can hand it over): until then the server's role check names the table and
refuses to start (`status` shows the same). `roles` refuses a schema that
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

## Workers and issue jobs

A worker is a process that calls `Service.RunOne` in a loop (the demo runs one). Workers that share a store claim
jobs from it by the database's clock, one lease at a time, and a claim is not filtered by what the worker can do:
every worker sharing a store must therefore run with the same capabilities (the issuer, the document authority of
each integration's adapter and the PDF renderer). A worker without one claims an issue job it cannot render and
fails it (UNSUPPORTED_COMBINATION), possibly after another worker's earlier attempt had reserved a number for it,
which is then voided.

Issue by group (S03-K, docs/API.md) adds no migration: a group issue job and its record carry the group in their JSON
payloads. It does add a rolling constraint. A group job is asked of an adapter that declares contract version 2
(`IssueContractVersion() == 2`) and fails with UNSUPPORTED_COMBINATION, asking the product nothing, on a worker whose
adapter does not; and a binary older than S03-K reads a group job as corrupt (its request hash is not a row's): its reads of
that job fail with INTEGRITY_FAILURE and its claim fails the job for good (see "A job that cannot be read" below), so an
older worker that meets a group job destroys it (before, the job stayed claimable for a newer worker). Roll the platform's binaries together:
stop the older workers before a group job can be admitted, and do not run an older worker against a store that holds group
jobs. Real products implement version 2 when they are integrated; until then a 2.0 template is issued only by the
contract simulators.

The failure of an issue job (S03-H, docs/API.md) is recorded before the source product is told, in this order:

1. the job is failed as `ISSUE_UNRECONCILED`, fenced by the worker's claim (running, the claim's generation, a
   lease that has not run out by the database's clock);
2. only if that took, the product is told to void whatever it holds for the job id, in a context of its own of at
   most 5 seconds;
3. when the void succeeded, the job's own code replaces `ISSUE_UNRECONCILED`.

If the failure cannot be recorded (the lease is lost, or the store cannot be reached) nothing is voided: the job is
left to its lease and the next claim, of a later generation, voids it. A void that failed, and a worker that died
between steps 1 and 3, leave the job `ISSUE_UNRECONCILED`: the product may still hold a reservation, which it finds
by the job id; reconciling it is the product's operator's work (retries of a failed void are not in the platform
yet). A job that failed for another reason shows its own code, and its number, if the product had reserved one, is
voided there. A job claimed again never asks the product for a new number: the product answers the same job id with
the same reservation.

### A job that cannot be read

A claim can meet a stored job whose payload it cannot decode: the row is corrupt (the payload does not match its digest, or
disagrees with the row's own columns), or it was written by a binary that knows more than this one (the rolling constraint
above). Such a job is never handed to a worker and never left running, which used to make every later claim, after its
lease, take it again at a higher generation, forever. In the transaction of its claim (the memory store: in the same step) it
is failed for good, and the claim goes on to the next job, so that a claim that returns nothing still means that nothing is
claimable and the jobs behind the unreadable one are not delayed.

- The code is `INTEGRITY_FAILURE`, except for an issue job that had been claimed before (its generation before this claim was at
  least 1): an earlier attempt may have reserved a number at the source product, which cannot be voided because the job cannot
  be read, so it is failed `ISSUE_UNRECONCILED` and the product finds the reservation by the job id (reconcile it as above). An
  issue job that was never claimed (generation 0) cannot have reached the product.
- The decision uses the row's own `kind` and `generation` columns, never the payload that could not be read, and nothing is
  logged: no value may reach a log, and the store has no logger. The record is the row (`state = 'failed'`, `error_code`,
  `generation` one above what it was); for example `SELECT id, kind, generation, error_code FROM reporting_s01.jobs WHERE
  state = 'failed' AND error_code IN ('INTEGRITY_FAILURE', 'ISSUE_UNRECONCILED')` (a job that failed its execution with the
  same code is listed too).
- The owner reads such a job as `409 INTEGRITY_FAILURE` (it cannot be read), as before, and an unreadable issue job also fails
  the owner's issue listing. A failed job is final: resubmit a snapshot or an export under a new idempotency key; do not
  resubmit an issue job that is `ISSUE_UNRECONCILED` before the product's operator has reconciled its reservation.

## Barcodes, images and the checker (S03-M)

Schema 2.1 (docs/API.md, ADR 0005) adds a third-party library to the formal render path and a decoder to the checker.

- `github.com/boombuler/barcode` v1.1.0 (MIT) encodes Code 128 and QR symbols behind `export/barcode`, the only package that imports it. It is
  pinned by go.mod and go.sum (CI runs `go mod verify`) and is reviewed at every upgrade: `export/barcode`'s tests pin what the wrapper relies on
  (the encoder accepts control characters, function codes and an empty QR payload, which the platform refuses before calling it; it quotes the value
  in its own errors, so its errors are never passed on; its Code 128 output is checked against the standard's patterns, and QR against the standard's
  capacities and format words). An upgrade that changes what these tests record must be reviewed before it is merged. The encoders keep no state between
  calls and are safe to share.
- Images live in the `images` directory of the background directory (`-background-directory`, mode 0700, one writer, as the backgrounds): there is no
  deletion, as for backgrounds, and no new flag. A directory that held only backgrounds gains the subdirectory when the binary starts. The directory is
  per identity, up to 16 images per dataset and, as the shared store, 8 MiB per identity; images are content of the templates that name them, so back
  up the directory with the template directory. The service draws a probe page with every upload when it has the renderer.
- The PDF checker (`scripts/check_pdf_export.py`, CI's pdf job) decodes every code with zxing-cpp 3.1.1 (Apache-2.0), pinned with its hash in
  `scripts/requirements-pdfcheck.txt` (the wheel for CPython 3.12 on x86-64 Linux; no other package is needed). A host that runs the checker
  needs it: `pip install --require-hashes --only-binary :all: --no-deps -r scripts/requirements-pdfcheck.txt`.
- Rolling: a schema 2.1 template needs the binary of this slice to be previewed, checked and issued (an older binary cannot read the template);
  1.x and 2.0 templates and their files are unchanged. No SQL migration.

## Embedding origins (S03-N)

Embedding (docs/EMBEDDING.md, ADR 0006) lets a product frame the console's views, for its own user, from a listed origin. Which origins may frame is
trusted server configuration:

- The demo: repeatable `-embed-origin integration=origin`, for example
  `-embed-origin brant-contract-demo=https://app.brant.example -embed-origin brant-contract-demo=http://127.0.0.1:7822`.
  Origins are exact `https://host[:port]` (lower case, ASCII, no default port, no path, query, fragment, user information, wildcard or list);
  `http://` is accepted for a loopback IP address only (the demo allows it; `embedding.Options.LoopbackHTTP` is the switch for a service). The
  integration must be one the deployment has. The configuration is validated before anything else is opened: a malformed origin, an unknown
  integration or a duplicate stops the start with a message naming the flag. Without the flag the embed routes exist and answer 403 to everyone.
- A service: `embedding.New(map[string][]string, embedding.Options)` (at most 32 integrations of 16 origins), given to `console.HandlerWithEmbedding`
  (the pages) and `httpapi.WithEmbedding` (the `embedding` flag of `GET /capabilities`; skip it when nothing is configured, the flag then says false).
  Neither reads configuration from a request. Changing the list needs a restart (it is read once).
- The product page and the platform need no CORS and no cookies: the page calls the platform's API from the platform's own origin with the
  bearer token the product page gives it by `postMessage`. Serve the platform over HTTPS in a real deployment: the product page's origin is
  https, and the frame needs a secure context for Web Crypto (the digest check of downloads).
- A product page that sandboxes the frame needs `allow-scripts allow-same-origin allow-downloads`. A reverse proxy in front of the platform must
  pass the `Content-Security-Policy` and `X-Frame-Options` headers of the answers unchanged: the embed page's has `frame-ancestors <origin>` and no
  `X-Frame-Options`, every other answer has `frame-ancestors 'none'` and `X-Frame-Options: DENY`.
- The browser evidence is local: `scripts/browser_embed_s03n.mjs` (header of the script: the demo with `-embed-origin`, two static servers of
  `console/embed-host/` on a listed and an unlisted loopback port, and a headless Chrome).

## Demo and tests

`reporting-demo -initialize-fixture` applies the migrations and inserts the fixture profile through the
same migrator. When a fixture store cannot be opened the demo prints the platform's own refusals (a migration's
precondition with its remedy, a schema older than the binary's, a database that is not a fixture one: they name the
reason and carry nothing of the connection) and nothing else of the error: a connection failure, which may carry the
connection's details, is reduced to a generic text. The CI postgres job and the local throwaway server run `TestPostgresMigrations` and
`TestMigrateCommand` with roles they create (`reporting_app_t`, `reporting_migrator_t`, the attribute,
membership and command-test roles): the application role runs every write the server performs through the
production path (with the test-only option that lifts TLS and the fixture name), the migrator role runs the
whole lifecycle with and without CREATE on the database, and every forbidden statement is attempted and
refused. The two test binaries share the fixture database, so the job runs packages one at a time.

Not covered yet: row-level security (one application role for all tenants; isolation is the service's),
retention and cleanup, object storage, backups and restore, capacity, key lifecycle.
