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

An expectation with an "issue" record describes an issued document (S03-G, S03-H): it is checked
as a preview, except that the strip holds the record's stamp "ISSUED No. <number> - <issue time>
- <record id>" in the text colour at 8 pt in place of the banner, the number is the source
product's string, the PDF is the one the record names by digest, and every date the file
carries is the record's issue time: the information dictionary's CreationDate and ModDate and
the XMP packet's CreateDate, ModifyDate, MetadataDate and every stEvt:when of its history, each
when present (the packet is parsed as XML and its dates are found by namespace, not by prefix;
one create date, at least, is required). Times, numbers and ids are matched as ASCII only.

An expectation with "kind": "document2" describes a schema 2.0 preview (S03-J): the bands of the template (page header,
document header, document footer, page footer) in millimetres with the values bound to their texts, the table with its
raw rows, the number of pages that carry the table and where the layout put the document footer. Every page must carry the
banner, the page header and the page footer, the first page the document header; each text is drawn in its box in the
weight it says, on one line or, wrapped, on lines 1.5 font sizes apart that hold its value with nothing lost, added or
reordered, every character at its plain-advance position; {page} and {pages} are filled with the page's own numbers; each
line and rectangle is drawn as the template says (end points, stroke and colour, fill and outline) and nothing else is;
the table is checked as above over the region each page leaves it (the first page's starts under the document header)
with the columns aligned as the template says; and the document footer is directly under the table's total row, whole,
on that page when it fits above the page footer and on a page of its own, under the page header, when it does not.

A "document2" expectation with an "issue" record describes an issued group document (S03-K, ADR 0004 decision 7): it is
checked as the preview of the same 2.0 document, except that the strip of every page holds the record's stamp in the text
colour at 8 pt in place of the banner, the PDF is the one the record names by digest and with its page count, and every date
the file carries is the record's issue time, exactly as for a 1.x issue (above).

A "document2" expectation may also hold schema 2.1 elements (S03-M, ADR 0005): `barcode` (Code 128), `qrcode` and `image`, and texts, barcodes
and QR codes whose source is `issue_number`. The checker derives the geometry itself and decodes every code with zxing-cpp, which shares nothing
with the encoder. A barcode: its bars are black rectangles of one height (the box's less the line of the value, when it prints one), the bars and
spaces whole numbers of one module, the symbol 13 + 11k modules, the box's width the symbol and ten modules of quiet zone on each side divided into
modules of at least 0.25 mm, the bars at least the larger of 5 mm and 15 % of the symbol's width high, the line of the value checked like a text, and
the decoder reads exactly the expected value as Code 128 where the bars are. A QR code: rectangles one module high on one grid, the symbol the box
less four modules of quiet zone on each side, a module of at least 0.4 mm, the version the least that holds the value at the level (the standard's
capacities), the finder and timing patterns, and the decoder reads the value and the error correction level. An image: a picture of the expected
pixels drawn contained in the element's box, centred, aspect ratio kept; no other picture and no raster in place of a code. An element that shows the
issue number must hold the record's number in an issued document and PREVIEW-NUMBER in a preview, so that the drawing is not compared with a wrong
expectation. The negative controls change the value, move a code, widen or raise its box, draw it with a module below the least, cover a module or a
finder pattern, replace it by a picture, and move or remove an image.

--golden FILE compares each document's page count and the first row on each page with FILE;
--write-golden FILE records them after a reviewed Typst or font upgrade. --negative corrupts
the expectations and the PDFs in several ways and fails unless each corruption is caught.
--verapdf IMAGE validates every PDF as PDF/A-2b with veraPDF in Docker without a network, and
a copy without XMP metadata, which must fail.

Usage: python -I scripts/check_pdf_export.py DIR [--negative] [--golden FILE | --write-golden FILE]
       [--font FILE] [--verapdf IMAGE]   (needs PyMuPDF and zxing-cpp: scripts/requirements-pdfcheck.txt)
"""
import argparse
import copy
import datetime
import decimal
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unicodedata
import xml.etree.ElementTree as ET

import pymupdf
import zxingcpp

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
                                "base": c["origin"][1], "size": round(span["size"], 1), "light": light, "color": rgb,
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
BANNER_SIZE = 10  # the preview banner, in the strip at 10 mm, 10 mm
STAMP_SIZE = 8  # the issue stamp: the same strip and left edge, at 8 pt so that every legal number fits
STAMP_COLOUR = 0x132238  # the issue stamp is drawn in the text colour, never the banner's red


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
    issue = exp.get("issue")
    if issue:
        problems += check_issue_record(directory, exp, issue)
    doc = pymupdf.open(os.path.join(directory, exp["file"]))
    xmp = doc.get_xml_metadata()
    if not re.search(r"pdfaid:part[^0-9]{0,4}2\b", xmp) or not re.search(r"pdfaid:conformance[^A-Z]{0,4}B\b", xmp):
        problems.append("no PDF/A-2b identification in the XMP metadata")
    if issue:
        problems += check_creation_date(doc, xmp, issue)
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
    if issue and doc.page_count != issue["pages"]:
        problems.append(f"{doc.page_count} pages, the record says {issue['pages']}")
    if table:
        problems += check_table(exp, pages, metrics, layout)
    for i, (page, chars, used) in enumerate(pages):
        rest = [c for c in chars if c["n"] not in used and c["c"].strip()]
        if rest:
            problems.append(f"page {i + 1}: unexpected text {''.join(c['c'] for c in rest)[:40]!r}")
        if not exp["background"] and ink_outside(page, document_rects(exp)):
            problems.append(f"page {i + 1}: ink outside the boxes and the banner")
    return problems, layout


# The source product's number (S03-H): 1..32 of A-Z a-z 0-9 . _ -, starting with a letter or a
# digit, as reporting.ValidIssueNumber has it.
PRODUCT_NUMBER = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,31}")

# Dates as a PDF's information dictionary (D:20261028061203Z, or +08'00') and its XMP metadata
# (2026-10-28T06:12:03+00:00, or Z) write them: only with a zone are they an instant.
# ASCII digits only (re.ASCII): \d alone would take the digits of every script, such as full-width ones,
# which int() then reads as numbers.
PDF_DATE = re.compile(r"D:(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})(?:(Z)|([+-])(\d{2})'?(\d{2})'?)", re.ASCII)
XMP_DATE = re.compile(r"(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:(Z)|([+-])(\d{2}):(\d{2}))", re.ASCII)


def utc_instant(match):
    """The UTC time that a date matched by PDF_DATE or XMP_DATE denotes; None when there is no such date."""
    year, month, day, hour, minute, second = (int(g) for g in match.groups()[:6])
    try:
        at = datetime.datetime(year, month, day, hour, minute, second, tzinfo=datetime.timezone.utc)
        if match.group(7) is None:
            offset = datetime.timedelta(hours=int(match.group(9)), minutes=int(match.group(10)))
            at = at - offset if match.group(8) == "+" else at + offset
    except (ValueError, OverflowError):
        return None
    return at


def utc_second(text):
    """The UTC time of a record's issued_at (YYYY-MM-DDTHH:MM:SSZ); None when it is not one."""
    match = XMP_DATE.fullmatch(text) if isinstance(text, str) else None
    return utc_instant(match) if match and match.group(7) else None


def record_instant(text):
    """The instant a record's issued_at denotes in any zone (the contract says UTC with a Z, and
    check_issue_record reports anything else); None when it is no date with a time zone. The dates
    of the file are compared with it even when the record's form is wrong, so that a wrong form
    never switches the comparison off."""
    match = XMP_DATE.fullmatch(text) if isinstance(text, str) else None
    return utc_instant(match) if match else None


def issue_stamp(number, issued_at, issue_id):
    """The line an issued document carries: the product's number, the issue time and the record id."""
    return f"ISSUED No. {number} - {issued_at} - {issue_id}"


def another_number(number):
    """A valid product number that differs from the given one in its last character alone: the
    next number a forger would print."""
    return number[:-1] + ("1" if number[-1] != "1" else "2")


def check_issue_record(directory, exp, issue):
    """An issued document's record (S03-G, S03-H): the number is the source product's string,
    the stamp is the contract's line for the number, the time and the id; the PDF is the one
    the record names, by digest."""
    problems = []
    number = issue.get("number")
    if not isinstance(number, str) or not PRODUCT_NUMBER.fullmatch(number):
        problems.append("the record's number is not a product number")
    if not re.fullmatch(r"[0-9a-f]{32}", str(issue.get("issue_id", ""))):
        problems.append("the record has no id")
    if utc_second(issue.get("issued_at")) is None:
        problems.append("the record's time is not a UTC second")
    if not problems and issue["stamp"] != issue_stamp(number, issue["issued_at"], issue["issue_id"]):
        problems.append("the stamp is not the record's number, time and id")
    with open(os.path.join(directory, exp["file"]), "rb") as f:
        digest = hashlib.sha256(f.read()).hexdigest()
    if digest != issue.get("sha256"):
        problems.append("the PDF is not the one the record names (digest)")
    return problems


XMP_BASIC = "http://ns.adobe.com/xap/1.0/"
XMP_EVENT = "http://ns.adobe.com/xap/1.0/sType/ResourceEvent#"
# The dates an XMP packet can carry, by namespace and local name: (label, is a create date).
XMP_DATES = {(XMP_BASIC, "CreateDate"): ("XMP create date", True), (XMP_BASIC, "ModifyDate"): ("XMP modify date", False),
             (XMP_BASIC, "MetadataDate"): ("XMP metadata date", False), (XMP_EVENT, "when"): ("XMP history time", False)}


def xmp_dates(xmp):
    """Every date an XMP packet carries, as (label, is a create date, text), and the problems in
    reading it. The packet is parsed as XML and a date is found by its namespace and name, as an
    element or as an attribute, whatever prefix the packet binds the namespace to."""
    if re.search(r"<!(DOCTYPE|ENTITY)", xmp):
        return [], ["the XMP packet declares a document type or entities"]
    try:
        root = ET.fromstring(xmp)
    except ET.ParseError as e:
        return [], [f"the XMP packet is not well-formed XML ({e})"]
    found = []
    for el in root.iter():
        for name, text in [(el.tag, el.text or "")] + list(el.attrib.items()):
            namespace, _, local = name[1:].partition("}") if name.startswith("{") else ("", "", name)
            if (namespace, local) in XMP_DATES:
                label, create = XMP_DATES[(namespace, local)]
                found.append((label, create, text.strip()))
    return found, []


def check_creation_date(doc, xmp, issue):
    """Every date the PDF carries is the record's issue time (S03-H): the product's time, not the
    renderer's clock. That is the information dictionary's CreationDate and ModDate and the XMP
    packet's CreateDate, ModifyDate, MetadataDate and every time of its history, each when the file
    carries it; it must carry a creation date. Each is a date with a time zone, and an instant, as
    the record's time is."""
    issued = record_instant(issue.get("issued_at"))
    if issued is None:
        return ["the PDF's creation date cannot be compared: the record's issue time is not a date with a time zone"]
    info = doc.metadata or {}
    dates = [(label, label == "creation date", "the information dictionary", PDF_DATE, info[key])
             for key, label in (("creationDate", "creation date"), ("modDate", "modification date")) if info.get(key)]
    in_xmp, problems = xmp_dates(xmp)
    dates += [(label, create, "the XMP metadata", XMP_DATE, text) for label, create, text in in_xmp]
    if not any(create for _, create, *_ in dates):
        problems.append("the PDF carries no creation date")
    for label, create, where, pattern, text in dates:
        match = pattern.fullmatch(text)
        found = utc_instant(match) if match else None
        if found is None:
            problems.append(f"the PDF's {label} in {where} ({text!r}) is not a date with a time zone (every date of the file is its creation date)")
        elif found != issued and create:
            problems.append(f"the PDF's creation date in {where} is {found:%Y-%m-%dT%H:%M:%SZ}, not the record's issue time {issue['issued_at']}")
        elif found != issued:
            problems.append(f"the PDF's {label} in {where} is {found:%Y-%m-%dT%H:%M:%SZ}, not the record's issue time {issue['issued_at']}: every date of the file is its creation date")
    return problems


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
    issue = exp.get("issue")
    strip = issue["stamp"] if issue else BANNER
    wanted = [(strip, 10, 10, 190, STAMP_SIZE if issue else BANNER_SIZE, "left")] + [(e["text"], e["x"] + exp["offset_x"], e["y"] + exp["offset_y"], e["width"], e["size"], e["align"]) for e in exp["elements"]]
    if issue:
        text = "".join(c["c"] for c in sorted(chars, key=lambda c: (round(c["base"]), c["x0"])))
        if BANNER in text:
            problems.append("preview text on an issued document")
        stamp = [c for c in chars if abs(c["base"] - (10 * MM + ASCENT * STAMP_SIZE)) <= 0.1 and c["c"].strip()]
        if any(c["color"] != STAMP_COLOUR for c in stamp):
            problems.append("the stamp is not in the text colour")
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
    # The pictures of a 2.1 document's image elements are checked with their elements (check_coded).
    if len(images) != int(exp["background"]) + exp.get("element_images", 0):
        problems.append(f"{len(images)} images, expected {int(exp['background']) + exp.get('element_images', 0)}")
    for img in images if exp["background"] else []:
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


TABLE_INSET, TABLE_INSET_Y, LINE_STEP, DESCENT = 3.0, 2.0, 1.5, 0.3
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


def check_table(exp, pages, metrics, layout, region_of=None, ends=None, drawings_of=None):
    """Checks the table over the pages: the header on every page (full mode), every row's
    cells complete, in order, on one page, each line inside its column at its plain-advance
    position and at the baseline its row and line give it, the brought-forward, carry-forward
    and total rows with exactly recomputed sums, the snapshot's totals equal to the sums, and
    in full mode the fills, cell rules and the rule above the total row. Records the first row
    of each page.

    A schema 2.0 table (S03-J) has a region of its own on each page, given by region_of(number),
    columns that may be aligned as the template says, the drawings the page's other elements
    left out in drawings_of(number), and records in ends the y at which it ends on each page."""
    problems = []
    t = exp["table"]
    region_of = region_of or (lambda number: table_region(exp))
    region = region_of(1)
    size = t["size"]
    edges, x = [], region.x0
    for col in t["columns"]:
        edges.append((x, x + col["width"] * MM))
        x += col["width"] * MM
    full = exp.get("mode", "full") == "full"
    metric = [c["metric"] for c in t["columns"]]
    align = [c.get("align") or ("right" if c["metric"] else "left") for c in t["columns"]]
    rows = t["rows"] or []
    expected = [[squash(group_digits(v) if metric[j] else v) for j, v in enumerate(row)] for row in rows]
    blank = [all(cell == "" for cell in row) for row in expected]
    paragraphs = lambda k: max([1] + [v.replace("\r\n", "\n").replace("\r", "\n").count("\n") + 1 for v in rows[k]])
    # Every carried sum prints with the column's longest fraction over all its rows.
    fracs = [max([c["scale"]] + [len(r[j].partition(".")[2]) for r in rows]) if metric[j] else 0 for j, c in enumerate(t["columns"])]
    sums = lambda upto: [group_digits(exact_sum([r[j] for r in rows[:upto]], fracs[j])) if metric[j] and j > 0 else "" for j in range(len(edges))]
    line_h = lambda n: 2 * TABLE_INSET_Y + (ASCENT + DESCENT) * size + (n - 1) * LINE_STEP * size
    k, acc = -1, None  # the current row and its accumulated cell texts
    first_rows, row_page = [], {}
    total_seen = False
    for number, (page, chars, _) in enumerate(pages, 1):
        region = region_of(number)
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
                    problems += [f"page {number}: {p}" for p in check_line(cs, text.rstrip(), size, left, right, align[j], metrics, bold)]
        text_of = lambda cells, j: "".join(c["c"] for c in cells.get(j, []))
        label_of = lambda line: text_of(line[2], 0).replace("\xa0", " ")  # a no-break space reads as a space
        is_tail = lambda line: label_of(line) == TOTAL or label_of(line).startswith(CARRY)
        # Leading bold lines: the header (full mode) and, from page 2 on, the brought-forward
        # row; with no rows on the page the tail comes right after them.
        leading = []
        while lines and lines[0][1] and not is_tail(lines[0]):
            leading.append(lines.pop(0))
        forward = leading.pop() if leading and label_of(leading[-1]).startswith(FORWARD) else None
        header = ["".join(text_of(cells, j) for _, _, cells in leading) for j in range(len(edges))]
        if full:
            for j, col in enumerate(t["columns"]):
                if squash(header[j]) != squash(col["label"]):
                    problems.append(f"page {number}: column {j + 1} header {header[j]!r}, expected {col['label']!r}")
        elif leading:
            problems.append(f"page {number}: a header in the variable layer")
        # Vertical placement: the header starts at the region top, every row where the one
        # before it ends, every line of a row 1.5 font sizes below the one above it.
        top = region.y0
        drawn_rows = []  # (name, baselines) in page order, for the vertical check

        def expect_rows(items):
            # A row's lines sit 1.5 font sizes apart from its first baseline; a line with
            # nothing to draw (an empty paragraph) leaves a gap, so each drawn baseline is
            # matched to the line index its offset gives it.
            nonlocal top
            for name, bases, least in items:
                first = top + TABLE_INSET_Y + ASCENT * size
                n = least
                for base in sorted(bases):
                    i = round((base - first) / (LINE_STEP * size))
                    want = first + i * LINE_STEP * size
                    if i < 0 or abs(base - want) > 0.1:
                        problems.append(f"page {number}: {name}: baseline {base:.2f} is not on a line of the row (first line {first:.2f})")
                        break
                    n = max(n, i + 1)
                top += line_h(n)
        header_bases = [b for b, _, _ in leading]
        if full or header_bases:
            # The header's height is reserved in both modes; in the variable layer it is the
            # label's line count, which the expectation does not record: one line per label row.
            expect_rows([("header", header_bases, 1)] if full else [])
        if not full:
            top += line_h(t.get("header_lines", 1))
        if number > 1:
            want = sums(k + 1)
            if forward is None:
                problems.append(f"page {number}: no brought-forward row")
            else:
                if label_of(forward) != FORWARD + str(number) + " 页）":
                    problems.append(f"page {number}: brought-forward label {label_of(forward)!r}")
                for j in range(1, len(edges)):
                    if text_of(forward[2], j).strip() != want[j]:
                        problems.append(f"page {number}: brought forward {text_of(forward[2], j)!r} in column {j + 1}, expected {want[j]!r}")
                expect_rows([("brought-forward row", [forward[0]], 1)])
        elif forward is not None:
            problems.append("page 1: a brought-forward row")
        # The trailing bold line: carry-forward, or the total on the last page.
        trailing = lines.pop() if lines and lines[-1][1] else None
        if any(bold for _, bold, _ in lines):
            problems.append(f"page {number}: bold text among the rows")
        # The rows: each line continues the current row's cells or starts the next row; a row
        # with nothing to draw is skipped but still takes a line of height.
        page_first = None
        row_bases = {}
        for base, _, cells in lines:
            if acc is None or all(acc[j] == expected[k][j] for j in range(len(edges))):
                k += 1
                while k < len(rows) and blank[k]:
                    row_page[k] = number
                    row_bases.setdefault(k, [])
                    drawn_rows.append((f"row {k + 1}", [], paragraphs(k)))
                    if page_first is None:
                        page_first = k
                    k += 1
                if k >= len(rows):
                    problems.append(f"page {number}: text after the last row: {[text_of(cells, j) for j in cells][:3]!r}")
                    break
                acc = [""] * len(edges)
                row_page[k] = number
                drawn_rows.append((f"row {k + 1}", row_bases.setdefault(k, []), paragraphs(k)))
                if page_first is None:
                    page_first = k
            elif row_page.get(k) != number:
                problems.append(f"row {k + 1} is split over pages {row_page.get(k)} and {number}")
                row_page[k] = number
                drawn_rows.append((f"row {k + 1} (continued)", row_bases.setdefault(k, []), 1))
            row_bases[k].append(base)
            for j, cs in cells.items():
                piece = squash("".join(c["c"] for c in cs))
                if not (expected[k][j].startswith(acc[j] + piece)):
                    problems.append(f"row {k + 1}, column {j + 1}: {''.join(c['c'] for c in cs)!r} does not continue {rows[k][j]!r}")
                else:
                    if acc[j] == "" and piece and abs(base - row_bases[k][0]) > 0.1 and not rows[k][j].lstrip(" \t").startswith(("\n", "\r")):
                        problems.append(f"row {k + 1}, column {j + 1}: the cell starts below the row's first line")
                    acc[j] += piece
        expect_rows([(name, sorted(set(bases)), least) for name, bases, least in drawn_rows])
        first_rows.append(page_first)
        last = number == len(pages)
        if trailing is None:
            problems.append(f"page {number}: no {'total' if last else 'carry-forward'} row")
            continue
        # Blank rows at the end of the page take the room between the last drawn row and the
        # tail; they are complete the moment they are reached.
        tail_top = trailing[0] - TABLE_INSET_Y - ASCENT * size
        while k + 1 < len(rows) and blank[k + 1] and (acc is None or all(acc[j] == expected[k][j] for j in range(len(edges)))) and tail_top - top > line_h(paragraphs(k + 1)) - 0.1:
            k += 1
            acc = None
            row_page[k] = number
            top += line_h(paragraphs(k))
            drawn_rows.append((f"row {k + 1}", [], paragraphs(k)))
            if page_first is None:
                page_first = k
                first_rows[-1] = k
        if abs(tail_top - top) > 0.1:
            problems.append(f"page {number}: the {'total' if last else 'carry-forward'} row starts at {tail_top:.2f}, expected {top:.2f}")
        if ends is not None:
            ends.append(top + line_h(1))
        label = label_of(trailing)
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
        if full:
            problems += [f"page {number}: {p}" for p in check_table_drawings(page, region, edges, size, leading, forward, drawn_rows, trailing, tail_top, last, drawings_of(number) if drawings_of else None)]
        else:
            if drawings_inside(page, region):
                problems.append(f"page {number}: a drawing in the table of the variable layer")
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


def check_table_drawings(page, region, edges, size, leading, forward, drawn_rows, trailing, tail_top, last, page_drawings=None):
    """Full mode: one stroked cell per column of every drawn row (0.5 pt), a fill per column of
    the header rows, a 1 pt rule above the total row on the last page and nowhere else, and no
    other drawing in the region. The drawings are the page's, or those given (a 2.0 page's, less the
    ones of its other elements)."""
    problems = []
    inside = lambda r: r.x1 >= region.x0 - 1 and r.x0 <= region.x1 + 1 and r.y1 >= region.y0 - 1 and r.y0 <= region.y1 + 1
    every = page.get_drawings() if page_drawings is None else page_drawings
    drawings = [d for d in every if inside(d["rect"]) and d["rect"].width > 0.01 and d["rect"].height > 0.01]
    rules = [d for d in every if inside(d["rect"]) and (d["rect"].height <= 0.01 or d["rect"].width <= 0.01)]
    line_h = lambda n: 2 * TABLE_INSET_Y + (ASCENT + DESCENT) * size + (n - 1) * LINE_STEP * size
    # The rows in page order with their heights.
    rows = []
    if leading:
        rows.append(("header", line_h(len(leading)), True))
    if forward is not None:
        rows.append(("brought-forward row", line_h(1), False))
    for name, bases, least in drawn_rows:
        rows.append((name, line_h(max(least, len(bases))), False))
    rows.append(("tail", line_h(1), False))
    top = region.y0

    def take(kind, rect, header):
        # The renderer draws a fill and a stroked rectangle; a reader may merge the two.
        hits = [i for i, d in enumerate(drawings) if max(abs(d["rect"].x0 - rect.x0), abs(d["rect"].x1 - rect.x1), abs(d["rect"].y0 - rect.y0), abs(d["rect"].y1 - rect.y1)) <= 0.3]
        fills = [drawings[i]["fill"] for i in hits if drawings[i]["fill"] is not None]
        strokes = [drawings[i]["width"] for i in hits if drawings[i]["width"] is not None]
        if header and not fills:
            problems.append(f"{kind}: no fill")
        for fill in fills:
            if not header:
                problems.append(f"{kind}: a fill on a body cell")
            elif max(abs(v - 0.91) for v in fill[:3]) > 0.02:
                problems.append(f"{kind}: fill {tuple(round(v, 2) for v in fill[:3])}")
        if not strokes:
            problems.append(f"{kind}: no cell rule")
        for width in strokes:
            if abs(width - 0.5) > 0.05:
                problems.append(f"{kind}: rule width {width}")
        for color in [drawings[i]["color"] for i in hits if drawings[i]["width"] is not None]:
            if color is None or max(abs(v - 0.588) for v in color[:3]) > 0.02:
                problems.append(f"{kind}: rule colour {None if color is None else tuple(round(v, 2) for v in color[:3])}")
        for i in sorted(hits, reverse=True):
            drawings.pop(i)
    for name, h, header in rows:
        for j, (a, b) in enumerate(edges):
            take(f"{name} column {j + 1}", pymupdf.Rect(a, top, b, top + h), header)
        top += h
    if drawings:
        problems.append(f"{len(drawings)} other drawings in the table")
    if last:
        total_rules = [d for d in rules if abs(d["rect"].y0 - tail_top) <= 0.3 and abs(d["rect"].x0 - region.x0) <= 0.3 and abs(d["rect"].x1 - region.x1) <= 0.3 and d["width"] is not None and abs(d["width"] - 1.0) <= 0.05]
        if not total_rules:
            problems.append("no 1 pt rule above the total row")
        for d in total_rules:
            if d["color"] is None or max(abs(v - 0.235) for v in d["color"][:3]) > 0.02:
                problems.append(f"total rule colour {d['color']}")
        if len(rules) != len(total_rules) or len(total_rules) > 1:
            problems.append(f"{len(rules) - min(1, len(total_rules))} other lines in the table")
    elif rules:
        problems.append("a rule on a page without the total row")
    return problems


def drawings_inside(page, region):
    """Drawings touching the region, lines of no height or width included."""
    return [d for d in page.get_drawings() if d["rect"].x1 >= region.x0 - 1 and d["rect"].x0 <= region.x1 + 1 and d["rect"].y1 >= region.y0 - 1 and d["rect"].y0 <= region.y1 + 1]


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


def issue_expectation_corruptions(exp, changed):
    """Yields the corruptions of an issue record that apply to an expectation with an "issue" block, whichever kind of
    document it describes (1.x, S03-G and S03-H; 2.0, S03-K): each must be caught by the check of the record or of the
    file's dates. `changed(name, change, expected)` is the caller's own."""
    if exp.get("issue"):
        yield changed("the record's number changed", lambda e: e["issue"].__setitem__("number", another_number(e["issue"]["number"])))
        yield changed("the record's time changed", lambda e: e["issue"].__setitem__("issued_at", "2001-01-01T00:00:00Z"))
        yield changed("the record's id changed", lambda e: e["issue"].__setitem__("issue_id", "0" * 32))
        yield changed("the record's stamp changed", lambda e: e["issue"].__setitem__("stamp", e["issue"]["stamp"].replace("ISSUED", "ISSUE")))
        yield changed("the record's digest changed", lambda e: e["issue"].__setitem__("sha256", "0" * 64))
        yield changed("the record's page count changed", lambda e: e["issue"].__setitem__("pages", e["issue"]["pages"] + 1))
        # Self-consistent records that disagree with the drawn stamp alone.
        def restamped(number=None, issue_id=None, issued_at=None):
            def change(e):
                i = e["issue"]
                i["number"] = number if number is not None else i["number"]
                i["issue_id"] = issue_id if issue_id is not None else i["issue_id"]
                i["issued_at"] = issued_at if issued_at is not None else i["issued_at"]
                i["stamp"] = issue_stamp(i["number"], i["issued_at"], i["issue_id"])
            return change
        yield changed("the record's number and stamp changed together", restamped(number=another_number(exp["issue"]["number"])))
        yield changed("the record's id and stamp changed together", restamped(issue_id="0" * 32))
        # Numbers the product could not have reserved: one with a space (ValidIssueNumber has
        # none) and an integer (S03-G's encoding, the digits of the number). The first leaves the
        # stamp and the PDF as they were; the second makes the record consistent with its own
        # integer. Either way only the check of the number itself says so.
        yield changed("the record's number is not a product number", lambda e: e["issue"].__setitem__("number", e["issue"]["number"] + " 1"), expected="is not a product number")

        def integer_number(e):
            i = e["issue"]
            i["number"] = int(re.sub(r"\D", "", i["number"]) or "1")
            i["stamp"] = issue_stamp(i["number"], i["issued_at"], i["issue_id"])
        yield changed("the record's number is an integer", integer_number, expected="is not a product number")
        # What the contract's number, id and time do not allow, each with the stamp recomputed so that the
        # record is consistent with itself and only the check of the field says so: a number that starts
        # with a dash or a dot (it names a file and prints on one line), one of 33 characters, one in
        # full-width letters and digits; an id that is not lowercase hexadecimal, or is in full-width digits;
        # a time in another zone (the same instant, which the file's dates do match, or another one, which
        # they must still be compared with), in full-width digits, or no date at all.
        number, issue_id, issued_at = exp["issue"]["number"], exp["issue"]["issue_id"], exp["issue"]["issued_at"]
        wide = lambda text: "".join(chr(ord(c) + 0xFEE0) if c.isascii() and c.isalnum() else c for c in text)
        wide_digits = lambda text: "".join(chr(ord(c) + 0xFEE0) if "0" <= c <= "9" else c for c in text)
        same_instant = (utc_second(issued_at) + datetime.timedelta(hours=8)).strftime("%Y-%m-%dT%H:%M:%S+08:00")
        for what, number_to in (("starts with a dash", "-" + number[1:]), ("starts with a dot", "." + number[1:]), ("has 33 characters", "A" * 33), ("is in full-width characters", wide(number)), ("has full-width digits", wide_digits(number))):
            if number_to == number:
                continue  # a number without digits (a 2.1 document may carry one of letters) has no full-width digits to put in
            yield changed(f"the record's number {what}", restamped(number=number_to), expected="is not a product number")
        for what, id_to in (("is not hexadecimal", issue_id[:-1] + "g"), ("is in upper case", "ABCDEF" + "0" * 26), ("is in full-width digits", wide("7" * 32))):
            yield changed(f"the record's id {what}", restamped(issue_id=id_to), expected="has no id")
        yield changed("the record's time is in full-width digits", restamped(issued_at=wide_digits(issued_at)), expected="is not a UTC second")
        yield changed("the record's time is no date", restamped(issued_at="yesterday"), expected="is not a UTC second")
        yield changed("the record's time is UTC+8, the same instant", restamped(issued_at=same_instant), expected="is not a UTC second")
        # The comparison with the file's dates goes on when the form is wrong: another instant is caught there too.
        yield changed("the record's time is UTC+8, another instant", restamped(issued_at="2001-01-01T08:00:00+08:00"), expected="creation date in")
        yield changed("the record's time is no date, so no date is compared", restamped(issued_at="yesterday"), expected="creation date cannot be compared")


def document_corruptions(exp):
    def changed(name, change, expected=""):
        # expected: what a problem found in the corrupted expectation must say; any problem
        # counts when it is empty.
        e = copy.deepcopy(exp)
        change(e)
        return name, e, expected
    yield changed("a value changed", lambda e: e["elements"][0].__setitem__("text", e["elements"][0]["text"] + "X"))
    yield changed("the offset moved by 0.2 mm", lambda e: e.__setitem__("offset_y", e["offset_y"] + 0.2))
    yield changed("a box moved by 0.2 mm", lambda e: e["elements"][0].__setitem__("x", e["elements"][0]["x"] + 0.2))
    flip = {"left": "right", "right": "center", "center": "left"}
    yield changed("an alignment changed", lambda e: e["elements"][0].__setitem__("align", flip[e["elements"][0]["align"]]))
    yield changed("a value missing", lambda e: e["elements"].pop())
    yield changed("a value added", lambda e: e["elements"].append({"text": "X", "x": 20, "y": 200, "width": 50, "height": 10, "size": 10, "align": "left"}))
    yield changed("the background flag flipped", lambda e: e.__setitem__("background", not e["background"]))
    yield from issue_expectation_corruptions(exp, changed)
    if exp.get("table"):
        rows = exp["table"]["rows"] or []
        columns = exp["table"]["columns"]
        solid = next((i for i, row in enumerate(rows) if any(squash(v) for v in row)), None)
        if solid is not None:
            cell = next(j for j, v in enumerate(rows[solid]) if squash(v))
            yield changed("a table cell changed", lambda e: e["table"]["rows"][solid].__setitem__(cell, e["table"]["rows"][solid][cell] + "X"))
            yield changed("a table row removed", lambda e: e["table"]["rows"].pop(solid))
            yield changed("a table row added", lambda e: e["table"]["rows"].insert(solid, list(e["table"]["rows"][solid])))
        other = next((i for i in range(len(rows)) if solid is not None and i != solid and any(squash(v) for v in rows[i])), None)
        if other is not None:
            yield changed("two table rows swapped", lambda e: e["table"]["rows"].__setitem__(slice(0, 0), []) or (e["table"]["rows"].__setitem__(solid, rows[other]), e["table"]["rows"].__setitem__(other, rows[solid])))
        summed = next((j for j, c in enumerate(columns) if j > 0 and c["metric"]), None)
        if summed is not None and columns[summed]["field"] in exp["table"]["totals"]:
            yield changed("a table total changed", lambda e: e["table"]["totals"].__setitem__(columns[summed]["field"], "1.00"))
        if summed is not None and rows:
            yield changed("a table amount changed", lambda e: e["table"]["rows"][0].__setitem__(summed, "0.01"))
        if exp.get("mode", "full") == "full":
            yield changed("a column label changed", lambda e: e["table"]["columns"][0].__setitem__("label", e["table"]["columns"][0]["label"] + "X"))
        yield changed("the table moved by 0.5 mm", lambda e: e["table"].__setitem__("x", e["table"]["x"] + 0.5))
        if len(columns) > 1:
            # The first column 5 mm narrower and the last 5 mm wider: every column between moves,
            # and so does the left edge of the last (a boundary between a left-aligned and a
            # right-aligned column alone would move no text in the variable layer).
            yield changed("a column width changed", lambda e: (e["table"]["columns"][0].__setitem__("width", e["table"]["columns"][0]["width"] - 5), e["table"]["columns"][-1].__setitem__("width", e["table"]["columns"][-1]["width"] + 5)))


def issue_pdf_corruptions(directory, exp, copy_with, text, font):
    """Yields (what, what a problem must say, directory of the corrupted copy) for the PDF-level corruptions that apply to
    an issued document of either kind: the preview in place of the issue, the banner text, a forged stamp or one covered
    on the last page, every date of the file, the stamp's colour. `copy_with(name, change)` and `text(...)` are the
    caller's own, `font` the substitute font they draw with."""
    if exp.get("issue"):
        def preview_instead(target):
            shutil.copy(os.path.join(directory, exp["issue"]["preview_file"]), os.path.join(target, exp["file"]))
        previewed = copy_with("preview", lambda doc: None)
        preview_instead(previewed)
        yield "the preview in place of the issue", "preview text", previewed
        yield "the banner text added", "preview text", copy_with("previewword", text(BANNER, (20 * MM, 285 * MM)))
        strip = pymupdf.Rect(10 * MM, 10 * MM, 200 * MM, 18 * MM)
        stamp_colour = (0x13 / 255, 0x22 / 255, 0x38 / 255)

        def restamp(doc):
            # The strip whited out and a stamp with another valid number (the next, as a forger
            # would print it) drawn in the stamp's colour.
            p = doc[0]
            p.draw_rect(strip, color=None, fill=(1, 1, 1), overlay=True)
            i = exp["issue"]
            other = issue_stamp(another_number(i["number"]), i["issued_at"], i["issue_id"])
            p.insert_font(fontname="Fx", fontbuffer=font.buffer)
            p.insert_text((10 * MM, 10 * MM + ASCENT * STAMP_SIZE), other, fontname="Fx", fontsize=STAMP_SIZE, color=stamp_colour)
        yield "the stamp redrawn with another number", "is not drawn on one line", copy_with("restamp", restamp)

        def unstamp_last(doc):
            doc[doc.page_count - 1].draw_rect(strip, color=None, fill=(1, 1, 1), overlay=True)
        yield "the stamp covered on the last page", "has no ink", copy_with("unstamp", unstamp_last)

        def redate(info=True, xmp=True):
            # Only the creation date changes, by one second: the information dictionary's, the
            # XMP's or both. (The record's digest is recomputed for it like for every PDF-level
            # control, so that nothing but the date comparison can catch it.)
            earlier = utc_second(exp["issue"]["issued_at"]) - datetime.timedelta(seconds=1)

            def change(doc):
                if info:
                    doc.set_metadata({"creationDate": earlier.strftime("D:%Y%m%d%H%M%SZ")})
                if xmp:
                    old = doc.get_xml_metadata()
                    new = re.sub(r"(<xmp:CreateDate>)[^<]*(</xmp:CreateDate>)", lambda m: m.group(1) + earlier.strftime("%Y-%m-%dT%H:%M:%S+00:00") + m.group(2), old)
                    assert new != old, "the XMP create date was not found"
                    doc.set_xml_metadata(new)
            return change
        # The other dates the file carries are the creation date too: a forged ModDate, a forged time in the
        # XMP history, another modify or metadata date, and a create date written under another prefix bound
        # to the same namespace, which a search for the literal xmp: prefix would never see.
        earlier = utc_second(exp["issue"]["issued_at"]) - datetime.timedelta(seconds=1)
        xmp_time = earlier.strftime("%Y-%m-%dT%H:%M:%S+00:00")

        def forge_xmp(change):
            def forge(doc):
                old = doc.get_xml_metadata()
                new = change(old)
                assert new != old, "the XMP packet was not changed"
                doc.set_xml_metadata(new)
            return forge

        def forge_info(**fields):
            return lambda doc: doc.set_metadata(fields)
        yield "a forged modification date in the information dictionary", "creation date", copy_with("moddate", forge_info(modDate=earlier.strftime("D:%Y%m%d%H%M%SZ")))
        yield "a forged time in the XMP history", "creation date", copy_with("history", forge_xmp(lambda x: re.sub(r"(<stEvt:when>)[^<]*(</stEvt:when>)", lambda m: m.group(1) + xmp_time + m.group(2), x, count=1)))
        yield "a forged XMP modify date", "creation date", copy_with("modify", forge_xmp(lambda x: re.sub(r"(<xmp:ModifyDate>)[^<]*(</xmp:ModifyDate>)", lambda m: m.group(1) + xmp_time + m.group(2), x)))
        yield "an XMP metadata date of another time", "creation date", copy_with("metadata", forge_xmp(lambda x: x.replace("<xmp:ModifyDate>", "<xmp:MetadataDate>" + xmp_time + "</xmp:MetadataDate><xmp:ModifyDate>", 1)))
        yield "an XMP create date under another prefix of the same namespace", "creation date", copy_with("prefix", forge_xmp(lambda x: re.sub(r"(<)xmp:(CreateDate>)[^<]*(</)xmp:(CreateDate>)", lambda m: m.group(1) + "xap:" + m.group(2) + xmp_time + m.group(3) + "xap:" + m.group(4), x).replace('xmlns:xmp="http://ns.adobe.com/xap/1.0/"', 'xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:xap="http://ns.adobe.com/xap/1.0/"', 1)))
        yield "an XMP create date without a time zone", "is not a date with a time zone", copy_with("xmp_nozone", forge_xmp(lambda x: re.sub(r"(<xmp:CreateDate>)[^<]*(</xmp:CreateDate>)", lambda m: m.group(1) + exp["issue"]["issued_at"][:-1] + m.group(2), x)))
        yield "a creation date without a time zone in the information dictionary", "is not a date with a time zone", copy_with("info_nozone", forge_info(creationDate=utc_second(exp["issue"]["issued_at"]).strftime("D:%Y%m%d%H%M%S")))
        yield "an XMP packet that is not XML", "not well-formed", copy_with("notxml", forge_xmp(lambda x: x.replace("</rdf:RDF>", "", 1)))
        yield "another creation date", "creation date", copy_with("created", redate())
        yield "another creation date in the information dictionary alone", "creation date", copy_with("created_info", redate(xmp=False))
        yield "another creation date in the XMP metadata alone", "creation date", copy_with("created_xmp", redate(info=False))

        def undate(doc):
            doc.set_metadata({"creationDate": ""})
            doc.set_xml_metadata(re.sub(r"<xmp:CreateDate>[^<]*</xmp:CreateDate>", "", doc.get_xml_metadata()))
        yield "no creation date", "carries no creation date", copy_with("undated", undate)

        def recolour(doc):
            page = doc[0]
            page.clean_contents()
            xref = page.get_contents()[0]
            raw = doc.xref_stream(xref).decode("latin1")
            # The stamp's fill colour, as Typst writes it, becomes the banner's red.
            new = raw.replace(".07450981 .13333334 .21960785 scn", ".54901966 .23137255 .1254902 scn")
            assert new != raw, "the stamp's colour was not found"
            doc.update_stream(xref, new.encode("latin1"))
        yield "the stamp in the banner's red", "text colour", copy_with("recolour", recolour)


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

    def text(t, at, color=(0, 0, 0), embedded=True, size=10):
        def change(doc):
            p = doc[0]
            if embedded:
                p.insert_font(fontname="Fx", fontbuffer=font.buffer)
                p.insert_text(at, t, fontname="Fx", fontsize=size, color=color)
            else:
                p.insert_text(at, t, fontname="helv", fontsize=size, color=color)
        return change

    e = exp["elements"][0] if exp["elements"] else None
    top = (e["y"] + exp["offset_y"]) * MM if e else 0
    # Below the safe region, outside every box and the table.
    free = (105 * MM, 285 * MM)
    yield "extra text", "unexpected text", copy_with("extra", text("X", free))
    yield from issue_pdf_corruptions(directory, exp, copy_with, text, font)
    yield "a font that is not embedded", "embedded as n/a", copy_with("font", text("X", free, embedded=False))

    def cover(doc):
        doc[0].draw_rect(pymupdf.Rect(0, top, 210 * MM, top + ASCENT * e["size"] + 0.3 * e["size"]), color=None, fill=(1, 1, 1), overlay=True)
    if e is not None:
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

    def move_glyphs(doc, moves, number=0):
        """Changes the engine's own transformations ("q 1 0 0 -1 x y cm") for the given
        characters: moves maps (x, y) in PDF coordinates to a new (x, y)."""
        page = doc[number]
        page.clean_contents()
        xref = page.get_contents()[0]
        raw = doc.xref_stream(xref).decode("latin1")

        def move(m):
            x, y = float(m.group(1)), float(m.group(2))
            for (wx, wy), (nx, ny) in moves:
                if abs(x - wx) < 0.01 and abs(y - wy) < 0.01:
                    return f"1 0 0 -1 {nx:.6f} {ny:.6f} cm"
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
        move_glyphs(doc, [(pdf_point(a, page), (b["x"], pdf_point(a, page)[1])), (pdf_point(b, page), (a["x"], pdf_point(b, page)[1]))])
    if e is not None and len(e["text"].rstrip()) >= 2:
        yield "two glyphs swapped", "left to right", copy_with("swap", swap)
    right = next((el for el in exp["elements"] if el["align"] == "right" and len(el["text"].rstrip()) >= 1), None)

    def past_edge(doc):
        page = doc[0]
        c = value_line(page, right)[-1]
        move_glyphs(doc, [(pdf_point(c, page), (c["x"] + 0.6, pdf_point(c, page)[1]))])
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
        line = value_line(page, e)
        c = line[1]
        move_glyphs(doc, [(pdf_point(c, page), (c["x"] + 1, pdf_point(c, page)[1]))])
    if font_file and e is not None and len(e["text"].rstrip()) >= 2 and not exp["background"]:
        yield "a glyph moved by 1 pt", "plain-advance position", copy_with("shift", shift)
    if exp.get("table"):
        if doc_pages(directory, exp) > 1:
            yield "the last page removed", "rows drawn complete", copy_with("lastpage", lambda doc: doc.delete_page(doc.page_count - 1))
        region = table_region(exp)
        size = exp["table"]["size"]
        edges, x = [], region.x0
        for col in exp["table"]["columns"]:
            edges.append((x, x + col["width"] * MM))
            x += col["width"] * MM

        def cover_carry(doc):
            # The carry-forward amounts of page 1: the last bold line of the region, right column.
            page = doc[0]
            bold = [c for c in characters(page, 0) if c["bold"] and region.y0 <= c["base"] <= region.y1 and c["c"].strip()]
            base = max(c["base"] for c in bold)
            page.draw_rect(pymupdf.Rect(region.x0 + region.width / 2, base - ASCENT * size - 1, region.x1 - 0.6, base + descent_of(size)), color=None, fill=(1, 1, 1), overlay=True)
        if any(j > 0 and c["metric"] for j, c in enumerate(exp["table"]["columns"])):
            yield "the carried amount covered", "no ink", copy_with("carry", cover_carry)

        def first_body_line(page):
            body = [c for c in characters(page, 0) if not c["bold"] and region.y0 <= c["base"] <= region.y1 and c["c"].strip() and abs(c["size"] - size) < 0.05]
            top = min(c["base"] for c in body)
            return sorted([c for c in body if abs(c["base"] - top) <= 0.1], key=lambda c: c["x"])

        def shift_cell(doc):
            page = doc[0]
            line = first_body_line(page)
            move_glyphs(doc, [(pdf_point(line[0], page), (line[0]["x"] + exp["table"]["columns"][0]["width"] * MM, pdf_point(line[0], page)[1]))])
        has_body = bool(exp["table"]["rows"]) and any(any(squash(v) for v in row) for row in exp["table"]["rows"])
        if has_body:
            yield "a cell line moved into the next column", "does not continue", copy_with("cellshift", shift_cell)

            def cell_down(doc):
                page = doc[0]
                line = first_body_line(page)
                move_glyphs(doc, [(pdf_point(c, page), (c["x"], pdf_point(c, page)[1] - 1)) for c in line if abs(c["x"] - line[0]["x"]) < 0.01 or True][:len(line)])
            yield "a cell line moved 1 pt down", "baseline", copy_with("celldown", cell_down)

            def out_of_region(doc):
                # The topmost body line of the last column, moved right until its last
                # character's box crosses the region's edge by 2 pt (a line is one text object,
                # placed at its first character).
                page = doc[0]
                a, b = edges[-1]
                body = [c for c in characters(page, 0) if not c["bold"] and region.y0 <= c["base"] <= region.y1 and c["c"].strip() and a - 0.5 <= (c["x0"] + c["x1"]) / 2 < b + 0.5]
                top_base = min(c["base"] for c in body)
                line = sorted([c for c in body if abs(c["base"] - top_base) <= 0.1], key=lambda c: c["x"])
                shift = region.x1 + 2 - line[-1]["x1"]
                move_glyphs(doc, [(pdf_point(line[0], page), (line[0]["x"] + shift, pdf_point(line[0], page)[1]))])
            yield "a line pushed over the table's edge", "leaves the table region", copy_with("outside_region", out_of_region)
        bold_face = font_file.replace("Regular", "Bold") if font_file else None

        def bold_lines(page):
            return [c for c in characters(page, 0) if c["bold"] and region.y0 <= c["base"] <= region.y1 and c["c"].strip()]

        def total_down(doc):
            page = doc[doc.page_count - 1]
            base = max(c["base"] for c in bold_lines(page))
            move_glyphs(doc, [(pdf_point(c, page), (c["x"], pdf_point(c, page)[1] - 3)) for c in bold_lines(page) if abs(c["base"] - base) <= 0.1], doc.page_count - 1)
        yield "the total row moved 3 pt down", "total row starts at", copy_with("totaldown", total_down)
        yield "text of another size in the table", "pt in the table", copy_with("tablesize", text("X", (region.x0 + 20, region.y1 - 3), size=14))
        if bold_face:
            def bold_among(doc):
                page = doc[0]
                regular = [c for c in characters(page, 0) if not c["bold"] and region.y0 <= c["base"] <= region.y1 and c["c"].strip()]
                base = min(c["base"] for c in regular) if regular else region.y1 - 3
                page.insert_font(fontname="Fb", fontfile=bold_face)
                page.insert_text((region.x1 - 40, base), "粗", fontname="Fb", fontsize=size, color=(0.07, 0.13, 0.22))
            yield "bold text among the rows", "bold", copy_with("boldrow", bold_among)
            if exp.get("mode", "full") != "full":
                def header_in_variables(doc):
                    page = doc[0]
                    page.insert_font(fontname="Fb", fontfile=bold_face)
                    page.insert_text((region.x0 + TABLE_INSET, region.y0 + TABLE_INSET_Y + ASCENT * size), "表头", fontname="Fb", fontsize=size, color=(0.07, 0.13, 0.22))
                yield "a header in the variable layer", "header in the variable layer", copy_with("varheader", header_in_variables)
            else:
                def forward_on_page_one(doc):
                    # Below the header, whatever its height: between its last line and the first
                    # line after it.
                    page = doc[0]
                    chars = [c for c in characters(page, 0) if region.y0 <= c["base"] <= region.y1 and c["c"].strip()]
                    regular = [c["base"] for c in chars if not c["bold"]]
                    first_regular = min(regular) if regular else max(c["base"] for c in chars)
                    header_last = max([c["base"] for c in chars if c["bold"] and c["base"] < first_regular - 0.1] or [region.y0])
                    page.insert_font(fontname="Fb", fontfile=bold_face)
                    page.insert_text((region.x0 + TABLE_INSET, (header_last + first_regular) / 2), "承前（第 1 页）", fontname="Fb", fontsize=size, color=(0.07, 0.13, 0.22))
                yield "a brought-forward row on page 1", "brought-forward row", copy_with("forward1", forward_on_page_one)

                def fill_body(doc):
                    # The first body row's first cell, taken from its own rule rectangle.
                    page = doc[0]
                    c = first_body_line(page)[0]
                    cell = next(d["rect"] for d in page.get_drawings() if d["width"] is not None and d["rect"].x0 - 0.5 <= c["x"] <= d["rect"].x1 and d["rect"].y0 <= c["base"] <= d["rect"].y1 and d["rect"].height > 1)
                    page.draw_rect(cell, color=None, fill=(0.91, 0.91, 0.91), overlay=False)
                if has_body:
                    yield "a fill on a body cell", "fill on a body cell", copy_with("bodyfill", fill_body)
        if doc_pages(directory, exp) > 1:
            def remove_carry(doc):
                page = doc[0]
                base = max(c["base"] for c in bold_lines(page))
                page.add_redact_annot(pymupdf.Rect(region.x0 + 0.5, base - ASCENT * size - 1, region.x1 - 0.5, base + descent_of(size)))
                page.apply_redactions(images=pymupdf.PDF_REDACT_IMAGE_NONE, graphics=pymupdf.PDF_REDACT_LINE_ART_NONE)
            yield "the carry-forward row removed", "no carry-forward row", copy_with("nocarry", remove_carry)
        if exp.get("mode", "full") == "full":
            def rewrite(doc, old, new):
                # The engine's own operators, in its own content stream (not normalised).
                hit = False
                for xref in doc[0].get_contents():
                    raw = doc.xref_stream(xref).decode("latin1")
                    if old in raw:
                        hit = True
                        doc.update_stream(xref, raw.replace(old, new).encode("latin1"))
                assert hit, f"{old!r} not in the content stream"
            yield "the cell rules thinned", "rule width", copy_with("thinrules", lambda doc: rewrite(doc, "0.5 w", "0.1 w"))
            yield "the cell rules turned white", "rule colour", copy_with("whiterules", lambda doc: rewrite(doc, "0.5882353 SCN", "1 SCN"))
            if doc_pages(directory, exp) == 1:
                yield "the total rule turned white", "total rule colour", copy_with("whitetotal", lambda doc: rewrite(doc, "0.23529412 SCN", "1 SCN"))
            yield "the header fill turned white", "fill", copy_with("whitefill", lambda doc: rewrite(doc, "0.9098039 scn", "1 scn"))
            if doc_pages(directory, exp) > 1:
                def cover_header(doc):
                    page = doc[1]
                    page.draw_rect(pymupdf.Rect(region.x0 - 0.6, region.y0 - 0.6, region.x1 + 0.6, region.y0 + 2 + ASCENT * size + descent_of(size)), color=None, fill=(1, 1, 1), overlay=True)
                yield "the header of page 2 covered", "no ink", copy_with("header2", cover_header)


# ---- schema 2.0 documents (S03-J) ------------------------------------------------------------------

# The greys a rectangle may be filled with, as the luma the page program draws; the rule colour of lines
# and outlines is luma 60. Written here, not read from the platform, so that the contract the checker
# verifies is stated where the documents are checked.
FILL_LUMA = {"grey1": 245, "grey2": 232, "grey3": 215, "grey4": 190}
RULE_LUMA = 60
FIT_MARGIN = 2.0  # a line of a text is narrower than its box by this much, in points
SAFE_TOP, SAFE_BOTTOM = 25, 270  # millimetres: the safe region's vertical extent
BAND_X = 10  # millimetres: where every band starts


def bands2(exp):
    return {b["name"]: b for b in exp["bands"]}


def heights2(exp):
    """The heights, in millimetres, of the page header, the document header, the document footer and the page footer."""
    bands = bands2(exp)
    return tuple(bands[n]["height"] if n in bands else 0 for n in ("page_header", "document_header", "document_footer", "page_footer"))


def region2(exp, number):
    """The table's region on a page: from the bottom of the bands above it to the top of the page footer or,
    on the page that holds the document footer under the table, to the top of that footer."""
    ph, dh, df, pf = heights2(exp)
    t = exp["table"]
    x0 = (BAND_X + t["x"]) * MM
    bottom = (SAFE_BOTTOM - pf) * MM
    footer = exp.get("footer")
    if footer and footer["page"] == number and number <= exp["table_pages"]:
        bottom = footer["top"]
    return pymupdf.Rect(x0, (SAFE_TOP + ph + (dh if number == 1 else 0)) * MM, x0 + t["width"] * MM, bottom)


def bands_on_page2(exp, number):
    """The bands a page carries with the y of their top edge in points: the page header, the document header
    on the first page, the document footer where the expectation puts it, and the page footer."""
    ph, dh, df, pf = heights2(exp)
    bands, footer = bands2(exp), exp.get("footer")
    if "page_header" in bands:
        yield bands["page_header"], SAFE_TOP * MM
    if "document_header" in bands and number == 1:
        yield bands["document_header"], (SAFE_TOP + ph) * MM
    if "document_footer" in bands and footer and footer["page"] == number:
        yield bands["document_footer"], footer["top"]
    if "page_footer" in bands:
        yield bands["page_footer"], (SAFE_BOTTOM - pf) * MM


def page_value(el, number, pages):
    """What a text shows on a page: its value, with {page} and {pages} filled for a page text; the line breaks
    and tabs a wrapped value may hold read as the layout reads them."""
    value = el["text"]
    if el["source"] == "page":
        value = value.replace("{page}", str(number)).replace("{pages}", str(pages))
    elif el["source"] == "group_sum":
        # The platform's own sum prints as the table prints its totals: the exact sum of the group's values at the
        # longest fraction or the column's scale, with thousands separators; the checker computes it itself.
        value = group_digits(exact_sum(el["sum_values"], el["scale"]))
    return value.replace("\r\n", "\n").replace("\r", "\n").replace("\t", " ")


def check_text2(chars, used, el, top, metrics, number, pages):
    """Checks one text element of a band whose top edge is at top (points): its value drawn in its box, on one
    line or, with wrap, on lines 1.5 font sizes apart that hold the value with nothing lost, added or reordered,
    each line aligned and inside the box less the 2 pt margin, in the weight the element says, the plain-advance
    position of every character with the font. Marks the characters it accounts for."""
    problems = []
    value = page_value(el, number, pages)
    size, bold, align = el["size"], el["bold"], el["align"]
    left, right = (BAND_X + el["x"]) * MM, (BAND_X + el["x"] + el["width"]) * MM
    base0 = top + el["y"] * MM + ASCENT * size
    free = [c for c in chars if c["n"] not in used and abs(c["size"] - size) < 0.05 and left - 1 <= (c["x0"] + c["x1"]) / 2 <= right + 1]
    if not el["wrap"]:
        text = value.rstrip()
        if not text.strip():
            return problems  # nothing is drawn for white space alone
        line = [c for c in free if abs(c["base"] - base0) <= 0.1]
        if "".join(c["c"] for c in line).rstrip() != text:
            return [f"{text!r} is not drawn on one line with its baseline at {base0:.2f} pt in its box"]
        used.update(c["n"] for c in line)
        if any(c["bold"] != bold for c in line if c["c"].strip()):
            problems.append(f"{text!r} is not drawn {'bold' if bold else 'regular'}")
        return problems + check_line(line, text, size, left, right, align, metrics, bold)
    # A wrapped value: the lines are the layout's choice; what is checked is that they hold the value and sit as lines.
    capacity = max(1, 1 + int((el["height"] * MM / size - (ASCENT + DESCENT)) / LINE_STEP + 1e-9))
    reach = base0 + (capacity - 1) * LINE_STEP * size + 0.1
    lines = {}
    for c in free:
        if base0 - 0.1 <= c["base"] <= reach:
            lines.setdefault(round((c["base"] - base0) / (LINE_STEP * size)), []).append(c)
    drawn = ""
    for k in sorted(lines):
        cs = sorted(lines[k], key=lambda c: c["x"])
        base = base0 + k * LINE_STEP * size
        if any(abs(c["base"] - base) > 0.1 for c in cs):
            problems.append(f"{value[:30]!r}: a line is not at a baseline of the box ({base:.2f} pt expected)")
            continue
        used.update(c["n"] for c in cs)
        text = "".join(c["c"] for c in cs).rstrip()
        drawn += squash(text)
        if not text.strip():
            continue
        solid = [c for c in cs if c["c"].strip()]
        if any(c["bold"] != bold for c in solid):
            problems.append(f"{text!r} is not drawn {'bold' if bold else 'regular'}")
        if solid[-1]["x1"] - cs[0]["x"] > (right - left) - FIT_MARGIN + 0.3:
            problems.append(f"{text!r} is wider than its box less the margin")
        problems += check_line(cs, text, size, left, right, align, metrics, bold)
    if drawn != squash(value):
        problems.append(f"{value[:30]!r} is not drawn in its box ({drawn[:30]!r} found)")
    return problems


def close(a, b, tolerance=0.3):
    return abs(a - b) <= tolerance


def line_matches(d, ends):
    """Whether a drawing is one straight line between the two points, in either direction."""
    if len(d["items"]) != 1 or d["items"][0][0] != "l":
        return False
    p, q = d["items"][0][1], d["items"][0][2]
    (ax, ay), (bx, by) = ends
    return (close(p.x, ax) and close(p.y, ay) and close(q.x, bx) and close(q.y, by)) or (close(p.x, bx) and close(p.y, by) and close(q.x, ax) and close(q.y, ay))


def claim_drawings(page, claims, drawings):
    """Matches the rectangles and lines the expectation names with the page's drawings, which it takes out of
    drawings: a line by its end points, 1 stroke of the width and rule colour it says; a rectangle by its edges,
    the fill and the outline it says and no other. Returns the problems and the drawings left."""
    problems, left = [], list(drawings)
    for el, top in claims:
        if el["type"] == "line":
            ends = [((BAND_X + el["x1"]) * MM, top + el["y1"] * MM), ((BAND_X + el["x2"]) * MM, top + el["y2"] * MM)]
            hits = [d for d in left if line_matches(d, ends)]
            if len(hits) != 1:
                problems.append(f"line {el['id']}: {len(hits)} drawings at its end points, expected one")
                continue
            d = hits[0]
            left.remove(d)
            if d["width"] is None or abs(d["width"] - el["stroke"]) > 0.05:
                problems.append(f"line {el['id']}: stroke width {d['width']}, expected {el['stroke']}")
            if d["color"] is None or max(abs(v - RULE_LUMA / 255) for v in d["color"][:3]) > 0.02:
                problems.append(f"line {el['id']}: rule colour {None if d['color'] is None else tuple(round(v, 2) for v in d['color'][:3])}")
        else:
            x0, y0 = (BAND_X + el["x"]) * MM, top + el["y"] * MM
            rect = pymupdf.Rect(x0, y0, x0 + el["width"] * MM, y0 + el["height"] * MM)
            hits = [d for d in left if max(abs(d["rect"].x0 - rect.x0), abs(d["rect"].x1 - rect.x1), abs(d["rect"].y0 - rect.y0), abs(d["rect"].y1 - rect.y1)) <= 0.3]
            fills = [d["fill"] for d in hits if d["fill"] is not None]
            strokes = [(d["width"], d["color"]) for d in hits if d["width"] is not None and d["color"] is not None]
            for d in hits:
                left.remove(d)
            want = FILL_LUMA.get(el["fill"])
            if want is None and fills:
                problems.append(f"rectangle {el['id']}: a fill where it has none")
            if want is not None and not fills:
                problems.append(f"rectangle {el['id']}: no fill")
            for fill in fills if want is not None else []:
                if max(abs(v - want / 255) for v in fill[:3]) > 0.02:
                    problems.append(f"rectangle {el['id']}: fill {tuple(round(v, 2) for v in fill[:3])}, expected luma {want}")
            if el["stroke"] > 0 and not strokes:
                problems.append(f"rectangle {el['id']}: no outline")
            if el["stroke"] == 0 and strokes:
                problems.append(f"rectangle {el['id']}: an outline where it has none")
            for width, color in strokes if el["stroke"] > 0 else []:
                if abs(width - el["stroke"]) > 0.05:
                    problems.append(f"rectangle {el['id']}: stroke width {width}, expected {el['stroke']}")
                if max(abs(v - RULE_LUMA / 255) for v in color[:3]) > 0.02:
                    problems.append(f"rectangle {el['id']}: outline colour {tuple(round(v, 2) for v in color[:3])}")
    return problems, left


# ---- schema 2.1 (S03-M, ADR 0005): barcodes, QR codes and images. The checker derives the modules, the quiet zones and the positions
# from the template's numbers and the drawn rectangles itself, and decodes every code with zxing-cpp, which shares nothing with the
# encoder (github.com/boombuler/barcode behind export/barcode) or with the layout. ----
CODED_TYPES = ("barcode", "qrcode", "image")
CODE_QUIET, QR_QUIET = 10, 4  # modules of quiet zone, inside the element's box
CODE_MIN_MODULE, QR_MIN_MODULE = 0.25, 0.4  # millimetres
CODE_BAR_MM, CODE_BAR_RATIO = 5.0, 0.15
HUMAN_GAP = 1.0  # points between the bars and the line of the value
DESCENT = 0.3  # below the last baseline, in font sizes
GRID_TOLERANCE = 0.05  # points: the precision of the numbers a PDF carries
# The data codewords of QR versions 1 to 17 at L, M, Q and H (the standard's table); a symbol of 256 bytes is at most version 17.
QR_DATA_CODEWORDS = [(19, 16, 13, 9), (34, 28, 22, 16), (55, 44, 34, 26), (80, 64, 48, 36), (108, 86, 62, 46), (136, 108, 76, 60), (156, 124, 88, 66),
                     (194, 154, 110, 86), (232, 182, 132, 100), (274, 216, 154, 122), (324, 254, 180, 140), (370, 290, 206, 158), (428, 334, 244, 180),
                     (461, 365, 261, 197), (523, 415, 295, 223), (589, 453, 325, 253), (647, 507, 367, 283)]


def qr_version(nbytes, ecc):
    """The least version that holds nbytes in byte mode at the level, from the standard's table; None beyond version 17."""
    level = "LMQH".index(ecc)
    for version, words in enumerate(QR_DATA_CODEWORDS, 1):
        if words[level] - (2 if version < 10 else 3) >= nbytes:
            return version
    return None


def black_rect(d):
    """The rectangle of a drawing that is one rectangle filled black and not outlined, else None."""
    if len(d["items"]) != 1 or d["items"][0][0] != "re" or d["fill"] is None or d["color"] is not None or max(d["fill"][:3]) > 0.02:
        return None
    return d["items"][0][1]


def take_bars(drawings, box):
    """Splits the drawings into the black rectangles inside box (widened by 0.3 pt) and the others."""
    inside = pymupdf.Rect(box.x0 - 0.3, box.y0 - 0.3, box.x1 + 0.3, box.y1 + 0.3)
    bars, rest = [], []
    for d in drawings:
        r = black_rect(d)
        if r is not None and inside.contains(r):
            bars.append(r)
        else:
            rest.append(d)
    return bars, rest


def decode_in(page, box, formats, module):
    """Decodes the codes inside box (a pymupdf.Rect) from the page rendered with the module of the symbol (points) at 6, 8, 4 and 10
    pixels, whichever the decoder reads first (its binarizer works on blocks of a few pixels), with white around. Returns the zxing
    results with their positions converted to page points; none when no rendering decodes."""
    pad = 24
    for pixels in (6, 8, 4, 10):
        scale = pixels / module
        pix = page.get_pixmap(matrix=pymupdf.Matrix(scale, scale), clip=pymupdf.Rect(box), colorspace=pymupdf.csGRAY, alpha=False)
        width, height = pix.width, pix.height
        samples = pix.samples
        padded = bytearray(b"\xff" * ((width + 2 * pad) * (height + 2 * pad)))
        for y in range(height):
            start = (y + pad) * (width + 2 * pad) + pad
            padded[start:start + width] = samples[y * pix.stride:y * pix.stride + width]
        padded = bytes(padded)  # kept alive for as long as the view is read
        view = zxingcpp.ImageView(memoryview(padded), width + 2 * pad, height + 2 * pad, zxingcpp.ImageFormat.Lum, width + 2 * pad, 1)
        out = []
        for b in zxingcpp.read_barcodes(view, formats=formats, try_rotate=False, try_invert=False):
            corners = [b.position.top_left, b.position.top_right, b.position.bottom_right, b.position.bottom_left]
            out.append((b, [(box.x0 + (c.x - pad) / scale, box.y0 + (c.y - pad) / scale) for c in corners]))
        if out:
            return out
    return []


def check_code128(page, el, top, bars, chars, used, metrics, number, pages):
    """Checks one barcode: its bars (black rectangles from one end to the other of the symbol, each as high as the box less the line of
    the value, on one module grid of the box's width over the symbol and ten modules of quiet zone on each side, a module of at least
    0.25 mm, bars at least the larger of 5 mm and 15 % of the symbol's width high), the line of the value under them when it prints it,
    and what a decoder reads from the page, which must be the value, as Code 128, where the symbol is."""
    name = f"barcode {el['id']}"
    left, box_top = (BAND_X + el["x"]) * MM, top + el["y"] * MM
    width, height = el["width"] * MM, el["height"] * MM
    if el.get("symbology") != "code128":
        return [f"{name}: symbology {el.get('symbology')!r} is not Code 128"]
    if not bars:
        return [f"{name}: no bars are drawn in its box"]
    problems = []
    zone = HUMAN_GAP + (ASCENT + DESCENT) * el["size"] if el["human_readable"] else 0
    bar_height = height - zone
    bars = sorted(bars, key=lambda r: r.x0)
    if any(abs(r.y0 - box_top) > GRID_TOLERANCE or abs(r.height - bar_height) > GRID_TOLERANCE for r in bars):
        problems.append(f"{name}: the bars are not all as high as the box less the line of the value ({bar_height:.2f} pt from {box_top:.2f} pt)")
    widths = [r.width for r in bars] + [b.x0 - a.x1 for a, b in zip(bars, bars[1:])]
    module = min(widths)
    # One module grid: every bar and every space is a whole number of modules.
    if module <= 0 or any(abs(w / module - round(w / module)) * module > GRID_TOLERANCE for w in widths):
        return problems + [f"{name}: the bars and spaces are not whole numbers of one module"]
    symbol = round((bars[-1].x1 - bars[0].x0) / module)
    total = symbol + 2 * CODE_QUIET
    if symbol < 46 or (symbol - 13) % 11 != 0:
        problems.append(f"{name}: a symbol of {symbol} modules is not a Code 128 symbol")
    elif (symbol - 13) // 11 - 3 > len(el["value"]):
        problems.append(f"{name}: a symbol of {symbol} modules has more symbols than characters")
    if abs(module - width / total) > GRID_TOLERANCE / 10 + 1e-4 * module:
        problems.append(f"{name}: the module is {module / MM:.4f} mm, not the box's width over the {total} modules of the symbol and its quiet zones ({width / total / MM:.4f} mm)")
    if abs(bars[0].x0 - (left + CODE_QUIET * module)) > GRID_TOLERANCE or abs(bars[-1].x1 - (left + width - CODE_QUIET * module)) > GRID_TOLERANCE:
        problems.append(f"{name}: the quiet zone is not ten modules on each side inside the box")
    if module / MM < CODE_MIN_MODULE - 1e-3:
        problems.append(f"{name}: the module is {module / MM:.4f} mm, below the least of {CODE_MIN_MODULE} mm")
    if bar_height / MM < max(CODE_BAR_MM, CODE_BAR_RATIO * symbol * module / MM) - 1e-3:
        problems.append(f"{name}: the bars are {bar_height / MM:.2f} mm high, below the larger of {CODE_BAR_MM} mm and 15 % of the symbol's width ({CODE_BAR_RATIO * symbol * module / MM:.2f} mm)")
    if el["human_readable"]:
        line = {"id": el["id"], "type": "text", "source": "literal", "text": el["value"], "wrap": False, "bold": False, "align": "center", "size": el["size"],
                "x": el["x"], "width": el["width"], "y": el["y"] + (height - (ASCENT + DESCENT) * el["size"]) / MM, "height": (ASCENT + DESCENT) * el["size"] / MM}
        problems += check_text2(chars, used, line, top, metrics, number, pages)
    box = pymupdf.Rect(left, box_top, left + width, box_top + height)
    read = decode_in(page, box, zxingcpp.BarcodeFormat.Code128, module)
    if len(read) != 1:
        return problems + [f"{name}: {len(read)} codes decoded from its box, expected one"]
    result, pts = read[0]
    if not result.valid or result.text != el["value"]:
        problems.append(f"{name}: the decoder reads a value that is not the expected one")
    # Where the decoder found the symbol: from the first bar to the last, within the bars' height, to two modules.
    xs, ys = [p[0] for p in pts], [p[1] for p in pts]
    if abs(min(xs) - bars[0].x0) > 2 * module or abs(max(xs) - bars[-1].x1) > 2 * module or min(ys) < box_top - 2 * module or max(ys) > box_top + bar_height + 2 * module:
        problems.append(f"{name}: the decoder finds the symbol at {min(xs):.1f}-{max(xs):.1f} pt, not where its bars are")
    return problems


def check_qr(page, el, top, bars):
    """Checks one QR code: its rectangles (each one module high, a whole number of modules wide, on one grid of the box's side over the
    symbol and four modules of quiet zone on each side, a module of at least 0.4 mm), the symbol's version (the least that holds the
    value at the level), the finder and timing patterns, and what a decoder reads from the page: the value, as a QR code of the level."""
    name = f"QR code {el['id']}"
    left, box_top, side = (BAND_X + el["x"]) * MM, top + el["y"] * MM, el["side"] * MM
    if not bars:
        return [f"{name}: no modules are drawn in its box"]
    problems = []
    module = min(r.height for r in bars)
    if module <= 0 or any(abs(r.height - module) > GRID_TOLERANCE for r in bars):
        return [f"{name}: the rectangles are not all one module high"]
    total = round(side / module)
    n = total - 2 * QR_QUIET
    if abs(module - side / total) > GRID_TOLERANCE / 10 + 1e-4 * module:
        problems.append(f"{name}: the module is {module / MM:.4f} mm, not the box's side over {total} modules ({side / total / MM:.4f} mm)")
    expected = qr_version(len(el["value"].encode("utf-8")), el["ecc"])
    if expected is None or n != 17 + 4 * expected:
        return problems + [f"{name}: a symbol of {n} modules is not version {expected} (the least that holds {len(el['value'].encode('utf-8'))} bytes at {el['ecc']})"]
    if module / MM < QR_MIN_MODULE - 1e-3:
        problems.append(f"{name}: the module is {module / MM:.4f} mm, below the least of {QR_MIN_MODULE} mm")
    grid = [[False] * n for _ in range(n)]
    for r in bars:
        row, col, run = round((r.y0 - box_top) / module) - QR_QUIET, round((r.x0 - left) / module) - QR_QUIET, round(r.width / module)
        if not (0 <= row < n and 0 <= col and col + run <= n and run >= 1) or abs(r.x0 - left - (col + QR_QUIET) * module) > GRID_TOLERANCE or abs(r.y0 - box_top - (row + QR_QUIET) * module) > GRID_TOLERANCE or abs(r.width - run * module) > GRID_TOLERANCE:
            return problems + [f"{name}: a rectangle is not on the module grid inside the quiet zone"]
        for k in range(run):
            if grid[row][col + k]:
                problems.append(f"{name}: a module is drawn twice")
            grid[row][col + k] = True
    # The symbol reaches the edges of the quiet zone (the finder patterns are at three corners), and has finder and timing patterns.
    if min(r.x0 for r in bars) - left > QR_QUIET * module + GRID_TOLERANCE or max(r.x1 for r in bars) < left + side - QR_QUIET * module - GRID_TOLERANCE:
        problems.append(f"{name}: the symbol does not fill its box inside the quiet zone")
    for ox, oy in [(0, 0), (n - 7, 0), (0, n - 7)]:
        for dy in range(7):
            for dx in range(7):
                if grid[oy + dy][ox + dx] != (dx in (0, 6) or dy in (0, 6) or (2 <= dx <= 4 and 2 <= dy <= 4)):
                    problems.append(f"{name}: a finder pattern is not as the standard has it")
                    break
            else:
                continue
            break
    if any(grid[6][i] != (i % 2 == 0) or grid[i][6] != (i % 2 == 0) for i in range(8, n - 8)):
        problems.append(f"{name}: a timing pattern is not as the standard has it")
    box = pymupdf.Rect(left, box_top, left + side, box_top + side)
    read = decode_in(page, box, zxingcpp.BarcodeFormat.QRCode, module)
    if len(read) != 1:
        return problems + [f"{name}: {len(read)} codes decoded from its box, expected one"]
    result, pts = read[0]
    if not result.valid or result.text != el["value"]:
        problems.append(f"{name}: the decoder reads a value that is not the expected one")
    if result.ec_level != el["ecc"]:
        problems.append(f"{name}: the decoder reads error correction level {result.ec_level!r}, not {el['ecc']!r}")
    # The symbol's corners, as the decoder finds them, are the box's less the quiet zone, to three modules (the fourth is estimated).
    corners = [(left + QR_QUIET * module, box_top + QR_QUIET * module), (left + side - QR_QUIET * module, box_top + QR_QUIET * module),
               (left + side - QR_QUIET * module, box_top + side - QR_QUIET * module), (left + QR_QUIET * module, box_top + side - QR_QUIET * module)]
    if any(abs(p[0] - c[0]) > 3 * module or abs(p[1] - c[1]) > 3 * module for p, c in zip(pts, corners)):
        problems.append(f"{name}: the decoder finds the symbol's corners at {[(round(x, 1), round(y, 1)) for x, y in pts]}, not where its modules are")
    return problems


def contained(el, box_left, box_top):
    """The rectangle a picture covers when it is drawn contained in the element's box: scaled to touch it on one axis, centred, aspect ratio kept."""
    w, h = el["width"] * MM, el["height"] * MM
    scale = min(w / el["width_px"], h / el["height_px"])
    dw, dh = el["width_px"] * scale, el["height_px"] * scale
    return pymupdf.Rect(box_left + (w - dw) / 2, box_top + (h - dh) / 2, box_left + (w + dw) / 2, box_top + (h + dh) / 2)


def take_image(images, el, top):
    """Matches the image element with a picture of the page at the contained rectangle, with the pixels it names; returns the problems
    and the pictures left."""
    name = f"image {el['id']}"
    want = contained(el, (BAND_X + el["x"]) * MM, top + el["y"] * MM)
    for i, info in enumerate(images):
        r = pymupdf.Rect(info["bbox"])
        if max(abs(r.x0 - want.x0), abs(r.y0 - want.y0), abs(r.x1 - want.x1), abs(r.y1 - want.y1)) <= 0.3:
            problems = []
            if (info["width"], info["height"]) != (el["width_px"], el["height_px"]):
                problems.append(f"{name}: the picture is {info['width']} x {info['height']} pixels, not {el['width_px']} x {el['height_px']}")
            return problems, images[:i] + images[i + 1:]
    return [f"{name}: no picture is drawn contained in its box ({want.x0:.2f}, {want.y0:.2f}, {want.x1:.2f}, {want.y1:.2f})"], images


def check_document2(directory, exp, font=None):
    """Checks a schema 2.0 document preview (S03-J) against its expectation: the bands of the template in
    millimetres with the values of their texts, the table with its raw rows, and where the layout put the
    document footer. Every page carries the banner, the page header and the page footer, the first the document
    header; the table is checked over its pages in the region each leaves it (the first page's starts under the
    document header); the document footer is under the table's total row, whole, on that page when it fits
    under it and on a page of its own, under the page header, when it does not. Lines and rectangles are
    drawn as the template says, and nothing else is drawn. Returns problems and the layout summary."""
    problems = []
    metrics = faces(font)
    # An expectation with an "issue" record describes an issued group document (S03-K): the strip holds the record's
    # stamp on every page in place of the banner, the PDF is the one the record names, every date of the file is the
    # record's issue time, and the page count is the record's.
    issue = exp.get("issue")
    if issue:
        problems += check_issue_record(directory, exp, issue)
    doc = pymupdf.open(os.path.join(directory, exp["file"]))
    xmp = doc.get_xml_metadata()
    if not re.search(r"pdfaid:part[^0-9]{0,4}2\b", xmp) or not re.search(r"pdfaid:conformance[^A-Z]{0,4}B\b", xmp):
        problems.append("no PDF/A-2b identification in the XMP metadata")
    if issue:
        problems += check_creation_date(doc, xmp, issue)
        if doc.page_count != issue["pages"]:
            problems.append(f"{doc.page_count} pages, the record says {issue['pages']}")
    # A group sum is bound exactly: the expectation's value must be the exact sum of the group's values.
    for band in exp["bands"]:
        for el in band["elements"]:
            if el["type"] == "text" and el["source"] == "group_sum" and el["text"] != exact_sum(el["sum_values"], el["scale"]):
                problems.append(f"group sum {el['id']}: the bound sum is not the exact sum of the group's values")
    # The issue number (schema 2.1): an element that shows it shows the record's number in an issued document and the placeholder in a
    # preview, never another value; the expectation is checked here, so that the checks of the drawing below (which compare the file
    # with the expectation) cannot agree with a wrong value.
    shown = issue["number"] if issue else "PREVIEW-NUMBER"
    for band in exp["bands"]:
        for el in band["elements"]:
            if el.get("source") == "issue_number" and el["type"] in ("text", "barcode", "qrcode") and el["text" if el["type"] == "text" else "value"] != shown:
                problems.append(f"element {el['id']} shows the issue number: the document must show the {'record' + chr(39) + 's number' if issue else 'placeholder ' + shown}")
    pages, footer, table_pages = doc.page_count, exp.get("footer"), exp["table_pages"]
    want_pages = footer["page"] if footer else table_pages
    if pages != want_pages or table_pages < 1 or table_pages > pages:
        return problems + [f"{pages} pages, expected {want_pages} ({table_pages} of them with the table)"], {"pages": pages}
    shim = {"elements": [], "offset_x": 0, "offset_y": 0, "background": False, "issue": issue}
    ph, dh, df, pf = heights2(exp)
    strip = pymupdf.Rect(10 * MM, 10 * MM, 200 * MM, 18 * MM)
    table_pages_data, per_page = [], []
    for i, page in enumerate(doc):
        number = i + 1
        # Barcodes, QR codes and images (schema 2.1) of the bands this page carries: checked after the texts, with the drawings they own.
        coded = [(el, top) for band, top in bands_on_page2(exp, number) for el in band["elements"] if el["type"] in CODED_TYPES]
        page_problems, chars, used = check_page(directory, page, i, dict(shim, element_images=sum(1 for el, top in coded if el["type"] == "image")), metrics, None)
        problems += [f"page {number}: {p}" for p in page_problems]
        claims, allowed = [], [strip]
        for band, top in bands_on_page2(exp, number):
            for el in band["elements"]:
                if el["type"] == "text":
                    found = check_text2(chars, used, el, top, metrics, number, pages)
                    problems += [f"page {number}: {p}" for p in found]
                    allowed.append(pymupdf.Rect((BAND_X + el["x"]) * MM, top + el["y"] * MM, (BAND_X + el["x"] + el["width"]) * MM, top + (el["y"] + el["height"]) * MM))
                elif el["type"] in CODED_TYPES:
                    side = el["side"] if el["type"] == "qrcode" else None
                    allowed.append(pymupdf.Rect((BAND_X + el["x"]) * MM, top + el["y"] * MM, (BAND_X + el["x"] + (side if side else el["width"])) * MM, top + (el["y"] + (side if side else el["height"])) * MM))
                else:
                    claims.append((el, top))
                    if el["type"] == "line":
                        allowed.append(pymupdf.Rect(min(el["x1"], el["x2"]) * MM + BAND_X * MM, top + min(el["y1"], el["y2"]) * MM, max(el["x1"], el["x2"]) * MM + BAND_X * MM, top + max(el["y1"], el["y2"]) * MM))
                    else:
                        allowed.append(pymupdf.Rect((BAND_X + el["x"]) * MM, top + el["y"] * MM, (BAND_X + el["x"] + el["width"]) * MM, top + (el["y"] + el["height"]) * MM))
        # The banner's white background is the page program's own: it is the strip, filled.
        banner_box = lambda d: d["fill"] is not None and max(abs(d["rect"].x0 - strip.x0), abs(d["rect"].x1 - strip.x1), abs(d["rect"].y0 - strip.y0), abs(d["rect"].y1 - strip.y1)) <= 0.1
        drawings = [d for d in page.get_drawings() if not banner_box(d)]
        pictures = page.get_image_info(xrefs=True)
        for el, top in coded:
            if el["type"] == "image":
                found, pictures = take_image(pictures, el, top)
            else:
                box = pymupdf.Rect((BAND_X + el["x"]) * MM, top + el["y"] * MM, (BAND_X + el["x"] + (el["side"] if el["type"] == "qrcode" else el["width"])) * MM, top + (el["y"] + (el["side"] if el["type"] == "qrcode" else el["height"])) * MM)
                bars, drawings = take_bars(drawings, box)
                found = check_qr(page, el, top, bars) if el["type"] == "qrcode" else check_code128(page, el, top, bars, chars, used, metrics, number, pages)
            problems += [f"page {number}: {p}" for p in found]
        if pictures:
            problems.append(f"page {number}: {len(pictures)} pictures that no image element draws")
        found, rest = claim_drawings(page, claims, drawings)
        problems += [f"page {number}: {p}" for p in found]
        if number <= table_pages:
            region = region2(exp, number)
            allowed.append(region)
            table_pages_data.append((page, [c for c in chars if c["n"] not in used], None))
            per_page.append(rest)
            used.update(c["n"] for c in chars if region.x0 - 1 <= (c["x0"] + c["x1"]) / 2 <= region.x1 + 1 and region.y0 - 1 <= c["base"] <= region.y1 + 1 and c["n"] not in used)
        else:
            other = [d for d in rest]
            if other:
                problems.append(f"page {number}: {len(other)} drawings that no element draws")
        leftover = [c for c in chars if c["n"] not in used and c["c"].strip()]
        if leftover:
            problems.append(f"page {number}: unexpected text {''.join(c['c'] for c in leftover)[:40]!r}")
        if ink_outside(page, allowed):
            problems.append(f"page {number}: ink outside the elements, the table and the banner")
    layout = {"pages": pages}
    ends = []
    problems += check_table(exp, table_pages_data, metrics, layout, region_of=lambda number: region2(exp, number), ends=ends, drawings_of=lambda number: per_page[number - 1])
    for number, rest in enumerate(per_page, 1):
        region = region2(exp, number)
        inside = lambda r: r.x1 >= region.x0 - 1 and r.x0 <= region.x1 + 1 and r.y1 >= region.y0 - 1 and r.y0 <= region.y1 + 1
        stray = [d for d in rest if not inside(d["rect"])]
        if stray:
            problems.append(f"page {number}: {len(stray)} drawings that no element draws")
    # The document footer follows the table's end, whole: on the last table page when it fits above the page
    # footer, else on a page of its own below the page header.
    if footer and len(ends) == table_pages:
        bottom = (SAFE_BOTTOM - pf) * MM
        fits = ends[-1] + df * MM <= bottom + 0.01
        want_page, want_top = (table_pages, ends[-1]) if fits else (table_pages + 1, (SAFE_TOP + ph) * MM)
        if footer["page"] != want_page:
            problems.append(f"the document footer is on page {footer['page']}, expected {want_page}" + ("" if fits else " (it does not fit under the table)"))
        elif abs(footer["top"] - want_top) > 0.1:
            problems.append(f"the document footer starts at {footer['top']:.2f}, expected {want_top:.2f}")
        layout["footer_page"] = footer["page"]
    return problems, layout


def exp_sum_has_separators(el):
    """Whether the printed sum of a group_sum element has thousands separators, so that leaving them out is a difference."""
    return "," in group_digits(exact_sum(el["sum_values"], el["scale"]))


def document2_corruptions(exp):
    """Yields (what, corrupted expectation, what a problem must say) for each corruption of a 2.0 expectation that applies."""
    def changed(name, change, expected=""):
        e = copy.deepcopy(exp)
        change(e)
        return name, e, expected

    def elements(kind=None):
        for b, band in enumerate(exp["bands"]):
            for k, el in enumerate(band["elements"]):
                if kind is None or el["type"] == kind:
                    yield b, k, el
    yield from issue_expectation_corruptions(exp, changed)
    single = next(((b, k) for b, k, el in elements("text") if not el["wrap"] and el["source"] != "page" and len(el["text"].rstrip()) >= 2), None)
    if single:
        b, k = single
        el = exp["bands"][b]["elements"][k]
        yield changed("a text value changed", lambda e: e["bands"][b]["elements"][k].__setitem__("text", el["text"] + "X"), "is not drawn on one line")
        yield changed("a text moved by 0.5 mm", lambda e: e["bands"][b]["elements"][k].__setitem__("x", el["x"] + 0.5), "is not")
        yield changed("a text moved down by 0.5 mm", lambda e: e["bands"][b]["elements"][k].__setitem__("y", el["y"] + 0.5), "is not drawn on one line")
        yield changed("a text's weight changed", lambda e: e["bands"][b]["elements"][k].__setitem__("bold", not el["bold"]), "drawn")
        yield changed("a text's type size changed", lambda e: e["bands"][b]["elements"][k].__setitem__("size", el["size"] + 1), "is not drawn on one line")
        flip = {"left": "right", "right": "center", "center": "left"}
        yield changed("a text's alignment changed", lambda e: e["bands"][b]["elements"][k].__setitem__("align", flip[el["align"]]), "is not aligned")
        yield changed("a text removed", lambda e: e["bands"][b]["elements"].pop(k), "unexpected text")
    summed_text = next(((b, k) for b, k, el in elements("text") if el["source"] == "group_sum"), None)
    if summed_text:
        b, k = summed_text
        el = exp["bands"][b]["elements"][k]
        yield changed("a group sum's values changed", lambda e: e["bands"][b]["elements"][k].__setitem__("sum_values", el["sum_values"] + ["0.01"]), "is not the exact sum")
        yield changed("a group sum's scale changed", lambda e: e["bands"][b]["elements"][k].__setitem__("scale", el["scale"] + 1), "is not the exact sum")
        yield changed("a group sum's value and values changed together", lambda e: (e["bands"][b]["elements"][k].__setitem__("sum_values", el["sum_values"] + ["0.01"]), e["bands"][b]["elements"][k].__setitem__("text", exact_sum(el["sum_values"] + ["0.01"], el["scale"]))), "is not drawn on one line")
    yield changed("a text added", lambda e: e["bands"][0]["elements"].append({"id": "e99", "type": "text", "source": "literal", "text": "ADDED", "wrap": False, "bold": False, "align": "left", "size": 10, "x": 0, "y": 0, "width": 40, "height": 6}), "is not drawn on one line")
    wrap = next(((b, k) for b, k, el in elements("text") if el["wrap"] and squash(el["text"])), None)
    if wrap:
        b, k = wrap
        el = exp["bands"][b]["elements"][k]
        yield changed("a wrapped value changed", lambda e: e["bands"][b]["elements"][k].__setitem__("text", el["text"] + "X"), "is not drawn in its box")
        yield changed("a wrapped box narrowed to 10 mm", lambda e: e["bands"][b]["elements"][k].__setitem__("width", 10), "")
        yield changed("a wrapped value moved by 0.5 mm", lambda e: e["bands"][b]["elements"][k].__setitem__("y", el["y"] + 0.5), "")
    paged = next(((b, k) for b, k, el in elements("text") if el["source"] == "page"), None)
    if paged:
        b, k = paged
        el = exp["bands"][b]["elements"][k]
        # With one page {page} and {pages} are the same number: the swap is then a digit more.
        pages = exp["footer"]["page"] if exp.get("footer") else exp["table_pages"]
        yield changed("a page text's placeholder swapped", lambda e: e["bands"][b]["elements"][k].__setitem__("text", el["text"].replace("{page}", "{pages}", 1) if "{page}" in el["text"] and pages > 1 else el["text"] + "9"), "is not drawn on one line")
    for b, band in enumerate(exp["bands"]):
        if band["name"] != "document_footer":
            yield changed(f"the {band['name']} made 0.5 mm taller", lambda e: e["bands"][b].__setitem__("height", band["height"] + 0.5))
    if exp.get("footer"):
        # The footer's height decides whether it fits under the table: made so tall that it cannot, or, when it was moved to
        # a page of its own, so short that it can, the footer is on the wrong page.
        index = next(i for i, band in enumerate(exp["bands"]) if band["name"] == "document_footer")
        ph, dh, df, pf = heights2(exp)
        moved = exp["footer"]["page"] > exp["table_pages"]
        yield changed("the document footer made " + ("short enough to fit under the table" if moved else "too tall to fit under the table"),
                      lambda e: e["bands"][index].__setitem__("height", 5 if moved else SAFE_BOTTOM - SAFE_TOP - ph - pf), "the document footer is on page")
    line = next(((b, k) for b, k, el in elements("line")), None)
    if line:
        b, k = line
        el = exp["bands"][b]["elements"][k]
        yield changed("a line moved by 0.5 mm", lambda e: (e["bands"][b]["elements"][k].__setitem__("y1", el["y1"] + 0.5), e["bands"][b]["elements"][k].__setitem__("y2", el["y2"] + 0.5)), "drawings at its end points")
        yield changed("a line's stroke changed", lambda e: e["bands"][b]["elements"][k].__setitem__("stroke", el["stroke"] + 0.25), "stroke width")
        yield changed("a line removed from the expectation", lambda e: e["bands"][b]["elements"].pop(k), "no element draws")
    rect = next(((b, k) for b, k, el in elements("rect") if el["fill"] != "none"), None)
    if rect:
        b, k = rect
        el = exp["bands"][b]["elements"][k]
        yield changed("a rectangle's fill changed", lambda e: e["bands"][b]["elements"][k].__setitem__("fill", "grey4" if el["fill"] != "grey4" else "grey1"), "fill")
        yield changed("a rectangle's fill removed", lambda e: e["bands"][b]["elements"][k].__setitem__("fill", "none") or e["bands"][b]["elements"][k].__setitem__("stroke", el["stroke"] or 0.5), "")
        yield changed("a rectangle moved by 0.5 mm", lambda e: e["bands"][b]["elements"][k].__setitem__("x", el["x"] + 0.5), "")
        yield changed("a rectangle's outline widened", lambda e: e["bands"][b]["elements"][k].__setitem__("stroke", el["stroke"] + 0.25), "")
        yield changed("a rectangle removed from the expectation", lambda e: e["bands"][b]["elements"].pop(k), "no element draws")
    # Schema 2.1: the controls of a barcode, a QR code and an image, one each, whichever the expectation has.
    numbered = next(((b, k, el) for b, k, el in elements() if el.get("source") == "issue_number"), None)
    if numbered:
        b, k, el = numbered
        key = "text" if el["type"] == "text" else "value"
        yield changed("an issue number element shows another number", lambda e: e["bands"][b]["elements"][k].__setitem__(key, "ZZ-999999"), "must show the")
    barcode = next(((b, k) for b, k, el in elements("barcode")), None)
    if barcode:
        b, k = barcode
        el = exp["bands"][b]["elements"][k]
        yield changed("a barcode's value changed", lambda e: e["bands"][b]["elements"][k].__setitem__("value", el["value"] + "X"), "decoder reads a value")
        yield changed("a barcode moved by 0.5 mm", lambda e: e["bands"][b]["elements"][k].__setitem__("x", el["x"] + 0.5), "quiet zone")
        yield changed("a barcode's box made 5 mm wider", lambda e: e["bands"][b]["elements"][k].__setitem__("width", el["width"] + 5), "the module is")
        yield changed("a barcode's box made 3 mm lower", lambda e: e["bands"][b]["elements"][k].__setitem__("height", el["height"] + 3), "high")
        yield changed("a barcode's printed value switched", lambda e: (e["bands"][b]["elements"][k].__setitem__("human_readable", not el["human_readable"]), e["bands"][b]["elements"][k].__setitem__("size", 0 if el["human_readable"] else 9)), "high")
        yield changed("a barcode removed from the expectation", lambda e: e["bands"][b]["elements"].pop(k), "no element draws")
    qr = next(((b, k) for b, k, el in elements("qrcode")), None)
    if qr:
        b, k = qr
        el = exp["bands"][b]["elements"][k]
        yield changed("a QR code's value changed", lambda e: e["bands"][b]["elements"][k].__setitem__("value", el["value"] + "X"), "")
        yield changed("a QR code's level changed", lambda e: e["bands"][b]["elements"][k].__setitem__("ecc", "L" if el["ecc"] != "L" else "H"), "")
        yield changed("a QR code moved by 0.5 mm", lambda e: e["bands"][b]["elements"][k].__setitem__("x", el["x"] + 0.5), "module grid")
        yield changed("a QR code's box made 2 mm larger", lambda e: e["bands"][b]["elements"][k].__setitem__("side", el["side"] + 2), "module")
        yield changed("a QR code removed from the expectation", lambda e: e["bands"][b]["elements"].pop(k), "no element draws")
    image = next(((b, k) for b, k, el in elements("image")), None)
    if image:
        b, k = image
        el = exp["bands"][b]["elements"][k]
        yield changed("an image moved by 1 mm", lambda e: e["bands"][b]["elements"][k].__setitem__("x", el["x"] + 1), "no picture is drawn contained")
        yield changed("an image's box made 3 mm narrower", lambda e: e["bands"][b]["elements"][k].__setitem__("width", el["width"] - 3), "no picture is drawn contained")
        yield changed("an image of other pixels", lambda e: (e["bands"][b]["elements"][k].__setitem__("width_px", el["width_px"] * 2), e["bands"][b]["elements"][k].__setitem__("height_px", el["height_px"] * 2)), "pixels, not")
        yield changed("an image removed from the expectation", lambda e: e["bands"][b]["elements"].pop(k), "no image element draws")
    t = exp["table"]
    rows, columns = t["rows"] or [], t["columns"]
    solid = next((i for i, row in enumerate(rows) if any(squash(v) for v in row)), None)
    if solid is not None:
        cell = next(j for j, v in enumerate(rows[solid]) if squash(v))
        yield changed("a table cell changed", lambda e: e["table"]["rows"][solid].__setitem__(cell, e["table"]["rows"][solid][cell] + "X"))
        yield changed("a table row removed", lambda e: e["table"]["rows"].pop(solid))
        yield changed("a table row added", lambda e: e["table"]["rows"].insert(solid, list(e["table"]["rows"][solid])))
    summed = next((j for j, c in enumerate(columns) if j > 0 and c["metric"]), None)
    if summed is not None and rows:
        yield changed("a table amount changed", lambda e: e["table"]["rows"][0].__setitem__(summed, "0.01"))
    yield changed("a column label changed", lambda e: e["table"]["columns"][0].__setitem__("label", columns[0]["label"] + "X"))
    yield changed("the table moved by 0.5 mm", lambda e: e["table"].__setitem__("x", t["x"] + 0.5))
    if len(columns) > 1:
        yield changed("a column width changed", lambda e: (e["table"]["columns"][0].__setitem__("width", columns[0]["width"] - 5), e["table"]["columns"][-1].__setitem__("width", columns[-1]["width"] + 5)))
        flip_column = {"left": "right", "right": "center", "center": "left"}
        current = columns[0].get("align") or ("right" if columns[0]["metric"] else "left")
        yield changed("a column's alignment changed", lambda e: e["table"]["columns"][0].__setitem__("align", flip_column[current]), "is not aligned")
    footer = exp.get("footer")
    if footer:
        yield changed("the document footer moved by 1 mm", lambda e: e["footer"].__setitem__("top", footer["top"] + MM), "")
        yield changed("the document footer on another page", lambda e: e["footer"].__setitem__("page", footer["page"] + 1 if footer["page"] == exp["table_pages"] else footer["page"] - 1), "")
        yield changed("the document footer removed from the expectation", lambda e: (e.pop("footer"), e["bands"].__delitem__(next(i for i, b in enumerate(e["bands"]) if b["name"] == "document_footer"))), "")
    yield changed("a table page more", lambda e: e.__setitem__("table_pages", exp["table_pages"] + 1), "")
    if exp["table_pages"] > 1:
        yield changed("a table page less", lambda e: e.__setitem__("table_pages", exp["table_pages"] - 1), "")


def document2_pdf_corruptions(directory, exp, scratch, font_file=None):
    """Yields (what, what a problem must say, directory of the corrupted copy) for the PDF-level corruptions of a 2.0 document."""
    font = pymupdf.Font("cjk")

    def copy_with(name, change):
        doc = pymupdf.open(os.path.join(directory, exp["file"]))
        change(doc)
        target = os.path.join(scratch, name)
        os.makedirs(target, exist_ok=True)
        doc.save(os.path.join(target, exp["file"]))
        return target

    def text(t, at, color=(0, 0, 0), embedded=True, size=10):
        def change(doc):
            p = doc[0]
            if embedded:
                p.insert_font(fontname="Fx", fontbuffer=font.buffer)
                p.insert_text(at, t, fontname="Fx", fontsize=size, color=color)
            else:
                p.insert_text(at, t, fontname="helv", fontsize=size, color=color)
        return change

    def move_glyphs(doc, moves, number=0):
        page = doc[number]
        page.clean_contents()
        xref = page.get_contents()[0]
        raw = doc.xref_stream(xref).decode("latin1")

        def move(m):
            x, y = float(m.group(1)), float(m.group(2))
            for (wx, wy), (nx, ny) in moves:
                if abs(x - wx) < 0.01 and abs(y - wy) < 0.01:
                    return f"1 0 0 -1 {nx:.6f} {ny:.6f} cm"
            return m.group(0)
        moved = re.sub(r"1 0 0 -1 (\S+) (\S+) cm", move, raw)
        assert moved != raw, "the glyphs' transformations were not found"
        doc.update_stream(xref, moved.encode("latin1"))

    def rewrite(doc, old, new, number=0):
        hit = False
        for xref in doc[number].get_contents():
            raw = doc.xref_stream(xref).decode("latin1")
            if old in raw:
                hit = True
                doc.update_stream(xref, raw.replace(old, new).encode("latin1"))
        assert hit, f"{old!r} not in the content stream"

    pdf_point = lambda c, page: (c["x"], page.rect.height - c["base"])
    free = (105 * MM, 285 * MM)
    yield "extra text", "unexpected text", copy_with("extra", text("X", free))
    yield from issue_pdf_corruptions(directory, exp, copy_with, text, font)
    yield "a font that is not embedded", "embedded as n/a", copy_with("font", text("X", free, embedded=False))
    yield "the XMP metadata removed", "PDF/A-2b identification", copy_with("xmp", lambda doc: doc.del_xml_metadata())
    yield "an extra page", "pages, expected", copy_with("pages", lambda doc: doc.new_page())
    if doc_pages(directory, exp) > 1:
        yield "the last page removed", "pages, expected", copy_with("lastpage", lambda doc: doc.delete_page(doc.page_count - 1))
    yield "a smaller page", "not A4", copy_with("mediabox", lambda doc: doc[0].set_mediabox(pymupdf.Rect(0, 0, 500, 700)))
    yield "a value in a light colour", "light colour", copy_with("light", text("X", free, color=(0.9, 0.9, 0.9)))
    yield "a replacement character", "replacement character", copy_with("fffd", text("�", free))
    yield "a shape outside the elements", "ink outside", copy_with("outside", lambda doc: doc[0].draw_circle((105 * MM, 285 * MM), 3, color=(0, 0, 0), fill=(0, 0, 0)))
    yield "text of another size in the table", "pt in the table", copy_with("tablesize", text("X", (region2(exp, 1).x0 + 20, region2(exp, 1).y1 - 3), size=14))

    def line_of(page, el, top):
        base = top + el["y"] * MM + ASCENT * el["size"]
        left, right = (BAND_X + el["x"]) * MM, (BAND_X + el["x"] + el["width"]) * MM
        return sorted([c for c in characters(page, 0) if abs(c["base"] - base) <= 0.1 and left - 1 <= c["x"] <= right + 1 and c["c"].strip() and not c["mark"]], key=lambda c: c["x"])
    header = [(el, top) for band, top in bands_on_page2(exp, 1) for el in band["elements"] if el["type"] == "text" and not el["wrap"] and el["source"] != "page" and len(el["text"].rstrip()) >= 2 and not el["bold"]]
    if header:
        el, top = header[0]

        def swap(doc):
            page = doc[0]
            line = line_of(page, el, top)
            a, b = line[0], line[1]
            move_glyphs(doc, [(pdf_point(a, page), (b["x"], pdf_point(a, page)[1])), (pdf_point(b, page), (a["x"], pdf_point(b, page)[1]))])
        yield "two glyphs swapped", "left to right", copy_with("swap", swap)

        def shift(doc):
            page = doc[0]
            c = line_of(page, el, top)[1]
            move_glyphs(doc, [(pdf_point(c, page), (c["x"] + 1, pdf_point(c, page)[1]))])
        if font_file:
            yield "a glyph moved by 1 pt", "plain-advance position", copy_with("shift", shift)

        def cover(doc):
            base = top + el["y"] * MM + ASCENT * el["size"]
            doc[0].draw_rect(pymupdf.Rect(0, base - ASCENT * el["size"], 210 * MM, base + 0.3 * el["size"]), color=None, fill=(1, 1, 1), overlay=True)
        yield "a text covered by a white box", "no ink", copy_with("cover", cover)

        def down(doc):
            page = doc[0]
            line = line_of(page, el, top)
            move_glyphs(doc, [(pdf_point(c, page), (c["x"], pdf_point(c, page)[1] - 3)) for c in line])
        yield "a text moved 3 pt down", "baseline", copy_with("down", down)
    right_aligned = [(el, top) for band, top in bands_on_page2(exp, 1) for el in band["elements"] if el["type"] == "text" and not el["wrap"] and el["align"] == "right" and el["text"].rstrip() and el["source"] != "page"]
    if right_aligned:
        el, top = right_aligned[0]

        def past_edge(doc):
            page = doc[0]
            c = line_of(page, el, top)[-1]
            move_glyphs(doc, [(pdf_point(c, page), (c["x"] + 0.6, pdf_point(c, page)[1]))])
        yield "the last glyph of a right-aligned text 0.6 pt past the edge", "leaves its box", copy_with("edge", past_edge)
    if any(el["type"] == "line" for band in exp["bands"] for el in band["elements"]):
        yield "the rules turned white", "colour", copy_with("whiterules", lambda doc: rewrite(doc, ".23529412 SCN", "1 SCN"))
        yield "the rules thinned", "stroke width", copy_with("thinrules", lambda doc: rewrite(doc, ".25 w", ".1 w") if any(el["type"] == "line" and el["stroke"] == 0.25 for band in exp["bands"] for el in band["elements"]) else rewrite(doc, ".5 w", ".1 w"))
    fills = [el["fill"] for band in exp["bands"] for el in band["elements"] if el["type"] == "rect" and el["fill"] != "none"]
    if fills:
        want = {"grey1": ".9607843 scn", "grey2": ".9098039 scn", "grey3": ".84313729 scn", "grey4": ".74509808 scn"}[fills[0]]
        yield "a rectangle's fill turned white", "fill", copy_with("whitefill", lambda doc: rewrite(doc, want, "1 scn"))

        def cover_rect(doc):
            band, top = next((b, t) for b, t in bands_on_page2(exp, 1) for el in b["elements"] if el["type"] == "rect" and el["fill"] != "none")
            el = next(e for e in band["elements"] if e["type"] == "rect" and e["fill"] != "none")
            x0, y0 = (BAND_X + el["x"]) * MM, top + el["y"] * MM
            doc[0].draw_rect(pymupdf.Rect(x0 + 1, y0 + 1, x0 + el["width"] * MM - 1, y0 + el["height"] * MM - 1), color=None, fill=(1, 1, 1), overlay=True)
        if any(el["type"] == "rect" and el["fill"] != "none" for band, top in bands_on_page2(exp, 1) for el in band["elements"]):
            yield "a rectangle covered by a white box", "no element draws", copy_with("coverrect", cover_rect)
    footer = exp.get("footer")
    if footer:
        band = bands2(exp)["document_footer"]
        texts = [el for el in band["elements"] if el["type"] == "text" and not el["wrap"] and el["text"].rstrip() and el["source"] != "page"]
        if texts:
            el = texts[0]

            def footer_down(doc):
                page = doc[footer["page"] - 1]
                base = footer["top"] + el["y"] * MM + ASCENT * el["size"]
                left, right = (BAND_X + el["x"]) * MM, (BAND_X + el["x"] + el["width"]) * MM
                line = [c for c in characters(page, 0) if abs(c["base"] - base) <= 0.1 and left - 1 <= c["x"] <= right + 1 and c["c"].strip()]
                move_glyphs(doc, [(pdf_point(c, page), (c["x"], pdf_point(c, page)[1] - 3)) for c in line], footer["page"] - 1)
            yield "the document footer's text 3 pt lower", "baseline", copy_with("footerdown", footer_down)
    # A group sum drawn as the exact digits without the table's separators: the sum is covered by a redaction and the
    # same digits are set in its place.
    all_pages = exp["footer"]["page"] if exp.get("footer") else exp["table_pages"]
    summed = next(((number, el, top) for number in range(1, all_pages + 1) for band, top in bands_on_page2(exp, number) for el in band["elements"]
                   if el["type"] == "text" and el["source"] == "group_sum" and not el["wrap"] and exp_sum_has_separators(el)), None)
    if summed:
        number, el, top = summed

        def ungroup(doc):
            page = doc[number - 1]
            base = top + el["y"] * MM + ASCENT * el["size"]
            left, right = (BAND_X + el["x"]) * MM, (BAND_X + el["x"] + el["width"]) * MM
            page.add_redact_annot(pymupdf.Rect(left - 0.5, base - ASCENT * el["size"], right + 0.5, base + 0.3 * el["size"]))
            page.apply_redactions(images=pymupdf.PDF_REDACT_IMAGE_NONE, graphics=pymupdf.PDF_REDACT_LINE_ART_NONE)
            page.insert_font(fontname="Fx", fontbuffer=font.buffer)
            page.insert_text((left + 1, base), exact_sum(el["sum_values"], el["scale"]), fontname="Fx", fontsize=el["size"], color=(0.07, 0.13, 0.22))
        yield "the group sum drawn without its separators", "is not drawn on one line", copy_with("ungrouped", ungroup)
    total_rows = exp["table"]["rows"]
    if total_rows and exp["table_pages"] > 1:
        yield "the last table page removed", "pages, expected", copy_with("lasttable", lambda doc: doc.delete_page(exp["table_pages"] - 1))
    # Schema 2.1: a code moved, drawn below its least module size, damaged or drawn as a picture instead of as rectangles.
    yield from coded_pdf_corruptions(directory, exp, copy_with)




def coded_pdf_corruptions(directory, exp, copy_with):
    """Yields (what, what a problem must say, directory of the corrupted copy[, the expectation to check it against]) for the
    corruptions of the first barcode and the first QR code on the first page of a 2.1 document: moved, drawn with a module below
    the least, damaged, and drawn as a picture instead of as rectangles."""
    page = pymupdf.open(os.path.join(directory, exp["file"]))[0]
    drawings = [d for d in page.get_drawings() if black_rect(d) is not None]
    done = set()
    # The first QR code, and the first barcode that does not print its value (the module control needs room for the line of the value).
    candidates = [(el, top) for band, top in bands_on_page2(exp, 1) for el in band["elements"] if el["type"] in ("barcode", "qrcode")]
    candidates.sort(key=lambda c: c[0]["type"] == "barcode" and c[0]["human_readable"])
    for el, top in candidates:
        if True:
            if el["type"] in done:
                continue
            qr = el["type"] == "qrcode"
            left, box_top = (BAND_X + el["x"]) * MM, top + el["y"] * MM
            width, height = (el["side"], el["side"]) if qr else (el["width"], el["height"])
            box = pymupdf.Rect(left, box_top, left + width * MM, box_top + height * MM)
            inside = pymupdf.Rect(box.x0 - 0.3, box.y0 - 0.3, box.x1 + 0.3, box.y1 + 0.3)
            mine = [black_rect(d) for d in drawings if inside.contains(black_rect(d))]
            if not mine:
                continue
            done.add(el["type"])
            name, tag = ("QR code" if qr else "barcode"), el["id"]
            bounds = pymupdf.Rect(min(r.x0 for r in mine), min(r.y0 for r in mine), max(r.x1 for r in mine), max(r.y1 for r in mine))
            bars = sorted(mine, key=lambda r: r.x0)
            module = min(r.height for r in mine) if qr else min([r.width for r in bars] + [b.x0 - a.x1 for a, b in zip(bars, bars[1:])])
            total = round(width * MM / module) if qr else round((bars[-1].x1 - bars[0].x0) / module) + 2 * CODE_QUIET

            def redraw(change, rects=mine, bounds=bounds):
                """Removes the rectangles of the code and draws the given ones in black in their place."""
                def apply(d):
                    p = d[0]
                    p.add_redact_annot(pymupdf.Rect(bounds.x0 - 0.5, bounds.y0 - 0.5, bounds.x1 + 0.5, bounds.y1 + 0.5))
                    p.apply_redactions(images=pymupdf.PDF_REDACT_IMAGE_NONE, graphics=pymupdf.PDF_REDACT_LINE_ART_REMOVE_IF_TOUCHED, text=pymupdf.PDF_REDACT_TEXT_NONE)
                    for r in change(rects):
                        p.draw_rect(r, color=None, fill=(0, 0, 0), overlay=True)
                return apply
            shift = 1.5 * MM
            yield (f"the {name} {tag} moved by 1.5 mm", "module grid" if qr else "quiet zone",
                   copy_with(f"moved-{tag}", redraw(lambda rs: [pymupdf.Rect(r.x0 + shift, r.y0, r.x1 + shift, r.y1) for r in rs])))
            # Drawn with a module below the least, in a box that is exactly as large as that module makes it, so that nothing else is
            # wrong: the checker must report the module alone.
            least = QR_MIN_MODULE if qr else CODE_MIN_MODULE
            narrow = 0.8 * least * MM
            f = narrow / module
            smaller = copy.deepcopy(exp)
            for b in smaller["bands"]:
                for e in b["elements"]:
                    if e["id"] == tag:
                        if qr:
                            e["side"] = total * narrow / MM
                        else:
                            e["width"], e["human_readable"], e["size"] = total * narrow / MM, False, 0
            if qr:
                make = lambda rs: [pymupdf.Rect(left + (r.x0 - left) * f, box_top + (r.y0 - box_top) * f, left + (r.x1 - left) * f, box_top + (r.y1 - box_top) * f) for r in rs]
            else:
                make = lambda rs: [pymupdf.Rect(left + (r.x0 - left) * f, r.y0, left + (r.x1 - left) * f, r.y1) for r in rs]
            if qr or not el["human_readable"]:
                yield (f"the {name} {tag} drawn with a module of {0.8 * least:.2f} mm", "below the least", copy_with(f"small-{tag}", redraw(make)), smaller)
            # Damaged: one rectangle of a barcode, or the first finder pattern of a QR code, covered by a white box the decoder cannot read through.
            if qr:
                victim = pymupdf.Rect(bounds.x0 - 0.2, bounds.y0 - 0.2, bounds.x0 + 7 * module + 0.2, bounds.y0 + 7 * module + 0.2)
            else:
                v = bars[len(bars) // 2]
                victim = pymupdf.Rect(v.x0 - 0.1, v.y0 - 0.1, v.x1 + 0.1, v.y1 + 0.1)
            yield (f"the {name} {tag} damaged", "codes decoded", copy_with(f"damaged-{tag}", lambda d, v=victim: d[0].draw_rect(v, color=None, fill=(1, 1, 1), overlay=True)))

            # Drawn as a picture instead of as rectangles: the rectangles are gone and a raster of the code is in their place.
            def raster(d, bounds=bounds):
                p = d[0]
                pix = p.get_pixmap(matrix=pymupdf.Matrix(4, 4), clip=bounds, colorspace=pymupdf.csGRAY, alpha=False)
                p.add_redact_annot(pymupdf.Rect(bounds.x0 - 0.5, bounds.y0 - 0.5, bounds.x1 + 0.5, bounds.y1 + 0.5))
                p.apply_redactions(images=pymupdf.PDF_REDACT_IMAGE_NONE, graphics=pymupdf.PDF_REDACT_LINE_ART_REMOVE_IF_TOUCHED, text=pymupdf.PDF_REDACT_TEXT_NONE)
                p.insert_image(bounds, pixmap=pix)
            yield (f"the {name} {tag} as a picture", "no image element draws", copy_with(f"raster-{tag}", raster))
    # An image deleted from the page, and one drawn 2 mm from its place.
    shown = next(((el, top) for band, top in bands_on_page2(exp, 1) for el in band["elements"] if el["type"] == "image"), None)
    if shown:
        el, top = shown
        want = contained(el, (BAND_X + el["x"]) * MM, top + el["y"] * MM)
        info = next(i for i in page.get_image_info(xrefs=True) if max(abs(i["bbox"][0] - want.x0), abs(i["bbox"][1] - want.y0)) <= 0.3)
        where = pymupdf.Rect(info["bbox"])

        def remove(d):
            d[0].add_redact_annot(pymupdf.Rect(where.x0 - 0.5, where.y0 - 0.5, where.x1 + 0.5, where.y1 + 0.5))
            d[0].apply_redactions(images=pymupdf.PDF_REDACT_IMAGE_REMOVE, graphics=pymupdf.PDF_REDACT_LINE_ART_NONE, text=pymupdf.PDF_REDACT_TEXT_NONE)

        def move(d):
            data = d.extract_image(info["xref"])["image"]
            remove(d)
            d[0].insert_image(pymupdf.Rect(where.x0 + 2 * MM, where.y0, where.x1 + 2 * MM, where.y1), stream=data)
        yield (f"the image {el['id']} missing", "no picture is drawn contained", copy_with(f"noimage-{el['id']}", remove))
        yield (f"the image {el['id']} drawn 2 mm from its place", "no picture is drawn contained", copy_with(f"movedimage-{el['id']}", move))


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
        document, banded = exp.get("kind") == "document", exp.get("kind") == "document2"
        if banded:
            problems, layouts[exp["file"]] = check_document2(args.directory, exp, args.font)
        else:
            problems, layouts[exp["file"]] = check_document(args.directory, exp, args.font) if document else check(args.directory, exp)
        print(f"{exp['file']}: {'ok' if not problems else 'FAILED'}")
        for p in problems[:20]:
            print("  " + p)
        failed |= bool(problems)
        if args.negative and banded:
            for what, corrupted, expected in document2_corruptions(exp):
                caught = any(expected in p for p in check_document2(args.directory, corrupted, args.font)[0])
                print(f"  negative control, {what}: {'caught' if caught else 'MISSED'}")
                failed |= not caught
            with tempfile.TemporaryDirectory() as scratch:
                for what, expected, directory, *other in document2_pdf_corruptions(args.directory, exp, scratch, args.font):
                    against = other[0] if other else exp
                    if exp.get("issue"):
                        # The record names the corrupted file by digest, so that every other check is exercised; the
                        # digest check has its own control among the expectations.
                        against = copy.deepcopy(against)
                        with open(os.path.join(directory, exp["file"]), "rb") as f:
                            against["issue"]["sha256"] = hashlib.sha256(f.read()).hexdigest()
                    caught = any(expected in p for p in check_document2(directory, against, args.font)[0])
                    print(f"  negative control, PDF with {what}: {'caught' if caught else 'MISSED'}")
                    failed |= not caught
        elif args.negative and document:
            for what, corrupted, expected in document_corruptions(exp):
                caught = any(expected in p for p in check_document(args.directory, corrupted, args.font)[0])
                print(f"  negative control, {what}: {'caught' if caught else 'MISSED'}")
                failed |= not caught
            with tempfile.TemporaryDirectory() as scratch:
                for what, expected, directory in document_pdf_corruptions(args.directory, exp, scratch, args.font):
                    against = exp
                    if exp.get("issue"):
                        # The record names the corrupted file by digest, so that every other
                        # check is exercised; the digest check has its own control above.
                        against = copy.deepcopy(exp)
                        with open(os.path.join(directory, exp["file"]), "rb") as f:
                            against["issue"]["sha256"] = hashlib.sha256(f.read()).hexdigest()
                    caught = any(expected in p for p in check_document(directory, against, args.font)[0])
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
