# S03-B3 private layout publication

2026-10-27 Asia/Shanghai. Fixed baseline 5c5556d14b72d23f9a57ca8063be55d0fc0a33fa.
Continue private metadata-only layout governance before real integration.

A layout starts unpublished. The owner explicitly publishes one immutable saved
version; new draft saves must not change the published pointer. Opening published
loads only that version's layout into an independently authorized current query.
Support explicit replacement/rollback publication and withdrawal; withdrawn layouts
have no published read (404). Keep ordered release history (revision/action/version/
server UTC timestamp), max64 events/layout, protected by expected release revision.
All release/history/published and mutation routes are owner scoped and recheck the
current published source catalog. No cross-user sharing, author permissions, data,
company selection or token in published layout. This is private publication only,
not approval roles, enterprise audit evidence, scheduled release or public links.
UI must explicitly read release state before publishing/withdrawing; refuse dirty
unsaved definitions and preserve unrelated query results on errors. Obey current
view generation, clear publication state on logout and when switching saved layout.
Uncertain mutations require re-reading state; no automatic retry.

Migrate old [][]Entry owner files on the next write into schema2 records containing
versions plus separate release histories. Preserve all IDs, bindings and immutable
layout versions. Reads/open do not rewrite old files. Reject malformed roots,
null histories, invalid revisions/actions or impossible version references. Retain
single-writer directory lock, 0700/0600, atomic rename, quotas and normal reopen.
Old binaries cannot read schema2: downgrade/rollback and crash/power-loss recovery
are not qualified. Trusted local directory only; no tamper-evident audit claim.
No production/browser/PG rerun claim. Real product/enterprise identity remains last.
Verification: public Store, actual HTTP and public DOM using loopback Go API.

Column-change policy: publish validates the exact target version's columns, and
published read validates that pinned version. Release history/withdrawal require
current dataset binding access (not obsolete v1 columns), so the owner can inspect
and withdraw publication after a field is removed. These metadata-only routes
return no layout fields or data; integration/dataset revocation still denies them.
