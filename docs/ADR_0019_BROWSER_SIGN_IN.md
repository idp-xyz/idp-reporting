# ADR 0019 · Browser sign-in: authorization code with PKCE, the code exchanged by the server, the verifier in the tab for one navigation

Date: 2026-11-09. Status: accepted (implementation: S15-A, docs/S15A_SCOPE.md).

## Context

People sign in to the console by pasting an access token (docs/CONSOLE.md). They have to get one from somewhere else, handle it by
hand, and paste it again every time it expires. The server already verifies access JWTs of a configured issuer (identity/accessjwt,
docs/ACCESS_JWT.md); what is missing is the browser's way of getting one.

The real identity provider's contract is not known (docs/DEPLOYMENT.md, go-live item 16), so the design must be generic: OpenID Connect's
authorization code flow with PKCE (RFC 7636, `S256`), configured rather than discovered.

Three constraints of the console decide the shape:

1. **The Content-Security-Policy** (`console/handler.go`) is `connect-src 'self'` and `form-action 'none'`. The page cannot call the
   provider's token endpoint or post a form to it. A top-level navigation is allowed. So the browser goes to the authorization endpoint by
   navigating, and the code exchange is the server's.
2. **The token is in memory only**, and tests assert that `localStorage` and `sessionStorage` are empty after a sign-in.
3. **A redirect loses memory.** The state, the nonce and the PKCE verifier are made before leaving and needed after coming back.

## Decision

1. **The flow.** `GET /api/reporting/v1/sign-in` (unauthenticated, `no-store`; `{"enabled": false}` without a sign-in) tells the console the authorization endpoint, the client
   id, the redirect URI, the scope and `S256`; nothing secret, not the token endpoint. The console makes a 32-byte verifier and a 16-byte
   state and nonce (Web Crypto), and navigates to the authorization endpoint with `response_type=code` and `code_challenge` =
   BASE64URL(SHA-256(verifier)). The provider redirects to the redirect URI, which must be the console's page (`/console/`). The console
   checks the state, and posts `{code, code_verifier, nonce}` to `POST /api/reporting/v1/sign-in/token`. The server sends the code, the
   verifier, the **configured** redirect URI and the client id to the **configured** token endpoint over HTTPS (bounded: at most 16 at
   once, 10 seconds, 64 KiB, no redirect followed), and answers the access token **only if its own verifier accepts it**. From then on
   the token is used exactly like a pasted one: every request is still verified by identity/accessjwt (issuer, audience, keys, bindings,
   roles).
2. **What survives the redirect: one sessionStorage entry of the tab, for the one navigation.** Just before leaving, the console writes
   `{state, verifier, nonce, at, hash}` under one key. When the page loads again, the very first thing it does, before rendering or any
   request, is to read that entry and **remove it**, whether this load is a return or not; the code and the state then leave the address
   (`history.replaceState`). Sign-out removes it, a page restored from the back-forward cache removes it, and an entry older than ten
   minutes is refused. Once the page runs, nothing is stored: the invariant the tests hold ("nothing in storage after sign-in") stays true.
3. **The ID token is not used for authorization.** When the provider returns one, the server compares its `nonce` with the one the
   browser sent (OpenID Connect Core §3.1.3.7 lets a code-flow client rely on TLS to the token endpoint instead of the signature) and
   drops it; it never reaches the browser, and neither does a refresh token.
4. **Replay.** A code is remembered (its SHA-256) from the moment it is presented until ten minutes later; a second presentation is
   `409 SIGN_IN_REPLAYED` without asking the provider. A code the provider refused is forgotten (the provider will refuse it again, and a
   memory of refused codes would let anyone fill it). This is per process; across servers the provider's own single-use rule
   (RFC 6749 §4.1.2) is the guard. The console refuses a return whose state is not its own, has no entry, or is too old, without any
   request.
5. **Fixed answers, nothing logged.** The browser gets one of `SIGN_IN_REFUSED`, `SIGN_IN_REPLAYED`, `SIGN_IN_TOKEN_REFUSED`,
   `SIGN_IN_UNAVAILABLE`, `SIGN_IN_BUSY` (or `INVALID_REQUEST`), never the provider's text; the console shows a fixed Chinese message
   for each, never the provider's `error_description`. The log has one `sign_in` line per exchange with an outcome word.
6. **No open redirect.** The server never redirects. The redirect URI is one configured value, `https://host[:port]/console/` with no
   query or fragment, sent unchanged in both requests; the browser does not send it to the server. The console refuses to start a sign-in
   when its own address is not the configured one.
7. **The paste form.** Without a `sign_in` section the console is unchanged. With one, the paste form is **hidden** unless the
   deployment sets `paste_fallback: true`. Hiding it is hygiene, not a boundary: the API accepts any valid token, as it must for API
   clients. The reasons: once people can sign in, a form that asks them to paste a credential is what a phishing message would ask them to
   use (a pasted attacker's token signs the victim into the attacker's workspace, where what they upload or design is the attacker's),
   and tokens handled by hand end up in chats and tickets. `paste_fallback` is for a break-glass procedure (an operator with a token from
   the provider's own tools).
8. **Public client.** The recommended registration is a public client with PKCE alone. A confidential client is supported
   (`client_secret_basic`), its secret only from `REPORTING_SIGN_IN_CLIENT_SECRET`; the configuration file refuses secret-like names.

## Options weighed for the verifier

- **A popup whose opener keeps the verifier in memory**, the code sent back by `postMessage` from the same origin: nothing stored at
  all. Refused: popups are blocked unless opened in the click itself (the S256 digest is asynchronous), many providers send
  `Cross-Origin-Opener-Policy: same-origin`, which severs `window.opener` so the code cannot come back, and a second window is an
  accessibility and mobile problem.
- **A login transaction held by the server**, keyed by an opaque id: the verifier never reaches the browser. Refused for now: without a
  cookie (the console has none, by design) the id must travel through the provider in `state`, so whoever holds the redirected URL
  (history, a screenshot, a proxy log) holds everything needed to redeem the code; PKCE's binding to the initiating browser is lost. It
  also needs shared state behind a balancer (several servers on one store, S11-B) and an unauthenticated route that creates server-side
  state, a memory anyone can fill.
- **The tab's sessionStorage for one navigation** (chosen): the verifier stays in the browser that started the flow, so a stolen code
  is useless without it; sessionStorage is per tab and per origin, gone when the tab closes, never sent to a server; the server stays
  stateless but for the replay memory. The cost: during the round trip, a script running in the console's origin could read the entry.
  Such a script could equally read the token from memory once signed in, the CSP (`script-src 'self'`, no inline script, no markup
  written from data, ADR 0008) is what keeps it out, and the entry lives only between the click and the return.

## Consequences

- The console has a 「使用企业账号登录」 button when `sign_in` is configured; signing in is a click and a provider page.
- When the token expires the console signs out as today (a 401 clears the page); signing in again is a click. There is no silent
  renewal (no `prompt=none` iframe: `frame-src` is `'none'`) and no refresh token. Both are open questions for the real provider.
- The embed page is unchanged: it receives its token from the host page (ADR 0006).
- What the real provider's contract may change: discovery (`/.well-known/openid-configuration`) instead of configured endpoints, an
  authorization endpoint with its own query parameters (refused today), `private_key_jwt` or mTLS client authentication, opaque access
  tokens (refused today: the platform verifies JWTs), its logout endpoint (sign-out today ends only the console's session), and the claim
  that maps a person to a binding.
