"""Check PDF exports against what each must show, independently of the engine that drew them.

For every <name>.json that the export/pdf Typst tests write (REPORTING_PDF_EVIDENCE), the
matching <name>.pdf must:
  - identify itself as PDF/A-2b in its XMP metadata, embed every font and use no Type 3 font;
  - have at least min_pages pages, all in the expected orientation;
  - print each table column, read page by page in drawing order, as exactly the column header on
    every page followed by the expected cells and total in order, ignoring whitespace: nothing
    lost, added, duplicated, reordered or moved to another column, and lines top to bottom;
  - keep every character inside its column and inside the page margins, table text above the
    footer, and no replacement character;
  - print "第 i 页 / 共 N 页" and the snapshot facts in the footer of every page.
With --negative it also corrupts every expectation in several ways and fails unless each
corruption is caught. With --verapdf IMAGE it validates every PDF as PDF/A-2b with veraPDF in
Docker without a network, and a copy stripped of its XMP metadata, which must fail.

Usage: python -I scripts/check_pdf_export.py DIR [--negative] [--verapdf IMAGE]
(needs PyMuPDF: scripts/requirements-pdfcheck.txt)
"""
import argparse
import copy
import json
import os
import re
import subprocess
import sys
import tempfile

import pymupdf

TABLE_SIZE = 8.0
FOOT_SIZE = 6.5
BOTTOM_MARGIN = 18 / 25.4 * 72


def squash(s):
    return re.sub(r"\s+", "", s)


def characters(page):
    """Yields (size, char, bbox, origin) in drawing order."""
    for block in page.get_text("rawdict")["blocks"]:
        for line in block.get("lines", []):
            for span in line["spans"]:
                for c in span["chars"]:
                    yield span["size"], c["c"], c["bbox"], c["origin"]


def check(directory, exp):
    problems = []
    doc = pymupdf.open(os.path.join(directory, exp["file"]))
    xmp = doc.get_xml_metadata()
    if not re.search(r"pdfaid:part[^0-9]{0,4}2\b", xmp) or not re.search(r"pdfaid:conformance[^A-Z]{0,4}B\b", xmp):
        problems.append("no PDF/A-2b identification in the XMP metadata")
    pages = doc.page_count
    if pages < exp["min_pages"]:
        problems.append(f"{pages} pages, expected at least {exp['min_pages']}")
    cols = exp["columns"]
    header = [squash(h) for h in exp["header"]]
    streams = [""] * len(cols)
    tail = ""  # 8 pt text after the table: listed totals or the empty-table label
    expected_tail = "".join(squash(t) for t in exp["totals"]) if exp["totals_listed"] else ""
    expected_tail += squash(exp.get("empty", ""))
    for i, page in enumerate(doc):
        for f in page.get_fonts(full=True):
            if f[1] == "n/a" or f[2] == "Type3":
                problems.append(f"page {i + 1}: font {f[3]} is {f[2]} and embedded as {f[1]}")
        width, height = page.rect.width, page.rect.height
        if (width > height) != exp["landscape"]:
            problems.append(f"page {i + 1}: orientation")
        chunks = [[] for _ in cols]
        footer = []
        table = []
        for size, ch, (x0, y0, x1, y1), origin in characters(page):
            if not ch.strip():
                continue
            if ch == "�":
                problems.append(f"page {i + 1}: a replacement character")
            tolerance = 0.5 * size + 0.5  # CJK punctuation trimmed at a line edge
            if x0 < exp["margin"] - tolerance or x1 > width - exp["margin"] + tolerance or y0 < 0 or y1 > height:
                problems.append(f"page {i + 1}: {ch!r} at {x0:.1f}..{x1:.1f} is outside the margins")
            if abs(size - FOOT_SIZE) < 0.05:
                footer.append(ch)
            elif abs(size - TABLE_SIZE) < 0.05:
                if y1 > height - BOTTOM_MARGIN + 0.5:
                    problems.append(f"page {i + 1}: table text {ch!r} in the footer area")
                table.append((ch, x0, x1, origin[1]))
        if i == pages - 1 and expected_tail:
            # The text after the table is the last expected_tail characters drawn on the page.
            n = len(expected_tail)
            tail, table = "".join(c[0] for c in table[-n:]), table[:-n]
        for ch, x0, x1, base in table:
            centre = (x0 + x1) / 2
            j = next((j for j, (a, b) in enumerate(cols) if a <= centre < b), None)
            if j is None:
                problems.append(f"page {i + 1}: {ch!r} at {x0:.1f} is in no column")
                continue
            if x0 < cols[j][0] - 0.5 or x1 > cols[j][1] + 0.5:
                problems.append(f"page {i + 1}: {ch!r} crosses the edge of column {j + 1}")
            chunks[j].append((ch, base))
        for j, chunk in enumerate(chunks):
            text = "".join(c for c, _ in chunk)
            if any(b < a - 0.5 for (_, a), (_, b) in zip(chunk, chunk[1:])):
                problems.append(f"page {i + 1}: column {j + 1} is not drawn top to bottom")
            if not text.startswith(header[j]):
                problems.append(f"page {i + 1}: column {j + 1} does not start with its header")
            streams[j] += text[len(header[j]):]
        foot = "".join(footer)
        for part in [f"第 {i + 1} 页 / 共 {pages} 页"] + exp["footer"]:
            if squash(part) not in foot:
                problems.append(f"page {i + 1}: footer lacks {part!r}")
    for j in range(len(cols)):
        want = "".join(squash(row[j]) for row in exp["rows"])
        if exp["totals"] and not exp["totals_listed"]:
            want += squash(exp["totals"][j])
        if streams[j] != want:
            at = next((k for k, (a, b) in enumerate(zip(streams[j], want)) if a != b), min(len(streams[j]), len(want)))
            problems.append(f"column {j + 1} differs at character {at} of {len(want)}")
    if tail != expected_tail:
        problems.append("the totals or the empty-table label after the table differ")
    return problems


def corruptions(exp):
    """Yields (name, corrupted expectation) for each corruption that applies."""
    rows = exp["rows"]
    first = next(((r, j) for r, row in enumerate(rows) for j, v in enumerate(row) if squash(v)), None)
    if first:
        e = copy.deepcopy(exp)
        r, j = first
        e["rows"][r][j] = e["rows"][r][j].rstrip()[:-1]
        yield "a cell lost a character", e
    distinct = [k for k in range(1, len(rows)) if [squash(v) for v in rows[k]] != [squash(v) for v in rows[0]]]
    if distinct:
        e = copy.deepcopy(exp)
        k = distinct[0]
        e["rows"][0], e["rows"][k] = e["rows"][k], e["rows"][0]
        yield "two rows swapped", e
    if rows:
        e = copy.deepcopy(exp)
        e["rows"].append(e["rows"][0])
        yield "a row duplicated", e
    if first and len(exp["columns"]) > 1:
        e = copy.deepcopy(exp)
        r, j = first
        k = (j + 1) % len(exp["columns"])
        if squash(e["rows"][r][j]) != squash(e["rows"][r][k]):
            e["rows"][r][j], e["rows"][r][k] = e["rows"][r][k], e["rows"][r][j]
            yield "cells moved between columns", e
    e = copy.deepcopy(exp)
    e["header"][0] += "X"
    yield "a header changed", e
    e = copy.deepcopy(exp)
    e["footer"][0] += "X"
    yield "a footer fact changed", e
    e = copy.deepcopy(exp)
    e["min_pages"] = 10**6
    yield "pages missing", e
    e = copy.deepcopy(exp)
    e["landscape"] = not e["landscape"]
    yield "the orientation flipped", e
    e = copy.deepcopy(exp)
    e["columns"] = [[a + 30, b + 30] for a, b in e["columns"]]
    yield "the columns moved", e
    if exp["totals"]:
        e = copy.deepcopy(exp)
        e["totals"][-1] += "1"
        yield "a total changed", e


def verapdf(image, directory, names):
    """Returns veraPDF's verdict ("PASS" or "FAIL") for each named PDF in directory."""
    p = subprocess.run(["docker", "run", "--rm", "--network", "none", "-v", f"{os.path.abspath(directory)}:/data:ro", image,
                        "--flavour", "2b", "--format", "text", *[f"/data/{n}" for n in names]],
                       capture_output=True, text=True, timeout=1800)
    verdicts = {}
    for line in p.stdout.splitlines():
        parts = line.split()
        if len(parts) >= 2 and parts[1].startswith("/data/"):
            verdicts[parts[1][len("/data/"):]] = parts[0]
    return verdicts


def check_pdfa(image, directory, pdfs):
    failed = False
    verdicts = verapdf(image, directory, pdfs)
    for name in pdfs:
        print(f"{name}: veraPDF PDF/A-2b {verdicts.get(name, 'no verdict')}")
        failed |= verdicts.get(name) != "PASS"
    with tempfile.TemporaryDirectory() as negative:
        doc = pymupdf.open(os.path.join(directory, pdfs[0]))
        doc.del_xml_metadata()
        doc.save(os.path.join(negative, "without-xmp.pdf"))
        os.chmod(negative, 0o755)
        verdict = verapdf(image, negative, ["without-xmp.pdf"]).get("without-xmp.pdf", "no verdict")
        print(f"negative control, {pdfs[0]} without XMP metadata: veraPDF {verdict}")
        failed |= verdict != "FAIL"
    return failed


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("directory")
    ap.add_argument("--negative", action="store_true")
    ap.add_argument("--verapdf", metavar="IMAGE")
    args = ap.parse_args()
    names = sorted(f for f in os.listdir(args.directory) if f.endswith(".json"))
    if not names:
        sys.exit("no expectations found")
    failed = False
    for name in names:
        with open(os.path.join(args.directory, name), encoding="utf-8") as f:
            exp = json.load(f)
        problems = check(args.directory, exp)
        print(f"{exp['file']}: {'ok' if not problems else 'FAILED'}")
        for p in problems[:20]:
            print("  " + p)
        failed |= bool(problems)
        if args.negative:
            for what, corrupted in corruptions(exp):
                caught = bool(check(args.directory, corrupted))
                print(f"  negative control, {what}: {'caught' if caught else 'MISSED'}")
                failed |= not caught
    if args.verapdf:
        pdfs = sorted(f for f in os.listdir(args.directory) if f.endswith(".pdf"))
        failed |= check_pdfa(args.verapdf, args.directory, pdfs)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
