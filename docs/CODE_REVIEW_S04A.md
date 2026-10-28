# S04-A independent review of the production storage foundation

Scope: versioned migrations, rollback, least privilege and the production opening path (docs/S04A_SCOPE.md)
on branch feat/s04-production-storage, implementation ee77e5f and the fixes after it. Two reviewers worked
independently of the author and of each other, on git-archive copies, on two axes: correctness and
security; test effectiveness and documentation accuracy. Every finding below was reproduced by the reviewer
before it was reported, and verified closed by the author with a test or a measurement.

## Round 1

### Correctness and security

Baseline on ee77e5f: build and vet clean; the slice's own qualification green on the reviewer's databases;
the demo with `-initialize-fixture` and the recovery probe's phases pass on the new opening path. The
reviewer's own checks: the old fixture (fixture.sql plus the v1→v2→v3 statements) and the migrations give
identical catalogs over columns, attributes, constraints, indexes, relation options, triggers, owners and
ACLs; a failed DDL step rolls back the ledger and the profile; a cancelled step leaves the ledger unchanged;
6 parallel ups and 120 mixed down/up runs end consistent; the TLS predicate matches pgx's verify-full and
environment variables cannot bypass the DSN rules; `InsecureTestOnly` is set by tests only.

| # | Finding | Disposition |
|---|---|---|
| 1 | Major (security): `roles` interpolated every table name of the schema unquoted into DDL; a role with CREATE on the schema could plant a table whose name is SQL, and the documented superuser run of `roles` executed it (a role was made superuser) | Fixed. Every identifier is quoted (`pgx.Identifier`), only the tables the binary knows are touched, an unknown table makes `roles` refuse and the role check name it; test with a planted name |
| 2 | Major (security): the role check ignored memberships: a member of the superuser role or a NOINHERIT member of the migrator passed and could `SET ROLE` to everything | Fixed. `session_user = current_user` and no membership at all are required; ownership is checked through `pg_has_role`; tests with both roles |
| 3 | Major: a step did not check the ledger's head under the lock, so a stale plan applied 003 onto a version-1 schema and left a gap the tool then refused to repair | Fixed. Under the lock the head must be exactly the step's predecessor (up) or the step (down), else a conflict; a profile at another version is an integrity failure there and a reported problem in `status`; test with a stale step |
| 4 | Major: 003 down ignored the issue counters; after an accepted down and up the numbering restarted at 1, reusing printed numbers; counters without rows were dropped without acceptance | Fixed. The counters and the issued PDFs are counted and the message says the numbering restarts; the profile row counts for 001; tests |
| 5 | Minor: `roles` left a leftover CREATE on the schema | Fixed. Revoked; test |
| 6 | Minor: `roles` failed below version 3 and the default privileges give SELECT only, so a recreated data table left the application role read-only | Fixed. Grants on the tables that exist; documented: run `roles` after every up or down that creates a data table; tests at version 2 |
| 7 | Minor: a half-initialised database (profile table, no row, no ledger) was treated as version 0 and an empty ledger was left behind | Fixed. A reported problem; the bootstrap needs a profile row; test |
| 8 | Minor: `down --to N` above the current version (and `up` below) silently did nothing | Fixed. Refused; tests and the command test |
| 9 | Minor: the refusal of a legacy database read as a contradiction | Fixed. Its own message |
| 10 | Minor (hardening): legacy rows skipped verification forever and the marker was an in-band value | Fixed. A production store refuses legacy rows; a legacy row must carry the bootstrap's marker; documented trust boundary |
| 11 | Minor: bare INVALID_REQUEST messages; `plan --to 0` could not plan a down to zero | Fixed |
| 12 | Note: any login can hold the advisory lock and stall a migration | Fixed. A 60 s lock timeout with a message naming the cause |
| 13 | Note: `Pool()` had no caller | Removed |
| 14 | Note: 001 down dropped the profile row without acceptance | Fixed with 4 |
| 15 | Note: 003 down left the issued PDFs as orphans | Fixed with 4: counted and named |
| 16 | Note: one 5-minute context for the whole run | Fixed. `--timeout` |
| 17 | Note: verify-full without `sslrootcert` uses the system trust store | Documented |

### Tests and documentation

Baseline on ee77e5f: the whole Go suite green (124 passed, 4 database skips); the reviewer's own
experiments on a private database (old fixture versus migrations identical; pre-ledger v3; a failed DDL
step; roles; plan edges; six concurrent ups over eight rounds; a stale profile), nine mutations.

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: concurrent migrators did not serialize on a fresh database with a warm pool (a backend waiting on the lock keeps a negative catalog cache entry, so `CREATE SCHEMA IF NOT EXISTS` failed on a duplicate); the subtest was flaky | Fixed. A catalog scan after the lock accepts the invalidations and the schema is created only when absent; the subtest warms the pool and runs five rounds of six migrators |
| 2 | Major: the documented migrator role could not run the first `up` (CREATE on the database is needed to create the schema); the test never migrated as the migrator | Fixed. Documented; the subtest grants it and runs down to zero, up, roles at version 2, up and init-profile as the migrator role |
| 3 | Major: `roles` failed at any version below 3 | Fixed with correctness finding 6; tested in the migrate test and the command test |
| 4 | Major (docs): "tables created by later migrations get the same grants" was false | Fixed. The guide says the opposite and when to run `roles` |
| 5 | Major: "a failed step leaves the previous version intact" was unproven (the only failed step failed on privileges) | Fixed. A conflicting table makes 003 up fail; ledger, profile and catalog are unchanged |
| 6 | Major: the realistic pre-ledger database (the old fixture at v3) was never opened by a test, and its refusal message was wrong | Fixed. Tested: refused with "no schema ledger", initialising records three legacy rows; refused on a production profile |
| 7 | Minor: the superuser rule was not isolated | Fixed for the attributes that can be: CREATEROLE, CREATEDB and BYPASSRLS each on its own role expecting exactly that problem; a superuser fails every rule by nature |
| 8, 9, 13, 14, 15 | Minor/Note (docs): "creates or verifies two roles", `init-profile … fixture`, "the whole contract suite as the application role", the legacy trust boundary, 001 down keeping the schema, stale comments | Fixed in the scope, the guide and the comments |
| 10 | Minor: the refusal cases asserted only the fact, never the reason; a stale profile was not a problem for `up` | Fixed. Each case asserts its reason in `status` and in the refusal; a stale profile is a reported problem and refused by `up` |
| 11 | Minor: the consistency rules of the embedded set were untested | Fixed. `migrationsFrom(fs.FS)` with a unit test on synthetic sets |
| 12 | Minor: the command had no Go test; `roles` below 3, `init-profile fixture` and silent no-ops were seen in the transcript only | Fixed. `run(args, io.Writer)`; argument cases without a database; every subcommand against the fixture (registered in the CI skip lists) |
| 16 | Note: the catalog snapshot lost type modifiers and nothing compared against the old fixture any more | Fixed. `format_type`; the version-3 catalog is committed and compared on every run |
| 17 | Note: CI skip lists correct | — |

## Round 2

### Tests and documentation

Baseline on 4249a8d: gofmt, vet and the unit tests clean; the argument test of the command makes no network
connection; the reviewer's own driver against a private database (warm-pool concurrency, the migrator role's
lifecycle, grants at version 2, the head precondition and failed-step atomicity, the stale profile, the
pre-ledger v3 database) and five mutations (the catalog scan, DELETE in the grants, the head precondition,
the `--to` requirement, the stale-profile problem). Every round-1 closure was confirmed except three parts of
finding 12 and the scope of finding 2, below.

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: the go job's skip lists lacked `TestMigrateCommand` (the strict runner would have failed) | Fixed. Both lists |
| 2 | Major (docs): "CREATE on the database for the very first `up`, nothing after" was false: 001's SQL still created the schema, so every `up` that applied 001 again needed it | Fixed. 001's SQL no longer creates the schema; the migrator creates it when absent; the test revokes the privilege after the first run and rolls back to zero and up again as the migrator role |
| 3 | Major: the command's database test and the integration tests share the fixture database while `go test ./...` runs packages in parallel | Fixed. The postgres job runs packages one at a time (`-p 1`); documented |
| 4 | Minor: the command test closed its pool before the role cleanups ran, so the roles leaked | Fixed. The close is registered first |
| 5 | Minor: the argument test accepted any error, so a missing check survived | Fixed. Each case asserts its message |
| 6, 7, 8 | Minor (docs): the scope's data-loss list and role rules lagged the code; the tests' roles and the skip count | Fixed |
| 9 | Info: an empty branch in a test | Removed |
| 10 | Info: `ApplyStepForTests` is exported from the production package | Accepted for now; unreachable from the command |
| 11 | Info: the README named the review record and the evidence before they existed | Both exist at the final commit |

### Correctness and security

Baseline on 4249a8d: the whole suite with PostgreSQL green, the demo and the recovery probe's phases green
on the new opening path; a probe per round-1 disposition fails on ee77e5f and passes on 4249a8d (every
closure confirmed by reproduction, including the warm-pool concurrency under the race detector). The
reviewer's own checks: the head shortcut cannot skip a step wrongly; the catalog scan after the lock accepts
invalidations even when the schema was dropped and recreated with new OIDs; predefined-role grants appear in
the membership rule; nothing the server needs is lost by the schema revocation; every SQL string is a
constant or a sanitized identifier; pools and transactions are released on every path.

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: 001's SQL still created the schema, so a migrator without CREATE on the database could not come back up after a rollback to zero, against the documented "first run only" | Fixed. The migrator creates the schema when absent and 001's SQL does not; the test revokes the privilege after the first run and rolls back to zero and up again as the migrator role (the checksum of 001 changed before any store carried it) |
| 2 | Major: an application role that owned the database or had CREATE on it passed the role check, yet could create schemas, set database parameters or drop the database | Fixed. Both are refused; documented that the database is never created as the application role; tests |
| 3 | Minor: column-level grants on the profile or the ledger were invisible to the table probes | Fixed. Any column-level grant in the schema is refused; `roles` revokes them; test |
| 4 | Minor: the REPLICATION attribute was not refused | Fixed; isolated test |
| 5 | Minor: the 003 message said the issued PDFs would be discarded while they stay without their records | Fixed. The message and the guide say what happens to them |
| 6 | Minor: the guide sent the operator to run `roles` as the migrator after a superuser's step, which cannot hand over the superuser's tables | Fixed. "as whoever ran the step" |
| 7 | Low: `--timeout 0` expired at once | Fixed. Refused |
| 8 | Low: the lock message blamed the server when the run's own time limit expired | Fixed. Distinguished |
| 9 | Low: a stale down whose ledger was dropped by another migrator surfaced a raw error | Fixed. A conflict that says to rerun |
| 10 | Low: a ledger without a profile table was not a problem | Fixed. Reported |
| 11 | Low: a stale `up --to N` whose head overshot the target returned success | Accepted: the command prints the real version; the API caller sees it in the state |

Final evidence after both rounds: see README.md and evidence/s04a/checks.json.
