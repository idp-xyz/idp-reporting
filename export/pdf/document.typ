// One A4 document preview. The server owns this template and computes every position in
// data.json; values are laid out as given, one line each, and never interpreted as markup.
#let d = json("data.json")

#set document(title: d.title)
#set page(width: 210mm, height: 297mm, margin: 0pt)
// A text's frame starts and ends at its baseline, so placing it at a baseline is exact. No
// kerning or overhang: the server measured plain advances.
#set text(font: d.fonts, fallback: false, lang: "zh", region: "cn", hyphenate: false, ligatures: false, kerning: false, overhang: false, top-edge: "baseline", bottom-edge: "baseline")

#if d.background {
  place(top + left, image("background.png", width: 210mm, height: 297mm, fit: "contain"))
}
#let side(a) = if a == "right" { right } else if a == "center" { center } else { left }
#for e in d.elements {
  place(top + left, dx: e.left * 1pt, dy: e.baseline * 1pt, box(width: e.width * 1pt, align(side(e.align), text(size: e.size * 1pt, fill: rgb("#132238"), e.text))))
}
#let b = d.banner
#place(top + left, dx: b.left * 1pt, dy: b.top * 1pt, rect(width: b.width * 1pt, height: b.height * 1pt, fill: white, stroke: none))
#place(top + left, dx: b.left * 1pt, dy: b.baseline * 1pt, text(size: b.size * 1pt, fill: rgb("#8C3B20"), b.text))
