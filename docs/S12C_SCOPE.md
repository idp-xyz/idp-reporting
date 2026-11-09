# S12-C the tenant's shared template space and publication approval

2026-11-09. ADR 0018 decisions 2 and 3 (accepted with Q2: one space per tenant, Q3: approval for the shared space only). Templates first; the
dashboards' and layouts' shared spaces follow the same pattern later.

C1 · The space is an owner: `reporting.TeamOf(identity)` = the identity's tenant and issuer with the reserved subject `~team`. No binding may
name it (the bindings file and the access-token profile refuse it), so no person is the team; row-level security keeps the space to its tenant
as it keeps any identity's rows; who of the tenant reads, changes and publishes it is the server's roles (S12-A). No migration.

C2 · Approval. `privatedocs.Entry.SavedBy` records who saved a version in a shared space (`reporting.ActorDigest`, the SHA-256 of the identity),
`SaveAs` and `PublishAs`; publishing a version by its saver, or a version without a saver, is `privatedocs.ErrApprovalRequired` (409
`APPROVAL_REQUIRED`); a withdrawal is any publisher's. The routes never answer the digest: a team version's read says `saved_by_me`.

C3 · Routes. `mountSpacedDocuments` is the private documents' mount over a space; the templates' routes (`mountTemplateRoutes`: list, read,
save, catalog, release, published, previews, issue) are mounted for the caller's own space and again under `/api/reporting/v1/team/...` with
`POST /team/issues`. The role and audit rules read a team route as the same route (`routePath`). An issue of a team template freezes
`Job.TemplateSpace = "team"` and the worker checks the publication in the space. The samples copy into one's own space.

C4 · Console. `window.reportingTemplateSpace` (documents.js) is the space the console works in; the template centre's 我的模板 / 团队模板 switch
sets it, and the centre, the studio, the previews and the official issue use its paths. Sign-out returns to one's own space. An approval refusal
is told as one.

Tests: `TestPublicationOfASharedVersionIsAnotherPersonsApproval`, `TestTeamSpaceIsSharedWithinTheTenantAndPublicationIsAnApproval` (a colleague
reads, the saver is refused, the colleague approves, another tenant does not read, the audit, the role rules of the team routes), DOM test
S12-C; openapi.json documents the team routes. Docs: API, ACCESS_JWT, SECURITY_REVIEW, CONSOLE.
