# S07-G one version and publication model in the three designers

2026-11-06. ADR 0011 decisions 1–4. The document studio's controls are the model and do not change; the layout designer and the
dashboard studio take them. Nothing on the server changes: the same routes, the same requests, the same embed events.

G1 · The layout designer. A bar at the head of its panel, as the studios' top bars: the state badge (未查询, 草稿, 已保存 v{n},
已发布 v{n}, · 有未保存修改) and 版本 · 保存版本 · 发布. 发布 opens 发布前确认 (布局, 将发布的版本, 数据集, 列, 当前发布, 发布修订); with
nothing saved or with unsaved changes its button is 保存并发布, which saves the next version, reads its release state and
publishes it, the three requests in order as one step (S07-B's 保存并发布). 版本 opens 布局版本与发布指针: the saved versions (read
from the listing when it opens, with the release state), the picked one 恢复到编辑器 (an unsaved change, the pointer unchanged)
or 发布此保存版本 (a historical version is a rollback, through the same confirmation, saving nothing), 撤回发布; then the old
controls under their old ids: 刷新发布状态, 发布当前保存版本, 打开已发布版本, 另存为新布局, and the listing with the version number
under 打开其他布局或指定版本. The two dialogs are the shell's (`.sh-overlay.center`, `.sh-dialog`): modal, focus-trapped, Esc
closes and the focus returns. A refusal the designer keeps says what is missing (请先查询…, 请先保存或打开…, 请先刷新发布状态…, 列或对齐有未保存的修改…,
当前没有已发布版本。) instead of the query form's 请检查数据集、公司范围及数据时点。, which it showed for every refusal.

G2 · The dashboard studio. The inspector's 版本 tab is gone (属性 and 文件 stay); 版本 in the top bar opens 看板版本与发布指针, built
like the document studio's and the layout designer's, with the tab's controls in it under their old ids. Restoring a version
and publishing a historical one (`dashboard.js`: `listVersions`, `restoreVersion`, `publish(n)`) are new; the rest is the old code.
The help dialog's 保存与版本 and 发布 say so.

G3 · Measurement. T6 on main (b322481 and after): 8 clicks, 1 page switch, 0 dialogs, 2 dead ends. After: 5 clicks (报表, the
designer's summary, 上移, 发布, 保存并发布), 1 page switch, 1 dialog, 0 dead ends; T1–T5 unchanged. The totals 34 / 10 / 0 / 2 become
31 / 10 / 0 / 0, held in `scripts/ci_browser_evidence.sh` (`docs/UX_BASELINE_S07.md`, `evidence/s07a/baseline-after-s07g.json`).

Tests and evidence. DOM S07-G G1 (the bar, the badge's transitions, 保存并发布's three requests in order, a clean version keeping
确认发布, Esc publishing nothing, the version dialog's list and pointer, restore, a rollback that saves nothing and the server's
history publish 1 → 2 → 1, 撤回发布, the refusals' texts, logout) and G2 (the dashboard's dialog read on opening, restore, a rollback,
打开已发布看板 and the raw opener inside it); S06-C C6 adapted (two inspector tabs, the old controls in `#bd-d-versions`). The
real-browser script of S06-C checks the layout designer's bar, its 保存并发布 and its version dialog, and the dashboard's version
dialog, with contrast; the manual's 09 and 15 screenshots are taken again.
