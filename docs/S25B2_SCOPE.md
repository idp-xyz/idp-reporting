# S25-B2 · The scope on the wire

2026-11-12. The third slice of ADR 0026 (docs/ADR_0026_RESULT_LIFETIME.md) and the second half of what its list calls S25-B. S25-A
separated the read, the scope and the result in the core and the stores (docs/S25A_SCOPE.md); S25-B1 made the store ready for results
that are kept (docs/S25B1_SCOPE.md). Until now only a contract simulator in the process could vouch for a scope, so no deployment kept
anything beyond a read. This slice lets a source product say it over the protocol: a read's optional `scope_expires_at`, the adapter
`http` that verifies it, a connection's declaration that the platform keeps results, the reference source, the durable simulator and
the checker's rules.

What a deployment sees of this slice:

- nothing, until an operator declares `kept_results: true` on a connection **and** that product answers its reads with
  `scope_expires_at`. Then an export file and a scheduled run's rows outlive the read by `retention.results` and an issued
  document's file by `retention.issued`, never beyond the time the product vouched for, and every download still asks the product;
- a product that vouches before the operator has declared has the reads that vouch refused (409 `INTEGRITY_FAILURE`, previews
  included) until the declaration is made;
- a time of the protocol written with an offset of a day or more (`+24:00` to `+24:59`, either sign) is refused where it is read;
  before, a read that expired at such a time was taken and failed where the first job made from it was stored (B6);
- `reporting-source-check` prints 34 rules, the two new ones last, skipped without `-kept-results`.

## What

B1 · **The read carries the scope** (ADR 0026 decision 2). `protocol.Read` gains `scope_expires_at`, optional, a time in the
protocol's canonical form; the protocol's revision is the one after S24-A's (the number is in the title of
docs/SOURCE_HTTP_PROTOCOL.md, in docs/source-openapi.json and in `protocol.Revision`, held together by
`TestTheOpenAPIDescriptionIsTheProtocol`; ADR 0026 said 1.2, which S24-A took first).

- A read without a scope is on the wire what it was, byte for byte, and its scope ends with it: a source of an earlier revision is
  served unchanged. A read that vouches is that read with one more key.
- The key is absent or a time: `null`, an empty text, a time that is not canonical and the zero time are no read at all. The field is
  a pointer in Go so that the key with an empty text is not taken for its absence, and the zero time is refused because a prepared
  read cannot tell it from none and would send the read back without the key, as another read than the source answered.
- The platform sends the read back with the scope exactly as the source gave it, in every later request about the read
  (`authorize`, `pages`, a document's `prepare`), also when the scope ends with the read and vouches for nothing.

B2 · **The adapter `http` verifies the scope, and takes it only from a connection that declares kept results.**

- *What verifies.* `PrepareRead` holds the scope to `reporting.PreparedRead.ValidLifetimes` (exported for it; the service and the
  stores already held every read to it): not before the read's own expiry, not after `9999-12-31T23:59:59Z`. A read that fails is
  an integrity failure and nothing of it is passed on. A read with a malformed scope is not sent back either (`sendable`).
- *The declaration.* `httpsource.Config.KeptResults`, a connection's option `kept_results` (docs/DEPLOYMENT.md). Without it a read
  that vouches for a scope **beyond its own expiry** does not verify (`INTEGRITY_FAILURE`; the adapter's words name the option).
  With it, a source that vouches for nothing is served as before. A scope that ends with the read vouches for nothing more than
  every read does and is taken from any connection. The scope is judged before the declaration is asked for, so a scope the
  platform takes from nobody is refused in words about the scope.
- *Why a declaration.* ADR 0026's decision 2, as it was accepted, did not ask for one: by its words a source that sends the field
  is taken. This is a contract-level matter (the source protocol, and what the platform stores of business data and for how
  long), so it was put to the product owner, who chose the declaration (ADR 0026, Q3, 2026-11-12). The reasons: that the
  platform holds a product's data for days and years after a read is the operator's to declare: the storage, the backups and the
  two retention times are the operator's, and a product's deployment should not turn it on by itself. It is of a kind with the
  protocol's rule that a source gains nothing until its connection declares, and sharper: an optional route is not asked of a
  connection that does not declare, while a field of an answer cannot be left unasked, so the read that carries it is refused.
  And it is the choice that can be undone: the requirement can be lifted in a later release without breaking a product, while
  introducing it later would refuse the reads of every product that already vouched.
  What it costs is an order: the operator declares first, the product vouches afterwards; the reverse refuses the reads that
  vouch until the declaration is made, loudly and without keeping anything. docs/DEPLOYMENT.md "Keeping results: the order" has
  the steps, for a new connection and for one in use, and the way back.
- *What the declaration does not decide.* Which reads are sent back. A read the adapter took is sent back as it is whatever the
  connection declares now, on every route, so that what the platform already keeps is still authorized by the source after the
  option is gone.
- An adapter in the process is not asked for a declaration: it says a scope by returning one, as the simulators do.

B3 · **The reference source and the composition of the tests.** `sourcetest` writes the scope the wrapped adapter's read declares
and passes a read that comes back to the adapter with the scope the request carries; whether that is the read the source made, and
until when it is authorized, is the adapter's decision by its own record. `sourcetest.Remote` declares kept results, so that a
simulator behind the protocol is taken as it is taken in process.

B4 · **The checker** (`adapters/httpsource/conformance`, `cmd/reporting-source-check -kept-results`): two rules, last, 34 in all.
They only read.

- K1: the sample read vouches for a scope, its `scope_expires_at` after its `expires_at` (that it is well formed the adapter
  verified at R1). A scope that ends with the read vouches for nothing and fails it, like no scope at all.
- K2: the source holds the scope's end itself. The read as the source gave it is authorized; with another scope expiry, and without
  one, it is refused, as a read with another watermark is; and as the source gave it, it is authorized again. A source that
  passed its own time back unchecked would authorize a read for as long as whoever sends it says. The unchanged read is asked
  for before the two altered ones and after them, and the rule fails, saying which, when either is refused: a read that ran out
  meanwhile would refuse the altered ones for that reason, and the rule would pass on refusals that show nothing.
- The other scope K2 asks with lies as far ahead of the clock as the source's own, so that a source which takes the scope's end
  from the request goes on saying yes to it and is found out: an hour later, or, for a scope at the end of the protocol's times
  (what a product that vouches "for ever" answers), an hour earlier.
- Without the flag the two are skipped, and a source that vouches fails R1 with the adapter's words, as through a connection that
  does not declare: the flag is the product saying that it vouches. With the flag, a source that vouches for nothing fails K1.
- What the checker cannot see is the promise itself, that the source goes on authorizing after the read has expired and refuses
  after the scope: a read lasts up to a quarter of an hour and a scope may last years. The product shows it with a test of its
  own; docs/REAL_SOURCE_ONBOARDING.md lists what that test shows (ADR 0026 "Consequences"), and the command's help says so.

B5 · **The durable simulator vouches** (`adapters/contractsim/pgsource`). `SetReadLifetime` and `SetScopeLifetime`, as the in-memory
simulator's: a read's pages and a document's row check last while the read does, the authorization while the scope does, by the
database's clock and by the grant of the moment. The scope's end is in the read's stored row, so the source keeps its word over a
reopening; the two settings are the opened value's own and are the defaults again after one. No change of the simulator's schema:
the stored read already was the JSON of the read, and the read's own expiry stays the column.

A read that is not the durable simulator's own (it comes back with another scope, or without the one it was given, or with any
other field changed) is `ErrDenied` now, as the in-memory simulator's always was and as the protocol's table says. It was
`ErrIntegrity`, together with a stored row that does not agree with itself; the two are kept apart, and the second stays
`ErrIntegrity`. Behind the reference source the durable simulator would have answered `integrity` where the checker's R7 and R8,
and now K2, want `denied`. The scenario of the document authority that both simulators run holds them to the same answer now.

B6 · **A protocol time is one a store can keep.** `protocol.ParseTime` accepted a time written with an offset of a day or more
(`+24:00` to `+24:59`, either sign): Go's parser reads it and writes it back, so it passed the canonical test. RFC 3339 has no such
offset and JSON cannot write it, so a read that expired at such a time was taken, a preview of it worked, and the first job made
from it failed at its admission with an error of no code (found while writing the scope's verification; reproduced on main at
efb79dc). It is refused where it is read now, for every time of the protocol.

B7 · **A poll at the instant a result ends.** `Service.Job` looks at the clock more than once: first to see whether the job's
result is over, then, after the source has been asked, when it reads the result's row. When the result's time lies between two of
those looks, the first said that the result lasts and a later one refused it as a result that has ended: a working freeze polled
at its read's expiry, and a kept file polled at its own time, answered 409 `INTEGRITY_FAILURE`, and a result kept to the end of
its scope 403. Such a poll now looks at the clock once more and, when the result is over, answers what a poll a moment later
answers, the job with `expired: true`. This is asked only of a read whose scope goes on after it: under a source that vouches for
nothing the poll at that instant answers what it always did (S25-A's rule A9). Found by this slice's server test, which met the
instant on a real clock that had just been stepped by nearly the read's whole lifetime.

## Not in this slice

- `result_expires_at` in the views, the console's 可下载至, what a workbook's `Metadata` sheet says in `expires_at`, and a demo that
  vouches (S25-C). Until then a kept file is served and its job's view says `expired: false`, and nothing says until when; and
  the administrator's view of a connection names its adapter and does not say that it keeps results.
- docs/API.md's and docs/OPERATIONS.md's "Retention" and docs/SECURITY_REVIEW.md's section on kept data (S25-D), and with them a
  way to remove kept results before their time: there is none but the product's (a revocation stops the downloads at once; the
  rows and files stay until their time). They describe a deployment in which no connection declares `kept_results`, which is every
  deployment until an operator declares it. This slice changes the documents of the wire and of the option: SOURCE_HTTP_PROTOCOL,
  source-openapi, DEPLOYMENT (the option, the order in which it is turned on and off, the checklist's item 20), OPERATIONS (the
  order of a rollout and the way back; the sentences on quotas and on the cursor keys that the option makes incomplete),
  REAL_SOURCE_ONBOARDING (what a product that vouches must do and show), the baseline's two rows of the adapter contract, and
  one clause of SECURITY_REVIEW's list of what the adapter verifies.
- A way for the platform to say at `reads` what a read is for. A source vouches before it can know: a preview's read is vouched for
  like a freeze's (see "Known").
- ADR 0026 decision 9's items: a register for the tenant, a product authorizing one document rather than a read's scope, a longer
  read for a long batch.
- The durable simulator behind the reference source in a test of its own. No test runs that composition; this slice removes the
  one difference that was known to fail it (B5).

## Known

- **What vouching costs a source.** A source that keeps a record per read keeps one for every read it answers, previews included,
  until the scope ends; and for an issued document's file to be kept its ten years, the source vouches for ten years. A handle
  that describes itself (and that the source can tell is its own) costs nothing to keep. The protocol and the onboarding guide say
  so; it is the first thing a real product's design has to settle.
- **Issued documents and narrowed rights.** The platform keeps an issued file until the scope ends and serves it while the
  issuer's current rights cover the whole scope of the read it came from (decision 4). A person whose rights have since narrowed
  cannot download a document they issued. Decision 9 names the refinement (a product authorizing one document); nothing here
  changes it.
- **The option and the migration step share a name.** `kept_results` is a connection's option (this slice) and the step
  `015_kept_results` (S25-B1). The documents say which wherever both could be meant.
- **One test waits on the real clock**: the server's, with a read of six seconds, of which the freeze, the export and the first
  download need tenths of one. A machine that stalls for longer in that stretch, or whose wall clock is stepped by as much, fails
  it without the code being wrong. On the machine this was written on (a virtual machine under time correction) a step of 2.95
  seconds was measured within forty seconds; the read is six seconds for that reason. The durable simulator's scenario does not
  wait: it moves the times of the source's own record into the past.
- **A way back to a binary before this slice**, once results are kept, loses the use of them for as long as it lasts and nothing
  else (docs/OPERATIONS.md "Workers and issue jobs"): the product stops vouching first.
- **A cursor into a kept snapshot** lives as long as the snapshot, days where a cursor lived a quarter of an hour; the rotation of
  the cursor keys says what that changes (docs/OPERATIONS.md "The cursor key ring").

## Review

Three independent readings were started on the commit before the changes listed here (the code, the tests, the documents against
the code). All three ran out of resources before they had written a report; their notes and what they had worked out were read as
leads, and each was checked against the code and fixed or answered. None found a way to disclose what should not be, or a read
that changes on its way through the platform.

1. *The contract-level question.* The documents reader found that the slice had added a condition to an accepted decision (a
   connection must declare) and called it a detail the text had left open. It was put to the product owner before the merge, who
   chose it; the ADR records the question, the answer and what the condition changes (B2).
2. *K2 could pass on a read that ran out.* The rule asked for the unchanged read first and then for the two altered ones; a read
   that ran out between the first ask and the second refused them for that reason and the rule passed. It asks for the unchanged
   read again at the end (B4).
3. *K2's other scope for a scope at the end of time* was the read's own expiry, which a source that trusts the request refuses
   once the read is over: a false pass in the same window. It is an hour earlier now, ahead of the clock (B4). No test ran that
   branch; three products that vouch "for ever" do: a correct one, one that trusts the request, and one that trusts the request
   and whose read has run out by the time K2 asks.
4. *K1's boundary.* Nothing tested that a scope which ends with the read fails K1, or that such a read goes back to the source with
   its key. A product that writes the key on every read and holds the read to it does both (the checker passes it without the
   flag and fails it at K1 with it), and the wire's test writes and reads such a read.
5. *The durable simulator answered `integrity` for a read that is not its own*, where the protocol says `denied`, and the
   onboarding guide named its scenario as a sample of a product's own test. It answers `denied` (B5).
6. *The durable scenario waited on the real clock*, a read of two seconds and a scope of six, and would have failed on a machine
   that stalls or steps its clock by seconds. It moves the record's times instead and waits for nothing. One of two mutations
   the first run had not caught was a gap of that scenario (a confirmed document asked for again after its read); the other is
   not observable (the scope's end read from the request after the request has been compared with the record).
7. *A test that could not fail*: that the two rules "reserve nothing" was asked of a run without the document authority, which
   cannot reserve. It is asked of the run with every flag: the product's next number is the one it would be without the two rules.
8. *What the tests did not pin*: the words of the scope's refusals and the order of the two new checks; a document's `prepare`
   through a connection that no longer declares; the two other ways a scope is malformed, on the routes that send a read back;
   that the check's own declaration is the target's and not the configuration's. Each has its assertion now.
9. *The documents.* The order in which the option and the vouching are turned on was said two ways (declare after the check
   passes; declare before the product vouches) and could not be followed for a connection in use: the steps are written out, with
   the test system first (docs/DEPLOYMENT.md). That declaring is a rolled restart, what a rollback of the binary does to kept
   results, how a product stops vouching, that the retention times are the deployment's and not a connection's, that the option
   and the migration step share a name, that an undeclared connection refuses the reads that vouch and not "every read that
   carries the field", that the comparison with an optional route is not exact, and that an offset of a day or more is sixty
   texts a sign and not one: each is said where it belongs. Two sentences of OPERATIONS that the option makes incomplete (the
   quota's, the cursor keys') are amended.
10. *A comment* said the poll's error stands "in every other case"; it is replaced whenever the result is over by the time the
    check has failed, whatever the check said. The comment says so.

## Evidence

- `adapters/httpsource/protocol`: `TestAReadIsWrittenAsBeforeAndWithItsScopeWhenItVouchesForOne` (a read without a scope against the
  literal main writes for it; with a scope, and with one that ends with the read, the same text and one more key; each read back,
  sent back and sent back from a store's encoding as the text it came as), `TestAScopeExpiryThatIsPresentIsATime` (eleven things
  under the key that are no time), `TestAProtocolTimeIsCanonicalAndOneAStoreCanKeep` (nine times that are read as themselves and
  can be stored; thirty-one texts that are not read, the offsets of a day or more among them).
- `adapters/httpsource`: `TestAReadCarriesTheScopeTheSourceVouchesForAndComesBackUnchanged` (the nine keys of a read and the ten
  of one that vouches, in the answer and in the three requests that send it back; the source's refusal of another scope and of
  none; three malformed scopes on three routes, none sent), `TestAScopeThatDoesNotVerifyIsAnIntegrityFailure` (eleven answers
  refused, each in the adapter's words for its reason, nothing of the read passed on; three taken),
  `TestAScopeIsTakenOnlyFromAConnectionThatDeclaresKeptResults` (the refusal and its words; the read sent back on the three routes
  by a connection that would not take it; the scope judged before the declaration; a scope that ends with the read taken
  undeclared; a source that vouches for nothing served by both), `TestTheConnectionDeclaresKeptResultsInItsOptions`,
  `TestTheOpenAPIDescriptionIsTheProtocol`.
- `adapters/httpsource/conformance`: `TestTheTargetDecidesWhetherTheCheckDeclaresKeptResults`.
- `cmd/reporting-source-check`: the reference source passes the 34 rules with every flag and gives out no number for the two new
  ones; without the flag it fails R1 in the adapter's words and stops; a source that vouches for nothing, and one whose scope
  ends with its read, fail K1 (the second passes every rule without the flag); a product that vouches for ever passes, and one
  that takes the scope from the request fails K2 there too, also when its read has run out; two products that take the scope
  from the request fail K2, each in the words of the read it accepted; a read that runs out before K2 or during it fails K2,
  saying where, and one that lasts passes.
- The lifetime contract with the simulator behind the protocol (`TestMemoryLifetimeContractOverTheProtocol`): the twelve subtests
  that apply to the memory store, as in process.
- `integration`: `TestAPollAtTheInstantAResultEndsSaysThatItIsOver` (a working freeze at its read's expiry, a kept export at its
  own time and at the end of its scope; under a source that vouches for nothing the poll answers as before).
- `adapters/contractsim/pgsource`: the scenario `a_read_serves_its_pages_while_it_lasts_and_is_authorized_while_its_scope_does`
  (the settings; the stored scope and the column; pages, a reserved and a confirmed document refused after the read; the
  authorization under an unavailable source, a revoked, a narrowed and a restored grant; another scope, no scope and another
  watermark refused as not the source's read, and a record that does not agree with itself as an integrity failure; a reopened
  source; the scope's end). The shared scenario of the document authority holds both simulators to `ErrDenied` for a forged read.
- `server`: `TestAConnectionThatDeclaresKeptResultsServesAnExportAfterItsRead` (the configuration file, the shipped registry, the
  protocol: the export downloaded after its read, refused when the source revokes, answers unavailable or is gone, its working
  snapshot ended), `TestAConnectionThatDoesNotDeclareKeptResultsTakesNoVouchedRead`; DEPLOYMENT's table is held to the option.
- Mutations: 65 changes that each break one rule of this slice, made one at a time in a copy of the tree; for each, the test
  named for the rule passes unchanged and fails with the rule broken. One more was tried and is not counted, because no caller
  can see it: the durable simulator reading the scope's end from the read that came back, after it has compared that read with
  its record.
- Every test that existed passes. One was changed in what it asserts: the shared scenario of a forged read, which accepted
  whatever error each simulator gave and asks for `ErrDenied` of both (B5). Without a database: 728 top-level tests in 36
  packages, plain and under the race detector. Against PostgreSQL 16 with the S3-compatible server: 740, plain and under the
  race detector. The Typst tests run in CI.
