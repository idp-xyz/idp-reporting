# S01-B · durable simulator and recovery qualification

2026-10-26 Asia/Shanghai. Accepted continuation of S00 and S01-A. Review fixed
point: S01-A delivery ec09065dc024419dc2e2e6e9cc2d1dcae6ee7132.

Keep the independent generic core and four source operations. Add a persistent
contract simulator in a separate protected source database. Persist immutable
source versions, prepared handles, original scopes and current grants. Preparing
a read must reference a version without selecting source fact rows. New source
versions never change an already prepared read. Startup seeds only absent
fixtures/grants; a persisted revocation must remain revoked, including when
initialization is requested again.

Platform metadata stores platform assets only; source records remain in the
source-side fixture database. Both require exact test database names, numeric
loopback hosts and profile markers. Add an explicit fixture v1→v2 metadata
integrity upgrade with canonical persisted-payload SHA-256 and SQL/payload
ownership consistency. This is a fixture upgrade, not a production migration.

Validate through the already accepted Service, four Adapter operations,
Repository, HTTP and XLSX seams. Fixture setup may inject a transaction failure
or corrupt stored bytes; assertions must use the public interfaces. Cover owner
axes, idempotency namespaces, immutable source pages, current authorization,
concurrent cancellation/publication, duplicate publication, expired-generation
publication and rollback after asset insertion. Never expose fixture controls or
worker ports as HTTP admin endpoints.

Run ordinary and actual race tests against real PostgreSQL, with all required
DB gates failing if DSNs are absent. Then actually stop/start the HTTP server
processes and PostgreSQL, verifying changed process IDs/server start times,
queued snapshot/export completion, abandoned-lease recovery, old cursor/bytes
and SHA continuity, revoked grants and cancellation continuity. Source and
platform reconnect must not reset scopes or silently reauthorize. While the DB
is stopped, result requests must fail closed and disclose no file bytes.

Preserve S01-A code/history and raw evidence as historical, independently verify
new artifacts, and deliver a complete reproducible source/history package.
Do not reuse old Brant restart evidence as proof for this project.

Qualification is bounded contract simulators and protected test fixtures.
Source and platform share one test PostgreSQL server but separate databases;
this is not an independent-host failure-domain or production least-privilege
claim. No power-loss/crash, backup restore, production RLS, SSO, real Brant/Parcel,
UI/PDF/designers, automatic retry/heartbeat or capacity qualification. Real
cross-system revocation/commit atomicity remains a source-contract concern.
