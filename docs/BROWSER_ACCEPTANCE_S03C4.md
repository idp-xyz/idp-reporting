# S03-C4 real-browser acceptance (post-review)

2026-10-27 Asia/Shanghai. Reviewed commit 802cc4a76f127089f7e9751f754a907ae7da3d66.
The two-axis review of that commit (docs/CODE_REVIEW_S03C4.md) performed no browser operations; this closes that gap
within the limits below. Only console/assets/index.html and console/assets/styles.css changed after review;
app.js, dashboard.js, dom.test.mjs and all Go sources are byte-identical (hashes of the two changed files are in
evidence/s03c4/post_review.json, the hashes of all other files in provenance.json).
The work is committed on reporting-s03a after 802cc4a (`git log 802cc4a..HEAD`). Its own independent review is
docs/CODE_REVIEW_S03C4_1.md.

## Method

Chrome 155.0.8059.85 (Windows, --headless=new, empty temporary profile) driven over CDP by
scripts/browser_visual_s03c4.mjs against reporting-demo (contract simulator, -memory, loopback, random token).
Seven viewports: desktop 1280x900, tablet 768x1024, mobile 390x844 at 2x, narrow 320x640 at 2x, mid 900x800,
laptop 1024x768, wide 1920x1080. Mobile and narrow are device emulation, not devices. Screenshots are taken at the
first three; the other four run the layout checks and the 500-digit case only. The real login, preview, dashboard and
Excel-export flow runs against the real server. Signed, duplicate, hostile, 500-digit, max-int, all-zero, 51-row,
invalid and empty cases are injected by wrapping the page's own fetch for /queries, as dom.test.mjs does: they test
rendering, not server data.

173 checks. Per viewport: 6 pages without horizontal overflow; report-layout designer controls not clipped, off the
card edge, with visible button borders and the 12px row gutter; real rows plotted with exact labels; dashboard controls
not clipped, toolbar groups on separate rows, visible button borders, 12px gutter, server panel off the card edge,
exact total on one line; per data case, correct rendering and no page overflow (all 7 cases at the three screenshot
viewports, the 500-digit case at the other four). Once: a real Chrome download parsed as a zip with every on-screen
cell value found exactly among the workbook strings; no console errors, failed requests, log-domain errors, exceptions
or dialogs. Button-border checks cover every static button in the dashboard and the report designer and fail on an
empty scope. report.json records the browser version and user agent and the sha256 of the served index.html,
styles.css, app.js and dashboard.js; these equal the files at the commit under test.

## Defects found on 802cc4a, fixes and the checks that assert each

| # | Symptom | Cause | Fix | Asserted by |
|---|---------|-------|-----|-------------|
| 1 | A 500-digit value makes the page 4905px wide at 1280 (4354 at 768, 4155 at 390) | the chart's scale line is a bare `<p>` without wrapping | `.board-chart>p{overflow-wrap:anywhere}` | "has no horizontal page overflow" |
| 2 | Dashboard page 642px wide at 390 and 841px at 768; report-layout and dashboard server rows clipped | `.designer-row` is flex without wrap and `.button` is nowrap | `flex-wrap:wrap` | "no horizontal overflow", "controls are not clipped" |
| 3 | Toolbar controls run together; buttons have no visible boundary (polish) | one unstructured panel; `.button` has a transparent border | grouped `.board-tool` rows (ids and element types unchanged); static buttons use the existing `.secondary` variant | "toolbar groups controls on separate rows", "buttons have visible boundaries" |
| 4 | Text, textarea and buttons touch the card edge in the server and layout panels; rows use a different gutter (polish) | direct children had no padding | `.form-panel` | "keeps text and controls off the card edge", "rows share the 12px gutter" |
| 5 | /favicon.ico returns 404 on every load, a console error (hygiene) | no icon link | `<link rel="icon" href="data:,">` (CSP img-src allows data:) | "no console errors, failed requests" |
| 6 | The exact server total breaks mid-number: on a phone, leaving `01` on its own line, and at 900px to 1024px where the grid has two narrow columns | fixed 30px `.board-value` | sized from the card width, `clamp(16px,8cqw,30px)` under `@supports (width:1cqw)`; browsers without container units keep `clamp(16px,6vw,24px)` at 640px and below | "exact server total stays on one line" |

Two intermediate #6 fixes failed and were found by checks added afterwards: a fixed 22px broke at 320px, then a
viewport-based clamp broke at 900px and 1024px. Both states exist only in the commit messages; no artifact is stored
for them.

## Negative control

The same script, with the same flags, against a build of 802cc4a: 90 of 173 checks pass and 83 fail. By defect: page
overflow and data-case overflow 25, clipped controls 6, toolbar rows 5, button borders 14, card edge 14, row gutter 14,
favicon console error 1, total on one line 4. Raw report: evidence/s03c4/negative_control_802cc4a.json. To reproduce,
`git worktree add ../s03c4-pre 802cc4a`, build and start reporting-demo from it on another port, then run the script
with --base pointing at that port and the same download flags (--skip-download instead gives 171 checks). No pre-fix
screenshots are stored.

## Result

173 of 173 on commit 37da3a0 plus its evidence. Evidence: evidence/s03c4/browser_visual (screenshots, report.json,
download.json and the downloaded download.xlsx). Regression logs, each ending in its exit code, with the commit and a
clean-tree check in environment.log: evidence/s03c4/post_review. go vet, go test and go test -race exit 0 with 10
packages ok, DOM 37 of 37, node --check on app.js, dashboard.js and the script. The DOM suite needs REPORTING_GO to
point at a Go 1.26 binary because its fixture forces GOTOOLCHAIN=local, and `npm ci` first.

Script behavior verified by hand: a non-loopback or https or malformed --base, a missing --skip-download/--download
pair, a flag without a value and an unknown flag exit 2; an unreachable CDP port exits 1 and still writes report.json.
Only the PNG names it generates and its own report and download files are removed from --out. Log entries replayed
from an earlier page lifetime are discarded before the run, which an earlier version of the script got wrong.

## Not covered

- A real mobile device, touch input, the on-screen keyboard; Firefox, Safari, Edge; older Chrome releases.
- The viewport-rule fallback for browsers without container query units: it is not exercised by any check.
- Contrast or accessibility audits, screen readers and keyboard navigation.
- Download: one frozen-snapshot Excel export in Chrome. Only the cell values visible on the first page (9 cells) were
  compared, as a set, with the workbook strings; positions, order and the whole workbook against the whole dataset were
  not compared.
- Extreme values come from injected responses, not from a source.
- PG rerun, crash recovery and production qualification remain undone; real integration stays last.

## Run

```bash
chrome --headless=new --remote-debugging-port=9333 --user-data-dir=<empty temp dir> about:blank &
node scripts/browser_visual_s03c4.mjs --out evidence/s03c4/browser_visual --token-file <file with REPORTING_DEMO_TOKEN> \
  --base http://127.0.0.1:7810 --cdp 9333 --download-chrome <dir> --download-local <same dir as this script sees it>
```

Use --skip-download instead of the two download flags to skip that check; it is then listed under `skipped` in
report.json. The script exits non-zero on any failed check, accepts only a loopback --base, never deletes anything in
the download directory and is not part of npm test. As run here: Chrome on Windows and Node and Go under WSL2 with
mirrored networking, so both sides share 127.0.0.1.
