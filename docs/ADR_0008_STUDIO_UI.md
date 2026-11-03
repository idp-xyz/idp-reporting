# ADR 0008 · The console adopts the studio prototype's interface

Date: 2026-11-02. Status: accepted. Implementation: S06-A (the document studio shell), S06-B (the template centre and
the sample pack), S06-C (the rest of the console).

## Context

The user's interactive prototype `IDP_Reporting_Designer_UIUX_V3.0.html` ("IDP Reporting Studio · 专业报表设计器") is
the interface the product was designed to have. In S03-I to S03-N the console took the prototype's *behaviour*
(selection, dragging, snapping, alignment, undo, the command palette, boundary samples, overprint, bands, barcodes)
but kept its own S03-A administrative layout; docs/UX_GAP.md measures the result: the functions exist, the
interface does not look or navigate like the prototype anywhere. The user has decided (2026-11-02) that the console
is to follow the prototype: the whole console, the template centre with the prototype's 37 templates as a sample
pack, and a faithful reproduction, not a loose interpretation.

What must hold: the console's security posture (a strict CSP with no inline script or style, no third-party code at
runtime, no `innerHTML`, the token in memory only), its accessibility (keyboard operation, accessible names, live
regions, light and dark themes, phone widths), its tests (DOM tests through the real Go server; real-browser
scripts as local evidence), and the server's authority over templates, versions, publication, rendering and issue
(ADR 0001–0007). The prototype's data model (localStorage, CSV samples, browser printing as official output, the
embedded barcode library, product-side business validations) stays out (docs/UX_GAP.md "原型里不能照搬的").

## Decision

1. **The prototype's visual system becomes the console's.** Its tokens (colours, type scale, spacing, radii,
   shadows), its layout (topbar, ribbon, tool rail, side panels, work area, inspector, status bar), its icon set
   (inline SVG symbols) and its labels are transplanted into `console/assets/`, rewritten where the CSP or the
   no-`innerHTML` rule requires, and kept byte-for-byte where they do not. Fidelity rule: at 1440 px the console's
   studio and the prototype's agree in structure, labels, states and spacing; every deviation is listed in the
   slice's document with its reason (a server rule, a security rule, an accessibility rule), and nothing deviates
   for taste.
2. **The document designer becomes a full-screen studio** (S06-A): the console's documents page is replaced by the
   studio shell; the canvas, the inspector, the panels and the ribbon call the functions `documents.js` and
   `canvas.js` already have. Components the server does not support (signature block, watermark, computed
   metric, amount summary) appear in the component library disabled, with the reason, so that the library's shape
   stays the prototype's and the user is not misled. Browser preview is the canvas; "预览与导出" is the server's
   PDF preview and the export jobs; "发布" is the server's publication.
3. **The template centre is a page of its own** (S06-B) listing the identity's server templates and a read-only
   sample pack: the prototype's 37 templates converted to schema 2.1 by a reviewed conversion tool, shipped with
   the binary, rendered and checked like any template, with a per-template record of what the conversion dropped
   (watermarks, signature blocks beyond a rectangle, product-side validations). A sample is copied into the
   identity's templates to be edited; it is never edited in place. Favourites, recents, archive and user
   collections are per identity on the server (the private template directory), not in the browser. The
   per-identity template cap rises from 16 to 64.
4. **The rest of the console follows** (S06-C): sign-in, the workspace, the data catalogue (the prototype's "数据中心"
   entry), the report workbench, the dashboard designer (a studio of its own kind), tasks and the system page take
   the same shell and tokens; the embed pages take the same views without the shell, as today.
5. **Evidence.** Each slice ships a real-browser script that screenshots the console beside the prototype at 1440,
   1280, 768 and 390 px (the prototype at 1440 only: it was designed for it) and a document that lists every
   deviation; the DOM tests cover the behaviour. Visual sameness is judged by a person from the screenshots, and
   said to be so.

## Consequences

The console's stylesheet and markup change wholesale; the JavaScript behaviour layers stay. Three slices of mostly
front-end work follow the S04 series. The sample pack adds 37 templates and their renderings to the checker's
corpus. Nothing on the server changes except the template centre's listing and copy routes, the sample pack, the
favourites and collections metadata, and the cap.
