# S01-B two-axis code review

Skill: code-review. Fixed point: accepted S01-A delivery
ec09065dc024419dc2e2e6e9cc2d1dcae6ee7132. Diff: git diff ec09065...HEAD.
Specification: docs/S01B_SCOPE.md and S00 migration/interface baseline.
Standards: CODING_STANDARDS.md and current README fixture constraints.
Standards and Spec were reviewed by two independent parallel agents.

## Standards

One P2 documented-rule breach: the recovery probe originally drained every ZIP
entry with io.Copy, so the compressed HTTP cap did not limit expanded bytes or
ensure cancellation during decompression. This violated the bounded-work rule.

Fixed in fe7729f94ca853150da535a51eaae00c96760b1e: at most64 entries,32MiB aggregate
expanded bytes, header/budget validation, a32KiB bounded-read loop with context
checks, and reader close on failure. Reviewer rechecked: PASS,0 open rule breaches,
0 substantive baseline smells. Final runtime qualification is separate evidence.

## Spec

0 confirmed findings through8f09a08. The diff implements persistent source
versions/handles/scopes/grants in a separate protected database, metadata-only
admission, revocation-preserving startup, canonical metadata SHA and owner
consistency, atomic publication/cancellation and actual process/PG recovery.
Generic core and four-operation Adapter interface remain unchanged. No scope
creep found. Newest durability cases were correctly treated as pending until the
final rerun. Historical S01-A review is preserved in CODE_REVIEW_S01A.md.

## Final verification delta

Independent auxiliary reviews checked the final scripts, status claims and raw
logs through85bd987. Spec PASS:36 PG children+2 parents per ordinary/race run,
18 host top-level/35 events with2 explicit DB skips each,4 actual server
processes/changed PG start,14 workbook hashes,3 expected DSN failures and all29
current Go hashes agree. The durable two-page version-change test passed.

The Standards axis found a second P2 verifier issue: selecting fixture expectations
from each workbook's own dataset metadata could accept consistently swapped
finance/shipment files. d3d4d33 independently binds all14 filenames to literal
request/dataset expectations, asserts metadata against that contract, and checks
known exact values. All14 positive files pass; a temporary swapped-file negative
control fails at the dataset/name assertion without modifying originals.

Standards final PASS through d3d4d33: both findings closed,0 open rule breaches,
0 substantive baseline smells. No Go/runtime changes followed829a9bf; no new
PostgreSQL run was required for the verifier-only fix.

Summary: Standards2 findings fixed,0 open; Spec0 confirmed findings,0 open.
