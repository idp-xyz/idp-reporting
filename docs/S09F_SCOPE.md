# S09-F one save / publish / history pattern in every designer

2026-11-11. ADR 0014 decision 11, after S09-G. The server's version model (ADR 0011: immutable saved versions, one published version, a
revision count for concurrency, withdrawal) and its API do not change; the console presents it with the three words a person knows —
保存 · 发布 · 历史 — in the report layout designer (`app.js`, `#layout-*`), the dashboard studio (`board.js` over `dashboard.js`), the document
studio (`studio.js` over `documents.js`) and the embed page's designer views (the shared sections of `embed.html` stay byte-identical with
`index.html`). The bars' labels, the dialogs' markup and the shared component `console/assets/versions.js` (S09-E) carry the words; the hosts
keep their request sequences.

F1 · The words. Before → after, everywhere the same:

| Before (S07-G / S09-E) | After (S09-F) | Where |
|---|---|---|
| 版本 · 保存版本 · 发布 (the bar) | 保存 · 发布 · 历史 (the document studio keeps 预览与导出 between 保存 and 发布) | the three bars, the embed |
| 版本历史与发布指针 / 布局版本与发布指针 / 看板版本与发布指针 (the dialog) | 历史 — “每次保存是一个版本：打开一个继续编辑，或发布一个” | the three dialogs |
| the card: 已发布 / 已保存 pill, “编辑器打开的版本（有未保存修改）” / “保存版本” | the card: v{n} with the marks 已发布 and 当前打开 as they apply, “有未保存的修改” under the open one | the component |
| 恢复到编辑器 | 打开 — its content into the editor as the draft; the note 打开后保存会成为下一个版本（v{n+1}）; disabled with the reason 编辑器已经打开这个版本 on the open, unchanged version | the component |
| 发布此保存版本 | 发布此版本 — that saved version as it is (an older one is a rollback; the open version with unsaved changes publishes the saved one, and the confirmation says the unsaved changes are not in it) | the component, the hosts |
| 撤回发布 on every pick (disabled when nothing is published) | 撤回发布 on the published version's card only | the component |
| 另存为新布局 / 另存为新看板 / 另存为新模板 (in the release box) | 复制为新布局 / 复制为新看板 / 复制为新模板 in the dialog's footer | the markup (same ids) |
| 刷新发布状态 / 刷新发布状态与历史 (a button) | gone: the dialog reads the list and the release state when it opens; a publish the server refuses because the state changed (409 `IDEMPOTENCY_CONFLICT`) brings the confirmation back, blocked until the state is read again, with the note 发布状态已改变（另一处发布或撤回过）：已重新读取，请核对后重试。 and the button 重试 | the component (`conflict()`), the hosts' publish paths |
| 发布当前保存版本 (a button) | gone: the bar's 发布 is it | — |
| 打开已发布版本 / 打开已发布看板 / 打开已发布模板 (buttons; the chooser's 打开已发布版本) | gone: issuing has its own page since S09-G; 历史's 打开 is the one action; a published older version is opened as the editor's version by number under 高级 | the markup, `app.js`, `dashboard.js`, `documents.js`, `studio.js` |
| 保存版本不可变；当前发布指针：v{n} (the footer) | 当前发布：v{n} / 无 (the revision number in its `title`) | the component |
| 发布前确认: the row 发布修订; 将发布的版本 “保存版本 2” | the row is gone (the revision is the `title` of the 当前发布 row's value); “版本 2”, “版本 1（历史版本）”, “先保存为版本 2，再发布它” | the component; the studio's own confirmation says 发布的是一个保存过的版本 where it said 已明确引用不可变保存版本 |
| 打开其他布局或指定版本 (a `<details>` in the dialog) | the same `<details>` inside the layout designer's 高级 · JSON 导入与导出、打开指定版本 section (decision 5) | `index.html`, `embed.html` |
| 打开其他看板或指定版本 (in the dialog) | 高级 · 打开其他看板或指定版本, a `<details>` in the inspector's 文件 tab, like the dashboard's JSON | `index.html`, `embed.html` |
| 打开指定版本 (the studio's chooser) | 高级 · 打开指定版本 | `index.html`, `embed.html` |
| 发布指针, 修订 (in notes, statuses, the chrome's `title`, the publication history list) | gone from the interface: 当前发布, 版本; the status lines and the publish-event list of the release box are no longer shown (the badge, the cards and the footer say what they said) | the three hosts |
| 按保存版本生成PDF | 按已保存的版本生成PDF | the preview dialog |
| 版本与回退 (the ribbon's tool) | 历史 | the studio's 检查与发布 |
| the command palette: 保存模板版本, 发布当前保存版本, 打开已发布模板 | 保存, 发布 (`st-publish`), 历史 (`st-versions`) | `canvas.js` |
| the template centre: “N 个保存版本”, “在设计器中打开最新保存版本”, “（保存版本 1）” | “N 个版本”, “在设计器中打开最新版本”, “（版本 1）” | `library.js` |

Every element the tests and the baseline address keeps its id (`layout-versions`, `layout-server-save`, `layout-publish`, `bd-versions`,
`board-server-save`, `bd-publish`, `st-versions`, `doc-server-save`, `st-publish`, `*-ver-restore`, `*-ver-publish`, `*-release-withdraw`,
`*-server-new`, the raw choosers' `*-server-list/-listing/-version/-open`). Removed from the DOM, with their listeners: `layout-release-status`,
`layout-release-publish`, `layout-release-open`, `layout-release-info`, `layout-release-history`, `board-release-read`, `board-release-publish`,
`board-published-open`, `board-release-status`, `board-release-history`, `doc-release-read`, `doc-release-publish`, `doc-published-open`,
`doc-release-status`, `doc-release-history`; `doc-server-status` (the studio's server status line) stays in the dialog's body.

F2 · 发布 with unsaved edits: the smallest honest behaviour. The bar's 发布 opens 发布前确认 as before (S07-B, S07-G); with unsaved edits, or
nothing saved yet, the summary says 先保存为版本 {n+1}，再发布它, the note names what is unsaved (列或对齐有未保存的修改 / 画布有未保存的修改 /
the studio's 需要先保存当前修改), the button is 保存并发布 and 取消 is beside it; 取消 saves and publishes nothing. 保存并发布 is the
host's own sequence (save, read the new version's release state, publish it). A clean saved version keeps 确认发布.

F3 · The published version is what the release state says. `documents.js` loses the `publishedOpen` flag (the ritual 打开已发布模板 set it):
the embed page's issue dialog — the console issues from the 出具 page since S09-G — checks that the editor holds the saved version the
release state names, unchanged; when this session has not read the state yet, it reads it once and then decides; the refusal says which
version is published and which is open (正式出具只接受当前发布版本（v1）：编辑器打开的是 v2。). An edit disarms it and undoing the edit arms
it again, since the content is the saved content once more; the server checks the version anyway. The badge's 已发布 follows the same
reading (`pointer()`), so it shows once 历史 or 发布 has read the state, as S06-A's deviation 3 and S06-C's deviation 6 say.

F4 · Conflicts. A publish refused with 409 `IDEMPOTENCY_CONFLICT` — another publish or withdrawal moved the revision since the state was
read — is told in the notice as 发布状态已改变（另一处发布或撤回过）：已重新读取，可在发布前确认里重试。 (`app.js` `fail`, the context's
`conflict` text) and handed to the component (`conflict()`): the confirmation comes back for the same version (a 保存并发布 has saved by then
and retries the publish alone), reads the release state again, shows the fresh 当前发布, and its button says 重试. The layout designer's
`changeRelease` and 保存并发布, the dashboard's and the studio's `changeRelease` through `ports.exchange(path, options, apply, label,
conflict)` carry the hook; a withdrawal has no retry (it is made from 历史, which has just read the state).

F5 · Tests. `console/dom.test.mjs`: every test that pressed the dialog's raw buttons publishes through the bar and 发布前确认 (`publishFromBar`),
reads through 历史 (`openHistory`) and opens a published older version by number under 高级; the asserted words follow; S21-A's dashboard test
(rebased under) does the same. New, at the end of the file: S09-F F1 (the three bars, the dialogs' titles, the old words and ids gone), F2
(发布 with unsaved edits: 保存并发布 or 取消), F3 (历史's marks, notes, actions, footer; 打开 and 撤回发布), F4 (a rollback through 发布此版本),
F5 (the changed-state 重试, in the layout designer and the document studio), F6 (the raw choosers under 高级, and one opening a version by
number). `console/embed.dom.test.mjs`: the embedded studio publishes through the bar, withdraws from 历史, issues the published version it
holds. The S03-H issue tests keep their shared published template, now published through the bar. `console/i18n.test.mjs`: 0 missing, 0 stale
(the retired words' entries removed, the new ones appended). Both runs of the suites pass: with the renderer (CI's PDF job: DOM + embed
236 tests, 235 passed, 1 skipped; i18n 5) and without it (CI's console job: DOM + embed + i18n 241 tests, 220 passed, 21 skipped, the
Typst tests skipping themselves).

F6 · Measurement (ADR 0011 decision 7). `scripts/ux_baseline_s07.mjs` clicks the new words (保存 in T2's old path; T6's published signal is the
badge); measured on the base (988075e, S09-G: `evidence/s07a/baseline-before-s09f.json`) and on this branch after the rebase onto S21-A and
S09-G's final commits (`evidence/s07a/baseline-after-s09f.json`): T1–T8 are the same in both runs, 35 clicks, 10 page switches, 0 shell
switches, 0 dead ends; the team tasks 25 / 8 / 0 / 1. No step was added or removed by the words — the dialog's 刷新发布状态 was never a step of
a baseline task — so `scripts/ci_browser_evidence.sh`'s held numbers stay 35 / 10 / 0 / 0 and 25 / 8 / 0 / 1. The real-browser scripts
(`browser_console_s06c.mjs`, `browser_studio_s06a.mjs`, `browser_embed_s03n.mjs`, `browser_visual_s03c4.mjs`, `manual_screens.mjs`) click the
new words and read the badge where they read the release box.

F7 · Documents. docs/CONSOLE.md (an S09-F note and the issue panel's sentences), docs/STUDIO.md (the two studios' region tables, deviations 3
and 6, the notes on 打开 and 发布此版本), docs/UX_GAP.md, docs/TEST_SCENARIOS.md, the manual's text (`docs/manual/index.html`: the captions,
the glossary, C13 and C15; the screenshots are not retaken here — the figures that show a designer's bar or dialog are stale and say so in
their captions where the dialog is the subject: 09, 12–15, 24–29, 31, 35, 38). ADR 0014's slice list marks S09-F done; ADR 0017's Progress
line names it.
