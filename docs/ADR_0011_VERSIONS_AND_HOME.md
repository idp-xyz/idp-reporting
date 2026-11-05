# ADR 0011 · One version and publication model in the three designers; the workbench as the home

Date: 2026-11-05. Status: proposed. Decides the two questions ADR 0009 §5 left open after S07-F: unifying the three
designers' version and publication controls, and folding the overview and the data centre into other pages.

## Context

**Versions and publication.** The server already has one model for all three designers: immutable saved versions, a
publication pointer with a revision, `GET/POST /{layouts|dashboards|document-templates}/{id}/release` taking
`{action: publish|withdraw, version, expected_revision}`, and `GET …/{id}/published`. The console shows that one model
three ways:

| | 报表布局设计器 (workbench) | 看板工作室 | 文档工作室 |
|---|---|---|---|
| Where | a collapsed `<details>` in the result panel, every control inline | top bar 版本 · 保存版本 · 发布; the rest in the inspector's 版本 tab | top bar 版本 · 保存版本 · 发布; 版本 opens 版本历史与发布指针 |
| Save | 保存服务器版本 | 保存版本 | 保存版本 |
| Pick a version | a select and a number field, 打开服务器版本 | a select and a number field, 打开服务器看板版本 | a version list: 预览此版本, 恢复到编辑器 |
| Release state | read only on 读取发布状态 | read by 发布, or 刷新发布状态与历史 | read by the dialogs |
| Publish | 发布当前已保存版本; refused before the state is read, refused with unsaved changes | 发布 → 发布前确认, 保存并发布 when unsaved (S07-B) | 发布 → 发布前确认 with checks, 保存并发布 when unsaved (S07-B); 发布此保存版本 for an older one |

The layout designer still has both refusals that S07-B removed from the studios. The task baseline does not see them
because none of its five tasks publishes a layout. The prototype (ADR 0008) specifies only the document studio; its
版本历史与发布指针 and 发布前确认 dialogs are the only version and publication interface it has.

**The overview and the data centre.** Every sign-in lands on the overview (`openPage` falls back to it). It holds four
counts, a short list of datasets that open the workbench, and the steps 从查询到原件. Each part is already available
elsewhere: the dataset and task counts are badges on the rail, the export formats are on 能力与环境, the product connections
are the data drawer's 产品连接 list, the manual's 十分钟上手 covers the same steps, and the dataset list leads to the
workbench. The data centre lists each dataset's columns with their types,
currencies and units. That is the one thing on these two pages that no other page shows. Since S07-A the data drawer has
been the way to choose data from any page. The template centre's 数据中心 button (the prototype's) still goes to the page.
None of the five baseline tasks uses either page. T1's single page switch is the step from the overview to the workbench.

## Decision

1. **The document studio is the model and does not change.** The dashboard studio and the layout designer take its
   controls, labels and order. The server, its routes and the embed page's events are unchanged.
2. **Three controls in the same place.** Each designer has 版本, 保存版本 and 发布 in its top bar. The layout designer
   gets a bar of its own at the head of its panel. 另存为新… and the layout file actions (生成布局文件, 打开布局) stay
   where they are.
3. **One version dialog.** 版本 opens a dialog built like 版本历史与发布指针: the version list, then 预览此版本,
   恢复到编辑器, 发布此保存版本, 撤回发布 and 打开已发布… for the selected version. Opening the dialog reads the release
   state, so 读取发布状态 and 刷新发布状态与历史 become a retry after a conflict. In the dashboard studio this replaces
   the inspector's 版本 tab and the select-and-number pair.
4. **One publication path.** 发布 opens 发布前确认 in all three designers, with 保存并发布 when there are unsaved
   changes or no saved version yet. The layout designer loses both of its refusals.
5. **The workbench is the home.** A sign-in lands on the workbench. The rail brand and the wordmark lead there. The
   overview page and the rail's 概览 item are removed. `#overview` opens the workbench, and so does any unknown page.
6. **The data centre becomes the data drawer's dataset picker.** Choosing a dataset in the drawer shows what a data
   centre card shows today: columns, types, currencies and units, and the counts of dimensions and metrics. The rail's
   数据 item and the page are removed. The template centre's 数据中心 button and `#catalog` open the drawer. Authorization
   and the catalogue requests are unchanged; the drawer shows the same listing.
7. **Two slices, each with the baseline.** S07-G does 1–4 and S07-H does 5–6. Each slice measures the baseline on main
   before its change and on its branch after. When a number changes, the PR updates docs/UX_BASELINE_S07.md and the held
   limits in scripts/ci_browser_evidence.sh together. A lower number becomes the new limit.
   - S07-G adds T6 (报表工作台：调整一列 → 保存 → 发布布局). Like T2, the script presses 发布 first and takes the old
     path only when refused, recording the dead end. T6 is measured on main before the change. Adding it raises the
     totals, so the limits are reset to the measured numbers in that PR.
   - S07-H changes the script's `openPage` so it does not click a page that is already open. Without that change, T1
     would still click the rail's 报表 item after landing on the workbench, and the click would be counted.

## Consequences

The embed page shares the designers' markup, so `index.html` and `embed.html` change together in the layout designer's
panel and the dashboard studio's inspector. The embed page has no overview or data centre and is otherwise unaffected.
`app.js` gains the layout designer's bar and dialogs and loses two pages. `board.js` and `dashboard.js` move the version
controls into a dialog. `studio.js` and `documents.js` do not change. The rail goes from eight destinations to six. The DOM
tests, the real-browser script of S06-C (its overview and data centre checks), docs/CONSOLE.md and the manual's text and
screenshots are updated with the slice that changes them.

Expected effect, to be confirmed by measurement: T1 loses one click and one page switch, T6 loses its dead end, and the
other tasks are unchanged. The fold of the data centre has no baseline task, so the numbers can show only that it adds no
cost, not that it helps.

## Open questions

- Which of the designer's actual tasks use the data centre's column list, and how often? If one is common, it should
  become a baseline task before S07-H, so that S07-H is measured on it.
- The dashboard studio: should the version dialog replace its inspector's 版本 tab (decision 3), or should the tab stay
  with the same contents and labels?
- Does 从查询到原件 need a place in the workbench's empty state, or is the manual's 十分钟上手 enough?
