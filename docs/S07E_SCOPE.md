# S07-E the task list's filters and pages

2026-11-05. S07-C made the console's task list the server's newest 50 jobs. Older jobs were reached only by id, and the list
could not be narrowed. S07-E adds a filter and a next page, on the server and in the panel.

E1 · `Repository.Jobs(ctx, identity, JobListing)`. `JobListing{Integrations, Limit, Kind, State, Before}`: one kind and one
state when set; strictly older than the identity's job `Before` (the last job of the previous page) when set. A `Before` that is
not the identity's job under these integrations, or no job, gives an empty page: the cursor discloses nothing. The memory store
walks its admission order from the cursor; the PostgreSQL statement compares `(created_at, id)` with the cursor's own row,
looked up under the same identity, and the plan test still logs an Index Scan of `jobs_identity_created`. `JobListing.Valid`
holds the bounds (limit 1..100, the five states, the three kinds, an id).

E2 · `Service.Jobs(ctx, principal, JobListing)`: the caller gives limit, kind, state and before; the integrations are the
service's (the held ones), and a listing that names them is refused. Each entry is still its poll's view.

E3 · `GET /jobs?limit=N&kind=K&state=S&before=ID`: each key at most once, none empty, no other key; a value the service refuses is
400. A page shorter than the limit is the last.

E4 · The panel. Two selects above the list (类型: 全部类型, 报表快照, 导出, 正式出具; 状态: 全部状态 and the five states) filter at
once what the console knows and read the server's first page under the filter. A full page (50) shows 加载更多, which reads the
page after its last job and puts it after everything shown; the console keeps at most 500 jobs and says so when it reaches them.
The rail's badge and the overview count every known job, the list's note the filtered rows; a filter nothing passes says 没有符合
筛选的任务. Only the latest read of the first page applies (a filter, the panel opening and 刷新状态 can overlap; the race showed
in the first run of the DOM test). Sign-out clears the list and the filters. The embed page has no panel and reads no list.

Tests: the repository contract (kind, state, both, pages of two, the cursor with a filter, another identity's cursor, a cursor
under another integration, an unknown cursor, refused values) on the memory and PostgreSQL stores; the HTTP listing test (the
same through the query, and the refused queries); DOM test S07-E (the filters against the real server, two pages through a
fake listing, the button in the panel, sign-out).

Out of scope: a total count of the identity's jobs, sorting other than newest first, a filter by integration or by date.
