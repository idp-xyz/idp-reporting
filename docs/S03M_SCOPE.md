# S03-M barcodes, QR codes, images and the issue number

2026-10-29. Implements ADR 0005 for previews, checks and issues of schema 2.1 templates. Templates 1.x and 2.0
keep their bytes; golden entries are only added.

M1 · Schema 2.1. `doctemplates` and the console's validator accept `schema_version: "2.1"`: everything 2.0 has,
plus `barcode` (`id`, `type`, `source` literal | field | issue_number, `text` or `field`, `x`, `y`, `width`,
`height`, `symbology` "code128", `human_readable` boolean, `font_size` when human readable), `qrcode` (`id`,
`type`, `source`, `text` or `field`, `x`, `y`, `size`, `ecc` L | M | Q | H) and `image` (`id`, `type`, `x`, `y`,
`width`, `height`, `image` {id, sha256}), allowed in every band but `detail`, and the text source
`issue_number`. Closed as 2.0; the shared cases file gains 2.1 cases run by Go and the console. An element
referencing an image of another binding or identity is refused like a background.

M2 · Encoding and geometry. A package (e.g. `export/barcode`) wraps `github.com/boombuler/barcode` v1.1.0
(pinned in go.mod/go.sum): `Code128(value) (modules []bool, err)` and `QR(value, ecc) (matrix [][]bool, err)`,
and the worst-case module counts for a legal issue number. The layout turns modules into merged vector
rectangles (one per run of dark modules per row for QR, one per bar for Code 128) at exact positions, with the
quiet zone inside the box, the uniform module width and the minimums of ADR 0005 decision 4; violations are
`DOES_NOT_FIT` with the element named; characters outside the alphabet `NOT_COVERED`. The page program draws
the rectangles with the existing fills. Deterministic.

M3 · The issue number. Texts, Code 128 and QR with source `issue_number`: in previews the placeholder
`PREVIEW-NUMBER`; in `CheckIssue` the worst case for every legal number (decision 5); in `Issue` the record's
number. The issuer passes the reserved number to the render; nothing else in the issue flow changes. Tests prove
that a template whose check passes renders with the widest and the longest legal numbers.

M4 · Images. A private image store beside the backgrounds (`POST /document-images` PNG or JPEG base64, ≤ 512
KiB, ≤ 2000 × 2000 px, decoded and re-checked on the server, at most 16 per identity and dataset; `GET
/document-images` lists the caller's; `GET /document-images/{id}/content` returns the bytes to their owner
only, with the console's security headers and `Cache-Control: no-store`). The template references an image by
id and SHA-256; previews, checks and issues resolve it with the same authorization as backgrounds; the image is
drawn contained in its box, centred. PDF/A-2b holds (veraPDF), including PNGs with alpha.

M5 · Console. The canvas draws codes as labelled placeholders (symbology, source, quiet zone) and images from
the content route; panels add and edit the three element types and the `issue_number` source; an image panel
uploads and lists images. CSP unchanged (`img-src 'self'` already allows the content route).

M6 · Checker and tests. `scripts/check_pdf_export.py` renders pages and decodes every code with zxing-cpp
(pinned with its hash in `scripts/requirements-pdfcheck.txt`; the CI pdf job installs it), compares value,
position and module size with the expectation, checks images by box and presence, and has negative controls
(wrong value, moved code, code below its minimum module size, missing image). Go tests for the schema, the
encoders' geometry (against known module patterns), every refusal, the worst-case number proofs, the image
store and its routes; Typst tests producing 2.1 samples (a waybill with a Code 128 of the issue number, an
invoice with a QR and a logo, an issued multi-page group with the number in the header) checked by the checker
and veraPDF; DOM tests for the panels and the canvas.

Not in this slice: other symbologies, electronic seals or signatures, images in the detail table, 2.x print
offsets and backgrounds.
