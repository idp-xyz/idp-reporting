# The reporting source protocol, version 1 (S19-A; revision 1.2, S24-A)

2026-11-09; revision 1.1 on 2026-11-11 adds one optional route, `companies` (S19-B, ADR 0015), and, in `catalog`, a dataset's optional
`filters` and `sorts` declarations, which a query of `reads` then carries (S20-A, ADR 0023; the checker gains F1–F6); the revision of
S24-A, the title's, adds one optional route, `issue-cancellations` (ADR 0025; the checker gains X1–X4). A revision changes nothing
else: a source written to an earlier one is served unchanged, and gains nothing until its connection declares. What a source product
implements so that the reporting platform reads it, through the platform's adapter `http` (`adapters/httpsource`), without the platform
carrying Go code for the product. It is the adapter contract of docs/s00/INTERFACES.md (`DescribeCatalog`, `PrepareRead`, `ReadPage`,
`AuthorizeCurrent`) and, optionally, the document authority of ADR 0003 (`PrepareDocument`, `ConfirmIssue`, `VoidIssue`), the company
directory of ADR 0015 (`ListCompanies`) and the cancellation of an issued document of ADR 0025 (`CancelDocument`), carried as JSON over
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
| `POST /reporting-source/v1/issue-cancellations` | `CancelDocument` (optional, S24-A) | `{context, cancellation}` → `{receipt}` |

The issue routes are needed only for a connection whose `issuance` is `rows` or `groups`; the `companies` route only for a connection with
`companies: true`, and the `issue-cancellations` route only for a connection with `cancellation: true`, which needs an issuance (the
platform never asks either otherwise). `{ref}` and `{job_id}` repeat a value of the body (`read.ref`, `intent.job_id`,
`confirmation.job_id`, `job_id`); a source refuses a mismatch with `invalid`. A cancellation names its job in the body alone.

### catalog

The published, approved dataset versions the context may read now (docs/API.md "Catalog"), at most 100, no `dataset` reference twice:

```json
{"datasets": [{"dataset": {"id": "receivables.aging", "version": "1.0.0"},
  "columns": [{"id": "customer.id", "label": "客户", "type": "string"}, {"id": "aging.bucket", "label": "账龄区间", "type": "string"},
              {"id": "ar.open_balance", "label": "应收余额", "type": "decimal", "scale": 2, "currency": "CNY"}],
  "metrics": [{"id": "ar.open_balance", "version": "1.0.0"}], "dimensions": ["customer.id", "aging.bucket"],
  "currency_policy": {"id": "ledger_base", "version": "1.0.0"}, "timezone": "Asia/Shanghai",
  "filters": [{"field": "customer.id", "operators": ["eq", "ne", "in", "prefix"]},
              {"field": "aging.bucket", "operators": ["eq", "in"], "values": ["current", "1_30", "31_60", "61_90", "90+"]},
              {"field": "ar.open_balance", "operators": ["lt", "le", "gt", "ge", "between"]}],
  "sorts": [{"field": "ar.open_balance", "directions": ["asc", "desc"]}, {"field": "customer.id", "directions": ["asc", "desc"]}]}]}
```

The platform requires of each dataset: `columns` are the `dimensions` then the `metrics` (by id) in that order, 1..64 of them, unique ids, a
label of 1..256 bytes, a type of `string`, `date`, `boolean`, `integer` (no scale) or `decimal` (scale 0..18), `currency`/`unit` at most 32
bytes; `timezone` an IANA zone; ids and versions 1..128 printable ASCII. A dataset version is immutable: a change is a new version.

**`filters` and `sorts`** (optional, revision 1.1, S20-A, ADR 0023) say what a query of the version may narrow by and order by; without
them a query carries no filter and no sort, as before this revision. The platform requires: at most 16 `filters` and 8 `sorts`, a `field`
at most once in each and a column id of the dataset; `operators` a non-empty set of `eq ne lt le gt ge in between prefix contains` that
fit the column's type (`prefix` and `contains` for `string` columns only; `lt le gt ge between in` for `date`, `integer`, `decimal` and
`string`; a `boolean` column takes `eq` and `ne` only); `values`, when present, 1..64 distinct values the field accepts, each well formed
for the column (a string of 1..128 characters without control characters, else the column's exact form), which an `eq`, `ne` or `in`
filter must take its values from; `required: true` when a query must carry exactly one filter on the field; `directions` a non-empty set
of `asc desc`. A declaration outside these rules is an integrity failure, like a malformed column, and the catalog is not used. The lists
are part of the version: a change is a new version.

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

`query` is the platform's query exactly as docs/API.md defines it (companies sorted; `filters` sorted by field, operator and value; `sort`
in the order the keys apply; both empty for a query that narrows and orders nothing); `query_hash` is its digest (`reporting.QueryHash`:
the SHA-256, lowercase hex, of the query's JSON without `page`), so a narrowed query has another hash than the plain one. The source
checks the query against the dataset and the context's current rights **in its own trusted transaction**, freezes the data version, and
answers:

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

**The query's filters and sort** (S20-A, ADR 0023) arrive as the query carries them: each filter `{"field", "operator", "value"}` on a
field the dataset declares with one of its operators, `value` a string well formed for the column (a date `YYYY-MM-DD`; an integer or a
decimal as the column writes it, exactly its scale; `true` or `false`; a string of 1..128 characters without control characters; from the
declared `values` when there is a list and the operator is `eq`, `ne` or `in`), and for `in` and `between` a JSON array of such strings
**as text** (`"value": "[\"current\",\"1_30\"]"`: 1..32 values for `in`, exactly 2 in ascending order for `between`, the first not greater
than the second as the column's type compares); the filters are a conjunction. Each sort key `{"field", "direction"}` is on a declared field in a declared direction, no field twice, at most three, in the
order they apply. The platform has validated all of this against the catalog it read, but **the source checks it again against what it
published** and refuses what it did not publish with `unsupported` (a field, an operator or a direction it did not declare, a value off
its closed list, a required filter missing or doubled; a malformed value is `unsupported` or `invalid`): the platform's validation is not
a substitute for the source's. The source applies the filters and the sort in its own trusted transaction, as it applies the company
scope. **It must bind every filter value as data (a query parameter, a typed comparison), never as text of a statement**: a value is a
string a person typed, and the platform's bounds on it are not an escape. An operator a source does not know, should the set ever grow, is
`unsupported`, never a guess. A dataset without a declaration refuses every filter and sort with `unsupported`, as the platform does before
asking.

### pages

```json
{"context": {…}, "read": {…}, "cursor": "", "limit": 50}
→ {"ref": "r-7f3a", "query_hash": "57ef…25", "watermark": "ledger-2026-11-09T09:58:00Z",
   "rows": [["customer_a", "current", "100.25"], ["customer_b", "1_30", "-0.24"]], "totals": {"ar.open_balance": "9007199254740993.01"},
   "next_cursor": "r-7f3a:2"}
```

The source checks the `read` against its own record (the platform sends it back unchanged; a different field is `denied`), re-authorizes the
actor (the same rule as `authorize`) and projects the **frozen** version: data that changed since the read was prepared does not reach a
later page. The rows are the read's query's scope: the companies, **narrowed by its filters and in its sort order**, the order stable
across pages (two rows the sort does not tell apart keep one order from the first page to the last, and from one reading of the read to
the next), and the `totals` are **the narrowed scope's**, not the dataset's. The platform verifies: `ref`, `query_hash` and `watermark`
are the read's; at most `limit` rows (1..100; the platform asks 50); every row has the dataset's columns and every cell its column's
type; `totals` holds one total per metric, of the metric column's type, the **same on every page** (the totals of the whole read);
`next_cursor` is `""` on the last page, otherwise 1..2048 printable ASCII, never the request's cursor and never after an empty page; a
cursor that comes back is a loop. The whole read is at most 1000 rows and 1 MiB. A cursor the source did not give is `invalid`.

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

### The cancellation of an issued document (optional; ADR 0025, S24-A)

A person who may issue asks the platform to cancel a document that was issued; the platform asks the source, **and the source decides**.
The platform never marks a document cancelled by itself: it records a cancellation only with the source's receipt in hand. A `void` is
something else: the platform's own undo of a job that did not complete, which no person asks for.

```json
{"context": {…},
 "cancellation": {"job_id": "0f3c5a…9a", "reservation_ref": "res-81", "confirmation_ref": "conf-81", "number": "AR-000123",
                  "reason": "客户退回：金额有误"}}
→ {"receipt": {"job_id": "0f3c5a…9a", "ref": "cxl-17", "cancelled_at": "2026-11-11T08:15:33Z"}}
```

The request names the document by the platform's job id and by what the source gave for it: the `ref` of its reservation, the `ref` of
its confirmation's receipt and the `number`, each exactly as the source answered it. `reason` is what the person wrote: 1..200
characters (not bytes) of UTF-8 text on one line, not blank, without control characters (C0, tab and line breaks included, DEL, U+FFFE,
U+FFFF). It is personal text: bind it as data, and keep it out of logs, as the platform does. The platform sends nothing else: no file,
no digest, no row.

The source, in its own trusted transaction:

- maps the context to its actor and decides by **its own rule** whether that actor may cancel this document now (`denied` otherwise:
  an unknown subject, an actor without the right, a rule of the product that says no, such as a closed period). The reference source
  lets whoever may issue cancel; a product may be stricter. Nothing in the request is authority;
- finds what it holds for the job. A job it never reserved is `not_found`. Only a **confirmed** document is cancelled: a reservation
  that was never confirmed (expired or not) and a voided job are `conflict`, and so is a request whose `reservation_ref`,
  `confirmation_ref` or `number` is not the document's;
- records the cancellation with a reference and a time of its own, and **keeps the number used**: a cancelled number is never given
  to another document. A corrected document is a new issue with a new number;
- answers the receipt: `job_id` the request's, `ref` 1..128 printable ASCII, `cancelled_at` a whole second in the canonical form, not
  before the confirmation's `confirmed_at`.

The route is **idempotent by the job id**: asked again about a document it has cancelled, the source answers the same receipt (the same
`ref` and `cancelled_at`), whatever the reason says this time, and keeps the first reason. That is how a cancellation is reconciled.
The platform asks the source first and records second, so a cancellation whose answer the platform did not receive (a timeout) or
could not keep is cancelled at the source and not yet at the platform; the person's next request asks again and the same receipt is
recorded. The reverse, cancelled at the platform and not at the source, cannot happen. A `void` of a job whose document was cancelled
is, as for any job, not an error: the reference source answers `voided`, keeps the document cancelled and goes on answering its receipt.

The platform verifies the receipt, and refuses with an integrity failure otherwise, recording nothing: `job_id` is the request's; `ref`
is of the form above; `cancelled_at` is canonical, a whole second within the years 1970..9999 and not before the confirmation the
platform recorded. With the receipt it keeps the source's reference and time and the person's reason beside its issue record, which
itself does not change, shows them to the document's owner and no longer hands the file out (docs/API.md, "Cancelling an issued
document": `ISSUE_CANCELLED`). A source without the route answers `unsupported`; the platform asks the route only for a connection with
`cancellation: true` (docs/DEPLOYMENT.md), and answers its own route `UNSUPPORTED_COMBINATION` for every other connection, without
asking.

## 4. Errors

Every refusal is the status of its code with exactly this body: `{"code": "<code>"}`. The set is closed:

| Code | Status | Platform error | When |
|---|---|---|---|
| `invalid` | 400 | `INVALID_REQUEST` | The request is not well formed: a body, a cursor, a limit, a path that does not match the body. |
| `denied` | 403 | `AUTH_SCOPE_DENIED` | The actor may not: unknown subject, a company or row outside its rights, rights that no longer cover a read, a read that is not the one the source made. |
| `not_found` | 404 | `NOT_FOUND` | Issue routes only (the cancellation's among them): a job the source never reserved. |
| `conflict` | 409 | `IDEMPOTENCY_CONFLICT` | Issue routes only (the cancellation's among them): another intent or digest under a job id, a voided job; a cancellation of what is not a confirmed document, or that names the document otherwise than the source did. |
| `expired` | 410 | `AUTH_SCOPE_DENIED` | A read or a reservation the source no longer holds or that has expired. |
| `unsupported` | 422 | `UNSUPPORTED_COMBINATION` | A dataset version or combination the source does not publish: a filter or a sort outside the dataset's declaration, an operator the source does not know. |
| `integrity` | 422 | `INTEGRITY_FAILURE` | The request contradicts itself (a query hash that is not the query's). |
| `budget` | 429 | `BUDGET_EXCEEDED` | The source's own bound on prepared reads or work is reached. |
| `unavailable` | 503 | `AUTHORITY_UNAVAILABLE` | The source's authority or data cannot be reached now. |

How the platform reads an answer that is not `200`: a well-formed error body whose code is in the table **and** sent with its own status
is that code's error (`not_found` and `conflict` only from the issue routes and `issue-cancellations`). Otherwise: a redirect, 401, 407, 408, 429, any 5xx, or a 4xx
without a well-formed body is **unavailable**; a well-formed body with an unknown code, a code with another status, or a code the route
may not answer is an **integrity failure**, and so is a 2xx other than 200. A `200` that does not verify is an integrity failure: it is
never passed on. The platform's texts for all of these are fixed: no URL, no credential, no byte of a body; a person sees the platform's
refusal (docs/API.md) and nothing of the source's.

## 5. Bounds

| What | Bound |
|---|---|
| A request body, an answer | 1 MiB (an answer: the connection's `max_response_bytes`, 4096..1048576) |
| Datasets in a catalog; columns of a dataset | 100; 1..64 |
| Declared `filters`; `sorts`; closed `values` of one filter | 16; 8; 64, each 1..128 characters |
| Filters of a query; sort keys; values of an `in`; of a `between` | 16; 3; 1..32; exactly 2 |
| A filter value | a string of 1..128 characters without control characters, else the column's exact form (an integer or a decimal at most 128 characters) |
| Companies in a directory; a company's `id` and `name` | 1000, no id twice; each 1..128 bytes of UTF-8 text without control characters, not blank |
| `read.ref`; `scope_handle`; `watermark` | 1..128 of `A-Z a-z 0-9 . _ ~ -`; 1..1024 and 1..256 printable ASCII |
| A read's lifetime | after now, at most 15 minutes ahead (the platform's clock) |
| Rows of a page; of a read | at most `limit` (1..100, the platform asks 50); 1000 and 1 MiB in all |
| A cursor | 1..2048 printable ASCII, `""` for none |
| A cell; an integer or decimal | 4096 bytes; 128 characters |
| A cancellation's `reason`; a receipt's `ref` | 1..200 characters of UTF-8 text on one line, not blank, without control characters; 1..128 printable ASCII |
| A request | the connection's `timeout`, 10 s by default, 1..60 s |

## 6. Idempotency and repetition

`catalog`, `companies`, `pages` and `authorize` are reads: repeating one changes nothing. `reads` makes a new read each time (a repeated job prepares
again; old reads expire by themselves; a source bounds how many it holds and answers `budget` beyond). The issue routes are idempotent by
job id as section 3 says, so a job that a second worker re-runs reaches the same outcome, and so is `issue-cancellations`: a
cancellation asked again answers the receipt it answered first. No `Idempotency-Key` header is used: the job id is the key.

## 7. Checking an implementation

```
export PRODUCT_REPORTING_AUTH=…          # the credential the operator gave the product
reporting-source-check -base-url https://product.staging.internal -auth-env PRODUCT_REPORTING_AUTH -ca-file /path/ca.pem \
    -context context.json -query query.json [-client-cert-file c.pem -client-key-file c.key] [-issue [-cancellation]] [-companies]
```

`context.json` is a context object (section 2) of a test subject the product has granted; `query.json` a query inside that grant with at
least one row. The command checks the connection the platform would make (P1), the credential (A1: a request without it is 401), the
catalog (C1–C4: it verifies, holds the query's dataset, refuses an unknown subject and another source tenant), the read (R1–R13: it
verifies, is authorized, pages verify and end, a second reading gives the same rows and totals, a forged cursor is `invalid`, a read with
another watermark or scope handle, a read the source never made and another subject's read are refused, a wrong query hash, a company
outside the grant and an unpublished dataset version are refused), the published filters and sorts (F1–F6, S20-A: for every declared
filter and each of its operators, a filter made from the read's own values narrows the rows to those that satisfy it, every row a row of
the whole scope and the totals the sum of the narrowed rows where the whole read's totals were sums; for every declared sort and
direction the rows come back in that order with the same rows and totals; a filter on an undeclared field, with an undeclared operator
or with a malformed value, and an undeclared sort, are refused `unsupported` or `invalid`; F1, F2, F4 and F5 are skipped when the
dataset declares nothing), with `-companies` the company directory (D1–D2: the list verifies, at most 1000 companies with unique ids and
texts within bounds; a read naming the listed companies, the first 100, is accepted and one naming a company the directory does not list
is refused; skipped without the flag, as a connection without `companies: true` never asks), with `-issue` the document authority
(I1: a reservation, the same reservation again, `conflict` for another intent, a void, a second void, `conflict` after the void; **this
reserves and voids one number: never against production**) and, with `-issue -cancellation`, the cancellation of an issued document
(X1–X4, S24-A, as a connection with `cancellation: true` would use it. X1: a document the check reserved and confirmed is cancelled,
the receipt verifies and is not dated before the confirmation, and asking again with another reason answers the same receipt. X2: a
reservation that was not confirmed is `conflict`, a job the source never reserved `not_found`. X3: a subject the product does not know
is `denied`, before the document is cancelled and after. X4: a job reserved after the cancellation gets another number. They are
skipped without the flag; a source that answers the route `unsupported` fails X1 and skips the rest, and a document the check could
not cancel is voided. **This confirms and cancels one document and reserves and voids two more numbers: never against production**;
`-cancellation` without `-issue` is a usage error): 32 rules in all. It prints `PASS`, `FAIL` or `SKIP` per rule with a fixed
text and exits 0 when nothing failed, 1 when a rule failed, 2 for a usage error. It never prints the credential, a URL or a byte of an
answer. Passing it is necessary, not sufficient: the reconciliation of the product's data (go-live item 18) is the product's and the
platform's together.

## 8. Not in version 1

- A health probe of a source (ADR 0016 left it open).
- A data class (ADR 0012 decision 6, S08-A). The protocol has no field for it and a source cannot send one (an unknown field of the
  catalog is refused, as ever): to the platform, whatever answers this protocol is a source product, and every result read through it
  says `data_class: "business-data"` in its evidence, 来源产品数据 in the footer of a PDF and `SOURCE PRODUCT DATA` in a workbook.

A later version is a new path prefix (`/reporting-source/v2/`); additions inside version 1 are new optional routes and optional fields
only, each a minor revision of this page (1.1: the `companies` route, S19-B; a dataset's `filters` and `sorts` and a query's filters and
sort, S20-A. The title's: the `issue-cancellations` route, S24-A). The revision's number is written in this page's title, in
docs/source-openapi.json (`info.version`) and in `protocol.Revision`, which a test holds together.
