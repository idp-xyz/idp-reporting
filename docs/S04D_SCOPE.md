# S04-D quotas: admission is bounded per identity

2026-11-01. The S03-H review noted (N10, docs/CODE_REVIEW_S03H.md) that admission never asks the source product, so any
admitted principal can create jobs without bound; S04-A and S04-B listed quotas for this slice. Today one identity can
queue any number of jobs, hold any number of results until they expire, and submit as fast as it likes. The private
stores (layouts, dashboards, templates, backgrounds, images) already have per-identity caps; this slice gives the
platform store its own, enforced where admission happens, atomically, on both stores, and tells callers and
operators what they are.

D1 · The quotas, per identity (tenant, issuer, subject), with defaults an operator can change within bounds:

- `jobs_in_flight`: queued and running jobs, default 8 (1..1000);
- `stored_results`: snapshots and artifacts the identity owns whose expiry is still in the future by the store's
  clock (an expired one waits for the sweep and does not count; an issue record never counts, it is permanent),
  default 64 (1..100000);
- `admissions_per_hour`: jobs admitted in the last hour by the store's clock, default 120 (1..100000).

`reporting.Quotas{JobsInFlight, StoredResults, AdmissionsPerHour int}` on `reporting.Config`; zero means the default,
a value outside its bounds is refused at construction (`NewService` returns `ErrInvalid`). The demo takes
`-quota-jobs`, `-quota-results`, `-quota-admissions` (same meaning, same bounds, refused at start).

D2 · Where and how they are enforced. In the repository's `Admit`, inside the admission transaction, after the
idempotent replay is settled and before the insert, so that a replay of an existing key is never refused by a quota
(the job exists already, the answer is its view) and so that two concurrent admissions cannot both pass a count of
one below the limit: the PostgreSQL store takes a transaction-scoped advisory lock keyed on the identity
(`pg_advisory_xact_lock(hashtextextended(tenant||issuer||subject, 0))` or equivalent; two identities never wait on
each other, and the lock ends with the transaction) and counts with the statements of D1 (the store's clock for
expiry and the hour); the memory store counts under its mutex with the same rules. `Repository.Admit` gains the quotas
as a parameter (`Admit(ctx, job, Quotas)`), so the store applies exactly what the service was configured with. A
refusal is `reporting.ErrQuota` ("quota exceeded"), a new sentinel, carrying which quota in its text (no counts of
other identities, no values); it changes nothing. Cancelling or finishing a job frees its slot; the sweep deleting
expired results does not change `stored_results` (they stopped counting at expiry).

D3 · What callers see. HTTP: 429 with `{"error":"QUOTA_EXCEEDED","quota":"jobs_in_flight"|"stored_results"|"admissions_per_hour"}`
and `Retry-After: 60` for every submission route (`POST /snapshot-jobs`, `POST /exports`, `POST /issues`); nothing is
admitted, no Location. `GET /capabilities` gains `quotas: {jobs_in_flight, stored_results, admissions_per_hour}` with the
numbers in force. The console validates the code and the `quota` key, shows a short notice naming the quota ("已达到
进行中任务上限 (8)，请等待任务完成或取消后再试", "已达到保留结果上限 (64)，结果过期后自动释放", "已达到每小时提交上限
(120)，请稍后再试", the numbers from capabilities), leaves the page as it was, and treats any other shape as an
unverifiable response. The embed page behaves the same (it runs the console's code) and does not post the quota to the
host (`reporting:error` codes stay the closed list; a quota refusal is not an error of the frame). API.md and CONSOLE.md
say all of this.

D4 · Tests. Contract on both stores (each quota at the limit and one over; the hour and the expiry by the store's
clock; cancelled, failed and succeeded jobs free in-flight slots; expired results do not count; issue records never
count; a replay with an existing key succeeds at the limit; another identity of the same tenant is not affected;
quotas apply to every job kind). Concurrency on PostgreSQL: N parallel admissions for one identity with
`jobs_in_flight` = K admit exactly K and refuse the rest, and parallel admissions of two identities do not serialize on
each other (both finish under the same lock rules; prove the lock is identity-keyed by `pg_locks` or by timing with a
held transaction of the other identity). HTTP: the 429 shape and header on the three routes, nothing admitted,
capabilities. DOM: the notice for each quota, the numbers from capabilities, refusal of a malformed body, and the
embed page not posting it. Service construction and the demo refuse out-of-bounds values. The PostgreSQL-only tests
go into the existing gated tests as subtests.

D5 · Docs. OPERATIONS.md gains "Quotas" (what is counted, defaults and bounds, the flags, how to see an identity's
usage with counts-only queries, interplay with retention), API.md (429, capabilities), CONSOLE.md, README S04-D section
at the top with the "以下为此前阶段历史说明：" line moved directly below it.

Not in this slice: rate limits on `POST /queries` and reads (no admission there; a later capacity slice), per-tenant or
per-integration quotas, bytes-based quotas (results are already bounded per item by MaxResultBytes and
MaxArtifactBytes, so `stored_results` bounds bytes too), quotas negotiated with the source product, object storage,
backups, capacity, key lifecycle.
