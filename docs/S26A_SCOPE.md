# S26-A · The cell

2026-11-13. The first slice of ADR 0028 (docs/ADR_0028_CATALOGUE_SEMANTICS.md). A value of a dataset (a cell of a row, the total of a
metric, a value of a group's key) has been a Go string since S01-A, and everything that reads one reads a string. ADR 0028 lets a
column declare that its value may be absent, and the absent value is JSON `null` wherever the value goes. For that, a value has to be
something that can be absent. This slice makes it a type of its own, `reporting.Cell`, in the core and in everything that reads a row,
**while no column can declare `nullable`**: a cell without a value can be written in Go and is refused wherever a value enters the
platform. The slices after it decide, place by place, what an absent value does. This one finds the places, and proves that a value
which is present is everywhere exactly the string it was.

What a deployment sees of this slice:

- nothing. No change of the source protocol or its revision, of the API, of the two OpenAPI descriptions, of the console, of a stored
  row, a digest or a file; no SQL migration; nothing to configure, and no order of rollout: a binary of this slice and one before it
  read and write the same stores;
- a source that answers `null` for a cell or a total is refused as before, in the same words, and so is a request that names a group
  by a key with a `null` in it;
- one thing changes outside a deployment. Go code that implements `reporting.Adapter` itself builds against this version only when
  its pages hold cells (`reporting.CellRows`, `reporting.CellTotals`). ADR 0022 lets a host register Go adapters of its own; the
  only adapters that exist are this repository's.

## What

A1 · **The cell** (`reporting/cell.go`). A `Cell` is a text or no value: `Text(s)` and `NoValue()`.

- The zero `Cell` is the empty text, as the zero string was: a row made with `make` is a row of empty texts, not of absent values.
- The text is reached in three ways, and each says what an absent value is there. `Value()` gives the text and whether there is
  one. `Required()` gives the text, or `ErrIntegrity` for a cell without a value. `Text()` gives the empty text for a cell without a
  value: what a person is shown. A cell is not a string and converts to none, which is the point of the type: the compiler lists
  every place that reads a value.
- Cells compare with `==`: two are equal when both hold the same text or both hold no value. A cell without a value equals no text,
  the empty one included.
- In JSON a cell that has a value is the JSON string of its text, byte for byte what `encoding/json` wrote for the string: alone, in
  a row, in a map, inside a structure, and under either setting of the encoder that asks (it escapes the HTML characters or it does
  not, it indents or it does not). A cell without a value is `null`. `MarshalJSON` returns the string without the HTML escapes, and
  the encoder that asked applies its own setting to it, as it does to the answer of every `Marshaler`. Two characters need care:
  the encoder escapes U+2028 and U+2029 in a string under both settings, and in a `Marshaler`'s answer only when it escapes HTML,
  so a text that holds one is written by an encoder of the cell's own, like every text that needs an escape. A text that needs
  none is written between two quotes as it is.
- A cell is read from a JSON string or `null`, and from nothing else: a number, a boolean, a list or an object is not a cell.
  Called by itself with bytes that are no JSON value, `UnmarshalJSON` refuses them rather than read a part.
- Formatted (`%v`, `%s`), a cell is what JSON writes for it, the text in its quotes or `null`: the message of a failed test tells an
  empty text from no value, and a cell formatted where its text was meant shows its quotes.

A2 · **Where a value is a cell.** What an adapter answers (`SourcePage.Rows`, `.Totals`); a query's answer, a snapshot's rows and a
page of them (`Result.Rows`, `.Totals`); a group's key, in the job, the intent and the record (`IssueGroup.Key`), in the views
(`IssueGroupView.Key`) and as the templates partition a snapshot (`doctemplates.RowGroup.Key`). The functions over them take and
give cells: `RowsDigest`, `GroupRows`, `NewIssueGroup`, `VerifyGroup`, `ValidateSourcePage`; the simulators' `FrozenRows`, `CheckRow`,
`CheckGroup` and `CheckIntent`; `doctemplates.GroupByKey`. The digest of a row, of a list of rows and of a result is the digest it
was, because the encoding is.

A3 · **No cell without a value gets in.** No column can say that it may be absent, so `validCell` refuses a cell without a value in
every column, whatever its type, and with it every total without one. The three places that hold values to their columns refuse it
as they refuse a value that is not of its column's type (`INTEGRITY_FAILURE`): the adapter `http` before it passes a page on
(`ValidateSourcePage`), the service for every page of every adapter, a query's and a snapshot job's alike, and both stores for what
a job publishes (`ValidatePublication`). A group's key that holds one is not well formed (`ValidGroupShape`): `NewIssueGroup` and
`VerifyGroup` refuse it (`INVALID_REQUEST`), and neither store admits a job that carries it.

A4 · **The strict decoder has one exception** (`reporting.DecodeStrict`, through which every request body, every answer of a
source, every template and every dashboard pass). It refused a `null` wherever one stood, as it read the document. It now reads the
document first and holds it to the type it is decoded into: a `null` is taken where that type is a `Cell` (a field that is one, an
element of a list of them, a value of a map of them) and refused everywhere else, as before: in place of a text, a number, a
list, a map or an object, inside a list or a map of texts, anywhere inside a value whose type has no shape (an `any`), and at the
root. A cell is a string or `null`; a number there is refused. Whether a cell may be without a value in that place is not the
decoder's question; the validation of A3 answers it. **No type the platform decodes strictly holds a cell in this slice** (A5), so
no document is accepted today that was refused yesterday; the exception is exercised by the tests with a structure of their own and
waits for S26-B.

A5 · **The wire and the requests stay texts until S26-B.** The structures of the source protocol (`protocol.PageResponse.Rows` and
`.Totals`, `protocol.Group.Key`) and the group key of an issue request are lists of texts, as the protocol's revision and the API
describe them: neither has an absent value until a column can declare one, and the revision that brings the declaration brings
`null` with it. Texts and cells meet at five places:

- the adapter `http` takes a page's texts as cells, each with a value, and holds them to the dataset;
- it writes a group's key into an intent as texts, after it has refused a group that is not well formed, so the wire never carries
  an empty text in the place of an absent value;
- the reference source (`sourcetest`) writes a page's cells as texts, and answers a page that holds a cell without a value with an
  integrity failure instead of a page: an adapter behind it that answers one answers what this revision cannot carry;
- the checker (`conformance`) computes with texts (it narrows, orders and sums what it read) and fails a read that holds a cell
  without a value. Through the adapter `http` it can meet none;
- the API takes the texts of a request's group key as cells.

A6 · **Every reader fails closed.** What reads a snapshot's cells to print, measure, add or group them calls `Required()`, and
answers `INTEGRITY_FAILURE` for a cell without a value: a snapshot holds none (A3), so a reader that meets one is reading a store
that does not agree with itself, and it does not print the cell as an empty text or add it as a zero. Each call is a place where
S26-B puts the rule of ADR 0028 decision 1 in its stead. They are:

- the presentation that the workbook and the PDF share (`export/present`): the subtotal column's value as the key of its group;
  every cell that is shown, before it is formatted, marked or summed; every total that is shown;
- the workbook's `Totals` sheet (`export/xlsx`) and the PDF's table without a layout (`export/pdf`): every cell and every total;
- the document templates (`doctemplates`): a 1.x element bound to a row's value or to a total, a 1.x table's cells and totals; a
  2.x template's group key (when the snapshot is partitioned, and when the key is printed), a field of the group, a summed value, a
  table's cell. The issuer and the previews bind through these;
- the document checks' choice of rows and groups (`transport/httpapi`): the cells of a row field's column, of a group field's
  column, of a summed column and of a table's column, which are measured by what they print.

What is not shown is not read: a layout that leaves a column out, a template that does not print it and a group that does not hold
the row never fail on its cell.

A7 · **The stores** have no change of code: their payloads are the JSON of the core's types, and the JSON is what it was. One thing
differs, in a state that no publication can produce: a stored `null` in a row was read as an empty text (that is what
`encoding/json` does with a `null` for a string) and is read as a cell without a value now, which every reader refuses (A6).

## Not in this slice

- A column that declares `nullable`, the protocol's revision, `null` on the wire and in the API, and the structures of A5 as lists
  of cells; the operators `null` and `not_null` and the order of `null` in a sort; what a sum, a subtotal, a mark, a group, a
  workbook, a PDF, a document's field and a code do with an absent value; the console, the embed page, the dashboards and the two
  OpenAPI descriptions; the reference source, both simulators and the checker's rules (S26-B).
- `datetime` and `aggregation` (S26-C), and the documents of the whole ADR (S26-D). docs/API.md, docs/SOURCE_HTTP_PROTOCOL.md and
  docs/REAL_SOURCE_ONBOARDING.md describe what a product and a caller see, which this slice leaves as it was.

## Known

- **What a cell costs.** `encoding/json` asks a `Marshaler` for its bytes, so writing a cell allocates where writing a string did
  not. The largest result the platform holds is 1000 rows; with twenty columns, on the machine this was written on, it is written
  in 1.4 ms where it took 0.3 ms, read in 1.9 ms where it took 1.6 ms, and digested in 1.5 ms (`BenchmarkRowsEncoding`, the
  repository's one benchmark; it is there so that a change of the encoding can be measured against these).
- **Two types are named `Cell`.** `reporting.Cell` is a value of the data. `present.Cell` (S23-A) is what a layout prints for one:
  its text, its exact text, its mark.
- **`Texts` gives the empty text for a cell without a value**, and has one caller outside the tests: the intent's key on the wire,
  behind the refusal of A5. S26-B writes the key as cells and the caller goes.
- **A map whose values hold cells further down** (a map of rows, say) would have a `null` inside it refused: the decoder walks a
  map's values only when they are cells themselves. The platform has no such type.
- **What the type does not stop.** A cell passed as an interface value compiles: to a format verb, an encoder, a comparison by
  reflection. For this slice every such place outside the test files was listed from the type information (a throwaway program
  over `go/types`; it is not in the repository and CI does not run it): the JSON encoder and decoder, `Digest`,
  `reflect.DeepEqual`, and one format verb, the message of a failed scenario in the tests' shared package
  (`contractsim/internal/authoritytest`). Nothing a person or a product is shown formats a cell.
- **Six checks stand behind another**, so no test can see them go. Each was removed in a copy of the tree and the whole suite passed
  without it. One is the decoder's refusal of a number where a cell is (the cell's own `UnmarshalJSON` refuses it next). Five are
  calls of `Required`: the presentation's subtotal key (the subtotal column is always shown, and the shown cell is refused); a
  template's summed value and the document checks' summed value (the empty text is no number, and the sum refuses it); the
  workbook's total (the presentation, built first, refuses it); the reference source's total (the adapter in front of it refuses
  the empty text as the total of a numeric metric, and the simulators have no other). The five stay because each is a place where
  S26-B puts a rule.

## Evidence

- `reporting`: `TestACellIsEncodedAsItsStringWas` (twenty-five texts, among them each character a JSON string escapes by itself,
  the HTML characters in a text that is otherwise plain and in one that is not, the two separators, bytes that are not UTF-8: a
  cell, a row, rows, totals and a key against the same values as strings, under the four settings of an encoder and under
  `json.Marshal`, and read back); `TestTheDigestsOfRowsAreWhatTheyWere` (the digest of a row and of rows against the digest of the
  strings, and against a digest written out by hand; a cell without a value is `null` in a digest and not the empty text);
  `TestACellIsATextOrNoValue` (what a cell answers about itself and how it is formatted; eight JSON values that are no string and
  no `null`, refused alone and in a row; seventeen JSON texts read as a string reads them; seventeen inputs that are no JSON value,
  refused by the method called by itself; the conversions); `TestTheStrictDecoderTakesANullOnlyWhereACellIs` (nine documents
  taken, thirty-two refused, a `null` for a pointer to a cell among them, and a `null` in a query refused where it always was);
  `TestACellWithoutAValueGetsInNowhere` (a page with a cell without a value in each column in turn and in each total; the service's
  query and its snapshot job; what a job publishes; a group's key). The vectors of docs/API.md for the digest of a row and of rows
  pass as they were (`TestRowDigestEncodingAndVectors`, `TestRowsDigestEncodingAndVectors`).
- `adapters/httpsource`: `TestAPageHoldsTextsAndANullInItIsNoAnswer` (five answers refused in the decoder's words, as before this
  slice, nothing passed on; a page that verifies passed on as cells with values), `TestAnIntentWhoseKeyHoldsACellWithoutAValueIsNotSent`
  (no request leaves; the same intent with a value is sent, its key a text), `TestTheReferenceSourceWritesNoCellWithoutAValue`.
- `export/present`, `export/xlsx`, `export/pdf`, `doctemplates`, `transport/httpapi`: one test each that the reader fails closed at
  every place of A6, and does not where the cell is not read (`TestBuildFailsClosedOnACellWithoutAValue`,
  `TestTheWorkbookIsNotWrittenFromACellWithoutAValue`, `TestTheTableIsNotLaidOutFromACellWithoutAValue`,
  `TestATemplateIsNotBoundToACellWithoutAValue`, `TestTheBoundaryPicksFailClosedOnACellWithoutAValue`); the issue request's two
  shapes with a `null` in the key (400 `INVALID_REQUEST`, as before).
- **Nothing that is not Go changed.** The files of test data are untouched: the PDF's golden description, the workbook's, the
  stored fixtures and migration catalogs of `integration/testdata`, the sample pack, the manual's. Every test that existed passes,
  and none was changed in what it asserts: where a test built rows from strings or compared a cell with a string, the strings are
  wrapped (`Cells`, `CellRows`, `CellTotals`, `Text`) and the values are the same.
- Mutations: 45 changes that each break one rule of this slice, made one at a time in a copy of the tree; for each, the test named
  for the rule passes unchanged and fails with the rule broken. The six changes that no test can see are under "Known".
- Without a database: 741 top-level tests in 36 packages, plain and under the race detector. Against PostgreSQL 16: 752, plain and
  under the race detector. The object-store tests were not run locally: the S3-compatible server's image could not be pulled, the
  registry not answering, and they run in CI. This slice changes nothing the object store holds, which is an artifact's bytes. The
  Typst tests run in CI.
