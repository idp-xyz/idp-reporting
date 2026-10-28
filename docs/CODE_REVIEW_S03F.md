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

Pending.
