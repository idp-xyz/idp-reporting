# S20-C · The exports say the scope: the query's filters and sort in the workbook and the PDF

2026-11-11. ADR 0023 (docs/ADR_0023_PUBLISHED_FILTERS_AND_SORTS.md) lets a query narrow and order a dataset by what the catalogue publishes
(S20-A, docs/S20A_SCOPE.md). A snapshot made with filters is a narrowed scope and its totals are that scope's: a person who reads a filtered
total as the whole is misled. Decision 6 of the ADR says the exports must say the scope, and this slice is that decision: the workbook's
`Metadata` sheet and the PDF table export say what the frozen snapshot's query (`Snapshot.Query.Filters`, `.Sort`) narrowed and ordered by,
with and without a layout (S23-A). The exports only say it: the source applied it (decision 4), and no export evaluates a filter. The
document templates do not change. The console's words are S20-B. No route, no store and no migration changes, and `reporting/` is untouched.

An export of a query without filters and sort stays what it is: the PDF byte for byte, the workbook cell for cell but for one row
("Without filters and sort" below).

## What

C1 · **The scope** (`export/present/scope.go`; standard library and `reporting` only). `ScopeOf(snapshot)` is the query's filters in its
normalized order (`reporting.Normalize`: by field, operator, value) and its sort keys in the order they apply, each with the label of the
snapshot's column. The values of an `in` and a `between` are read with the core's `reporting.FilterValues` and nowhere else. The frozen
query is held to the frozen columns by the core's rules that need no catalogue: at most 16 filters and 3 sort keys; a field that is a
column of the snapshot with a label; an operator of the closed set that fits the column's type (`reporting.OperatorFits`); every value well
formed for the column (`reporting.ValidFilterValue`); the two of a `between` in ascending order (`reporting.CompareValues`); a direction of
the closed set; no sort field twice. The query was validated against the catalogue's declaration when the snapshot was admitted, so
anything else is a store that does not agree with itself: `INTEGRITY_FAILURE`, no export says any of it, and the error carries no field and
no value. The words are closed lists beside the core's operators and directions:

| | eq | ne | lt | le | gt | ge | in | between | prefix | contains | asc | desc |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PDF (`Chinese`) | 等于 | 不等于 | 小于 | 不大于 | 大于 | 不小于 | 属于 a、b | 介于 a 和 b 之间 | 开头为 | 包含 | 升序 | 降序 |
| workbook (`English`) | equals | does not equal | is less than | is less than or equal to | is greater than | is greater than or equal to | is one of a, b | is between a and b | starts with | contains | ascending | descending |

A sentence is the column's label, the words and the values: `账龄区间 属于 1_30、31_60`, `应收余额 大于 1000.00`, `应收余额 降序`;
`账龄区间 is one of 1_30, 31_60`. A value is the query's exact string in both: a number is never grouped, rounded or translated (it is the
person's input, shown as given: `1000.00` where the table's cells print `1,000.00`), a boolean stays `true` or `false`. Tests (table-driven):
`TestEveryOperatorOfEveryTypeHasItsWords` (36 sentences in both languages: every operator on every column type it fits — string, date,
boolean, integer, decimal — held to `OperatorFits` so that the table is exactly the fitting pairs; the field, operator and value to the
character; both directions), `TestScopeOfReadsTheQueryInItsNormalizedOrder` (the order whatever the frozen order, the values of an `in` in
the query's own order, the snapshot's query left as it was, the empty scope, a query at its bounds), `TestScopeOfRefusesWhatAFrozenQueryCannotHold`
(32 frozen queries that are integrity failures; none returns a part of the scope or names a value),
`TestAValueThatCouldBeMisreadIsQuotedInThePDFsWords` (C3's quotation marks: eleven values, quoted in the Chinese sentence where they
must be and nowhere else, never in the English one) (present).

C2 · **Workbook** (`export/xlsx/writer.go`). The `Metadata` sheet ends, after every row it had before (the last was the last column of the
`field | type | scale | currency | unit` table), with the scope:

```
query_scope | filters | 2 | sort_keys | 1
filter_1    | 账龄区间 is one of current, 1_30             | aging.bucket    | in | ["current","1_30"]
filter_2    | 应收余额 is greater than or equal to -1.00   | ar.open_balance | ge | -1.00
sort_1      | 应收余额 descending                          | ar.open_balance | desc
```

- The scope row `query_scope | filters | <n> | sort_keys | <m>` is in **every** workbook: its third cell counts the query's filters and its
  fifth the sort keys, `0` and `0` for a query without them. A reader tells "no filter" (`0`) from "a workbook written before S20-C" (no
  such row; nothing is known of its scope). It is found by its first two cells, which no row of the columns table can have (a column's
  type is never `filters`).
- The n rows after it are the filters, `filter_<i> | <words> | <field> | <operator> | <value>`, i from 1 in the query's normalized order;
  the m rows after those are the sort keys, `sort_<i> | <words> | <field> | <direction>`, in the order they apply. The first cells are
  distinct, so a reader that maps the first cell to the second gets every item's words.
- The words are English (the sheet's keys and notes are), with the column's label. The three cells beside them are exactly what the
  query has: the field id, the operator, and the value as the query carries it, for an `in` or a `between` the JSON array of strings as
  text (`["current","1_30"]`). Every cell is text, like the rest of the sheet: a count is not a number cell, a value that looks like a
  number is not one, a value that starts with `=` is no formula.
- Nothing else of the workbook knows of the scope: the Data and Totals sheets are the snapshot's rows and totals (the source narrowed
  them), and the earlier `Metadata` rows are unchanged (`query_hash` is the narrowed query's, as it always was).
- A cell holds 32,767 characters and the library cuts a longer text silently. The values of a query at its bounds fit (the 32 values of
  128 characters as words; the JSON array even when every character is written as an escape, 24,673 bytes); a longer text, which only a
  column label beyond every sensible length makes, is refused (`UNSUPPORTED_COMBINATION`), never cut.

Tests, each reading the workbook back with excelize: `TestTheMetadataSheetSaysTheScopeOfTheQuery` (three filters given out of order and
two sort keys: the six rows, their place at row 23, every cell text, no formula, every fact of the workbook before the scope row equal to
the same snapshot's without filters, two renders the same bytes, the same rows through a presentation that hides a filtered column and
relabels another), `TestAWorkbookOfAQueryWithoutFiltersCountsNone` (`0 | 0` and nothing after; one filter alone; one sort key alone),
`TestAScopeTheWorkbookCannotSayIsRefused` (five frozen queries that do not fit, plain and presented: `INTEGRITY_FAILURE` and no file; the
bounds written whole; words longer than a cell refused), `TestAWorkbookWithoutFiltersOrSortIsWhatItWasButForTheScopeRow` ("Without filters
and sort" below) (xlsx).

C3 · **PDF table export** (`export/pdf/scope.go`, `layout.go`, `template.typ`; the snapshot report, not the document templates). The
query's scope is said in two places, in Chinese, following the template's existing labels:

- **Under the meta line of the first page**, in the same block and type (7.5 pt): `筛选：账龄区间 属于 current、1_30；应收余额 不小于 -1.00`
  and `排序：应收余额 降序`. Filters are joined by ；, sort keys by 、. A value that could be misread in the sentence is set between “ and ”,
  unchanged: one that begins or ends with white space, or that holds a mark the sentence itself uses (、 ； “ ”, and 和 between the two
  values of a `between`): `客户 属于 “甲、乙”、丙`. Any other value is as the query has it.
- **In the footer of every page**, beside the snapshot's id: `快照 <id>（已按 2 项条件筛选、1 项排序）`, or `（已按 2 项条件筛选）`,
  `（已按 1 项排序）`. The total row is on the last page and the words on the first: the footer is what tells a reader of any page that
  the snapshot it names is a narrowed or ordered scope. It always counts every filter and sort key.

**The bound.** A query may carry 16 filters of 32 values of 128 characters, more than a page of words, and a PDF never cuts silently. The
filter sentence takes **at most 6 lines** of the meta block and the sort sentence **at most 2** (lines of the page's text width at 7.5 pt:
about 68 ideographs or 125 Latin characters on a portrait page, 100 or 185 on a landscape one; the sort sentence has a bound because a
column label has none). A sentence that fits is printed whole. One that does not says the count, lists the first items that fit **whole**
(in the query's order, never a part of one) and says where the rest are:
`筛选（共 16 项，此处列出前 5 项）：…；其余 11 项见本快照的查询及其 XLSX 导出的 Metadata 工作表。` When not even the first item fits:
`筛选：共 16 项，此处未能列出，见本快照的查询及其 XLSX 导出的 Metadata 工作表。` (the same two forms with 排序). The workbook's `Metadata` sheet
holds every one exactly (C2).

The lines are composed by the server, not by Typst (`measurer.lines`): filled greedily, broken only where a line may certainly break and
at a label's own line breaks, a run wider than the line cut into pieces that fit, each line measured to fit the text width with the slack
that absorbs what Typst adds. `data.json` carries them as `scope`, one string a printed line, and the template sets each on a line of its
own, so the number of lines is the server's and the same input gives the same lines. Every character is checked against the fonts by that
measuring, the words included, and **the whole scope is checked, whether or not the bound lets all of it be printed**: a filter value or a
label the fonts do not cover is `UNSUPPORTED_COMBINATION`, as a value of the table is, never dropped; the snapshot remains available as a
workbook. The words are checked only when they are printed: a font without them still renders a query without filters.

Tests without Typst (the built data, a fake font of known widths): `TestBuildSaysTheScopeOfAFilteredSnapshot` (the two lines, the footer to
the character, the `scope` key, everything else of the document equal to the same snapshot's without filters; one filter alone, sort keys
alone, a quoted value and a `between`), `TestTheScopeLinesAreBoundAndSayWhenTheyContinue` (16 short filters whole; 16 of sixty characters:
five listed, eleven elsewhere, and one more would have made a seventh line; a query at its bounds and one filter longer than the block:
the sentence that lists none, in one line, laid out in well under the job's time; three long sort keys: one listed; the footer's count is
the query's in every case), `TestTheScopeIsCheckedAgainstTheFontsAndTheColumns` (a value the fonts lack, one the bound keeps off the page,
six characters of the words and the mark, a label only the scope prints, a label with a line break, six frozen queries that do not fit,
plain and presented), `TestLinesFillGreedilyAndLoseNothing` (nine literal layouts, 4,000 random texts: nothing lost, no line break lost,
every line within the width, a combining mark never parted from its character), `TestABuildWithoutFiltersOrSortIsWhatItWas` (pdf).
With Typst and the pinned fonts: `TestTypstExportsThroughTheService` (C6), `TestTypstLaysOutDocuments/filtered_bound` (16 filters and 2
sort keys over three pages: the six lines with five filters listed, the sort line, the count in every page's footer),
`/filtered_unlisted` (a query at its bounds: the one line that lists none), `/filtered_run` (a value that is one unbreakable run of 128
characters, cut by the server and printed whole in order; a quoted value). Each is rendered twice to the same bytes and written into
`REPORTING_PDF_EVIDENCE` **with its expectation**: `scripts/check_pdf_export.py` reads the scope lines back from the first page, above the
table, and the mark from the footer of every page, with its negative controls, and veraPDF validates the files as PDF/A-2b (text only: no
transparency, the embedded fonts). `export/pdf/testdata/golden.json` gains their four layouts and nothing else.

C4 · **Presented exports** (S23-A) carry the same scope: the same `Metadata` rows and the same lines and mark, whatever the layout shows.
The labels are the dataset's, also for a column the layout relabels or leaves out (a filter may be on a column the layout does not show,
and then its label is printed, and checked against the fonts, in the scope line only). Tests: `TestAPresentedBuildCarriesTheSameScope`
(pdf), the presented halves of `TestTheMetadataSheetSaysTheScopeOfTheQuery` and `TestAScopeTheWorkbookCannotSayIsRefused` (xlsx),
`TestAFilteredSnapshotsWorkbookSaysItsScope` (httpapi), `presented_filtered.pdf` of `TestTypstExportsThroughTheService` (veraPDF; the
checker has no presented mode, as S23-A said).

C5 · **The document templates do not change** (ADR 0023 decision 6): a preview and an issued PDF are per row or group and say nothing of
the scope. The issue record keeps the snapshot's id and digest as for any snapshot, and that snapshot's frozen query holds the filters.
Test: `TestAnIssueFromAFilteredSnapshotRecordsThatSnapshot` (reporting): a filtered, sorted query is frozen; row 0 of it (another row
than row 0 of the whole dataset) is issued; the product is asked about that row of its own narrowed read; the record and the view name
the filtered snapshot and its digest; the same row index of the whole dataset's snapshot is another record.

C6 · **Through the service and the routes.** `TestAFilteredSnapshotsWorkbookSaysItsScope` (httpapi): `POST /snapshot-jobs` with two filters
and a sort, `POST /exports`, the download: the Data and Totals sheets are the narrowed scope's rows and total, `Metadata` ends with the
four scope rows and its `query_hash` is the narrowed query's; the whole dataset's workbook ends with `query_scope | filters | 0 | sort_keys | 0`;
the export through a saved layout says the same scope. `TestTypstExportsThroughTheService` (pdf, with Typst): the same query frozen and
exported as PDF through the service: the built document's lines and footer, other bytes than the unfiltered export, two renders the same
bytes; `filtered.pdf` with its expectation and `presented_filtered.pdf` are written as evidence.

C7 · **Docs**: this file; docs/API.md ("The exports say the scope", the export row, the workbook paragraph, the filters paragraph);
docs/openapi.json (the description of `POST /exports`; no schema changes); ADR 0023's status line and slice list; ADR 0017's Progress line.

C8 · Not done: the console's chip, subtitles and evidence line (S20-B); a route that serves a snapshot's query; the scope in the document
templates (decided against by the ADR); a presented-table mode of the PDF checker (S23-A's open item).

## Without filters and sort

- **PDF: byte for byte what it was.** `data.json` has no `scope` key and the footer is the string it was; the template lays out the meta
  block exactly as before when the key is absent. `export/pdf/testdata/plain_sha256.json` and `TestTypstLaysOutDocuments/plain_golden`
  hold the ten plain documents to the SHA-256 recorded before S23-A, unchanged by this slice (the file is not touched).
  `TestABuildWithoutFiltersOrSortIsWhatItWas` holds the data without Typst: no key, the footer to the character, the same data for nil
  and for empty lists.
- **Workbook: exactly one row more.** The scope row `query_scope | filters | 0 | sort_keys | 0`, five text cells, after the last row
  the `Metadata` sheet had. Every other sheet, pane, column width and cell (address, type, raw value, formula, number format, weight,
  font colour, fill) is what it was. `export/xlsx/testdata/cells_before_s20c.json` lists two workbooks fact by fact as the writer
  rendered them at 43f8db5, before this slice (the ledger fixture plain and through a presentation; 131 and 135 facts), and
  `TestAWorkbookWithoutFiltersOrSortIsWhatItWasButForTheScopeRow` requires today's workbooks to be those facts, in their order,
  followed by the five cells of the scope row and nothing else (`REPORTING_XLSX_WRITE_GOLDEN=1` rewrites the file after a reviewed
  change). The test is by cells, so that it survives an upgrade of the spreadsheet library. The bytes were compared once, by hand, part
  by part of the zip, between the workbook the writer at 43f8db5 makes of the fixture and today's: ten of the twelve parts are the same
  bytes (the Data and Totals sheets, the styles, the workbook, the properties); `xl/worksheets/sheet3.xml` has the one `<row r="23">`
  more, and `xl/sharedStrings.xml` three strings more (`query_scope`, `filters`, `sort_keys`; `0` was there) with its two counts.

## Interpretations of ADR 0023 decision 6

- **"The PDF table export's footer carry the filters and the sort in words."** The footer of every page has room for three lines, a
  scope can be many more, and the words must not be cut: the words are in the meta block of the first page, within the bound, and the
  footer of every page says, beside the snapshot's id, that and by how many filters and sort keys the snapshot was narrowed and ordered
  (decision 3: "a snapshot made with filters says so wherever a snapshot is named"). The page that carries the total row is thus never
  silent about the scope.
- **The count row is always present**, so that "no filter" can be told from "old file"; it is one row, appended, so that an unfiltered
  workbook changes as little as that allows and no cell that existed moves. It has five cells (`query_scope`, then `filters` and
  `sort_keys` each followed by its count) so that the one row says both counts by name.
- **The words follow each file's language**: Chinese in the PDF, English in the workbook, the dataset's own labels in both. The labels
  are the snapshot's columns', never a layout's.
- **Exact strings.** Values are printed as the query has them: no digit grouping, no translation of `true`/`false`, the values of an `in`
  in the query's order. The workbook's exact `value` cell is the query's text to the character, including the JSON text of a list as the
  client wrote it (the core keeps it verbatim and compares it so).
- **Quotation marks in the PDF only.** The page has no cell beside the words that holds the exact value, so a value that could be
  misread in the sentence is quoted; the workbook's row has the exact cells and quotes nothing.
- **The bound is by whole items**, and the two sentences have separate bounds (6 and 2 lines), so that a long filter list never takes
  the sort's place. The whole scope is checked against the fonts even when the bound keeps part of it off the page: the same value is
  then refused or printed whatever else the query carries.
- **`between` is said as the operator, `介于 a 和 b 之间` / `is between a and b`**; whether its ends are included is the source's
  published meaning of the operator (the simulators include both), and the export does not restate it.
- **A frozen query that does not fit is an integrity failure**, not an unsupported combination: admission validated it, so the store no
  longer agrees with itself. The checks are the core's rules that need no catalogue; a declaration's closed `values` and `required` are
  not asked again (the declaration is not frozen with a snapshot).

## Evidence

- `go test ./export/... ./reporting/... ./transport/httpapi/...`: the tests named above; the strict Go check (`scripts/go_test_strict.py`
  with CI's `EXPECTED_SKIPS`), `gofmt -l .` and `go vet ./...` are clean. No new top-level Typst test: the three skip lists of `ci.yml`
  are unchanged.
- `go test ./export/pdf/... -count=1` with `REPORTING_TYPST`, `REPORTING_TYPST_SHA256`, `REPORTING_PDF_FONTS` and
  `REPORTING_PDF_REQUIRE=1` passes with the pinned Typst 0.15.1 and Noto Sans SC 2.004, `plain_golden` included.
- `scripts/check_pdf_export.py --negative` over that run's evidence, with the pinned PyMuPDF: `filtered.pdf`, `filtered_bound.pdf`,
  `filtered_unlisted.pdf` and `filtered_run.pdf` are ok (rows, totals, the scope lines above the table, the mark in every footer), every
  negative control is caught, and the layouts of the documents that existed are the golden file's.
- The console suite (format, lint, `go test ./console/...`, the jsdom tests against the real demo server with Typst) passes unchanged:
  the console sends no filter yet (S20-B), and its exports are the unfiltered ones.

## Known

- The sentence that says "elsewhere" names the snapshot's query and the workbook's `Metadata` sheet. No route serves a snapshot's query
  today (the console's chip of S20-B shows the current one); the workbook export of the same snapshot is where a person reads every
  filter exactly.
- The number of lines is the server's measure, which is made never to be narrower than what Typst draws (the slack of the table export);
  should a line be wider than measured, Typst wraps it and nothing is lost. The checker verifies that the lines are on the first page
  above the table, not how many they are.
- A workbook written between S20-A and this slice has no `query_scope` row, like any older one: its scope is unknown from the file, and
  its `query_hash` still binds it.
- The scope lines of a presented PDF are validated by veraPDF and by the built-data tests, not by the checker (it has no presented mode).
- The labels are those frozen with the snapshot; the catalogue is not asked again.
