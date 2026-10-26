# S01-A verification record

2026-10-26 Asia/Shanghai. Independent engineering slice; no production approval.

## Evidence already obtained

- Independent project-only copy with GOWORK=off built and started, then completed
  both HTTP chains (catalog/query/snapshot/pages/export/private download).
  `evidence/independence_checks.json`, exact output XLSX and run log.
- Adding the shipment simulator changed **zero core files** at that implementation
  step: commits 206ca85 → 1801a8a, `evidence/second_adapter_proof.json`.
  Later generic API/integrity hardening is reviewed separately; this proof is not
  a claim that core files never change in the remainder of the project.
- Real, unmodified PostgreSQL 16.14 in an isolated Linux guest, Go 1.26.5:
  17 SQL-backed child scenarios + parent passed in ordinary and actual race
  binaries, zero DB failures/skips. `evidence/vm_checks.json`, both JSONL files,
  original serial output, command/tool hashes, and four transferred XLSX files.
- Public Service/Repository/source Adapter/HTTP tests cover exact values,
  final-read revocation, stable multiple source pages, malformed bindings, budgets,
  cancellation, generation fencing, strict JSON and formula-safe XLSX.

Host full-suite commands, independent workbook readback and final review are
recorded in the final `evidence/verification_summary.json` and docs/CODE_REVIEW.md.
Unexecuted gates remain NOT_RUN/PARTIAL in docs/ACCEPTANCE_S01A.json.

## Running real PostgreSQL tests

Use an isolated local DB named `reporting_test_s01` or `reporting_test_s01_race`.
The fixture role currently has write/schema privileges; least privilege/RLS is
not qualified. The test resets platform fixture tables; never use a business DB.

```bash
export REPORTING_S01_TEST_DSN='postgresql://postgres@127.0.0.1:55432/reporting_test_s01?sslmode=disable'
export REPORTING_S01_REQUIRE_PG=1
go test ./integration -run '^TestPostgresContract$' -count=1
go test -race ./integration -run '^TestPostgresContract$' -count=1
```

For the full guest runner provide existing tool paths: GO_BIN, REPORTING_PG_BUNDLE
(bin/lib/share PostgreSQL bundle), QEMU_BIN, REPORTING_VM_KERNEL (Linux),
REPORTING_VM_BUSYBOX (static), REPORTING_QEMU_DATA and REPORTING_VM_BIOS.
Optional private QEMU dependencies use REPORTING_QEMU_LOADER,
REPORTING_QEMU_LIBRARY_PATH and REPORTING_QEMU_MODULE_DIR. The host PostgreSQL
bundle must resolve its own shared libraries; do not globally substitute QEMU's
private libc for PostgreSQL. Run `python3 scripts/run_pg16_vm.py`.

The guest has no NIC; PostgreSQL runs as UID 65534 under the normal upstream
root restriction. Ordinary/race use separate databases. No Docker or hosted CI
was run. Shared public Go dependency caches are allowed; no Brant source tree
or product private module is present in the independent build.

## Limits and next gates

Persistent platform assets are implemented, but simulator handles/grants are
process-local. Real source and platform restart, crash/power loss, cleanup,
backup/restore, production roles, TLS/SSO, real product reconciliation, UI/PDF,
formal document confirmation, migration upgrades and capacity are NOT qualified.
Final authorization and platform commit are separate source/system events.

The first guest execution's raw ordinary/race tests passed; its summary verifier
mistakenly treated the dynamic `completed_` test prefix as a literal case.
The verifier was corrected and the entire guest run repeated successfully.
The earlier diagnostic is retained under evidence/first_verifier_failure and is
not the current validation result.
