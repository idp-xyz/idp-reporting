# ADR 0020 · Scheduled reports and their delivery

Date: 2026-11-09. Status: **accepted** (built in S16-A, docs/S16A_SCOPE.md). ADR 0017's S16; it depends on S12 (roles and the audit, ADR 0018).

## Context

A person can freeze a report and export it when they ask. An enterprise reporting platform is also expected to produce a report on a calendar
(every Monday at 07:30 in Shanghai, the first of the month) and to tell some other system when it is ready. The platform's rules still hold:
the source product's authority decides every read, as of the moment of the read; who may do what is the bindings' (ADR 0016, ADR 0018); the
connections and anything else that reaches outside are the deployment's, never written through the API; nothing of a report's values leaves
the platform except to its owner.

Four things had to be decided: what a run is, as whom it acts, what a delivery carries and to whom, and how several servers share the work.

## Decision

1. **A run is the owner's own snapshot job, and optionally its export.** A schedule freezes a query of the published catalog (checked against
   the catalog when it is made, no row read) and, if asked, an export format. At each occurrence the scheduler calls `Service.SubmitSnapshot`
   with the schedule's query (its `as_of` set by the schedule's rule: the occurrence's date, the day before, or the query's own) under the
   Idempotency-Key `schedule:<run id>`, waits for the job, then `Service.SubmitExportFormat` the same way. The jobs are the owner's: the same
   admission, quotas and listing as the owner's own, cancellable by the owner. **An official issue is never scheduled**: an issue reserves a
   number at the source product and is a person's decision (ADR 0003); a schedule that issues would issue without one.
2. **A run acts as its owner's binding in force at the run.** Nothing of the person's session is kept: the scheduler asks the verifier for the
   identity's binding now (`Verifier.Bound`; with several bindings of the identity, one per client, only what all of them grant) and refuses
   the run (`refused`, `AUTH_SCOPE_DENIED`) when there is none or it no longer holds the product, and (`AUTH_ROLE_DENIED`) when it lacks
   `viewer`, the role a person needs to submit the same jobs. The source product decides the read as for any job (`PrepareRead`,
   `AuthorizeCurrent`), and a refusal there is the run's refusal. Making, pausing, resuming and deleting a schedule are a `viewer`'s too.
3. **A delivery is a notification, signed, to the deployment's one endpoint.** When a run ends, the scheduler POSTs to the endpoint the
   configuration names (`schedules.webhook.url`; https, plain http only to a loopback IP address for tests) a JSON notification: the run's and
   the schedule's ids, the owner (tenant, issuer, subject), the product, the occurrence, the outcome and its code, the ids of the snapshot and
   the artifact. No row, no query, no name. It is signed with HMAC-SHA256 over the exact body under a secret from the environment
   (`REPORTING_WEBHOOK_SECRET`), redirects are not followed, and a delivery the receiver cannot take now is retried with a doubling wait, at
   most six times. The receiver that wants the file reads it with the owner's own access (`GET /artifacts/{id}/content`), where the source
   authority is asked again. No route reads or writes the endpoint (ADR 0016's rule). Email, a file drop or a per-tenant endpoint are a later
   slice: each is another `Deliverer` behind the same notification.
4. **The store's locks and leases share the work.** Every server runs the scheduler. A due schedule is taken with `FOR UPDATE SKIP LOCKED`,
   its run inserted (unique per schedule and occurrence) and its next occurrence set in one transaction, and only when it has no open run: no
   occurrence runs twice, and after a downtime the missed occurrences are caught up once, by the oldest. Runs are advanced under a lease and a
   generation, as jobs are. A delivery is at least once: the run's id is the receiver's key.

## Consequences

- A person's schedule keeps working only while the person is bound and holds the product; an operator who removes a binding stops their
  schedules at the next run, which ends `refused` and is delivered as such.
- The jobs a schedule submits count against the owner's quotas (S04-D) like the owner's own; a schedule that runs into a quota fails that run
  (`QUOTA_EXCEEDED`) and runs again at its next occurrence.
- The application role never deletes (S04-A): a deleted schedule is a tombstone, and runs accumulate. Their retention is S16-B's.
- The webhook's secret is a deployment secret like the store's connection string: rotating it is a restart, rolled.
