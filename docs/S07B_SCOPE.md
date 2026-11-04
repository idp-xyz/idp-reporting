# S07-B the tasks panel and 保存并发布

2026-11-04. ADR 0009 §5 named what to decide next on the S07-A numbers. Two more cheap, reversible changes, measured with the
same task baseline: the tasks become a panel over any shell (the baseline's T4 still paid page switches to see a job's
result), and the two designers' publish confirmations save the unsaved changes first instead of refusing (the baseline's
T2 and T3 paid 保存版本 → 发布 → 确认 with a refusal in between). Nothing on the server changes.

B1 · The tasks panel. The rail's 任务 button (`data-panel="tasks"`, `aria-haspopup="dialog"`, `aria-expanded`) opens a
right drawer (the frame of the data drawer) over whatever is on screen: the tasks page's restore row, its list (the
same table and buttons: 刷新, 查看结果, 下载, 取消) and its 刷新状态 button are moved into the drawer while it is open and put
back after. 查看结果 goes to the workbench and the drawer closes; opening the data drawer closes the tasks panel and the
other way round; Esc closes and returns the focus to the rail's button; the counts and the badge are unchanged. The
tasks page itself stays (the `#tasks` address and the overview's counter still reach it); it is no longer a destination of
the rail.

B2 · 保存并发布. In the dashboard studio, 发布 with unsaved changes or without a saved version opens the 发布前确认 as
before, now naming what happens (先保存为版本 N，再发布它; 新看板 when nothing is saved) with the button 保存并发布: confirming
saves the next version, reads its release state and publishes it; a clean saved version keeps 确认发布; the refusals
請先保存… are gone (the refusal without data stays). In the document studio, the same confirmation's 确认发布 was disabled
with 先保存再发布 while the canvas had unsaved changes or no version was saved; it is now 保存并发布 (`api.save(then)` →
`api.readRelease(then)` → `api.publish()`), with the footnote 先保存为新版本，再发布它; publishing a historical version from the
versions dialog is unchanged. Nothing about what a version or a publication is changes: the same two requests, in order.

B3 · Tests and evidence. DOM: the panel over the dashboard studio (the parked rows, the focus, Esc, 查看结果 landing in the
workbench with the content back, the two drawers never overlapping); 保存并发布 in the dashboard studio (nothing saved →
version 1 published; unsaved changes on a saved dashboard → version 2 published; a clean version keeps 确认发布) and in
the document studio (草稿 → 已发布 v1 → · 有未保存修改 → 已发布 v2); the S06-A, S06-C and S07-A assertions adapted (the
rail's rows, the confirmation's state). The task baseline run again (`evidence/s07a/baseline-after-s07b.json`, the table
in `docs/UX_BASELINE_S07.md`); `scripts/browser_console_s06c.mjs` updated (the tasks panel, the confirmation) and re-run.

Not in this slice: a home with recent items, merging the three full-screen shells, unifying the designers' version and
publication controls beyond this one step (ADR 0009 §5).
