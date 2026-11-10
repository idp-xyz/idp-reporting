# ADR 0022 · Source products connect through a fixed protocol, served by the platform's own adapter

Date: 2026-11-09. Status: accepted (the product owner asked for S19-A on that day; implementation: S19-A, docs/S19A_SCOPE.md). It changes
how go-live item 17 is met and what the shipped command registers; no request, stored record or adapter contract changes.

## Context

The adapter contract (`reporting.Adapter`, and `reporting.DocumentAuthority` for issuing) is Go. Until now a source product connected only
through Go code written for it and registered in a host's own `main` (S05-A: "the shipped command registers no adapter; a host's registry
is its own code to review"; ADR 0016: "the connections need the host's code anyway"). Every product would therefore need the platform's
team, or a host, to write, review and ship an adapter: per product, in the platform's language, against the product's private API. That
puts the platform in the product's release cycle and puts product-specific code next to the core that holds every tenant's data.

What an adapter must do is already fixed and product-neutral: four read operations and three issue operations, each with a precise
contract and refusals (docs/s00/INTERFACES.md, docs/REAL_SOURCE_ONBOARDING.md, ADR 0003). Only the transport is missing.

## Decision

1. **A protocol, not per-product code.** The reporting source protocol (docs/SOURCE_HTTP_PROTOCOL.md, docs/source-openapi.json) carries
   the contract as JSON over HTTPS, under a versioned path (`/reporting-source/v1/`), every route a `POST` with the source context in the
   body, a closed set of error codes each with its status, exact-string values, bounds on every size. A product implements it in its own
   service, in its own language, behind its own authorization.
2. **One adapter, the platform's.** `adapters/httpsource` implements the protocol against `reporting.Adapter` (and `DocumentAuthority`,
   `VersionedAuthority` when the connection's `issuance` says so). It is standard-library-only, platform-reviewed, and registered in the
   shipped command as `http` (`server.DefaultRegistry`). It is not a simulator. A host may still register Go adapters of its own.
3. **The adapter trusts nothing it has not verified.** Every answer is held to what was asked before it reaches the service, which checks
   it again; an answer that does not verify is an integrity failure, a source that cannot be reached or answers outside the protocol is
   unavailable. The adapter's texts are fixed: no URL, credential or answer byte.
4. **The platform authenticates as the platform; the source authorizes the person.** The service credential (bearer from the environment,
   and/or an mTLS client certificate) proves only that the platform is asking. Who is asking is the context (platform tenant, issuer,
   subject, integration, source tenant), which the source maps to its own actor and authorizes by its own current rights. The person's
   access token never leaves the platform.
5. **Conformance is checked before a connection exists.** `reporting-source-check` exercises the protocol through the platform's own
   adapter against a product's endpoint; go-live item 17 becomes "the product implements the protocol and passes the checker". The checker
   is necessary, not sufficient: reconciliation (item 18) stays.
6. **What is not in version 1 waits for its capability.** The company directory (ADR 0015) is not implemented in the core yet; it will be an
   additive route of version 1 (S19-B). A breaking change is a new path prefix.

## Consequences

A product team works from a document, an OpenAPI description, a reference implementation (`adapters/httpsource/sourcetest`) and a
checker, without the platform's release cycle. The platform's contract tests run through the protocol unchanged (the whole adapter
contract and the document authority's conformance suite), which is the evidence that a product behind the protocol is held to the same
rules as an adapter in process. The cost is a network hop per call (the service's own bounds, `WorkTimeout` and the 15-minute read
lifetime, are unchanged) and one more credential per product to provision and rotate (docs/OPERATIONS.md, docs/DEPLOYMENT.md "Secrets").
A leaked service credential lets its holder ask the product anything it would answer the platform for any context: products must
authorize by the context and accept the credential from the platform's addresses only; mTLS is preferred.
