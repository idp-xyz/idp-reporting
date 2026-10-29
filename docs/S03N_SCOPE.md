# S03-N embedding

2026-10-29. Implements ADR 0006: a product shows the platform's report viewer, dashboards and document designer
inside its own pages through an iframe, for its own signed-in user. The standalone console does not change its
security posture.

N1 · Entry points. `GET /embed/report`, `/embed/dashboard`, `/embed/document-designer` serve one embed page
(`console/assets/embed.html`, the console's scripts in an embed mode) with the query `integration`,
`parent` (the parent origin, exact), and the resource (`dataset` for the report viewer, `dashboard` for a
dashboard, `template` and optional `version` for the designer). The embed mode shows only that view: no
navigation shell, no token form, no logout; everything else (authorization, refusals, downloads, the canvas,
issuing) works as in the console. Assets are the console's, embedded and served with the same headers; the
embed page gets its own CSP (below). No inline script or style.

N2 · Allowed origins. Trusted configuration per integration (the demo: repeatable flag
`-embed-origin integration=origin`; the service API takes a map), exact origins only (`https://host[:port]`;
`http://` only for loopback in the demo), validated at startup (a malformed origin stops the service).
A request whose `integration` is unknown or whose `parent` is not on that integration's list answers 403 with
`frame-ancestors 'none'` and `X-Frame-Options: DENY`; an allowed one answers the page with
`Content-Security-Policy: <the console's policy with frame-ancestors <parent>>` and no `X-Frame-Options`. The
console and every other route keep `frame-ancestors 'none'` and `X-Frame-Options: DENY` (test every route).
`GET /capabilities` adds `embedding: true|false` for the caller's integration (never the origins).

N3 · Token handoff. The embed page holds no credential until the parent sends one: it posts
`{type:"reporting:ready", v:1}` to `window.parent` with targetOrigin = the served parent origin; it accepts
`{type:"reporting:token", v:1, token}` only when `event.source === window.parent` and `event.origin ===` that
origin, keeps the token in memory only, and sends it only as the Bearer header to its own origin. It posts
`reporting:token-request` after a 401 or when asked to re-authenticate and waits for a fresh token (the failed
request is retried once with the new token; a second 401 shows the console's signed-out state). The token never
appears in a URL, a cookie, Web Storage, a message to anyone but the parent, or the DOM.

N4 · Messages. Parent → frame: `reporting:token`, `reporting:theme` (`light` | `dark`). Frame → parent:
`reporting:ready`, `reporting:token-request`, `reporting:resize` (`height` px, debounced, on every size
change), `reporting:event` (`name` in `saved` | `published` | `issued`, with `template_id`/`version` or
`job_id` only), `reporting:error` (`code` only). `v: 1` on every message; others ignored; no values.

N5 · Reference host. `console/embed-host/` (not served by the platform): a static host page and a ~20-line
script showing how a product embeds a view and answers `ready`/`token-request` (here from a demo token field),
listens to `resize` and `event`. Used by the browser test, documented in docs/EMBEDDING.md (new: the protocol,
the configuration, the security rules, the host code).

N6 · Tests. Go: the embed routes' answers and headers for allowed, unlisted and malformed origins and unknown
integrations, every non-embed route still `frame-ancestors 'none'`, the configuration parser, capabilities.
DOM: the protocol (ready to the exact origin; tokens from another origin or source ignored; token-request on
401 and the single retry; resize and events; no token in storage, URL or DOM; unknown messages ignored).
Real browser (`scripts/browser_embed_s03n.mjs`, Windows Chrome over CDP): a host page on one loopback origin
(e.g. `http://127.0.0.1:<port A>`) frames the platform on another (`http://127.0.0.1:<port B>`): the
dashboard, the report viewer and the designer work in the frame after the token handoff; a host on a third,
unlisted origin cannot frame it (Chrome blocks it: check the frame never loads the page); the token appears in
no request URL, cookie or storage (CDP network log and storage inspection); resize and events reach the host.
Local evidence, not CI.

Not in this slice: real identity-provider token issuance for embedded users (with the real integration), a
JavaScript SDK in the product's page, embedding the issue records list on its own.
