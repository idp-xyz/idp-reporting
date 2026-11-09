package tracecontext

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

const (
	traceHex = "4bf92f3577b34da6a3ce929d0e0e4736"
	spanHex  = "00f067aa0ba902b7"
	valid    = "00-" + traceHex + "-" + spanHex + "-01"
)

var shape = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)

func TestParseAcceptsTheSpecificationsExamples(t *testing.T) {
	for _, c := range []struct {
		in    string
		flags byte
	}{
		{valid, 0x01},
		{"00-" + traceHex + "-" + spanHex + "-00", 0x00},
		// Unknown flags are dropped: only the sampled bit is kept.
		{"00-" + traceHex + "-" + spanHex + "-ff", 0x01},
		{"00-" + traceHex + "-" + spanHex + "-02", 0x00},
		// A higher version is read as 00 for its first 55 characters; what follows a dash is ignored.
		{"01-" + traceHex + "-" + spanHex + "-01", 0x01},
		{"cc-" + traceHex + "-" + spanHex + "-01-what-the-future-holds", 0x01},
		{"fe-" + traceHex + "-" + spanHex + "-00-", 0x00},
	} {
		s, ok := Parse(c.in)
		if !ok {
			t.Errorf("%q refused", c.in)
			continue
		}
		if s.TraceIDString() != traceHex || s.SpanIDString() != spanHex || s.Flags != c.flags {
			t.Errorf("%q: %s %s %02x", c.in, s.TraceIDString(), s.SpanIDString(), s.Flags)
		}
	}
}

func TestParseRefusesWhatTheSpecificationRefuses(t *testing.T) {
	zeroTrace := strings.Repeat("0", 32)
	zeroSpan := strings.Repeat("0", 16)
	for name, in := range map[string]string{
		"empty":                   "",
		"all-zero trace id":       "00-" + zeroTrace + "-" + spanHex + "-01",
		"all-zero parent id":      "00-" + traceHex + "-" + zeroSpan + "-01",
		"uppercase trace id":      "00-" + strings.ToUpper(traceHex) + "-" + spanHex + "-01",
		"uppercase parent id":     "00-" + traceHex + "-" + strings.ToUpper("00f067aa0ba902bF") + "-01",
		"uppercase flags":         "00-" + traceHex + "-" + spanHex + "-0A",
		"uppercase version":       "0A-" + traceHex + "-" + spanHex + "-01",
		"version ff":              "ff-" + traceHex + "-" + spanHex + "-01",
		"short trace id":          "00-" + traceHex[1:] + "-" + spanHex + "-01",
		"long trace id":           "00-" + traceHex + "a-" + spanHex + "-01",
		"short parent id":         "00-" + traceHex + "-" + spanHex[1:] + "-01",
		"long parent id":          "00-" + traceHex + "-" + spanHex + "a-01",
		"one-digit flags":         "00-" + traceHex + "-" + spanHex + "-1",
		"three-digit flags":       "00-" + traceHex + "-" + spanHex + "-001",
		"version 00 with more":    valid + "-00",
		"version 00 with a space": valid + " ",
		"short version":           "0-" + traceHex + "-" + spanHex + "-01",
		"non-hex version":         "0g-" + traceHex + "-" + spanHex + "-01",
		"non-hex trace id":        "00-" + "4bf92f3577b34da6a3ce929d0e0e473z" + "-" + spanHex + "-01",
		"non-hex flags":           "00-" + traceHex + "-" + spanHex + "-0x",
		"underscores":             "00_" + traceHex + "_" + spanHex + "_01",
		"leading space":           " " + valid,
		"future version, no dash": "01-" + traceHex + "-" + spanHex + "-01x",
		"future version, short":   "01-" + traceHex + "-" + spanHex + "-1",
		"future version, zeros":   "01-" + zeroTrace + "-" + spanHex + "-01",
		"plus sign in hex":        "00-" + traceHex + "-+0f067aa0ba902b7-01",
	} {
		if s, ok := Parse(in); ok || s != (Span{}) {
			t.Errorf("%s: %q accepted as %+v", name, in, s)
		}
	}
}

func TestStringIsVersion00AndRoundTrips(t *testing.T) {
	s, _ := Parse(valid)
	if s.String() != valid {
		t.Errorf("%q", s.String())
	}
	future, _ := Parse("cc-" + traceHex + "-" + spanHex + "-03-xyz")
	if future.String() != valid {
		t.Errorf("a higher version is not sent as 00 with the sampled flag only: %q", future.String())
	}
	n := New()
	if !n.Valid() || n.Flags != FlagSampled || !shape.MatchString(n.String()) {
		t.Errorf("new: %q", n.String())
	}
	if back, ok := Parse(n.String()); !ok || back != n {
		t.Errorf("round trip: %+v %+v", back, n)
	}
	if New().TraceID == n.TraceID {
		t.Error("two new traces share an id")
	}
}

func TestIncomingContinuesOneValidHeaderAndStartsANewTraceOtherwise(t *testing.T) {
	s, continued := Incoming([]string{valid})
	if !continued || s.TraceIDString() != traceHex || s.SpanIDString() == spanHex || s.Flags != FlagSampled {
		t.Errorf("continued %v: %s %s %02x", continued, s.TraceIDString(), s.SpanIDString(), s.Flags)
	}
	unsampled, _ := Incoming([]string{"00-" + traceHex + "-" + spanHex + "-00"})
	if unsampled.Flags != 0 {
		t.Error("the caller's unsampled decision is not passed on")
	}
	for name, values := range map[string][]string{
		"absent":  nil,
		"invalid": {"00-" + strings.ToUpper(traceHex) + "-" + spanHex + "-01"},
		"two":     {valid, valid},
	} {
		s, continued := Incoming(values)
		if continued || !s.Valid() || s.TraceIDString() == traceHex || s.Flags != FlagSampled {
			t.Errorf("%s: continued %v, %s", name, continued, s.String())
		}
	}
}

func TestTheContextCarriesTheSpanAndFormatsTheChildCall(t *testing.T) {
	ctx := context.Background()
	if _, ok := FromContext(ctx); ok || Traceparent(ctx) != "" {
		t.Error("an empty context carries a span")
	}
	if WithSpan(ctx, Span{}) != ctx {
		t.Error("an invalid span was stored")
	}
	s, _ := Parse(valid)
	ctx = WithSpan(ctx, s)
	got, ok := FromContext(ctx)
	if !ok || got != s {
		t.Fatalf("%+v %v", got, ok)
	}
	out := Traceparent(ctx)
	child, ok := Parse(out)
	if !ok || child.TraceID != s.TraceID || child.SpanID == s.SpanID || child.Flags != s.Flags || !strings.HasPrefix(out, "00-"+traceHex+"-") || !strings.HasSuffix(out, "-01") {
		t.Errorf("outgoing %q", out)
	}
	if Traceparent(ctx) == out {
		t.Error("two calls share a span id")
	}
}
