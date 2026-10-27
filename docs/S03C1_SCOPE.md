# S03-C1 Dashboard Composer first slice

2026-10-27. Baseline118abcf00d745c7c627343cfbb17ed0a358f398d.
Build a distinct dashboard pane consuming only the currently authorized result.
Allow adding server-total metric cards and current-page tables with selected
columns, moving components up/down and removing them. Maximum12 widgets; one
integration/dataset version only. Numeric values remain exact strings; no browser
aggregation, formulas, SQL or float conversion. Explicitly distinguish server
whole-scope totals from bounded preview/current page tables. No charts in this slice.

Save and reopen metadata-only JSON (schema_version/kind/binding/widgets),64KiB
UTF-8 max. Strict fields, same integration/dataset binding independent of JSON key
order, known fields, authorized totals, supported widget types and unique table
columns. Empty board is valid. No values, companies, date filters, identities,
tokens or permissions in config. File is local; do not reuse report-column server
layout/version/publication routes for dashboard configs.

Query changes, product changes, denial/outage and logout clear visible dashboard
data/selectors/file-editor contents; do not allow building with no valid result or
while an operation is pending. Reauthorize by a new query before rendering values
again. Metadata can survive same-binding query changes within a session, but logout
resets it; incompatible results discard invalid metadata. Imports/actions are atomic
and do not issue queries or change access. Public DOM+actual Go HTTP tests, static
asset whitelist/embedding and syntax checks. New module rather than expanding core.

Not complete dashboard product: server dashboard persistence/release, charts,
multiple datasets, cross-product joins, scheduling, sharing, PDF and production
remain future. Existing private report layout publication unaffected. No real
browser/mobile/visual/download qualification; previous environment block remains.
Real products/enterprise identity still last. Save complete source/history/evidence.
