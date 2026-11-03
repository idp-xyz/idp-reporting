# S06-B the template centre and the sample pack

2026-11-02. ADR 0008 §3: the template centre of the prototype becomes a page of the console, listing the identity's server
templates and a read-only sample pack, the prototype's 37 templates converted to the platform's schema by a reviewed
tool. The inventory of the prototype's library (taken from the running prototype with `scripts/prototype_inventory.mjs`,
saved as `doctemplates/samples/prototype/inventory.json`): 37 templates in four domains (仓储 WMS 24, 运输/货代 7, 国际贸易
4, 通用业务 2), node types text, field (scope `meta` or `row`), table (columns with field, label, width, align, format;
repeatHeader, carry, subtotal, groupField, groupTotals), line, rect, image, signature, summary, aggregate, barcode, qr,
pageNumber, watermark; bands pageHeader / reportHeader / detail / reportFooter / pageFooter with heights; page presets
A4 and 4 × 6 in labels; two overprint templates (MAWB, `mode: variables`) with 72 fields; the B/L with 52. The prototype's
datasets (ledger, orders, warehouse, mawb, usps, fedex, bol, the trade and WMS kinds) do not exist on the platform: a
sample binds to no dataset until it is copied.

The work is in three parts, each one agent and one review, in this order, all on one branch:

## Part 1 · the server and the renderer

B1 · Page presets. The template document gains `page: {"preset": ...}` with `a4` (default; absent means A4 portrait,
so every stored template is unchanged), `a4_landscape`, `a5`, `label_4x6in` (101.6 × 152.4 mm), `label_100x150`,
`label_100x100`. Typst renders the page size; the safe region is the page less 10 mm on the left and right and the
banner strip plus 7 mm at the top and 27 mm at the bottom for A4 (the current numbers), and scaled rules for the
others, stated in `doctemplates/schema2.go` as one table (preset → width, height, safe region, banner strip); the canvas
draws the page at its size; the checker's page-size assertion reads the preset from the template under test; the
issue stamp strip exists on every preset (a label still carries the stamp). 1.x templates stay A4 only.

B2 · Overprint for 2.x. `page.print: {offset_x, offset_y, background: {id, sha256}, variables_only: bool}` with the 1.x
semantics (S03-E/S03-H: the offset moves every element, the background PNG is drawn under the page in full mode and
omitted in variables mode, literal texts are omitted in variables mode); the studio's existing print panel applies to
2.x; previews and the checker cover both modes on a 2.x template.

B3 · Limits. `MaxItems` 64 → 128 and `MaxBandItems` 24 → 96, so that the MAWB (72 fields) and the B/L (52) fit; the
worst-case pre-issue proof and the 15-second preview budget are re-measured with the heaviest samples and the numbers
go in docs/CAPACITY.md (a sample that does not fit the budget is converted with fewer elements and said so).

B4 · Samples and the catalogue on the server. `doctemplates/samples/` (embedded with `embed.FS`): the converted
templates, each with `kind`, `name`, `domain`, `flow`, `description`, `fields` (the prototype's field slots: id, label,
type, scope `document`|`row`) and `rows` (the prototype's sample rows, for the preview of the sample). Routes:
`GET /document-templates/samples` (the list with domain, flow, name, description, preset, element counts; no
rows), `GET /document-templates/samples/{kind}` (the document and its fields and sample rows),
`POST /document-templates/samples/{kind}/previews` (a PDF preview rendered from the sample's own rows, PREVIEW-marked,
no dataset, no authorization beyond a signed-in identity), `POST /document-templates/samples/{kind}/copy`
`{dataset, name, mapping: {<sample field id>: {"column": "<dataset column id>"} | {"literal": "<text>"}}}` which binds
the sample to the caller's dataset (document-scope fields become the group fields or literals; row-scope fields become
columns or literals; an unmapped field becomes the literal "[<label>]"), validates the result exactly as a save does, and
stores it as the identity's new template version 1 (quota and cap apply; the response is the saved template). Per-template
catalogue metadata on the identity's store: `domain`, `flow`, `favourite`, `archived`, `collections` (names, at most 16 of
at most 40 characters), `last_opened` (set by the server when a version is read), with `GET /document-templates/{id}/catalog`
and `PUT` (whole object, `expected_revision`), and the listing carrying it. The per-identity cap rises from 16 to 64
templates (store, docs, tests). Everything is per identity; nothing is shared between identities.

## Part 2 · the conversion

B5 · Extraction. `scripts/prototype_templates.mjs` (Node, CDP, the pattern of `browser_ux_gap.mjs`) loads the
prototype from a path given on the command line and writes the 37 documents (the last version's `doc`, the dataset
definition it binds and the sample rows the prototype holds for it) as `doctemplates/samples/prototype/<kind>.json`,
plus `inventory.json`. The files are committed: they are the input of the converter and the record of what the
prototype said. (The prototype file itself is not committed.)

B6 · Converter. `cmd/reporting-samples` (`convert --in doctemplates/samples/prototype --out doctemplates/samples`,
deterministic, run by `go generate`): prototype document → schema 2.1 sample. Mapping, each rule with a test: bands
and heights → bands (页眉/页脚 heights kept; 报表头/报表尾 → document header/footer; the detail band → the table);
text → text (size, bold, align, wrap from the style; colours dropped); field scope `meta` → a document-scope field
slot, scope `row` outside the table → a row field slot of the first row (as 1.x row fields), inside the table → a
column; table → the detail table (columns with width and align; `repeatHeader`, `carry` and `subtotal` → the
platform's repeated header, carried sums and group totals; `groupField` → the group fields; `striped`, `sortField`,
`negativeRed`, `headerColor` dropped); line → line; rect → rect; image → image slot (the prototype's images are
empty; an image placeholder is a rect with the label); barcode and qr → barcode (code128) and qrcode with the value from
the field slot; pageNumber → page text with `{page}` / `{pages}` from the pattern; signature → rect plus the literal
("签章与签名属于后续阶段" in the record); summary and aggregate → group_sum of the named column where the prototype
computes a sum of one column, else a literal with the label and a record entry; watermark → dropped, recorded; parameters
→ dropped (the platform's query panel is the parameter); the two overprint templates → `page.print` with
`variables_only` and the offsets, the background left empty (the MAWB form guide of the prototype is a drawing, not a PNG:
recorded). Every drop or substitution goes into `doctemplates/samples/<kind>.record.json` and `docs/SAMPLES.md` lists them per
template. The converter refuses to emit a sample that fails `doctemplates` validation; the samples are regenerated from
the committed prototype files and a test asserts the committed samples equal the converter's output.

B7 · Qualification of the pack. Every sample renders with its own rows (B4's preview route) and passes the checker
(`scripts/check_pdf_export.py` gains the samples as a corpus: 37 PDFs, the page preset asserted per sample, barcodes
decoded where present); the 37 PDFs under `evidence/s06b/` with the checker's report; every sample opens in the
studio canvas (DOM test over all 37); the heaviest render times are in CAPACITY.md (B3).

## Part 3 · the template centre

B8 · The page. A new console page `模板中心` (nav entry, the studio's crumb and wordmark go there, the prototype's "返回
设计器" returns to the studio with the last template) reproducing `evidence/ux-gap/proto-10-library.png`: the quick access
list (全部模板, 我的收藏, 最近使用, 归档, with counts), 业务域 with counts (the four domains, from the identity's templates'
catalogue metadata plus the samples), 自定义集合 (the identity's collections), the search box (name, flow, tags,
collection, id), the filters (业务流程, 状态: 草稿 / 已发布 / 已归档, 排序: 名称 / 最近打开 / 最近更新), list and grid views,
the result count line, template cards (icon by kind, name, domain / flow / preset, status badge 已发布 vN / 草稿, "N 个保存
版本", favourite star, 分类 (a popover: domain, flow, collections), 复制, 归档, 编辑 → the studio), the header buttons 返回设计器,
数据中心 (→ the data catalogue page), 新建模板 (B9), 导入模板 (JSON, as the studio's import). A section "示例模板" (37, the
prototype's four domains, each card with 预览 (B4's sample preview, in the studio's preview dialog) and 复制到我的模板 (the
mapping dialog of B9, then opens the copy in the studio)). Samples are read-only and never appear in 我的收藏 or 归档; a
copied sample is an ordinary template.

B9 · The new-template wizard and the mapping dialog. 新建模板: step 1 业务流程 (domain → flow, or 空白模板), step 2 版式
(a sample of that flow, or blank with a page preset), step 3 数据 (the dataset of the current snapshot, shown; the mapping
table: each sample field slot → a dataset column (by type where it fits, pre-filled by label match) or a literal), step 4
名称; finish = B4's copy route (or a blank save), then the studio opens it. The same mapping dialog serves 复制到我的模板.
The dialog is `role=dialog`, focus-trapped, keyboard-operable.

B10 · Tests and evidence. Go: presets (every preset's page, safe region, stamp strip; 1.x refuses a preset), 2.x
overprint (both modes, the offset, variables omission, previews and the checker), limits, the samples routes (listing, one
sample, preview, copy with every mapping case, quota and cap, another identity cannot read a copy), catalogue metadata
(round trip, revision conflict, limits, last_opened), the cap, the converter's rules and determinism. DOM: the page's
every control with a real server and real templates and samples; the wizard and the mapping dialog; the cards' actions;
the crumb from the studio. Real-browser script `scripts/browser_library_s06b.mjs`: the centre beside the prototype's
library at 1440, alone at 1280/768/390 light and dark, a session (search, filter, favourite, collection, copy a sample
with a mapping, open in the studio, archive). docs/SAMPLES.md (the 37, their mapping records), docs/API.md, docs/CONSOLE.md,
docs/STUDIO.md (the crumb), docs/CAPACITY.md (B3), README S06-B.

Not in this slice: the other console pages (S06-C), sharing templates between identities, the prototype's business
validations (product-side), watermarks and signatures as elements, printing from the browser, custom page sizes
beyond the presets.
