# S23-A · Layouts 1.1, exports that carry a layout, typed workbook cells

2026-11-11. ADR 0024 (docs/ADR_0024_TABLE_REPORT_FORMATTING.md) decided that a layout is the table report's presentation and that the
exports carry it. This slice is the server side: the layout schema 1.1, a domain-neutral presentation in the core that an export job
freezes, one presentation step shared by the PDF and the workbook, typed workbook cells beside exact columns, and `POST /exports` with
`layout_id`. The console (the layout designer's new fields, the table view, the export buttons, baseline task T11) is S23-B.

## What

A1 · **Layouts schema 1.1** (`layouts/store.go`): additive, all optional: `title` (1..120 characters) and `footnote` (1..400); on a column
`label` (1..64, replaces the dataset's label and its currency and unit), `align` gains `center`, `format` `{grouping, negative, zero}`
(defaults `true`, `minus`, `digits`; a key absent takes its default) on a `decimal` or `integer` column only; `subtotals: {by}` (one level,
a dimension shown by the layout); `marks`: 1..8 rules `{column, when, value?, mark}` (`lt le gt ge eq ne` with an exact decimal `value` on a
numeric column, `eq ne blank` on any other, `mark` one of `good warn bad note`). A 1.0 document is exactly what 1.0 defined: one that carries
a 1.1 key is refused. `layouts.Validate` is the shape (every bound of ADR 0024 decision 1, `ErrInvalid`; 64 KiB `ErrBudget`), `layouts.Check`
the rules that need the dataset (a column the dataset lacks `ErrDenied` as before; a format on a non-numeric column, a subtotal column that
is not a dimension, a relation that does not fit the column's type `ErrInvalid`), and `layouts.Presentation` the conversion to the core's
form with the defaults filled in. The HTTP layer's `authorize` applies `Check` where it checked the columns against the catalog, so the
layout routes answer 400 for the 1.1 rules and 403 for an unknown column, unchanged. Tests: `TestSchema11IsValidAndSchema10IsUnchanged`,
`TestSchema11RefusalsNameNoValue`, `TestCheckHoldsALayoutToItsDataset`, `TestPresentationOfALayoutFillsTheDefaults`,
`TestASchema11LayoutIsStoredAsSaved` (layouts); `TestTheLayoutRoutesAcceptSchema11AndRefuseWhatTheDatasetForbids` (httpapi).

A2 · **The core's presentation** (`reporting/presentation.go`): `Presentation` (title, footnote, columns `{ID, Label, Align, Format}`,
`SubtotalBy`, marks, and the identity `LayoutID`, `LayoutVersion`) with `Validate` (shape only, no knowledge of layouts), `Fits` (does it
apply to a result's columns and dimensions; `ErrUnsupported`, the whole presentation refused, never applied in part) and `Clone`. `Job`
carries `Presentation` (frozen at admission, as an issue job freezes its template; kept by retention so that the view still names the
layout), `Artifact` carries `LayoutID` and `LayoutVersion`, `JobView` gains `layout: {id, version}`. `ExportRequestHashWithLayout` binds the
snapshot, the format and the layout version, and is `ExportRequestHash` without a layout, so that every export job admitted before keeps
its hash; `ValidateAdmission`/`ValidateReduced` hold the hash and the presentation (only an export job may carry one, and then it must name
its layout), `ValidatePublication` holds the artifact to the job's layout version. `Service.SubmitExportWith(ctx, p, id, ExportOptions{Format,
Presentation}, key)` is the options form; `SubmitExport` and `SubmitExportFormat` call it and every caller compiles unchanged. It refuses a
presentation that is not whole (400), that names no layout (400), for a format whose writer is not a `PresentedWriter` (422), or that does
not fit the snapshot (422). The worker calls `WritePresented` when the job has a presentation, `Write` otherwise, and the artifact records
the layout. No store change: jobs and artifacts are JSON payloads in both stores, so no migration (as S03-K's group). Tests:
`TestPresentationValidateHoldsTheShape`, `TestPresentationFitsTheSnapshotOrIsRefusedWhole`, `TestPresentationCloneSharesNothing`,
`TestExportRequestHashBindsTheLayoutVersion`, `TestAPresentedExportFreezesItsPresentationAndRecordsItsLayout` (replay, conflict on another
version and on no layout, the worker's call, the artifact, the listing, the plain export unchanged),
`TestAPresentedExportIsRefusedWhenItCannotBeRendered`, `TestAdmissionAndPublicationHoldThePresentation` (reporting).

A3 · **The shared presentation step** `export/present` (standard library and `reporting` only): `Build(snapshot, presentation)` picks and
orders the columns, labels them, formats the numeric values (`Format`: grouping, parentheses, dash; it changes no digit), partitions the
rows into subtotal groups by the exact strings of the subtotal column (groups ordered by their first row, the snapshot's order kept inside,
as ADR 0004 decision 2) with the exact sum of each shown metric, evaluates the marks (exact decimal comparison on numeric columns, exact
string comparison and blankness otherwise; the first rule that holds wins; a subtotal row is never marked) and carries the source's totals
as they came, the 合计 label in the first shown column that is not a summed metric (a list when every shown column is one). `Whole(snapshot)`
is the presentation of the whole snapshot as the exports always showed it. `Sum` is the exact decimal accumulator moved out of
`export/pdf/table.go` (the document tables' carries and totals use it from there now): one implementation, no second one. `Palette` is the
closed set of mark colours (light tints, black text) shared by the PDF and the workbook, for the console to copy. `RoundTrips` and
`ExcelNumberFormat` are the workbook's rules. Tests (table-driven): `TestFormatChangesNoDigit`, `TestExcelNumberFormatFollowsScaleAndFormat`,
`TestRoundTripsThroughFloat64AtTheScale`, `TestCompareAndHoldsAreExact`, `TestWholeIsTheSnapshotAsItAlwaysWas`,
`TestBuildPresentsColumnsGroupsAndMarks`, `TestBuildListsTotalsWhenEveryShownColumnIsAMetric`, `TestBuildRefusesWhatDoesNotFit`,
`TestSumIsExactAndNeverRounds`, `TestPaletteIsClosedAndLight` (present); `TestColumnSumsExactly`, `TestSumDecimalsIsTheTablesRule` (pdf: the
document tables' carries, totals and group sums still print what they printed).

A4 · **Workbook** (`export/xlsx`, ADR 0024 decision 3, every workbook with or without a layout): in `Data` a `decimal` or `integer` column
is numeric cells with an Excel number format from the column's scale and the format (`#,##0.00;-#,##0.00`, `#,##0.00;(#,##0.00)`, a third
section `"—"` for a dash zero; a numeric dimension without a format `0;-0`), each followed by a text column `<label>（精确）` with the exact
frozen string; a numeric cell is written only when the exact string survives a float64 at the column's scale (`RoundTrips`), else the cell
is its formatted text and `Metadata` counts it in `inexact_cells`. `Totals` gains `value` beside `exact_value` under the same rule.
Subtotal rows are bold values, never formulas; marks are cell fills from the palette; `Metadata` names the layout (`layout_id`,
`layout_version`, `layout_title`, `layout_footnote`, `subtotal_by`; empty without one) and replaces the precision note. `WritePresented`
renders through a presentation, `Write` through `present.Whole`, byte for byte the same file as the whole presentation. Tests:
`TestEveryWorkbookWritesTypedCellsBesideExactColumns`, `TestAPresentedWorkbookCarriesLayoutSubtotalsAndMarks`,
`TestAPresentationThatDoesNotFitIsRefusedWhole` (xlsx; each reads the workbook back with excelize and asserts cell types, raw values,
number formats, bold and fills); `TestExportPreservesPreciseTextAndNoFormulas` (reporting, unchanged: the exact digits survive, no formula).

A5 · **PDF** (`export/pdf`): `WritePresented` and `buildTable(snapshot, presentation)`; `build` is `buildTable` without one. With a
presentation the table shows the layout's columns in its order with its labels (a layout label replaces the dataset's label and its
currency and unit, as a 2.0 document table column's does), alignments (`center` joins `left` and `right`), formats, the subtotal rows (小计,
bold, a rule above like the total row), the source's total row unchanged (合计, or the totals list when every shown column is a metric),
the title as the heading (the dataset stays in the meta line and the footer), the footnote under the table, the marks as cell fills, and
`布局 <id>@<version>` in the meta line. More than `MaxColumns` shown columns is refused as before; a label, title or footnote the fonts do not
cover is refused like a value. The template (`template.typ`) reads `subtotal_rows`, `fills` and `footnote` when present and lays out
exactly what it did when they are absent: `export/pdf/testdata/plain_sha256.json` holds the SHA-256 of the ten plain documents of
`TestTypstLaysOutDocuments` as the pinned Typst and fonts render them at the fixed creation time, recorded before this change and compared
after it (`TestTypstLaysOutDocuments/plain_golden`; `REPORTING_PDF_WRITE_GOLDEN=1` rewrites it after a reviewed Typst or font upgrade).
Tests without Typst: `TestBuildPresentsTheSnapshotThroughALayout`, `TestAPlainBuildHasNoneOfThePresentationKeys` (the plain data has none
of the new keys and `build` equals `buildTable` without a presentation), `TestBuildRefusesAPresentationThatDoesNotFitOrIsTooWide`. With
Typst: `TestTypstExportsThroughTheService` exports the demo snapshot through a presentation via the service (the job and the artifact name
the layout, the bytes differ from the plain export, two renders are the same bytes) and writes `presented.pdf` into
`REPORTING_PDF_EVIDENCE`; `TestTypstLaysOutDocuments/presented_rows1000` renders the thousand rows with seven subtotal groups, a centred
column, a mark and a footnote and writes `presented_rows1000.pdf`. CI's veraPDF validates both as PDF/A-2b (the fills are opaque colours,
the fonts the embedded ones); the layout checker (`scripts/check_pdf_export.py`) has no presented-table mode, so neither has an expectation
file (the issue previews are written the same way).

A6 · **HTTP** (`transport/httpapi/api.go`): `POST /exports` accepts `layout_id`. The handler resolves the latest saved version of the layout
in the caller's own space, then the tenant's shared space (`~team`); not found, or another identity's, is 404 as the layout routes answer;
a malformed id 400; a server without a layout store 422. It reads the snapshot through `AuthorizedSnapshot` (as `POST /issues` does), requires
the layout's binding to be the snapshot's integration and dataset (else 422 UNSUPPORTED_COMBINATION), holds the layout to the snapshot's
frozen columns and dimensions with `layouts.Check` (400 for a saved layout that no longer fits its dataset's rules), converts it with
`layouts.Presentation` and calls `SubmitExportWith`. The catalog is not asked again: the snapshot's authorization is the caller's current
right on that dataset. Job views, `GET /jobs/{id}` and `GET /jobs` show `layout`. `docs/openapi.json` (held to the routes by
`TestOpenAPIMatchesTheBoundRoutes`): `LayoutDocument` 1.0 | 1.1 with the new keys, `LayoutFormat`, `LayoutMark`, `LayoutRef`, `layout_id` on
the export body, `layout` on `JobView`. `docs/API.md`: the export row, the workbook paragraph, a section "Layouts and presented exports".
Tests: `TestAnExportMayCarryALayout` (the view, the poll and the listing name the layout; the workbook differs from the plain one and carries
the title, the labels and one subtotal row per bucket; replay; 409 once the layout has a newer version; a new key exports version 2; the
plain key with a layout and the layout's key without one are conflicts), `TestAnExportRefusesALayoutItCannotUse` (another dataset and
another integration 422, another identity's 404, unknown 404, malformed 400, a team layout 202, a writer that cannot present 422, a layout
with a format on a string column 400, no layout store 422) (httpapi).

A7 · **Docs**: this file; docs/HISTORY.md (a note at the top); README (the ADR range to 0024, `export/present`); docs/API.md and
docs/openapi.json (A6). docs/CONSOLE.md is S23-B's. ADR 0017 is not edited.

A8 · Not done: the console (S23-B); a presented-table mode of the PDF checker; the layouts' shared-space routes (`/team/layouts`, which
the templates have and the layouts never had: an export reads the team space, the routes do not write it); pivot tables, several levels of
subtotals, a colour of the person's choosing, formulas in the workbook (ADR 0024 decision 6).

## Interpretations of ADR 0024

- A numeric **dimension** without a format (a year, a count) is shown as it is, as the exports always printed a dimension; a metric without a
  format is grouped, as always; a format on either follows the format. The workbook writes both as numeric cells.
- `subtotals.by` must be a column of the layout (and, of the dataset, a dimension): the 小计 label stands in the first shown column that is
  not a summed metric and the reader must see what grouped the rows. The total row's 合计 label stands in the same column.
- A layout `label` replaces the dataset's label **and** its currency and unit, as a 2.0 document table column's `label` does (ADR 0004
  decision 5); the workbook's exact column is `<that label>（精确）`.
- Several marks on one cell: the first rule in the layout's order that holds colours it. Subtotal and total rows are never marked: they are
  the platform's arithmetic, never compared with anything.
- The workbook's `Totals` sheet lists every metric of the query, as before, whichever columns the layout shows: it is the snapshot's record.
  The PDF's total row shows the totals of the shown metrics.
- An export with a team layout uses the layout's latest saved version, as the ADR says for every layout; the team routes of the templates
  let every viewer read every version, so no new right is granted.
- The PDF's heading becomes the layout's title; the dataset stays in the meta line and the footer. A 1.0 layout, or a 1.1 one without a
  title, keeps the dataset as the heading.
- Byte identity without a layout is the PDF's (the golden file). The workbook changes for every export by ADR 0024 decision 3 (typed cells,
  exact columns, `value`, the Metadata rows); `Write` and `WritePresented(Whole)` are byte for byte the same file.

## Evidence

- The plain PDFs are the same bytes as before the change: ten documents (mixed, kerned, hostile, punctuation, edges, emptynodims, wide,
  nodims, empty, rows1000) against `testdata/plain_sha256.json` (`TestTypstLaysOutDocuments/plain_golden`, with the pinned Typst 0.15.1 and
  Noto Sans SC 2.004).
- `go test ./export/pdf/... -count=1` with `REPORTING_TYPST`, `REPORTING_TYPST_SHA256`, `REPORTING_PDF_FONTS` and `REPORTING_PDF_REQUIRE=1`
  passes; the presented documents are written into `REPORTING_PDF_EVIDENCE` for veraPDF.
- The strict Go check (`scripts/go_test_strict.py` with CI's `EXPECTED_SKIPS`), `gofmt -l .` and `go vet ./...` are clean; no new top-level
  Typst test, so the three skip lists of `ci.yml` are unchanged.
- The console suite (format, lint, `go test ./console/...`, the jsdom tests against the real demo server) passes unchanged: the console sends
  no `layout_id` yet and the job view's `layout` is absent for its exports.

## Known

- The PDF checker verifies the plain exports and the documents; a presented PDF is validated by veraPDF only until the checker learns
  labels, formats, subtotal rows, centred columns and fills.
- The Metadata sheet's `field` table lists the snapshot's columns, as before, not the layout's: the record of the dataset the file was made
  from.
- Excel shows a numeric cell with more than 15 significant digits rounded on screen (the stored value is the exact float64); the （精确）
  column beside it is the reconciliation value, as ADR 0024 decision 3 says.
