# S03-D1 PDF export of frozen snapshots

2026-10-27. Baseline cf9960e. Engine and conditions: docs/ADR_0001_PDF_ENGINE.md.

Add PDF as a second export format of the existing asynchronous export: POST /exports takes
`{snapshot_id, format: "xlsx"|"pdf"}` (absent format means xlsx), the worker renders the frozen snapshot,
and GET /artifacts/{id}/content returns `application/pdf` with a `.pdf` filename after the same current
authorization checks as Excel. The capabilities response lists both formats. One built-in, server-owned
template renders a generic table: dataset title, column labels as a header repeated on every page, rows in
snapshot order with breakable rows, exact decimal strings right-aligned, the Query.Metrics totals as in the
Excel Totals sheet, and a footer with page X of Y, snapshot ID, dataset@version, as-of date, source watermark
and the contract-simulator data class.

Core changes are limited to what a second format needs: jobs and artifacts carry a format, the service holds
one writer per format behind a standard-library interface, and the artifact media type comes from the
recorded format. Format travels inside the existing JSON-encoded job and asset records; records without it
read as xlsx, so stored jobs and artifacts stay valid (verify for the PostgreSQL store). reporting/ stays
domain-neutral and standard-library-only; the earlier "core unchanged since S01-A" qualification ends here
and is replaced by review of this change.

export/pdf (not core) builds a private root holding the embedded template and a data.json, checks every
printed character against the configured fonts with golang.org/x/image/font/sfnt and fails closed with
UNSUPPORTED_CHARACTER, and runs the configured Typst binary: checksum verified at startup, empty environment,
new user and network namespace, empty package store, only the configured fonts, PDF/A-2b, creation time from
the service clock, a timeout inside WorkTimeout. Output above MaxArtifactBytes fails with the existing budget
error. Typst stderr is never logged with row values. The demo enables PDF only when both the Typst binary and
font directory flags are given; otherwise the format is not offered.

Tests: unit tests for data and template construction, character coverage, bounds and failure mapping through
public seams; an integration test with the real pinned Typst and fonts that checks rows, repeated header,
page numbering, exact values, hostile strings, a row taller than a page and determinism with a fixed clock;
veraPDF PDF/A-2b on the output with a negative control; DOM tests for the console's PDF export action and
download. CI downloads the pinned Typst and fonts by checksum.

Not in this slice: the template designer, user templates, grouped statements and carried totals (承前过次),
images, barcodes, signatures, OFD, real products and production qualification.
