# ADR 0025 · Issue operations: a cancellation is the product's decision, and a batch is many issues by one person

Date: 2026-11-11. Status: **accepted** by the product owner on 2026-11-11. It is a contract-level decision (the adapter contract and
the source protocol, what the platform stores of business data, who may see what), so it is the owner's own (CODING_STANDARDS.md).
Engineering's acceptance of the same day under the owner's general delegation ("按你的建议全面开始吧"; the functional review's fourth
gap) was not that answer; the ADR was then put to the owner as one question, and the answer is at the end. Extends ADR 0003 (it
changes none of its decisions) and amends ADR 0013 decision 5 in one respect. ADR 0026 (accepted the same day) changes, once its
slices have landed, two sentences here about how long a file lives; they are marked where they stand.
Implementation: S24-A (cancellation on the server: written, docs/S24A_SCOPE.md), S24-B (batches on the server), S24-C (the console's
issue page).

## Context

Since S09-G issuing has a page of its own, and it issues one document at a time: a template, one row or group, a confirmation, a number.
Two things an operator does every period have no place in the platform.

**Withdrawing an issued document.** ADR 0003's `VoidIssue` is the platform cleaning up after itself: it voids what the product holds for a
job that failed, whatever the principal's rights. A person who must withdraw a document that was issued correctly but should not stand
(the wrong customer was picked, the order was cancelled) does it in the product, and the platform's register goes on saying 已出具 and
handing out the file. ADR 0013 decision 5 gave the manual source a cancellation "through the manual source's API", a second way beside
the product's own.

**Issuing many.** The period's statements are hundreds of documents from one snapshot through one template. Today that is hundreds of
passes through the page.

What bounds the answers:

- The product authorizes every issue, owns the number and confirms the file; nothing is atomic across the two systems, and every outcome
  is reconcilable by the platform's job id (ADR 0003 decisions 1, 2 and 4). A number is never reused.
- A snapshot lives as long as the source read it was frozen from, **at most 15 minutes** (`Service.validPrepared`; the protocol's
  `MaxReadLifetime`), and an issue is admitted and rendered only while it lives. Whatever a batch is, it is over in a quarter of an hour.
- An identity's jobs are bounded (S04-D: 8 in flight, 120 admissions an hour by default), and a job, a record and a file belong to the
  identity that made them (row-level security).
- An issue is a person's decision: it is never scheduled (ADR 0020 decision 1), an `issuer`'s (ADR 0018), and its audit event must be
  written or the issue is refused (ADR 0018 Q4).
- The record of an issue is permanent; the file lives while the snapshot's read does and the retention's grace after it (S04-B).

  *Note, 2026-11-12 (ADR 0026, accepted 2026-11-11).* This holds until ADR 0026's slices (S25-A to S25-D) have landed. S25-A, in
  the code since 2026-11-12, separates the read, the scope and the result in the core and the stores, and keeps a file beyond its
  read only within a scope its source vouches for; no source of a deployment can vouch before S25-B, so until then every file
  still ends with its read. After that an issued file is kept by its class, 10 years by default, and handed out for as long, each
  download still authorized by the source (ADR 0026 decisions 3 to 5). What does not change: issuing still needs the read (ADR
  0026 decision 6), so the second bound above stands and a batch is over when the read is; and the refusal of a cancelled file
  (decision 4 below) stands for as long as the file is kept, which S24-A is built and tested for on S25-A's code.

## Decision

### Cancellation

1. **A cancellation is asked of the product, which decides.** A new optional adapter capability beside `DocumentAuthority`,
   `reporting.DocumentCanceller`, with one call: `CancelDocument(ctx, SourceContext, IssueCancellation) (IssueCancellationReceipt, error)`.
   The request names the issue by the platform's job id, the product's reservation and confirmation references and the number, and
   carries the person's reason (1 to 200 characters of text, no control characters). The product refuses (`ErrDenied`) when the principal
   may not cancel or its own rules say no (the document was settled, a period was closed), answers `ErrConflict` for a document that is not
   confirmed, and is idempotent by the job id: asking again for a cancelled document returns the same receipt (its cancellation reference
   and time), whatever the reason says the second time. The number stays used. `VoidIssue` keeps its meaning and its callers (the worker,
   for a failed job); a void of a job whose document was cancelled is, as before, not an error. An adapter without the capability cannot be
   asked: the route answers 422, as issuing does for an adapter without `DocumentAuthority`.
2. **The route is the issuing identity's, and synchronous.** `POST /issues/{id}/cancellation` with `{"reason": "…"}`: an `issuer`'s, for an
   issue of the caller's own that succeeded (another identity's is not found, as every record is). The platform asks the product within the
   request (one call, the adapter's timeout; no job, nothing to render), then writes the cancellation and its audit event (`issue.cancel`)
   in one transaction and answers the issue as the listing shows it. If the product cannot be reached the answer is 503 and nothing is
   written; if the platform fails after the product cancelled, the person asks again and the product's idempotent answer completes the
   record: the outcome is reconcilable by the job id, as every issue outcome is. The audit event that cannot be written fails the request
   (ADR 0018 Q4 names the issue; a cancellation is the same act in reverse), and the retry completes it.
3. **The record is added to, never changed.** A cancellation is its own immutable row (`issue_cancellations`: the issue, the product's
   reference and time, the reason, when the platform recorded it), inserted once by the application role, never updated or deleted, under
   the row-level security of the issue it belongs to. The issue record keeps every field it had. Views of the issue (`GET /issues`,
   `GET /issues/{id}`, an issue job's view) gain `cancellation: {ref, cancelled_at, reason}`; the state word stays the job's (`succeeded`),
   so that a client that does not know cancellations reads what it read.
4. **A cancelled document is no longer handed out.** `GET /artifacts/{id}/content` of a cancelled issue answers 409 `ISSUE_CANCELLED`. The
   platform cannot recall the copies already downloaded and does not alter a file the product confirmed (a stamped copy would be another
   file, with a digest the product never saw); what it can do is stop being the place the document comes from, and keep saying, in the
   register, the number, the digest and the page count of what was cancelled, so that any copy can be checked against the record.
5. **The source protocol** (docs/SOURCE_HTTP_PROTOCOL.md) gains the optional route `issue-cancellations` in a minor revision, declared by
   the connection as `issuance` and `companies` are; `sourcetest` serves it; the conformance checker gains rules for it (a confirmed
   document is cancelled once and the same receipt comes back; an unconfirmed or unknown one is refused; another principal is refused; the
   number is not given out again), run with the issue rules. Both contract simulators implement the capability. `GET /capabilities` says
   per product whether cancellation can be asked, so that a client offers it only where it can be.
6. **The manual source cancels the same way.** ADR 0013 decision 5 is amended: the manual source implements `DocumentCanceller`, and its
   documents are cancelled through the platform's route like any product's, not through a route of the manual source's own API. Who may,
   what the register shows and that the number stays used are as decision 5 says.

### Batches

7. **A batch is many ordinary issues, made by one person while the snapshot lives.** Each document of a batch is an issue exactly as
   today: its own `POST /issues`, job, `PrepareDocument`, number, render, `ConfirmIssue`, record, audit event and quota. The product cannot
   tell a batch from a person issuing fast, and the source protocol does not change. A batch is not atomic (ADR 0003 decision 4): a
   document the product refuses or the layout cannot print fails with its code, and the others stand.
8. **The server plans, the client paces.** `POST /issue-plans` (an `issuer`'s; it admits nothing and stores nothing) takes a published
   template version, the caller's snapshot and a selection — `{"all": true}`, `{"rows": [...]}` or `{"groups": [[...], ...]}` — and answers
   the list of documents that selection is: one item per row for a template issued by row, one per row group for a template issued by
   group (its key values and its row count), in the snapshot's order, each checked as `POST /issues` checks one (the publication, the
   binding, the row or the group), so that what cannot be issued is refused before any number is reserved. At most 500 items: a larger
   selection is refused with the count, and the person narrows the snapshot with the catalogue's filters (ADR 0023). The client then
   issues the items one request each, a few at a time, under its own token, its roles and its quotas of that moment: a quota refusal makes
   it wait, a refusal of the session stops it. Nothing runs on the server on the person's behalf after the person is gone.
9. **A batch has a name the register knows.** `POST /issues` takes an optional `batch`: an id the client makes for the batch (the form of
   the platform's ids), kept on the job and shown in the issue's views; `GET /issues?batch=<id>` lists the batch's issues, so that the page
   shows the batch's progress, survives a reload and can go on with what is left. A label groups at most 500 issues of one identity; it is
   part of the request the Idempotency-Key binds. It is a label and nothing else: no state, no order, no rights.
10. **What a batch does not do.** It does not download hundreds of files: each is downloaded from the register as today (an archive of a
    batch's files would be a new kind of artifact, not decided here). It does not outlive its snapshot: documents not issued when the read
    expires are not issued, the page says which, and the person freezes again and plans the rest (group keys carry over; row indices do
    not, and the page says so). It is never scheduled.

    *Note, 2026-11-12 (ADR 0026, accepted 2026-11-11).* "It does not outlive its snapshot" is about issuing, and stays true when
    ADR 0026's slices have landed: an issue is admitted and rendered only while the read lasts (ADR 0026 decision 6). What changes
    then is that the batch's files outlive it: until a source can vouch for a scope (S25-B) each file is downloaded while the
    snapshot's read lasts, and after that the files are kept by their class and downloaded from the register afterwards.

### The console (S24-C)

11. The issue page's register gets 作废… on an issued document of a product that can cancel: a dialog that names the number, asks the
    reason and says what it does (向来源产品申请作废；编号不再使用；文件不再提供下载), then the row says 已作废 with the reason and the time.
    The page gets 批量出具: the selection (全部 / 行号范围 / 分组), the plan's count and what will not be issued, a confirmation that names
    the template, the count and that each document gets its own number from the product, then the progress (已出具 n / m, the failures
    with their reasons, the time the snapshot has left), 停止 and, after a reload or a stop, 继续. Two baseline tasks join the held totals
    (ADR 0011 decision 7): 批量出具三份 and 作废一份已出具的文件, measured before on main and after on the slice.

## Alternatives

- **A batch job on the server** (a stored list of items, a runner that admits them as the quotas allow, leases, the owner's binding in
  force at each admission, as the scheduler's runs). It survives a closed tab, which matters when a batch takes hours; here a batch is
  over when its snapshot is, in at most 15 minutes, and the person is at the page. The runner would act for the person after the
  person's last request, which the schedules needed a rule for (ADR 0020 decision 2) and an issue should not need. The plan and the label
  give the page everything the runner's state would, at the cost of one read-only route and one column.
- **One admission of N jobs, exempt from the quota of jobs in flight.** The store would take hundreds of queued jobs of one identity at
  once, ahead of everybody else's: exactly what the quota prevents (S04-D).
- **Cancelling by `VoidIssue`.** It is the platform's cleanup, deliberately independent of the principal's rights, and final for a job id
  whatever it held. A person's cancellation must be authorized for that person by the product, and refused when the product's rules say
  no.
- **Keep serving a cancelled file, marked in a header or the file name.** A header is not seen by the person who opens the PDF.
- **A stamped copy (已作废 across the page).** A new file under the old number with a digest the product never confirmed; if a product wants
  one, it is the product's to issue.
- **A register for the whole tenant**, so that a colleague can cancel. Records are their owner's (row-level security); who else may see
  them is a decision about the register, not about cancelling, and is not taken here. The product's own system cancels for anyone it
  lets.

## Consequences

The issue contract gains a third optional capability, the protocol a minor revision, the checker its rules; a product that does not
implement it is unchanged, and its documents are cancelled in the product as today, with the platform's register not knowing (the page
says, for such a product, that cancelling is done in the product). The platform's store gains one append-only table and one nullable
column, each in a migration of its own slice. The security review gains a write path that reaches the product on a person's word (the
cancellation: the reason is the only free text, bounded, stored, never logged) and a read-only planning route bounded at 500 items.
A deployment that issues in batches sets `quotas.admissions_per_hour` to what a batch needs (the default 120 is two batches of 60);
docs/DEPLOYMENT.md says so. The manual source (S08-D) implements one more capability and no cancellation route of its own.

## Slices

- **S24-A** — cancellation on the server: `DocumentCanceller`, the protocol revision, `sourcetest`, the checker's rules, both simulators,
  the store (the table, its policy, the audit's `issue.cancel`), the route, the views, the download's refusal, `GET /capabilities`, the
  OpenAPI descriptions, the documents. No console change.
- **S24-B** — batches on the server: `POST /issue-plans`, the `batch` label (the column, the listing's filter, its bound), the OpenAPI
  description, the documents. No console change.
- **S24-C** — the console's issue page: 作废 and 批量出具, the English catalogue, the DOM tests, the two baseline tasks. After S24-A, S24-B
  and S09-G.

## The owner's answer (2026-11-11)

The ADR was put to the product owner as one question with three answers: (a) both parts as recommended — a cancellation is asked of
the product, which decides, and a cancelled file is no longer handed out (decisions 1 to 6); a batch is planned by the server and
paced by the client (decisions 7 to 10); (b) the cancellation alone, the batches left for later; (c) nothing of either until ADR
0026's slices have landed.

- **(a), both parts as recommended.** The owner chose (a) over (b) and (c). The decisions above are unchanged by the answer; what
  ADR 0026 changes about a file's lifetime is noted where the two sentences stand, and changes nothing of what S24-A builds.
