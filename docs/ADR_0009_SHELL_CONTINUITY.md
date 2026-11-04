# ADR 0009 · One navigation and one data context across the console's shells

Date: 2026-11-03. Status: accepted (merged with S07-A). Implementation: S07-A (the application rail and the data drawer), S07-B (the
tasks panel and 保存并发布); later slices decide the rest on the evidence these produce.

## Context

ADR 0008 made the console follow the prototype `IDP_Reporting_Designer_UIUX_V3.0.html`; S06-A, S06-B and S06-C did so.
The prototype has two screens (the document studio and the template centre), each full-screen; the console has eight
pages. The result is four shells that replace one another (the console shell, the document studio, the template
centre, the dashboard studio), each with its own way out (the wordmark to the overview, the crumb to the workbench or
the centre, 返回设计器, 数据中心), and a data context (product, dataset, company scope, date, preview or frozen snapshot)
that lives only in the workbench page: every designer starts with a card that sends the person back there.

The task baseline of `scripts/ux_baseline_s07.mjs` on main 789b293 (docs/UX_BASELINE_S07.md): five tasks, 34 clicks,
19 page switches, 15 shell changes, two dead ends (the numbers of the script as it stands after S07-B, which also counts
a publish refused for unsaved work as a dead end; the first measurement, before that rule, read 33 and one). Going from the document studio to the dashboard studio to the centre
and back costs 7 clicks and 7 shell changes; publishing a dashboard from an empty session costs 11 clicks, 3 shell
changes and two dead ends. These are measured, not felt.

The question "is the console the primary surface" is answered by ADR 0006 and docs/s00/ARCHITECTURE.md: products embed
the viewer, the dashboards and the document designer for their users; the console is the platform's own workbench for
the people who design templates and dashboards, run exports and read tasks. The console's user is the designer; the
cost above is the designer's.

What must hold: the fidelity rule of ADR 0008 for what the prototype specifies (the studio's regions, labels, states and
spacing; the centre's); the security posture (CSP, no `innerHTML`, the token in memory); the server's authority over
data, snapshots, versions and publication; the DOM tests through the real Go server.

## Decision

1. **The prototype specifies screens, not the passage between them.** Between the studio, the centre and the console
   pages the prototype has only its own buttons (返回设计器, 数据中心, the wordmark). This ADR owns that passage. Inside a
   screen the fidelity rule of ADR 0008 stands unchanged.
2. **One application rail, on every shell.** A 56 px vertical rail at the left edge of the viewport, present on every
   page including the studios and the centre: the brand square, the eight destinations as icon-and-label buttons with
   the active one marked and the counts (datasets, tasks) as small badges, the connection state at the bottom. The
   studios' and the centre's full-screen boxes start to its right; their own wordmarks, crumbs and buttons stay (the
   prototype's) but no longer are the only way out. The console pages lose the 222 px navigation column: the rail is
   the navigation. At phone widths the rail is a horizontal strip at the top.
3. **One data context, reachable from everywhere.** The shell header, the centre's header and the dashboard studio's
   context bar carry a data chip that names the current context (dataset @ version · company scope · date · 冻结快照 or
   查询预览, or 未选择数据) and opens the data drawer: the workbench's query panel, moved into a right drawer while it is
   open and put back after (the mechanism the document studio already uses for 参数与筛选), with 查询预览 and 创建快照 as
   they are. A result arriving closes the drawer. The document studio keeps its own 参数与筛选 drawer, which is the
   same panel. Nothing about authorization, scope or snapshots changes: the panel is the same panel, the requests the
   same requests.
4. **No dead ends.** A page that needs data says what it needs and offers the drawer (选择数据…); it never sends the
   person to another page for it. The dashboard studio's start card and the centre's edit refusal are the two today.
5. **Evidence first, then the rest.** S07-A ships 2–4 and re-runs the task baseline; the numbers before and after go
   into docs/UX_BASELINE_S07.md. Whether to merge the overview into a home, to fold the data centre into a picker, to
   move tasks into a header panel and to unify the three designers' version and publication controls is decided after
   that, with its own ADR, on those numbers and on the designer's actual tasks.

## Consequences

The console's chrome changes once more, by a narrow rule; the studios' and the centre's internals do not. `styles.css`,
`studio.css`, `library.css` gain the rail offset; `app.js` gains the chip, the drawer and the parking; `library.js` and
`board.js` take a `data` port; `index.html` and `embed.html` change in the shared sections only where the start card's
button changes (the embed hides it either way). The real-browser script of S06-C is updated for the rail's geometry;
the task baseline becomes a script that is run before and after every UX slice.
