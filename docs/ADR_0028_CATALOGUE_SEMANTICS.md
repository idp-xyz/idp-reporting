# ADR 0028 · The catalogue says when a value may be absent, what an instant is, and whether a metric may be summed

Date: 2026-11-11. Status: **proposed**. It changes the adapter contract and the source protocol, so the product owner answers the
three questions at the end and nothing here is built before that (CODING_STANDARDS.md). ADR 0027's S26. Accepted, it would add
to the catalogue row of docs/s00/INTERFACES.md and amend ADR 0024 decision 1 (which metrics a subtotal sums) and ADR 0004
decision 6 (which values the platform adds for presentation).

## Context

A dataset's column has one of five types: `string`, `date`, `boolean`, `integer`, `decimal` with a scale (`reporting.validColumn`),
and every cell is the exact text its type allows (`validCell`). Three things cannot be said.

**That a value is absent.** A `date`, `boolean`, `integer` or `decimal` cell must be a well-formed value; only a `string` may be
empty. The first real source has absent values wherever a fact has not happened yet (docs/REAL_SOURCE_DISCOVERY_PARCEL.md
section 7): a charge that is not confirmed has no confirmation time and no attribution date, a parcel that is not delivered has
no delivery date, a dimension that was not declared has no value. Today such a source must invent a value (a date in year 1, a
zero), which is wrong data that looks like data, or publish the column as a string and lose its type.

**An instant.** There is `date` and nothing finer. Submission, decision, handover, delivery and publication are instants in the
first real source, stored as `timestamptz`.

**Whether a metric may be summed.** A dataset lists its metrics; nothing says what adding them means. The platform adds them
all the same: a layout's subtotal row carries "the exact decimal sum of each shown metric column" (ADR 0024 decision 1,
`export/present`), a banded document prints group sums (ADR 0004 decision 6), and a document table carries its columns from
page to page. A unit price, a rate, a percentage or a count of distinct things would be summed like an amount, silently. One
place fails closed by accident and with the wrong word: a 1.x document table requires the sum of its rows to equal the
snapshot's total and answers `INTEGRITY_FAILURE` when it does not (`export/pdf/table.go`). The conformance checker has to guess:
it takes a metric for additive when the read's total happens to equal the sum of its rows (`additiveMetrics`,
`adapters/httpsource/conformance`), and holds only those to "a narrowed read's total is the sum of its rows".

What bounds the answers: values are exact strings and never JSON numbers; the answers of a source hold no `null` and no unknown
field (the strict rules of docs/SOURCE_HTTP_PROTOCOL.md section 1); a snapshot's rows are `[][]string`, and its digest, its
exports, the layouts, the templates and the console all read them so; the platform never rounds or computes a value of the data
(ADR 0024); a dataset version is immutable, and a change of a column is a new version; unknown capabilities fail closed. No
product implements the protocol yet, so today a change costs the simulators and the reference source.

## Decision (proposed)

1. **A column may be nullable, and its absent value is the empty text.** `Column` gains the optional `nullable: true`, allowed on
   a `date`, `datetime`, `boolean`, `integer` or `decimal` column, a dimension or a metric. A cell of such a column is a
   well-formed value or the empty string, which means that there is no value. The empty string was never a value of those
   types, so nothing becomes ambiguous; a `string` column keeps what it has, where the empty string already says "nothing" and
   takes no `nullable`. Rows stay `[][]string`, the wire stays free of `null`, and a dataset version that declares nothing is
   exactly what it was. What follows:
   - The total of a nullable metric may be empty. The platform's sums skip an absent value and are empty when every value they
     would add is absent; an absent value is never a zero.
   - Filters: two operators join ADR 0023's closed set, `blank` and `not_blank`, which take no value and which a source may
     declare for a nullable column or a `string` column. Every other operator matches only cells that have a value: `ne` does
     not match an absent one. In a declared sort, absent values come after every value ascending and before every value
     descending. The checker verifies both.
   - A layout's mark `blank` (ADR 0024, today for columns that are not numeric) is allowed on a nullable column of any type.
     Exports write an empty cell. A document template's field prints nothing, and a code whose value is absent is refused as
     today (`NOT_COVERED`).
2. **A type for an instant: `datetime`.** Its text is `YYYY-MM-DDTHH:MM:SSZ`: UTC, whole seconds, twenty characters, years 0001
   to 9999. One instant has one text, and the order of the texts is the order of the instants, so the platform compares them
   as it compares dates. A source that holds finer times gives the second they fall in. The platform shows an instant in the
   dataset's `timezone`, which every dataset already declares and the core already validates as an IANA zone, and it says the
   zone wherever it shows one. What follows:
   - Filters and sorts take `datetime` as they take `date` (`eq ne lt le gt ge in between`), with values in the same text. The
     console lets a person pick a local date and time in the dataset's zone and sends the instant.
   - A layout may show a `datetime` column to the second, to the minute or as its date (a closed format, as a number's). The
     workbook writes a date-time cell beside the exact text, by ADR 0024's rule for numbers; the PDF and the screen print local
     time and keep the exact text in reach. A document template prints it by the same closed formats.
3. **A metric says whether it may be summed.** A metric's column gains the optional `aggregation`, one of a closed set: `sum`
   (the total over any set of rows of one read is the exact sum of their values) or `none` (it is not: a price, a rate, a
   share, a count of distinct things, a balance over time). `sum` is allowed on an `integer` or `decimal` column only. **A
   metric that declares nothing is `none`**: the platform does not add what the source did not say may be added. What follows:
   - The platform adds only `sum` metrics: a layout's subtotal row leaves any other column empty; a template that asks for a
     group sum or a page carry of one is refused when it is bound; a document table prints the source's total for it and no
     carry. The 1.x table's check that the rows sum to the total applies to `sum` metrics, where it is right, and no longer
     turns a rate into an integrity failure.
   - The source's own total of every metric is shown as the source gave it, as today. For a `none` metric it is the source's
     figure for the whole read (the overall rate, the distinct count), which the platform could not have computed.
   - The checker stops guessing: for a `sum` metric the read's total must be the sum of its rows, narrowed or not; a `none`
     metric is not held to it.
   - The two simulated datasets declare `sum` (their metrics are sums), so the demo, the layouts and the documents behave as
     today.
4. **The protocol** gains these as a minor revision of version 1 (the next after ADR 0026's): `nullable` and `aggregation` on a
   column, the type `datetime`, the operators `blank` and `not_blank`. All are optional. A source of an earlier revision is
   served unchanged in every respect but one: its metrics declare nothing, so the platform no longer sums them (Q3).
5. **Not decided here.** JSON `null` in rows. A time of day or a duration. Instants finer than a second. A third aggregation for
   a balance that may be summed across everything but time (it is `none` until a dataset needs it; a new value of the closed
   set is a minor revision). Display scales and rounding (never, ADR 0024). What grouping on a snapshot may do with `sum`
   metrics (ADR 0027's S30, its own ADR).

## Alternatives

- **JSON `null` for an absent value.** The usual encoding. Rows would become lists of nullable strings: the snapshot's stored form
  and digest, the row digests a product checks when it issues, both exports, the layouts, the templates, the console and the
  protocol's "no `null`" rule all change, and every stored snapshot is the old form. The empty text costs one flag on a column.
- **Leave it to the source** (a sentinel date, a zero, a string column). It is what a source must do today: wrong values that
  cannot be told from real ones, or untyped columns that cannot be filtered or formatted as what they are.
- **An instant with the source's own offset.** It keeps what the source wrote. One instant then has many texts, the texts do not
  sort as the instants do, and two rows of one column may be in different offsets. The dataset's zone already says how to show
  it.
- **An instant as two columns**, a date and a text. No change, and no ordering, no filtering by range, and a day boundary the
  source has to pick for every reader.
- **A metric that declares nothing is summed, as today.** Nothing changes for a dataset that exists. But the default is the
  dangerous one: a product that forgets the declaration gets wrong subtotals that look right.
- **Infer it**, as the checker does today. A rate whose total happens to equal the sum of its rows on the day of the check is
  taken for additive.

## Consequences

- The core's column and cell rules, the filter and sort rules, the reference source, both simulators, the `http` adapter and
  the checker change (S26-A); then the presentation of exports, the layouts and the document templates (S26-B); then the console:
  the type's name, the pickers, the formats, the empty cells (S26-C). Each with the OpenAPI descriptions and the documents.
- A real dataset can carry its facts as they are: Parcel's unconfirmed charges without a made-up date, its instants as
  instants, its counts marked as what may and may not be added.
- A layout that had subtotals over a dataset whose metrics declare nothing loses them. Only the simulated datasets exist, and
  they declare.
- The platform needs the zone database to show instants; it is already embedded for the schedules.
- More to check in a product's implementation (the order of absent values, the two new operators, the sums), and the checker
  checks it.

## Questions for the product owner

- **Q1, absent values.** (a) A nullable column whose absent value is the empty text (recommended). (b) JSON `null` in the rows.
  (c) Nothing: a source substitutes a value.
- **Q2, instants.** (a) `datetime` as UTC seconds, shown in the dataset's zone (recommended). (b) A text with the source's own
  offset. (c) No type: a date and a string.
- **Q3, a metric that declares nothing.** (a) It is not summed (recommended). (b) It is summed, as today.
