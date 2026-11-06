# S09-B the chrome gives the content the screen: header, topbar, ribbon and context bar heights, held by the browser script

2026-11-06. ADR 0014 decisions 2 and 10, on top of S09-A. Stylesheets and the real-browser script only; no markup, no request, no id
changes. ADR 0010's one shell stays exactly as it is: nothing is merged, moved or removed, every bar only gets shorter.

B1 · Heights. The console header 68 → 56px (`.console-header`, `--shell-head`, `.console-layout`); the document studio's and the
dashboard studio's topbar 64 → 52px (`#page-documents .studio` and `#page-dashboard .bstudio` grid rows, the dashboard inspector's
offset at ≤ 950px, the embed page's grid); the ribbon 83 → 74px (tabs 31 → 30, tools 51 → 44; the studio's narrow-width drawers
start at 126px instead of 147); both context bars 38 → 34px. The template centre's header was 56px already. The narrow layouts
(≤ 700px, ≤ 660px) keep their own rows.

Measured at 1440 × 900 in headless Chromium, before (main 52ba843) → after: the document studio's canvas area begins at 274 → 216px
(30% → 24% of the viewport; at 1366 × 768 36% → 28%), the dashboard's desk at 170 → 142px, the workbench's query panel at 196 →
187px. Nothing overflows at 1440, 1366, 1280, 768 or 390px; contrast is unchanged.

B2 · Held numbers. `scripts/browser_console_s06c.mjs` gains `legibility(name, shell)`: the smallest `font-size` of visible text
nodes (the canvas drawing excluded) must be ≥ 11px, except inside eyebrows, badges, the rail's and tool rails' labels and keyboard
hints, where it must be ≥ 10px (the rail's 帮助 label was 8.3px: a `<small>` inside 10px text; `small` now has a 12px base size and
the rail's labels 11px); and the content of each shell must begin at or above 225px (the workbench's `.query-panel`, the
studio's canvas, that is its ruler below the context bar, the dashboard's desk or start card). It runs on the workbench, the dashboard start and the document
studio at 1440 × 900 in the light scheme; a failure fails CI's browser job like the task baseline does. The geometry check reads
56px for the header. `docs/UX_BASELINE_S07.md` gains the table of these two numbers; raising them means changing that file in the
same pull request.

Tests and evidence. The DOM suite is unaffected (no markup changed); `scripts/ci_browser_evidence.sh` run locally on this branch
(Chromium 130, the pinned Typst): the console, library and baseline scripts pass, the baseline totals are 31 / 9 / 0 / 0 as before.
`docs/STUDIO.md` gains the 可读性 table (the deviations from the prototype that ADR 0014 introduces).
