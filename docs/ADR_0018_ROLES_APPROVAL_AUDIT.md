# ADR 0018 · Roles, publication approval, a shared design space and an audit of people's actions

Date: 2026-11-09. Status: **proposed** (the four questions at the end are the product owner's; the recommended answers are what S12 builds
unless they are changed). ADR 0017's S12.

## Context

Today an identity is a tenant, an issuer and a subject with a list of products (ADR 0016's bindings). Everything else is the same for every
identity: whoever is bound to a product may query it, freeze snapshots and export them, design layouts, dashboards and templates, publish and
withdraw versions, and issue official documents. Designs belong to the identity that made them: nobody else in the tenant can see, review or
use them. The platform records jobs and issue records, which are an audit of what was produced, but not of what people did: who changed a
template, who published version 4, who downloaded a file.

An enterprise expects four things the platform does not have: roles that separate designing, publishing and issuing; a publication that a
second person approves; designs a team shares; and an audit of people's actions that an administrator can read. ADR 0016 keeps who may do
what in operator configuration; the access token carries no role and a token's own claims never widen what the bindings grant.

## Decision

1. **Roles come from the bindings, like products.** A binding gets an optional `roles` list of five fixed names:
   - `viewer`: query, freeze, export and read published reports, dashboards and templates (what every bound identity may do today short of
     designing and issuing);
   - `designer`: create and change layouts, dashboards, templates, backgrounds and images, and their catalog;
   - `publisher`: publish and withdraw versions;
   - `issuer`: issue official documents;
   - `administrator`: read the tenant's audit.
   A binding without `roles` keeps today's rights: `viewer, designer, publisher, issuer` (recommended answer to Q1). A role in a token is
   ignored. `GET /session` lists the roles; the console hides what the roles do not allow, and the server refuses it (403
   `AUTH_ROLE_DENIED`) whatever the console shows.
2. **A tenant's shared design space.** Beside each identity's own designs there is one space per tenant and issuer (recommended answer to
   Q2): designers of the tenant save to it, viewers read its published versions, publishers publish its versions. In the database the space is
   an owner with an empty subject; a new row-level security policy lets a connection that says its tenant and issuer see the space's rows. A
   design moves into the space by being saved there (a copy keeps its history out of the space).
3. **Publication approval** (recommended answer to Q3: on for the shared space, off for one's own designs): a version of the shared space can
   be published only by a publisher who did not save it. Each version records who saved it (a digest of the identity in the version, never shown
   to another identity in clear).
4. **An audit of people's actions.** Migration 012 adds `audit_events (tenant, issuer, subject, at, action, object kind, object id, version,
   outcome)`, append-only: the application role inserts and never updates or deletes; the sweep never touches it. Recorded: design saved,
   catalog changed, version published or withdrawn, issue admitted, artifact downloaded, export admitted, sign-in refused for a missing role.
   An administrator reads the tenant's events (`GET /audit?limit=&before=`), others their own. An event that cannot be written fails the
   publication, issue or approval it records (recommended answer to Q4: fail closed for those three, best effort for downloads and catalog
   changes).

## Slices

| Slice | What |
|---|---|
| S12-A | Roles in the bindings, `Principal.Roles`, enforcement in the HTTP layer, `GET /session` roles, the console hides what is not allowed |
| S12-B | Migration 012 `audit_events`, the recorded actions, `GET /audit`, an audit page for administrators |
| S12-C | The tenant's shared design space (row-level security policy, the console's 我的 / 团队 switch), publication approval |

## Questions for the product owner

- **Q1** A binding without `roles`: today's rights (recommended, nothing changes on upgrade), or `viewer` only (safer default, every existing
  binding must be edited)?
- **Q2** The shared space: one per tenant (recommended, simple), or named team spaces with their own members?
- **Q3** Approval: required for the shared space only (recommended), for every publication, or a per-deployment switch?
- **Q4** The audit: fail closed for publication, approval and issue (recommended), or best effort everywhere?
