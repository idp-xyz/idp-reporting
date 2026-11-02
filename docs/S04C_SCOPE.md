# S04-C row-level security: the store refuses the rows of another identity

2026-11-01. Today tenant isolation is the service's alone: every store query names the owner (tenant, issuer,
subject) in its predicate, and the application role is one role for every tenant (docs/S04A_SCOPE.md left the
database's own isolation, "with the per-request session variable it needs", to this slice). This slice makes the
database refuse any row of another identity to a connection that has not said whose rows it works on: a missing
predicate in a future query, or a connection that forgot its context, leaks nothing. No context, no rows.

What it is not: a defence against a compromised server process, which can set any context; against that stand the
roles of S04-A and the real identity provider of the last slice. It is defence in depth against the platform's own
mistakes, and it is proved by making such a mistake on purpose.

C1 · Schema. Migration 007 `row_level_security` enables row-level security on `jobs` and `assets` (ENABLE, not
FORCE: the migrator, as owner, bypasses it, so migrations and their data checks run as today; a superuser does too)
and creates two permissive policies on each, for every command, to PUBLIC:

- `identity`: USING and WITH CHECK `tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer =
  nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject',
  true), '')`;
- `worker`: USING and WITH CHECK `current_setting('reporting.scope', true) = 'worker'`.

A setting that is missing or empty makes the comparison NULL: no row is visible, no row may be written. The
ledgers (`schema_migrations`, `sweeps`), the profile and `issue_numbers` (version 3 only) carry no business rows and
get no policy. The down step drops the policies and disables row-level security. The migration qualification's
catalog gains the policies (name, command, roles, qualifications) and each table's `relrowsecurity` /
`relforcerowsecurity`, committed as testdata/catalog_007.txt; 006 and earlier catalogs are unchanged by that
extension only if it prints nothing for tables without policies, so keep them byte-identical.

C2 · The store sets the context, per call, transaction-local, never session-level. Every identity-scoped call of
`storage/postgres` (Admit, Job, Cancel, Snapshot, Artifact, Issue, Issues) runs inside one transaction that first
sets the three identity settings with `set_config(name, $n, true)` (the values are parameters, never interpolated)
and then runs its statements. Every worker call (Claim, Publish, Fail, ResolveFailure) and every batch of the sweep
sets `reporting.scope` to `worker` the same way. A context never outlives its transaction, so a pooled connection
carries none into the next call; `set_config(..., false)` and `SET` without LOCAL do not appear in the package (a
source test holds it to that, as `TestSweepNeverDecidesByTheHostsClock` does for clocks). The memory store does not
change. `ResetFixture` and the fixture path's superuser connections are unchanged.

C3 · Roles. The application role and the sweep role are subject to the policies (neither has BYPASSRLS; S04-A already
refuses the attribute). `RoleProblems` and `SweeperProblems` gain the store's shape: row-level security enabled on
`jobs` and `assets` and both policies present with their expected qualifications, else a problem that names the
table (an administrator's `DISABLE ROW LEVEL SECURITY` or a dropped policy leaves the ledger intact, so the ledger
alone would not see it); `OpenProduction` therefore refuses to start, and `status` says so. The `roles` command
grants nothing new: policies need no grant.

C4 · Proof, in the PostgreSQL qualification (`TestPostgresMigrations`, `TestDurablePostgresContract` or new subtests
of them; the roles created there), as the application role and the sweep role through the production path, with
two identities A and B of the same tenant and a third of another tenant, each with a job, a snapshot, an artifact and
an issue record made through the service:

- no context: every SELECT on `jobs` and `assets` answers no rows; an INSERT and an UPDATE are refused (SQLSTATE
  42501, "new row violates row-level security policy"); nothing is changed;
- context A: only A's rows are visible, through the service and in raw SQL; an INSERT or UPDATE that would make a
  row B's or the other tenant's is refused; B's rows cannot be read, updated, cancelled or (as the sweep role)
  deleted; a service call by B for A's job, snapshot, artifact or issue is not found, as today;
- the mistake on purpose: the SQL of `Job` and of `asset` with their identity predicates removed, run under A's
  context, still answers only A's rows; the same SQL run as the superuser answers everyone's (this is the test that
  fails if the policies are missing);
- worker scope: `Claim` claims jobs of every identity in admission order, `Publish` writes assets under the job's
  owner, the sweep deletes across identities, and a worker transaction without the scope setting claims nothing;
- no leak between calls: with a pool of one connection, after each kind of call (one per method, and a sweep) a
  probe `SELECT current_setting('reporting.tenant', true), current_setting('reporting.scope', true)` on that pool
  answers NULL, NULL;
- migration 007 round trip and catalog, `status`, `down --to 6`, and a legacy store from version 6 upgrading; the
  catalog files of 004, 005 and 006 unchanged;
- `DISABLE ROW LEVEL SECURITY` on `jobs`, and separately a dropped `identity` policy on `assets`, make
  `OpenProduction` refuse with a problem naming the table, `status` print it, and the server's role check fail;
  restoring them makes it open again;
- the full existing suites keep passing on the fixture path (superuser: the policies do not apply there, which
  OPERATIONS.md says).

C5 · Docs. OPERATIONS.md gains "Row-level security" (what it protects and what it does not, the settings, which
roles are subject, the verification queries: `pg_class.relrowsecurity` for the two tables and `pg_policies` for the
schema, and the fixture caveat); README gains an S04-C section at the top in the style of the recent ones, with the
line "以下为此前阶段历史说明：" moved directly below it; API.md and CONSOLE.md are unchanged (no behaviour changes
for a caller: a view that was 404 stays 404).

Not in this slice: one role or connection per tenant, policies on the ledgers, object storage, quotas, backup and
restore, capacity, key lifecycle.
