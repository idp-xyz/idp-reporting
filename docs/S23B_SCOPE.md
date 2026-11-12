# S23-B · The layout designer formats the table report, and the exports carry the layout

2026-11-12. ADR 0024 (docs/ADR_0024_TABLE_REPORT_FORMATTING.md) decided that a layout is the table report's presentation and that the
exports carry it; S23-A (docs/S23A_SCOPE.md) built the server side: layouts 1.1, `export/present`, typed workbook cells, `POST /exports` with
`layout_id`. This slice is the console's side (decision 5): the layout designer's new controls, the table view that shows what the exports
print, the export buttons that send the saved layout, the embed page's designer view, baseline task T11, the English catalogue. No Go code
changed.

## What

B1 · **`console/assets/present.js`** (`window.reportingPresent`): the console's copy of the presentation step, standard DOM-free functions
the designer, the table view and the tests share. `format(value, format)` (digit grouping, `minus` / `parentheses`, `digits` / `dash`) and
`groupDigits` work on the exact string and never on a JavaScript number; `sum(values, scale)` adds exact decimals as scaled `BigInt`s and
prints the result at the longest scale seen; `compare(a, b)` is the exact decimal comparison; `holds(mark, value, numeric)` is a mark rule's
test; `summed(column, query)` is the one place that says which columns a subtotal row sums (today: a numeric column that is a metric of the
query, as `export/present` sums every numeric metric a subtotal shows; ADR 0028 will narrow it to `aggregation: sum` here and there);
`effectiveFormat(column, entry, query)` is a column's format with the defaults filled in (a metric without a format is grouped, a dimension is
shown as it is, as S23-A reads the ADR); `validate(layout, result, query)` is the server's rule book in the console's words (a `Refusal` with
the rule); `uses11(layout)` says whether a document needs schema 1.1; `build(result, query, layout)` is the presented table: the columns in
the layout's order with label, header title, alignment and format, the rows with each cell's exact value, formatted text and mark, the 小计
rows after each group (groups by the exact strings of the subtotal column, in first-appearance order, the snapshot's row order kept inside;
the 小计 label in the first shown column that is not a summed metric; the first rule that holds colours a cell; subtotal rows are never
marked), the title and the footnote. The file is served at `/console/present.js` and embedded like `companies.js` (`console/handler.go`,
`console/embed_test.go`, `console/handler_test.go`, the `node --check` list of `.github/workflows/ci.yml`, `scripts/i18n_extract.mjs`,
`console/i18n.test.mjs`); `index.html` and `embed.html` load it before `companies.js`.

B2 · **The designer's controls** (`console/assets/index.html`, `embed.html`, `app.js` `renderDesigner`), in the same panel as the column order,
now titled 报表布局设计器 · 列、格式与小计: a 标题 (120) and a 脚注 (400) above the columns; per column the checkbox, 上移, the alignment
(左对齐 / 居中 / 右对齐), a 列标签 field (empty = the dataset's label) and, on a `decimal` or `integer` column, the 格式 (千分位 on or off,
负数 负号 / 括号, 零 数字 / — 横线); under the columns 小计 (无 or one shown dimension column); 标记规则 with 添加规则 (disabled at eight,
saying so), each rule a 列 (a shown column), a 关系 by word (小于 … 不等于 on a numeric column; 等于 / 不等于 / 为空 on any other), a 值
where the relation takes one, and a 标记 (好 / 注意 / 差 / 备注, each with its swatch) and 移除. Hiding a column drops the subtotal and the
rules on it. What a control cannot express is not offered (no format on a text column, no text column under 小计, no ninth rule); a value
that is not a number on a numeric rule marks the field invalid, holds 保存 and 发布 with the reason (标记规则 N：数值列比较的值必须是一个数（如
0、-12.50）；改正后才能保存。) and is never sent. A layout that uses any of this saves as `schema_version` 1.1; one that uses none of it saves as
1.0 with exactly the keys it had (`normalizeLayout` drops empty texts and labels, an empty `marks`, a `subtotals` of 无, and a format equal to
the column's effective default; a format that differs is written whole, the three keys); an imported or opened document keeps its schema. The
document the designer builds for a layout with every feature is the one test B1 saves and reads back:

```json
{"schema_version": "1.1",
 "binding": {"integration_id": "brant-contract-demo", "dataset": {"id": "receivables.aging", "version": "1.0.0"}},
 "title": "应收账龄报表", "footnote": "金额为账面值，未做汇率换算。",
 "columns": [{"id": "customer.id", "align": "center"}, {"id": "aging.bucket", "align": "left"},
             {"id": "ar.open_balance", "align": "right", "label": "余额",
              "format": {"grouping": false, "negative": "parentheses", "zero": "dash"}}],
 "subtotals": {"by": "customer.id"},
 "marks": [{"column": "ar.open_balance", "when": "lt", "value": "0", "mark": "bad"}]}
```

`applyLayout` (the JSON import, 历史 › 打开, the saved and published versions) runs `present.validate` and refuses with the server's rule in
the console's words (`LayoutRefusal`): the 1.0 fence (1.0 版式没有标题…请把 schema_version 写成 1.1), the lengths, the three format keys, a
format on a non-numeric column, a subtotal column not shown or not a dimension, more than eight rules, and for a rule its column, mark, value
length, the relation against the column's type, a numeric value that is not a number, 为空 with a value, 64 KiB. The server's refusal is the
truth: test B2 sends each refused document straight to `POST /layouts` and reads the 400. Opening a saved 1.1 layout (历史 › 打开, the JSON
under 高级) restores every control (B5).

B3 · **The table view** (`renderTable`): with a 1.1 layout bound to the current dataset the table is `present.build`: the shown columns in
order, the header the label or the dataset's label (the dataset's label, currency and unit as the header's title when a label replaces
them), `th`/`td` aligned by class (`layout-left` / `layout-center` / `layout-right`), numbers in their format with the exact frozen string as
the cell's `title`, a mark as the cell's class (`mark-good` … `mark-note`; the colours are CSS custom properties `--mark-*` with one ink
`--mark-ink`, no inline style), a 小计 row (`tr.layout-subtotal`) after each group, the title (`#result-layout-title`) above the table and the
footnote (`#result-layout-footnote`) under it, and when a subtotal column is set the note 小计按屏幕上显示的行分组计算；导出文件按整个快照分组
(the screen groups the rows it shows — the current page of a snapshot — while the files group the whole snapshot). Without a layout, or
with a 1.0 layout, the table is exactly as today (raw strings, columns and alignments of the layout). The source's 合计 stays the result's
evidence line; the platform shows no grand total of its own.

B4 · **The exports carry the layout** (`exportWithLayout`, `createExport`, `checkJob`, `renderJobs`). The two buttons decide from the saved
layout (`layoutSaved()`, bound to the current dataset) and the designer's state (`layoutDirty()`, `defaultLayout()`):

| state of the designer | 导出 Excel / 导出 PDF |
|---|---|
| a saved layout, no unsaved edits | the export is submitted at once with `layout_id`; the button's title reads 导出使用已保存的布局（版本 N） |
| a saved layout with unsaved edits | the dialog 导出前确认 (`#ly-d-export`, the shell's centred frame like 发布前确认) says 布局有未保存的修改。导出文件使用已保存的版本 N；要让这次修改进入文件，先保存。 and offers 取消, **按已保存的版本导出** (sends the saved id) and **保存后导出** (saves a new version, then exports with it — the manner of 保存并发布); the button's title says 布局有未保存的修改：导出使用已保存的版本 N |
| nothing saved, a layout designed | the same dialog says 布局尚未保存：导出文件不带布局（列、格式与小计都不会打印）。要让它进入文件，先保存。 and offers 取消, **不带布局导出** and **保存后导出** |
| nothing saved, nothing designed | the plain export, as today |

The notice says {Excel/PDF} 导出任务已提交（布局版本 N）。, the job view's `layout {id, version}` is checked (`checkJob` requires it on an export
job that carries one and refuses a job that names another layout), and the tasks row is titled Excel 导出 · 布局版本 N with 布局 <id> · 版本 N as
its tooltip. The server's refusals are told as the page tells other refusals: 422 UNSUPPORTED_COMBINATION → 这个布局不属于当前快照的数据集，导出文
件不能带它；请换一个布局或不带布局导出。 (`LAYOUT_EXPORT_UNSUPPORTED`); 400 → 已保存的布局不再符合数据集的规则（列或类型已变）：请在设计器里调整并保存，再导出。
(`LAYOUT_EXPORT_INVALID`). The idempotency fingerprint of `submit` includes the layout id, so the same export with another layout is another
job. The dialog is documented in docs/CONSOLE.md; the choice (two buttons, no third "export the draft") follows ADR 0024 decision 2: unsaved
edits are not exported, the console says so.

B5 · **The embed page** (`embed.html`, `console/embed.dom.test.mjs`): the report-designer view shares the designer; the designer section,
the dialogs and the table's title, footnote and note are byte for byte the console's (the existing shared-sections test holds it), and the
embed test drives a title into the table and reads the formatted metric.

B6 · **Baseline task T11** 选列、设置格式和小计、保存、导出 Excel (`scripts/ux_baseline_s07.mjs`; T9 is the filters slice's, T10 is reserved):
on T8's snapshot — the workbench, hide 账龄区间, 负数 → 括号, 小计 → 客户, 保存, 导出 Excel; a choice in a list box is one click, as T7's
dataset. It measures **6 clicks, 0 texts, 1 page switch, 0 shells, 0 dialogs, 0 dead ends**; T1–T8 are unchanged (35 / 10 / 0 / 0, texts 3,
dialogs 8) on the base and on the branch (`evidence/s07a/baseline-before-s23b.json`, `baseline-after-s23b.json`). It cannot run on main
(`baseline-t11-main-s23b.json`: the branch's script against the base's demo, T11 times out waiting for the format and subtotal controls).
The held totals of `scripts/ci_browser_evidence.sh` become **41 / 11 / 0 / 0** (35 / 10 plus T11's own); the team set stays 25 / 8 / 0 / 1.
docs/UX_BASELINE_S07.md "T11：格式与小计".

B7 · **Tests.** `console/dom.test.mjs` (S23-B B1–B5, against the real demo server): every control builds the documented 1.1 document and the
server keeps it as saved; a plain layout still saves as 1.0 with the three keys; twelve refusals, each paired with the server's 400 for the
same document sent directly; a malformed mark value holds 保存 and 发布 with the reason; eight rules disable 添加规则; hiding a column drops
its subtotal and rules; the table view against the workbook the server exports for the same snapshot and layout (headers, texts, exact
columns, the 小计 rows equal to the exact sums, the Metadata and Totals sheets; the test reads the zip and the sheet XML itself); the
`sum`/`compare`/`format`/`holds` unit cases (grouping, parentheses, the dash, the 2^53 row); the export dialog's three paths and the plain
export; the 422 and 400 refusals; a saved 1.1 layout opened again from 历史 and from JSON. `console/embed.dom.test.mjs`: the embed view.
`scripts/browser_console_s06c.mjs`: the designer with its controls and the formatted table with its 小计 rows and its marked cell at 1440 and
390, light and dark (`console-layout-designer-*`, `console-layout-table-*`): no horizontal overflow, the contrast of every visible text and
of the marked cell's text ≥ 4.5:1, legibility; its S21-A check now reads a data cell's exact value from the cell's title and skips the 小计
rows. `console/i18n.test.mjs`'s walk covers the new controls; the catalogue has 77 new entries, no missing and no stale key.

B8 · **Docs**: this file; docs/CONSOLE.md (the note at the top); ADR 0024's implementation line; the manual's words on the layout designer
and on exporting (docs/manual/index.html; figures to regenerate below); docs/UX_BASELINE_S07.md. ADR 0017 is not edited.

## Interpretations of ADR 0024 (the console's)

- **The formatted table view is tied to schema 1.1.** A 1.0 layout and the default layout render as today (raw strings, the layout's
  columns, order and alignments); a 1.1 layout shows what the exports print, which includes the default grouping of a metric without a
  format. Grouping a metric on screen for a 1.0 layout would change today's view for every existing layout, and the ADR keeps 1.0 meaning
  what it meant.
- **A format is stored only when it differs from the column's effective default**, and then whole (`grouping`, `negative`, `zero`): a
  layout does not carry the defaults it did not change, and a reader of the JSON sees the whole format where there is one.
- **The schema is computed from the controls on every change** (`uses11`), never typed: the person cannot save a 1.1 document that uses
  nothing of 1.1 or a 1.0 one that does. An imported or opened document keeps its own schema until a control changes it.
- **Empty texts are absent texts.** An empty 标题, 脚注 or 列标签 is the key left out (the server's `omitempty` reads it the same way), so
  clearing a field never produces a refusal for a too-short literal.
- **The screen groups the rows it shows.** A snapshot's page is what the person sees; the subtotal rows on screen sum that page's rows by
  the page's groups, and the note under the table says that the files group the whole snapshot. A page of 25 rows of the demo is the whole
  snapshot, so the DOM tests compare the screen with the workbook row for row.
- **Unsaved edits and the export.** The ADR says unsaved edits are not exported and the console says so; the dialog offers the two honest
  exports (the saved version, or save first) and nothing that would print the draft. With nothing saved the first choice is the plain export,
  named as such (不带布局导出), because the file would carry no layout.
- **The subtotal rule in one place.** `present.summed` is the console's only statement of which columns a 小计 sums; the view, the designer
  and the tests ask it. ADR 0028's restriction to `aggregation: sum` is one line there and one in `export/present`.
- **Which refusals the designer prevents and which it only reports.** A control that cannot express an invalid state does not offer it (the
  format, the subtotal list, the ninth rule, the relation lists); a field that can hold anything (a rule's value on a numeric column) is
  checked and holds 保存; a document that arrives whole (import, 打开) is validated whole and refused with the rule. The server is asked in
  every case that reaches it; the tests pair each client rule with the server's answer.
- **A 1.1 layout of another dataset** is refused by the binding check before the rules, as before.

## Figures of the manual to regenerate

The words of docs/manual/index.html are updated; these figures (`scripts/manual_screens.mjs`) show the designer or the table and should
be taken again: `09-layout-designer.png` (the designer with 标题, 列标签, 格式, 小计 and a 标记规则), `08-workbench-snapshot.png` (the
snapshot's table with a 小计 row and a marked cell once a layout is saved), `07-workbench-preview.png` if the designer is shown open, and
the new `09a-layout-export-confirm.png` (导出前确认) if the manual gets a figure for the dialog.

## Evidence

- `bash /tmp/idp-agent/domtest.sh` (the three jsdom suites with the renderer) and `domtest-nopdf.sh` (without): see the pull request.
- `scripts/ci_browser_evidence.sh`: console 170 checks, library, baseline 41 / 11 / 0 / 0 held, team 25 / 8 / 0 / 1 held, english.
- `node scripts/i18n_extract.mjs`: every Chinese string of the console in the English catalogue, none stale.
- Prettier and ESLint clean; `go test ./console/...` (the embed and handler tests know `present.js`).

## Known

- The format row of a numeric column is wider than a text column's row, so the 上移 and alignment controls do not line up across rows at
  1440; the rows wrap at 390 and stay legible.
- A PDF export with a layout is offered where the server offers PDF; the PDF's look is S23-A's.
- The table view shows 小计 rows for the page it shows; a page size of 2 on the demo's three rows groups two rows and then one, and the files
  group all three. The note says so; a per-page carry (页承) is not shown.
- `present.js` repeats the rules of `layouts.Validate`/`Check` in JavaScript for the designer's immediate answers; the server stays the truth
  and the tests hold the two together on the twelve refusals.
