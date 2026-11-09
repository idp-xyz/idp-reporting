// Package tracecontext is the W3C Trace Context (https://www.w3.org/TR/trace-context/) of a request: the traceparent header parsed
// strictly, a span of this process, and the context.Context that carries it to the service and the source adapters. It depends on the
// standard library only and records nothing: there is no exporter, no sampler and no span store. What the platform does with a trace is
// log its ids (docs/DEPLOYMENT.md "Logging") and hand it to an adapter, which may send Traceparent(ctx) to its source product.
//
// tracestate is not read, kept or sent: it is a vendor's opaque list that a caller could fill with anything, and the source products
// sit behind this platform's trust boundary. This is a deliberate departure from the specification's "MUST propagate" (docs/S14A_SCOPE.md).
package tracecontext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// Header is the name of the header, as the specification spells it.
const Header = "traceparent"

// FlagSampled is the only trace flag of version 00 that this package keeps: the caller's sampled decision, passed on unchanged. Every other
// bit is cleared, since a version 00 header must not set a flag its sender does not know.
const FlagSampled byte = 0x01

// Span is one span of a trace: the trace id shared by every span of it, this span's id and the flags. The zero Span is not valid.
type Span struct {
	TraceID [16]byte
	SpanID  [8]byte
	Flags   byte
}

// Valid says whether neither id is all zero (the specification's invalid values).
func (s Span) Valid() bool { return s.TraceID != [16]byte{} && s.SpanID != [8]byte{} }

// TraceIDString is the trace id in 32 lowercase hex digits.
func (s Span) TraceIDString() string { return hex.EncodeToString(s.TraceID[:]) }

// SpanIDString is the span id in 16 lowercase hex digits.
func (s Span) SpanIDString() string { return hex.EncodeToString(s.SpanID[:]) }

// String is the span as a version 00 traceparent value: the header a callee of this span receives, with this span as its parent.
func (s Span) String() string {
	b := make([]byte, 0, 55)
	b = append(b, "00-"...)
	b = hex.AppendEncode(b, s.TraceID[:])
	b = append(b, '-')
	b = hex.AppendEncode(b, s.SpanID[:])
	b = append(b, '-')
	b = hex.AppendEncode(b, []byte{s.Flags & FlagSampled})
	return string(b)
}

// Child is a new span of the same trace with a new random span id and the same flags.
func (s Span) Child() Span {
	s.SpanID = newSpanID()
	return s
}

// New is the root span of a new trace: random ids and the sampled flag set (the platform logs every request it answers).
func New() Span {
	var s Span
	for s.TraceID == [16]byte{} {
		_, _ = rand.Read(s.TraceID[:])
	}
	s.SpanID = newSpanID()
	s.Flags = FlagSampled
	return s
}

func newSpanID() [8]byte {
	var id [8]byte
	for id == [8]byte{} {
		_, _ = rand.Read(id[:])
	}
	return id
}

// Parse reads a traceparent value by the rules of the specification, section "traceparent Header Field Values" and "Versioning of
// traceparent": the version is two lowercase hex digits other than ff; version 00 is exactly 55 characters; a higher version is at least 55
// characters, read as version 00 for its first 55, and a longer one has a dash at the 56th, the rest being ignored. The ids are lowercase
// hex and not all zero; the flags are two lowercase hex digits, of which only the sampled bit is kept. The parent id becomes the returned
// Span's SpanID: it is the caller's span, never this process's.
func Parse(v string) (Span, bool) {
	var s Span
	if len(v) < 55 {
		return s, false
	}
	var version [1]byte
	if !lowerHex(version[:], v[0:2]) || version[0] == 0xff || v[2] != '-' {
		return s, false
	}
	if version[0] == 0 && len(v) != 55 || version[0] != 0 && len(v) > 55 && v[55] != '-' {
		return s, false
	}
	var flags [1]byte
	if !lowerHex(s.TraceID[:], v[3:35]) || v[35] != '-' || !lowerHex(s.SpanID[:], v[36:52]) || v[52] != '-' || !lowerHex(flags[:], v[53:55]) {
		return Span{}, false
	}
	if !s.Valid() {
		return Span{}, false
	}
	s.Flags = flags[0] & FlagSampled
	return s, true
}

// lowerHex decodes src into dst when src is exactly 2*len(dst) lowercase hex digits; uppercase is refused, as the specification requires.
func lowerHex(dst []byte, src string) bool {
	if len(src) != 2*len(dst) {
		return false
	}
	for i := 0; i < len(src); i++ {
		if c := src[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	_, err := hex.Decode(dst, []byte(src))
	return err == nil
}

// Incoming is the server span of a request whose traceparent header has the given values: a child of the caller's span when there is exactly
// one value and it parses, otherwise the root span of a new trace. The second result says whether the caller's trace was continued.
func Incoming(values []string) (Span, bool) {
	if len(values) == 1 {
		if parent, ok := Parse(values[0]); ok {
			return parent.Child(), true
		}
	}
	return New(), false
}

type key struct{}

// WithSpan returns a context that carries the span. An invalid span is not stored: ctx is returned as it is.
func WithSpan(ctx context.Context, s Span) context.Context {
	if !s.Valid() {
		return ctx
	}
	return context.WithValue(ctx, key{}, s)
}

// FromContext is the span ctx carries, if any.
func FromContext(ctx context.Context) (Span, bool) {
	s, ok := ctx.Value(key{}).(Span)
	return s, ok
}

// Traceparent is the traceparent value of a call that the span in ctx makes to another service (an adapter's request to its source
// product): the same trace, a new span id for the outgoing call, the span's flags. It is "" when ctx carries no span; the caller then
// sends no header.
func Traceparent(ctx context.Context) string {
	s, ok := FromContext(ctx)
	if !ok {
		return ""
	}
	return s.Child().String()
}
