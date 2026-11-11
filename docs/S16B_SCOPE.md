# S16-B email as a second deliverer of a scheduled run's notification

2026-11-11. ADR 0020 decision 3 left email to a later slice ("another `Deliverer` behind the same notification"); this slice builds it, and
records what it fixed as the amendment at the end of docs/ADR_0020_SCHEDULED_REPORTS.md. The console's page of schedules, which S16-A had
also deferred, was built by S18-A.

B1 · The bindings' `email` (`identity/accessjwt`, `server/files.go`): a binding may name one address, an RFC 5322 addr-spec as `net/mail` reads
it, written bare (no display name, angle brackets or comment), of printable ASCII, at most 254 characters. It is validated when the
configuration is, as the other fields of a binding are (`accessjwt.Replace`, so a reload with a bad address keeps the previous bindings), and
the bindings file's strict reader refuses it with its own text. `Verifier.Emails(identity)` is where the identity's runs are mailed: the
distinct addresses of the bindings in force that name the identity, whatever their client, sorted, at most four; none without one, for another
issuer, or after a reload removed them; it does not ask whether the bindings still share a product, so that a run refused for that reason is
still the person's to hear of. No route writes it (ADR 0016); no notification carries it.

B2 · `delivery/email` (standard library: `net/smtp`, `net/mail`, `mime`): `email.New(Config)` checks the host (a name or an IP address, no
port), the port (0: 587), the from address, the console URL (https; http only to a loopback IP address with `LoopbackPlain`), STARTTLS (false
only to a loopback IP address with `LoopbackPlain`), the timeout (0: 10 s; at most 30), the username with its password (1..1024 bytes, from
the environment; a password without a username is refused) and the recipients function; `ErrConfig` names no value. `Deliver` is one SMTP
session within the timeout: connect, EHLO, STARTTLS when required (a server that does not offer it gets nothing, not even MAIL), AUTH PLAIN
when configured (`smtp.PlainAuth` itself refuses a connection that is neither TLS nor loopback), MAIL, one RCPT per address, DATA, QUIT. The
message is one text/plain part in UTF-8, base64 encoded: `From`, `To` (every address), `Subject` (`[IDP Reporting] 定时报表 <name> · <成功 |
失败 | 被拒绝 | 已取消>`, RFC 2047 encoded whole), `Date`, `Message-ID`, `Auto-Submitted: auto-generated`, `X-Reporting-Delivery` (the run's
id) and `X-Reporting-Event`; the body says the schedule's name (the store's owner-scoped read; its id when the store cannot give it), the
occurrence and the end in UTC, the outcome with the state word and the code, the export format, the run's and the schedule's ids, and the
link `<console_url>/console/#schedules`. No row, no query, no attachment, no artifact content, no snapshot or artifact id (the console shows
them). The recipients are the owner's addresses, each valid, distinct, at most four; an owner without one is `delivered` without a mail.
Outcomes: `delivered` (250), `throttled` (a 4yz reply; retried), `rejected` (a 5yz reply: the sender, a recipient or the credentials
refused; not retried), `server_error` (a reply out of order), `unreachable` (no reply: the connection, TLS, the timeout, no STARTTLS offered;
retried). Nothing of the address, the name, the body or the password is logged or returned.

B3 · `delivery.Multi` (`delivery/delivery.go`): the fan-out of named deliverers (`webhook`, `email`). `Deliver` gives the notification to each in
order, every one of them whatever the others answered, reports each attempt (the deliverer's word, the ids, the attempt's number, the outcome
and status) to the function it was composed with, and answers the first deliverer's result when all delivered, else the first outcome that is
retried (so that the scheduler's retry reaches the deliverer that could not take it now), else the first that is not. A retry repeats every
deliverer: a delivery is at least once by the run's id to each (ADR 0020 decision 4). `NewMulti` refuses no deliverer, one without a name or
a deliverer, and a name twice.

B4 · The server: `schedules.email` (`host`, `port`, `from`, `username`, `starttls`, `loopback_plain`, `timeout`, `console_url`; docs/DEPLOYMENT.md's
table) is validated by the file's own rules with the usual texts; `REPORTING_SMTP_PASSWORD` is required with a `username`, refused without
one and refused at step `secrets` when it is not 1..1024 bytes without control characters, never shown. The mailer reads the owner's addresses
through the verifier in force and a schedule's name through the store, so a reload changes the addresses as it changes what a run acts as.
The deliverers the deployment configured go behind one `delivery.Multi`; a deliverer that is not configured is absent; nothing else in the
scheduler changes. The start line says `webhook` and `email`. The `delivery` log line is one per deliverer and attempt: `deliverer`, its
`outcome` and `status`, `attempt`, the delivery's state after the attempt as a whole, the run's and the schedule's ids; never the address, the
name, the body, the endpoint or a secret. `reporting_deliveries_total{outcome}` keeps counting the attempt as a whole. The administrator's view
(S18-B) is unchanged: it says of the webhook what it said, and nothing of an address.

Deferred: the runs' retention (a sweep-role step, as the jobs'; ADR 0020's consequences gave it to S16-B, the amendment moves it on); a
per-tenant endpoint; the administrator's view saying whether an email is configured and a binding's address; a change to a binding's email
counted by the reload line (`bindings_changed` counts the products alone, as before); a `ca_file` for the SMTP server (the host's roots are
used); SMTPUTF8 addresses.

Tests: `TestEmailsAreTheBindingsAddresses` (distinct, sorted, bounded, not bound but mailed, every bad address refused and nothing changed);
`delivery/email`'s `TestTheConfigurationIsRefusedOutsideItsBounds` (addresses, hosts, console URLs, plain SMTP refused to any host that is not
a loopback IP address or without the option, a username without its password, the timeout, no value in a refusal),
`TestAMailCarriesTheNotificationForAPersonAndNothingOfTheReport` (the headers and the decoded body, no attachment or second part, no snapshot or
artifact id, a failed run's code, the id in place of a name the store cannot give, a name with CRLF reaches no header),
`TestTheRecipientsAreTheOwnersAddressesBounded` (none: delivered without a mail; seven named: four distinct valid ones),
`TestSTARTTLSIsRequiredAndTheCredentialsGoOverIt` (TLS with a certificate of the fake's, the password over it, an untrusted certificate
unreachable, a wrong password rejected 535, a server without STARTTLS gets nothing, plain loopback with credentials), `TestOutcomesOfReplies`
(550, 451, 554, a silent server within the timeout, a context that ends, a closed port) against an in-process fake SMTP server;
`delivery`'s `TestACompositionIsRefusedWithoutNamedDeliverers`, `TestEveryDelivererGetsTheNotificationAndTheResultIsTheOneToRetry`;
the server's `TestTheServerMailsARunsEndToTheOwnersAddressFromTheBindings` (both deliverers, the mail from the binding's address, the log's two
lines and what is not in it, the administrator's view), `TestTheEmailAloneAndWithoutAnAddressAndItsPassword`, the configuration refusals
(`TestConfigRefusalsCarryNoSecret`), the bindings file (`TestBindingsFileIsAStrictList`), the reference table held to the new fields
(`TestDeploymentExampleIsAValidConfiguration`). Docs: DEPLOYMENT, OPERATIONS, ACCESS_JWT, API, SECURITY_REVIEW, ADR 0017, ADR 0020.
