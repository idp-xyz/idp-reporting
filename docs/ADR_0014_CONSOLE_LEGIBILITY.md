# ADR 0014 · A legible console: type scale, chrome height, one notice stack, plain words, formatted code

Status: Accepted 2026-11-06. The owner delegated this decision to the platform's engineering on 2026-11-06 ("你来自决"); it
may be revisited when real designers have used the console. The three questions it left open were delegated the same
evening and are decided below (decisions 11–13). Amends ADR 0008 decision 1 for the type scale only; ADR 0009,
0010 and 0011 stay in force. Implementation: a formatting chore, then S09-A to S09-E (the S08 series is reserved by the
proposed ADR 0012 and 0013).

## Context

Every number below was measured on 2026-11-06 on main 52ba843, with the demo (`cmd/reporting-demo`, contract simulator) at
1440 × 900 in headless Chromium, unless another source is named.

**Where the console stands.** Since ADR 0008 (2026-11-02) the console follows the user's prototype
`IDP_Reporting_Designer_UIUX_V3.0.html`: its tokens, type scale, spacing and layout, transplanted in S06-A to S06-C and kept
under the fidelity rule through S07-A to S07-H. The owner's judgement on 2026-11-06 is that the result is not attractive, is
hard to use and is not what people are used to, and that the front-end code is untidy. The task baseline
(docs/UX_BASELINE_S07.md, held in CI by `scripts/ci_browser_evidence.sh` at 31 clicks, 9 page switches, 0 shell changes and
0 dead ends) counts clicks, page switches, shell switches, dialogs and dead ends; it does not measure legibility or
comprehension, and says so. The facts below are what such a measure would have shown.

**Type scale.** `console/assets/styles.css` sets `body{font:12px/1.55 var(--font)}` (line 27). The distribution of font
sizes over the visible text nodes: on the template centre 497 nodes at 11 px, 61 at 13 px and 25 at 12 px; on the document
studio 85 nodes at 9 px, 41 at 11 px, 28 at 10 px and 2 at 8 px. The eyebrow under the wordmark is 7 px and the rail labels
are 9 px, and the rail's 帮助 label, a `<small>` inside 10 px text, is 8.3 px. The four stylesheets declare a size below
13 px 315 times, 17 of them on the canvas drawing (rulers, tags, band labels), which is the paper and not the interface.
Chinese text below 11 px is not comfortably readable; 12 to 14 px is the convention of Chinese business software (a 14 px
body is the default of the common design systems).

**Chrome before content.** The application rail is 56 px wide (`--app-rail`); the console header is 68 px (`--shell-head`,
ADR 0010); the document studio's topbar is 64 px, then the ribbon's tab row, its tool row and a canvas context bar of 38 px.
The canvas begins at y = 274 px, which is 30 % of 900 px and 36 % of the 768 px of a 1366 × 768 laptop. The workbench's
query panel begins at y = 196 px.

**Repetition, and English in a Chinese interface.** On the workbench the page's name appears three times (the wordmark
label REPORT WORKBENCH, the eyebrow REPORT WORKBENCH, the h1 报表工作台), plus the rail label 报表. English small-caps labels
run through the interface (SIGN IN, DOCUMENT OPERATIONS).

**Identifiers as primary text.** The header's data chip reads `receivables.aging@1.0.0 · company_demo · 2026-11-06`;
products are `brant-contract-demo`; the currency policy is `ledger_base @ 1.0.0`; the task list's identifying column is a
32-hex job id; field types print as `string` and `decimal`.

**Things that do not exist.** The component library's 水印 card, the 插入 ribbon's 水印 and the 页面 ribbon's 插入水印 are
greyed for good (docs/UX_GAP.md: not implemented, greyed with a note; docs/STUDIO.md deviations 7 and 8), and the 数据
ribbon's 数据中心 group is greyed with the reason 属于后续阶段（S06-C）, four slices after the data centre became the data
drawer (S07-H). They stand beside tiles that are disabled only for a state reason — 图片 / 标识, 分隔线, 矩形容器, 条形码,
二维码, 页码信息 and 签章栏 need a 2.x template — and beside tiles whose names promise more than they do (计算指标 and 金额摘要
are both the server's 分组合计; 签章栏 is a rectangle with the word 签章). The three kinds look the same.

**Two notification mechanisms.** The workbench has the banner `#notice` at the top of the page; the studios have toasts at
the bottom. In the manual's screenshots (`docs/manual/img/24-studio.png`, `docs/manual/img/12-dashboard-widgets.png`) toasts
overlap each other and a button.

**Prose as the way to teach invariants.** `index.html` has 23 note blocks and 7 help paragraphs. The studio's inspector
keeps two standing paragraphs (画布显示版面，不显示数据；PDF 预览以冻结快照为准。 and 样本是冻结快照的行…, docs/STUDIO.md deviation
12). The export buttons are disabled until a snapshot exists and do not say why.

**Version machinery everywhere, raw JSON in the open.** 版本 · 保存版本 · 发布 · 发布指针 · 修订 · 撤回, with a version
dialog of up to seven actions (ADR 0011), applies even to the report layout, which is only column order and alignment. The
report layout designer keeps a raw JSON textarea (`#layout-json`) with its open and save buttons in the middle of its panel;
the document studio's (`#doc-json`) is a dialog and the dashboard's (`#board-json`) the inspector's 文件 tab.

**The company scope is typed.** The company scope is a text input of ids separated by commas. There is no contract for
listing the companies an identity may query: the source adapter's four operations (docs/s00/INTERFACES.md,
docs/REAL_SOURCE_ONBOARDING.md) describe the catalogue and authorize each read against the current rights, and the source
product is the authority over what an identity may see (ADR 0003 states the same rule for issue).

**Code.** `app.js` has a 2,691-character line (line 35) and 106 of its 687 lines are over 200 characters; `documents.js`
has a 2,484-character line; `index.html` is 169 lines averaging 617 characters, with one line of 23,255 characters, 638 ids
and 289 buttons. `eslint.config.mjs` says, in its own words, that reformatting the dense code is a separate decision. The
scripts are classic scripts wired through 11 `window.*` factories in load order; the DOM tests (`console/dom.test.mjs`)
evaluate them with the JSDOM window's `eval` in that order, so ES modules would need a new harness. The versions-and-publish
flow exists three times (`app.js` for layouts, `studio.js`, `board.js`); 43 of the 50 class names that `styles.css` styles
under `#page-dashboard` repeat class names of `studio.css`; 49 `current(m)` staleness checks are written by hand.

**What is good and must be kept.** The CSP with no inline style or script, no runtime dependency, keyboard operation, focus
management, contrast of at least 4.5:1 checked in CI (`scripts/browser_console_s06c.mjs`), the dark scheme, refusals that
name their place, and zero dead ends (docs/UX_BASELINE_S07.md).

## Decision

1. **Type scale.** The smallest visible interface text is 11 px (captions, help lines, table meta); only letter-spaced
   uppercase eyebrows, badges, the labels of the application rail and the studios' tool rails and keyboard hints may be
   10 px, and nothing is smaller; body text is 13 px and secondary text 12 px; the h1 stays 22 px. Every size below 13 px
   in the four stylesheets (`styles.css`, `studio.css`, `library.css`, `embed.css`) moves up one step: 7, 7.5, 8 and 9 → 10,
   10 → 11, 11 → 12, 12 → 13; sizes at or above 13 px stay; the rail's and tool rails' labels are set to 11 px; `small` gets
   a 12 px base size so that no `<small>` inherits a fraction of a small size. The canvas drawing (`.cv-*`, the studio's
   ruler and band labels) keeps its sizes: it draws the paper, not the interface. The prototype's tokens otherwise stay:
   ADR 0008 decision 1 is amended for the type scale only. A one-step shift keeps the prototype's hierarchy while moving
   its floor, and it is a deviation on measured evidence, which the fidelity rule allows as it allows accessibility rules,
   not a deviation for taste. Fixed panel widths may grow by at most 24 px where text wraps badly (none needed to).
2. **Chrome height.** The console header goes from 68 to 56 px and the studios' topbar from 64 to 52 px; the ribbon's tab
   row and tool row are tightened (30 and 44 px) and the context bars go from 38 to 34 px; the canvas must begin at or
   above 25 % of a 900 px viewport (≤ 225 px). ADR 0010's one shell stays; nothing is merged or removed, because the
   measured cost is in the height of each row and not in their number. The real-browser geometry checks
   (`scripts/browser_console_s06c.mjs`, `scripts/browser_library_s06b.mjs`) are updated in the same pull request.
3. **One notification stack.** On a studio page the console's notice `#notice` joins the studio's own toast stack
   (`#st-toasts`, `#page-dashboard .toast-stack`), where the toasts are laid out in one column and cannot overlap; on the
   other pages it stays the banner at the top of the main column, where a page without toasts has one place to look. The
   studios' status toasts keep dismissing themselves after a few seconds; the console's notice keeps standing until the
   next one replaces it, since it carries the server's answers; every one of them is a `role=status` region. The element,
   its id, its classes and the texts the tests assert do not change.
4. **Nothing is shown that does not exist.** Tiles and ribbon items with no implementation — 水印, 插入水印 and the 数据中心
   group — are removed from the DOM. An item that exists but is unavailable in the current state (a 1.x template cannot
   take an image) stays disabled and says why in its `title` and `aria-disabled`; an item whose name promises more than it
   does (计算指标, 金额摘要, 签章栏) keeps saying what it does in its `title` until S09-C renames it. A greyed tile promises a
   feature; a missing one promises nothing. docs/UX_GAP.md's component table is the list of what exists.
5. **Raw JSON is advanced.** The layout designer's JSON textarea and its open and save buttons move into a closed
   `<details>` named 高级 · JSON 导入与导出 at the end of the panel; the functions and ids are unchanged; the baseline tasks
   do not touch them. The document studio's JSON is already a dialog and the dashboard's the inspector's 文件 tab, which
   is the same rule. They stay the import and export path and the tests' way in, but they are not a designer's daily view.
6. **A disabled primary action says why.** The export buttons carry a `title` and a visible one-line help
   (导出需要冻结快照：先创建快照) while a preview is on screen and no snapshot exists; 发布 and 正式出具 already answer a click
   with what is missing (S07-G) and keep doing so. A button that is grey without a reason is a dead end that the baseline
   does not count.
7. **Words** (for S09-C, decided now). The primary label of a thing is its name in the user's words, never an identifier;
   identifiers, hashes and versions are secondary text or a copy action. English small-caps labels are removed except the
   brand: the `#shell-label` page label becomes the Chinese page name in the header and the eyebrow is dropped. Standing
   explanatory paragraphs become one sentence or a help link to `/console/manual/`. The data chip reads
   `应收余额 · company_demo · 2026-11-06`, with the dataset's technical id in its `title`.
8. **The company scope needs a contract.** A picker needs the source product to list the companies an identity may
   query; that is a contract amendment, an optional capability of the source adapter in the manner of ADR 0003's
   `DocumentAuthority`, proposed as a later ADR. Until then the text input stays and remembers nothing, so that the
   console never shows a company the source has not confirmed for this identity.
9. **Code.** Prettier, pinned in `package.json`, formats `console/**/*.{js,mjs,css}`, `scripts/*.mjs` and
   `eslint.config.mjs`; `npm run format:check` runs in CI's console job (`.github/workflows/ci.yml`); the reformat is one
   commit listed in a new `.git-blame-ignore-revs`. The pages (`index.html`, `embed.html`, `embed-host/index.html`) are not
   formatted: a formatter wraps text at spaces, the DOM tests compare `textContent` byte for byte and the embed page must be
   the console's own markup byte for byte (the first attempt broke 图片 / 标识). The preset table in `canvas.js` stays JSON
   under a `prettier-ignore`, because a Go test parses it. Tests that read a stylesheet's or a script's source for a rule
   in its compact form allow the formatter's whitespace; they test the same thing. Then, as later slices: one shared
   versions-and-publish component replaces the three copies; the dashboard studio's `#page-dashboard` block in `styles.css`
   shares the studio's rules instead of repeating them; a small declarative checker replaces the boolean chains that
   validate server shapes. ES modules are not adopted now, because the test harness evaluates classic scripts; splitting
   `index.html` waits for server-side composition.
10. **Measuring.** docs/UX_BASELINE_S07.md gains two held numbers alongside the clicks: the smallest font size of visible
    interface text (≥ 11 px; ≥ 10 px inside eyebrows, badges, rail labels and keyboard hints; the canvas drawing excluded)
    and the y at which the main content begins on each shell (≤ 225 px at 1440 × 900: the workbench's query panel, the
    studio's canvas below its context bar, the dashboard's desk or start card), measured by `legibility()` in
    `scripts/browser_console_s06c.mjs` in the same CI job as the clicks. Like the click counts they move one way only,
    towards legibility: the smallest size may rise and the content's y may fall, and neither goes the other way without
    the baseline document saying so.

## Consequences

The baseline's click counts (31 / 9 / 0 / 0) do not change in S09-A or S09-B: no step is added or removed; what changes is
what a person can read and where the content starts. Decision 10 exists so that this kind of change shows in the baseline at
all.

S09-C changes many asserted strings in `console/dom.test.mjs` (page labels, the chip's text, the notes and help lines) and
the manual's screenshots under `docs/manual/img/`; the manual's text changes with them. The tests that assert the old ids
keep passing through S09-A and S09-B, since ids and functions do not move (decisions 3 and 5).

Text one step larger is wider. At 1320 px and below the fixed panel widths (the studio's sidebar 244 px and inspector 286 px,
docs/STUDIO.md) may change by up to 24 px and the studio's breakpoints may shift; the 1280, 768 and 390 px screenshots of
the evidence scripts are retaken. The prototype and the console diverge in type scale and chrome; docs/STUDIO.md's deviation
list records each change with this ADR as its reason, so that the deviation lists stay the one place where deviations live.

The console's notice is moved between the main column and a studio's stack by `app.js` (`placeNotice`) when a page opens;
`embed.html` shares the markup (ADR 0006) and does the same in its one view. Removing the phantom tiles makes the component
library smaller than the prototype's; docs/UX_GAP.md's component table says 不再显示 where it said 置灰并说明, and
docs/STUDIO.md's deviations 7 and 8 say which tiles remain disabled and why.

Decision 7 needs a name for each dataset. The contract simulator names columns (客户, 账龄区间, 应收余额 in
`adapters/contractsim/financial.go`) but not datasets, and the 数据目录 row of docs/REAL_SOURCE_ONBOARDING.md does not ask for
one; S09-C takes the metric's label where no name exists, and the next onboarding adds a dataset name to the catalogue facts.

The Prettier reformat touches every line of the console's scripts once; the chore is reviewed by ESLint and the DOM tests,
not by reading the diff, and `git blame` skips the commit. Until decision 9's later slices land, a fix to the versions and
publish flow must still be made in three places. Decision 8 leaves the company scope as it is: the picker is the one item
of the owner's judgement this ADR does not deliver, and the reason is a contract, not the console.

## Slices

- **S09-A** — type scale, notification stack, phantom tiles, advanced JSON, disabled-action help: decisions 1, 3, 4, 5 and
  6. CSS and small JavaScript; the DOM tests are updated and gain assertions for the stack, the `<details>` and the help lines.
- **S09-B** — chrome heights and the two held numbers: decisions 2 and 10. It changes `styles.css`, `studio.css`,
  `embed.css`, `scripts/browser_console_s06c.mjs`, `scripts/browser_library_s06b.mjs` and docs/UX_BASELINE_S07.md
  together.
- **S09-C** — words: decision 7. The DOM tests' expected texts change with it, and so do the manual's text and screenshots.
- **S09-D** — company picker, after its contract ADR: decisions 8 and 13. ADR 0015 is accepted (2026-11-06); the slice
  starts once the contract simulator and the platform API implement the capability.
- **S09-E** — shared versions/publish component, CSS de-duplication and the declarative checker: decision 9's later part.
  No visible change; the DOM tests are the proof. Part 1 (the component) is done.
- **S09-F** — one save / publish / history pattern: decision 11. After S09-G.
- **S09-G** — 正式出具 as a page of its own, with the register: decision 12. A baseline task T8 comes with it.

The Prettier reformat is a chore before S09-A, not a slice: one pull request with the pinned dependency, the `format` and
`format:check` scripts, the CI step, the formatting itself and `.git-blame-ignore-revs`. Each slice measures the baseline on main before and on
its branch after, as ADR 0011 decision 7 requires; a number that improves becomes the new limit.

## The open questions, decided (2026-11-06, evening)

The three questions this ADR left for the owner were delegated to engineering the same evening ("你帮我决定吧"). They are
decided here as decisions 11 to 13, on the evidence above and on what people know from the tools they already use.

11. **One save / publish / history pattern (S09-F, after S09-G).** Yes. The three things a person does with a design are
    save it, publish it and go back to an earlier one, and every tool they know says so (Save, Publish, History or
    Versions). The console says 版本 · 保存版本 · 发布 · 发布指针 · 修订 · 撤回发布 · 恢复到编辑器 · 发布此保存版本 · 刷新发布状态 ·
    发布当前保存版本 · 打开已发布版本 · 另存为: twelve names for six ideas, and 发布指针 and 修订 are the server's consistency
    model, which a person never needs. The server's model stays exactly as it is (immutable saved versions, one published
    version, a revision count for concurrency, withdrawal). The console presents it as: the bar 保存 · 发布 · 历史, the badge
    as it is; 历史 lists the versions, marks 已发布 and 当前打开, and offers for each 打开 (its content into the editor as
    the draft, which is what 恢复到编辑器 does today, said plainly: 打开后保存会成为下一个版本) and 发布此版本 (a rollback),
    and on the published one 撤回发布; 另存为 becomes 复制为新… in the dialog's footer; 刷新发布状态 goes (the dialog reads
    on open, and a publish that finds the state changed says so and offers 重试); 发布当前保存版本 goes (the bar's 发布 is
    it); 打开已发布版本 goes once S09-G has taken issuing out of the studio, because it was the ritual issuing needed; the
    raw chooser (打开其他…或指定版本) goes under 高级 like the JSON (decision 5). 发布前确认 keeps its rows and says 当前发布
    where it said 发布修订 (the revision becomes a title). Since S09-E this is one component (`versions.js`), the bars'
    labels and the dialogs' markup; the tests, the baseline script and the manual follow the words. The order is S09-G
    first: the studio's 打开已发布模板 exists for issuing, and only when issuing has its own page can 历史's 打开 be one action.
12. **正式出具 is a page of its own (S09-G).** Yes. Issuing is an operator's recurring task; designing is occasional. Today
    the recurring task runs through the design tool (文档 → 版本 → 打开已发布模板 → 检查与发布 → 正式出具 → the row → 正式出具 →
    刷新出具记录: seven steps, two of them the designer's own ritual), and the register (出具记录), which is the audit record,
    lives in a dialog. The rail gets 出具 between 文档 and 任务 (seven destinations; ADR 0011 decision 5's six were the designs
    and the operations the console had then). The page takes the data context from the chip (a frozen snapshot is needed and
    it says so), lists the published templates bound to the current dataset by name, takes the row or the group, previews
    with the PREVIEW banner, issues after a confirmation that names the template, its version, the row or group and the
    product that gives the number, and shows the register as its main table (编号, 状态, 时间, 模板·版本, 快照·行/分组, 摘要与
    页数, 来源确认, 下载). The studio's 检查与发布 keeps 正式出具 as the way to the page with the template preselected, and no
    longer needs 打开已发布模板. The embed's document-designer view keeps today's dialog until an issue view exists (ADR 0006
    names three views; a fourth is its own amendment). A new baseline task T8 (从已发布模板出具一份文件并下载) is measured
    on main before and on the branch after, as ADR 0011 decision 7 requires. ADR 0012 and 0013 (manually entered datasets,
    issuing under the operator's numbers) extend this page, not the studio.
13. **The company picker is worth the contract (ADR 0015; then S09-D).** Yes. Typing company ids separated by commas is the
    largest usability defect left after S09-C: a person does not know ids, and a wrong one is refused only when the query
    runs. The console must not invent the list (docs/s00/INTERFACES.md: `company_ids` are selectors and authorization is
    the source's), so the list comes from the source as an optional adapter capability in the manner of ADR 0003's
    `DocumentAuthority`: which companies the principal may currently choose, with their names, and whether the list is
    complete. It is advisory; every read is authorized by `PrepareRead` and `AuthorizeCurrent` exactly as today, and an
    adapter without the capability is unchanged (the console keeps the text input for it). docs/ADR_0015_COMPANY_DIRECTORY.md
    states the contract; S09-D builds the picker (names shown, ids secondary, nothing remembered across sessions) once the
    contract simulator implements it. T1's second typed text becomes a click; S09-D measures it.

Order of the remaining work: S09-G, then S09-F; S09-D as soon as ADR 0015's capability exists in the contract simulator
and the platform API, which is Go work before console work. The rest of S09-E (the dashboard studio's repeated CSS block,
the declarative checker) has no visible effect and goes last.
