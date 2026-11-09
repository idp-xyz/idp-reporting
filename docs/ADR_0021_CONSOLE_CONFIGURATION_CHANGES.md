# ADR 0021 · Changing data sources and bindings from the console

Date: 2026-11-09. Status: **proposed** (the product owner decides; nothing here is implemented). Context: ADR 0016, ADR 0018, S18-B.

## Context

ADR 0016 decided that product connections and bindings are operator configuration: the connections in the server's configuration file, the
bindings in the bindings file, both reviewed in the operator's change process; the platform has no endpoint that writes either, and the console
shows the state without changing it. Two of its reasons were that the platform had no administrator identity or authorization model (the
access token carries no role) and that a writable administration API would become the most valuable target in the service. S12-A (ADR 0018)
has since added roles, among them `administrator`, read from the bindings; S12-B records people's actions in an append-only audit; S18-B shows
an administrator the configuration in force, read-only.

So the first reason no longer holds. The others do: a connection needs an adapter the host registers in its own reviewed code (a form cannot
create one), a connection's options and secrets are infrastructure (hosts, certificates, credentials read from the environment), and a
binding decides who may read which product, which is a security decision that today has a reviewer.

## Options

1. **Keep ADR 0016 (no writes).** The console shows (S18-B); the operator changes files; a reload or restart applies them; the digests on the
   page and in the log say which version is in force. Nothing new to attack. Cost: every change of a binding waits for the operator.
2. **Bindings through the platform, with approval; connections stay in files.** An administrator proposes a change of a binding of their own
   tenant (add or remove a product of the tenant's existing connections, change roles); a second administrator approves it (never the
   proposer, as S12-C's publication); the approved change is written to a bindings store, recorded in the audit with the before and after,
   and applied like a reload. Guard rails: only products of connections that already exist for the tenant; no administrator can remove the
   last administrator of the tenant or grant themselves a role; a per-tenant switch, off by default; the file stays authoritative when the
   deployment keeps one (the platform's store is then refused). Connections, adapters, options, secrets and the webhook remain files and
   environment.
3. **Connections and bindings through the platform.** As 2, plus creating and editing connections from the registered adapters, with their
   options in the platform's store and their secrets in a secret manager the platform reads. Largest convenience; it moves infrastructure
   configuration and secret handling into the reporting service, which then needs the controls of a configuration management system.

## Recommendation

Option 2 when a customer asks for self-service bindings, and option 1 until then. Bindings change often (people join and leave) and are
already tenant-scoped data with an approval pattern the platform has (S12-C); connections change rarely, need code anyway, and carry secrets.
Option 3 is not recommended.

## If option 2 is chosen: the work

- A `bindings` table (migration), row-level security per tenant, the verifier reading it alongside or instead of the file, the same checks
  as the file (served products, known roles, at most 4096).
- Routes under `/admin/bindings` (administrator only): propose, list proposals, approve, reject; the approval is 409 `APPROVAL_REQUIRED` for
  the proposer, as S12-C.
- The audit's actions `binding.propose`, `binding.approve`, `binding.reject`, recorded before they apply (fail closed, as ADR 0018 Q4).
- The 管理 page gains a proposal form and an approvals list; the identity provider's group mapping (go-live checklist item 16) decides whether
  subjects are typed or chosen.
- A security review of the new write path, and a test that no route lets an administrator act outside their tenant.

## Questions for the product owner

1. Option 1, 2 or 3?
2. With option 2: is a second administrator's approval required for every change, or only for granting `administrator` and `issuer`?
3. With option 2: may a deployment keep the bindings file authoritative (the platform's writes refused), as a compliance setting?
