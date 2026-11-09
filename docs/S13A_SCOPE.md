# S13-A an OpenAPI description of the API, held to the bound routes

2026-11-09. External systems integrate against a file, not a prose table. `docs/openapi.json` is that file; a Go test fails when it and
the handlers disagree on which routes exist. No route, handler or answer changes.

A1 · The description. `docs/openapi.json` is OpenAPI 3.1 in JSON (the core is standard-library only, so no YAML). It has 38 paths and 48
operations: the 46 that the API binds under `/api/reporting/v1` with every mount composed (the core API, its layout store, WithDashboards,
WithDocumentAssets with its image store, WithDocumentPreviews with a renderer that can check, WithDocumentTemplates with its sample pack
and renderer; WithEmbedding and WithProfile answer `GET /capabilities` again) and the production server's `GET /healthz` and
`GET /readyz`, which are on the same listener. Each operation has its parameters with their bounds (`limit` 1..100, defaults 50 and 25,
the `kind` and `state` of the job listing, `version` of a saved document, `Idempotency-Key`), its body with its route's bound (64 KiB,
4 KiB, 1 MiB), its statuses and the schema of its answer. The schemas are read from the Go structs and their json tags: the job view,
the issue view, the catalog entry, the query spec, the query result, the rows page, the session, the capabilities, the layouts, dashboards
and templates with their summaries, versions, release state and catalogue metadata, the samples, backgrounds, images, the check result,
and the error body `{"error": CODE, "detail"?, "quota"?}` with the codes of `classify` and the in-flight bound. `x-reporting-mount` names
the handler that binds each operation; a deployment serves only the mounts it composes. The security scheme is HTTP bearer, a JWT access
token (docs/ACCESS_JWT.md); the demo's shared bearer secret (`DemoBearer`) is marked demo-only, as are the health routes not served by
the demo. Layouts and dashboards, which docs/API.md had no table for, are described here.

A2 · What the test holds. `TestOpenAPIMatchesTheBoundRoutes` (transport/httpapi/openapi_test.go) reads the package's non-test source with
`go/parser` and takes every pattern given to `HandleFunc`, `Handle` or a closure that passes its first parameter on to them (`bind`),
resolving constants and the parameters of `mountPrivateDocuments` and `mountSamples` through their calls; a pattern it cannot evaluate
fails the test. Each candidate and each documented operation is then requested of the API composed with every mount, with an
authenticator that records `req.Pattern` and refuses; a pattern is bound when a request for it reaches exactly it. This drops what the
source over-approximates (the catalogue routes, which only the templates mount: 48 candidates, 46 bound). The test fails when a bound
route is not documented, when a documented operation is not bound, when a `$ref` does not resolve, and when an operation's path
parameters are not its path's wildcards. `TestOpenAPIMatchesTheHealthRoutes` (server/openapi_test.go) does the same for the paths
outside `/api/reporting/` against `healthHandlers`. Both run in well under a second and read nothing but the source and the file.

Not covered. The test holds the set of routes, their methods and path parameters, not the schemas: a field added to a struct without the
description is not caught, and neither is a status the description lists that a route cannot answer. The nested shapes of a document
template (its elements, table, page, group and bands) are left open and pointed to ADR 0004, ADR 0005 and docs/STUDIO.md. `GET /metrics`
is on its own loopback listener and is not described; nor are the console's pages and embed pages. docs/API.md stays the prose reference
for behaviour.
