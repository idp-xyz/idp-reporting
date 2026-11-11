# S19-A · A generic HTTP source adapter: the reporting source protocol

2026-11-09. The product owner asked that a source product connect by implementing a fixed HTTPS protocol instead of the platform writing Go
code for each product. This slice writes the protocol, the platform's adapter of it, a reference source, a conformance checker, and
registers the adapter in the shipped server. ADR 0022 records the decision.

## What

A1 · **The protocol** (docs/SOURCE_HTTP_PROTOCOL.md; docs/source-openapi.json, held to the wire types by
`TestTheOpenAPIDescriptionIsTheProtocol`). JSON over HTTPS, every route `POST` under `/reporting-source/v1/`: `catalog`, `reads`,
`reads/{ref}/pages`, `reads/{ref}/authorize`, and for a connection that issues `issues/{job_id}/prepare|confirm|void`. Every request carries
the source context (platform tenant, issuer, subject, integration, source tenant) in its body, a random `X-Request-Id`, `traceparent` when
there is a trace, and the service credential (bearer and/or mTLS). Refusals are `{"code": …}` of a closed set: `invalid` 400, `denied` 403,
`not_found` 404 and `conflict` 409 (issue routes only), `expired` 410, `unsupported` 422, `integrity` 422, `budget` 429, `unavailable` 503.
Values are exact strings; times are canonical RFC 3339; a read is sent back unchanged. Bounds: 1 MiB each way, 100 datasets, refs 128,
scope handles 1024, watermarks 256, cursors 2048, 100 rows a page, a read at most 15 minutes ahead. The wire types are
`adapters/httpsource/protocol`, shared by the adapter, the reference source and the checker.

A2 · **The adapter** `adapters/httpsource` (standard library and the module's own `reporting`, `tracecontext` only): `reporting.Adapter`;
with `issuance: rows` also `DocumentAuthority`, with `groups` also `VersionedAuthority` (contract version 2). Options: `base_url`,
`loopback_http`, `timeout`, `ca_file`, `client_cert_file`/`client_key_file`, `auth_env`, `max_response_bytes`, `issuance`
(docs/DEPLOYMENT.md "Connecting a source product over HTTP"). Every answer is verified against what was asked (A1's page lists it); what does not
verify is `INTEGRITY_FAILURE`, never passed on; timeouts, 5xx, 401, redirects and answers outside the protocol are `AUTHORITY_UNAVAILABLE`. The
datasets the catalog answered are kept in memory (bounded) so that each page is checked against its dataset's columns, and the catalog is asked
again when a page's dataset is not known. Errors are `*httpsource.Error`: operation, fixed reason, request id, the platform error; two of them
are `errors.Is`-equal when their platform errors are, as an in-process adapter's sentinels are. Safe for concurrent use.

A3 · **The core** gains one exported function, `reporting.ValidateSourcePage` (the service's own row and total checks for a dataset), so that
the adapter verifies a page with exactly the service's rules.

A4 · **The shipped server** registers `http` (`server.DefaultRegistry`, `cmd/reporting-server`). It is not a simulator. Its options are read
strictly at start; a bad object refuses the start at the adapters step with the fixed text, never the value.

A5 · **The reference source** `adapters/httpsource/sourcetest`: an `http.Handler` that serves the protocol from any `reporting.Adapter` (and
its document authority), with an optional bearer check and mTLS requirement; `sourcetest.Remote` puts an adapter behind it on loopback TLS and
returns the platform's `http` adapter of it.

A6 · **The checker** `cmd/reporting-source-check` (library `adapters/httpsource/conformance`): 20 rules through the platform's own adapter
(P1, A1, C1–C4, R1–R13, I1 with `-issue`), `PASS`/`FAIL`/`SKIP` with fixed texts, exit 0/1/2 (docs/SOURCE_HTTP_PROTOCOL.md section 7).

A7 · Not done: the company directory (ADR 0015 was not in the core yet; S19-B added it, with `POST /reporting-source/v1/companies`,
docs/S19B_SCOPE.md); a demo flag that puts the demo's simulator behind the protocol (not needed: the tests do it); a health probe of a source.

## Evidence

- The whole repository contract (`runContract`, 21 scenarios: two domains, owner axes, revocation, currency and zone changes, outage, leases,
  listing, group issue records) runs through the protocol: `TestMemoryContractOverHTTP`, and `TestPostgresContract/over_http` on
  PostgreSQL.
- The document authority's conformance suite (`authoritytest.Run` and `RunConcurrent`) runs through the protocol unchanged:
  `TestDocumentAuthorityOverHTTP`, `TestDocumentAuthorityConcurrentCallsOverHTTP`. Running it found that the protocol needed `not_found`
  for a confirmation of an unknown job, a void reason of more than ASCII, and a local refusal of a malformed group (JSON would have
  rewritten invalid UTF-8): all three are in the protocol now.
- The service in process and over the protocol give the same outcomes: a 120-row snapshot paged in three source pages with one watermark while
  the source's data changes, revocation, expiry, a new read on new data; an issue confirmed, and a failed render voided
  (`TestTheServiceFreezesAndPagesASnapshotThroughTheProtocol`, `TestTheServiceIssuesThroughTheProtocol`, each `in_process` and `over_http`).
- Hostile sources: every status and body of the error table and outside it, 14 tampered reads, 15 tampered pages, a cursor that does not move,
  three non-authorizations, 7 broken catalogs, malformed JSON (trailing data, duplicate keys, nulls, other cases, another content type),
  answers over the bound (also 8 MiB), five redirect statuses never followed, a source that never answers and one whose body never ends, the
  caller's own deadline, mTLS required, a CA pin, TLS 1.1 refused, plain HTTP only to loopback with the flag, 26 configuration refusals, no
  credential, URL or body marker in any error (`adapters/httpsource`).
- The shipped server: an `http` connection starts with simulators disallowed, serves the catalog, a query, a snapshot job, refuses rows after
  a revocation (403) and answers 503 when the source is gone, and its log has neither the credential nor the address; five bad options
  refuse the start without the value (`TestTheShippedRegistryServesAnHTTPSourceEndToEnd`, `TestABadHTTPConnectionRefusesTheStartAndIsNeverShown`).
- The checker passes the reference source (20 of 20 with `-issue`), fails a source without a credential check and with a lax authorization
  on exactly A1, R7–R10, and fails a closed source at C1 (`cmd/reporting-source-check`).

## Known

- `Evidence.DataClass` in a result is the core's fixed `contract-simulator` whatever the adapter (the HTTP header `X-Reporting-Data-Class`
  is `business-data` on a production server). Changing it changes the digest of new snapshots; it is left for its own change.
- No request signing beyond TLS; the service credential authenticates the platform for every context (ADR 0022 Consequences).
- Not tested against a real product.
