# ADR 0017 · From a qualified single server to an enterprise reporting platform: the order of the work

Date: 2026-11-09. Status: accepted. A plan of slices, each with its own scope document and pull request; this ADR is updated as they land.
Progress: S11-A, S11-B, S11-C, S12-A, S12-A2, S12-B, S12-C, S12-D, S13-A, S14-A, S15-A, S16-A, S17-A merged (S15-A: ADR 0019; S16-A: ADR 0020); S18 written (docs/S18_SCOPE.md: the schedules page, the administrator's read-only view of data sources and bindings; ADR 0021 proposed); S19-A written (docs/S19A_SCOPE.md: the reporting source protocol and the generic adapter `http` in the shipped server, ADR 0022); S19-B written (docs/S19B_SCOPE.md: the company directory of ADR 0015 on the server and in the protocol); S16-B written (docs/S16B_SCOPE.md: email as a second deliverer of a scheduled run's notification, ADR 0020's amendment).

## Context

The platform's core is in place and qualified on its own terms: the source's authority over every read, frozen snapshots, verified exports and
source-confirmed issue, row-level security, quotas, retention, object storage, a production server with strict configuration, metrics, the
real-browser checks in CI. Reviewing it as an enterprise reporting platform (2026-11-09) found three kinds of gap.

1. **What waits for others** (go-live checklist items 16–19): the real identity provider and its bindings, a real adapter for each source
   product and the reconciliation of its data, the admission decision.
2. **What a single server cannot do.** The designs (report layouts, dashboards, document templates, page backgrounds, images) are kept in
   private directories that one process locks (`privatedocs`): two servers do not share them, so the platform cannot scale out or survive the
   loss of its host without losing its designs' availability. Each design belongs to one identity: nobody else in a tenant can see or use it.
3. **What enterprises expect of a reporting platform**: roles (who may design, publish, issue, administer) and publication approval — the
   access token carries no role by design, so today every bound identity may do everything; an audit of what people did (designs changed,
   versions published, files downloaded), not only of jobs and issue records; browser single sign-on; scheduled reports and their delivery;
   a production OpenAPI description and callbacks; tracing; a console in more than one language.

## Decision

The work goes in this order; a later slice may start while an earlier one is in review when it touches other files.

| Slice | What | Depends on |
|---|---|---|
| S11-A | `privatedocs.Backend`: the private stores over a backend (the directory, memory, the platform's database, migration 011) | — |
| S11-B | The server keeps the designs in the database (`design_store`), several servers share them; the demo's `-memory` keeps them in memory | S11-A |
| S11-C | An import of the private directories into the database, for an existing deployment | S11-B |
| S12 | Roles and approval: roles from the bindings file first (ADR 0016's ownership), a tenant-shared design space, publication by a publisher, an audit log of people's actions | S11 |
| S13-A | A production OpenAPI 3.1 description held to the routes by a test | — |
| S14-A | W3C Trace Context on the API and in the logs, forwarded to adapters | — |
| S15 | Browser single sign-on (OIDC, authorization code with PKCE) once the identity provider's contract is known; until then a generic implementation tested against a fake provider | the IdP contract |
| S16 | Scheduled reports and their delivery | S12 |
| S17-A | An English console: a catalog keyed by the Chinese source text, a 中文 / English switch (docs/S17A_SCOPE.md) | — |
| S19-A | The reporting source protocol and the generic adapter `http`: a product implements the protocol and passes `reporting-source-check` instead of the platform writing an adapter for it (ADR 0022) | — |
| S19-B | The company directory (ADR 0015) on the server: `reporting.CompanyDirectory`, the simulators, `GET /companies`, the protocol's optional route `companies` (revision 1.1); the console picker is S09-D | S19-A |
| — | Each product's implementation of the protocol, reconciliation, admission (items 17–19) | the source products |

What does not change: the source's authority over every read; no write endpoint for connections or bindings (ADR 0016); the core stays
standard-library-only; every slice keeps the five required checks green.

## Consequences

The designs' storage moves before the roles because roles change who owns a design (an identity today, a tenant's shared space with roles
after S12), and that change is cheaper on one database table than on files locked by one process. S13-A and S14-A run beside S11 because
they touch other files.
