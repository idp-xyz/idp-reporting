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
