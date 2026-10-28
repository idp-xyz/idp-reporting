"""Compares where the two engines drew every character of a document expectation's values:
python -I compare.py EXPECTATION.json TYPST.pdf PDFME.pdf. Prints a JSON report: for each
box, the characters both drew and the largest horizontal difference of their origins, in
points (a box whose text differs between the engines is listed without a number)."""
import json
import os
import sys

import pymupdf

MM = 72 / 25.4
ASCENT = 1.16


def chars(path):
    out = []
    for b in pymupdf.open(path)[0].get_text("rawdict")["blocks"]:
        for l in b.get("lines", []):
            for s in l["spans"]:
                if "PREVIEW" in "".join(c["c"] for c in s["chars"]):
                    continue
                for c in s["chars"]:
                    out.append((c["origin"][1], c["origin"][0], c["c"]))
    return out


exp = json.load(open(sys.argv[1], encoding="utf-8"))
typst, pdfme = chars(sys.argv[2]), chars(sys.argv[3])
report = {"expectation": os.path.basename(sys.argv[1]), "boxes": [], "worst_pt": 0}
for e in exp["elements"]:
    base, left = (e["y"] + exp["offset_y"]) * MM + ASCENT * e["size"], (e["x"] + exp["offset_x"]) * MM
    right = left + e["width"] * MM
    pick = lambda cs: sorted([c for c in cs if abs(c[0] - base) < 0.3 and left - 8 <= c[1] <= right + 8 and c[2].strip()], key=lambda c: c[1])
    a, b = pick(typst), pick(pdfme)
    box = {"text": e["text"], "align": e["align"], "size": e["size"], "typst": "".join(c[2] for c in a), "pdfme": "".join(c[2] for c in b)}
    if box["typst"] == box["pdfme"] and a:
        box["max_dx_pt"] = round(max(abs(x[1] - y[1]) for x, y in zip(a, b)), 3)
        report["worst_pt"] = max(report["worst_pt"], box["max_dx_pt"])
    else:
        box["max_dx_pt"] = None
    report["boxes"].append(box)
report["worst_mm"] = round(report["worst_pt"] / MM, 4)
print(json.dumps(report, ensure_ascii=False, indent=1))
