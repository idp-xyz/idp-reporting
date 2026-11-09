package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/tracecontext"
)

// tracing is an adapter that remembers the span each call's context carried and the traceparent it would send to its source product.
type tracing struct {
	r.Adapter
	mu       sync.Mutex
	catalog  []tracecontext.Span
	pages    []tracecontext.Span
	outgoing []string
	fail     error // ReadPage fails with it
}

func (a *tracing) IsSimulator() bool { return true }
func (a *tracing) seen(ctx context.Context, into *[]tracecontext.Span) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := tracecontext.FromContext(ctx); ok {
		*into = append(*into, s)
	}
	a.outgoing = append(a.outgoing, tracecontext.Traceparent(ctx))
}
func (a *tracing) DescribeCatalog(ctx context.Context, sc r.SourceContext) ([]r.Dataset, error) {
	a.seen(ctx, &a.catalog)
	return a.Adapter.DescribeCatalog(ctx, sc)
}
func (a *tracing) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	a.seen(ctx, &a.pages)
	if a.fail != nil {
		return r.SourcePage{}, a.fail
	}
	return a.Adapter.ReadPage(ctx, sc, p, cursor, limit)
}
func (a *tracing) snapshot() (catalog, pages []tracecontext.Span, outgoing []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]tracecontext.Span(nil), a.catalog...), append([]tracecontext.Span(nil), a.pages...), append([]string(nil), a.outgoing...)
}

func startTracing(t *testing.T, fail error) (*started, *tracing) {
	t.Helper()
	w := newWorld(t)
	a := &tracing{Adapter: w.sim, fail: fail}
	ctx, cancel := context.WithCancel(context.Background())
	lb := &logBuffer{}
	reg := Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return a, nil }}
	srv, err := Start(ctx, w.cfg, reg, w.getenv, lb, WithStoreForTests(NewMemoryStoreForTests(time.Now)), WithPollIntervalForTests(5*time.Millisecond))
	if err != nil {
		cancel()
		t.Fatalf("start: %v\n%s", err, lb.String())
	}
	s := &started{Server: srv, log: lb, cancel: cancel, w: w, base: "https://" + srv.Addr()}
	tr := newTLSTransport(w)
	s.client = &http.Client{Transport: tr, Timeout: 20 * time.Second}
	t.Cleanup(func() { cancel(); <-srv.Done(); tr.CloseIdleConnections() })
	return s, a
}

var traceparentShape = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)

const (
	callerTrace  = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan   = "00f067aa0ba902b7"
	callerHeader = "00-" + callerTrace + "-" + callerSpan + "-01"
	// plantedState is a tracestate a caller sends: none of it may reach the log, the answer or the adapter.
	plantedState = "congo=TRACESTATE-Zq7-never-shown,rojo=00f067aa0ba902b7"
)

// The server span: a valid traceparent is continued (same trace, a new span, the caller's flags), an absent or invalid one starts a new trace;
// the span is in the context the adapter is called with, is the one traceparent of the answer, and is on the access line. tracestate goes
// nowhere.
func TestTheServerSpanContinuesAValidTraceparentAndReachesTheAdapter(t *testing.T) {
	s, a := startTracing(t, nil)
	token := mint(t, s.w.keys[0], claims{})
	catalog := api + "/catalog?integration_id=" + url.QueryEscape(s.w.finance)
	cases := []struct {
		name    string
		headers []string
		cont    bool
		flags   string
	}{
		{"valid", []string{"traceparent", callerHeader, "tracestate", plantedState}, true, "01"},
		{"unsampled", []string{"traceparent", "00-" + callerTrace + "-" + callerSpan + "-00"}, true, "00"},
		{"future version", []string{"traceparent", "cc-" + callerTrace + "-" + callerSpan + "-01-future"}, true, "01"},
		{"absent", nil, false, "01"},
		{"uppercase", []string{"traceparent", "00-" + strings.ToUpper(callerTrace) + "-" + callerSpan + "-01", "tracestate", plantedState}, false, "01"},
		{"all-zero trace id", []string{"traceparent", "00-" + strings.Repeat("0", 32) + "-" + callerSpan + "-01"}, false, "01"},
		{"version ff", []string{"traceparent", "ff-" + callerTrace + "-" + callerSpan + "-01"}, false, "01"},
	}
	var answered []string
	for i, c := range cases {
		status, h, body := s.do(t, "GET", catalog, token, nil, c.headers...)
		if status != 200 {
			t.Fatalf("%s: %d %s", c.name, status, body)
		}
		got := h.Values("Traceparent")
		if len(got) != 1 || !traceparentShape.MatchString(got[0]) {
			t.Fatalf("%s: the answer's traceparent is %q", c.name, got)
		}
		if h.Get("Tracestate") != "" {
			t.Errorf("%s: the answer carries a tracestate", c.name)
		}
		span, _ := tracecontext.Parse(got[0])
		if (span.TraceIDString() == callerTrace) != c.cont || span.SpanIDString() == callerSpan || !strings.HasSuffix(got[0], "-"+c.flags) {
			t.Errorf("%s: answered %q", c.name, got[0])
		}
		seen, _, outgoing := a.snapshot()
		if len(seen) != i+1 || seen[i] != span {
			t.Errorf("%s: the adapter's context carried %+v, the answer %q", c.name, seen, got[0])
		} else if child, ok := tracecontext.Parse(outgoing[i]); !ok || child.TraceID != span.TraceID || child.SpanID == span.SpanID {
			t.Errorf("%s: the adapter would send %q", c.name, outgoing[i])
		}
		answered = append(answered, got[0])
	}
	// A request outside the API (health, the console, a 404) has its span too.
	for _, path := range []string{"/healthz", "/nope", api + "/nope"} {
		_, h, _ := s.do(t, "GET", path, "", nil, "traceparent", callerHeader)
		if got := h.Get("Traceparent"); !strings.HasPrefix(got, "00-"+callerTrace+"-") {
			t.Errorf("%s: %q", path, got)
		}
	}
	s.cancel()
	<-s.Done()
	byRequest := map[string]map[string]any{}
	for _, line := range s.log.events(t, "access") {
		if tid, _ := line["trace_id"].(string); len(tid) != 32 {
			t.Errorf("access line without a trace_id: %v", line)
		}
		if sid, _ := line["span_id"].(string); len(sid) != 16 {
			t.Errorf("access line without a span_id: %v", line)
		}
		byRequest[fmt.Sprint(line["trace_id"], "-", line["span_id"])] = line
	}
	for _, tp := range answered {
		span, _ := tracecontext.Parse(tp)
		line := byRequest[span.TraceIDString()+"-"+span.SpanIDString()]
		if line == nil || line["route"] != "GET "+api+"/catalog" {
			t.Errorf("no access line for the answer %q", tp)
		}
	}
	text := s.log.String()
	for _, never := range []string{"TRACESTATE-Zq7", "congo", callerSpan, strings.ToUpper(callerTrace), "future"} {
		if strings.Contains(text, never) {
			t.Errorf("the log carries %q", never)
		}
	}
}

// A job runs in a trace of its own: the adapter's calls for it carry a span, which is not the admitting request's, and a worker_failed line
// names that trace by its ids only.
func TestAJobRunsInATraceOfItsOwnAndItsFailureNamesIt(t *testing.T) {
	s, a := startTracing(t, fmt.Errorf("%w: the source said postgresql://u:%s@h", r.ErrUnavailable, testDSNPass))
	token := mint(t, s.w.keys[0], claims{})
	status, h, body := s.do(t, "POST", api+"/snapshot-jobs", token, contractsim.FinancialQuery(), "Idempotency-Key", "traced", "traceparent", callerHeader)
	var j r.JobView
	if status != 202 || json.Unmarshal(body, &j) != nil {
		t.Fatalf("snapshot: %d %s", status, body)
	}
	if !strings.HasPrefix(h.Get("Traceparent"), "00-"+callerTrace+"-") {
		t.Errorf("admission answered %q", h.Get("Traceparent"))
	}
	if j := s.waitJob(t, token, j.ID); j.State != "failed" {
		t.Fatalf("%+v", j)
	}
	s.cancel()
	<-s.Done()
	_, pages, _ := a.snapshot()
	if len(pages) == 0 {
		t.Fatal("the worker's read carried no span")
	}
	failures := s.log.events(t, "worker_failed")
	if len(failures) == 0 {
		t.Fatal("no worker_failed line")
	}
	f := failures[0]
	if f["code"] != "AUTHORITY_UNAVAILABLE" || f["trace_id"] != pages[0].TraceIDString() || f["span_id"] != pages[0].SpanIDString() {
		t.Errorf("worker_failed %v, the read's span %s %s", f, pages[0].TraceIDString(), pages[0].SpanIDString())
	}
	if pages[0].TraceIDString() == callerTrace {
		t.Error("the job claims the admitting request's trace, which the store does not keep")
	}
	for k := range f {
		switch k {
		case "ts", "level", "event", "code", "trace_id", "span_id":
		default:
			t.Errorf("worker_failed has the field %q", k)
		}
	}
	if strings.Contains(s.log.String(), testDSNPass) {
		t.Error("the error reached the log")
	}
}
