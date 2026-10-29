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
| POST /document-checks | Sample check (S03-I, docs/S03I_SCOPE.md): `{config, snapshot_id, rows}` (1–50 distinct row indices) or `{config, snapshot_id, samples: "boundary"}`, never both; answers `200 {"rows": [{row_index, ok, code?, reasons?}]}` in row order, `no-store`, without rendering. Mounted with the previews when the renderer can check (the PDF writer can) |
| GET, POST /document-templates | List authorized templates; save `{id, expected_version, config}` as a new immutable version |
| GET /document-templates/{id}?version=N | One saved version, after current field authorization |
| GET, POST /document-templates/{id}/release | Release history; `{action: publish|withdraw, version, expected_revision}` |
| GET /document-templates/{id}/published | The published version |
| POST /document-templates/{id}/previews | `{version, snapshot_id, row_index}`; previews the stored version, never caller configuration |
| GET, POST /document-backgrounds | List authorized PNG backgrounds; upload `{binding, png}` (base64, 512 KiB) and get `{id, sha256}` |
| POST /issues | Official issue under the source product's authority (S03-G, docs/S03G_SCOPE.md; S03-H, docs/S03H_SCOPE.md, docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md): `{template_id, version, snapshot_id, row_index}` with an Idempotency-Key admits a job of kind "issue" (202, `Location: /jobs/{id}`); the version must be the published one (409 TEMPLATE_NOT_PUBLISHED), the row must exist (400), the fields and background must be authorized (403), and the snapshot's integration must have an adapter that can issue, else 422 UNSUPPORTED_COMBINATION, nothing admitted; the job view carries `issue: {template_id, template_version, template_sha256, row_index}`, and the product's `number` once the job succeeded (read from its record), wherever the job is shown: the 202 of a request repeated with the key of a job that has succeeded (a client whose answer was lost asks again), `GET /jobs/{id}` and the 200 of `POST /jobs/{id}/cancel` |
| GET /issues?limit=N | The caller's issue jobs, newest first (N 1..100, default 25): `issue_id, state, error_code, template_*, snapshot_id, row_index` for every job, and, once it succeeded, its record: `number` (the product's string, such as `AR-000012`), `artifact_id, snapshot_sha256, sha256, pages, stamp, issued_at` and the product's references `reservation_ref, confirmation_ref, confirmed_at` (RFC 3339, UTC). A queued, running, failed or cancelled job has none of them; a failed job shows no number even when the product had reserved one (it is voided there), unless it failed with ISSUE_UNRECONCILED, where the product may still hold it |
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
