package server

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// metrics is the server's Prometheus text exposition (docs/DEPLOYMENT.md "Metrics"), served on its own loopback listener when metrics_listen is set.
// It adds no dependency: the format is a few lines of text. Every label is a word the server chose: the mux pattern of the route (never the
// path), a status class, an error code, a /readyz reason. No tenant, subject, client, integration, job id, path or query is ever a label, so
// the exposition says how the server is doing and nothing about who uses it.
type metrics struct {
	mu        sync.Mutex
	requests  map[[2]string]uint64 // {route, status class} -> count
	durations map[string]*histogram
	workerErr map[string]uint64 // code -> count
	sweeps    map[string]uint64 // "ok" or the code -> count
	reloads   map[string]uint64 // "ok" or the code -> count
	schedules map[string]uint64 // a scheduler pass: "ok" or the code -> count (S16-A)
	delivers  map[string]uint64 // a delivery attempt's outcome word -> count (S16-A)
	jobsRun   atomic.Uint64
}

// durationBounds are the histogram's upper bounds in seconds (Prometheus's default buckets).
var durationBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type histogram struct {
	counts []uint64 // per bound, not cumulative; the exposition accumulates
	count  uint64
	sum    float64
}

func newMetrics() *metrics {
	return &metrics{requests: map[[2]string]uint64{}, durations: map[string]*histogram{}, workerErr: map[string]uint64{}, sweeps: map[string]uint64{}, reloads: map[string]uint64{}, schedules: map[string]uint64{}, delivers: map[string]uint64{}}
}

// statusClass is 2xx, 3xx, 4xx or 5xx.
func statusClass(status int) string {
	if status < 200 || status > 599 {
		return "5xx"
	}
	return fmt.Sprintf("%dxx", status/100)
}

func (m *metrics) request(route string, status int, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[[2]string{route, statusClass(status)}]++
	h := m.durations[route]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(durationBounds))}
		m.durations[route] = h
	}
	s := d.Seconds()
	for i, b := range durationBounds {
		if s <= b {
			h.counts[i]++
			break
		}
	}
	h.count++
	h.sum += s
}

// count adds one to the counter key of the family pick names. The family is chosen under the guard, so a nil collector is a no-op.
func (m *metrics) count(pick func(*metrics) map[string]uint64, key string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	pick(m)[key]++
	m.mu.Unlock()
}

func (m *metrics) workerFailed(code string) {
	m.count(func(m *metrics) map[string]uint64 { return m.workerErr }, code)
}
func (m *metrics) swept(code string) {
	m.count(func(m *metrics) map[string]uint64 { return m.sweeps }, code)
}
func (m *metrics) reloaded(code string) {
	m.count(func(m *metrics) map[string]uint64 { return m.reloads }, code)
}
func (m *metrics) scheduled(code string) {
	m.count(func(m *metrics) map[string]uint64 { return m.schedules }, code)
}
func (m *metrics) delivered(outcome string) {
	m.count(func(m *metrics) map[string]uint64 { return m.delivers }, outcome)
}
func (m *metrics) jobRan() {
	if m != nil {
		m.jobsRun.Add(1)
	}
}

// gauges are read when the exposition is written: the server's own state at that moment.
type gauges struct {
	inFlight, workersBusy, draining, ready int64
	notReady                               string
}

// write is the exposition: counters and the histogram in a stable order, then the gauges.
func (m *metrics) write(g gauges, version string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b bytes.Buffer
	family := func(name, kind, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	}
	family("reporting_build_info", "gauge", "The binary's version; always 1.")
	fmt.Fprintf(&b, "reporting_build_info{version=%q} 1\n", version)

	family("reporting_http_requests_total", "counter", "Requests answered, by the route pattern the mux matched and the status class.")
	keys := make([][2]string, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i][0] < keys[j][0] || keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "reporting_http_requests_total{route=%q,status=%q} %d\n", k[0], k[1], m.requests[k])
	}

	family("reporting_http_request_duration_seconds", "histogram", "Time from the request's arrival to the handler's return, by route pattern.")
	routes := make([]string, 0, len(m.durations))
	for r := range m.durations {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	for _, r := range routes {
		h := m.durations[r]
		var cum uint64
		for i, bound := range durationBounds {
			cum += h.counts[i]
			fmt.Fprintf(&b, "reporting_http_request_duration_seconds_bucket{route=%q,le=%q} %d\n", r, trimFloat(bound), cum)
		}
		fmt.Fprintf(&b, "reporting_http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", r, h.count)
		fmt.Fprintf(&b, "reporting_http_request_duration_seconds_sum{route=%q} %s\n", r, trimFloat(h.sum))
		fmt.Fprintf(&b, "reporting_http_request_duration_seconds_count{route=%q} %d\n", r, h.count)
	}

	family("reporting_jobs_run_total", "counter", "Jobs a worker of this process claimed and ran to their end.")
	fmt.Fprintf(&b, "reporting_jobs_run_total %d\n", m.jobsRun.Load())
	for _, f := range []struct {
		name, help, label string
		values            map[string]uint64
	}{
		{"reporting_worker_failures_total", "Calls of a worker that failed, by the platform's error code.", "code", m.workerErr},
		{"reporting_sweeps_total", "Retention sweeps of this process's loop, by outcome: ok or the failure's code.", "outcome", m.sweeps},
		{"reporting_reloads_total", "Issuer reloads (SIGHUP), by outcome: ok or the refusal's code.", "outcome", m.reloads},
		{"reporting_schedule_passes_total", "Passes of this process's scheduler, by outcome: ok or the failure's code.", "outcome", m.schedules},
		{"reporting_deliveries_total", "Delivery attempts of scheduled runs' ends, by outcome word.", "outcome", m.delivers},
	} {
		family(f.name, "counter", f.help)
		names := make([]string, 0, len(f.values))
		for k := range f.values {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Fprintf(&b, "%s{%s=%q} %d\n", f.name, f.label, k, f.values[k])
		}
	}

	family("reporting_http_in_flight", "gauge", "API requests being served now (the max_in_flight gauge).")
	fmt.Fprintf(&b, "reporting_http_in_flight %d\n", g.inFlight)
	family("reporting_workers_busy", "gauge", "Workers and sweeps inside a call now.")
	fmt.Fprintf(&b, "reporting_workers_busy %d\n", g.workersBusy)
	family("reporting_draining", "gauge", "1 while the server drains.")
	fmt.Fprintf(&b, "reporting_draining %d\n", g.draining)
	family("reporting_ready", "gauge", "1 when /readyz would answer 200; else 0, with the reason /readyz would give.")
	reason := g.notReady
	if reason == "" {
		reason = "none"
	}
	fmt.Fprintf(&b, "reporting_ready{reason=%q} %d\n", reason, g.ready)
	return b.Bytes()
}

func trimFloat(f float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", f), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// metricsHandler is GET /metrics; anything else is 404 or 405. The listener is loopback-only (Validate), so it carries no authentication.
func (s *Server) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, req *http.Request) {
		g := gauges{workersBusy: s.busy.Load()}
		if s.inflight != nil {
			g.inFlight = s.inflight()
		}
		if s.draining.Load() {
			g.draining = 1
		}
		if reason := s.notReady(req); reason == "" {
			g.ready = 1
		} else {
			g.notReady = reason
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(s.metrics.write(g, s.version))
	})
	return mux
}

// serveMetrics runs the metrics listener until the drain closes it. It is not part of the drain's wait: a scrape in flight is cut off.
func (s *Server) serveMetrics(l net.Listener) {
	srv := &http.Server{Handler: s.metricsHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	s.defer_(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(l) }()
}
