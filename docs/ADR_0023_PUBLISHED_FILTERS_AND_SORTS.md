# ADR 0023 · The catalogue says what a query may narrow and order: published filters and sorts

Date: 2026-11-11. Status: accepted, by the decision the product owner delegated to engineering on 2026-11-11 ("按你的建议全面开始吧",
after the functional review of that day named this the largest gap), and ratified by the owner that evening, when it was put to the
owner again as a contract-level decision (it changes the adapter contract and the source protocol; CODING_STANDARDS.md). It amends one sentence of docs/s00/INTERFACES.md's catalogue row and
docs/CONSOLE.md's "当前只有已发布查询组合，自定义筛选、排序、SQL、公式不开放"; it keeps every other rule of the baseline. Implementation:
S20-A (the core, the protocol, the adapters, the API, the checker; docs/S20A_SCOPE.md, S20-A: this pull request), S20-B (the console),
S20-C (the exports say the scope, decision 6: built, docs/S20C_SCOPE.md).

## Context

A query names a published dataset version, the companies and the as-of date; `QuerySpec` has carried `filters` and `sort` since the S00
examples (`reporting/types.go`), but the core refuses any value in them (`reporting/validation.go`: `len(q.Filters) != 0 || len(q.Sort) != 0`
is `ErrUnsupported`), the catalogue has no way to declare which would be allowed, and the source protocol says "`filters` and `sort` empty in
version 1" (docs/SOURCE_HTTP_PROTOCOL.md "reads"). So every report is exactly the dataset's one published shape: a person who wants the
customers over sixty days, or the largest balances first, freezes the whole dataset, exports it and does the rest in Excel — outside the
source's authorization of what was asked, outside the frozen snapshot, outside the evidence. The functional review of 2026-11-11 called the
console a dataset viewer with an exporter for this reason, and put it first among the gaps.

The baseline's rule is right and stays: "不能暴露任意 SQL" (docs/s00/INTERFACES.md), "通用核心不得硬编码 … 任何产品业务字段", the source
authorizes every read against the current rights, and "原范围冻结" — the frozen snapshot is exactly what was asked, and the evidence names it.
Nothing in that rule forbids a *published* filter: the source already publishes which datasets, columns, metrics and dimensions a principal
may query; it can publish, in the same catalogue, which columns a query may narrow by and order by, and with which operators. The source
applies them in its own trusted transaction, as it applies the company scope today; the platform validates the request against the
declaration and never evaluates a filter itself, so the totals stay the source's and agree with the rows.

What a person cannot do today, in the words of the review: filter, sort, choose the grouping, drill, compute, join. This ADR gives the first
two, because they are the ones the baseline's authorization model admits without a new principle. Grouping choices, computed fields and
joins would each be a new catalogue concept and are not decided here.

## Decision

1. **The catalogue declares filters and sorts per dataset version.** `Dataset` gains two optional lists:
   - `filters: [{field, operators, values?, required?}]` — `field` is a column id of the dataset; `operators` a non-empty subset of the
     closed set `eq ne lt le gt ge in between prefix contains`; `values` an optional closed list of the values the field accepts (at most
     64, each 1..128 characters), in which case `eq`/`ne`/`in` must take one of them; `required: true` means a query must carry exactly one
     filter on this field (a mandatory parameter, such as a period).
   - `sorts: [{field, directions}]` — `directions` a non-empty subset of `asc desc`.
   At most 16 declared filters and 8 declared sorts; a field at most once in each. `prefix` and `contains` apply only to `string` columns;
   `lt le gt ge between` only to `date`, `integer`, `decimal` and `string`; `boolean` takes `eq`/`ne`. A dataset version without the lists
   is as today: no filter, no sort. The lists are part of the version: a change is a new version, as for columns.
2. **A query's filters and sort are validated against the declaration, fail closed.** `Normalize` sorts a query's filters by field, operator,
   value and leaves the sort keys in their order (order is meaning); `Validate` (against the dataset) accepts at most 16 filters and 3 sort
   keys, each on a declared field with a declared operator or direction, no field twice in the sort, every value well formed for the column's
   type (`date` as `YYYY-MM-DD`; `integer` and `decimal` as the exact strings the columns use, at most 128 characters; `boolean` as `true`
   or `false`; `string` 1..128 characters without control characters). `in` and `between` carry their values in `Value` as a JSON array of
   strings (`["a","b"]`: 1..32 values for `in`, exactly 2 in ascending order for `between`, each bounded as above), so that `Filter` keeps
   its three string fields and every stored query and hash keeps its form. A required filter that is missing, an undeclared field or
   operator, a malformed value: `ErrUnsupported` (422 `UNSUPPORTED_COMBINATION`), as any other combination the catalogue does not publish.
   The platform never evaluates a filter or a sort: it has no rows to do it on, and the source's totals must be the filtered scope's.
3. **The query hash covers them.** `QueryHash` is already the digest of the normalized query without `page`; filters and sort are in it. A
   frozen snapshot's query, its `query_hash`, the evidence the result carries and the source's `PrepareRead` check of the hash therefore
   bind the narrowed scope exactly as they bind the companies and the date today. A snapshot made with filters says so wherever a snapshot
   is named (S20-B, S20-C).
4. **The source applies them, as parameters.** `PrepareRead` receives the query with its filters and sort, checks them against what it
   published (the platform's validation is not a substitute: a source refuses what it did not publish with `unsupported`), applies them in
   its own trusted transaction and returns a read whose rows and totals are the narrowed scope's, in the sort order asked, stable across
   pages (the baseline's "分页如何维持同一事实" holds for a sorted read as for any). The protocol (docs/SOURCE_HTTP_PROTOCOL.md, a minor
   revision of version 1) carries the declaration in `catalog` and the values in `reads` as they are in the query; it says that a source
   must bind filter values as data (parameters), never as text of a statement, and that an unknown operator is `unsupported`. The contract
   simulator declares filters and sorts for its two datasets and applies them in memory; `sourcetest` does the same; the conformance checker
   verifies, for every declared filter and sort, that the rows obey them and that an undeclared field, operator or value is refused.
5. **The console (S20-B).** The query panel in the data drawer grows a filters block built from the declaration (a row per filter: the
   field by its label, the operator by a word, the value by the column's type — a date field, a number, a list for closed values, a text —
   at most 16 rows, `required` rows present from the start), and a sort block (up to three keys, field by label and 升序 / 降序). They
   are part of the query: the preview and the snapshot carry them, the data chip says `· 2 个筛选 · 按应收余额降序`, the result's subtitle
   and the evidence line say the scope is narrowed, and the export buttons stay what they are. Nothing of the layout designer, the
   dashboards or the templates changes: they bind to a dataset and show the current result, as today. A new baseline task T9 (筛选后冻结
   → 导出) is measured on main before and on the slice after (ADR 0011 decision 7).
6. **The exports and the documents say the scope (S20-C).** The XLSX `Metadata` sheet and the PDF table export's footer carry the filters and
   the sort in words (field label, operator, value); a document template's `PREVIEW` and issued PDF are per row or group and do not change,
   but the issue record keeps the snapshot id, whose query already holds the filters.
7. **Not decided here.** Client expressions or computed fields; a choice of grouping (dimensions stay the published set); joins across
   datasets; free-text search across columns; OR between filters (a query's filters are a conjunction); filters on columns the source did not
   declare; a source-side default sort other than what the source returns today. Each would be its own ADR.

## Consequences

The contract change is additive: a catalogue entry without the lists means what it meant, an existing adapter is unchanged, the protocol's
version 1 gains optional fields (products implementing it today pass the checker as before, and gain nothing until they declare). The query
shape, its hash and the stored snapshots are unchanged in form, so no migration. Every bound of the request is stated (counts, lengths, the
operator set), and the values reach a source as strings it must bind as parameters, which the protocol document and the onboarding guide now
say; the platform's validation rejects anything outside the declaration before a source sees it.

A narrowed snapshot's totals are the narrowed scope's: the console, the exports and the dashboard's metric card (a server total) must say
so, or a person reads a filtered total as the whole; S20-B and S20-C carry that text. Sorting moves to the source, which must keep the order
stable across pages of a read; the checker tests it. The console's data drawer grows; T1 and T7 do not change (no filter is required in the
demo's datasets), and T9's own clicks join the held totals.

The simulators' declarations are the demo's and the tests' only filters until a real product declares its own: what a real product can
narrow by is the product's decision, and docs/REAL_SOURCE_ONBOARDING.md's 数据目录 row asks it.

## Slices

- **S20-A** — the catalogue's declaration, normalization and validation in the core, the hash, the protocol revision, the adapters (`contractsim`,
  `httpsource`, `sourcetest`), the conformance checker, the API (`/catalog` carries the lists; `/queries` and `/snapshot-jobs` accept the
  filters), the OpenAPI descriptions and their tests. No console change; a query without filters behaves exactly as before.
- **S20-B** — the console's filters and sort blocks, the chip, the subtitles, the evidence line; the DOM tests; T9 in the baseline.
- **S20-C** — the exports' metadata and footer. Built (docs/S20C_SCOPE.md): the workbook's `Metadata` sheet ends with the count of the
  query's filters and sort keys and a row for each; the PDF table export says them in words under the meta line of its first page, within
  a stated bound, and counts them beside the snapshot's id in the footer of every page; an export of a query without them is what it was.
