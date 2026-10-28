# S03-H source-confirmed issue

2026-10-28. Corrects S03-G (docs/S03G_SCOPE.md) to the architecture baseline: the source product
authorizes every formal document, numbers it and confirms the issued file; the platform renders and keeps
it (docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md). Everything of S03-G not named here stays: the issue job and
its admission rules (published version only, frozen canonical template, request hash, authorization of
fields and background), rendering through the document page model with the stamp in the banner strip,
artifact and record published in one transaction, listing and reading routes, download rules, the console
flow and the checker. Platform numbering is withdrawn.

H1 · The product capability. `reporting.DocumentAuthority` (reporting/types.go) is an optional adapter
interface: `PrepareDocument(sc, IssueIntent) -> IssueReservation`, `ConfirmIssue(sc, IssueConfirmation) ->
IssueReceipt`, `VoidIssue(sc, jobID, reason)`. Every call is idempotent by the platform job id; the
semantics are in the interface's comments and are the contract the simulators and real products implement.
The intent carries the frozen snapshot's source read, the row index, the digest of the row's values
(`Digest(row)`) and the template id, version and digest; the product checks the row against its own source
snapshot. A reservation carries the product's reference, the number (`ValidIssueNumber`: 1..32 of
`A-Za-z0-9._-`, starting alphanumeric), the issue time (UTC, whole second) and an expiry. A receipt carries
the product's confirmation reference and time. An integration whose adapter does not implement the interface
cannot issue: `POST /issues` answers 422 and a stored job fails with UNSUPPORTED_COMBINATION.

H2 · The worker. For an issue job the worker, after the S03-G checks (template still published, snapshot
current and verified, row present), calls `PrepareDocument`; validates the reservation (reference 1..128
bytes, the job's id, a valid number, issue time a UTC whole second, expiry after it; else
INTEGRITY_FAILURE); renders with the stamp `ISSUED No. <number> - <issue time> - <record id>`, set at 8 pt in
the banner strip so that the stamp of every number the contract allows fits with every record id and time
(the widest measures 534.6 of the strip's 538.6 pt), and the issue time as the PDF's creation date, so the
bytes depend only on frozen inputs and the reservation; calls
`ConfirmIssue` with the digest and page count and validates the receipt (reference 1..128 bytes, time a UTC
whole second not before the issue time); re-checks the source as before; publishes the artifact and the
record. When the job fails after this attempt called `PrepareDocument` (whatever its outcome), or after a
claim of a later generation (an earlier attempt may have reserved or confirmed), other than by the lease
being lost to another worker, the worker calls `VoidIssue(job id, error code)` with a detached context of at
most 5 seconds before failing the job; if the void cannot be delivered the job fails with
ISSUE_UNRECONCILED instead of its own code. A first attempt that fails before reaching the product calls
nothing there. A lost lease never voids: the worker that holds the job reuses the same reservation (idempotent by job
id), renders the same bytes and confirms idempotently. Issue jobs can be cancelled only while queued; the
cancel route returns a running issue job unchanged.

H3 · Records and views. The record (`reporting.Issue`) holds the product's number (string), the
reservation and confirmation references, the confirmation time and the issue time from the product, beside
S03-G's fields; the stamp is recomputed from number, issue time and id wherever it is checked. A job carries
no number. The job view's `issue` summary gains `number` only once the job succeeded (read from its
record); `GET /issues` and `GET /issues/{id}` return `number` (omitted until succeeded), `reservation_ref`,
`confirmation_ref`, `confirmed_at` and `issued_at`. The download keeps the name `issued-<record id>.pdf`;
the console saves it as `issued-<number>.pdf`.

H4 · Storage. Migration 004 drops `issue_numbers`. Its up step refuses (with the reason and the remedy
`down --to 2 --accept-data-loss`) while issue jobs or issue records exist: they are S03-G's platform-numbered
documents, never confirmed by a product. Its down step recreates the empty table and refuses while issue
jobs or records exist unless data loss is accepted (they would be unreadable by an S03-G binary); accepted,
it deletes them, the issued PDFs staying as plain artifacts. The application role's data tables become
`jobs` and `assets`; `roles` still accepts the older schemas.

H5 · Simulators. Both contract simulators (in memory and durable) implement the capability: numbers
`<PREFIX>-<6 digits>` per integration and source tenant (`AR` for the finance fixture, `SH` for shipment),
consecutive, never reused, a voided number stays voided; the issue time is the simulator's clock; a
reservation expires 10 minutes after it was made or with the source read, whichever is first; the grant
gains `IssueDocuments` (true in `DefaultGrant`) and a principal without it is refused; the row is checked by
re-reading the frozen source read and comparing `Digest(row)`; a per-source switch refuses as "not approved"
for tests. The durable simulator stores reservations in its own schema (version 2, upgraded from 1).

H6 · Console and checks. The console submits as before, shows the product's number once the job succeeded
(`正式出具 No. AR-000012`), hides cancel for a running issue job, lists records with the number,
confirmation reference and time, and downloads as `issued-<number>.pdf` after checking the record's digest.
The checker reads the number as a string and recomputes the stamp from the record; its controls stay.

Not in this slice: product-side reconciliation tooling beyond lookups by job id, retries of a failed void,
batch issue, revocation or correction of a confirmed document, real product adapters (last), embedding
(next), the remaining S04 work.
