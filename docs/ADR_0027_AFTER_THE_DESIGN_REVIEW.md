# ADR 0027 · After the design review: a reporting system as well as a document system, proven on a real source

Date: 2026-11-11. Status: **accepted**. Decisions 1 and 2 are the product owner's answers of 2026-11-11. Decision 3, the order,
is engineering's; each item of it that is contract-level waits for its own ADR and the owner's answers before it is built
(CODING_STANDARDS.md). It succeeds ADR 0017, whose roadmap is done except for what waits for others.

## Context

On 2026-11-11 the owner asked whether the platform's design is professional, sound and complete as a reporting system. The
functional review of earlier that day had listed what was missing as features (filters and sorts, charts, table formatting, email,
an issue page; ADR 0023 to 0025 and their slices). The design review looked at what more features would not fix. It found the
platform strong as a backend that renders and issues trusted documents: the source's authority over every read and disclosure,
exact values, fenced jobs, the store's lifecycle, the tests. It found seven problems of design:

1. One lifetime, the source read's, for everything made from a read. ADR 0026.
2. A read is at most 1000 rows and 1 MiB, and a snapshot is one JSON value in one row.
3. The platform computes nothing, so a chart draws the rows of a page and every analytical cut is a dataset the source must
   publish.
4. There is no report: nothing a person opens by name, with its parameters, and runs. Layouts, dashboards and templates are
   designs without parameters, and ADR 0024 rejected a report object.
5. The catalogue cannot say that a value is absent, has no type for an instant, and does not say whether a metric may be summed.
6. Nothing real has tested any of it. The only sources are simulators written by the platform's own team; S02-A found no ledger
   to read in Brant and, for Parcel, read a repository that is not Parcel's backend (docs/REAL_SOURCE_DISCOVERY_PARCEL.md).
7. Decisions about the contract were proposed, accepted and built by the same hand in the same day. The rule now in
   CODING_STANDARDS.md.

The owner was then asked three things. The lifetime model is ADR 0026. The other two answers are below.

## Decision

1. **The platform is both** (the owner). Asked to choose between documents with summary reports first, a general reporting system
   now, and documents and exports only, the owner answered that all of it is needed. The platform issues documents under a
   source's authority, and it is a reporting system in the general sense: reports a person opens by name, results larger than a
   thousand rows, grouping and pivoting, charts that mean something. Nothing built is dropped, and the dashboards go on.
2. **IDP Parcel is the first real source** (the owner): it has a backend. docs/REAL_SOURCE_DISCOVERY_PARCEL.md records what that
   backend is today and what it lacks as a source. The order "real integration last", which the owner gave at S03-A, ends here.
3. **The order of what is new.** What the functional review started (S08, S09-D and F, S16-C and D, S20-B and C, S21-B, S23-B, S24)
   goes on beside it.

   | Slice | What | Why here | Waits for |
   |---|---|---|---|
   | S25 | A result outlives its read (ADR 0026) | Before any product implements the protocol | In progress |
   | S26 | The catalogue's semantics: absent values, instants, whether a metric may be summed (ADR 0028, proposed) | The first real datasets need all three, and they change the protocol | The owner's answers to ADR 0028 |
   | S27 | Parcel, one thin dataset end to end: the protocol implemented in Parcel, the checker passed, the totals reconciled with Parcel's own statements, a real person reading a real report | Evidence before more design | The owner's answers to section 8 of the discovery: who builds in Parcel, what a company is, how identities map, an environment |
   | S28 | The report: a named object (dataset, parameters, filters, sort, layout) that a person opens and runs; a list of reports; a schedule of a report | The largest gap for a person who is not a designer, and the one that depends least on the contract | Its own ADR: who may see a report is authorization |
   | S29 | Results beyond a thousand rows: a snapshot's rows out of the single JSON value, paging and exports that stream, the protocol's bounds | Detail reports and bulk exports | Its own ADR: the protocol and the store |
   | S30 | Grouping and pivoting on a frozen snapshot: the platform aggregates what it already holds under the source's authorization, by the additivity the metrics declare | Charts and cross-tabs without a dataset per cut | Its own ADR: it changes the baseline's rule that the platform computes nothing; after S26 and S29 |

   The dashboards are drawn from S30's aggregates once they exist.
4. **Evidence before features.** S27 comes before S28 to S30 on purpose. What a stable read is, how a frozen scope is authorized
   again, and every bound of the protocol have been implemented only by simulators; the first real implementation will change
   some of it, and S29 and S30 should build on the contract as it is after that. If Parcel's side cannot start yet (Parcel has
   frozen work that does not block its own launch, and it has no deployed environment), S28 goes first.
5. **What does not change.** The source's authority over every read and every disclosure; exact values; fail closed; a core that
   needs only the standard library; the five checks.

## Consequences

- ADR 0028 is written now, as a proposal with its questions, because S26 is next after S25.
- S27 needs work in another repository, under that repository's own rules (Parcel takes an ADR of its own for a new way in).
  This repository's part is the protocol, the checker, the reference source and the reconciliation.
- docs/REAL_SOURCE_ONBOARDING.md's paragraph about Parcel is rewritten at S27 from the discovery; its paragraph about Brant stands
  until Brant has a receivables ledger to read.
- S28 reopens an alternative ADR 0024 rejected ("a separate report object over snapshot and layout"). ADR 0024's reason stands
  for an export, which names a snapshot and a layout; it does not give a person something to open tomorrow.
