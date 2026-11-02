# S05-A the production server: composition, configuration, lifecycle

2026-11-02. S04 made the stores, the workers and the operator tools ready for production. What is missing is the process
itself: the only server in the repository is `cmd/reporting-demo`, which authenticates with a demo bearer, talks to the
guarded fixture databases only and requires `-profile contract-simulator`. The operations guide's step "start the server"
therefore names no command. This slice writes the production composition: one package that turns a configuration file,
secrets from the environment and files, and the adapters of the source products into a running server, and a command
that runs it. It does **not** write an adapter for a real source product (that is the integration slice, last), and it
does not grant production admission: `GET /capabilities` still says `production_approved: false`, and that value changes
only by a code change after the admission decision.

S1 · Shape. `idp.local/reporting/server` (package `server`) exports:

- `Config` (the parsed, validated configuration), `LoadConfig(path)`, `Registry` (`map[string]AdapterFactory`, where
  `AdapterFactory func(options json.RawMessage, getenv func(string) string) (reporting.Adapter, error)`), and
  `Run(ctx, cfg, reg, getenv, logOut) error` (start, serve, drain, return);
- `cmd/reporting-server`: `reporting-server check-config -config FILE` (offline: parses and validates the file, the
  referenced files' existence, sizes and permissions, the JWKS and bindings, the cursor keys, the quotas; prints
  counts and the first problem, never a value), `reporting-server run -config FILE`, and `reporting-server version`.
  The command registers **no adapter**, so `run` refuses to start with "no adapter is registered for integration X"; a
  host that has an adapter writes a three-line `main` that calls `server.Main(os.Args, myRegistry)`. `docs/DEPLOYMENT.md`
  shows it. The contract simulators are never registered by the shipped command (they are test fixtures; a registry
  entry whose adapter reports itself as a simulator is refused by `Run` unless the config says `"allow_simulator":
  true`, which the shipped example does not contain, and `/capabilities` then states the profile as simulated).

S2 · Configuration: one JSON file (`-config`), at most 256 KiB, strict: unknown fields, duplicate keys, trailing
data and a non-UTF-8 file are refused. Secrets are never in it: a field named like a secret (`dsn`, `password`,
`secret`, `token`, `key` with an inline value) is refused; secrets arrive as file paths (read at start, refused unless the
file is a regular file owned by the process's user, mode 0600 or 0400, not a symlink out of its directory) and environment
variables (`REPORTING_STORE_DSN`, `REPORTING_OBJECT_STORE_ACCESS_KEY`, `_SECRET_KEY`). Contents:

- `listen`, `tls_cert_file`, `tls_key_file` (TLS 1.3 only; a non-loopback `listen` without TLS is refused; plain HTTP is
  accepted only on a loopback IP with `"allow_plain_loopback": true`, for a host behind a trusted local proxy), timeouts
  and `max_header_bytes` with the demo's values as defaults, `max_in_flight`, `workers` (default 2), `shutdown_grace`
  (default 30 s);
- `store`: the pool size (`pool_max_conns`, checked as `OpenProduction` does) and `workers`; the DSN is the environment's;
- `object_store`: endpoint, bucket, region, `ca_file`, `loopback_http`, as S04-E;
- `issuer`: `issuer`, `audience`, `required_scope`, `max_token_lifetime` (seconds, 1..3600, required), `jwks_file`,
  `bindings_file` (JSON list of `{subject, client_id, tenant, integrations}`);
- `cursor_key_files` (1 to 4 files, newest first; each file holds the key base64 or hex-encoded, 32 to 64 bytes);
- `connections`: a list of `{tenant, integration, source_tenant, adapter, options}`; `adapter` names a registry entry, `options`
  is passed to its factory untouched (and never logged);
- `quotas` (the four of S04-D/G), `retention` (`sweep_interval`, `grace`, `batch`), `pdf` (typst path, sha256, fonts), the
  private directories (`layout_directory`, `dashboard_directory`, `template_directory`, `background_directory`),
  `embed_origins` (integration to origins, validated as `embedding.New` does), `profile_label` (shown in `/capabilities`,
  default `unqualified`).

S3 · Start-up, in this order, each failure a refusal with a fixed text naming the failed step and never a secret: load and
validate the configuration; read the secrets; build the cursor ring, the issuer verifier, the registry's adapters (one per
connection; an unknown name, a duplicate (tenant, integration) or an adapter that fails its factory is a refusal); open the
store with `OpenProduction` (verify-full unless the DSN is loopback and the config allows it for a test, the production profile, the
ledger at the binary's version, the role check); open the object store and `Check` it; build the service; start the workers
and the sweep; listen. `GET /capabilities` carries the `profile_label`. The demo's behaviour is untouched.

S4 · Endpoints and logging.

- `GET /healthz`: 200 `{"status":"ok"}` while the process is up and not draining. No authentication, no dependency touched.
- `GET /readyz`: 200 only if the store answers, its ledger is at the binary's version and the role check is clean, the
  object store (when configured) answered its last check (cached for 10 s), and the server is not draining; otherwise 503
  `{"status":"not_ready","reason":"store"|"schema"|"roles"|"object_store"|"draining"}`; nothing else in the body.
  Both answers carry the security headers of the API and `Cache-Control: no-store`; neither is reachable
  through the console's static handler.
- Log lines are JSON on the writer given (stderr in the command), one per event, counts and codes only: start (the
  configuration's counts, the binary version, the ledger version, the cursor ring size, the trusted `kid`s), reload
  outcomes, sweep counts, worker failures by code, drain begin/end, one access line per request (method, route
  pattern, status, duration in ms, a random request id; no query string, no body, no header, no subject, no tenant). A test
  runs a whole session with a known token, subject, tenant, cursor key, DSN password and object store key and asserts
  none of them appears in any line.

S5 · Lifecycle.

- **Reload (SIGHUP; `Server.Reload()` for tests):** re-reads the issuer files (`jwks_file`, `bindings_file`) and calls
  `Verifier.Replace` (S04-H): a valid change takes effect from the next authentication; a failing one leaves the previous
  configuration in force and is logged by its code. Nothing else is reloaded (the cursor ring, the quotas, the connections,
  TLS need a restart; say so in DEPLOYMENT.md). The log line names the trusted `kid`s after a successful reload.
- **Shutdown (SIGTERM/SIGINT; `ctx` cancel):** `readyz` turns 503 `draining` at once; the listener closes; in-flight requests
  finish within `shutdown_grace`; the workers stop claiming and let a running job finish within the same grace (a job still
  running when the grace ends keeps its lease and is claimed by another process after it runs out; log the count);
  the pools close; the exit code is 0 for a clean drain, 1 for a refused start, 2 for a drain that ran out of grace.
- A second `Run` of the same config on another port while the first is up works (stateless processes; the stores do the
  coordinating): test it with two servers on one database sharing a job queue and one identity's quota.

S6 · Proof (the PostgreSQL and object store qualification of the repository; test adapters are the contract simulators,
registered by the test only):

- configuration: every refusal above (unknown field, duplicate key, oversized, inline secret, file permissions, a
  symlink, TLS missing on a public address, a ring of five, a missing adapter, an unknown adapter, a simulator without
  the allowance), each with a text that carries no secret;
- an end-to-end session over real TLS (a certificate generated by the test) with real RS256 access tokens from a test
  issuer: no token 401; a token of the wrong audience or scope refused; a valid token: `/session`, `/capabilities`, a
  query, a snapshot job, an export (stored in the object store when configured), the download, a quota refusal;
- reload: a rotated JWKS (overlap then removal) and changed bindings take effect without a restart; a broken file keeps
  the old configuration and logs the code;
- readiness: not ready while the store is unreachable or behind in schema or the role check fails, ready again after;
  draining on shutdown; health stays 200 until the listener closes;
- shutdown: an in-flight request completes, a new connection is refused afterwards, a worker mid-job either finishes inside
  the grace or leaves a claimable job, exit codes as above;
- logging hygiene as in S4; TLS 1.2 client refused; plain HTTP on a public address refused at start;
- two servers on one database.

S7 · Docs. `docs/DEPLOYMENT.md`: the configuration reference (every field, default, bound), how each secret is provisioned,
a hardened systemd unit as an example (`NoNewPrivileges`, `ProtectSystem`, `PrivateTmp`, a non-root user, `Restart=on-failure`,
`ExecReload` with SIGHUP, `TimeoutStopSec` above `shutdown_grace`), the upgrade procedure (migrate, then roll the
servers one at a time; the ledger check refuses an older binary), the three-line host `main`, what `/readyz` means to a load
balancer, and the **go-live checklist**: every S04 document's prerequisites in one list, each item marked done by tests in this repository, an
operator task, or **open** (the real issuer and bindings, a real adapter and its contract, data reconciliation with the product,
the admission decision itself). OPERATIONS.md's "start the server" steps point to it; README S05-A at the top with the "以下为此前阶段历史说明：" line
moved directly below it. API.md and CONSOLE.md only where an endpoint is added (`/healthz`, `/readyz`).

Not in this slice: an adapter for any real source product, the real identity provider (discovery, emergency revocation), production
admission, containers or orchestration manifests beyond the systemd example, metrics endpoints and tracing (a later slice if wanted), automatic
certificate renewal, reloading anything but the issuer files.
