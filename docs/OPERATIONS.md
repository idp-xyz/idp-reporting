# Operating the platform store (S04-A)

The platform store is a PostgreSQL database that holds only the platform's own assets: jobs, snapshots,
artifacts, issue records (with the numbers and references the source product gave: the platform numbers
nothing since 004), the schema ledger and the profile row. Source product data never lives in it. This page is the operator's reference for the schema lifecycle and the roles; it is not a
production admission (docs/s00/MIGRATION.md: that discussion follows S04).

**Starting the server:** `reporting-server run -config FILE` (S05-A) is the production command; its configuration, secrets, endpoints, reload, shutdown, systemd unit, upgrade procedure and go-live checklist are in docs/DEPLOYMENT.md. Every place below that says "start the server" means that command (the demo is the contract-simulator fixture and is not a deployment).

## Schema lifecycle

The schema is a sequence of embedded migrations, `storage/postgres/migrations/NNN_name.{up,down}.sql`:

| Version | Name | Adds | Down discards |
|---|---|---|---|
| 001 | base | the schema, `profile`, `jobs`, `assets` (S01-A) | every row (refused unless accepted) |
| 002 | payload_digests | `data_sha256` on jobs and assets (S01-B) | nothing (the digests are derived) |
| 003 | issues | the issue kind on jobs and assets, `issue_numbers` (S03-G: the platform's own counters, version 3 only) | issue jobs, records and counters (refused unless accepted) |
| 004 | source_confirmed_issue | drops `issue_numbers`: the source product, not the platform, numbers every document (S03-H, docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md) | issue jobs and records (refused unless accepted: an S03-G binary could not read them), and recreates the empty `issue_numbers`; the up step is refused while issue jobs or records exist |
| 005 | retention | `assets.expires_at` and its index (S04-B, "Retention") | nothing (the payload keeps the expiry) |
| 006 | sweeps | the `sweeps` ledger (S04-B) | the ledger (refused while it has rows unless accepted) |
| 007 | row_level_security | row-level security and the `identity` and `worker` policies on `jobs` and `assets` (S04-C, "Row-level security") | nothing (policies only) |
| 008 | object_storage | the `objects` and `orphans` counts of the `sweeps` ledger (S04-E, "Object storage") | the two counts, and it is refused while a ledger row says objects were deleted or an artifact's file is an object, unless the loss is accepted |
| 009 | capacity_indexes | four indexes: `jobs_identity_created`, `jobs_identity_in_flight`, `assets_identity_kind_expires`, `jobs_reducible` (S04-G, "Capacity") | nothing (an index is derived from the rows) |
| 010 | jobs_identity_kind_stats | extended statistics `jobs_identity_kind_stats` on `jobs (tenant, issuer, subject, kind)` and an `ANALYZE`, so that the issue listing of an identity with a long issue history reads `jobs_identity_created` ("Capacity", docs/CAPACITY.md "Migration 010") | nothing (statistics are derived from the rows) |

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
back exactly the catalog of version 3 (integration/migrate_test.go, integration/testdata/catalog_00{3,4,5,6,7,8,9}.txt).

## Roles

Two roles, created by the DBA with their own login and password policy, and an optional third for the retention sweep (S04-B, below):

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
  Both it and the sweep role are subject to the row-level policies of 007 ("Row-level security", below): they see and write the rows of
  the identity a transaction says it works on, and of every identity only in a worker's transaction.

- the sweep role (optional, `roles --sweep-role NAME`) is what the scheduled retention sweep connects as: USAGE on
  the schema, SELECT on every table, UPDATE and DELETE on `assets` (UPDATE because PostgreSQL requires it for the row
  locks the sweep takes), UPDATE on `jobs` (it reduces finished payloads), INSERT on the `sweeps` ledger, and every
  other rule of the application role (no TRUNCATE, no DDL, no ownership, no attributes, no memberships). It is the
  one role that deletes. Without it the migrator runs the sweep; the server's role never can. `status` reports the
  connected role against both rule sets (`role:` and `sweeper:`).

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

A worker is a process that calls `Service.RunOne` in a loop (the demo runs one; `reporting-server` runs `workers` of them, default 2, and stops them at a drain after the job in hand, docs/DEPLOYMENT.md "Shutdown"). Workers that share a store claim
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
- **Templates per identity (S06-B).** An identity keeps at most **64** private document templates (16 until S06-B, so that the 37 samples can be copied; `doctemplates.MaxTemplates`);
  the layouts, dashboards and images keep the 16 they had. A save or a sample copy beyond it is 413 `BUDGET_EXCEEDED`. The cap is the private store's and is not one of
  the platform's quotas (those bound jobs and stored results). The file of an identity is still bounded by the store's 8 MiB, which a full 64 templates of many large versions can
  reach before the count does: that too is 413. A store is never opened with a lower cap than it was written with (the file would read as corrupt), so the cap only rises.
  The per-template catalogue metadata (favourite, archive, collections, last opened) lives in the same file and is backed up with it; `last_opened` is written on each read of a version.
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

## Retention (S04-B)

What the platform stores of a result lives as long as the source read it was made from, plus a grace period, and is
then deleted (docs/S04B_SCOPE.md). The rules, each one in the SQL of the batch that acts on it and each one proved by a
test that fails if the sweep breaks it:

- A snapshot (the frozen business rows) is deleted once its expiry (`assets.expires_at`, the source read's expiry
  that every download and page already enforces) is older than the grace period by the database's clock
  (`clock_timestamp()`), never the platform host's. An artifact (XLSX, PDF, an issued PDF) is deleted by the same
  rule, and never before its snapshot: while the snapshot is held for a later expiry, its artifacts stay.
- An issue record (`kind='issue'`) is never deleted: it is the permanent record of what was issued (ADR 0003), its
  `expires_at` is NULL, and the delete statements name the kinds they delete. A row whose expiry could not be read
  (NULL) is never deleted either.
- Nothing a queued or running job names is deleted: not the snapshot it reads from, not an artifact of that snapshot.
- A finished job keeps its row (state, code, ids, times: the audit of what happened). Once its own expiry plus the grace
  is past and neither its snapshot nor its artifact is stored any more, its payload is reduced: the frozen query, the
  prepared source read and the columns are cleared, the request hash and the rest stay, the payload is marked
  `Reduced` and its digest recomputed (only a payload that is a JSON object whose digest matches is rewritten).
  The frozen template of an issue job stays: it carries no business data.
- The private stores on disk (layouts, dashboards, templates, backgrounds, images) are configuration, not results,
  and are not swept.

The sweep (`postgres.Sweep`, `Service.Sweep`) runs artifacts, then snapshots, then job payloads, in batches of at
most `--batch` rows (default 500, at most 10000), each batch one transaction with `FOR UPDATE SKIP LOCKED` and a lock
timeout of 10 s: a failed batch changes nothing, concurrent sweeps take different rows, a row a worker, a download or
a cancel holds is left for the next sweep, and it takes no advisory lock, so it runs beside the workers and refuses
to wait on a migration. The grace (`--grace`, default 24 h, at most ten years) is a retention period: zero is read as
the default by the service and refused by the command, and a negative or oversized value is refused, so that a
slip can only keep data longer, never delete it sooner. The sweep refuses a store whose ledger is not at the binary's
version and a role that lacks what it needs, before anything is changed. It is idempotent and returns counts only. `reporting-server` has the same loop (`retention.sweep_interval`, off by default), but it connects as the application role, which never deletes, so there the sweep is refused and logged by its code: schedule `reporting-migrate sweep` (docs/DEPLOYMENT.md "The sweep").

With an object store configured (S04-E, "Object storage" below) the artifact step also deletes, after each batch commits, the objects the deleted rows
named, and a fourth step reconciles the bucket; the counts above gain `objects` and `orphans`.

Running it: `reporting-migrate sweep [--grace D] [--batch N] [--dry-run]` on `REPORTING_STORE_DSN`, as the sweep
role or the migrator (the application role has no DELETE and is refused), prints `swept: A artifacts, S snapshots,
J job payloads reduced (grace, batch, T transactions)` (or `dry run, would sweep: ...`) and exits 0, or prints how far
it got and exits non-zero. Schedule it (cron, a timer) from a host that holds the sweep role's credentials and never
the migrator's. The demo runs one in its worker loop every `-sweep-interval` (default 10 minutes, `0` disables it,
not run with `-pause-workers`) with the default grace and batch, through the fixture's own connection, and logs
counts only.

Every sweep, dry runs included and cut-short ones too, writes one row to the `sweeps` ledger (migration 006): when,
dry run or not, complete or not, grace, batch and the four counts. No id of a business resource, no value. `006
down` drops the ledger and is refused while it has rows unless `--accept-data-loss` is given. Migration 005 adds
`assets.expires_at` and its index, backfilled from the payload's `ExpiresAt` (cut to microseconds, as the driver
writes it; the digest is over the payload only, so nothing is re-signed); a payload with no readable expiry gets
NULL and is never deleted; the step never refuses to run. `005 down` drops the column and the index: the payload still
holds the expiry, nothing is lost. The store checks the column against the payload whenever it reads a snapshot or an
artifact (an issue record must have NULL), so the two cannot drift apart unnoticed.

Verifying, per kind (as any role that reads the schema; counts, no values):

```sql
-- artifacts and snapshots past their grace that are still held: after a sweep only those a queued or running job
-- names, those whose snapshot is held for a later expiry, and those with no readable expiry
SELECT kind, count(*) FROM reporting_s01.assets
 WHERE kind IN ('artifact','snapshot') AND expires_at < clock_timestamp() - interval '24 hours' GROUP BY kind;
-- issue records: never fewer than before, never with an expiry
SELECT count(*), count(expires_at) FROM reporting_s01.assets WHERE kind='issue';
-- finished jobs whose payload was reduced, and finished jobs past their grace that still hold a frozen read
SELECT count(*) FILTER (WHERE data ? 'Reduced'), count(*) FILTER (WHERE NOT data ? 'Reduced' AND source_expires_at < clock_timestamp() - interval '24 hours')
  FROM reporting_s01.jobs WHERE state IN ('succeeded','failed','cancelled');
-- the ledger
SELECT at, dry_run, complete, grace_seconds, batch, artifacts, snapshots, jobs, batches FROM reporting_s01.sweeps ORDER BY at DESC LIMIT 20;
```

What the views say after a sweep is in docs/API.md ("Retention"): a job keeps its state and ids and says `expired`,
an issue keeps its record, and what is gone is 404, never a server error or an integrity failure.

Not in this slice: object storage for artifacts, quotas (S04-D), row-level security, backups, deleting private configuration
stores, legal holds.

## Row-level security (S04-C)

Until S04-C the isolation of tenants was the service's alone: every store query names the owner (tenant, issuer,
subject) in its predicate, and the application role is one role for every tenant. Migration 007 `row_level_security`
(docs/S04C_SCOPE.md) adds the database's own refusal as defence in depth: a missing predicate in a future query, or a
connection that forgot to say whose rows it works on, leaks nothing. **No context, no rows.**

What it protects: the business rows, in `jobs` and `assets`, against the platform's own mistakes (a query written
without its identity predicate, a code path that reuses a pooled connection, an UPDATE that would hand a row to another
identity). What it does not protect against: a compromised server process, which can set any context itself. Against
that stand the roles of S04-A (no DELETE, no DDL, no bypass) and the real identity provider; this is not a defence
against an attacker holding the application role's credentials.

How it works. Row-level security is `ENABLE`d, not `FORCE`d, on `jobs` and `assets`, with two permissive policies on each,
for every command, to PUBLIC (USING and WITH CHECK are the same expression):

```sql
CREATE POLICY identity ON reporting_s01.<jobs|assets> AS PERMISSIVE FOR ALL TO PUBLIC
  USING      (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))
  WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''));
CREATE POLICY worker ON reporting_s01.<jobs|assets> AS PERMISSIVE FOR ALL TO PUBLIC
  USING      (current_setting('reporting.scope', true) = 'worker')
  WITH CHECK (current_setting('reporting.scope', true) = 'worker');
```

A setting that is missing or empty makes the comparison NULL: no row is visible and none may be written. (A pooled
connection that ran a transaction with the settings shows the empty string, not NULL, afterwards; that is what the
`nullif` is for.) A write under a wrong context is refused by the database, SQLSTATE 42501 "new row violates row-level
security policy", not by Go; an UPDATE or DELETE of a row the context cannot see touches no row.

The settings, set by the store and by nothing else:

- `reporting.tenant`, `reporting.issuer`, `reporting.subject`: every identity-scoped call (`Admit`, `Job`, `Cancel`,
  `Snapshot`, `Artifact`, `Issue`, `Issues`) runs in one transaction that first sets the three with
  `set_config(name, $n, true)` and then runs its statements. The values are parameters, never interpolated.
- `reporting.scope = 'worker'`: every worker call (`Claim`, `Publish`, `Fail`, `ResolveFailure`) and every batch of the
  sweep, dry runs included, sets it the same way. A worker works on the rows of every identity: `Claim` takes the
  oldest job of any, `Publish` writes the assets under the job's owner, the sweep deletes across identities.

The context is transaction-local only: it ends with the transaction, so a pooled connection carries none into the next
call. `set_config(..., false)`, `SET` and `SET SESSION` do not appear in `storage/postgres` (a source test holds it to
that, and to every statement of the store going through a context transaction). The memory store is unchanged.

Which roles are subject to it. The application role and the sweep role: neither has BYPASSRLS (S04-A already refuses the
attribute). The migrator owns the tables and, because the policies are not `FORCE`d, bypasses them, so migrations and
their data checks (what a down step would discard) see every row; run `up`, `down` and `roles` as the migrator or the
owner, never as a role that is not the owner. A superuser bypasses too. **The fixture path is a superuser connection**
(`postgres.Open` against the throwaway fixture databases, the demo, the test suites that use it): the policies do not
apply there, and nothing about tenant isolation is proved by a run on the fixture path; the PostgreSQL qualification
proves it as the application role and the sweep role through the production path. `roles` grants nothing new: policies
need no grant.

What the server checks. `RoleProblems` and `SweeperProblems` (so `OpenProduction`, which refuses to start, and
`reporting-migrate status`) report, for a store at version 7 or later, a problem that names the table when row-level
security is not enabled on `jobs` or `assets`, when one of the two policies is missing, when a policy does not have the
qualification above (compared as the database prints it, ignoring spaces, parentheses and casts), is not permissive for
all commands to PUBLIC, or when another policy exists on those tables. An administrator's `DISABLE ROW LEVEL SECURITY`
or a dropped policy leaves the schema ledger intact, so the ledger alone would not see it.

Reverting: `reporting-migrate down --to 6` drops the policies and disables row-level security; no row is touched and no
acceptance is needed. A store at version 6 upgrades with its rows in place; the application role then sees none of them
until the new binary sets its context, so upgrade the binary and the schema together (the old binary cannot run on 007).

Verifying (as any role that reads the catalog):

```sql
-- row-level security on the two tables with business rows, and not on the others
SELECT relname, relrowsecurity, relforcerowsecurity FROM pg_class
 WHERE relnamespace = 'reporting_s01'::regnamespace AND relkind = 'r' ORDER BY relname;
-- the policies: two per table, ALL, PERMISSIVE, {public}
SELECT tablename, policyname, permissive, roles, cmd, qual, with_check FROM pg_policies WHERE schemaname = 'reporting_s01' ORDER BY 1, 2;
-- what a connection with no context sees (the application role): 0 and 0
SELECT (SELECT count(*) FROM reporting_s01.jobs), (SELECT count(*) FROM reporting_s01.assets);
```

Not in this slice: one role or connection per tenant, policies on the ledgers (`schema_migrations`, `sweeps`), the
profile and `issue_numbers` (they carry no business rows), object storage, quotas (S04-D), backups and restore, capacity, key
lifecycle.

## Quotas (S04-D)

Admission asks the source product nothing about volume, so before S04-D any admitted principal could queue jobs, hold results
until they expired, and submit as fast as it liked (N10 of docs/CODE_REVIEW_S03H.md). Each identity (tenant, issuer, subject; the
integration is not part of it, so an identity's two products share its quotas) is now bounded at the place where admission happens
(docs/S04D_SCOPE.md).

What is counted, per identity, at the moment of an admission:

| Quota | What it counts | Default | Bounds |
|---|---|---|---|
| `jobs_in_flight` | jobs of every kind (snapshot, export, issue) in state `queued` or `running` | 8 | 1..1000 |
| `stored_results` | snapshots and artifacts the identity owns whose expiry is still in the future by the store's clock | 64 | 1..100000 |
| `admissions_per_hour` | jobs admitted in the last hour by the store's clock, whatever became of them | 120 | 1..100000 |

Rules that follow from it:

- A job that is `succeeded`, `failed` or `cancelled` frees its in-flight slot. It still counts toward `admissions_per_hour` for the
  hour after its admission.
- An expired snapshot or artifact does not count: it waits for the retention sweep (docs/OPERATIONS.md "Retention") and the sweep
  deleting it changes nothing about the count. A row whose expiry could not be read (NULL, which the sweep never deletes) is not
  "in the future" and does not count. An issue record never counts: it is permanent and is not a result in this sense (the issued
  document's artifact is, until it expires). A stored result bounds bytes too, as each is already bounded by `MaxResultBytes` and `MaxArtifactBytes`.
- A replay of an existing Idempotency-Key is never refused by a quota: the job exists and the answer is its view. A replay with a
  changed request is still `IDEMPOTENCY_CONFLICT`, not a quota refusal.
- A refusal admits nothing and changes nothing. The check order, when more than one quota is reached, is `jobs_in_flight`,
  `stored_results`, `admissions_per_hour`; the answer names the first.
- Counts use the store's clock: on PostgreSQL `clock_timestamp()` in the counting statement, never the platform host's.

Configuration. `reporting.Config.Quotas` (`reporting.Quotas{JobsInFlight, StoredResults, AdmissionsPerHour}`): zero takes the default,
a value outside its bounds makes `NewService` return `ErrInvalid`. The demo takes `-quota-jobs`, `-quota-results` and `-quota-admissions`
with the same meaning and bounds; an out-of-bounds value stops the start with the flag's name. The service hands the resolved values to
`Repository.Admit(ctx, job, quotas)`, so the store applies exactly what the service was configured with; a store called directly with
zero fields applies the defaults and refuses a value out of bounds (`ErrInvalid`).

How it is made atomic. In the one transaction of `Admit` (which already sets the identity's row-level context, docs/OPERATIONS.md
"Row-level security"), the PostgreSQL store takes a transaction-scoped advisory lock keyed on the identity, then settles a replay of the
key, then counts, then inserts:

```sql
SELECT pg_advisory_xact_lock(hashtextextended($1, 0));  -- $1 = 'reporting.admit:' || length-prefixed tenant, issuer, subject
SELECT EXISTS (SELECT 1 FROM reporting_s01.jobs WHERE tenant=$1 AND integration=$2 AND issuer=$3 AND subject=$4 AND kind=$5 AND idem_key=$6);
-- only if no job has the key:
SELECT
 (SELECT count(*) FROM reporting_s01.jobs   WHERE tenant=$1 AND issuer=$2 AND subject=$3 AND state IN ('queued','running')),
 (SELECT count(*) FROM reporting_s01.assets WHERE tenant=$1 AND issuer=$2 AND subject=$3 AND kind IN ('snapshot','artifact') AND expires_at > clock_timestamp()),
 (SELECT count(*) FROM reporting_s01.jobs   WHERE tenant=$1 AND issuer=$2 AND subject=$3 AND created_at > clock_timestamp() - interval '1 hour');
```

Two admissions of one identity run one after the other (the second counts what the first committed); two identities never wait on
each other; the lock ends with the transaction, committed or refused. The memory store counts under its mutex with the same rules.
Under row-level security the statements see only the identity's own rows, which is what is wanted; the worker scope is not used for
admission.

Seeing an identity's usage. Counts only, never payloads (run as a role that can read the tables, not as an application connection
without a context, which sees nothing):

```sql
SELECT count(*) FILTER (WHERE state IN ('queued','running')) AS jobs_in_flight,
       count(*) FILTER (WHERE created_at > clock_timestamp() - interval '1 hour') AS admissions_last_hour
  FROM reporting_s01.jobs WHERE tenant = :'tenant' AND issuer = :'issuer' AND subject = :'subject';
SELECT count(*) AS stored_results FROM reporting_s01.assets
 WHERE tenant = :'tenant' AND issuer = :'issuer' AND subject = :'subject' AND kind IN ('snapshot','artifact') AND expires_at > clock_timestamp();
```

What the caller sees: 429 `QUOTA_EXCEEDED` with the quota's name and `Retry-After: 60` (docs/API.md "Quotas"); `GET /capabilities` carries the
numbers in force; the console names the quota (docs/CONSOLE.md). Interplay with retention: `stored_results` frees itself at expiry, not
at deletion, so a slow or stopped sweep does not hold anyone at their limit; the jobs table keeps its rows (the hour count reads them),
and an identity that wants room sooner waits for expiry or cancels what it no longer needs. A quota of `stored_results` smaller than a
tenant's working set makes its users wait for expiry: size it with the 24-hour grace and the ten-minute source reads in mind.

The counting statements read the identity's rows by the tenant prefix of the unique key of `jobs`; S04-G ("Capacity") added the indexes that
lead with (tenant, issuer, subject) and measured the difference (docs/CAPACITY.md).

Not in this slice: rate limits on `POST /queries` and reads (no admission there; a later capacity slice), per-tenant or per-integration
quotas, bytes-based quotas, quotas negotiated with the source product, object storage, backups, capacity, key lifecycle.

## Object storage (S04-E)

Until S04-E the bytes of an artifact (an XLSX or PDF export, an issued PDF) were base64 inside the JSON payload of its row in `assets`, up to
`MaxArtifactBytes` = 8 MiB, so backups, replication and vacuum of the database carried the files. With an object store configured the file is an
object in an S3-compatible bucket, the row keeps the metadata, and the database stops growing with files (docs/ADR_0007_OBJECT_STORAGE.md,
docs/S04E_SCOPE.md).

What moves and what does not:

- **Moves**: the content of every new artifact. Its row carries `Object` (`artifacts/<artifact id>`; ids are random, so a key says nothing), `Size`
  (the length in bytes) and the `SHA256` it always had, and no `Content`. The row's payload digest covers that metadata.
- **Stays**: snapshots (business rows, bounded by `MaxResultBytes`, read page by page under the source product's authority), jobs, issue records,
  quotas (an artifact is one stored result whatever holds its bytes), retention's rules and row-level security (they are the row's). An artifact
  stored before S04-E stays inline and is served as before until it expires and the sweep deletes it; nothing is moved in bulk.
- **Does not change**: the memory store keeps content inline (it is the explicit ephemeral store; the demo refuses `-memory` together with the
  object store flags), the fixture path without an object store, `GET /capabilities` (it does not say where files live), the job view.

### Configuration

The demo and `reporting-migrate sweep` take the same five flags and the same two environment variables:

| | |
|---|---|
| `-object-store-endpoint` | `https://host[:port]`, or `http://<loopback IP>:port` with `-object-store-loopback-http`; no user, path, query or fragment |
| `-object-store-bucket` | the bucket (S3 naming rules); it must exist, the platform never creates or configures a bucket, a lifecycle rule or an encryption setting |
| `-object-store-region` | the signing region, default `us-east-1` |
| `-object-store-ca` | optional PEM file; the server is then verified against these certificates alone, otherwise against the system roots |
| `-object-store-loopback-http` | the explicit option that allows `http://` on a loopback IP (127.0.0.1, ::1; not `localhost`); it is for the demo and the qualification and is refused with an `https://` endpoint |
| `REPORTING_OBJECT_STORE_ACCESS_KEY`, `REPORTING_OBJECT_STORE_SECRET_KEY` | the credentials; both required; a flag for either is refused before anything is opened, and the refusal does not repeat what followed the flag |

Any other configuration is refused at construction, with a text that repeats no key. The client is written in the repository over `net/http` and
the standard library's crypto (no SDK, no module in `go.mod`): AWS Signature Version 4, path-style, one bucket, PUT, GET, DELETE,
ListObjectsV2 by prefix and HEAD bucket; `x-amz-content-sha256` is the hex SHA-256 of the body on every request; 10 seconds per request; one retry
of an idempotent request after a network error (not after an answer, a timeout or a cancelled context); redirects are not followed; no multipart
(an artifact is at most 8 MiB).

At startup the demo sends HEAD bucket and **refuses to start** when it does not answer 200, with `the object store's bucket "NAME" is not usable:
HEAD bucket answered status N` (or `got no answer`): the bucket and the status, never the endpoint or a key. `reporting-migrate sweep` does the
same before it deletes a row.

### The policy the credentials need

PutObject, GetObject, DeleteObject and ListBucket on that one bucket, and nothing else:

```json
{"Version": "2012-10-17", "Statement": [
  {"Effect": "Allow", "Action": ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"], "Resource": "arn:aws:s3:::BUCKET/artifacts/*"},
  {"Effect": "Allow", "Action": ["s3:ListBucket"], "Resource": "arn:aws:s3:::BUCKET"}]}
```

`s3:ListBucket` serves the startup check (HEAD bucket), the reconciliation (ListObjectsV2 of `artifacts/`) and a detail of S3: without it a missing key
is answered 403 instead of 404, which the platform then reports as the store being unavailable instead of as a missing object. Give the platform a
bucket of its own. The sweep needs only DeleteObject and ListBucket; a deployment may give the scheduled sweep narrower credentials than the server's.

### Order, and what a failure leaves

| Step | Order | If it fails |
|---|---|---|
| Export or issue job | The worker PUTs the object, then publishes the row in the same transaction as the job's completion | PUT fails: the job fails with `EXECUTION_FAILED` (an issue is voided at the source product like any failure, S03-H) and nothing is published. The row is never written before the object exists. |
| Issue job | The PDF is put **before** the source product is told it exists (`ConfirmIssue`) | A failed PUT leaves the product with a reservation that is voided, never a confirmation of a file the platform did not keep. |
| Publication fails after the PUT (lost lease, a store error) | | The object is an orphan: no row names it. The reconciliation removes it after the grace. |
| Sweep | Rows are deleted and committed in the batch's transaction (S04-B), then the objects they named, read in that batch's `RETURNING` | A delete that fails is counted (`ObjectsLeft`) and left: the object is an orphan for the next reconciliation. A download between the two answers as a swept artifact does (404). |
| Download | Row first (source product's authority, expiry, row-level context, issue cross-checks), then GET with the row's SHA-256 and length, then the bytes are compared with the row again | A missing object, another length or another digest is 409 `INTEGRITY_FAILURE`, never a file. A store that cannot be reached is 503 with `Retry-After: 10`. |

A publication with an object store configured must carry an object and no content; without one it must carry content and no object; the durable
store refuses the other shapes (`INTEGRITY_FAILURE`, nothing written), so a service and a store configured differently fail closed. A row that
says where its file is in both ways, in neither, or names an object that is not `artifacts/<its own id>`, is `INTEGRITY_FAILURE` when read. The job
poll reads rows only: it answers whether or not the store is up and does not read an object (the bytes are checked when they are downloaded).
Nothing of the store reaches a caller: no error carries a response body, a key, an endpoint or a credential (an unexpected status is the store
being unavailable with the status code only).

### The sweep and the reconciliation

`Service.Sweep`, `postgres.SweepWithObjects` and `reporting-migrate sweep` with the object store flags run S04-B's three steps and a fourth:

1. Artifacts: the rows are deleted in batches (each one transaction); after a batch commits, the objects its rows named are deleted. A key is deleted
   only if it is `artifacts/<the id of the row that named it>`; a row that names anything else (a tampered payload) has its row deleted and its key left.
2. Snapshots, then job payloads: as before.
3. Reconciliation: list `artifacts/` in pages of 1000 and delete every object that (a) has the key of a canonical artifact id (anything else under
   the prefix is another process's and is left alone), (b) is older than the grace, and (c) is named by no row of `assets` of any kind (a lookup per page
   with `id = ANY($1)` in a transaction of the worker scope, so row-level security shows it every identity's rows).

**Clock.** The age of an object is judged by the platform host's clock against the time the object store stamped on the object, because that clock is the
one that stamped it; it is the only place the host's clock decides anything in the sweep (the rows' expiries stay the database's, and a source test holds
both). The grace is the sweep's `--grace` (default 24 h) but never less than 15 minutes (`MinOrphanAge`) for objects: a worker that takes a minute
between the PUT and the publication is slow, not dead, and deleting its object would leave a published row that promises a missing file. A host whose
clock is far ahead of the store's would take young objects for old; keep hosts on NTP.

Counts only, as every sweep. The result and the command's output carry `objects` (deleted after their rows), `orphans` (deleted by the reconciliation)
and `objects left` (meant to be deleted and not: a failed delete, or no object store configured); the `sweeps` ledger gains the first two (migration
008, default 0 for earlier rows). A dry run lists and counts, deletes nothing. A sweep whose store cannot be listed fails (non-zero) after its rows, with
the counts it had.

`reporting-migrate sweep [--grace D] [--batch N] [--dry-run] [--object-store-endpoint ... --object-store-bucket ...]` prints, besides the S04-B line,
`objects: N deleted, M orphans deleted, K left for the next reconciliation`; without the flags it deletes rows only, prints `objects: no object store
configured, rows only; the files of N deleted artifacts that were in an object store, and any orphan, wait for a reconciliation (...)` and exits 0. The
demo's worker loop sweeps every `-sweep-interval` through the store it was opened with and logs the object counts when there are some.

Migration 008 (`object_storage`) adds the two ledger columns; its down drops them and is refused while a ledger row counts deleted objects or an
artifact's file is an object (a binary older than the step cannot read such a row: it would stay, un-downloadable, until it expires), unless
`--accept-data-loss`. The catalog is committed as `integration/testdata/catalog_008.txt`; 003 to 007 are byte for byte unchanged.

### Verifying

```sql
-- rows that name an object, held (the number of objects that should exist, plus orphans not yet old enough to be reconciled)
SELECT count(*) FROM reporting_s01.assets WHERE kind='artifact' AND data ? 'Object';
-- rows that still hold their content inline (stored before S04-E); the two counts add up to the artifacts held
SELECT count(*) FROM reporting_s01.assets WHERE kind='artifact' AND NOT data ? 'Object';
-- the ledger: objects deleted after their rows and orphans reconciled, per sweep
SELECT at, dry_run, complete, artifacts, objects, orphans FROM reporting_s01.sweeps ORDER BY at DESC LIMIT 20;
```

Against the bucket, count the objects under `artifacts/` with the store's own tool (`mc ls --recursive ALIAS/BUCKET/artifacts | wc -l`,
`aws s3 ls s3://BUCKET/artifacts/ --recursive | wc -l`). The difference between that count and the first query is the orphans (objects whose
transaction failed, deletes that failed): it should be zero after a sweep once the grace has passed, and `sweep --dry-run` with the flags prints how
many the reconciliation would delete. A count of objects *below* the rows' count is not an orphan problem: a row promises a file that is gone, every
download of it is `INTEGRITY_FAILURE`, and the bucket's versioning or backup is where it is recovered from.

### What it costs, and what is not covered

A deployment gains a second service with its own credentials, availability and backup: a bucket that is down makes exports and issues fail (the job
fails, an issue is voided) and downloads 503, never a wrong file. The row store and the bucket must be backed up in a way that keeps them consistent
(a restored database whose bucket is older promises files that are not there; "Backup and restore (S04-F)" below says how to restore and how to check). The qualification runs against a throwaway
S3-compatible server, as PostgreSQL's (MinIO where one is already present locally; versitygw in CI, since MinIO's images are no
longer publicly pullable from quay.io or Docker Hub); **AWS S3 itself is not qualified by a test here.** The fixture's `ResetFixture` truncates
rows, not objects: a test that shares a bucket empties it first.

Not in this slice: moving existing inline artifacts, snapshots in the object store, server-side encryption, bucket lifecycle rules, multipart
uploads, AWS S3 qualification by test, backups, capacity, key lifecycle.

## Designs in the store (S11, ADR 0017)

From migration 011 the designs (report layouts, dashboards, document templates, page backgrounds, images) may live in the table `designs`
instead of the server's private directories: `"design_store": "database"` in the server's configuration (docs/DEPLOYMENT.md "Several servers").
After `up` to 011 run `roles` (a new data table). Back the table up with the rest of the database; it is not in the object store.

**Moving a deployment from its directories.** With the servers stopped (the import takes each directory's lock, as a server does):

```
reporting-migrate import-designs --issuer https://idp.example.com --bindings /etc/reporting/issuer/bindings.json \
  --layout-directory /var/lib/reporting/layouts --dashboard-directory /var/lib/reporting/dashboards \
  --template-directory /var/lib/reporting/templates --background-directory /var/lib/reporting/backgrounds
```

A file of a private directory is named by a digest of its owner, not by the owner, so the import looks for the documents of the identities the
bindings file binds (`--issuer` is the server's `issuer.issuer`). It reads every such document first and holds it to its store's rules (the
layouts', dashboards' and templates' own validation of every version; the record rules for the backgrounds and images); if one fails, nothing
is written. Then each document is one transaction as its owner. A document already in the store is kept unless `--replace`. It prints, per
store, how many were imported and kept and how many files belong to no bound identity (those are not imported: add the binding, or leave them).
No identity is printed. Then set `design_store` to `database`, remove the four directory fields and start the servers.

## The audit (S12-B, ADR 0018)

Migration 012 adds `audit_events`: people's actions (designs saved, versions published or withdrawn, issues and exports submitted, files
downloaded, actions refused for a role), each with its outcome. The application role inserts and reads and never updates or deletes; the sweep
does not touch it; nothing deletes it. A publication, a withdrawal and an issue are recorded before they run: if the database cannot take the
event the action is refused (503 `AUDIT_UNAVAILABLE`), so an action that ran has its record. Back it up with the rest of the database. It grows
with use: plan its retention outside the platform (an export, then a `DELETE` by the migrator) if a regulation sets one.

## Scheduled reports (S16-A, ADR 0020)

Migration 013 adds `schedules` and `schedule_runs` (row-level security as on the jobs; data tables of the application role; run
`reporting-migrate roles` after `up`) and lets the audit record `schedule.change` and `schedule.run`. With a `schedules` section every server runs
the scheduler every `poll_interval`: it takes each due schedule with `FOR UPDATE SKIP LOCKED`, starts one run of its occurrence (unique per
schedule and occurrence) and moves it to its next occurrence, then advances the open runs under a 2-minute lease. Several servers never run an
occurrence twice; a downtime is caught up once, by the oldest missed occurrence. The servers' clocks do not decide it: the database's does.

A run is its owner's snapshot job (and export) submitted as the owner's binding in force then. An operator who removes a binding, or a product
from it, stops that person's schedules: the next run ends `refused` (`AUTH_SCOPE_DENIED`), and so does one the source product refuses; a binding
without `viewer` ends them `AUTH_ROLE_DENIED`. The jobs count against the owner's quotas. To see what ran: `GET /schedules/{id}/runs` as the owner,
the `schedule.run` events of the audit (an administrator's tenant listing shows everyone's), and the log's `schedule` and `delivery` lines.

The webhook gets each run's end, signed with HMAC-SHA256 over the body under `REPORTING_WEBHOOK_SECRET`; the receiver checks
`X-Reporting-Signature` against the raw body before it parses it (`webhook.Verify` is the reference), treats `X-Reporting-Delivery` (the run's id)
as an idempotency key (a delivery may come twice), answers 2xx quickly and fetches nothing it does not need. A 5xx, 408, 429 or no answer is retried
after 30 s, 1, 2, 4 and 8 minutes; a 3xx or another 4xx is not (the run's `delivery` says `failed`, the log `rejected` or `redirected`). Alert on
`reporting_deliveries_total{outcome!="delivered"}` and `reporting_schedule_passes_total{outcome!="ok"}`.

Deleted schedules stay as rows (the application role never deletes) and runs accumulate, a few hundred bytes each; until S16-B adds them to the
retention, remove old ones with the migrator if they grow (`DELETE FROM reporting_s01.schedule_runs WHERE finished_at < now() - interval '400 days'`).
Migration 013 down drops both tables and the audit's schedule events.

## Backup and restore (S04-F)

Since S04-E the platform's state is in two places: the PostgreSQL store (jobs, snapshots, artifact rows, issue records, the ledgers) and, with an object
store configured, the files the artifact rows name. A backup of one is not a backup of the platform. This section says what a restore must leave true,
gives the command that checks it, and gives the order of a restore (docs/S04F_SCOPE.md). **The platform has no backup tool**: `pg_dump`,
`pg_basebackup`, WAL archiving, the bucket's versioning and replication are the operator's; this says how to use them and how to know it worked.

### What to back up

| What | Where it lives | How |
|---|---|---|
| The platform store | the database (`reporting_s01`: `jobs`, `assets`, `schema_migrations`, `profile`, `sweeps`) | `pg_dump -Fc`, or a base backup with WAL archiving. A per-database dump holds the tables, the row-level security flags and policies; it does **not** hold the roles (cluster objects): they are re-created, and `roles` re-run (below). |
| The artifact files | the bucket, under `artifacts/` | the bucket's versioning, replication or the provider's backup. Take the bucket's copy **after** the database's dump finishes (or replicate continuously): then every object a dumped row names is in the copy, and what the bucket has beyond the dump is orphans, which are harmless. The other order leaves rows that promise files the copy never had. |
| The private directories | files on one host (`-layout-directory`, `-dashboard-directory`, `-template-directory`, `-background-directory` and its `images`), mode 0700 | **not in the database and not covered by either backup above**: they need a file-level backup of their own. A restore without them loses layouts, dashboards, published templates, backgrounds and images; issue jobs already admitted carry their frozen template in the job row and are not affected. |
| The configuration | flags, environment (`REPORTING_STORE_DSN`, the object store flags and keys, the demo token), the database roles and their passwords, the object store's credentials and policy | the operator's configuration management. Keys never live in the platform's own backups: they are the environment's alone. |

### What must be true after a restore

1. **The schema is the binary's.** `reporting-migrate status` prints no `problem:` line and exits 0: the ledger, the profile and the checksums are those of the binary (if the dump is older than the binary, `status` lists the `pending:` steps; `up` applies them as the migrator).
2. **The roles and the policies are in place.** The roles are re-created and `reporting-migrate roles --migrator-role … --app-role … [--sweep-role …]` is re-run as the owner (it is idempotent); row-level security is enabled on `jobs` and `assets` with both policies (`identity`, `worker`): `status` run as the application role says the role satisfies the rules, and the server refuses to start otherwise.
3. **A row never promises a missing file.** Every artifact row that names an object has that object, with the length and SHA-256 the row says. An object that no row names is an *orphan*: harmless, and removed by the sweep's reconciliation after the grace. `verify-objects --deep` (below) checks this; a download of a row whose object is missing or is another file is `409 INTEGRITY_FAILURE`, never a wrong file.
4. **Issue records are the platform's copy of what the product confirmed.** A restore older than the product's confirmations leaves numbers the platform does not know (below). The platform never invents or reuses a number.
5. **Jobs resume.** A job that was queued or running at the backup is claimed again once its lease has run out, by the database's clock (30 s after the claim it was restored with; a restored `running` job whose lease already ran out is claimed at once): snapshot and export jobs run again; an issue job runs again under the same job id, which the product answers idempotently (S03-H); nothing is issued twice. A client's retry (the idempotency key) of a job the restored database has answers that job; of one it does not have admits a new job under the same key. The quotas count the restored rows.

### The order of a restore

1. Stop the server and every worker and sweep of the platform being replaced (no process may write while the two halves are being put together).
2. Restore the database into a fresh database: `pg_restore -d NEWDB --no-owner --no-acl dump` (or recover the base backup to the point you want).
3. Create the roles if the cluster is new, then re-run `reporting-migrate roles --migrator-role … --app-role … [--sweep-role …]` against the restored database, as the owner. A dump restored with `--no-owner --no-acl` needs this; one restored with its owners and grants finds it a no-op.
4. Restore the bucket (or point the platform at the replica) so that its state is **not older than the database's**.
5. `reporting-migrate status` (clean; apply `up` if steps are pending).
6. `reporting-migrate verify-objects --deep --object-store-endpoint … --object-store-bucket …` (below). It must exit 0.
7. Only then start the server (`reporting-server run`, docs/DEPLOYMENT.md; `/readyz` is 200 once the store and the bucket answer and the schema is right). The workers claim the jobs whose leases have run out.

### `verify-objects`

```
reporting-migrate verify-objects [--deep] [--ids] --object-store-endpoint URL --object-store-bucket NAME [--object-store-region R] [--object-store-ca FILE] [--object-store-loopback-http]
```

It runs on `REPORTING_STORE_DSN`, with the object store flags as `sweep` takes them and the keys in `REPORTING_OBJECT_STORE_ACCESS_KEY` and `REPORTING_OBJECT_STORE_SECRET_KEY` (a key given as a flag is refused, as for `sweep`). Without the object store flags it refuses: there is nothing to compare. **It never writes**: it reads the artifact rows that name an object in read-only transactions of the worker scope (`SELECT` on `assets` is all the role needs: the application role, the sweep role or any role with that grant; the tests run it as a role with nothing else), and the bucket with `List` and, with `--deep`, `Get`. It pages both sides in key order and joins them, so its memory is a page whatever the store holds. It prints counts only:

```
rows:       2 artifact rows name an object
objects:    4 under artifacts/
missing:    0 rows whose object is absent
orphans:    2 objects no row names (informational; the sweep's reconciliation removes them after the grace)
invalid:    0 rows that do not say where their file is, or fail their digest
mismatched: 0 objects whose length or SHA-256 is not the row's
```

| Count | Meaning | Fails the command |
|---|---|---|
| `missing` | rows whose object is absent: the database is newer than the bucket | yes |
| `mismatched` | with `--deep` only: the object's length or SHA-256 is not the row's (another file under the key). Without `--deep` the line says `not checked` | yes |
| `invalid` | rows that do not say, as the store requires, where their file is (the object is not the artifact's own key, the length or the digest is missing, content and object together) or whose payload fails its digest. The download of such a row is `INTEGRITY_FAILURE` too | yes |
| `orphans` | objects under `artifacts/` that no artifact row names: the bucket is newer than the database | no |

Exit 0 when `missing`, `mismatched` and `invalid` are zero, non-zero otherwise (the error line is counts too: `verify-objects: 2 missing, 0 mismatched, 0 invalid`). **No identifier, key or value is printed by default.** `--ids` additionally prints `missing id: …`, `mismatched id: …` and `invalid id: …` lines (at most 10,000 of each kind; a line says when a list is cut), which are artifact ids: an operator restoring a bucket needs them, and they go to stdout, so redirect them with care. `--deep` reads every object once, at most `MaxArtifactBytes` (8 MiB) each, so it is the slow check for after a restore and not a probe; the whole run is bounded by `--timeout` (default 5 minutes: size it for the bucket). An object store that cannot be reached is an error, never a count.

Run it with the server stopped. On a live platform a sweep can delete a row and its object between the two reads and show as a transient `missing`, and an object put just before its row is published shows as an orphan; neither is a fault. A bucket copy taken after the dump may also lack objects a sweep deleted in between: those rows have expired, `missing` counts them, and the first sweep after the restore deletes them (`SELECT id, expires_at FROM reporting_s01.assets WHERE id = ANY('{…}')` with the ids from `--ids` says so).

### Reading the result

* **`orphans` only** (the bucket is newer): nothing to do. The rows the database lost are `404` on download; their objects wait for the sweep's reconciliation (`reporting-migrate sweep` with the object store flags deletes them once older than the grace).
* **`missing`** (the database is newer): the rows promise files that are not there. Put the objects back from the bucket's versioning or backup under the same keys (`artifacts/<id>`, the bytes the row says) and run `verify-objects` again; every download of such a row is `409 INTEGRITY_FAILURE` meanwhile. What cannot be recovered is lost: the artifact can be exported again from its snapshot while that lives, and the row is deleted by the sweep when it expires. An issued PDF whose object is lost is not regenerated by the platform (the job has succeeded; a new issue job is a new job id and so a new number): whether the lost document stands is a question for the product, which holds the confirmed digest.
* **`mismatched`**: another file is under the key (a restore of another moment, a mistaken put). Put the right bytes back. The platform never serves it: the download refuses a body whose digest or length is not the row's.
* **`invalid`**: the row itself is damaged or was restored from a store that is not this platform's. Do not start the server on it before it is understood.

### Issue jobs, the product and a restore older than the product

An issue job is the platform's side of a number the source product confirmed (docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md). The product answers by the **job id** the issue was reserved under. After a restore the two can differ in either direction.

List the issue jobs the restored database holds:

```sql
SELECT id, state, generation, created_at FROM reporting_s01.jobs WHERE kind = 'issue' ORDER BY created_at, id;
```

* A job in the list that is `queued` or `running` runs again once its lease has run out, under the same job id. The product answers the same reservation and the same confirmation for the same file, so the platform ends with one record and one number (the tests and the drill prove it for a number confirmed before the restore point and for a worker killed between the reservation and the confirmation).
* A restore **older than the product's confirmations** leaves issue jobs the platform does not have at all: the product holds a confirmed number for a job id the restored database never heard of. The platform does not learn it, invent it, reuse it or issue the next one over it (the product's counter has moved on; the next issue is the next number). The product can find each by the job id it was reserved under; **the reconciliation is the operator's, with the product**: ask the product for the issues it confirmed for the integration since the time of the backup, and for each decide with its owner whether the document stands (the file is the product's, confirmed by digest; the platform's copy is gone) or is voided at the product. The client of a lost issue that retries (same idempotency key) is admitted as a new job and gets a new number.
* The platform's own record of a number is never the only copy: a restored database that is *newer* than the product (the product was restored to an earlier point) is the product's to reconcile in the other direction, and the platform reads that as it reads any product answer.

### Tested, and not

* Tested (docs/S04F_SCOPE.md F3; `TestDurablePostgresContract/object_storage/*/restore`, `/restore`, `TestDurablePostgresIssues/restore_…`, `TestPostgresMigrations/row_level_security/a_restore_…`, `TestMigrateCommand`): the cases above, on rewound rows and objects with the memory object store and the throwaway S3-compatible server; `verify-objects` as the application role, the sweep role and a role with `SELECT` on `assets` only; paging over two thousand rows and objects that interleave.
* Run for real, once, on one machine, by `scripts/restore_drill.sh` (not CI): a demo session through the object store, `pg_dump -Fc`, a copy of the bucket's objects through the platform's client, more work, `pg_restore` into a fresh database, `roles`, `status`, `verify-objects --deep` with the bucket newer, with the bucket restored, and with the database newer; a download of every artifact in each; a `kill -9` of the server inside an issue job and the restart. The transcript of the run is kept with the slice's evidence and summarised in docs/HISTORY.md (S04-F).
* **Not qualified here**: point-in-time recovery, cross-region replication, encrypted backups, a restore from provider snapshots, AWS S3 itself, a backup of the private directories, the restore of a cluster (roles with passwords). They work the way the operator's tools do; the invariants above and `verify-objects` are how to know.

## Capacity (S04-G)

docs/S04G_SCOPE.md, docs/CAPACITY.md. The slice indexes what the hot paths scanned, bounds what was unbounded, and measures. The numbers are in
docs/CAPACITY.md with the machine and what they do not show; this section is what an operator does.

### The indexes of migration 009

| Index | Serves |
|---|---|
| `jobs_identity_created` on `jobs (tenant, issuer, subject, created_at DESC)` | the admissions-in-the-last-hour count of S04-D; the issue listing of an identity with a long issue history, which the planner reads newest first once migration 010's statistics tell it how many rows match (before 010 it kept the unique key and sorted them) |
| `jobs_identity_in_flight` on `jobs (tenant, issuer, subject) WHERE state IN ('queued','running')` | the jobs-in-flight count; partial, so it holds only the live rows and stays tiny whatever the history |
| `assets_identity_kind_expires` on `assets (tenant, issuer, subject, kind, expires_at)` | the stored-results count (which scanned every asset of every tenant) |
| `jobs_reducible` on `jobs (source_expires_at) WHERE state IN ('succeeded','failed','cancelled') AND NOT (data ? 'Reduced')` | the retention sweep's job reduction (S04-B): the finished jobs not yet reduced, by the expiry of their own source read; a job leaves it when reduced |

**`CREATE INDEX` blocks writes to the table while it builds.** `reporting-migrate up` builds the four inside the migration's transaction, so
admissions, publications and the sweep of a store in use wait for the build of `jobs` and `assets` (0.1 s at 10,000 job rows, 4.4 s at 1,000,000 on the
measurement host; yours will differ, and the build takes longer on a loaded machine). Reads are not blocked. For a store that cannot stop writing for that
time, build them beforehand without the lock, with the same names and definitions, outside any transaction (`CREATE INDEX CONCURRENTLY` cannot run in
one), as the migrator or an owner; then `up` finds them and builds nothing:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_identity_created ON reporting_s01.jobs (tenant, issuer, subject, created_at DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_identity_in_flight ON reporting_s01.jobs (tenant, issuer, subject) WHERE state IN ('queued','running');
CREATE INDEX CONCURRENTLY IF NOT EXISTS assets_identity_kind_expires ON reporting_s01.assets (tenant, issuer, subject, kind, expires_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_reducible ON reporting_s01.jobs (source_expires_at) WHERE state IN ('succeeded','failed','cancelled') AND NOT (data ? 'Reduced');
```

A concurrent build that fails leaves an *invalid* index under that name, and `IF NOT EXISTS` would keep it. Migration 009 therefore checks, after its
`CREATE INDEX IF NOT EXISTS` statements, that each of the four exists, is valid and has exactly the definition above, and refuses (naming the index) if it does not:
drop the invalid or different index and run again. `down` drops the four (no rows are lost). An index made beforehand is recognised by its name and
definition, not by who made it.

### Bounds

- **Request bodies:** per route, 4 KiB, 64 KiB or 1 MiB, over which a request is 413 `REQUEST_TOO_LARGE` before it is parsed (docs/API.md "Limits").
- **Requests in flight:** the demo's `-max-in-flight` and the server's `max_in_flight` (library: `httpapi.WithMaxInFlight`; default 128, 1..4096). Beyond it a request is 503 `OVERLOADED`
  with `Retry-After: 1` and does no work. It is per process; a deployment behind several processes has that many times the bound. Choose it with the
  pool and the worker count in mind: a request in flight holds a connection only while its transaction runs, but a burst of admissions for one identity
  queues on the identity's lock holding one connection each.
- **Queries in flight per identity:** `queries_in_flight` (default 4, 1..64; demo `-quota-queries`; `reporting.Quotas.QueriesInFlight`): the `POST /queries` and
  snapshot row reads one identity has running at once, **in this process**. It protects this process's cores and its connections to the source product; it is not
  durable and not shared between processes, so the cross-process bounds remain the three durable quotas of S04-D. Beyond it: 429 `QUOTA_EXCEEDED` with
  `quota: "queries_in_flight"` and `Retry-After: 1`, nothing run. Reads of other kinds (catalog, jobs, downloads) are not counted.
- **Pool:** `pool_max_conns` in the DSN of the production store (`OpenProduction`) is now read: a value below the number of worker loops plus 2 or above 100
  stops the open (`ErrInvalid`); without it the pool is 8 connections as before (raised to the workers plus 2 when that is more). The demo's fixture path keeps
  its fixed 8. Figure: start at 32 for the default bound of 128 requests in flight and one worker loop; the measurement host did 32 concurrent admissions of different
  identities at 5,765 per second on a pool of 64 (docs/CAPACITY.md), and a pool smaller than the writers on one identity makes the others wait. This is a starting
  point from one host, not a sizing: measure the admission p99 under your own concurrency, and keep `pool_max_conns` (times the number of processes) below the
  server's `max_connections` minus what the migrator, the sweep and your monitoring hold.

### Measuring, and growth

`scripts/capacity_run.sh` (docs/CAPACITY.md) seeds the guarded fixture database, measures before and after migration 009, and removes what it seeded; `-growth`
prints what the real service's rows weigh. The `jobs` table keeps one row per admission **forever, by design**: it is the audit of what happened (retention reduces the payload of a
finished job, about 1.6 KB to 0.8 KB, and keeps the row). Size the table by identities x jobs per identity per day x 365 (docs/CAPACITY.md "Rows per year"); at the default
`admissions_per_hour` of 120 one identity can add at most 1,051,200 rows a year. When the table is too big the answer is to archive rows older than a cutoff to a separate table or
a dump, out of band, as the migrator; the platform does **not** do it: it would be a rule about audit (which rows may be forgotten, and that an Idempotency-Key dies with its row)
that the platform does not own. Partitioning by `created_at` is not done either.
The retention sweep (S04-B) was found to cost 95 s for 2,000 expired rows at 200,000 job rows, because of a snapshot probe the row-level policies kept from using the primary key; S04-G rewrote the probe and added `jobs_reducible`, and the sweep is now about 2 s for the same rows (docs/CAPACITY.md "Sweep and verification"); it still grows with the jobs in flight.

## Keys and credentials (S04-H)

The platform holds none of the secrets below at rest. Each comes from the process environment or from the host's secret store, is read at start,
and is used by long-lived pools, clients and verifiers. This section is the inventory, the procedure for changing each one, how long each change
leaves something exposed or failing, and how to see that it worked. It states that a procedure causes **no downtime** only where a named test proves
it; everywhere else it states the window or says that the claim is not made. The platform does no automatic rotation, enforces no key age (the
"suggested maximum age" column is a suggestion and nothing refuses an older key), has no KMS or HSM, no envelope encryption and no revocation of a
single token (docs/S04H_SCOPE.md).

### Inventory

| Secret or trust anchor | Protects | Held where; who provides it | Generated by | A leak lets an attacker | Rotation | Suggested max age (a suggestion) |
|---|---|---|---|---|---|---|
| Cursor key ring (`reporting.Config.CursorKeys`, 1 to 4 keys of at least 32 bytes) | the integrity of the position a cursor names in a snapshot | the host that builds the `Service`: from its secret store into the config; the demo derives its ring from `REPORTING_DEMO_TOKEN` and `REPORTING_DEMO_PREVIOUS_TOKENS` | the operator, from a cryptographic random source | forge cursors: choose a position in a snapshot the forger can already read (the snapshot is looked up by the caller's identity before the cursor is read); nothing else | rolling restart in four steps, **no downtime** (proved) | 90 days |
| Issuer JWKS (`accessjwt.Config.JWKS`, public keys; integrity, not secrecy) | which signatures the platform trusts as the issuer's | the host's protected configuration, obtained out of band from the issuer; handed to `Verifier.Replace` | the issuer's key management | an attacker who can *change* it (not read it) makes the platform trust a key of their own: protect it like code | `Replace` with old and new keys, then with the new one: **no restart, no downtime** (proved) | the issuer's policy; a suggestion is 180 days for a signing key |
| The issuer's private signing key (not held by the platform) | everything the JWKS verifies | the issuer | the issuer | mint tokens for any bound subject until the key is removed from the JWKS | remove it with `Replace`; exposure ends at the next authentication (proved) | the issuer's policy |
| Database password of the application role | the server's reads and writes of platform rows | the server's DSN, from the host's secret store | the DBA (`ALTER ROLE ... PASSWORD`) | read and write every identity's rows (the row-level policies key on session settings the connection sets itself), never delete or change the schema | change on the server, then a rolling restart; **a window, not zero downtime** (below) | 90 days |
| Database password of the sweep role | the retention sweep's deletes | the DSN of `reporting-migrate sweep` (`REPORTING_STORE_DSN`) | the DBA | delete expired assets and reduce finished jobs, write the sweeps ledger | change on the server, use the new DSN at the next sweep; **a window** (below) | 90 days |
| Database password of the migrator role | the schema and every table | `REPORTING_STORE_DSN` of `reporting-migrate up/down/roles` | the DBA | everything in the platform schema: rewrite the ledger, drop tables, disable row-level security | change on the server, use the new DSN at the next run; **a window** (below) | 90 days; keep it out of the server's environment altogether |
| PostgreSQL TLS trust (`sslmode=verify-full`, the CA: the system store or `sslrootcert`) | the server's identity, so that the password and the data go to the real server | the host's trust store or the file `sslrootcert` names | the PKI | with the CA's private key: impersonate the database and read the password and the data | a CA overlap and a rolling restart; **no downtime claim is made** | the PKI's policy |
| Object store key pair (`REPORTING_OBJECT_STORE_ACCESS_KEY`, `_SECRET_KEY`) | the artifact files (workbooks and issued PDFs) | the process environment, from the host's secret store; a flag for either is refused | the object store's own key management | read every artifact file, replace or delete them (a replaced file is refused at download: the row's SHA-256 and length are checked, 409 `INTEGRITY_FAILURE`) | the store's own procedure: create the new pair, rolling restart, delete the old pair; **the failure side is proved, "no downtime" is not claimed** | 90 days |
| Object store CA (`-object-store-ca`) | the object store server's identity | the file the flag names | the PKI | impersonate the store: read and alter files in flight (digests still catch alteration, not reading) | a CA overlap and a rolling restart; **no downtime claim is made** | the PKI's policy |
| Typst binary and fonts pins (`-pdf-typst-sha256`, `-pdf-typst`, `-pdf-fonts`) | what the server will execute and which glyphs it draws; integrity, not secrecy | the flags and the files they name | the operator, from the Typst release and the font files | with write access to the files: run other code as the server's user (inside the Typst sandbox) | change the pin and the file together, rolling restart | on a Typst advisory; no age |
| Demo token `REPORTING_DEMO_TOKEN` (**demo only; never a production credential**) | the demo's bearer authentication and its cursor key | the demo's environment | whoever starts the demo | use the demo; the demo serves contract-simulator data only | change it and restart; `REPORTING_DEMO_PREVIOUS_TOKENS` keeps the cursors of the old ones | not applicable |
| Demo colleague token `REPORTING_DEMO_COLLEAGUE_TOKEN` (**demo only, optional**, S12-D) | a second person of the demo's tenant (subject `user_b`, the same source grants), so that the team space's approval can be shown and measured | the demo's environment | whoever starts the demo | act as that second person in the demo | change it and restart; at least 32 characters and different from the demo token, or the start stops | not applicable |

Everything below this line is the procedure and the proof for each row.

### The cursor key ring

`reporting.Config.CursorKeys` is the ring, newest key first: the first signs every new cursor, any of them verifies. `Config.CursorKey` is the ring of one (setting both,
neither, a key under 32 bytes, a repeated key or more than four keys is `ErrInvalid` at `NewService`, whose text carries no key). The cursor format has no
key id, so a cursor issued by a binary that had only `CursorKey` stays valid under a ring that still contains that key. Verification tries every key
of the ring with no early exit and compares with `hmac.Equal`.

A cursor lives as long as its snapshot, which expires at most 15 minutes after the prepared read was checked (`validPrepared`; after that the rows are refused whatever the
cursor says: `TestACursorStopsWorkingWhenItsSnapshotExpires`). The four-step rotation, with K1 the old key and K2 the new one:

1. Deploy the ring `[K1, K2]` to every server, one at a time. K1 still signs; K2 is known to all.
2. Deploy `[K2, K1]`, one at a time. K2 signs; K1 is still accepted.
3. Wait until no cursor signed by K1 can still be in a user's hand: 15 minutes after the last server that signed with K1 (step 2's last restart), or longer if you prefer.
4. Deploy `[K2]`.

**No downtime, proved**: `TestCursorKeyRotationAsARollingRestartRefusesNoCursor` runs the four rings against each other and shows that a cursor issued by
a server of any of them is accepted by the servers of the ring before and after it (which is what a rolling restart puts side by side), and that the final ring refuses K1's
cursors (which is why step 3 waits); `TestPaginationContinuesAcrossARestartThatRotatesTheCursorKey` takes a real cursor over HTTP before a restart that rotates the key
and uses it after; `TestCursorRingVerifiesOlderKeysAndSignsWithTheNewest` has the pairwise matrix (K1 under `[K2,K1]`, `[K1]` accepted and `[K2]` refused; K2 under `[K3,K2]`,
`[K2,K1]`, `[K2]` accepted). Skipping the order (deploying `[K2,K1]` to some servers while others still run `[K1]`) is what the roll must not do: a K2 cursor reaches a server
that does not know K2 and is refused with 400 `INVALID_REQUEST`.

The emergency form, for a leaked key: deploy `[K2]` at once. The window is the cursors in users' hands: their next page is 400 `INVALID_REQUEST` and they start again from the first page
(`TestPaginationContinuesAcrossARestartThatRotatesTheCursorKey` shows that refusal). Nothing else breaks. To confirm: a page request made with a cursor taken before the rotation is 400 after step 4, and one
taken after step 2 is 200.

Supplying the key is the host's job: 32 or more bytes from a cryptographic random source, in the host's secret store, never in a flag or the repository. The demo derives its current key from
`REPORTING_DEMO_TOKEN` exactly as before (a single token gives the key it always gave) and takes optional `REPORTING_DEMO_PREVIOUS_TOKENS`, comma separated, newest first, at most 3, each at
least 32 characters and none repeated, so that a rotation can be rehearsed; a refusal names the variable and the entry number, never a token (`TestDemoCursorKeysAreDerivedFromTheTokensAndNeverNamedInARefusal`,
`TestTheDemosLogCarriesNoTokenAndNoKey`, which runs the demo and reads its output for the tokens and the keys derived from them). The demo's start line says how many cursor keys it holds (`cursor-keys=N`).

### The issuer's keys

`accessjwt.Verifier` trusts up to 16 public keys by `kid` (docs/ACCESS_JWT.md). `Verifier.KeyIDs()` returns the sorted `kid`s of the configuration in force and nothing else (no modulus,
no exponent): it is what to check after a rotation, and what the host may log at start. Rotation by overlap, with real RS256 tokens in `TestIssuerKeyRotationByOverlap`:

1. The issuer publishes a new key. Call `Replace` with the old and the new key in the JWKS (and the same issuer, audience, scope, lifetime and bindings): tokens signed by either are accepted.
2. The issuer starts signing with the new key. Wait until no token signed by the old key can still be valid: its `MaxTokenLifetime`.
3. Call `Replace` with the new key only. A token signed by the removed key is 401 `invalid_token` **from the next authentication on**, a token of the new key is accepted. No restart.

**No downtime, proved**: steps 1 and 3 are one atomic swap each; `TestIssuerKeyRotationByOverlap` shows both keys accepted in the overlap and the removed key refused after step 3, over HTTP.
A `Replace` that fails validation (a repeated `kid`, a short modulus, 17 keys, a repeated binding, a lifetime over the bound, an `http` issuer, no keys) returns `ErrInvalidConfig` and
changes nothing, so a rotation that fails half way leaves the previous configuration in force (`TestFailedReplaceLeavesThePreviousConfigurationInForce`): the caller must treat that error as "not rotated".
An authentication that had already read the configuration completes under it, so the removal is effective at the next authentication, not at the instant of the call
(`TestAnAuthenticationInFlightCompletesUnderTheOldConfiguration`). A token naming a `kid` that is not trusted is refused, and neither the error nor the HTTP answer names a key
(`TestAnUntrustedKidIsRefusedWithoutNamingAKey`). `Replace` is a Go method and the repository ships no endpoint or file watcher for it; the production server (S05-A) calls it on SIGHUP after re-reading `jwks_file` and `bindings_file`, logging the outcome by its code and the trusted `kid`s after a success (docs/DEPLOYMENT.md "Reload"; `TestReloadAppliesRotatedKeysAndBindingsAndKeepsTheOldOnesOnFailure`, `TestSIGHUPReloadsThroughTheCommand`, and a separate process in `internal/servertesthost`).
The demo uses `DemoBearer`, not this verifier, so it has no issuer keys to log; `reporting-server` uses this verifier and logs the trusted `kid`s at start and after each reload.

**Two different exposure windows, which must not be confused:**

- A **leaked signing key** is exposed until the operator removes it with `Replace`: effective at the next authentication, no restart. Tokens it minted are refused from then, whatever their expiry.
  Remove it at once; do not wait for step 2's lifetime.
- A **stolen token** is exposed until it expires, at most `MaxTokenLifetime` (1 to 3600 seconds, set by the operator). There is no revocation of a single token and no `jti` denylist: the
  same token is accepted as often as it is presented (`TestAStolenTokenIsBoundedByItsLifetimeAndNotRevocable`), so the lifetime is the only control. Choose it as the window the organisation accepts for a stolen token;
  a shorter one costs the issuer more token requests. The only way to end a stolen token's use early is to remove the key that signed it, which ends every token of that key.

To confirm: `KeyIDs()` lists what you expect; a token signed by the retired key gets 401.

### The database passwords

There is no dual-password state in PostgreSQL: `ALTER ROLE ... PASSWORD` replaces the verifier, and from that moment a connection with the old password is refused. What happens next is proved in
`TestPostgresMigrations/application_role_reads_and_writes_rows_and_nothing_else` (`credentialRotation`, for the application and the sweep role) and `TestMigrateCommand` (`migratorCredentials`, for the migrator):

- connections that were already open keep working, including the production store's pooled ones;
- a new connection with the old password fails (28P01), with a text that carries no password, no verifier and no connection string;
- `OpenProduction` with the old password is refused with such a text; with the new one it opens, and the role still passes the application role's rules;
- a running server whose pool must open a connection after its sessions were dropped (a database restart, a network cut; the test terminates them) cannot with the old password: every answer it gives meanwhile is an error that names no credential,
  and the server restarted with the new password serves;
- the sweep role and the migrator behave the same way with their own DSNs, and one role's password does not open another role.

**So the procedure has a window, and it is not called zero-downtime:**

1. Generate the new password; keep it in the secret store. 2. `ALTER ROLE <role> PASSWORD '<new>'` on the server (this ends the old password for every *new* connection). 3. Restart the servers one at a time with the new DSN
(`OpenProduction` refuses to start a server that cannot authenticate, so a wrong secret is found at once). 4. For the sweep and the migrator, change the DSN of the scheduled job and of the operator's shell.

The window for the application role is from step 2 to the restart of each server: until then a server serves from the connections it holds and fails the requests that need a new one (a new connection is opened
under load and after any connection loss). Size it as the time to roll the fleet, and do step 3 immediately after step 2; roll the servers in the order of the least busy first if the fleet is uneven. A
scheme with two application roles (create the second with `roles --app-role`, switch the DSNs, drop the first) would have no window; it is **not part of this slice and not qualified**.

To confirm: `reporting-migrate status` with the new DSN shows the `role:` line; a connection attempt with the old password is refused; the server logs show no authentication failure after the roll.

How the proof was made: the throwaway PostgreSQL servers of the qualification (the local container and the CI service) authenticate by `trust`, which accepts any password, so a changed password cannot be shown refusing the old one
by talking to them directly. The tests connect through `internal/pgauthproxy`, which runs SCRAM-SHA-256 against the verifier the server itself stores for the role (read at every connection, so `ALTER ROLE` changes the answer at once) and relays to the
server only the connections that passed. `ALTER ROLE`, the stored verifier, the sessions and the roles are the server's; the decision to refuse a new connection is the proxy's. That a PostgreSQL with `scram-sha-256` in `pg_hba.conf` does the
same is PostgreSQL's documented behaviour; it is **not** shown here against a server that enforces it.

A connection string that cannot be used never appears in an error: the pool library redacts the password of a string it can read, but named an unreadable string as it was (a password in the wrong place was echoed), so `postgres.ParseDSN` replaces
that error with a fixed text; `OpenProduction` and `reporting-migrate` use it (`TestOpenProductionRefusalsCarryNoPassword`, `TestMigrateCommandRefusalsCarryNoPassword`).

### The PostgreSQL TLS trust

`OpenProduction` requires `sslmode=verify-full`; the CA is the system trust store or the file `sslrootcert` names, read when the DSN is parsed at start. Replacing the server's certificate with one the same CA signs needs nothing on the platform side.
Replacing the CA: put the old and the new CA in one `sslrootcert` bundle, restart the servers (a rolling restart), change the server's certificate, and remove the old CA from the bundle at the next restart. Nothing in this slice tests it,
and **no downtime claim is made**; connections already open are not re-verified, so a restart is when a new certificate is first checked. To confirm: the server starts with `verify-full` against the new certificate.

### The object store key pair and CA

The keys are read from the environment at start and signed into every request; the platform has no other copy. The object store's own procedure is: create the new key pair with the same policy ("The policy the credentials need" above), restart the servers
and the scheduled sweep with it (a rolling restart), and delete the old pair **last**, once nothing uses it.

What is proved, in `TestS3Objects/a_key_the_server_does_not_accept_...` against the throwaway S3-compatible server: a client built with a key the server does not accept (a wrong secret, an unknown access key) fails its start check with
exactly `the object store's bucket "NAME" is not usable: HEAD bucket answered status 403`, and a running client with it fails every request kind (check, put, get, delete, list) as the store being unavailable (`ErrUnavailable`, which the API answers
503 with `Retry-After: 10`), with the status and never the key, the endpoint or the server's error code. The throwaway server has one root key pair and no way to make a second (MinIO could with its admin API; the versitygw of CI is started with a root key only), so a *revoked* key cannot be shown
being revoked: a request signed by a key the server does not accept is answered 403 whether the key was never known, was deleted or has the wrong secret, and the test shows that. Creating a second pair and running with both is the store's own and **was not tested here**; "no downtime when the old pair is deleted last"
is therefore expected, not proved. What is proved is the failure side: **deleting the old pair before every server and sweep runs the new one makes their object requests fail** until they are restarted (artifact downloads and exports, 503; the job view does not depend on the store).

Confirm: the new server starts (HEAD bucket passes), an artifact downloads, and `reporting-migrate verify-objects` runs with the new keys. The CA (`-object-store-ca`) is a file read at start: the overlap is a bundle with both, a rolling restart, then the bundle without the old one; no downtime claim.

### The Typst and font pins

`-pdf-typst-sha256` is the SHA-256 of the binary the server will run; the server refuses to start when the file does not match, and runs Typst from the descriptor it verified, so a file put at the path later never runs. The font directory is the only one Typst may use;
the server checks its files at start (the families it needs) and before every render checks that neither the binary nor the fonts changed since they were read. The server does **not** pin the fonts by hash; the hashes are in `scripts/fetch_pdf_tools.py`, which verifies what it downloads. To update: obtain the new release, check its SHA-256 against
the publisher's, change the file and the pin together, re-derive the golden files (`export/pdf/testdata/golden.json`: a different Typst can lay out differently), and roll the servers. The window is a rolling restart; a server with the new pin and the old file does not start.

### The demo token

`REPORTING_DEMO_TOKEN` is the demo's bearer credential and its cursor key. It exists for the contract-simulator demo only and is **never a production credential**; a production deployment uses `accessjwt` and a ring from the host's secret store. Changing it changes the bearer every client must send; list the old
tokens in `REPORTING_DEMO_PREVIOUS_TOKENS` to keep the old cursors valid.

### Nothing secret in an error, a log line or an answer

The tests assert on the texts with known secrets: the cursor keys of the tests, the demo's tokens and the keys derived from them (the demo's output, its refusals), the passwords of the test roles (every refusal above, the HTTP answers while the old password
fails, `/capabilities`), the object store's keys and endpoint. The rules they hold to: `NewService` refuses an invalid ring with `ErrInvalid` and no key in the text; `Verifier.KeyIDs()` returns ids only and the refusal of an untrusted `kid` names none; a connection
string is never echoed; an object store error is a status. `/capabilities` is static and carries none of this.

### The emergency order

Rotate the thing that leaked first; when a host is compromised and you do not know what was read, this is the order, from what lets an attacker act as anyone to what lets them do least:

1. **The issuer trust**, if the platform's configuration could have been changed or the issuer's signing key may be out: `Replace` with a JWKS you have verified with the issuer, and the bindings you expect. It takes effect at the next authentication and needs no restart. Then shorten `MaxTokenLifetime` if the window of a stolen token is more than you accept.
2. **The migrator password**: it can disable row-level security and rewrite the ledger. Change it, and look at `reporting-migrate status` (the ledger's checksums and the role checks say if the schema was altered).
3. **The application and sweep role passwords**, then the rolling restart (the window above): they read and write every identity's rows.
4. **The object store key pair**: it reads every artifact file. Create the new pair, roll, delete the old one; downloads are the thing to watch.
5. **The cursor ring**, in its emergency form if the key leaked: it costs only users' open pagination.
6. **The TLS CAs**, if a CA's private key may have leaked, or the Typst pin and files, if the files were writable by someone else (rebuild the host).

This order is the platform's recommendation, not something a test establishes.

### Tested, and not

Tested here: the ring and its rolling form, the issuer overlap, the failed `Replace`, the in-flight authentication, the two exposure windows' mechanics, the database passwords' effect on open and new connections (through the password-checking proxy above), the production opener's and the
command's refusals, the object store's refusal of a key it does not accept. Not tested: a PostgreSQL that enforces passwords itself, the object store accepting two key pairs, a CA rotation (either), a two-role scheme, a real issuer's reload procedure (the server's SIGHUP reload is tested with a test issuer), any age enforcement, the Typst pin update, the behaviour of a real IdP (docs/ACCESS_JWT.md: automatic discovery and emergency revocation wait for its contract).

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

The row-level security qualification (S04-C, `row_level_security` in `TestPostgresMigrations`) creates its own migrator, application
and sweep roles (`reporting_rls_*_t`) and proves the policies as the application role and the sweep role, with two identities of one
tenant and a third of another; the fixture suites run as the superuser and do not exercise the policies.

The quota qualification (S04-D) runs inside `TestDurablePostgresContract` (`quotas`): the quota contract of both stores, parallel admissions of one identity (exactly the limit admit), and the identity-keyed lock watched in `pg_locks`; the same contract runs on the memory store in `TestMemoryQuotaContract`.

The object storage qualification (S04-E) runs inside `TestDurablePostgresContract` (`object_storage`: the contract of both object stores on the durable store, then what only it has to qualify) and in `TestMemoryObjectContract` (the memory store); both run the same contract, against `objects.Memory` and, when `REPORTING_OBJECT_STORE_TEST_ENDPOINT`, `_ACCESS_KEY` and `_SECRET_KEY` are set, against a throwaway S3-compatible server (`REPORTING_OBJECT_STORE_REQUIRE=1` makes a missing endpoint a failure, as `REPORTING_S01_REQUIRE_PG=1` does for PostgreSQL). `TestS3Objects` qualifies the client alone against the same server; the signing is qualified against the published examples of the AWS Signature Version 4 documentation without one. The CI postgres job starts versitygw (Apache-2.0, `versity/versitygw` pinned by digest; MinIO's images are no longer publicly pullable) before the tests; the test creates the bucket `reporting-test-s04e` with a PUT bucket request, the one bucket-level call in the repository, in the test only (a source test holds it to that).

The backup and restore qualification (S04-F) runs inside the same tests and in `scripts/restore_drill.sh` ("Backup and restore (S04-F)" above). The capacity measurements are in "Capacity (S04-G)" above.

The key lifecycle qualification (S04-H) adds no top-level test that needs a database or an object store: the credential proofs are `credentialRotation` inside `TestPostgresMigrations/application_role_reads_and_writes_rows_and_nothing_else`, `migratorCredentials` inside `TestMigrateCommand` and a subtest of `TestS3Objects`, so both skip lists of `.github/workflows/ci.yml` are unchanged and the CI object store stays versitygw; `internal/pgauthproxy` is test plumbing that is never linked into a binary. What is not covered is listed at the end of "Keys and credentials (S04-H)".
