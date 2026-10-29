# S03-H independent review of source-confirmed issue

Scope: official documents issued under the source product's authority (ADR 0003, docs/S03H_SCOPE.md):
`DocumentAuthority` (PrepareDocument, ConfirmIssue, VoidIssue), deterministic rendering with the product's
issue time, void on failure and ISSUE_UNRECONCILED, migration 004. Implementation 95e0dd3, merged into main
as PR #11 (0abd6b1) on the user's instruction before the review ended ("立即合并，通过测试发现问题吧": merge
now and find problems through testing). Two reviewers worked independently of the author and of each other,
on git-archive copies, on two axes: correctness and security; test effectiveness, the PDF checker and
documentation accuracy. Both were briefed against the s00 baselines as well as the slice's own scope, since
the S03-G reviews had checked S03-G against its own scope only and missed that it contradicted the baseline.

## Round 1

### Correctness and security

Baseline on 95e0dd3: vet clean; `go test -p 1 ./...` and `-race -count=2` pass with no skips (PostgreSQL and
Typst present); the conformance and concurrency suites of both product simulators pass `-race -count=12`;
DOM 66/66; the checker 27 documents, 847 negative controls caught. The reviewer's own checks: a randomized
fault-injection model of the whole protocol (every product call, publish, fail and render failure; worker
death at six points), a differential fuzz of the two simulators (200 seeds × 60 operations), a real SIGKILL
of the demo between ConfirmIssue and Publish on PostgreSQL, 6 workers × 120 jobs on PostgreSQL under
`-race`, determinism and Typst width measurements, a character sweep of 23,298 characters and fuzzes of the
issue check against the real render, HTTP and DOM runs on the real server, and the migration tool with
concurrent migrators.

| # | Finding | Disposition |
|---|---|---|
| F1 | Major: PostgreSQL's Publish answered ErrLease when the source read expired in the last seconds while the lease was still valid; the worker failed the job LEASE_LOST without voiding, and the product kept a confirmed number for a file that was never published. An adapter answering the platform's lease error suppressed the void too | Fixed in PR #12. Publish answers ErrDenied for an expired source under a valid lease (as the memory store did); the worker believes a lost lease only when the store confirms it; an adapter's lease error is an integrity failure; tests on PostgreSQL that failed before |
| F2 | Minor: a table cell with a Latin letter next to `…` or `⋯` passed the issue check and failed the render (Typst draws it 0.59 pt wider), after the product had reserved a number | Fixed. Split around U+2026 and U+22EF whatever the neighbour; unit and Typst tests that failed before |
| F3 | Minor: the void was decided by a read and sent without a fence; a void delayed past the lease could void a number the next worker had confirmed and published | Fixed. The failure is recorded first as ISSUE_UNRECONCILED, fenced by the claim; then the void; then `ResolveFailure` records the job's own code (both stores, fenced on state, generation and code). A crash in between leaves ISSUE_UNRECONCILED, which is true. The reviewer's scenario and the crash case are tests |
| F4 | Minor: replay of `POST /issues` and `POST /jobs/{id}/cancel` of a succeeded issue job answered without the number; the console rejected the answer | Fixed. One job view for all three answers; HTTP and DOM tests |
| F5 | Minor: product references accepted any bytes (controls, NUL, invalid UTF-8); a NUL failed the PostgreSQL publication after the product confirmed | Fixed. Printable ASCII, 1–128 bytes, refused before anything is published; boundary tests |
| F6 | Minor: product times were checked only for whole seconds; a confirmation in year 10000 made the memory store panic with the job running | Fixed. Every product time within 1970–9999 (the issue time exactly the writer's range); the memory store returns an error; boundary tests |
| F7 | Minor: after an accepted down migration the issued PDFs were described as plain artifacts; they keep their issue link, cannot be downloaded and are listed nowhere | Fixed in the docs and the tool's messages |
| N1 | Note: no unique index on the product's numbers | Deferred: the product is the authority; a unique index would turn a product bug into a failed job and a void |
| N2 | Note: the row digest was named, not specified, so a non-Go product could not compute it | Fixed. API.md specifies the encoding with test vectors, pinned by a Go test |
| N3 | Note: the two simulators answer differently for a revoked principal with an unavailable source (pre-existing) | Deferred |
| N4 | Note: a panic in an adapter killed the worker | Fixed. A panic in a job or an adapter call is EXECUTION_FAILED; a panic in the void is a void that failed |
| N5 | Note: claims are not filtered by capability | Documented: every worker sharing a store runs with the same capabilities |
| N6 | Note: the application role can write jobs and assets (not new) | Accepted; the digests are corruption detection, not a signature |
| N7 | Note: a stale `.lock` after a kill blocks the demo's restart (pre-existing) | Deferred |
| N8 | Note: `plan` and `status` do not show the 004 precondition | Deferred |
| N9 | Note: the README still described platform numbering | Fixed with this record |
| N10 | Note: admission never asks the product, so any admitted principal can create jobs without bound | Deferred to the quota work of S04 |
| N11 | Note: a job failed ISSUE_UNRECONCILED shows no number | Deferred; the product finds the reservation by the job id |

### Tests, checker and documentation

Baseline the same. The reviewer mutated the code and the checker and recorded what the suites caught.

| # | Finding | Disposition |
|---|---|---|
| T-1 | The worker's row-index guard had no test, and a panic in a job would kill the worker | Fixed. Test of a crafted job beyond the snapshot; panics recovered (see N4) |
| T-2 | The void's time budget was not pinned | Fixed. A test pins the void timeout and that the fenced failure happens within the lease |
| T-3 | No table test of the number alphabet | Fixed. Every ASCII character at the first and later positions, controls, non-ASCII, length bounds |
| T-4 | Publication and view cross-checks were not tested field by field | Fixed. Each checked field altered in turn |
| T-5 | Location header, admission binding, canonical bytes and console record shapes untested | Fixed |
| T-6 | Tests could not tell the product's time from the platform's | Fixed. The simulator's clock runs 90 s ahead; the record, stamp and request carry the product's time |
| T-7 | Three test comments promised more than was asserted | Fixed. The assertions were added |
| T-8 | Redundant checks | No change: defence in depth |
| T-9 | No full-stack determinism test | Fixed. The real issuer and Typst writer issue the same job twice, also with another writer clock, to the same bytes |
| K-1 | The checker found the XMP dates by a literal prefix and compared only the creation date | Fixed. XMP parsed by namespace; every date the file carries compared with the issue time; controls for forged dates |
| K-2 | Non-ASCII digits matched in record times, numbers and ids | Fixed. ASCII only; a full-width control |
| K-3 | Missing controls for malformed numbers, ids, zones and a non-UTC issue time | Fixed. Controls added; a non-UTC time is reported and does not disable the date check |
| K-4 | The real-stack issued PDF was checked by veraPDF only | Fixed. The DOM test writes its expectation and CI runs the checker on it |
| D-1 | Down-migration effects misdescribed | Fixed (with F7) |
| D-2 | The issuance capability flag misdescribed | Fixed |
| D-3 | "A failed job shows no number" lacked the ISSUE_UNRECONCILED case | Fixed |
| D-4 | A stale comment about where layout refusals happen | Fixed |
| D-5 | S03-G's scope was not marked superseded | Fixed |
| D-6 | The failure order, canonical ids and the demo's messages undocumented | Fixed |
| D-7 | The cancel deviation from INTERFACES.md and the time rule unstated | Fixed |
| I-1 | The CI comment on `-p 1` omitted the shared source database | Fixed |

## Closing

At the user's request the process after round 1 was shortened ("永远只有你一个人开发，没有那么繁琐"): no second
review round; the lead engineer verified every fix against the reviewers' repro descriptions, the new tests
were run against the unfixed code and failed there (except the ones listed as coverage), and the full
qualification was run on the fixed code (evidence/s03h).
