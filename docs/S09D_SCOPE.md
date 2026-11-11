# S09-D The company picker: the source's directory in the console and the embed (ADR 0015 decision 4)

2026-11-11. ADR 0015 (accepted 2026-11-06) gave the console a contract for the one usability defect S09-C could not fix: the 公司范围 field
was a text field of ids. S19-B (2026-11-11) implemented the server side: `reporting.CompanyDirectory`, the simulators, and
`GET /api/reporting/v1/companies?integration_id=…`. This slice is the console side: the field becomes a picker of the companies the
source lists, by name, in the console and in the embed page alike; the query still sends ids; a product without the capability, an
answer that does not verify and a failing route all leave the text field exactly as it was. The server is untouched. ADR 0014 decisions
8 and 13 are closed by it.

D1 · **The picker** (`console/assets/companies.js`, `window.createReportingCompanyPicker(ports)`; `app.js` owns the request). On sign-in
and on each product change `loadProduct` asks the product's directory with its catalogue (`GET /companies?integration_id=…`), once per
product and session (`state.companies`, forgotten at sign-out; 刷新目录 asks again, as it re-reads the catalogue). With `supported: true`
the 公司范围 field is a list of the companies in the product's order, each a checkbox whose label is the **name** with the **id as secondary
text** (`.company-name`, `.company-id`; ADR 0014 decision 7); the field's label 公司范围 names the group (`role=group`, `aria-labelledby`).
A filter (`#query-companies-filter`, name or id, case apart) appears above eight companies. The selection is bounded by the query's
sixteen: the sixteenth chosen disables the rest and a line says why (已选 16 家，达到一次查询的上限；取消一家才能再选其他公司。); one
unchosen frees them. **One listed company is chosen already**: a forced choice is not asked of the person, who may still unmake it
(this is what the demo shows: 演示公司). The data chip names the companies: one name, two names joined with 、, more than two the first
and the count (`演示公司 · 共 3 家`); the technical ids stay off the chip, and the wire carries `company_ids` as before (`buildQuery` reads
`companies.ids()`). The schedule dialog's 数据 line names them the same way. A change of the selection is a change of the query, as a
typed id was (the result is cleared). The data drawer's first focus follows: the company to choose when none is (the filter when there
is one), 查询预览 once one is chosen. Nothing is stored; a new session asks again. Tests: DOM S09-D "with the source's directory…"
(the picker against the real demo: names and ids, the one company chosen, `company_ids: ['company_demo']` on the wire, the chip
应收余额 · 演示公司 · date, the schedule dialog, the drawer's focus, sign-out and a new session), "a partial directory… the sixteenth…
a filter above eight… the chip says the first and the count" (20 companies: the filter on name and id and its 没有匹配的公司。 line, two
chosen and two typed → four ids on the wire with a typed id already chosen sent once, the chip with 共 4 家 and with two names, the bound of
sixteen with its line and its class, a seventeenth refused on the page).

D2 · **Fail closed.** The answer must be exactly the documented shape (ADR 0015 decision 3): the three keys `supported`, `partial`,
`companies` and no other, booleans and an array, at most 1000 entries, each entry exactly `id` and `name` of 1–128 characters of text
(not blank, no control character), no id twice; `supported: false` only with `partial: false` and `[]`. Anything else, and any failure
of the route (the source's 503, a 404 of a server before the route, a 403, a non-JSON answer), leaves the field the text field of ids
(`companies.check` → null, `askCompanies` → null) for the session, without a notice: the catalogue's own refusal already said what there
is to say. The typed path is therefore still there, and the refusal of a typed wrong company is still what it was (docs/manual figure
10: `company_other`, which the demo lists under no grant). Tests: DOM S09-D "without the capability, or when the route fails…" (the
default harness answer `supported: false`, then 503, 404, 403 and `text/html`, each with the text field, the catalogue intact and no
notice, `company_other` refused by the source), "an answer that is not exactly the documented shape…" (19 shapes, each the text field
and no page error; the inclusive bounds, 1000 entries of 128-character ids and names, verify).

D3 · **The partial directory.** With `partial: true` the text field stays under the picker as 输入其他公司 ID，多个以逗号分隔 (its
`aria-label` 输入其他公司 ID) and its ids join the selection after the chosen ones (an id typed and chosen is sent once); the line under
the list says the list is incomplete. An empty list is an answer (ADR 0015 decision 1): complete, the line says 当前身份没有可选择的公司。
and no query can be made; partial, it says 来源没有列出公司；可在下方输入公司 ID。 and the typed line is what is left. Tests: DOM S09-D
"a partial directory…", "an empty directory is an answer…".

D4 · **The embed page** (ADR 0006, ADR 0015 decision 4). `embed.html` loads `companies.js` before `app.js` as `index.html` does; the view
sections stay byte for byte the console's, so the embed's query panel is the same picker, with the same ids on the wire, and the names
cross to the frame and nowhere else: nothing of them goes to the parent page. The embed's DOM harness answers the directory as a product
without it unless a test asks for the server's, as the console's harness does (the tests written before S09-D type ids). Tests: embed
DOM "S09-D: the embed page's 公司范围 is the same picker…" (the picker from the real demo, `company_ids` on the wire, nothing in the
messages to the parent, the text field without the directory); the embed route tests list `/console/companies.js` (`embed_test.go`,
`handler_test.go`).

D5 · **English** (S17-A). The picker's ten texts have their entries at the end of `en.js` (the filter's label and placeholder, the
three note lines, 没有匹配的公司。, the typed line's placeholder and label, 共 {0} 家 for the chip); `scripts/i18n_extract.mjs` reads `companies.js`;
the English walk of `console/i18n.test.mjs` marks the demo's directory partial so that it sees the picker, its typed line and its note and
still types the refused company, and it counts the directory's names among the server's data. `scripts/browser_english_s17a.mjs` drives
the picker and counts the names as data too. Tests: `node --test console/i18n.test.mjs` (1818 keys, 0 missing, 0 stale; the walk with
the picker).

D6 · **Measurement** (ADR 0015 decision 6, ADR 0011 decision 7). `scripts/ux_baseline_s07.mjs` gained `chooseCompany()`: it pays what
the page asks — nothing when the one listed company is chosen already, one click on a listed company not yet chosen, one typed text
when the field is the text field — and T1, T2, T7 and the team T8 call it where they typed `company_demo`. The same script was run on
the base (S09-G as rebased on main 43f8db5, 50c3a87; a demo built from that tree: `evidence/s07a/baseline-before-s09d.json`) and on
this branch (`evidence/s07a/baseline-after-s09d.json`), 2026-11-11, headless Chromium at 1440 × 900. T1 (登录 → 冻结快照 → 导出 Excel): 2 clicks, **2 →
1** texts, 0 page switches, 0 shells, 0 dialogs, 0 dead ends. T2 (从空会话发布一个带指标卡的看板): 8 clicks, **2 → 1** texts, 1, 0, 2, 0. T3–T8
unchanged. Totals T1–T8: clicks **35 → 35**, texts **5 → 3**, pages 10, shells 0, dialogs 8, dead ends 0. The team set (S12-D) the same
way: T8 3 → 2 texts, T9 2 → 1, totals 25 clicks, 5 → 3 texts, 8, 0, 3, 1 (`evidence/s07a/baseline-team-before-s09d.json`,
`baseline-team-after-s09d.json`). ADR 0015 expected texts to fall from 5 to 4 ("T1's second typed text becomes a click"): it had
overlooked that T2 types the company as well, and the demo lists one company, which the picker chooses for the person, so neither task
types it nor clicks for it. The held numbers of `scripts/ci_browser_evidence.sh` (35 / 10 / 0 / 0; the team's 25 / 8 / 0 / 1) do not
change: nothing went up (texts and dialogs are not held). docs/UX_BASELINE_S07.md has the table.

D7 · **The real browser.** `scripts/browser_console_s06c.mjs` checks the picker open at 1440 (演示公司 with company_demo as secondary text,
chosen, no typed line, no filter, overflow and contrast, `console-03a-company-picker.png`), the chip's name after the preview, the data
drawer's focus with a company chosen, the picker in the data drawer at 390 (it fits the drawer, no overflow, contrast,
`console-390-company-picker.png`), and — the demo listing one company — a directory of twenty, partial, answered by a stub of the page's
fetch for the route alone in a new session: the filter, the typed line, the sixteenth chosen disabling the rest with its line, overflow
and contrast (`console-03b-company-picker-many.png`); the workspace with the picker is in the pages checked at every width and scheme as
before. `scripts/browser_english_s17a.mjs` drives the picker too. Evidence: the CI job locally (`scripts/ci_browser_evidence.sh`): console,
library, baseline, baseline-team and english pass.

D8 · **Docs.** This page; docs/CONSOLE.md (the S09-D note); docs/ADR_0015_COMPANY_DIRECTORY.md (the implementation line);
docs/ADR_0014_CONSOLE_LEGIBILITY.md (the S09-D slice done); docs/ADR_0017_ENTERPRISE_ROADMAP.md (the Progress line);
docs/UX_BASELINE_S07.md (the S09-D section); docs/STUDIO.md (the data drawer's focus and the chip's text); docs/TEST_SCENARIOS.md (the
company scope); the manual's words where it said to type ids (docs/manual/index.html: the quick start, the workbench's control table,
figure 10's caption, the test data, cases C03 and C06). The manual's screenshots were **not** retaken: those that show the query panel
(02-home, 03-data-fields, 05-data-drawer, 07-workbench-preview, 08-workbench-snapshot, 10-refused-company, 11-shipment-preview,
34-dark-workbench, 36-phone-workbench) now show the picker instead of the text field, and every figure taken after a query shows the
chip with 演示公司 where it showed `company_demo`; `scripts/manual_screens.mjs` types `company_other` for figure 10 and needs the typed
line (a partial or absent directory) to take it again.

## Interpretations of ADR 0015 made here

- **One listed company is chosen already.** The ADR says the selection is the person's; with exactly one company there is no choice to
  make, and the product and dataset selects already take the first entry. The person can unmake it. With two or more nothing is chosen.
  This is also what keeps the baseline's clicks at 35 (D6).
- **"1–128 characters"** is held on the console as the JavaScript string length (1–128), not blank, without a control character
  (C0, DEL) — a superset of the server's 128 bytes (S19-B), so nothing the server passes is refused here and anything longer is.
- **The bound of sixteen** counts the chosen and the typed ids together: the sixteenth chosen disables the rest of the list, and a
  seventeenth through the typed line is refused on the page as seventeen typed ids were (请检查数据集、公司范围及数据时点。).
- **A typed id that is also chosen** is sent once.
- **The chip** for more than two companies is the first name and the count (`演示公司 · 共 3 家`); the same rule applies to ids when
  there are no names. The schedule dialog's line follows the chip.
- **Any failure of the route**, not only the source's 503, leaves the text field (a 404 of an older server, a 403, a non-JSON body); the
  catalogue's own notice is the only one shown.
- **刷新目录** asks the directory again with the catalogue: "once per product and session" is the console's own doing, not a refusal
  to re-ask when the person asks.
- **The data drawer's first focus** (S07-A: the empty company field, else 查询预览) becomes: the company to choose (the filter when there
  is one) when none is chosen, else 查询预览.
- **The DOM harness** answers the directory as a product without it by default (the tests written before S09-D type ids, and the
  typed path is a real mode of the field), as it answers the job listing empty (S07-C); the S09-D tests ask for the server's.

## Known

- The demo lists one company, so the filter, the bound and the partial line are exercised by the DOM tests (intercepted directories)
  and not in a real browser; a product that lists more will show them there.
- Names are looked up in the current product's directory only: a snapshot restored from the task list for another product shows its
  ids on the chip, as before.
- The manual's figures listed in D8 are to be retaken with `scripts/manual_screens.sh` once figure 10's path is adapted.
