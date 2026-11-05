# ADR 0011 · One version and publication model in the three designers; the workbench as the home

Date: 2026-11-05. Status: accepted. Decides the two questions ADR 0009 §5 left open after S07-F: unifying the three
designers' version and publication controls, and folding the overview and the data centre into other pages. The three
questions the draft left open were decided on the owner's instruction, without the designer's input; "Decided questions"
below gives each answer and what would reopen it.

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

The layout designer still has both refusals that S07-B removed from the studios. Task T6 of the baseline (报表工作台：调整一列
→ 保存 → 发布布局, added on main with b322481) measures them: 8 clicks, 1 page switch and 2 dead ends, first for no saved
version and then for the release state not read. Both refusals show the same text, "请检查数据集、公司范围及数据时点。",
because `layoutAction` reports each as a 400 INVALID_REQUEST, so nothing tells the person which step is missing. The
prototype (ADR 0008) specifies only the document studio; its 版本历史与发布指针 and 发布前确认 dialogs are the only
version and publication interface it has. The dashboard studio is not in the prototype.

**The overview and the data centre.** Every sign-in lands on the overview (`openPage` falls back to it). It holds four
counts, a short list of datasets that open the workbench, and the steps 从查询到原件. Each part is already available
elsewhere: the dataset and task counts are badges on the rail, the export formats are on 能力与环境, the product connections
are the data drawer's 产品连接 list, the manual's 十分钟上手 covers the same steps, and the dataset list leads to the
workbench. The data centre lists each dataset's columns with their types,
currencies and units. That is the one thing on these two pages that no other page shows. Since S07-A the data drawer has
been the way to choose data from any page. The template centre's 数据中心 button (the prototype's) still goes to the page.
None of the six baseline tasks uses either page. T1's single page switch is the step from the overview to the workbench.
The workbench's empty state says only 从一个已发布的数据集开始 and 金额与大整数按原始精度显示。.

## Decision

1. **The document studio is the model and does not change.** The dashboard studio and the layout designer take its
   controls, labels and order. The server, its routes and the embed page's events are unchanged.
2. **Three controls in the same place.** Each designer has 版本, 保存版本 and 发布 in its top bar. The layout designer
   gets a bar of its own at the head of its panel. 另存为新… and the layout file actions (生成布局文件, 打开布局) stay
   where they are.
3. **One version dialog.** 版本 opens a dialog built like 版本历史与发布指针: the version list, then 预览此版本,
   恢复到编辑器, 发布此保存版本, 撤回发布 and 打开已发布… for the selected version. Opening the dialog reads the release
   state, so 读取发布状态 and 刷新发布状态与历史 become a retry after a conflict. In the dashboard studio this replaces
   the inspector's 版本 tab and the select-and-number pair; the inspector keeps 属性 and 文件.
4. **One publication path.** 发布 opens 发布前确认 in all three designers, with 保存并发布 when there are unsaved
   changes or no saved version yet. The layout designer loses both of its refusals. A refusal that remains (a binding
   that no longer matches the data, a conflict) says what is missing instead of the generic query message.
5. **The workbench is the home.** A sign-in lands on the workbench. The rail brand and the wordmark lead there. The
   overview page and the rail's 概览 item are removed. `#overview` opens the workbench, and so does any unknown page.
   While the workbench has no result, its empty state shows the overview's three steps (选择已发布数据集, 预览并创建快照,
   生成 Excel 并下载) with a link to the manual's 十分钟上手. They go away with the first result.
6. **The data centre becomes the data drawer's dataset picker.** Choosing a dataset in the drawer shows what a data
   centre card shows today: columns, types, currencies and units, and the counts of dimensions and metrics. The rail's
   数据 item and the page are removed. The template centre's 数据中心 button and `#catalog` open the drawer. Authorization
   and the catalogue requests are unchanged; the drawer shows the same listing.
7. **Two slices, each with the baseline.** S07-G does 1–4 and S07-H does 5–6. Each slice measures the baseline on main
   before its change and on its branch after. When a number changes, the PR updates docs/UX_BASELINE_S07.md and the held
   limits in scripts/ci_browser_evidence.sh together. A lower number becomes the new limit.
   - S07-G is measured by T6, which is already on main. Its before numbers are main's held totals: 34 clicks, 10 page
     switches, 0 shell changes, 2 dead ends.
   - Before S07-H, a separate PR adds T7 (查看一个数据集的字段与币种 → 用它查询预览) and measures it on main, as T6 was
     added before S07-G. On main the task goes through the data centre's card and its 打开工作台; after S07-H it goes
     through the data drawer. The limits are reset to the measured totals in that PR.
   - S07-H changes the script's `openPage` so it does not click a page that is already open. Without that change, T1
     would still click the rail's 报表 item after landing on the workbench, and the click would be counted.

## Consequences

The embed page shares the designers' markup, so `index.html` and `embed.html` change together in the layout designer's
panel and the dashboard studio's inspector. The embed page has no overview or data centre and is otherwise unaffected.
`app.js` gains the layout designer's bar and dialogs and loses two pages. `board.js` and `dashboard.js` move the version
controls into a dialog. `studio.js` and `documents.js` do not change. The rail goes from eight destinations to six. The DOM
tests, the real-browser script of S06-C (its overview and data centre checks), docs/CONSOLE.md and the manual's text and
screenshots are updated with the slice that changes them.

Expected effect, to be confirmed by measurement. S07-G: T6 goes from 8 clicks and 2 dead ends to 5 clicks and none (报表,
open the designer, 上移, 发布, 保存并发布), with one more dialog (发布前确认), and the other tasks are unchanged. S07-H: T1
loses one click and one page switch, and T7 should not cost more than it does on main.

## Decided questions

The draft left three questions for the designer. The owner asked for them to be decided without that input (2026-11-05).

1. **How often do real tasks use the data centre's column list?** Unknown, so it is measured instead of assumed: T7 is
   added before S07-H (decision 7). Decision 6 keeps the column list, so the fold removes a page, not information.
   *Reopen if* T7 costs more after S07-H than on main.
2. **The dashboard studio's 版本 tab: replaced or kept?** Replaced by the version dialog (decision 3). Keeping it would
   leave the same controls in two places in one studio, the inconsistency this ADR removes. On narrow screens the dialog
   also works without opening the inspector first. *Reopen if* the designer works with the version list open while
   editing, which a modal dialog does not allow.
3. **The 从查询到原件 steps: the workbench's empty state, or the manual only?** The empty state (decision 5). Once the
   overview is gone, the steps are the console's only orientation for a first sign-in. They cost no click and disappear
   with the first result. *Reopen if* they crowd the workbench at phone widths.
