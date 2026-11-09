# S15-A browser sign-in for the console: OpenID Connect authorization code with PKCE, the code exchanged by the server

2026-11-09. People stop pasting access tokens: with a `sign_in` section the console signs in through the organisation's identity provider.
The real provider's contract is not known (go-live item 16), so the implementation is generic and configured, and is tested against fake
providers. Decision and trade-offs: docs/ADR_0019_BROWSER_SIGN_IN.md.

A1 · The exchange (`identity/signin`, standard library only). `signin.Config` holds the authorization and token endpoints, the client id,
the redirect URI, the scope, `paste_fallback`, a CA file, `loopback_http` and a timeout; `Check` holds each URL to
`https://host[:port]/path` with no user, query or fragment (`http://` only on a loopback IP and only with `loopback_http`), the redirect
URI to the console's page `…/console/`, the client id to 1..256 printable characters, the scope to RFC 6749 §3.3, the timeout to 0..60 s;
its refusals name the rule and never a value. `Client.Exchange(code, verifier, nonce)` bounds its inputs (a code of 1..2048 VSCHAR, a
verifier of 43..128 unreserved characters, a nonce of 22..128), takes one of 16 slots (else `ErrBusy`), remembers the SHA-256 of the code
for ten minutes (a second presentation is `ErrReplayed`; a code the provider refused is forgotten; at most 4096 remembered, else
`ErrBusy`), and posts `grant_type=authorization_code`, the code, the configured redirect URI, the verifier and, for a public client,
`client_id` (a confidential client's secret, from `REPORTING_SIGN_IN_CLIENT_SECRET` only, goes as `client_secret_basic`). TLS 1.2 or
later, verified against the CA file or the system's roots; no redirect followed; the whole exchange within the timeout (10 s). The answer
must be 200, `application/json`, at most 64 KiB, one JSON document with a printable access token of at most 16 KiB and `token_type`
Bearer (any case), `expires_in` 1..86400 when present; an ID token, when present, must carry the nonce sent (its payload is read, its
signature is not: TLS to the configured endpoint vouches for it, OpenID Connect Core §3.1.3.7). Status 400 is `ErrRefused`, 401 too
(the outcome says the client was refused, a configuration error), anything else `ErrUnavailable`, as are a timeout, a refused connection, an
untrusted certificate and an answer that is not a token answer. Each exchange reports one outcome word for the log (`ok`, `replayed`,
`busy`, `provider_refused`, `provider_client_refused`, `provider_status`, `timeout`, `unreachable`, `oversized`, `malformed`,
`nonce_mismatch`). `Config` prints without its secret.

A2 · The routes (`httpapi.WithSignIn`, unauthenticated, under `secured`). `GET /sign-in` answers `signin.Public`
(`enabled: true, authorization_endpoint, client_id, redirect_uri, scope, code_challenge_method: "S256", paste_fallback`); a query is 400. `POST
/sign-in/token` takes `{code, code_verifier, nonce}` (strict JSON, at most 4 KiB), exchanges it, and judges the token with the
deployment's own `Authenticate` before answering `{access_token, expires_in?}`; a token the verifier refuses is `403
SIGN_IN_TOKEN_REFUSED`. The other answers: `400 INVALID_REQUEST` (a body out of its bounds, before any exchange), `400 SIGN_IN_REFUSED`,
`409 SIGN_IN_REPLAYED`, `502 SIGN_IN_UNAVAILABLE`, `503 SIGN_IN_BUSY` with `Retry-After: 1`, `413`. Without a `sign_in` section the mount is
not composed: the core API answers `GET /sign-in` with `{"enabled": false}` (not a 404 on every load of the console) and the exchange is the
mux's 404. docs/openapi.json describes both (`x-reporting-mount: WithSignIn`, `security: []`, the three
schemas, five new error codes); `TestOpenAPIMatchesTheBoundRoutes` now also takes the pattern a mux wrote on the request, so that a route
that never reaches the authenticator is seen as bound.

A3 · The server and the demo. `server.Config.SignIn` (`sign_in`, docs/DEPLOYMENT.md reference, example, "Browser sign-in", secrets
table, start step **sign in**, the `sign_in` log event, the start line's `sign_in`, `check-config`'s `browser sign-in` line and the
secret's presence) is validated by `Validate` without reading files; `ca_file` is an absolute clean path checked by `check-config` as the
object store's is; the start reads the secret from the environment and builds the client before the store; `WithSignIn` is composed after
`WithProfile`, inside `max_in_flight`. `reporting-demo` takes `-sign-in-authorization-endpoint`, `-sign-in-token-endpoint`,
`-sign-in-client-id`, `-sign-in-redirect-uri`, `-sign-in-scope`, `-sign-in-paste-fallback` and `-sign-in-ca` for a local fake provider
(loopback HTTP is allowed: the demo is loopback-only); the fake must answer the demo's bearer secret, the only token the demo accepts.

A4 · The console (`console/assets/app.js`, `index.html`, `styles.css`). As the page loads, before anything else, `takeSignInReturn` reads
and removes the one sessionStorage entry (`reporting-sign-in`) whatever this load is; when the address has `code`, `state` or `error`, the
query leaves the address at once (`history.replaceState`, the page's `#hash` restored) and the return is checked: an entry of the right
shape, younger than ten minutes, and exactly one `state` equal to it, else 「登录结果与本页发起的登录不符或已过期」 without a request;
`error` from the provider is a fixed message, its `error_description` never shown. Then `GET /sign-in`: a valid configuration (an
`https:` endpoint, or `http:` on a loopback IP, without query, fragment or user; S256; the scope's grammar) shows 「使用企业账号登录」 and
hides the paste form unless `paste_fallback`; a redirect URI that is not this page's address disables the button with a message naming the
right address; anything else is no sign-in. The button makes the verifier, the state and the nonce (Web Crypto), the S256 challenge,
writes the entry and navigates (an anchor with `rel=noreferrer`); after 15 s still on the page, the entry is removed. A return posts
`{code, code_verifier, nonce}` and starts the session with the token as `start` does for a pasted one; each refusal code has its fixed
message. Sign-out and a page restored from the back-forward cache remove the entry. The Tab trap of the card follows what is shown.
`console/embed.dom.test.mjs`'s `notInEmbed` lists the three new ids; the embed page and its shared sections are unchanged.

Tests. `identity/signin` (a fake provider on `httptest.NewTLSServer` that knows each code's S256 challenge and redirect URI and spends a
code on its first use): a good code with the exact form sent and the replay refused locally; a wrong verifier, a spent code, an unknown
code, a code issued for another redirect URI, an unknown client; a 500, a 503, a redirect, an oversized answer, not JSON, HTML, no
access token, a MAC token, a token with a space, a string lifetime, two documents, an ID token of another nonce, without a nonce, not a
JWT; no ID token; a timeout, the caller's deadline, an untrusted certificate, nobody listening; 16 exchanges at once and the 17th busy;
a code presented twice at once; a confidential client's Basic header; inputs refused before any request; every configuration rule with a
value that must not appear in the refusal; the public part. `transport/httpapi`: the public answer and its headers, each refusal's
status and code, the platform's verifier refusing the provider's token, every malformed body refused before the exchange, and the whole
chain with the real client against a TLS fake. `server`: the section's acceptance and refusals (a `client_secret` field refused as a
secret), and a started server against a TLS fake provider minting the world's RS256 tokens: the public part, the exchange, `/session`
with the token, the replay, a refusal, a token for another audience refused, the log's outcome words and none of the values; a refused
secret at the **sign in** step. DOM (`console/dom.test.mjs`): the console without a configuration (paste form alone, nothing stored);
the departure (the eight parameters, the S256 of the stored verifier, one entry) and the return (the exchange's body, the address
cleaned, nothing stored, signed in, signed out with the button back); a forged state, no entry, an old entry, a repeated state, the
provider's error with markup in its description, each server code and an answer that is not a token, with no exchange for the first
five; `paste_fallback`, the focus and the Tab trap, an address the provider does not know, five configurations the console must not
trust; and end to end, a second Go demo with the `-sign-in` flags against a fake token endpoint in Node that checks the challenge, the
redirect URI and the client: signed in, then the same return refused as a replay without reaching the provider, and nothing of the code,
the token or the provider's text in the server's log.

Not covered. No real provider: discovery, the provider's logout, silent renewal and refresh tokens, `private_key_jwt` and mTLS client
authentication, opaque access tokens and an authorization endpoint with its own query are not supported (ADR 0019 "Consequences"). The
replay memory is per process. No rate limit of the unauthenticated routes beyond `max_in_flight` and the 16 slots (the balancer's job,
docs/DEPLOYMENT.md). The real-browser job does not run the sign-in (its demo has no `-sign-in` flags, so the card is the one it measured).
