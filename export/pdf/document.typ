// A document preview of one or more A4 pages. The server owns this template and computes every
// position in data.json: each grapheme cluster of a box value, and each line of a table cell,
// is placed at the x the server derived from the fonts' plain advances, so Typst shapes nothing
// across them and the width the server measured is the width drawn. Values are never
// interpreted as markup.
#let d = json("data.json")

#set document(title: d.title)
#set page(width: 210mm, height: 297mm, margin: 0pt)
// Plain advances only: no kerning, ligatures, overhang, hyphenation or CJK-Latin spacing, and
// the English language setting keeps dashes at their glyph widths. A text's frame starts and
// ends at its baseline, so placing it at a baseline is exact.
#set text(font: d.fonts, fallback: false, lang: "en", hyphenate: false, ligatures: false, kerning: false, overhang: false, top-edge: "baseline", bottom-edge: "baseline", cjk-latin-spacing: none)

// A text is drawn after an empty inline box, so that Typst does not trim CJK punctuation at the
// start of its line, which would move the glyph half an em to the left. Typst may measure a
// text narrower than the server did (it trims punctuation at the end of a line) but never
// wider: a text Typst would draw wider, for instance with a glyph it adds, stops the render.
#let glyph(g) = {
  let t = box() + text(size: g.size * 1pt, weight: if g.bold { "bold" } else { "regular" }, fill: rgb("#132238"), g.text)
  context {
    assert(measure(t).width <= (g.width + 0.05) * 1pt, message: "reporting: a glyph is wider than measured")
  }
  place(top + left, dx: g.x * 1pt, dy: g.baseline * 1pt, t)
}
#let b = d.banner
#for (i, pg) in d.pages.enumerate() {
  if i > 0 { pagebreak() }
  if d.background {
    place(top + left, image("background.png", width: 210mm, height: 297mm, fit: "contain"))
  }
  for f in pg.fills { place(top + left, dx: f.x * 1pt, dy: f.y * 1pt, rect(width: f.w * 1pt, height: f.h * 1pt, fill: luma(232), stroke: none)) }
  for c in pg.cells { place(top + left, dx: c.x * 1pt, dy: c.y * 1pt, rect(width: c.w * 1pt, height: c.h * 1pt, fill: none, stroke: 0.5pt + luma(150))) }
  for l in pg.rules { place(top + left, dx: l.x0 * 1pt, dy: l.y0 * 1pt, line(end: ((l.x1 - l.x0) * 1pt, (l.y1 - l.y0) * 1pt), stroke: l.stroke * 1pt + luma(60))) }
  for g in pg.texts { glyph(g) }
  place(top + left, dx: b.left * 1pt, dy: b.top * 1pt, rect(width: b.width * 1pt, height: b.height * 1pt, fill: white, stroke: none))
  place(top + left, dx: b.left * 1pt, dy: b.baseline * 1pt, text(size: b.size * 1pt, fill: rgb("#8C3B20"), b.text))
}
