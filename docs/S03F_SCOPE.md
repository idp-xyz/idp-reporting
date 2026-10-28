# S03-F multi-page documents

2026-10-28. Extends the S03-E document templates (docs/S03E_SCOPE.md, ADR 0002) with a detail table that flows
over as many A4 pages as it needs, with the header repeated, exact carried totals and the same preprinted form on
every page. Same engine, same sandbox, same placement contract; behaviour is S03-E's unless stated here.

F1 · Table element. Schema 1.3 adds a `table` to the document (required on 1.3, absent on the other schemas): `{x, y, width, height, font_size,
columns: [{field, width}]}`, a region inside the safe region (10..200 × 25..270 mm, 0.1 mm grid, at least 30 mm
high) that overlaps no text box, 1..12 columns of the bound dataset's fields whose widths (≥ 8 mm) add up to the
table's width, 8..12 pt. The table lists every row of the frozen snapshot in snapshot order (the snapshot holds
at most 1,000 rows, and a durable snapshot is complete by the repository contract). Cells are formatted as the
table export formats them: metric numbers with thousands separators and aligned right, other values as given and
aligned left; line breaks and tabs in a value print as the export prints them. Text boxes keep S03-E's exact
single-line values.

F2 · Pagination owned by Go. Go breaks every cell into lines with the export's conservative line-break model
(runs that may certainly break, runs wider than the cell cut to fit), sizes each row from its tallest cell (never
split across pages), and lays the rows into the region page by page: each page starts with the header row and,
from page 2 on, a 承前 row; each page but the last ends with a 过次 row; the last page ends with a 合计 row. Every
page is the same form: background (full mode), every text box with the same values, the banner. Typst draws what
Go computed: every text line placed at its own position after an empty inline box (ADR 0002), cell rules and the
header fill as rectangles, and asserts that it draws no line wider than Go measured. A row taller than the region
can hold, or a document of more than 100 pages, is 422 UNSUPPORTED_COMBINATION. In variables mode the pages
print the cell values and the carried rows only: no rules, no header text, no background.

F3 · Exact carried totals. For every numeric column (decimal or integer) but the first, Go adds the printed rows
exactly (integers scaled to the column's longest fraction, no floating point): 过次 on page p and 承前 on page p+1
show the sum of the rows up to page p, 合计 the sum of all rows. The first column holds the labels, with the page
numbers: "承前（第 p 页）", "过次（转第 p+1 页）", "合计". Amounts, carried sums and the labels (measured for the
widest page number) are never cut or wrapped: one wider than its column's content width is 422
UNSUPPORTED_COMBINATION, as is a header and total row taller than the region. When the snapshot's whole-scope
total of a metric column differs from the sum of its rows, the preview is refused as INTEGRITY_FAILURE: a durable
snapshot is complete, so the two must agree.

F4 · Designer and checks. The console designer adds a table panel (region, font size, columns from the current
authorized result with widths that must add up), validates it like the boxes, and previews through the same
routes; saved versions, publication, offsets and backgrounds work unchanged. scripts/check_pdf_export.py checks a
multi-page document: the page count, every page A4, the banner and the text boxes on every page, the header on
every page, every row's cells complete and in order on exactly one page with no row split, each text line inside
its cell, at its plain-advance position (--font, bold lines with the Bold face) and at the baseline its row and
line give it (rows start where the one before ends, lines 1.5 font sizes apart), the carried rows' amounts
recomputed exactly from the rows, the total equal to the sum of all rows, in full mode one stroked cell per
column of every row, the header fills and the rule above the total row (and no drawing at all in the variable
layer), no ink outside the boxes, the table region and the banner (without a background), and the layout golden
(page count and first row of each page) recorded for Typst upgrades, with a negative control for each check.

Not in this slice: grouping rows per document (one document per customer from one snapshot), subtotals per
group, column headers other than the catalog labels, images other than the background, barcodes, signatures,
official issue records, a canvas editor, physical printer calibration, production qualification.
