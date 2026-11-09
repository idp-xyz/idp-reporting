package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/delivery/webhook"
	r "idp.local/reporting/reporting"
)

var secret = []byte("0123456789abcdef0123456789abcdef-webhook")

func notification() r.Notification {
	return r.Notification{Event: r.NotificationEvent, RunID: strings.Repeat("a", 32), ScheduleID: strings.Repeat("b", 32), Tenant: "t", Issuer: "https://idp.example", Subject: "s", Integration: "p", State: r.RunSucceeded, Attempt: 1}
}

// The endpoint is https, or plain http to a loopback IP address with the option; no user information, no fragment; the secret is 32..1024
// bytes and the timeout 1..30 seconds.
func TestTheConfigurationIsRefusedOutsideItsBounds(t *testing.T) {
	for _, c := range []struct {
		url      string
		loopback bool
		ok       bool
	}{
		{"https://hooks.example/reporting", false, true},
		{"https://hooks.example:8443/a?b=c", false, true},
		{"http://127.0.0.1:9000/hook", true, true},
		{"http://[::1]:9000/hook", true, true},
		{"http://127.0.0.1:9000/hook", false, false},
		{"http://localhost:9000/hook", true, false},
		{"http://hooks.example/hook", true, false},
		{"https://user:pw@hooks.example/", false, false},
		{"https://hooks.example/#frag", false, false},
		{"ftp://hooks.example/", false, false},
		{"https:///nohost", false, false},
		{"hooks.example/hook", false, false},
		{"", false, false},
	} {
		if got := webhook.ValidURL(c.url, c.loopback); got != c.ok {
			t.Errorf("%q loopback=%v: %v", c.url, c.loopback, got)
		}
		_, e := webhook.New(webhook.Config{URL: c.url, Secret: secret, LoopbackHTTP: c.loopback})
		if (e == nil) != c.ok {
			t.Errorf("New %q: %v", c.url, e)
		}
	}
	for _, c := range []webhook.Config{
		{URL: "https://hooks.example/", Secret: secret[:31]},
		{URL: "https://hooks.example/", Secret: make([]byte, 1025)},
		{URL: "https://hooks.example/", Secret: secret, Timeout: 500 * time.Millisecond},
		{URL: "https://hooks.example/", Secret: secret, Timeout: 31 * time.Second},
	} {
		if _, e := webhook.New(c); e == nil || strings.Contains(e.Error(), string(secret[:8])) || strings.Contains(e.Error(), "hooks.example") {
			t.Errorf("%+v: %v", c.Timeout, e)
		}
	}
}

// A delivery is one POST of the notification's JSON, signed over the exact body, naming the run and the event.
func TestADeliveryIsSignedOverItsBody(t *testing.T) {
	var mu sync.Mutex
	var got *http.Request
	var body []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got = req
		body, _ = io.ReadAll(req.Body)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	h, e := webhook.New(webhook.Config{URL: srv.URL + "/hook", Secret: secret, Transport: srv.Client().Transport})
	if e != nil {
		t.Fatal(e)
	}
	n := notification()
	res := h.Deliver(context.Background(), n)
	if res.Outcome != r.DeliveryOutcomeDelivered || res.Status != 204 {
		t.Fatalf("%+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.Method != http.MethodPost || got.URL.Path != "/hook" || got.Header.Get("Content-Type") != "application/json" || got.Header.Get(webhook.DeliveryHeader) != n.RunID || got.Header.Get(webhook.EventHeader) != r.NotificationEvent {
		t.Fatalf("%s %s %v", got.Method, got.URL, got.Header)
	}
	sig := got.Header.Get(webhook.SignatureHeader)
	if !strings.HasPrefix(sig, "sha256=") || len(sig) != 7+64 || !webhook.Verify(secret, body, sig) {
		t.Fatalf("signature %q", sig)
	}
	if webhook.Verify(secret, append(body, ' '), sig) || webhook.Verify([]byte("another secret of thirty-two bytes!!"), body, sig) {
		t.Fatal("a signature verifies another body or another secret")
	}
	var back r.Notification
	if e := json.Unmarshal(body, &back); e != nil || back != n {
		t.Fatalf("%s %v", body, e)
	}
}

// Every answer has its fixed outcome word; a redirect is not followed; no answer is unreachable; nothing of the answer is returned.
func TestOutcomesOfAnswers(t *testing.T) {
	var redirected int
	mux := http.NewServeMux()
	for path, status := range map[string]int{"/ok": 200, "/accepted": 202, "/bad": 400, "/gone": 410, "/timeout": 408, "/busy": 429, "/down": 503, "/broken": 500} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(strings.Repeat("x", 200<<10)))
		})
	}
	mux.HandleFunc("/moved", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/elsewhere", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, _ *http.Request) { redirected++; w.WriteHeader(200) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for path, want := range map[string]string{"/ok": r.DeliveryOutcomeDelivered, "/accepted": r.DeliveryOutcomeDelivered, "/bad": r.DeliveryOutcomeRejected, "/gone": r.DeliveryOutcomeRejected,
		"/timeout": r.DeliveryOutcomeThrottled, "/busy": r.DeliveryOutcomeThrottled, "/down": r.DeliveryOutcomeServerError, "/broken": r.DeliveryOutcomeServerError, "/moved": r.DeliveryOutcomeRedirected} {
		h, e := webhook.New(webhook.Config{URL: srv.URL + path, Secret: secret, LoopbackHTTP: true})
		if e != nil {
			t.Fatal(e)
		}
		if res := h.Deliver(context.Background(), notification()); res.Outcome != want {
			t.Errorf("%s: %+v, want %s", path, res, want)
		}
	}
	if redirected != 0 {
		t.Fatal("a redirect was followed")
	}
	h, _ := webhook.New(webhook.Config{URL: srv.URL + "/slow", Secret: secret, LoopbackHTTP: true, Timeout: time.Second})
	start := time.Now()
	if res := h.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeUnreachable || res.Status != 0 || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("%+v after %v", res, time.Since(start))
	}
	closed := httptest.NewServer(mux)
	url := closed.URL
	closed.Close()
	h, _ = webhook.New(webhook.Config{URL: url + "/ok", Secret: secret, LoopbackHTTP: true})
	if res := h.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeUnreachable {
		t.Fatalf("%+v", res)
	}
	// A TLS server whose certificate the default client does not trust is unreachable too: no certificate check is skipped.
	tlsSrv := httptest.NewTLSServer(mux)
	defer tlsSrv.Close()
	h, _ = webhook.New(webhook.Config{URL: tlsSrv.URL + "/ok", Secret: secret})
	if res := h.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeUnreachable {
		t.Fatalf("an untrusted certificate: %+v", res)
	}
	for status, want := range map[int]string{204: r.DeliveryOutcomeDelivered, 301: r.DeliveryOutcomeRedirected, 404: r.DeliveryOutcomeRejected, 429: r.DeliveryOutcomeThrottled, 502: r.DeliveryOutcomeServerError} {
		if got := webhook.OutcomeOf(status); got != want {
			t.Errorf("%d: %s", status, got)
		}
	}
}
