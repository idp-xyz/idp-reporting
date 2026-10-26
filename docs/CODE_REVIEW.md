# S01-A two-axis code review

Skill: code-review. Fixed point is the specification-only initial commit
`465d2c2efa9a725092de603d862503b34340430f`, selected from the accepted S00/S01-A
implementation baseline. Diff: `git diff 465d2c2...HEAD`.

Standards and Spec ran as separate parallel reviewers. The originating scope is
docs/S01A_SCOPE.md and docs/s00/{ARCHITECTURE,INTERFACES,MIGRATION}.md.

## Standards

Final reviewer status: **PASS at 084fc87**. No unresolved documented-standard
violations or substantive baseline-smell findings.

Two substantive issues were found and fixed:

1. Memory publication initially checked the lease before copying its payload.
   `e1bda3f` moves candidate materialization before a final context/lease/TTL
   check at the atomic mutation point. Reviewer rechecked and closed it.
2. PostgreSQL deferred rollback originally stripped cancellation/deadline with no
   replacement bound. `084fc87` uses a fresh two-second cleanup timeout. Reviewer
   rechecked and closed it.

The review specifically does not add runtime or production qualification.

## Spec

Final reviewer report: **No confirmed Spec findings**. The implementation matches
S01-A: independent generic core, four source operations, no-read asynchronous
admission, frozen scope and pages, live authorization, cancellation, owner-scoped
idempotency, generation fencing, atomic publication, strict JSON and exact XLSX.
Production identity, real products, restart recovery, roles/RLS, UI/PDF and
capacity remain explicitly deferred. Pending verification was not treated as
an implementation defect.

Summary: Standards 2 findings fixed, 0 open; Spec 0 confirmed findings, 0 open.

## Final evidence delta

Both reviewers separately rechecked `084fc87...65ec201`: new host/independence/
workbook verification scripts, acceptance statuses, raw logs and source hashes.
Standards PASS through `65ec201`, Spec no confirmed findings. Each independently
confirmed host16/33/one explicit PG skip, real PG17 children/no skips, six matching
workbook hashes, three worksheets/file, explicit string cells and zero formulas.
No application Go changes followed the reviewed `084fc87` fixes.
