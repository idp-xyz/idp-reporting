# Deploying the server (S05-A)

`reporting-server` (`cmd/reporting-server`, package `server`) is the production composition of the platform: one strict configuration file, secrets from files
and the environment, and the adapters a host registers, turned into a running server with health and readiness endpoints, JSON logs that carry counts and codes
only, an atomic reload of the issuer files and a graceful shutdown. It is a mechanism with its qualification, **not a production admission**: `GET /capabilities`
says `production_approved: false` whatever the configuration says (there is no configuration field for it; changing it is a code change after the admission
decision), and the repository contains no adapter written for any one source product: since S19-A a product connects through the generic adapter `http`
(docs/SOURCE_HTTP_PROTOCOL.md). The go-live checklist at the end says what is done, what the operator does and what is open.

## The command and the host's `main`

```
reporting-server check-config -config FILE   # offline: parses and validates the file, the files it names, the JWKS, the bindings, the cursor keys, the quotas; prints counts, never a value
reporting-server run -config FILE            # start, serve, drain
reporting-server version                     # the binary's version and the schema version it needs
```

The shipped command registers **one adapter, `http`** (S19-A, `adapters/httpsource`): the platform's generic source adapter, which speaks the reporting source
protocol (docs/SOURCE_HTTP_PROTOCOL.md) to a product that implements it. It is the platform's own reviewed code and not a simulator; any other adapter name in
the file stops `run` at the adapters step with `the adapter "X" of integration "Y" is not registered`. A host that has an adapter of its own (Go code of
its own review) builds its own command; the whole of its `main` is:

```go
package main

import (
	"os"

	"idp.local/reporting/server"
)

func main() {
	reg := server.DefaultRegistry()  // the http adapter
	reg["my-product"] = myProductFactory // func(options json.RawMessage, getenv func(string) string) (reporting.Adapter, error)
	server.Main(os.Args, reg)
}
```

`options` is the connection's `options` object from the configuration, untouched and never logged; the factory reads its own secrets from `getenv` by names the
host chooses, and its error is never shown (the start line says which connection failed). The contract simulators are test fixtures: they report themselves as
simulators (`IsSimulator()`), and `Run` refuses a registry entry that returns one unless the configuration says `"allow_simulator": true`, which the example below
does not contain; with it, `/capabilities` states the profile as `simulated:<profile_label>`.

**The data class of an adapter** (S08-A, ADR 0012 decision 6). Every result says where its data comes from (`evidence.data_class`, the catalog's entries, the
footer of a PDF, the `Metadata` sheet of a workbook) in the word of the connection's adapter: `contract-simulator`, `manual-entry` or `business-data`. An adapter of
a source product needs to do nothing: one that says no class serves `business-data`, as the `http` adapter does. An adapter says another class by implementing
`DataClass() string` (`reporting.ClassedAdapter`); the word is read once, at start, and a word outside the three refuses the start. An adapter that reports itself a
simulator, by `IsSimulator()` or by the class `contract-simulator`, needs `allow_simulator`; one that says `IsSimulator()` without the class `contract-simulator`
refuses the start, so that a simulator's data is never labelled a source product's. A wrapper around an adapter (one that embeds only `reporting.Adapter`) hides the
word of what it wraps, like every other optional capability, and must say it itself.

Exit codes of `run`: **0** a clean drain, **1** a refused start (or a runtime failure of the listener), **2** a drain that ran out of `shutdown_grace`.

## The configuration file

One JSON file, at most 256 KiB, valid UTF-8, one document. It is strict: an unknown field, a field spelled with another case, a repeated key (at any depth, the adapters'
`options` included), `null`, data after the document and a nesting deeper than 12 are refused. **No secret is in it**: a field named like a secret (a name with
`dsn`, `password`, `secret`, `token`, `credential`, `passphrase`, or ending in `key`, other than the schema's own `…_key_file`/`cursor_key_files`) is refused with
its own text, in the file and inside `options`. Every duration is a whole number of **seconds**; every path is absolute and clean. A refusal names the field and
the rule and never repeats a value of the file.

```json
{
  "listen": "0.0.0.0:8443",
  "tls_cert_file": "/etc/reporting/tls/server.crt",
  "tls_key_file": "/etc/reporting/tls/server.key",
  "max_in_flight": 128,
  "workers": 2,
  "shutdown_grace": 30,
  "store": { "pool_max_conns": 0 },
  "object_store": { "endpoint": "https://s3.example.com", "bucket": "reporting-artifacts", "region": "us-east-1", "ca_file": "/etc/reporting/tls/object-store-ca.pem" },
  "issuer": {
    "issuer": "https://idp.example.com",
    "audience": "reporting-api",
    "required_scope": "reporting.read",
    "max_token_lifetime": 900,
    "jwks_file": "/etc/reporting/issuer/jwks.json",
    "bindings_file": "/etc/reporting/issuer/bindings.json"
  },
  "cursor_key_files": ["/etc/reporting/keys/cursor-2026-11.key", "/etc/reporting/keys/cursor-2026-08.key"],
  "connections": [
    { "tenant": "acme", "integration": "erp-finance", "source_tenant": "acme-prod", "adapter": "http",
      "options": { "base_url": "https://erp.internal/reporting", "ca_file": "/etc/reporting/tls/erp-ca.pem", "auth_env": "REPORTING_SOURCE_ERP_AUTH", "issuance": "rows", "companies": true } }
  ],
  "quotas": { "jobs_in_flight": 8, "stored_results": 64, "admissions_per_hour": 120, "queries_in_flight": 4 },
  "retention": { "sweep_interval": 0 },
  "profile_label": "staging-eu",
  "sign_in": {
    "authorization_endpoint": "https://idp.example.com/oauth2/authorize",
    "token_endpoint": "https://idp.example.com/oauth2/token",
    "client_id": "reporting-console",
    "redirect_uri": "https://reporting.example.com/console/",
    "scope": "openid reporting.read"
  }
}
```

(`TestDeploymentExampleIsAValidConfiguration` parses this block with the production loader.)

### Reference

| Field | Default | Bounds and rules |
|---|---|---|
| `listen` | required | `host:port`. A host that is not a loopback IP (an empty host, `0.0.0.0`, a name) needs TLS. |
| `metrics_listen` | none (off) | `host:port` of the Prometheus listener (see "Metrics" below). A loopback IP only, never the port of `listen`. |
| `tls_cert_file`, `tls_key_file` | none | Both or neither. TLS 1.3 only (a TLS 1.2 client is refused at the handshake). The certificate must be valid when the server starts; the key is a secret file. |
| `allow_plain_loopback` | `false` | Plain HTTP, accepted only with no certificate and a loopback IP `listen` (a host behind a trusted local proxy). Contradictory with a certificate. |
| `allow_simulator` | `false` | Lets `Run` accept adapters that are contract simulators. Not for production. |
| `read_header_timeout`, `read_timeout`, `write_timeout`, `idle_timeout` | 5, 25, 25, 30 | 0 (default) or 1..300 seconds. |
| `max_header_bytes` | 16384 | 0 (default) or 1024..1048576. |
| `max_in_flight` | 128 | 0 (default) or 1..4096 (`httpapi.WithMaxInFlight`; beyond it a request is 503 `OVERLOADED`). |
| `workers` | 2 | 0 (default) or 1..32 worker loops in this process. |
| `shutdown_grace` | 30 | 0 (default) or 1..600 seconds; `TimeoutStopSec` of the unit must be above it. |
| `store.pool_max_conns` | 0 | 0 (the default pool, 8 raised to workers+2) or workers+2..100, as `OpenProduction` checks it. A connection string that also names `pool_max_conns` is refused at start. |
| `object_store.endpoint`, `.bucket` | none | As S04-E (`docs/OPERATIONS.md` "Object storage"). Absent section: artifact files stay inline in the rows. |
| `object_store.region`, `.ca_file`, `.loopback_http` | `us-east-1`, none, `false` | `loopback_http` allows `http://` on a loopback IP, for qualification only. |
| `issuer.issuer` | required | `https://` URL. |
| `issuer.audience`, `.required_scope` | required | Exact match. |
| `issuer.max_token_lifetime` | required | 1..3600 seconds. |
| `issuer.jwks_file`, `.bindings_file` | required | The public keys (at most 64 KiB, 16 RSA keys of 2048..4096 bits, e=65537) and the list of `{subject, client_id, tenant, integrations[, roles][, email]}` (`roles`, S12-A, ADR 0018: 1..5 of `viewer`, `designer`, `publisher`, `issuer`, `administrator`; absent: `viewer, designer, publisher, issuer`, the rights before roles; `email`, S16-B: one address, an RFC 5322 addr-spec of printable ASCII, at most 254 characters, where the person's scheduled runs' ends are mailed when `schedules.email` is configured; absent: no mail) (strict, at most 4096). Re-read by a reload. |
| `cursor_key_files` | required | 1..4 files, newest first; each holds one key, hex or base64 (a text valid as both is read as hex), 32..64 bytes; two files may not hold the same key. |
| `connections` | required | 1..256 entries `{tenant, integration, source_tenant, adapter, options}`; `(tenant, integration)` unique; `adapter` names a registry entry (`http` in the shipped command; "Connecting a source product over HTTP" below for its `options`). |
| `quotas.*` | 8, 64, 120, 4 | `jobs_in_flight` 1..1000, `stored_results` 1..100000, `admissions_per_hour` 1..100000, `queries_in_flight` 1..64; 0 is the default of each. |
| `retention.sweep_interval` | 0 (off) | 0 or 60..604800 seconds. See "The sweep" below. |
| `retention.grace`, `.batch` | 24 h, 500 | `grace` 0 (default) up to 10 years in seconds; `batch` 0 (default) up to 10000. |
| `retention.results`, `.issued` | 7 days, 10 years | How long a result is kept after the source read it was made from has expired (ADR 0026, S25-A), in seconds: `results` for export files and the snapshots of scheduled runs (0 for the default, or 60..31622400), `issued` for the files of issued documents (0 for the default, or 60..3153600000). Nothing is kept beyond the scope the source vouches for: with a source that declares none, every result ends with its read, whatever these say. A change applies to the jobs admitted after it. |
| `pdf.typst`, `.sha256`, `.fonts` | none | All three or none (the pinned Typst and the only fonts PDF export may use). |
| `layout_directory`, `dashboard_directory`, `template_directory`, `background_directory` | none | Private single-process directories (mode 0700, owned by the user the server runs as), as in the demo. They are **per process**: two servers do not share them. |
| `design_store` | `directories` | `directories` (the four private directories above, one server each) or `database`: every design (layouts, dashboards, templates, backgrounds, images) in the platform's store, table `designs` (migration 011), shared by every server on it; the four directory fields must then be absent, and the four features are on. See "Several servers" below (S11-B, ADR 0017). |
| `embed_origins` | none | `{integration: [origin]}`, validated as `embedding.New` does (https origins only here); every integration must be served by a connection. |
| `profile_label` | `unqualified` | 1..64 printable ASCII characters, shown in `/capabilities`. |
| `sign_in.authorization_endpoint`, `sign_in.token_endpoint` | none (the section is off: the console shows its paste form alone) | The console's browser sign-in (S15-A, ADR 0019; "Browser sign-in" below). `https://host[:port]/path`, no user, query or fragment. The browser is sent to the first; the server exchanges the code at the second. |
| `sign_in.client_id` | required with the section | 1..256 printable ASCII characters without a space: the client registered at the provider. A confidential client's secret is the environment's (`REPORTING_SIGN_IN_CLIENT_SECRET`), never a field. |
| `sign_in.redirect_uri` | required with the section | Exactly the URI registered at the provider, which must be the console's page: `https://host[:port]/console/`, no query or fragment. Sent unchanged in the authorization request and the exchange. |
| `sign_in.scope` | required with the section | 1..512 characters, scope tokens separated by one space (`openid` and whatever the provider needs to put the platform's `issuer.required_scope` in the access token). |
| `sign_in.paste_fallback` | `false` | `true` keeps the paste-a-token form under the sign-in button (ADR 0019: for a break-glass procedure; the API accepts any valid token either way). |
| `sign_in.ca_file` | none (the system's roots) | PEM file of the CA that vouches for an `https://` token endpoint; it replaces the system's roots for that endpoint. |
| `sign_in.loopback_http` | `false` | Allows `http://` for an endpoint or the redirect URI on a loopback IP, for qualification only. |
| `sign_in.timeout` | 10 | 0 (default) or 1..60 seconds for one exchange at the token endpoint (connection, TLS, request and answer). |
| `schedules` | none (off) | S16-A (docs/S16A_SCOPE.md): present, the routes under `/schedules` and the scheduler in this server are on. Every server on the store may run it: the store gives each run to one of them. |
| `schedules.poll_interval`, `.per_identity` | 30, 20 | `poll_interval` 0 (default) or 5..300 seconds between two passes; `per_identity` 0 (default) or 1..200 schedules an identity may hold. |
| `schedules.webhook.url`, `.timeout`, `.loopback_http` | none, 10, `false` | The one endpoint every run's end is delivered to: `https://` without user information or fragment (`loopback_http` allows `http://` to a loopback IP address, for tests only); `timeout` 0 (default) or 1..30 seconds per attempt. The secret is `REPORTING_WEBHOOK_SECRET`. |
| `schedules.email.host`, `.port`, `.from` | none, 587, none | S16-B (docs/S16B_SCOPE.md, ADR 0020's amendment): every run's end is also mailed to its owner, at the `email` of the owner's bindings (none: no mail, and nothing recorded of it). `host` is the SMTP server, a name or an IP address without a port; `from` the sender, one address (RFC 5322 addr-spec, printable ASCII, at most 254 characters, no display name). Either deliverer, both or neither may be configured; with both, one attempt tries both and a failure of either is retried. |
| `schedules.email.username` | none | The SMTP user (SASL PLAIN, sent only over TLS or to a loopback IP address): 1..256 printable ASCII characters without a space. Its password is `REPORTING_SMTP_PASSWORD`, required with it and refused without it. |
| `schedules.email.starttls`, `.loopback_plain` | `true`, `false` | STARTTLS is required: a server that does not offer it gets no mail (the attempt is `unreachable`), and the certificate is checked against the host's roots for `host`. `starttls: false` sends in the clear and is accepted only with `loopback_plain: true` and a loopback IP `host`, for tests. |
| `schedules.email.timeout` | 10 | 0 (default) or 1..30 seconds for one attempt, the connection to the server's last reply. |
| `schedules.email.console_url` | required with the section | The console's base the mail links to (`<console_url>/console/#schedules`, where the owner downloads the file with their own access): `https://host[:port][/path]`, no user information, query or fragment; `http://` only to a loopback IP address, with `loopback_plain`. |

## Secrets

Nothing secret is in the file, in an error, a log line or a response (`TestSessionOverTLSWithRealTokens` runs a whole session with a known token, subject,
tenant, cursor key, connection-string password and object store keys and looks for each in every line; every start refusal is tested the same way).

| Secret | How it is provisioned | Rules checked at start |
|---|---|---|
| Store connection string | environment `REPORTING_STORE_DSN` (the unit's `EnvironmentFile=`, mode 0600) | `sslmode=verify-full`, not a fixture database, the `production` profile row, the ledger at the binary's version, the connected role is the application role (`OpenProduction`). |
| Object store keys | environment `REPORTING_OBJECT_STORE_ACCESS_KEY`, `REPORTING_OBJECT_STORE_SECRET_KEY`, required when `object_store` is configured | Both set; `HEAD bucket` answers 200 (`the object store's bucket "NAME" is not usable: HEAD bucket answered status N`). |
| Cursor keys | files named by `cursor_key_files` | Regular file, **not a symbolic link**, owned by the user the server runs as, mode **0600 or 0400**, at most 4 KiB. Generate with `openssl rand -hex 32`. |
| TLS private key | file named by `tls_key_file` | The same rules as a cursor key; the certificate file must not be writable by group or others. |
| Issuer JWKS and bindings | files named by `jwks_file`, `bindings_file` (public data, personal identifiers) | Regular file, not a symbolic link, owned by the server's user or root, not writable by group or others. |
| Sign-in client secret | environment `REPORTING_SIGN_IN_CLIENT_SECRET`, only for a confidential client; absent for a public client (the recommended registration, PKCE alone) | 1..512 printable ASCII characters without a space; sent as HTTP Basic (`client_secret_basic`) to the token endpoint only. |
| Source credential (S19-A) | environment variable named by a connection's `options.auth_env` (one per product, e.g. `REPORTING_SOURCE_ERP_AUTH`), and/or the client key file `options.client_key_file` | Set and 1..4096 printable characters without a space; sent only as `Authorization: Bearer` to that connection's `base_url`. The key file: regular, not a link, mode 0600 or 0400, at most 64 KiB. |
| Webhook signing secret (S16-A) | environment `REPORTING_WEBHOOK_SECRET`, required when `schedules.webhook` is configured | 32..1024 bytes (generate with `openssl rand -hex 32`); give the receiver the same value. Never shown; rotating it is a restart, rolled, with the receiver accepting both for the while. |
| SMTP password (S16-B) | environment `REPORTING_SMTP_PASSWORD`, required when `schedules.email.username` is set, refused when it is not | 1..1024 bytes without control characters; sent as SASL PLAIN only over TLS or to a loopback IP address, to `schedules.email.host` alone. Never shown; rotating it is a restart, rolled. |

A symbolic link is refused for the file itself (it is opened with `O_NOFOLLOW`); a link in a directory above it is the operator's layout and is not checked.

## Connecting a source product over HTTP (S19-A)

A connection with `"adapter": "http"` reaches a product that implements the reporting source protocol (docs/SOURCE_HTTP_PROTOCOL.md). Its `options`, read
strictly (an unknown field, `null` or a repeated key refuses the start; the error names the field, never the value):

| Option | Default | Rules |
|---|---|---|
| `base_url` | required | `https://host[:port][/path]`, no user, query or fragment, a clean path; `http://` only to a loopback IP address with `loopback_http`. |
| `loopback_http` | `false` | Allows `http://` to a loopback IP address, for qualification only. |
| `timeout` | 10 | 0 (default) or 1..60 seconds for one request, connection to the last byte. |
| `ca_file` | none (the system's roots) | Absolute path of a PEM file of the CA that vouches for the product; it **replaces** the system's roots for this connection (a pin). |
| `client_cert_file`, `client_key_file` | none | Both or neither: the platform's client certificate for mTLS and its key (mode 0600 or 0400). |
| `auth_env` | none | The **name** of the environment variable that holds the bearer credential (`A-Z 0-9 _`); the variable must be set. |
| `max_response_bytes` | 1048576 | 0 (default) or 4096..1048576. |
| `issuance` | `none` | `none` (issuing through the connection is unsupported), `rows` (the document authority of ADR 0003, one row per document) or `groups` (contract version 2, a row group per document, S03-K). |
| `companies` | `false` | `true` declares that the product implements the company directory of ADR 0015 (`POST /reporting-source/v1/companies`, protocol revision 1.1, S19-B): the platform then asks it for `GET /companies` and the console can offer a picker. Without it the route is never asked and the platform answers `supported: false`. Declare it only after `reporting-source-check -companies` passes. |

A connection needs a credential: `auth_env`, a client certificate, or both (`loopback_http` alone is accepted for tests). The adapter follows no redirect,
uses TLS 1.2 or later and no proxy from the environment, and verifies every answer before it is used (an answer that does not verify is
`INTEGRITY_FAILURE`, a product that cannot be reached, is too slow or answers 5xx is `AUTHORITY_UNAVAILABLE`, 503 to the caller). Each request carries a
random `X-Request-Id` and the request's `traceparent`; the product logs them. **Before** a connection is added the product passes `reporting-source-check`
(docs/SOURCE_HTTP_PROTOCOL.md section 7) against the endpoint the platform will use, with the credential and CA the connection will use, and with
`-companies` when the connection will declare `companies`. Rotating the credential
is a restart, rolled one server at a time, with the product accepting the old and the new credential for the while.

## What a start does, in order

Each failure is a refusal whose text is fixed and names the step (`reporting-server: start refused at step "store": …`) and is logged as `start_refused` with the step only:
**configuration** (the file's own rules, including a ring of five, plain HTTP on a public address) → **secrets** (cursor keys, the connection string's presence, the object store keys, the
webhook's secret and the SMTP password when their sections ask for them, the TLS pair) → **issuer** (the access-token profile; then every product of every binding must be a connection of the binding's tenant, ADR 0016) → **sign in** (the `sign_in` section with the client secret of the environment, the CA file read) → **adapters** (an unregistered or unknown adapter name, a factory that fails, a simulator without the allowance) → **store**
(`OpenProduction`; its own refusals keep their text, any other error, a connection failure, is reduced to a fixed text because it may name the host or the user) → **object store**
(`HEAD bucket`) → **pdf**, **private directories** → **service** → workers and sweep → **listen** (`the listen address cannot be bound`). A refused start releases everything it
opened and stops the workers it started. The start line (`start`) carries the binary's version, the ledger's version, the number of cursor keys, the connections and workers, the trusted `kid`s,
and whether TLS, an object store, PDF, issuance, embedding, the browser sign-in (`sign_in`), the scheduled reports (`schedules`) and their deliverers (`webhook`, `email`) are on, and (ADR 0016) `connections_sha256`, `bindings` and `bindings_sha256`: the digests of
what is in force, the same that `check-config` prints, so that a reviewed file can be matched to a running server.

**Adding a product** (ADR 0016): add the connection to the configuration file and restart (rolled), then add the product to the bindings and reload. A
binding that names a product no connection of its tenant serves is refused, at start and in `check-config` with the number of such products, at reload with
`ISSUER_BINDING_UNSERVED`. `GET /session` lists only the products the server serves. Removing a product goes the other way: the bindings first, then the
connection.

## Browser sign-in (S15-A)

With a `sign_in` section the console offers 「使用企业账号登录」: OpenID Connect's authorization code flow with PKCE (S256), the code exchanged by the
server (ADR 0019, docs/S15A_SCOPE.md). The console reads `GET /api/reporting/v1/sign-in` (unauthenticated, `no-store`, nothing secret; `{"enabled": false}` without the section), sends the browser
to `authorization_endpoint` with `response_type=code`, `client_id`, `redirect_uri`, `scope`, a random `state` and `nonce`, and `code_challenge` with
`code_challenge_method=S256`; the provider sends it back to `redirect_uri` (the console's page) with `code` and `state`; the console checks `state`, removes
the code from the address, and posts `{code, code_verifier, nonce}` to `POST /api/reporting/v1/sign-in/token`. The server sends `grant_type=authorization_code`,
the code, the verifier and the configured `redirect_uri` (and `client_id` for a public client) to `token_endpoint` over HTTPS, and answers the access token
only when its own verifier (`issuer`, docs/ACCESS_JWT.md) accepts it. From then on the token is used exactly like a pasted one.

**Registering the client at the provider** (to be done with the real IdP, go-live item 16):

- Client type: **public** (no secret; PKCE is the proof) is the recommended registration. A provider that insists on a confidential client gets its secret in
  `REPORTING_SIGN_IN_CLIENT_SECRET`, sent as `client_secret_basic`; `private_key_jwt` and mTLS client authentication are not supported.
- Grant: authorization code only; PKCE **required**, method `S256` only (no `plain`); no implicit or hybrid flow; refresh tokens are not used (the server drops one if the provider returns it).
- Redirect URI: exactly `sign_in.redirect_uri`, `https://<the console's host>/console/`, registered as an exact string (no wildcard, no prefix match).
- Access token: a JWT the platform's `issuer` section accepts: `iss`, `aud` (= `issuer.audience`), `scope` with `issuer.required_scope`, a lifetime within `max_token_lifetime`, signed by a key of `jwks_file`,
  and a `sub` and client that a binding lists. A provider that issues opaque access tokens cannot be used as is (`SIGN_IN_TOKEN_REFUSED`).
- Allowed origins / CORS: none needed: the browser never calls the token endpoint.

The exchange is bounded: at most 16 at once in a process (beyond it `503 SIGN_IN_BUSY`), `timeout` seconds each, an answer of at most 64 KiB, no redirect
followed. A code is redeemed once per process (`409 SIGN_IN_REPLAYED`); behind a balancer, a code replayed to another server is refused by the provider,
which must refuse a second redemption (RFC 6749 §4.1.2). The routes are unauthenticated: rate-limit `POST /api/reporting/v1/sign-in/token` at the
balancer like any login endpoint. The paste form stays hidden unless `paste_fallback` is `true`.

## Health and readiness, and the load balancer

- `GET /healthz` → `200 {"status":"ok"}`. No authentication, no dependency touched; it stays 200 for as long as the process answers (a drain closes the listener, so a new connection is refused).
- `GET /readyz` → `200 {"status":"ready"}` only if the store answers, its ledger is at the binary's version and the profile row is `production`, the connected role still satisfies the application role's rules
  (privileges and row-level security, so a grant or a `DISABLE ROW LEVEL SECURITY` made under a running server is seen), the object store answered its last check (cached for 10 seconds, so a probe is not a request
  to the bucket) and the server is not draining. Otherwise `503 {"status":"not_ready","reason":"store"|"schema"|"roles"|"object_store"|"draining"}` and nothing else. Concurrent probes share one check at a time, so a flood of probes is one query at a time, not a storm; the routes are unauthenticated and say, to anyone who can reach them, which dependency is down, so expose them to the balancer and the monitoring network and not to the internet.

Both answers carry the API's security headers and `Cache-Control: no-store`, and are mounted ahead of the console, so the console's static handler never serves them. **Point the balancer's
health check at `/readyz`** (take a server out of rotation when it is 503) and use `/healthz` only for "restart the process if it does not answer". `draining` is for the balancer to see during a
rolling stop: the first act of a drain is to turn readiness to 503; the listener closes right after, and the server waits for requests in flight and jobs in hand.

## Metrics

With `metrics_listen` set (for example `"127.0.0.1:9464"`), the server serves `GET /metrics` in the Prometheus text format on a second, plain-HTTP
listener; nothing else is on it, and nothing of it is on the API's listener. The address must be a loopback IP: the metrics carry no
authentication, so a scraper on another host reaches them through an agent or a local proxy on the same machine. Unset, there is no listener.

| Series | Kind | Labels |
|---|---|---|
| `reporting_http_requests_total` | counter | `route` (the mux pattern, as in the access log; `unmatched` for a 404), `status` (`2xx`..`5xx`) |
| `reporting_http_request_duration_seconds` | histogram | `route` |
| `reporting_jobs_run_total` | counter | none: jobs this process's workers claimed and ran to their end |
| `reporting_worker_failures_total` | counter | `code` (the platform's error code, as in `worker_failed`) |
| `reporting_sweeps_total`, `reporting_reloads_total` | counter | `outcome`: `ok` or the code of the failure or refusal |
| `reporting_schedule_passes_total` | counter | `outcome`: `ok` or the code of a failed pass of the scheduler (S16-A) |
| `reporting_deliveries_total` | counter | `outcome`: `delivered`, `rejected`, `redirected`, `throttled`, `server_error`, `unreachable` (one per attempt as a whole: with a webhook and an email both configured, the fan-out's result) |
| `reporting_http_in_flight`, `reporting_workers_busy`, `reporting_draining` | gauge | none |
| `reporting_ready` | gauge | `reason`: `none`, or the word `/readyz` would answer |
| `reporting_build_info` | gauge | `version` |

As in the log, no tenant, subject, client, integration, job id, path or query is ever a label: the series say how the server is doing, not
who uses it. Counters start at zero with the process. Alert on `reporting_ready == 0` (with its reason), a rising rate of
`reporting_worker_failures_total` or `5xx`, and `reporting_sweeps_total{outcome!="ok"}`.

## Logging

One JSON object per line on stderr (the journal): `ts`, `level`, `event` and counts and codes only. Events: `start`, `start_refused`, `reload` (outcome `ok` with the trusted `kid`s and the binding count, or `refused` with a code),
`sweep` (counts) and `sweep_failed` (a code), `schedule` (counts of runs started, advanced and whose lease was lost) and `schedule_failed` (a code), `delivery` (S16-A, S16-B: one line per deliverer and attempt: `deliverer` (`webhook` or `email`), its outcome word and status (an HTTP status, an SMTP reply code), the attempt's number, the delivery's state after the attempt as a whole and the run's and schedule's platform ids; never the endpoint, the secret, an address, a schedule's name or a mail's body), `sign_in` (one per code exchange, S15-A: `outcome` `ok` at info, else `replayed`, `busy`, `provider_refused`, `provider_client_refused`, `provider_status`, `timeout`, `unreachable`, `oversized`, `malformed` or `nonce_mismatch` at warn; never the code, the verifier, a token or the provider's text), `worker_failed` (a code: the platform's own, else `EXECUTION_FAILED`), `drain_begin`, `drain_end`, `http_server_error` (a panic in a handler or a failed handshake, with
neither the panic's value nor the client's address), and one `access` line per request: `method`, `route` (the mux pattern, e.g. `GET /api/reporting/v1/jobs/{id}`, `unmatched` for a 404), `status`, `duration_ms` and a random
`request_id` (also the `X-Request-Id` of the answer), and the request's `trace_id` and `span_id`. No query string, body, header, subject, tenant, path or error text is logged.

Tracing (S14-A, docs/S14A_SCOPE.md) follows W3C Trace Context. Every request, health and console included, gets a server span: when it carries exactly one `traceparent` header that parses
strictly (version `00` exactly 55 characters, lowercase hex, neither id all zero; a higher version read by the specification's rules; `ff` refused), the span continues that trace with a new
span id and the caller's sampled flag; otherwise it is the root of a new trace (random ids, sampled). The answer carries the server span as its own `traceparent` (`00-<trace_id>-<span_id>-<flags>`,
nothing else); `access` logs the same two ids. A `tracestate` header is ignored: not logged, not answered, not passed on. The trace id is the caller's when its header is valid, so it correlates and
proves nothing: `request_id` is always the server's own. The span is in the request's context, where a source adapter reads it (`tracecontext.FromContext`) and sends
`tracecontext.Traceparent(ctx)` to its source product. A job runs in a trace of its own, started when a worker claims it: `worker_failed` carries that `trace_id` and `span_id` (the admitting
request's trace is not stored with the job). No exporter or span store exists; the ids are for the log and the source products' logs.

## Reload (SIGHUP)

`kill -HUP` (or `systemctl reload`) re-reads `jwks_file` and `bindings_file` and calls `Verifier.Replace`: a valid change takes effect from the next authentication (a removed signing key is refused from then on, an added one
accepted; see docs/ACCESS_JWT.md for the two exposure windows); a failing one leaves the previous configuration in force and is logged by its code: `ISSUER_FILE_UNREADABLE` (missing, a symlink, not regular, too large, wrong
owner or mode), `ISSUER_FILE_INVALID` (the bindings file is not the strict list) or `ISSUER_CONFIG_REFUSED` (the access-token profile refused the whole configuration, which changes nothing), or `ISSUER_BINDING_UNSERVED` (a binding names a product that no connection of its
tenant serves; checked after the profile, ADR 0016). A successful reload logs `bindings`, `bindings_sha256` and how many bindings it added, removed and changed (`bindings_added`,
`bindings_removed`, `bindings_changed`; a binding is its subject, client and tenant, a change is another list of products); never an identifier. **Nothing else is reloaded**:
the cursor ring, the quotas, the connections, TLS (certificate renewal is manual and needs a restart), the object store, the store's connection string and every other field need a restart, rolled one server at a time.
Install a changed issuer file by writing a new file and renaming it over the old one, so that a reload never reads half a file.

## Shutdown (SIGTERM, SIGINT)

`/readyz` turns 503 `draining` at once; the loops stop claiming; the listener closes (a new connection is refused); requests in flight finish and a job a worker already holds runs to its end, both within
`shutdown_grace`; then the pools and the object store's idle connections close and the process exits 0. If the grace runs out the remaining connections are closed, the store is left open under a worker
that is still inside a call, the log says how many jobs were left (`drain_end`, `jobs_left`) and the exit code is **2**: the job keeps its lease and another process claims it once the lease (30 seconds, the
database's clock) has run out. Set the unit's `TimeoutStopSec` above `shutdown_grace`.

## The sweep

The retention sweep (`retention.sweep_interval`) is **off by default and cannot work on a production store**: the server connects as the application role, which never deletes (S04-A), so the sweep is refused and logged as
`sweep_failed` with `AUTH_SCOPE_DENIED`. Run `reporting-migrate sweep` (docs/OPERATIONS.md "Retention") from a scheduler with the sweep role. The loop exists for a store whose role may sweep, and is tested with counts and codes.

## A hardened systemd unit

```ini
# /etc/systemd/system/reporting-server.service
[Unit]
Description=Reporting platform server
After=network-online.target
Wants=network-online.target

[Service]
User=reporting
Group=reporting
EnvironmentFile=/etc/reporting/server.env            # REPORTING_STORE_DSN=…, REPORTING_OBJECT_STORE_ACCESS_KEY=…, …  (mode 0600, owner reporting)
ExecStart=/usr/local/bin/reporting-server run -config /etc/reporting/server.json
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=2
TimeoutStopSec=45                                    # above shutdown_grace (30)
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true          # not with a pdf section: the Typst sandbox needs user namespaces
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
AmbientCapabilities=CAP_NET_BIND_SERVICE             # only if listen is below 1024
SystemCallFilter=@system-service
UMask=0077
# Private directories (layout_directory, …) live under StateDirectory= and must be mode 0700:
StateDirectory=reporting
StateDirectoryMode=0700

[Install]
WantedBy=multi-user.target
```

`Restart=on-failure` restarts after exit codes 1 and 2 and after a crash, not after a clean stop. The unit is an example: it is not tested here (no systemd in the qualification), only the properties it relies on are
(exit codes, SIGHUP, SIGTERM, file modes).

## Several servers (S11-B)

With `"design_store": "database"` the designs live in the store, so any number of servers on the same database serve the same layouts, dashboards,
templates, backgrounds and images; a change of one owner's designs is one transaction that locks that owner's row, so two servers never interleave
their changes. Jobs, snapshots, artifacts and issue records were already shared (the store, the object store). What stays per process: the
requests-in-flight cap (`max_in_flight`) and the queries-in-flight quota (docs/API.md "Limits"). The start line says `design_store`.

Moving an existing deployment from the directories to the database: stop the servers, run `reporting-migrate import-designs` (docs/OPERATIONS.md
"Designs in the store"), then switch `design_store` and start them.

## Upgrade procedure

1. Build or install the new binary beside the old one. Run `reporting-server check-config -config …` with it (offline).
2. `reporting-migrate status`, then `reporting-migrate up` as the migrator role, then `reporting-migrate roles …` if a step created a table (011 `designs`, 012 `audit_events` and 013 `schedules` and `schedule_runs` do: without `roles` the server refuses to start at step store, "no INSERT … on designs", "on audit_events" or "on schedules") (docs/OPERATIONS.md "Schema lifecycle").
   The ledger check goes both ways: an **older binary refuses to start against a newer ledger, and a running older server turns `/readyz` 503 `schema`** the moment `up` has been applied. That is the check doing its work, so
   roll the servers right after the migration; a migration is not a thing to apply and then leave for a day.
3. Roll the servers **one at a time**: take one out of the balancer (`kill -TERM`; `/readyz` says `draining`), wait for it to exit (0), start the new binary, wait for `/readyz` 200, put it back, next. Binaries that read the jobs of a newer version may destroy them (docs/OPERATIONS.md "Workers and issue jobs"): never run an older worker against a store that holds newer jobs.
4. A rollback is the reverse order with `reporting-migrate down --to N` only where the step's rules allow it.

## Tested, and not

Tested here, against real TLS and real RS256 tokens: every refusal of the file and of a start; the whole session (no token 401, wrong audience or scope refused, `/session`, `/capabilities`, a query, a snapshot job, an export in the
object store, the download, a quota refusal); the rotation overlap and removal by reload, changed bindings, a broken file keeping the old configuration; readiness for each reason and its recovery, on PostgreSQL too (the database unreachable through a proxy, the schema rolled back, a privilege granted,
row-level security disabled); a drain with a request in flight, with a job in hand, out of grace (the job left claimable), the exit codes; TLS 1.2 refused; two servers on one database sharing a queue and one identity's quota; a real second process (`internal/servertesthost`).
The `http` adapter (S19-A) is tested against the reference source over loopback TLS with a pinned CA and with mTLS, against hostile sources (redirects,
oversize, slow and malformed answers, every status, answers that do not verify) and through a whole server session (`TestTheShippedRegistryServesAnHTTPSourceEndToEnd`).
Not tested: a real product's endpoint, a product behind a TLS-terminating proxy, systemd, a real load balancer, a PostgreSQL that enforces TLS and passwords (the throwaway server is `trust` and plain), certificate renewal, a drain of a PostgreSQL-backed worker out of grace, file systems where `O_NOFOLLOW` or owner checks differ,
a non-Linux host.

## Go-live checklist

Every prerequisite of the S04 documents and of this slice, in one list. **Done** means proved by tests in this repository; **Operator** is a task of the deploying organisation that the repository cannot do; **Open** is not done and not the operator's
alone to do.

| # | Item | Status | Where |
|---|---|---|---|
| 1 | The schema is migrated by the migrator role, the ledger is at the binary's version, the profile row is `production` | Done (mechanism) / Operator (run it) | S04-A; `reporting-migrate up`, `init-profile production` |
| 2 | Three roles exist (migrator, application, optionally sweep) with their grants and passwords; the server connects as the application role, which never deletes | Done (the grants and the role check) / Operator (create the roles, passwords in the secret store) | S04-A, S04-H; `reporting-migrate roles` |
| 3 | The store connection is `sslmode=verify-full` against a CA the host trusts | Done (the refusal) / Operator (the PKI, the CA, `sslrootcert`) | S04-A, S04-H |
| 4 | Row-level security is on and the policies are intact; no role of the platform has `BYPASSRLS` | Done (the policies, the role check, readiness `roles`) | S04-C |
| 5 | Retention: a scheduler runs `reporting-migrate sweep` with the sweep role, and its counts are watched | Operator (the schedule and the watch) / Done (the command and the ledger) | S04-B |
| 6 | Quotas are chosen for the organisation's identities and the request cap and pool for its load | Done (the mechanisms and bounds) / Operator (the numbers) | S04-D, S04-G; `quotas`, `max_in_flight`, `store.pool_max_conns` |
| 7 | The object store: bucket, policy, keys in the secret store, a CA if private; artifact files in it | Done (client, start and readiness checks, sweep reconciliation) / Operator (the bucket and its policy, versioning, the keys) | S04-E |
| 8 | Backups of the database **and** the bucket, taken so that the bucket copy is not older than the database's; a restore rehearsed; `verify-objects --deep` before the server starts | Operator (the backup tools and the drill) / Done (`verify-objects`, the invariants, one local drill) | S04-F |
| 9 | Capacity: the numbers of docs/CAPACITY.md are one host's, synthetic, hot-cache and not a plan; a load test on the real deployment | Operator (load test) / Open (no production capacity claim) | S04-G |
| 10 | The cursor key ring: 32+ random bytes in the secret store, rotated by the four-step rolling restart, at most every 90 days | Done (the ring, the rotation proofs) / Operator (generation, custody, schedule) | S04-H |
| 11 | Issuer key rotation by reload: overlap, then removal; the two exposure windows understood (a leaked signing key vs a stolen token, bounded by `max_token_lifetime`) | Done (S04-H proofs, SIGHUP reload here) / Operator (the procedure with the real issuer) | S04-H, docs/ACCESS_JWT.md |
| 12 | Database and object store credentials rotated by a rolling restart with the stated window | Done (the failure side proved) / Operator (the schedule) | S04-H |
| 13 | TLS certificate for the server, renewed before it expires (a restart) | Operator (no automatic renewal) | S05-A |
| 14 | The unit, the user, the file modes, `TimeoutStopSec` above `shutdown_grace`, the balancer pointed at `/readyz` | Operator (the example above is untested) | S05-A |
| 15 | Logs shipped and watched (`worker_failed`, `reload` refusals, `drain_end` with `clean:false`, `http_server_error`); metrics scraped from `metrics_listen` and alerts on `reporting_ready` and the failure counters | Done (the log events, the metrics listener, trace ids in the log and the `traceparent` header, S14-A) / Operator (shipping, scraping, the alerts) / Open (no span exporter; a job's trace is not the admitting request's) | S05-A, S14-A, "Metrics", "Logging" |
| 16 | **The real issuer**: its JWKS, the real bindings (subject, client, tenant, integrations), its lifetime policy, discovery and emergency revocation; the console's client registered for the browser sign-in (public, PKCE S256, the exact redirect URI) and the `sign_in` section filled in | **Open** (waits for the real IdP contract; docs/ACCESS_JWT.md) / Done (the generic sign-in, tested against a fake provider, S15-A) | S02-A, S15-A, "Browser sign-in" |
| 17 | **Each source product implements the reporting source protocol** (catalog, prepared reads, current authorization, and the issue authority when it issues) and **passes `reporting-source-check`** against the endpoint, credential and CA its `http` connection will use | Done (the protocol, the adapter `http` in the shipped command, the reference source and the checker, S19-A) / **Open** (each product's implementation and its passing run, with the product team) | docs/SOURCE_HTTP_PROTOCOL.md, docs/REAL_SOURCE_ONBOARDING.md |
| 18 | **Data reconciliation** with the product: totals, the issue records against the product's numbers after a restore, the grants | **Open** (needs the real adapter) | S03-H, S04-F |
| 19 | **The admission decision** itself: `production_approved` changes only by a code change after it | **Open** | docs/s00/MIGRATION.md |
