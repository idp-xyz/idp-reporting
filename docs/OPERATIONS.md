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
| 005 | retention | `assets.expires_at` and its index (S04-B, "Retention") | nothing (the payload keeps the expiry) |
| 006 | sweeps | the `sweeps` ledger (S04-B) | the ledger (refused while it has rows unless accepted) |
| 007 | row_level_security | row-level security and the `identity` and `worker` policies on `jobs` and `assets` (S04-C, "Row-level security") | nothing (policies only) |

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
back exactly the catalog of version 3 (integration/migrate_test.go, integration/testdata/catalog_00{3,4,5,6,7}.txt).

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
version and a role that lacks what it needs, before anything is changed. It is idempotent and returns counts only.

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

The counting statements read the identity's rows by the tenant prefix of existing indexes; no migration was added. An identity with a very
long history of jobs makes the hour count scan more rows; watch admission latency and add an index on `(tenant, issuer, subject, created_at)`
in a later migration if it matters.

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
(a restored database whose bucket is older promises files that are not there; S04's backup slice owns that). The qualification runs against MinIO
(a throwaway server, as PostgreSQL's), locally and in CI; **AWS S3 itself is not qualified by a test here.** The fixture's `ResetFixture` truncates
rows, not objects: a test that shares a bucket empties it first.

Not in this slice: moving existing inline artifacts, snapshots in the object store, server-side encryption, bucket lifecycle rules, multipart
uploads, AWS S3 qualification by test, backups, capacity, key lifecycle.

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

The object storage qualification (S04-E) runs inside `TestDurablePostgresContract` (`object_storage`: the contract of both object stores on the durable store, then what only it has to qualify) and in `TestMemoryObjectContract` (the memory store); both run the same contract, against `objects.Memory` and, when `REPORTING_OBJECT_STORE_TEST_ENDPOINT`, `_ACCESS_KEY` and `_SECRET_KEY` are set, against a throwaway MinIO (`REPORTING_OBJECT_STORE_REQUIRE=1` makes a missing endpoint a failure, as `REPORTING_S01_REQUIRE_PG=1` does for PostgreSQL). `TestS3Objects` qualifies the client alone against the same server; the signing is qualified against the published examples of the AWS Signature Version 4 documentation without one. The CI postgres job starts the MinIO (the image pinned by digest) before the tests; the test creates the bucket `reporting-test-s04e` with a PUT bucket request, the one bucket-level call in the repository, in the test only (a source test holds it to that).

Not covered yet: backups and restore, capacity, key lifecycle.
