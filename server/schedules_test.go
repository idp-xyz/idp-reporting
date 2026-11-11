package server

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/delivery/email"
	"idp.local/reporting/delivery/webhook"
	r "idp.local/reporting/reporting"
)

const (
	testWebhookSecret = "webhook-secret-Zq7-0123456789abcdef0123"
	testEmail         = "zhang.san-Zq7@example.com"
	testSMTPPassword  = "smtp-PASSWORD-Zq7-never-shown"
)

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

// mailReceiver is the deployment's SMTP relay in a test: plain SMTP on 127.0.0.1 (the configuration's loopback_plain), taking every message.
type mailReceiver struct {
	ln       net.Listener
	mu       sync.Mutex
	messages []mailMessage
}

type mailMessage struct {
	From string
	To   []string
	Data string
}

func startMailReceiver(t *testing.T) *mailReceiver {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &mailReceiver{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go m.serve(conn)
		}
	}()
	return m
}

func (m *mailReceiver) port() int { return m.ln.Addr().(*net.TCPAddr).Port }

func (m *mailReceiver) received() []mailMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mailMessage(nil), m.messages...)
}

func (m *mailReceiver) serve(conn net.Conn) {
	defer conn.Close()
	reply := func(s string) { _, _ = fmt.Fprintf(conn, "%s\r\n", s) }
	reply("220 relay ESMTP")
	br := bufio.NewReader(conn)
	var msg mailMessage
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			reply("250 relay")
		case "MAIL":
			msg = mailMessage{From: strings.TrimSuffix(strings.TrimPrefix(arg, "FROM:<"), ">")}
			reply("250 ok")
		case "RCPT":
			msg.To = append(msg.To, strings.TrimSuffix(strings.TrimPrefix(arg, "TO:<"), ">"))
			reply("250 ok")
		case "DATA":
			reply("354 go ahead")
			var data strings.Builder
			for {
				l, err := br.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			msg.Data = data.String()
			m.mu.Lock()
			m.messages = append(m.messages, msg)
			m.mu.Unlock()
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

// scheduleRuns makes a daily schedule of the financial query through the API, moves the clock to its occurrence, waits for the run's job and
// lets the scheduler end and deliver the run; it answers the run as the owner lists it. ticks drives the scheduler's passes, offset the clock.
func scheduleRuns(t *testing.T, s *started, token string, offset *atomic.Int64, ticks chan time.Time, hooks *Hooks) r.RunView {
	t.Helper()
	tick := func() {
		t.Helper()
		ticks <- time.Now()
		select {
		case <-hooks.Scheduled:
		case <-time.After(10 * time.Second):
			t.Fatal("no pass of the scheduler")
		}
	}
	next := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
	in := map[string]any{"name": "AR daily", "query": contractsim.FinancialQuery(), "recurrence": map[string]any{"frequency": "daily", "at": next.Format("15:04"), "timezone": "UTC"}}
	code, _, body := s.do(t, "POST", "/api/reporting/v1/schedules", token, in, "Idempotency-Key", "s1")
	var v r.ScheduleView
	if code != 201 || json.Unmarshal(body, &v) != nil {
		t.Fatal(code, string(body))
	}
	offset.Store(int64(time.Until(next) + time.Second))
	tick()
	var runs []r.RunView
	if code, _, body = s.do(t, "GET", "/api/reporting/v1/schedules/"+v.ID+"/runs", token, nil); code != 200 || json.Unmarshal(body, &runs) != nil || len(runs) != 1 {
		t.Fatal(code, string(body))
	}
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
	if code, _, body = s.do(t, "GET", "/api/reporting/v1/schedules/"+v.ID+"/runs", token, nil); code != 200 || json.Unmarshal(body, &runs) != nil || runs[0].State != r.RunSucceeded {
		t.Fatal(code, string(body))
	}
	return runs[0]
}

// S16-B in the production server: with a webhook and an email configured, a run's end goes to both, the mail to the address the owner's binding
// names in the bindings file, with the schedule's name, the outcome, the ids and the console's link, and nothing of the report; the log has one
// delivery line per deliverer and never the address, the name, the body or the endpoint; the start line says both.
func TestTheServerMailsARunsEndToTheOwnersAddressFromTheBindings(t *testing.T) {
	w := newWorld(t)
	receiver := &hookReceiver{}
	hook := httptest.NewServer(receiver)
	defer hook.Close()
	relay := startMailReceiver(t)
	writeFile(t, w.binds, []byte(`[{"subject":"`+testSubject+`","client_id":"`+testClient+`","tenant":"`+testTenant+`","integrations":["`+w.finance+`"],"email":"`+testEmail+`"}]`), 0o644)
	off := false
	w.cfg.Schedules = &SchedulesConfig{
		Webhook: &WebhookConfig{URL: hook.URL + "/reporting", LoopbackHTTP: true},
		Email:   &EmailConfig{Host: "127.0.0.1", Port: relay.port(), From: "reporting@platform.example", StartTLS: &off, LoopbackPlain: true, ConsoleURL: "http://127.0.0.1:8443/", Timeout: 5},
	}
	w.env = map[string]string{webhook.SecretEnv: testWebhookSecret}
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	w.sim.SetClock(now)
	ticks := make(chan time.Time)
	hooks := &Hooks{Scheduled: make(chan struct{}, 1)}
	s := startWith(t, w, WithStoreForTests(NewMemoryStoreForTests(now)), WithPollIntervalForTests(5*time.Millisecond), WithHooksForTests(hooks),
		WithScheduleTickerForTests(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }))
	if start := s.log.events(t, "start")[0]; start["schedules"] != true || start["webhook"] != true || start["email"] != true {
		t.Fatalf("%v", start)
	}
	run := scheduleRuns(t, s, mint(t, w.keys[0], claims{}), &offset, ticks, hooks)
	if run.Delivery != r.DeliveryDelivered {
		t.Fatalf("%+v", run)
	}
	if bodies, bad := receiver.received(); len(bodies) != 1 || bad != 0 {
		t.Fatalf("%d webhook deliveries, %d unsigned", len(bodies), bad)
	}
	mails := relay.received()
	if len(mails) != 1 || mails[0].From != "reporting@platform.example" || len(mails[0].To) != 1 || mails[0].To[0] != testEmail {
		t.Fatalf("%+v", mails)
	}
	msg, err := mail.ReadMessage(strings.NewReader(mails[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || subject != "[IDP Reporting] 定时报表 AR daily · 成功" || msg.Header.Get("To") != testEmail || msg.Header.Get(email.DeliveryHeader) != run.ID {
		t.Fatalf("%q %v", subject, msg.Header)
	}
	raw, _ := io.ReadAll(msg.Body)
	body, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AR daily", run.ID, run.ScheduleID, "成功（succeeded）", "http://127.0.0.1:8443/console/#schedules"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the mail lacks %q:\n%s", want, body)
		}
	}
	for _, absent := range []string{"customer_a", run.SnapshotID, "attachment", "multipart"} {
		if strings.Contains(string(body), absent) || strings.Contains(mails[0].Data, absent) {
			t.Errorf("the mail carries %q", absent)
		}
	}
	lines := s.log.events(t, "delivery")
	if len(lines) != 2 {
		t.Fatalf("%v", lines)
	}
	byDeliverer := map[string]map[string]any{}
	for _, l := range lines {
		byDeliverer[l["deliverer"].(string)] = l
	}
	if h, m := byDeliverer["webhook"], byDeliverer["email"]; h == nil || m == nil || h["outcome"] != "delivered" || h["status"] != float64(204) || m["outcome"] != "delivered" || m["status"] != float64(250) ||
		h["delivery"] != "delivered" || m["delivery"] != "delivered" || h["run_id"] != run.ID || m["run_id"] != run.ID || m["attempt"] != float64(1) {
		t.Fatalf("%v", lines)
	}
	if log := s.log.String(); strings.Contains(log, testEmail) || strings.Contains(log, "zhang") || strings.Contains(log, "AR daily") || strings.Contains(log, "platform.example") || strings.Contains(log, hook.URL) || strings.Contains(log, testWebhookSecret) {
		t.Fatal("the log names the address, the sender, the schedule's name, the endpoint or the secret")
	}
	// The administrator's view still says of the webhook what it said, and nothing of an address.
	replaceFile(t, w.binds, []byte(`[{"subject":"`+testSubject+`","client_id":"`+testClient+`","tenant":"`+testTenant+`","integrations":["`+w.finance+`"],"roles":["viewer","administrator"],"email":"`+testEmail+`"}]`), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if code, _, body := s.do(t, "GET", "/api/reporting/v1/admin/configuration", mint(t, w.keys[0], claims{}), nil); code != 200 || !strings.Contains(string(body), `"webhook":true`) || strings.Contains(string(body), testEmail) {
		t.Fatalf("%d %s", code, body)
	}
}

// An owner whose binding names no address gets no mail, and the run is delivered all the same; the email alone is a deliverer (the start line
// says so, the webhook stays off); the SMTP password is the environment's: required with a username, refused without one, never shown.
func TestTheEmailAloneAndWithoutAnAddressAndItsPassword(t *testing.T) {
	w := newWorld(t)
	relay := startMailReceiver(t)
	off := false
	w.cfg.Schedules = &SchedulesConfig{Email: &EmailConfig{Host: "127.0.0.1", Port: relay.port(), From: "reporting@platform.example", StartTLS: &off, LoopbackPlain: true, ConsoleURL: "http://127.0.0.1:8443"}}
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	w.sim.SetClock(now)
	ticks := make(chan time.Time)
	hooks := &Hooks{Scheduled: make(chan struct{}, 1)}
	s := startWith(t, w, WithStoreForTests(NewMemoryStoreForTests(now)), WithPollIntervalForTests(5*time.Millisecond), WithHooksForTests(hooks),
		WithScheduleTickerForTests(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }))
	if start := s.log.events(t, "start")[0]; start["schedules"] != true || start["webhook"] != false || start["email"] != true {
		t.Fatalf("%v", start)
	}
	run := scheduleRuns(t, s, mint(t, w.keys[0], claims{}), &offset, ticks, hooks)
	if run.Delivery != r.DeliveryDelivered {
		t.Fatalf("%+v", run)
	}
	if mails := relay.received(); len(mails) != 0 {
		t.Fatalf("a mail to no one: %+v", mails)
	}
	if lines := s.log.events(t, "delivery"); len(lines) != 1 || lines[0]["deliverer"] != "email" || lines[0]["outcome"] != "delivered" || lines[0]["status"] != float64(0) || lines[0]["delivery"] != "delivered" {
		t.Fatalf("%v", lines)
	}
	// The password.
	for _, c := range []struct {
		username, password, reason string
	}{
		{"sender", "", "must be set"},
		{"", testSMTPPassword, "names no username"},
		{"sender", "pw\n" + testSMTPPassword, "1 to 1024 bytes"},
	} {
		x := newWorld(t)
		x.cfg.Schedules = &SchedulesConfig{Email: &EmailConfig{Host: "smtp.example.com", From: "reporting@platform.example", Username: c.username, ConsoleURL: "https://reporting.example.com"}}
		x.env = map[string]string{email.PasswordEnv: c.password}
		_, err := Start(t.Context(), x.cfg, x.registry(), x.getenv, io.Discard, WithStoreForTests(NewMemoryStoreForTests(time.Now)))
		var se *StartError
		if err == nil || !errors.As(err, &se) || se.Step != StepSecrets || !strings.Contains(se.Reason, email.PasswordEnv) || !strings.Contains(se.Reason, c.reason) || strings.Contains(err.Error(), testSMTPPassword) || strings.Contains(err.Error(), "smtp.example.com") {
			t.Fatalf("%q/%q: %v", c.username, c.password, err)
		}
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
