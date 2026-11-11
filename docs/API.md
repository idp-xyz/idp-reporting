# API · S03-A console composition over the existing protected source profile

Base: `/api/reporting/v1`. The machine-readable description of every route is
`docs/openapi.json` (OpenAPI 3.1, S13-A, docs/S13A_SCOPE.md): the methods, paths,
parameters, bodies, statuses and the JSON schemas of the main shapes.
`TestOpenAPIMatchesTheBoundRoutes` (transport/httpapi/openapi_test.go) and
`TestOpenAPIMatchesTheHealthRoutes` (server/openapi_test.go) fail when a bound
route is not in it or it documents a route that is not bound. This document is
the prose reference; it is not an OIDC profile or a designer API. Bearer
authentication is a replaceable server-side port. `DemoBearer` is explicit synthetic identity only.
S02-A adds identity/accessjwt at that same port; see ACCESS_JWT.md. Provisioning
it does not change the simulator data classification or qualify a real issuer.

| Method and path | Request / response |
|---|---|
| GET /session | Trusted authenticated platform identity and integration IDs; no token, business data or service credentials. The integrations are the principal's that the server has a connection for (ADR 0016); a bound product without a connection is not listed `roles` (S12-A) are the binding's effective roles. |
| (every route) | S12-A (ADR 0018): a route needs a role, from its pattern: a `.../release` a `publisher`, `POST /issues` an `issuer`, any other change of a layout, dashboard, template, background or image (and its catalog, a sample's copy) a `designer`; reading, querying, freezing, exporting, previews and checks a `viewer`. A principal without it gets 403 `{"error":"AUTH_ROLE_DENIED"}` before the route does anything. |
| GET /audit?limit=N&before=ID&scope=tenant | S12-B (ADR 0018): people's actions, newest first (N 1..200, default 50): the caller's own `{id, at, action, object_kind, object_id?, outcome, code?}`; with `scope=tenant` (the `administrator` role, else 403 `AUTH_ROLE_DENIED`) the tenant's and issuer's, each with its `subject`. Recorded: `design.change`, `design.release`, `issue.submit`, `export.submit`, `artifact.download`, `role.refused`, and (S16-A) `schedule.change` and `schedule.run`, each with its outcome (`succeeded`, `refused`, `failed`); a publication, a withdrawal and an official issue are first recorded as `requested` and refused with 503 `{"error":"AUDIT_UNAVAILABLE"}` when that cannot be written. 404 when the server keeps no audit. |
| /team/document-templates…, POST /team/issues | S12-C (ADR 0018): the template routes over the tenant's shared design space instead of the caller's own (same bodies, roles and audit). Every version records who saved it; `GET /team/document-templates/{id}?version=N` answers `saved_by_me`; publishing a version by the person who saved it is 409 `{"error":"APPROVAL_REQUIRED"}` (another publisher approves by publishing it); a withdrawal is any publisher's. An issue of a team template reads its publication in the shared space. The samples copy into the caller's own space. |
| POST /schedules | S16-A (ADR 0020, "Scheduled reports" below): `{name, query, export_format?, as_of?, recurrence}` + one Idempotency-Key → 201 the schedule and `Location`. A `viewer`'s. 400 for a request outside the forms, 422 for a dataset or format the server does not have, 403 for a product the caller does not hold, 429 `QUOTA_EXCEEDED` with `quota: "schedules"` over the limit. |
| GET /schedules?limit=N&before=ID | The caller's schedules under the products the caller holds, newest first (N 1..100, default 50); `before` is the last `schedule_id` of the previous page. |
| GET /schedules/{id} | One of the caller's schedules: `{schedule_id, name, integration_id, state, recurrence, as_of, export_format?, query, next_run_at?, created_at}`. Another identity's or a deleted one is 404. |
| POST /schedules/{id}/pause, POST /schedules/{id}/resume, DELETE /schedules/{id} | No body. The schedule after the change (`paused` without `next_run_at`; `active` from the next occurrence after now, the catalog checked again; `deleted`, then 404). Each is `schedule.change` in the audit. |
| GET /schedules/{id}/runs?limit=N&before=ID | The schedule's runs, newest occurrence first: `{run_id, schedule_id, due_at, state, as_of, snapshot_job_id?, export_job_id?, snapshot_id?, artifact_id?, export_format?, error_code?, delivery, finished_at?}`. The jobs and results are read through their own routes, as the owner. |
| GET /capabilities | Exact simulator capability profile; production_approved=false. `quotas` (S04-D) is `{jobs_in_flight, stored_results, admissions_per_hour, queries_in_flight}`, the numbers in force for every identity; see "Quotas" below. `embedding` (S03-N) is true when embedding is configured for at least one of the caller's integrations, else false (never the origins); see "Embedding" below |
| GET /catalog?integration_id=… | Only a product explicitly allowed for the authenticated principal. Each dataset version carries, when it declares any, `filters: [{field, operators, values?, required?}]` and `sorts: [{field, directions}]` (S20-A, ADR 0023): the columns a query may narrow by and order by, with their operators, an optional closed list of values and whether a filter on the field is required |
| GET /companies?integration_id=… | S19-B (ADR 0015): the companies the principal may choose in that product, under the catalog's authority and refusals (`integration_id` exactly once, else 400; a product the principal does not hold 403). Exactly `{"supported": true, "partial": false, "companies": [{"id", "name"}, …]}`: at most 1000 entries in the product's order, each id and name 1–128 characters, no id twice; for an adapter without the capability `{"supported": false, "partial": false, "companies": []}` (200, not an error); `partial` true when the product cut the list, and the person may still type an id. The list is advisory: a read still names company ids and the source authorizes it as before; nothing of it is stored, cached across requests or logged, and the answer carries the API's `Cache-Control: no-store`. The source's failure is the catalogue's 503; a list outside the bounds is 409 `INTEGRITY_FAILURE` and none of it is shown. A `viewer`'s |
| POST /queries | QuerySpec from S00 examples; typed columns, string cells, totals, completeness, issues, evidence. `filters` and `sort` within what the catalog entry declares (below); the rows and the totals are the narrowed scope's |
| POST /snapshot-jobs | QuerySpec + one Idempotency-Key; 202 queued, Location header; no row reads during admission. The filters and the sort are validated at admission, frozen in the job's query and bound by its `query_hash` |
| GET /jobs?limit=N&kind=K&state=S&before=ID | S07-E: `kind` (snapshot, export, issue) and `state` (queued, running, succeeded, failed, cancelled) narrow the page; `before` is the last `job_id` of the previous page and the page starts strictly after it (a `before` that is not the caller's job under the caller's integrations gives `[]`). Each key at most once and not empty, anything else 400. A page shorter than `limit` is the last. The caller's jobs of every kind under the integrations the caller holds, newest first (N 1..100, default 50; any other query is 400): the job view of each (`job_id, kind, state, snapshot_id, format, artifact_id, error_code, issue, expired`) as `GET /jobs/{id}` shows it, without asking the source: the listing authorizes no result, the reads do (`GET /jobs/{id}`, the snapshot's rows, the artifact's content). Another identity's jobs are never listed; an identity without integrations gets `[]`. An issue record that cannot be read as published is 409 for the whole page, as in `GET /issues` (S07-C, docs/S07C_SCOPE.md) |
| GET /jobs/{id} | Owner-scoped status; current source authority even after success. A job that succeeded and whose source read has expired is not re-authorized (there is no result left to authorize): it answers 200 with `expired: true`, its own state and ids, and, for an issue, the number from its record; the same after retention deleted its snapshot and artifact (S04-B, "Retention" below) |
| POST /jobs/{id}/cancel | Empty body; idempotent cleanup; no output references returned. A running issue job is not cancelled (the source product may already hold a reservation for it): it is returned unchanged, and a queued one is cancelled |
| GET /snapshots/{id}/rows?limit=1&cursor=… | Frozen rows; total_row_count; HMAC cursor bound to owner/product/query/snapshot; limits 1..100 |
| POST /exports | `{ "snapshot_id": "…", "format": "pdf", "layout_id": "…" }` + one Idempotency-Key; asynchronous. `format` is optional: absent or `xlsx` is the workbook, `pdf` needs a server started with PDF (listed in capabilities `formats`); other values are 422 UNSUPPORTED_COMBINATION. The job view carries `format` for non-workbook exports. `layout_id` (S23-A, ADR 0024, "Layouts and presented exports" below) is optional: the export is rendered through the latest saved version of that layout and the job view carries `layout: {id, version}`. The file of a snapshot whose query has filters or a sort says so (S20-C, "The exports say the scope" below) |
| GET /artifacts/{id}/content | Current whole-scope authority, expiry, private XLSX (`application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, `report-{id}.xlsx`) or PDF (`application/pdf`, `report-{id}.pdf`), exact byte length and SHA-256; an issued PDF is named `issued-<issue id>.pdf` (the console saves it as `issued-<number>.pdf`) |

The standalone HTML/CSS/JS console is served at /console/. Static assets contain
no credentials or private rows; they do not bypass protected API routes.

Every answer of every route below, success or refusal, whichever optional mounts a deployment composes,
carries `Cache-Control: no-store`, `X-Content-Type-Options: nosniff` and `X-Reporting-Data-Class` (`contract-simulator` for the core API, the demo and every mount as such; the production server, S05-A, sets it on every answer, health endpoints, 404s and redirects included, to `business-data`, or keeps `contract-simulator` when its configuration allows simulators; always exactly one value)
(one wrapper, applied by the core API and by each optional mount; a route adds only its own headers).

JSON POST bodies require application/json. Unknown/case-mismatched fields,
duplicate keys, null, extra roots, excessive nesting and >1 MiB bodies are rejected.
QuerySpec accepts only the dataset/version, metrics, dimensions, timezone and
currency policy declared by a published catalog entry. Company IDs are constrained selectors.

**Filters and sort** (S20-A, ADR 0023). A query's `filters` and `sort` are accepted exactly as far as the catalog entry declares them
(`filters` and `sorts` of the entry; an entry without them takes none, as before). At most 16 filters, a conjunction, each
`{field, operator, value}` on a declared field with one of its declared operators (`eq ne lt le gt ge in between prefix contains`), its
`value` a string well formed for the column: a date `YYYY-MM-DD`; an integer or a decimal as the column writes it (exactly its scale), at
most 128 characters; `true` or `false`; a string of 1..128 characters without control characters; from the entry's closed `values` when
it has them and the operator is `eq`, `ne` or `in`. `in` and `between` carry their values in `value` as a JSON array of strings as text
(`"value": "[\"current\",\"1_30\"]"`): 1..32 for `in`, exactly 2 in ascending order for `between` (the first not greater than the second,
compared as the column's type compares: exactly for numbers, as text for dates and strings). A field the entry marks `required`
must be filtered exactly once. At most 3 `sort` keys `{field, direction}`, in the order they apply, each on a declared field in a declared
direction (`asc`, `desc`), no field twice. Anything else — an undeclared field, operator or direction, a value off the list or malformed, a
required filter missing or doubled — is 422 `UNSUPPORTED_COMBINATION`, as any combination the catalog does not publish; the platform never
evaluates a filter itself. The source product applies them in its own transaction, binding the values as parameters, and the result's
rows **and totals** are the narrowed scope's, in the order asked, stable across pages. The normalized query sorts the filters by field,
operator and value and keeps the sort keys in their order; `query_hash`, the frozen snapshot and the evidence cover both, so a narrowed
snapshot is another snapshot than the whole dataset's, and its exports say what narrowed and ordered it (S20-C, "The exports say the
scope" below). The demo's two datasets declare: `receivables.aging` — `customer.id` eq/ne/in/prefix,
`aging.bucket` eq/in from `current 1_30 31_60 61_90 90+`, `ar.open_balance` lt/le/gt/ge/between; sorts `ar.open_balance` and `customer.id`
asc/desc; `shipments.daily` — `shipment.destination_country` eq/in, `shipment.service_code` eq, `shipment.count` ge/le; sorts
`shipment.count` and `shipment.destination_country` asc/desc.

Cells and totals are strings with declared types (string/date/boolean/integer/
decimal), scale, currency and unit. No float64 business conversion. Totals cover
the full original authorized result. A truncated preview says `preview`; a
partial snapshot page says `page`; all rows says `complete`. Snapshot paging does
not reread the source. Cursor alone confers no access.

The workbook has Data/Totals/Metadata sheets. Since S23-A (ADR 0024 decision 3) a `decimal` or
`integer` column of the Data sheet is numeric cells with an Excel number format derived from the
column's scale (and the layout's format, when there is one), each followed by a text column
`<label>（精确）` that holds the exact frozen string; a numeric cell is written only when the exact
string survives a float64 and comes back as the same digits at the column's scale, else the cell
is text too and `Metadata` counts it in `inexact_cells`. The `Totals` sheet has `value` beside
`exact_value` under the same rule. Every other cell is text: exact decimals, negative values and
integers beyond JavaScript/Excel precision survive unchanged in the exact columns; external
strings beginning '=' do not generate formulas, and no formula is ever generated (a layout's
subtotal rows are values). `Metadata` names the layout (`layout_id`, `layout_version`,
`layout_title`, `layout_footnote`, `subtotal_by`; empty without one) and ends with the scope of
the snapshot's query (S20-C, "The exports say the scope" below).

A PDF is PDF/A-2b, rendered by a pinned Typst binary from a server-owned template
(export/pdf): the same rows in snapshot order, the column labels repeated on every page,
metric numbers with thousands separators and every digit kept, the metric totals, and a
footer with page X of Y, snapshot ID, dataset@version, as-of date, source watermark and
data class. A character the configured fonts do not cover (with Noto Sans SC this includes emoji and
zero-width spaces and joiners), a soft hyphen (Typst would print it
as "-" at a line break), or more than 32 columns fails the job with UNSUPPORTED_COMBINATION;
a document over the artifact limit, or one that exceeds the renderer's own time (15 s) or
memory (2 GiB) limit, fails with BUDGET_EXCEEDED. Such snapshots remain available as
workbooks. Other renderer failures, and the 20 s job timeout, are EXECUTION_FAILED.

**The exports say the scope** (S20-C, ADR 0023 decision 6, docs/S20C_SCOPE.md). A snapshot made with filters is a narrowed scope and its
totals are that scope's; one made with a sort is ordered as asked. Both files say what the frozen query narrowed and ordered by, with or
without a layout (the scope is the query's, whatever the layout shows). They only say it: the source applied it, and the rows and totals
are the snapshot's.

- **Workbook.** The `Metadata` sheet ends, after every row it had before, with the scope row
  `query_scope | filters | <n> | sort_keys | <m>`: the third cell counts the query's filters, the fifth its sort keys. Every workbook has
  it, `0` and `0` for a query without filters and sort; a workbook without the row was written before S20-C and says nothing of its
  scope. The n rows after it are the filters, `filter_<i> | <words> | <field> | <operator> | <value>`, in the query's normalized order
  (by field, operator, value), and the m rows after those the sort keys, `sort_<i> | <words> | <field> | <direction>`, in the order they
  apply (i from 1). The words are English with the column's label (`equals`, `does not equal`, `is less than`,
  `is less than or equal to`, `is greater than`, `is greater than or equal to`, `is one of a, b`, `is between a and b`, `starts with`,
  `contains`; `ascending`, `descending`); the cells beside them are exactly what the query has, the value of an `in` or a `between` the
  JSON array of strings it carries. All are text cells, like the rest of the sheet:

  ```
  query_scope | filters | 2 | sort_keys | 1
  filter_1    | 账龄区间 is one of current, 1_30                 | aging.bucket    | in | ["current","1_30"]
  filter_2    | 应收余额 is greater than or equal to -1.00       | ar.open_balance | ge | -1.00
  sort_1      | 应收余额 descending                              | ar.open_balance | desc
  ```

- **PDF.** Under the meta line of the first page, in words: `筛选：账龄区间 属于 current、1_30；应收余额 不小于 -1.00` and
  `排序：应收余额 降序` (the column's label, the operator in a word — 等于, 不等于, 小于, 不大于, 大于, 不小于, 属于 with the values
  joined by 、, 介于 a 和 b 之间, 开头为, 包含; 升序, 降序 — and the values; filters joined by ；, sort keys by 、). In the footer of
  every page, beside the snapshot's id: `快照 <id>（已按 2 项条件筛选、1 项排序）` (or `（已按 2 项条件筛选）`, `（已按 1 项排序）`), so
  that the page with the total row says the total is a narrowed scope's. A value is the query's exact string: a number is not grouped
  (`1000.00`, where the table's cells print `1,000.00`), a boolean is `true` or `false`. A value that could be misread in the sentence
  — one that begins or ends with white space, or holds 、 ； “ ” (or 和, in a `between`) — is set between “ and ”, unchanged.
  The words are bounded and never cut silently: the filter sentence takes at most 6 lines of the meta block and the sort sentence at
  most 2 (lines of the page's text width at 7.5 pt). A sentence that does not fit says the count, lists the first whole items that fit
  and says where the rest are, `筛选（共 16 项，此处列出前 5 项）：…；其余 11 项见本快照的查询及其 XLSX 导出的 Metadata 工作表。`; when not
  even the first fits, `筛选：共 16 项，此处未能列出，见本快照的查询及其 XLSX 导出的 Metadata 工作表。` (the same two forms for 排序). The
  footer always counts every filter and sort key. Every character of the scope is checked against the fonts like a value of the table,
  whether or not the bound lets it be printed: a filter value or a label the fonts do not cover fails the PDF job with
  UNSUPPORTED_COMBINATION, and the snapshot remains available as a workbook.

A frozen query that does not fit the frozen columns by the core's rules fails either export with INTEGRITY_FAILURE and nothing of it is
printed. An export of a query without filters and sort is what it was: the PDF byte for byte (the golden test of S23-A holds it), the
workbook cell for cell but for the one `query_scope` row at the end of `Metadata`. An issued document and a preview say nothing of the
scope (they are per row or group); the issue record names the snapshot, whose query holds the filters.

Document templates (S03-E, docs/S03E_SCOPE.md; multi-page tables S03-F, docs/S03F_SCOPE.md) are mounted when the
demo has the matching directories, and previews when it has the PDF flags:

| Method and path | Request / response |
|---|---|
| POST /document-previews | `{config, snapshot_id, row_index}`; one A4 PDF/A-2b preview of the template bound to that frozen row and the whole-scope totals; `application/pdf`, `no-store`, `X-Content-SHA256` |
| POST /document-checks | Sample check (S03-I, docs/S03I_SCOPE.md): `{config, snapshot_id, rows}` (1–50 distinct row indices) or `{config, snapshot_id, samples: "boundary"}`, never both; answers `200 {"rows": [{row_index, ok, code?, detail?, reasons?}]}` in row order, `no-store`, without rendering (`detail`: S03-J, below). Mounted with the previews when the renderer can check (the PDF writer can) |
| GET, POST /document-templates | List authorized templates: `[{id, version, binding, name, schema_version, preset, published_version, catalog}]` (the latest version's `name` (`""` when it has none), schema and page preset, and the published version, `0` when none; `catalog` as in `GET /{id}/catalog`; none of these opens a template, so a listing never changes `last_opened`); save `{id, expected_version, config}` as a new immutable version |
| GET /document-templates/{id}?version=N | One saved version, after current field authorization; reading a version records the time as the template's `last_opened` |
| GET, PUT /document-templates/{id}/catalog | The template centre's metadata of one template of the caller (S06-B): `{domain, flow, favourite, archived, collections, last_opened, revision}`. A template that never had any answers the empty one (`revision` 0, `collections` `[]`, `last_opened` `""`). `PUT` takes the whole object without `last_opened` and with `expected_revision`: `{domain, flow, favourite, archived, collections, expected_revision}`; a stale revision is 409, a name that is not valid 400 (a domain or flow of at most 40 characters on one line; at most 16 collections, distinct, each 1-40 characters, no control character, no space around it) and the answer is the stored object one `revision` on. `last_opened` is the server's: reading a version sets it and a `PUT` keeps it; neither it nor `revision` is changed by the other. Per identity: another identity's template is 404 |
| GET /document-templates/samples | The sample pack (S06-B), the same for every signed-in identity: `[{kind, name, domain, flow, description, preset, schema_version, elements, fields}]`, no rows and no document |
| GET /document-templates/samples/{kind} | One sample in full: the summary and `field_slots` (`{id, label, type, scope: document|row[, metric, scale]}`), `rows` (the sample's own rows, objects by slot id) and `document` (a 2.x document bound to no dataset: its text, code and table elements name slots in place of columns). 404 for a kind the pack lacks. Samples are read-only: no other method is routed |
| POST /document-templates/samples/{kind}/previews | A PREVIEW-marked PDF/A-2b preview of the sample from its own rows; no body (or `{}`), no dataset, no authorization beyond a signed-in identity; `application/pdf`, `no-store`, `X-Content-SHA256`. Mounted where the preview renderer is |
| POST /document-templates/samples/{kind}/copy | `{dataset: {integration_id, id, version}, name, mapping: {<slot id>: {"column": <dataset column>} or {"literal": <text>}}}`: binds the sample to the caller's dataset on the server and saves it as version 1 of a new template, answering the saved template as `POST /document-templates` does (201, `{id, version, config}`). A slot mapped to a column is that column (a document-scope slot named in the sample's group becomes a group field); a slot mapped to a literal is a literal text; **a slot not in the mapping is the literal `[<label>]`**. A table column whose slot is a literal (or unmapped) has no column to print: it is dropped and its width shared among the others (400 when none is left). An empty `name` is the sample's. The result is validated and authorized exactly as a save (a column the dataset lacks 403, a group field that is a metric or a group sum of a dimension as a save says, a literal a box cannot hold 400), stored in the caller's private store, which bounds it (64 templates per identity, 413 beyond) and starts in the sample's domain and flow in the catalogue; the platform's job and result quotas do not apply to templates |
| GET, POST /document-templates/{id}/release | Release history; `{action: publish|withdraw, version, expected_revision}` |
| GET /document-templates/{id}/published | The published version |
| POST /document-templates/{id}/previews | `{version, snapshot_id, row_index}`; previews the stored version, never caller configuration |
| GET, POST /document-backgrounds | List authorized PNG backgrounds; upload `{binding, png}` (base64, 512 KiB) and get `{id, sha256}` |
| GET, POST /document-images | Private images of schema 2.1 templates (S03-M, ADR 0005): list the caller's images of the datasets the caller may still read; upload `{binding, image}` (PNG or JPEG in base64, at most 512 KiB, 2000 x 2000 pixels, the format read from the content) and get `201 {id, sha256, binding, format, width, height}`; mounted with the backgrounds; the answers of the three image routes are `no-store` and `nosniff` |
| GET /document-images/{id}/content | The bytes of one image to its owner only (another identity's id is 404), while the caller may read its dataset (else 403): `Content-Type` `image/png` or `image/jpeg`, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none'; sandbox; frame-ancestors 'none'`, `X-Content-SHA256` (the digest the template names) |
| POST /issues | Official issue under the source product's authority (S03-G, docs/S03G_SCOPE.md; S03-H, docs/S03H_SCOPE.md, docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md): `{template_id, version, snapshot_id, row_index}` (a schema 2.0 template: `row_index` or `group`, see "Issue by group" below) with an Idempotency-Key admits a job of kind "issue" (202, `Location: /jobs/{id}`); the version must be the published one (409 TEMPLATE_NOT_PUBLISHED), the row must exist (400), the fields and background must be authorized (403), and the snapshot's integration must have an adapter that can issue, else 422 UNSUPPORTED_COMBINATION, nothing admitted; the job view carries `issue: {template_id, template_version, template_sha256, row_index}` (a group issue: `group` in place of `row_index`), and the product's `number` once the job succeeded (read from its record), wherever the job is shown: the 202 of a request repeated with the key of a job that has succeeded (a client whose answer was lost asks again), `GET /jobs/{id}` and the 200 of `POST /jobs/{id}/cancel` |
| GET /issues?limit=N | The caller's issue jobs, newest first (N 1..100, default 25): `issue_id, state, error_code, template_*, snapshot_id, row_index` (a group issue: `group`) for every job, and, once it succeeded, its record: `number` (the product's string, such as `AR-000012`), `artifact_id, snapshot_sha256, sha256, pages, stamp, issued_at` and the product's references `reservation_ref, confirmation_ref, confirmed_at` (RFC 3339, UTC), and `expired: true` once the file is no longer served (S04-B). A queued, running, failed or cancelled job has none of them; a failed job shows no number even when the product had reserved one (it is voided there), unless it failed with ISSUE_UNRECONCILED, where the product may still hold it |
| GET /issues/{id} | One issue job with its record; 404 for any other job or another identity's. The listing and the record read fail closed: a record that cannot be read as published (tampered, missing) is 409 INTEGRITY_FAILURE for the whole page. A succeeded issue job's `GET /jobs/{id}` re-authorizes the source and checks the download like an export's while the snapshot's read is valid; once it expired the job view says `expired: true` without asking the source (S04-B; the download is 409 inside the grace period and 404 once swept), and `GET /issues/{id}` answers with `expired: true` and the unchanged record; the job view also requires the artifact to claim the record and the record to name the artifact (409 otherwise) |

An issue job (S03-G) freezes the published version's canonical configuration and digest, the snapshot and the
row. The source product, not the platform, authorizes the issue, owns the number and confirms the file (S03-H);
the platform never numbers a document, so the job has no number. The worker re-checks the source, the snapshot
and the row, and has the issuer check everything it can without the product and without rendering: that the
frozen configuration is still the published version (else the job fails with TEMPLATE_NOT_PUBLISHED), that the
row and the table bind, that the background resolves and that the renderer's layout accepts the values (a value
too wide for its box, a character the fonts lack, a table that cannot be laid out and the like fail the job with
UNSUPPORTED_COMBINATION, as do the other refusals of the preview with their codes). A product must account for
every number it voids, so none of these may cost it a reservation: they are checked first, and a job refused by
them has asked the product nothing. Only then does the worker ask the product to prepare the document
(`PrepareDocument`, idempotent by the job id) with the frozen read, the row's index and the digest of its
values (see below), and the template's id, version and digest. The product refuses (the job fails with AUTH_SCOPE_DENIED,
nothing reserved) when the principal may not issue documents, the row is not the one its own source snapshot
produced, or its approval rules say no; otherwise it answers with a reservation: its reference, the number
(1..32 of `A-Za-z0-9._-`, starting with a letter or a digit: the finance simulator gives `AR-000001`,
`AR-000002`, ..., the shipment one `SH-000001`, ..., per integration and never reused), the issue time (a
whole second between 1970-01-01T00:00:00Z and 9999-12-31T23:59:59Z; the time is judged by value, so the
product may give it in any zone, and the platform keeps it in UTC) and an expiry (after the issue time, within
the same years, not necessarily a whole second); its reference is 1..128 printable ASCII characters
(0x21..0x7E: no space, control character, NUL, DEL, byte beyond ASCII or bidirectional mark), as is the
confirmation reference of its receipt, whose time is a whole second in the same years, not before the issue
time; an answer that is not that fails the job with INTEGRITY_FAILURE, before anything is rendered or
published. The worker then
renders the preview's pages with the issue stamp `ISSUED No. AR-000012 - 2026-10-28T06:12:03Z - <issue id>`
(the product's number and issue time) in the banner strip (in the text colour, in variables mode too) and with
the issue time as the PDF's creation date, so that the same inputs give the same bytes; the issuer repeats its
checks, because the template can be withdrawn between the check and the render, and a job that fails there
(TEMPLATE_NOT_PUBLISHED) voids the reservation; what only the render can find, a glyph Typst would draw wider
than measured, fails the same way. It has the product confirm the file (`ConfirmIssue`: the reference, the
SHA-256 and the page count; it answers with its confirmation reference and time), re-checks the source and
publishes the PDF artifact (format "pdf", carrying the issue id; downloaded as `issued-<id>.pdf` through
GET /artifacts/{id}/content, with the snapshot's expiry and authorization rules, and only when the record names
that artifact and its digest) together with the permanent record in one transaction. The record holds the product's number, `issued_at`, the product's
`reservation_ref` and `confirmation_ref` and its `confirmed_at`. Records are never changed or deleted. There is no
atomicity across the two systems (README), so every outcome is reconcilable: when a job fails after the worker called
`PrepareDocument` (whatever it answered), or on a claim later than the first (an earlier attempt may have reserved
or confirmed), other than by losing its lease, the failure is recorded first and the product is told after: the
worker fails the job as ISSUE_UNRECONCILED, fenced by its claim (the job must be running, at the claim's
generation, with a lease that has not run out); only if that took does it call `VoidIssue` with the job id and
the job's error code as the reason, with a context of its own bounded to 5 seconds; the product voids whatever
it holds for the job, reserved or confirmed, and the number stays voided; then the job's own code replaces
ISSUE_UNRECONCILED. A void that cannot be delivered leaves the job ISSUE_UNRECONCILED, and the product can find the
reservation by the job id; so does a crash between the steps, which is true, the product may hold a reservation.
If the failure cannot be recorded (the lease is lost, or the store cannot be reached) nothing is voided: the job is
left to its lease and the next claim, of a later generation, voids it. A void that is delayed past the lease thus
never voids a number that another worker has confirmed and published: once the failure is recorded nobody can claim
the job. A panic in a job's execution, in the issuer or in an adapter, is a failure with the code EXECUTION_FAILED
(its value is not kept) and follows the same rule. A first attempt that fails before it reached the product (at the source, snapshot or row check
or at the issuer's check) calls nothing there. A worker that loses its lease never voids: the worker that holds
the job reuses the reservation (the product answers the same job id again), renders the same bytes and has them
confirmed again, which the product does idempotently. A lease is lost only when the store says so: an answer of lost lease while the store still has the claim running with its lease intact (or from the product's adapter, which cannot speak it) is INTEGRITY_FAILURE and voids, and a publication refused only because the snapshot's source read expired is denied (AUTH_SCOPE_DENIED) and voids. A running issue job cannot be cancelled (it finishes, or
fails and voids; its cancel returns it unchanged); a queued one can. `POST /issues` is mounted with the template routes (`template_routes` in the capabilities) and
answers 422 UNSUPPORTED_COMBINATION when the deployment cannot issue (no issuer, or an issuer without the PDF
format) or the integration's adapter does not implement the product's document authority; a stored job whose
adapter lost it fails with UNSUPPORTED_COMBINATION. The capabilities flag `issuance` is false only without an
issuer: with an issuer but without the `pdf` format it stays true and `POST /issues` answers 422 (the console
then offers no issue button, as `formats` lacks `pdf`). `issuance` says only that the server has an issuer;
whether an integration can issue is the product's (the adapter's) capability. The preview's banner text is refused
as a value or a cell of an issued document.

The digest of a row (`IssueIntent.RowSHA256`, `reporting.Digest(row)`) is what lets a product check the row against
its own source snapshot, so a product not written in Go must be able to compute it: it is the SHA-256, in lowercase
hexadecimal, of the row's values as one JSON array of strings in column order, with no white space (`["a","b"]`),
escaped as Go's `encoding/json` escapes: `"` and `\` with a backslash, the controls `\b \f \n \r \t` by their letter
and every other control below U+0020 as `\u00xx` in lowercase hexadecimal, `<`, `>` and `&` as `\u003c`, `\u003e` and
`\u0026`, U+2028 and U+2029 as `\u2028` and `\u2029`; DEL and every other character, ideographs included, as
themselves in UTF-8; a byte that is not valid UTF-8 as the six characters `\ufffd` (a valid U+FFFD is written as
itself). Test vectors (reporting/row_digest_test.go reproduces them):

| Row | The bytes hashed | SHA-256 |
|---|---|---|
| `customer_a`, `9007199254740893.00`, `CNY` | `["customer_a","9007199254740893.00","CNY"]` | `aa1b2c6c29e265b6b34c10d929b210a1f2899afecb0c9d690d0d0e962dc0329d` |
| `甲方<&>`, `a` U+2028 `b` U+2029 `c`, `1.00` | `["甲方\u003c\u0026\u003e","a\u2028b\u2029c","1.00"]` | `cf9dc98dd84f895258a57dd2714aa7231639c9d401c957c21a3788e8b0b8869f` |
| `a"b\c`, LF, TAB, U+0001, BS, FF, DEL, the byte 0xFF; the empty string | `["a\"b\\c\n\t\u0001\b\f` DEL `\ufffd",""]` | `aa997ec4fc9d759902afb618c37b68720017938ac533a317c1061df3f9f64212` |

Platform ids (jobs, snapshots, artifacts, templates, issue records) are the 32 lowercase hexadecimal characters
the platform makes and no other spelling of the same bytes: an uppercase id is not the id it spells (the issue
stamp's width guarantee assumes the lowercase form), so it is 404 where a path or a snapshot names it and 400 in a
request body's template id.

A preview places each value on one line with its baseline 1.16 font sizes below the top of its box, at the box's
left edge, right edge or centre, every character at the position the fonts' plain advances give it (ADR 0002);
trailing white space is not drawn. A value the fonts do not cover, a soft hyphen, a Hangul tone mark, a leading
combining mark, a value that does not fit its box, one with control characters or over 256 characters, or one
Typst would draw wider than measured is 422 UNSUPPORTED_COMBINATION, as is a template naming a background on a server without
a background store; a preview whose wait for one of the renderer's processes and render together exceed the
render timeout (15 s) is 413 BUDGET_EXCEEDED (previews hold at most all but one of the processes, so they never
fail an export). Previews are never issued documents and are not stored. A background upload is tried with the
renderer and refused when its preview would exceed the artifact limit.

The sample check (S03-I) asks, for rows of the frozen snapshot, what the worker asks before it asks a source product
for a number: the layout check of the PDF writer (`CheckIssue`: the fonts' plain advances, no Typst process, no render
slot, no PDF). `rows` are the preview's global row indices (0-based, 1–50, distinct, each inside the snapshot); the
answer lists them in ascending order. `samples: "boundary"` lets the server pick: the first and the last row and, for
each row-field box, the rows with the longest and the shortest value by display width (an East Asian wide or
fullwidth character counts two, every other character one; ties go to the lowest index), at most 50 (2 + 2 x 24).
`reasons` says why a boundary row was picked: `first`, `last`, `longest:<box id>`, `shortest:<box id>`; explicit rows
have none. A snapshot without rows answers an empty list. A row's `code` is the error code a preview of that row
would answer: a value that does not fit its box or column, a character the fonts lack, a value the preview cannot
print and the like are `UNSUPPORTED_COMBINATION`, and a table that cannot be laid out refuses every row alike. Any
other error (an integrity failure, an unavailable authority, a budget, a context error) is the whole request's and
answers as the preview would. A request that carries both or neither of `rows` and `samples`, a `samples` other than
`boundary`, no row, a duplicate, a negative or out-of-range row, more than 50 rows or an invalid template is 400.
Authorization is the preview's, in the preview's order: the snapshot must be the caller's (404 otherwise), the
template's binding and every field of its boxes and table must be authorized (403) and a background must resolve for
the caller (the variables mode drops it); the snapshot and the document are authorized again before the answer.
Answers carry row indices, codes and reasons, never values. What only the render can find (a glyph Typst would draw
wider than measured, a Typst failure) is not found: the PDF preview stays authoritative.

A schema 1.3 template adds `table: {x, y, width, height, font_size, columns: [{field, width}]}` (S03-F): the
preview then lists every row of the frozen snapshot over as many A4 pages as it needs, each page with the same
background, boxes and banner, the header row, a 承前 row from page 2 on, a 过次 row on every page but the last
and the 合计 row on the last, with the metric columns' sums computed exactly on the server. A table column the
dataset does not have is 403; a row taller than the table's pages can hold, an amount, a printed carried sum or a
carried row's label (measured for page 100) wider than its column, a header and total row taller than the region,
or a document of more than 100 pages, is 422; a snapshot whose whole-scope total differs from the sum of its rows is 409 INTEGRITY_FAILURE.

Refusal details (S03-J, ADR 0004 decision 8). A layout or binding refusal of either template family, a 422
UNSUPPORTED_COMBINATION as ever, may add `detail` to the problem answer, `{"error": "UNSUPPORTED_COMBINATION", "detail":
{reason, band?, element?, row?, column?}}`, and to a refused row of a check (the same detail a preview of that row answers).
`reason` is one of `DOES_NOT_FIT` (a value wider or taller than its box or column, a carried label or sum that does
not fit its column, a table row taller than a page), `NOT_COVERED` (a character the fonts lack, a control character, a
leading combining mark, the preview banner's text in an issued document), `INCONSISTENT_GROUP` (a field that is not a
group field differs between the rows of a group), `TOO_MANY_PAGES` (over 100) and `BAND_OVERFLOW` (a band, or the
table's header and total row, taller than the space its page leaves). The place is names and indices, never a value:
`band` (a 2.0 band), `element` (a 1.x box id `n1`-`n24`, a 2.0 element id `e1`-`e64`), `row` (the snapshot's global
row index, 0-based: a table row's, or the first row that differs in a group) and `column` (the table column's field id).
For 1.x the place is the box or the table column; for 2.0 also the band. A client that ignores `detail` works as
before; the console shows it and, for 1.x, outlines the box or column on the canvas. Codes and statuses do not change.

Schema 2.0 (S03-J, ADR 0004): a closed JSON form beside 1.x, `schema_version: "2.0"`, with `binding`, an optional
`group: {fields}` (1-3 distinct dimension fields of the dataset, never metrics) and `bands`; it has no `elements`,
`print`, `background` or `table` key and no 1.x template is converted by the server. One document is made per row group:
the frozen rows are partitioned by the exact strings of the group fields (the empty string is a key like any other),
groups ordered by their first row, each keeping the snapshot's row order; without `group` the whole snapshot is one
document (of no rows if the snapshot has none, with a header, an empty table and a zero total). `bands` has
`page_header` (every page), `document_header` (the first page, under the page header), `detail` (required: exactly one
`table` and nothing else, the only band that grows and continues over pages), `document_footer` (once, directly under
the table's total row, whole; it moves to a new page, below the page header, when it does not fit under the table, and
is refused with BAND_OVERFLOW when it fits no empty page) and `page_footer` (every page, at the bottom of the safe
region); each but `detail` is `{height, elements}` with `height` of 5 to 245 mm on the 0.1 mm grid, `detail` is
`{elements}`. `detail` may be absent (S06-B part 2): the template is then a *form*, which draws its page header, document header,
document footer and page footer on one page, with no table, no rows of its own and no carried sums; the document footer lies directly under the
document header; bands that together exceed the safe region are refused with DOES_NOT_FIT, never cut; at least one band is needed, `"detail": null`
is refused, and a 1.x template has none. A form's documents are made as above (the group fields select the rows, which must agree on every field
that is not a group field); without `group` it is one document per row, as a 1.x template. Bands lie inside the safe region (x 10-200, y 25-270 mm) and the strip 10-18 mm stays the banner's or the
stamp's. The heights of the page header, document header and page footer plus the least detail band (the table's
header, brought-forward, one body and carry-forward row, one line each at the table's type size) must fit the safe
region. Element positions are in millimetres on the 0.1 mm grid, relative to the band (which starts at x 10 mm); ids
`e1`-`e64` are unique in the template, at most 64 elements and 24 per band, a template at most 64 KiB, at most 12 table
columns. Elements, each with exactly the keys of its `type` (unknown keys and unknown types are refused; `barcode`,
`qrcode`, `image` and the text source `issue_number` are schema 2.1, below, and refused in a 2.0 document):

- `text`: `id type source text field wrap x y width height font_size align bold`. `source` is `literal` (`text`, at most
  256 characters, no control; with `wrap` up to 1024 and line breaks), `field` (`field` of the dataset; the value of
  the group: a group field, or any field that has the same value in every row of the group, else INCONSISTENT_GROUP),
  `group_sum` (`field` a metric, whether or not it is a column of the table: the exact decimal sum of the group's
  frozen values, kept at the longest fraction or the column's scale, bound exactly and printed as the table prints its
  totals, with thousands separators; unlike a 1.x `total`, which is the source's own value and prints as it is, this
  number is the platform's, so one amount never has two formats on a page; the grouped text is the one that must fit
  the box) or `page` (`text` with `{page}` and/or `{pages}` and no other
  brace, never wrapped, filled once the pages are known; the widest is checked on every page). `font_size` 8-28, the
  box at least 10 x 5 mm and 0.556 mm per point of type size high, `align` left, centre or right, `bold` boolean. Without
  `wrap` a value is one line as in 1.x (it must be 2 pt narrower than its box); with `wrap` it breaks into lines at the
  places the table cells break within the box width less 2 pt, 1.5 font sizes apart, and more lines than the box's
  height holds are refused (DOES_NOT_FIT); its value may hold line breaks and tabs. No two text boxes of a band overlap.
- `line`: `id type x1 y1 x2 y2 stroke`, two different points inside the band, `stroke` 0.25-2 pt in quarters.
- `rect`: `id type x y width height stroke fill`, `stroke` 0-2 pt in quarters, `fill` `none` or one of four greys
  (`grey1` to `grey4`, luma 245, 232, 215, 190); never both none and 0. Lines and rectangles are drawn under the texts.
- `table` (detail band only): `id type x width font_size columns`, `font_size` 8-12, `columns` of `{field, width}`
  (at least 8 mm, adding up to `width`) with an optional `align` (left, centre, right: the default is right for a
  metric, else left) and an optional literal `label` in place of the dataset's header (with no currency or unit
  suffix). The table keeps the 1.3 behaviour over the group's rows: header on every page, wrapped cells, brought-forward
  and carry-forward rows, a total row, exact sums; a group's table has no snapshot-wide total to check.

Amounts stay the product's: the platform sums printed metric values for presentation only (carries, table totals and
`group_sum`, all printed with thousands separators); tax, rounding, document totals and amounts in words are fields the product supplies. A 2.0 preview
(`POST /document-previews`) renders the group that contains `row_index`; `POST /document-checks` binds and checks the
group of each row asked for (a group once, every one of its rows with its result), and `samples: "boundary"` picks groups:
the first and last, the one with the most and the one with the fewest rows (`first`, `last`, `most_rows`,
`fewest_rows`), for each `field` and `group_sum` text the groups with the longest and the shortest value
(`longest:<id>`, `shortest:<id>`) and for each table column the group with the longest cell (`cell:<field>`), each
by its first row, at most 50. Authorization checks every field the template names (a group field a dimension: 422 for a
metric; a group sum a metric; a table column or text field a column the caller may read: 403). A 2.0 template is issued by group (S03-K), under the source product's authority exactly as a 1.x row is: see "Issue by
group" below.

Issue by group (S03-K, docs/S03K_SCOPE.md, ADR 0004 decision 7). A schema 2.0 template makes one document per row group, and
`POST /issues` issues one: `{template_id, version, snapshot_id, group}`, where `group` is the list of the group's key values in the
order of the template's `group.fields` (the empty list for a template without `group`, whose one group is the whole snapshot), or,
for a client that holds a row number (the console), `{template_id, version, snapshot_id, row_index}`, the group containing that row.
A *form* without group fields (S06-B, above: one document per row) is issued as a 1.x template is: `{template_id, version, snapshot_id, row_index}` alone
(`group` is 400), the document is that row's own, and the job, the product's intent, the record, the listing and the job view carry `row_index` and no
`group`; the product-authority contract is the one of a 1.x issue (contract version 1 suffices). A form with group fields is issued by group as any 2.x template.
Both or neither is 400, and so is a `group` of a 1.x template (a 1.x template keeps `row_index` alone, as ever) or of a length other than
the template's fields. The admission resolves the group in the frozen snapshot, as the preview does, and refuses as the preview refuses
(a field that differs inside the group, a value the layout cannot print: 422 with the reason and the place, never a value); a key no group
of the snapshot has, and the whole-snapshot group of a snapshot with no rows, is 404 NOT_FOUND, and a `row_index` beyond the snapshot is
400. Every refusal and error carries no value: the key values appear only in the request, in the job and the record and in the views of
the caller's own data. The group is frozen with the job: its fields, its key values, its row indices and the digest of its rows; the request
hash covers the resolved group, so `row_index` and `group` naming the same group are one request (the same Idempotency-Key replays the
one job; another group under the key is 409 IDEMPOTENCY_CONFLICT; a row request is never a group request).

The source contract has a second version for it. An adapter declares contract version 2 of `DocumentAuthority` by implementing
`IssueContractVersion() int` and returning 2 (`reporting.VersionedAuthority`; an adapter without it speaks version 1). The platform
never sends a group intent to an adapter that has not declared it: `POST /issues` of a group answers 422 UNSUPPORTED_COMBINATION at
admission, nothing admitted, and a stored group job whose adapter lost the declaration fails with UNSUPPORTED_COMBINATION before the
source is read or the product asked. A group intent is `IssueIntent.Group` (`Fields`, `Key`, `Rows` and `RowsSHA256`) and no
`RowIndex` or `RowSHA256`; the intent of a 1.x row is exactly what it was. The product checks every row of the group against its own
source snapshot and decides as it decides for a row: it refuses (AUTH_SCOPE_DENIED, nothing reserved) when the principal may not issue, when
a group field is not a dimension of its frozen read, when a row is not one of that read's rows or does not have the group's key, when the group
is not whole (its read has another row with the key; without fields the group is every row) or when the digest of the rows is not the one
the platform froze; otherwise it reserves the number, confirms the file and voids as for a row. Idempotency by the job id, reservation,
confirmation, void, tombstones and ISSUE_UNRECONCILED are unchanged, and another intent under a job id, a group for a row or another group, is
ErrConflict.

The digest of the rows of a group (`IssueGroup.RowsSHA256`, `reporting.RowsDigest`) is the encoding of one row's digest (above) applied to the
rows together: the SHA-256, in lowercase hexadecimal, of the group's rows as one JSON array of the rows' arrays of strings, in the order of
the group (the snapshot's order), no white space, escaped as for one row; no row is `[]`. Test vectors (reporting/row_digest_test.go
reproduces them; the group of no rows is never issued):

| Rows | The bytes hashed | SHA-256 |
|---|---|---|
| (`customer_a`, `current`, `10.00`), (`customer_a`, `1_30`, `30.00`), (`customer_a`, `current`, `50.00`) | `[["customer_a","current","10.00"],["customer_a","1_30","30.00"],["customer_a","current","50.00"]]` | `42785716cd90e7e5c5eaac4159000d35054396fad9d8cc31578e8d1dbaffd1e7` |
| (`甲方<&>`, `a` U+2028 `b` U+2029 `c`, `1.00`), (empty, `x"y\z`, empty) | `[["甲方\u003c\u0026\u003e","a\u2028b\u2029c","1.00"],["","x\"y\\z",""]]` | `209828bd1e32d244685625799e6d48ce46055607aedfc5331caa0868fd31bcb6` |
| (`customer_b`, `1_30`, `20.00`) | `[["customer_b","1_30","20.00"]]` | `034bcc9595806bc506e14adebf2fd7f70058fa8a28389f8700a8e037d226a390` |

The worker re-checks the frozen group against the frozen snapshot before the product is asked (a stored group that is not that snapshot's,
whole, fails with INTEGRITY_FAILURE and calls nothing), then has the issuer check what it can without the product: the frozen template is
still the published version, the group binds as a preview binds it (BindGroup), and the layout accepts the document; the issuer refuses a
group job whose fields are not the template's, a 2.0 job without a group and a 1.x job with one. After the product's reservation it renders
the group's document, the stamp `ISSUED No. <number> - <issue time> - <record id>` set in the strip of every page, with the same
determinism as a row (the product's issue time is the PDF's creation date, so that the same job gives the same bytes), has the product
confirm the file and publishes the artifact and the record. The failure order is S03-H's, unchanged: the failure recorded first, fenced by the
claim, as ISSUE_UNRECONCILED, then the void, then the job's own code. The record keeps the group (its fields, key values, row indices and the
digest of its rows) in place of the row, and `ValidatePublication` and every view check it against the job. No SQL migration is needed:
the job's and the record's JSON payloads carry the group (a binary older than S03-K reads a group job as corrupt: INTEGRITY_FAILURE;
platform binaries are rolled together, docs/OPERATIONS.md).

The views show the group where they showed the row: `issue.group` of a job view and `group` of an entry of `GET /issues` and of
`GET /issues/{id}` are `{fields, key, row_count, rows_sha256}` (the template's group fields, the key values in their order, how many rows, the
digest of those rows), and a group issue has no `row_index`; a row issue has no `group`. The row indices stay in the record, which the
views check but do not repeat.

Schema 2.1 (S03-M, docs/S03M_SCOPE.md, ADR 0005): `schema_version: "2.1"` is 2.0 plus three element types and one text
source; everything else, grouping, bands, the table, the issue by group and the refusal `detail`, is 2.0's, and 2.0 and 1.x
documents are unchanged and render byte for byte as before (the evidence PDFs of every earlier schema are compared). The new
elements are allowed in every band but `detail`; a text, a barcode, a QR code and an image each own a box of their band, and no two of
them overlap (lines and rectangles may lie under them). Closed like 2.0, with exactly these keys:

- `barcode`: `id type source text field x y width height symbology human_readable` and, only when `human_readable` is true,
  `font_size` (8-28; a barcode that does not print its value has no `font_size` key). `symbology` is `code128`. The box is at
  least 10 x 5 mm.
- `qrcode`: `id type source text field x y size ecc`: a square box of `size` mm (10-190) and `ecc` `L`, `M`, `Q` or `H`.
- `image`: `id type x y width height image`, a box of at least 5 x 5 mm and `image: {id, sha256}`, a private image of the template's
  binding and identity (below). The picture is drawn contained in the box, centred, its aspect ratio kept.
- `text` gains the source `issue_number` (`text` and `field` empty, no `wrap`): the number the source product assigned.

`source` of a barcode and a QR code is `literal` (`text`: 1-64 characters for Code 128, 1-256 bytes for QR), `field` (`field` of the
dataset, bound as a text's field is: a group field or the same value in every row of the group, else INCONSISTENT_GROUP) or
`issue_number`; the key of the other two is empty. The alphabet is the symbology's: Code 128 takes printable ASCII (32-126) with
the code set chosen automatically, QR takes UTF-8 text in byte mode without control characters; a value outside it, or empty, is
refused with NOT_COVERED and one longer than the limit with DOES_NOT_FIT, naming the band and the element, never altered. The
encoders are `github.com/boombuler/barcode` v1.1.0 (MIT), pinned by go.mod and go.sum behind `export/barcode`; Go turns the modules
into rectangles at exact positions and Typst draws them: no raster code, no browser library and no Typst plugin in the formal path.
Scanability is a layout rule, never shrunk: the quiet zone lies inside the element's box (Code 128: ten modules on each side; QR: four);
the module is the box's width divided by the modules of the symbol and its quiet zones (the side for QR), equal everywhere, and at least
0.25 mm (Code 128) or 0.4 mm (QR); a Code 128 bar, the box's height less the line of the value when `human_readable` (1 pt and 1.46 type
sizes), is at least the larger of 5 mm and 15 % of the symbol's width. A value that would need narrower modules or lower bars is
refused with DOES_NOT_FIT and the element's name.

The issue number. A preview (`POST /document-previews`, the previews of a saved version) prints the placeholder `PREVIEW-NUMBER` in a text
and encodes it in a code, so that no preview carries a number-like value. The sample check (`POST /document-checks`) and the issue worker's
check, which run before the source product is asked for a number, take no number and prove the fit for every legal one (1-32 characters of
`A-Z a-z 0-9 . _ -`): a text with the widest number at its size and weight (a letter or digit first, then the widest characters of the
alphabet), a Code 128 with the most modules 32 such characters can need (32 symbols: the encoder's code sets never need more symbols than
characters), a QR code with 32 bytes at its level. A number the product assigns therefore cannot make the render fail after the reservation:
the proof is tested through Typst for the widest and the longest numbers at the tightest boxes the check accepts. The issue passes the number
the product reserved (`IssueRequest.Number`, which the issuer requires to be the stamp's) to the renderer, and the issued file shows it in every
such element; the checker decodes every code of the issued file and compares it with the record's number.

Images. An image is a private, immutable asset of the caller's identity and the template's binding (integration and dataset), kept beside the
backgrounds (the `images` directory of `-background-directory`) and named by an id and the SHA-256 of the bytes the platform keeps. An
upload is decoded completely and normalized: a PNG is written again by the standard encoder (chunks the renderer has no use for are
dropped), a JPEG (baseline or progressive, grey or YCbCr, not CMYK) keeps its entropy-coded data untouched and loses its metadata segments
(EXIF, XMP, ICC profiles, comments), because an issued document would carry them; the result must be at most 512 KiB and 2000 x 2000 pixels.
When the service has the renderer it draws the image on a probe page before storing it, so that one Typst cannot embed or whose PDF would
exceed the artifact limit is refused at upload with the renderer's code. At most 16 images are kept for an identity and a dataset (413 beyond),
and the shared store holds at most 16 records and 8 MiB per identity. A template, a preview, a check and an issue resolve the image with the
caller's identity and the template's binding: another identity's image is 404, another binding's 403, a digest other than the stored one 409; a
deployment without the store cannot draw one (422). An image of a seal or a signature is only a picture: the platform does not sign documents. The
PDF stays PDF/A-2b (veraPDF), including PNGs with an alpha channel, 16-bit and palette PNGs and JPEGs.

Authorization adds to 2.0's: the `field` of a barcode or QR code must be a column of the dataset the caller may read (403), as a text's.
`POST /document-checks` and `samples: "boundary"` also pick the groups with the longest field value of each code. A renderer that cannot draw
2.1 (an implementation of neither `RenderWith` nor `CheckIssueWith`/`IssueWith`) refuses such a template with 422 UNSUPPORTED_COMBINATION; the
PDF writer implements all three.

Configured fixture budgets: 1,000 rows, 1 MiB result, 8 MiB artifact, 50 source rows
per page, 20 s work timeout and 30 s repository lease. These are test-profile
limits, not production SLOs. A job that loses its lease or is cancelled cannot
publish. No retry/backoff or heartbeat is claimed.

Job error codes beyond the HTTP ones (a failed job's `error_code`): ISSUE_UNRECONCILED, an issue job that failed
after the source product may have reserved or confirmed a number for it and whose void was not done: it could not
be delivered, or the worker died before it was (the failure is recorded as this before the void is asked for), or
the job's own code could not be recorded after it. The product may still hold the number, which it finds by the job
id.

Errors: invalid request 400; missing/invalid authentication 401; scope denied 403;
owner-not-found 404; idempotency/integrity/lease conflict 409; unsupported shape
422; budget 413; source authority, or the object store (S04-E), unavailable 503; unclassified execution failure
500. Raw SQL errors, source rows and credentials are never error response bodies.
Missing/non-Bearer credentials receive the bare Bearer realm challenge; invalid
Bearer tokens receive invalid_token. Verified mapped tokens without the required
OAuth scope receive 403 insufficient_scope. Body errors remain generic.

Identity is platform tenant + issuer + subject; assets additionally bind
integration. Source tenant is a separate configured mapping. The same source
tenant/user name in two products never implies a grant. Source authorizations
are checked on admission, execution, final publication, polling and disclosure.
Cleanup after source business revocation is allowed while transport identity
still has the product connection; cleanup never returns snapshot/artifact IDs.

Cross-system revocation is checked immediately before commit and at every
disclosure, not an asserted distributed atomic transaction. Durable source
snapshots are qualified only in the protected S01-B simulator fixture. S02-A's
signature/audience/token-purpose/scope checks are locally qualified. Real
delegated subjects, issuer/membership authority and source authorization SLA
remain S02 contracts, not simulated by a service account.

## Layouts and presented exports (S23-A, docs/ADR_0024_TABLE_REPORT_FORMATTING.md, docs/S23A_SCOPE.md)

A layout (`layouts.Document`, routes `GET, POST /layouts`, `GET /layouts/{id}?version=N`, `GET, POST /layouts/{id}/release`,
`GET /layouts/{id}/published`; the schemas `LayoutDocument`, `LayoutFormat`, `LayoutMark` of docs/openapi.json) is a private design
bound to one integration and dataset. Schema 1.0 picks, orders and aligns (`left`, `right`) the columns. Schema 1.1 adds, all optional,
what the exports print:

```json
{
  "schema_version": "1.1",
  "binding": {"integration_id": "brant_finance", "dataset": {"id": "receivables.aging", "version": "1.0.0"}},
  "title": "应收账龄",
  "footnote": "小计为本表所列金额之和。",
  "columns": [
    {"id": "aging.bucket", "align": "center", "label": "账龄"},
    {"id": "customer.id", "align": "left"},
    {"id": "ar.open_balance", "align": "right", "format": {"grouping": true, "negative": "parentheses", "zero": "digits"}}
  ],
  "subtotals": {"by": "aging.bucket"},
  "marks": [
    {"column": "ar.open_balance", "when": "lt", "value": "0", "mark": "bad"},
    {"column": "aging.bucket", "when": "eq", "value": "current", "mark": "good"},
    {"column": "customer.id", "when": "blank", "mark": "note"}
  ]
}
```

- `title` 1..120 characters and `footnote` 1..400: literals printed above the table and under the total row, never a value of the data.
- A column's `label` (1..64) replaces the dataset's label and its currency and unit in the header; `align` gains `center`; `format` is
  allowed on a `decimal` or `integer` column only: `grouping` (default `true`), `negative` `minus` | `parentheses` (default `minus`), `zero`
  `digits` | `dash` (default `digits`; the dash is `—`). A format changes how the digits are shown, never which: no rounding, scaling or
  unit conversion.
- `subtotals.by` names a column of the layout that is a dimension of the dataset. The rows are partitioned by its exact frozen strings,
  the groups ordered by their first row and the snapshot's order kept inside, and each group is followed by a 小计 row with the exact
  decimal sum of each shown metric (the rule the document tables use for their totals). The source's 合计 row is shown as it came.
- `marks` is 1..8 rules `{column, when, value?, mark}`: `when` is `lt le gt ge eq ne` on a numeric column (an exact decimal comparison with
  `value`, an exact decimal string) or `eq ne blank` on any other column (an exact string comparison; `blank` is the empty string and takes
  no `value`); `mark` is `good`, `warn`, `bad` or `note`, a closed palette with one light colour each, the same on screen, in the PDF and in
  the workbook. The first rule that holds colours the cell; a mark never hides a row, changes a value or adds text.
- Bounds and refusals (400 INVALID_REQUEST): 100 columns, 8 marks, 64 KiB, unknown keys, a 1.0 document with a 1.1 key, a format on a
  column that is not numeric, a subtotal column that is not a dimension or not in the layout, a mark whose column is not in the layout or
  whose relation does not fit the column's type. A column the dataset does not have is 403, as before.

`POST /exports` with `layout_id`: the layout must be readable by the caller (the caller's own, or the tenant's shared space; else 404) and
bound to the snapshot's integration and dataset (else 422 UNSUPPORTED_COMBINATION); its latest saved version at the time of the request is
frozen on the job, as an issue job freezes its template, and the job view, `GET /jobs/{id}` and `GET /jobs` carry it:

```json
{"job_id": "…", "kind": "export", "state": "queued", "snapshot_id": "…", "format": "pdf", "layout": {"id": "…", "version": 3}}
```

The Idempotency-Key binds the layout version: the same key with the same version answers the same job; with another version (the layout
was saved again meanwhile), or without the layout, it is 409 IDEMPOTENCY_CONFLICT. The artifact records the layout id and version; the
workbook's `Metadata` sheet names them and the PDF's meta line says `布局 <id>@<version>`. Both exports show the layout's columns in its
order with its labels, alignments and formats, the subtotal rows (bold, under a rule), the marks as cell fills (text stays black), the
title above the table and the footnote under the total row; a PDF of more than 32 shown columns is refused as before. Without `layout_id`
the PDF is byte for byte what it was before S23-A (a golden test guards it), and the workbook is the whole snapshot. Unsaved edits of a
layout are not exported: the console sends the saved layout's id (S23-B). A server without a layout store answers 422 for a `layout_id`.

## Embedding (S03-N, ADR 0006, docs/EMBEDDING.md)

No API answer can be framed: every route of every mount answers with `X-Frame-Options: DENY` and `Content-Security-Policy: frame-ancestors 'none'`
(the one policy a route sets of its own, the image content route's, includes the same source), next to `Cache-Control: no-store`,
`X-Content-Type-Options: nosniff` and `X-Reporting-Data-Class` (its value depends on the profile: `contract-simulator`, or `business-data` on the production server). The one frameable answer of the platform is an embed page of the console
package (`GET /embed/report`, `/embed/dashboard`, `/embed/document-designer`), served only for an integration's configured origin and framed by
that one origin; the API itself is unchanged by embedding and needs no CORS: the embed page calls it from its own origin with the same
`Authorization: Bearer` header as the console. `GET /capabilities` carries `embedding: true|false` for the caller's integrations (`httpapi.WithEmbedding`;
the core API alone says false).

## Retention (S04-B, docs/S04B_SCOPE.md, docs/OPERATIONS.md "Retention")

A snapshot and the artifacts made from it live as long as the source read they were made from (the expiry every
download and page already enforces) plus a grace period of 24 hours by the store's clock; then a sweep deletes them.
An issue record is never deleted; a job keeps its row, and once its results are gone its payload is reduced to what
its view and the audit need (no query, no prepared read, no columns; the ids and the request hash stay).

What the API says, at each stage, for a job that succeeded:

- Read valid: as before; no `expired` field.
- Read expired, inside the grace: `GET /jobs/{id}` answers 200 with `expired: true`, `state: "succeeded"`, the same
  `snapshot_id`/`artifact_id` and, for an issue, `issue.number`; the source is not asked. `GET /issues` entries and
  `GET /issues/{id}` carry `expired: true` with the record unchanged. `GET /snapshots/{id}/rows` and
  `GET /artifacts/{id}/content` are refused as they always were for an expired read (409 INTEGRITY_FAILURE with the
  contract simulators).
- Swept: the same views with the same ids and `expired: true`; `GET /snapshots/{id}/rows`, `GET /artifacts/{id}/content`,
  `POST /exports` and `POST /issues` naming the snapshot are 404 NOT_FOUND; a request repeated with the Idempotency-Key
  of a swept job is 403 (as the replay of an expired job always was); `POST /jobs/{id}/cancel` answers the job unchanged.
  Never a 500, never an integrity failure caused by the sweep.

`expired` is present only when true and only on a job that succeeded; the console refuses any other value. A failed or
cancelled job is reduced like any other once its own read's expiry plus the grace is past; its view does not change.

## Quotas (S04-D, docs/S04D_SCOPE.md, docs/OPERATIONS.md "Quotas")

Every identity (tenant, issuer, subject) is bounded in what it may hold: `jobs_in_flight` (queued and running jobs, default 8),
`stored_results` (snapshots and artifacts not yet expired, default 64; an issue record never counts) and `admissions_per_hour` (jobs
admitted in the last hour, default 120), all by the store's clock, and, since S04-G, `queries_in_flight` (below). `GET /capabilities` carries
the numbers in force:

```json
"quotas": {"jobs_in_flight": 8, "stored_results": 64, "admissions_per_hour": 120, "queries_in_flight": 4}
```

A submission that would take the identity over one of them (`POST /snapshot-jobs`, `POST /exports`, `POST /issues`) is refused with

```
HTTP/1.1 429 Too Many Requests
Retry-After: 60
{"error":"QUOTA_EXCEEDED","quota":"jobs_in_flight"}
```

`quota` is one of `jobs_in_flight`, `stored_results`, `admissions_per_hour` (the first reached, in that order; `queries_in_flight` has its own refusal, below) and the body carries
nothing else: no count, no limit, nothing of another identity. Nothing is admitted and there is no `Location`. The refusal is
the identity's own state, so a later request may succeed: when a job finishes or is cancelled (`jobs_in_flight`), when results expire
(`stored_results`), or as the hour passes (`admissions_per_hour`); `Retry-After: 60` is a fair wait, not a promise. A replay of an
Idempotency-Key whose job exists is never refused by a quota (it answers the job as before), and a replay with a changed request is
still 409 `IDEMPOTENCY_CONFLICT`. Reads, cancels and downloads are not subject to the three durable quotas; `POST /queries` and the row reads have the fourth.

## Object storage (S04-E, docs/S04E_SCOPE.md, docs/OPERATIONS.md "Object storage")

With an object store configured the file of an artifact is an object and the row keeps its metadata and its SHA-256. For a caller nothing is renamed and no
view changes: the job view, `GET /issues` and `GET /capabilities` say nothing of where files live (no field, no word), and `GET /artifacts/{id}/content`
answers what it always did (the file, its exact `Content-Length`, `X-Content-SHA256`, the media type and the name), after the same checks: the source
product's current authority, the expiry, the identity's rows, and for an issued PDF the record it was issued under. What is new is only how a download
can fail:

- The object is not what the row says (another length, another SHA-256) or is missing: `409 {"error":"INTEGRITY_FAILURE"}`, never a file. The row's
  SHA-256 and length are what every byte served is checked against, in the object store client and again in the service.
- The object store cannot be reached: `503 {"error":"AUTHORITY_UNAVAILABLE"}` with `Retry-After: 10`. It is the code the source product's outage already
  has (the 503 of this API is "an authority or store the platform depends on is unavailable, try again"); the body is that code and nothing else, with no
  status of the store, no key and no endpoint. A source product's own outage keeps its 503 without a `Retry-After`.

A job poll (`GET /jobs/{id}`) reads rows only: it answers 200 whether or not the object store is up, and does not read the object, so a job whose object was
lost still shows `succeeded` and its download is the 409 above. An export or issue job whose file could not be put in the object store fails with
`error_code: "EXECUTION_FAILED"` and publishes nothing (an issue job is voided at the source product like any failed one, S03-H); the job view is otherwise
unchanged. An artifact stored before the object store was configured is served exactly as before. The console handles the 503 of a download as it handles
every 503 (it needs no change).

## Limits (S04-G, docs/S04G_SCOPE.md, docs/OPERATIONS.md "Capacity")

**Request bodies.** Every route that reads a body reads at most the bound of its route and refuses a larger one before parsing any of it:

```
HTTP/1.1 413 Request Entity Too Large
{"error":"REQUEST_TOO_LARGE"}
```

| Routes | Bound |
|---|---|
| `POST /queries`, `POST /snapshot-jobs` | 64 KiB |
| `POST /exports`, `POST /issues`, `POST /layouts/{id}/release`, `POST /dashboards/{id}/release`, `POST /document-templates/{id}/release`, `PUT /document-templates/{id}/catalog`, `POST /document-templates/samples/{kind}/previews` | 4 KiB |
| `POST /layouts`, `POST /dashboards`, `POST /document-templates`, `POST /document-templates/samples/{kind}/copy`, `POST /document-templates/{id}/previews`, `POST /document-previews`, `POST /document-checks`, `POST /document-backgrounds`, `POST /document-images` | 1 MiB (`MaxResultBytes`; an image or a background is base64 of at most 512 KiB, which fits) |
| `POST /jobs/{id}/cancel` | no body: one byte is read and a body is 400 |

A body over the bound is 413 whether or not it declares its length; a body of exactly the bound is read and judged as before (a malformed one is
400). `REQUEST_TOO_LARGE` is not `BUDGET_EXCEEDED` (a result over its budget).

**Requests in flight.** The server works on at most `-max-in-flight` API requests at once (default 128, 1..4096). Beyond it a request is refused
before authentication, with no work done (the session and capabilities requests count too):

```
HTTP/1.1 503 Service Unavailable
Retry-After: 1
{"error":"OVERLOADED"}
```

This 503 is the process's own; it is not the 503 of an unavailable authorization source or object store (those carry other codes and the object
store's has `Retry-After: 10`). The console shows "服务繁忙，请稍后再试" for it.

**Queries in flight.** `POST /queries` and `GET /snapshots/{id}/rows` share one per-identity bound in the service, `queries_in_flight` (default 4,
1..64; `-quota-queries`), counted per process. Beyond it:

```
HTTP/1.1 429 Too Many Requests
Retry-After: 1
{"error":"QUOTA_EXCEEDED","quota":"queries_in_flight"}
```

Nothing was run (the source product was not asked). Unlike the three durable quotas this one is not shared between processes.

## Browser sign-in (S15-A, ADR 0019, docs/S15A_SCOPE.md)

Mounted by `httpapi.WithSignIn` when the deployment configures `sign_in` (docs/DEPLOYMENT.md "Browser sign-in"). Without it the core API
answers `GET /sign-in` with `200 {"enabled": false}` (so that the console's question before sign-in is not a 404 on every page load) and
`POST /sign-in/token` is the mux's 404. Both routes are **unauthenticated**, carry the API's security headers and `Cache-Control: no-store`, and are inside
`max_in_flight`.

| Method and path | Request / response |
|---|---|
| GET /sign-in | No query (else 400). `200 {"enabled": true, "authorization_endpoint", "client_id", "redirect_uri", "scope", "code_challenge_method": "S256", "paste_fallback"}`: what the console needs to send the browser to the provider. Nothing secret and not the token endpoint |
| POST /sign-in/token | `{"code", "code_verifier", "nonce"}`, strict JSON, at most 4 KiB: a code of 1..2048 printable ASCII characters, a PKCE verifier of 43..128 of `[A-Za-z0-9._~-]`, a nonce of 22..128 of the same; anything else `400 INVALID_REQUEST` before any exchange. The server exchanges the code at the configured token endpoint with the verifier and the configured redirect URI, and answers `200 {"access_token", "expires_in"?}` only when its own verifier accepts the token; the token is then the Bearer of every request, like a pasted one. Refusals, each `{"error": CODE}` and nothing of the provider's answer: `400 SIGN_IN_REFUSED` (the provider refused the code, the verifier, the redirect URI or the client, or the ID token's nonce is not the one sent), `403 SIGN_IN_TOKEN_REFUSED` (the provider's token is not one this server accepts), `409 SIGN_IN_REPLAYED` (the code was already presented to this server, within ten minutes), `502 SIGN_IN_UNAVAILABLE` (no answer within the timeout, unreachable, an untrusted certificate, a status other than 200/400/401, a redirect, an answer over 64 KiB, not JSON or without a Bearer access token), `503 SIGN_IN_BUSY` with `Retry-After: 1` (16 exchanges at once in the process), `413 REQUEST_TOO_LARGE` |

The ID token and a refresh token, when the provider returns them, never leave the server. The server never redirects; the redirect URI is
the configured one, the console's page.

## Scheduled reports (S16-A, docs/S16A_SCOPE.md, docs/ADR_0020_SCHEDULED_REPORTS.md)

Mounted when the server's configuration has a `schedules` section (the demo: unless `-schedule-interval 0`). A schedule is the caller's: a query of
the published catalog (the same `QuerySpec` as `POST /snapshot-jobs`, checked against the catalog when it is made, no row read), an optional
`export_format` (`xlsx` or a format of `/capabilities`), an `as_of` rule and a `recurrence`:

```json
{"name": "应收周报", "query": {…}, "export_format": "xlsx", "as_of": "previous_day",
 "recurrence": {"frequency": "weekly", "at": "07:30", "weekday": 1, "timezone": "Asia/Shanghai"}}
```

`frequency` is `daily`, `weekly` (with `weekday` 1 Monday..7 Sunday) or `monthly` (with `day` 1..28); `at` is `HH:MM`; `timezone` an IANA zone
(`UTC`, `Asia/Shanghai`; not `Local`). A field of another form is 400. `as_of` is `run_date` (the default: each run reads as of the occurrence's
date in the zone), `previous_day`, or `fixed` (the query's own). At each occurrence the server submits the snapshot job as the schedule's owner,
exactly as the owner's `POST /snapshot-jobs` would be (the owner's binding in force then, the `viewer` role, the source product's authority, the
quotas), then the export. The jobs are the owner's: they are in `GET /jobs` and readable and cancellable by the owner. An occurrence missed while
no server ran is run once when one runs again; one missed while the schedule was paused is not. Runs end `succeeded`, `failed`, `refused` or
`cancelled` with a code (docs/S16A_SCOPE.md A2).

When the deployment configures a webhook, each run's end is POSTed to it, signed (`X-Reporting-Signature: sha256=<hex HMAC-SHA256 of the body>`,
`X-Reporting-Delivery: <run_id>`, `X-Reporting-Event: schedule.run.finished`), with the run's ids and outcome and never a value of the report;
a run's `delivery` says `pending`, `delivered` or `failed` (`none` without a deliverer). The endpoint is the deployment's: no route writes it,
and an administrator sees only its host (`GET /admin/configuration`). With `schedules.email` (S16-B, docs/S16B_SCOPE.md) the same end is also
mailed to the owner, at the `email` of the owner's binding in the bindings file (no route reads or writes it): the schedule's name, the
outcome, the ids and a link to the console's 定时报表 page, never a row, a query, an attachment or the file; `delivery` is the attempt as a
whole. An official issue is never scheduled. The console's 定时报表 page (S18-A) makes a schedule from the current query and lists the runs.

## The configuration in force (S18-B, docs/S18_SCOPE.md)

`GET /api/reporting/v1/admin/configuration` answers an administrator (the `administrator` role, else 403 `AUTH_ROLE_DENIED`, recorded as
`role.refused`) the configuration in force for their own tenant, read-only:

```json
{"tenant": "t1", "version": "v1.4.0", "started_at": "2026-11-09T08:00:00Z",
 "connections": [{"integration_id": "erp", "source_tenant": "acme", "adapter": "erp-http"}], "connections_sha256": "…",
 "bindings": [{"subject": "u1", "client_id": "console", "integrations": ["erp"], "roles": ["viewer", "administrator"]},
              {"subject": "u2", "client_id": "console", "integrations": ["erp"], "roles": null}],
 "bindings_sha256": "…", "bindings_loaded_at": "2026-11-09T09:30:00Z",
 "schedules": {"enabled": true, "per_identity": 20, "webhook": true, "webhook_host": "hooks.example.com"}, "sign_in": true}
```

`roles: null` is a binding that names none (the default roles). The digests are the ones the start and reload log lines give. Nothing of an
adapter's options, no secret, no key and of the webhook only its host. No route writes a connection or a binding (ADR 0016; ADR 0021 proposes
what could change). A query string is 400.

## Health and readiness (S05-A, docs/DEPLOYMENT.md)

The production server (`reporting-server`) mounts two routes outside `/api/reporting/v1`, ahead of the API and the console, **without authentication** and without touching the API's authentication or `max_in_flight`. They carry
the API's security headers and `Cache-Control: no-store`; any other method is 405 `{"error":"METHOD_NOT_ALLOWED"}`. The demo does not serve them.

| Method and path | Answer |
|---|---|
| GET /healthz | `200 {"status":"ok"}` while the process answers. No dependency is touched; it does not turn 503 during a drain (the listener closes instead) |
| GET /readyz | `200 {"status":"ready"}` when the store answers, its ledger is at the binary's version, the profile row is `production` and the connected role satisfies the application role's rules, the object store (when configured) answered its last check (cached 10 seconds), and the server is not draining. Otherwise `503 {"status":"not_ready","reason":"store"\|"schema"\|"roles"\|"object_store"\|"draining"}`; the body holds nothing else |

`GET /capabilities` of the production server shows its `profile_label` in `profile` (`simulated:<label>` when a contract simulator is allowed) and keeps `production_approved: false`.

## Page presets, the 2.x print profile and the limits of a 2.x template (S06-B)

A 2.x template may carry `page: {preset?, print?}` (at least one key, written between `binding` and `group`). `preset` is one of the rows of
`doctemplates.Presets`, the one table of the platform (the canvas, Typst and the checker take their numbers from it; the console's copy is
compared with it by a test): the template without a preset is A4 portrait, as every template stored before S06-B is, and a 1.x template has none.

| preset | page (mm) | safe region: left/right margin, top, bottom | banner strip (x, y, width x height) | stamp |
|---|---|---|---|---|
| `a4` (default) | 210 x 297 | 10, y 25-270 | 10, 10, 190 x 8 | 1 line, 8 pt |
| `a4_landscape` | 297 x 210 | 10, y 25-183 | 10, 10, 277 x 8 | 1 line, 8 pt |
| `a5` | 148 x 210 | 10, y 25-183 | 10, 10, 128 x 8 | 2 lines, 8 pt |
| `label_4x6in` | 101.6 x 152.4 | 4, y 12-146.4 | 4, 3, 93.6 x 6 | 2 lines, 6 pt |
| `label_100x150` | 100 x 150 | 4, y 12-144 | 4, 3, 92 x 6 | 2 lines, 6 pt |
| `label_100x100` | 100 x 100 | 4, y 12-94 | 4, 3, 92 x 6 | 2 lines, 6 pt |
| `label_150x100` | 150 x 100 (landscape) | 4, y 12-94 | 4, 3, 142 x 6 | 2 lines, 6 pt |

The bands span the safe region's width (page width less both margins) and the stack of bands is held to its height; the issue stamp strip exists on
every preset. Where the widest stamp the contract allows does not fit one line (a stamp is `ISSUED No. <number> - <time> - <id>`), it is broken after the
first ` - ` into two lines that read, joined by a space, as the stamp; a test proves the widest legal stamp fits every preset. A background must have the
aspect ratio of the template's preset (within 0.005; 210-1754 pixels each way).

`page.print: {offset_x, offset_y, variables_only, background?: {id, sha256}}` (every key but `background` required) is the 1.x print profile for a 2.x
template: the offsets (at most 10 mm, 0.1 mm grid) move every element, the table and its pages; the background is drawn under the page in full mode and left out
when `variables_only` is true; **in a variables-only print everything that is not a variable is left out** (literal texts and codes, lines, rectangles, images; the
table's rules, fills and header). The offset must leave everything printed inside the safe region, so a vertical offset needs room in the bands above or below the table.
Previews, checks, issues and the checker cover both modes.

`MaxItems` is 128 (64 before) and `MaxBandItems` 96 (24 before); element ids are `e1`-`e128`. docs/CAPACITY.md has the measured proofs.
