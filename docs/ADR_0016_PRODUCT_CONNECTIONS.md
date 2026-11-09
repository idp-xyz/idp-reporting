# ADR 0016 · Product connections and bindings are operator configuration, checked, recorded and shown

Date: 2026-11-09. Status: accepted (implementation: S10-A on the server, S10-B in the console).

## Context

A 产品连接 is what lets an identity read a source product through the platform. Two things make one, and both exist:

- **The connection**: `connections` in the server's configuration file (docs/DEPLOYMENT.md): `{tenant, integration, source_tenant,
  adapter, options}`, the adapter built by a factory the host registers in its own reviewed `main` (`server.Registry`), its secrets
  read from the environment by names the host chooses. Changing one needs a restart, rolled one server at a time.
- **The binding**: `issuer.bindings_file`: `{subject, client_id, tenant, integrations}` per access-token subject and client, at most
  4096, at most 32 products each, no wildcard. A token cannot widen it (docs/ACCESS_JWT.md). It is re-read by `SIGHUP`.

What a person sees in the console is the binding's list, as `GET /session` returns it; every read is then refused unless the
connection exists and the source product authorizes it now.

Reading the code for a product-level design (2026-11-09) found what is missing, not a missing owner:

1. **Nothing checks a binding against the connections.** A binding may name a product that no connection serves for its tenant.
   The server starts, `GET /session` lists the product, the console offers it, and every request on it is refused. The embed
   configuration has had this check since S03-N; the bindings never had it.
2. **No record of what is in force.** A start logs the number of connections; a reload logs the number of bindings. Nobody can
   tell from the server which file it runs, or what a reload changed.
3. **The console cannot say why.** A product whose source does not answer, or an identity with no product, ends in a generic
   refusal ("无法验证的响应" when the session lists none).

The alternative, a writable administration API and page in the platform, was weighed and refused for now: it needs an
administrator identity and an authorization model the platform does not have (the access token carries no role, by design), it
would become the most valuable target in the service, and it would move a security decision (who may read which product) from a
reviewed file into a request. The bindings are the identity provider's contract (go-live checklist item 16); the connections need
the host's code anyway (item 17).

## Decision

1. **Ownership stays where it is.** Connections are the configuration file's, bindings the bindings file's; both are the operator's,
   reviewed in the operator's change process. The platform has no endpoint that writes either. The console stays the designer's
   workbench (ADR 0006); it shows the state, it does not change it.
2. **A binding must be served.** Every product of a binding must be a connection of the binding's tenant. At start and in
   `check-config` a binding that names an unserved product refuses the configuration (`issuer.bindings_file`, the count, no
   identifier); at reload it is refused with `ISSUER_BINDING_UNSERVED` and the previous bindings stay in force. The check runs after
   the access-token profile has accepted the file, so its refusals keep their codes. `GET /session` lists only the products the
   service serves for the principal (the same filter the issue listing uses), so a gap can never reach the console.
3. **What is in force is recorded.** The start line and every successful reload carry `bindings_sha256` (of the file as read) and
   the start line `connections_sha256` (of the sorted `tenant, integration, source_tenant, adapter, options`); a reload also says how
   many bindings it added, removed and changed (a binding is its subject, client and tenant; a change is another list of products).
   `check-config` prints the same two digests, so a reviewed file can be matched to a running server. No subject, client, tenant or
   product is logged: the bindings are personal data.
4. **The console says what it knows** (S10-B). The data drawer and 能力与环境 show each product of the session with the state its
   catalog answered (available, the source refused, the source did not answer); an identity with no product gets a page that says
   so and who to ask, not a refusal.

Not decided here, waiting for their contracts: the real adapters (item 17), the real issuer and its bindings (item 16), and a
health probe of a source (an optional adapter method; the S09 metrics keep their rule of no product label).

## Consequences

An operator who adds a product edits the configuration (restart) before the bindings (reload), in that order; the reverse order is
refused with a code that names the problem. A reload that changes nothing is visible as `added 0, removed 0, changed 0` with the
same digest. Hosts with bindings that already name unserved products must fix them before upgrading; `check-config` finds them.
