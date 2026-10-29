# S03-L canvas for bands and the 1.x to 2.0 conversion

2026-10-29. Completes ADR 0004's console side: the S03-I canvas edits schema 2.0 templates (S03-J) as well as
1.x, and a 1.x template can be converted into 2.0 when the user asks. The server does not change; every edit
still goes through the designer's validation (the console's closed 2.0 validator, which shares its cases with
Go), and the PDF preview stays the authority for layout.

L1 · Bands on the canvas. A 2.0 template is drawn as its page: the reserved banner/stamp strip, then the bands
stacked from the top of the safe region (page header, document header, detail, document footer, page footer
at the bottom), each a labelled region with its height; the detail band shows the table (header row, column
boundaries, labels or field names, one sample body row) and stretches to the space the page leaves it.
Elements are drawn inside their bands: texts (source, field or text, wrap marker, bold, alignment), lines and
rectangles (stroke, fill grey). The page texts show their placeholders. Positions on the canvas are band
positions plus the band's top.

L2 · Editing 2.0. Everything S03-I does for 1.x works for 2.0 elements: select, multi-select, drag, resize,
snapping (1 mm, Alt 0.1 mm, edges and centres within the band), nudge, align, distribute, duplicate (lowest free
`e` id), delete, undo/redo, the palette. Dragging an element across a band boundary moves it into the band under
the pointer (its position becomes relative to that band); a move the validator refuses snaps back and names
the reason. A band's bottom edge can be dragged to change its height (not the detail band's). Lines are moved
by their body and edited by their two end handles. The table's column boundaries are dragged as in 1.x;
columns are added, removed, reordered and given a literal header label and an alignment from the table's panel.

L3 · Panels. A 2.0 template gets panels in place of the 1.x element inputs: group fields (choose 1–3 dimension
fields, in order, or none); add an element to a chosen band (text with its source — literal, field, group sum,
page text — line, rectangle); the selected element's properties (every key its type has, as validated inputs);
bands (add or remove an optional band, its height). The inputs and the canvas stay in sync both ways, and every
change is one undo step.

L4 · Conversion. "转换为 2.0" turns the open 1.x template into a 2.0 configuration and opens it as unsaved (it
is saved as a new version only when the user saves). Mapping: boxes above the table (or all boxes when there is
no table) go to the document header with their positions made relative to its top (the top of the safe
region), its height reaching the table's top; the table becomes the detail band's table with its columns and
widths; boxes entirely below the table go to the document footer; a 1.x `row` box becomes a group `field`
text, a `total` box a `group_sum` text, a literal stays a literal; no group (the whole snapshot is one document)
unless the user picks group fields afterwards. Refused, with the box named, when a box overlaps the table's
vertical extent. A template with a print offset, variables mode or a background converts with a warning that
these are not part of 2.0 and are dropped. A test pins the mapping on representative templates, and the
converted template's preview places every box converted into the document header at the same page position as
the 1.x preview (boxes moved to the document footer follow the table, as 2.0 places them, and the user is told so).

L5 · Refusals on the canvas. A preview or check refusal's `detail` outlines its band, element or column for
2.0 as for 1.x.

L6 · Tests. DOM tests for drawing, every 2.0 editing path, band moves and heights, the panels, conversion
(mapping, refusal, warning, position equality through the real preview), undo and the palette; the shared
2.0 cases still agree in Go and the console; a real-browser script `scripts/browser_canvas_s03l.mjs` (as
`browser_canvas_s03i.mjs`: Windows Chrome over CDP, real mouse and keyboard, 390/768/1280 px, light and dark)
that builds an invoice-like 2.0 template from a converted 1.x one, edits bands and elements, previews it and
checks the JSON after each step; local evidence, not CI.

Not in this slice: barcodes, QR codes and images (ADR 0005), 2.0 print offsets and backgrounds, sub-groups,
server-side changes.
