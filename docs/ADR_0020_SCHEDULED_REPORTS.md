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

## Amendment 2026-11-11 (S16-B): email as a second deliverer

Decision 3 left email to a later slice, "another `Deliverer` behind the same notification". S16-B (docs/S16B_SCOPE.md) builds it, and the
decision holds as written; what follows fixes what it left open.

1. **An email carries the same notification, written for a person.** `delivery/email` sends, when a run ends, one text/plain mail with the
   subject `[IDP Reporting] 定时报表 <schedule name> · <outcome>` and a body that says the schedule's name, the occurrence, the outcome and
   its code, the run's and the schedule's ids, and a link to the console (`<console_url>/console/#schedules`), where the owner downloads the
   file with their own access, the source authority asked again. The name may appear here, unlike in the webhook: the recipient is the owner,
   who wrote it. **No row, no query, no attachment, no artifact content**: the file is read through the platform under the owner's authority,
   as decision 3 says of the receiver. The subject is RFC 2047 encoded and the body base64, so nothing the person wrote reaches a header as it is.
2. **The recipient comes from the bindings file, and from nowhere else.** A binding gets an optional `email`: one address, an RFC 5322
   addr-spec of printable ASCII, at most 254 characters, validated at load as the other fields of a binding are (`identity/accessjwt`), read
   through the verifier in force (`Verifier.Emails`), so that a reload changes it as it changes what a run acts as. No route writes it (ADR
   0016's rule) and no notification carries it. A run whose owner's bindings name no address gets no mail, and the scheduler records nothing
   special for it: the delivery is `delivered` without a mail, and the webhook, if configured, still goes. With several bindings of the
   identity, the mail goes to each distinct address they name, at most four (sorted, the first four).
3. **Configuration under `schedules.email`**: `host` (required), `port` (default 587), `from` (required, an addr-spec), `username`
   (optional), `starttls` (default `true`; `false` only together with `loopback_plain: true`, which allows plain SMTP to a loopback IP address
   for tests, as the webhook's `loopback_http`), `timeout` (default 10 s, at most 30), `console_url` (required: the https base the links use;
   `http://` only to a loopback IP address with `loopback_plain`). The password is the environment's `REPORTING_SMTP_PASSWORD`, required
   with a username and refused without one, never in the file. The implementation is the standard library's `net/smtp`: STARTTLS through
   `smtp.Client.StartTLS` with `tls.Config{ServerName: host}` (a server that does not offer it gets nothing, and the attempt is `unreachable`),
   SASL PLAIN only over TLS or to loopback. One `Deliver` is one SMTP session and one attempt; the scheduler's policy retries it as it retries
   the webhook: a 4yz reply is `throttled`, no reply (the connection, TLS, the timeout) `unreachable`, both retried; a 5yz reply (the sender,
   a recipient or the credentials refused) is `rejected` and not.
4. **Composition.** When both `schedules.webhook` and `schedules.email` are configured, the scheduler gets one fan-out deliverer
   (`delivery.Multi`) that attempts each, every one of them whatever the others answered, and reports failure if any failed (the outcome that
   is retried, when there is one), so that the scheduler's one retry covers both; a retry repeats every deliverer, and a delivery stays at
   least once by the run's id, to each of them. A deliverer that is not configured is simply absent. Nothing else in the scheduler changes.
5. **Logging**: one `delivery` line per deliverer and attempt, with the run's and the schedule's ids, the deliverer's word (`webhook`, `email`),
   its outcome and status, and the delivery's state after the attempt as a whole; never the address, the name or the body.

The runs' retention, which the consequences above gave to S16-B, is a later slice's, as is a per-tenant endpoint.
