# S16-A scheduled reports and their delivery

2026-11-09. ADR 0017's S16 (after S12), decided in docs/ADR_0020_SCHEDULED_REPORTS.md.

A1 · `reporting.Schedule`: one identity's definition under one of its products: a name (1..120 bytes of text), a query of the published
catalog frozen as written, an optional export format (`xlsx` or a configured one), an as-of rule (`run_date`, the default, `previous_day`,
`fixed`) and a `Recurrence`: `daily`, `weekly` (`weekday` 1..7, ISO) or `monthly` (`day` 1..28) at `HH:MM` in an IANA zone (`UTC` or
`Area/Location`, never `Local`; the zone database is embedded, `time/tzdata`). No free-form expression. `Next(after)` is the first occurrence
strictly after a time; a wall-clock time a zone skips or repeats runs once that day. A schedule is `active`, `paused` or `deleted` (a tombstone:
not found, not listed, not counted, never run). At most `schedules.per_identity` (default 20, at most 200) that are not deleted: 429
`QUOTA_EXCEEDED`, quota `schedules`.

A2 · `reporting.Schedules` (the core, standard library only): `Create` (under an Idempotency-Key; the query is checked as the caller against
the catalog, no row read, no source read prepared), `List`, `Get`, `SetState` (a resumption checks the catalog again and runs from the next
occurrence after now: what was missed while paused is not run), `Runs`, and `Tick`, one pass of the scheduler: it starts the runs that are due,
then advances the open ones. A run (`reporting.ScheduleRun`) freezes the schedule's query with its as_of and its format when it starts, then
goes `due` → `snapshot` (the snapshot job submitted as the owner, `Service.SubmitSnapshot`, key `schedule:<run id>`) → `export` (when a
format is set, `Service.SubmitExportFormat`) → `succeeded`, or ends `failed` (the job's code, `QUOTA_EXCEEDED`, `RUN_TIMEOUT` after 6 hours,
`AUTHORITY_UNAVAILABLE` when the source stayed down for an hour after the start, retried each minute until then), `refused`
(`AUTH_SCOPE_DENIED`: no binding, the product no longer bound, or the source refused; `AUTH_ROLE_DENIED`: no `viewer` role) or `cancelled`
(`SCHEDULE_PAUSED`, `SCHEDULE_DELETED` before it started; `JOB_CANCELLED` when the owner cancelled its job). The principal is
`ScheduleOptions.Resolve` of the owner's identity at each step: the production server's is `accessjwt.Verifier.Bound` (the bindings in force; with
several bindings of the identity only what all grant). A job's success is read through `Service.Job` as the owner, so the source authority is
asked again. Each run's end is recorded in the owner's audit as `schedule.run` (object kind `schedule`, the run's outcome and code).

A3 · Delivery: `reporting.Deliverer` and `delivery/webhook` (standard library). A run that ends is `pending` and its notification
(`reporting.Notification`: event `schedule.run.finished`, run, schedule, tenant, issuer, subject, product, occurrence, end, state, code, snapshot,
artifact, format, attempt, sent; never a value) is POSTed with `X-Reporting-Signature: sha256=<hex HMAC-SHA256 of the body>`,
`X-Reporting-Delivery: <run id>`, `X-Reporting-Event`. The outcome of an attempt is one word: `delivered` (2xx), `rejected` (another 4xx),
`redirected` (3xx; never followed), `throttled` (408, 429), `server_error` (5xx), `unreachable` (no answer within the timeout, TLS, the connection).
The last three are retried after 30 s, 1, 2, 4 and 8 minutes (at most 15), six attempts in all; then the delivery is `failed`. `webhook.Verify` is the
receiver's check. At least once: a lease lost after an answer repeats the attempt.

A4 · Storage. `reporting.ScheduleStore` on the memory store and PostgreSQL. Migration 013 `schedules` (identity, product, key, request
hash, state, the definition as JSONB with its digest, `next_run_at` set exactly while active, unique (identity, key)) and `schedule_runs` (unique
(schedule, occurrence), the frozen query with its digest, the jobs, the results, the code, the delivery and its attempts, `check_at`,
`generation`, `lease_until`), indexes for the due schedules, the open runs and the listings; row-level security as 007 (the identity's rows,
and every row in the worker scope); the audit's action and object-kind checks gain `schedule.change`, `schedule.run`, `schedule`. Both are data
tables of the application role (INSERT, UPDATE; never DELETE), held to their policies by the role check from version 13. `StartDueRuns` takes one
due schedule per transaction with `FOR UPDATE SKIP LOCKED`, only when it has no open run, inserts its run (`ON CONFLICT (schedule_id, due_at) DO
NOTHING`) and sets its next occurrence: several servers start an occurrence once, and a downtime is caught up once. `ClaimRuns` claims open or
delivery-pending runs whose `check_at` has come and whose lease has run out (one statement, `SKIP LOCKED`); `UpdateRun` is fenced by the
generation and the lease. A run whose stored definition does not match its digest is ended `failed INTEGRITY_FAILURE` in the claim's
transaction and never handed out. Every time is the database's clock.

A5 · The API (`httpapi.WithSchedules`, docs/API.md, docs/openapi.json): `POST /schedules` (Idempotency-Key; 201 and `Location`), `GET /schedules`
(`limit` 1..100, `before`), `GET /schedules/{id}`, `POST /schedules/{id}/pause`, `POST /schedules/{id}/resume`, `DELETE /schedules/{id}` (no body,
each answers the schedule), `GET /schedules/{id}/runs`. Every route is a `viewer`'s (`RequiredRole`: the role of the jobs a run submits); every
change is `schedule.change` in the audit (`AuditAction`). Another identity's schedule is 404; one under a product the caller no longer holds 403.
No route reads or writes the delivery endpoint.

A6 · The server: a `schedules` section (`poll_interval` 5..300 s, default 30; `per_identity`; `webhook.url`, `.timeout` 1..30 s, default 10,
`.loopback_http`) turns the routes and the scheduler on; `REPORTING_WEBHOOK_SECRET` (32..1024 bytes) is required with a webhook, refused at
step `secrets` otherwise, never shown. Every server runs the scheduler's loop on the work context, so the drain lets a pass finish. Logged:
`schedule` (counts), `schedule_failed` (a code), `delivery` (the outcome word, the status, the attempt, the delivery's state, the run's and
schedule's platform ids; never the endpoint, the secret or a value). Metrics: `reporting_schedule_passes_total{outcome}`,
`reporting_deliveries_total{outcome}`. The start line says `schedules` and `webhook`. The demo mounts the schedules on its store
(`-schedule-interval`, 30 s; 0 turns them off) without a webhook.

Deferred to S16-B: the console's page of schedules and runs (another pull request is changing the console; the API is complete without it);
the runs' retention (a sweep-role step, as the jobs'); email and per-tenant endpoints (another `Deliverer`); a schedule of a report layout or
dashboard rather than a query; plan tests for the new statements (the indexes are named in A4).

Tests: `TestRecurrenceIsBounded`, `TestRecurrenceNext` (zones, weekdays, month days, New York's two changes for 300 days),
`TestStartRunFreezesTheAsOf`, `TestScheduleNames`, `TestSchedulesAreMadeListedAndChangedByTheirOwner`,
`TestAScheduledRunFreezesExportsAndIsDelivered`, `TestAScheduleCatchesUpOnceAfterADowntime`, `TestSchedulersOnOneStoreRunAnOccurrenceOnce`,
`TestAScheduledRunActsAsItsOwnerNow` (unbound, no viewer, revoked at the source, the product unbound, the source down and back),
`TestARunEndsWithoutItsJob`, `TestADeliveryIsRetriedWithBackoffAndThenGivenUp`, `TestRunsWithoutDeliveryAndLeases`; `delivery/webhook`'s
configuration, signature and outcomes (no redirect followed, an untrusted certificate unreachable); `TestScheduleRoutes`; the role and audit
tables; `TestBoundIsTheBindingInForce`; `TestMemoryScheduleContract` and `TestPostgresScheduleContract` (the contract, eight schedulers at once,
an occurrence restored onto its run, tampered rows); the row-level security case (the whole contract as the application role);
`TestTheServerRunsSchedulesAndDeliversThem` (an API-made schedule run, delivered signed, logged, audited), `TestTheWebhookNeedsItsSecret`, the
configuration refusals; `catalog_013.txt`; openapi.json held to the routes. Docs: API, DEPLOYMENT, OPERATIONS, SECURITY_REVIEW, ADR 0020.
