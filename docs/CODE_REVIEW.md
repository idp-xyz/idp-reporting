# S03-A independent review

Date: 2026-10-26 Asia/Shanghai.
Fixed point: accepted S02-A 6f782bd7b4b7b2b4b554cd934e86b41609d0fb6b.
Final implementation: 7403fdbd23ad7fa8f6ba75cc2984aa777cd65b5c.
Diff: git diff 6f782bd7b4b7b2b4b554cd934e86b41609d0fb6b...7403fdbd23ad7fa8f6ba75cc2984aa777cd65b5c.

Specifications: docs/S03A_SCOPE.md, docs/CONSOLE.md, S00 trust/source contracts
and the user's instruction to finish platform capabilities before real integration.
Standards: CODING_STANDARDS.md and scope/evidence rules in docs/VALIDATION.md.
Two independent review axes ran in parallel under the applied code-review workflow.
No reviewer edited files, changed product repositories, retried the blocked browser,
or requested early real identity/product integration.

## Standards axis — PASS

Reviewer: s03a_standards_review.
Final independent complete DOM run: 21/21 passed, zero failure/cancellation/skip.
All 44 qualified source hashes and 23 unchanged core/adapter/storage/module hashes
matched the fixed implementation.

No unresolved hard standards violation or substantive Fowler smell.
Same-origin bounded requests, token cleanup, static/API composition, exact string
precision, authorization failure handling and scoped task errors met the standards.
Private preview configuration, runtime binaries and node_modules are not in Git.

## Specification axis — PASS

Reviewer: s03a_spec_review. Final independent complete DOM run: 21/21 passed.

(a) Missing/partial items: real browser layout, responsive visual/accessibility
and real browser downloads remain unverified. The observed route/URL-policy block
is an explicitly allowed, disclosed environmental deferral in S03A_SCOPE.
Three designers, templates/PDF, embedding, complete durable task history,
production operations and real integrations remain subsequent work.

(b) Unsolicited scope: none confirmed.

(c) Incorrect behavior: all confirmed findings below closed. No new confirmed
specification defect at the final fixed implementation.

## Findings and verification

1. P2 paging race (initial 00b37668): overlapping Next requests consumed a cursor
   twice; changing page size during a pending page could mix page/count state.
   Fixed by serializing result operations, capturing target page before awaiting,
   disabling pagination while pending and restoring a queued limit change.
2. P2 catalog race (initial 00b37668): repeated same-product responses duplicated
   options; old product failures could clear a newer result.
   Fixed by invocation generation checks on success and failure, option replacement,
   loading guards, and stale-error suppression.
3. P2 stale task errors (found at 7f9aa7c): row refresh/cancel errors could clear
   results after query change. Fixed by capturing view and task snapshot context;
   task state still updates, but stale notices/results do not.
4. P2 task resource error scope (found at 647b28a): bulk refresh, restore and task
   result/file actions could clear unrelated current results. All task entrypoints
   now carry their matching snapshot context; partial bulk failure is not overwritten
   by a success message. Current denial still clears the corresponding snapshot.

Four regression tests fail against the initial application; three further task
tests fail against 7f9aa7c; four task-entrypoint tests fail against 647b28a.
These 11 expected old-code failures are in evidence/s03a_diagnostics, never counted
as current acceptance failures.

Independent review also found a nondeterministic test latch: release callbacks
were recorded after network completion, rather than invocation. Registration now
happens before awaiting HTTP. Five focused repeats passed; both final reviewers
independently ran the full 21-test suite successfully.

Current evidence/s03a: ordinary/race each 33 top-level / 164 parent-child pass
events, zero failures, two explicit DB entry skips; vet/build/mod verify and JS
syntax passed; five actual XLSX independently verified. This is software DOM and
real loopback HTTP evidence, not browser/visual or production qualification.

The final documentation/provenance commit changes no qualified source hash.
Intermediate 9/13/17-test checkpoints and historical S01-B/S02-A evidence remain
separate and retain their stated limits.
