# Deploying the server (S05-A)

`reporting-server` (`cmd/reporting-server`, package `server`) is the production composition of the platform: one strict configuration file, secrets from files
and the environment, and the adapters a host registers, turned into a running server with health and readiness endpoints, JSON logs that carry counts and codes
only, an atomic reload of the issuer files and a graceful shutdown. It is a mechanism with its qualification, **not a production admission**: `GET /capabilities`
says `production_approved: false` whatever the configuration says (there is no configuration field for it; changing it is a code change after the admission
decision), and the repository contains no adapter for any real source product. The go-live checklist at the end says what is done, what the operator does and what is open.

## The command and the host's `main`

```
reporting-server check-config -config FILE   # offline: parses and validates the file, the files it names, the JWKS, the bindings, the cursor keys, the quotas; prints counts, never a value
reporting-server run -config FILE            # start, serve, drain
reporting-server version                     # the binary's version and the schema version it needs
```

The shipped command registers **no adapter**, so `run` stops at the adapters step with `no adapter is registered for integration "X"`. A host that has an adapter
(the integration slice's) builds its own command; the whole of its `main` is:

```go
package main

import (
	"os"

	"idp.local/reporting/server"
)

func main() {
	server.Main(os.Args, server.Registry{"my-product": myProductFactory}) // func(options json.RawMessage, getenv func(string) string) (reporting.Adapter, error)
}
```

`options` is the connection's `options` object from the configuration, untouched and never logged; the factory reads its own secrets from `getenv` by names the
host chooses, and its error is never shown (the start line says which connection failed). The contract simulators are test fixtures: they report themselves as
simulators (`IsSimulator()`), and `Run` refuses a registry entry that returns one unless the configuration says `"allow_simulator": true`, which the example below
does not contain; with it, `/capabilities` states the profile as `simulated:<profile_label>`.

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
    { "tenant": "acme", "integration": "my-product", "source_tenant": "acme-prod", "adapter": "my-product", "options": { "base_url": "https://product.internal" } }
  ],
  "quotas": { "jobs_in_flight": 8, "stored_results": 64, "admissions_per_hour": 120, "queries_in_flight": 4 },
  "retention": { "sweep_interval": 0 },
  "profile_label": "staging-eu"
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
| `issuer.jwks_file`, `.bindings_file` | required | The public keys (at most 64 KiB, 16 RSA keys of 2048..4096 bits, e=65537) and the list of `{subject, client_id, tenant, integrations}` (strict, at most 4096). Re-read by a reload. |
| `cursor_key_files` | required | 1..4 files, newest first; each holds one key, hex or base64 (a text valid as both is read as hex), 32..64 bytes; two files may not hold the same key. |
| `connections` | required | 1..256 entries `{tenant, integration, source_tenant, adapter, options}`; `(tenant, integration)` unique; `adapter` names a registry entry. |
| `quotas.*` | 8, 64, 120, 4 | `jobs_in_flight` 1..1000, `stored_results` 1..100000, `admissions_per_hour` 1..100000, `queries_in_flight` 1..64; 0 is the default of each. |
| `retention.sweep_interval` | 0 (off) | 0 or 60..604800 seconds. See "The sweep" below. |
| `retention.grace`, `.batch` | 24 h, 500 | `grace` 0 (default) up to 10 years in seconds; `batch` 0 (default) up to 10000. |
| `pdf.typst`, `.sha256`, `.fonts` | none | All three or none (the pinned Typst and the only fonts PDF export may use). |
| `layout_directory`, `dashboard_directory`, `template_directory`, `background_directory` | none | Private single-process directories (mode 0700, owned by the user the server runs as), as in the demo. They are **per process**: two servers do not share them. |
| `design_store` | `directories` | `directories` (the four private directories above, one server each) or `database`: every design (layouts, dashboards, templates, backgrounds, images) in the platform's store, table `designs` (migration 011), shared by every server on it; the four directory fields must then be absent, and the four features are on. See "Several servers" below (S11-B, ADR 0017). |
| `embed_origins` | none | `{integration: [origin]}`, validated as `embedding.New` does (https origins only here); every integration must be served by a connection. |
| `profile_label` | `unqualified` | 1..64 printable ASCII characters, shown in `/capabilities`. |

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

A symbolic link is refused for the file itself (it is opened with `O_NOFOLLOW`); a link in a directory above it is the operator's layout and is not checked.

## What a start does, in order

Each failure is a refusal whose text is fixed and names the step (`reporting-server: start refused at step "store": …`) and is logged as `start_refused` with the step only:
**configuration** (the file's own rules, including a ring of five, plain HTTP on a public address) → **secrets** (cursor keys, the connection string's presence, the object store keys, the
TLS pair) → **issuer** (the access-token profile; then every product of every binding must be a connection of the binding's tenant, ADR 0016) → **adapters** (an unregistered or unknown adapter name, a factory that fails, a simulator without the allowance) → **store**
(`OpenProduction`; its own refusals keep their text, any other error, a connection failure, is reduced to a fixed text because it may name the host or the user) → **object store**
(`HEAD bucket`) → **pdf**, **private directories** → **service** → workers and sweep → **listen** (`the listen address cannot be bound`). A refused start releases everything it
opened and stops the workers it started. The start line (`start`) carries the binary's version, the ledger's version, the number of cursor keys, the connections and workers, the trusted `kid`s,
and whether TLS, an object store, PDF, issuance and embedding are on, and (ADR 0016) `connections_sha256`, `bindings` and `bindings_sha256`: the digests of
what is in force, the same that `check-config` prints, so that a reviewed file can be matched to a running server.

**Adding a product** (ADR 0016): add the connection to the configuration file and restart (rolled), then add the product to the bindings and reload. A
binding that names a product no connection of its tenant serves is refused, at start and in `check-config` with the number of such products, at reload with
`ISSUER_BINDING_UNSERVED`. `GET /session` lists only the products the server serves. Removing a product goes the other way: the bindings first, then the
connection.

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
| `reporting_http_in_flight`, `reporting_workers_busy`, `reporting_draining` | gauge | none |
| `reporting_ready` | gauge | `reason`: `none`, or the word `/readyz` would answer |
| `reporting_build_info` | gauge | `version` |

As in the log, no tenant, subject, client, integration, job id, path or query is ever a label: the series say how the server is doing, not
who uses it. Counters start at zero with the process. Alert on `reporting_ready == 0` (with its reason), a rising rate of
`reporting_worker_failures_total` or `5xx`, and `reporting_sweeps_total{outcome!="ok"}`.

## Logging

One JSON object per line on stderr (the journal): `ts`, `level`, `event` and counts and codes only. Events: `start`, `start_refused`, `reload` (outcome `ok` with the trusted `kid`s and the binding count, or `refused` with a code),
`sweep` (counts) and `sweep_failed` (a code), `worker_failed` (a code: the platform's own, else `EXECUTION_FAILED`), `drain_begin`, `drain_end`, `http_server_error` (a panic in a handler or a failed handshake, with
neither the panic's value nor the client's address), and one `access` line per request: `method`, `route` (the mux pattern, e.g. `GET /api/reporting/v1/jobs/{id}`, `unmatched` for a 404), `status`, `duration_ms` and a random
`request_id` (also the `X-Request-Id` of the answer). No query string, body, header, subject, tenant, path or error text is logged.

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

Moving an existing deployment from the directories to the database needs its designs imported (S11-C, docs/ADR_0017_ENTERPRISE_ROADMAP.md);
until then keep `directories` on one server.

## Upgrade procedure

1. Build or install the new binary beside the old one. Run `reporting-server check-config -config …` with it (offline).
2. `reporting-migrate status`, then `reporting-migrate up` as the migrator role, then `reporting-migrate roles …` if a step created a table (011 `designs` does: without `roles` the server refuses to start at step store, "no INSERT and UPDATE on designs") (docs/OPERATIONS.md "Schema lifecycle").
   The ledger check goes both ways: an **older binary refuses to start against a newer ledger, and a running older server turns `/readyz` 503 `schema`** the moment `up` has been applied. That is the check doing its work, so
   roll the servers right after the migration; a migration is not a thing to apply and then leave for a day.
3. Roll the servers **one at a time**: take one out of the balancer (`kill -TERM`; `/readyz` says `draining`), wait for it to exit (0), start the new binary, wait for `/readyz` 200, put it back, next. Binaries that read the jobs of a newer version may destroy them (docs/OPERATIONS.md "Workers and issue jobs"): never run an older worker against a store that holds newer jobs.
4. A rollback is the reverse order with `reporting-migrate down --to N` only where the step's rules allow it.

## Tested, and not

Tested here, against real TLS and real RS256 tokens: every refusal of the file and of a start; the whole session (no token 401, wrong audience or scope refused, `/session`, `/capabilities`, a query, a snapshot job, an export in the
object store, the download, a quota refusal); the rotation overlap and removal by reload, changed bindings, a broken file keeping the old configuration; readiness for each reason and its recovery, on PostgreSQL too (the database unreachable through a proxy, the schema rolled back, a privilege granted,
row-level security disabled); a drain with a request in flight, with a job in hand, out of grace (the job left claimable), the exit codes; TLS 1.2 refused; two servers on one database sharing a queue and one identity's quota; a real second process (`internal/servertesthost`).
Not tested: systemd, a real load balancer, a PostgreSQL that enforces TLS and passwords (the throwaway server is `trust` and plain), certificate renewal, a drain of a PostgreSQL-backed worker out of grace, file systems where `O_NOFOLLOW` or owner checks differ,
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
| 15 | Logs shipped and watched (`worker_failed`, `reload` refusals, `drain_end` with `clean:false`, `http_server_error`); metrics scraped from `metrics_listen` and alerts on `reporting_ready` and the failure counters | Done (the log events, the metrics listener) / Operator (shipping, scraping, the alerts) / Open (no tracing) | S05-A, "Metrics" |
| 16 | **The real issuer**: its JWKS, the real bindings (subject, client, tenant, integrations), its lifetime policy, discovery and emergency revocation | **Open** (waits for the real IdP contract; docs/ACCESS_JWT.md) | S02-A |
| 17 | **A real adapter** for each source product and its contract (catalog, prepared reads, current authorization, issue authority), registered in the host's `main` | **Open** (the integration slice) | docs/REAL_SOURCE_ONBOARDING.md |
| 18 | **Data reconciliation** with the product: totals, the issue records against the product's numbers after a restore, the grants | **Open** (needs the real adapter) | S03-H, S04-F |
| 19 | **The admission decision** itself: `production_approved` changes only by a code change after it | **Open** | docs/s00/MIGRATION.md |
