# S25-A · Three lifetimes in the core and the stores

2026-11-11. The first slice of ADR 0026 (docs/ADR_0026_RESULT_LIFETIME.md, accepted by the product owner that day). One time, the
source read's expiry (at most 15 minutes), bounded everything the platform made from a read. This slice separates the read, the
scope and the result in the core and in both stores. Nothing on the wire changes: no source of the protocol can declare a scope yet
(S25-B), so in a deployment every result still ends with its read, and with a source that declares none every stored value has
the encoding it had. The in-memory contract simulator can vouch for a scope, which is what the tests run against.

## What

A1 · **The read says its scope.** `PreparedRead.ScopeExpiresAt`: until when the source goes on answering `AuthorizeCurrent` for the
read after its pages are gone. Zero means the read's own `ExpiresAt` (`PreparedRead.ScopeEnd`); the field is omitted from the
encoding when zero, so a read without it, and every stored job and snapshot, digests as before. A declared scope before the read's
expiry, or outside the years a product's time may name, is refused (`ErrIntegrity` from the service, `ErrInvalid` at admission).

A2 · **A job freezes how long its result is kept.** `Job.KeepUntil`, set at admission by `KeepUntil(read, class)`: the read's expiry
plus the class's time, or the read's scope expiry when that is earlier; zero (omitted) when that is the read's own expiry.
`Job.ResultEnd` is the expiry of what the job makes. The classes (`reporting.Retention`, `Config.Retention`):

| Result | Made by | Time beyond the read |
|---|---|---|
| a working snapshot | `SubmitSnapshot` (a person's 创建快照) | none |
| a kept snapshot | `SubmitKeptSnapshot` (the scheduler's run; no route reaches it) | `Results`, default 7 days, 1 minute to 366 days |
| an export file | `SubmitExportWith` | `Results` |
| an issued document's file | `SubmitIssue` | `Issued`, default 10 years, 1 minute to 100 years |

`Repository.Admit` refuses (`ValidateAdmission`) a job that keeps beyond its read's scope, or whose `KeepUntil` is not after its
read's expiry. `ValidatePublication` holds a snapshot's and a file's expiry to the job's `ResultEnd`, where it held them to the
read's.

A3 · **A file stands without its snapshot.** An artifact whose job keeps it beyond the read carries the job's frozen query and read
(`Artifact.Query`, `Artifact.Prepared`; both or neither; `ValidatePublication` holds them to the job's). `Service.Download` of such a
file checks: the row is the caller's, the product is still bound, the file's own expiry lies between the read's and the read's scope
and has not passed, the source authorizes the read's scope now, the record of an issued file names it, the bytes match the row. Its
snapshot is not read and may be gone. A file without a basis is served through its snapshot and never later than it, as every file
was. What this costs in data: the frozen query (the companies, the as-of date, the filter values) and the read's references stay
with a kept file, and in its job's payload, for as long as the file is kept, because the source is asked by them; S04-B cleared
them a day after the read. No row value is kept beyond the file itself.

A4 · **Every disclosure asks the source, by the scope.** `Service.currentScope` is `current` for what needs the source's
authorization and not its pages: the read's scope has not passed, then `AuthorizeCurrent`. A page of rows (`Service.snapshot`), a
download, an export's admission and its worker, and the poll of a finished job use it; a refusal is a refusal and a source that
cannot be asked is an error, as before. What needs the read itself keeps `current`: the pages of a freeze (`collect`), a snapshot
job and an issue job while they run. `SubmitIssue` refuses (`ErrDenied`) a snapshot whose read has expired: the product checks the
row against its own frozen rows (ADR 0026 decision 6), so a kept snapshot is read and exported after its read, and not issued from.

A5 · **The stores.** A job's deadline (`Job.PublishDeadline`) is the read's expiry for a snapshot or an issue job and the file's
expiry for an export. Both stores fence publication by it and reduce a finished job's payload once it is past the grace; the
PostgreSQL column `jobs.source_expires_at` holds it, so for every job that keeps nothing its value is what it was. The assets'
`expires_at` column is the result's expiry, and the sweep's statements are unchanged: an artifact goes with or after its snapshot
when both are past the grace, and a kept file outlives its working snapshot because the snapshot's own expiry is earlier. No
migration.

A6 · **The scheduler** freezes a run's snapshot with `SubmitKeptSnapshot`, so a run's snapshot and export are both kept results.

A7 · **Configuration.** `retention.results` and `retention.issued`, in seconds, 0 for the default; out of bounds is refused by
`check-config` and at start (docs/DEPLOYMENT.md). A change applies to the jobs admitted after it.

A8 · **The contract simulator** (`adapters/contractsim`, in memory): `SetScopeLifetime` makes it vouch for the scope of the reads it
prepares, and `SetReadLifetime` shortens its reads. It serves pages and checks an issue's row only while the read lasts, and
answers `AuthorizeCurrent` while the scope lasts, by the grant the read was frozen on; a read whose pages are over keeps that grant
and lets go of its data version. The durable simulator (`pgsource`) is unchanged and declares no scope until S25-B.

## Not in this slice

- The source protocol's `scope_expires_at`, the `http` adapter, `sourcetest`, the durable simulator and the checker's rules (S25-B).
  Until then no deployment keeps a result beyond its read.
- **The quota of ADR 0026 decision 7** (an issued file is not a stored result; the default rises from 64 to 512). It belongs with
  S25-B and must land before a source can vouch, for two reasons found while writing this slice. Without it an issued file kept
  for years holds a slot of `stored_results` for years. And the count must not read payloads to tell an issued file from an
  export: `data ? 'Issue'` would detoast every unexpired artifact row at every admission (the file itself, where no object store
  is configured), and the number of unexpired issued files grows with an identity's history. The count needs a partial index
  that holds the snapshots and export files only, so a migration; today's count is unchanged here, and with no result outliving
  its read it counts what it always did.
- `result_expires_at` in the views, and the console's 可下载至 (S25-C).
- docs/API.md's "Retention", docs/OPERATIONS.md's "Retention" and docs/SECURITY_REVIEW.md as a whole (S25-D). This slice adds the
  two configuration fields to docs/DEPLOYMENT.md and changes no other document: nothing they say is untrue while no source vouches.
- ADR 0026 decision 9's items: a register for the tenant, a product authorizing one document rather than a read's scope, a longer
  read for a long batch.

## Evidence

- `reporting`: `TestAResultIsKeptItsClasssTimeAfterTheReadAndNeverBeyondTheScope` (the scope without and with a declaration, the
  five cases of `KeepUntil`, what each kind of job says of its result and its deadline, the encodings unchanged without a scope),
  `TestTheRetentionTimesAreBounded`, `TestAScheduledRunsResultsAreKeptWhenTheSourceVouchesForTheScope` (two hours after the run
  the owner reads the rows and downloads the file; without a scope both are refused as before; a revocation reaches both; eight
  days later both are over).
- The lifetime contract on both stores (`TestMemoryLifetimeContract`, and `TestDurablePostgresContract/lifetimes` on the real
  clock with reads of three seconds): a source that vouches for nothing is served as before; an export outlives its read, the
  working snapshot does not, and every download asks the source (unavailable, revoked, narrowed, restored), also once the sweep
  has taken the snapshot; an issued file is kept until the scope ends when that is before its ten years, is served under its
  record after the read, and is refused after a revocation while the record stays; a kept snapshot's rows are served after its
  read, an export of it is admitted and served, an issue from it is refused; an export whose worker meets its working snapshot
  after the read is denied, not an integrity failure; the store refuses a job that keeps beyond its scope or declares a scope
  before its read, and a publication whose expiry or basis is not the job's.
- `server`: the two fields' defaults, values and four refusals; DEPLOYMENT's reference lists `retention.results`.
- Every test that existed passes unchanged, without a database (644 top-level tests, plain and race) and against PostgreSQL 16
  (655; the object-store subtests and the Typst tests were not run locally and run in CI).
