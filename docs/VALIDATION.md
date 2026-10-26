# S03-A verification and remaining browser gate

Pinned Go 1.26.5 and Node 24.22.1. Production UI is buildless HTML/CSS/JavaScript
embedded in the Go binary. jsdom 30.1.3 is a locked development-only test tool.

    npm ci --ignore-scripts --no-audit --no-fund
    python3 scripts/run_s03a_checks.py --go /path/to/go1.26.5/bin/go
    python3 scripts/verify_s03a_workbooks.py

Current evidence lives in evidence/s03a. Ordinary/race host suites include the
static asset and authenticated session tests plus existing API/service/storage
contract tests. Two PostgreSQL entry tests are explicitly SKIP: no PG/VM run is
claimed. Core/adapters/storage/modules remain byte-identical to the accepted
S02-A baseline. S01-B evidence remains in evidence/s01b_baseline and S02-A in
evidence/s02a_baseline; those historical runs are not current qualification.

The software-DOM suite executes the actual app.js and index.html, drives public
form/button events, and calls an actual Go demo over 127.0.0.1 HTTP. It covers
precision, snapshots/pages/export, company denial, invalid login, ambiguous
submission/idempotency, stale response suppression, authorization outage, literal
XSS text, 401 cleanup, logout during flight, checksum failure, serialized frozen
paging, page-limit consistency, repeated catalog refresh, and stale product errors.
The 4 async regressions fail against the pre-fix application at 00b37668; the
negative-control log is kept separately in evidence/s03a_diagnostics and is not
a qualification run. The initial 9-test checkpoint is historical in
evidence/s03a_pre_review. Current evidence/s03a contains the fixed 13-test suite. Network fault
injections are test-only fetch-boundary controls; no private application state is
read. OS file download is a test port: captured bytes are written as one actual
XLSX and independently read with Python ZIP/XML. It does not qualify an actual
browser or its download manager. Four current authenticated HTTP workbooks are
verified separately; the five files are not five browser downloads.

## Browser limitation observed

The managed Sites preview could start the prebuilt Go demo at the exact supplied
URL, http://terminal.local:4173/. The cloud browser returned ERR_CONNECTION_REFUSED.
After status verification, its error-page reload was rejected by the URL policy
(frame protocol). We stopped preview and browser interaction. No alternate route,
network permission change, browser installation, security exception or bypass
was used. No browser success, screenshot, mobile layout or pixel QA is claimed.
Software-DOM tests are additional code validation, not a workaround to control
that browser. Browser/visual/accessibility verification remains open.

The first preview attempt lacked the Go toolchain in the separate preview
container. The standard local preview runner now supports a prebuilt binary and
the CLI's port flags. Runtime Go/app network safeguards remain loopback-only.
Private local preview config, binaries and node_modules are ignored and must not
appear in release archives or commit history.

Current API profile still declares synthetic sources, production_approved=false.
Real source and enterprise identity integration are deferred by the user's
explicit instruction. Three designers, template release/PDF, embedding, complete
durable task-history listing and production operations remain future slices.
