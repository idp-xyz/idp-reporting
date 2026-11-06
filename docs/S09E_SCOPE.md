# S09-E (part 1) the version dialog and 发布前确认 are one component that the three designers call

2026-11-06. ADR 0014 decision 9, the first of its later slices. No visible change is intended, and none is asserted: the proof is
that `console/dom.test.mjs` and `console/embed.dom.test.mjs` pass with no assertion changed (213 tests), and that the real-browser
scripts, whose tasks T2, T3 and T6 go through 版本, 保存版本, 发布 and 保存并发布 in all three designers, pass with the baseline's
numbers unchanged (31 / 9 / 0 / 0).

E1 · One component. `console/assets/versions.js` defines `window.createReportingVersions(ports)`: the version dialog (the cards with
`aria-current`, the detail card's title and note, 恢复到编辑器 and 发布此保存版本, the empty state, the pointer line) and the 发布前确认
(the summary rows, the note, 确认发布 / 保存并发布, the decision of what a publish does: 保存并发布 when the editor holds unsaved
changes or nothing is saved yet (S07-B), a rollback when a historical version is picked (S07-G)). It writes text and DOM nodes only,
loads nothing and stores nothing. The host supplies what differs between the three as ports: its id prefix (`ly`, `bd`, `st`) and
the ids that do not follow it, its two dialogs as `{open, close, isOpen}`, `state()` (the saved version, the latest, dirty, the
release state, the pointer, the name, the dataset, the count), its words (`thing`, `opened`, `content`, `dirty`, `count`, the
editor's notes, the unread note), its guard `run(fn)`, its requests in its own sequence (`readVersions`, `readRelease`, `restore`,
`publish(plan)`), whether the confirmation opens before the release state is read (`readsWhileOpen`, the document studio), and its
own additions to a card, a pick, the detail card and the confirmation. The component returns `openVersions`, `openPublish`, `sync`,
`picked` and `clear`.

E2 · The hosts. `app.js` (the report layout designer), `studio.js` (the document studio) and `board.js` (the dashboard studio) each
delete their copy and call the component; every request sequence stays the host's (the layout's three-request 保存并发布, the board's
save → read release → publish with its early exits, the studio's `api.save → readRelease → publish`), and every text the tests pin
stays. Line counts: app.js 2,685 → 2,595, studio.js 1,359 → 1,297, board.js 476 → 371; versions.js 253. The new script is served
by `console/handler.go` (`//go:embed`, `/console/versions.js`), loaded by both pages before the scripts that call it, listed in the
DOM harness, in ESLint's factory names and in CI's syntax step.

E3 · What is unified where no test looked. The studio's version dialog focuses the picked card on open (it focused the first card)
and follows the editor's version as the other two did; publishing or restoring from the version dialog closes that dialog before the
host's request rather than after; an open 发布前确认 is drawn again on every host sync (its content is the same in every reachable
state). These are the behaviour of two of the three designers made the behaviour of all three.

Left for the rest of S09-E: the dashboard studio's `#page-dashboard` block of `styles.css` sharing the studio's rules instead of
repeating them (43 of its 50 class names), and a small declarative checker for the server shapes that `checkJob`, `validDataset`
and `showRelease` validate with boolean chains.
