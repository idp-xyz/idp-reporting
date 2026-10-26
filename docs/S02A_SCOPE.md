# S02-A: real-source discovery and access-token authentication

Date: 2026-10-26 Asia/Shanghai. Fixed point: accepted S01-B delivery
f425c0d69e0b687565b04a48ca347f9781aa9bea. Continue S00; keep independent architecture.
This slice does not change product business rules or the generic execution core.

## Public seams and acceptance

The existing S00 identity-to-HTTP boundary is the agreed seam: an Authenticate
function produces reporting.Principal and is passed to httpapi.Handler. S00 requires
verified issuer, subject, audience, signature, lifetime and access-token purpose;
server-controlled tenant/product mapping; no ID-token or email identity merging.
The user's continuation authorizes implementing this existing seam. No new testing
seam, remote issue tracker or external repository is introduced.

1. Add identity/accessjwt, a bounded RS256 access-token verifier wired through the
   existing HTTP authentication port. Use Go standard crypto; no new dependencies.
2. Trust only operator-provisioned HTTPS issuer, exact audience, bounded public
   JWKS and explicit subject+client_id to tenant/integration bindings. Validate
   configuration before atomic replacement. Reject unknown keys/identities; return
   copied principals. Empty bindings are a deliberate deny-all configuration.
3. Require RFC 9068 access-token typ and iss/sub/aud/exp/iat/jti/client_id, plus
   the operator-provisioned required OAuth scope. Reject
   unsigned/HMAC/ID tokens, altered signatures, wrong issuer/audience, invalid or
   excessive lifetime, future nbf/iat, ambiguous JSON, unsupported JOSE controls,
   delegated act or proof-bound cnf tokens. No network access from token claims.
4. Tokens do not grant products, tenants, roles or budgets. Bindings authorize the
   platform entry; each source still checks current whole-scope business rights.
5. A replacement key/binding set applies to subsequent authentications; concurrent
   in-flight authentications can finish with their starting configuration. No
   cross-system atomic revocation, remote introspection or token denylist claim.
6. Keep HTTP errors generic: invalid credentials get 401 invalid_token; a verified,
   mapped token lacking the required scope gets 403 insufficient_scope.
7. Test the public verifier + HTTP path with independently signed local RSA JWTs,
   TLS loopback HTTP snapshot/export/readback and current source revocation. Add
   malicious-token/configuration/reload/concurrency controls; preserve S01 tests.
8. Inspect current Brant and Parcel source packages plus connected repository
   evidence. Record exact hashes/commits and concrete missing contracts. No
   simulator or frontend seed is qualified as a real source adapter.

## Deliberate supported profile

One provisioned issuer per Verifier; RS256 only; 2048–4096-bit RSA modulus,
exponent 65537; at+jwt or application/at+jwt typ (media type case-insensitive);
maximum 32 KiB compact JWT, 64 KiB JWKS, 16 keys and 4096 bindings. Token dates are
integer seconds with zero skew, maximum lifetime explicitly 1–3600 seconds. The
provisioned maximum is a profile limit, not a production SLO or IdP recommendation.
JOSE headers contain only alg/kid/typ; use=sig, alg=RS256 and key_ops=[verify]
are enforced when present in JWKS. Certificate metadata is never a key authority.
The provisioned issuer's subject bindings are operator-controlled identities;
this does not establish a human login session or a delegated service account.
OAuth scope membership is exact and cannot expand server bindings; at most 64
scope words and 4096 bytes. The local fixture uses reporting.read as an example,
not as a globally registered scope. Roles/tenant/email claims confer no authority.

## Actual-source gates remain

S02-A is protocol-module qualification with a local signer and synthetic source.
I14 (two real source products), I15 (real issuer/OIDC/SSO qualification) and I16
(delegated service-account/current source rights) do not become full passes.
Remote discovery/JWKS refresh, browser code+PKCE, enterprise membership authority,
real product adapters/reconciliation, operator control plane, production key
rotation and deployed-provider acceptance remain further S02 work. The original
S01-B PostgreSQL evidence is historical unchanged baseline evidence, not rerun here.

