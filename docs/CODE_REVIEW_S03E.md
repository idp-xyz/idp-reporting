# S03-E independent review of the document templates on Typst

Scope: the port of the S03-D1..D4 document template delivery (temp/, commit 79aa396) onto the Typst table
export as S03-E1..E4 (docs/S03E_SCOPE.md, ADR 0002), on branch feat/s03e-document-templates. Two reviewers worked
independently of the author and of each other, on git-archive copies, on two axes: correctness and security;
test effectiveness, checker quality and documentation accuracy. Every finding below was reproduced by the
reviewer before it was reported, and verified closed by the author with a test, a checker control or a
measurement.

## Round 1 (ported code, 9671188..75de1bd)

The correctness reviewer reported before the author's fixes; the tests reviewer's run was cut off by an API
limit before it reported and was repeated in round 2.

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: Go's fit check was not an upper bound on what Typst draws. Over every character the fonts cover, five are drawn wider than their advances: the Hangul tone marks U+302E/U+302F (Typst draws a dotted circle before each, which is not text, so a run of them ran off the page while the checker read the page as correct) and the em, two-em and three-em dashes in Chinese text. The same held for the table export | Fixed. Tables: the tone marks are refused and the dashes measured at one, two and three ems (S03-D1 1938c3e). Documents: Go now places every grapheme cluster itself at its plain-advance position with the English language setting (document.go layOut, document.typ), and the template asserts that Typst measures no cluster wider than Go did; the assertion is 422 UNSUPPORTED_COMBINATION (widerThanMeasuredError). The checker, given --font, derives every character's position from the font and fails on ink outside the boxes and the banner, with negative controls for both |
| 2 | Major: the delivery's limit of one render at a time was lost in the port; previews run inside HTTP requests, so any user could start Typst processes without limit (24 parallel calls ran 16 processes) | Fixed. Writer.acquire/release with Config.Concurrency (default 2), shared by exports and previews; a render that waits longer than the render timeout is 413 BUDGET_EXCEEDED. Unit test of the slots; six concurrent renders in the Typst test |
| 3 | Major: the claim that previews keep pdfme's placement held only for the 30 simple boxes measured. Typst adds a quarter em between Chinese and Latin, compresses Chinese punctuation at line ends and in runs, keeps trailing spaces (pdfme trimmed them) and draws the em dash at 1 em under lang zh; the cited evidence file did not exist | Fixed by the placement model of finding 1, which uses no Typst line shaping at all, and by trimming trailing white space. Measured again on 24 boxes of hard values with the archived pdfme renderer: every character within 0.009 pt (evidence/s03e/cross_engine.json, scripts in evidence/s03e/cross_engine). The one difference, pdfme substituting a two-em dash glyph for two em dashes, is recorded in ADR 0002 |
| 4 | Minor: a snapshot value with a tab or newline was 413 BUDGET_EXCEEDED, and DEL passed Bind and Validate but failed every preview with 400 | Fixed. Bind refuses control characters, DEL and values over 256 characters with 422; Validate refuses DEL in literals like other controls. Tests in doctemplates |
| 5 | Minor: the checker could not see inserted glyphs or overflow, failed correct output with combining marks, "……——" and trailing spaces, and the fixtures held no adjacent Chinese/Latin text, punctuation, dashes or padded values | Fixed. Per-character positions and the ink check (finding 1); the marks and compressed punctuation exemptions of 1938c3e; trailing white space trimmed on both sides; a fourth fixture, document_hard, with the reviewer's values and more |
| 6 | Minor: typical Chinese values with full-width punctuation were refused, Go over-estimating their width by 9-23% (the quarter-em gap next to punctuation plus the 3% slack) | Fixed. Documents use the plain measurer: advances only, no gap, no slack; the 2 pt margin remains |
| 7 | Minor: the startup probe rendered no background and only left alignment | Fixed. The probe renders every alignment, Chinese punctuation, a dash and a generated A4 background |
| 8 | Nits: a dead `generation` counter and a sentence about the canvas in the console; preview buttons shown without Typst (404) | Fixed. Both removed; the console offers the two preview buttons only when the capabilities list pdf (DOM test) |
| 9 | Nit: Typst re-encodes backgrounds much larger than the PNG (33 KB to 1.35 MB), and uploads were not checked against the artifact limit | Fixed. Assets.Fits renders a probe document on the background at upload and refuses it with the renderer's error; the demo wires it when Typst is configured. Measured: a 1240×1754 RGB gradient PNG of 67 KB becomes a 1.86 MB PDF, a 322 KB RGBA one 1.88 MB, far below 8 MiB, so the check is a guard rather than a routine refusal |

### Verified correct by the reviewer in round 1

Port fidelity against the delivery (only main's format changes and the moved test PNG differ); values travel only
in data.json and are never Typst markup; characters that would break lines, change direction or be invisible are
refused as uncovered; background checks run in the right order, drawn only in full mode, fitted and centred,
PDF/A-2b with RGBA8, RGBA16, Gray8, Gray16 and paletted PNGs, byte-identical output; the banner drawn last on its
white backing; errors carry no values; temporary files 0600 in 0700 directories and removed; the demo mounts
previews only with Typst and templates and backgrounds without it; the console keeps the strict CSP, checks the
media type, digest and signature of previews, and guards stale downloads.

## Round 2 (the fixes, 1938c3e..c03cbc9)

### Correctness and security

Baseline on c03cbc9: vet clean, Go tests normal and race (12 packages), DOM 53/53, the checker with --font and
--negative over 15 PDFs, all passing. The reviewer measured the placement model exhaustively: every one of the
30,887 runes the plain measurer accepts, measured in Typst exactly as document.typ does, is drawn no wider than
Go's advance (20 closing CJK punctuation marks measure narrower at a line end, which moves no glyph); all 13
covered combining marks after nine kinds of base never widen; all covered runes rendered through Writer.Render in
24 documents gave 61,183 glyphs with none missing, moved (0.05 pt) or added, and a negative control of 50 shifted
positions was caught; pdfme parity on further space, mark and stacked-mark cases stayed within 0.009 pt; the
template's assertion fails a render end to end and maps to 422 even with 6,144 failing glyphs; the fit boundary
with the real font holds for W, 。, j, f, the em dash and （ at 8 to 28 pt; a worst-case document renders in
about 120 ms. No major finding.

| # | Finding | Disposition |
|---|---|---|
| 1 | Minor: previews and exports shared the same slots and an export that waited the render timeout got a terminal BUDGET_EXCEEDED, so sustained previews could deny PDF export to everyone | Fixed. Previews hold at most all but one slot (at least one), so an export waits for at most one preview render; the export's wait is bounded by the same timeout as before. Unit test of the slot arithmetic; API.md and ADR 0002 say so |
| 2 | Minor: the background upload check ran inside the store's mutex with a background context and before the quota, so uploads of all owners queued behind renders, a disconnecting client could not cancel, and a ninth background still cost a render | Fixed. Put checks the binding, the PNG and the quota first, calls Fits outside the lock with the request's context, then locks, re-checks the quota and saves. Tests: the context reaches the check; a ninth background is refused without a render |
| 3 | Nit: a preview could wait 15 s for a slot and then render 15 s, past the demo server's 25 s write timeout, while API.md spoke of the render timeout alone | Fixed. One deadline, the render timeout, now covers wait and render for previews and exports (Writer.bounded); exceeding it is BUDGET_EXCEEDED unless the caller's context ended first |
| 4 | Nit: a schema 1.2 template on a server without -background-directory was 503 AUTHORITY_UNAVAILABLE, which API.md defines as a source outage (pre-existing in the delivery) | Fixed. 422 UNSUPPORTED_COMBINATION, with an HTTP test and a sentence in API.md and the scope |
| 5 | Nit: ADR 0002 said Typst draws a dotted circle before a stray combining mark; only the Hangul tone marks do that with the pinned fonts, the other 13 covered marks draw as given | Fixed in the ADR |

Verified correct by the reviewer: values reach Typst only as string arguments of text(); the preview sandbox is
the export sandbox unchanged; temporary files 0600 in 0700 directories; backgrounds reach Render only from the
store or the probe and are re-checked in layOut; the semaphore is paired by defer on every path; the preview
route's order of checks and its status codes (403, 422, 413, 409, 500, 400); the stored-version preview renders
only the stored configuration; the rebase changed nothing in the table export (layout.go and template.typ
identical, fonts.go only gains the plain measurer); the console gating and the strict CSP; the documentation's
claims apart from findings 4 and 5.

### Test effectiveness, checker and documentation

Pending.
