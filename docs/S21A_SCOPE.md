# S21-A · The dashboard studio draws lines and pies, and a widget has a title and a width

2026-11-11. The dashboard studio (docs/STUDIO.md "看板工作室") had three components: a metric card (a server total over the whole
scope), a detail table (the current page's columns) and a bar chart (the current page's first 50 rows, one bar a row). This slice adds
two charts of the same shape, a line and a pie, and gives every widget an optional title and a width. Nothing about what a dashboard
is changes: it is metadata (a binding and the widgets), the data comes from the current authorised query each time, and the dashboard
computes no new aggregate.

## What

A1 · **The document** (`dashboards/store.go`). Two widget types, `line` and `pie`, each `{type, category, field}` exactly as `bar`: a
column of the dataset and a numeric field, no `columns`. Two optional keys on every widget: `title` (1 to 64 characters, none of them
a control character: `dashboards.ValidTitle`) and `width` (`half` or `full`; absent is half, the grid's column). `Validate` enforces
them and `Widget.UnmarshalJSON` keeps the exact field set: the required keys of the type, optionally `title` and `width` as values
(never `null`), nothing else. Unknown types are still refused; the bounds stay: 12 widgets, 64 KiB as JSON. The route authorises a
line and a pie as it authorises a bar (`transport/httpapi/dashboards.go`: the field numeric and the category a column of the bound
dataset, 403 otherwise). docs/openapi.json describes the two types and the two keys (`DashboardWidget`, `DashboardWidgetTitle`,
`DashboardWidgetWidth`).

A2 · **The line** (`console/assets/dashboard.js` `lineChart`). The current page's first 50 rows in row order: x is the row's index,
y its value on a linear scale that includes zero (from the lowest value or zero to the highest value or zero; all zero puts the line
in the middle), a dashed zero line, the line as an SVG `polyline`, a small circle a row whose tooltip is `第 n 行 · 分类 · 值`. The
note says `当前页 · 前 50 行 · 按行顺序。逐行连线，不分组、不汇总。` and the currency or unit; the caption names the smallest and
the largest value as the literal strings and says the drawing is approximate; the legend below lists every row (`第 n 行 · 分类` and
the exact value). The picture is `role="img"` with an `aria-label` that names the field and the number of points.

A3 · **The pie** (`pieChart`). The current page's first 12 rows, one slice a row; a longer page is said (`当前页有 N 行，只绘制前 12
行。`). The dashboard sums nothing, so a category that repeats is not merged: the widget shows `分类重复，饼图按行显示` and draws one
slice per row. A zero or negative value is listed in the legend as `未绘制` and named in the note (`零或负值不绘制扇区：…`), and is
not a slice; when no row is positive the widget says so and draws nothing. The proportions come from the exact magnitudes (BigInt at
one common scale): each boundary is the cumulative share rounded once to a millionth, so the last slice closes exactly at twelve
o'clock; a slice over half the circle is drawn as two arcs (one arc between the same two points is nothing, and the one slice of a
pie is the whole disc). Numbers are used for the angles alone; every label is the cell's literal string. Colours are the classes
`board-slice-1` … `board-slice-12` (tokens `--slice-1` … `--slice-12`, a light and a dark set in `styles.css`), on the slice and on the
legend's swatch; nothing is an inline style (the page's CSP). The legend lists swatch → `第 n 行 · 分类` → exact value.

A4 · **Title and width** (`render`). Every card's control row has a text input `组件标题` (placeholder `标题（可选）`, `maxlength`
64) and a select `组件宽度` (`半宽` / `全宽`) before the three buttons 上移 / 下移 / 移除, which keep their places (the structure
panel and the older tests click them by index). A title replaces the widget's default heading (`应收余额`, `明细表 · 当前结果页`,
`应收余额 · 当前页柱状图` …) wherever it is shown, the structure panel included; an emptied title brings the default back and
removes the key. `全宽` adds the class `board-wide` (`grid-column: 1 / -1`). Both are plain keys of the widget's JSON: `canonical`
keeps them where present, so they are in the file, saved, published and reopened with the document; `validate` refuses what the
server refuses, with its own words (`组件标题需为 1–64 个字符，且不含控制字符。`, `组件宽度只能是半宽或全宽。`,
`折线图需要当前授权的分类字段和数值字段。`, `饼图需要…`).

A5 · **The studio** (`index.html`, `embed.html` byte for byte, `board.js`). Two tools in the component library after the bar's,
each with its own category and value selects (filled as the bar's are) and a button: `折线图 · 当前页 · 前 50 行 · 按行顺序 ·
添加折线图`, `饼图 · 当前页 · 前 12 行 · 每行一个扇区 · 添加饼图`. `添加指标卡` keeps its place and label (the task baseline's T2
clicks it). The structure panel lists a line and a pie under their headings with the chart icon. The standing texts that
enumerated "表格和柱状图" now say "表格和图表", and the help dialog's limits name the three charts, the title and the width.

A6 · **English** (`en.js`): every new string, appended at the end of the catalog; the six changed texts updated in place.

## Not done

- No aggregation of any kind: a pie of a page with repeated categories is a pie of rows, said as such (ADR 0008 §4, docs/STUDIO.md:
  the dashboard computes no new aggregates). A grouped chart needs a server-side query, which is a different thing.
- Only the current page: a chart draws the page the console holds (50 rows for the line and the bar, 12 for the pie); the server's
  totals are the metric card's. Paging through a snapshot redraws the charts for that page.
- No axis ticks or value labels inside the SVG: the exact values are the legend's literal strings, where the contrast audit measures
  them; text in the picture would be a second, approximate rendering of the same numbers.
- Icons: the sprite has no line or pie glyph; the structure panel uses the chart icon for the three charts.
- The user manual (docs/manual) still shows three components; its screenshots are retaken by `scripts/manual_screens.sh` as a chore.

## Evidence

- Go: `dashboards` (`TestLineAndPieWidgetsAreChartsOfTheBarShape`, `TestWidgetTitleAndWidthRules`,
  `TestTitledAndWideChartsReopenAsSaved`: shapes, 12 refused variants of title and width, the Go-built document, the 12-widget and
  64 KiB bounds, a reopen from the published pointer), `transport/httpapi`
  (`TestLineAndPieChartsAreAuthorizedAsTheBarAndKeepTitleAndWidth`: 201 with the document as written, 403 for a field or category the
  dataset lacks, 400 for a bad title, width, type or shape).
- DOM (`console/dom.test.mjs`, three tests at the end): a line and a pie added from the sidebar and drawn from the real result (points
  and slices per row, tooltips, the zero line, the exact path of the slices, the legend's literal values equal to the result table's
  cells), titled and widened from the card, listed in the structure panel, saved to a file and to the server, published and
  reopened by exact metadata, the emptied title; a pie with a repeated category and a zero and a negative row (the notes, two slices,
  four legend lines, the swatches), a line whose scale includes zero, every row zero or negative, one row, all zero, 60 rows (12 and
  50), a 500-digit value, invalid cells, an empty page, a hostile category as text; the console's refusals of 21 bad documents with
  the reason, five accepted ones, and the server's 400 for each of the 21 and 201 for a good one. `console/i18n.test.mjs` adds the two
  tools to its dashboard.
- Real browser (`scripts/browser_console_s06c.mjs`): the session adds a line and a pie after the three widgets (five on the desk),
  types a title into the line's card and sees it as the heading and in the structure panel, and the contrast, overflow and
  legibility checks at 1440/1280/768/390 in both schemes run with the charts on the desk; the task baseline's totals are unchanged.
