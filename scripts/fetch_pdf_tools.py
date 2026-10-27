"""Download the pinned Typst binary and Noto Sans SC fonts that PDF export runs with, and check
every archive and every extracted file against its pinned SHA-256. Nothing downloaded is run.

Usage: python3 -I scripts/fetch_pdf_tools.py DIR [--github-env]

DIR/typst is the binary and DIR/fonts the font directory (Regular and Bold only, as export/pdf
requires). The script prints the variables that the export/pdf tests, the console DOM tests and
the demo's -pdf-* flags use; with --github-env it also appends them to $GITHUB_ENV. Archives
are kept in DIR/downloads and reused when their checksum still matches. Downloads honour the
usual HTTPS_PROXY variables. Upgrading Typst or the fonts means changing these pins and passing
the golden-document and veraPDF checks again (docs/ADR_0001_PDF_ENGINE.md, condition 6).
"""
import argparse
import hashlib
import io
import os
import sys
import tarfile
import urllib.request
import zipfile

TYPST_URL = "https://github.com/typst/typst/releases/download/v0.15.1/typst-x86_64-unknown-linux-musl.tar.xz"
TYPST_ARCHIVE_SHA256 = "a6d077d0a95eed5a2eba715b2dae06be954f624ccbf85758a03f389ded33118c"
TYPST_MEMBER = "typst-x86_64-unknown-linux-musl/typst"
TYPST_SHA256 = "29273eaa04f6d00edd0c2bec578f565fc9c65be856bfbffc894567c68ed0b237"

FONTS_URL = "https://github.com/notofonts/noto-cjk/releases/download/Sans2.004/18_NotoSansSC.zip"
FONTS_ARCHIVE_SHA256 = "4d107c09ada479d3e48b6e78c83835773cbd9214bf6e12cdb7b60f8e068292ec"
FONTS = {
    "NotoSansSC-Regular.otf": "faa6c9df652116dde789d351359f3d7e5d2285a2b2a1f04a2d7244df706d5ea9",
    "NotoSansSC-Bold.otf": "c6cb5a93abaa9edc8ee7463b7ebb7f42d618d40e6ed2f7a5371c97b0b64767c0",
    "LICENSE": "6a73f9541c2de74158c0e7cf6b0a58ef774f5a780bf191f2d7ec9cc53efe2bf2",
}
MAX_ARCHIVE = 128 << 20


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def archive(downloads, url, want):
    path = os.path.join(downloads, url.rsplit("/", 1)[1])
    if os.path.exists(path):
        with open(path, "rb") as f:
            data = f.read()
        if sha256(data) == want:
            return data
    with urllib.request.urlopen(url, timeout=120) as response:
        data = response.read(MAX_ARCHIVE + 1)
    if len(data) > MAX_ARCHIVE or sha256(data) != want:
        sys.exit(f"{url} does not match its pinned checksum")
    with open(path, "wb") as f:
        f.write(data)
    return data


def place(path, data, want, mode):
    if sha256(data) != want:
        sys.exit(f"{os.path.basename(path)} does not match its pinned checksum")
    tmp = path + ".tmp"
    with open(tmp, "wb") as f:
        f.write(data)
    os.chmod(tmp, mode)
    os.replace(tmp, path)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("directory")
    ap.add_argument("--github-env", action="store_true")
    args = ap.parse_args()
    root = os.path.abspath(args.directory)
    downloads, fonts = os.path.join(root, "downloads"), os.path.join(root, "fonts")
    for d in (root, downloads, fonts):
        os.makedirs(d, mode=0o755, exist_ok=True)
        os.chmod(d, 0o755)

    # Members are read by exact name and written to fixed paths; no archive path is trusted.
    with tarfile.open(fileobj=io.BytesIO(archive(downloads, TYPST_URL, TYPST_ARCHIVE_SHA256)), mode="r:xz") as t:
        member = t.getmember(TYPST_MEMBER)
        if not member.isfile():
            sys.exit("the Typst archive holds no binary")
        place(os.path.join(root, "typst"), t.extractfile(member).read(), TYPST_SHA256, 0o755)
    with zipfile.ZipFile(io.BytesIO(archive(downloads, FONTS_URL, FONTS_ARCHIVE_SHA256))) as z:
        for name, want in FONTS.items():
            place(os.path.join(fonts, name), z.read(name), want, 0o644)
    extra = sorted(set(os.listdir(fonts)) - set(FONTS))
    if extra:
        sys.exit(f"{fonts} holds other files: {', '.join(extra)}")

    env = {"REPORTING_TYPST": os.path.join(root, "typst"), "REPORTING_TYPST_SHA256": TYPST_SHA256, "REPORTING_PDF_FONTS": fonts}
    for k, v in env.items():
        print(f"export {k}={v}")
    if args.github_env:
        with open(os.environ["GITHUB_ENV"], "a", encoding="utf-8") as f:
            for k, v in env.items():
                f.write(f"{k}={v}\n")


if __name__ == "__main__":
    main()
