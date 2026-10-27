"""Evaluate Typst for statement PDFs: pagination, exact amounts, CJK line breaking, fonts,
determinism, PDF/A-2b, speed and template safety. Writes results.json and page renders.

Usage (PyMuPDF venv):
  python -I eval.py --spike-bin BIN --typst TYPST --fonts DIR --out DIR [--verapdf-image IMAGE]
"""
import argparse
import hashlib
import json
import os
import re
import subprocess
from decimal import Decimal, getcontext

import pymupdf

getcontext().prec = 80  # exact for every amount this spike produces

ap = argparse.ArgumentParser()
for flag in ("--spike-bin", "--typst", "--fonts", "--out"):
    ap.add_argument(flag, required=True)
ap.add_argument("--verapdf-image", default="verapdf/cli:v1.30.3")
args = ap.parse_args()
OUT = os.path.abspath(args.out)
os.makedirs(OUT, exist_ok=True)
results = {"typst": subprocess.run([args.typst, "--version"], capture_output=True, text=True).stdout.strip()}

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
    p = subprocess.run([args.spike_bin, "-typst", args.typst, "-fonts", args.fonts, "-out", pdf, *flags],
                       capture_output=True, text=True, timeout=3600)
    if p.returncode != 0:
        return None, (p.stdout + p.stderr).strip()
    info = json.loads(p.stdout.strip().splitlines()[-1])
    with open(pdf + ".json", encoding="utf8") as f:
        info["data"] = json.load(f)
    return info, None


def check_statement(info):
    """Every row placed once and in order, header and numbering on every page, carried totals exact."""
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
    last_cum = {}
    for r in rows:
        if r["doc"] in page_of:
            last_cum[page_of[r["doc"]]] = r["cum"]
    carried_bad, numbering_bad = [], []
    for k, t in enumerate(texts):
        m = re.search(r"第\s*(\d+)\s*页\s*/\s*共\s*(\d+)\s*页", t)
        if not m or (int(m.group(1)), int(m.group(2))) != (k + 1, n):
            numbering_bad.append(k + 1)
        fwd = re.search(r"承前页累计：\s*(-?\d+\.\d{2})", t)
        nxt = re.search(r"过次页累计：\s*(-?\d+\.\d{2})", t)
        got = (fwd.group(1) if fwd else None, nxt.group(1) if nxt else None)
        want = (last_cum.get(k - 1) if k > 0 else None, last_cum.get(k) if k < n - 1 else None)
        if got != want:
            carried_bad.append({"page": k + 1, "got": got, "want": want})
    by_page = {}
    for r in rows:
        by_page.setdefault(page_of.get(r["doc"]), []).append(r)
    desc_missing = [r["doc"] for k, rs in by_page.items() if k is not None for r in rs
                    if squash(r["desc"]) not in squash(texts[k]) or r["amount"] not in texts[k]]
    last = squash(texts[-1])
    return {
        "pages": n, "rows": len(rows), "bytes": os.path.getsize(info["pdf"]),
        "running_totals_match_independent_sum": not cum_mismatch and data["total"] == (rows[-1]["cum"] if rows else "0.00"),
        "rows_missing": missing[:5], "rows_duplicated": duplicated[:5], "rows_in_order": seq == sorted(seq),
        "header_missing_on_pages": [k + 1 for k, t in enumerate(texts) if not all(h in t for h in ("应收账款对账单", "序号", "摘要", "金额"))][:5],
        "page_numbering_wrong_on": numbering_bad[:5],
        "carried_totals_wrong": carried_bad[:5], "carried_totals_checked_pages": n,
        "rows_with_text_or_amount_not_rendered_literally": desc_missing[:5],
        "final_total_and_capital_on_last_page": squash("合计：" + data["total"]) in last and squash(data["total_upper"]) in last,
    }


def check_typography(info):
    """Line-start/line-end prohibitions in the description column, fonts and searchable text."""
    doc = pymupdf.open(info["pdf"])
    violations, lines, fonts = [], 0, set()
    for k, page in enumerate(doc):
        head = page.search_for("摘要")
        if head:
            x, y = head[0].x0, head[0].y1
            for block in page.get_text("dict")["blocks"]:
                for line in block.get("lines", []):
                    if abs(line["bbox"][0] - x) > 1.5 or line["bbox"][1] < y:
                        continue
                    text = "".join(s["text"] for s in line["spans"]).strip()
                    if not text:
                        continue
                    lines += 1
                    if text[0] in NO_START or text[-1] in NO_END or SPLIT_PAIR.search(text):
                        violations.append({"page": k + 1, "line": text})
        for f in page.get_fonts(full=True):
            fonts.add((f[3], f[1]))
    return {
        "description_lines_checked": lines, "line_break_prohibition_violations": violations[:8],
        "fonts": sorted(f"{b} ({ext})" for b, ext in fonts),
        "only_embedded_subset_noto": all(re.fullmatch(r"[A-Z]{6}\+NotoSansSC-(Regular|Bold)(-Identity-H)?", b) and ext not in ("n/a", "") for b, ext in fonts),
        "text_searchable": bool(doc[0].search_for("应收账款对账单")) and any(p.search_for("退货冲减") for p in doc),
        "metadata": {k: doc.metadata.get(k) for k in ("title", "author", "keywords", "format", "creationDate")},
    }


def verapdf(pdf):
    d, name = os.path.split(pdf)
    p = subprocess.run(["docker", "run", "--rm", "--network", "none", "-v", f"{d}:/data:ro", args.verapdf_image,
                        "--flavour", "2b", "--format", "text", f"/data/{name}"], capture_output=True, text=True, timeout=900)
    return (p.stdout + p.stderr).strip().splitlines()[0] if (p.stdout + p.stderr).strip() else f"exit {p.returncode}"


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
results["sample"] = {**check_statement(sample), **check_typography(sample),
                     "typst_seconds": sample["typst_seconds"], "typst_max_rss_kb": sample["typst_max_rss_kb"]}
doc = pymupdf.open(sample["pdf"])
for k in sorted({0, 1, doc.page_count - 1}):
    doc[k].get_pixmap(dpi=110).save(os.path.join(OUT, f"sample-p{k + 1}.png"))

# 2. Line breaking stress: long, punctuation-dense descriptions.
stress, err = render("linebreak-stress", "-rows", "400", "-seed", "11", "-long")
assert stress, err
results["linebreak_stress"] = {**check_statement(stress), **check_typography(stress)}
pymupdf.open(stress["pdf"])[0].get_pixmap(dpi=110).save(os.path.join(OUT, "linebreak-stress-p1.png"))

# 2b. A row taller than a page (rows are unbreakable in the template): is anything lost silently?
tall, err = render("tall-row", "-rows", "3", "-tall-row")
results["row_taller_than_page"] = {"rendered": tall is not None, "message": (err or "")[-300:]}
if tall:
    checked = check_statement(tall)
    long_text = tall["data"]["rows"][1]["desc"]
    shown = sum(p.get_text().count("补充协议") for p in pymupdf.open(tall["pdf"]))
    results["row_taller_than_page"].update({"pages": checked["pages"], "typst_output": tall["typst_output"],
                                            "rows_not_rendered_literally": checked["rows_with_text_or_amount_not_rendered_literally"],
                                            "clause_occurrences_shown": shown, "clause_occurrences_in_data": long_text.count("补充协议")})

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

# 5. Speed and memory: one page, about 100 pages and about 1000 pages (rows per page from the sample).
per_page = results["sample"]["rows"] / results["sample"]["pages"]
results["performance"] = []
for label, rows in (("1 page", 5), ("~100 pages", int(per_page * 100)), ("~1000 pages", int(per_page * 1000))):
    info, err = render(f"perf-{rows}", "-rows", str(rows))
    assert info, err
    checked = check_statement(info)
    results["performance"].append({"case": label, "rows": rows, "pages": checked["pages"], "bytes": checked["bytes"],
                                   "typst_seconds": round(info["typst_seconds"], 3), "typst_max_rss_mb": round(info["typst_max_rss_kb"] / 1024, 1),
                                   **{k: checked[k] for k in ("rows_missing", "rows_duplicated", "rows_in_order", "header_missing_on_pages",
                                                              "page_numbering_wrong_on", "carried_totals_wrong", "running_totals_match_independent_sum",
                                                              "rows_with_text_or_amount_not_rendered_literally", "final_total_and_capital_on_last_page")}})
    if rows > 1000:
        os.remove(info["pdf"])

# 6. Exact amounts at the edge: 16 integer digits render exactly; 30 digits fail closed in Go.
edge, err = render("amount-16-digits", "-rows", "1", "-first-amount", "9999999999999999.99")
results["amounts"] = {"16_digit_total_rendered_exactly": bool(edge) and "9999999999999999.99" in pymupdf.open(edge["pdf"])[0].get_text(),
                      "16_digit_capital": edge["data"]["total_upper"] if edge else err}
_, err = render("amount-30-digits", "-rows", "1", "-first-amount", "123456789012345678901234567890.12")
results["amounts"]["30_digit_total_refused"] = err

# 7. Template safety (relevant once users can author templates).
results["safety"] = {
    "read_outside_root": safety("read-outside-root", '#read("/etc/passwd")'),
    "package_import_isolated": safety("package-import", '#import "@preview/cetz:0.3.4": *\nx'),
    "package_import_with_network": safety("package-import-net", '#import "@preview/cetz:0.3.4": *\nx', "-allow-network"),
    "infinite_loop": safety("infinite-loop", "#while true {}"),
    "deep_recursion": safety("deep-recursion", "#let f(n) = f(n + 1)\n#f(0)"),
    # 400 million iterations with small arrays (a single range() of that size would allocate gigabytes).
    "slow_template_killed_by_timeout": safety("slow", "#for a in range(20000) { for b in range(20000) {} }", "-timeout", "3s"),
}

capped, err = render("memory-capped", "-rows", str(int(per_page * 100)), "-max-memory-mb", "96")
results["safety"]["about_100_pages_under_96mb_address_space"] = {"rendered": capped is not None, "message": (err or "")[-300:]}

with open(os.path.join(OUT, "results.json"), "w", encoding="utf8") as f:
    json.dump(results, f, ensure_ascii=False, indent=2)
print(json.dumps(results, ensure_ascii=False, indent=1))
