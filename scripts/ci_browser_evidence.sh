#!/usr/bin/env bash
# Runs the real-browser evidence of the console in one go (CI job "browser", docs/CONSOLE.md "真实浏览器检查"):
#   1. scripts/browser_console_s06c.mjs  – the shell, the pages, the drawers and panels at 1440 → 390, light and dark, contrast
#   2. scripts/browser_library_s06b.mjs  – the template centre, its samples, PDF previews and wizard
#   3. scripts/ux_baseline_s07.mjs       – the eight-task baseline (T8 出具 since S09-G), held to the numbers of docs/UX_BASELINE_S07.md
#   4. the same script with --tasks team – the three team-space tasks of two people (S12-D), held to their own numbers there
#   5. scripts/browser_english_s17a.mjs  – the English console (S17-A) at 1440 and 390: overflow, cut-off controls, Chinese left
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
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$work/colleague"; chmod 600 "$work/colleague"

start() { # a fresh server and, unless one is running, a fresh browser
  local data="$work/data-$1"; mkdir -p "$data"/{layouts,dashboards,templates,backgrounds}; chmod 700 "$data"/*
  REPORTING_DEMO_TOKEN=$(cat "$work/token") REPORTING_DEMO_COLLEAGUE_TOKEN=$(cat "$work/colleague") "$work/reporting-demo" -profile contract-simulator -memory -listen "127.0.0.1:$PORT" \
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
run baseline-team scripts/ux_baseline_s07.mjs --out "$out/baseline-team.json" --label ci --tasks team --colleague-token-file "$work/colleague"
# S17-A: the English console, last (it leaves the profile with no stored language choice, so a Chinese run after it would not see one).
run english scripts/browser_english_s17a.mjs --out "$out/english"

# The baseline is held to docs/UX_BASELINE_S07.md: a slice may lower these numbers, never raise them without saying so there.
hold() { # file clicks pages shells deadEnds
node -e '
const t=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).totals;
const [clicks,pages,shells,deadEnds]=process.argv.slice(2).map(Number);
const max={clicks,pages,shells,deadEnds};
const over=Object.keys(max).filter(k=>t[k]>max[k]);
console.log("== baseline totals",JSON.stringify(t),"held to",JSON.stringify(max));
if(over.length){console.error("== baseline: over the documented numbers: "+over.join(", "));process.exit(1);}
' "$@"
}
# S09-G added task T8 (出具) to the core tasks: its own 5 clicks and 1 page switch are in these totals (T1–T7 measure 30 / 9 on the same run;
# docs/UX_BASELINE_S07.md "T8：出具"). The dialogs and the texts are not held.
hold "$out/baseline.json" 35 10 0 0 || failed=1
# S12-D: the team tasks. Their one dead end is the approver's: the studio needs their own authorized data before it opens a template.
hold "$out/baseline-team.json" 25 8 0 1 || failed=1
exit $failed
