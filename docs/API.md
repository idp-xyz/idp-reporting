# API · S03-A console composition over the existing protected source profile

Base: `/api/reporting/v1`. This is the implemented subset, not a complete
production OpenAPI, OIDC profile or designer API. Bearer authentication is a
replaceable server-side port. `DemoBearer` is explicit synthetic identity only.
S02-A adds identity/accessjwt at that same port; see ACCESS_JWT.md. Provisioning
it does not change the simulator data classification or qualify a real issuer.

| Method and path | Request / response |
|---|---|
| GET /session | Trusted authenticated platform identity and integration IDs; no token, business data or service credentials |
| GET /capabilities | Exact simulator capability profile; production_approved=false |
| GET /catalog?integration_id=… | Only a product explicitly allowed for the authenticated principal |
| POST /queries | QuerySpec from S00 examples; typed columns, string cells, totals, completeness, issues, evidence |
| POST /snapshot-jobs | QuerySpec + one Idempotency-Key; 202 queued, Location header; no row reads during admission |
| GET /jobs/{id} | Owner-scoped status; current source authority even after success |
| POST /jobs/{id}/cancel | Empty body; idempotent cleanup; no output references returned. A running issue job is not cancelled (the source product may already hold a reservation for it): it is returned unchanged, and a queued one is cancelled |
| GET /snapshots/{id}/rows?limit=1&cursor=… | Frozen rows; total_row_count; HMAC cursor bound to owner/product/query/snapshot; limits 1..100 |
| POST /exports | `{ "snapshot_id": "…", "format": "pdf" }` + one Idempotency-Key; asynchronous. `format` is optional: absent or `xlsx` is the workbook, `pdf` needs a server started with PDF (listed in capabilities `formats`); other values are 422 UNSUPPORTED_COMBINATION. The job view carries `format` for non-workbook exports |
| GET /artifacts/{id}/content | Current whole-scope authority, expiry, private XLSX (`application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, `report-{id}.xlsx`) or PDF (`application/pdf`, `report-{id}.pdf`), exact byte length and SHA-256; an issued PDF is named `issued-<issue id>.pdf` (the console saves it as `issued-<number>.pdf`) |

The standalone HTML/CSS/JS console is served at /console/. Static assets contain
no credentials or private rows; they do not bypass protected API routes.

JSON POST bodies require application/json. Unknown/case-mismatched fields,
duplicate keys, null, extra roots, excessive nesting and >1 MiB bodies are rejected.
QuerySpec accepts only the dataset/version, metrics, dimensions, timezone and
currency policy declared by a published catalog entry; filters and sort are
explicitly unsupported in this slice. Company IDs are constrained selectors.

Cells and totals are strings with declared types (string/date/boolean/integer/
decimal), scale, currency and unit. No float64 business conversion. Totals cover
the full original authorized result. A truncated preview says `preview`; a
partial snapshot page says `page`; all rows says `complete`. Snapshot paging does
not reread the source. Cursor alone confers no access.

Every XLSX cell is explicit text, with Data/Totals/Metadata sheets. Exact decimals,
negative values and integers beyond JavaScript/Excel precision survive unchanged.
External strings beginning '=' do not generate formulas. Consumers wanting Excel
arithmetic must use an approved typed import; automatic numeric coercion is not
promised by this slice.

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

Document templates (S03-E, docs/S03E_SCOPE.md; multi-page tables S03-F, docs/S03F_SCOPE.md) are mounted when the
demo has the matching directories, and previews when it has the PDF flags:

| Method and path | Request / response |
|---|---|
| POST /document-previews | `{config, snapshot_id, row_index}`; one A4 PDF/A-2b preview of the template bound to that frozen row and the whole-scope totals; `application/pdf`, `no-store`, `X-Content-SHA256` |
| POST /document-checks | Sample check (S03-I, docs/S03I_SCOPE.md): `{config, snapshot_id, rows}` (1–50 distinct row indices) or `{config, snapshot_id, samples: "boundary"}`, never both; answers `200 {"rows": [{row_index, ok, code?, detail?, reasons?}]}` in row order, `no-store`, without rendering (`detail`: S03-J, below). Mounted with the previews when the renderer can check (the PDF writer can) |
| GET, POST /document-templates | List authorized templates; save `{id, expected_version, config}` as a new immutable version |
| GET /document-templates/{id}?version=N | One saved version, after current field authorization |
| GET, POST /document-templates/{id}/release | Release history; `{action: publish|withdraw, version, expected_revision}` |
| GET /document-templates/{id}/published | The published version |
| POST /document-templates/{id}/previews | `{version, snapshot_id, row_index}`; previews the stored version, never caller configuration |
| GET, POST /document-backgrounds | List authorized PNG backgrounds; upload `{binding, png}` (base64, 512 KiB) and get `{id, sha256}` |
| POST /issues | Official issue under the source product's authority (S03-G, docs/S03G_SCOPE.md; S03-H, docs/S03H_SCOPE.md, docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md): `{template_id, version, snapshot_id, row_index}` (a schema 2.0 template: `row_index` or `group`, see "Issue by group" below) with an Idempotency-Key admits a job of kind "issue" (202, `Location: /jobs/{id}`); the version must be the published one (409 TEMPLATE_NOT_PUBLISHED), the row must exist (400), the fields and background must be authorized (403), and the snapshot's integration must have an adapter that can issue, else 422 UNSUPPORTED_COMBINATION, nothing admitted; the job view carries `issue: {template_id, template_version, template_sha256, row_index}` (a group issue: `group` in place of `row_index`), and the product's `number` once the job succeeded (read from its record), wherever the job is shown: the 202 of a request repeated with the key of a job that has succeeded (a client whose answer was lost asks again), `GET /jobs/{id}` and the 200 of `POST /jobs/{id}/cancel` |
| GET /issues?limit=N | The caller's issue jobs, newest first (N 1..100, default 25): `issue_id, state, error_code, template_*, snapshot_id, row_index` (a group issue: `group`) for every job, and, once it succeeded, its record: `number` (the product's string, such as `AR-000012`), `artifact_id, snapshot_sha256, sha256, pages, stamp, issued_at` and the product's references `reservation_ref, confirmation_ref, confirmed_at` (RFC 3339, UTC). A queued, running, failed or cancelled job has none of them; a failed job shows no number even when the product had reserved one (it is voided there), unless it failed with ISSUE_UNRECONCILED, where the product may still hold it |
| GET /issues/{id} | One issue job with its record; 404 for any other job or another identity's. The listing and the record read fail closed: a record that cannot be read as published (tampered, missing) is 409 INTEGRITY_FAILURE for the whole page. A succeeded issue job's `GET /jobs/{id}` re-authorizes the source and checks the download like an export's, so once the snapshot expired it is refused (403 with the contract simulators, whose adapter denies an expired read; 409 for the download), while `GET /issues/{id}` still answers; the job view also requires the artifact to claim the record and the record to name the artifact (409 otherwise) |

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
`{elements}`. Bands lie inside the safe region (x 10-200, y 25-270 mm) and the strip 10-18 mm stays the banner's or the
stamp's. The heights of the page header, document header and page footer plus the least detail band (the table's
header, brought-forward, one body and carry-forward row, one line each at the table's type size) must fit the safe
region. Element positions are in millimetres on the 0.1 mm grid, relative to the band (which starts at x 10 mm); ids
`e1`-`e64` are unique in the template, at most 64 elements and 24 per band, a template at most 64 KiB, at most 12 table
columns. Elements, each with exactly the keys of its `type` (unknown keys, unknown types and the reserved `barcode`,
`qrcode` and `image` are refused):

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
422; budget 413; source authority unavailable 503; unclassified execution failure
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
