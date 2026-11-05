#!/usr/bin/env bash
# Runs the real-browser evidence of the console in one go (CI job "browser", docs/CONSOLE.md "真实浏览器检查"):
#   1. scripts/browser_console_s06c.mjs  – the shell, the pages, the drawers and panels at 1440 → 390, light and dark, contrast
#   2. scripts/browser_library_s06b.mjs  – the template centre, its samples, PDF previews and wizard
#   3. scripts/ux_baseline_s07.mjs       – the seven-task baseline, held to the numbers of docs/UX_BASELINE_S07.md
# Each run gets a fresh reporting-demo (memory store, its own directories) and a fresh headless Chrome profile: the scripts assume an
# identity with nothing saved yet. The prototype is not in the repository, so its screens and the side-by-side pairs are skipped.
#
# Usage: scripts/ci_browser_evidence.sh OUT_DIR
# Environment: REPORTING_TYPST, REPORTING_TYPST_SHA256, REPORTING_PDF_FONTS (scripts/fetch_pdf_tools.py), GO (default go),
#   CHROME (default google-chrome), CDP_PORT (default 9347). With CHROME_RUNNING=1 a Chrome already listening on CDP_PORT is used
#   (a local run against Windows Chrome from WSL) and none is started or stopped. The held numbers have no override: changing
#   them means changing this file and docs/UX_BASELINE_S07.md in the same pull request.
set -euo pipefail
out=${1:?usage: scripts/ci_browser_evidence.sh OUT_DIR}
root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"; out=$(cd "$out" && pwd)
: "${REPORTING_TYPST:?}" "${REPORTING_TYPST_SHA256:?}" "${REPORTING_PDF_FONTS:?}"
GO=${GO:-go}; CHROME=${CHROME:-google-chrome}; CDP_PORT=${CDP_PORT:-9347}; PORT=${PORT:-7860}
work=$(mktemp -d); server=; chrome=
cleanup() { [ -n "$server" ] && kill "$server" 2>/dev/null || true; [ -n "$chrome" ] && kill "$chrome" 2>/dev/null || true; rm -rf "$work"; }
trap cleanup EXIT

(cd "$root" && "$GO" build -o "$work/reporting-demo" ./cmd/reporting-demo)
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$work/token"; chmod 600 "$work/token"

start() { # a fresh server and, unless one is running, a fresh browser
  local data="$work/data-$1"; mkdir -p "$data"/{layouts,dashboards,templates,backgrounds}; chmod 700 "$data"/*
  REPORTING_DEMO_TOKEN=$(cat "$work/token") "$work/reporting-demo" -profile contract-simulator -memory -listen "127.0.0.1:$PORT" \
    -layout-directory "$data/layouts" -dashboard-directory "$data/dashboards" -template-directory "$data/templates" -background-directory "$data/backgrounds" \
    -pdf-typst "$REPORTING_TYPST" -pdf-typst-sha256 "$REPORTING_TYPST_SHA256" -pdf-fonts "$REPORTING_PDF_FONTS" > "$out/server-$1.log" 2>&1 &
  server=$!
  for _ in $(seq 1 100); do curl -fsS -o /dev/null "http://127.0.0.1:$PORT/console/" && break; sleep 0.2; done
  curl -fsS -o /dev/null "http://127.0.0.1:$PORT/console/"
  if [ "${CHROME_RUNNING:-0}" != 1 ]; then
    "$CHROME" --headless=new --remote-debugging-port="$CDP_PORT" --user-data-dir="$work/chrome-$1" --no-first-run --no-default-browser-check \
      --no-proxy-server --disable-gpu --window-size=1440,900 about:blank > "$out/chrome-$1.log" 2>&1 &
    chrome=$!
    for _ in $(seq 1 100); do curl -fsS -o /dev/null "http://127.0.0.1:$CDP_PORT/json/version" && break; sleep 0.2; done
  fi
  curl -fsS -o /dev/null "http://127.0.0.1:$CDP_PORT/json/version"
}
stop() {
  kill "$server" 2>/dev/null || true; wait "$server" 2>/dev/null || true; server=
  if [ -n "$chrome" ]; then kill "$chrome" 2>/dev/null || true; wait "$chrome" 2>/dev/null || true; chrome=; fi
}

failed=0
run() { # name script args...
  local name=$1; shift
  start "$name"
  if (cd "$root" && node "$@" --token-file "$work/token" --base "http://127.0.0.1:$PORT" --cdp "$CDP_PORT"); then echo "== $name: pass"; else echo "== $name: FAIL"; failed=1; fi
  stop
}
run console scripts/browser_console_s06c.mjs --out "$out/console"
run library scripts/browser_library_s06b.mjs --out "$out/library"
run baseline scripts/ux_baseline_s07.mjs --out "$out/baseline.json" --label ci

# The baseline is held to docs/UX_BASELINE_S07.md: a slice may lower these numbers, never raise them without saying so there.
node -e '
const t=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).totals;
const max={clicks:31,pages:9,shells:0,deadEnds:0};
const over=Object.keys(max).filter(k=>t[k]>max[k]);
console.log("== baseline totals",JSON.stringify(t),"held to",JSON.stringify(max));
if(over.length){console.error("== baseline: over the documented numbers: "+over.join(", "));process.exit(1);}
' "$out/baseline.json" || failed=1
exit $failed
