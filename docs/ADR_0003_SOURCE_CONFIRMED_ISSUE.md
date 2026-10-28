# ADR 0003 · Formal documents are issued under the source product's authority

Date: 2026-10-28. Status: accepted. Supersedes the numbering and issuing rules of docs/S03G_SCOPE.md.

## Context

The architecture baseline assigns the business authority over formal documents to the source product:
"来源产品负责事实、业务字段/口径、当前业务权限、金额计算、审批、编号和正式出具确认。平台保留正式文件及内容版本，
不独立批准业务单据或修改交易事实" (docs/s00/ARCHITECTURE.md). The interface baseline names the capability:
"正式单据的 PrepareDocument/ConfirmIssue 是后续独立能力，必须由来源产品授权" (docs/s00/INTERFACES.md), and the
migration plan repeats it for S03: "正式单据保留来源产品的审批与出具确认" (docs/s00/MIGRATION.md).

S03-G (PR #9) shipped official issue with the platform as the authority: the platform store assigned
consecutive numbers per tenant and integration, and a published template, a frozen snapshot and a row were
enough to issue; the source product was never asked and never told. That contradicts all three baselines.
The two-axis reviews of S03-G checked the slice against its own scope, not against the baseline, and missed
it. No production store exists yet, so the correction costs a migration on test fixtures only.

## Decision

1. The product authorizes every issue, owns the number and confirms the file. The platform renders the
   document and keeps the formal file and its record; it never invents a number and never issues alone.
2. The capability is an optional adapter interface, `reporting.DocumentAuthority`, with three calls, each
   idempotent by the platform's job id: `PrepareDocument` (authorize, check the row against the product's
   own source snapshot, apply the product's approval rules, reserve the number and the issue time),
   `ConfirmIssue` (bind the reserved number to the file's digest and page count) and `VoidIssue` (void
   whatever the product holds for the job, reserved or confirmed, with the platform's reason).
3. The platform renders deterministically from what is frozen and what the product reserved (template,
   snapshot, row, number, issue time, record id), with the product's issue time as the PDF's creation date,
   so that a job re-run after a crash produces the same bytes and confirms idempotently.
4. Cross-system atomicity is not promised (README: no cross-system commit atomicity). Instead every
   outcome is reconcilable: a job that fails after reserving or confirming voids the product's reservation
   by job id; if the void cannot be delivered the job fails with `ISSUE_UNRECONCILED`, and the product can
   find the reservation by the job id. A running issue job cannot be cancelled (it finishes, or fails and
   voids); a queued one can.
5. Platform numbering is withdrawn: migration 004 drops `issue_numbers`; it refuses to run while
   platform-numbered (S03-G) issue jobs or records exist, because they were never confirmed by a product.

## Consequences

Issuing requires an adapter that implements the capability; an integration without it answers 422. The
contract simulators implement it so the console, the demo and the tests can issue; real products implement
it when they are integrated (last, per the plan). Numbers are strings chosen by the product. The record
keeps the product's reservation and confirmation references so either side can reconcile.
