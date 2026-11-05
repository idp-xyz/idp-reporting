# ADR 0010 · One shell: the console header on every page

Date: 2026-11-05. Status: accepted (with S07-F). Supersedes the part of ADR 0009 §2 that let the studios and the template centre
replace the console header with their own.

## Context

ADR 0009 gave every page one application rail and one data context, and left one question open (§5): whether the three
full-screen boxes, the document studio, the template centre and the dashboard studio, should keep replacing the console header
with their own top bar. The task baseline answered it with a number that did not move through S07-A and S07-B: 8 shell changes
over five tasks, every one of them a box swapping in its own header (its own wordmark, its own data chip in the centre) and
swapping it back. The person who designs templates and dashboards goes between these boxes all day (T5 alone is 4 of the 8).
The designer asked for the boxes to sit in the same shell (2026-11-05).

## Decision

1. **The console header is the header of every page.** The brand with the page's label (DOCUMENT STUDIO, TEMPLATE WORKSPACE,
   DASHBOARD STUDIO, as the other pages have theirs), the data chip, the environment, the identity and 退出工作区 stay where they
   are on every page. The three boxes start below it (`--shell-head`, 68 px; 56 px at phone widths) as they start right of the
   rail (`--app-rail`).
2. **A box's own top bar becomes its toolbar.** Inside the box the prototype's top bar stays (ADR 0008's fidelity rule): the
   crumb, the title and its state, the help, 版本, 保存版本, 预览与导出, 发布; the centre's 返回设计器, 数据中心, 新建模板. What
   would repeat the console header is hidden in the console: the boxes' wordmarks (they were the prototype's way out of a
   full-screen screen, which the rail and the crumbs now are) and the centre's second data chip. The centre's bar is 56 px.
3. **The embed page is unchanged.** It has no console header (`--shell-head` is 0 there) and keeps its own rules.
4. **The baseline counts a shell by what is on screen**: the console header while it is shown, else the box that replaced it.

## Consequences

Shell changes go from 8 to 0 over the five tasks; clicks, typing, page switches, dialogs and dead ends are unchanged (no step was
removed). Each box loses 68 px of height to the header on a laptop screen; the studios' canvases and the centre's list scroll as
before. The markup is unchanged (the embed page shares it byte for byte); the change is in the stylesheets and in the header's
labels. The real-browser scripts measure the boxes below the header; the template centre's script also found a layout fault
older than this slice (its cards inherited the data centre's column layout and pushed their actions outside), fixed with it.
