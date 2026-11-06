package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
)

// scrape reads GET path of the metrics listener.
func scrape(t *testing.T, s *started, method, path string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+s.metricsListener.Addr().String()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// The metrics listener (docs/DEPLOYMENT.md "Metrics"): off unless metrics_listen is set; the Prometheus text exposition of the requests by route
// pattern and status class, their durations, the jobs run, the gauges and readiness with its reason; nothing that names a tenant, a subject,
// a path or a query; GET only; not on the API's listener.
func TestMetricsListener(t *testing.T) {
	w := newWorld(t)
	off := startWorld(t, w)
	if off.metricsListener != nil {
		t.Fatal("a metrics listener without metrics_listen")
	}
	if status, _, _ := off.do(t, "GET", "/metrics", "", nil); status == http.StatusOK {
		t.Fatal("the API's listener answers /metrics")
	}
	if !strings.Contains(off.log.String(), `"metrics":false`) {
		t.Fatal("the start line does not say metrics are off")
	}

	w2 := newWorld(t)
	w2.cfg.MetricsListen = "127.0.0.1:0"
	store := NewMemoryStoreForTests(time.Now)
	s := startWith(t, w2, WithStoreForTests(store), WithPollIntervalForTests(5*time.Millisecond))
	if s.metricsListener == nil || !strings.Contains(s.log.String(), `"metrics":true`) {
		t.Fatalf("no metrics listener: %s", s.log.String())
	}
	good := mint(t, w2.keys[0], claims{})
	if status, _, body := s.do(t, "GET", api+"/capabilities", good, nil); status != 200 {
		t.Fatalf("capabilities: %d %s", status, body)
	}
	if status, _, _ := s.do(t, "GET", api+"/jobs/0123456789abcdef0123456789abcdef?private=Zq7", good, nil); status != 404 {
		t.Fatalf("an unknown job: %d", status)
	}
	status, h, body := s.do(t, "POST", api+"/snapshot-jobs", good, contractsim.FinancialQuery(), "Idempotency-Key", "metrics-1")
	if status != 202 {
		t.Fatalf("snapshot: %d %s", status, body)
	}
	_ = h
	deadline := time.Now().Add(10 * time.Second)
	var text string
	for {
		code, hdr, b := scrape(t, s, "GET", "/metrics")
		if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain; version=0.0.4") || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("scrape: %d %v", code, hdr)
		}
		text = b
		if strings.Contains(text, "reporting_jobs_run_total 1\n") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{
		"# TYPE reporting_http_requests_total counter",
		`reporting_http_requests_total{route="GET /api/reporting/v1/capabilities",status="2xx"} 1`,
		`reporting_http_requests_total{route="GET /api/reporting/v1/jobs/{id}",status="4xx"} 1`,
		`reporting_http_requests_total{route="POST /api/reporting/v1/snapshot-jobs",status="2xx"} 1`,
		`reporting_http_request_duration_seconds_bucket{route="GET /api/reporting/v1/capabilities",le="+Inf"} 1`,
		`reporting_http_request_duration_seconds_count{route="GET /api/reporting/v1/capabilities"} 1`,
		"reporting_jobs_run_total 1\n",
		"reporting_draining 0\n",
		`reporting_ready{reason="none"} 1`,
		"reporting_build_info{version=",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the exposition lacks %q:\n%s", want, text)
		}
	}
	// Nothing about who: no tenant, subject, client, issuer, path value or query, and no job id.
	for _, leak := range []string{testTenant, testSubject, testClient, testIssuer, "Zq7", "0123456789abcdef", "private="} {
		if strings.Contains(text, leak) {
			t.Errorf("the exposition carries %q", leak)
		}
	}
	// Readiness with its reason, as /readyz would say it.
	store.SetNotReady("schema")
	if _, _, b := scrape(t, s, "GET", "/metrics"); !strings.Contains(b, `reporting_ready{reason="schema"} 0`) {
		t.Errorf("readiness not followed:\n%s", b)
	}
	store.SetNotReady("")
	// GET only, and nothing else on that listener.
	if code, _, _ := scrape(t, s, "POST", "/metrics"); code != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics: %d", code)
	}
	if code, _, _ := scrape(t, s, "GET", "/readyz"); code != http.StatusNotFound {
		t.Errorf("the metrics listener serves another path: %d", code)
	}
	// The scrape is not counted as an API request.
	if _, _, b := scrape(t, s, "GET", "/metrics"); strings.Contains(b, `route="GET /metrics"`) {
		t.Error("the scrape counts itself")
	}
}

// statusClass and the exposition's number format.
func TestMetricsFormatting(t *testing.T) {
	for in, want := range map[int]string{200: "2xx", 204: "2xx", 302: "3xx", 404: "4xx", 503: "5xx", 0: "5xx", 999: "5xx"} {
		if got := statusClass(in); got != want {
			t.Errorf("statusClass(%d) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[float64]string{0.005: "0.005", 1: "1", 2.5: "2.5", 0: "0", 10: "10"} {
		if got := trimFloat(in); got != want {
			t.Errorf("trimFloat(%v) = %s, want %s", in, got, want)
		}
	}
	var m *metrics
	m.request("x", 200, time.Second) // a nil collector is a no-op, never a panic
	m.workerFailed("X")
	m.swept("ok")
	m.reloaded("ok")
	m.jobRan()
}
