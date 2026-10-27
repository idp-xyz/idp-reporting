# S03-C4.1 independent review of the browser acceptance fixes

Subject: the commits after the reviewed baseline 802cc4a on reporting-s03a (`git log 802cc4a..HEAD`): the console layout
fixes in index.html and styles.css, scripts/browser_visual_s03c4.mjs, and the acceptance evidence and records
(docs/BROWSER_ACCEPTANCE_S03C4.md, evidence/s03c4/post_review.json).
Two reviewer agents ran read-only and independently of the author, on two axes: standards and engineering quality, and
spec, scope and claim accuracy. Each was told to verify from the repository rather than trust any summary. The spec
reviewer ran go build, go vet and go test ./... (also with -race in round 2); the standards reviewer ran go build,
go vet, go test ./console/... and node --check. Neither could run the DOM suite (no jsdom, `npm ci` was forbidden), so
the author ran it and stored the log. Both axes ran twice: round 1 on the first three fix commits, round 2 on the
commits that answered round 1. No finding was a blocker or major in either round.

## Round 1

| Axis | Finding | Disposition |
|------|---------|-------------|
| Std 1 | Stale output survives a failed run; a missing download flag skips silently | Fixed: own-file cleanup, explicit `--skip-download`, `skipped` in report.json |
| Std 2 | Download check only tests the PK magic; the xl/workbook.xml claim has no artifact | Fixed: the zip is parsed, entries and on-screen cell values asserted, workbook kept as evidence |
| Std 3 | Log domain and failed requests not observed | Fixed |
| Std 4 | No loopback guard; the script deletes files in the download directory | Fixed: loopback only, nothing deleted from the download directory |
| Std 5 | No timeout or closed-connection handling | Fixed |
| Std 6 | Only 3 of 6 defects have assertions; no 320px or wide viewport | Fixed, then extended in round 2 |
| Std 7 | `.designer-row .button` override restyles JS-built buttons; colours copied from `.secondary` | Fixed: static buttons use the `secondary` variant |
| Std 8 | Inconsistent gutters, redundant margins | Fixed |
| Std 9 | Stale "two commits", forward references, README wording, report lacks browser version | Fixed |
| Std 10 | Hand-rolled argument parser, double evaluation | Fixed with `util.parseArgs`; the `{a, b}` spacing nit is left as is |
| Spec 1 | The commit message overclaims detection; only 3 of 6 defects asserted | Fixed by the added assertions. Commit 01bab4a keeps its message (history not rewritten); its numbers are superseded by the acceptance record |
| Spec 2 | "valid archive with xl/workbook.xml" had no artifact | Fixed (see Std 2) |
| Spec 3 | Commit count and hash location misstated | Fixed |
| Spec 4 | Log domain and loadingFailed not covered | Fixed |
| Spec 5 | Evidence lacks browser version, served hashes, post-fix logs | Fixed: report.json fields and regression logs |
| Spec 6 | docs/CONSOLE.md and docs/VALIDATION.md still say the browser pass is blocked | Update notes added; the earlier paragraphs stay as history. docs/S03C1_SCOPE.md is a scope record and is left unchanged |
| Spec 7 | Polish and favicon labelled as defects; column buttons restyled | Fixed in the acceptance record |

## Round 2

| Axis | Finding | Disposition |
|------|---------|-------------|
| Std A, Spec A | The cleanup regex deletes every *.png in --out | Fixed: anchored on the generated names. Checked against unrelated names |
| Std B | The total's font fix applies only at 640px and below but the grid has two columns from 801px; PLAUSIBLE at about 801 to 1000px | Confirmed by adding 900px and 1024px viewports, which failed on the old rule. Fixed with a container query unit and a viewport fallback |
| Std C | Only 8 of 25 buttons checked for borders; the 12px gutter unasserted | Fixed: every static button in two scopes, empty scope fails; gutter asserted |
| Std D | Workbook comparison is set membership | Accepted and disclosed in the record and in post_review.json |
| Std E, Spec B | Malformed `--base` exits 1, not 2 | Fixed and verified |
| Std F, Spec 5 | Regression logs are empty or lack exit codes and a clean-tree binding | Fixed: each log ends with its exit code; environment.log records the commit and a clean-tree check |
| Spec C | The negative control recipe omits the download flags | Fixed in the record, with the check count |
| Spec D | "22px still broke at 320px" has no artifact | The record now says the intermediate states exist only in commit messages |
| Spec 3 | "hashes in post_review.json" was imprecise | Fixed |
| Spec 5 | "Windows" appears only in prose | Fixed: the user agent is recorded in report.json |

The spec reviewer verified from the repository that the stated numbers, hashes and counts matched the artifacts, that the
Go sources, app.js, dashboard.js, dom.test.mjs, provenance.json and checks.json are unchanged from 802cc4a, and that all
110 element ids resolve with the same tags. The standards reviewer verified the HTML structure and ids independently and
found no path for the bearer token into any evidence file.

## State after round 2

The round 2 fixes are the commit that sizes the total by card width and tightens the checks, and the final evidence
refresh. They were verified by the author: the 173-check browser run passes, the 802cc4a negative control fails 83 of
173, the served file hashes equal the committed files, and go vet, go test, go test -race, DOM 37 of 37 and node
--check pass on a clean tree. They were not reviewed by a third independent pass.

Accepted residuals: the set-membership workbook comparison, the `{a, b}` object spacing, and the untouched
S03C1_SCOPE.md. The limits in docs/BROWSER_ACCEPTANCE_S03C4.md ("Not covered") still apply; real devices, other
browsers, accessibility, PG rerun, crash recovery and production qualification remain unverified.
