# ADR 0026 · A result outlives the read it was frozen from: the source vouches for the scope, the platform keeps by class

Date: 2026-11-11. Status: **accepted** 2026-11-11. This is a contract-level decision (the source protocol, and what the platform
stores of business data and for how long), so it waited for the product owner's answers to the two questions at the end
(CODING_STANDARDS.md); the owner chose the model of the decision below with its default times. It adds a second time to the
`PrepareRead` row of docs/s00/INTERFACES.md, makes ADR 0003 decision 1 true, and changes the sentence of ADR 0025 (S24, in
review) that a file lives while its snapshot's read does. It keeps every authorization rule of the baseline. Implementation:
S25-A to S25-D.

## Context

One time bounds everything the platform makes from a source read: `PreparedRead.ExpiresAt`, which the source gives and the
platform refuses when it is more than 15 minutes ahead (`Service.validPrepared`, `protocol.MaxReadLifetime`; both simulators give
10 minutes). The snapshot takes that time as its own, and so does every file made from the snapshot: the store accepts a
snapshot, an export or an issued PDF only when its expiry equals the read's (`ValidatePublication`). Every disclosure is held to
it: a page of rows and a download both go through `Service.snapshot`, which refuses once the time has passed, and a file is
served only through its snapshot and never later than it (`artifactRow`). The sweep deletes the rows and the files a grace
period (24 hours) after that time (S04-B).

That one time stands for three different things:

1. how long the source serves pages of the data version it froze (its cost: it holds a frozen version);
2. how long the source answers whether the person's current rights still cover what the read was frozen on (its cost: it
   remembers a scope);
3. how long the platform keeps, and hands out, what it made from the read.

Tying the third to the first has consequences that the slices built since S04 did not look at:

- **Scheduled reports** (ADR 0020, S16-A, S16-B). A run freezes and exports at 07:30. The webhook and the mail say it is done,
  and the mail links to the console, "where the owner downloads the file with their own access". Within at most 15 minutes of
  the run (10 with the simulators) the download answers 409 `INTEGRITY_FAILURE`, and a day later the file is deleted. Neither
  ADR 0020 nor the scopes of S16-A and S16-B mention this.
- **Formal documents.** ADR 0003 decision 1 says "The platform renders the document and keeps the formal file and its record",
  after the baseline's "平台保留正式文件及内容版本" (docs/s00/ARCHITECTURE.md). S04-B B1 deletes an issued PDF with its snapshot,
  and the record's view "says the file is no longer held". An issued document can therefore be downloaded for the minutes its
  snapshot has left and never again. The platform cannot make it again (a new issue is a new job id and a new number,
  docs/OPERATIONS.md), and the product holds the digest of a file that nobody has unless the person saved it in time.
- **Batches** (ADR 0025): "Whatever a batch is, it is over in a quarter of an hour", and each of its files has to be downloaded
  inside that quarter of an hour.

The rule had a reason, and the reason stays. S04-B: "keeping business data after its authorization has expired is a
data-minimisation defect". The baseline lets the platform keep "有生命周期的授权投影/结果", and requires that a revocation refuses
the whole result, that an authority that cannot be asked refuses too, and that nothing falls back to the owner alone
(docs/s00/INTERFACES.md: "撤权…整份结果/文件拒绝", "不能退化为仅凭 owner 放行"). What the reason needs is that the platform never
discloses a result the source would no longer authorize. It does not need the authorization to end when the source's paging
does.

No product implements the protocol yet and no production store exists, so today the change costs a revision of a document and
of the simulators. After the first product it costs that product a release.

## Decision

1. **Three lifetimes, each with its own name.**
   - *The read*: `expires_at`, at most 15 minutes ahead, unchanged. While it lasts the source serves pages, and the product can
     check a row against its own frozen rows, which issuing needs (`PrepareDocument`, ADR 0003).
   - *The scope*: `scope_expires_at`, new and optional. Until it passes, the source answers `AuthorizeCurrent` for the read.
   - *The result*: until when the platform keeps a snapshot or a file. The platform decides it, never later than the scope.
2. **The source vouches for the scope.** `PreparedRead` gains `ScopeExpiresAt`; on the wire the read gains the optional
   `scope_expires_at` (protocol revision 1.2, additive). Absent, it is `expires_at`: a source that says nothing is served exactly
   as today, and everything made from its reads lives as it does today. Present, it is a canonical time not before `expires_at`,
   within the years the protocol's times allow. Until it passes, `authorize` answers what it answers today: whether the actor's
   *current* rights still cover the whole frozen scope (every company, every row the read was made on, the zone and the currency
   policy; widened rights never widen an old read, narrowed rights refuse it). It answers so after `expires_at` too, when
   `pages` answers `expired`. What a source keeps in order to answer (a row per read with the grant it froze, or a handle that
   describes itself) is its own business; it need not keep the data version for it. The platform never asks after
   `scope_expires_at`.
3. **The platform keeps a result by its class, never beyond the scope.** A result's expiry is fixed when the job that makes it
   is admitted: the read's `expires_at` plus its class's time, or the read's `scope_expires_at` when that is earlier. The classes:
   - a *working snapshot* (a person's 创建快照): no time beyond the read's `expires_at`, as today. It is working state; the person
     freezes again.
   - a *kept result*: every export file, and the snapshot of a scheduled run. `retention.results`, default 7 days.
   - an *issued document*: `retention.issued`, default 10 years.

   The sweep deletes by the result's own expiry plus the grace, as today. The issue record stays permanent. A change of the two
   times applies to the jobs admitted after it.
4. **Every disclosure still asks the source.** A page of a kept snapshot and every download check, in this order: the row is the
   caller's (row-level security), the product is still bound, the result's own expiry has not passed, the source authorizes the
   read's scope now, and the bytes match the row's digest. A source that cannot be reached is 503, a refusal is a refusal, and
   nothing falls back to the owner. Revoking a person's rights at the product therefore stops the next download of everything
   that person holds, however long it is kept.
5. **A file stands without its snapshot.** An artifact carries the frozen query and read it was made from, and its own expiry, so
   that it can be authorized after its snapshot's rows are gone. The rule "never later than its snapshot" goes. A file stored
   before this change keeps the old rule.
6. **What still needs the read.** Freezing a snapshot (the pages) and issuing: the product checks the row or the group against
   its own frozen rows, so an issue is admitted and rendered only while the read lasts, and a batch (ADR 0025) is still over when
   the read is. What changes for a batch is that its files are there afterwards. What needs only the platform's stored rows and
   the source's authorization (a page of rows, a preview, an export) needs the snapshot and its scope, not the read; of today's
   snapshots that concerns the kept ones, a scheduled run's.
7. **Quotas.** `stored_results` counts the snapshots and export files that are stored and not yet expired, as today. Issued files
   do not count, as their records do not: admissions per hour bound them. Its default rises from 64 to 512, because an export
   now lives for days; a deployment sets it together with `retention.results`.
8. **The views say until when.** A job's view gains `result_expires_at`; `expired` keeps its meaning (the result is no longer
   served). The console shows 可下载至 … on the tasks, the issue register and a schedule's runs.
9. **Not decided here.** Who else of the tenant may read a kept file (a register for the tenant, ADR 0025's last alternative):
   until that is decided a kept file is its owner's alone, and when the person's binding is removed nobody reads it through the
   platform. A product authorizing one document rather than the whole scope of the read it came from: a document outlives
   changes of its issuer's rights, so this is likely to be asked for with the first real product, and it would be an additive
   call of the document authority. A longer read for a long batch. An archive of a batch's files. A legal hold. Encryption at
   rest beyond what the operator's store gives.

## Alternatives

- **Keep the one time, and tell people to download at once.** What the platform does today. It leaves ADR 0003 decision 1 untrue
  and a scheduled report without a file by the time its mail is read.
- **After publication, the owner alone.** Keep the files and serve them to their owner without asking the source again. The
  least work for a product. It breaks the baseline's "不能退化为仅凭 owner 放行": a person whose rights the product has revoked
  would go on reading every file they ever made.
- **Raise the cap on the read** from 15 minutes to days. The source would hold a frozen data version for as long. That cost
  grows with the data, and the years of an issued document are out of reach. A scope is a few hundred bytes.
- **Keep nothing; make the file again when it is asked for.** A fresh read is other data, so the file would not be the one that
  was delivered, and an issued document cannot be made again at all.
- **Give the file to the product to keep.** It moves the archive into every product and contradicts the baseline. A product that
  wants its own copy downloads it like anyone.
- **Authorize documents one by one and leave the rest.** It would fix issued documents and neither exports nor scheduled reports.
  It is decision 9's refinement, on top of this.

## Consequences

- The source protocol gains one optional field. A product on revision 1.0 or 1.1 is unchanged, and so is everything made from
  its reads. A product that vouches pays for what it vouches: a scope it can still judge, for as long as it said.
- ADR 0003 decision 1 becomes true: the platform keeps the formal file. A scheduled report's file is there when its mail is
  read. A batch's files are there after the batch.
- The platform holds business data longer: export files and scheduled snapshots for days, issued documents for years, in the
  object store and the database. Backups (S04-F) and `verify-objects` cover them as they cover today's. Encryption at rest is
  the operator's. docs/SECURITY_REVIEW.md gains a section and the go-live checklist a row for the two retention times.
- The checker can verify that a declared scope is well formed and that a refusal still refuses. It cannot wait for a read to
  expire, so that a source goes on authorizing afterwards is verified by the product's own test, and the onboarding guide says
  so.
- S24 and S16 need not wait for this: the refusal of a cancelled file (S24-A), the plan of a batch (S24-B) and the runs'
  retention (S16-C) do not change. Their sentences about a file living while the snapshot's read does change when this lands.
- No change of the store's schema is expected: the expiry is a column since migration 005, and the payloads gain optional
  fields. S25-A confirms it or adds the migration.

## Slices

- **S25-A**: the core and the stores. The three lifetimes, an artifact's own basis and expiry, the retention classes and their
  configuration, the sweep; the in-memory simulator (a scope it keeps after the data); the memory and PostgreSQL contracts. With
  no source declaring a scope, every test of today passes unchanged.
- **S25-B**: the protocol's revision 1.2, the adapter `http`, `sourcetest`, the durable simulator, the checker's rules; the quota
  of decision 7, with the index it needs; the reconciliation of the object store, on a cadence of its own.
- **S25-C**: the views (`result_expires_at`), the console (the tasks, the issue register, a schedule's runs), the manual, and a
  baseline task (打开昨天的定时报表并下载).
- **S25-D**: the documents. API, OPERATIONS, SECURITY_REVIEW, DEPLOYMENT, the onboarding guide; the notes in ADR 0003, 0020 and
  0025.

Amended 2026-11-12, with S25-A (docs/S25A_SCOPE.md). The list above first had the quota in S25-A and both simulators in S25-B.
No decision changed; what changed is which slice does what, and two conditions of S25-B that the first slice found:

- The quota of decision 7 is in S25-B, and lands there before any source can vouch: counting the issued files out of
  `stored_results` without reading every artifact's payload at every admission needs a partial index, so a migration, and the
  three lifetimes need none (the expectation in "Consequences" held for them).
- The in-memory simulator vouches for a scope in S25-A, because the lifetime contract of both stores runs against it.
- The reconciliation of the object store lists the whole bucket at every sweep (S04-E). That was a day of files; with kept
  results it would be the kept history. S25-B gives it a cadence of its own, or keys it can list by age, before a source can
  vouch.
- A server of the code before S25-A does not know a kept result. Every server on a store runs S25-A's code before a source of that
  deployment declares a scope; S25-D writes the rule into docs/OPERATIONS.md.

## Questions for the product owner

- **Q1, the model.** (a) The source vouches for the scope and the platform keeps by class, as above (recommended). (b) After
  publication the owner alone, without asking the source again. (c) Leave the one time as it is.
- **Q2, the default times** (a deployment may change them). Export files and scheduled snapshots 7 days, issued documents 10
  years, working snapshots unchanged (recommended); or other numbers.

## The owner's answers (2026-11-11)

- **Q1: (a).** The source vouches for the scope, the platform keeps by class, and every download still asks the source. The
  owner was shown (b) with its cost (a revocation would not reach the files already kept) and (c), and chose (a).
- **Q2: the recommended times.** Export files and scheduled snapshots 7 days, issued documents 10 years, working snapshots
  unchanged; a deployment may change the two times.
