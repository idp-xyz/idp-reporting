# S18 · The schedules page, the administrator's read-only view of data sources and bindings, and a proposal for changing them

2026-11-09. The product owner asked for three things after S16-A and S17-A: a console page for scheduled reports (S16-A had the API and the
webhook only, so a person could not use them), a page where an administrator sees the data sources (product connections), the bindings and
the webhook in force, and a decision document on whether data sources may be changed from the console. This slice does the first two and
proposes the third (docs/ADR_0021_CONSOLE_CONFIGURATION_CHANGES.md, status proposed: the product owner decides).

## A · 定时报表 (the console's page for S16-A)

A1 · Where. A rail item 定时 (`data-page="schedules"`) between the tasks and the audit, and a 设为定时 button beside the result's exports. A
schedule is always made from the current query: the dialog shows the dataset, the companies and the as-of of the result on screen and never
asks for a query again. With no result the button is disabled; from the page, 新建定时报表 without a result says to query first and opens the
workbench.

A2 · The dialog. Name (the dataset's name by default, which is data and reads the same in both languages), frequency (每天 / 每周 with the
weekday / 每月 with the day 1–28, S16-A's three forms only), the time HH:MM, the IANA zone (the query's zone by default), the as-of rule
(the day before the run, the run date, or fixed at the current query's), and the export (snapshot only, Excel, or a format the server's
capabilities list). The console checks the same shapes the server does and says which field is wrong; the server's refusal is shown in the
dialog (a 429 naming `schedules` says the per-person limit was reached). One idempotency key per opening of the dialog.

A3 · The page. The person's schedules, newest first: name, data (dataset · companies · as-of rule · format), recurrence, next run (only while
active), state, and 运行记录 / 暂停 or 恢复 / 删除 (with a confirmation). 运行记录 opens a panel with the schedule's runs: planned time, as-of,
the result (a code when it did not succeed), the delivery (未配置推送 / 等待推送 / 已推送 / 推送失败), and for a run that succeeded 查看结果 (the
frozen snapshot opens in the workbench with the schedule's query at the run's as-of) and 下载 (the export, through the same checked download
as the tasks: content type, file header). A deployment without schedules answers the routes 404: the page says scheduled reports are not
enabled and the button disappears.

A4 · The audit page knew six actions and nine object kinds and refused anything else as an unverifiable answer; S16-A's `schedule.change`,
`schedule.run` and the kind `schedule` would have broken 操作记录 as soon as anyone made a schedule. They are known now (更改定时报表, 定时运行,
定时报表).

A5 · The demo. A scheduled run of the S12-D colleague (`REPORTING_DEMO_COLLEAGUE_TOKEN`) resolves to the colleague; before, the demo's
scheduler knew only user_a and the colleague's runs would have been refused as unbound.

## B · 数据源与绑定 (the administrator's read-only view)

B1 · The route. `GET /api/reporting/v1/admin/configuration`, the administrator role only (`RequiredRole`: every `/admin/` route, reads included;
a refusal is `role.refused` in the audit). It answers the configuration in force for the caller's tenant: the product connections (product,
source tenant, the adapter's registered name; never its options), the bindings (subject, client, products, roles or null for the defaults),
the SHA-256 digests the start and reload log lines give for the connections and the bindings file, when the server started and when the
bindings were last loaded (a successful SIGHUP moves it), and whether scheduled reports are on (and the per-person limit), whether a webhook is
configured and its host (never its path or query, where a receiver may keep a token; never the signing secret), and whether the browser
sign-in is configured. A query string is 400; a view of another tenant than the caller's is an integrity failure. The host builds the view:
the server from its configuration and the bindings in force (read under the reload lock), the demo from its own people.

B2 · The page. A rail item 管理 (`needs-administrator`: hidden unless the session's roles include administrator). Four panels: the
configuration in force (tenant, version, start, the two digests shortened to 12 characters, the bindings' load time), the connections with a
状态 column, the bindings, and the scheduled reports with their delivery. 检查连接 reads each product's catalog through the administrator's
own binding (the same check as 能力与环境, cached the same way); a product outside that binding says 未检查（不在您的绑定中） rather than pretend.
Nothing on the page writes: a connection or a binding is changed in the files, through the operator's change process, and takes effect on a
restart or a reload (ADR 0016 decision 1 stands; ADR 0021 proposes what could change).

B3 · The demo. `-administrator` gives user_a the five roles, so the tenant's audit and this page can be shown; the colleague keeps the
default roles. Off by default: the console tests and the browser checks run without it.

## C · The tests' demos pick their own port

The three console suites each start the demo binary; each picked a free port, closed it and passed it to the demo. Run in parallel (as CI
runs them), another suite's demo could take that port in between, and a suite then talked to the wrong server: the embed suite failed 20 of
its tests on a 403 from a demo without its embed origins. The demos now listen on `127.0.0.1:0` and the suites read the address from the start
line. The browser sign-in test still picks its port first: its redirect URI must name the port before the demo starts.

## Tests

- Go: `TestTheAdministratorsViewIsTheAdministratorsAlone` (role, tenant, query, empty lists), `TestTheServerShowsTheConfigurationInForceToAnAdministrator`
  (a default-role refusal in the audit, the digests of the start and reload lines, no webhook path, query or secret, no adapter options, a
  reload's bindings), `TestTheDemosPeopleResolveAndTheAdministratorsViewListsThem`, `RequiredRole` for `/admin/`, the OpenAPI route test.
- DOM: S18-A (from the result to a weekly schedule, a refused zone, pause, resume, runs with a refused run and a failed delivery, delete, the
  audit with the schedule's events, sign-out; a deployment without schedules) and S18-B (hidden for a non-administrator; the view's
  bindings, roles, digests and delivery; a product outside the binding not checked; the own product checked). The embed suite's id list and
  the rail order tests know the new elements. The English pass visits both pages, the dialog and a schedule with its runs.

## Not in this slice

Creating a schedule from a saved layout or dashboard rather than the current query; a schedule's runs are not removed when it is deleted
(they expire with their jobs; S16-B's retention); email delivery; the administrator's view of other identities' schedules; any write of a
connection, a binding or the webhook (ADR 0021).
