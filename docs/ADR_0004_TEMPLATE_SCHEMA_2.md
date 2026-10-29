# ADR 0004 · Template schema 2.0: banded documents over row groups

Date: 2026-10-28. Status: accepted. Implementation: S03-J (schema and layout), S03-K (issue by group),
S03-L (canvas for bands).

## Context

A schema 1.x document template (1.0–1.3) is at most 24 single-line text boxes on the first page and one
detail table that continues over pages. Its text boxes are bound to one row of the frozen snapshot, while its
table lists every row of that snapshot, and S03-H issues one document per row (`IssueIntent.RowIndex`,
`RowSHA256`). Real formal documents do not have that shape: an invoice, a delivery note or a waybill is one
document per business key (an order, a shipment) carrying that key's lines, with a header and a footer on
every page, page numbers, text that wraps, and, later, barcodes and images. The user's studio prototype
(`IDP_Reporting_Designer_UIUX_V3.0.html`, the UX specification) designs such documents as bands on an A4
canvas; S03-I gives the console a canvas for 1.x.

What 2.0 must keep: Go owns every position and Typst only draws (export/pdf), so that the layout can be
checked without Typst before a product reserves a number (S03-H); a value that does not fit is refused,
never cut, shrunk or moved; the same input gives the same bytes; the source product owns business facts,
amounts and numbers, the platform renders and keeps the file (docs/s00/ARCHITECTURE.md).

## Decision

1. **A new major version beside 1.x.** `schema_version: "2.0"` is a new, closed JSON form. Templates
   1.0–1.3 stay valid, render byte for byte as today (golden tests guard it) and keep issuing per row.
   Nothing converts on the server; the console converts a 1.x template into a new 2.0 template version only
   when the user asks, and shows the preview before saving.
2. **One document per row group.** `group.fields` names 1–3 dimension fields of the bound dataset (no
   metrics). The frozen rows are partitioned by the exact frozen strings of those fields (the empty string is
   a key like any other); groups are ordered by their first row and keep the snapshot's row order. Without
   `group` the whole snapshot is one document. A document per row is a group by a unique key.
3. **Bands.** From top to bottom inside the safe region (x 10–200 mm, y 25–270 mm): `page_header` (every
   page), `document_header` (first page, under the page header), `detail` (the table: the only band that
   grows and continues over pages), `document_footer` (once, after the last detail row, kept whole: it moves
   to a new page if it does not fit), `page_footer` (every page, at the bottom of the safe region). Every band
   but `detail` has a fixed height on the 0.1 mm grid. The strip 10–18 mm from the top stays reserved on
   every page for the preview banner or the issue stamp; no band enters it. More than 100 pages is refused, as
   today.
4. **Typed elements.** Each element has a `type`; 2.0 defines `text`, `line`, `rect` (drawing only) and
   `table` (one at most, in `detail` only). Positions are relative to the band, in mm on the 0.1 mm grid.
   A `text` shows a literal; a `field` of the group; a `group_sum` of a metric field (the exact decimal sum of
   the group's frozen values, by the rule the table's totals already use); or a `page` text, a literal with
   the placeholders `{page}` and `{pages}`. A `field` must be one of the group's fields or have the same value
   in every row of the group; otherwise the document is refused, because picking one row's value would be a
   silent decision. `wrap: true` breaks a value into lines by the platform's line-break rules within the
   box's width; lines beyond the box's height are refused. Without `wrap`, a box is one line as in 1.x. The
   type names `barcode`, `qrcode` and `image` are reserved: each arrives in a 2.x minor version once the
   server renders it (ADR 0005). Unknown types, keys and fields are refused.
5. **The detail table** keeps the 1.3 behaviour (header on every page, wrapped cells, carried-forward and
   carried-over rows, final total, exact sums) over the group's rows. A column may set its `align` and a
   literal header `label` in place of the dataset's.
6. **Amounts stay the product's.** The platform sums printed metric values for presentation only (page
   carries, table totals, `group_sum`). Tax, rounding, document totals and amounts in words are fields the
   product supplies; the platform never computes them.
7. **Issue by group.** For a 2.0 template, `POST /issues` names the group by its key values, and
   `IssueIntent` carries `Group` (the group's fields, key values, row indices and the digest of those rows)
   in place of `RowIndex` and `RowSHA256`. This is version 2 of the `DocumentAuthority` contract: an adapter
   declares it, and without it a 2.0 template cannot be issued (422). The product checks the rows against its
   own source snapshot and decides, as it does for a row. The record keeps the group's fields, key values, rows
   and digest; the stamp is unchanged. 1.x keeps the row form. (Wording aligned with the S03-K implementation.)
8. **Refusals say where.** A layout refusal may add `detail` to the problem answer: `reason` (`DOES_NOT_FIT`,
   `NOT_COVERED`, `INCONSISTENT_GROUP`, `TOO_MANY_PAGES`, `BAND_OVERFLOW`) and, where they apply, `band`,
   `element`, `row` and `column` — names and indices, never values. Clients that ignore `detail` keep
   working; the canvas uses it to point at the box (S03-I can only point at the row).
9. **Limits.** A template stays within 64 KiB; at most 64 elements, 24 per band; at most 12 table columns;
   1–3 group fields; a snapshot keeps at most `MaxRows` rows; a document at most 100 pages.

## Alternatives

- **Extend 1.x in place (a 1.4 with bands).** Every 1.x rule (first-page boxes, one bound row, a table of all
  rows) would need an exception; a new major version leaves 1.x frozen and simple.
- **Let Typst flow the text.** Typst would decide positions: the check before a product reserves a number
  could no longer run without Typst, and a value cut or moved by the engine could pass unnoticed.
- **Lay out in the browser, as the prototype does.** Browser text metrics differ from the PDF's fonts and
  engine; the formal file and its check must come from the server.

## Consequences

Two template families are maintained until 1.x is retired, which is a later decision, not taken before
every published 1.x version has been withdrawn by its owner. The issue contract gains a second form, and
the contract simulators implement both. Work order: S03-J (2.0 validator, layout and preview with bands,
groups, wrapping, lines, rectangles, page texts and refusal details; the 1.x golden bytes unchanged; checker
support), S03-K (issue by group: contract version 2, simulators, record, console), S03-L (canvas for bands
and the 1.x to 2.0 conversion), then ADR 0005 (barcodes, QR codes and images rendered on the server as part
of the formal file; no browser library in the formal path).
