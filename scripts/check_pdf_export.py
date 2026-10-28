"""Check PDF exports against the snapshots they were made from, independently of export/pdf.

The export/pdf Typst tests (REPORTING_PDF_EVIDENCE=DIR) write <name>.pdf and <name>.json: the
snapshot's own values, the column edges and the facts the page furniture must show. This
script derives the printed text itself (thousands separators for metric numbers, header
suffixes, the total row or list, the empty-table label) and requires of each PDF:
  - PDF/A-2b identification in the XMP metadata, every font embedded, no Type 3 font;
  - at least min_pages pages, all in the expected orientation;
  - the dataset title (13 pt) and the meta facts (7.5 pt) on page 1 above the table, and
    "第 i 页 / 共 N 页" with the snapshot facts in the footer (6.5 pt) of every page;
  - each column, read in drawing order, as its header on every page and then every cell and
    the total in order: no character lost, added, duplicated, reordered or in another column; a
    line break where a value has one, a space or line break where it has spaces, no space where
    it has none; lines top to bottom;
  - every non-empty cell of a row starting on the same page and baseline;
  - the totals list and the empty-table label after the table, below its last line;
  - every character inside the page margins and inside its cell's padding (CJK punctuation,
    which Typst may push into the padding, inside the column), table text above the footer,
    text in a dark colour with ink inside its box (not hidden, covered or clipped), the
    characters of a line from left to right, no character drawn over another, no replacement
    character, and no ink at all in the side margins (which catches glyphs the engine adds
    but that are not text, such as dotted circles);
  - the header and the total row in bold, other cells not, and metric numbers aligned right.
Expectations with "kind": "document" describe a document template preview instead: the boxes
of the template in millimetres, the print offsets and the values. Each value must be drawn on
one line, exactly and without its trailing white space, with its baseline 1.16 font sizes
below the top of its box, starting at the box's left edge, ending at its right edge or centred
in it, and, with --font FILE (the Regular face), each of its characters at the position the
font's plain advances give it, so that nothing the engine shapes (kerning, spacing,
punctuation compression, substitution) moved a glyph; the banner "PREVIEW - NOT AN ISSUED
DOCUMENT" at 10 mm, 10 mm; the background PNG, when expected, fitted into the A4 page and
centred; nothing else, and without a background no ink at all outside the boxes and the
banner; with the same font, colour, ink and PDF/A checks as tables.

--golden FILE compares each document's page count and the first row on each page with FILE;
--write-golden FILE records them after a reviewed Typst or font upgrade. --negative corrupts
the expectations and the PDFs in several ways and fails unless each corruption is caught.
--verapdf IMAGE validates every PDF as PDF/A-2b with veraPDF in Docker without a network, and
a copy without XMP metadata, which must fail.

Usage: python -I scripts/check_pdf_export.py DIR [--negative] [--golden FILE | --write-golden FILE]
       [--font FILE] [--verapdf IMAGE]   (needs PyMuPDF: scripts/requirements-pdfcheck.txt)
"""
import argparse
import copy
import decimal
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unicodedata

import pymupdf

TITLE, META, TABLE, FOOT = 13.0, 7.5, 8.0, 6.5
BOTTOM_MARGIN = 18 / 25.4 * 72
TOTAL, EMPTY = "合计", "无数据行"
# Only characters the PDF draws, and ligature characters as they are.
FLAGS = pymupdf.TEXT_INHIBIT_SPACES | pymupdf.TEXT_PRESERVE_WHITESPACE | pymupdf.TEXT_PRESERVE_LIGATURES
INK_SCALE = 3  # pixels per point for the ink check


def squash(s):
    return re.sub(r"\s+", "", s)


def group(v):
    """Thousands separators, written independently of export/pdf."""
    sign, rest = ("-", v[1:]) if v.startswith("-") else ("", v)
    whole, dot, frac = rest.partition(".")
    out = ""
    while len(whole) > 3:
        whole, out = whole[:-3], "," + whole[-3:] + out
    return sign + whole + out + dot + frac


def printed(exp):
    """Returns the headers, rows, total row (or None) and the text after the table."""
    def header(c):
        suffix = " · ".join(x for x in (c["currency"], c["unit"]) if x)
        return c["label"] + (f"（{suffix}）" if suffix else "")

    def cell(j, v):
        return group(v) if exp["metric"][j] else v.replace("\r\n", "\n").replace("\r", "\n").replace("\t", " ")

    heads = [header(c) for c in exp["labels"]]
    rows = [[cell(j, v) for j, v in enumerate(row)] for row in exp["rows"]]
    dims = exp["dimensions"]
    totals = [cell(dims + i, v) for i, v in enumerate(exp["totals"])]
    total_row, after = None, ([] if rows else [EMPTY])
    if totals and dims:
        total_row = [TOTAL] + [""] * (dims - 1) + totals
    elif totals:
        after += [heads[i] + "：" + t for i, t in enumerate(totals)]
    return heads, rows, total_row, after


def cjk_punctuation(c):
    """Punctuation that Typst compresses or pushes into the cell padding in Chinese text,
    including the middle dot."""
    cp = ord(c)
    return unicodedata.category(c).startswith("P") and (cp == 0xB7 or 0x3000 <= cp <= 0x303F or 0xFF00 <= cp <= 0xFFEF or 0x2010 <= cp <= 0x206F)


def characters(page, number):
    """The characters a page draws, in drawing order."""
    out = []
    for block in page.get_text("rawdict", flags=FLAGS)["blocks"]:
        for line in block.get("lines", []):
            for span in line["spans"]:
                rgb = span["color"]
                light = (0.2126 * (rgb >> 16 & 255) + 0.7152 * (rgb >> 8 & 255) + 0.0722 * (rgb & 255)) / 255 > 0.6
                for c in span["chars"]:
                    x0, y0, x1, y1 = c["bbox"]
                    out.append({"c": c["c"], "page": number, "x0": x0, "y0": y0, "x1": x1, "y1": y1, "x": c["origin"][0],
                                "base": c["origin"][1], "size": round(span["size"], 1), "light": light,
                                "bold": "Bold" in span["font"], "n": len(out),
                                "mark": unicodedata.category(c["c"]).startswith("M")})
    return out


def ink_in_margins(page, margin):
    """Whether anything is drawn left or right of the side margins, with 1 pt to spare."""
    pix = page.get_pixmap(matrix=pymupdf.Matrix(2, 2), colorspace=pymupdf.csGRAY)
    buf, w, h = pix.samples, pix.width, pix.height
    left, right = int((margin - 1) * 2), int((page.rect.width - margin + 1) * 2)
    return any(buf[y * w + x] < 128 for y in range(h) for x in [*range(0, max(0, left)), *range(min(w, right), w)])


def inkless(page, chars):
    """Characters with no dark pixel where their glyph is drawn: invisible, covered or clipped.
    The area looked at is the middle of the advance box, from cap height to below the baseline,
    so that neighbours and the lines above and below do not lend it ink."""
    pix = page.get_pixmap(matrix=pymupdf.Matrix(INK_SCALE, INK_SCALE), colorspace=pymupdf.csGRAY)
    buf, w, h = pix.samples, pix.width, pix.height
    found = []
    for c in chars:
        if not c["c"].strip() or c["x1"] - c["x0"] < 0.5:
            continue
        inset = 0.1 * (c["x1"] - c["x0"])
        xs = range(max(0, int((c["x0"] + inset) * INK_SCALE)), min(w, int((c["x1"] - inset) * INK_SCALE) + 1))
        ys = range(max(0, int((c["base"] - 0.95 * c["size"]) * INK_SCALE)), min(h, int((c["base"] + 0.25 * c["size"]) * INK_SCALE) + 1))
        if not any(buf[y * w + x] < 128 for y in ys for x in xs):
            found.append(c)
    return found


def overlaps(chars):
    """Characters drawn over others. Neighbours drawn one after the other on one line may
    overlap: kerning and the compression of punctuation move their advance boxes together."""
    grid, found = {}, []
    for a in chars:
        if not a["c"].strip() or cjk_punctuation(a["c"]):
            continue
        cells = {(gx, gy) for gx in range(int(a["x0"] // 12), int(a["x1"] // 12) + 1)
                 for gy in range(int(a["y0"] // 12), int(a["y1"] // 12) + 1)}
        for key in cells:
            for b in grid.get(key, []):
                w = min(a["x1"], b["x1"]) - max(a["x0"], b["x0"])
                h = min(a["y1"], b["y1"]) - max(a["y0"], b["y0"])
                small = min((a["x1"] - a["x0"]) * (a["y1"] - a["y0"]), (b["x1"] - b["x0"]) * (b["y1"] - b["y0"]))
                neighbours = a["n"] - b["n"] == 1 and abs(a["base"] - b["base"]) < 0.5
                if w > 0 and h > 0 and w * h > 0.25 * small and not neighbours and b not in found:
                    found.append(b)
        for key in cells:
            grid.setdefault(key, []).append(a)
    return found


class Column:
    """A column's characters in drawing order, read cell by cell."""

    def __init__(self, chars):
        self.chars, self.at = chars, 0

    def take(self, text):
        """Consumes text; returns (start (page, baseline) or None, problem or None). Afterwards
        taken holds the characters consumed, and leading whether text starts with a line break,
        which puts its first character below the start of the row."""
        start, prev, gap = None, None, None
        self.taken, self.leading = [], "\n" in text[:len(text) - len(text.lstrip())]
        for ch in text:
            if ch == "\n":
                gap = "line"
                continue
            if ch.isspace():
                gap = gap or "space"
                continue
            spaced = False
            while self.at < len(self.chars) and not self.chars[self.at]["c"].strip():
                spaced, self.at = True, self.at + 1
            if self.at >= len(self.chars):
                return start, f"ends before {ch!r}"
            got = self.chars[self.at]
            if got["c"] != ch:
                return start, f"has {got['c']!r} where {ch!r} belongs (page {got['page'] + 1})"
            if prev is None:
                start = (got["page"], got["base"])
            else:
                broke = (got["page"], round(got["base"], 1)) != (prev["page"], round(prev["base"], 1))
                if gap == "line" and not broke:
                    return start, f"lost a line break before {ch!r}"
                if gap == "space" and not (spaced or broke):
                    return start, f"lost a space before {ch!r}"
                if gap is None and spaced:
                    return start, f"has a space before {ch!r} that the value lacks"
            prev, gap, self.at = got, None, self.at + 1
            self.taken.append(got)
        return start, None

    def rest(self):
        return "".join(c["c"] for c in self.chars[self.at:] if c["c"].strip())


def check(directory, exp):
    """Returns the problems found and the layout summary for the golden file."""
    problems = []
    doc = pymupdf.open(os.path.join(directory, exp["file"]))
    xmp = doc.get_xml_metadata()
    if not re.search(r"pdfaid:part[^0-9]{0,4}2\b", xmp) or not re.search(r"pdfaid:conformance[^A-Z]{0,4}B\b", xmp):
        problems.append("no PDF/A-2b identification in the XMP metadata")
    pages = doc.page_count
    if pages < exp["min_pages"]:
        problems.append(f"{pages} pages, expected at least {exp['min_pages']}")
    heads, rows, total_row, after = printed(exp)
    cols, inset, margin = exp["columns"], exp["inset"], exp["margin"]
    streams = [[] for _ in cols]
    headers_ok = True
    tail, table_bottom = [], 0.0
    for i, page in enumerate(doc):
        for f in page.get_fonts(full=True):
            if f[1] == "n/a" or f[2] == "Type3":
                problems.append(f"page {i + 1}: font {f[3]} is {f[2]}, embedded as {f[1]}")
        width, height = page.rect.width, page.rect.height
        if (width > height) != exp["landscape"]:
            problems.append(f"page {i + 1}: orientation")
        chars = characters(page, i)
        for c in overlaps(chars):
            problems.append(f"page {i + 1}: {c['c']!r} at {c['x0']:.1f},{c['y0']:.1f} is drawn over another character")
        for c in inkless(page, chars)[:5]:
            problems.append(f"page {i + 1}: {c['c']!r} at {c['x0']:.1f},{c['y0']:.1f} has no ink")
        if ink_in_margins(page, margin):
            problems.append(f"page {i + 1}: something is drawn in a side margin")
        # Each line, in drawing order, runs left to right. A combining mark sits over the
        # character before it, and Typst may pull compressed CJK punctuation back.
        lines = {}
        for c in chars:
            if c["c"].strip():
                lines.setdefault((c["size"], round(c["base"], 1)), []).append(c)
        for line in lines.values():
            for a, b in zip(line, line[1:]):
                if b["mark"] or cjk_punctuation(a["c"]) or cjk_punctuation(b["c"]):
                    continue
                if b["x"] < a["x"] - 0.5:
                    problems.append(f"page {i + 1}: {b['c']!r} at {b['x']:.1f} is drawn after {a['c']!r} at {a['x']:.1f}, not left to right")
                    break
        by_size = {}
        for c in chars:
            if c["c"] == "�":
                problems.append(f"page {i + 1}: a replacement character")
            if c["light"] and c["c"].strip():
                problems.append(f"page {i + 1}: {c['c']!r} is drawn in a light colour")
            tolerance = 0.5 * c["size"] + 0.5  # CJK punctuation trimmed at a line edge
            if c["x0"] < margin - tolerance or c["x1"] > width - margin + tolerance or c["y0"] < 0 or c["y1"] > height:
                problems.append(f"page {i + 1}: {c['c']!r} at {c['x0']:.1f}..{c['x1']:.1f} is outside the margins")
            by_size.setdefault(c["size"], []).append(c)
        if set(by_size) - {TITLE, META, TABLE, FOOT}:
            problems.append(f"page {i + 1}: text of unexpected sizes {sorted(set(by_size) - {TITLE, META, TABLE, FOOT})}")
        table = by_size.get(TABLE, [])
        for c in table:
            if c["y1"] > height - BOTTOM_MARGIN + 0.5:
                problems.append(f"page {i + 1}: table text {c['c']!r} in the footer area")
        if i == pages - 1 and after:
            n, k = len(squash("".join(after))), len(table)
            while n and k:
                k -= 1
                n -= bool(table[k]["c"].strip())
            tail, table = table[k:], table[:k]
        chunks = [[] for _ in cols]
        for c in table:
            centre = (c["x0"] + c["x1"]) / 2
            j = next((j for j, (a, b) in enumerate(cols) if a <= centre < b), None)
            if j is None:
                problems.append(f"page {i + 1}: {c['c']!r} at {c['x0']:.1f} is in no column")
                continue
            pad = 0 if cjk_punctuation(c["c"]) else inset
            if c["c"].strip() and (c["x0"] < cols[j][0] + pad - 0.5 or c["x1"] > cols[j][1] - pad + 0.5):
                problems.append(f"page {i + 1}: {c['c']!r} at {c['x0']:.1f}..{c['x1']:.1f} leaves the cell of column {j + 1}")
            chunks[j].append(c)
            table_bottom = max(table_bottom, c["base"]) if i == pages - 1 else table_bottom
        for j, chunk in enumerate(chunks):
            if any(b["base"] < a["base"] - 0.5 for a, b in zip(chunk, chunk[1:])):
                problems.append(f"page {i + 1}: column {j + 1} is not drawn top to bottom")
            column = Column(chunk)
            _, problem = column.take(heads[j])
            if problem:
                problems.append(f"page {i + 1}: the header of column {j + 1} {problem}")
                headers_ok = False
            elif not all(c["bold"] for c in column.taken):
                problems.append(f"page {i + 1}: the header of column {j + 1} is not bold")
            streams[j] += chunk[column.at:]
        first = [c for c in chars if c["size"] in (TITLE, META)]
        if i == 0:
            title = [c for c in first if c["size"] == TITLE]
            meta = [c for c in first if c["size"] == META]
            if squash("".join(c["c"] for c in title)) != squash(exp["title"]):
                problems.append("page 1 lacks the dataset title")
            for fact in exp["meta"]:
                if squash(fact) not in squash("".join(c["c"] for c in meta)):
                    problems.append(f"page 1: the meta line lacks {fact!r}")
            top = min((c["base"] for c in table), default=height)
            if title and meta and not (max(c["base"] for c in title) < min(c["base"] for c in meta) <= max(c["base"] for c in meta) < top):
                problems.append("page 1: the title and the meta line are not above the table in that order")
        elif first:
            problems.append(f"page {i + 1}: the title or the meta line is repeated")
        foot = by_size.get(FOOT, [])
        text = squash("".join(c["c"] for c in foot))
        for fact in [f"第 {i + 1} 页 / 共 {pages} 页"] + exp["footer"]:
            if squash(fact) not in text:
                problems.append(f"page {i + 1}: the footer lacks {fact!r}")
        if foot and table and min(c["base"] for c in foot) <= max(c["base"] for c in table):
            problems.append(f"page {i + 1}: the footer is not below the table")
    all_rows = rows + ([total_row] if total_row else [])
    starts = [[None] * len(cols) for _ in all_rows]
    for j in range(len(cols)):
        if not headers_ok:
            break
        column = Column(streams[j])
        for r, row in enumerate(all_rows):
            start, problem = column.take(row[j])
            if problem:
                problems.append(f"column {j + 1}, row {r + 1}: the cell {problem}")
                break
            starts[r][j] = start and (start[0], start[1], column.leading)
            bold = r == len(rows)  # the total row
            if any(c["bold"] != bold for c in column.taken):
                problems.append(f"column {j + 1}, row {r + 1}: the cell is {'not ' if bold else ''}bold")
            if exp["metric"][j] and column.taken and max(c["x1"] for c in column.taken) < cols[j][1] - inset - 1:
                problems.append(f"column {j + 1}, row {r + 1}: the number is not aligned right")
        else:
            if column.rest():
                problems.append(f"column {j + 1} has text after its last cell: {column.rest()[:20]!r}")
    row_start = []
    for r, row in enumerate(starts):
        seen = [s for s in row if s is not None]
        first = [s for s in seen if not s[2]] or seen
        row_start.append(min(first)[:2] if first else None)
        if any(s[0] != first[0][0] or abs(s[1] - first[0][1]) > 0.5 for s in first if not s[2]):
            problems.append(f"row {r + 1}: its cells do not start on one baseline")
        if any(s[2] and (s[0], s[1]) <= min(first)[:2] for s in seen if [x for x in seen if not x[2]]):
            problems.append(f"row {r + 1}: a value that starts with a line break does not start below the row")
    if squash("".join(c["c"] for c in tail)) != squash("".join(after)):
        problems.append("the totals or the empty-table label after the table differ")
    elif tail and min(c["base"] for c in tail) <= table_bottom:
        problems.append("the text after the table is not below it")
    first_rows = [next((r for r, s in enumerate(row_start) if s and s[0] == p), None) for p in range(pages)]
    return problems, {"pages": pages, "first_rows": first_rows}


MM = 72 / 25.4
ASCENT = 1.16  # baseline below the top of a box, in font sizes (the document contract)
BANNER = "PREVIEW - NOT AN ISSUED DOCUMENT"


def clusters(text):
    """Grapheme clusters as the renderer forms them: a character and the marks after it."""
    out = []
    for ch in text:
        if out and unicodedata.category(ch).startswith("M"):
            out[-1] += ch
        else:
            out.append(ch)
    return out


def cluster_positions(font, text, size, left, right, align):
    """Where each cluster of a value starts, from the font's plain advances alone."""
    pieces = clusters(text)
    widths = [font.text_length(c, fontsize=size) for c in pieces]
    total = sum(widths)
    x = {"left": left, "right": right - total, "center": (left + right) / 2 - total / 2}[align]
    out = []
    for c, w in zip(pieces, widths):
        out.append((c[0], x))
        x += w
    return out


def ink_outside(page, rects, tolerance=1.5):
    """Whether anything is drawn outside the given rectangles, each widened by tolerance pt."""
    scale = 2
    pix = page.get_pixmap(matrix=pymupdf.Matrix(scale, scale), colorspace=pymupdf.csGRAY)
    buf, w, h = bytearray(pix.samples), pix.width, pix.height
    for rect in rects:
        x0, x1 = max(0, int((rect.x0 - tolerance) * scale)), min(w, int((rect.x1 + tolerance) * scale) + 1)
        for y in range(max(0, int((rect.y0 - tolerance) * scale)), min(h, int((rect.y1 + tolerance) * scale) + 1)):
            buf[y * w + x0:y * w + x1] = b"\xff" * (x1 - x0)
    return min(buf) < 128


def document_rects(exp):
    """The banner, the boxes and the table region of a document, in points, with the offsets."""
    ox, oy = exp["offset_x"], exp["offset_y"]
    return [pymupdf.Rect(10 * MM, 10 * MM, 200 * MM, 18 * MM)] + [
        pymupdf.Rect((e["x"] + ox) * MM, (e["y"] + oy) * MM, (e["x"] + ox + e["width"]) * MM, (e["y"] + oy + e["height"]) * MM) for e in exp["elements"]] + (
        [table_region(exp)] if exp.get("table") else [])


def check_document(directory, exp, font=None):
    """Checks a document template preview; returns problems and the layout summary. With font,
    the path of the Regular face, every character must sit at its plain-advance position. A
    document with a table has as many pages as its rows need; every page carries the banner,
    the text boxes and the background, and the table is checked across the pages."""
    problems = []
    metrics = faces(font)
    doc = pymupdf.open(os.path.join(directory, exp["file"]))
    xmp = doc.get_xml_metadata()
    if not re.search(r"pdfaid:part[^0-9]{0,4}2\b", xmp) or not re.search(r"pdfaid:conformance[^A-Z]{0,4}B\b", xmp):
        problems.append("no PDF/A-2b identification in the XMP metadata")
    table = exp.get("table")
    if table is None and doc.page_count != 1:
        return problems + [f"{doc.page_count} pages, expected one"], {"pages": doc.page_count}
    region = table_region(exp) if table else None
    pages = []
    for i, page in enumerate(doc):
        page_problems, chars, used = check_page(directory, page, i, exp, metrics, region)
        problems += [f"page {i + 1}: {p}" for p in page_problems]
        pages.append((page, chars, used))
    layout = {"pages": doc.page_count}
    if table:
        problems += check_table(exp, pages, metrics, layout)
    for i, (page, chars, used) in enumerate(pages):
        rest = [c for c in chars if c["n"] not in used and c["c"].strip()]
        if rest:
            problems.append(f"page {i + 1}: unexpected text {''.join(c['c'] for c in rest)[:40]!r}")
        if not exp["background"] and ink_outside(page, document_rects(exp)):
            problems.append(f"page {i + 1}: ink outside the boxes and the banner")
    return problems, layout


def faces(font):
    """The Regular face given with --font and the Bold face next to it, by weight; None without."""
    if not font:
        return None
    bold = font.replace("Regular", "Bold")
    return {False: pymupdf.Font(fontfile=font), True: pymupdf.Font(fontfile=bold) if bold != font and os.path.exists(bold) else None}


def table_region(exp):
    t = exp["table"]
    return pymupdf.Rect((t["x"] + exp["offset_x"]) * MM, (t["y"] + exp["offset_y"]) * MM, (t["x"] + exp["offset_x"] + t["width"]) * MM, (t["y"] + exp["offset_y"] + t["height"]) * MM)


def check_page(directory, page, number, exp, metrics, region):
    """Checks one page: A4, fonts, ink and colour, the banner and every text box at its place,
    the background; returns problems, the characters and the characters accounted for. Text
    inside the table region is left to check_table."""
    problems = []
    if abs(page.rect.width - 210 * MM) > 0.1 or abs(page.rect.height - 297 * MM) > 0.1:
        problems.append("the page is not A4 portrait")
    for f in page.get_fonts(full=True):
        if f[1] == "n/a" or f[2] == "Type3":
            problems.append(f"font {f[3]} is {f[2]}, embedded as {f[1]}")
    chars = characters(page, number)
    for c in overlaps(chars):
        problems.append(f"{c['c']!r} is drawn over another character")
    for c in inkless(page, chars)[:5]:
        problems.append(f"{c['c']!r} at {c['x0']:.1f},{c['y0']:.1f} has no ink")
    for c in chars:
        if c["light"] and c["c"].strip():
            problems.append(f"{c['c']!r} is drawn in a light colour")
        if c["c"] == "\ufffd":
            problems.append("a replacement character")
    used = set()
    wanted = [(BANNER, 10, 10, 190, 10, "left")] + [(e["text"], e["x"] + exp["offset_x"], e["y"] + exp["offset_y"], e["width"], e["size"], e["align"]) for e in exp["elements"]]
    for text, x, y, width, size, align in wanted:
        base, left, right = y * MM + ASCENT * size, x * MM, (x + width) * MM
        # The characters drawn on this baseline, at this size, within the box.
        line = [c for c in chars if c["n"] not in used and abs(c["base"] - base) <= 0.1 and abs(c["size"] - size) < 0.05 and left - 1 <= (c["x0"] + c["x1"]) / 2 <= right + 1]
        if not text.strip():
            continue  # nothing is drawn for white space alone
        if "".join(c["c"] for c in line).rstrip() != text.rstrip():
            problems.append(f"{text!r} is not drawn on one line with its baseline at {base:.2f} pt in its box")
            continue
        used.update(c["n"] for c in line)
        problems += check_line(line, text.rstrip(), size, left, right, align, metrics)
    if region is not None:
        for c in chars:
            if region.x0 - 1 <= (c["x0"] + c["x1"]) / 2 <= region.x1 + 1 and region.y0 - 1 <= c["base"] <= region.y1 + 1:
                used.add(c["n"])
    images = page.get_images(full=True)
    if len(images) != int(exp["background"]):
        problems.append(f"{len(images)} images, expected {int(exp['background'])}")
    for img in images:
        w, h = img[2], img[3]
        scale = min(210 * MM / w, 297 * MM / h)
        x0, y0 = (210 * MM - w * scale) / 2, (297 * MM - h * scale) / 2
        for rect in page.get_image_rects(img[0]):
            if max(abs(rect.x0 - x0), abs(rect.y0 - y0), abs(rect.x1 - x0 - w * scale), abs(rect.y1 - y0 - h * scale)) > 0.5:
                problems.append(f"the background is drawn at {rect}, not fitted and centred")
        if exp.get("background_file") and not same_picture(pymupdf.Pixmap(page.parent, img[0]), pymupdf.Pixmap(os.path.join(directory, exp["background_file"]))):
            problems.append("the background is not the PNG named")
    return problems, chars, used


def check_line(line, text, size, left, right, align, metrics, bold=False):
    """Checks one drawn line of text against its box edges and, with metrics, the position
    the plain advances of the face of its weight give every character."""
    problems = []
    metrics = metrics.get(bold) if metrics else None
    solid = [c for c in line if c["c"].strip()]
    start, end = line[0]["x"], solid[-1]["x1"]
    bases = [c for c in line if not c["mark"]]
    if any(b["x"] < a["x"] for a, b in zip(bases, bases[1:])):
        problems.append(f"{text!r} is not drawn left to right")
    # Leading spaces count: the value is centred as a whole, as pdfme centred it.
    if align == "left" and abs(start - left) > 0.3 or align == "right" and abs(end - right) > 0.3 or align == "center" and abs((start + end) / 2 - (left + right) / 2) > 0.5:
        problems.append(f"{text!r} is not aligned {align} in its box ({start:.2f}..{end:.2f} pt, box {left:.2f}..{right:.2f})")
    if end > right + 0.3 or start < left - 0.3:
        problems.append(f"{text!r} leaves its box")
    if metrics is not None:
        # Every cluster where the plain advances put it: the engine shaped nothing across them.
        expected = cluster_positions(metrics, text, size, left, right, align)
        if len(bases) != len(expected):
            problems.append(f"{text!r}: {len(bases)} characters drawn, {len(expected)} expected")
        for c, (ch, x) in zip(bases, expected):
            if abs(c["x"] - x) > 0.1:
                problems.append(f"{text!r}: {c['c']!r} at {c['x']:.2f} pt is not at its plain-advance position {x:.2f} pt")
                break
    return problems


TABLE_INSET = 3.0
FORWARD, CARRY, TOTAL = "承前（第 ", "过次（转第 ", "合计"


def group_digits(v):
    """Thousands separators in the integer part of an exact decimal string, as the export prints."""
    sign, rest = ("-", v[1:]) if v.startswith("-") else ("", v)
    whole, dot, frac = rest.partition(".")
    out = ""
    for i, ch in enumerate(whole):
        if i and (len(whole) - i) % 3 == 0:
            out += ","
        out += ch
    return sign + out + dot + frac


def exact_sum(values, scale):
    """The exact sum of decimal strings, with the longest fraction seen or the column's scale."""
    frac = max([scale] + [len(v.partition(".")[2]) for v in values])
    total = sum(decimal.Decimal(v) for v in values) if values else decimal.Decimal(0)
    return format(total.quantize(decimal.Decimal(1).scaleb(-frac)) if frac else total, "f")


def check_table(exp, pages, metrics, layout):
    """Checks the table over the pages: the header on every page (full mode), every row's
    cells complete, in order, on one page, each line inside its column at its plain-advance
    position, the brought-forward, carry-forward and total rows with exactly recomputed sums,
    and the snapshot's totals equal to the sums. Records the first row of each page."""
    problems = []
    t = exp["table"]
    region = table_region(exp)
    size = t["size"]
    edges, x = [], region.x0
    for col in t["columns"]:
        edges.append((x, x + col["width"] * MM))
        x += col["width"] * MM
    full = exp.get("mode", "full") == "full"
    metric = [c["metric"] for c in t["columns"]]
    rows = t["rows"]
    expected = [[squash(group_digits(v) if metric[j] else v) for j, v in enumerate(row)] for row in rows]
    sums = lambda upto: [group_digits(exact_sum([r[j] for r in rows[:upto]], t["columns"][j]["scale"])) if metric[j] and j > 0 else "" for j in range(len(edges))]
    k, acc = -1, None  # the current row and its accumulated cell texts
    first_rows, row_page = [], {}
    total_seen = False
    for number, (page, chars, _) in enumerate(pages, 1):
        inside = [c for c in chars if region.x0 - 1 <= (c["x0"] + c["x1"]) / 2 <= region.x1 + 1 and region.y0 - 1 <= c["base"] <= region.y1 + 1]
        for c in inside:
            if c["x0"] < region.x0 - 0.3 or c["x1"] > region.x1 + 0.3 or c["y0"] < region.y0 - 0.3 or c["y1"] > region.y1 + 0.3:
                problems.append(f"page {number}: {c['c']!r} leaves the table region")
                break
        # Baseline groups, top to bottom; each with its text per column.
        groups = {}
        for c in inside:
            if abs(c["size"] - size) > 0.05:
                problems.append(f"page {number}: {c['c']!r} at {c['size']} pt in the table")
                break
            groups.setdefault(round(c["base"], 1), []).append(c)
        lines = []
        for base in sorted(groups):
            cells = {}
            for c in sorted(groups[base], key=lambda c: c["x"]):
                j = next((j for j, (a, b) in enumerate(edges) if a - 0.5 <= (c["x0"] + c["x1"]) / 2 < b + 0.5), None)
                if j is None:
                    problems.append(f"page {number}: {c['c']!r} at {c['x']:.1f} is in no column")
                    continue
                cells.setdefault(j, []).append(c)
            bold = all(c["bold"] for cs in cells.values() for c in cs)
            if not bold and any(c["bold"] for cs in cells.values() for c in cs):
                problems.append(f"page {number}: a line mixes bold and regular text")
            lines.append((base, bold, cells))
        for base, bold, cells in lines:
            for j, cs in cells.items():
                text = "".join(c["c"] for c in cs)
                if text.strip():
                    left, right = edges[j][0] + TABLE_INSET, edges[j][1] - TABLE_INSET
                    problems += [f"page {number}: {p}" for p in check_line(cs, text.rstrip(), size, left, right, "right" if metric[j] else "left", metrics, bold)]
        text_of = lambda cells, j: "".join(c["c"] for c in cells.get(j, []))
        # Leading bold lines: the header (full mode) and, from page 2 on, the brought-forward row.
        leading = []
        while lines and lines[0][1]:
            leading.append(lines.pop(0))
        forward = leading.pop() if leading and text_of(leading[-1][2], 0).startswith(FORWARD) else None
        header = ["".join(text_of(cells, j) for _, _, cells in leading) for j in range(len(edges))]
        if full:
            for j, col in enumerate(t["columns"]):
                if squash(header[j]) != squash(col["label"]):
                    problems.append(f"page {number}: column {j + 1} header {header[j]!r}, expected {col['label']!r}")
        elif leading:
            problems.append(f"page {number}: a header in the variable layer")
        if number > 1:
            want = sums(k + 1)
            if forward is None:
                problems.append(f"page {number}: no brought-forward row")
            else:
                if text_of(forward[2], 0) != FORWARD + str(number) + " 页）":
                    problems.append(f"page {number}: brought-forward label {text_of(forward[2], 0)!r}")
                for j in range(1, len(edges)):
                    if text_of(forward[2], j).strip() != want[j]:
                        problems.append(f"page {number}: brought forward {text_of(forward[2], j)!r} in column {j + 1}, expected {want[j]!r}")
        elif forward is not None:
            problems.append("page 1: a brought-forward row")
        # The trailing bold line: carry-forward, or the total on the last page.
        trailing = lines.pop() if lines and lines[-1][1] else None
        if any(bold for _, bold, _ in lines):
            problems.append(f"page {number}: bold text among the rows")
        # The rows: each line continues the current row's cells or starts the next row.
        page_first = None
        for base, _, cells in lines:
            if acc is None or all(acc[j] == expected[k][j] for j in range(len(edges))):
                k += 1
                if k >= len(rows):
                    problems.append(f"page {number}: text after the last row: {[text_of(cells, j) for j in cells][:3]!r}")
                    break
                acc = [""] * len(edges)
                row_page[k] = number
                if page_first is None:
                    page_first = k
            elif row_page.get(k) != number:
                problems.append(f"row {k + 1} is split over pages {row_page.get(k)} and {number}")
                row_page[k] = number
            for j, cs in cells.items():
                piece = squash("".join(c["c"] for c in cs))
                if not (expected[k][j].startswith(acc[j] + piece)):
                    problems.append(f"row {k + 1}, column {j + 1}: {''.join(c['c'] for c in cs)!r} does not continue {rows[k][j]!r}")
                else:
                    acc[j] += piece
        first_rows.append(page_first)
        last = number == len(pages)
        if trailing is None:
            problems.append(f"page {number}: no {'total' if last else 'carry-forward'} row")
            continue
        label = text_of(trailing[2], 0)
        want = sums(k + 1)
        if last:
            if label != TOTAL:
                problems.append(f"page {number}: total label {label!r}")
            total_seen = True
        elif label != CARRY + str(number + 1) + " 页）":
            problems.append(f"page {number}: carry-forward label {label!r}")
        for j in range(1, len(edges)):
            if text_of(trailing[2], j).strip() != want[j]:
                problems.append(f"page {number}: {'total' if last else 'carried'} {text_of(trailing[2], j)!r} in column {j + 1}, expected {want[j]!r}")
    if k + 1 != len(rows) or (acc is not None and any(acc[j] != expected[k][j] for j in range(len(edges)))):
        problems.append(f"{k + 1} rows drawn complete, {len(rows)} expected")
    if not total_seen:
        problems.append("no total row")
    for j, col in enumerate(t["columns"]):
        if metric[j] and j > 0 and col["field"] in t["totals"]:
            if decimal.Decimal(t["totals"][col["field"]]) != decimal.Decimal(exact_sum([r[j] for r in rows], col["scale"])):
                problems.append(f"column {j + 1}: the snapshot total {t['totals'][col['field']]} is not the sum of the rows")
    layout["first_rows"] = first_rows
    return problems


def same_picture(a, b):
    """Whether two pixmaps show the same picture: equal size and RGB samples."""
    def rgb(p):
        if p.alpha:
            p = pymupdf.Pixmap(p, 0)
        if p.colorspace is None or p.colorspace.n != 3:
            p = pymupdf.Pixmap(pymupdf.csRGB, p)
        return p
    a, b = rgb(a), rgb(b)
    return (a.width, a.height) == (b.width, b.height) and a.samples == b.samples


def document_corruptions(exp):
    def changed(name, change):
        e = copy.deepcopy(exp)
        change(e)
        return name, e
    yield changed("a value changed", lambda e: e["elements"][0].__setitem__("text", e["elements"][0]["text"] + "X"))
    yield changed("the offset moved by 0.2 mm", lambda e: e.__setitem__("offset_y", e["offset_y"] + 0.2))
    yield changed("a box moved by 0.2 mm", lambda e: e["elements"][0].__setitem__("x", e["elements"][0]["x"] + 0.2))
    flip = {"left": "right", "right": "center", "center": "left"}
    yield changed("an alignment changed", lambda e: e["elements"][0].__setitem__("align", flip[e["elements"][0]["align"]]))
    yield changed("a value missing", lambda e: e["elements"].pop())
    yield changed("a value added", lambda e: e["elements"].append({"text": "X", "x": 20, "y": 200, "width": 50, "height": 10, "size": 10, "align": "left"}))
    yield changed("the background flag flipped", lambda e: e.__setitem__("background", not e["background"]))
    if exp.get("table"):
        yield changed("a table cell changed", lambda e: e["table"]["rows"][0].__setitem__(0, e["table"]["rows"][0][0] + "X"))
        yield changed("a table row removed", lambda e: e["table"]["rows"].pop())
        yield changed("two table rows swapped", lambda e: e["table"]["rows"].__setitem__(slice(0, 2), [e["table"]["rows"][1], e["table"]["rows"][0]]))
        yield changed("a table row added", lambda e: e["table"]["rows"].append(list(e["table"]["rows"][0])))
        yield changed("a table total changed", lambda e: e["table"]["totals"].__setitem__(next(iter(e["table"]["totals"])), "1.00"))
        yield changed("a table amount changed", lambda e: e["table"]["rows"][1].__setitem__(2, "0.01"))
        if exp.get("mode", "full") == "full":
            yield changed("a column label changed", lambda e: e["table"]["columns"][0].__setitem__("label", e["table"]["columns"][0]["label"] + "X"))
        yield changed("the table moved by 0.5 mm", lambda e: e["table"].__setitem__("x", e["table"]["x"] + 0.5))
        yield changed("a column width changed", lambda e: (e["table"]["columns"][0].__setitem__("width", e["table"]["columns"][0]["width"] + 5), e["table"]["columns"][1].__setitem__("width", e["table"]["columns"][1]["width"] - 5)))


def document_pdf_corruptions(directory, exp, scratch, font_file=None):
    font = pymupdf.Font("cjk")

    def copy_with(name, change):
        doc = pymupdf.open(os.path.join(directory, exp["file"]))
        change(doc)
        target = os.path.join(scratch, name)
        os.makedirs(target, exist_ok=True)
        doc.save(os.path.join(target, exp["file"]))
        if exp.get("background_file"):
            shutil.copy(os.path.join(directory, exp["background_file"]), target)
        return target

    def text(t, at, color=(0, 0, 0), embedded=True):
        def change(doc):
            p = doc[0]
            if embedded:
                p.insert_font(fontname="Fx", fontbuffer=font.buffer)
                p.insert_text(at, t, fontname="Fx", fontsize=10, color=color)
            else:
                p.insert_text(at, t, fontname="helv", fontsize=10, color=color)
        return change

    e = exp["elements"][0]
    top = (e["y"] + exp["offset_y"]) * MM
    # Below the safe region, outside every box and the table.
    free = (105 * MM, 285 * MM)
    yield "extra text", "unexpected text", copy_with("extra", text("X", free))
    yield "a font that is not embedded", "embedded as n/a", copy_with("font", text("X", free, embedded=False))

    def cover(doc):
        doc[0].draw_rect(pymupdf.Rect(0, top, 210 * MM, top + ASCENT * e["size"] + 0.3 * e["size"]), color=None, fill=(1, 1, 1), overlay=True)
    yield "a value covered by a white box", "has no ink", copy_with("cover", cover)
    yield "the XMP metadata removed", "PDF/A-2b identification", copy_with("xmp", lambda doc: doc.del_xml_metadata())
    if not exp["background"]:
        yield "a shape outside the boxes", "outside the boxes", copy_with("outside", lambda doc: doc[0].draw_circle((105 * MM, 285 * MM), 3, color=(0, 0, 0), fill=(0, 0, 0)))
    yield "an extra page", "no total row" if exp.get("table") else "pages, expected one", copy_with("pages", lambda doc: doc.new_page())
    yield "a smaller page", "not A4", copy_with("mediabox", lambda doc: doc[0].set_mediabox(pymupdf.Rect(0, 0, 500, 700)))
    yield "a value in a light colour", "light colour", copy_with("light", text("X", free, color=(0.9, 0.9, 0.9)))
    yield "a replacement character", "replacement character", copy_with("fffd", text("\ufffd", free))

    def value_line(page, element):
        base = (element["y"] + exp["offset_y"]) * MM + ASCENT * element["size"]
        left, right = (element["x"] + exp["offset_x"]) * MM, (element["x"] + exp["offset_x"] + element["width"]) * MM
        return sorted([c for c in characters(page, 0) if abs(c["base"] - base) <= 0.1 and left - 1 <= c["x"] <= right + 1 and c["c"].strip() and not c["mark"]], key=lambda c: c["x"])

    def move_glyphs(doc, moves):
        """Changes the x of the engine's own transformations ("q 1 0 0 -1 x y cm") for the given
        characters: moves maps (x, y) in PDF coordinates to a new x."""
        page = doc[0]
        page.clean_contents()
        xref = page.get_contents()[0]
        raw = doc.xref_stream(xref).decode("latin1")

        def move(m):
            x, y = float(m.group(1)), float(m.group(2))
            for (wx, wy), nx in moves:
                if abs(x - wx) < 0.01 and abs(y - wy) < 0.01:
                    return f"1 0 0 -1 {nx:.6f} {m.group(2)} cm"
            return m.group(0)
        moved = re.sub(r"1 0 0 -1 (\S+) (\S+) cm", move, raw)
        assert moved != raw, "the glyphs' transformations were not found"
        doc.update_stream(xref, moved.encode("latin1"))

    def pdf_point(c, page):
        return (c["x"], page.rect.height - c["base"])

    def swap(doc):
        page = doc[0]
        line = value_line(page, e)
        a, b = line[0], line[1]
        move_glyphs(doc, [(pdf_point(a, page), b["x"]), (pdf_point(b, page), a["x"])])
    if len(e["text"].rstrip()) >= 2:
        yield "two glyphs swapped", "left to right", copy_with("swap", swap)
    right = next((el for el in exp["elements"] if el["align"] == "right" and len(el["text"].rstrip()) >= 1), None)

    def past_edge(doc):
        page = doc[0]
        c = value_line(page, right)[-1]
        move_glyphs(doc, [(pdf_point(c, page), c["x"] + 0.6)])
    if right is not None:
        yield "the last glyph of a right-aligned value 0.6 pt past the edge", "leaves its box", copy_with("edge", past_edge)
    if exp["background"]:
        def shift_image(doc):
            page = doc[0]
            page.clean_contents()
            xref = page.get_contents()[0]
            raw = doc.xref_stream(xref).decode("latin1")
            moved, n = re.subn(r"(\S+) 0 0 (\S+) (\S+) (\S+) cm(\s*/\S+ Do)", lambda m: f"{m.group(1)} 0 0 {m.group(2)} {float(m.group(3)) + 2:.6f} {m.group(4)} cm{m.group(5)}", raw)
            assert n == 1, f"{n} image placements found"
            doc.update_stream(xref, moved.encode("latin1"))
        yield "the background shifted by 2 pt", "not fitted and centred", copy_with("image", shift_image)
        if exp.get("background_file"):
            def replace_image(doc):
                page = doc[0]
                xref = page.get_images(full=True)[0][0]
                other = pymupdf.Pixmap(pymupdf.csRGB, pymupdf.Pixmap(os.path.join(directory, exp["background_file"])))
                other.invert_irect(pymupdf.IRect(0, 0, other.width // 2, other.height // 2))
                page.replace_image(xref, pixmap=other)
            yield "the background replaced by another picture", "not the PNG named", copy_with("picture", replace_image)

    def shift(doc):
        # The second character of the first value moved 1 pt to the right in the content stream,
        # where the engine placed it with its own transformation ("q 1 0 0 -1 x y cm").
        page = doc[0]
        base = top + ASCENT * e["size"]
        line = sorted([c for c in characters(page, 0) if abs(c["base"] - base) <= 0.1 and c["c"].strip() and not c["mark"]], key=lambda c: c["x"])
        c = line[1]
        page.clean_contents()
        xref = page.get_contents()[0]
        raw = doc.xref_stream(xref).decode("latin1")

        def move(m):
            x, y = float(m.group(1)), float(m.group(2))
            if abs(x - c["x"]) < 0.01 and abs(y - (page.rect.height - c["base"])) < 0.01:
                return f"1 0 0 -1 {x + 1:.6f} {m.group(2)} cm"
            return m.group(0)
        moved = re.sub(r"1 0 0 -1 (\S+) (\S+) cm", move, raw)
        assert moved != raw, "the glyph's transformation was not found"
        doc.update_stream(xref, moved.encode("latin1"))
    if font_file and len(e["text"].rstrip()) >= 2 and not exp["background"]:
        yield "a glyph moved by 1 pt", "plain-advance position", copy_with("shift", shift)
    if exp.get("table"):
        yield "the last page removed", "rows drawn complete", copy_with("lastpage", lambda doc: doc.delete_page(doc.page_count - 1))
        region = table_region(exp)
        size = exp["table"]["size"]

        def cover_carry(doc):
            # The carry-forward amounts of page 1: the last bold line of the region, right column.
            page = doc[0]
            bold = [c for c in characters(page, 0) if c["bold"] and region.y0 <= c["base"] <= region.y1 and c["c"].strip()]
            base = max(c["base"] for c in bold)
            page.draw_rect(pymupdf.Rect(region.x0 + region.width / 2, base - ASCENT * size - 1, region.x1 - 0.6, base + descent_of(size)), color=None, fill=(1, 1, 1), overlay=True)
        yield "the carried amount covered", "no ink", copy_with("carry", cover_carry)

        def first_body_line(page):
            body = [c for c in characters(page, 0) if not c["bold"] and region.y0 <= c["base"] <= region.y1 and c["c"].strip() and abs(c["size"] - size) < 0.05]
            top = min(c["base"] for c in body)
            return sorted([c for c in body if abs(c["base"] - top) <= 0.1], key=lambda c: c["x"])

        def shift_cell(doc):
            page = doc[0]
            line = first_body_line(page)
            move_glyphs(doc, [((line[0]["x"], page.rect.height - line[0]["base"]), line[0]["x"] + exp["table"]["columns"][0]["width"] * MM)])
        yield "a cell line moved into the next column", "does not continue", copy_with("cellshift", shift_cell)
        if doc_pages(directory, exp) > 1 and exp.get("mode", "full") == "full":
            def cover_header(doc):
                page = doc[1]
                page.draw_rect(pymupdf.Rect(region.x0 - 0.6, region.y0 - 0.6, region.x1 + 0.6, region.y0 + 2 + ASCENT * size + descent_of(size)), color=None, fill=(1, 1, 1), overlay=True)
            yield "the header of page 2 covered", "no ink", copy_with("header2", cover_header)


def descent_of(size):
    return 0.3 * size + 2


def doc_pages(directory, exp):
    return pymupdf.open(os.path.join(directory, exp["file"])).page_count


def corruptions(exp):
    """Yields (name, corrupted expectation) for each corruption that applies."""
    rows = exp["rows"]
    text_cols = [j for j in range(len(exp["columns"])) if not exp["metric"][j]]
    cells = [(r, j) for r, row in enumerate(rows) for j in text_cols if squash(row[j])]

    def changed(name, change):
        e = copy.deepcopy(exp)
        change(e)
        return name, e

    if cells:
        r, j = cells[0]
        yield changed("a cell lost a character", lambda e: e["rows"][r].__setitem__(j, e["rows"][r][j].rstrip()[:-1]))
        short = next(((r, j) for r, j in cells if 2 <= len(rows[r][j]) <= 8 and not re.search(r"\s", rows[r][j])), None)
        if short:
            r2, j2 = short
            v = rows[r2][j2]
            yield changed("a line break the PDF lacks", lambda e: e["rows"][r2].__setitem__(j2, v[0] + "\n" + v[1:]))
            yield changed("a space the PDF lacks", lambda e: e["rows"][r2].__setitem__(j2, v[0] + " " + v[1:]))
        spaced = next(((r, j) for r, j in cells if re.search(r"\S \S", rows[r][j]) and "\n" not in rows[r][j] and len(rows[r][j]) <= 12), None)
        if spaced:
            r3, j3 = spaced
            yield changed("a space dropped", lambda e: e["rows"][r3].__setitem__(j3, re.sub(r"(\S) (\S)", r"\1\2", rows[r3][j3], count=1)))
        pair = next(((r, j) for r, j in cells if r + 1 < len(rows) and squash(rows[r + 1][j]) and len(squash(rows[r][j])) > 1), None)
        if pair:
            r4, j4 = pair

            def move(e):
                v = e["rows"][r4][j4].rstrip()
                e["rows"][r4][j4], e["rows"][r4 + 1][j4] = v[:-1], v[-1] + e["rows"][r4 + 1][j4]
            yield changed("a character moved to the next row", move)
    distinct = [k for k in range(1, len(rows)) if rows[k] != rows[0]]
    if distinct:
        k = distinct[0]
        yield changed("two rows swapped", lambda e: e["rows"].__setitem__(slice(0, k + 1, k), [rows[k], rows[0]]))
    if rows:
        yield changed("a row duplicated", lambda e: e["rows"].append(rows[0]))
    if cells and len(exp["columns"]) > 1:
        r, j = cells[0]
        k = (j + 1) % len(exp["columns"])
        if squash(rows[r][j]) != squash(rows[r][k]):
            def swap(e):
                e["rows"][r][j], e["rows"][r][k] = rows[r][k], rows[r][j]
            yield changed("cells moved between columns", swap)
    yield changed("a header changed", lambda e: e["labels"][0].__setitem__("label", e["labels"][0]["label"] + "X"))
    yield changed("a footer fact changed", lambda e: e["footer"].__setitem__(0, e["footer"][0] + "X"))
    yield changed("a meta fact changed", lambda e: e["meta"].__setitem__(0, e["meta"][0] + "X"))
    yield changed("the title changed", lambda e: e.__setitem__("title", e["title"] + "X"))
    yield changed("pages missing", lambda e: e.__setitem__("min_pages", 10**6))
    yield changed("the orientation flipped", lambda e: e.__setitem__("landscape", not e["landscape"]))
    yield changed("the columns moved", lambda e: e.__setitem__("columns", [[a + 30, b + 30] for a, b in e["columns"]]))
    if exp["totals"]:
        yield changed("a total changed", lambda e: e["totals"].__setitem__(-1, e["totals"][-1] + "1"))


def pdf_corruptions(directory, exp, scratch):
    """Yields (name, problem the check must report, directory) for corrupted copies of one PDF.
    Each corruption targets one check; inserted text uses an embedded font unless the
    corruption is about embedding."""
    font = pymupdf.Font("cjk")
    original = pymupdf.open(os.path.join(directory, exp["file"]))
    first = characters(original[0], 0)
    table = [c for c in first if c["size"] == TABLE and c["c"].strip()]
    foot = [c for c in first if c["size"] == FOOT and c["c"].isalnum()]
    width, height = original[0].rect.width, original[0].rect.height
    margin, inset, cols = exp["margin"], exp["inset"], exp["columns"]
    head = min(c["base"] for c in table)
    body = sorted({round(c["base"], 1) for c in table if c["base"] > head + 0.5})

    def copy_with(name, change):
        doc = pymupdf.open(os.path.join(directory, exp["file"]))
        change(doc)
        target = os.path.join(scratch, name)
        os.makedirs(target, exist_ok=True)
        doc.save(os.path.join(target, exp["file"]))
        return target

    def text(t, at, size, color=(0, 0, 0), embedded=True, page=0):
        def change(doc):
            p = doc[page]
            if embedded:
                p.insert_font(fontname="Fx", fontbuffer=font.buffer)
                p.insert_text(at, t, fontname="Fx", fontsize=size, color=color)
            else:
                p.insert_text(at, t, fontname="helv", fontsize=size, color=color)
        return change

    below = (margin + 1, height - 6)
    left = cols[0][0] + inset
    # In a document with text after the table, inserted table text is read as that text
    # (and fails it); the corruptions aimed at the table itself run on the other documents.
    plain = not printed(exp)[3]
    yield "text outside the margins", "outside the margins", copy_with("margins", text("X", (3, height - 6), FOOT))
    yield "table text in the footer area", "footer area", copy_with("footer", text("X", (left + 1, height - 30), TABLE))
    yield "a font that is not embedded", "embedded as n/a", copy_with("font", text("X", below, FOOT, embedded=False))
    yield "text in white", "light colour", copy_with("white", text("X", below, FOOT, color=(1, 1, 1)))
    yield "a replacement character", "replacement character", copy_with("fffd", text("\ufffd", below, FOOT))
    yield "text of another size", "unexpected sizes", copy_with("size", text("X", below, 9))
    yield "a character drawn over another", "drawn over another", copy_with("over", text("X", (foot[0]["x0"], foot[0]["base"]), FOOT))
    yield "meta text above the title", "in that order", copy_with("meta", text("X", (margin + 1, 12), META))
    yield "footer text inside the table", "footer is not below", copy_with("footerup", text("X", (width - margin - 6, head + 1), FOOT))
    if plain:
        yield "table text above its column", "top to bottom", copy_with("order", text("X", (left + 1, head - 9), TABLE))
    yield "the XMP metadata removed", "PDF/A-2b identification", copy_with("xmp", lambda doc: doc.del_xml_metadata())
    # A drawing that is not text, like the dotted circle Typst adds before a stray mark.
    yield "a shape in the margin", "side margin", copy_with("shape", lambda doc: doc[0].draw_circle((width - margin / 2, height / 2), 2, color=(0, 0, 0), fill=(0, 0, 0)))
    if body and plain:
        line = [c for c in table if abs(c["base"] - body[0]) < 0.5 and cols[0][0] <= c["x0"] < cols[0][1]]
        if line:
            yield "a character placed before its line", "left to right", copy_with("ltr", text("X", (line[0]["x0"], body[0]), TABLE))
            yield "text in the cell padding", "leaves the cell", copy_with("pad", text("X", (cols[0][1] - inset + 1, body[-1]), TABLE))

            def cover(doc):
                c = line[0]
                doc[0].draw_rect(pymupdf.Rect(c["x0"], c["base"] - c["size"], c["x1"], c["base"] + 0.3 * c["size"]), color=None, fill=(1, 1, 1), overlay=True)
            yield "a character covered by a white box", "has no ink", copy_with("cover", cover)
    if plain and cols[-1][1] + 20 < width - margin:
        yield "table text beside the table", "in no column", copy_with("beside", text("X", (cols[-1][1] + 8, head), TABLE))
    if original.page_count > 1:
        yield "the title on another page", "repeated", copy_with("title", text("X", (margin + 1, 20), TITLE, page=1))
    last = original[-1]
    last_table = [c for c in characters(last, original.page_count - 1) if c["size"] == TABLE]
    if last_table and plain and max(c["base"] for c in last_table) + 30 < last.rect.height - BOTTOM_MARGIN:
        yield "text after the last cell", "after its last cell", copy_with("after", text("X", (left + 1, max(c["base"] for c in last_table) + 20), TABLE, page=original.page_count - 1))


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
    ap.add_argument("--golden", metavar="FILE")
    ap.add_argument("--write-golden", metavar="FILE")
    ap.add_argument("--verapdf", metavar="IMAGE")
    ap.add_argument("--font", metavar="FILE", help="the Regular face; checks every character of a document at its plain-advance position")
    args = ap.parse_args()
    names = sorted(f for f in os.listdir(args.directory) if f.endswith(".json"))
    if not names:
        sys.exit("no expectations found")
    failed, layouts = False, {}
    for name in names:
        with open(os.path.join(args.directory, name), encoding="utf-8") as f:
            exp = json.load(f)
        document = exp.get("kind") == "document"
        problems, layouts[exp["file"]] = check_document(args.directory, exp, args.font) if document else check(args.directory, exp)
        print(f"{exp['file']}: {'ok' if not problems else 'FAILED'}")
        for p in problems[:20]:
            print("  " + p)
        failed |= bool(problems)
        if args.negative and document:
            for what, corrupted in document_corruptions(exp):
                caught = bool(check_document(args.directory, corrupted, args.font)[0])
                print(f"  negative control, {what}: {'caught' if caught else 'MISSED'}")
                failed |= not caught
            with tempfile.TemporaryDirectory() as scratch:
                for what, expected, directory in document_pdf_corruptions(args.directory, exp, scratch, args.font):
                    caught = any(expected in p for p in check_document(directory, exp, args.font)[0])
                    print(f"  negative control, PDF with {what}: {'caught' if caught else 'MISSED'}")
                    failed |= not caught
        elif args.negative:
            for what, corrupted in corruptions(exp):
                caught = bool(check(args.directory, corrupted)[0])
                print(f"  negative control, {what}: {'caught' if caught else 'MISSED'}")
                failed |= not caught
            if exp["rows"]:
                with tempfile.TemporaryDirectory() as scratch:
                    for what, expected, directory in pdf_corruptions(args.directory, exp, scratch):
                        found = check(directory, exp)[0]
                        caught = any(expected in p for p in found)
                        print(f"  negative control, PDF with {what}: {'caught' if caught else 'MISSED'}")
                        failed |= not caught
    if args.write_golden:
        with open(args.write_golden, "w", encoding="utf-8") as f:
            json.dump(layouts, f, indent=1, sort_keys=True)
            f.write("\n")
    if args.golden:
        with open(args.golden, encoding="utf-8") as f:
            golden = json.load(f)
        for name in sorted(set(golden) | set(layouts)):
            if golden.get(name) != layouts.get(name):
                print(f"{name}: layout differs from the golden file: {layouts.get(name)} instead of {golden.get(name)}")
                failed = True
    if args.verapdf:
        pdfs = sorted(f for f in os.listdir(args.directory) if f.endswith(".pdf"))
        failed |= check_pdfa(args.verapdf, args.directory, pdfs)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
