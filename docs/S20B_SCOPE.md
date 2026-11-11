# S20-B · The console's filters and sort: the query panel, the chip, the result's scope (ADR 0023 decision 5)

2026-11-11. ADR 0023 (accepted the same day) lets the catalogue publish, per dataset version, which columns a query may narrow by and
order by; S20-A built the server side (`/catalog` carries `filters` and `sorts`, `/queries` and `/snapshot-jobs` take `filters` and `sort`
and refuse what the entry does not declare). This slice is the console: the query panel grows a 筛选 and a 排序 block built from the
declaration, they are part of the query wherever the query goes, and every place that names a result says when its scope is narrowed.
The server is untouched (Go: only `console/handler.go` serving the new script, and its two route tests). A query without filters is
exactly what it was.

B1 · **The blocks** (`console/assets/filters.js`, `window.createReportingFilters(ports)`; markup in `index.html` and `embed.html`,
`#query-narrow`, between the panel's meta line and its actions). `app.js` tells the script which dataset is on show (`show`), and the
script builds from that entry's `filters` and `sorts` and from nothing else. A dataset that declares neither shows no block; one that
declares only sorts shows 排序 alone (ADR 0014 decision 4: a control that can never work is not shown). Nothing is chosen for the
person: under each title a line 添加筛选 / 添加排序 offers the declared fields as buttons, each the column's **label** with the id in
its title and its name (`添加筛选：账龄区间`, `添加筛选：账龄区间（aging.bucket）`). The rows start over whenever the dataset on show
changes: another dataset, another product, 刷新目录, sign-out. Tests: DOM S20-B "the query panel's 筛选 and 排序 blocks are built from
the dataset's declaration…" (the real demo's `receivables.aging`: the two groups and their names, the three fields and two sorts
offered by label, each row's parts, focus, 移除, no inline style), "the blocks are the dataset's…" (a dataset that declares nothing,
one that declares sorts only, back and forth, the second product's `shipments.daily` by its own labels, sign-out and a new session).

B2 · **A filter row** is the field (its label, the id as secondary text: ADR 0014 decision 7), the operator and the value. The operator
is a word — 等于 不等于 小于 不大于 大于 不小于 属于 介于 开头为 包含 for `eq ne lt le gt ge in between prefix contains` — and only
the operators the declaration gives the field are offered, as radios in the declaration's order, its first chosen; a field with one
operator shows the word alone. The value follows the column's type and the operator:

| Declaration and operator | Control | On the wire (`value`) |
|---|---|---|
| closed `values`, 等于 / 不等于 | a select of the values, 请选择… first | the value |
| closed `values`, 属于 | a checkbox per value | a JSON array of the ticked values, in the declaration's order |
| `boolean` | a select 是 / 否 | `true` / `false` |
| `date` | a date field | `YYYY-MM-DD` |
| `integer`, `decimal` | a text field (`inputmode=decimal`), its placeholder the column's form (`0`, `0.00`) | the string as typed: never parsed into a float |
| `string` | a text field | the string, without the spaces around it |
| 属于 without closed values | a text area, one value a line | a JSON array of the lines (trimmed, empty ones dropped, a repeated one once) |
| 介于 | two of the type's fields, 至 between them | a JSON array of the two |

A field has at most one row: 添加筛选 offers the fields that have none, so a query has at most as many filters as the dataset
declares, which is at most sixteen, the server's bound of one query. A `required` field's row is there from the start, is marked 必填
and has no 移除. An operator that takes the same control keeps what was typed (等于 → 不等于); 等于 → 介于 keeps it as the first of the
two. 移除 gives the field back to 添加筛选 and the focus with it; a row just added takes the focus on its value. Tests: DOM S20-B "the
blocks are built…", "the value control follows the column's type and the operator…" (a catalogue that declares a filter on a column of
every type: the ten words, a date, an integer, a decimal of four places, a boolean, a string; every control and its wire form), "a
required filter's row…", "a declaration that breaks…" (sixteen rows).

B3 · **The sort block**: up to three keys in the order they apply, each a declared field once, by its label, with 升序 / 降序 as the
declaration gives them (one direction: the word alone). 添加排序 offers the fields that are not keys yet and goes at three; the block's
standing line says 最多 3 个排序键，按先后顺序生效. Each key shows its place (1, 2, 3); 上移 moves a key before the one above it, 移除
takes it away. Tests: DOM S20-B "the 排序 block takes up to three keys…" (the real demo: one key each way, two keys, 上移, with a
filter; a catalogue with four sorts: the bound of three, a one-directional field, the declaration's order).

B4 · **They are part of the query.** `buildQuery` puts the two lists into the query exactly as the server takes them (docs/API.md
"Filters and sort"): `filters: [{field, operator, value}]` in the rows' order, the values of 属于 and 介于 as a JSON array of strings
in `value`; `sort: [{field, direction}]` in the keys' order. 查询预览 (`POST /queries`) and 创建快照 (`POST /snapshot-jobs`) send that
query; what is kept as `state.query` is the query that was sent, so 设为定时 (`POST /schedules`), the task's remembered query and every
other reader of `state.query` carry the lists unchanged. A change of a filter or of a sort key is a change of the query, as a change of
the companies is: the result on show goes. The exact bodies of the demo:

- 客户 等于 customer_a → `"filters":[{"field":"customer.id","operator":"eq","value":"customer_a"}],"sort":[]`
- 账龄区间 属于 1_30、31_60 → `"filters":[{"field":"aging.bucket","operator":"in","value":"[\"1_30\",\"31_60\"]"}]`
- 应收余额 介于 -1.00 至 9007199254740892.99 → `{"field":"ar.open_balance","operator":"between","value":"[\"-1.00\",\"9007199254740892.99\"]"}`
- 应收余额 降序, then 客户 升序 → `"sort":[{"field":"ar.open_balance","direction":"desc"},{"field":"customer.id","direction":"asc"}]`

Tests: DOM S20-B "等于, 属于 and 介于 go to the server in its own form and the rows and totals come back narrowed…" (the real server's
rows and totals for each: 等于 one row and 100.25; 属于 the two buckets and 9007199254740892.76; 介于 with a bound one cent under
customer_c's balance keeps it out, which a float could not tell; a conjunction of two; the snapshot job asked with the same lists, its
frozen page and its workbook), "the 排序 block…" (the rows in the order asked), "a snapshot restored from the tasks…" (the schedule's
body).

B5 · **What the console can see is wrong is said before the query is sent** (ADR 0014 decision 6: a disabled action says why). A filter
without a value (a required one says 必填), 介于 without both values or with the first greater than the second, more than 32 values for
属于, a number that is not in the column's form (an integer; a decimal with exactly the column's scale), a date that is not one, a text
over 128 characters or holding a control character: 查询预览 and 创建快照 are disabled, the reason is their `title` and the line beside
them (`#query-hint`, `aria-live`, the buttons' `aria-describedby`), and nothing is asked of the server. The first row that is wrong is
the one said. The data drawer's first focus goes to that row's value once a company is chosen (S07-A, S09-D: what the person has to do
next). The rules are the server's own (`reporting/filters.go` `ValidFilterValue`, `CompareValues`): characters are counted as
characters, numbers are compared by their digits (never through a float), text by code point, so that nothing the server would accept
is held back here. Whoever gets past the disabled button gets the same refusal on the page (`buildQuery`). The console validates to
help, never to authorize. Tests: DOM S20-B "the value control follows…" (every reason, on the buttons and beside them, nothing sent;
the accepted forms next to the refused ones, `-0`, 128 digits, 128 emoji; the forced click), "a required filter's row…", "a
declaration that breaks…" (thirty-three of sixty-four closed values).

B6 · **The server's refusal is the truth.** What the console cannot see — a catalogue on the page that is older than the source's, a
value only the source can judge — is the server's 422 `UNSUPPORTED_COMBINATION`, told in the panel's notice as its other refusals are.
When the refused query carried filters or a sort the notice names them (服务端不接受这组筛选或排序：字段、运算符、取值或排序方向不在
数据集当前发布的范围内。请核对筛选值，或刷新目录后重试。); a 422 of a query without them stays 当前数据集不支持此查询组合。 The rows
stay on the panel to be corrected; no result, no job. Tests: DOM S20-B "what the catalogue on the page declares and the server does
not…" (the catalogue intercepted to declare 包含 on the customer and a sort by the aging bucket; the real server refuses the preview,
the snapshot at admission and the sort; what it declares too is answered).

B7 · **The scope in words, wherever a result is named** (ADR 0023 decisions 3 and 5, Consequences: a narrowed total must not be read
as the whole). `dataSummary()` adds the narrowing to the chip's text after the date — `应收余额 · 演示公司 · 2026-11-11 · 2 个筛选 ·
按应收余额降序`, a second key as `、再按客户升序` — so the header's chip and its title, the template centre's chip, the issue page's
data line and its confirmation say it; the schedule dialog's 数据 line and the schedules list say it the same way. On the result:
the total's card reads 筛选后范围合计 where it read 原范围合计; the subtitle ends `· 已按 2 个筛选缩小范围，合计是筛选后的范围`; the
evidence line begins with 范围已缩小：行与合计只含符合全部筛选的数据, one `筛选：账龄区间 属于 1_30、31_60` for each filter and
`排序：按应收余额降序`, before the query digest that binds them. A sort alone narrows nothing: the chip and the evidence line name the
order, the card stays 原范围合计. **A restored snapshot** (查看结果 from the tasks, a schedule's run) is rendered from the query it was
frozen with, so it shows that query's filters and sort in the same places, read-only; the panel is not touched — it is the next
query's, as its companies and date are. A snapshot shown without its query (restored by id, or listed by the server in a later
session: the job view carries no query) says so instead of guessing: `筛选与排序以原快照为准` in the subtitle and
筛选与排序：本会话没有这个快照的查询，以原快照为准 in the evidence line. Fields are named by the labels of the result's own columns
(a schedule's by the product's catalogue), by id where there is none. Tests: DOM S20-B "等于, 属于 and 介于…", "the 排序 block…", "a
snapshot restored from the tasks shows the filters and the sort it was frozen with…" (the restore, 设为定时, the schedules list, the
later session).

B8 · **The declaration is verified.** `filters.check` holds a dataset's declaration to ADR 0023 decision 1 as the server's
`validDeclaration` does (at most 16 filters and 8 sorts, a column of the dataset at most once in each, operators a non-empty set of the
ten that fit the column's type, at most 64 distinct closed values well formed for the column, `required` a boolean, directions a
non-empty set of `asc` `desc`), and `validDataset` counts it: a declaration that breaks a rule is a catalogue the console does not
build on, as a malformed column is (服务返回了无法验证的响应。, no dataset offered, no block). Tests: DOM S20-B "a declaration that
breaks ADR 0023's rules…" (28 shapes; the inclusive bounds verify: sixteen filters, sixty-four values of 128 characters, eight sorts).

B9 · **The embed page** (ADR 0006). `embed.html` loads `filters.js` before `app.js` as `index.html` does, and the view sections stay
byte for byte the console's, so the embed's query panel has the same blocks, the same wire form and the same words; in a dashboard or
designer view they sit with the panel above the view. Nothing of a filter goes to the parent page. Tests: embed DOM "S20-B: the embed
page's query panel filters and sorts as the console's does…"; the embed's own "byte for byte" test holds the sections and now reads
`filters.js` for markup writes, storage and messages; the route tests list `/console/filters.js` (`handler_test.go`, `embed_test.go`).

B10 · **English** (S17-A). The 73 new texts have their entries at the end of `en.js` (one stale entry, the panel's old line 未开放
自定义筛选、排序或公式, is gone with its text); `scripts/i18n_extract.mjs` reads `filters.js`. The sentences are built so that the
translator finds them: each operator's sentence, each reason and each sort key is one pattern (`{0} 属于 {1}`, `按{0}降序`,
`再按{0}升序`), and the pieces of the chip are joined with ` · ` as before. The English walk adds a filter that waits for its value,
one from the closed list, a number not in the column's form, 介于 out of order and two sort keys, and runs its preview and snapshot
narrowed (the three filters keep the dataset's three rows), so every page it looks at after shows the scope in English.
`scripts/browser_english_s17a.mjs` does the same in the real browser. Tests: `node --test console/i18n.test.mjs`.

B11 · **Measurement** (ADR 0023 decision 5, ADR 0011 decision 7). `scripts/ux_baseline_s07.mjs` gained the ninth core task, **T9
筛选后冻结并导出**: from wherever T8 left the person, the header's data chip, 添加筛选 账龄区间, 属于, 1_30, 添加排序 应收余额, 降序,
创建快照, then the workbench's 导出 Excel. On main the task is not possible — the query panel has no filter to click — and the script
says so before it clicks anything; on this branch it is **9 clicks, 0 texts, 1 page switch, 0 shell switches, 1 dialog (the data
drawer), 0 dead ends**. T1–T8 are the same on both runs (35 clicks, 10 page switches), so the held totals of
`scripts/ci_browser_evidence.sh` grow by exactly T9's own: **44 / 11 / 0 / 0** (they were 35 / 10 / 0 / 0). Evidence:
`evidence/s07a/baseline-before-s20b.json` (main 75e3e29 with this branch's script), `evidence/s07a/baseline-after-s20b.json`;
docs/UX_BASELINE_S07.md "T9：筛选后冻结并导出".

B12 · **The real browser.** `scripts/browser_console_s06c.mjs` checks, at 1440: what the panel offers, a filter that waits for its
value (the disabled actions and their reason), a number not in the column's form, two filter rows (a closed list, a typed number) and
a sort key inside the panel, legibility, overflow and contrast in the light and the dark scheme, the narrowed preview (rows, total,
card, chip, subtitle, evidence line) and 移除; at 390, in the data drawer: two filter rows and a sort key stacked inside the drawer,
nothing scrolling sideways, legibility, overflow and contrast in both schemes. Screenshots `console-03c-filter-waiting.png`,
`console-03d-filters-and-sort.png`, `console-1440-dark-filters-and-sort.png`, `console-03e-narrowed-preview.png`,
`console-390-filters-and-sort.png`, `console-390-dark-filters-and-sort.png`. Evidence: the CI job locally
(`scripts/ci_browser_evidence.sh`): console, library, baseline, baseline-team and english pass.

B13 · **Docs.** This page; docs/CONSOLE.md (the S20-B note; the paragraph on published combinations); ADR 0023's implementation line;
docs/ADR_0017_ENTERPRISE_ROADMAP.md (the Progress line); docs/UX_BASELINE_S07.md (T9); docs/STUDIO.md (the chip's text and the data
drawer's focus); the manual's words (docs/manual/index.html: the quick start, the data drawer's and the workbench's captions, the
workbench's control table, case C06). The manual's screenshots were **not** retaken: see "The manual's figures" below.

## Interpretations of ADR 0023 made here

- **A field has one row.** The server takes several filters on one field (two bounds, two 不等于); the console offers a field once, as
  the slice was specified ("a row for a field that has none yet"). 介于 covers a closed range; an open range on both sides, or the
  exclusion of two values, needs a declaration that publishes it another way, or a later slice.
- **添加筛选 is the list of the fields that can be added**, one button a field, rather than one button and a field select in the row:
  the declaration is on the page (a person sees what can be narrowed by), a click less a filter, and a row's field never changes.
  添加排序 the same.
- **Operators and directions are radios, not a select**: the declared ones are all in sight, one click each, and a field with one
  operator shows the word and no control.
- **Nothing is chosen for the person**: a new row starts on the declaration's first operator with no value, and waits; a select of
  closed values starts on 请选择…. A filter the person did not fill is never sent, and never silently dropped either: the query waits
  and says which filter.
- **The spaces around a typed value are not part of it** (as around a typed company id); a list's empty lines are dropped and a
  repeated value counts once. Numbers and dates are otherwise sent exactly as typed and are never reformatted: `100` for a column of
  two places is refused on the page with the form to use, not turned into `100.00`.
- **The number's form is checked on the page.** The ADR names a missing value, 介于 out of order and a list too long; a number the
  server would refuse for its form (422, without a word about which filter) is the same kind of thing the console can see, so it says
  it. The check is the server's rule, digit for digit.
- **The total's card says 筛选后范围合计** for a narrowed result. The ADR names the subtitle and the evidence line; the card is where
  the total itself is read, and 原范围合计 beside "the scope is narrowed" would contradict it.
- **A snapshot without its query says so.** The ADR's "a snapshot made with filters says so wherever a snapshot is named" can only be
  kept where the console has the query; the job view and the rows of a snapshot carry none (S20-A did not add it), so a snapshot
  restored in a later session says that its filters and sort are its own and not known here. Carrying the frozen query on the
  snapshot's rows would close this; it is server work.
- **A restored snapshot's filters are shown with the result, not put back into the panel**: the panel stays the next query's, as it
  does for the restored snapshot's companies and date.
- **A declaration that does not verify refuses the catalogue**, as a malformed column does, rather than showing the dataset without
  its blocks: a `required` filter the console could not show would make every query fail without a way to satisfy it.
- **The 422 of a narrowed query is told as the filters' or the sort's.** The notice is the panel's own, as for every refusal; its words
  name what the person can correct.
- **The dashboard's metric card is untouched** ("nothing of the dashboards changes"): its line still reads 服务端原范围合计, and the
  header's chip above it says the scope. ADR 0023's Consequences name the card; that text is left to the slice that takes the
  dashboards.
- **T9 starts where T8 ends** (the issue page), opens the data drawer as the ADR describes and then goes to the workbench for the
  export: 9 clicks and 1 page switch. From the workbench itself the panel is on the page and the task would be 8 clicks and no dialog.

## The manual's figures

Not retaken (`scripts/manual_screens.sh`). The figures that show the query panel now have the 筛选 and 排序 blocks under the meta line
and the new sentence in the actions row: `02-home`, `03-data-fields`, `05-data-drawer`, `07-workbench-preview`,
`08-workbench-snapshot`, `10-refused-company`, `11-shipment-preview`, `34-dark-workbench`, `36-phone-workbench`. No figure shows a
filter row, a sort key or a narrowed result yet: a new figure of the data drawer with 账龄区间 属于 1_30 and 应收余额 降序, and one
of the narrowed result (the chip, 筛选后范围合计, the evidence line), would illustrate the workbench's new rows of the control table.

## Known

- The demo's datasets have no date or boolean column and no required filter: those controls are exercised by the DOM tests through
  an intercepted catalogue (the wire form checked against what the console sends), not against the real source.
- A query at the very bounds (16 属于 filters of 32 long values) does not fit the server's 64 KiB body (S20-A "Known"); the console
  does not measure the body, and such a query is the server's 413.
- The exports say nothing of the scope yet (S20-C): the workbook of a narrowed snapshot holds the narrowed rows and totals, and its
  Metadata sheet does not name the filters.
