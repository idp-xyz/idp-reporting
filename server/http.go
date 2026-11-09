package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"idp.local/reporting/tracecontext"
	"idp.local/reporting/transport/httpapi"
)

// tracker counts the requests whose handler is running, for the one question a drain asks of it: are there any left.
type tracker struct {
	mu      sync.Mutex
	n       int
	waiting chan struct{}
}

func (t *tracker) enter() { t.mu.Lock(); t.n++; t.mu.Unlock() }
func (t *tracker) leave() {
	t.mu.Lock()
	t.n--
	if t.n == 0 && t.waiting != nil {
		close(t.waiting)
		t.waiting = nil
	}
	t.mu.Unlock()
}
func (t *tracker) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.n }

// idle returns a channel that is closed when no handler is running (at once, when none is).
func (t *tracker) idle() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n == 0 {
		c := make(chan struct{})
		close(c)
		return c
	}
	if t.waiting == nil {
		t.waiting = make(chan struct{})
	}
	return t.waiting
}

// statusRecorder remembers the status of the answer. Unwrap lets http.ResponseController reach the connection's own writer.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

var requestSeq atomic.Uint64

// requestID is 16 random bytes in hex: it links an access line to a client's report and says nothing about the request.
func requestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		n := requestSeq.Add(1)
		for i := 0; i < 8; i++ {
			b[15-i] = byte(n >> (8 * i))
		}
	}
	return hex.EncodeToString(b[:])
}

// knownMethods are logged by name; any other method a client invents is logged as OTHER.
var knownMethods = map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true}

// access is the outermost handler: it counts the request in, gives it an id (also the X-Request-Id of the answer) and a server span (S14-A:
// a child of the caller's when its one traceparent header parses strictly, else the root of a new trace; the span is in the request's context
// for the service and the adapters, and is the traceparent of the answer) and, when the handler has returned or panicked, writes the one
// access line: method, the route pattern the mux matched (never the path, so no identifier and no query), status, duration in milliseconds,
// the id and the span's trace_id and span_id. The traceparent header is the only header read for it, and only its two ids and the sampled
// flag are kept; tracestate is ignored. No body, query, subject or tenant is read.
func (s *Server) access(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.requests.enter()
		defer s.requests.leave()
		start := s.now()
		id := requestID()
		span, _ := tracecontext.Incoming(req.Header.Values(tracecontext.Header))
		w.Header().Set("X-Request-Id", id)
		w.Header().Set(tracecontext.Header, span.String())
		req = req.WithContext(tracecontext.WithSpan(req.Context(), span))
		rec := &statusRecorder{ResponseWriter: w}
		finished := false
		defer func() {
			status := rec.status
			if status == 0 {
				status = http.StatusOK
				if !finished {
					status = http.StatusInternalServerError
				}
			}
			method := req.Method
			if !knownMethods[method] {
				method = "OTHER"
			}
			route := req.Pattern
			if route == "" {
				route = "unmatched"
			}
			elapsed := s.now().Sub(start)
			s.log.info("access", map[string]any{"method": method, "route": route, "status": status, "duration_ms": elapsed.Milliseconds(), "request_id": id,
				"trace_id": span.TraceIDString(), "span_id": span.SpanIDString()})
			s.metrics.request(route, status, elapsed)
		}()
		next.ServeHTTP(rec, req)
		finished = true
	})
}

// healthHandlers are /healthz and /readyz, outside the API's authentication and outside the console's static handler: they are mounted on
// the top mux ahead of both. Both carry the API's security headers (httpapi.Secured) and Cache-Control: no-store.
func (s *Server) healthHandlers(top *http.ServeMux) {
	write := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body + "\n"))
	}
	notAllowed := httpapi.Secured(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Allow", "GET, HEAD")
		write(w, http.StatusMethodNotAllowed, `{"error":"METHOD_NOT_ALLOWED"}`)
	}))
	top.Handle("/healthz", notAllowed)
	top.Handle("/readyz", notAllowed)
	// Health stays 200 for as long as the process answers at all: a drain closes the listener, and a connection that is already open gets its
	// answer. Whether to send traffic is readiness's question.
	top.Handle("GET /healthz", httpapi.Secured(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		write(w, http.StatusOK, `{"status":"ok"}`)
	})))
	top.Handle("GET /readyz", httpapi.Secured(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if reason := s.notReady(req); reason != "" {
			write(w, http.StatusServiceUnavailable, `{"status":"not_ready","reason":"`+reason+`"}`)
			return
		}
		write(w, http.StatusOK, `{"status":"ready"}`)
	})))
}

// The reasons of /readyz, and the only words the body can hold.
const (
	reasonStore       = "store"
	reasonSchema      = "schema"
	reasonRoles       = "roles"
	reasonObjectStore = "object_store"
	reasonDraining    = "draining"
)

const (
	storeCheckTimeout  = 3 * time.Second
	objectCheckTimeout = 5 * time.Second
	// objectCheckTTL is how long the object store's last answer stands: the probe of a load balancer is not a request to the bucket.
	objectCheckTTL = 10 * time.Second
)

// verdict is one dependency's last answer. Concurrent probes share one check at a time: a probe that arrives while another is checking waits for
// it and takes its answer when the answer is still within the ttl, so a flood of probes is one query at a time, never a storm.
type verdict struct {
	mu   sync.Mutex
	at   time.Time
	have bool
	val  string
}

func (v *verdict) get(now func() time.Time, ttl time.Duration, check func() string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.have && ttl > 0 && now().Sub(v.at) < ttl {
		return v.val
	}
	v.val, v.at, v.have = check(), now(), true
	return v.val
}

// notReady is "" when the server is ready, else the reason.
func (s *Server) notReady(req *http.Request) string {
	if s.draining.Load() {
		return reasonDraining
	}
	if reason := s.stored.get(s.now, 0, func() string {
		ctx, cancel := contextWithTimeout(context.Background(), storeCheckTimeout)
		defer cancel()
		switch s.store.Ready(ctx) {
		case "":
			return ""
		case "schema":
			return reasonSchema
		case "roles":
			return reasonRoles
		}
		return reasonStore
	}); reason != "" {
		return reason
	}
	if s.objects != nil {
		return s.objectVerdict.get(s.now, objectCheckTTL, func() string {
			ctx, cancel := contextWithTimeout(context.Background(), objectCheckTimeout)
			defer cancel()
			if s.objects.Check(ctx) != nil {
				return reasonObjectStore
			}
			return ""
		})
	}
	return ""
}
