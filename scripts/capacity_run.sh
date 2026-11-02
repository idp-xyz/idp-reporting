#!/usr/bin/env bash
# The capacity measurement of S04-G (docs/S04G_SCOPE.md G2, docs/CAPACITY.md): seeds the guarded fixture database through SQL at each size, measures the
# hot paths before migration 009 (its indexes reverted) and after, and removes everything it seeded. Local evidence of one run on one machine; it is
# not a CI job and not a production sizing.
#
#   REPORTING_S01_TEST_DSN   the fixture database (loopback, reporting_test_s01, never port 55432); required
#   GO                       the go command (default go)
#   REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY, _SECRET_KEY   optional: the throwaway S3-compatible server, for verify-objects
#   extra arguments go to the tool (-sizes, -ops, -sweep-rows, -verify-rows, -identities, -tenants, -hot-percent, -assets-percent, -phases ...)
#
#   scripts/capacity_run.sh 2>&1 | tee capacity.txt
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
: "${REPORTING_S01_TEST_DSN:?set REPORTING_S01_TEST_DSN to the guarded fixture database}"
case "$REPORTING_S01_TEST_DSN" in *:55432/*) echo "refusing port 55432"; exit 2 ;; esac
"${GO:-go}" run ./internal/capacity/cmd/capacity "$@"
