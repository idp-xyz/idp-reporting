# S11-B the server keeps the designs in the store; several servers serve the same designs

2026-11-09. ADR 0017, after S11-A.

B1 · `design_store` in the configuration: `directories` (the default, as before) or `database`. With `database` the four `*_directory` fields
must be absent (a configuration problem otherwise) and the server opens the layouts, dashboards, templates, backgrounds and images over the
store's backends (`Store.Designs`), so the four design features are on with nothing else to configure. `check-config` says where the designs
are; the start line carries `design_store`.

B2 · The design packages open over a backend: `layouts.OpenBackend`, `dashboards.OpenBackend`, `doctemplates.OpenBackend`,
`doctemplates.OpenAssetsBackend` (backgrounds and images). The server's `Store` interface has `Designs`; the test memory store keeps one memory
backend per private store, shared by every server started on it.

Tests: `TestTwoServersShareTheDesignsOfTheStore` (two servers on one memory store: a layout saved through one is read, versioned and published
through the other, and a stale save through the first is a conflict) and its PostgreSQL twin in `TestServerAgainstPostgres`
(`two_servers_share_the_designs_of_the_database`, through the production store as the application role, under row-level security); two
configuration refusals (an unknown `design_store`, `database` with a directory).

Docs: DEPLOYMENT (the field, "Several servers", the upgrade's `roles` after 011). Not here: importing a deployment's directories (S11-C).
