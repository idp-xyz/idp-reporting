# S06-C the rest of the console

2026-11-03. ADR 0008 §4: after the document studio (S06-A) and the template centre (S06-B), the rest of the console takes
the prototype's shell and tokens: the sign-in, the workspace overview, the data catalogue (the prototype's "数据中心" entry),
the report workbench, the dashboard designer (a studio of its own kind), the tasks page and the system page; the embed
pages take the same views without the shell, as today. The prototype `IDP_Reporting_Designer_UIUX_V3.0.html` has two
screens that are not the document studio: its template centre (`evidence/ux-gap/proto-10-library.png`, reproduced by
S06-B) and its data centre (the `dataCenterView`: a header, a 260 px left column, a titled main column with tabs and a
white workbench card). Those two screens are the specification of the console's shell; where the console has a page the
prototype has not (overview, tasks, system, sign-in), the page is built from the same regions and components and the
difference is listed. Fidelity rule as before (ADR 0008 §1): at 1440 px the shell agrees with the prototype's library and
data centre in structure, labels, states and spacing; every deviation is listed in `docs/STUDIO.md` with its reason;
nothing deviates for taste. What must hold is unchanged: the CSP (no inline script or style), no `innerHTML`, no
third-party code, the token in memory only, keyboard operation and accessible names, light and dark themes, phone
widths, the DOM tests through the real Go server, the server's authority. Nothing on the server changes.

C1 · The shell. Every page that is not a studio is laid out as the prototype lays out its template centre and data
centre: a 68 px header (`library-header`) with the wordmark (brand square, "IDP REPORTING", a small label that names the
page: REPORTING WORKSPACE, DATA CENTER, REPORT WORKBENCH, BACKGROUND TASKS, PLATFORM CAPABILITIES), the environment tag
(合同模拟器, the prototype's `prototype-tag` style), the identity's subject and 退出工作区; a 222 px left column
(`catalog-sidebar`) holding the main navigation as `catalog-nav` rows with counts where a count exists (工作空间:
工作区概览, 数据中心 with the dataset count, 报表工作台, 看板工作室, 模板中心, 文档工作室, 任务工作区 with the task count; 平台:
能力与环境) and the connection note at the bottom (the prototype's `catalog-footnote`); the main column (`catalog-main`)
with the title block (eyebrow, title, lead, the page's primary action on the right, as `catalog-top`) and the page's
content. Tokens are the library's (`library.css`: background #f7f9fc, white panels, #e0e6ee lines, #4261df blue, the
Inter stack, radii 5 / 6 / 8, the pill set, the `.btn` family); dark tokens are derived as `library.css` derives them
and are also selectable by `data-theme` for the embed. The components of the pages are rewritten in the prototype's
vocabulary: `.btn`, fields, `data-table` in `dc-table-scroll`, pills, `dc-note`, `dc-empty`, `catalog-stats`,
`dc-source-cards`, `dc-tabs`, `dc-section-head`, `dc-source-summary`. The S03-A shell and component rules leave
`styles.css`; the canvas rules (`cv-*`, `cv2-*`) stay as they are. The studio and the centre keep their own full-screen
shells (they are the prototype's already); the body classes `studio-on` / `library-on` now hide the new chrome.

C2 · Sign-in. The connection card in the shell's tokens: the wordmark, 进入报表工作区, the lead, the token field,
连接工作区, the hint about the simulator and the token in memory. The prototype has no sign-in (a listed deviation); the
behaviour (focus, Tab cycle, errors, no token in the URL or storage) is unchanged.

C3 · 工作区概览: the four counters as `catalog-stats` (big number, small label) in a white card row; 可用数据集 as the
data centre's dataset list rows (name, id · version, 打开); 从查询到原件 as the steps panel; the footer note.

C4 · 数据中心 (the data catalogue): the prototype's data centre screen: the title block (DATA CENTER, 数据中心, lead, 刷新目录
on the right); the product connection as a chip row (`dc-chips`); the datasets as `dc-source-cards` (icon, name,
id@version, "N 个维度 · M 个指标", the column table of the dataset in a `dc-table-scroll` with label, id, type, currency or
unit, 打开工作台). The prototype's data centre edits dataset definitions, imports them, tests and versions them; the
platform's catalogue comes from the source product and is read-only: none of that is reproduced (listed).

C5 · 报表工作台: the query panel as the data centre's workbench card (`dc-workbench` with `dc-form-grid` fields, the meta
line, the actions row); the result as a section with `dc-section-head` (title, subtitle, 重新校验 / 导出 Excel / 导出 PDF),
the summary cards, the `data-table` in `dc-table-scroll`, the pager as `catalog-pagination`, the evidence line; the report
layout designer (columns, order, alignment, server layouts, release) as a collapsible section under the result with the
same controls and ids.

C6 · 看板工作室, a studio of its own kind: the studio's regions with the dashboard's functions, every existing control
keeping its id (the DOM tests drive the dashboard by id): the topbar (wordmark DASHBOARD STUDIO, crumb 报表工作台 ›, the
title 看板 and the state badge 未查询 / 草稿 / 已保存 v{n} / 已发布 v{n} / … · 有未保存修改, derived from the dashboard's own
state, the help button, 版本 (opens the inspector's 版本 tab), 保存版本 (`board-server-save`), 发布 (reads the release
state, shows the 发布前确认 dialog with the saved version, the binding and the widget count, then publishes)); the tool rail
(组件, 结构, 数据; 帮助 at the bottom) switching the side panel: 组件库 (the dataset card; 指标卡 with the metric chooser
and 添加指标卡; 明细表 with the column chooser and 添加明细表; 当前页柱状图 with the category and value choosers and
添加当前页柱状图; the count and 清空组件), 组件与顺序 (the widgets in order with 上移 / 下移 / 移除, the same functions as
the cards' buttons), 数据来源 (the binding, the date, snapshot page or preview, the note that metric cards are server
totals and tables and charts are the current page); the work area (the context bar with the binding and 回工作台查询, the
desk with the widget cards in the prototype's card style, the start card when there is no authorized result, the status
toast); the inspector with tabs 属性 (the binding pill, the widget count, the server state), 版本 (刷新我的看板, the listing,
the version number, 打开服务器看板版本, 另存为新看板; 刷新发布状态与历史, 发布当前保存版本, 撤回发布, 打开已发布看板, the release
status and history) and 文件 (生成看板配置文件, the JSON, 打开看板配置, the note on what a configuration holds); the status
bar (the state, the count, the binding). `dashboard.js` gains a state accessor and tells its port when the state changes;
its actions return their promise so that the studio layer can wait for them; nothing else in it changes. At 1280 the
inspector narrows; at 768 the side panel and the inspector are drawers; at 390 the inspector is a bottom sheet; no
horizontal overflow. No ribbon: the dashboard has no tools that are not in the side panel (listed as a decision).

C7 · 任务工作区 and 能力与环境: the title blocks; 恢复查看 as a `dc-source-summary` row; the tasks as a `data-table` in a
`dc-table-scroll` with the empty state; the identity and the capabilities as two cards of definition lists; the scope
note as a `dc-note`.

C8 · The embed pages: the same three sections, byte for byte (the existing test); `embed.css` is rewritten for the new
classes: no header, no navigation, the content at the frame's width, the dashboard studio not fixed (the frame is as
tall as its content), the dark theme by `data-theme` over the same tokens.

C9 · Responsiveness, themes, keyboard: no horizontal overflow at 1440, 1280, 768, 390; the navigation column is 222 px,
190 px at ≤ 1100 px and a horizontal chip row at ≤ 700 px (as the prototype's library sidebar); every control has an
accessible name; the navigation is a `nav` with `aria-current`; the skip link stays; `document.title` names the page;
the dark theme by `prefers-color-scheme` and `data-theme`, with the contrast of every visible text measured by the
browser script.

C10 · Tests and evidence. DOM (through the real Go server): the shell's regions, labels and counts on every page; the
navigation by keyboard and the active state; `document.title`; the body classes; the sign-in card; the dashboard studio
(every old control present under its old id and in its new region; the badge's transitions 未查询 → 草稿 → 已保存 v1 →
已发布 v1 → · 有未保存修改; the structure panel's buttons do what the cards' buttons do; the publish confirm's reading, text
and outcome; the drawers' toggles; the start card; the source scan of `board.js`); the embed page's three sections byte
for byte and the query panel landing in the dashboard page. Existing tests are adapted to the new markup without
weakening their assertions. Real-browser script `scripts/browser_console_s06c.mjs` (Windows Chrome over CDP, the pattern
of `browser_library_s06b.mjs`): every page at 1440 beside the prototype's library and data centre, alone at 1280, 768
and 390, light and dark, the overflow and the contrast measured; a session (sign in, overview, data centre, workbench
preview and snapshot, the dashboard studio with a metric card, a table and a chart, save, versions, publish, tasks,
system, sign out). `docs/CONSOLE.md` rewritten as the map of the new console; `docs/STUDIO.md` gains the sections
"控制台外壳" and "看板工作室" (region → function, deviations with reasons, decisions, tests, screenshot index); README S06-C
at the top.

Not in this slice: any server change; the prototype's data centre as an editor (definitions, imports, tests, versions);
a ribbon for the dashboard studio; new dashboard widgets; sharing between identities.
