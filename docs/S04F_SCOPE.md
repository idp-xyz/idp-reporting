# S04-F backup and restore: what a restore leaves consistent, and the tools to prove it

2026-11-02. Since S04-E the platform's state is in two places: the PostgreSQL store (jobs, snapshots, artifact rows,
issue records, the ledgers) and, with an object store configured, the files the artifact rows name. A backup of one is
not a backup of the platform. This slice defines what a restore must leave true, gives the operator a command that
checks it, proves the cases by tests and by a real dump-and-restore drill, and writes the runbook. It does not
implement a backup tool: `pg_dump`/`pg_basebackup`/WAL archiving and the bucket's versioning or replication are the
operator's, and this slice says how to use them.

F1 · What must be true after a restore (the invariants, in OPERATIONS.md "Backup and restore"):

- the schema ledger, the profile and the checksums are those of the binary (`reporting-migrate status` is clean);
  the roles and policies are in place (`roles` re-run as the owner; row-level security enabled and both policies
  present: the role check says so);
- every artifact row that names an object has that object (a row must never promise a missing file); an object that no
  row names is an orphan, harmless, and removed by the sweep's reconciliation after the grace;
- issue records are the platform's copy of what the source product confirmed: a restore older than the product's
  confirmations leaves numbers the platform does not know. The product can find each by the job id it was
  reserved under (docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md); the runbook says how to list the issue jobs restored and
  how an operator reconciles the rest with the product, and states that the platform never invents or reuses a number;
- jobs that were queued or running at the backup are claimed again once their lease has run out (the lease is in the
  database, by the database's clock): snapshot and export jobs run again; an issue job runs again with the same job id,
  which the product answers idempotently (S03-H); nothing is double-issued.

F2 · The command. `reporting-migrate verify-objects [--deep] [--object-store-… flags as for sweep]` on
`REPORTING_STORE_DSN` with the object store configured as `sweep` is: pages through the `artifact` rows that name an
object (worker scope, a read-only role is enough: SELECT on assets), lists the bucket's `artifacts/` prefix in pages, and prints counts only:
rows that name an object, objects found, **missing** (rows whose object is absent), **orphans** (objects no row names,
informational), and, with `--deep`, **mismatched** (the object's length or SHA-256 differs from the row's: it reads
every object once, at most MaxArtifactBytes each, so it is the slow check for after a restore, not a probe). Exit 0
when missing and mismatched are zero, non-zero otherwise; no ids, no keys, no values on stdout (an `--ids` flag that
lists the artifact ids of the missing and mismatched rows, to stdout, is allowed because an operator restoring a
bucket needs them; it is off by default and documented as printing identifiers). Without the object store flags it
refuses (`verify-objects` has nothing to compare). The checks live in `storage/postgres` (`VerifyObjects(ctx, pool,
objects, deep)` returning counts) so tests can drive them; the command is a thin wrapper. It never writes.

F3 · The consistency cases, proved by tests (PostgreSQL + `objects.Memory` and the throwaway S3-compatible server where
the suite already uses it; the restore is simulated by rewinding rows and objects to an earlier state, because the
guarded fixture databases cannot be dropped and recreated):

- bucket newer than the database (rows rewound, objects kept): the objects are orphans; `verify-objects` counts them and
  exits 0; the sweep's reconciliation deletes them after the grace; downloads of the vanished rows are 404;
- database newer than the bucket (objects removed, rows kept): `verify-objects` counts them missing and exits non-zero;
  `--ids` lists exactly them; the download is 409 INTEGRITY_FAILURE, never a wrong file; after the operator puts the
  object back (same bytes) `verify-objects` is clean and the download works; a different bytes put back is
  `mismatched` with `--deep` and 409 on download;
- a job running at the backup, its lease run out (by the database's clock): it is claimed again and finishes; an issue
  job runs again against a product that already confirmed it and ends with one record and one number;
- an idempotency-key replay of a job that exists in the restored database answers the job; of one that does not, admits
  a new job (the client's retry after a lost response), and the quotas count the restored rows;
- the invariants of F1 after a rewind: `status` clean, the role and policy checks pass, row-level security still hides
  other identities' rows.

F4 · The drill (local evidence, not CI): `scripts/restore_drill.sh` (bash, no dependencies beyond `docker`, `pg_dump`/`pg_restore` through
`docker exec` on the throwaway PostgreSQL container, and the built binaries). On scratch databases it creates itself
(`reporting_restore_s04f_*`, dropped at the end; never the fixture databases, never port 55432) it: runs a demo
session against the throwaway S3-compatible server (export, issue, download), `pg_dump -Fc`s the database, copies the
bucket's objects aside through the client (a small Go helper under `scripts/` or an `internal/` tool, not an exported API), makes more
work, restores the dump into a fresh database with `pg_restore`, re-runs `roles`, and runs `status`, `verify-objects
--deep` and a download of each artifact in the combination bucket-newer and bucket-restored; a `kill -9` of the demo
in the middle of a job and the restart (the job finishes after its lease; the transcript shows it). The transcript
is saved under the scratchpad and summarised in README; it is evidence of one run on one machine.

F5 · Docs. OPERATIONS.md "Backup and restore" (the invariants; what to back up: the database with WAL or dumps, the bucket
with versioning or replication, the private directories (layouts, dashboards, templates, backgrounds, images) which are
files on one host and are **not** in the database, and the configuration; the order of a restore: database, roles,
bucket, `status`, `verify-objects --deep`, then start the server; what a restore older than the product loses and
how to reconcile; the tested and the untested: point-in-time recovery, cross-region, encrypted backups are the
operator's and not qualified here), README S04-F at the top with the "以下为此前阶段历史说明：" line moved directly below it.
API.md and CONSOLE.md unchanged.

Not in this slice: a backup scheduler or tool, point-in-time recovery qualification, cross-region replication,
encryption of backups, backing up the private directories (the runbook says they need a file-level backup), capacity,
key lifecycle.
