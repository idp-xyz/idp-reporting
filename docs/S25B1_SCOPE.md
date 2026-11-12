# S25-B1 · The store is ready for results that outlive their read

2026-11-12. The second slice of ADR 0026 (docs/ADR_0026_RESULT_LIFETIME.md) and the first half of what its list calls S25-B. S25-A
separated the read, the scope and the result (docs/S25A_SCOPE.md). No source of the protocol can declare a scope yet, so no
deployment keeps a result beyond its read. Before one can, the store needs the two things S25-A found ("Not in this slice" there),
and the binaries need a rule for how they are rolled. This slice brings the three, and nothing of the wire: the protocol's
`scope_expires_at`, the `http` adapter, `sourcetest`, the durable simulator and the checker's rules are S25-B2.

What a deployment sees of this slice today, while every result still ends with its read:

- the default of `quotas.stored_results` is 512 and no longer 64, and an issue is never refused for that quota;
- with an object store, the sweeps that run on a schedule list the bucket about once a day, and at once after a sweep that left
  objects behind or did not complete, where every sweep listed it. The file of a publication that failed after it was put was
  removed by the first sweep after the grace; it now waits for the next listing, up to 23 hours longer by default;
- two migrations, `kept_results`, which builds an index on `assets`, and `stored_results_index_cleanup`, which drops one.

## What

B1 · **An issued document's file is not a stored result** (ADR 0026 decision 7).

- *What is counted.* `stored_results` counts an identity's snapshots and export files whose expiry is in the future by the store's
  clock. The file of an issued document is left out, as its record always was: it is kept for `retention.issued` (ten years by
  default) where the source vouches, and it would hold its slot for as long. The memory store leaves out the artifacts that name an
  issue (`Artifact.Issue`); PostgreSQL leaves out the rows whose payload has the key (`AND NOT (data ? 'Issue')`). The key is
  omitted from an export's payload and has been written on every issued file since there were any (S03-G).
- *What is refused.* `reporting.MakesStoredResult(kind)`: a freeze makes a snapshot and an export a file, an issue makes neither. Both
  stores refuse a job for `stored_results` only when its kind makes one. So at the limit a freeze and an export are refused and an
  issue is admitted: it adds nothing to what the quota counts, and refusing it would make a person delete or wait out exports in
  order to issue a document. This is how the slice reads the decision's "issued files do not count: admissions per hour bound
  them"; the decision does not say it in these words. Issuing is bounded by `jobs_in_flight` and `admissions_per_hour`, and each
  file by `MaxArtifactBytes`. The order of the checks is unchanged (`jobs_in_flight`, `stored_results`, `admissions_per_hour`).
- *The default* is 512 (`reporting.DefaultStoredResults`), within the unchanged bounds 1..100000. It was 64 while a result lived a
  quarter of an hour at most; an export kept for a week holds its slot for a week. `GET /capabilities` says the number in force,
  as before, and a deployment that sets the quota keeps its number.
- *The index.* The count must not read a payload to tell an issued file from an export: without an object store the payload is the
  file. Migration `kept_results` adds `assets_identity_results` on `assets (tenant, issuer, subject, expires_at) WHERE kind IN
  ('snapshot','artifact') AND NOT (data ? 'Issue')`. The statement is written with the index's predicate, so the planner answers
  from the index, under the row-level policies too. Measured for an identity with 20,000 issued files stored inline and 300 results
  (docs/CAPACITY.md "Migration `kept_results`"): the count as it was read 130 pages and counted 20,300; the new count without the
  index 95,275 page accesses in 101 ms; with it 5 pages in 0.1 ms. As 009's, the step accepts its index built beforehand with
  `CREATE INDEX CONCURRENTLY` and refuses anything else under the name (another definition, an invalid index, something that is no
  index), changing nothing; it compares the definition letter for letter, because the kinds and the key are text
  (docs/OPERATIONS.md "Quotas").
- *The index it replaces*, `assets_identity_kind_expires` of 009, served this count alone and held every issued file. It is dropped
  by the next step, `stored_results_index_cleanup`, and not by `kept_results` (B4 says why).

B2 · **The object store is reconciled when it is due, not at every sweep.**

The reconciliation lists the whole `artifacts/` prefix to find the objects no row names (S04-E). It is the one step of a sweep that
costs what the bucket holds and not what the sweep handles, and with kept results the bucket holds the exports of a week and the
issued documents of ten years.

- `SweepOptions.Reconcile` is how long a reconciliation settles the bucket for. Zero reconciles at every sweep, as before, and is what
  a caller of `Service.Sweep` gets who says nothing. A negative value or one above `MaxReconcileInterval` (30 days) is `ErrInvalid`:
  the reconciliation is what removes a file whose row is gone, and cannot be put off for good.
- **The rule.** Two kinds of sweep concern the bucket, dry runs apart: a sweep that left objects behind or did not complete, which
  counts from the moment it ended, and a reconciliation that ran to its end and left none, which counts from the moment its listing
  began. With an interval, a sweep leaves the bucket alone while it is *settled*: the newest of these is such a reconciliation, and
  its listing began less than the interval ago by the store's clock. What follows:
  - a sweep that leaves objects makes the next sweep reconcile whatever the interval: a delete that failed, or a sweep that was given
    no object store and deleted rows whose files are objects. Where the sweeps of a store have one grace, a file whose row is gone
    is removed as soon as it was before ("Known" has the other case);
  - so does a sweep that did not complete: it was stopped, a batch failed, the bucket could not be listed. It cannot always say what
    it left: in the durable store a batch whose commit was never answered may have deleted its rows;
  - a reconciliation that itself left an object does not settle the bucket;
  - a reconciliation settles only what was left before its listing began, because a listing knows the bucket as it was then. With
    two sweeps at once, one that leaves an object while the other is listing, at a key the listing has passed, is not hidden by the
    reconciliation that ends after it;
  - a reconciliation of a sweep whose grace is under `MinOrphanAge` (15 minutes) settles nothing. The reconciliation takes an object
    no row names only when it is older than the grace and than that floor, so a sweep with a shorter grace can leave an object that
    its own next reconciliation finds too young. With such a grace every sweep reconciles, as before;
  - a store with no reconciliation on record is due: the first sweep after the migration reconciles;
  - a dry run lists when the bucket is due and settles nothing; a listing that fails settles nothing, and the next sweep tries again;
  - what waits for the interval is what no sweep's ledger row knows of ("Known").
- `SweepResult.Reconciled` says whether the sweep reconciled, to the end of its listing. While the bucket is settled `Orphans` is zero
  because nothing was looked for, not because nothing is there; after a listing that failed it is what was deleted before the
  failure.
- **PostgreSQL.** The `sweeps` ledger says of each sweep whether it reconciled (`reconciled`), how many objects it left behind
  (`objects_left`; until now only the sweep's output said that) and, of a reconciliation that ran to its end and left none, when its
  listing began (`settled_from`: the database's clock, read before the first listing; a constraint keeps the column to such rows).
  The sweep reads the rule from the ledger in one statement against the database's clock (`bucketSettled`), so every process that
  sweeps a store agrees, the operator's command and a server's loop alike, and how often the bucket is listed does not grow with how
  many of them run or how often. The statement reads the partial index `sweeps_reconciliation`, which holds only the sweeps the rule
  reads, in its order: a ledger of a sweep a minute costs it nothing. A sweep that reads the ledger needs `SELECT` on it and is
  refused without, before anything is changed; the sweep role has had it since S04-B. The rows the ledger held before the step say
  nothing of the three. A sweep whose ledger row cannot be written fails, also when it had failed already: both are said.
- **The memory store** keeps the newest such entry in a field and applies the same rule. It orders the entries by a count of its own
  and not by its clock, which a test may hold still; the interval is measured by its clock.
- **Who asks for what.** `reporting-migrate sweep` has `--reconcile-every`, and a server's sweep loop `retention.reconcile_interval`
  (seconds; 0 is the default; otherwise 1 to 2,592,000). The default of both is 23 hours (`DefaultReconcileInterval`), an hour short
  of a day, so that a sweep scheduled once a day reconciles every day: the interval is measured from the start of the last listing
  to the moment a sweep asks, and with exactly a day the bucket could be reconciled every other day only, decided by which of two
  runs got to its listing a second sooner. The command prints one of four lines about the objects (docs/OPERATIONS.md): it
  reconciled; the bucket is settled, and no orphan is counted; the sweep failed, with the orphans it had deleted by then; no object
  store was given. The loop logs `reconciled`. The demo's loop reconciles at every sweep: its bucket is a fixture's.

B3 · **The rollout rule, and what enforces it.** A binary older than S25-A does not know a kept result: its worker would publish a kept
job's file with the read's expiry and without its basis (docs/S25A_SCOPE.md). No such binary may work on a store on which a job can
keep its result. Migration `kept_results` enforces most of it: a binary that does not know the step refuses to open a store that has
it, as for every step, and every binary that knows it knows kept results. What the ledger cannot stop is a server that was already
running when `up` was applied; it turns not ready, and the upgrade procedure rolls it at once. docs/OPERATIONS.md "Workers and issue
jobs" says the rule and docs/DEPLOYMENT.md "Upgrade procedure" the step. The older binary's own count read the index the second step
drops: until it is rolled, each admission it still makes scans `assets`.

B4 · **The migrations.** Two steps, each one transaction.

- `kept_results`: the index of B1 and, on `sweeps`, `reconciled boolean NOT NULL DEFAULT false`, `objects_left bigint NOT NULL
  DEFAULT 0 CHECK (objects_left >= 0)`, `settled_from timestamptz` with the constraint `sweeps_settled_from_check` (only a row that
  is not a dry run, is complete, reconciled and left nothing may have it) and the index `sweeps_reconciliation
  (COALESCE(settled_from, at), id) WHERE NOT dry_run AND (settled_from IS NOT NULL OR objects_left > 0 OR NOT complete)`. Its down
  gives back the catalog of the version before exactly, loses no row of a job or an asset, and is refused while a ledger row says
  that a sweep reconciled or left objects, unless `--accept-data-loss`: that record goes with the columns, the counts of what each
  sweep deleted stay.
- `stored_results_index_cleanup`: `DROP INDEX IF EXISTS assets_identity_kind_expires`, and nothing else. Its down builds the index
  again as 009 made it, and fails, changing nothing, when something else stands under that name.
- **Why two.** Dropping an index takes the table's exclusive lock. After `CREATE INDEX` in the same transaction that is a second,
  stronger lock on `assets` asked for while the first is held. A transaction that holds a weaker lock on `assets` and waits to
  write it (a sweep's batch is one statement that does both) then waits for the migration while the migration waits for it, and
  PostgreSQL ends the migration after the whole build. In a step of its own the exclusive lock is the only lock asked for on
  `assets`: it waits, at most the minute every migration waits for a lock, and holds the lock for an instant.
- Neither creates a table, so `roles` is not needed after them. Each step is found by its name wherever the code or a test needs it
  (`keptResultsVersion`, held to the name by a test, which also holds the second step to the place after the first), so that
  renumbering them costs the four file names, the constant, the two catalog files and the rows of the table in docs/OPERATIONS.md.
  They were written as 014 and became 015 and 016 when S24-A's step was merged first.

B5 · **The capacity tool and the plan test.** The tool's seed makes two in fifteen of the assets issued files with nine years left,
inert to the sweep, so that the plan test's identities hold what the count must not read. The stored-results count is held to
`assets_identity_results` by name, for a typical identity and for the one with the long history, and to a plan with no filter on the
payload. The tool's line about the steps it applies names all of them.

## Not in this slice

- **S25-B2, the wire**: `scope_expires_at` in the source protocol (revision 1.2), the `http` adapter, `sourcetest`, the durable
  simulator, the checker's rules, the source's OpenAPI. Until then no deployment keeps a result beyond its read, and a source that
  sent the field is refused by the adapter's strict decoding. One thing for S25-B2 to settle, which follows from B3: the ledger cannot
  stop a server that was running across `up`, so letting a source vouch should be a step of its own after the roll and not something
  a product can start by sending a field. A switch on the connection that accepts a declared scope would make it one.
- **A record of the keys a sweep means to delete.** ADR 0026's amendment named two ways to keep the reconciliation from growing with
  the kept history: a cadence of its own, or keys it can list by age. This slice takes the first. The second would be a table of the
  keys of deleted rows, written in the batch's transaction and worked off by the next sweep without a listing. It would close the
  gap under "Known" (a sweep killed between the rows and the objects), and it would make the rule above unnecessary for the files
  of deleted rows; it costs a table that holds identifiers of business resources where the ledger holds counts only, and a wider
  sweep role. The cadence does not need it: what it leaves open is bounded by the interval.
- `result_expires_at` in the views and the console (S25-C). The documents as a whole (S25-D): this slice changes the sections its
  own behaviour makes untrue (docs/OPERATIONS.md "Quotas", "The sweep and the reconciliation", "Workers and issue jobs" and the
  schema table; docs/DEPLOYMENT.md's reference, "The sweep" and "Upgrade procedure"; docs/API.md "Quotas"; docs/CAPACITY.md; the
  console's document, the manual and the scenarios, for the default).
- The downs of 011, 012 and 013 drop their tables with the rows and ask for no acceptance, unlike 001, 003, 004, 006, 008, 014 and
  `kept_results`. Seen while writing the down rule of this step; not changed here.
- README.md's 现状 still names migration 010.

## Known

- **What waits for the interval** is what no ledger row knows of: at most 23 hours by default, 30 days at the largest interval.
  - The orphan of a publication that failed (the object was put and its row never came). It was removed by the first sweep after
    the grace. It names no row and nobody is served it; it is a file of business data all the same.
  - The objects of a batch whose sweep was killed after the rows were committed and before the objects were deleted, or whose
    ledger row could not be written (that sweep fails and says so). These are files whose rows are gone. A sweep that is stopped
    (its context cancelled: the command turns an interrupt into that) or that fails writes its row, which says that it did not
    complete, so the next sweep reconciles; only a process that dies, or a database that is gone when the row is written, leaves
    no word. `sweep --reconcile-every 0` takes them at once.
- **A bitmap that outgrows `work_mem`.** Where the planner reads `assets_identity_results` through a bitmap, PostgreSQL evaluates the
  index's predicate on rows again only for the pages of a bitmap that went lossy. Measured: at `work_mem` 64 kB a bitmap of 900 pages
  was exact and one of 1,300 went lossy; at 1 MB 12,000 exact and 20,000 lossy; at the default 4 MB 20,000 exact (docs/CAPACITY.md).
  That is one entry of about 64 bytes a page, some 65,000 pages at the default, which only a quota far above 512 allows an identity
  to reach. It costs time on those pages and never changes the count. The plan test asserts that no filter reads the payload; it
  cannot assert how PostgreSQL treats a bitmap it did not build.
- **The count still grows with what it counts**, up to the quota: an identity at a quota of 100,000 has that many index entries
  read at each admission, as before.
- **Sweeps that overlap list the bucket more than once.** A sweep that starts while another is reconciling finds the bucket due as
  well, because the ledger knows of a reconciliation when it has ended, and lists it too. Nothing is deleted twice (a delete of an
  object that is gone is not an error of the object store), and it is what every pair of sweeps did before; but where the listing
  takes longer than the schedule's period, more than one runs at a time, once per interval. One reconciliation at a time would take
  a lock that a sweep holds while it lists; the sweep takes none today (S04-B), and this slice does not add one.
- **A sweep that does not complete costs a reconciliation**: the next sweep lists the bucket, also when the failed one had deleted
  nothing. It is the price of not knowing what a batch whose commit was not answered did. A sweep that is refused before it begins
  (its options, its role, the schema's version) writes no row and costs nothing.
- **Sweeps of one store with different graces.** A reconciliation takes an object no row names only when it is older than its own
  sweep's grace, as before. An object left by a sweep with a shorter grace (one run by hand with `--grace 1h` beside a scheduled one
  with the default) is too young for the next sweep's reconciliation, which settles the bucket all the same; the object goes with the
  first reconciliation whose grace it has outlived, up to an interval later than it would have. With one grace for every sweep, of
  15 minutes or more, what a sweep leaves is old enough for the next one.
- **An interval equal to the schedule's period** can reconcile every other run only (B2); the default avoids it for a daily schedule,
  and docs/OPERATIONS.md says to choose an interval clearly under a multiple of the period.
- **An interval under a microsecond** is no interval for the durable store (the statement counts in microseconds) and one for the
  memory store while its clock stands still. Only a test's clock stands still.
- **The server's loop cannot be told "at every sweep"** in so many words: 0 is the default there. An interval of 1 second does it
  for every sweep that begins more than a second after the last listing did.
- **`stored_results_index_cleanup` waits for the table.** Its exclusive lock on `assets` waits for every transaction that uses the
  table, and while it waits the others queue behind it, for at most a minute; then the step gives up, nothing has changed and `up`
  is run again. The platform's own transactions on `assets` are short.
- The capacity tool reverts the steps after 008 to seed its history, these two among them, and accepts no loss: on a fixture whose
  ledger says that a sweep reconciled or left objects it stops with the step's refusal. A fresh fixture has no such row.
- **One figure of the manual still says 64** (`docs/manual/img/33-system.png`, 能力与环境: 保留结果 64); the manual's text says 512.
  The figures are retaken by `scripts/manual_screens.sh`, and on main that script stops at its seventh figure: it types a company
  id into a field that the company picker replaced (S09-D), so the refusal it waits for never comes. Tried here with the pinned
  Typst and a headless Chromium; no figure was changed.

## Review

Four independent readings of the change, all static, each given one part: the code, the tests, the documents. The first ran out of
resources before it reported and so did two of the later three, so their working notes were read as leads, each checked against
the code and then fixed or answered. What they found, and what was done:

1. **Two sweeps at once.** The rule first ordered the sweeps by when each ended. A reconciliation that was listing while another
   sweep left an object, at a key it had passed, and that ended last, settled the bucket over that object. A reconciliation counts
   from when its listing began (`settled_from`); both stores, a test with a sweep inside a sweep.
2. **A sweep that is cut short.** A batch whose commit was not answered may have deleted its rows, and the sweep's row then counted
   no object left. A sweep that does not complete unsettles the bucket.
3. **A sweep killed between the rows and the objects, or whose ledger row is not written**, leaves no word, and the files wait for
   the interval. Not closed: "Known" says it, and "Not in this slice" what would close it. The sweep now reports a ledger row that
   could not be written also when it had failed for another reason.
4. **An interval equal to the schedule's period** reconciles every other run only. The default is 23 hours, and the least interval
   of the configuration is a second where it was a minute, which could not be under a sweep interval of a minute.
5. **A grace under the least age of an orphan.** A reconciliation skips an object younger than 15 minutes without counting it, and
   settled the bucket over an object the same sweep had just left. Such a sweep settles nothing.
6. **The lock order of the migration.** One step built an index on `assets` and dropped another: a sweep's batch running beside it
   would have been in a deadlock with it, and the migration the one to fail, after the build. Two steps (B4).
7. The step's check read `'\s+'`, which is another pattern where `standard_conforming_strings` is off, and called a name held by
   something that is no index "another definition". A character class, and a message of its own.
8. The command's line for a sweep that failed left out the orphans it had deleted. Its line for a sweep without the object store
   said that any orphan waits for a reconciliation "run with the object store flags", which is true only when the bucket is due.
9. The down of the first version kept whatever stood under the name of 009's index. In the two-step form the down that builds the
   index fails on it instead.
10. Tests that could not fail, or held less than they said: the index's predicate was retyped in the test and asked of the test's
    own rows (it is read from the catalog now, and the store's own count is run); a refused step was held to its version and one
    index only (the whole catalog now); the refusal of a role that cannot read the ledger had no row a sweep would have deleted;
    the largest interval was never accepted anywhere; a listing was only made to fail at its first page; the loop's line was only
    ever seen to say `false`; a subtest left fifty thousand ledger rows behind, and the step's test left the fixture at another
    version when it failed; the rule's table took two readings of the clock for one row and had no two rows at one instant.

## Evidence

- `reporting`: `TestSweepOptionsDefaultsAndBounds` (the interval's zero, its bounds, the two constants, 23 hours and under a day);
  `TestQuotasTakeDefaultsAndRefuseWhatIsOutOfBounds` (512).
- The quota contract on both stores (`TestMemoryQuotaContract`, `TestDurablePostgresContract/quotas/quota_contract`): at the limit
  of the stored results a freeze and an export are refused and an issue is admitted; two issued files and their records hold no
  slot, an export file holds one, the limit is exact before and after an issue runs at it; the default.
- The lifetime contract on both stores (`TestMemoryLifetimeContract`, `TestDurablePostgresContract/lifetimes`):
  `a_kept_export_holds_its_slot_of_the_stored_results_and_an_issued_file_holds_none` (under a scope: the working snapshot's slot is
  free when its read ends, the kept export still holds its own, the issued file kept beyond the read never held one, and both files
  are still served).
- The object-store contract on the memory store (`TestMemoryObjectContract`) and on PostgreSQL
  (`TestDurablePostgresContract/object_storage`, with the in-memory object store and with a real S3-compatible server), five
  subtests:
  - `the_bucket_is_reconciled_when_it_is_due_and_not_at_every_sweep` counts the listings asked of the object store: the first sweep
    reconciles; within the interval a sweep and a dry run ask for none and say so; after the interval a dry run lists and settles
    nothing and the sweep after it reconciles; without an interval every sweep does; a listing that fails settles nothing; a sweep of
    the rows alone is not taken for a reconciliation; a delete that fails, a reconciliation that leaves an object and a sweep of the
    rows alone that leaves one each make the next sweep reconcile within the interval; an interval out of bounds is refused with
    nothing swept and nothing listed, and the largest one is accepted.
  - `a_listing_that_fails_on_a_later_page_is_not_a_reconciliation`: a bucket of one key a page; the first page's orphan is deleted,
    the second listing fails; the result does not say reconciled and counts the one; the next sweep lists from the start.
  - `a_reconciliation_does_not_settle_what_another_sweep_left_while_it_was_listing`: two sweeps at once. While a reconciliation is
    deleting an orphan, a sweep of the rows alone deletes the row of an export the listing had found named, and leaves its object.
    The reconciliation ends last and left nothing; the next sweep reconciles within the interval and takes the object; the one after
    it does not list.
  - `a_reconciliation_with_a_grace_under_an_orphans_least_age_settles_nothing`: three sweeps with a grace a second under the floor
    each reconcile; with the floor itself the first reconciles and the next two, of either grace, do not.
  - `a_sweep_that_does_not_complete_makes_the_next_one_reconcile`: a sweep is stopped right after it deleted a row and its object,
    with nothing left that it knows of; the next sweep reconciles although the bucket was settled an hour before.
- PostgreSQL: `the_ledger_says_what_each_sweep_did_about_the_bucket_and_the_sweep_reads_it_by_its_index` (the ledger rows of seven
  sweeps, of which only the reconciliations that left nothing say since when the bucket is settled, with a time not after their
  row's; the rule on eighteen ledgers written for it, among them a reconciliation that began before another sweep left objects and
  ended after it, one that began before the interval and ended within it, sweeps that did not complete, and two rows at one
  instant in either order; four rows the ledger's constraint refuses; among fifty thousand sweeps the statement reads
  `sweeps_reconciliation` and does not scan the ledger); `TestWhetherTheBucketIsDueIsTheLedgersWordByTheDatabasesClock` and
  `TestAdmissionCountsByTheDatabasesClockUnderTheIdentitysLock` (the two statements are written with their indexes' predicates and
  take no time from the caller; the time a listing began is the database's, read before the listing; which sweeps say that the
  bucket is settled); `application_role_reads_and_writes_rows_and_nothing_else` (a sweep role whose `SELECT` on the ledger was taken
  away is refused, a dry run too, before a row is deleted, the bucket listed or the ledger written, and is told what it lacks;
  without an interval or without the object store it sweeps; `roles` repairs it).
- The migrations: `kept_results_step_adds_the_index_of_the_stored_results_and_gives_the_ledger_its_word_on_reconciliations` (a store
  at the version before with rows: the catalogs of both versions as committed, no row lost, the old ledger rows say none of the
  three, what the index holds read from the catalog and what the store's own statement counts, the down refused for either word
  with the store unchanged and accepted with the loss, the index built beforehand accepted and not rebuilt, nine other definitions
  under the name, a table under the name, an index a failed build left invalid and an invalid one with the step's own definition
  refused, each with the catalog as it was); `stored_results_index_cleanup_step_drops_the_index_of_009_and_its_down_builds_it_again`
  (the one line of the catalog that goes; no row lost; the step on a store whose index was dropped already; the down building 009's
  index, and failing with nothing changed while another index stands under the name); the round trip of every version;
  `TestMigrationSetRules` (the constant is the step of that name, the step after it is the cleanup and only drops the index, and
  `kept_results` drops no index).
- The plans: `capacity_plans` (the count reads `assets_identity_results` for both identities and filters no payload; before 009 it
  cannot use either index).
- `cmd/reporting-migrate`: the flag's refusals; against PostgreSQL and the S3-compatible server: the sweep without the object store
  flags records the object it left and prints its line, and the sweep with them reconciles; then the bucket is settled, and a sweep
  and a dry run say so and count no orphan, with the default and with the largest interval; `--reconcile-every 0` and an interval
  already past reconcile, and each such row says since when; the sweep role does the same with the privileges it has; a sweep that
  fails prints the line of a failed sweep.
- `server`: the field's default and values (1 second to 30 days) and three refusals, one a number that wraps around; the loop asks
  every sweep for the configured interval, and on a store with an object store its first line says `reconciled` true and its second
  false; DEPLOYMENT's reference lists the field.
- The console's DOM test against the real demo reads 512 from `/capabilities`.
- Mutations: 76 changes that each break one rule of this slice (the count takes the issued files, an issue is refused at the limit,
  a dry run or a failed listing settles the bucket, a sweep that left objects is forgotten, a reconciliation counts from its end, a
  sweep that did not complete or one with a short grace settles, the ledger does not record, the interval's bounds and defaults, the
  command's lines, the steps' checks, their downs, the index dropped in the step that builds one, and so on) were made one at a
  time in a copy of the tree, the SQL of the two steps included; for each, the test named for the rule passes unchanged and fails
  with the rule broken.
