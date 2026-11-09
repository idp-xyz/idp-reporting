# S12-A roles from the bindings, enforced on every route

2026-11-09. ADR 0018 decision 1 (proposed; built with the recommended answer to Q1).

A1 · A binding may name `roles`: 1..5 distinct names of `viewer`, `designer`, `publisher`, `issuer`, `administrator`. `r.Principal.Roles` carries
them; nil (a binding that names none) means `r.DefaultRoles`, the rights every bound identity had before roles; `administrator` is never
implied. The bindings file accepts the field (an empty list is refused with its own text); the access-token profile refuses an unknown or
repeated name (start, `check-config`, reload: `ISSUER_CONFIG_REFUSED`, the previous bindings kept). A token's role claims are ignored.

A2 · `httpapi.RequiredRole(method, pattern)` derives a route's role from the pattern the mux matched: GET and HEAD a viewer's; `.../release` a
publisher's; `POST /issues` an issuer's; `.../previews` a viewer's; any other change under the design paths (layouts, dashboards,
document-templates, document-backgrounds, document-images) a designer's; the rest (queries, snapshot jobs, exports, a job's cancel, document
previews and checks) a viewer's. `authenticated` refuses before the route runs: 403 `{"error":"AUTH_ROLE_DENIED"}`. A route added later falls
under the same rules. `GET /session` lists the effective roles.

A3 · The console says what a role refusal means (当前身份没有执行此操作的角色…请联系平台运营方在身份绑定中授予). Hiding the controls a role does not
allow is S12-A2.

Tests: `TestRequiredRoleOfEachRouteClass` (every route class), `TestRolesAreEnforcedOnTheRoutes` (a viewer, a designer, a publisher, no issuer,
an administrator alone, no roles named; the session's roles), `TestBindingsCarryRoles` (the file, the default, a reload to a role, an unknown
role refused and the previous kept), the session test's fields, DOM test S12-A. Docs: API, DEPLOYMENT, ACCESS_JWT.
