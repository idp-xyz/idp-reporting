# ADR 0007 · Object storage for artifacts

Date: 2026-11-01. Status: accepted. Implementation: S04-E.

## Context

An artifact (an XLSX or PDF export, an issued PDF) is stored today as bytes inside the JSON payload of its row in
`assets` (base64 in JSONB, up to MaxArtifactBytes = 8 MiB), next to the snapshot rows and the job rows. That was
right for S01–S03: one store, one transaction, one digest over the whole payload. It is wrong for production: the
database holds the files (backups, replication and vacuum carry megabytes of PDFs; a database sized for rows is
sized for files), every download reads a whole payload through the row store, and the capacity and backup slices
that follow S04 need the files out of the row store first. docs/s00/MIGRATION.md lists "私有对象存储" for S04.

What must hold: the row stays the authority (who owns the artifact, when it expires, its SHA-256 and length, which
issue record it was issued under); every download is authorized by the source product and checked against the
row's digest exactly as today; row-level security (S04-C) and retention (S04-B) keep applying; the formal
documents' bytes are the same whatever store holds them; no third-party dependency is added without a reason the
platform can review; the memory store and the fixture path keep working without any object store.

## Decision

1. **Content leaves the row; metadata stays.** With an object store configured, a new artifact's row carries the
   metadata and an object key (`Artifact.Object`, `artifacts/<artifact id>`; ids are random, so a key says nothing)
   and no `Content`; the payload digest covers the metadata. The content is the object, and the row's SHA-256
   and length are what the object must match. An artifact stored before, with its content inline, stays inline
   and is served as today until it expires and the sweep deletes it: there is no bulk move. Snapshots (business
   rows, bounded by MaxResultBytes) stay in the row store: they are read page by page under the source product's
   authority, which the row store does best; this ADR is about files.
2. **An S3-compatible object store, through a client the platform writes.** The store is any service that
   speaks the S3 API (MinIO, AWS S3, the others), path-style, one bucket, SigV4. The client is written in the
   repository over `net/http` and the standard library's crypto: PUT (with `x-amz-content-sha256` of the body),
   GET (checked against the row's SHA-256 and length before a byte is served), DELETE, ListObjectsV2 by prefix (for
   reconciliation) and HEAD bucket (at startup). An AWS SDK or minio-go was rejected: each brings a dependency
   tree the platform would have to pin and review (the console has no dependency and the server has four), and
   the platform needs five operations, none of them multipart (an artifact is at most 8 MiB).
3. **Write before the row, reconcile what the row never named.** The worker PUTs the object, then publishes the
   row in the same transaction as the job's completion, as today. If the transaction fails after the PUT, the
   object is an orphan: the sweep's reconciliation lists the bucket's `artifacts/` prefix and deletes objects
   whose id names no asset row and whose last-modified time is older than the grace period. The row is never
   written before the object exists, so a row never promises a missing object.
4. **Delete the row, then the object.** The sweep deletes expired artifact rows in its transaction (S04-B) and then
   the objects they named; an object whose delete fails is an orphan for the next reconciliation. A download
   between the two answers as a swept artifact does (404). Counts only, as every sweep.
5. **Reads check the row first.** A download reads the row (the source product's current authority, the expiry,
   the identity's row-level context, the issue cross-checks), then GETs the object, then checks SHA-256 and
   length against the row. A mismatch, or an object missing where the row promises one, is INTEGRITY_FAILURE (409),
   never a served file. The object store's own errors are never relayed to the caller.
6. **Configuration and privileges.** Endpoint, bucket and region come from flags, the access key and secret from
   the environment, never from flags or files the server reads; the endpoint is `https://` with the system trust
   store (an operator-named CA file is allowed), `http://` only on a loopback address and only when asked (the
   demo and the qualification). The server HEADs the bucket at startup and refuses to start when it cannot. The
   credentials need PutObject, GetObject, DeleteObject and ListBucket on that one bucket and nothing else
   (OPERATIONS.md gives the policy); the platform never creates or configures buckets, lifecycle rules or
   encryption settings: expiry is the row's and the sweep's, not the bucket's.
7. **Nothing else changes.** Quotas count rows (an artifact is one stored result whatever holds its bytes),
   retention's rules are the row's, row-level security guards the row. The memory store keeps content inline. The
   operator's `reporting-migrate sweep` takes the same object store configuration to delete objects; without it,
   it deletes rows only and says the objects wait for a reconciliation.

## Consequences

The database stops growing with files. A deployment needs a second service with its own credentials, backups and
availability; a bucket that is down makes exports and issues fail (the job fails, an issue is voided, as any
renderer failure today) and downloads answer UNAVAILABLE (503), never a wrong file. The qualification runs against a
throwaway S3-compatible server, as PostgreSQL's: MinIO where an image is already present locally, versitygw in CI (MinIO's
images are no longer publicly pullable, found at S04-E); AWS S3 is not qualified by a test here and is listed as such. A later slice may move snapshots or add server-side encryption; this ADR does not.
