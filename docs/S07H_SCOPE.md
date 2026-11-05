# S07-H the workbench is the home; the data centre is the data drawer's field list

2026-11-06. ADR 0011 decisions 5–6. Nothing on the server changes: the same catalogue request, the same authorization.

H1 · The home. A sign-in lands on the report workbench. The rail's brand square and the header's wordmark lead there; the rail
loses 概览 and 数据 (six destinations: 报表, 看板, 模板, 文档, 任务, 能力). `#overview` and any unknown address open the
workbench. While the workbench has no result, its empty state shows the overview's three steps (选择已发布数据集, 预览并创建快照,
生成 Excel 并下载); the first result hides them and a cleared result shows them again. The embed page hides them (`embed.css`).
The overview's counters are gone: the task count is the rail's badge, the product connections, the identity and the export
formats are on 能力与环境. The dashboard studio's wordmark and the document studio's 返回 go to the workbench.

H2 · The data centre. Its column table is the data drawer's, below the query panel: 数据集字段 for the dataset chosen in the
panel (the columns with their types, currencies and units; the dataset's id, version, dimensions and metrics; 刷新目录), and it
follows the choice. The data centre page, its product chip row and its cards are gone; the template centre's 数据中心 (the
prototype's label) and the old address `#catalog` open the drawer, over the centre or the workbench. A result that arrives from
the drawer while the centre is on screen updates the centre's cards once the console is idle.

H3 · Measurement. The baseline's `openPage` no longer clicks the page that is already on screen (ADR 0011 decision 7): with
the workbench as the home, T1 no longer pays for reaching it. T7 reads the fields in the drawer and previews from there.
The same script on main (f32e43f) and on this branch:

| | Clicks | Page switches | Dialogs |
|---|---|---|---|
| T1 | 4 → 3 | 1 → 0 | 0 → 0 |
| T7 | 3 → 2 | 2 → 0 | 0 → 1 |
| Totals | 33 → 31 | 12 → 9 | 6 → 7 |

T2 is 8 on both (the new `openPage` saves the click to the workbench that T2 makes after signing in, on main too); the other
tasks and the dead ends (0) are unchanged. The held totals become 31 / 9 / 0 / 0 (`docs/UX_BASELINE_S07.md`,
`evidence/s07a/baseline-main-s07h-script.json`, `evidence/s07a/baseline-after-s07h.json`).

Tests and evidence. DOM S06-C C1 (the workbench after the sign-in, the six destinations, the old addresses, the drawer's field
list with its types and currency, the start steps shown, hidden and shown again, sign-out clearing the list), S07-A, S07-C,
S06-B C4 and part 3, the PDF formats test and the embed page's id list adapted. The real-browser scripts of S06-C (the home, the
drawer's fields, six pages at every width and scheme with contrast) and S06-B (数据中心 opens the drawer over the centre). The
manual's tour, the 数据与字段 section and its screenshots 02, 03, 34 and 36 are new.
