# ADR 0012 · Manually entered datasets: a source of their own, marked wherever they show

Date: 2026-11-06. Status: accepted 2026-11-11, by the decision the product owner delegated to engineering that day ("按你的建议全面开始吧");
the open questions are decided at the end. Implementation: S08-A (the data class), S08-B (the manual source), S08-C (the
editor). ADR 0013 decides issuing from these datasets (S08-D). Written before ADR 0018 gave the platform roles; the decisions
below say how the two meet.

## Context

The platform reads data only through an adapter's four calls, `DescribeCatalog`, `PrepareRead`, `ReadPage` and
`AuthorizeCurrent` (`reporting/types.go`, docs/s00/INTERFACES.md). The baseline gives the facts to the source product
and keeps them out of the platform's store: "来源产品负责事实……平台只拥有目录、模板、任务、快照、产物引用与审计数据；来源业务事实
通过版本化接口接入" (docs/s00/ARCHITECTURE.md). S02-A concluded that the receivables need a real source, "而非在报表平台内部另建
财务真值" (docs/REAL_SOURCE_DISCOVERY_S02A.md). The prototype imported local CSV and JSON samples; ADR 0008 left its data
model out, and the document studio's 导入样本 is disabled: "数据来自来源产品的冻结快照；平台不导入 CSV 或 JSON 样本". A
template's `literal` source is text in the template, not data. The only data today is the contract simulators' two
synthetic datasets.

On 2026-11-06 the owner asked for data that people type in or paste, used like any other dataset in reports,
dashboards, exports and documents, and decided that formal documents may be issued from it (ADR 0013).

What the code does that bears on this:

- A dataset is one fixed query shape. A query names exactly the dataset's dimensions and metrics, with no filter and no
  sort (`ValidateQuery`), over at most 64 typed columns: `string`, `date`, `boolean`, `integer`, and `decimal` with its
  scale, currency or unit.
- A connection is configured per (tenant, integration) with an adapter name and options. The shipped command registers
  no adapter; a host's three-line main does (`server/registry.go`, docs/DEPLOYMENT.md). A connection changes with a
  restart. A reload re-reads only the issuer's keys and the bindings, and a binding lists a subject's integrations.
- Every result's evidence says `data_class: "contract-simulator"`, whatever the adapter (`collect` in
  `reporting/service.go`). The PDF footer prints it as 合同模拟器 / 合成数据（非真实业务数据）; the XLSX metadata sheet says
  CONTRACT SIMULATOR / NOT REAL BUSINESS DATA without reading it (`export/xlsx/writer.go`). With a product's adapter this
  would mislabel real data. With typed-in data it would hide where the data came from.
- The platform's store is owner-scoped. An identity sees only its own jobs, snapshots and assets; PostgreSQL enforces
  it with row-level security; the sweep deletes what has expired. Typed-in data is shared and must not expire: one
  person enters it, others read it and issue from it.

## Decision

1. **A manual source is an adapter, `adapters/manual`.** It implements the four calls as a product's adapter does. The
   core, the platform API and the worker do not learn about it. A host registers it under the name `manual` and
   configures a connection for it like any other. It is not a simulator, so a production server may run it.
2. **Its own database.** The manual source keeps its datasets, versions and rows, and with ADR 0013 its numbers, in a
   PostgreSQL database of its own, with its own migrations and database roles, as the durable simulator source does
   (`adapters/contractsim/pgsource`). The connection's options name the environment variable that holds the DSN. A
   memory store serves the demo and the tests. The platform's store, its migrations (001–010), row-level security,
   roles and sweep do not change. The cost is one more database to run and back up. The gain is that business facts
   stay out of a store where every row belongs to one identity and may expire.
3. **Definitions and immutable versions.** A manual dataset has an id, a label, one company id, a timezone and its
   columns, each a dimension or a metric (an `integer` or a `decimal`). The column shape is the dataset's catalog
   version: changing the columns makes a new catalog version, as it would for a product. Rows are never edited in
   place. Each save makes a version that holds all the rows, who saved them, when, and their SHA-256. A version is a
   draft until someone with the `confirm` role confirms it; confirming makes it the dataset's current version. Whether
   the person who saved a version may also confirm it is the connection's choice, except where ADR 0013 requires a
   second person.
4. **Reads see the confirmed version only.** `DescribeCatalog` lists the datasets that have a confirmed version.
   `PrepareRead` freezes the current confirmed version: the version is the source snapshot reference, and its digest is
   the watermark. `ReadPage` reads that version and nothing else, and the totals are the sums of the metrics. Snapshots,
   paging, exports, dashboards and previews therefore work as they do for any source, and a snapshot does not change
   when a new version is confirmed. Versions are never deleted, so every snapshot can be traced to its rows.
5. **Roles by connection.** The connection's options give four roles, each a list of subjects: `read`; `edit`, to
   define datasets and save versions; `confirm`; and `issue` (ADR 0013). A subject also needs the integration in its
   binding. `AuthorizeCurrent` asks again at every read and download, so taking a subject out of the bindings (a
   reload) or out of a role (a restart) stops disclosure, as it does for a product. Datasets for other people go in
   another connection, under another integration id.
6. **Marked wherever it shows.** The data class becomes the adapter's word instead of a constant: `contract-simulator`
   for a simulator, `manual-entry` for the manual source, `business-data` for any other adapter. The result's evidence
   carries it. The PDF footer and the XLSX metadata print it (手工录入（非来源产品数据） and MANUAL ENTRY / NOT FROM A SOURCE
   PRODUCT). The data drawer and the workbench show 手工录入 beside the dataset. The snapshot's source reference names the
   version, so an export can be traced to who saved and who confirmed its rows. The `X-Reporting-Data-Class` header
   keeps saying what kind of server answers (simulator or business), not where one dataset comes from.
7. **Editing has its own API and page.** The manual source serves a small API of its own under `/api/manual/v1/`:
   define a dataset, save a version from rows or from pasted CSV, list and compare versions, confirm one. The server
   mounts it when a connection uses the manual adapter, behind the same authentication, strict JSON, body limits and
   security headers as the platform API, whose routes do not change. The server parses the CSV: UTF-8, a header row
   naming the columns, values in the platform's cell formats, and a bound on rows and bytes that S08-B measures. The
   console sends the pasted text and shows the server's answer. The editor page is reached from the data drawer
   (新建手工数据集, and 编辑数据 on a manual dataset), not from a new rail entry, and exists only when a manual
   connection is configured. The document studio's 导入样本 stays disabled: a template's sample is still not data.
8. **Only for data that no product holds.** A manual dataset must not retype what an integrated product owns, such as
   the receivables of S02-A: that would be the second financial truth S02-A ruled out. The platform cannot check this.
   The manual and docs/DEPLOYMENT.md say it, and the data class shows every such dataset for what it is.
9. **Three slices.** S08-A makes the data class the adapter's word in the core, both exports and the catalog; any real
   adapter needs this too. S08-B adds `adapters/manual`: both stores, the database's migrations and roles, the four
   calls and the connection's roles, registered in the demo, with versions made by tests and the seed script. S08-C
   adds the editing API, the editor page, the manual's chapter, DOM tests and a baseline task, T10 (新建手工数据集 →
   粘贴三行 → 确认 → 在工作台预览; T8 is S09-G's issue task and T9 ADR 0023's filtered export). Each slice adds what it changes to docs/SECURITY_REVIEW.md.

## Consequences

For manual datasets the platform's operator holds business facts and answers for them. The baseline's rule that the
source product owns the facts still holds, with the manual source as that product. Backups cover two databases. The
deployment checklist gains a row for the manual source: its database, its backup, and who holds its roles. The security
review gains a write path: the editing API, the CSV parser and the role checks. Products' adapters change in one
respect, the data class that S08-A takes from the adapter. A manual dataset is not joined with a product's (one dataset
per query, as before), and it has no formulas and no attachments.

## Open questions

- Should reports see draft versions? Proposed: no (decision 4). A draft is seen only in the editor.
- Is one set of roles per connection enough (decision 5), or are roles per dataset needed from the start?

## Decisions on the open questions (2026-11-11)

- **Drafts are not reports.** Reports, snapshots, exports, dashboards and templates see the confirmed version only (decision 4 stands);
  a draft is seen in the editor by those who may edit or confirm. A report built on a draft would be a report on data nobody has
  answered for.
- **Roles per connection, taken from the platform's roles.** ADR 0018 gave the platform five roles from the bindings file. The manual
  source's four (decision 5) are not a second role system: `read` is the platform's `viewer`, `edit` is `designer`, `confirm` is
  `publisher`, `issue` is `issuer`, for the subjects bound to the connection's integration; the connection's options may further
  restrict each to a list of subjects, and may not widen it. Roles per dataset are not needed from the start: datasets with different
  keepers go in different connections (decision 5's last sentence), and a per-dataset restriction can be added inside the connection
  later without changing the contract. Every edit, confirmation and issue is an audit event of ADR 0018 decision 4.
