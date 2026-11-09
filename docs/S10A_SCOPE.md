# S10-A product connections on the server: served bindings, what is in force

2026-11-09. ADR 0016 decisions 2 and 3.

A1 · A binding must be served. `unservedBindings` counts the products of the bindings that no connection of the binding's tenant
serves. `newVerifier` (start, `check-config`) refuses such a file at step **issuer** with the count and the order to follow; `Reload`
judges the files with the access-token profile first (its refusals keep `ISSUER_CONFIG_REFUSED`), then refuses unserved products with
`ISSUER_BINDING_UNSERVED`, the previous bindings staying in force. `GET /session` lists `Service.Served(p)`: the principal's products
the service has a connection for, in the principal's order (the filter the issue and job listings already use).

A2 · What is in force. The bindings file's SHA-256 as read, and the connections' (sorted by tenant and product; tenant, product,
source tenant, adapter and options, NUL-separated). The start line carries `connections_sha256`, `bindings` and `bindings_sha256`;
a successful reload `bindings_sha256` and `bindings_added`, `bindings_removed`, `bindings_changed` (a binding is subject, client and
tenant; a change is another set of products). `check-config` prints both digests. No identifier is logged.

Tests: `server/connections_test.go` (a reload refused for an unserved product, beside a served one, under another tenant, the
profile's refusal first; the start line and `check-config` agree; the counts of an unchanged, an added and a removed binding; nothing
named in the log; `check-config`'s refusal; the digest's canonical order; the change counts), two cases in
`TestStartRefusalsNameTheStepAndCarryNoSecret`, `transport/httpapi/session_served_test.go`.

Upgrade: a deployment whose bindings already name an unserved product does not start with this version; `check-config` finds it first.
