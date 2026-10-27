"""Check a pdfme probe PDF with the same rules as spikes/pdfeval/eval.py (subset that applies).
Usage: python -I check.py <pdf> <data.json>"""
import json
import re
import sys

import pymupdf

NO_START = set("，。、；：？！）］｝」』】》〉”’·％")
NO_END = set("（［｛「『【《〈“‘")
SPLIT_PAIR = re.compile(r"^(—|…)(?!\1)|(?<!—)—$|(?<!…)…$")
squash = lambda s: re.sub(r"\s+", "", s)

pdf, data = sys.argv[1], json.load(open(sys.argv[2], encoding="utf8"))
doc, rows = pymupdf.open(pdf), data["rows"]
texts = [p.get_text() for p in doc]
page_of = {}
for k, t in enumerate(texts):
    for m in re.findall(r"AR-\d{6}", t):
        page_of.setdefault(m, k)
missing = [r["doc"] for r in rows if r["doc"] not in page_of]
seq = [page_of[r["doc"]] for r in rows if r["doc"] in page_of]


def desc_lines(page):
    head = page.search_for("摘要")
    if not head:
        return []
    x, y = head[0].x0, head[0].y1
    found = []
    for b in page.get_text("dict")["blocks"]:
        for l in b.get("lines", []):
            if x - 5.4 <= l["bbox"][0] <= x + 1.5 and l["bbox"][1] >= y:
                t = "".join(s["text"] for s in l["spans"]).strip()
                if t:
                    found.append((l["bbox"][1], t))
    return [t for _, t in sorted(found)]


stream, pos, not_literal = squash("".join(l for p in doc for l in desc_lines(p))), 0, []
for r in rows:
    s = squash(r["desc"])
    if stream.startswith(s, pos):
        pos += len(s)
    else:
        not_literal.append(r["doc"])
        f = stream.find(s, pos)
        pos = f + len(s) if f >= 0 else pos
lines = [l for p in doc for l in desc_lines(p)]
violations = [l for l in lines if l[0] in NO_START or l[-1] in NO_END or SPLIT_PAIR.search(l)]
fonts = sorted({f"{f[3]} ({f[1]})" for p in doc for f in p.get_fonts(full=True)})
n = doc.page_count
numbering = [k + 1 for k, t in enumerate(texts) if not re.search(rf"第\s*{k + 1}\s*页\s*/\s*共\s*{n}\s*页", t)]
print(json.dumps({
    "pages": n, "rows": len(rows), "rows_missing": missing[:5], "rows_in_order": seq == sorted(seq),
    "header_missing_on_pages": [k + 1 for k, t in enumerate(texts) if not all(h in t for h in ("序号", "摘要", "金额"))][:5],
    "page_numbering_wrong_on": numbering[:5],
    "descriptions_not_complete_and_literal": not_literal[:5], "description_lines": len(lines),
    "line_break_violations": len(violations), "violation_examples": violations[:4],
    "fonts": fonts, "searchable": any(p.search_for("退货冲减") for p in doc),
    "metadata": {k: doc.metadata.get(k) for k in ("title", "producer", "creationDate", "format")},
}, ensure_ascii=False, indent=1))
doc[0].get_pixmap(dpi=90).save(pdf[:-4] + "-p1.png")
