# ADR 0005 · Barcodes, QR codes, images and the issue number in the document

Date: 2026-10-29. Status: accepted. Implementation: S03-M.

## Context

Formal documents carry machine-readable codes and pictures: a waybill or delivery note has a Code 128 bar
code of its number, an invoice or statement a QR code, most documents a logo. ADR 0004 reserved the element
types `barcode`, `qrcode` and `image` for a minor version of schema 2.0 "once the server renders them". The
user's studio prototype draws codes in the browser with an inlined third-party library; the console has no
dependencies and its canvas is not the formal file.

A document's own number is assigned by the source product when it reserves the issue (ADR 0003); today it is
printed only in the issue stamp. Real documents print it in their header and encode it in their bar code.

What must hold: Go owns every position and Typst only draws (export/pdf), so that the layout check runs
without Typst before a product reserves a number (S03-H); what does not fit is refused, never shrunk; the
same input gives the same bytes; the output stays PDF/A-2b; a code that cannot be scanned is a defect of the
formal file, so the evidence must show that every code decodes to its value.

## Decision

1. **Schema 2.1.** `schema_version: "2.1"` is 2.0 plus the element types `barcode`, `qrcode` and `image` and
   the text source `issue_number`. 2.0 and 1.x documents are unchanged and render byte for byte as before.
2. **Encoding in Go, drawing in Typst.** A pinned library, `github.com/boombuler/barcode` (MIT), encodes a
   value into a module matrix behind a small internal package; Go turns the matrix into merged vector
   rectangles at exact positions and Typst draws them. No raster images of codes, no browser library and no
   Typst plugin in the formal path. Writing our own encoders was rejected: QR's error correction and masking
   are where encoder bugs hide, and an independent decoder checks the result anyway (decision 7). A Typst
   plugin was rejected: Go would no longer know the code's geometry before rendering. The library is
   pinned by go.sum, reviewed at every upgrade, and wrapped so that it can be replaced.
3. **Symbologies and values.** Code 128 (automatic code sets; ASCII 32–126; 1–64 characters) and QR (byte
   mode, UTF-8; error correction L, M, Q or H; 1–256 bytes). A value is a literal, a group field, or the
   issue number. A value outside the symbology's alphabet is refused (`NOT_COVERED`), never altered. Other
   symbologies (EAN-13, GS1-128, Data Matrix, PDF417) come in later minor versions.
4. **Scanability is a layout rule.** The quiet zone lies inside the element's box (Code 128: 10 modules each
   side; QR: 4 modules). The module width is uniform, the box width divided by the module count, and must be at
   least 0.25 mm for Code 128 and 0.4 mm for QR; a Code 128 bar is at least max(5 mm, 15 % of the symbol width)
   high; a QR box is square. A value that would need narrower modules is refused (`DOES_NOT_FIT`). A Code 128
   may print its value under the bars, inside its box, checked like a text.
5. **The issue number in the document.** A text, Code 128 or QR may show `issue_number`. A preview prints a
   fixed placeholder (the text `PREVIEW-NUMBER`, encoded as such in a code), so no preview carries a
   number-like value. The layout check before the product is asked proves the fit for every legal number
   (1–32 characters of `[A-Za-z0-9._-]`): a text with the widest 32-character number at its size, a Code 128
   with the largest module count 32 such characters can need, a QR with 32 bytes at its error correction. A
   number the product assigns can therefore never make the render fail after the reservation. The issued file
   shows the record's number in every such element, and the checker verifies it.
6. **Images.** An `image` element references a private image asset of the template's binding (PNG or JPEG,
   at most 512 KiB and 2000 × 2000 px, stored per identity and dataset and immutable, as backgrounds are) by
   id and SHA-256; it is drawn contained in its box, centred, aspect ratio kept. The console shows it on the
   canvas through an owner-only content route. An image is content of the template, never data. An image of
   a seal or a signature is only a picture: the platform does not sign documents, and electronic seals and
   signatures are a separate, later decision.
7. **Evidence decodes.** The PDF checker renders each page and decodes every code with an independent
   decoder (zxing-cpp, pinned in the checker's requirements), compares the decoded text with the expected
   value and the code's position and module size with the expectation, and has negative controls for a wrong
   value, a moved code and a code drawn below its minimum module size. Images are checked by box and
   presence. veraPDF checks PDF/A-2b as before.
8. **The canvas** draws codes as labelled placeholders (symbology, value source, the box and its quiet zone)
   and images from the content route; the PDF preview stays the authority.

## Consequences

A third-party library enters the formal render path, pinned and wrapped; the checker gains a decoder. Schema
2.1 adds three element types and one text source to both validators (Go and the console share their cases).
Documents can carry their own number where real documents print it, with no new risk of a failed render
after the reservation. Work order: S03-M (schema 2.1, encoders and geometry, images and their store, the issue
number source, checker decoding, canvas and panels), then embedding, then the rest of S04.
