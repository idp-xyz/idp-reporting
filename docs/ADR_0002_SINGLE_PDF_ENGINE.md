# ADR 0002 · One PDF engine: Typst renders document templates too

2026-10-27. Accepted. Extends ADR 0001 (Typst for PDF) and supersedes the engine choice of the S03-D1..D4
document template delivery (commit 79aa396, kept as an archive in `temp/`), which rendered previews with
pdfme 6.2.8 in a local Node process.

## Decision

The platform has one PDF engine, the pinned Typst of ADR 0001. Document template previews are rendered by
export/pdf with the same sandbox, font checks and limits as table exports. pdfme, the Node renderer, the pdfme
designer bundle (7.4 MB of React and Ant Design) and the console's `style-src 'unsafe-inline'` are removed. The
engine-independent parts of that delivery are kept unchanged: the document model and its validation, private
template versions and publication, print offsets and the variable layer, private PNG backgrounds, the
authorization checks, the HTTP routes and the console's own editing controls. Their phases become S03-E1..E4
(docs/S03E_SCOPE.md), so that S03-D1 keeps meaning the table export.

## Reasons

- Two engines would mean two runtimes, two font pipelines, two sets of glyph and width checks, two sandboxes and
  two upgrade gates, for one platform.
- pdfme's limits sit on the roadmap: in the ADR 0001 measurements it truncated a row taller than a page without
  warning, had no carried totals and failed PDF/A-2b, which multi-page documents and official issue need.
- The delivery's value is its business model, not its engine: it kept a platform-owned document schema behind a
  renderer interface, so the engine could be replaced in one package.
- pdfme's own advantage, its drag-and-drop canvas, was qualified only as geometry on blank paper and unverified in
  a browser, at the cost of the bundle and the weaker CSP.

## Placement contract

Previews keep the placement pdfme used, so that templates calibrated on its previews print in the same place.
pdfme set a value as a row of the font's plain advances, with no kerning, spacing, compression or substitution
between characters, its baseline 1.16 font sizes below the top of its box (Noto Sans SC's ascender), starting at
the box's left edge, ending at its right edge or centred, and it did not draw trailing white space. Typst lays
out a line as a whole and cannot be told not to: it puts a quarter em between Chinese and Latin text, halves
Chinese punctuation at a line's ends and between marks, widens dashes in Chinese text and draws a dotted circle
before a stray Hangul tone mark. So export/pdf does not hand Typst the value. It places every grapheme cluster
itself, at the x the fonts' plain advances give it, after an empty inline box (which keeps Typst from trimming
punctuation at the start of the cluster's own line), with the English language setting and no CJK-Latin spacing;
and the template asserts that Typst measures no cluster wider than Go did, so a glyph Typst would add fails the
render as an unsupported value. Measured on 24 boxes of hard values (Chinese next to digits and Latin,
punctuation at the start, in runs and alone, dashes, kerned pairs, combining marks, leading and trailing spaces
of both kinds, every alignment, print offsets, 8 to 28 pt), every character lies within 0.009 pt (0.003 mm) of
where pdfme drew it (evidence/s03e/cross_engine.json). The one difference found: pdfme's shaper replaced two em
dashes in a row by one two-em dash glyph, where Typst draws the two characters as given. Values are never
shrunk, clipped or wrapped: a value that does not fit on one line with 2 pt to spare, or holds a character the
fonts lack, a soft hyphen or a Hangul tone mark, is refused. The preview banner stays at 10 mm, 10 mm on a white
backing.

## Consequences

- Document previews need the same host as PDF export: the Typst binary, its checksum and the font directory
  (-pdf-typst, -pdf-typst-sha256, -pdf-fonts) and unprivileged user namespaces. The -pdf-node and -pdf-script
  flags are gone. Without Typst, template metadata routes still work and previews are not offered.
- Previews are now PDF/A-2b and byte-identical for the same input and clock; they keep leading and repeated
  spaces as given and, like pdfme's, do not draw trailing white space.
- Previews and table exports share the renderer's bounded number of Typst processes (Config.Concurrency,
  default 2), and previews may hold all but one of them, so a burst of previews delays an export by at most one
  render and never fails it. One deadline, the render timeout, covers a render's wait and its run; a preview
  that exceeds it is a budget error. A background is tried with the renderer when it is uploaded, outside the
  store's lock and with the request's context.
- There is no canvas in the console; geometry is edited with the existing millimetre controls. A canvas, if users
  need one, is a later, separately loaded feature written without a framework.
- Revisit only with evidence: if Typst fails a hard requirement of a document feature, measure the alternatives
  again under ADR 0001's conditions.
