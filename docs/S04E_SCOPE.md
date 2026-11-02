# S04-E object storage for artifacts

2026-11-01. ADR 0007 (docs/ADR_0007_OBJECT_STORAGE.md) decides: artifact content leaves the row for an S3-compatible
object store behind a client the platform writes; the row keeps the metadata and the digest; the object is written
before the row and reconciled when the row never came; reads check the row first and the bytes against it. This
slice implements it; the memory store and the fixture path without an object store keep working unchanged.

E1 · The client: package `storage/objects`. `objects.S3{Endpoint, Bucket, Region, AccessKey, SecretKey, CA, LoopbackHTTP}`
with `Put(ctx, key, content, sha256)`, `Get(ctx, key, sha256, length) ([]byte, error)` (refuses a body of another
length or digest, reads at most MaxArtifactBytes), `Delete(ctx, key)`, `List(ctx, prefix, after string, limit int)
([]Object{Key, LastModified}, next string, error)` (ListObjectsV2 with continuation), `Check(ctx)` (HEAD bucket).
SigV4 over `net/http` with the standard library only; path-style; `x-amz-content-sha256` on every request; the host
header, the canonical query and the signed headers exactly per the specification (test against the specification's
published example signatures); a 10 s timeout per request and at most one retry of an idempotent request on a
network error; the endpoint is `https://` (system roots or the CA file) or `http://` on a loopback IP with
`LoopbackHTTP`; anything else is refused at construction. Errors never carry the response body, the key or the
credentials: an unexpected status is `ErrUnavailable` with the status code only. `objects.Memory` (a map) is the
second implementation, for the memory store's tests of the service path and for the PostgreSQL store's tests that
do not need MinIO. Both satisfy `reporting.ObjectStore`.

E2 · The store. `postgres.Open`/`OpenProduction` take an optional `reporting.ObjectStore`; with one, `Publish` requires
the artifact's content to be already stored (the worker PUTs first: `Service` does it in `execute` before calling
`Publish`, under the job's claim) and stores the row with `Object` set and `Content` empty; the payload digest is over
that payload. `Artifact.Object string` is a new field (`json:",omitempty"`); `Content` stays, empty when `Object` is
set. Reading (`Artifact`, the download path) returns the row; `Service.Download` fetches the object through the
store when `Object` is set and checks it (E4). A row with both or neither of `Object` and `Content` is ErrIntegrity.
The memory store keeps content inline whatever is configured (it is the explicit ephemeral store).

E3 · The sweep. After each batch that deleted artifact rows commits, the sweep deletes the objects those rows named
(their keys are read in the batch's `RETURNING`); a delete that fails is logged as a count and left for
reconciliation. Reconciliation, a fourth step of the sweep when an object store is configured: list `artifacts/`
in pages, and delete every object whose id names no `assets` row (any kind; a lookup per page with `= ANY($1)`,
under the worker scope) and whose last-modified time is older than the grace by the platform's clock (the only
place the host's clock is used, because the object store's clock is the one that stamped the object; say so). The
ledger row gains `objects` and `orphans` counts (migration 008 adds the two columns, default 0, to `sweeps`; its down
drops them; catalog_008). `reporting-migrate sweep` takes the object store flags and environment; without them it
deletes rows only, prints that the objects wait for a reconciliation, and exits 0.

E4 · Reads and failures. A download of an artifact with `Object`: row first (as today), then `Get` with the row's
SHA-256 and length; a mismatch or a missing object is ErrIntegrity (409 INTEGRITY_FAILURE); an object store that
cannot be reached is `ErrUnavailable` (503 UNAVAILABLE, a new mapping if none exists; `Retry-After: 10`). An export
or issue job whose PUT fails fails the job with EXECUTION_FAILED (an issue is voided as any failure, S03-H); nothing
is published. The job view is unchanged. The console needs no change beyond handling 503 on a download as it
handles UNAVAILABLE elsewhere (check; add a DOM test if a path is new).

E5 · Configuration. The demo: `-object-store-endpoint`, `-object-store-bucket`, `-object-store-region` (default
`us-east-1`), `-object-store-ca`, `-object-store-loopback-http`; `REPORTING_OBJECT_STORE_ACCESS_KEY` and
`REPORTING_OBJECT_STORE_SECRET_KEY` from the environment (a flag for either is refused). At startup `Check` must
pass or the server refuses to start with a text that names the bucket and the status, never the credentials.
`reporting-migrate sweep` takes the same. `GET /capabilities` does not say where files live.

E6 · Qualification. Unit: SigV4 against the specification's example (the "GET Object" and "PUT Object" examples of
the AWS signature test suite with their published signatures), the canonicalization of keys and query strings, the
refusals of construction, the length and digest checks of `Get`, the error texts carrying no body, key or secret.
Memory object store: the service path (export and issue jobs put the object, publish the row with `Object`,
download checks, a corrupted object is 409, a removed object is 409, the sweep deletes objects and counts, the
reconciliation deletes an orphan older than the grace and keeps a younger one and one a row names). MinIO: the same
through `objects.S3` against a throwaway MinIO when `REPORTING_OBJECT_STORE_TEST_ENDPOINT`,
`REPORTING_OBJECT_STORE_TEST_ACCESS_KEY` and `REPORTING_OBJECT_STORE_TEST_SECRET_KEY` are set (the test creates the
bucket `reporting-test-s04e` with a PUT bucket request if it is missing; this is the one bucket-level call, in the
test only), gated like the PostgreSQL tests (`REPORTING_OBJECT_STORE_REQUIRE=1` makes a missing endpoint a failure),
as a subtest of `TestDurablePostgresContract` where the store is involved and a top-level `TestS3Objects` for the
client alone (into both go-job skip lists of ci.yml, and run in the postgres job, which gains a MinIO service
container with pinned image digest, as PostgreSQL's). The worker's PUT-then-publish ordering is proved by a failure
injected between them (the object is an orphan, the row absent, the reconciliation removes it after the grace).
The whole existing suites keep passing with no object store configured.

E7 · Docs. OPERATIONS.md "Object storage" (what moves and what does not, the policy the credentials need, the
startup check, the sweep and the reconciliation, the operator command, the verification: a count of rows with
`Object` against a count of objects, orphans), API.md (503 on a download, the unchanged views), README S04-E at the
top with the "以下为此前阶段历史说明：" line moved directly below it.

Not in this slice: moving existing inline artifacts, snapshots in the object store, server-side encryption, bucket
lifecycle rules, multipart uploads, AWS S3 qualification by test, backups, capacity, key lifecycle.
