// One A4 document preview. The server owns this template and computes every position in
// data.json: each grapheme cluster of a value is placed at the x the server derived from the
// fonts' plain advances, so Typst shapes nothing across clusters and the width the server
// measured is the width drawn. Values are never interpreted as markup.
#let d = json("data.json")

#set document(title: d.title)
#set page(width: 210mm, height: 297mm, margin: 0pt)
// Plain advances only: no kerning, ligatures, overhang, hyphenation or CJK-Latin spacing, and
// the English language setting keeps dashes at their glyph widths. A text's frame starts and
// ends at its baseline, so placing it at a baseline is exact.
#set text(font: d.fonts, fallback: false, lang: "en", hyphenate: false, ligatures: false, kerning: false, overhang: false, top-edge: "baseline", bottom-edge: "baseline", cjk-latin-spacing: none)

#if d.background {
  place(top + left, image("background.png", width: 210mm, height: 297mm, fit: "contain"))
}
// A cluster is drawn after an empty inline box, so that Typst does not trim CJK punctuation at
// the start of its line, which would move the glyph half an em to the left. Typst may measure
// a cluster narrower than the server did (it trims punctuation at the end of a line) but never
// wider: a cluster Typst would draw wider, for instance with a glyph it adds, stops the render.
#let glyph(g) = {
  let t = box() + text(size: g.size * 1pt, fill: rgb("#132238"), g.text)
  context {
    assert(measure(t).width <= (g.width + 0.05) * 1pt, message: "reporting: a glyph is wider than measured")
  }
  place(top + left, dx: g.x * 1pt, dy: g.baseline * 1pt, t)
}
#for g in d.glyphs { glyph(g) }
#let b = d.banner
#place(top + left, dx: b.left * 1pt, dy: b.top * 1pt, rect(width: b.width * 1pt, height: b.height * 1pt, fill: white, stroke: none))
#place(top + left, dx: b.left * 1pt, dy: b.baseline * 1pt, text(size: b.size * 1pt, fill: rgb("#8C3B20"), b.text))
