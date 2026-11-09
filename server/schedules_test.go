package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/delivery/webhook"
	r "idp.local/reporting/reporting"
)

const testWebhookSecret = "webhook-secret-Zq7-0123456789abcdef0123"

// hookReceiver is the deployment's endpoint in a test: it checks every delivery's signature and keeps the bodies.
type hookReceiver struct {
	mu     sync.Mutex
	bodies [][]byte
	bad    int
}

func (h *hookReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !webhook.Verify([]byte(testWebhookSecret), b, req.Header.Get(webhook.SignatureHeader)) {
		h.bad++
		w.WriteHeader(401)
		return
	}
	h.bodies = append(h.bodies, b)
	w.WriteHeader(204)
}

func (h *hookReceiver) received() ([][]byte, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]byte(nil), h.bodies...), h.bad
}

// S16-A in the production server: a person makes a schedule through the API; at its occurrence the scheduler submits the snapshot as the
// person's binding, a worker runs it, the run ends succeeded, its end is delivered signed to the deployment's webhook and logged by its
// outcome word, the run is listed for its owner and both the change and the run are in the owner's audit. The start line says so.
func TestTheServerRunsSchedulesAndDeliversThem(t *testing.T) {
	w := newWorld(t)
	receiver := &hookReceiver{}
	hook := httptest.NewServer(receiver)
	defer hook.Close()
	w.cfg.Schedules = &SchedulesConfig{Webhook: &WebhookConfig{URL: hook.URL + "/reporting", LoopbackHTTP: true}}
	w.env = map[string]string{webhook.SecretEnv: testWebhookSecret}
	// One clock for the store and the source product, moved forward to the occurrence: by at most a minute and a few seconds, which the
	// service's own clock (the real one) tolerates in the source's reads.
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	w.sim.SetClock(now)
	store := NewMemoryStoreForTests(now)
	ticks := make(chan time.Time)
	hooks := &Hooks{Scheduled: make(chan struct{}, 1)}
	s := startWith(t, w, WithStoreForTests(store), WithPollIntervalForTests(5*time.Millisecond), WithHooksForTests(hooks),
		WithScheduleTickerForTests(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }))
	if start := s.log.events(t, "start")[0]; start["schedules"] != true || start["webhook"] != true {
		t.Fatalf("%v", start)
	}
	tick := func() {
		t.Helper()
		ticks <- time.Now()
		select {
		case <-hooks.Scheduled:
		case <-time.After(10 * time.Second):
			t.Fatal("no pass of the scheduler")
		}
	}
	token := mint(t, w.keys[0], claims{})
	q := contractsim.FinancialQuery()
	next := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
	in := map[string]any{"name": "AR daily", "query": q, "recurrence": map[string]any{"frequency": "daily", "at": next.Format("15:04"), "timezone": "UTC"}}
	code, _, body := s.do(t, "POST", "/api/reporting/v1/schedules", token, in, "Idempotency-Key", "s1")
	var v r.ScheduleView
	if code != 201 || json.Unmarshal(body, &v) != nil || v.NextRunAt != next.Format(time.RFC3339) {
		t.Fatal(code, string(body), next)
	}
	tick()
	if code, _, body = s.do(t, "GET", "/api/reporting/v1/schedules/"+v.ID+"/runs", token, nil); code != 200 || strings.TrimSpace(string(body)) != "[]" {
		t.Fatal("a run before the occurrence", code, string(body))
	}
	offset.Store(int64(time.Until(next) + time.Second))
	tick()
	var runs []r.RunView
	if code, _, body = s.do(t, "GET", "/api/reporting/v1/schedules/"+v.ID+"/runs", token, nil); code != 200 || json.Unmarshal(body, &runs) != nil || len(runs) != 1 || runs[0].State != r.RunSnapshot {
		t.Fatal(code, string(body))
	}
	// The run's job is the owner's: the worker runs it, the owner reads it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, _, body = s.do(t, "GET", "/api/reporting/v1/jobs/"+runs[0].SnapshotJob, token, nil)
		if code == 200 && strings.Contains(string(body), `"state":"succeeded"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run's job did not succeed", code, string(body))
		}
		time.Sleep(10 * time.Millisecond)
	}
	offset.Add(int64(r.RunRecheck))
	tick()
	if code, _, body = s.do(t, "GET", "/api/reporting/v1/schedules/"+v.ID+"/runs", token, nil); code != 200 || json.Unmarshal(body, &runs) != nil || runs[0].State != r.RunSucceeded || runs[0].Delivery != r.DeliveryDelivered || runs[0].SnapshotID == "" {
		t.Fatal(code, string(body))
	}
	bodies, bad := receiver.received()
	if len(bodies) != 1 || bad != 0 {
		t.Fatalf("%d deliveries, %d unsigned", len(bodies), bad)
	}
	var n r.Notification
	if json.Unmarshal(bodies[0], &n) != nil || n.RunID != runs[0].ID || n.State != r.RunSucceeded || n.Subject != testSubject || n.Tenant != testTenant || n.SnapshotID != runs[0].SnapshotID {
		t.Fatalf("%s", bodies[0])
	}
	if strings.Contains(string(bodies[0]), "customer_a") || strings.Contains(string(bodies[0]), "AR daily") {
		t.Fatalf("the delivery carries a value: %s", bodies[0])
	}
	lines := s.log.events(t, "delivery")
	if len(lines) != 1 || lines[0]["outcome"] != "delivered" || lines[0]["status"] != float64(204) || lines[0]["run_id"] != runs[0].ID {
		t.Fatalf("%v", lines)
	}
	if log := s.log.String(); strings.Contains(log, hook.URL) || strings.Contains(log, testWebhookSecret) || strings.Contains(log, "AR daily") {
		t.Fatal("the log names the endpoint, the secret or the schedule's name")
	}
	code, _, body = s.do(t, "GET", "/api/reporting/v1/audit", token, nil)
	if code != 200 || !strings.Contains(string(body), `"action":"schedule.change"`) || !strings.Contains(string(body), `"action":"schedule.run"`) {
		t.Fatal(code, string(body))
	}
}

// The webhook's secret is the environment's: without it, or with one too short, the server does not start, and the refusal shows neither.
func TestTheWebhookNeedsItsSecret(t *testing.T) {
	for _, secret := range []string{"", "short-Zq7"} {
		w := newWorld(t)
		w.cfg.Schedules = &SchedulesConfig{Webhook: &WebhookConfig{URL: "https://hooks.example/reporting"}}
		w.env = map[string]string{webhook.SecretEnv: secret}
		_, err := Start(t.Context(), w.cfg, w.registry(), w.getenv, io.Discard, WithStoreForTests(NewMemoryStoreForTests(time.Now)))
		var se *StartError
		if err == nil || !errors.As(err, &se) || se.Step != StepSecrets || !strings.Contains(se.Reason, webhook.SecretEnv) || (secret != "" && strings.Contains(err.Error(), secret)) || strings.Contains(err.Error(), "hooks.example") {
			t.Fatalf("%q: %v", secret, err)
		}
	}
	// Without a schedules section there are no schedule routes.
	w := newWorld(t)
	s := startWorld(t, w)
	if code, _, _ := s.do(t, "GET", "/api/reporting/v1/schedules", mint(t, w.keys[0], claims{}), nil); code != 404 {
		t.Fatal("the routes answer without the section", code)
	}
}
