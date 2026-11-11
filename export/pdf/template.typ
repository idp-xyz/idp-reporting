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

// A presented export (S23-A) may add subtotal rows (bold under a rule, like the total row),
// cell fills for its marks (one colour or "" per cell of each row) and a footnote; without a
// presentation the keys are absent and the document is laid out exactly as before.
#let subtotal-rows = d.at("subtotal_rows", default: ())
#let fills = d.at("fills", default: none)
#let cell-fill(x, y) = if y == 0 { luma(232) } else if fills != none and y - 1 < fills.len() and fills.at(y - 1).at(x) != "" {
  rgb(fills.at(y - 1).at(x))
}

#set document(title: d.title)
// No kerning: the server measures plain advances, and some pairs kern outward. No overhang:
// a full stop or comma at the end of a line would hang into the cell padding, and numbers
// must align exactly right.
#set text(font: d.fonts, fallback: false, size: 8pt, lang: "zh", region: "cn", hyphenate: false, ligatures: false, kerning: false, overhang: false)
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

// A query with filters or a sort (S20-C) adds the scope lines under the meta line, in the same block: each is one line the server
// composed and measured to fit the text width, set on a line of its own. Without them the key is absent and the block is the meta
// line alone, exactly as before.
#let scope = d.at("scope", default: ())

#block(below: 7pt, text(size: 13pt, weight: "bold", show-text(d.heading)))
#block(below: 8pt, text(size: 7.5pt, fill: luma(60), if scope.len() == 0 { show-text(d.meta) } else { show-text(d.meta) + scope.map(line => linebreak() + line).join() }))

#table(
  columns: d.widths.map(w => w * 1pt),
  inset: (x: 4pt, y: 3pt),
  stroke: 0.5pt + luma(150),
  align: (x, y) => (if d.align.at(x) == "right" { right } else if d.align.at(x) == "center" { center } else { left }) + top,
  fill: cell-fill,
  table.header(..d.header.map(c => text(weight: "bold", show-text(c)))),
  ..d.rows.enumerate().map(((i, row)) => if subtotal-rows.contains(i) {
    (table.hline(stroke: 1pt + luma(60)),) + row.map(c => text(weight: "bold", show-text(c)))
  } else { row.map(show-text) }).flatten(),
  ..if d.total_row != none {
    (table.hline(stroke: 1pt + luma(60)),) + d.total_row.map(c => text(weight: "bold", show-text(c)))
  } else { () },
)

#if d.empty { text(fill: luma(60), d.labels.empty) }
#if d.total_list != none {
  for c in d.total_list { block(above: 6pt, text(weight: "bold", show-text(c))) }
}
#if d.at("footnote", default: none) != none { block(above: 6pt, text(size: 7.5pt, fill: luma(60), show-text(d.footnote))) }
