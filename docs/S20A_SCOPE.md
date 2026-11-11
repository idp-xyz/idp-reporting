# S20-A · Published filters and sorts: the catalogue declares them, the core validates them, the source applies them

2026-11-11. The functional review of that day named the largest gap: every report is exactly the dataset's one published shape, and a
person who wants the customers over sixty days or the largest balances first freezes the whole dataset, exports it and filters in Excel,
outside the source's authorization, the frozen snapshot and the evidence. ADR 0023 decides how the catalogue may publish, per dataset
version, which columns a query may narrow by and order by, and with which operators; this slice is the server side of it: the core, the
source protocol, the adapters, the API, the conformance checker. The console is S20-B, the exports' metadata S20-C. A query without filters
behaves exactly as before.

## What

A1 · **The declaration** (`reporting.Dataset.Filters []FilterSpec`, `Sorts []SortSpec`; JSON `filters` and `sorts`, omitted when empty). A
filter spec is `{field, operators, values?, required?}`: a column of the dataset, a non-empty set of `eq ne lt le gt ge in between prefix
contains` that fit the column's type (`prefix`/`contains` for strings only; `lt le gt ge between in` for dates, integers, decimals and
strings; a boolean takes `eq`/`ne`), an optional closed list of 1..64 values the field accepts, and whether a query must filter on it
exactly once. A sort spec is `{field, directions}`, directions a non-empty set of `asc desc`. At most 16 filters and 8 sorts, a field at
most once in each. `ValidateQuery` holds the declaration to these rules as it holds the columns: a malformed one is `INTEGRITY_FAILURE` and
the dataset is not run. The lists are part of the version; a version without them is as today.

A2 · **The query** (`reporting/filters.go`). `Normalize` sorts the filters by field, operator, value and keeps the sort keys in their order;
`ValidateQuery` accepts at most 16 filters and 3 sort keys, each filter on a declared field with a declared operator, every value well
formed for the column (a date `YYYY-MM-DD`; an integer or a decimal as the column writes it, at most 128 characters; `true`/`false`; a
string of 1..128 characters without control characters), from the closed list for `eq`/`ne`/`in` when there is one, `in` and `between`
carrying their values in `Value` as a JSON array of strings as text (1..32 for `in`, exactly 2 in ascending order for `between`), a
required field filtered exactly once, every sort key on a declared field in a declared direction and no field twice. Anything else is
`UNSUPPORTED_COMBINATION` (422), as any combination the catalogue does not publish, before the source is asked. `QueryHash` is unchanged in
form and already covers both lists: the unfiltered demo query hashes to what it hashed before (`57ef45fc…3d25`, the protocol example's).
Exported for the adapters, the checker and the console: `FilterValues` (the one decoding of an `in`/`between` value), `ValidFilterValue`,
`CompareValues` (exact for integers and decimals through `math/big`, textual otherwise), `OperatorFits`, `FilterOperators`,
`SortDirections` and the bounds as constants.

A3 · **The contract simulators** (`adapters/contractsim`) declare, for `receivables.aging`: `customer.id` eq/ne/in/prefix; `aging.bucket`
eq/in from the closed list `current 1_30 31_60 61_90 90+` (`contractsim.AgingBuckets`); `ar.open_balance` lt/le/gt/ge/between; sorts
`ar.open_balance` asc/desc and `customer.id` asc/desc. For `shipments.daily`: `shipment.destination_country` eq/in; `shipment.service_code`
eq; `shipment.count` ge/le; sorts `shipment.count` asc/desc and `shipment.destination_country` asc/desc. `ReadFrozenPage` applies the
frozen query's filters (a conjunction, values compared as data by the column's type) and sort (stable, so the order is the same on every
page) to the authorized rows and totals the narrowed rows (`narrow.go`); `PrepareRead` checks the query against the declaration as the
platform does and refuses the rest `UNSUPPORTED_COMBINATION`. The grants and the authorization are untouched. The durable fixture
(`pgsource`) stores the dataset with its declaration and reads through the same `ReadFrozenPage`, so it behaves the same without a change
of its own.

A4 · **The protocol** (docs/SOURCE_HTTP_PROTOCOL.md revision 1.1, the same revision as S19-B's optional `companies` route: one revision,
both additions, every field optional; docs/source-openapi.json 1.1 with `FilterSpec` and `SortSpec` schemas, held to the Go types by
`TestTheOpenAPIDescriptionIsTheProtocol`). `catalog` carries `filters` and `sorts`; `reads` carries the
query's filters and sort as they are; the source must check them against what it published, bind every value as a parameter and never as
text of a statement, answer `unsupported` for an operator it does not know, keep a sorted read stable across pages and total the narrowed
scope. The bounds table gains the declaration's and the query's bounds; section 8 no longer lists filters as absent. The adapter
`adapters/httpsource` verifies the catalog's declaration with the core's rules (a malformed one is an integrity failure like a malformed
column) and sends the query as it is; nothing else of it changes. `sourcetest` serves the wrapped adapter's declaration and refusals.

A5 · **The checker** (`adapters/httpsource/conformance`, `cmd/reporting-source-check`): six rules after R13, before S19-B's D1–D2 and I1
(28 rules in all). F1: for every declared filter
and each of its operators, a filter made from the whole read's own values (the closed list's when there is one) narrows the rows to those
that satisfy it, every row is a row of the whole scope and the totals are the sum of the narrowed rows for every metric whose whole total
was its rows' sum; F2: for every declared sort and direction the rows come back in that order, the same rows and totals, stable across
pages; F3–F5: a filter on an undeclared field, with an undeclared operator (every undeclared one of the set and one outside it) or with
a malformed value is refused `unsupported` or `invalid`; F6: an undeclared sort is refused. A dataset that declares nothing skips F1, F2,
F4 and F5 and still passes F3 and F6. The reference source passes 28 of 28 with `-issue -companies`; a source that ignores filters and sort
fails F1–F6.

A6 · **The API**: `GET /catalog` carries the lists; `POST /queries` and `POST /snapshot-jobs` accept filters and sort and answer 422
`UNSUPPORTED_COMBINATION` for what the entry does not declare; a filter object outside the query's shape is 400 as any unknown field.
docs/openapi.json (QuerySpec `filters`/`sort`, Dataset `filters`/`sorts`, `FilterSpec`, `SortSpec`, the three routes' descriptions),
docs/API.md, docs/s00/INTERFACES.md (the catalogue row), docs/REAL_SOURCE_ONBOARDING.md (数据目录), docs/CONSOLE.md, ADR 0017's table.

A7 · Not done here: the console's filters and sort blocks, the chip, the subtitles and the evidence line (S20-B); the exports' metadata
(S20-C); everything ADR 0023 decision 7 leaves undecided (expressions, grouping, joins, free text, OR, a source-side default sort).

## Evidence

- `reporting`: the declaration's every rule (27 malformed declarations are `INTEGRITY_FAILURE`, the bounds 16/8/64/128 are accepted); every
  operator and type pair with well-formed and malformed values (29 accepted, 43 refused); the query's bounds, undeclared fields,
  operators and directions, closed values, a required filter missing, present and doubled, a sort field twice; `Normalize`'s order and the
  caller's slice left alone; the hash unchanged for the unfiltered demo queries (literal values), different for a filter or a sort, the same
  for a nil and an empty list, independent of the filters' order and dependent on the sort keys' order, without the page; `FilterValues`
  on 14 malformed texts and the byte bound; `CompareValues` on exact decimals beyond 2^53; the service running a filtered, sorted query
  and snapshot through the simulator with the narrowed totals and the hash, and refusing four undeclared queries without a read
  (`TestTheServiceRunsAFilteredAndSortedQuery`).
- `contractsim`: 15 filters over the receivables fixture with their rows and totals (an exact decimal bound a float would miss), a
  conjunction, two filters of the shipments fixture with an integer beyond 2^53; four sorts; a 120-row fixture sorted descending with ties,
  stable, the same read in pages of 7 as whole; eight refusals at `PrepareRead`, a frozen undeclared query refused at projection, a
  version without a declaration refusing a sort and showing no lists, the grant unchanged.
- `httpsource`: the catalog's lists over the protocol equal the simulator's; a filtered, sorted read's query goes over the wire as it is and
  pages come back narrowed and ordered, one row a page the same; the plain query's hash is an integrity failure for the narrowed query;
  four undeclared queries are `unsupported` through the protocol with no leak; 11 tampered declarations are integrity failures (a column
  that is not, a field twice, an operator outside the set, `prefix` on a decimal, no operator, 17 filters, 65 values, a control
  character, a direction that is not, a sort on a column that is not, an unknown key).
- `cmd/reporting-source-check`: the reference source passes every rule (28 with `-issue -companies`, F1–F6 included, in the order P, A, C,
  R, F, D, I), a dataset without a declaration skips F1, F2, F4 and F5 and passes F3 and F6, a product without the directory still passes
  28 without the flags, a source that ignores filters and sort fails F1–F6 with the fixed texts.
- `transport/httpapi`: `GET /catalog` carries the two lists in the JSON the console will read and none for a dataset without a declaration;
  `POST /queries` and `POST /snapshot-jobs` run a filtered, sorted query (rows, totals, hash, the frozen snapshot's rows), refuse 12
  undeclared or malformed combinations with 422 `{"error":"UNSUPPORTED_COMBINATION"}` on both routes, and refuse 4 filter objects outside
  the shape with 400.
- Unchanged: every existing test of the repository, the console's DOM and embed suites (they send empty lists).

## Known

- A durable demo database seeded before this slice keeps the fixture version it has (`EnsureFixture` never updates one): its catalogue shows
  no lists until the fixture is replaced or the database initialized again. The in-memory demo shows them at once.
- `POST /queries` and `POST /snapshot-jobs` keep their 64 KiB body bound: a query at the very bounds (16 `in` filters of 32 values of 128
  CJK characters) does not fit; a realistic one does by far.
- The simulators' declarations are the demo's and the tests' only filters until a real product declares its own (ADR 0023 Consequences).
