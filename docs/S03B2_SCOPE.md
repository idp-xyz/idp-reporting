# S03-B2 private local server layout versions

2026-10-27. Baseline 77297f7f49cb4b38e33e2a5944ae6d2ca5dd46e6.
Implement opt-in metadata-only server layout storage and UI save/list/open versions.
Ownership is tenant + issuer + subject, never client claims. Each layout is bound
to integration/dataset version; validate every save/get/list against current
published catalog and columns. No rows, company scope, token or identity in layouts.
Strict request bodies, bounded layouts (64KiB), 16 layouts/owner, 20 versions/layout,
8MiB owner file. Expected latest version required for updates; immutable history.
Private 0700 directory/0600 files, single-process exclusive directory lock,
atomic rename and fsync; normal close/reopen retains data. Fail closed on damaged
files. The directory is trusted local storage, not a production database or audit
system. Crash/power-loss recovery, filesystem tamper resistance, distributed
locking, quota management/deletion and automatic stale-lock recovery are not qualified.
A crash can leave .lock; an administrator must verify no writer exists before
removing that lock. Do not expose filesystem controls in HTTP.
Default layout API disabled unless -layout-directory is explicitly supplied.
Current demo -memory still loses query/job data; opt-in layout files persist separately.
UI save creates a new layout or immutable version, lists private authorized layouts,
opens an explicit historical version into current authorized result, rejects mismatched
binding. Old version save conflicts instead of overwrite. Async actions obey existing
view-generation rules; errors must not clear unrelated query results.
Real identity/products last. Browser qualification still open. No complete template
publication, Dashboard Composer or PDF designer claim. Verify public Store/HTTP/DOM.
