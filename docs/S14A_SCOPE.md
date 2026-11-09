# S14-A request tracing with W3C Trace Context

2026-11-09. Closes the "no tracing" gap of docs/SECURITY_REVIEW.md as far as the log goes: an operator follows one request through the
log by its trace id, and a source adapter can forward the trace to its source product. Standard library only; no OpenTelemetry, no
exporter, no new module.

A1 · The package. `tracecontext/` (beside `identity/` and `transport/`, outside the domain-neutral `reporting/`, importable by an adapter
in another module): `Parse` reads a `traceparent` value by the specification's rules (version two lowercase hex digits, `ff` refused;
version `00` exactly 55 characters; a higher version at least 55, read as `00` for its first 55, a dash at the 56th when longer, the rest
ignored; trace id 32 and parent id 16 lowercase hex digits, neither all zero; flags two lowercase hex digits). Only the two ids and the
sampled bit are kept; other flag bits are cleared, and every header sent is version `00`. `Incoming` is the server span: a child of the
caller's span when exactly one header value parses, else `New`, the root of a new trace (crypto/rand ids, sampled). `WithSpan` and
`FromContext` carry the span in a `context.Context`; `Traceparent(ctx)` is the header of a call the span makes (same trace, a new span id,
the same flags; `""` without a span).

A2 · The server. `server/http.go`'s `access` handler, the outermost one, makes the span for every request (API, health, console, a 404),
puts it in the request's context (which `reporting.Service` passes to the adapters), answers it as the one `traceparent` header of the
response, and logs `trace_id` and `span_id` on the `access` line. The `traceparent` header is the only header read; nothing of it but the
validated ids reaches the log. The access line is the only log line made in the scope of a request; `http_server_error` has no request.

A3 · tracestate is ignored: not read, logged, answered or passed on. It is an opaque vendor list a caller (a browser, an embedding page)
could fill with anything, and the source products sit behind the platform's trust boundary; bounding it to the specification's grammar
would make it well-formed, not trustworthy. This departs from the specification's "MUST propagate" on purpose.

A4 · Jobs. The worker runs each claim in the root span of a new trace: the adapter's calls for the job carry one `traceparent`, and a
`worker_failed` line carries `trace_id` and `span_id`. The admitting request's trace id is not carried: a job is persisted (its JSONB
document and columns) and may be claimed by another process, so carrying it means storing it with the job, a change of the stored job's
shape that this slice does not make.

What a caller can and cannot do: a valid `traceparent` chooses the trace id the log shows for its request, so the trace id correlates and
proves nothing; `request_id` stays the server's own random id. The answer's `traceparent` names the server span and nothing else.

Tests: `tracecontext/tracecontext_test.go` (the specification's valid values and higher versions; all-zero ids, uppercase, wrong lengths,
version `ff`, a version `00` with trailing data, flags; the version `00` round trip; `Incoming` with one, two, an invalid and no value; the
context and the child call); `server/trace_test.go` (`TestTheServerSpanContinuesAValidTraceparentAndReachesTheAdapter`: the answer's header,
the span in the adapter's context and its outgoing header, health and 404, the access lines, a planted tracestate nowhere in the log or
the answer; `TestAJobRunsInATraceOfItsOwnAndItsFailureNamesIt`: the worker's span in the adapter's read and on `worker_failed`, not the
admitting request's, no other field); `TestSessionOverTLSWithRealTokens` allows the two new access fields and still scans every line for
the planted secrets.

Not in this slice: a span exporter or any recording of spans; the admitting request's trace on a job (a stored field, follow-up);
`traceparent` from the demo command (`cmd/reporting-demo` has no access log); `Access-Control-Expose-Headers` for a cross-origin caller
(the console and the embed page are same-origin).
