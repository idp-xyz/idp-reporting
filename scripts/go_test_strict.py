#!/usr/bin/env python3
"""Run `go test -json ./...` with the given flags and fail unless the skipped top-level tests are
exactly EXPECTED_SKIPS (space-separated names, empty for none), so that a new t.Skip or a
missing database cannot pass unnoticed.

Usage: EXPECTED_SKIPS="..." python3 -I scripts/go_test_strict.py [go test flags]
"""
import json
import os
import subprocess
import sys

proc = subprocess.run(["go", "test", "-json", *sys.argv[1:], "./..."], capture_output=True, text=True)
events = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")]


def top_level(event):
    return bool(event.get("Test")) and "/" not in event["Test"]


if proc.returncode != 0:
    failed = {(e["Package"], e["Test"]) for e in events if e["Action"] == "fail" and e.get("Test")}
    print("failed tests:", *(f"{p} {t}" for p, t in sorted(failed)) or ["(none reported)"], sep="\n  ")
    for e in events:
        if (e.get("Package"), e.get("Test")) in failed or e["Action"] == "build-output":
            sys.stdout.write(e.get("Output", ""))
    sys.stdout.write(proc.stderr)
    sys.exit(1)

skipped = sorted({e["Test"] for e in events if e["Action"] == "skip" and top_level(e)})
expected = sorted(set(os.environ.get("EXPECTED_SKIPS", "").split()))
passed = sum(1 for e in events if e["Action"] == "pass" and top_level(e))
packages = len({e["Package"] for e in events if e["Action"] == "pass" and not e.get("Test")})
print(f"passed: {passed} top-level tests in {packages} packages; skipped: {' '.join(skipped) or 'none'}")
if skipped != expected:
    print(f"skipped tests differ from EXPECTED_SKIPS ({' '.join(expected) or 'none'})")
    sys.exit(1)
