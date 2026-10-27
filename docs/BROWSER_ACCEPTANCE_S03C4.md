# S03-C4 real-browser acceptance (post-review)

2026-10-27 Asia/Shanghai. Reviewed commit 802cc4a76f127089f7e9751f754a907ae7da3d66 (SOURCE_HISTORY.bundle).
The two-axis review (docs/CODE_REVIEW_S03C4.md) performed no browser operations; this closes that gap
within the limits below. Only console/assets/index.html and console/assets/styles.css changed after review;
app.js, dashboard.js, dom.test.mjs and all Go sources are byte-identical (hashes in evidence/s03c4/post_review.json).
They are committed on reporting-s03a after 802cc4a as two commits: the layout fix, then the acceptance script,
evidence and this record. The review of those commits is docs/CODE_REVIEW_S03C4_1.md.

## Method

Chrome 155.0.8059.85 (Windows, --headless=new, empty temporary profile) driven over CDP by
scripts/browser_visual_s03c4.mjs against reporting-demo (contract simulator, -memory, loopback, random token).
Viewports: desktop 1280x900, tablet 768x1024, mobile 390x844 at 2x. Mobile is device emulation, not a device.
The real login, preview, dashboard and Excel-export flow runs against the real server. Signed, duplicate,
hostile, 500-digit, max-int, all-zero, 51-row, invalid and empty cases are injected by wrapping the page's own
fetch for /queries, as dom.test.mjs does: they test rendering, not server data.

72 checks: 6 pages x 3 viewports without horizontal overflow; report layout designer and dashboard controls not
clipped; real rows plot 3 bars with exact labels; 7 data cases x 3 viewports render as specified and without
overflow; a real Chrome download of a verified .xlsx; no console errors, failed requests, exceptions or dialogs.

## Defects found on 802cc4a and fixed

| # | Symptom | Cause | Fix |
|---|---------|-------|-----|
| 1 | A 500-digit value makes the page 4905px wide at 1280 (4354 at 768, 4155 at 390) | the chart's scale line is a bare `<p>` without wrapping | `.board-chart>p{overflow-wrap:anywhere}` |
| 2 | Dashboard page 642px wide at 390 and 841px at 768; report-layout and dashboard server rows clipped | `.designer-row` is flex without wrap and `.button` is nowrap | `flex-wrap:wrap` |
| 3 | Dashboard toolbar controls run together; buttons have no visible boundary | one unstructured panel; `.button` has a transparent border | grouped `.board-tool` rows (ids and element types unchanged); `.designer-row` and `.form-panel` buttons use the secondary border |
| 4 | Text, textarea and buttons touch the card edge in the server and layout panels | direct children had no padding | `.form-panel` |
| 5 | /favicon.ico returns 404 on every load, a console error | no icon link | `<link rel="icon" href="data:,">` (CSP img-src allows data:) |
| 6 | The exact server total breaks mid-number at 390px, leaving `01` on its own line | 30px `.board-value` | 22px at 640px and below |

## Negative control

The same script against a build of 802cc4a: 49 of 71 checks pass and 22 fail (the download check was skipped
there). The failures are the overflow, clipped-control and favicon defects above, so the checks do detect them.

## Result

72 of 72 on the working tree. Evidence: evidence/s03c4/browser_visual (39 files: screenshots, report.json,
download.json). Regression on the same tree: go vet, go test and go test -race all report ok for every package,
DOM 37 of 37, node --check on app.js, dashboard.js and the new script. The DOM suite needs REPORTING_GO to point at
a Go 1.26 binary because its fixture forces GOTOOLCHAIN=local.

## Not covered

- A real mobile device, touch input, the on-screen keyboard; Firefox, Safari, Edge; older Chrome releases.
- Contrast or accessibility audits, screen readers and keyboard navigation.
- Download: one frozen-snapshot Excel export in Chrome (PK header, valid archive with xl/workbook.xml); its cell
  values were not compared with the on-screen rows in the browser.
- Extreme values come from injected responses, not from a source.
- PG rerun, crash recovery and production qualification remain undone; real integration stays last.

## Run

```bash
chrome --headless=new --remote-debugging-port=9333 --user-data-dir=<empty temp dir> about:blank &
node scripts/browser_visual_s03c4.mjs --out evidence/s03c4/browser_visual --token-file <file with REPORTING_DEMO_TOKEN> \
  --base http://127.0.0.1:7810 --cdp 9333 --download-chrome <dir> --download-local <same dir as this script sees it>
```

The script exits non-zero on any failed check and is not part of npm test.
