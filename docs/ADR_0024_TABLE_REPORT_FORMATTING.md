# ADR 0024 · Table reports: a layout is the report's presentation, and the exports carry it

Date: 2026-11-11. Status: accepted. Implementation: S23-A (layouts 1.1, exports with a layout, typed workbook cells),
S23-B (the console's layout designer and table view, baseline task T11).

## Context

A table report is today a frozen snapshot shown as the source returned it. A *layout* (`layouts.Document`, schema 1.0)
picks and orders the columns and aligns them, and it is applied on screen only: `POST /exports` renders the whole
snapshot, every column in catalogue order, and knows nothing of the layout the person was looking at. The workbook
writes every cell as text (`export/xlsx`: "All values are exact text cells"), so a finance user cannot `SUM` a column
without converting it first; the PDF groups digits and nothing else. There are no subtotals, no number formats, no
conditional marks, no title or footnote. The functional review of 2026-11-11 ranked this sixth of the gaps, after the
query filters (ADR 0023), the dashboards, the delivery and the issue page.

What must not change: the source product owns the facts and the amounts; the platform sums printed values for
presentation only (ADR 0004 decision 6) and *never rounds or computes a value* (`export/pdf.build`); exact decimal
strings are what is stored, compared and digested; the same input gives the same bytes; a value that does not fit is
refused, never cut; every list is bounded; the core stays domain-neutral.

## Decision

1. **Layout schema 1.1, additive.** A 1.0 layout stays valid and means what it meant. 1.1 adds, all optional:
   - `title` (a literal, 1..120 characters) and `footnote` (a literal, 1..400 characters). Printed by the exports; never
     a value of the data.
   - On a column: `align` gains `center`; `label` (a literal, 1..64 characters) replaces the dataset's label in the
     header, as a 2.0 document table column may (ADR 0004 decision 5); `format`, allowed only on a `decimal` or
     `integer` column, is `{"grouping": bool, "negative": "minus" | "parentheses", "zero": "digits" | "dash"}` with the
     defaults `true`, `"minus"`, `"digits"`. A format changes how the digits are shown, never which digits: there is no
     rounding, no scaling and no unit conversion. A person who wants thousands asks the source for a thousands metric.
   - `subtotals`: `{"by": "<dimension column id>"}`, one level. The rows are partitioned by the exact frozen strings of
     that column, the groups are ordered by their first row and keep the snapshot's row order inside, as ADR 0004
     decision 2 partitions a 2.0 document; after each group a row labelled 小计 carries the exact decimal sum of each
     shown metric column, by the rule the document tables use for their totals. The grand total row stays the source's
     `Totals`, labelled 合计, and is not recomputed; the platform does not reconcile the two.
   - `marks`: 1..8 rules `{"column": id, "when": op, "value": v, "mark": m}`. `op` is one of `lt le gt ge eq ne` on a
     numeric column (exact decimal comparison, `value` an exact decimal) or `eq ne blank` on any other column (`value`
     a string of at most 128 characters, absent for `blank`). `mark` is one of `good warn bad note`: a closed palette
     with one colour each, the same on screen, in the PDF and in the workbook. A mark colours a cell; it never hides a
     row, changes a value or adds text.
   - Bounds: at most 100 columns as today, 8 marks, 64 KiB. Unknown keys, a format on a non-numeric column, a mark
     whose column is not in the layout, a subtotal column that is not a dimension, a comparison that does not fit the
     column's type are all refused (`ErrInvalid`, 400).
2. **An export may carry a layout.** `POST /exports` gains an optional `layout_id`. The layout must be bound to the
   snapshot's integration and dataset and readable by the principal (own or the team's), else 422
   UNSUPPORTED_COMBINATION; the export uses the layout's *latest saved version* at the time of the request and records
   its id and version on the job and the artifact, so the file is reproducible and the console can say which layout it
   printed. Unsaved edits in the console are not exported; the console says so. Without `layout_id` the exports are
   byte for byte what they are today; golden tests guard it.
   With a layout, both exports show the layout's columns in its order with its labels, alignments and formats, the
   subtotal rows, the marks, the title above the table and the footnote under the total row. A PDF of more than
   `MaxColumns` shown columns is refused as today.
3. **Typed workbook cells.** In the `Data` sheet a `decimal` or `integer` column is written as a numeric cell with an
   Excel number format derived from the column's scale and the layout's format (grouping, parentheses), followed by a
   text column `<label>（精确）` holding the exact frozen string. A numeric cell is written only when the exact string
   round-trips through a float64 and back to the same digits at the column's scale; a value that does not round-trip
   is written as text in the numeric column too, and the `Metadata` sheet counts those cells (`inexact_cells`). The
   `Totals` sheet gains `value` beside `exact_value` under the same rule. Subtotal rows are written as values, never as
   formulas: the workbook stays a record, not a calculation. The `Metadata` sheet names the layout (`layout_id`,
   `layout_version`, `layout_title`) and replaces the precision note with: numeric cells for arithmetic, the exact
   columns for reconciliation. This applies to every workbook, with or without a layout, because the exact columns are
   what the text-only cells promised.
4. **The platform's arithmetic stays presentation.** Subtotals are sums of printed values, like page carries and group
   sums; they are never sent to a source, a webhook, an issue or an audit event, and never compared with the source's
   totals by the platform. A layout is a private design (ADR 0018: owned by a person or by the tenant's shared space,
   released by a publisher); it carries no row, no value and no token.
5. **Console (S23-B).** The layout designer gains the title, the footnote, a column's label and format, the subtotal
   column and the mark rules, in the same drawer as the column order; the table view shows what the exports will
   print (labels, formats, subtotal rows, marks, with the exact value as a cell's title); the export buttons of the
   table view send the saved layout's id and say when the layout has unsaved edits. Baseline task T11 (选列、设置格式和
   小计、保存、导出 Excel) joins the held totals; the English catalogue covers every new word.
6. **Not decided here.** Pivot tables and multi-level subtotals; a colour of the person's choosing; conditional rules
   over several columns; formulas in the workbook (never); charts in the workbook; a layout per export format.

## Alternatives

- **Formats on the dataset, decided by the source.** The catalogue could declare how a column is shown. It would put
  presentation in every source product and make one dataset look the same in every report; a layout is the report's
  own.
- **Round to the layout's decimals.** Every reporting tool does; this platform's PDF has refused to since S02 because a
  shown value that differs from the frozen one is a value the platform decided. The exact column in the workbook and
  the title on screen keep the frozen digits in view; the digits themselves are the source's.
- **Numeric cells only.** Simpler, but a float64 holds 15–17 significant digits and a Chinese ledger in fen can
  exceed them; an exact column beside the number costs nothing and keeps the reconciliation the text cells gave.
- **Formulas for subtotals.** They recompute on open and differ by spreadsheet application; the file is a record of
  what was shown.
- **A separate "report" object over snapshot and layout.** Everything it would hold is already the layout (the
  presentation) and the snapshot (the data); the pair is what `POST /exports` names.

## Consequences

The layouts store validates 1.1 beside 1.0 (`layouts.Validate`), and the console's `applyLayout` accepts both. The
exports gain a presentation step shared by the PDF and the workbook (column pick, labels, formats, groups, marks) in
a package of its own, standard-library only, tested with golden files; the Typst template learns titles, footnotes,
subtotal rows, centred columns and cell fills; the workbook writer learns typed cells and the exact columns. The
OpenAPI description, `docs/API.md`, `docs/CONSOLE.md` and the acceptance baseline grow accordingly. The work is S23-A
(server and exports, with the OpenAPI and docs) and S23-B (console and T11); S23-B waits for S23-A.
