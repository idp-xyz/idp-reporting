# S07-A the application rail and the data drawer

2026-11-04. ADR 0009 §2–§5: the two cheap, reversible changes to the passage between the console's shells, measured
before and after with the task baseline. Nothing inside the document studio, the template centre or the dashboard
studio changes except the two dead ends (§4); nothing on the server changes.

A1 · The application rail. A 56 px vertical rail (`nav.app-rail`, `aria-label="主导航"`) at the left edge of every
shell: the brand square (to the overview), the eight destinations as icon-and-label buttons in the order of the old
column (工作区概览, 数据中心, 报表工作台, 看板工作室, 模板中心, 文档工作室, 任务工作区; 能力与环境 after a gap), the active
one marked with `aria-current="page"`, the dataset and task counts as small badges on their buttons, the connection
state at the bottom (the dot; the labels for assistive technology). The 222 px navigation column of S06-C goes; the
console header and the main column move right by the rail. The document studio, the template centre and the dashboard
studio keep their full-screen boxes, now starting at the rail's right edge (`--app-rail`); their give-way rules no
longer hide the navigation. At ≤ 700 px the rail is a 48 px horizontal strip at the top of the viewport and the boxes
start below it. The rail is above the shells and below every dialog.

A2 · The data chip and the data drawer. The current data context is one object the console already holds
(`state.query`, `state.lastResult`, `state.snapshot`); a chip names it: `数据集 @ 版本 · 公司范围 · 时点 · 冻结快照 | 查询预览`,
or `未选择数据` with a dashed border. The chip is in the console header (every console page), in the template centre's
header (left of 返回设计器) and in the dashboard studio's context bar (where 回工作台查询 was, as 参数与筛选 next to the
binding text). It opens the data drawer: a right drawer (`role="dialog"`, modal, focus-trapped, Esc closes, focus
returns; the drawer frame of the studios) titled 当前数据, whose body is the workbench's query panel moved there while
the drawer is open and put back after (the park/unpark of `studio.js`, now in `app.js`): product, dataset, company scope,
date, the meta line, 查询预览 and 创建快照, unchanged in behaviour. A result arriving (preview or snapshot) closes the
drawer and the chip updates everywhere. The document studio keeps its own 参数与筛选 drawer (the same panel). The embed
pages have no chip and no drawer (the query panel sits in the view, as today).

A3 · No dead ends. The dashboard studio's start card says 选择数据集与公司范围，预览或冻结快照后再组合指标卡、明细表和柱状图。
and its button 选择数据… opens the drawer; the data panel's button does the same. The centre's refusal line reads
先选择数据集并预览或冻结快照：设计器需要当前授权的数据集。 and carries a 选择数据… button; the per-card tooltips say the same.
In the embed page these buttons are hidden by the page's rule, as the page-opening buttons are.

A4 · Tests and evidence. DOM: the rail on every shell with its rows, badges and the single `aria-current`; the chip's
text through 未选择数据 → 查询预览 → 冻结快照 and back on sign-out; opening the drawer moves the query panel in and Esc
moves it back where it was; a preview run inside the drawer closes it and the dashboard's start card gives way without
a page change; the start card's and the centre's 选择数据… open the drawer; the drawer's focus rules; the existing
S06-C tests adapted to the rail. Embed: the dashboard section's page-opening controls are now the wordmark and the
crumb; the data buttons are hidden by rule. The task baseline `scripts/ux_baseline_s07.mjs` is run again on the same
tasks and the numbers go beside the S06-C ones in `docs/UX_BASELINE_S07.md` (`evidence/s07a/baseline-before.json`,
`baseline-after.json`); `scripts/browser_console_s06c.mjs` is updated for the rail's geometry and re-run
(`evidence/s07a/console/`). docs/STUDIO.md gains "应用栏与数据抽屉"; CONSOLE.md and the README are updated.

Not in this slice: merging pages, a home with recent items, tasks as a header panel, unifying the designers' version
and publication controls, any change to the studios' internals (ADR 0009 §5).
