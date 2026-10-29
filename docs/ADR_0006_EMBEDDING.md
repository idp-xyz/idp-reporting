# ADR 0006 · Embedding the platform's views and designers in a product

Date: 2026-10-29. Status: accepted. Implementation: S03-N.

## Context

The architecture baseline: "平台有独立控制台，也提供嵌入产品的展示与设计入口" (docs/s00/ARCHITECTURE.md), and the
migration plan names "独立控制台及嵌入层" for S03 (docs/s00/MIGRATION.md). A product (Brant, a second product)
must be able to show the platform's report viewer, dashboards and the document designer inside its own pages,
for its own signed-in user, without the user visiting the console.

What must hold: the platform authenticates only a single `Authorization: Bearer` access token — never a cookie,
a query parameter or a forwarded identity header (docs/ACCESS_JWT.md); the console keeps the token in memory
only; every page the platform serves forbids framing (`frame-ancestors 'none'`, `X-Frame-Options: DENY`) and
runs under a strict CSP with no inline code; the source product keeps confirming current business permission
on every read. Embedding must not weaken any of these for the standalone console, and must not let a site
that is not the product's frame a page or receive anything from it.

## Decision

1. **Framed pages, not a script in the product's page.** The platform serves separate embed entry points
   (`/embed/dashboard`, `/embed/report`, `/embed/document-designer`, with the resource named in the query) that
   run the console's own code in an embed mode (no navigation shell, no token form). The product puts one in an
   `<iframe>`. A JavaScript SDK running in the product's own origin was rejected: the platform's UI and the
   user's platform token would live in the product page's origin, exposed to that page's scripts and its CSP,
   and the platform would need CORS for its API.
2. **Who may frame.** Each integration has, in the platform's trusted server configuration (never in a
   request), a list of allowed embedding origins (exact `https://host[:port]` origins; `http://127.0.0.1:port`
   only in the demo). The embed URL carries the integration and the parent origin; the server answers only if
   that origin is on the integration's list, and sets `Content-Security-Policy: … frame-ancestors <that one
   origin>` (and no `X-Frame-Options`, which cannot express it). Any other origin gets 403 and a page that
   cannot be framed. The console and every other page keep `frame-ancestors 'none'`.
3. **The token arrives by postMessage, from the parent only.** The embedded page loads with no credential and
   posts `reporting:ready` to `window.parent` with the exact parent origin as target. The product page answers
   with `reporting:token` (the user's platform access token, obtained by the product's backend from the
   identity provider — the platform never sees the product's session). The embedded page accepts a message
   only when `event.source === window.parent` and `event.origin` is exactly the parent origin it was served
   for (which the browser already enforced through `frame-ancestors`); it keeps the token in memory, sends it
   only as the Bearer header to its own origin, and asks for a fresh one with `reporting:token-request` before
   expiry or after a 401. No token ever travels in a URL, a cookie or storage. The same API, scopes and
   source-product checks apply as for the console; embedding grants nothing.
4. **Messages are a small, versioned, value-free protocol.** Parent → frame: `reporting:token`,
   `reporting:theme` (light or dark). Frame → parent: `reporting:ready`, `reporting:token-request`,
   `reporting:resize` (content height in px), `reporting:event` (a closed list: `saved`, `published`,
   `issued`, with the resource or job id only) and `reporting:error` (a code only). Each message carries
   `v: 1`; unknown types and versions are ignored. No row value, no field value and no file ever crosses.
5. **Downloads and issued files stay inside the frame**, through the same authorized, digest-checked download
   as the console. The parent learns an id, never the content.
6. **Configuration** is part of the integration's trusted configuration (the demo takes it from flags), and
   `GET /capabilities` tells an authenticated caller whether embedding is configured for its integration (not
   the origins).

## Alternatives

- **Token in the iframe URL** (a short-lived signed embed token): URLs leak into logs, history and Referer;
  the access-token rules forbid query credentials.
- **Third-party cookies or a session cookie on the platform's origin**: browsers block third-party cookies,
  and the platform authenticates no cookies by design.
- **A JavaScript SDK in the product's page**: see decision 1.

## Consequences

Products embed with an iframe and about twenty lines of host code (answer `ready` and `token-request`,
listen for `resize` and `event`); a reference host page ships with the platform for the demo and the tests.
Real identity-provider token issuance for embedded users comes with the real integration (last). Work: S03-N
(embed entry points and mode, origin configuration and `frame-ancestors`, the message protocol, the reference
host page, DOM tests for the protocol, a real-browser script with a host on another origin that proves
framing is refused to an unlisted origin and that the token never appears in a URL, a request or storage
other than the Bearer header).
