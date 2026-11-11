# Engineering rules

- Keep reporting/ domain-neutral and standard-library-only. Product identifiers
  belong in adapters, composition or examples. No product-private imports.
- Fail closed on unknown query capabilities, source binding errors and authority
  failures. Never put caller-supplied identity, permission or budget in QuerySpec.
- Keep identifiers and values opaque at transport boundaries. Use exact decimal
  and integer strings; never convert business precision through float64.
- Every repository disclosure is owner-scoped. Worker claims are a trusted
  internal port; never expose them over HTTP. Do not log credentials or raw rows.
- Bound input, result, pages, work duration and artifacts. Check context errors.
- Persist artifacts and the successful task in the same transaction. Final
  publication uses lease time and generation, not a prechecked state alone.
- Tests use public seams and literal independent expected values. Keep actual
  runtime logs and distinguish simulator, host, live DB and production evidence.
- The demo is explicit, loopback-only and protected by a random bearer secret.
  It must refuse production database names and cannot serve as production SSO.
- Contract-level decisions are the product owner's own: the adapter contract and
  the source protocol; what the platform stores of business data and for how
  long; who may see what (authorization, roles, isolation); a migration that
  discards data; the admission decision. An ADR on one of them stays `proposed`,
  with its questions and a recommended answer to each, until the owner has
  answered those questions. A general delegation ("you decide", "按你的建议")
  starts the work and is not that answer. Engineering decides everything else
  and records it in an ADR.
