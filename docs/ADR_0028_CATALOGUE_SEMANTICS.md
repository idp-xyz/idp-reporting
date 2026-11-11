# ADR 0028 · The catalogue says when a value may be absent, what an instant is, and whether a metric may be summed

Date: 2026-11-11. Status: **accepted** 2026-11-11. It changes the adapter contract and the source protocol, so it waited for the
product owner's answers to the three questions at the end (CODING_STANDARDS.md): an absent value is JSON `null`, without an
exception; an instant is UTC seconds; a metric that declares nothing is not summed. ADR 0027's S26. It adds to the catalogue row
of docs/s00/INTERFACES.md, amends ADR 0024 decision 1 (which metrics a subtotal sums) and ADR 0004 decisions 2 and 6 (a group's
key may hold `null`; which values the platform adds for presentation), and lets the group key of the issue contract hold `null`
(ADR 0003, ADR 0004 decision 7). Implementation: S26-A to S26-D.

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

## Decision

1. **A column may be nullable, and its absent value is JSON `null`, wherever that value goes** (the owner's answer to Q1).
   `Column` gains the optional `nullable: true`, allowed on a column of any type, a dimension or a metric. A cell of such a
   column is a text of its type or `null`. In a column that does not declare it, `null` stays what it is today: an answer that
   does not verify. For a `string` column `null` and the empty text are two things, nothing and a text of no characters. What
   follows:
   - **One rule, held by one type.** A cell is a type of its own in the core, and `null` is accepted exactly where that type
     is: a cell of a row, the total of a metric, a value of a group's key. Everywhere else every strict decoder refuses `null`
     as it does today, so that a required field still cannot be passed as `null`. The protocol's rule "no `null`" becomes: no
     `null` except those three, in a column that declares `nullable`. The platform's own API answers and accepts them the same
     way, and the console holds an answer to it.
   - **Rows are lists of cells** wherever they travel and rest: a source's pages, the platform's results, a snapshot's rows,
     the rows a row digest covers. A row without a `null` is encoded exactly as it was, so every stored snapshot, every digest
     of a row or a group and every issue record stays valid, and a dataset version that declares nothing is exactly what it
     was. A `null` is part of a digest, as `null`.
   - The total of a nullable metric may be `null`. The platform's sums skip a `null` and are `null` when every value they would
     add is; a `null` is never a zero.
   - Filters: two operators join ADR 0023's closed set, `null` and `not_null`, which take no value and which a source may declare
     for a nullable column. Every other operator matches only cells that have a value, as in SQL: `ne` does not match a `null`.
     In a declared sort, `null` comes after every value ascending and before every value descending. The checker verifies both.
   - **A nullable column may be a group field, and a group's key may hold `null`.** The rows without a value in a group field
     are one group, as in SQL, for a layout's subtotals and for a document alike. A group's key is a list of cells wherever it
     travels: in an issue request, in the intent a product is asked to authorize, in the issue record and its views. Whether a
     document may be issued for the group that has no customer is the product's decision when it is asked to prepare it, as
     every issue is (ADR 0003); the platform does not decide it by refusing the template. A key without a `null` is encoded and
     digested exactly as it was, and a product that declares no nullable dimension never sees one.
   - A layout's mark `blank` (ADR 0024, today for columns that are not numeric) matches a `null` and an empty text, on a nullable
     column of any type: it is a rule about what a person sees. Exports write an empty cell, in the exact column too. A document
     template's field prints nothing for a `null`, and a code whose value is `null` is refused as an empty one is today
     (`NOT_COVERED`).
2. **A type for an instant: `datetime`** (the owner's answer to Q2). Its text is `YYYY-MM-DDTHH:MM:SSZ`: UTC, whole seconds,
   twenty characters, years 0001 to 9999. One instant has one text, and the order of the texts is the order of the instants, so
   the platform compares them as it compares dates. A source that holds finer times gives the second they fall in. The platform
   shows an instant in the dataset's `timezone`, which every dataset already declares and the core already validates as an IANA
   zone, and it says the zone wherever it shows one. What follows:
   - Filters and sorts take `datetime` as they take `date` (`eq ne lt le gt ge in between`), with values in the same text. The
     console lets a person pick a local date and time in the dataset's zone and sends the instant.
   - A layout may show a `datetime` column to the second, to the minute or as its date (a closed format, as a number's). The
     workbook writes a date-time cell beside the exact text, by ADR 0024's rule for numbers; the PDF and the screen print local
     time and keep the exact text in reach. A document template prints it by the same closed formats.
3. **A metric says whether it may be summed** (the owner's answer to Q3). A metric's column gains the optional `aggregation`,
   one of a closed set: `sum` (the total over any set of rows of one read is the exact sum of their values) or `none` (it is
   not: a price, a rate, a share, a count of distinct things, a balance over time). `sum` is allowed on an `integer` or
   `decimal` column only. **A metric that declares nothing is `none`**: the platform does not add what the source did not say
   may be added. What follows:
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
   column, the type `datetime`, the operators `null` and `not_null`, and `null` as a cell, a total or a value of a group's key
   where the column allows it. All are optional. A source of an earlier revision is served unchanged in every respect but one:
   its metrics declare nothing, so the platform no longer sums them.
5. **Not decided here.** A time of day or a duration. Instants finer than a second. A third aggregation for a balance that may
   be summed across everything but time (it is `none` until a dataset needs it; a new value of the closed set is a minor
   revision). Display scales and rounding (never, ADR 0024). What grouping on a snapshot may do with `sum` metrics (ADR 0027's
   S30, its own ADR).

## Alternatives

- **The empty text for an absent value** (engineering's first recommendation for Q1): a nullable column of a type other than
  `string` accepts the empty text, which was never a value of those types. Rows stay lists of texts and nothing that reads them
  has to change. That is its one advantage and its defect. The absent value travels in band, as a text, and every piece of code
  that does not know the convention treats it as one: a number of no digits, a date that sorts before every date, a text that
  a sum or a chart must remember to skip. Nothing finds those places; with a cell as a type of its own, the compiler lists
  every one of them in the Go code. It cannot tell an absent string from an empty one. And it cannot be undone: once a product
  has implemented the convention, moving to `null` is a new version of the protocol, where choosing `null` now, before any
  product implements anything, closes no door. What it saves is the platform's work, once. Engineering recommended it for that
  reason and withdrew the recommendation after reading the code: most of the work it saves is the review of every reader of a
  row, which has to happen either way. (One argument for `null` does not apply to the first real source: Parcel's nullable
  text columns are constrained to NULL or a non-blank text, so the difference between an absent string and an empty one does
  not arise there.)
- **`null` with exceptions**: no nullable group field, so that the issue contract's key stays a list of texts (the first draft
  of this decision), or `nullable` on every type but `string`. Each exception is a rule a product has to learn and the platform
  has to test and explain, made to save work in the platform; and a key is a projection of a row's cells, so two types for the
  same values need a conversion at every place they meet. The owner asked for the judgment without a compromise.
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

- **Everything that reads a row or a group's key learns that a cell may be `null`**: the core's cell and row types, validation
  and digests; the strict decoder, which every request body, every answer of a source, every template and every dashboard pass
  through and which refuses `null` wholesale today; the two stores' payloads; the cursor and paging of snapshot rows; the issue
  group, in the request, the intent and the record; the reference source, both simulators, the `http` adapter and the checker;
  the workbook and PDF writers and the presentation they share; the layouts; the document templates, their layout and the
  issuer; the console, the embed page and the dashboards; the two OpenAPI descriptions. It changes a type that has been a list
  of texts since S01-A and that every later slice reads. It is bounded by one fact that the slices must prove with the existing
  golden files and stored fixtures: a row and a key without a `null` encode, digest, print and export byte for byte as before.
- The issue contract stays at version 2: a key without a `null` is what it was, and `nullable` is a declaration a product makes.
  A product that declares a nullable dimension answers for the group without a value when it is asked to prepare a document.
- A real dataset can carry its facts as they are: Parcel's unconfirmed charges without a made-up date, its instants as
  instants, its counts marked as what may and may not be added.
- A layout that had subtotals over a dataset whose metrics declare nothing loses them. Only the simulated datasets exist, and
  they declare.
- The platform needs the zone database to show instants; it is already embedded for the schedules.
- More to check in a product's implementation (where a `null` may be, its order, the two new operators, the sums), and the
  checker checks it.

## Slices

The cell type goes first, because everything else of this ADR is written on top of it.

- **S26-A**: the cell as a type of its own through the core, the strict decoder, the stores, the issue group, the adapters, the
  exports, the layouts, the templates and the issuer, **with no dataset able to declare `nullable` yet**. Nothing a person or a
  product sees changes; every golden file, fixture and digest is unchanged, which is what this slice proves.
- **S26-B**: `nullable` can be declared: the protocol's revision, the reference source, both simulators, the `http` adapter, the
  checker; the operators `null` and `not_null`, the order of `null`, the sums, the groups; what the exports, the templates, the
  console, the embed page and the dashboards do with an absent value.
- **S26-C**: `datetime` and `aggregation`, through the same layers; the English catalogue; a baseline task.
- **S26-D**: the documents: API, the protocol, the onboarding guide, SECURITY_REVIEW; the notes in ADR 0003, 0004, 0023 and 0024.

## Questions for the product owner

- **Q1, absent values.** (a) A nullable column whose absent value is the empty text (recommended). (b) JSON `null` in the rows.
  (c) Nothing: a source substitutes a value.
- **Q2, instants.** (a) `datetime` as UTC seconds, shown in the dataset's zone (recommended). (b) A text with the source's own
  offset. (c) No type: a date and a string.
- **Q3, a metric that declares nothing.** (a) It is not summed (recommended). (b) It is summed, as today.

## The owner's answers (2026-11-11)

- **Q1: (b), JSON `null`.** The owner chose (b) over the recommended (a), then asked engineering whether the choice was sound.
  Engineering read the code and Parcel's migrations and answered that it was the better choice (the alternatives above say
  why, and which argument does not hold for Parcel). The owner confirmed (b) and asked for engineering's best judgment without
  a compromise. Decision 1 is that judgment: `null` wherever the value goes, a group's key included, with no exception. The
  first draft's exception (no nullable group field) was withdrawn, and the owner was told so before this ADR was merged.
- **Q2: (a).** UTC seconds, shown in the dataset's zone.
- **Q3: (a).** A metric that declares nothing is not summed; the two simulated datasets declare.
