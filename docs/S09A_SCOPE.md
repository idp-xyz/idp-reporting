# S09-A the console is legible: the type scale, one notification stack, nothing shown that does not exist

2026-11-06. ADR 0014 decisions 1, 3, 4, 5 and 6. Nothing on the server changes; no request, id or route changes. The pull request
sits on the Prettier reformat (ADR 0014 decision 9), so that its diff is the change and not the formatting.

A1 · Type scale. `body` stays at 13px (it was 12px); every `font-size` and `font` shorthand below 13px in `styles.css`, `studio.css`,
`library.css` and `embed.css` moves up one step (7, 7.5, 8 and 9px → 10px; 10 → 11; 11 → 12; 12 → 13), 298 declarations by a script
over the formatted stylesheets; sizes at or above 13px stay. The labels of the application rail and of the studios' tool rails are set
to 11px. The canvas drawing (`.cv-*`, the studio's ruler and band labels) keeps its sizes: it draws the paper, not the interface. The
smallest interface text is now 10px, and only the letter-spaced uppercase eyebrows (REPORT WORKBENCH), the badges and the `<small>`
of the wordmark are that small; the dominant size went from 11px to 12px (the template centre: 497 nodes at 11px → 319 at 12px). No
horizontal overflow at 1440, 1366, 1280, 768 or 390px; the real-browser contrast check is unchanged (the colours did not move).

A2 · One notification stack. On the document studio and the dashboard studio the console's `#notice` is appended to the studio's own
toast stack (`#st-toasts`, `#page-dashboard .toast-stack`) when the page opens (`app.js` `placeNotice`, also in the embed page's
`showEmbedView`), and put back at the top of the main column when another page opens. The stacks lay their toasts out in one column
with a gap, so the console's notice and the studio's status toast can no longer cover each other or a button (the manual's
`24-studio.png` and `12-dashboard-widgets.png` showed both). The element, its id, classes and `role=status` are unchanged; the two
`body.studio-on #notice` / `body.board-on #notice` fixed-position rules are replaced by one `.toast-stack > .notice` rule.

A3 · Nothing shown that does not exist. Removed from both pages: the component library's 水印 card, the 插入 ribbon's 水印, the
页面 ribbon's 插入水印, the 数据 ribbon's 数据中心 group (its reason read "属于后续阶段（S06-C）"; the data centre has been the
data drawer since S07-H) and the `#i-watermark` sprite. The `cardRules.watermark` rule in `studio.js` goes with them. Tiles that
exist but are unavailable in the current state (a 1.x template cannot take a line, rectangle, image, code, page number or signature)
stay disabled with their reason, as before. The resources panel's sentence that themes, colours and watermarks are not in the schema
stays: it is true.

A4 · Raw JSON is advanced. The layout designer's 生成布局文件, the 布局 JSON textarea and 打开布局 move into a closed `<details
class="form-panel advanced">` named 高级 · JSON 导入与导出 at the end of the designer; `#layout-status` stays visible above it. Ids
and functions are unchanged. The document studio's 模板JSON was already a dialog and the dashboard studio's 看板 JSON the inspector's
文件 tab.

A5 · A disabled export says why. While a preview is on screen and no snapshot exists, 导出 Excel and 导出 PDF carry the title
导出需要冻结快照：先创建快照 and the same sentence stands beside them (`#export-hint`); with a snapshot, or before any result, nothing
is said (`app.js` `exportHint`, called wherever the buttons' disabled state is set).

Measurement. The task baseline's numbers (31 / 9 / 0 / 0) do not change: no step of T1–T7 touches what moved. The held font-size and
content-start numbers of ADR 0014 decision 10 come with S09-B, which changes the chrome.

Tests and evidence. DOM S06-A A1/A2 and the ribbon inventory lose their watermark and 数据中心 expectations and gain "no watermark
card, no 数据中心 tool"; everything else passes unchanged (213 tests). `docs/UX_GAP.md`, `docs/STUDIO.md` rows 7–8 and the manual's
插入 caption say what is shown now. Screenshots before and after at 1440 × 900 were compared by eye; the manual's screenshots are
refreshed with S09-B, which changes the chrome again.
