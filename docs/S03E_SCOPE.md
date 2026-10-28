# S03-E1..E4 document templates

2026-10-27. Ported from the S03-D1..D4 delivery (commit 79aa396, archived in `temp/` with its scopes, reviews and
evidence) onto the S03-D1 table export, with the renderer replaced by Typst (ADR 0002). Behaviour is the
delivery's unless stated here.

E1 · Template designer and preview. A platform-owned document model (doctemplates.Document): one A4 page, 1..24
single-line text boxes, each literal text or a binding to a row field or a whole-scope total of the bound dataset;
geometry in millimetres inside a 10..200 × 25..270 safe region, no overlap, 8..28 pt. Strict JSON, 64 KiB. POST
/document-previews takes {config, snapshot_id, row_index}, binds exact strings from the frozen snapshot (never
caller values), checks the owner, current source authority, catalog fields and snapshot integrity before and
after rendering, and returns a PDF with a SHA-256 digest that the console verifies. Every preview carries the
banner "PREVIEW - NOT AN ISSUED DOCUMENT"; nothing is issued or stored.

E2 · Private versions and publication. /document-templates: save, list, explicit version reads, release history,
publish, withdraw and published reads, owner-private with compare-and-set versions; previews of a saved version
render the stored configuration only.

E3 · Preprinted paper. Schema 1.1 adds print {mode: full | variables, offset_x, offset_y} on a 0.1 mm grid with
offsets of -10..10 mm applied at rendering; the variable layer prints bound values only. Schema 1.0 stays
readable.

E4 · PNG backgrounds. Private immutable A4 PNG backgrounds (512 KiB, 210..1240 × 297..1754 px, A4 ratio), decoded
and re-encoded in Go, bound to owner, integration and dataset, referenced by schema 1.2 as {id, sha256}; drawn
fitted and centred in full mode only.

Changed in the port:

- Rendering is export/pdf's Typst (Writer.Render) in the ADR 0001 sandbox, PDF/A-2b, with the placement contract
  of ADR 0002: Go places every grapheme cluster at its plain-advance position, baseline 1.16 font sizes below the
  box top, starting at the left edge, ending at the right edge or centred by alignment; Typst asserts that it
  draws no cluster wider; trailing white space is not drawn.
- Refused with 422 UNSUPPORTED_COMBINATION: characters the fonts lack, soft hyphens, Hangul tone marks (Typst
  adds a dotted circle), values that do not fit their box on one line with 2 pt to spare, a cluster Typst would
  draw wider than measured, and snapshot values the single-line preview cannot print (control characters, DEL,
  more than 256 characters). A literal with such characters is an invalid template (400).
- At most Config.Concurrency (default 2) Typst processes run at once, previews and exports together; a preview
  that waits longer than the render timeout (15 s) for one is 413 BUDGET_EXCEEDED.
- A background is tried with the renderer at upload (Assets.Fits) and refused with the renderer's error when its
  preview would exceed the artifact limit.
- The pdfme canvas, its 7.4 MB bundle, the console font route and the relaxed style CSP are removed. The console
  edits geometry with its millimetre controls and offers the two preview buttons only when the server lists the
  pdf format.
- The demo enables previews with the PDF flags of S03-D1; -template-directory and -background-directory are
  unchanged; -pdf-node and -pdf-script are gone.
- Tests: the delivery's Go model, store, asset and HTTP tests and its console DOM tests run unchanged apart from
  the canvas test, which is dropped; DOM tests that render run with the real Typst (required in CI). New: layout
  unit tests (clusters, trailing space, alignment, refusals, the concurrency slots, the assertion mapping),
  real-Typst document tests with a fourth fixture of hard values, six concurrent renders and the upload fit check,
  and scripts/check_pdf_export.py, which with --font derives every character's position from the font's plain
  advances, fails on ink outside the boxes and the banner, and has negative controls for both (a glyph moved by
  1 pt in the content stream, a shape outside the boxes); and a comparison of 24 boxes against pdfme's output
  (evidence/s03e/cross_engine).

Not in this slice: multi-page documents and tables, carried totals, images other than the background, barcodes,
signatures, official issue records, a canvas editor, physical printer calibration, production qualification.
