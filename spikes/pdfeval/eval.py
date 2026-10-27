"""Evaluate a statement PDF engine with engine-independent checks: pagination, exact amounts, no
silent loss, CJK line breaking, fonts, metadata, determinism, PDF/A-2b, speed, concurrency and
(for Typst) template safety. Writes results.json and page renders.

Usage (PyMuPDF + fontTools venv):
  python -I eval.py --engine typst --spike-bin BIN --typst TYPST --fonts DIR --out DIR
  python -I eval.py --engine fpdf  --spike-bin BIN --icc ICC     --fonts DIR --out DIR
"""
import argparse
import concurrent.futures
import hashlib
import io
import json
import os
import re
import statistics
import subprocess
import time
from decimal import Decimal, getcontext

import pymupdf
from fontTools.cffLib import CFFFontSet
from fontTools.ttLib import TTFont

getcontext().prec = 80  # exact for every amount this spike produces

ap = argparse.ArgumentParser()
ap.add_argument("--engine", choices=("typst", "fpdf"), required=True)
for flag in ("--spike-bin", "--fonts", "--out"):
    ap.add_argument(flag, required=True)
ap.add_argument("--typst")
ap.add_argument("--icc")
ap.add_argument("--verapdf-image", default="verapdf/cli:v1.30.3")
args = ap.parse_args()
OUT = os.path.abspath(args.out)
os.makedirs(OUT, exist_ok=True)
if args.engine == "typst":
    version = subprocess.run([args.typst, "--version"], capture_output=True, text=True).stdout.strip()
else:
    built = subprocess.run(["go", "version", "-m", args.spike_bin], capture_output=True, text=True).stdout
    version = " ".join(re.findall(r"dep\s+(codeberg\.org/go-pdf/fpdf\s+\S+)", built)) or "fpdf (version unknown)"
results = {"engine": args.engine, "engine_version": version}

# GB/T 15834-2011: these never start a line (the interpunct "generally" not), opening marks never
# end one, and the two-em dash and ellipsis may start a line but must not be split across two.
NO_START = set("，。、；：？！）］｝」』】》〉”’·％")
NO_END = set("（［｛「『【《〈“‘")
SPLIT_PAIR = re.compile(r"^(—|…)(?!\1)|(?<!—)—$|(?<!…)…$")
ROW = re.compile(r"AR-\d{6}")


def squash(s):
    return re.sub(r"\s+", "", s)


def render(name, *flags):
    pdf = os.path.join(OUT, name + ".pdf")
    cmd = [args.spike_bin, "-fonts", args.fonts, "-out", pdf]
    cmd += ["-typst", args.typst] if args.engine == "typst" else ["-icc", args.icc]
    p = subprocess.run(cmd + list(flags), capture_output=True, text=True, timeout=3600)
    if p.returncode != 0:
        return None, (p.stdout + p.stderr).strip()
    info = json.loads(p.stdout.strip().splitlines()[-1])
    with open(pdf + ".json", encoding="utf8") as f:
        info["data"] = json.load(f)
    return info, None


def description_lines(page):
    """Lines of the description column, top to bottom, found by the left edge of its header. A line
    may start up to 0.6em (9pt text) further left, where an engine trims the blank half of an
    opening mark at the line start."""
    head = page.search_for("摘要")
    if not head:
        return []
    x, y = head[0].x0, head[0].y1
    found = []
    for block in page.get_text("dict")["blocks"]:
        for line in block.get("lines", []):
            if x - 5.4 <= line["bbox"][0] <= x + 1.5 and line["bbox"][1] >= y:
                text = "".join(s["text"] for s in line["spans"]).strip()
                if text:
                    found.append((line["bbox"][1], line["bbox"][0], text))
    return [t for _, _, t in sorted(found)]


def check_statement(info):
    """Every row placed once and in order, every description complete across pages, header and
    numbering on every page, carried totals exact, metadata readable."""
    data, doc = info["data"], pymupdf.open(info["pdf"])
    rows, n = data["rows"], doc.page_count
    texts = [p.get_text() for p in doc]
    running, cum_mismatch = Decimal(0), []
    for r in rows:  # independent re-addition of the Go running totals
        running += Decimal(r["amount"])
        if Decimal(r["cum"]) != running:
            cum_mismatch.append(r["doc"])
    page_of, duplicated = {}, []
    for k, t in enumerate(texts):
        for m in ROW.findall(t):
            if m in page_of:
                duplicated.append(m)
            page_of[m] = k
    missing = [r["doc"] for r in rows if r["doc"] not in page_of]
    seq = [page_of[r["doc"]] for r in rows if r["doc"] in page_of]
    owned = {}
    for r in rows:
        if r["doc"] in page_of:
            owned[page_of[r["doc"]]] = r["cum"]
    carried_bad, numbering_bad, carried = [], [], None
    for k, t in enumerate(texts):
        m = re.search(r"第\s*(\d+)\s*页\s*/\s*共\s*(\d+)\s*页", t)
        if not m or (int(m.group(1)), int(m.group(2))) != (k + 1, n):
            numbering_bad.append(k + 1)
        fwd = re.search(r"承前页累计：\s*(-?\d+\.\d{2})", t)
        nxt = re.search(r"过次页累计：\s*(-?\d+\.\d{2})", t)
        carried_in = carried
        carried = owned.get(k, carried)
        got = (fwd.group(1) if fwd else None, nxt.group(1) if nxt else None)
        want = (carried_in, carried if k < n - 1 else None)
        if got != want:
            carried_bad.append({"page": k + 1, "got": got, "want": want})
    # The description column, read page after page, must be exactly the descriptions in order.
    stream, pos, not_literal = squash("".join(l for p in doc for l in description_lines(p))), 0, []
    for r in rows:
        s = squash(r["desc"])
        if stream.startswith(s, pos):
            pos += len(s)
        else:
            not_literal.append(r["doc"])
            found = stream.find(s, pos)
            pos = found + len(s) if found >= 0 else pos
    amount_missing = [r["doc"] for r in rows if r["doc"] in page_of and r["amount"] not in texts[page_of[r["doc"]]]]
    last, meta = squash(texts[-1]), doc.metadata
    return {
        "pages": n, "rows": len(rows), "bytes": os.path.getsize(info["pdf"]),
        "running_totals_match_independent_sum": not cum_mismatch and data["total"] == (rows[-1]["cum"] if rows else "0.00"),
        "rows_missing": missing[:5], "rows_duplicated": duplicated[:5], "rows_in_order": seq == sorted(seq),
        "header_missing_on_pages": [k + 1 for k, t in enumerate(texts) if not all(h in t for h in ("应收账款对账单", "序号", "摘要", "金额"))][:5],
        "page_numbering_wrong_on": numbering_bad[:5],
        "carried_totals_wrong": carried_bad[:5],
        "descriptions_not_complete_and_literal": not_literal[:5],
        "description_column_has_extra_text": pos != len(stream),
        "amounts_not_on_their_row_page": amount_missing[:5],
        "final_total_and_capital_on_last_page": squash("合计：" + data["total"]) in last and squash(data["total_upper"]) in last,
        "metadata_readable": meta.get("title") == data["meta"]["title"] and meta.get("author") == data["meta"]["company"]
                             and data["meta"]["snapshot"] in (meta.get("keywords") or ""),
    }


def clean(c):
    """True when a check_statement result has no failure."""
    return (not c["rows_missing"] and not c["rows_duplicated"] and c["rows_in_order"] and not c["header_missing_on_pages"]
            and not c["page_numbering_wrong_on"] and not c["carried_totals_wrong"] and not c["descriptions_not_complete_and_literal"]
            and not c["description_column_has_extra_text"] and not c["amounts_not_on_their_row_page"]
            and c["running_totals_match_independent_sum"] and c["final_total_and_capital_on_last_page"] and c["metadata_readable"])


def check_typography(info):
    """Line-start/line-end prohibitions in the description column; embedded font programs."""
    doc = pymupdf.open(info["pdf"])
    violations, lines, fonts, families = [], 0, set(), set()
    for k, page in enumerate(doc):
        for text in description_lines(page):
            lines += 1
            if text[0] in NO_START or text[-1] in NO_END or SPLIT_PAIR.search(text):
                violations.append({"page": k + 1, "line": text})
        for f in page.get_fonts(full=True):
            fonts.add((f[0], f[3], f[1]))
    for xref, _, _ in fonts:
        name, ext, _, content = doc.extract_font(xref)
        if not content:
            families.add(f"{name} (not embedded)")
            continue
        try:  # read the family from the embedded font program itself, TrueType or bare CFF
            families.add(TTFont(io.BytesIO(content), fontNumber=0)["name"].getDebugName(1))
        except Exception:
            try:
                cff = CFFFontSet()
                cff.decompile(io.BytesIO(content), None)
                families.add(cff.fontNames[0])
            except Exception as e:
                families.add(f"{name} (unreadable: {e.__class__.__name__})")
    return {
        "description_lines_checked": lines, "line_break_prohibition_violations": violations[:8],
        "fonts": sorted(f"{b} ({ext})" for _, b, ext in fonts),
        "embedded_font_families": sorted(families),
        "only_embedded_noto": all(ext not in ("n/a", "") for _, _, ext in fonts) and all("Noto" in f for f in families),
        "subset_tag_prefix": all(re.match(r"[A-Z]{6}\+", b) for _, b, _ in fonts),
        "text_searchable": bool(doc[0].search_for("应收账款对账单")) and any(p.search_for("退货冲减") for p in doc),
    }


def verapdf(pdf):
    d, name = os.path.split(pdf)
    p = subprocess.run(["docker", "run", "--rm", "--network", "none", "-v", f"{d}:/data:ro", args.verapdf_image,
                        "--flavour", "2b", "--format", "text", f"/data/{name}"], capture_output=True, text=True, timeout=900)
    out = (p.stdout + p.stderr).strip()
    return out.splitlines()[0] if out else f"exit {p.returncode}"


def sha(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def safety(name, source, *flags):
    path = os.path.join(OUT, name + ".typ")
    with open(path, "w", encoding="utf8") as f:
        f.write(source)
    info, err = render(name, "-rows", "3", "-template", path, *flags)
    os.remove(path)
    return {"rendered": info is not None, "message": (err or "")[-400:]}


# 1. Representative statement with hostile strings, rendered as PDF/A-2b.
sample, err = render("sample", "-rows", "150", "-hostile")
assert sample, err
results["sample"] = {**check_statement(sample), **check_typography(sample), "render_seconds": sample["render_seconds"]}
doc = pymupdf.open(sample["pdf"])
for k in sorted({0, 1, doc.page_count - 1}):
    doc[k].get_pixmap(dpi=110).save(os.path.join(OUT, f"sample-p{k + 1}.png"))

# 2. Line breaking stress: long, punctuation-dense descriptions.
stress, err = render("linebreak-stress", "-rows", "400", "-seed", "11", "-long")
assert stress, err
results["linebreak_stress"] = {**check_statement(stress), **check_typography(stress)}
pymupdf.open(stress["pdf"])[0].get_pixmap(dpi=110).save(os.path.join(OUT, "linebreak-stress-p1.png"))

# 2b. A row taller than a page: everything must appear, or the render must be refused.
tall, err = render("tall-row", "-rows", "3", "-tall-row")
results["row_taller_than_page"] = {"rendered": tall is not None, "message": (err or "")[-300:]}
if tall:
    checked = check_statement(tall)
    shown = sum(p.get_text().count("补充协议") for p in pymupdf.open(tall["pdf"]))
    results["row_taller_than_page"].update({"pages": checked["pages"], "engine_output": tall["engine_output"],
                                            "descriptions_not_complete_and_literal": checked["descriptions_not_complete_and_literal"],
                                            "clause_occurrences_shown": shown,
                                            "clause_occurrences_in_data": tall["data"]["rows"][1]["desc"].count("补充协议"),
                                            "all_checks_clean": clean(checked)})

# 3. Determinism: same input and timestamp twice.
again, err = render("sample-again", "-rows", "150", "-hostile")
assert again, err
results["deterministic_bytes"] = sha(sample["pdf"]) == sha(again["pdf"])
results["sample_sha256"] = sha(sample["pdf"])

# 4. PDF/A-2b with veraPDF, and a negative control rendered without the PDF/A option.
plain, err = render("sample-no-pdfa", "-rows", "150", "-hostile", "-standard", "")
assert plain, err
results["verapdf_2b"] = {"sample": verapdf(sample["pdf"]), "negative_control_without_pdfa": verapdf(plain["pdf"])}
draft, err = render("sample-draft", "-rows", "40", "-draft")
assert draft, err
results["verapdf_2b"]["draft_watermark"] = verapdf(draft["pdf"])
pymupdf.open(draft["pdf"])[0].get_pixmap(dpi=80).save(os.path.join(OUT, "sample-draft-p1.png"))

# 5. Speed and memory with the same row counts for every engine; the ~1000-page case five times.
results["performance"] = []
for rows, repeats in ((5, 1), (3000, 1), (30000, 5)):
    runs = []
    for i in range(repeats):
        info, err = render(f"perf-{rows}", "-rows", str(rows))
        assert info, err
        runs.append(info)
    checked = check_statement(runs[0])
    secs, rss = [r["render_seconds"] for r in runs], [r["render_max_rss_kb"] / 1024 for r in runs]
    results["performance"].append({"rows": rows, "pages": checked["pages"], "bytes": checked["bytes"], "runs": repeats,
                                   "seconds_median": round(statistics.median(secs), 3), "seconds_min": round(min(secs), 3), "seconds_max": round(max(secs), 3),
                                   "max_rss_mb_median": round(statistics.median(rss), 1), "max_rss_mb_max": round(max(rss), 1),
                                   "all_checks_clean": clean(checked)})
    if rows > 1000:
        os.remove(runs[0]["pdf"])

# 5b. Four renders of 3000 rows at once.
started = time.time()
with concurrent.futures.ThreadPoolExecutor(4) as pool:
    outcomes = list(pool.map(lambda i: render(f"concurrent-{i}", "-rows", "3000"), range(4)))
results["concurrency_4x3000_rows"] = {"all_rendered": all(o for o, _ in outcomes), "wall_seconds": round(time.time() - started, 3),
                                      "sum_of_peak_rss_mb": round(sum(o["render_max_rss_kb"] for o, _ in outcomes if o) / 1024, 1),
                                      "all_checks_clean": all(o and clean(check_statement(o)) for o, _ in outcomes)}

# 6. Exact amounts at the edge: 16 integer digits render exactly; 30 digits fail closed in Go.
edge, err = render("amount-16-digits", "-rows", "1", "-first-amount", "9999999999999999.99")
results["amounts"] = {"16_digit_total_rendered_exactly": bool(edge) and "9999999999999999.99" in pymupdf.open(edge["pdf"])[0].get_text(),
                      "16_digit_capital": edge["data"]["total_upper"] if edge else err}
_, err = render("amount-30-digits", "-rows", "1", "-first-amount", "123456789012345678901234567890.12")
results["amounts"]["30_digit_total_refused"] = err

# 7. Template safety applies to Typst, whose templates are code; the fpdf engine has no template language.
if args.engine == "typst":
    results["safety"] = {
        "read_outside_root": safety("read-outside-root", '#read("/etc/passwd")'),
        "package_import_isolated": safety("package-import", '#import "@preview/cetz:0.3.4": *\nx'),
        "package_import_with_network": safety("package-import-net", '#import "@preview/cetz:0.3.4": *\nx', "-allow-network"),
        "infinite_loop": safety("infinite-loop", "#while true {}"),
        "deep_recursion": safety("deep-recursion", "#let f(n) = f(n + 1)\n#f(0)"),
        # 400 million iterations with small arrays (a single range() of that size would allocate gigabytes).
        "slow_template_killed_by_timeout": safety("slow", "#for a in range(20000) { for b in range(20000) {} }", "-timeout", "3s"),
    }
    capped, err = render("memory-capped", "-rows", "3000", "-max-memory-mb", "96")
    results["safety"]["3000_rows_under_96mb_address_space"] = {"rendered": capped is not None, "message": (err or "")[-300:]}
else:
    results["safety"] = "not applicable: no template language; the layout is Go code and fpdf's core imports no network package"

with open(os.path.join(OUT, "results.json"), "w", encoding="utf8") as f:
    json.dump(results, f, ensure_ascii=False, indent=2)
print(json.dumps(results, ensure_ascii=False, indent=1))
