"""Check probe PDFs: veraPDF PDF/A-2b failed rules, embedded fonts and literal text extraction.

Usage (PyMuPDF venv): python -I check.py PDF [PDF ...]
"""
import os
import re
import subprocess
import sys
import xml.etree.ElementTree as ET

import pymupdf

EXPECTED = ["应收账款对账单", "序号", "摘要", "金额", "按合同第3.2条约定结算货款，含运费与保险费。",
            "根据《补充协议》（编号BC-2026-017），逾期按日万分之五计收违约金。", "退货冲减：SKU-A12（加急）共12件，已签收“退货单”。",
            "预收款核销——2026年第三季度服务费，见附件1、附件2……", "汇率差额调整（USD→CNY），参考中间价7.1234。",
            "华东大区重点客户·上海浦东新区张江高科技园区第二事业部", "第1页/共2页"]


def verapdf(pdf):
    d, name = os.path.split(os.path.abspath(pdf))
    p = subprocess.run(["docker", "run", "--rm", "--network", "none", "-v", f"{d}:/data:ro", "verapdf/cli:v1.30.3",
                        "--flavour", "2b", "--format", "xml", f"/data/{name}"], capture_output=True, text=True, timeout=600)
    root = ET.fromstring(p.stdout[p.stdout.index("<"):])
    report = next(root.iter("validationReport"))
    failed = [f'{r.get("clause")}-{r.get("testNumber")}: {(r.findtext("description") or "").strip()[:110]}'
              for r in root.iter("rule") if r.get("status") == "failed"]
    return report.get("isCompliant"), failed


for pdf in sys.argv[1:]:
    doc = pymupdf.open(pdf)
    text = re.sub(r"\s+", "", "".join(p.get_text() for p in doc))
    fonts = sorted({f"{f[3]} ({f[1]}, {f[2]})" for p in doc for f in p.get_fonts(full=True)})
    compliant, failed = verapdf(pdf)
    print(f"== {os.path.basename(pdf)}: {os.path.getsize(pdf)} bytes, {doc.page_count} pages, pdf version {doc.metadata.get('format')}")
    print("   fonts:", fonts)
    print("   expected strings missing from extracted text:", [s for s in EXPECTED if re.sub(r'\s+', '', s) not in text])
    print("   searchable '退货冲减':", any(p.search_for("退货冲减") for p in doc))
    print(f"   veraPDF PDF/A-2b compliant={compliant}; failed rules ({len(failed)}):")
    for f in failed:
        print("     -", f)
    doc[0].get_pixmap(dpi=90).save(os.path.splitext(pdf)[0] + "-p1.png")
