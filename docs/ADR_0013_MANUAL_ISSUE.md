# ADR 0013 · Issuing from manual datasets: the operator numbers the documents and answers for them

Date: 2026-11-06. Status: **proposed**. Engineering had marked it accepted on 2026-11-11 under the owner's general delegation; who
issues under whose number is the owner's own decision (CODING_STANDARDS.md), and, asked that evening, the owner chose to wait with
the manual source and its issuing for the outcome of the real-source discovery (ADR 0012's status). Nothing of it is built; the
answers to the open questions at the end are engineering's recommendations until the owner gives them. Once accepted it amends ADR
0003 decisions 1 and 5, and the same rule in docs/s00/ARCHITECTURE.md and
docs/s00/INTERFACES.md, for the datasets of a manual source (ADR 0012) and for nothing else. Implementation: S08-D, after
S09-G (the issue page, where manual issuing is done from).

## Context

ADR 0003 gives every formal document to the source product: "The product authorizes every issue, owns the number and
confirms the file. The platform renders the document and keeps the formal file and its record; it never invents a
number and never issues alone." Migration 004 withdrew the platform numbering of S03-G. The capability is the adapter's
`reporting.DocumentAuthority`. `PrepareDocument` authorizes, checks the row against the source's own snapshot, applies
the source's approval rules and reserves the number and the issue time; `ConfirmIssue` binds the number to the file's
digest and page count; `VoidIssue` voids what the source holds for a failed job. Each call is idempotent by the
platform's job id, and every outcome is reconcilable by that id instead of atomic across the two systems (ADR 0003
decision 4). `VersionedAuthority` adds the group intents of ADR 0004. Both simulators implement the capability; the
durable one gives out `<PREFIX>-<6 digits>` from one locked row per source (`adapters/contractsim/pgsource/issue.go`),
and one conformance suite runs against both (`adapters/contractsim/internal/authoritytest`).

A manual dataset has no product behind it. On 2026-11-06 the owner decided that formal documents may be issued from
manual datasets, with numbers the platform gives and the platform's operator answering for them, instead of keeping
manual data out of issuing.

## Decision

1. **The manual source is the issuing authority of its datasets.** It implements `DocumentAuthority` and
   `VersionedAuthority`. The core's issue path does not change: the worker checks, asks the adapter to prepare,
   renders, confirms, and voids on failure. The platform's store still holds no number counter, and migration 004
   stands. What changes is who stands behind the number. For a manual dataset, ADR 0003 decision 1 reads "the manual
   source authorizes every issue, owns the number and confirms the file", and the operator who runs the platform
   answers for it. Decision 5 does not reach the manual source's own database. For every other adapter, ADR 0003 is
   unchanged.
2. **Number series from the configuration.** A manual connection that issues names its series in its options: a prefix
   of 1 to 8 characters, A–Z and 0–9, starting with a letter (the simulators' rule). Numbers are `<PREFIX>-<6 digits>`,
   with more digits after the millionth, consecutive within the series, never reused and never restarted, given out by
   one locked row in the manual source's database. A voided or cancelled number stays used, so every gap in a series
   is a void or a cancellation on record. Numbering that restarts each year is a new series with a new prefix, by a
   configuration change. A connection without a series has no document authority, so issuing from it answers 422, as
   it does today for an adapter without the capability.
3. **Only from a version a second person confirmed.** On a connection that issues, the person who confirms a version
   cannot be the person who saved it. `PrepareDocument` refuses (`ErrDenied`) unless the principal holds the `issue`
   role, the frozen version is still the dataset's current confirmed version (a correction confirmed after the
   snapshot needs a new snapshot), and the row, or every row of the group, is the version's row (`RowSHA256`), as a
   product checks against its own snapshot.
4. **A register of the documents.** The reservation records the job id, the intent's digest, the dataset and version,
   the row or group, the template's id, version and digest, the principal, the number and the issue time. The issue
   time is the manual source's database clock; the document prints it and the PDF takes it as its creation date, as
   for any source. `ConfirmIssue` adds the file's digest and page count, and `VoidIssue` marks the record void with the
   platform's reason. This register is the record of the operator's documents. The editor page lists it, and it can be
   exported.
5. **Cancelling an issued document.** Someone with the `issue` role can cancel (作废) a confirmed document through the
   manual source's API, giving a reason. The number stays used, and the register shows who cancelled it, when and why.
   The platform's issue record and its file do not change, as when a product cancels a document in its own system. A
   corrected document is a new issue with a new number.
6. **Not for documents that another system must number.** The manual source's numbers are the operator's own. A
   document that a law or a tax system requires that system to number or issue, such as a 发票, is not issued this way.
   The platform cannot check this; the manual and the deployment checklist say it.
7. **One slice, S08-D**, after the three of ADR 0012. The conformance suite moves out of `contractsim/internal` so that
   it also runs against the manual source's two stores. PostgreSQL tests cover concurrent reservations in one series,
   the second person's confirmation, the void and the cancellation. docs/API.md, docs/SECURITY_REVIEW.md (§7), the
   manual and the deployment checklist are updated.

## Consequences

The platform's operator becomes the issuing party for manual documents: a wrong row, once two people have confirmed
it, becomes a formal document under the operator's number. The baseline's "平台……不独立批准业务单据" no longer holds for
these datasets: the approval is the manual source's second confirmation, inside the platform's deployment. The controls
are that confirmation, versions that never change, the register, and cancellations with reasons. There is no product
to reconcile with, so the register and its backups are the record. A deployment that issues from a manual source must
state, before its admission decision (checklist item 19), who holds the `confirm` and `issue` roles, which series it
uses and how the manual source's database is backed up. Products' documents are unchanged. An integration's documents
are numbered by its product or by a manual source, never by both, because a connection has one adapter.

## Open questions

- Is a second person's confirmation (decision 3) the right control, or should one person be able to confirm and issue
  on a connection that issues?
- Are the series of decision 2 enough: one prefix per connection, numbers that never restart, a new prefix for a new
  year?

## Recommended answers to the open questions (2026-11-11; engineering's, not yet the owner's)

- **A second person confirms before anyone issues.** Decision 3 stands, and it is the same control ADR 0018 decision 3 gives publication
  in the shared design space: the person who saved the version cannot be the one who confirms it, and only a confirmed version is issued
  from. One person who may both confirm and issue may still not confirm their own version. A connection cannot switch this off: the
  numbers of ADR 0013 are the operator's own, and the second pair of eyes is what the operator has in place of the source product's approval.
- **Series per dataset, never restarting.** Decision 2 is amended in one respect: a series belongs to a dataset of the connection, not to
  the connection, because a connection may hold several kinds of document (an invoice and a receipt do not share a sequence). The
  dataset's options name its prefix (1 to 8 characters, A–Z and 0–9, starting with a letter); numbers are `<PREFIX>-<6 digits>` from one
  locked row per series, consecutive, never reused, never restarted; a yearly restart is a new prefix (such as `AR26`), which is a new
  series and a new locked row. Two datasets may not name the same prefix within a connection.
