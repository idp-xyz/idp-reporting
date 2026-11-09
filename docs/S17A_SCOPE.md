# S17-A an English console: a catalog keyed by the Chinese source text, applied as the page is written

2026-11-09. ADR 0017 lists "a console in more than one language" among the gaps an enterprise sees. This slice gives the console, its
studios, the template centre, the drawers and panels, the audit log, the sign-in card and every notice and error message an English
version, chosen by the person, while Chinese stays the default and behaves byte for byte as before.

A1 · The mechanism. `console/assets/en.js` is a catalog keyed by the console's Chinese source text: 1,648 entries, 1,385 whole texts and
263 messages the code joins from text and values (`'已创建「' + name + '」。'` is the key `已创建「{0}」。`, English `Created “{0}”.`;
the English may reorder the placeholders). `console/assets/lang.js` applies it: on load to the static markup, then through one
MutationObserver to every text node and every `aria-label`, `title`, `placeholder` and `alt` the console writes. A text is looked up
whole; else the most specific pattern whose values all translate (a value between quotes 「」“” is a name the person gave and is kept);
else the best of that pattern with a value that is data, the text split at its coarsest separator (` · `, `；`, `：`, `，`, `、`,
` / `, but only when a piece is the console's), and a word of the console at either end (`锁定 n1`, `账龄（明细级）`), taking whichever
leaves the least Chinese. Text that is not the console's (the server's data) stays as it is. A subtree marked `translate="no"` or
`lang="zh…"` is never translated: the switch's 中文 and the template's own text drawn on the studio's canvas, which is the document's
content, not the console's.

Why this and not `t()` calls at the source: the console's ~23,000 lines write their text in about 1,650 places, through `textContent`,
the `h()` helpers, notices and concatenations; wrapping each in `t()` would touch nearly every line of nine files that the Chinese DOM
suite (223 tests) holds to exact Chinese strings, for no change in Chinese. Keyed by the source text, the Chinese code stays as it is, and
the catalog is held to it mechanically: `scripts/i18n_extract.mjs` parses the scripts (espree, now a pinned dev dependency) and the
markup and lists every Chinese literal and joined message; `console/i18n.test.mjs` fails on one without English, on English for text the
console no longer has, on a key twice, on placeholders that differ and on Chinese left in the English. A new or changed Chinese string
therefore fails CI until its English is written (`node scripts/i18n_extract.mjs` lists them). The cost: one Chinese text has one English
whatever its context (发布 is "Publish" as a button and as a role), and the fallbacks are heuristics, which the DOM test below keeps honest.

A2 · Code that compared the page's text. Three places read text back from the page and compared it with Chinese; they now compare what
was written (`reportingLanguage.source(node)`, the same as `textContent` in Chinese): documents.js's in-place update of an inspector
panel (`shape` and `listOptions` compare option labels; in English every update would otherwise replace the panel and lose the focus),
studio.js's `text()` (skips an unchanged text) and canvas.js's idle status line. No code looks an element up by its Chinese text.

A3 · Choosing the language. A 中文 / English switch (two `aria-pressed` buttons in a group, no ids) sits in the console header, which every
shell keeps (the studios and the centre open below it), and on the sign-in card. `?lang=en` or `?lang=zh` in the address chooses too; the
parameter is read once and taken out of the address with `history.replaceState` (the S15-A sign-in compares the page's address with the
registered one, and a bookmark should not carry it). The choice is kept in `localStorage` under one key, `reporting-console-lang`, written
only when English is chosen and removed when Chinese is: a Chinese console stores exactly what it stored before (the DOM suites' "nothing
stored" assertions hold unchanged), and the key holds a preference, never data or a credential. The address alone would not do: the token
is in memory only, so a person comes back through the sign-in card, and the provider's return (S15-A) is a new page without the
parameter. Switching never reloads (that would sign the person out): to English it translates the page and starts the observer, back to
Chinese it restores every text and attribute it changed, stops the observer and removes the key. `<html lang>` follows (`en` /
`zh-CN`), and so does the tab's title. embed.dom.test.mjs's storage rule now names this one key as lang.js's only exception, and its
markup rule covers lang.js.

A4 · Layout. English is longer: in English (`html[lang='en']` rules in styles.css, Chinese untouched) a quiet text button takes its text's
width (the widgets' Move up / Move down / Remove, the tasks panel's Refresh / View result were 29 px wide), the rails' labels are 10 px
(allowed for rail labels by ADR 0014 decision 10), the eyebrows keep a gap between their two parts; the rail labels are short words
(Reports, Dashboard, Template, Docs, Tasks, Audit, System, Help; the studios' tool rails Blocks, Data, Structure, Resources). Long
eyebrow notes are shortened in the catalog.

A5 · Tests. `console/i18n.test.mjs` (CI's console and PDF jobs, `npm run test:console`): the catalog against the source; the translator
on its own (Chinese by default changes and stores nothing; `?lang=en` translates markup and later writes, keeps names and data, leaves the
address; the switch back restores and removes the key; a stored choice, `?lang=zh` and an unknown value); and the real console in jsdom
against the real Go server in English: the sign-in card, the workbench with a refusal, a preview and a snapshot, the data drawer, the tasks
panel, the dashboard studio with three components, the document studio with a sample copied and opened and each of its tabs, the
template centre with its wizard and a sample's mapping dialog, the audit log and the system page. It fails on any text node, `aria-label`,
`title`, `placeholder` or `alt` with Chinese once the server's data is taken out (the identity's catalogs and the sample pack as the API
answers them, nothing listed by hand). `scripts/browser_english_s17a.mjs` (run last by scripts/ci_browser_evidence.sh, job "browser")
does the same in a real Chrome at 1440 and 390 px with screenshots, and checks no horizontal overflow, no control whose English is cut off
and no visible Chinese of the console, then switches back to Chinese and leaves no stored choice in the profile. The Chinese runs
(scripts/browser_console_s06c.mjs, browser_library_s06b.mjs, both task baselines) are unchanged and keep their numbers.

Out of scope. The user manual (docs/manual, served at /console/manual/) stays Chinese; so do the embed pages (embed.html does not load
lang.js and the embed URL admits no `lang` parameter; a host that needs English is a later slice), the server's own texts (dataset and
field labels from the source products, the sample pack's names, descriptions and texts, which are data), the template's content, default
names the console writes into a new design (they become the design's data when saved), Excel and PDF outputs, and dates formatted with
`zh-CN` (numeric only). A person's data that happens to equal a console phrase is shown translated (in English mode only).
