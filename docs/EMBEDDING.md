# Embedding the platform's views and designers in a product (S03-N)

2026-10-29. Implements ADR 0006 (docs/ADR_0006_EMBEDDING.md) as scoped in docs/S03N_SCOPE.md. A product shows the platform's report
viewer, a dashboard or the document designer inside its own page, for its own signed-in user, in an `<iframe>`. The user never visits the
console. The standalone console does not change its security posture. Data class: contract simulator, as everywhere.

## What the product does

1. Serve a page from an origin the platform lists for the product's integration (below).
2. Put one `<iframe>` in it whose address is an embed entry point of the platform.
3. Answer two messages of the frame (`reporting:ready`, `reporting:token-request`) with the user's platform access token, and, if it wants,
   listen to `reporting:resize` and `reporting:event`.

`console/embed-host/` is the reference host: `index.html` and `host.js` (about twenty lines are the integration, marked in the file). It
is not served by the platform; serve it from any origin the platform lists (the browser acceptance script does, from two loopback ports).

```html
<iframe id="frame" title="IDP Reporting" src="https://reporting.example/embed/dashboard?integration=brant&parent=https%3A%2F%2Fapp.brant.example&dashboard=0123…"
        sandbox="allow-scripts allow-same-origin allow-downloads" referrerpolicy="no-referrer"></iframe>
<script>
const platform = 'https://reporting.example', frame = document.getElementById('frame');
addEventListener('message', event => {
  if (event.source !== frame.contentWindow || event.origin !== platform) return;      // this frame, this origin, nothing else
  const m = event.data;
  if (!m || m.v !== 1) return;
  if (m.type === 'reporting:ready' || m.type === 'reporting:token-request')
    frame.contentWindow.postMessage({type: 'reporting:token', v: 1, token: tokenOfTheSignedInUser()}, platform);   // never '*'
  else if (m.type === 'reporting:resize') frame.style.height = m.height + 'px';
  else if (m.type === 'reporting:event') console.log(m.name, m.template_id, m.version, m.job_id);
});
</script>
```

`sandbox` is the product's own hardening and is recommended: `allow-scripts` and `allow-same-origin` are needed (the page runs its
scripts and its requests go to its own origin), `allow-downloads` because the exports and issued files are downloaded from inside the frame,
and nothing else is (no forms, popups, modals or top navigation). The frame page also works without `sandbox`.

## Entry points

`GET /embed/report`, `GET /embed/dashboard` and `GET /embed/document-designer` serve one page (`console/assets/embed.html`) that runs the
console's own scripts in an embed mode. The query is exact: no other parameter, none repeated.

| Route | Query besides `integration` and `parent` | The view |
|---|---|---|
| `/embed/report` | `dataset=<id>` or `dataset=<id>@<version>` | The report workbench: query panel, result table, exports, the column layout designer. The dataset is selected (an id with several versions in the catalog, one the identity cannot see, or an unknown one is refused with `RESOURCE_UNAVAILABLE`). |
| `/embed/dashboard` | `dashboard=<32 hex id>` | The dashboard designer with the query panel on top. The identity's saved dashboard (its latest version) is read once to learn its dataset, which is selected; the saved version opens when the authorized result is on the page. |
| `/embed/document-designer` | `template=<32 hex id>`, optional `version=<1..20>` | The document designer (canvas, panels, previews, saved versions, release, official issue) with the query panel on top. Same as the dashboard; the version is the latest unless named. |

- `integration` is the platform integration (the product) the frame is for; `parent` is the exact origin of the product page that frames it
  (`https://host[:port]`, lower case, no path or trailing slash; the page the browser serializes as `location.origin`).
- The user still gives the company scope and the data date in the frame, as in the console; the resource named in the address is never
  opened before an authorized result is on the page, and a resource that is not the identity's, or is bound to another integration than the
  frame's, is `RESOURCE_UNAVAILABLE` (the same refusal as an unknown one).
- The frame shows only that view: no navigation, no token form, no logout. The integration select offers only the frame's integration.
  Everything else (authorization, refusals, digest-checked downloads, the canvas, previews, issuing) is the console's code and the same API.

## Who may frame, and what the page answers

Configuration is the platform's trusted server configuration, never a request: for each integration, a list of exact allowed origins.

- The demo: repeatable `-embed-origin integration=origin` (docs/OPERATIONS.md). The service API: `embedding.New(map[string][]string, embedding.Options)`
  (package `embedding`), given to `console.HandlerWithEmbedding` (the pages) and `httpapi.WithEmbedding` (the capabilities flag).
- Origins are exact and validated at startup; a malformed one stops the service: `https://host[:port]` only (ASCII lower-case DNS names or
  canonical IP literals, no default port, no path, query, fragment, user information, wildcard or list); `http://` only for a loopback IP
  address and only when the deployment asks for it (`Options.LoopbackHTTP`; the demo does). At most 32 integrations of 16 origins each.
- A request whose `integration` is unknown, whose `parent` is missing, repeated, malformed or not on that integration's list, or whose query
  does not parse, is answered **403** with `X-Frame-Options: DENY` and `Content-Security-Policy: frame-ancestors 'none'`, and no page. A
  listed origin with a malformed or extra resource parameter gets **400** with the same headers.
- An allowed request is answered 200 with the embed page and `Content-Security-Policy:` the console's policy with `frame-ancestors <that one
  origin>` and **no** `X-Frame-Options` (it cannot name an origin); `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`. Nothing else in the policy differs: scripts and styles from the platform's own origin only, no inline script
  or style, `connect-src 'self'`, `form-action 'none'`, `frame-src` none.
- **Every other answer** of the console handler (the console and each asset, the redirect, a 404 or 405, every route of the API, the embed
  routes' other paths and methods) carries `X-Frame-Options: DENY` and `frame-ancestors 'none'`; the API's own wrapper adds both to every API
  answer too (tests cover every route of every mount). The browser checks every ancestor of the frame, so a listed host that is itself
  framed by an unlisted page cannot frame the embed page either.
- `GET /capabilities` (authenticated) says `embedding: true` when embedding is configured for at least one of the caller's integrations,
  else `false`; it never carries an origin.

## Messages

All messages are `window.postMessage` between the product page and the frame, plain objects with `v: 1` (others are ignored; so are unknown
types, extra fields and anything not an object).

Product page to frame:

| Message | Fields | Meaning |
|---|---|---|
| `reporting:token` | `token` | The user's platform access token (an RFC 6750 b64token, at most 32 KiB). |
| `reporting:theme` | `theme`: `light` or `dark` | Colour scheme of the frame (the dark palette is coarse; light is the console's own). |

Frame to product page:

| Message | Fields | When |
|---|---|---|
| `reporting:ready` | | The page is loaded and listening for a token. |
| `reporting:token-request` | | A request got 401 (the token is expired or refused), a minute before a JWT token's stated `exp` (at most every 15 s), or the person pressed the retry button. |
| `reporting:resize` | `height` (whole px) | The height of the content, coalesced to at most one message per 50 ms, whenever it changes. The product sets the frame's height from it. |
| `reporting:event` | `name`: `saved`, `published` or `issued`, with `template_id` and `version` (saved, published: a template version) or `job_id` (issued: the official issue job, once it has succeeded) | Something the product may want to react to. Nothing else raises an event (dashboards and column layouts do not). |
| `reporting:error` | `code` | A failure class: `UNAUTHENTICATED`, `FORBIDDEN`, `NOT_FOUND`, `RESOURCE_UNAVAILABLE`, `UNAVAILABLE`, `INVALID_RESPONSE`, `INTEGRITY_FAILURE`, `REQUEST_FAILED`. Never a message, an id or a value. |

**No value ever crosses to the product page**: no row, field, total, template content, file or name; only the ids and codes above.

## The token

- It arrives only as `reporting:token` from `window.parent`, and the frame accepts the message only when `event.source === window.parent` **and**
  `event.origin` is exactly the parent origin the page was served for. Messages from any other window (a sibling frame, a popup, the page
  itself) or origin are ignored, whatever they carry. A page that is not framed (it is `window.parent`), or whose address is not exactly one
  the server serves, does not start and does not listen.
- Every message to the product page is posted with that same origin as `targetOrigin`: never `"*"`. If the parent is somewhere else, the
  browser drops the message instead of delivering it.
- The token lives in memory only (`state.token`). It is sent only as the `Authorization: Bearer` header of requests to the platform's own origin
  (fetch with `credentials: 'omit'`), and never in a URL, a cookie, Web Storage, IndexedDB, a message, the DOM or the log. The frame has no
  token form.
- After a **401** the frame posts `reporting:token-request` once and waits (10 s) for a token that is not the one that failed; the request is
  retried once with it. A second 401, or no token in time, is the console's signed-out state (data cleared, the card says so, the page
  reports `UNAUTHENTICATED`); a token pushed later, or the retry button, starts the session again. Concurrent requests share one wait.
- A fresh token is first checked with `GET /session`: only a token of the **same identity** (tenant, issuer, subject) replaces the one in use. A
  token of another identity is a different user: the page starts over with it and nothing of the previous user stays on screen.
- The product still decides whether to give a token and which: the platform never sees the product's session. How the product obtains an
  embedded user's token from the identity provider comes with the real integration (not in this slice).

## What embedding grants

Nothing. The same API, scopes and source-product checks apply as for the console: the source product confirms current business permission on
every read, the identity is the token's, and the embed page can do what the console can do for that identity and nothing more. An embedded view
is exactly as authorized as the user's token.

## Tests and evidence

- Go: the embedding configuration (`embedding/embedding_test.go`), the embed routes, headers and every other answer (`console/embed_test.go`),
  the capabilities flag and the frame headers of every API route of every mount (`transport/httpapi`), the demo's flag (`cmd/reporting-demo`).
- DOM, against the real Go server (`console/embed.dom.test.mjs`): the protocol (ready to the exact origin, tokens from another origin or
  window ignored, token-request on 401 and the single retry, the signed-out state, identity change, renewal, resize, events, theme, unknown
  messages), no token in storage, URL, DOM, any request but the Bearer header or any message, the embed page equal to the console's views.
- Real browser (`scripts/browser_embed_s03n.mjs`, Windows Chrome over CDP; local evidence, not CI): a host on one loopback origin frames the
  platform on another; the three views work after the handoff; a host on a third origin, a host that claims the listed origin, an unlisted
  frame under the listed host, the console and a malformed address are all refused by the browser; the CDP network log, the storage, the
  DOM and every message show where the token went. See the header of the script for how to run it.

## Not in this slice

Real identity-provider token issuance for embedded users (with the real integration), a JavaScript SDK in the product's page (rejected in
ADR 0006: the token and the platform's UI would live in the product page's origin), embedding the issue records list on its own, the
company scope or the data date in the address (the user gives them in the frame).
