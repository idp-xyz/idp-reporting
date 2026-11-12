# ADR 0029 · What the first real source's design asks of the protocol: the platform's credential by the client-credentials grant, and the answer for a read over the bounds

Date: 2026-11-13. Status: **proposed**. Both parts are contract-level (the source protocol, and how the platform proves to a source
that it is the platform), so the ADR stays proposed until the product owner has answered the two questions at the end
(CODING_STANDARDS.md). It was found while writing Parcel's own ADR, which ADR 0027 decision 2 asks for and whose draft is in the
idp-parcel repository (branch `cursor/reporting-source-adr`, commit `793d9d7c6`,
`docs/adr/0384-reporting-source-is-an-optional-capability-…-judged-by-the-operator-register.md`). ADR 0027 decision 4 expected
it: "the first real implementation will change some of it". Nothing here changes what travels on the wire, and no revision of the
protocol follows from it. Implementation: S27-A.

## Context

**How the platform proves itself to a source today.** docs/SOURCE_HTTP_PROTOCOL.md section 1: `Authorization: Bearer <credential>`
"with the credential the operator gave both sides, and/or a client certificate (mTLS)". In the adapter `http` the bearer is the
value of the environment variable that `options.auth_env` names, read once when the server starts
(`httpsource.Options.Config`); rotating it "is a restart, rolled one server at a time, with the product accepting the old and the
new credential for the while" (docs/DEPLOYMENT.md).

**What Parcel has.** Parcel verifies every caller by something the caller itself holds, and none of it is a fixed secret
(read in its repository at main `9bf6e8603`; docs/REAL_SOURCE_DISCOVERY_PARCEL.md section 9):

- its machine callers ("integration clients": a customs broker's, a carrier's, a bank's system) present an access token that
  the deployment's authorization server issued by the OAuth 2.0 client credentials grant; Parcel verifies issuer, audience,
  signature and expiry, finds the token's subject in a register in which a client is bound to one tenant, and looks up what the
  client was granted, with grants that end and can be revoked;
- the credential itself is never stored (its ADR-0100: the credential "只在校验那一刻存在，不落库"); the register keeps the
  client's subject and a reference to where the credential is kept;
- its API process serves plain HTTP behind whatever terminates TLS, so a client certificate does not reach it.

Parcel's draft therefore registers the reporting platform as such a client, with a grant of its own kind, and has it present a
client-credentials token. To take the protocol's fixed bearer instead it would have to store a secret or its digest, which it
does for no other caller, or keep a second kind of verification for this one caller; and the secret would not expire. The
adapter `http` cannot obtain or renew such a token today.

This will not be Parcel's question alone. A product that runs an authorization server for its machine clients will expect the
platform to be one of them.

**A read over the bounds.** "The whole read is at most 1000 rows and 1 MiB" (section 3, `pages`), and section 4 gives `budget` for
"The source's own bound on prepared reads or work is reached". Nothing says what a source answers when the result of a query is
larger than a read may be. A source that freezes the rows at `reads`, as Parcel will, knows the size then. The platform, where it
finds a read over the bounds itself while it collects the pages, answers `BUDGET_EXCEEDED` (`Service.collect`).

## Decision

1. **A connection may obtain its bearer by the OAuth 2.0 client credentials grant** (RFC 6749 section 4.4). The request to the
   source is what it was: `Authorization: Bearer <token>`. What a connection declares is where the bearer comes from: `auth_env`,
   a secret given to both sides, as today; or a token endpoint and a client.
   - **Options.** `token_url` (https; http only to a loopback address with `loopback_http`), `client_id`, `client_secret_env`
     (the **name** of the environment variable that holds the secret, as `auth_env` names one), and optionally `token_scope`,
     `token_audience` and `token_ca_file` (the roots for the token endpoint's certificate; `ca_file` stays the source's). A
     connection declares `auth_env` or this group, not both; a client certificate may accompany either, as today.
   - **The request.** `POST` to `token_url`, the client authenticated with HTTP Basic (`client_secret_basic`), the body
     `grant_type=client_credentials` and, when set, `scope` and `audience`. No redirect is followed; the request is bounded by
     the connection's `timeout`.
   - **The answer.** `200` with one JSON object of at most 64 KiB, of which three fields are read: `access_token` (1..8192
     printable ASCII), `token_type` (`Bearer`, in any case) and `expires_in` (seconds, 1..86400). Anything else is a token that
     could not be obtained.
   - **Keeping it.** The token is kept in memory only and used until a margin before its expiry (60 seconds, or a tenth of its
     lifetime when that is less), then asked for again. One request per connection asks at a time and the others wait for its
     answer; a request of the platform causes at most one request to the token endpoint.
   - **Failing.** A token that cannot be obtained is the source being unavailable (`AUTHORITY_UNAVAILABLE`), in fixed words: no
     URL, no client id, no byte of the answer. The secret and the token are never logged and never part of an error.
   - **A `401` from the source** is "unavailable", as today, and the kept token is dropped, so that the next request asks for a
     new one. The platform still repeats no request by itself.
   - **The checker** takes the same options as flags, and its rule A1 (a request without the credential is 401) is unchanged.
   - **Rotation.** Tokens renew themselves. The client secret is rotated as today's credential is: a rolled restart, with the
     authorization server accepting the old and the new for the while.
2. **The protocol's page says what the bearer may be** (section 1): a secret the operator gave both sides, or an access token
   that the product's authorization server issued to the client the product registered for the platform. A product that takes
   such a token verifies it as it verifies its other machine clients' tokens (issuer, audience, signature, expiry); the token's
   subject is the platform's client and never a person: who a request is for is the `context` alone, as it is today.
3. **A source that can tell at `reads` that the result is over a read's bounds refuses with `budget`** and does not cut the
   result: the totals of a cut read are not the totals of anything. The row of `budget` in section 4 says so. The platform does
   what it does today.

## Alternatives

- **Leave the two ways; Parcel takes the fixed secret.** The protocol allows it, and it stays for a product that has no
  authorization server. For Parcel it means a stored secret or a verification kept for one caller, a credential that does not
  expire, and a rotation that both sides make at once.
- **Leave the two ways; Parcel takes the client certificate.** It stays too. Parcel's API does not see the certificate; to
  trust a header that a proxy adds is one more thing to trust, on the path that carries a person's name.
- **`private_key_jwt` or a certificate towards the token endpoint, in place of a client secret.** Stronger, and more to
  configure, test and explain. The secret is held as today's credential is, in an environment variable. Another way to
  authenticate the client can be added later as another option, without changing a connection that uses a secret.
- **A token for the person** (token exchange, RFC 8693), so that the source verifies the person's own token. It does not fit
  what the platform does: a scheduled report runs with nobody present, and a kept file is authorized days after the person
  left. The rule that the platform never sends a person's token stays.
- **Repeat the request once after a `401` with a new token.** The platform repeats nothing by itself (section 1); a worker's
  lease does. A token refused before its expiry is a client that was revoked, and the next request finds out.
- **`unsupported` for a read over the bounds.** It says the source does not publish the combination. It does: the same query
  with a narrower filter is served.
- **Cut the result and mark it.** Every page carries the totals of the whole read; a cut read has none.

## Consequences

- The adapter `http` gains a client for the token endpoint, a kept token and six options; `reporting-source-check` gains the
  flags. A connection that declares `auth_env` is exactly what it was.
- A connection has one more thing that can be unavailable, the authorization server, and the platform says of it what it says
  of the source: unavailable, nothing disclosed.
- The secret leaves the platform towards the token endpoint only, and the token towards the source only
  (docs/SECURITY_REVIEW.md gains the two sentences).
- Documents: the protocol's sections 1 and 4, docs/DEPLOYMENT.md (the options, the credential's row, the rotation),
  docs/REAL_SOURCE_ONBOARDING.md (服务身份), docs/SECURITY_REVIEW.md, docs/source-openapi.json (the security scheme's
  description).
- Tests need a token endpoint that can be made slow, wrong and absent, and a clock for the expiry: the adapter's own tests,
  the reference source's, and the checker's against a product that takes tokens.
- Parcel's draft can be built as it is written; it names this ADR's first question as one of the four its owner answers first.

## Slices

- **S27-A**: decision 1 in the adapter and the checker, decisions 2 and 3 in the documents. It does not wait for S26 and is done
  before Parcel's side is connected.

## Questions for the product owner

- **Q1, the platform's credential.** (a) A connection may obtain its bearer by the client credentials grant, beside the fixed
  secret and the client certificate, as decision 1 says (recommended). (b) The two ways stay as they are, and Parcel takes the
  fixed secret. (c) The two ways stay, and Parcel takes the client certificate.
- **Q2, a read over the bounds.** (a) The source refuses with `budget` and the page says so (recommended). (b) Another answer.
