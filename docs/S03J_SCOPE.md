# S03-J template schema 2.0: groups, bands and layout

2026-10-28. Implements decisions 1–6, 8 and 9 of ADR 0004 for previews and checks. Issuing a 2.0 template is
S03-K (until then `POST /issues` refuses a 2.0 template with 422 UNSUPPORTED_COMBINATION at admission); the
canvas for bands is S03-L (until then a 2.0 template is edited as JSON, and the canvas says it shows 1.x only).
Templates 1.0–1.3 keep their bytes: the golden digests in `export/pdf/testdata/golden.json` do not change.

J1 · Schema. `doctemplates` accepts `schema_version: "2.0"` with `group` (optional; `fields`: 1–3 distinct
dimension fields of the binding) and `bands` (`page_header`, `document_header`, `detail`, `document_footer`,
`page_footer`; each optional except `detail`, each with `height` on the 0.1 mm grid except `detail`, and
`elements`). Element `type`: `text` (`source` literal | field | group_sum | page; `text` for literal and page,
`field` otherwise; `wrap` boolean; box, `font_size` 8–28, `align`, `bold`), `line` (two points, `stroke`
0.25–2 pt), `rect` (box, `stroke` 0–2 pt, `fill` none or one of four greys), `table` (detail only: 1.3's
table keys plus per-column `align` and optional literal `label`). Element ids `e1`–`e64`, unique in the
template. Positions relative to the band; every element inside its band; no two text boxes overlap; band
heights add up (with the detail band's minimum, the table header row and one body row) within the safe
region. Reserved types (`barcode`, `qrcode`, `image`), unknown keys and unknown fields are refused. The JSON
is canonical as for 1.x; at most 64 KiB. The console's `validate`/`canonical` accept the same 2.0 form
(JSON editing, previews, save, publish).

J2 · Groups. `doctemplates.Groups(d, snapshot)` partitions the frozen rows by the group fields' exact strings,
ordered by first row; without `group`, one group of every row. `BindGroup` gives the band values and the
table data for one group: `field` from the group's rows, refused with `INCONSISTENT_GROUP` when a non-group
field differs within the group; `group_sum` the exact decimal sum of a metric field over the group (the
table-total rule; scale kept; a value that is not a decimal is refused); `page` texts filled at layout.

J3 · Layout. `export/pdf` lays out a 2.0 document: page header on every page, document header on the first,
the detail table between them and the page footer with the 1.3 carries and final total, the document footer
once after the last row (whole; moved to a new page when it does not fit, refused when it does not fit an
empty page), page footer on every page. Text with `wrap` breaks by the table cells' line-break rules within
the box width; more lines than the box height holds is refused; without `wrap` one line as 1.x. `{page}` and
`{pages}` are filled after pagination; the widest page text is checked for every page. Lines and rectangles
are drawn by the existing Typst page program (fills, cells, rules) from Go's coordinates. The banner and
stamp strip stays on every page. More than 100 pages is refused. `Render` and `CheckIssue` take a 2.0
document and one group; the result is deterministic.

J4 · Refusal details. Layout and binding refusals of both families carry a typed reason with the place
(`reason`, `band`, `element`, `row`, `column`; never a value), and the problem answer of previews and checks
adds `detail` with it. Reasons: DOES_NOT_FIT, NOT_COVERED, INCONSISTENT_GROUP, TOO_MANY_PAGES, BAND_OVERFLOW.
For 1.x the place is the element id or the table column. Codes and statuses stay as they are.

J5 · Routes and console. `/document-previews` with a 2.0 template renders the group that contains
`row_index`; `/document-checks` checks the groups of the given or sampled rows (one result per row, the
same group's result for each of its rows), and a refused row carries the same `detail` as a preview. The
console shows `detail` in its error messages and check results and, for 1.x on the canvas, outlines the element
or column a refusal names.

J6 · Tests and evidence. Go tests for the schema (every rule and limit, canonical form), grouping (order,
empty keys, inconsistent fields, exact sums), layout (bands on every page, footer moved and refused, wrap,
page texts, lines and rectangles, the strip), refusal details for both families; 1.x golden digests
unchanged; 2.0 golden PDFs (an invoice-like sample with three groups, a multi-page group with carries, a
wrapped address, a document footer moved to a new page) checked by `scripts/check_pdf_export.py`, extended
for 2.0 expectations, with negative controls, and by veraPDF in the CI pdf job. Typst-gated tests go into
the CI skip lists. DOM tests: 2.0 JSON open, preview, save and publish; refusal details shown.

Not in this slice: issuing 2.0 (S03-K), the canvas for bands and the 1.x→2.0 conversion (S03-L),
barcodes, QR codes and images (ADR 0005), sub-groups and group headers inside a document.
