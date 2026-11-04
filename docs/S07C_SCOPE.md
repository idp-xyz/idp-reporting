# S07-C the identity's jobs, listed by the server

2026-11-04. The console's task list was the session's: the jobs this browser session had created or restored by id. The
manual and the test scenarios (docs/TEST_SCENARIOS.md §3) had to say so, and the seeded jobs of `scripts/seed_demo_data.mjs`
were reached by pasting ids. The server had a listing of one kind only (`GET /issues`). S07-C adds the listing of every
kind and makes the console's list the server's.

C1 · `Repository.Jobs(ctx, identity, integrations, limit)`: the identity's jobs under the given integrations, newest first,
at most limit (1..`MaxJobListing` = 100) of them; `Issues` is the same with `kind='issue'`. The memory store walks its
admission order backwards; the PostgreSQL store runs `jobListingSQL` (the issue listing's statement without the kind),
which `jobs_identity_created` serves: the plan test (`integration/plan_test.go`) holds it to the issue listing's rule and
logs an Index Scan of that index for both identities at 30,000 rows (docs/CAPACITY.md). The statement is in
`CapacityStatementsForTests` after the issue listing's, so the measurement tool's index into that list is unchanged.

C2 · `Service.Jobs(ctx, principal, limit)`: the integrations the principal holds and the service connects (`held`, the
filter `Issues` already applied), the repository's page, and for each job the view its poll shows: `expiredView` when
the job's read is over (S04-B), else `jobView` (an issue's number from its record once it succeeded). The source is not
asked and no result is authorized by the listing: `GET /jobs/{id}`, the snapshot's rows and the artifact's content
authorize when they are read, as before. A record that cannot be read as published fails the page (ErrIntegrity), as in
`Issues`.

C3 · `GET /jobs?limit=N` (default 50, 1..100; the query is `limit` alone, once, else 400), before `GET /jobs/{id}` in the
mux and distinct from it. Another identity gets `[]`; so does an identity without integrations. The route takes the
bearer like every other. Tests: `transport/httpapi/jobs_test.go` (order, kinds, the entries' fields, the limit's bounds,
another subject, the poll still answers, a cancel shows), the retention test's listing after the sweep (`expired: true`
on all three, the issue's number kept), the repository contract case `jobs_listing_newest_first_under_the_identity_and_its_integrations`
on the memory and the PostgreSQL stores.

C4 · The console. `loadJobs()` reads `GET /jobs?limit=50`, checks the list as a poll's answer is checked (an array of at
most 100 job views, no id twice; anything else is 无法验证的响应 and the rows stay), and makes it the task list in the
server's order, each entry keeping what the session knew of it (the query behind a snapshot); what the session knows and
the server did not list (a job restored by id from outside the window) stays, older than the listed. It runs after
sign-in (the embed, which shows one view and has no panel, does not), when the tasks panel opens (quietly: the page is
not held, the rows change when the answer comes) and first thing under 刷新状态, which then polls each known job as it
did (the poll is the authorized view). The texts say what the list is: 我的任务, 服务器上当前身份最近的 50 项任务; the
overview's card and the rail's badge count the same list. The DOM harness answers the listing with `[]` unless a test
asks for it (`app(intercept, {listing: true})`): the server and its identity are shared by every test of the file, and
the tests' row counts are their own jobs'. The S07-C test uses the real listing: the count after sign-in is the server's,
a job made through the API appears when the panel opens and under 刷新状态, a listed job restored by id is not a second
row, a list that cannot be believed is refused whole, sign-out clears it.

Out of scope: paging beyond 50 (the restore row reaches older jobs by id), filtering by kind or state, a count of all
jobs, and any change to what a job view discloses.
