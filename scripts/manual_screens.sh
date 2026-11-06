#!/usr/bin/env bash
# Retakes the user manual's figures (docs/manual/img/*.png, docs/CONSOLE.md "用户手册") from the current console in one go:
# a fresh reporting-demo (memory store, its own private directories in a temp dir), a fresh headless Chrome profile, and
# scripts/manual_screens.mjs driving the console over the DevTools protocol. The script assumes an identity with nothing saved
# yet, like the CI scripts (scripts/ci_browser_evidence.sh), so what the figures show is the console's own work during the run.
#
# Usage: scripts/manual_screens.sh [OUT_DIR]          (default: docs/manual/img)
# Environment: GO (default go), CHROME (default google-chrome), CHROME_FLAGS (extra Chrome flags, e.g. --no-sandbox for a
#   Chromium that has no sandbox helper), CDP_PORT (default 9370), PORT (default 7880). REPORTING_TYPST, REPORTING_TYPST_SHA256
#   and REPORTING_PDF_FONTS (scripts/fetch_pdf_tools.py) give the demo its PDF renderer: the figures of the PDF preview, the
#   edge-sample check and the official issue need it; without them the demo runs without PDF and those figures show the console
#   without one. With CHROME_RUNNING=1 a Chrome already listening on CDP_PORT is used and none is started or stopped (start it in
#   the zh-CN locale, as below, or its date fields come out in the browser's own format).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$root/docs/manual/img}
mkdir -p "$out"; out=$(cd "$out" && pwd)
GO=${GO:-go}; CHROME=${CHROME:-google-chrome}; CDP_PORT=${CDP_PORT:-9370}; PORT=${PORT:-7880}
work=$(mktemp -d); server=; chrome=; status=1
cleanup() {
  # Both are waited for: Chrome keeps writing its profile for a moment after SIGTERM, and the temp directory goes only once it has left.
  if [ -n "$server" ]; then kill "$server" 2>/dev/null || true; wait "$server" 2>/dev/null || true; fi
  if [ -n "$chrome" ]; then kill "$chrome" 2>/dev/null || true; wait "$chrome" 2>/dev/null || true; fi
  if [ "$status" != 0 ] && [ -s "$work/server.log" ]; then echo "== reporting-demo log (tail)"; tail -n 20 "$work/server.log"; fi
  rm -rf "$work" 2>/dev/null || true
}
trap cleanup EXIT

(cd "$root" && "$GO" build -o "$work/reporting-demo" ./cmd/reporting-demo)
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$work/token"; chmod 600 "$work/token"

data="$work/data"; mkdir -p "$data"/{layouts,dashboards,templates,backgrounds}; chmod 700 "$data"/*
pdf=()
if [ -n "${REPORTING_TYPST:-}" ]; then
  pdf=(-pdf-typst "$REPORTING_TYPST" -pdf-typst-sha256 "${REPORTING_TYPST_SHA256:?REPORTING_TYPST_SHA256 goes with REPORTING_TYPST}" -pdf-fonts "${REPORTING_PDF_FONTS:?REPORTING_PDF_FONTS goes with REPORTING_TYPST}")
else
  echo "== note: REPORTING_TYPST is not set; the demo runs without a PDF renderer and the PDF figures show its refusals" >&2
fi
REPORTING_DEMO_TOKEN=$(cat "$work/token") "$work/reporting-demo" -profile contract-simulator -memory -listen "127.0.0.1:$PORT" \
  -layout-directory "$data/layouts" -dashboard-directory "$data/dashboards" -template-directory "$data/templates" -background-directory "$data/backgrounds" \
  ${pdf[@]+"${pdf[@]}"} > "$work/server.log" 2>&1 &
server=$!
for _ in $(seq 1 100); do curl -fsS -o /dev/null "http://127.0.0.1:$PORT/console/" && break; sleep 0.2; done
curl -fsS -o /dev/null "http://127.0.0.1:$PORT/console/"
if [ "${CHROME_RUNNING:-0}" != 1 ]; then
  # The console is Chinese: Chrome runs in the zh-CN locale (LANG decides how a date field is shown; --lang the rest of the browser's UI).
  # shellcheck disable=SC2086 # CHROME_FLAGS is a list of flags by design
  LANG=zh_CN.UTF-8 LC_ALL=zh_CN.UTF-8 "$CHROME" ${CHROME_FLAGS:-} --headless=new --remote-debugging-port="$CDP_PORT" --user-data-dir="$work/chrome" \
    --no-first-run --no-default-browser-check --no-proxy-server --disable-gpu --lang=zh-CN --window-size=1440,900 about:blank > "$work/chrome.log" 2>&1 &
  chrome=$!
  for _ in $(seq 1 100); do curl -fsS -o /dev/null "http://127.0.0.1:$CDP_PORT/json/version" && break; sleep 0.2; done
fi
curl -fsS -o /dev/null "http://127.0.0.1:$CDP_PORT/json/version"

(cd "$root" && node scripts/manual_screens.mjs --token-file "$work/token" --base "http://127.0.0.1:$PORT" --cdp "$CDP_PORT" --out "$out")
status=0
echo "== manual figures written to $out"
