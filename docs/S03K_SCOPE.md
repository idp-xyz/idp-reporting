# S03-K issue by group

2026-10-29. Implements ADR 0004 decision 7: a schema 2.0 template is issued one document per row group, under
the source product's authority exactly as a 1.x row is (ADR 0003). 1.x issuing does not change.

K1 · Request. `POST /issues` with a 2.0 template takes `{template_id, version, snapshot_id, group}` where `group`
is the list of the group's key values in the order of `group.fields` (an empty list for a template without
`group`: the whole snapshot), or, as a convenience for the console, `row_index` (the group containing that
row); both or neither is 400. The admission resolves the group in the frozen snapshot (an unknown key is 404
NOT_FOUND, a template whose group the snapshot cannot form is refused as the preview refuses it) and freezes
the group: its key values, its row indices and the digest of its rows. The request hash covers the resolved
group, so `row_index` and `group` naming the same group are one request. A 1.x template keeps `row_index` only
(`group` is 400 there).

K2 · Contract version 2. `IssueIntent` gains `Group *IssueGroup` (`Fields`, `Rows` — the snapshot row indices,
ascending — and `RowsSHA256`, the digest of those rows: the row digest encoding of API.md applied to the JSON
array of the rows' arrays). A group intent has no `RowIndex`/`RowSHA256`. An adapter declares version 2 by
implementing `IssueContractVersion() int` returning 2; the platform never sends a group intent to an adapter
that does not, and answers 422 at admission instead. Both contract simulators implement version 2: they check
every row of the group against their own source snapshot and refuse a mismatch as they refuse a row today.
Idempotency, reservation, confirmation, void, tombstones and ISSUE_UNRECONCILED are unchanged.

K3 · Worker and rendering. The issuer re-checks the published version, binds the frozen group (`BindGroup`),
runs the 2.0 layout check (no Typst) before `PrepareDocument`, renders the group's document with the stamp on
every page (`Writer.Issue` for 2.0), confirms and publishes. The output is deterministic as for 1.x.

K4 · Record and views. The issue record keeps the group (fields, key values, row indices, rows digest) in place
of the row for a group issue; `ValidatePublication` and the view cross-checks cover it; `GET /issues`, the job
views and the console's issue list show the group (key values) instead of a row number. No SQL migration is
needed if the JSON payloads carry it; if one is, it is 005 with a reversible down that refuses to drop group
records without `--accept-data-loss`.

K5 · Console. The issue button works for a published 2.0 template opened unchanged: it issues the group of the
preview row number (`row_index`), shows the group's key values in the confirmation, the task and the record.

K6 · Tests and evidence. Service, HTTP, durable (PostgreSQL) and both simulators' conformance tests for group
intents (a version-1 adapter refused at admission; a mismatched row refused by the product and voided; replay
of `row_index` and `group` as one request; determinism); Typst tests issuing 2.0 groups (single and multi-page,
the stamp on every page) checked by the checker (`document2` with the issue block) and veraPDF; DOM tests for
the console flow; 1.x issue output byte-identical.

Not in this slice: the canvas for bands (S03-L), batch issue of every group of a snapshot, barcodes and images.
