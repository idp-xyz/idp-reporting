# S03-G independent review of the official issue records

Scope: official issue records (docs/S03G_SCOPE.md) on branch feat/s03g-issuance, implementation ea9e3d3 and the
fixes after it. Two reviewers worked independently of the author and of each other, on git-archive copies, on
two axes: correctness and security; test effectiveness, checker quality and documentation accuracy. Every
finding below was reproduced by the reviewer before it was reported, and verified closed by the author with a
test, a checker control or a measurement.

## Round 1

### Correctness and security

Baseline on ea9e3d3: vet clean; Go with Typst and PostgreSQL green except the two issue tests that failed at
random (finding 2); the checker with every control caught on the issued fixtures; DOM 60/60 with Typst; the
fixture after a fresh open at v3 with both kind constraints and the counter table. The reviewer's own checks:
superseded and withdrawn versions fail queued jobs with TEMPLATE_NOT_PUBLISHED and nothing is published; a
replayed key with another version is an idempotency conflict; crafted jobs (number 0, altered row, other
template bytes) never publish; a canonical template with `<`, `&`, quotes, backslashes, CJK and floats survives
JSONB byte for byte; eight concurrent admissions and eight concurrent replays of one key give distinct
consecutive numbers and one job; a failed admission inside the transaction leaves no gap; the widest possible
stamp (number MaxInt64) measures 161 mm and renders; Typst markup in values renders literally; the issue takes
an export slot only; every content route goes through Service.Download.

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: `GET /issues` applied the limit over all of the identity's issue jobs and only then dropped the integrations the principal no longer holds, so a page could come back short or empty while records existed (1 finance issue and 3 shipment issues: `limit=3` gave nothing to a finance-only principal) | Fixed. The repository filters by the principal's integrations before the limit (PostgreSQL `integration=ANY`, memory `slices.Contains`); a principal without integrations gets an empty page; tests in reporting and on PostgreSQL |
| 2 | Major: the memory repository claimed jobs in Go map order while PostgreSQL claims by creation time; the issue tests assumed the order and failed 7/30 and 6/30 runs | Fixed. `Claim` walks the admission order (commit dbdd4dd); the two tests pass 30/30 and 200/200 |
| 3 | Minor: after 打开已发布模板 and 保存模板版本 the console still submitted the new, unpublished version (the server refused it with 409, so nothing was issued, but the console's own rule was not enforced) | Fixed. Saving disarms the button; DOM test |
| 4 | Minor: `Download` served an issued PDF without comparing it with its record; an artifact row rewritten consistently (content, digest, row digest) downloaded as issued | Fixed. The download requires the record to name the artifact, its digest and its snapshot (ErrIntegrity otherwise), and the console's records panel compares the download with the record's SHA-256; PostgreSQL and DOM tests |
| 5 | Minor: the capabilities `routes` list was static and `POST /issues` was mounted only with a renderer (404 instead of 422 on a deployment with templates but no PDF) | Fixed. The route is mounted with the templates and answers 422 without an issuer; `routes` lists only the handler's own routes and `template_routes` the template handler's |
| 6 | Minor: one unreadable record fails the whole listing | Kept as designed (fail closed); documented in docs/API.md |
| 7 | Note: a bound value equal to the banner text rendered on an issued document while the checker refuses any such text | Fixed: `Writer.Issue` refuses values and cells containing the banner text; the checker looks for the exact phrase |
| 8 | Note: a succeeded issue job's `GET /jobs/{id}` is 409 once the snapshot expired (as for exports), while the record stays readable | Documented in docs/API.md |

### Tests, checker and documentation

Baseline on ea9e3d3: 27 PDFs ok, 817 controls caught (125 on the issued fixtures), 0 missed; DOM 60/60; the
three issued fixtures missing from the golden (expected); 41 Go mutations (31 caught), 6 renderer mutations (4
caught by Go, 2 only by the checker), 18 console mutations (13 caught), 2 checker mutations (1 caught), 18
crafted PDFs (all caught by the checker).

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: the two main issue tests were flaky (26/200 and 24/200) because the memory store claimed in random order | Fixed with correctness finding 2 |
| 2 | Major: the Typst test never looked at the stamp: an issue that kept the PREVIEW banner or drew the stamp in the banner's red passed every Go test (only the checker noticed) | Fixed. `Writer.issuePage` is asserted: the stamp in the strip in the text colour, the title, the page count, no banner glyph on any page; the preview keeps its banner |
| 3 | Major: no negative control exercised the comparison of the drawn stamp with the record; the record-level controls were caught by the record's self-consistency and the digest alone, and a checker that never compared the stamp text still reported every control caught | Fixed. PDF-level controls on issued documents run with the record's digest recomputed, so the digest never masks another check; new controls: the stamp redrawn with the next number, the stamp covered on the last page, the preview in place of the issue (now caught by the stamp line) |
| 4 | Major: "consecutive per tenant and integration" was never tested (every test used finance only); a sequence keyed by tenant alone survived | Fixed. A shipment issue takes number 1 while finance is at 3, in the memory and the PostgreSQL tests |
| 5 | Major (docs): the README had no S03-G section | Fixed. The section leads the README |
| 6 | Minor: the issuer's background handling (no store → unsupported; variables → no background) was claimed but unreached | Fixed. TestIssuerResolvesTheBackgroundAsThePreviewDoes |
| 7 | Minor: the listing's integration re-check and the record cross-check were untested | Fixed. Repository listing under another integration, a principal without integrations, a PostgreSQL record tampered consistently with its stamp but naming another row |
| 8 | Minor: refusal cases shared one idempotency key | Fixed. One key per case |
| 9 | Minor: "any change takes the issue button away" was tested through the unsaved-change path only | Fixed. Save-as-new-version case |
| 10 | Minor: an issue job view without its summary was reported as an unconfirmed submission, indistinguishable from a TypeError | Fixed. The console reports an unverifiable response as such; the test asserts no page error |
| 11 | Minor: the TEMPLATE_NOT_PUBLISHED exemption from clearing the result was untested | Fixed. The frozen result stays after the refusal |
| 12 | Minor: the pdf-format half of the gating and the 100-record bound were untested | Fixed. Two cases |
| 13–16, 20 | Minor/Note (docs): artifact download names, the route comment ("unprintable value"), 404 versus 409 for a missing version, the Typst test comment, the two download names | Fixed in docs/API.md, docs/CONSOLE.md, docs/S03G_SCOPE.md and the comments |
| 17 | Note: redundant guards in the contract and the service survived mutations | Kept as defence in depth |
| 18 | Note: vector ink outside the boxes of a background document without a table is a pre-existing blind spot of the checker | Noted; not in this slice |
| 19 | Note: a dead `time` import kept alive in a test | Removed |
| 21 | Note: the PostgreSQL test did not cover per-integration numbering or a stamp-consistent record tamper | Fixed with 4 and 7 |

## Round 2

### Correctness and security

Baseline on df80f55: build, vet, the whole Go suite with Typst and PostgreSQL, DOM 60/60 and the checker with
every control caught, all green. Every round-1 disposition was reproduced as closed (the old failure now
refused or caught), with two residues below. The reviewer's own checks: unknown or duplicate integrations in
the listing are harmless and a repository leaking a shipment job into a finance page is an integrity failure;
lease recovery claims the oldest expired job first, as PostgreSQL does; four keys with three concurrent
replays each give numbers 1..4 and a counter of exactly 4; number order and listing order agree; a cancel
during the render publishes nothing; the record and the listing stay readable after the snapshot expired
while the download is refused.

| # | Finding | Disposition |
|---|---|---|
| 1 | Minor: a succeeded issue job whose record row is missing was 404 on the record, the listing, the job view and the download, where the API promised an integrity failure | Fixed. A missing record of a succeeded job, or one an artifact names, is ErrIntegrity on every path; PostgreSQL test with the record deleted |
| 2 | Minor: the download cross-check ran only when the artifact claimed to be issued; an artifact rewritten consistently with its issue mark removed downloaded as a plain export and the job view read clean | Fixed. The view of a succeeded issue job requires the artifact to claim the record and the record to name the artifact and its digest; PostgreSQL test with the mark stripped. (The content route alone cannot know a record names an artifact that disowns it: the job view and the records panel are where the two directions meet; documented) |
| 3 | Note: after the snapshot expired the job view is 403 with the contract simulators (the adapter denies the expired read before the download check), not the 409 the API said | Documented correctly |
| 4 | Note: with the templates not mounted `POST /issues` was the mux's bare 405 | Fixed. The handler mounts a stub answering 422 in the problem shape; the template handler overrides it |
| 5 | Note: the banner-text refusal scanned literals that variables mode does not print, and accepts variants of the phrase, consistent with the checker's exact phrase but narrower than the scope's wording | Fixed the first (literals unprinted in variables mode are not scanned); the exact phrase is the rule, stated in the scope |
| 6 | Note: a job view naming another template, version or row was remembered as a task and the idempotency key was spent before the console checked it | Fixed. The console verifies the view before it keeps the task or clears the key; DOM test asserts one key, no task |
| 7 | Note: the listing does one record read per succeeded job, at most 100 per page | Accepted at this scale |

### Tests, checker and documentation

Baseline on df80f55: vet clean; 271 Go tests with Typst, the two issue tests 100/100; the skips without Typst
and databases exactly the CI lists; the checker with 823 controls caught and the golden matched; DOM 60/60;
26 Go, 6 checker and 12 console mutants, 11 own corruptions. Every round-1 closure was confirmed by mutation
(the memory and HTTP suites; the PostgreSQL ones by reading, as the gate runs them in CI and fails instead of
skipping). Three guards are reachable only through the PostgreSQL gate: the download cross-check, its digest
term and the record's row term.

| # | Finding | Disposition |
|---|---|---|
| 1 | Minor: the "stamp redrawn with the next number" control was caught for a reason unrelated to the number (the inserted glyphs never match the line), so it did not show that the drawn number is compared with the record's | Fixed. Two record-level controls isolate the comparison: the number and the stamp changed together, the id and the stamp changed together (self-consistent records that disagree with the drawn stamp alone); the PDF-level control stays as an overdraw control |
| 2 | Minor (docs): the scope and the README said "no PREVIEW text anywhere" while the renderer and the checker enforce the exact banner phrase | Fixed. Both say the banner phrase; the Typst test searches the laid-out texts for the phrase |
| 3 | Minor: "a superseded version is 409" was untested; a handler that replaced the requested version by the published one survived | Fixed. HTTP case: version 2 saved after version 1 published is 409 TEMPLATE_NOT_PUBLISHED |
| 4 | Minor: the console's reset of the published-open state on any template change, and the row term of its job-view check, were untested | Fixed. DOM cases: opening the draft version disarms the button without a POST; a job view naming another row is an invalid response, not followed |
| 5 | Observation: three service guards are exercised by the PostgreSQL gate only | Stated here; the gate fails instead of skipping in CI |
| 6 | Nit: a principal whose token names an integration without a connection was untested; the integrity branch after the repository filter is unreachable | Tested (such an integration lists nothing, no error); the branch stays as defence in depth |
| 7 | Nit: the capabilities test did not pin that `routes` omits POST /issues | Fixed |
| 8 | Nit: "the preview in place of the issue" could expect its most specific reason | Fixed. It expects the preview text |
| 9 | Nit (docs): the README named the review record and the evidence before they existed; the numbers were placeholders | Filled from the final evidence run below |

Final evidence after both rounds: see README.md and evidence/s03g/checks.json.
