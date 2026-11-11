# The reporting source protocol, version 1 (S19-A; revision 1.1, S19-B)

2026-11-09; revision 1.1 on 2026-11-11 (S19-B) adds one optional route, `companies`, and changes nothing else: a source written to the
first revision is served unchanged. What a source product implements so that the reporting platform reads it, through the platform's
adapter `http` (`adapters/httpsource`), without the platform carrying Go code for the product. It is the adapter contract of
docs/s00/INTERFACES.md (`DescribeCatalog`, `PrepareRead`, `ReadPage`, `AuthorizeCurrent`) and, optionally, the document authority of ADR
0003 (`PrepareDocument`, `ConfirmIssue`, `VoidIssue`) and the company directory of ADR 0015 (`ListCompanies`), carried as JSON over
HTTPS. The machine-readable description is docs/source-openapi.json
(`TestTheOpenAPIDescriptionIsTheProtocol` holds it to the bodies the adapter sends and accepts); this page wins where they differ. The
obligations behind each call (what a stable read is, what "covers the frozen scope" means, how numbers are accounted for) are those of
docs/REAL_SOURCE_ONBOARDING.md and ADR 0003; this page says how they travel.

A product proves its implementation with `reporting-source-check` (below) before a connection is configured (docs/DEPLOYMENT.md, go-live
item 17). `adapters/httpsource/sourcetest` is a reference implementation in Go over any `reporting.Adapter`; the platform's tests run the
contract simulators behind it and the platform behaves identically (docs/S19A_SCOPE.md).

## 1. Transport

- **HTTPS**, TLS 1.2 or later, a certificate the platform's `ca_file` (or the system's roots) vouches for. Plain HTTP is accepted by the
  platform only to a loopback IP address with `loopback_http`, for tests.
- **Every route is `POST`** under `{base_url}/reporting-source/v1/`, with a JSON body. Nothing of a request is in the query string: the
  identity of a person never appears in a URL, a proxy log or an access log line by the URL.
- **Authentication of the platform** (the service, never a person): `Authorization: Bearer <credential>` with the credential the operator
  gave both sides, and/or a client certificate (mTLS). A request without a valid one is **401** (a body is not needed). The platform
  treats 401 as "the source is unavailable" and never retries it as another identity.
- **Headers the platform sends**: `Content-Type: application/json`, `Accept: application/json`, `X-Request-Id` (32 lowercase hexadecimal
  characters, random per request: log it, so one refusal can be found on both sides), and `traceparent` (W3C Trace Context) when the
  platform's request carried a trace. The source may echo `X-Request-Id`; the platform does not require it.
- **Redirects are never followed**: a 3xx is "unavailable".
- **Answers**: `200` with `Content-Type: application/json` (`charset=utf-8` or none), **at most 1 MiB** (the connection's
  `max_response_bytes` may lower it), exactly one JSON object: no unknown field, no field spelled in another case, no `null`, no duplicate
  key, no nesting deeper than 32, nothing after the object. Required fields are present (docs/source-openapi.json lists them). Requests the
  platform sends follow the same rules and are at most 1 MiB; a source should refuse anything else with `invalid`.
- **Time**: the platform's request has a timeout (the connection's `timeout`, 10 s by default, at most 60 s) from connection to the last byte
  of the answer. The platform never retries a request itself; its worker re-runs a job after a lease runs out, which may repeat a request
  (see Idempotency).
- **Values** are strings: every cell of a row and every total is the exact text the column's type allows (an integer `-?(0|[1-9][0-9]*)`, a
  decimal with exactly the column's `scale` digits after the point, a date `YYYY-MM-DD`, `true`/`false`, any UTF-8 string of at most 4096
  bytes without control characters other than tab, CR and LF). Never a JSON number for a value.
- **Times** are RFC 3339 in the canonical form Go's `time.RFC3339Nano` writes: no trailing zeros in a fraction (`2026-11-09T10:00:00Z`,
  `2026-11-09T10:00:00.5Z`, `2026-11-09T18:00:00+08:00`), so that a time the platform stores and sends back is the same text. UTC with `Z`
  is recommended.

## 2. The source context

Every request carries the context explicitly:

```json
"context": {"platform_tenant": "acme", "issuer": "https://idp.example.com", "subject": "u-123", "integration": "erp-finance", "source_tenant": "acme-prod"}
```

`platform_tenant`, `issuer` and `subject` are what the platform verified in the person's access token; `integration` and `source_tenant` are
the connection's. **The source maps (`platform_tenant`, `issuer`, `subject`) to its own actor on its own side**, by its own table, and
decides every request by that actor's current rights. It must not trust anything else of a request for authority: `company_ids` are a
selector, a `read` sent back is checked against the source's own record, and an unknown subject or a `source_tenant` that is not the one
configured for the credential is `denied`. The platform never sends the person's access token.

## 3. Routes

| Route | Contract operation | Body → answer |
|---|---|---|
| `POST /reporting-source/v1/catalog` | `DescribeCatalog` | `{context}` → `{datasets: [Dataset]}` |
| `POST /reporting-source/v1/companies` | `ListCompanies` (optional, revision 1.1) | `{context}` → `{companies: [{id, name}], partial}` |
| `POST /reporting-source/v1/reads` | `PrepareRead` | `{context, query, query_hash}` → `{read: Read}` |
| `POST /reporting-source/v1/reads/{ref}/pages` | `ReadPage` | `{context, read, cursor, limit}` → `{ref, query_hash, watermark, rows, totals, next_cursor}` |
| `POST /reporting-source/v1/reads/{ref}/authorize` | `AuthorizeCurrent` | `{context, read}` → `{authorized: true}` |
| `POST /reporting-source/v1/issues/{job_id}/prepare` | `PrepareDocument` | `{context, intent}` → `{reservation}` |
| `POST /reporting-source/v1/issues/{job_id}/confirm` | `ConfirmIssue` | `{context, confirmation}` → `{receipt}` |
| `POST /reporting-source/v1/issues/{job_id}/void` | `VoidIssue` | `{context, job_id, reason}` → `{voided: true}` |

The issue routes are needed only for a connection whose `issuance` is `rows` or `groups`; the `companies` route only for a connection with
`companies: true` (the platform never asks it otherwise). `{ref}` and `{job_id}` repeat a value of the body (`read.ref`, `intent.job_id`,
`confirmation.job_id`, `job_id`); a source refuses a mismatch with `invalid`.

### catalog

The published, approved dataset versions the context may read now (docs/API.md "Catalog"), at most 100, no `dataset` reference twice:

```json
{"datasets": [{"dataset": {"id": "receivables.aging", "version": "1.0.0"},
  "columns": [{"id": "customer.id", "label": "客户", "type": "string"}, {"id": "aging.bucket", "label": "账龄区间", "type": "string"},
              {"id": "ar.open_balance", "label": "应收余额", "type": "decimal", "scale": 2, "currency": "CNY"}],
  "metrics": [{"id": "ar.open_balance", "version": "1.0.0"}], "dimensions": ["customer.id", "aging.bucket"],
  "currency_policy": {"id": "ledger_base", "version": "1.0.0"}, "timezone": "Asia/Shanghai"}]}
```

The platform requires of each dataset: `columns` are the `dimensions` then the `metrics` (by id) in that order, 1..64 of them, unique ids, a
label of 1..256 bytes, a type of `string`, `date`, `boolean`, `integer` (no scale) or `decimal` (scale 0..18), `currency`/`unit` at most 32
bytes; `timezone` an IANA zone; ids and versions 1..128 printable ASCII. A dataset version is immutable: a change is a new version.

### companies (optional; ADR 0015, revision 1.1, S19-B)

The companies the context may choose **now**, under the same authority as the catalog (an unknown subject or another source tenant is
`denied`), each with the id a query names in `company_ids` and the name a person reads, in the product's order:

```json
{"companies": [{"id": "company_demo", "name": "演示公司"}, {"id": "company_7", "name": "第七分公司"}], "partial": false}
```

The platform requires: at most 1000 entries; each `id` and each `name` 1..128 bytes of UTF-8 text without control characters (C0, DEL,
U+FFFE, U+FFFF) and not blank; no `id` twice; both fields present (`partial` says whether the product cut the list: then the person may
still type an id). An empty list is an answer: nothing may be chosen. The list is **advisory**: `reads` and `authorize` decide every read
exactly as without it, so a company the product listed may be refused a moment later when the rights changed, and one it did not list is
refused as today. The platform does not store the list, does not cache it across requests and never logs a name; a frozen read keeps
company ids, never names. A source without the capability answers `unsupported`; the platform asks the route only for a connection with
`companies: true` (docs/DEPLOYMENT.md), and answers its console `supported: false` for every other connection.

### reads

`query` is the platform's query exactly as docs/API.md defines it (companies sorted; `filters` and `sort` empty in version 1);
`query_hash` is its digest (`reporting.QueryHash`: the SHA-256, lowercase hex, of the query's JSON without `page`). The source checks the
query against the dataset and the context's current rights **in its own trusted transaction**, freezes the data version, and answers:

```json
{"read": {"ref": "r-7f3a", "scope_handle": "opaque-to-the-platform", "query_hash": "57ef…25", "dataset": {"id": "receivables.aging", "version": "1.0.0"},
  "company_ids": ["company_demo"], "timezone": "Asia/Shanghai", "currency_policy": {"id": "ledger_base", "version": "1.0.0"},
  "watermark": "ledger-2026-11-09T09:58:00Z", "expires_at": "2026-11-09T10:08:00Z"}}
```

The platform verifies, and refuses with an integrity failure otherwise: `query_hash` is the request's; `dataset`, `company_ids` (same order),
`timezone` and `currency_policy` (absent when the query has none) are the query's; `ref` is 1..128 of `A-Z a-z 0-9 . _ ~ -`;
`scope_handle` 1..1024 and `watermark` 1..256 printable ASCII; `expires_at` canonical, after the platform's clock and **at most 15 minutes
ahead** (an already expired read is a refusal). The platform keeps the read and sends it back, every field unchanged, in each later
request about it. A source answers `integrity` when `query_hash` is not the query's, `unsupported` for a dataset version or a combination
it does not publish, `denied` for a company outside the actor's rights, `invalid` for a query that is not well formed.

### pages

```json
{"context": {…}, "read": {…}, "cursor": "", "limit": 50}
→ {"ref": "r-7f3a", "query_hash": "57ef…25", "watermark": "ledger-2026-11-09T09:58:00Z",
   "rows": [["customer_a", "current", "100.25"], ["customer_b", "1_30", "-0.24"]], "totals": {"ar.open_balance": "9007199254740993.01"},
   "next_cursor": "r-7f3a:2"}
```

The source checks the `read` against its own record (the platform sends it back unchanged; a different field is `denied`), re-authorizes the
actor (the same rule as `authorize`) and projects the **frozen** version: data that changed since the read was prepared does not reach a
later page. The platform verifies: `ref`, `query_hash` and `watermark` are the read's; at most `limit` rows (1..100; the platform asks 50);
every row has the dataset's columns and every cell its column's type; `totals` holds one total per metric, of the metric column's type, the
**same on every page** (the totals of the whole read); `next_cursor` is `""` on the last page, otherwise 1..2048 printable ASCII, never the
request's cursor and never after an empty page; a cursor that comes back is a loop. The whole read is at most 1000 rows and 1 MiB. A
cursor the source did not give is `invalid`.

### authorize

`{"authorized": true}` when the actor's **current** rights still cover the whole frozen scope (every company, every row the read was made
on, the zone and the currency policy), otherwise a refusal (`denied`, or `expired` for a read the source no longer holds). Widened rights
never widen an old read; narrowed rights refuse it rather than filter it. The platform calls it before every disclosure (a page of rows, an
export, an issue) and after the last page. Any other `200` body is an integrity failure.

### The document authority (optional; ADR 0003, S03-H, S03-K)

The source authorizes a formal document, owns its number and confirms the file; the platform renders it and keeps it. Every call is
**idempotent by the platform's job id**:

- `prepare` with `{"intent": {"job_id", "read", "row_index", "row_sha256", "template_id", "template_version", "template_sha256"}}` (a row,
  contract version 1) or, for a connection with `issuance: groups`, the same with `"group": {"fields", "key", "rows", "rows_sha256"}`,
  `row_index` 0 and `row_sha256` "" (contract version 2). `row_sha256` and `rows_sha256` are the digests docs/API.md defines; the source
  checks them against **its own** frozen rows of `read` and refuses (`denied`) when they differ, when the actor may not issue, when its
  approval rules say no. It answers `{"reservation": {"ref", "job_id", "number", "issued_at", "expires_at"}}`: `ref` 1..128 printable ASCII,
  `job_id` the intent's, `number` 1..32 of `A-Z a-z 0-9 . _ -` starting with a letter or digit, `issued_at` a whole second, `expires_at`
  after it. The same job with the same intent answers the same reservation; another intent under that job id, or a voided job, is
  `conflict`; an expired reservation is `expired`.
- `confirm` with `{"confirmation": {"job_id", "reservation_ref", "sha256", "pages"}}` answers `{"receipt": {"ref", "confirmed_at"}}`
  (`confirmed_at` a whole second, not before `issued_at`). The same digest again answers the same receipt; another digest, or a voided
  reservation, is `conflict`; an expired, unconfirmed one `expired`; a job the source never reserved `not_found`.
- `void` with `{"job_id", "reason"}` (`reason` the platform's error code, UTF-8, at most 1024 bytes; keep what fits) voids whatever the
  source holds for the job, reserved or confirmed, and answers `{"voided": true}`. Holding nothing is not an error, voiding twice is not
  either, and a void is **final** for the job id: a later `prepare` of it is `conflict`. It does not depend on the actor's current rights.

## 4. Errors

Every refusal is the status of its code with exactly this body: `{"code": "<code>"}`. The set is closed:

| Code | Status | Platform error | When |
|---|---|---|---|
| `invalid` | 400 | `INVALID_REQUEST` | The request is not well formed: a body, a cursor, a limit, a path that does not match the body. |
| `denied` | 403 | `AUTH_SCOPE_DENIED` | The actor may not: unknown subject, a company or row outside its rights, rights that no longer cover a read, a read that is not the one the source made. |
| `not_found` | 404 | `NOT_FOUND` | Issue routes only: a job the source never reserved. |
| `conflict` | 409 | `IDEMPOTENCY_CONFLICT` | Issue routes only: another intent or digest under a job id, a voided job. |
| `expired` | 410 | `AUTH_SCOPE_DENIED` | A read or a reservation the source no longer holds or that has expired. |
| `unsupported` | 422 | `UNSUPPORTED_COMBINATION` | A dataset version or combination the source does not publish. |
| `integrity` | 422 | `INTEGRITY_FAILURE` | The request contradicts itself (a query hash that is not the query's). |
| `budget` | 429 | `BUDGET_EXCEEDED` | The source's own bound on prepared reads or work is reached. |
| `unavailable` | 503 | `AUTHORITY_UNAVAILABLE` | The source's authority or data cannot be reached now. |

How the platform reads an answer that is not `200`: a well-formed error body whose code is in the table **and** sent with its own status
is that code's error (`not_found` and `conflict` only from the issue routes). Otherwise: a redirect, 401, 407, 408, 429, any 5xx, or a 4xx
without a well-formed body is **unavailable**; a well-formed body with an unknown code, a code with another status, or a code the route
may not answer is an **integrity failure**, and so is a 2xx other than 200. A `200` that does not verify is an integrity failure: it is
never passed on. The platform's texts for all of these are fixed: no URL, no credential, no byte of a body; a person sees the platform's
refusal (docs/API.md) and nothing of the source's.

## 5. Bounds

| What | Bound |
|---|---|
| A request body, an answer | 1 MiB (an answer: the connection's `max_response_bytes`, 4096..1048576) |
| Datasets in a catalog; columns of a dataset | 100; 1..64 |
| Companies in a directory; a company's `id` and `name` | 1000, no id twice; each 1..128 bytes of UTF-8 text without control characters, not blank |
| `read.ref`; `scope_handle`; `watermark` | 1..128 of `A-Z a-z 0-9 . _ ~ -`; 1..1024 and 1..256 printable ASCII |
| A read's lifetime | after now, at most 15 minutes ahead (the platform's clock) |
| Rows of a page; of a read | at most `limit` (1..100, the platform asks 50); 1000 and 1 MiB in all |
| A cursor | 1..2048 printable ASCII, `""` for none |
| A cell; an integer or decimal | 4096 bytes; 128 characters |
| A request | the connection's `timeout`, 10 s by default, 1..60 s |

## 6. Idempotency and repetition

`catalog`, `companies`, `pages` and `authorize` are reads: repeating one changes nothing. `reads` makes a new read each time (a repeated job prepares
again; old reads expire by themselves; a source bounds how many it holds and answers `budget` beyond). The issue routes are idempotent by
job id as section 3 says, so a job that a second worker re-runs reaches the same outcome. No `Idempotency-Key` header is used: the job id
is the key.

## 7. Checking an implementation

```
export PRODUCT_REPORTING_AUTH=…          # the credential the operator gave the product
reporting-source-check -base-url https://product.staging.internal -auth-env PRODUCT_REPORTING_AUTH -ca-file /path/ca.pem \
    -context context.json -query query.json [-client-cert-file c.pem -client-key-file c.key] [-issue] [-companies]
```

`context.json` is a context object (section 2) of a test subject the product has granted; `query.json` a query inside that grant with at
least one row. The command checks the connection the platform would make (P1), the credential (A1: a request without it is 401), the
catalog (C1–C4: it verifies, holds the query's dataset, refuses an unknown subject and another source tenant), the read (R1–R13: it
verifies, is authorized, pages verify and end, a second reading gives the same rows and totals, a forged cursor is `invalid`, a read with
another watermark or scope handle, a read the source never made and another subject's read are refused, a wrong query hash, a company
outside the grant and an unpublished dataset version are refused), with `-companies` the company directory (D1–D2: the list verifies, at
most 1000 companies with unique ids and texts within bounds; a read naming the listed companies, the first 100, is accepted and one naming
a company the directory does not list is refused; skipped without the flag, as a connection without `companies: true` never asks) and, with
`-issue`, the document authority (I1: a reservation, the same reservation again, `conflict` for another intent, a void, a second void,
`conflict` after the void; **this reserves and voids one number: never against production**). It prints `PASS`, `FAIL` or `SKIP` per
rule with a fixed text and exits 0 when nothing failed, 1 when a rule failed, 2 for a usage error. It never prints the credential, a URL or
a byte of an answer. Passing it is necessary, not sufficient: the reconciliation of the product's data (go-live item 18) is the product's
and the platform's together.

## 8. Not in version 1

- Filters and sorts beyond what the platform supports (none in query version 1).
- A health probe of a source (ADR 0016 left it open).

A later version is a new path prefix (`/reporting-source/v2/`); additions inside version 1 are new optional routes only, each a minor
revision of this page (1.1: `companies`, S19-B).
