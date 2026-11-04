# Capacity: what was measured, on what, and what it does not show (S04-G)

This document holds the numbers `scripts/capacity_run.sh` printed on one machine. **They are not a production sizing.** They say how the
store's hot paths behave as the `jobs` and `assets` tables grow on that machine, before and after the indexes of migration 009, and they let an
operator repeat the measurement on their own hardware with the same tool. Everything below is copied from the saved raw output
(`capacity_full.txt`, `capacity_rowsizes.txt`, `capacity_growth.txt` in the run's scratch directory); nothing is extrapolated.

## The machine and the run

- Windows Subsystem for Linux 2, kernel 6.18.54.1-microsoft-standard-WSL2, Intel Core Ultra 9 285K, 20 logical CPUs, 65,845,080 kB memory.
- PostgreSQL 16.14 in a Docker container (`reporting-s03g-pg`) on loopback, no TLS, default configuration: `shared_buffers=128MB`, `work_mem=4MB`,
  `fsync=on`, `effective_cache_size=4GB`. The tool turns off parallel query for its own sessions (`max_parallel_workers_per_gather=0`) because the
  container's `/dev/shm` is 64 MB, which a parallel plan over a million rows exceeds.
- The platform's Go code ran on the same host and talked to the database over loopback. The object store for `verify-objects` was a MinIO container
  on loopback, plain HTTP.
- Command (the fixture database of the other tests; the tool refuses any other database and port 55432):

```bash
REPORTING_S01_TEST_DSN='postgresql://postgres@127.0.0.1:55433/reporting_test_s01?sslmode=disable' \
REPORTING_OBJECT_STORE_TEST_ENDPOINT=http://127.0.0.1:59000 REPORTING_OBJECT_STORE_TEST_ACCESS_KEY=... REPORTING_OBJECT_STORE_TEST_SECRET_KEY=... \
scripts/capacity_run.sh 2>&1 | tee capacity_full.txt          # the tables below
scripts/capacity_run.sh -phases none 2>&1 | tee capacity_rowsizes.txt   # row sizes of the seed only, after VACUUM FULL
scripts/capacity_run.sh -growth 2>&1 | tee capacity_growth.txt          # G4: the real service writes the rows
```

  Defaults: sizes 10,000 / 200,000 / 1,000,000 job rows, assets at 50% of them, 1,000 identities in 10 tenants, identity 0 holding 10% of the rows,
  history spread over 90 days, 300 operations per case, 25 admissions per writer, 2,000 expired rows for the sweep, 2,000 objects for `verify-objects`.
  The roles used for the measurements are the application role (SELECT, INSERT, UPDATE on `jobs` and `assets`) and the sweep role, created for the
  run and dropped at its end, so that row-level security applies as in production. The tool seeds with SQL (`generate_series`) as the fixture's
  administrator, reverts migration 009 for the "before" phase, applies it for "after" (the four indexes are built over the same rows), and removes what it seeded.

## What the numbers do not show

- One host: client, database and object store share CPU and memory; there is no network between them, so a round trip costs microseconds where a
  deployment's costs a millisecond or more, and every operation below would pay that on top.
- No TLS, no connection pooler, one default-configured PostgreSQL; a warm cache (the data of every size fits in the 62 GB the host has; the first
  pass over a table was not separated from the later ones).
- Synthetic rows: the payloads are the same 1.3 KB job payload (the financial fixture) and ~0.5 KB assets with a 128-byte file; real results are
  larger (G4 below). Identities and tenants are spread evenly except for identity 0; the number of tenants matters for the "before" figures,
  because the old plans read the tenant prefix of the unique key: with one tenant the old numbers would be much worse, with a tenant per identity
  much better.
- Only the stores' own statements, one process at a time (up to 32 writers); no HTTP, no source product, no workers executing jobs, no autovacuum control:
  autovacuum ran when it chose to, and a table just bulk-loaded is not a table in steady state.
- Cases of 300 operations (25 per writer for the parallel cases, 9 claims at 10,000 rows because only 18 jobs were queued): a p99 of such a case
  is its second-worst sample.
- The sweep could be measured only at version 9 (the binary refuses a store below it), so its "before" is the sweep without the fix on a version-9 store, from the first run.

## Results

Latency of one operation in milliseconds as p50 / p95 / p99, and throughput as operations per second over the case's wall time (for parallel cases,
all writers together). "Long history" is identity 0, holding 10% of the rows (1,000 / 20,000 / 100,000 jobs). The `n` is the number of operations.

#### 10,000 job rows

| Case | n | before 009: p50 / p95 / p99 (ms) | ops/s | after 009: p50 / p95 / p99 (ms) | ops/s |
|---|---|---|---|---|---|
| admit, identity with a long history | 300 | 3.50 / 4.24 / 4.82 | 279.8 | 2.51 / 3.30 / 3.80 | 380.1 |
| admit, typical identity | 300 | 3.18 / 4.03 / 4.26 | 308.7 | 2.48 / 3.22 / 3.51 | 387.7 |
| admit, fresh identity (no history) | 300 | 3.02 / 3.91 / 4.68 | 323.5 | 2.22 / 2.89 / 3.29 | 437.3 |
| issue listing (25), long history | 300 | 2.00 / 3.39 / 4.69 | 449.6 | 1.09 / 1.32 / 2.03 | 888.7 |
| issue listing (25), typical identity | 300 | 0.70 / 0.86 / 0.94 | 1390.5 | 0.80 / 0.98 / 1.12 | 1238.1 |
| job by id | 300 | 0.72 / 0.88 / 1.04 | 1360.3 | 0.68 / 0.95 / 1.13 | 1414.6 |
| artifact (asset) by id | 300 | 0.70 / 0.85 / 0.99 | 1398.2 | 0.64 / 0.79 / 0.94 | 1547.9 |
| claim (deep non-queued history) | 9 | 2.32 / 12.04 / 12.04 | 305.3 | 1.87 / 12.36 / 12.36 | 328.5 |
| admit, 1 writer(s), one hot identity | 25 | 3.78 / 4.84 / 4.87 | 256.9 | 2.56 / 3.19 / 3.39 | 378.9 |
| admit, 8 writer(s), one hot identity | 200 | 25.66 / 31.47 / 45.89 | 298.2 | 18.29 / 22.97 / 25.54 | 425.3 |
| admit, 8 writer(s), one identity each | 200 | 4.06 / 5.06 / 5.17 | 1874.4 | 3.39 / 4.35 / 4.41 | 2203.9 |
| admit, 32 writer(s), one hot identity | 800 | 112.97 / 143.73 / 156.86 | 262.3 | 89.95 / 101.65 / 103.15 | 356.8 |
| admit, 32 writer(s), one identity each | 800 | 6.76 / 10.95 / 14.91 | 4189.4 | 5.43 / 9.13 / 12.43 | 5431.5 |

#### 200,000 job rows

| Case | n | before 009: p50 / p95 / p99 (ms) | ops/s | after 009: p50 / p95 / p99 (ms) | ops/s |
|---|---|---|---|---|---|
| admit, identity with a long history | 300 | 28.75 / 32.39 / 36.23 | 34.4 | 4.74 / 5.63 / 6.43 | 207.5 |
| admit, typical identity | 300 | 19.45 / 22.07 / 24.34 | 51.1 | 2.60 / 3.35 / 3.75 | 367.5 |
| admit, fresh identity (no history) | 300 | 17.24 / 19.98 / 22.78 | 57.2 | 2.59 / 3.40 / 3.81 | 375.9 |
| issue listing (25), long history | 300 | 20.89 / 22.37 / 22.57 | 47.7 | 20.46 / 21.92 / 22.89 | 48.4 |
| issue listing (25), typical identity | 300 | 1.11 / 1.38 / 1.52 | 882.4 | 1.17 / 1.42 / 1.72 | 846.1 |
| job by id | 300 | 0.85 / 1.04 / 1.31 | 1156.0 | 0.82 / 1.00 / 1.25 | 1203.1 |
| artifact (asset) by id | 300 | 0.78 / 0.95 / 1.10 | 1260.4 | 0.80 / 1.00 / 1.35 | 1232.9 |
| claim (deep non-queued history) | 100 | 2.40 / 4.63 / 5.90 | 382.7 | 2.50 / 3.31 / 4.29 | 388.3 |
| admit, 1 writer(s), one hot identity | 25 | 36.77 / 38.59 / 41.23 | 27.8 | 4.94 / 5.58 / 5.63 | 199.4 |
| admit, 8 writer(s), one hot identity | 200 | 244.56 / 266.99 / 275.51 | 32.7 | 36.76 / 42.94 / 50.17 | 213.3 |
| admit, 8 writer(s), one identity each | 200 | 25.10 / 27.62 / 29.19 | 317.0 | 3.73 / 4.60 / 4.68 | 2059.4 |
| admit, 32 writer(s), one hot identity | 800 | 1007.06 / 1081.03 / 1098.70 | 32.0 | 153.74 / 162.34 / 164.30 | 207.7 |
| admit, 32 writer(s), one identity each | 800 | 37.51 / 53.09 / 63.46 | 790.6 | 4.97 / 7.73 / 9.30 | 5826.4 |

#### 1,000,000 job rows

| Case | n | before 009: p50 / p95 / p99 (ms) | ops/s | after 009: p50 / p95 / p99 (ms) | ops/s |
|---|---|---|---|---|---|
| admit, identity with a long history | 300 | 177.39 / 188.99 / 200.57 | 5.6 | 13.61 / 15.03 / 16.61 | 73.6 |
| admit, typical identity | 300 | 83.59 / 89.98 / 110.41 | 11.8 | 2.80 / 3.54 / 4.33 | 348.1 |
| admit, fresh identity (no history) | 300 | 79.26 / 83.70 / 86.74 | 12.6 | 2.33 / 3.08 / 3.91 | 405.0 |
| issue listing (25), long history | 300 | 125.99 / 132.06 / 134.28 | 7.9 | 124.95 / 149.05 / 169.12 | 7.9 |
| issue listing (25), typical identity | 300 | 1.86 / 2.17 / 2.35 | 530.5 | 1.80 / 2.03 / 2.19 | 552.7 |
| job by id | 300 | 0.74 / 0.88 / 0.94 | 1323.8 | 0.76 / 0.94 / 1.11 | 1303.9 |
| artifact (asset) by id | 300 | 0.74 / 1.00 / 1.50 | 1284.0 | 0.71 / 0.91 / 1.12 | 1385.4 |
| claim (deep non-queued history) | 100 | 3.72 / 4.78 / 18.04 | 253.4 | 3.66 / 4.62 / 7.27 | 261.7 |
| admit, 1 writer(s), one hot identity | 25 | 170.50 / 179.35 / 181.88 | 5.9 | 12.90 / 14.43 / 14.57 | 76.5 |
| admit, 8 writer(s), one hot identity | 200 | 1306.28 / 1377.49 / 1403.75 | 6.3 | 103.22 / 109.74 / 112.09 | 77.6 |
| admit, 8 writer(s), one identity each | 200 | 132.55 / 139.73 / 141.90 | 61.3 | 4.19 / 6.44 / 7.61 | 1735.9 |
| admit, 32 writer(s), one hot identity | 800 | 5318.35 / 5868.66 / 6628.54 | 6.0 | 416.34 / 432.90 / 439.05 | 76.4 |
| admit, 32 writer(s), one identity each | 800 | 253.49 / 317.88 / 348.58 | 122.2 | 4.72 / 8.21 / 10.20 | 5902.8 |

Index build (migration 009, its four indexes, over the history, inside its transaction): 0.05 s at 10,000 job rows, 0.82 s at 200,000, 4.41 s at 1,000,000.
`CREATE INDEX` holds a lock that blocks writes to the table for that time (docs/OPERATIONS.md "Capacity").

### Sweep and verification

One sweep over 2,000 expired artifacts and their 2,000 jobs (batch 500, as the sweep role, grace 24 h), and `verify-objects` over 2,000 rows and objects:

| Size | Sweep before the fix | Sweep after the fix | `verify-objects`, presence | `verify-objects --deep` |
|---|---|---|---|---|
| 10,000 job rows | 4.77 s (839 rows/s) | 0.19 s (20,789 rows/s) | 0.04 s | 0.98 s (2,036 rows/s) |
| 200,000 | 95.36 s (42 rows/s) | 2.05 s (1,949 rows/s) | 0.07 s | 0.97 s (2,058 rows/s) |
| 1,000,000 | 104.23 s (38 rows/s) | 1.59 s (2,508 rows/s) | see the raw output | 1.17 s (1,714 rows/s) |

("Rows" are artifacts deleted plus jobs reduced. "Before the fix" is the sweep of the first run, `capacity_full_before_sweepfix.txt`; it exists only
after 009 because the binary refuses a store below version 9. A second run at 200,000 and 1,000,000 rows gave 1.89 s and 2.16 s, so the after figures
vary by about a quarter between runs. `verify-objects` is unchanged by the fix; the figure before 009 is in the raw output.)

**The sweep was slow, and why.** `EXPLAIN (ANALYZE, BUFFERS)` of the sweep's statements at 200,000 job rows with 2,000 expired artifacts and jobs
(sweep role, worker scope set in the transaction, as the sweep runs them; `sweep_plans_before.txt` and `sweep_plans_after.txt`):

| Statement | Before the fix | After the fix |
|---|---|---|
| `deleteArtifacts` (per batch) | 39,412.8 ms | 399.0 ms |
| `deleteSnapshots` | 0.3 ms | 0.5 ms |
| `reduceJobs` (per batch) | 1,611.8 ms | 252.5 ms |
| dry-run `countSweep` | 36,315.1 ms | 1,234.1 ms |

- **The dominant node was in `deleteArtifacts`, not in `reduceJobs`.** The artifact's snapshot probe, `NOT EXISTS (SELECT 1 FROM assets s WHERE s.kind = 'snapshot'
  AND s.id = a.data->>'SnapshotID' AND (s.expires_at IS NULL OR s.expires_at >= c.cutoff))`, was planned as a nested-loop anti join whose inner side was a bitmap scan
  of every snapshot not yet past its grace (50,000 rows) **re-run for each of the 2,000 candidate artifacts**: 100,000,000 rows removed by the join filter, 19.8 million buffer
  hits, 39 s. Without row-level security the same predicate is a hash anti join (checked by hand as the administrator). With it, the join clause compares a column with
  `a.data->>'SnapshotID'`, a function that is not leakproof, and PostgreSQL does not use such a clause as an index or hash condition on a table under a security barrier,
  so it was applied as a filter. This came in with S04-C and was invisible at the sizes of the sweep tests.
- **The hypotheses of the review:** that `reduceJobs` sorts the whole table was **rejected**: it reads `jobs` with a `Seq Scan` (199,267 rows, 1.37 s of the 1.6 s) and sorts nothing
  (0 rows survive the filters before the artifacts are gone); the filtering on `data ? 'Reduced'`, `jsonb_typeof` and the state is what makes that scan cost 1.4 s a batch, and
  it is a real cost, per batch, growing with the table. That the probes on `assets` by id are cheap was **confirmed** for `reduceJobs` (primary-key lookups) and **rejected** for the
  artifact's snapshot probe above.

**The fix** (two small changes, the rules of S04-B untouched):
1. In `deleteArtifacts` and the dry run, the snapshot id is taken in a `LATERAL (SELECT a.data->>'SnapshotID' AS sid OFFSET 0)` subquery, so that the probe compares two
   plain columns (`s.id = a.sid`) and is answered from `assets_pkey`: 2,000 lookups per batch. The condition is the same text with the same meaning; `countSweep`
   shares it, and the existing tests that assert the dry run equals the sweep pass.
2. Migration 009 gains a fourth index, `jobs_reducible ON jobs (source_expires_at) WHERE state IN ('succeeded','failed','cancelled') AND NOT (data ? 'Reduced')`: the finished,
   unreduced jobs by the expiry of their own source read. `reduceJobs` and the dry run read it instead of scanning `jobs`; a job leaves it when it is reduced, so the history
   already reduced is never walked. It is 1.4 MB at 200,000 job rows and 7.0 MB at 1,000,000 (the index sizes line of a run of the tool; the first run did not print it). It has the same
   `IF NOT EXISTS` and definition check as the other three.

**What did not improve.** The sweep still costs more as the table grows: 0.19 s at 10,000 rows, 2.05 s at 200,000 (1.59 s and 2.16 s at 1,000,000 in two runs). What remains, in the plans
after the fix, is the probe of each candidate artifact against the queued and running jobs (`jobs_claim`, 733 rows scanned per candidate at 200,000: about 0.34 s of the 0.40 s per
batch of `deleteArtifacts`), whose cost is the candidates times the jobs in flight; and the dry run (1.2 s) reads the same. It is linear in the in-flight jobs and bounded by the
quotas, and was not changed. The bulk jobs of this seed are never reducible (their source reads expire in a year), so the index holds the 2,000 that are; a table in which
many jobs have a past expiry measures differently (its cost is the batch).

### What changed, and what did not

- **Admission** (three counts and the insert under the identity's lock): at 1,000,000 rows from 165.87 ms to 17.46 ms (p50) for the identity with the long
  history, from 81.40 ms to 2.70 ms for a typical identity, 75.49 ms to 2.37 ms for a fresh one. The "before" cost grows with the table (about 6.4 ms, 16.6 ms and 75.5
  ms for a fresh identity at 10,000, 200,000 and 1,000,000 rows); the "after" cost does not (2.22, 2.26, 2.37 ms). The long-history identity stays slower than
  the others because its own counts read 100,000 jobs and 50,000 assets worth of index entries.
- **Parallel writers.** One identity serialises on its advisory lock (S04-D, by design): at 1,000,000 rows, 32 writers on the hot identity complete about 76
  admissions per second (before: about 6) and a request waits 420 ms at the median (before: 5.3 s) because each admission of that identity holds the lock for 13-17 ms. Writers on different identities do not
  wait for each other: 5,765 operations per second with 32 writers (before: 123).
- **Issue listing.** For a typical identity nothing changed (the unique key already serves it). For the identity with 20,000 issue jobs it did **not** improve
  (118 ms before, 122 ms after, 1,000,000 rows): the planner keeps the unique key index and sorts 20,000 rows. Its estimate is 91 rows against 20,000 real ones, and so it does not choose
  `jobs_identity_created`, which would find 25 newest issue jobs in a few hundred entries. At 30,000 rows the same statement does use the new index
  (the plan test logs both). Reported, not forced.
- **Reads by id, Claim**: unchanged within noise, as expected (primary key and `jobs_claim`).

### Plans

The plan test (`TestPostgresMigrations/capacity_plans_*`, as the application role, the identity's context set, 30,000 jobs and 15,000 assets analyzed)
logs and asserts them. Before 009 the stored-results count is a `Seq Scan` on `assets` and the other statements use `jobs_claim` or the unique key. After:

| Statement | Plan after migration 009 |
|---|---|
| jobs in flight | Index Only Scan `jobs_identity_in_flight` |
| stored results | Index Only Scan `assets_identity_kind_expires` (a bitmap scan of it for the identity with the long history) |
| admissions in the last hour | Index Only Scan `jobs_identity_created` (a bitmap scan of it for the long history) |
| issue listing | typical identity: Index Scan on the unique key; long history at 30,000 rows: Index Scan `jobs_identity_created`; long history at 1,000,000 rows: Bitmap Heap Scan on the unique key and a sort of 20,000 rows (150 ms) |
| job listing (S07-C, `GET /jobs`, 50) | the issue listing's statement without `kind='issue'`: at 30,000 rows an Index Scan of `jobs_identity_created` for both identities (the plan test logs it; held to the issue listing's rule, no sequential scan); not measured at a million rows |

The sweep's statements (`TestPostgresMigrations/capacity_plans_*/the_sweeps_statements`, as the sweep role in the worker scope, 2,000 expired artifacts and jobs under 30,000 jobs) are held to the same rule and the reduction must read `jobs_reducible`; their plans are logged. No sequential scan of `jobs` or `assets` remains in any of them. The row-level policies add their predicates to the plans and the indexes are used
with them in force.

## Growth (G4)

Bytes, from the real service writing the rows (`-growth`), with `pg_column_size` of the whole row (the stored size of the row's columns; a heap
tuple adds about 24 bytes of header and alignment), for a result set of 3, 100 and 1,000 rows of three columns:

| Result rows | Where the file is | Job at admission | Job finished | Job as retention reduces it | Snapshot row | Artifact row | of which `data` | The .xlsx itself |
|---|---|---|---|---|---|---|---|---|
| 3 | inline in the row | 1641 | 1681 | 811 | 1656 | 11128 | 10898 | 7887 |
| 3 | in the object store | 1641 | 1681 | 811 | 1664 | 688 | 464 | 7888 |
| 100 | inline in the row | 1641 | 1681 | 810 | 2760 | 13680 | 13447 | 9797 |
| 100 | in the object store | 1641 | 1681 | 807 | 2760 | 696 | 466 | 9797 |
| 1000 | inline in the row | 1649 | 1681 | 807 | 9872 | 23984 | 23750 | 26914 |
| 1000 | in the object store | 1649 | 1681 | 809 | 9872 | 696 | 471 | 26915 |

Reading: a job row is about 1.6 KB at admission, whatever the result, and about 0.8 KB once retention has reduced its payload (the row stays: the platform keeps one
`jobs` row per admission forever, by design, as the audit of what happened). A snapshot row grows with its result: 1.7 KB for 3 rows, 2.8 KB
for 100, 9.9 KB for 1,000 (bounded by `MaxResultBytes`, 1 MiB). An artifact row with the file inline is the file in base64 (11-24 KB for these files, bounded by
`MaxArtifactBytes`, 8 MiB), and with the object store about 0.7 KB whatever the file: the bytes are in the bucket. Snapshots and artifacts are deleted by
retention after their expiry; jobs are not.

Seeded tables (`capacity_rowsizes.txt`, after `VACUUM FULL`, table and toast): 1,641 bytes per job row at 10,000 rows, 1,716 at 200,000 and 1,778 at 1,000,000
(a 1.3 KB payload kept inline, plus the columns, the tuple header and unused page space); 745 to 752 bytes per asset row. Index sizes at 1,000,000 job rows
and 500,000 assets: `jobs_pkey` 77.9 MB, the unique key 157.9 MB, `jobs_claim` 47.1 MB, **`jobs_identity_created` 50.2 MB, `jobs_identity_in_flight` 0.16 MB**,
`assets_pkey` 47.3 MB, `assets_expires_at` 4.1 MB, **`assets_identity_kind_expires` 6.3 MB**, **`jobs_reducible` 7.0 MB** (from a later run of the tool); the table `jobs` is 1,961.8 MB before
and after (the new indexes add 50.4 MB, 2.6% of the table's heap). At 200,000 rows the two new `jobs` indexes are 10.4 MB and 0.10 MB, and at 10,000 rows 1.1 MB and 0.05 MB.

### Rows per year at the default quotas

An identity may admit at most `admissions_per_hour` = 120 jobs an hour, so a single identity can add at most 120 x 24 x 365 = 1,051,200 job rows a year, and N
identities saturating the limit around the clock N times that: an upper bound, not a forecast. A forecast is identities x jobs per identity per day x 365:
for example 1,000 identities each admitting 20 jobs a working day, taking 260 days, is 5.2 million rows a year (7.3 million at every day of the year). At the measured
1.64 to 1.78 KB a job row plus about 0.33 KB of index entries per row (the five indexes of `jobs` at 1,000,000 rows sum to 333 MB), that is roughly 2.0 KB a row: about 10 GB
for 5.2 million rows, before any reduction; reduced rows are about half the payload but the file does not shrink until the pages are reused or the table rewritten.
The `assets` table does not grow without bound: its rows are deleted by the sweep after their expiry and the grace.

### When the `jobs` table is too big

The platform does **not** prune or archive `jobs` (nothing in S04-G does): the rows are the audit of what was admitted, and a deletion rule would be a
decision about audit that this slice does not take. An operator whose table is too big archives by `created_at` out of band: copy the rows older than a
cutoff to a separate table or a dump, check the count, and delete them as the migrator in batches; no row of a job that is queued or running, and none that an
unexpired snapshot or artifact still names, may go. Idempotency keys die with their rows: a replay of an archived key is a new admission. Do it as the migrator
role, not the application role (which cannot delete). Partitioning by `created_at` is the structural answer and is not done here.

## To measure on your hardware

Run the tool against a copy of your configuration's PostgreSQL (the guard allows only the fixture database names, so use a throwaway cluster with
`reporting_test_s01`), with `-sizes` at your expected table sizes and the number of tenants and identities you expect (`-tenants`, `-identities`). Look
first at the admission p99 for the identity with the longest history and at the parallel cases for one identity, then at the sweep. The figure to size the pool
from is the number of writers you expect on one identity times your latency to the database: they queue holding connections.

## Raised template limits (S06-B part 1, B3)

`MaxItems` rises from 64 to 128 and `MaxBandItems` from 24 to 96, so that the MAWB sample (72 fields in one band) and the B/L (52) fit. The proofs that
depend on them were re-measured on the worst template the new limits allow (`TestTypstTheRaisedLimitsRenderCheckAndIssueWithinTheBudget`, the pinned Typst
and fonts, one machine, WSL2, three runs): 128 elements, 96 of them in the document header, whose first is a text of the issue number across the band and the
rest are fields and literals in a 3-column grid; a page header of 25 lines; a document footer with a Code 128 and five QR codes (level H) of the issue number;
a table of 40 rows over two columns.

| Step | Measured | Bound |
|---|---|---|
| Preview (`RenderWith`, layout and Typst, 3 pages, 42.9 KB) | 52-54 ms | 15 s (the render timeout `defaultTimeout`; a preview waits for a slot inside it) |
| Pre-issue check over the widest legal number (`CheckIssueWith`, fonts only) | 6.1-6.9 ms | the worker's check before the product is asked for a number |
| Issue with the widest legal number, 32 characters of the widest glyph (`IssueWith`) | 61-71 ms | the issue job's 15 s render |

The worst-case proof itself is per element and does not depend on how many there are: a text, a Code 128 or a QR code that shows the issue number is proved
for the widest (or longest) number its box can be given, which `TestTypstIssuesTheWidestLegalNumbers` sweeps to the tightest box the check accepts; that test is
unchanged and passes. With 128 elements the check adds the elements' own cost only (about 6 ms here), so the old limits were not what the budget was spent on.

### The 37 samples of the pack (S06-B part 2, B7)

`TestTypstTheSamplePackRendersWithinTheBudgetAndTheHeaviestAreMeasured` renders each converted sample five times from its own rows (the preview route's
path, `RenderWith`: layout and Typst) and times the pre-issue proof (`CheckIssueWith`, fonts only); same machine, pinned Typst and fonts, WSL2. The heaviest
by render time (best of five, then the worst of the five), and the extremes of the rest:

| Sample | Elements | Pages | Preview, best / worst of 5 | PDF | Pre-issue check |
|---|---|---|---|---|---|
| `proforma` (形式发票) | 57 | 1 | 54.7 / 61.0 ms | 47.9 KB | 0.6 ms |
| `commercial` (商业发票) | 57 | 1 | 52.8 / 55.0 ms | 47.4 KB | 1.8 ms |
| `bol` (海运提单, 52 fields, a form) | 52 | 1 | 48.1 / 50.2 ms | 42.2 KB | 0.7 ms |
| `origin` (原产地证书) | 35 | 1 | 43.7 / 44.7 ms | 38.6 KB | 0.5 ms |
| `export_packing` (出口装箱单) | 44 | 1 | 43.5 / 47.8 ms | 41.1 KB | 0.6 ms |
| `order` (销售订单, 28 rows) | 25 | 2 | 39.1 / 42.6 ms | 53.6 KB | 0.5 ms |
| `mawb` (72 fields, a form) | 72 | 1 | 40.5 / 43.3 ms | 32.7 KB | 0.6 ms |
| the 4 WMS labels (lightest, forms) | 17 | 1 | 26.6-27.2 / 28.2-28.8 ms | 19.9-20.1 KB | 0.2 ms |

All 37 render in 27-61 ms and are proved in about 2 ms or less: against the 15 s budget (and the 5 s the test itself allows a preview) none comes near it, so no sample was
converted with fewer elements for the budget (a sample's elements are dropped only where the platform's geometry forbids them, and each is in its record:
none of the nine forms (the two waybills, the B/L, the two parcel labels, the four WMS labels) loses an element). The largest sample by element count is the MAWB (72: its 72 fields; a form has no table), under the template limit of 128 and
the band limit of 96; the heaviest by time are the two 57-element invoices. The 128-element worst case above stays the bound; the samples are well inside it.

The per-identity template cap rises from 16 to 64 (docs/OPERATIONS.md): the file of an identity grows with it, bounded by the store's 8 MiB. A template of 128
elements is about 25-30 KB canonical (64 KiB is the bound of one), so 64 templates of one version each is under 2 MB; the 20 versions a template may keep are what
can reach 8 MiB first.
