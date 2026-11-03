# S06-A the document studio shell

2026-11-02. ADR 0008 decides that the console follows the prototype `IDP_Reporting_Designer_UIUX_V3.0.html`
(the file lives outside the repository; docs/UX_GAP.md measures the gap with screenshots in `evidence/ux-gap/`). This
slice turns the console's documents page into the prototype's full-screen studio: the same shell, the same panels,
the same labels and states, driven by the functions `console/assets/documents.js` and `canvas.js` already have. The
behaviour layer is not rewritten; the markup and the stylesheet are. Fidelity rule (ADR 0008 §1): at 1440 px the
studio agrees with the prototype in structure, labels, states and spacing; every deviation is listed in
`docs/STUDIO.md` with its reason, and nothing deviates for taste.

A1 · The shell. When the documents page is open the console's left navigation and top bar give way to the studio
(the other pages keep their shell until S06-C); the studio fills the viewport, scrolls only inside its own regions,
and keeps the console's token-in-memory session, CSP (no inline script or style; positions through CSSOM and SVG
attributes), no `innerHTML` and no third-party code. Transplant the prototype's stylesheet (its tokens, layout
classes, icon symbols as inline SVG `<symbol>`s referenced by `<use>`) into `console/assets/studio.css` and the
markup into the documents section of `index.html` (the embed page shares that section byte-for-byte, as the
existing test requires; the embed mode hides the wordmark crumb and nothing else). Regions, in the prototype's order
and with its labels:

- **Topbar:** wordmark "IDP REPORTING · DOCUMENT STUDIO"; crumb "模板中心 ›" (in this slice it opens the template
  chooser modal of A6; S06-B replaces it with the template centre page); the template name, editable in place
  (a new optional `name` of at most 80 characters in the template document, schema 1.x and 2.x, validated by
  `doctemplates` and the console alike, shared cases updated; a template without a name shows "模板 <id 前八位>"); the
  state badge (草稿 / 已保存 v{n} / 已发布 v{n} / 已发布 v{n}·有未保存修改, from the designer's existing state); the
  help button (the existing help text in a dialog); 版本 (A5); 保存版本; 预览与导出 (the server's PDF preview; the
  download of the preview); 发布 (the server's publication, with the 发布前确认 dialog of A5).
- **Ribbon:** tabs 设计 / 插入 / 数据 / 页面 / 检查与发布 with `role="tablist"`, one tool row each, the labels and
  grouping of the prototype (read them from the prototype's `ribbonTools` definitions and reproduce the order and the
  separators), "搜索命令 ⌘K" on the right opening the existing command palette, and the autosave/status text slot
  ("本地版本与面板一致" becomes the server-state text: 未保存的修改 / 已保存 / 已发布). Mapping:
  设计: 撤销, 重做 (existing); 复制, 剪切, 粘贴 (new: a clipboard of elements inside the template, paste puts the copy at
  the nearest free place in the current band; cut = copy + delete), 格式刷 (new: copies font size, alignment, bold and
  wrap from the selection to the next clicked element, one shot); 左对齐 … 底对齐, 水平等距, 垂直等距 (existing); 组合,
  解组 (disabled: "版式 schema 没有分组对象；多选可一起移动"); 锁定 (new, session only: a locked element is not moved or
  resized by the pointer or the keys until unlocked; shown in the structure panel); 网格 (new: a 10 mm grid overlay on
  the page, toggle); 吸附 (existing snapping, toggle); 区段 (toggle the band labels and separators); 删除 (existing).
  插入: 文本, 绑定字段, 动态明细表, 分隔线, 矩形容器, 图片/标识, 条形码, 二维码, 页码信息 (existing adds, into the current
  band at the nearest free place); 计算指标 and 金额摘要 (enabled only as "分组合计", the server's group_sum; the
  button says so in its tooltip); 签章栏 (inserts a rectangle with the literal "签章" and a tooltip "签章与签名属于后续
  阶段"); 水印 (disabled: "正式文件不支持水印").
  数据: the prototype's data tools (绑定数据集 → the query panel drawer of A3; 参数与筛选 → the same; 查看数据 → the
  snapshot rows drawer; 刷新示例 → re-run the snapshot query is **not** done here: the snapshot is frozen, say so).
  页面: 纸张与页边距 (A4 fixed, the safe region; a dialog that shows them read-only with the reason), 区段设置 (the
  existing band add/remove/height panel), 套打 (the existing print offset, variables layer and background panel).
  检查与发布: 运行版式检查 (the designer's validation plus the server check without samples), 边界样本 (existing),
  预览与导出, 发布前确认 (A5), 版本与回退 (A5), 导出模板JSON, 导入模板JSON (existing JSON open/save).
- **Tool rail and side panels** (A2), **work area** (A3), **inspector** (A4), **status bar:** left "版式检查通过" /
  "版式检查未通过 · N 项" (the designer's validation state) and "页面 / 未选择组件" or the selection's name; right "毫米定位 /
  0.1 mm" and the data source "冻结快照 <id 前八位> · <dataset>".

A2 · Tool rail (组件, 数据, 结构, 资源, then 模板 and 帮助 at the bottom) switching the side panel:
- 组件库: the prototype's three groups (基础内容: 文本, 绑定字段, 图片/标识, 分隔线, 矩形容器, 签章栏; 数据与结构: 动态明细表
  "流式分页", 计算指标, 金额摘要; 专业单据: 条形码, 二维码, and the rest of the prototype's list) as cards; click inserts
  (as the ribbon's 插入), drag onto the page inserts at the drop point (pointer events, no HTML5 drag API needed);
  cards of unsupported components are disabled with the reason as their tooltip and `aria-disabled`. The dataset card
  at the top shows the snapshot's dataset and row count with 编辑单据数据 → the rows drawer and 输出预览 → PDF preview.
- 数据与字段: the dataset card (id@version, rows, "更换 / 映射" → the query drawer); a search box; 单据级字段 (the group
  fields of a 2.0 template, or "未分组" with a hint) and 明细级字段 (the snapshot's columns) as rows with "+" that adds a
  field text or, for 明细级 when the table is selected, a column.
- 区段与图层: each band (页眉, 报表头, 明细区, 报表尾, 页脚; 1.x: 文本框, 明细表) with its elements in reading order,
  lock and visibility toggles (visibility is session-only: a hidden element is still in the template and is said so in
  the tooltip; it is not hidden in previews), click selects on the canvas and the canvas selection highlights here.
- 资源与样式: the existing background and image upload lists and the font size/alignment presets.
- 模板: the chooser modal of A6. 帮助: the help dialog.

A3 · Work area: the context bar (template name; "<N> 条示例" = the snapshot's row count; 参数与筛选 opens the query
panel in a right drawer, submitting re-freezes a snapshot as the workbench does, the studio then re-binds; 查看数据
opens the snapshot rows in a bottom drawer with the existing pagination), the horizontal ruler, the band labels on the
left of the page (页眉 / 报表头 / 明细区 / 报表尾 / 页脚; 1.x shows 横幅 / 正文), the A4 page centred on a light grey desk
(the prototype's, not the current dark desk), the canvas of `canvas.js` inside it unchanged in behaviour, the bottom
hint "拖拽定位 · Shift 多选 · Space 平移" (Space-drag panning is new), the pager "‹ 1 / N ›" where N is the last preview's
page count (1 until a preview), and the zoom control (−, 适合宽度 ▾ with 50/75/100/150/200 %, +).

A4 · Inspector: title "页面属性 / 纸张、区段与打印设置" or the selected element's kind; tabs 属性 / 规则 / 检查.
属性: for the page, the template card (name, dataset chip, "A4 · 自动分页 · 重复表头"), 编辑业务样本 → rows drawer,
导出JSON样本 → the snapshot sample download (JSON of the first rows, no new route: the console has the rows), 输出预览;
the notes of the prototype's page card are replaced by the designer's own hints (list the texts as a deviation);
纸张与布局 (210 × 297 mm read-only, 纸张与页边距 and 区段设置 buttons). For an element: every key of its kind as the
existing inputs, regrouped as the prototype groups them (位置与尺寸, 内容, 文字, 边框), with the existing validation and
undo. 规则: the template's constraints as the designer knows them (safe region, overlap, 24-box limit, band rules, group
fields) and the last refusal's `detail` with a "在画布上标出" button (the existing outline). 检查: the boundary-sample
results (row, ok, code, reasons) with a 重新检查 button.

A5 · Dialogs: 版本 ("版本历史与发布指针": the server's version list with the published pointer, 预览此版本 (PDF
preview of that version), 恢复到编辑器 (open as unsaved), 撤回发布, 发布此保存版本, the note that versions are
immutable and restore is not publication); 发布前确认 (what will be published: version number, name, dataset, element
count, the validation state; 确认发布 / 取消); 帮助; 纸张与页边距 (read-only). All dialogs are `role="dialog"`,
`aria-modal`, labelled, focus-trapped, Esc closes and restores focus, as the command palette is.

A6 · Template chooser (the crumb and the rail's 模板 in this slice): a modal listing the identity's server templates
(id, name, versions, published version, dataset) with 打开, 打开已发布版本, 新建 (the default template), 导入JSON; it is the
seed of S06-B's template centre and is replaced by it.

A7 · Responsiveness and themes: at 1280 the inspector narrows; at 768 the side panel and the inspector become
drawers opened from the rail and a toolbar button; at 390 the ribbon tools scroll horizontally and the inspector is a
bottom sheet; no horizontal overflow at 390, 768, 1280, 1440. Dark theme: derive dark tokens for the studio (the
prototype has none: a listed deviation), keeping the page white. Keyboard: the ribbon, rail, panels and inspector are
operable by keyboard with the existing canvas shortcuts unchanged; every control has an accessible name; the status
bar's selection text is a polite live region.

A8 · Tests and evidence. DOM tests (through the real Go server, as the existing ones): every ribbon and rail control
either calls the function its old button called (assert on the same outcomes the old tests asserted) or is disabled
with its reason; the clipboard, format painter, lock, grid, pager and zoom; the panels' contents from a real snapshot
and template; the inspector's three tabs; the dialogs' focus rules; the name field's validation and round trip; the
embed section still byte-identical; no `innerHTML`, no inline style attributes, no third-party script (the existing
source tests). The existing documents and canvas DOM tests are adapted to the new markup without weakening their
assertions. Real-browser script `scripts/browser_studio_s06a.mjs` (Windows Chrome over CDP, the pattern of
`browser_canvas_s03l.mjs` and `browser_ux_gap.mjs`): screenshots of the studio in the states of
`evidence/ux-gap/proto-*.png` beside the prototype at 1440, and the studio alone at 1280, 768 and 390, light and dark;
a real session (open, insert from the library and by drag, align, lock, copy/paste, check, preview, save, publish,
versions dialog). `docs/STUDIO.md`: the studio's map (region → function), the deviation list with reasons, the
screenshot index; CONSOLE.md updated; README S06-A at the top with the "以下为此前阶段历史说明：" line moved directly
below it.

Not in this slice: the template centre page, the sample pack and the new-template wizard (S06-B); the other console
pages, the sign-in page and the dashboard studio (S06-C); any server change beyond the optional template `name`;
watermarks, signatures, grouping objects, persistent locks or hidden elements in the schema.
