# S01-A · independent contract-simulator backend

Date: 2026-10-26 Asia/Shanghai. Origin: docs/s00/{ARCHITECTURE,INTERFACES,MIGRATION}.md.

Deliver a standalone Go module with no Brant source, replace directive, private
product import or business table dependency. A financial aging simulator and a
shipment-count simulator must use one catalog-driven engine without domain
fields in the core. Simulators are not real product integrations.

The executable slice includes authenticated catalog, bounded query preview,
asynchronous snapshot admission, stable source paging, immutable snapshot paging,
asynchronous XLSX export, private download, cancellation and live authorization
on every disclosure and before publication. JSON authority declarations and
unsupported query constructs are rejected. Precise values remain strings.

Platform ownership binds tenant, integration, trusted issuer and subject.
Source scope is frozen at admission. Grant expansion cannot widen it; revocation,
timezone/currency changes, expiry and authority outages prevent disclosure.
Idempotency binds owner, operation, key and canonical request. Publication and
task completion are atomic; expired or cancelled attempts cannot publish.

S01-A public test seams (already specified in the S00 contract): the reporting
Service, the four source Adapter operations, the Repository lease/publication
contract, the HTTP API and exported XLSX contents. Test behavior at these seams.

An in-memory repository is a local model only. A protected PostgreSQL fixture
implements database-time leases, generation fencing, cancellation and atomic
publication of platform-owned assets. Tests without a live PG DSN must report
the missing gate as SKIP, never PASS. A real-PG ordinary/race run is recorded
separately when available. Production migrations, RLS/least-privilege roles,
crash recovery, identity provider, real product adapters, UI/PDF and production
storage/capacity remain later gates. No retry/heartbeat/cross-product joins.

Final source authorization is checked immediately before publication and again
before every disclosure. A remote source's revocation and a platform DB commit
are not a distributed transaction. This slice does not promise atomic revocation
across independent systems; stronger source guarantees belong to S02.
