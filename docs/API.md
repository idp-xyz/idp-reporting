# API · S01-B source profile with S02-A composable authentication

Base: `/api/reporting/v1`. This is the implemented subset, not a complete
production OpenAPI, OIDC profile or designer API. Bearer authentication is a
replaceable server-side port. `DemoBearer` is explicit synthetic identity only.
S02-A adds identity/accessjwt at that same port; see ACCESS_JWT.md. Provisioning
it does not change the simulator data classification or qualify a real issuer.

| Method and path | Request / response |
|---|---|
| GET /capabilities | Exact simulator capability profile; production_approved=false |
| GET /catalog?integration_id=… | Only a product explicitly allowed for the authenticated principal |
| POST /queries | QuerySpec from S00 examples; typed columns, string cells, totals, completeness, issues, evidence |
| POST /snapshot-jobs | QuerySpec + one Idempotency-Key; 202 queued, Location header; no row reads during admission |
| GET /jobs/{id} | Owner-scoped status; current source authority even after success |
| POST /jobs/{id}/cancel | Empty body; idempotent cleanup; no output references returned |
| GET /snapshots/{id}/rows?limit=1&cursor=… | Frozen rows; total_row_count; HMAC cursor bound to owner/product/query/snapshot; limits 1..100 |
| POST /exports | `{ "snapshot_id": "…" }` + one Idempotency-Key; asynchronous XLSX only |
| GET /artifacts/{id}/content | Current whole-scope authority, expiry, private XLSX, exact byte length and SHA-256 |

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
