# S03-C3 owner-private dashboard publication

2026-10-27. Baseline 7b4208e28a4526cc5a01b520e87dc161855c668b.
Expose dashboard GET /{id}/release, POST /{id}/release and GET /{id}/published
under the existing opt-in /api/reporting/v1/dashboards API. Reuse privatedocs
release primitives: immutable saved target versions, revision CAS, 64 events,
server UTC timestamps, normal close/reopen persistence. Save drafts must not
change the published pointer. Rollback is an explicit publish of an old version.
Publish and published-read recheck the actual target widgets against current
source catalog. History and withdrawal check current dataset binding, allowing
withdrawal after field removal; revoked integration/dataset still denies access.
All disclosure is tenant/issuer/subject private; no public sharing or approval flow.
Strict bounded action/version/expected_revision body; errors must not mutate state.

Console: read release state/history, publish current clean saved version, withdraw,
open published version. Require refreshed release state before mutation. Save/open
invalidates release state. Dirty metadata cannot publish. Conflicts or uncertain
responses require explicit history refresh, no automatic retry. Stale responses
cannot alter another query/session. Preserve authorized result on metadata errors.
Clear release metadata on query invalidation/logout. Open published version checks
current result binding/fields and renders only current authorized result values;
publication contains configuration only, no frozen data or entitlement grant.

No core/source/storage schema changes, report publication regression, charts,
multi-dataset dashboards, sharing, PDF, production or real identity integration.
Browser/visual/mobile and PG reruns remain unqualified. Real integration last.
Validate public HTTP/catalog change, store reopen/CAS, and software DOM loopback.
