# S03-D1 independent review of PDF export

Subject: `git log cf9960e..HEAD` on feat/s03d1-pdf-export — export formats in the core and the HTTP API, the export/pdf
package (Typst 0.15.1, ADR 0001), the demo flags, the console's PDF export, scripts/fetch_pdf_tools.py and
scripts/check_pdf_export.py. Two reviewer agents ran read-only and independently of the author, on two axes: correctness
and security; and test effectiveness, verification soundness and code quality. Both ran the Go, DOM and checker suites
with the pinned Typst and fonts, built adversarial inputs in scratch copies, and the second ran about 60 mutations. Their
findings were verified by the author before any fix; every code fix below has a regression test or negative control.

## Correctness and security

| # | Finding | Disposition |
|---|---------|-------------|
| 1 | Major: fonts whose family Typst names differently ("Source Han Sans CN" becomes "Source Han Sans") or whose extension has mixed case made Typst lay out every string with no font, warn, exit 0 and produce an empty PDF | Fixed three ways: New runs `typst fonts --variants` in the sandbox and requires exactly the faces Go loaded (family, file, style, weight); any Typst output on standard error fails the run; extensions follow Typst's rule. Tests: renamed fonts are refused at startup; an unknown family fails a render; a mixed-case extension is refused |
| 2 | Major: runs of CJK punctuation (60 "！", 40 "。") were taken as breakable, overflowed their cell and ran off the page | Fixed: a conservative line-break model — a line certainly breaks only between two ideographs and after spaces before a letter or digit (not after opening or before closing punctuation); everything else is a run, measured and cut when wider than its cell. Also counts the quarter em Typst puts between Chinese and Latin, which made short headers wrap. Tests: unit cases of the model, a punctuation document checked column by column |
| 3 | Minor: a hard core limit of 0 made PDF export fail to start (prlimit EPERM) | Fixed: limits never exceed this process's hard limits. With a hard core limit of 0 New refuses only when kernel.core_pattern pipes dumps to a collector (which ignores limit 0), and says so. Test runs New in a child process with the limit lowered |
| 4 | Minor: a soft hyphen prints as "-" where Typst breaks the line | Fixed: refused as unsupported, like an uncovered character |
| 5 | Hardening: no filesystem or Unix-socket isolation; root maps to root; binary and fonts were checked by path (time of check to time of use) | Fixed in part: Typst runs from the file descriptor New opened and hashed, so a file put at the path later never runs and a modified or unlinked one is refused; New refuses to run as root; the font and work directories and all their parents must be changeable only by root or this user (sticky directories allowed). Not done: Landlock or a mount namespace. ADR condition 3 is met as written; an exploited Typst could still read what the service user can read |
| 6 | Nit: a killed process leaves render directories with data.json | Fixed: New removes this user's render directories older than ten minutes |
| 7 | Nit: running out of the memory limit was EXECUTION_FAILED | Fixed: Rust's allocation-failure abort maps to BUDGET_EXCEEDED |
| 8 | Nit: capabilities listed `"formats": null` without writers | Fixed: an empty list |
| 9 | Nit: rolling back to cf9960e with PDF records stored | Recorded in README; an older binary would serve a stored PDF as a workbook |
| 10 | Nits: the console did not check the format of the returned job; the stderr buffer kept the head but was called tail | Fixed |

## Tests, verification and quality

| # | Finding | Disposition |
|---|---------|-------------|
| 1 | Major: the checker joined each column across rows, so amounts drawn one row lower passed | Fixed: the checker splits each column back into cells and requires the cells of a row to start on one page and baseline. Replaying the reviewer's corruption on a real PDF now fails four rows |
| 2 | Major: expectations came from the code under test, and whitespace was ignored, so a value cut to its first line passed | Fixed: the Go tests write the snapshot's raw values; the checker derives grouping, header suffixes and totals itself and requires a line break where a value has one, a space or break where it has spaces and no space where it has none |
| 3 | Major: no layout guarantee ran in CI; the documents never pushed the measuring limits | The CI job (below) runs the documents, the checker with negative controls and a golden layout file, and veraPDF. Added documents with runs of every length around their cell width with brackets glued on, a bold total that is the widest number, and an empty table without dimensions. The checker now holds glyphs other than CJK punctuation inside the cell padding |
| 4 | Major (likely): Ubuntu 24.04 runners forbid unprivileged user namespaces | The CI job allows them on the throwaway runner; the startup error now names this cause |
| 5 | Minor: title, meta line, data class, invisible or overlapping text, and text after the table were unchecked | Fixed: all checked, with negative controls that corrupt the PDF itself (text outside the margins, in the footer area, in white, with an unembedded font, drawn over other text, a replacement character, no XMP) |
| 6 | Minor: page count and page breaks not pinned (ADR condition 6) | Fixed: export/pdf/testdata/golden.json records the page count and first row of each page; the checker compares it |
| 7 | Minor: an empty table without dimensions failed the checker | Fixed (order of the text after the table), document added |
| 8 | Minor: the environment case was vacuous; host and built-in fonts untested | Fixed: TYPST_FEATURES=html reaches Typst only through the environment; built-in and host font families must fail. The package-path flag is not tested separately: without it Typst would still find no packages offline |
| 9 | Minor: fetch_pdf_tools.py rewrote files on every run (breaking a running server), printed unquoted paths and changed directory modes | Fixed |
| 10 | Minor: Linux-only tests not tagged; untested snapshot-job format, unknown stored format over HTTP, PDF download from the task list | Fixed |
| 11 | Nits (names, comments, a map used as a ternary, declaration order in app.js, the WorkbookWriter name) | Fixed or documented |

## Re-check of the surviving mutations after the fixes

Bold measured with the Regular face, text cut to its first line, newlines printed as spaces, no heading, white table
text, an inherited environment, host or built-in fonts allowed, long runs cut only at 1.2 times the cell width,
warnings ignored, the font listing not compared, punctuation treated as breakable, soft hyphens allowed and no gap
between Chinese and Latin are each now caught by a Go test or the checker. Measuring with no slack (1.0 instead of
1.03) still passes everything: the measurement is already conservative, and the slack is a margin, not a fix.
RLIMIT_CORE 0 was not run as a mutation because it writes a real crash dump on this host; the core-limit test covers
the decision instead.

## Evidence

evidence/s03d1/checks.json and the logs beside it: Go 81 top-level tests normal and race (only the two PostgreSQL
entries skip), 41 DOM tests with the real Typst, 9 documents checked, 162 negative controls caught, the golden layout
matched, veraPDF PDF/A-2b passed for all 10 PDFs (including the console download) and failed the negative control.
