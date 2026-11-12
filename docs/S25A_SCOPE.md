# S25-A · Three lifetimes in the core and the stores

2026-11-12. The first slice of ADR 0026 (docs/ADR_0026_RESULT_LIFETIME.md, accepted by the product owner on 2026-11-11). One time,
the source read's expiry (at most 15 minutes), bounded everything the platform made from a read. This slice separates the read,
the scope and the result in the core and in both stores. Nothing on the wire changes: no source of the protocol can declare a
scope yet (S25-B), so in a deployment every result still ends with its read, and with a source that declares none every decision,
every error and every stored encoding is the one it was (A9). The in-memory contract simulator can vouch for a scope, which is
what the tests run against.

## What

A1 · **The read says its scope.** `PreparedRead.ScopeExpiresAt`: until when the source goes on answering `AuthorizeCurrent` for the
read after its pages are gone. Zero means the read's own `ExpiresAt` (`PreparedRead.ScopeEnd`); the field is omitted from the
encoding when zero, so a read without it, and every stored job and snapshot, digests as before. A declared scope is refused when it
is before the read's expiry, outside the years a product's time may name, or a time that cannot be encoded as it is (a year after
9999 in the time's own zone, a zone a day or more from UTC): `ErrIntegrity` from the service, before anything is admitted or read,
and `ErrInvalid` at a store's admission.

A2 · **A job freezes how long its result is kept.** `Job.KeepUntil`, set at admission by `KeepUntil(read, class)`: the read's expiry
plus the class's time, or the read's scope expiry when that is earlier; zero (omitted) when that is the read's own expiry.
`Job.ResultEnd` is the expiry of what the job makes. The classes (`reporting.Retention`, `Config.Retention`):

| Result | Made by | Time beyond the read |
|---|---|---|
| a working snapshot | `SubmitSnapshot` (a person's 创建快照) | none |
| a kept snapshot | `SubmitKeptSnapshot` (the scheduler's run; no route reaches it) | `Results`, default 7 days, 1 minute to 366 days |
| an export file | `SubmitExportWith` | `Results` |
| an issued document's file | `SubmitIssue` | `Issued`, default 10 years, 1 minute to 100 years |

`Repository.Admit` refuses (`ValidateAdmission`) a job whose read declares a scope that is not well formed, a job that keeps beyond
its read's scope, and one whose `KeepUntil` is not after its read's expiry. `ValidatePublication` holds a snapshot's and a file's
expiry to the job's `ResultEnd`, where it held them to the read's.

A3 · **A file stands without its snapshot.** An artifact whose job keeps it beyond the read carries the job's frozen query and read
(`Artifact.Query`, `Artifact.Prepared`; both or neither; `ValidatePublication` holds them to the job's, compared as the stores encode
them). It is made only from the snapshot of that read: the worker refuses (`ErrIntegrity`) a snapshot whose query or read is not the
job's, before anything is rendered and before the product is asked for a document (`sameBasis`), because once the snapshot is gone
the source is asked by the job's copy alone. `Service.Download` of such a file checks: the row is the caller's, the product is
still bound, the file's own expiry is after the read's, not after the read's scope, and has not passed, the source authorizes the
read's scope now, the record of an issued file names it, the bytes match the row. Its snapshot is not read and may be gone. A file
without a basis is served through its snapshot and never later than it, as every file was. What this costs in data: the frozen
query (the companies, the as-of date, the filter values) and the read's references stay with a kept file, and in its job's payload,
for as long as the file is kept, because the source is asked by them; S04-B cleared them a day after the read. No row value is kept
beyond the file itself.

A4 · **Every disclosure asks the source, by the scope.** `Service.currentScope` is `current` for what needs the source's
authorization and not its pages: the read's scope has not passed, then `AuthorizeCurrent`. A page of rows (`Service.snapshot`), a
download, an export's admission and its worker, and the poll of a finished job use it; a refusal is a refusal and a source that
cannot be asked is an error, as before; after the scope the source is not asked and nothing is served. What needs the read itself
keeps `current`: the pages of a freeze (`collect`), and a snapshot job and an issue job while they run (`Service.authorizeJob` asks
by a job's kind and state). What follows from the two:

- `SubmitIssue` refuses (`ErrDenied`) a kept snapshot whose read has expired: the product checks the row against its own frozen
  rows (ADR 0026 decision 6), so a kept snapshot is read and exported after its read, and not issued from. An issue or a freeze
  that no worker reaches while its read lasts is denied, and so is its poll, as before; the product is not asked.
- An export whose worker meets its working snapshot after the read (the scope goes on; the snapshot has ended and may have been
  swept) is denied (`ErrDenied`), as an export whose read had expired always was. It is not an integrity failure of the store.
- A replay of a finished freeze whose result is over is denied (`ErrDenied`), the same before the sweep has reduced the job and
  after. While a kept snapshot lasts, the replay answers its job, asked by the scope.
- An export whose file would already be over is refused at admission (`ErrDenied`) and nothing is admitted: its snapshot was kept
  under a longer `retention.results` than the one in force. One that was admitted in time and that no worker reached until its
  file's time had passed is denied by the worker, before anything is rendered or put in the object store.

A5 · **The stores.** A job has one deadline in a store, and it means two things in turn. While the job can still publish it is the
time after which it may not (`Job.PublishDeadline`): the read's expiry for a snapshot or an issue job, which need the read itself,
and for an export the time its file is kept until. Both stores fence publication by it. Once the job has ended it is the time after
which, and the grace, the sweep may reduce its payload (`Job.ReduceAfter`): the result's expiry when the job succeeded, the read's
expiry when it made nothing. The memory store computes both from the job. In PostgreSQL the column `jobs.source_expires_at` holds
the deadline: `Admit` writes the time the job may publish until; `Publish` sets it to `KeepUntil` when the job keeps its result
beyond the read, in the statement whose condition is the fence, which therefore reads what admission wrote; `Fail` and `Cancel`
bring it back to the read's expiry (`LEAST`: it never moves later; a cancel of a job that has succeeded, or one repeated after the
payload was reduced, leaves it). A job whose payload the store cannot read is failed at its claim without anything of the payload
being trusted, as before; if it is an export that froze a later time, its deadline is brought back to the latest its read can have
expired, `MaxReadLifetime` after the job's admission. For a job that keeps nothing all of these are the read's expiry, written
once, at admission, as before. Two things follow. A job whose file is kept for years is not a candidate of the sweep's reduction
until the file's time has passed (the index `jobs_reducible` is on this column), so the sweep's three statements cost what their
batch handles and not what the kept history holds. And the frozen query of an export that failed or was cancelled goes with its
read's grace, not a results' time later. The assets' `expires_at` column is the result's expiry, and the sweep's statements are
unchanged: an artifact goes with or after its snapshot when both are past the grace, and a kept file outlives its working snapshot
because the snapshot's own expiry is earlier. No migration.

A6 · **The scheduler** freezes a run's snapshot with `SubmitKeptSnapshot`, so a run's snapshot and export are both kept results.

A7 · **Configuration.** `retention.results` and `retention.issued`, in seconds, 0 for the default; out of bounds is refused by
`check-config` and at start (docs/DEPLOYMENT.md). Each is held to its bounds as a number of seconds before it becomes a duration,
and so is `retention.grace` now: 18446744134 seconds overflow a duration and wrap around to about a minute, which the bounds of
the duration accepted. A change of the two times applies to the jobs admitted after it. `Service.Retention` says the times in
force.

A8 · **The contract simulator** (`adapters/contractsim`, in memory): `SetScopeLifetime` makes it vouch for the scope of the reads it
prepares, and `SetReadLifetime` shortens its reads. It serves pages and checks an issue's row only while the read lasts, and
answers `AuthorizeCurrent` while the scope lasts, by the grant the read was frozen on; a read whose pages are over keeps that grant
and lets go of its data version, and the scopes it remembers are bounded (`MaxScopes`). The durable simulator (`pgsource`) is
unchanged and declares no scope until S25-B.

A9 · **A source that declares no scope is served as before, exactly.** Its reads have no scope beyond themselves, so no job freezes
a `KeepUntil`, no file carries a basis, and each scope check is the read check it replaced, value for value. Where the new code
decides something the old code left to a later check, or checks something the old code did not, it does so only for a read whose
scope goes on after it (`PreparedRead.vouched`), for a job that keeps its result, or for a snapshot that is kept: the replay rule,
the denial of an export that meets its ended snapshot or its file's own time, the refusal to issue from a kept snapshot whose read
is over, the snapshot held to the job's basis, the deadline of a job that cannot be read. A file that keeps nothing takes its
snapshot's expiry, as it did (`fileEnd`), so a snapshot row whose expiry is not its read's is still refused at the publication.
The encodings are held literally by a test whose literals were written by the code before this slice (a job, a snapshot and an
artifact without the new fields are the bytes they were), and the first subtest of the lifetime contract runs the whole path on
both stores without a scope.

## Not in this slice

- The source protocol's `scope_expires_at`, the `http` adapter, `sourcetest`, the durable simulator and the checker's rules (S25-B).
  Until then no deployment keeps a result beyond its read. A source that sent the field today would be refused by the `http`
  adapter's strict decoding (`INTEGRITY_FAILURE`), so a product declares it only to a platform that has S25-B.
- **Two things S25-B must bring before a source can vouch**, found while writing and reviewing this slice. ADR 0026's list of slices
  is amended accordingly.
  - *The quota of ADR 0026 decision 7* (an issued file is not a stored result; the default rises from 64 to 512). Without it an
    issued file kept for years holds a slot of `stored_results` for years. And the count must not read payloads to tell an issued
    file from an export: `data ? 'Issue'` would detoast every unexpired artifact row at every admission (the file itself, where no
    object store is configured), and the number of unexpired issued files grows with an identity's history. The count needs a
    partial index that holds the snapshots and export files only, so a migration; today's count is unchanged here, and with no
    result outliving its read it counts what it always did.
  - *The reconciliation of the object store.* Every sweep lists the whole `artifacts/` prefix and looks each page of keys up in the
    rows (S04-E), to find the objects no row names. While a file lived a quarter of an hour and a day of grace, the bucket held a
    day of files. With kept results it holds the exports of a week and the issued documents of ten years, so the listing of every
    sweep would grow with the kept history: a million issued documents are a thousand listings and a thousand lookups a sweep. The
    sweep's three statements on the rows do not grow so (A5). The reconciliation needs a cadence of its own, or keys it can list
    by age.
- **How S25-B is rolled out.** A server of the code before this slice does not know `KeepUntil`, the scope or a file's basis: it
  would publish a kept job's file with the read's expiry and without a basis, which a server of this code then refuses to serve
  while the job's view does not say expired. The two are equivalent only while no source vouches, which S25-A guarantees for
  itself. Every server on a store must run this code before a source of that deployment declares a scope; S25-D writes it into
  docs/OPERATIONS.md.
- `result_expires_at` in the views, and the console's 可下载至 (S25-C). With them, what a workbook's `Metadata` sheet says in
  `expires_at`: today the snapshot's expiry, which for a kept file made from a working snapshot is earlier than the file's own.
- docs/API.md's "Retention", docs/OPERATIONS.md's "Retention" and docs/SECURITY_REVIEW.md as a whole (S25-D). This slice adds the
  two configuration fields to docs/DEPLOYMENT.md and changes no other document: nothing they say is untrue while no source vouches.
- ADR 0026 decision 9's items: a register for the tenant, a product authorizing one document rather than a read's scope, a longer
  read for a long batch.

## Known

- `POST /exports` holds a replay of a request to the checks it makes before it asks the store, as it always did (a replay after
  the snapshot has ended is refused although the job exists). The refusal of an export whose file would be over is one of them: a
  replay made after `retention.results` was shortened that far is refused, and the job it names is still read by its id.
- The two stores differ for a job that cannot be read. The memory store knows the read of a job it holds and reduces a failed one
  with it; PostgreSQL trusts nothing of the payload, so an export's deadline may be up to `MaxReadLifetime` later than its read's
  expiry (A5). A payload whose digest does not match is never reduced, as before (S04-B). The difference keeps data longer, never
  shorter.
- A file's basis is held to its job's when it is published, and its job's to the snapshot when it is made (A3). Nothing holds a
  stored file's basis to its job again at a download: an export's row does not name its job. The row's digest is what protects it
  there, as it protects every row.
- The durable half of the lifetime contract waits on the real clock (reads of six seconds, a scope of fourteen). What it does while
  a read or a scope lasts takes tenths of a second and has more than five seconds to spare; a machine that stalls for longer, or
  whose wall clock is stepped forward by as much, fails a subtest without the code being wrong, and the test says so. On the
  machine this was written on (a virtual machine under time correction) steps of up to three and a half seconds were seen.

## Review

Two independent reviews, both static.

**The first** was started before the fixes listed here. It ran out of resources before it wrote its report, so its working notes
were read as leads, and each was checked against the code and fixed or answered:

1. The sweep would have visited every kept issue job and kept-snapshot job on every run for as long as its file is kept (years),
   because the column stayed at the read's expiry and the job stayed a candidate of the reduction. `Publish` now sets the column
   (A5). This was the one finding of weight; it would have cost nothing until a source could vouch.
2. An export that failed or was cancelled kept its frozen query until the time its file would have been kept until. `Fail` and
   `Cancel` bring the column back; `Job.ReduceAfter` in the memory store (A5).
3. A replay of a finished working freeze answered the job until the sweep reduced it and was denied afterwards. It is denied from
   the read's end (A4).
4. An export could be admitted with a `KeepUntil` already in the past. It is refused at admission (A4).
5. The retention times were multiplied into a duration before their bounds were checked, and the grace always had been (A7).
6. In two race windows a source without a scope would have been answered `ErrDenied` where the old code answered `ErrIntegrity`.
   Both are behind `PreparedRead.vouched` now (A9).
7. A scope in the last hours of 9999 UTC, given in a zone east of Greenwich, passed the check of the years and could not be encoded
   (A1).
8. Comparing a file's basis with its job's panicked on a basis that cannot be encoded. It is `ErrIntegrity` now.
9. The comments of `JobView.Expired`, `IssueView.Expired` and the sweep still said that a result ends with its read.
10. Gaps in the tests: a sweep while an issued file and a kept snapshot are kept; the source's refusals again after the sweep took
    the snapshot; the end of the scope; the store's refusal of a malformed scope on its own, and the service's; the simulator's
    own rules; the slack of the real-clock world.

**The second** read the commit after those fixes, with the question of A9 put to every changed line. Its verdict: nothing that
discloses data it must not, nothing that corrupts state, and no difference for a source that declares no scope, apart from one on
a snapshot row that neither version can write (item 3 below). What it found, and what was done:

1. A job that cannot be read, failed at its claim, kept the deadline its admission wrote: for an export, the time its file would
   have been kept until. Its deadline is brought back without reading the payload (A5).
2. A cancel repeated after the sweep had reduced a cancelled export wrote the zero time into the column. Nothing read it; it is
   left alone now.
3. A file's expiry was taken from the job where the old code took it from the snapshot. The two are equal for every snapshot the
   platform publishes, and differ for a snapshot row whose expiry was changed outside it, which the old code then refused at the
   publication and the new code would not have. `fileEnd` is the old rule for a job that keeps nothing (A9).
4. Nothing held a kept file's basis to the snapshot it was made from. No path through the service could separate them, and the
   worker checks it now (A3).
5. An export that waited past its own time was rendered, and its object put, before the store's fence refused it. The worker denies
   it first (A4).
6. A scope in a zone a day or more from UTC passed, and could not be encoded (A1).
7. The reconciliation of the object store grows with the kept history: not this slice's code, and a condition of S25-B ("Not in
   this slice"). The rollout rule for S25-B is the same review's.
8. Twelve claims no test would have failed for. The tests under "Evidence" answer eleven. The one left: the memory store's
   `ReduceAfter` of a succeeded job is not observable through a sweep (its assets have to be gone first, which implies its time
   has passed), so only the test of the value holds it. And four assertions that could not fail. Two can now: a subtest that does
   not apply to a world is skipped, not passed, and what the simulator lets go of is counted. Two are what they are: the memory
   world has no stored deadline to show, and the test of the encodings looks for keys that some of its values cannot have.
9. The slack of the real-clock world: its reads last six seconds and its scope fourteen (they were four and ten), only what needs
   a read happens while it lasts, a wait looks at the clock again when it ends, and a failure says what kind of test it is
   ("Known").

## Evidence

- `reporting`: `TestAResultIsKeptItsClasssTimeAfterTheReadAndNeverBeyondTheScope` (the scope without and with a declaration, the
  five cases of `KeepUntil`, what each kind of job says of its result and its deadline, when each finished job's payload may be
  reduced, a read's encoding literally), `TestAJobASnapshotAndAnArtifactThatKeepNothingAreEncodedAsBefore` (five stored values
  against literals written by main at bfb0613), `TestTheRetentionTimesAreBounded`,
  `TestAScheduledRunsResultsAreKeptWhenTheSourceVouchesForTheScope` (two hours after the run the owner reads the rows and downloads
  the file; without a scope both are refused as before; a revocation reaches both; eight days later both are over).
- `adapters/contractsim`: `TestTheSimulatorServesPagesWhileTheReadLastsAndAuthorizesWhileTheScopeDoes`, asked of the simulator
  itself: pages and an issue's document while the read lasts, the authorization while the scope lasts (unavailable, revoked,
  narrowed, restored), a read with a scope the simulator did not give, nothing after the scope, and what it remembers (the reads
  within their scope, the data versions of the reads whose pages last).
- The lifetime contract on both stores (`TestMemoryLifetimeContract` on a clock the test moves, and
  `TestDurablePostgresContract/lifetimes` on the real clock). Every sweep in it follows a dry run that must have counted the same.
  - A source that vouches for nothing is served as before: no job freezes a time, no file carries a basis, the store's deadline
    of every job is the read's expiry, everything ends with the read, and the sweep takes it all.
  - An export outlives its read, the working snapshot does not, and every download asks the source once (unavailable, revoked,
    narrowed, restored), and so does the poll of its job, also after the sweep has taken the snapshot; a cancel of the job that
    succeeded changes nothing; at its own time the file is refused without the source being asked, and the sweep takes it and
    reduces its job.
  - An issue's deadline is the read's expiry until it has published and the file's time afterwards. The issued file is kept until
    the scope ends when that is before its ten years, is served under its record after the read and refused when the source
    refuses, a sweep while it is kept reduces nothing of the issue, and its record stays when the file is gone.
  - A kept freeze's deadline is the read's expiry until it has published. Its rows are served after its read, a replay of the
    freeze answers the job, an export of it is admitted and served, an issue from it is refused, and a sweep while it is kept
    takes nothing.
  - An export that is cancelled (once, twice, and again after its payload is gone), and one whose worker meets its ended snapshot
    (denied, not an integrity failure), keep nothing: the store's deadline of each comes back to the read's expiry and the sweep
    reduces both with the read.
  - After the scope the source is not asked, by the worker or by the poll (memory).
  - A replay of a finished working freeze is denied from the read's end, before the sweep and after.
  - An export whose file would be over is refused at admission, and one admitted in time that waited too long is denied before
    anything is rendered (memory).
  - The store refuses a job that keeps beyond its scope, one that keeps no longer than its read, a scope before its read and one
    that cannot be encoded; it holds a publication's expiry and basis to the job's, and refuses a basis that cannot be encoded.
  - A store fences a kept freeze by its read and an export by its file's time, with publications it would otherwise take (memory;
    the durable store's fence is its column against its clock, unchanged, and what the column holds while each kind of job can
    publish is checked in the subtests above).
  - Three jobs the durable store cannot read are failed at their claim: the export that froze a later time has its deadline
    brought back to `MaxReadLifetime` after its admission, and the export without one and the kept freeze keep theirs to the
    microsecond (PostgreSQL).
- `integration`, on the memory store: `TestAScopeThatIsNotWellFormedIsAnIntegrityFailure` (four malformed scopes: nothing
  admitted, no page read); `TestAFreezeAndAnIssueNeedTheReadItselfAlsoUnderAScope` (after the read, under a scope that still
  answers and from a snapshot that is still kept: the three jobs and their polls are denied, no page is read, the product is not
  asked for a document); `TestAKeptFileIsMadeOnlyFromTheSnapshotOfTheReadItsJobFroze` (a store that serves another read's
  snapshot under the id: the export and the issue fail, nothing is rendered, the product is not asked);
  `TestAKeptFileWhoseRowDoesNotAgreeWithItsBasisIsNotServed` (seven rows a store could hold: refused before the source is asked;
  a kept file with no basis at all: refused by the rule of the files that have none);
  `TestAFileOfASnapshotWhoseExpiryIsNotItsReadsIsRefusedAsBefore`; `TestAKeptFilesObjectOutlivesItsSnapshotAndGoesWithItsRow`
  (with an object store: the object survives the sweep that takes the snapshot, is read back by the source's answer, and goes
  with its row).
- `server`: the two fields' defaults and values, seven refusals of which three are numbers that wrap around; the service of a
  started server keeps results for the times its file says; DEPLOYMENT's reference lists `retention.results`.
- Mutations: 28 changes that each break one rule of this slice (a download that does not ask, a fence by the wrong time, a
  deadline that is not brought back, a basis that is not checked, an encoding that says a new field, and so on) were made one at a
  time in a copy of the tree; for each, the test named for the rule passes unchanged and fails with the rule broken.
- Every test that existed passes, and none was changed in what it asserts. Without a database: 677 top-level tests in 34 packages,
  plain and under the race detector. Against PostgreSQL 16: 688, plain and under the race detector. The object-store subtests of
  the durable store and the Typst tests were not run locally; they run in CI.
