# S03-G official issue records

2026-10-28. Turns the document templates of S03-E/F (docs/S03E_SCOPE.md, docs/S03F_SCOPE.md, ADR 0002) into
official documents: a published template version, bound to one row of a frozen snapshot, rendered once by the
durable job machinery of S01/S02 into an immutable PDF/A-2b artifact with a numbered, permanent issue record.
Same engine, same sandbox, same placement contract and the same page model as the previews; behaviour is
S03-F's unless stated here.

G1 · Issue job. `POST /issues` with an Idempotency-Key and `{template_id, version, snapshot_id, row_index}`
admits a job of kind "issue". At admission the version must be the template's published version (a draft,
a withdrawn or a later version is 409 TEMPLATE_NOT_PUBLISHED), the caller must be authorized for the
template's fields and background exactly as for a preview, the snapshot must be the caller's, current and
unexpired, and the row must exist. The job freezes the template configuration (its canonical JSON and its
SHA-256), the template id and version, the snapshot id and the row index; the request hash covers all of
them, so a replayed key with other input is 409 IDEMPOTENCY_CONFLICT. The repository assigns the issue
number when it admits the job: consecutive per tenant and integration, starting at 1, never reused. A job
that fails or is cancelled keeps its number, so a gap in the records is always explained by a job with that
number. Jobs of kind "issue" are listed, read and cancelled like the other jobs; their view carries the
number, the template id, version and digest and the row index.

G2 · Execution. The worker re-authorizes the source as for an export, reads the frozen snapshot (digest
verified, unexpired), checks that the frozen configuration is still the template's published version (the
same version and the same canonical JSON, else the job fails with TEMPLATE_NOT_PUBLISHED), binds the row
and the detail table, resolves the background and renders the pages exactly as the preview would, with one
difference: the strip the preview banner occupies (10..18 mm from the top, over the whole safe width, white
underneath) carries the issue stamp instead, `ISSUED No. 000012 - 2026-10-28T06:12:03Z - <issue id>`, in
the text colour: the number, the issue time (the worker's clock at render time, UTC, whole seconds) and the
job id, which is the record id. In variables mode the stamp is printed too. Layout refusals are the
preview's (UNSUPPORTED_COMBINATION), budgets the export's; nothing is published on any error.

G3 · Record. Publication stores two immutable assets in one transaction: the PDF artifact (format "pdf",
the same expiry and the same download rules as an export artifact, with the record id) and the issue record
`{id, owner, number, template_id, template_version, template_sha256, snapshot_id, snapshot_sha256,
row_index, artifact_id, sha256, pages, stamp, issued_at}`. The contract refuses a publication whose record
and artifact disagree with each other or with the job (ErrIntegrity): the artifact's digest, the page count
(1..100), the stamp recomputed from the number, the time and the id. `GET /issues?limit=N` lists the
caller's issue jobs newest first (N 1..100, default 25) with the record of each that succeeded;
`GET /issues/{id}` reads one. Records never expire and are never changed or deleted by any route. The PDF
is downloaded through `GET /artifacts/{id}/content` and expires with the snapshot it was made from: the
data in it stays bound to the source authorization, as every derived artifact is, until a retention slice
says otherwise; the record keeps the digest and page count so that a copy kept elsewhere can be verified.

G4 · Console, checks and storage. The document page gets "正式出具" for the published template that is open
(the designer opened it with 打开已发布模板 and did not change it), the frozen snapshot and the row: it
submits the job with an idempotency key, follows it like an export and downloads the issued PDF as
`issued-000012.pdf`; the task list shows issue jobs with their numbers; a records panel lists the caller's
issue records (number, state, time, template, row, digest) and downloads them. The memory and PostgreSQL
repositories implement the job kind, the numbering and the two assets; the PostgreSQL fixture gains the
kind, the asset kind and a counter table, with a v2→v3 upgrade of the protected test fixture.
scripts/check_pdf_export.py checks an issued document as it checks a preview, plus: the stamp in place of
the banner with the record's number, time and id, no PREVIEW text anywhere, the PDF's digest and page count
equal to the record's, and a negative control for each.

Not in this slice: issuing several rows or a whole snapshot at once, numbering schemes other than one
sequence per tenant and integration, a retention or object-storage policy for issued PDFs, revocation or
correction records, signatures, time stamps from an authority, a canvas editor, physical printer
calibration, production qualification.
