# S24-A · The cancellation of an issued document, asked of the source product (ADR 0025 decisions 1 to 6), on the server

2026-11-12. ADR 0025 (docs/ADR_0025_ISSUE_OPERATIONS.md, accepted by the product owner on 2026-11-11) decided that a document which was
issued correctly and should not stand is withdrawn by asking the source product, which decides; that the platform records the product's
answer beside the issue record without changing the record; and that a cancelled file is no longer handed out. This slice is the server
side of that: the adapter capability, the core, both stores, the route, the source protocol's optional route with the adapter `http`, the
reference source and the checker, both contract simulators, and the documents.

**No console change.** The register's 作废… (the dialog that names the number and asks the reason, the row that says 已作废) is S24-C;
until then the route is used through the API. Batches (decisions 7 to 10) are S24-B. The manual source of decision 6 does not exist yet
(S08-D, ADR 0013, proposed): nothing of it is built here; when it is, it implements `DocumentCanceller` and nothing else changes.

What does not change: `VoidIssue` and the worker's failure path (the platform's own undo of a job that did not complete); an adapter or
a connection without the capability (it behaves exactly as before: nothing new is asked of it, and its documents show no cancellation);
the issue record (every field it had); the lifetime of snapshots and files. That lifetime is ADR 0026's, in its own slices: the slice
sits on S25-A (three lifetimes in the core and the stores, merged on 2026-11-12), and the refusal of a cancelled file holds for a file
that lives with its read and for one that is kept beyond it alike.

## What

A1 · **The capability and the core** (`reporting/issue_cancellation.go`; the core stays standard-library-only). The optional adapter
capability `DocumentCanceller` beside `DocumentAuthority`: `CancelDocument(ctx, SourceContext, IssueCancellation) (IssueCancellationReceipt,
error)`. The request names the document by the platform's job id, the product's reservation and confirmation references and its number,
and carries the person's reason; the receipt is the product's reference and time. `ValidCancellationReason`: 1..200 characters (counted
in characters, not bytes) of text on one line, not blank, without control characters, the rule of every text a person writes
(`plainText`). `Service.CancelIssue(ctx, principal, issueID, reason)`, in this order: the reason is one (`INVALID_REQUEST`); the issue is
the caller's own (another identity's, another kind of job: `NOT_FOUND`) under a product the caller holds (`AUTH_SCOPE_DENIED`) and its
job succeeded (`IDEMPOTENCY_CONFLICT`: there is no issued document); a cancellation already recorded is answered as it stands, without
asking the product; the adapter is a `DocumentCanceller` (`UNSUPPORTED_COMBINATION`); the product is asked once, within the caller's
context, and its refusal is the answer with nothing written (denied, conflict, unavailable); its receipt is held to the contract (the
reference's form, a whole second between the confirmation and 9999; `INTEGRITY_FAILURE` otherwise, and for a product that says it never
reserved a job whose confirmation the platform holds, or that answers with one of the platform's own refusals); the cancellation and its
audit event are recorded in one step, in a context of the write's own (a caller that went away after the product cancelled does not keep
the platform from recording it); the issue is answered as the listing shows it. `Service.CanCancel(principal)` says per served product
whether the adapter has the capability. A panic of the adapter is an error without the panic's value. No error carries the reason, the
number or a reference.

A2 · **The views and the download** (`reporting/types.go`, `reporting/service.go`). `IssueSummary` gains `cancellation: {ref,
cancelled_at, reason}` (omitted unless cancelled), so `GET /issues`, `GET /issues/{id}` and the `issue` object of a job's view carry it;
the state word stays the job's (`succeeded`). `IssueView` gains `integration_id`, the product the issue was made under, which a client
needs to read the per-product capability. A cancellation a repository reads with a record is held to that record
(`ValidateCancellation`) wherever the issue is shown: one that is not the record's is `INTEGRITY_FAILURE`, like a record that is not the
job's. `Service.Download` of the artifact of a cancelled issue answers `ErrIssueCancelled` (`ISSUE_CANCELLED`, 409), decided by the
artifact's own record before the source is asked, whether the file lives with its read and is served through its snapshot or is kept
beyond it and authorized by its scope (ADR 0026, S25-A); every other answer about an issued file is what it was. A job's view does
not serve the file and is not refused.

A3 · **The repository contract and the memory store** (`reporting/types.go` `Repository.RecordCancellation`,
`reporting/repository_contract.go`, `storage/memory`). `RecordCancellation(ctx, identity, IssueCancellationRecord)` keeps a cancellation
once, with its audit event (`issue.cancel`, `succeeded`, at the store's clock) in the same atomic step: both or neither
(`AUDIT_UNAVAILABLE` when the event cannot be written). The same receipt again answers the row as first kept, whatever the reason says;
another receipt for the issue is a conflict; an issue that is not the identity's own record is not found; a cancellation that is not of
the contract's form or is dated before the record's confirmation is invalid. `Issue` answers the record with its cancellation.
`ValidatePublication` refuses a record that arrives with a cancellation (a worker never writes one). The sweep never touches a
cancellation.

A4 · **PostgreSQL** (`storage/postgres/migrations/014_issue_cancellations.{up,down}.sql`, `storage/postgres/cancellations.go`,
`production.go`, `migrate.go`; docs/OPERATIONS.md "Cancelled issues"). Table `reporting_s01.issue_cancellations`: `issue_id` (primary key:
one cancellation an issue), the owner's `tenant`, `issuer`, `subject`, the product's `ref` and `cancelled_at`, the `reason` (1..200
characters by a check), `recorded_at` (the database's clock). Row-level security: policy `identity` (SELECT, the connection's identity)
and policy `issue_owner` (INSERT, the same identity and only for an issue record it can see, which the policies of 007 on `assets` make
its own); no policy admits an update or a delete and none is a worker's. The application role is granted INSERT and SELECT and nothing
else (`appendTables`, with the audit's table): the role check refuses a role that can update or delete it, and `rlsProblems` holds the
two policies to their text. The audit's action check gains `issue.cancel`. The insert and the audit event are one transaction. The down
step drops the table and the audit's `issue.cancel` events, and is refused while cancellations exist unless `--accept-data-loss`.
Catalog snapshot `integration/testdata/catalog_014.txt`. No foreign key to `assets`: the record is permanent and the insert policy
already requires it.

A5 · **The route** (`transport/httpapi/api.go`; docs/API.md "Cancelling an issued document"; docs/openapi.json). `POST
/api/reporting/v1/issues/{id}/cancellation` with exactly `{"reason": "…"}` (strict JSON, the small bound of 4 KiB, no Idempotency-Key:
the issue id is the key). An `issuer`'s (`RequiredRole`). Audited as `issue.cancel`, mandatory (`AuditAction`): `requested` is recorded
before the product is asked and the request is refused (503 `AUDIT_UNAVAILABLE`) when that cannot be written; `succeeded` is the store's
event of A3, so the route adds none of its own and a repeat leaves `requested` alone; a refusal or a failure is recorded as every
route's. Answers 200 with the issue view (a repeat answers the same body). `classify` maps `ISSUE_CANCELLED` to 409. `GET /capabilities`
gains `issue_cancellation`, one boolean per product the server serves for the caller, and names the route.

A6 · **The source protocol** (docs/SOURCE_HTTP_PROTOCOL.md, docs/source-openapi.json, `adapters/httpsource/protocol`; a minor revision:
the number is in the protocol document's title, the description's `info.version` and `protocol.Revision`, held together by
`TestTheOpenAPIDescriptionIsTheProtocol`). One additive optional route, `POST /reporting-source/v1/issue-cancellations`: `{context,
cancellation: {job_id, reservation_ref, confirmation_ref, number, reason}}` → `{receipt: {job_id, ref, cancelled_at}}`; an issue route
for the error table (`not_found`, `conflict`). `adapters/httpsource`: the connection option `cancellation: true`, which needs `issuance`
`rows` or `groups` (a source cancels the documents it issues; the configuration is refused otherwise); the value `New` returns is then
a `DocumentCanceller` (`CancellingAuthority` and its three forms), so that the method set says what the connection declared; a request
that is not of the contract's form is never sent; the receipt is decoded strictly and verified (of the request's job, the reference's
form, a canonical whole second), an answer that does not verify is `INTEGRITY_FAILURE` with nothing passed on. `sourcetest` serves the
route from the wrapped adapter's `DocumentCanceller` (`unsupported` without one), and `sourcetest.Remote` declares the capability when
the wrapped adapter issues and cancels, beside the data class it passes on (S08-A).

A7 · **The checker** (`adapters/httpsource/conformance/cancellation.go`, `cmd/reporting-source-check`). The flag `-cancellation`, with
`-issue` (alone it is a usage error), checks four rules as a connection that declares the capability would use it; 32 rules in all:

- **X1** a confirmed document is cancelled (a receipt of the job, with a reference and a whole second not before the confirmation), and
  asking again, with another reason, answers the same receipt;
- **X2** a reservation that was not confirmed is not cancelled (`conflict`), nor is a job the source never reserved (`not_found`);
- **X3** a cancellation asked by a subject the product does not know is refused (`denied`), before the document is cancelled and after;
- **X4** the number of a cancelled document stays used: a job reserved afterwards gets another number.

They confirm one document of the check's own and cancel it, and reserve and void two more numbers: never against production. Without
the flag they are skipped; a source that answers the route `unsupported` fails X1 and skips the rest; a document the check confirmed and
could not cancel is voided.

A8 · **The simulators** (`adapters/contractsim`, `adapters/contractsim/pgsource`, one suite for both in
`adapters/contractsim/internal/authoritytest`). Both implement `CancelDocument` with one rule (`IssueRecord.Cancel`): only a confirmed
document; the request must name it as the product holds it; idempotent by the job id (the same receipt, the first reason kept); the
number stays used; a principal that may not issue may not cancel; the product's approval switch refuses; a void of a cancelled document
is not an error and leaves it cancelled. The durable simulator (schema version 3: a table `issue_cancellations` beside `issues`, an
upgrade from version 2 in the transaction of `Open(initialize)`) cancels in its own transaction with the issue row locked, as its issue
calls do.

A9 · **Documents**: this page; ADR 0025 (the owner's answer of 2026-11-11, the notes about ADR 0026, the implementation line);
docs/API.md (the route's row and section, the capabilities, the audit, the download, the views, the limits); docs/openapi.json;
docs/SOURCE_HTTP_PROTOCOL.md and docs/source-openapi.json; docs/SECURITY_REVIEW.md (section 7); docs/REAL_SOURCE_ONBOARDING.md (the
row); docs/DEPLOYMENT.md (the option `cancellation`, the pre-connection check, `roles` after `up`); docs/OPERATIONS.md ("Cancelled
issues"); README.

## The order of the two systems

Nothing is atomic across the product and the platform (ADR 0003 decision 4). The product is asked first and the platform records
second, so the platform never says cancelled of a document the product did not cancel. When the platform has no answer to record (a
timeout, the product's 5xx) or cannot record it (the store, or the audit event that must be written with it), the document is cancelled
at the product and not yet at the platform. That state is visible (the audit has `requested` and `failed` and no `succeeded`) and is
closed by the same request again: the product, idempotent by the job id, answers the receipt it gave, and the platform records it.
The platform does not retry on its own: a cancellation is a person's act.

## Where the ADR was interpreted

1. **The reason is counted in characters**, 200 of them (at most 800 bytes of UTF-8), by the rule of every text a person writes
   (`plainText`: one line, no control characters). The ADR says "1 to 200 characters of text, no control characters".
2. **`IssueView` gains `integration_id`.** The ADR says the capabilities answer per product; a listing that spans the caller's products
   must then say which product an entry belongs to.
3. **`issue_cancellation` is an object keyed by product** (`{"<integration_id>": true|false}`) for the products the server serves for
   the caller. It is the one part of the capabilities that depends on the caller.
4. **Two policies, not one.** "Under the row-level security of the issue it belongs to" is implemented as the identity's own rows for
   reading and, for inserting, the identity's own row whose issue record the identity can see. No worker policy: no worker reads or
   writes a cancellation.
5. **The down step refuses to discard cancellations** unless the loss is accepted (the rule of 003, 004, 006 and 008), and deletes
   the audit's `issue.cancel` events with the table (the check constraint could not be restored otherwise).
6. **An issue job that has not succeeded is 409 `IDEMPOTENCY_CONFLICT`** (the platform's conflict code): the ADR says the route is
   "for an issue of the caller's own that succeeded" and names no code for one that did not.
7. **A product that says "not found" for a job the platform holds its confirmation of is `INTEGRITY_FAILURE`**, not 404: the caller
   can see the issue in the register, and the two systems disagree. So is one of the platform's own refusals in an adapter's mouth.
8. **The audit.** The ADR says the cancellation and its audit event are written in one transaction, and that the event that cannot be
   written fails the request. Implemented as: `requested` before the product is asked (the rule of every mandatory action, ADR 0018
   Q4), `succeeded` by the store with the cancellation, and no second `succeeded` from the route. A repeat of a recorded cancellation
   records `requested` only.
9. **A recorded cancellation is answered before the capability is looked at.** A connection that lost the capability still shows, and
   repeats, the cancellations it recorded.
10. **The receipt's time may not precede the confirmation** the platform recorded; the protocol says so to the product.
11. **The record is written in a context of its own** (5 seconds), detached from the caller's: once the product has cancelled, a
    caller that went away must not leave the platform's record incomplete when it could be written.
12. **`ISSUE_CANCELLED` is decided before the source is asked** to authorize the snapshot or, for a file kept beyond its read (ADR
    0026), its scope: a cancelled file is refused whether or not the read or the scope is still valid, for as long as its row is
    there. Once retention has removed the file the download is 404, as for any file.
13. **The wire receipt carries `job_id`**, which the adapter checks against the request, beyond the reference and the time of the
    Go contract: an answer about another job must not be recorded.
14. **The simulators let whoever may issue cancel** (`Grant.IssueDocuments`), and their approval switch refuses cancellations too. A
    product may be stricter; the protocol leaves the rule to the product.
15. **The checker's flag needs `-issue`** and its rules are X1 to X4, run after I1; "run with the issue rules" in the ADR.
16. **Decision 6** (the manual source cancels the same way) has nothing to build: the manual source does not exist. ADR 0013 already
    carries the amendment.

## Evidence

- **The core** (`reporting/issue_cancellation_test.go`, with a fake authority): `TestCancelIssueAsksTheProductThenRecordsTheReceipt`,
  `TestCancelIssueRefusesBeforeTheProductIsAsked`, `TestCancelIssueNeedsAnAdapterThatCancels`,
  `TestCancelIssueRefusedByTheProductWritesNothing`, `TestCancelIssueHoldsTheReceiptToTheContract`,
  `TestCancelIssueIsCompletedByARetryAfterThePlatformFailed`, `TestCancelIssueFailsClosedWhenItsAuditEventCannotBeWritten`,
  `TestCancelIssueRecordsWhenTheCallerWentAwayWhileTheProductDecided`, `TestCancelIssueKeepsTheFirstReceiptWhenTheProductGivesAnother`,
  `TestConcurrentCancellationsOfOneIssueRecordOnce`, `TestACancellationOutlivesTheSnapshotAndTheFile`,
  `TestIssueViewsHoldTheCancellationToItsRecord`, `TestDownloadRefusesACancelledIssuesFileByItsOwnRecord`,
  `TestCancellationReasonRequestAndReceiptForms`, `TestValidateCancellationHoldsItToTheRecord`, `TestIssueViewsNameTheirProduct`; and
  in `reporting/issue_test.go` the publication that arrives with a cancellation (`TestIssueContractRefusesInconsistentJobsAndPublications`).
- **Both stores through one contract** (`integration/cancellation_test.go`, run by `TestMemoryContract` and `TestPostgresContract`):
  `an_issue_cancellation_is_kept_once_beside_its_record_with_its_audit_event` (who cannot, what is not a cancellation, the audit down,
  the row and its event, once, a conflict, other identities, the bounds, a sweep). The retention contract on both stores
  (`TestMemoryRetentionContract`, `TestDurablePostgresContract/retention`):
  `a_cancellation_is_asked_of_the_product_and_stays_with_the_record_when_the_file_is_swept` (the service with the product of each
  store's harness: the durable simulator on PostgreSQL). The lifetime contract of S25-A on both stores (`TestMemoryLifetimeContract`,
  `TestDurablePostgresContract/lifetimes`): `a_kept_issued_document_that_is_cancelled_is_no_longer_handed_out` (a simulator that
  vouches for a scope: the document is cancelled after its read, its kept file is refused without the source being asked, the other
  kept file is served under the source's answer of the moment, and the record keeps the cancellation when the scope has ended).
- **PostgreSQL only** (`TestPostgresMigrations`):
  `issue_cancellations_step_adds_the_table_its_policies_and_the_audits_action_and_refuses_to_lose_cancellations` (the catalog of 014 and
  of 013 after the down, the checks, the down's refusal and its acceptance) and, under `row_level_security`,
  `a_cancellation_is_its_issues_owners_and_is_never_rewritten` (as the application role: another identity neither reads nor inserts,
  whatever owner the row claims; no update, no delete; a role granted them is reported; a changed policy is reported).
- **The simulators** (`authoritytest.Run` and `RunConcurrent`, against the in-memory simulator, the same through the protocol, and the
  durable one in `TestDurableDocumentAuthority`): thirteen scenarios (the receipt and the record; the same receipt again; the number is
  not given out again; only a confirmed document; an unknown job; the request must name the document; a principal that may not issue;
  the product's own rules; malformed requests and the bounds; a void of a cancelled document; scoped to the source; an outage; a
  document of a group) and two concurrent ones. The durable simulator's own: the schema of version 3, the upgrade from version 2 and
  from version 1, the constraints of its cancellations table, a cancellation surviving a reopening.
- **The protocol through the adapter and the reference source** (`adapters/httpsource/cancellation_test.go`):
  `TestCancellationIsDeclaredByTheConnection`, `TestTheCancellationRequestIsTheProtocols`,
  `TestACancellationReceiptThatDoesNotVerifyIsAnIntegrityFailure` (22 tampered receipts, 8 malformed answers),
  `TestTheCancellationsRefusalsAreTheProtocols`, `TestASourceThatCannotCancelOrDoesNotAnswer`,
  `TestTheReferenceCompositionPassesTheCancellationOnBesideTheDataClass`; the service in process and over the protocol
  (`TestTheServiceCancelsAnIssueThroughTheProtocol`, `TestAConnectionWithoutCancellationCannotCancel`); the description
  (`TestTheOpenAPIDescriptionIsTheProtocol`: nine routes, the four schemas, the revision in its three places).
- **The checker** (`cmd/reporting-source-check`): `TestTheReferenceSourcePassesEveryRule` (32 of 32 with `-issue -companies
  -cancellation`, what the check leaves in the product, the skips without the flag), `TestASourceThatCancelsWronglyFailsTheCancellationRules`
  (a source that gives the cancelled number out again fails X4; one that cancels for a subject it does not know fails X3; one that
  cancels an unconfirmed reservation fails X2; one that answers another receipt, or dates it before the confirmation, fails X1; one
  that declares without implementing fails X1 and skips the rest), `TestUsageErrorsExitTwo`.
- **The route** (`transport/httpapi/issue_cancellation_test.go`): `TestTheCancellationRouteAsksTheProductAndAnswersTheIssue` (the
  capabilities, the view, the record unchanged, every view, the download's 409, the repeat, the number, the audit),
  `TestTheCancellationRouteRefusesWithoutTouchingTheDocument` (the role, 401, another identity's 404, what is no issue, the 409 of a
  queued, a cancelled and a failed job, 17 bodies that are not the request, the content type, 413, the product's refusals and outage,
  the bound of 200 characters, the audit of it all), `TestTheCancellationRouteNeedsTheProductsCapability` (422, `false`, `{}`),
  `TestACancellationWhoseAuditEventCannotBeWrittenFailsAndIsCompletedByTheRetry`; the tables of `TestRequiredRoleOfEachRouteClass` and
  `TestAuditActionOfEachRouteClass`; the security-header sweep (200, 404 and the download's 409); `TestOpenAPIMatchesTheBoundRoutes`.
- **The shipped server end to end** (`server/cancellation_test.go`): `TestTheShippedServerCancelsAnIssueThroughAnHTTPSource` (the
  configuration's `cancellation` option, real tokens over TLS, the reference source behind the adapter `http`: the capabilities, the
  route, the views, the download, the repeat, the product's refusal, the audit, the outage, and a log that carries nothing of the
  reason, the number, a reference, the credential or the source's address) and
  `TestAConnectionWithoutTheOptionCannotBeAskedToCancel` (422 without a request to the source; a configuration that declares
  cancellation without issuance refuses the start).
- `gofmt -l .` empty, `go vet ./...`, the strict Go run, the PostgreSQL run, `go test -race` on the touched packages, the Typst run
  and both console suites (unchanged: no console file is touched).

## Known

- No real product implements the route; the simulators' rule of who may cancel is the simplest one.
- The platform does not retry a cancellation whose answer it could not record; the person does. Nothing reports such documents by
  itself; the audit shows them (`requested`, `failed`, no `succeeded`).
- A colleague of the tenant cannot cancel another person's document: records are their owner's (row-level security). A register for
  the tenant is a decision of its own (ADR 0025, "Alternatives").
- The checker's X3 proves that one unknown subject is refused, not that a product refuses every actor without the right; that remains
  the product's obligation (docs/REAL_SOURCE_ONBOARDING.md).
- The reason is kept as the person wrote it, in the database and its backups, and shown to the issue's owner. When a request is
  repeated after a failure, the platform's row carries the reason of the request it recorded and the product keeps the first.
- The console does not offer the cancellation yet (S24-C).
