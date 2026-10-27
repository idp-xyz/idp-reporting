// Receivables statement for the Typst evaluation spike. Every amount and running total
// arrives as an exact string computed in Go; the template only lays them out and refuses
// to finish if its last running total differs from the Go total.
#let data = json("data.json")
#let meta = data.meta

#set document(title: meta.title, author: meta.company, keywords: (meta.snapshot, meta.template))
#set text(font: "Noto Sans SC", lang: "zh", region: "cn", size: 9pt)
#set par(leading: 0.6em)
// Rows may split across pages, so a row taller than a page is continued instead of clipped.
#set table.cell(breakable: true)
// GB/T 15834: the interpunct should not start a line, so keep it with the character before it.
#show regex("[^\\s]·"): box

// Running total of the last row laid out so far. Read in the header it is the total carried
// from the previous page; read in the footer it is the total carried to the next page.
#let cum = state("cum", none)

#set page(
  paper: "a4",
  margin: (top: 32mm, bottom: 26mm, x: 15mm),
  background: if meta.draft { rotate(-30deg, text(72pt, fill: luma(225), weight: "bold")[草稿]) },
  header: context {
    align(center, text(14pt, weight: "bold", meta.title))
    grid(columns: (1fr, 1fr), [单位：#meta.company], align(right)[客户：#meta.customer])
    grid(columns: (1fr, 1fr), [期间：#meta.period], {
      let c = cum.get()
      if c != none { align(right)[承前页累计：#c] }
    })
  },
  footer: context {
    let total = counter(page).final().first()
    if here().page() < total {
      let c = cum.get()
      if c != none { align(right)[过次页累计：#c] }
    }
    grid(
      columns: (1fr, auto, 1fr),
      text(7pt, fill: luma(100))[快照 #meta.snapshot],
      [第 #counter(page).display() 页 / 共 #total 页],
      align(right, text(7pt, fill: luma(100))[模板 #meta.template]),
    )
  },
)

#table(
  columns: (3.4em, 5.8em, 6.6em, 1fr, 8.6em),
  align: (right, left, left, left, right),
  stroke: 0.4pt + luma(150),
  inset: (x: 4pt, y: 3.5pt),
  table.header([*序号*], [*日期*], [*单据号*], [*摘要*], [*金额*]),
  ..data.rows.enumerate().map(((i, r)) => (
    str(i + 1), r.date, r.doc, r.desc, [#r.amount#cum.update(r.cum)],
  )).flatten(),
)

#v(3mm)
#grid(
  columns: (1fr, auto),
  gutter: 4mm,
  [合计（人民币大写）：#data.total_upper],
  [合计：#data.total],
)

#context {
  let last = cum.final()
  if data.rows.len() > 0 and last != data.total {
    panic("running total " + last + " differs from the Go total " + data.total)
  }
}
