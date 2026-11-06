# S09-C the console speaks the user's words: page names, the data chip, field types, one-sentence notes

2026-11-06. ADR 0014 decision 7, on top of S09-B. Pages, three scripts and the stylesheets; no request, route or id changes.
Decision 7 is larger than this slice: the parts that need new facts from the source (a dataset's name in the catalogue) or
a shared component (fewer version words on the layout designer, S09-E) wait for them; what follows is what the console can
say today without inventing anything.

C1 · One name for the page. The header's label `#shell-label` is the page's name (报表工作台, 看板工作室, 模板中心, 文档工作室,
任务工作区, 能力与环境; 报表工作区 before a page is chosen) instead of the prototype's English small caps (REPORT WORKBENCH …);
`app.js` writes `pages[page]`, the same words as `document.title` and the rail's `aria-label`. The English eyebrows that
repeated the label above the h1 (REPORT WORKBENCH on the workbench, BACKGROUND TASKS, PLATFORM CAPABILITIES, DOCUMENT
OPERATIONS over the template centre's title, SIGN IN on the connection card) are gone: a title block is the title and its
lead. The studios' own wordmarks, hidden in the console since S07-F, say 文档工作室 / 看板工作室 / 模板中心 so that the embed
page and the console agree. The brand's tagline on the connection card (DOCUMENT PLATFORM) stays: it is the brand, not a
label. `.wordmark small` is 11px with a little spacing (the wide uppercase spacing stays for the tagline only).

C2 · The data chip names the dataset. `#sh-data-text` reads `应收余额 · company_demo · 2026-11-06` instead of
`receivables.aging@1.0.0 · …`: the name is the catalogue entry's title the workbench already shows in its dataset list
(`datasetTitle`: the labels of the numeric columns, the id when there are none); the technical `id@version` moves to the chip's
`title` with the kind. The studios' dataset pills (`st-page-dataset`, `bd-side-dataset`, `bd-insp-dataset`) keep the id: they
are the binding, shown as metadata.

C3 · Field types in words. The data drawer's column table says 文本 / 小数 / 整数 / 日期 / 时间 / 是/否 for the catalogue's
string / decimal / integer / date / time / boolean, with the raw type as the cell's `title`; an unknown type is shown as it comes.

C4 · One sentence where a paragraph stood. The layout designer's two paragraphs (what to do first, where the server saves,
what publishing does not share) become one sentence each: 选择列、顺序与对齐；布局只影响屏幕显示，Excel 仍导出完整结果。 and
发布只固定你自己的布局版本，不共享数据；修改草稿不改变已发布版本。 The document studio inspector's second standing paragraph becomes
要换样本数据，在“参数与筛选”里重新冻结快照；模板文件不含业务数据。 The dashboard inspector's 范围 note becomes 指标卡是全范围的服务端合计，
表格与柱状图只是当前页；看板不改变查询权限，发布版本只属于你自己。 Nothing the sentences dropped was wrong; it was the
architecture explaining itself where the user wanted to act, and the manual (`/console/manual/`) keeps the long form.

Left for later, with the reason: the company scope stays typed (decision 8, a contract); the task list keeps the job id as a
column (it is the one handle a person can quote to an administrator, and the restore field takes it); product connections and
currency policies are shown by their ids because the catalogue has no other name for them (the next source onboarding adds
names, docs/REAL_SOURCE_ONBOARDING.md); 版本 · 保存版本 · 发布 on the report layout wait for S09-E's shared component.

Measurement. The task baseline's numbers do not change (31 / 9 / 0 / 0). The held legibility numbers do not change.

Tests and evidence. DOM: S06-A (the studio's wordmark), S06-B C1 (the centre's wordmark, no eyebrow), S06-C C1 (the label is the
page's name, no eyebrow in the title block), S07-A (the chip's text and title), S07-F (the labels), S07-H (the field types);
the browser script's checks of the labels and the drawer's types. The manual's text and screenshots follow in the screenshot
refresh (`scripts/manual_screens.mjs`).
