// Generic table export of a frozen snapshot. The server owns this template; users never write
// Typst. Every printed string, including the labels, comes from data.json, which the server has
// checked against the fonts. Values are laid out as given: the template computes nothing.
#let d = json("data.json")

// A cell is a string, or a list of strings and {pieces: [...]}: a run that is wider than the
// cell, cut by the server into pieces that each fit one line. Each piece ends its line, so the
// text stays in order even if a piece turned out wider than measured; a line break adds no
// character to the text layer.
#let show-text(c) = if type(c) == str { c } else {
  c.map(p => if type(p) == str { p } else { p.pieces.join(linebreak()) }).join()
}

#set document(title: d.title)
#set text(font: d.fonts, fallback: false, size: 8pt, lang: "zh", region: "cn", hyphenate: false, ligatures: false)
#set par(justify: false)
#set page(
  paper: "a4",
  flipped: d.landscape,
  margin: (x: 12mm, top: 14mm, bottom: 18mm),
  footer: context {
    set text(size: 6.5pt, fill: luma(70))
    grid(
      columns: (1fr, 80pt),
      align: (left + top, right + top),
      show-text(d.footer),
      d.labels.page_before + counter(page).display() + d.labels.page_between + str(counter(page).final().first()) + d.labels.page_after,
    )
  },
)
#set table.cell(breakable: true)

#block(below: 7pt, text(size: 13pt, weight: "bold", show-text(d.heading)))
#block(below: 8pt, text(size: 7.5pt, fill: luma(60), show-text(d.meta)))

#table(
  columns: d.widths.map(w => w * 1pt),
  inset: (x: 4pt, y: 3pt),
  stroke: 0.5pt + luma(150),
  align: (x, y) => (if d.align.at(x) == "right" { right } else { left }) + top,
  fill: (x, y) => if y == 0 { luma(232) },
  table.header(..d.header.map(c => text(weight: "bold", show-text(c)))),
  ..d.rows.map(row => row.map(show-text)).flatten(),
  ..if d.total_row != none {
    (table.hline(stroke: 1pt + luma(60)),) + d.total_row.map(c => text(weight: "bold", show-text(c)))
  } else { () },
)

#if d.empty { text(fill: luma(60), d.labels.empty) }
#if d.total_list != none {
  for c in d.total_list { block(above: 6pt, text(weight: "bold", show-text(c))) }
}
