# S04-H key lifecycle: rotating what the platform holds, without breaking what is in flight

2026-11-02. The platform holds a handful of secrets and trust anchors, and until now only some of them could be changed
without an outage or without invalidating something a user holds. Reading the code: the cursor HMAC key is a single key
(`reporting.Config.CursorKey`, used by `encodeCursor`/`decodeCursor`), so replacing it makes every cursor in a user's hands
invalid (the demo derives it from `REPORTING_DEMO_TOKEN`, which is no way to hold a key); the issuer's public keys
(`accessjwt.Config.JWKS`, up to 16 `kid`s, swapped atomically by `Replace`) can be rotated by overlap, but nothing proves
the procedure, nothing shows which keys are trusted, and the exposure windows are not written down; the database
passwords and the object store keys are read at start and used by long-lived pools and clients, with no stated
procedure for changing them. This slice gives each of them a rotation that is proved, bounded and written down. It
does not add automatic rotation, a KMS or HSM, envelope encryption, or revocation of a single token.

H1 · Cursor key ring. `reporting.Config.CursorKeys [][]byte`: the newest key first, 1 to 4 keys, each at least 32 bytes, pairwise
distinct; the first signs every new cursor, any of them verifies. `CursorKey` stays as the ring of one (setting both, or
neither, or an invalid ring, is `ErrInvalid` at `NewService`, whose error never contains key material). The cursor
format does not change (no key id): verification tries the ring in order and evaluates every key in constant time with
respect to which one matched, so a cursor issued by the previous binary stays valid. The demo derives its current key
as now from `REPORTING_DEMO_TOKEN` and takes optional `REPORTING_DEMO_PREVIOUS_TOKENS` (comma-separated, newest
first, at most 3) for the older ones, so that a rotation can be rehearsed. Proved: a cursor issued under K1 is accepted by the
rings [K2,K1] and [K1], rejected by [K2]; one issued under K2 verifies under [K3,K2], [K2,K1] and [K2]; pagination of a
snapshot continues across a service restart that rotates the key (HTTP level, a real cursor taken before and used
after); a tampered cursor, a cursor of another snapshot or owner, an oversized one and one signed by an unknown key are
`ErrInvalid` exactly as before; the ring's validation (empty, short key, duplicate, five keys, both fields).

H2 · Issuer key rotation. `accessjwt.Verifier.KeyIDs() []string`: the sorted `kid`s of the configuration in force, nothing
else (no modulus, no exponent). Proved with real RS256 tokens: the overlap rotation is `Replace` with old and new
keys (tokens signed by either are accepted), then `Replace` with the new key only (a token signed by the removed key is
401 `invalid_token` from the next request on, a token signed by the new key is accepted); a `Replace` that fails
validation leaves the previous configuration in force and returns the error (a rotation that fails half-way
changes nothing); an authentication that had started under the old configuration completes (the documented
semantics); a token naming a `kid` that is not trusted is refused and the refusal text names no key. The demo
logs the trusted `kid`s at start (ids only). Written down: the two different exposure windows. A leaked *signing key*
is exposed until the operator removes it with `Replace` (effective at the next authentication, no restart). A stolen
*token* is exposed until it expires, at most `MaxTokenLifetime` (1 to 3600 seconds, set by the operator); there is no
revocation of a single token (no `jti` denylist: S02-A's boundary, restated), so the lifetime is the control, and
OPERATIONS.md says to choose it as the window the organisation accepts.

H3 · Credentials. Proved, in the PostgreSQL and object store qualifications:
- the database password of the application role is changed (`ALTER ROLE … PASSWORD`): connections already open keep working,
  a new connection with the old password fails, `OpenProduction` with the old password is refused with a text that
  carries no secret, and with the new one opens. The consequence is documented: a running server whose pool later
  opens a new connection with the old password starts failing, so the rolling restart with the new secret follows the
  change at once (the order is: set the new password on the server, restart servers one at a time with the new
  DSN; there is no dual-password state in PostgreSQL, so state the window);
- the same for the sweep role's DSN and for `reporting-migrate` run with the migrator's;
- the object store access key and secret: a client built with a revoked key fails its `Check` at start (the server refuses to start, the text names the bucket and the status only) and
  a running client's next request fails with the unavailable error, never with the key; the procedure is the object
  store's own (create the new key pair, restart with it, delete the old pair), written down;
- no secret appears in any error text, log line or `/capabilities` answer produced by these failures (assert on the
  texts with the known secrets).

H4 · The inventory and the runbook. OPERATIONS.md "Keys and credentials": a table with one row per secret or trust anchor:
the cursor key ring; the issuer JWKS (public: integrity, not secrecy); the application, sweep and migrator role
passwords; the PostgreSQL TLS trust (`sslmode=verify-full` and the CA); the object store key pair and its CA; the Typst
binary and fonts pins (SHA-256 pins of what the server will execute: integrity, with their own update procedure); the
demo token (demo only, never a production credential: say so). For each: what it protects, where it is held (the
platform holds none of them at rest; they come from the process environment or the host's secret store: say who
provides them), who generates it, the blast radius of a leak, the rotation procedure step by step with its downtime
(none, a rolling restart, or the window above), how to confirm it worked, and a suggested maximum age labelled as a
suggestion (the platform enforces no age). The runbook ends with the emergency order: which to rotate first when
which leaks. Verified claims only: every statement that a procedure causes no downtime is the one a test above proves.

H5 · Tests and docs. The tests above; the demo's key derivation is unchanged for a single token; no secret in the demo's log
lines (assert on its output with a known token and a known previous token). OPERATIONS.md "Keys and credentials",
docs/ACCESS_JWT.md (rotation and the two windows; replace the sentence that leaves rotation to "后续真实 IdP 合同" with what is now
true and what still waits for the real IdP: automatic discovery and emergency revocation), README S04-H at the top
with the "以下为此前阶段历史说明：" line moved directly below it. API.md and CONSOLE.md are unchanged.

Not in this slice: automatic rotation or key age enforcement, a KMS/HSM, envelope encryption of the stores, backup
encryption keys, TLS certificate rotation beyond the runbook's step list, per-token revocation, dynamic JWKS discovery
(waits for the real IdP), production admission.
