# S09-G 正式出具 is a page of its own, with the register

2026-11-11 (begun 2026-11-06 on the main of that day, then brought onto the main after S19-A). ADR 0014 decision 12, with the
baseline task T8 that decision asks for. The server is untouched: the page sends the requests the document studio's dialog sent
(`POST /issues` or `POST /team/issues` with an Idempotency-Key, the job polled, `GET /issues?limit=50`, the artifact downloaded under
its number after the digest check), through the same ports in `app.js`. The embed page has no pages and keeps the studio's dialog
(ADR 0006 names three views; an issue view is its own amendment).

G1 · The page. The rail gets 出具 between 文档 and 任务 (`#page-issue`, `console/assets/issue.js`, `window.createReportingIssuePage`).
It takes the data context the console holds: the chip's summary and 选择数据… open the data drawer, and the page says 出具需要冻结快照
until a snapshot exists. It lists the published templates bound to the current dataset by name, the id as secondary text, a radio
list; one template is picked at once, the only one by itself. The row is the frozen snapshot's row number; a 2.x template is issued
for the group that holds it, as the studio did. PDF 预览 renders the picked template on the row; 正式出具 opens 出具前确认, a dialog of
the shell (`#sh-d-issue`: the template and its published version, its space, the data, the snapshot, the row or group, and that the
number is the product's), and 确认出具 issues. The register (出具记录) is the page's main table: the same checks and rows as the studio's
dialog (`documents.js` exports them as `issueRecords`), read when the page opens and again after each issue; 下载 downloads under the
number.

G1a · Both template spaces (S12-C, ADR 0018 decisions 2 and 3). The page reads `GET /document-templates` and `GET /team/document-templates`
and lists the published templates of both, each card saying 我的 or 团队; a picked template's published version is read through the
routes of its space, and so are its preview and its issue (`/team/document-templates/{id}/published`, `/team/document-templates/{id}/previews`,
`POST /team/issues`). The route is the one port the studio uses, `window.reportingTemplateSpace.path(path, space)`, which now takes the
space to name when it is not the one the console works in; the studio's issue port (`issue(body, banded, then, space)` → `createIssue` →
`submit`) carries it, and a pending submission's fingerprint includes the route, so that one idempotency key never names two requests.
The studio's way in (G2) passes the space it works in with the template.

G1b · The issuer's role (S12-A2, ADR 0018 decision 1). The rail's 出具 carries `needs-issuer` and the page's 正式出具 button too; the
stylesheet's rule hides them under `body.no-issuer`, the mechanism the administrator's 管理 uses. The page, reached by its address
(`#issue`) without the role, says 当前角色不能出具 (the role, who grants it, and that the register stays readable), offers nothing to press,
and reads the register. The server's refusal stays the authority.

G1c · English (S17-A). Every Chinese text of the page, the dialog, the rail item, the studio's tool title and the notices has its entry
in `console/assets/en.js` (53 entries); `scripts/i18n_extract.mjs` lists `issue.js` among the scripts it reads, `console/i18n.test.mjs`
loads it and visits the page in English, and `scripts/browser_english_s17a.mjs` measures it in a real Chrome.

G2 · The studio. 检查与发布 › 正式出具 leads to the page with the open template preselected, in its space (`api.issuePage`, through
`documents.js` to `app.js`); its title says so. Without the page (the embed, or the page removed) the studio's dialog opens as before,
so the embed page is unchanged and S06-A A5 still exercises the dialog. The studio's 打开已发布模板 stays until S09-F.

G3 · Measurement (ADR 0011 decision 7). T8 — 冻结快照 → 用已发布模板出具 → 下载 — is the eighth task of `scripts/ux_baseline_s07.mjs`'s
core set (the team set of S12-D keeps its own T8–T10) and was measured with the same script on main (5620490, after S19-A) and on this
branch, on 2026-11-11. The precondition, a published 1.x template of the dataset, is made through the API and not counted (T3's copied
sample is a banded 2.0 template whose values the renderer refuses to print). On main the task runs through the design tool and is
refused once (打开已发布模板 first): 12 clicks, 2 page switches, 3 dialogs, 1 dead end. With the page: 5 clicks (创建快照, 出具, the
template, 正式出具, 确认出具), 1 page switch, 1 dialog, 0 dead ends. T1–T7 are the same in both runs (30 clicks, 9 page switches). The
held totals become 35 / 10 / 0 / 0 (`scripts/ci_browser_evidence.sh`, docs/UX_BASELINE_S07.md "T8：出具"), the difference from T1–T7
being T8's own 5 clicks and 1 page switch. Evidence: `evidence/s07a/baseline-t8-main-s09g.json`, `evidence/s07a/baseline-after-s09g.json`.

Tests and evidence. DOM S09-G G1 (the rail with 出具 after 文档, the empty state and its reason, the drawer from the page, the templates of
both spaces by name with an unpublished one left out and each card's space, 出具前确认 and its rows, the issue through one `POST /issues`,
the download under the number, the register, a team template read and issued through `/team/…`, the way in from the studio with the
template preselected in its space, sign-out clearing the page, and a session without the issuer role: `no-issuer`, the page's refusal,
the register still read); S06-C C1 and S07-A A1 list the rail with 出具; S06-A A5 opens the studio's dialog as the embed does. The
real-browser run (console with the page among the pages checked at every width and scheme, library, baseline with T8, the team
baseline, English with the page) passes locally. `console/handler.go` serves `/console/issue.js`; `handler_test.go`, `embed_test.go`,
the DOM harness, the i18n suite, ESLint and CI's syntax step list it; the embed suite lists the page's ids as console-only.
