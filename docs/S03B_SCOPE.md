# S03-B1 Report Designer first slice

2026-10-27 Asia/Shanghai. Accepted baseline e1c2fcefde05ca4935c7c8bd7843f5b2cdd943c3.
Continue platform capabilities; real integration remains last.

Implement a screen report layout editor on currently authorized results: select
at least one column, reorder columns, choose left/right alignment, reset layout,
export a bounded metadata-only JSON layout and reopen it through the JSON editor.
Bind each layout to integration + dataset id/version. Reject unknown/duplicate
columns, foreign binding, empty selection, unsupported format and extra fields.
Keep exact original strings and source query/authorization unchanged. No rows,
company selection, token, identity or permissions in saved layout. Clear the
editor on logout; refuse editing without a current result or during operations.
This is a local-file screen-layout slice, not server durable templates/publication,
Dashboard Composer, pdfme Document Template Designer, grouping/formulas, or styled
Excel/PDF. Existing full frozen Excel output is unchanged and labelled in UI.
Do not claim real browser/visual qualification: previous route block stays open.

Public DOM events and actual loopback Go HTTP are the test seam. Preserve prior
S03-A evidence as historical; capture new checks separately. All original core,
adapters and storage remain unchanged. Package complete source/history and save.
