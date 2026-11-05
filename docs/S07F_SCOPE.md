# S07-F one shell

2026-11-05. ADR 0010. The console header stays on every page; the document studio, the template centre and the dashboard studio
start below it, beside the rail.

F1 · Stylesheets. `--shell-head` (68 px, 56 px at ≤ 700 px, 0 on the embed page) offsets the three fixed boxes as `--app-rail`
does; `body.studio-on`, `body.library-on` and `body.board-on` no longer hide `.console-header` (they still hide the footer and
the skip link). The boxes' wordmarks with their divider and the centre's `#lib-data` are hidden outside the embed page; the
centre's header is 56 px and its layout's minimum height accounts for the header above it.

F2 · Labels. The header's label names the three boxes (DOCUMENT STUDIO, TEMPLATE WORKSPACE, DASHBOARD STUDIO), as the other pages
have theirs; the page title is unchanged.

F3 · Measurement. `scripts/ux_baseline_s07.mjs` counts a shell by the header on screen. Before (main, 2026-11-05): 26 clicks,
5 texts, 9 page switches, 8 shell changes, 5 dialogs, 0 dead ends. After: the same but 0 shell changes (docs/UX_BASELINE_S07.md).

F4 · Found on the way. The template centre's cards in the card view inherited the data centre's `.catalog-card` column layout
(styles.css, S06-C), which wrapped their actions into a second column outside the card and made the centre scroll sideways at
every width; the centre's rule now sets its own direction. `scripts/browser_console_s06c.mjs` checked a task count with a Node
variable inside page code (S07-C), and a digit class that a template string turned into a letter; both fixed. The task panel's filter row (S07-E) had no side padding and the longer heading text pushed the count badge onto a
second line; the row actions (刷新, 查看结果, 下载) read as one word: the filters are padded, the heading is shorter, the actions take the
theme's selection ink (4.5:1 in both themes) and a visible gap.

Tests and evidence. DOM test S07-F (the label and title of each box, the header's controls on each, the stylesheets no longer hide
the header and hide the duplicates); the real-browser scripts of S06-C (136 checks) and S06-B (41 checks) with the boxes below the
header, nothing overflowing at 1440, 1280, 768 and 390, and contrast as before; the manual's screenshots taken again.
