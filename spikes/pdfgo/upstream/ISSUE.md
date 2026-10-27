# Trailer /Info points at an ICC profile when an output intent is added; no /ID for PDF/A

**Version:** codeberg.org/go-pdf/fpdf v0.12.0 (2026-05-18)

## Problem 1: `/Info` names the wrong object

`puttrailer` writes `/Info %d 0 R` with `f.n-1`, assuming the information dictionary is the object just
before the catalog. `enddoc` writes the output intent profile streams (`putOutputIntentStreams`) between
the information dictionary and the catalog, so with one output intent `/Info` names the ICC profile
stream instead.

Effect: readers find no Title, Author, Subject, Keywords, Creator, Producer, CreationDate or ModDate.
PyMuPDF, for example, returns empty metadata. veraPDF does not flag it for PDF/A-2b, because the
information dictionary is optional there and the XMP packet still carries the metadata. That may be why
it went unnoticed.

The reference file `pdf/reference/Fpdf_AddOutputIntent.pdf` shows the defect. Its trailer has
`/Info 7 0 R`, and object 7 is the profile stream, not the dictionary with `/Producer`. The example test
therefore asserts the wrong output.

Reproduce:

```go
pdf := fpdf.New("P", "mm", "A4", "")
pdf.SetTitle("trailer", false)
pdf.AddOutputIntent(fpdf.OutputIntentType{SubtypeIdent: fpdf.OutputIntent_GTS_PDFA1,
    OutputConditionIdentifier: "sRGB", Info: "sRGB", ICCProfile: iccBytes})
pdf.AddPage()
pdf.OutputFileAndClose("out.pdf") // trailer /Info points at the ICC stream
```

## Problem 2: no file identifier

For unencrypted documents the trailer has no `/ID`. PDF/A (ISO 19005-2, clause 6.1.3) requires one, and
PDF 2.0 requires it for every file. veraPDF reports rule 6.1.3-1 for an otherwise conforming PDF/A-2b
file made with `AddOutputIntent` and `SetXmpMetadata`. Encrypted documents get `/ID [()()]`.

## Proposed change (patch attached)

- Record the information dictionary's object number when it is written, and use it in the trailer.
- For unencrypted documents with an output intent, write `/ID [<h> <h>]`, where `h` is the MD5 of the
  bytes written before the trailer. It is deterministic, so output stays reproducible with
  `SetCreationDate`/`SetModificationDate` fixed. Documents without an output intent are byte-identical to
  before. Writing an `/ID` for every unencrypted document would be closer to PDF 2.0, but it would change
  every reference file, so I left that choice to you.
- New tests in `trailer_test.go` fail on v0.12.0 and pass with the change. `pdf/reference/Fpdf_AddOutputIntent.pdf`
  is regenerated, because it recorded the defect. All other tests pass unchanged (`go test ./...`).

Found while validating PDF/A-2b output with veraPDF 1.30.3. With both fixes the files conform with no
failed rules.
