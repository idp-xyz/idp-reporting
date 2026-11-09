# S12-B the audit of people's actions

2026-11-09. ADR 0018 decision 4 (accepted with Q4's recommended answer: fail closed for publication, approval and issue).

B1 · `reporting.AuditLog` (`Record`, `Events`) on the memory and PostgreSQL stores. Migration 012 `audit_events (id, tenant, issuer, subject, at,
action, object_kind, object_id, outcome, code)`, checked values, two indexes for the two listings; row-level security: the identity policy of
007 for every command, and `tenant_audit` for SELECT when the transaction says `reporting.audit = 'tenant'` (only the server's tenant listing,
only for an administrator). The application role inserts and reads (a new class of table, append-only: `appendTables`), never updates or deletes;
the role and policy checks know it from version 12.

B2 · `httpapi.WithAudit(handler, log)` puts the log in the requests' context (and gives the access line back the route the inner mux matched);
`authenticated` records what `httpapi.AuditAction(method, pattern)` names: `design.change`, `design.release` and `issue.submit` (recorded as
`requested` before they run, 503 `AUDIT_UNAVAILABLE` when that fails), `export.submit`, `artifact.download`; each with its outcome from the status
the handler wrote; a role refusal as `role.refused`. `GET /audit` lists one's own events, or the tenant's with `scope=tenant` for an administrator.
The production server and the demo compose it.

B3 · The console's 操作记录 page (rail: 记录): one's own actions, newest first, 加载更多; for an administrator a 范围 select to the tenant's, with a
人员 column. `no-administrator` hides the select from others.

Tests: `TestAuditActionOfEachRouteClass`, `TestAuditRecordsActionsAndListsThem` (the actions and outcomes, pages, own and tenant listings,
the refusals, a failing log refusing a withdrawal but not a save, no audit no route), `TestMemoryAuditContract` and
`TestPostgresAuditContract`, the row-level security case, `catalog_012.txt`, DOM test S12-B, the rail tests and the evidence script's rail list.
Docs: API, DEPLOYMENT, OPERATIONS, SECURITY_REVIEW, CONSOLE, openapi.json.
