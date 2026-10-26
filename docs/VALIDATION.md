# S01-B verification record

2026-10-26 Asia/Shanghai. Protected contract-simulator fixtures only.

Final reviewed source: 17 original platform cases + 19 durable-source/storage cases.
Ordinary and actual race binaries each passed36 child cases +2 parents, zero DB
skips/failures. Host ordinary/race each passed18 top-level/35 parent+child events,
with two explicit missing-DSN DB skips per host run. All5 host command groups and
static checks passed. Actual4-process/PG recovery,14 independent workbook readbacks
and3 missing-DSN negative controls passed. Current structured evidence is
evidence/verification_summary.json.

The recovery probe verifies changed server process PID/start ticks and PostgreSQL
start time, old artifact SHA/bytes, old cursor/source binding, queued snapshot and
export completion, an intentionally abandoned claim's next generation, old-claim
fencing, cancelled state/idempotency, actual DB outage non-disclosure and source
revocation after another initializing startup. Platform and source are separate
DBs on one guest PostgreSQL instance; independent-host failure domains are not
qualified. The abandoned claim is made by the fixture probe, not a power-loss or
worker-crash qualification.

For direct PG tests use both guarded local DSNs:
REPORTING_S01_TEST_DSN=postgresql://postgres@127.0.0.1:55432/reporting_test_s01?sslmode=disable
REPORTING_S01_SOURCE_DSN=postgresql://postgres@127.0.0.1:55432/reporting_source_test_s01?sslmode=disable
Set REPORTING_S01_REQUIRE_PG=1. Run go test ./integration -count=1 and the same
with -race. Each destructive setup is restricted to the named fixture databases.
Missing required DSNs fail; host runs without DSNs explicitly skip both DB entries.

Full guest runner: python3 scripts/run_pg16_vm.py. Provide existing paths GO_BIN,
REPORTING_PG_BUNDLE (bin/lib/share PostgreSQL), QEMU_BIN, REPORTING_VM_KERNEL,
REPORTING_VM_BUSYBOX (static), REPORTING_QEMU_DATA and REPORTING_VM_BIOS. Optional
private QEMU dependencies: REPORTING_QEMU_LOADER, REPORTING_QEMU_LIBRARY_PATH,
REPORTING_QEMU_MODULE_DIR. PG must resolve its own shared libraries. No tools are
downloaded by the runner. Go1.26.5/PG16 are required; no source-product repo needed.

The fixture upgrade adds canonical JSONB payload checksums. Those detect damaged
or mismatched records; a privileged DB writer able to change both payload/hash is
outside this integrity guarantee. SQL/payload owner mismatch is separately denied.
Production roles/RLS and migration/rollback qualification remain S04. Fixture v1
layout tests preserve durable-source assets; they do not recreate old S01-A
in-memory handles that were never stored.

No Docker/hosted CI, real SSO/product adapters, UI/PDF/designers, capacity, object
storage, backup, crash/power loss or cross-system atomic revocation is claimed.
S01-A evidence and review are retained under evidence/s01a_baseline/ and
corresponding *_S01A.md documents.
