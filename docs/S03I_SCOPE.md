# S03-I document canvas editor, interaction core

2026-10-28. The first step toward the designer that the studio prototype (`IDP_Reporting_Designer_UIUX_V3.0.html`)
describes: a visual A4 canvas in the console's document designer for the template schema that exists today
(text boxes and one detail table, schema 1.0–1.3). The prototype is the UX specification, not code to merge.
The editor never decides what a template means: every edit produces a template configuration that goes
through the designer's existing validation (`validate`/`canonical` in `console/assets/documents.js`), and the
server stays the authority for layout (Typst previews; the issue check). The server gains one read-only check
route (I5); nothing else on the server changes.

I1 · Canvas. The designer page gains an A4 canvas (210 × 297 mm at a fixed scale, zoom 50–200 %) showing, in mm:
rulers on the top and left edges, the safe region (x 10–200, y 25–270), the reserved banner and stamp strip
(10–18 mm from the top, shaded, not selectable), every text box (its id, its source — literal, row field or
total — and its text or field label, at its font size), the detail table region with its column boundaries and
header labels, and the print offset (the variables mode greys out the literals). Pure DOM in a new script
`console/assets/canvas.js` (one inline SVG or absolutely positioned elements), no third-party code, no
`innerHTML` (DOM nodes and `textContent` only, as the rest of the console). The console's Content-Security-Policy
stays unchanged: positions are set through `element.style` properties (CSSOM) or SVG attributes, never through
`style=` attributes or inline `<style>`.

I2 · Editing. Click selects; Shift-click adds or removes; Esc clears; Ctrl/Cmd-A selects every box and the table.
Dragging moves the selection; corner and edge handles resize one box or the table region; the table's column
boundaries can be dragged, the column to the right giving up or taking the width (the sum stays the table
width, each column keeps the designer's minimum, the first column keeps the label minimum). Resizing the table
scales its columns in proportion, rounded to 0.1 mm with the remainder on the last column. Snapping: positions
and sizes snap to 1 mm by default, to 0.1 mm with Alt held, and to the edges and centres of the other items
within 1 mm (a guide line shows the snap). Arrow keys nudge by 0.1 mm, Shift-arrows by 1 mm. Delete removes the
selected boxes (or the table); Ctrl/Cmd-D duplicates a box under the lowest free id at the nearest free place
below it. Align (left, centre, right, top, middle, bottom; two or more items) and distribute (horizontally,
vertically; three or more) act on the selection. An edit the designer's validation refuses (out of the safe
region, overlapping, too small, the last box, more than 24 boxes) is not applied: the items snap back and a
status line names the reason. A schema 1.0 template edited off whole millimetres becomes 1.1, as the inputs
already do. The existing per-element inputs stay and stay in sync both ways.

I2a · Undo. Every applied change of the template — from the canvas, the inputs, the add/remove buttons, the
table and print panels — is one undo step (Ctrl/Cmd-Z; redo Ctrl/Cmd-Shift-Z or Ctrl/Cmd-Y; at least 100 steps;
a drag is one step, not one per pointer move; a refused edit is no step). Opening a template (JSON, server
version, published version), resetting it and a new query clear the history. Undo never restores the "opened
published, unedited" state that issuing requires: after any edit, issuing needs the published version opened
again.

I3 · Command palette. Ctrl/Cmd-K opens a palette listing the designer's actions (add literal, add row field, add
total, add or remove the table, align and distribute, undo, redo, PDF preview, check samples, save version,
publish, open published, issue) filtered by typing; Enter runs the highlighted one. An action runs exactly what
its button runs and is listed only when its button is present, visible and enabled. The palette is a modal
dialog (role=dialog, aria-modal, labelled), operable by keyboard alone, keeps focus inside, and Esc closes it and
restores focus.

I4 · Accessibility and layout. The canvas is keyboard-operable (Tab moves through the items in reading order;
Enter or Space selects; arrows nudge), every item has an accessible name (its id, source and text or field, its
position and size), selection changes are announced through a polite live region, the page has no horizontal
overflow at 390, 768 and 1280 px (the canvas scrolls inside its own panel), and the colours work in light and
dark themes as the console does.

I5 · Boundary samples. "Check samples" checks the current template against rows of the frozen snapshot without
rendering. New route `POST /api/reporting/v1/document-checks` with `{config, snapshot_id, rows}` (explicit row
indices, 1–50 distinct, the preview's index convention) or `{config, snapshot_id, samples: "boundary"}` (the
server picks: the first and the last row and, for each row-field box, the rows with the longest and the
shortest value by display width — East Asian wide characters count two — ties to the lowest index; at most 50).
It answers `{"rows": [{"row_index", "ok", "code"?, "reasons"?}]}` in row order: `code` is the error code a
preview of that row would answer (e.g. UNSUPPORTED_COMBINATION for a value that does not fit its box), `reasons`
says why a boundary row was picked. Authorization is the preview's (the snapshot is the caller's, the fields
are authorized, re-checked before answering); a refusal of the whole request (authorization, an invalid
template, a row out of range) is an ordinary problem answer. The check per row is the layout check the worker
runs before it asks a product for a number (`Writer.CheckIssue`: fonts only, no Typst process, no slot). The
route is mounted with the previews when the PDF writer can check. The console shows the result per row; a row
can be loaded into the preview row number with one click.

I6 · Tests. DOM tests (jsdom) for the model operations, the pointer paths (events dispatched at coordinates
computed from the canvas's fixed scale; jsdom has no layout), the keyboard, undo/redo and the palette; Go tests
for the check route (authorization like a preview, row bounds and the cap, explicit and boundary sampling,
per-row codes, no Typst); and a real-browser script `scripts/browser_canvas_s03i.mjs` (after
`browser_visual_s03c4.mjs`) that drives Windows Chrome over CDP with real mouse and keyboard input — drag,
resize, column-boundary drag, snap, align, undo, palette, check samples — and takes screenshots at the three
widths, checking the template JSON after each step. The real-browser run is local evidence, not CI.

Not in this slice: bands, wrapping text, barcodes, images, groups, lock, rotation, the template schema 2.0 (its
ADR comes next), background images on the canvas (the background store has no content route yet), templates
from the studio's library.
