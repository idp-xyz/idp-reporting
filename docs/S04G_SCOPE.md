# S04-G capacity: indexes, guards and measured numbers

2026-11-02. Until now nothing measured how the platform behaves as its tables grow, and a few paths are unbounded.
Reading the schema and the code: `jobs` has the idempotency unique key (tenant, integration, issuer, subject, kind,
idem_key) and `jobs_claim (state, created_at)`; `assets` has its primary key and `assets_expires_at`. The three counts
of S04-D (jobs in flight, stored results, admissions in the last hour) filter on (tenant, issuer, subject), which no
index leads with, so every admission scans; the stored-results count scans every asset of every tenant; the issue
listing orders an identity's jobs by `created_at` with no index for it. The server has timeouts but no cap on
requests in flight, and `POST /queries` and the row reads are the only expensive paths with no per-identity bound
(S04-D left them here). The jobs table is never pruned (S04-B reduces payloads, keeps rows), so it only grows. This
slice indexes what is scanned, bounds what is unbounded, and measures, with honest limits on what the numbers mean.

G1 · Indexes, migration 009 `capacity_indexes` (ledger, checksum, one transaction, reversible; catalog_009;
003–008 byte-identical):

- `jobs (tenant, issuer, subject, created_at DESC)`: the hour count and the issue listing;
- `jobs (tenant, issuer, subject) WHERE state IN ('queued','running')`: jobs in flight, tiny whatever the history;
- `assets (tenant, issuer, subject, kind, expires_at)`: stored results (and any per-identity read).

`CREATE INDEX` in the migration's transaction blocks writes to the table while it builds: OPERATIONS.md says so and
gives the out-of-band alternative (`CREATE INDEX CONCURRENTLY` with the same names and definitions before running
`up`, which then finds nothing to build: write the migration with `IF NOT EXISTS` so that this works, and test it).
`down` drops them. The row-level policies (S04-C) add predicates the planner must still use these indexes with: prove
it in the plans (G2).

G2 · Plans and numbers. A seeding tool and a measurement run, in `internal/capacity` (a test-only/`internal` package, not an
exported API) driven by `scripts/capacity_run.sh` and, for the plan assertions, by a gated subtest. Seeding uses SQL
(`generate_series`) as the fixture's admin connection on the guarded fixture database: N identities (default 1000)
with jobs and assets spread over them, sized by flags (defaults 10k / 200k / 1M job rows; assets in proportion),
then `ANALYZE`. Measured, as p50/p95/p99 and throughput, at each size, before 009 and after:

- admission (`Repository.Admit` with the quota counts) for one identity with a long history and for a fresh one;
- the issue listing of an identity; `Job` and `asset` reads by id; `Claim` with a deep non-queued history;
- one sweep over N expired rows (rows/second, batch 500) and `verify-objects` over N rows (S04-F);
- the same admissions under 1, 8 and 32 parallel writers (identity-keyed lock contention: one hot identity versus
  many identities).

A plan test (gated like the other PostgreSQL subtests, in `TestDurablePostgresContract` or `TestPostgresMigrations`)
seeds enough rows that the planner prefers the indexes, runs `EXPLAIN (FORMAT JSON)` of the four statements as the
application role with a context set (so the policies apply), and asserts each plan uses its new index by name and
no sequential scan of `jobs` or `assets` remains in them. The numbers go in docs/CAPACITY.md with the machine
(WSL2, CPU, memory, PostgreSQL 16 in a container on loopback), the sizes, the command that reproduced them and
what they do not show (one host, no network, no TLS, warm cache, synthetic rows); the document says they are not a production sizing and
what to measure on the real hardware, and the tool is the way to measure it.

G3 · Guards.

- **Request bodies:** every route that reads a body reads at most a stated bound; list the bounds per route in
  API.md ("Limits") and add `http.MaxBytesReader` (or the existing equivalent) where one is missing, with a test per route
  that an oversized body is refused (413) before it is parsed.
- **Requests in flight:** `httpapi.WithMaxInFlight(n)` (default 128, 1..4096; the demo flag `-max-in-flight`):
  beyond n concurrent API requests the handler answers 503 `{"error":"OVERLOADED"}` with `Retry-After: 1` and does no
  work; `GET /session` and `/capabilities` are included (a refused request costs a counter, not a query). The console
  shows "服务繁忙，请稍后再试" for that code (and not the authorization-source text it shows for other 503s: separate the two).
- **Queries in flight per identity:** a fourth quota, `queries_in_flight` (default 4, 1..64), on `reporting.Quotas`: the
  number of `POST /queries` and `GET /snapshots/{id}/rows` the service runs at once for one identity. Enforced in the
  service, per process (it is a protection of this process's cores and its connections to the source product; the
  durable quotas of S04-D stay the cross-process ones; say so in OPERATIONS.md). Beyond it: 429 `QUOTA_EXCEEDED` with
  `quota: "queries_in_flight"`, `Retry-After: 1`, nothing run. `GET /capabilities` carries it; the console accepts the
  fourth name, shows "查询进行中过多 (4)，请稍后再试", leaves the page as it was; the embed page still does not post it.
- **Pool:** the pool size is a documented DSN parameter (`pool_max_conns`); the production opener rejects a value below the workers plus 2
  or above 100 (check what `OpenProduction` does now; keep behaviour if it already bounds it), with the figure OPERATIONS.md recommends from
  the measurements.

G4 · Growth. Measure and document how fast the stores grow per job after reduction (bytes per job row, per snapshot, per
artifact row with and without the object store) and say what to size: the `jobs` table keeps one row per admission
forever by design (the audit of what happened); the doc states the rows-per-year arithmetic at the default quotas
and the index sizes measured, and what an operator does when the table is too big (archive by `created_at` to a
separate table out of band, which the platform does not do: say it is not done and why). No pruning is added.

G5 · Tests and docs. Migration 009 round trip and catalog (and the `IF NOT EXISTS` path with the indexes pre-created);
the plan test; the body-limit tests; the in-flight cap (N+1th request is 503 with the header, a request finishing frees
the slot, the counter cannot leak on a panic or a client disconnect); the fourth quota on the service (one identity at
the cap, another unaffected, released on completion and on error); HTTP shapes; DOM for the two new messages and the
fourth quota name, and for the 503 split; demo flags bounds. OPERATIONS.md "Capacity", docs/CAPACITY.md, API.md ("Limits",
429 and 503 shapes, capabilities), CONSOLE.md, README S04-G at the top with the "以下为此前阶段历史说明：" line moved
directly below it.

Not in this slice: pruning or archiving of `jobs`, partitioning, read replicas, rate limits by time on the read paths (the in-flight
caps are the bound), autoscaling, a production sizing claim, key lifecycle.
