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
| POST /jobs/{id}/cancel | Empty body; idempotent cleanup; no output references returned |
| GET /snapshots/{id}/rows?limit=1&cursor=… | Frozen rows; total_row_count; HMAC cursor bound to owner/product/query/snapshot; limits 1..100 |
| POST /exports | `{ "snapshot_id": "…", "format": "pdf" }` + one Idempotency-Key; asynchronous. `format` is optional: absent or `xlsx` is the workbook, `pdf` needs a server started with PDF (listed in capabilities `formats`); other values are 422 UNSUPPORTED_COMBINATION. The job view carries `format` for non-workbook exports |
| GET /artifacts/{id}/content | Current whole-scope authority, expiry, private XLSX (`application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, `report-{id}.xlsx`) or PDF (`application/pdf`, `report-{id}.pdf`), exact byte length and SHA-256 |

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

Document templates (S03-E, docs/S03E_SCOPE.md) are mounted when the demo has the matching directories, and
previews when it has the PDF flags:

| Method and path | Request / response |
|---|---|
| POST /document-previews | `{config, snapshot_id, row_index}`; one A4 PDF/A-2b preview of the template bound to that frozen row and the whole-scope totals; `application/pdf`, `no-store`, `X-Content-SHA256` |
| GET, POST /document-templates | List authorized templates; save `{id, expected_version, config}` as a new immutable version |
| GET /document-templates/{id}?version=N | One saved version, after current field authorization |
| GET, POST /document-templates/{id}/release | Release history; `{action: publish|withdraw, version, expected_revision}` |
| GET /document-templates/{id}/published | The published version |
| POST /document-templates/{id}/previews | `{version, snapshot_id, row_index}`; previews the stored version, never caller configuration |
| GET, POST /document-backgrounds | List authorized PNG backgrounds; upload `{binding, png}` (base64, 512 KiB) and get `{id, sha256}` |

A preview places each value on one line with its baseline 1.16 font sizes below the top of its box, at the box's
left edge, right edge or centre, every character at the position the fonts' plain advances give it (ADR 0002);
trailing white space is not drawn. A value the fonts do not cover, a soft hyphen, a Hangul tone mark, a value
that does not fit its box, a snapshot value with control characters or over 256 characters, or one Typst would
draw wider than measured is 422 UNSUPPORTED_COMBINATION; a preview that waits longer than the render timeout for
one of the renderer's processes is 413 BUDGET_EXCEEDED. Previews are never issued documents and are not stored. A
background upload is tried with the renderer and refused when its preview would exceed the artifact limit.

Configured fixture budgets: 1,000 rows, 1 MiB result, 8 MiB artifact, 50 source rows
per page, 20 s work timeout and 30 s repository lease. These are test-profile
limits, not production SLOs. A job that loses its lease or is cancelled cannot
publish. No retry/backoff or heartbeat is claimed.

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
