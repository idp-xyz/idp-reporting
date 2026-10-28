# S03-F independent review of the multi-page document tables

Scope: the detail table of schema 1.3 documents (docs/S03F_SCOPE.md) on branch feat/s03f-multipage-documents,
range 668ee86..4c999e3 and the fixes after it. Two reviewers worked independently of the author and of each
other, on git-archive copies, on two axes: correctness and security; test effectiveness, checker quality and
documentation accuracy. Every finding below was reproduced by the reviewer before it was reported, and verified
closed by the author with a test, a checker control or a measurement.

## Round 1

### Correctness and security

Baseline on 4c999e3: vet clean, Go tests with Typst (18 PDFs written), race on the table tests, the checker with
441 controls caught and the golden matched, DOM 56/56 with Typst (46 plus 10 skips without), the evidence counts
reproduced. The reviewer's own checks: 200 randomised columns and 1000 rows of 80- and 120-digit values agree
with big.Rat sums; pagination on grid heights fills pages exactly, moves the last row when the total row would
not fit, renders exactly 100 pages and refuses 101, never splits a row, and carries the same sums forward; every
wrapped line of 20 hard texts at 8 and 12 pt in 8 and 40 mm columns is no wider than its cell and equals Go's
width; every Go text is found on the rendered pages at its position with drawn width no wider than Go's; values
reach Typst only through data.json; the preview route answers 403 for a table field the catalog or the snapshot
lacks and 409 for a snapshot with short rows; the console's and Go's validation agree; limits: 1000 × 12 cells
render in about 1.3 s into 0.7 MB, 67 pages of three-line cells in 0.6 s into 1.8 MB, 100 pages of 4 KiB cells
in 0.16 s.

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: the carried rows (承前/过次/合计 labels and sums) were never width-checked; with the console's default split of twelve columns at 12 pt an eleven-digit carry-forward sum started left of the region and lost its leading "1," off the page, and the labels and sums overran neighbouring columns | Fixed. Every amount, every running sum and the three labels (measured for the widest page number) are one line each and unsupported (422) when wider than their column's content width; a twelve-column fixture at 8 pt is in the evidence; the scope, API and README say so |
| 2 | Minor: a metric value wider than its column was cut into right-aligned fragments that read as separate numbers | Fixed with finding 1: amounts are never cut |
| 3 | Minor: column.equals panicked (negative Repeat count) when the declared scale exceeded the fraction digits seen; unreachable with a snapshot that passed validation, but a panic in a handler path | Fixed. The comparison uses the common scale of the sum, the column and the operand; unit test |
| 4 | Minor: an empty table whose header was taller than the region drew the header past the region and off the page instead of 422 | Fixed. A header and total row taller than the region is unsupported regardless of the row count; unit test |
| 5 | Minor: the layout ran before the render's deadline and the running sums were quadratic (4.7 s for 1000 rows of 120-digit values) | Fixed for the sums: one scaled integer per column, rescaled when the fraction grows. The layout still runs before the bounded context; its worst measured case is now well under a second, and Render checks the caller's context before starting |
| 6 | Nits: README named this review record before it existed; the "inside its cell" claim did not hold for the carried rows; the veraPDF results are in evidence/s03f/pdf_checks.log | The record exists; the claim holds after finding 1 |
| 7 | Nits: a sums list that was never read, a no-op `!math.IsNaN(0)`, a trim that could never trim | Removed |

### Test effectiveness, checker and documentation

Baseline on 4c999e3 reproduced: vet, Go 116/263 with the two PostgreSQL skips, the checker over 18 PDFs with 441
controls caught and the golden matched, the two table PDFs byte-identical to the evidence, DOM 56/56, the
evidence logs consistent. The reviewer ran 44 Go and Typst mutations, 32 model and route mutations, 30 console
mutations, 13 PDF corruptions and their own fixtures (one and twelve columns, metric first column, variables
mode with offsets, backgrounds, empty tables, blank rows).

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: the carried rows were never wrapped, cut or fit-checked (the same defect as finding 1 of the correctness review) | Fixed there: amounts, carried sums and labels are one line each and 422 when wider than their column; narrow-column fixture and unit tests |
| 2 | Major: the vertical placement of table text was verified nowhere: the baseline inset dropped or the line step changed survived every suite, and a cell moved 1 or 3 pt down was invisible to the checker | Fixed. The unit test asserts the header, first-row, brought-forward, second-page and second-line baselines from the row heights; the checker derives every row's top from the rows before it and requires each line's baseline within 0.1 pt of the position its row and line give it, the cells of a row on the same baselines and the tail row at the computed top; controls: a cell line moved 1 pt down, the total row moved 3 pt down |
| 3 | Minor: the fills, cell rules and the rule above the total row were not checked, and the only full-mode fixture carried a background, so the ink check never ran on a table | Fixed. The checker matches one stroked cell (0.5 pt) per column of every drawn row, a fill (luma 232) per column of the header rows, the 1 pt rule above the total row on the last page and nowhere else, and no other drawing in the region (none at all in the variable layer); a merged fill-and-stroke rectangle of a one-column table is accepted. Fixtures without background: the twelve-column, the empty and the blank-row tables; controls: rules thinned, header fill turned white |
| 4 | Minor: the checker crashed on an empty table (rows null; header and total both leading bold lines), miscounted rows with nothing to draw, and its control generators assumed the fixture's shape | Fixed. Null rows are empty; the total or carry-forward row is recognised by its label before the header split; blank rows are counted and sized by their paragraphs, including those at the end of a page from the room before the tail; the generators pick existing rows, a summed column and skip controls that need boxes, a second page or a metric. Fixtures: an empty table and a one-column table with blank, space-only and newline-only rows |
| 5 | Minor: the console's table rules were untested one by one (17 of 22 deletions survived) and the width split was only tested with two columns | Fixed. A DOM test selects every column (widths on the 0.1 mm grid adding up to the table's width), rejects 24 single-rule violations, keeps a background through the canonical form and the fallback to 1.2 on removal, and checks the variable-layer rule |
| 6 | Minor: Go rules without a test of their own: x under 10, y under 25, a field over 128 bytes, a column width off the grid, a binding to another dataset, short snapshot rows, an invalid background on 1.3, and the catalog authorization of table fields | Fixed with unit tests and an HTTP test (a table field the snapshot has but the catalog no longer lists is 403 and nothing is rendered) |
| 7 | Minor: the page cap was tested far from its boundary and the too-tall-row check was shadowed by it | Fixed. 101 rows are exactly 100 pages and 102 one too many; the too-tall message is asserted |
| 8 | Minor: nine messages of the table check had no negative control of their own | Fixed. Controls for text of another size, bold text among the rows, a header in the variable layer, a brought-forward row on page 1, the carry-forward row removed, a line pushed over the region's edge, besides those of findings 2 and 3; "in no column" and "split over pages" remain exercised only through other controls |
| 9 | Minor: documentation: the review record did not exist yet; "optional table" where 1.3 requires it; "multi-page preview" through the console where the demo data yield one page | Fixed in the scope and README; the record exists |
| 10 | Nits: a no-op `!math.IsNaN(0)`, quadratic re-summing, an order-blind `has()` helper | Fixed with the correctness round (first two); the pagination test now checks the rows' order within a page |

Solid per the reviewer: every sum, scale, sign and off-by-one mutation falls to the unit tests, and the checker
independently recomputes the carried sums and catches digit reordering, a wrong page's value, duplicated or
removed pages and reordered rows; header, boxes, bold labels, page numbers and the total row's placement are
each caught by a unit test and the checker; the evidence reproduces byte for byte.

## Round 2 (the fixes, 4c999e3..b072dea)

### Correctness and security

Baseline on b072dea reproduced: vet, Go tests with Typst (21 PDFs), race on the table packages, the checker with
565 controls caught and the golden matched, the five table PDFs byte-identical to the evidence (the round-1
fixtures unchanged by the fixes), DOM 57/57; limits with the new checks: 1000 × 12 metric columns lay out in
12 ms and render in 0.37 s, 1000 rows of 41-digit amounts lay out in 6 ms. Every round-1 finding of both axes is
closed, with the remarks below. No major finding.

| # | Finding | Disposition |
|---|---|---|
| 1 | Minor: "widths ≥ 8 mm" was not honest: an 8 mm decimal column is refused at every size (the bold zero sum needs 8.1 mm at 8 pt), a bold 999,999.99 sum needs 17 to 25 mm, and the first column must hold the bold label for page 100 (about 28 to 41 mm) | Fixed in wording and in the console: the scope, the API, the panel text and the README state the measured minimums; the console sizes the first column for the label at the chosen type size before splitting the rest and refuses a narrower first column; 8 mm stays the geometric floor for short text and small integers |
| 2 | Minor: the console accepted tables Go refuses only at preview, and reported the 422 as a dataset problem | Fixed. A document-preview 422 reads as a layout problem of the template (what to widen or shrink); DOM test with a 12 mm amount column |
| 3 | Minor: Go refused a document over a running sum that is never printed (amounts changing sign) | Fixed. Only the sums the carried rows print are required to fit; unit test with a short total after a wide intermediate sum |
| 4 | Minor: checker false positives: blank rows at a page end (the row after them taken as a continuation), an empty paragraph among drawn lines, a wrapped header in the variable layer (greedy character packing differed from the renderer's wrapping), carried sums at the column's longest fraction over the rows so far instead of over all rows | Fixed. The next page starts a new row after blank rows; each drawn baseline is matched to the line its offset gives it; the expectation records the header's line count for the variable layer; sums use the column's longest fraction over all rows |
| 5 | Minor: the stroke colour of the rules was not checked, so white rules passed | Fixed. Cell rules must be luma 150 and the total rule luma 60; controls turn each white |
| 6 | Nit: "measured for page 100" was documented only in the scope | Fixed in the API, README and console text |

Verified correct by the reviewer: the linear sums with rescaling, negatives and the common-scale equality; the
page-cap simplification; labels measured bold and amounts regular against the content width with no off-by-one;
no new path to Typst and no value in an error; one and twelve columns at 8 and 12 pt, a row with text only in
the last column, a metric first column with offsets, a wrapped header in full mode and a background all pass the
checker.

### Test effectiveness, checker and documentation

Baseline reproduced on b072dea and 34b3cae: vet, Go 116/263, the five table PDFs byte-identical to the evidence,
the checker 565 then 570 controls caught, DOM 57/57, veraPDF on the table PDFs. The reviewer re-ran every
surviving round-1 mutant (all seven Go and Typst survivors now fall, the page cap and messages are pinned), ran
new mutants against the fixes, 60 PDF corruptions and their own fixtures (hard strings as cells with offsets,
mixed fractions, wrapped headers in both modes, metric-only columns).

| # | Finding | Disposition |
|---|---|---|
| 1 | Major: the amount and carried-sum refusals masked each other in the tests (each case was refused by the other check), so disabling either survived | Fixed. Twenty amounts of 999.99 whose printed sum does not fit (message "a carried sum"), and two amounts of 123,456,789.00 whose total 0.00 fits (message "an amount") |
| 2 | Major: the rescaling of the exact sum when a longer fraction arrives had no test and no fixture; removing it survived | Fixed. "1", "2.5", "0.125", "-1" sum to 2.625 in the unit test, and the document_table_mixed fixture carries a column whose fractions grow row by row, which the checker recomputes |
| 3 | Major: a table line was one text object, so Typst compressed consecutive Chinese punctuation and joined dashes inside it; the checker failed a correct render of the project's own hard strings in cells, and no fixture tried them; trailing ideographic spaces were drawn but stripped by the checker | Fixed. A line is drawn as one text object per piece, split between two consecutive characters Typst would shape together (CJK punctuation, dashes, ellipses, the middle dot), each piece placed at its plain-advance position; trailing white space of every kind is dropped as for box values; the document_table_hard fixture renders the hard strings as cells at 12 pt with print offsets and passes every per-character check |
| 4 | Major: the HTTP test of the table's catalog authorization also revoked the box's field, so the table loop's removal survived; no HTTP case reached BindTable's own denial | Fixed. Only the table's column is revoked (the box keeps its field); a column the catalog lists but the frozen snapshot lacks passes authorization and is refused by the binding, with no render |
| 5 | Minor: the console rule test did not isolate the rules (11 of 29 deletions survived) and the first-column minimum and sizing were untested | Fixed. Every case breaks exactly one rule of a base the designer accepts; the first column is asserted at the label minimum when the even split is narrower; 31.4 mm passes at 9 pt and fails at 10 pt |
| 6 | Minor: three Go model cases were rejected by neighbouring rules | Fixed: x under 10, y under 25 and an off-grid column width each break only their rule |
| 7 | Minor: extra lines inside the table on the last page passed (a strike-through, a second rule) | Fixed. The last page must hold exactly the one total rule; other pages none |
| 8 | Minor: a line drawn in the variable layer passed (PyMuPDF's intersects ignores a zero-height rectangle) | Fixed with a coordinate test |
| 9 | Minor: a cell's text moved from the first to the second line of its own row passed | Fixed. A cell's first piece must sit on its row's first line unless the value starts with a line break |
| 10 | Minor: the evidence had not been refreshed for 34b3cae; the header line count had no fixture | Fixed. Evidence regenerated; document_table_wrapheader_variables has a three-line header in the variable layer |
| 11 | Minor: the refusal boundaries were far from the tests (label for page 100, header-and-total height) | Fixed. A first column that holds the page-1 label but not the page-100 one is refused; a header and total row one point taller than the region is refused and one that just fits is accepted |
| 12 | Minor: the page-1 brought-forward control assumed a one-line header; "a fill on a body cell" and "in no column" had no control | Fixed for the first two: the control is inserted below a header of any height, and a fill on a body cell is a control; "in no column" cannot be produced, as the columns cover the region |
| 13 | Nit: the console's first-column minimums exceeded the measured need by up to 0.85 mm | Fixed. 28.2, 31.4, 34.7, 38.0 and 41.2 mm, the measured widths rounded up to 0.1 mm, in the console, the panel text and the scope |
| 14 | Nits: near-equivalent mutants of the fit comparison and the row loop; bold versus regular face indistinguishable with the fake font | Accepted |

Solid per the reviewer: the vertical check catches 0.2 pt on one cell, a line or all text and a wrong line step
independently of the unit test; the drawings check catches a missing, thinner, recoloured or misplaced rule, a
body fill, a darker header fill and a total rule on the wrong page; every sum, scale and sign mutation falls;
the evidence reproduces byte for byte.
