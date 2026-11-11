# S08-A · The data class is the adapter's word

2026-11-11. ADR 0012 (docs/ADR_0012_MANUAL_DATASETS.md) decision 6 marks data "wherever it shows" by a data class that "becomes the
adapter's word instead of a constant". Until this slice every result's evidence said `data_class: "contract-simulator"` whatever the adapter
(`collect` in `reporting/service.go`), the PDF footer printed 合同模拟器 / 合成数据（非真实业务数据） from it, and the workbook's `Metadata`
sheet said `CONTRACT SIMULATOR / NOT REAL BUSINESS DATA` without reading it. With a product's adapter (the generic `http` adapter exists
since S19-A) real data was labelled synthetic; with typed-in data (S08-B) its origin would be hidden. Decision 9 gives this slice: "S08-A
makes the data class the adapter's word in the core, both exports and the catalog; any real adapter needs this too". The manual source is
S08-B, the console's wording S08-C.

For a contract simulator nothing changes: the same word in the evidence, a PDF byte for byte what it was, a workbook cell for cell ("For a
simulator" below).

## What

A1 · **The closed set and the capability** (`reporting/dataclass.go`; the core stays standard-library-only and domain-neutral). Three words,
`contract-simulator`, `manual-entry`, `business-data` (`DataClassSimulator`, `DataClassManual`, `DataClassBusiness`; `DataClasses`,
`ValidDataClass`). An adapter says its class through the optional capability `ClassedAdapter` (`DataClass() string`), in the manner of
`DocumentAuthority` and `CompanyDirectory`: an adapter has it or not by its type. An adapter without it serves `business-data`
(`DataClassOf`), which is ADR 0012's "`business-data` for any other adapter": an adapter of a source product needs to do nothing. The word
is the adapter's as a whole, not a dataset's, a principal's or a read's.

A2 · **The service** (`reporting/service.go`). `NewService` reads the word of each connection's adapter once and refuses a connection whose
adapter says a word outside the set (`ErrInvalid`; an adapter that has the capability and says the empty word is refused too: it is not an
adapter that says nothing). `collect` writes the connection's word into `Evidence.DataClass`, for a preview and for a snapshot.
`Service.DataClass(principal, integration)` answers the word of one of the principal's products, with the catalogue's resolution and
refusal. Nothing else of the core assumed the constant: the scheduler submits the owner's snapshot and export jobs and reads no evidence;
the issue path names snapshots and never a class. Tests (reporting): `TestTheDataClassIsTheAdaptersWord` (a fake adapter per class and one
that says nothing, each over the receivables fixture: the word in a preview's evidence, in the stored snapshot, on both pages of it, in
what the export worker hands a writer, and from `Service.DataClass`; a product the principal does not hold is refused),
`TestNewServiceRefusesAnAdapterThatSaysAnotherWord` (seven words outside the set; each of the three accepted).

A3 · **The adapters.** The two contract simulators say `contract-simulator`: `contractsim.Simulator.DataClass` and
`pgsource.Source.DataClass` (the durable fixture). `adapters/httpsource` says nothing, in any of the six forms `New` returns: the source
protocol carries no data class, and a product behind the protocol is business data. `sourcetest.Remote`, the tests' composition of an
adapter behind the reference source, passes the wrapped adapter's word on (as it passes on its issuance and its company directory): it is
the one place where `sourcetest` returns an adapter that stands for the wrapped one, and a test knows what it put behind the protocol; the
returned adapter keeps exactly the capabilities the connection declares. A wrapped adapter that says nothing is returned as the platform's
adapter is. Tests: `TestTheAdapterSaysNoDataClassAndTheReferenceCompositionPassesTheWrappedOneOn` (httpsource: the six forms say none; the
six shapes behind `Remote` say the wrapped word and keep their capabilities; the evidence of a service in process and through the protocol
alike), the contract scenario `the_evidence_says_the_adapters_data_class` (integration: both simulators, in process and over the protocol,
in the memory store and in PostgreSQL), `TestDurablePostgresContract` (the durable fixture's word and the snapshot PostgreSQL keeps),
`TestTheShippedRegistryServesAnHTTPSourceEndToEnd` (server: the shipped `http` adapter in front of the reference source answers
`business-data` in the catalog and the evidence).

A4 · **Stored evidence** (`reporting/repository_contract.go`, `reporting/service.go`). A frozen snapshot keeps the word it was frozen
with: the service never rewrites it from the connection. `ValidatePublication`, which both stores call before they store a snapshot,
refuses evidence whose data class is not one of the three (`INTEGRITY_FAILURE`), and every read of a stored snapshot (`Service.snapshot`:
a page of rows, a job poll, an export, an issue, a snapshot handed to a renderer) refuses one that holds another word, or none. No
migration: every snapshot stored before this slice says `contract-simulator`. Tests (reporting):
`TestAStoredSnapshotKeepsTheWordItWasFrozenWith` (the same store and source behind an adapter that says another word: the old snapshot is
served and exported under its own word, new reads under the new one), `TestStoredEvidenceIsHeldToTheClosedSet` (the three words publish,
five others do not; a store that answers a snapshot with a word outside the set: no page, no export, no snapshot for a renderer, no job
view).

A5 · **Exports** (`export/pdf/layout.go`, `export/xlsx/writer.go`). Both print the snapshot's own word in fixed words, one text for each
class; a stored word outside the set is refused before anything is laid out or written (`INTEGRITY_FAILURE`), never printed as it is.

| `data_class` | PDF footer, every page | Workbook `Metadata`, row `data_class` |
|---|---|---|
| `contract-simulator` | `合同模拟器 / 合成数据（非真实业务数据）` (unchanged) | `CONTRACT SIMULATOR / NOT REAL BUSINESS DATA` (unchanged) |
| `manual-entry` | `手工录入（非来源产品数据）` | `MANUAL ENTRY / NOT FROM A SOURCE PRODUCT` |
| `business-data` | `来源产品数据` | `SOURCE PRODUCT DATA` |

The footer is `<class text> · 快照 <id> · <dataset>@<version> · 截至 <date> · 来源水位 <watermark>`, as before, with S20-C's mark beside the
snapshot when the query has filters or a sort. The class's text is data like every label: its characters are checked against the fonts
when it is printed. Presented exports (S23-A) print the same. The document templates' previews and the issued documents print no data
class, before and after this slice (the banner says PREVIEW, the stamp the product's number, time and record id; `doctemplates`,
`issuance`, `export/pdf/document.go`, `layout2.go` and `document.typ` name no class), so nothing changes there. Tests:
`TestTheFooterPrintsTheDataClassOfTheSnapshot` (pdf: the three footers to the character, plain, presented and with a filtered query;
everything else of the document equal to the simulator's; the closed set; a font that lacks a character of one text; six words outside the
set, refused as integrity failures before the fonts are asked), `TestTheMetadataSheetSaysTheDataClassOfTheSnapshot` (xlsx: row 1 for each
class, plain and presented; each workbook equal to the simulator's but for that one cell; five words outside the set refused, no file),
`TestTypstLaysOutDocuments/class_manual` and `/class_business` (with Typst and the pinned fonts: rendered twice to the same bytes, another
file than the simulator's, written into `REPORTING_PDF_EVIDENCE` with their expectations, so the checker reads the class's text back from
the footer and veraPDF validates them; `testdata/golden.json` gains their two layouts).

A6 · **The catalog and the API** (`transport/httpapi/api.go`). Each entry of `GET /catalog?integration_id=…` carries `data_class`, the
word of the integration's connection, so that a client knows what it is looking at before it reads a row:

```json
[{"dataset": {"id": "receivables.aging", "version": "1.0.0"}, "columns": […], "metrics": […], "dimensions": […],
  "currency_policy": {…}, "timezone": "Asia/Shanghai", "filters": […], "sorts": […], "data_class": "contract-simulator"}]
```

It is the last key of the entry and the platform's word about the connection: it is no field of `reporting.Dataset`, which a source
publishes, and no part of the source protocol, so a source cannot send it. The evidence of `POST /queries` and of
`GET /snapshots/{id}/rows` carried `data_class` already and now says the adapter's word. `docs/openapi.json` (held to the routes by
`TestOpenAPIMatchesTheBoundRoutes`): the schema `DataClass` (the closed enum), `Dataset.data_class` (required) and `data_class` of the two
evidence objects as that schema; the catalog's description. `docs/API.md`: "Data class". Test (httpapi):
`TestTheCatalogAndTheEvidenceSayTheDataClassOfTheConnection` (three connections, a simulator, an adapter that says `manual-entry` and one
that says nothing: the catalog's answer to the byte, the published dataset followed by the word; a preview's evidence, a page's, the
workbook's `Metadata` row; a product the principal does not hold is 403 and says nothing).

A7 · **The server** (`server/registry.go`). An adapter reports itself a contract simulator by `IsSimulator()`, as before, or by the data
class `contract-simulator`; either needs `allow_simulator`. The two words must agree: an adapter that says `IsSimulator()` without the class
`contract-simulator`, or a class outside the set, refuses the start (step `adapters`, a fixed text naming the integration and the adapter
of the configuration), so that a simulator's data is never labelled a source product's on a server that runs it. Tests (server):
`TestStartRefusalsNameTheStepAndCarryNoSecret` (five new refusals: a simulator by its class alone without the allowance; one that hides
the simulator's class, even where simulators are allowed; one that says another class; a word outside the set; the empty word),
`TestAResultSaysItsAdaptersDataClassUnderTheServersHeader` (below).

A8 · **No console change.** `console/assets/app.js` does not validate the evidence's data class: it reads `query_hash`,
`source_snapshot_ref` and `source_watermark` for the evidence line and nothing else of the evidence, `validDataset` checks the types of
the catalog keys it uses and accepts others, and no script reads the `X-Reporting-Data-Class` header. A business adapter's result and a
catalog entry with `data_class` are therefore not refused, and the DOM suite passes unchanged against the demo, whose catalog now carries
the key. The console's own wording (数据类别 in the capability details, the 合同模拟器 badge) is still fixed text and is S08-C's.

A9 · **Docs**: this file; docs/API.md ("Data class", the catalog row); docs/openapi.json (A6); docs/DEPLOYMENT.md (the data class of a
host's adapter); docs/SOURCE_HTTP_PROTOCOL.md ("Not in version 1": no data class); docs/SECURITY_REVIEW.md (a section, and the S19-A note
that the class was a constant); ADR 0012's status line; ADR 0017's Progress line. docs/REAL_SOURCE_ONBOARDING.md does not describe the
evidence and is unchanged.

A10 · Not done: the manual source that says `manual-entry` (S08-B); the console's words for the class (S08-C); a data class on the issued
documents (ADR 0013 decides issuing from manual datasets, S08-D).

## For a simulator nothing changes

- **Evidence and catalog**: `data_class` is `contract-simulator`, the word the constant was; the catalog's entries gain the key (A6).
- **PDF: byte for byte.** The footer's text for the class is the string it was. `export/pdf/testdata/plain_sha256.json` is not touched
  and `TestTypstLaysOutDocuments/plain_golden` holds the ten plain documents to the SHA-256 recorded before S23-A.
- **Workbook: cell for cell.** `export/xlsx/testdata/cells_before_s20c.json`, recorded at 43f8db5 when the note was a constant, is not
  touched: `TestAWorkbookWithoutFiltersOrSortIsWhatItWasButForTheScopeRow` requires a simulator's workbook, plain and presented, to be
  those facts (and S20-C's scope row). The fixture now carries the simulator's class, as every stored snapshot does.

## The header and the data class

`X-Reporting-Data-Class` keeps its meaning (`httpapi.WithDataClass`, `server/server.go`): it says what kind of **server** answers, on
every answer, a 404 and a health probe included: `contract-simulator` for the core API, the demo and a production server whose
configuration says `allow_simulator`; `business-data` for a production server. `data_class` says where the data of **one connection**
comes from, in the adapter's word, and has a third value. They meet in one rule, which A7 makes true: a server whose header says
`business-data` has no connection whose data class is `contract-simulator`, so its results say `business-data` or `manual-entry`; a
server whose header says `contract-simulator` may serve data of any class. `TestAResultSaysItsAdaptersDataClassUnderTheServersHeader`
runs the four combinations through a real server: a simulator and typed-in data where simulators are allowed, typed-in data and an
adapter that says nothing on a production server; `TestDataClassFollowsTheProfile` (S05-A) is unchanged.

## Interpretations of ADR 0012 decisions 6 and 9

- **"The catalog"** of decision 9 is the answer of `GET /catalog`: each entry carries the class, because the answer is a list of datasets
  and the data drawer shows the mark "beside the dataset" (decision 6). The word is the connection's, so the entries of one integration
  all say the same. It is added by the platform at the route, not by a field of `reporting.Dataset`: a source must not be able to say it.
- **`business-data` is worded as where the data comes from**, 来源产品数据 / `SOURCE PRODUCT DATA`, and claims nothing of its truth or
  of its being "real": the platform cannot know that, and an operator could put the `http` adapter in front of anything.
- **The word is read once**, when the service is made, and a snapshot keeps its own: a class does not change under a running server, and
  what was frozen as typed-in data stays marked as that.
- **An unknown stored word is an integrity failure at both ends**, when a snapshot is published and whenever it is read; the exports
  check again and never print a word as it is.
- **An adapter's two words agree** (A7). ADR 0012 says nothing of `IsSimulator`; without the rule a host's simulator that hides its class
  would be labelled a source product's, the one mislabel the ADR exists to prevent.
- **`sourcetest` passes the word on in `Remote` only**: the reference source's handler serves the protocol, which carries no class.
- **The documents print no class** and were not given one: the slice makes the existing marks the adapter's, it adds none.

## Evidence

- The strict Go check (`scripts/go_test_strict.py` with CI's `EXPECTED_SKIPS`), `gofmt -l .` and `go vet ./...` are clean; no new
  top-level Typst test, so the three skip lists of `ci.yml` are unchanged. `reporting/` depends on the standard library only.
- The PostgreSQL step (`REPORTING_S01_REQUIRE_PG=1`, `-p 1`) passes: the contract scenario and the durable fixture's word, and every
  publication through `ValidatePublication` in the durable store.
- `go test ./export/pdf/... -count=1` with the pinned Typst and fonts and `REPORTING_PDF_REQUIRE=1` passes, `plain_golden` included;
  `scripts/check_pdf_export.py --negative` finds the class's text in the footers of `class_manual.pdf` and `class_business.pdf` and
  catches every negative control.
- The console suite (format, lint, `go test ./console/...`, the jsdom tests against the real demo server with Typst) passes unchanged.

## Known

- A wrapper that embeds only `reporting.Adapter` hides the word of what it wraps, as it hides `IsSimulator` and every other optional
  capability, and its data is then `business-data`. The shipped command registers only the platform's own `http` adapter; a host's
  registry, and any wrapper in it, is the host's code to review (docs/DEPLOYMENT.md says how an adapter says its class).
- A host adapter that reports `IsSimulator()` and does not yet say the class `contract-simulator` refuses the start after this slice,
  with a text that says why: it must add `DataClass()`.
- No adapter says `manual-entry` yet (S08-B); the class is exercised by fake adapters in the tests.
- The console shows a fixed 数据类别 until S08-C; a result of another class is displayed, exported and marked in the files correctly, and
  mis-described only in that fixed text.
- The manual's sample `docs/manual/testdata/catalog.json` is a recorded example from before S20-A (it has no `filters` either) and is not
  regenerated here.
