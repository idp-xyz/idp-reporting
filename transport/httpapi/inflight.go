package httpapi

import (
	r "idp.local/reporting/reporting"
	"net/http"
	"sync/atomic"
)

// Requests in flight (S04-G, docs/S04G_SCOPE.md G3, docs/OPERATIONS.md "Capacity"). The server has timeouts and no other bound on how many
// requests it works on at once; this is that bound.
const (
	DefaultMaxInFlight = 128
	MaxMaxInFlight     = 4096
)

// WithMaxInFlight bounds the requests next works on at once, in this process: beyond n, a request is answered 503 {"error":"OVERLOADED"} with
// `Retry-After: 1` and next is not called, so a refused request costs a counter and nothing else (not an authentication, not a query). Zero
// is the default, 128; a value outside 1..4096 is ErrInvalid. Every request that reaches it counts, the session and the capabilities
// included; wrap the API, not the console's static pages, as the demo does. The slot is given back when next returns, however it returns:
// normally, with an error, with a panic, or when the client has gone (the handler sees its context cancelled and returns).
func WithMaxInFlight(next http.Handler, n int) (http.Handler, error) {
	h, _, e := WithMaxInFlightGauge(next, n)
	return h, e
}

// WithMaxInFlightGauge is WithMaxInFlight that also returns the number of requests holding a slot at this moment, so that a host can show
// (and a test prove) that every slot is given back once the server has drained.
func WithMaxInFlightGauge(next http.Handler, n int) (http.Handler, func() int64, error) {
	if next == nil || n < 0 || n > MaxMaxInFlight {
		return nil, nil, r.ErrInvalid
	}
	if n == 0 {
		n = DefaultMaxInFlight
	}
	limit := int64(n)
	var running atomic.Int64
	overloaded := secured(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Retry-After", "1")
		_ = send(w, http.StatusServiceUnavailable, map[string]string{"error": "OVERLOADED"})
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		for {
			current := running.Load()
			if current >= limit {
				overloaded.ServeHTTP(w, req)
				return
			}
			if running.CompareAndSwap(current, current+1) {
				break
			}
		}
		defer running.Add(-1)
		next.ServeHTTP(w, req)
	}), running.Load, nil
}
