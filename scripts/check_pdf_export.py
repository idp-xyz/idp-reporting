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
    characters of a line from left to right, no character drawn over another and no
    replacement character;
  - the header and the total row in bold, other cells not, and metric numbers aligned right.
--golden FILE compares each document's page count and the first row on each page with FILE;
--write-golden FILE records them after a reviewed Typst or font upgrade. --negative corrupts
the expectations and the PDFs in several ways and fails unless each corruption is caught.
--verapdf IMAGE validates every PDF as PDF/A-2b with veraPDF in Docker without a network, and
a copy without XMP metadata, which must fail.

Usage: python -I scripts/check_pdf_export.py DIR [--negative] [--golden FILE | --write-golden FILE]
       [--verapdf IMAGE]   (needs PyMuPDF: scripts/requirements-pdfcheck.txt)
"""
import argparse
import copy
import json
import os
import re
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
                                "bold": "Bold" in span["font"], "n": len(out)})
    return out


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
        # Each line, in drawing order, runs left to right.
        lines = {}
        for c in chars:
            if c["c"].strip():
                lines.setdefault((c["size"], round(c["base"], 1)), []).append(c)
        for line in lines.values():
            for a, b in zip(line, line[1:]):
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
    args = ap.parse_args()
    names = sorted(f for f in os.listdir(args.directory) if f.endswith(".json"))
    if not names:
        sys.exit("no expectations found")
    failed, layouts = False, {}
    for name in names:
        with open(os.path.join(args.directory, name), encoding="utf-8") as f:
            exp = json.load(f)
        problems, layouts[exp["file"]] = check(args.directory, exp)
        print(f"{exp['file']}: {'ok' if not problems else 'FAILED'}")
        for p in problems[:20]:
            print("  " + p)
        failed |= bool(problems)
        if args.negative:
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
