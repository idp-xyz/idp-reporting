package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

const api = "/api/reporting/v1"

// waitJob polls a job until it is in a final state. The wait is on the answer of the server, not on time.
func (s *started) waitJob(t *testing.T, token, id string) r.JobView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		status, _, body := s.do(t, "GET", api+"/jobs/"+id, token, nil)
		var j r.JobView
		if status != 200 || json.Unmarshal(body, &j) != nil {
			t.Fatalf("job: %d %s", status, body)
		}
		if j.State == "succeeded" || j.State == "failed" || j.State == "cancelled" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job did not finish: %+v", j)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// secretsOf is every value a log line must never carry in a session of this world.
func secretsOf(w *world, token string) []string {
	return []string{token, testSubject, testTenant, testClient, cursorKeyHex, cursorKey2, testDSNPass, testDSN, testAccess, testSecret, w.dir, "Bearer ", "secretq", "authorization"}
}

// The whole session over real TLS with real RS256 tokens of a test issuer: the refusals of authentication, then a valid caller's session,
// capabilities, a query, a snapshot job, an export, its download and a quota refusal; and, through all of it, the log.
func TestSessionOverTLSWithRealTokens(t *testing.T) {
	snap := goroutines()
	w := newWorld(t)
	w.cfg.Quotas.AdmissionsPerHour = 2
	// An object store that only answers its readiness check, so that the keys are in play without a real one.
	fake := newFakeS3(t)
	w.cfg.ObjectStore = &ObjectStoreConfig{Endpoint: fake.URL, Bucket: "bucket", LoopbackHTTP: true}
	s := startWorld(t, w)
	good := mint(t, w.keys[0], claims{})

	// Health and readiness need no token, carry the API's headers, and say nothing else.
	for path, want := range map[string]string{"/healthz": `{"status":"ok"}`, "/readyz": `{"status":"ready"}`} {
		status, h, body := s.do(t, "GET", path, "", nil)
		if status != 200 || strings.TrimSpace(string(body)) != want {
			t.Errorf("%s: %d %s", path, status, body)
		}
		for k, v := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'"} {
			if h.Get(k) != v {
				t.Errorf("%s: %s is %q", path, k, h.Get(k))
			}
		}
	}
	if status, _, _ := s.do(t, "POST", "/healthz", "", nil); status != 405 {
		t.Errorf("POST /healthz: %d", status)
	}
	if status, _, _ := s.do(t, "GET", "/nope", good, nil); status != 404 {
		t.Errorf("/nope: %d", status)
	}
	// Not reachable through the console's static handler.
	if status, _, _ := s.do(t, "GET", "/console/healthz", "", nil); status != 404 {
		t.Errorf("/console/healthz: %d", status)
	}

	// Authentication.
	refused := map[string]struct {
		token string
		want  int
	}{
		"no token":                {"", 401},
		"garbage":                 {"not.a.token", 401},
		"the wrong audience":      {mint(t, w.keys[0], claims{Aud: "someone-else"}), 401},
		"the wrong issuer":        {mint(t, w.keys[0], claims{Iss: "https://other.example.test"}), 401},
		"the wrong scope":         {mint(t, w.keys[0], claims{Scope: "other.scope"}), 403},
		"an expired token":        {mint(t, w.keys[0], claims{Expired: true}), 401},
		"a key that is not known": {mint(t, w.keys[1], claims{}), 401},
		"an unknown kid":          {mint(t, w.keys[0], claims{KID: "kid-zz"}), 401},
		"a subject not bound":     {mint(t, w.keys[0], claims{Sub: "someone-else"}), 401},
		"a client not bound":      {mint(t, w.keys[0], claims{Client: "another-client"}), 401},
	}
	for name, c := range refused {
		status, _, body := s.do(t, "GET", api+"/session", c.token, nil)
		if status != c.want {
			t.Errorf("%s: %d %s", name, status, body)
		}
		if c.token != "" && strings.Contains(string(body), c.token) {
			t.Errorf("%s: the answer repeats the token", name)
		}
	}
	// A valid token is a valid caller.
	status, _, body := s.do(t, "GET", api+"/session?secretq=1", good, nil)
	var session struct {
		Identity     map[string]string `json:"identity"`
		Integrations []string          `json:"integrations"`
	}
	if status != 200 || json.Unmarshal(body, &session) != nil || session.Identity["subject"] != testSubject || session.Identity["tenant"] != testTenant || session.Integrations[0] != w.finance {
		t.Fatalf("session: %d %s", status, body)
	}
	status, _, body = s.do(t, "GET", api+"/capabilities", good, nil)
	var caps struct {
		Profile  string `json:"profile"`
		Approved bool   `json:"production_approved"`
		Quotas   map[string]int
	}
	if status != 200 || json.Unmarshal(body, &caps) != nil || caps.Approved || caps.Profile != "simulated:test-lab" || caps.Quotas["admissions_per_hour"] != 2 {
		t.Fatalf("capabilities: %d %s", status, body)
	}
	status, _, body = s.do(t, "POST", api+"/queries", good, contractsim.FinancialQuery())
	var result r.Result
	if status != 200 || json.Unmarshal(body, &result) != nil || len(result.Rows) == 0 {
		t.Fatalf("query: %d %s", status, body)
	}
	status, h, body := s.do(t, "POST", api+"/snapshot-jobs", good, contractsim.FinancialQuery(), "Idempotency-Key", "snap-1")
	var job r.JobView
	if status != 202 || json.Unmarshal(body, &job) != nil || h.Get("Location") == "" {
		t.Fatalf("snapshot: %d %s", status, body)
	}
	job = s.waitJob(t, good, job.ID)
	if job.State != "succeeded" || job.SnapshotID == "" {
		t.Fatalf("%+v", job)
	}
	status, _, body = s.do(t, "POST", api+"/exports", good, map[string]string{"snapshot_id": job.SnapshotID}, "Idempotency-Key", "export-1")
	var export r.JobView
	if status != 202 || json.Unmarshal(body, &export) != nil {
		t.Fatalf("export: %d %s", status, body)
	}
	export = s.waitJob(t, good, export.ID)
	if export.State != "succeeded" || export.ArtifactID == "" {
		t.Fatalf("%+v", export)
	}
	status, h, body = s.do(t, "GET", api+"/artifacts/"+export.ArtifactID+"/content", good, nil)
	if status != 200 || len(body) < 4 || string(body[:2]) != "PK" || !strings.Contains(h.Get("Content-Type"), "spreadsheetml") {
		t.Fatalf("download: %d %q", status, h.Get("Content-Type"))
	}
	// The third admission of the hour is refused: the quota is the configuration's.
	status, h, body = s.do(t, "POST", api+"/snapshot-jobs", good, contractsim.FinancialQuery(), "Idempotency-Key", "snap-2")
	if status != 429 || !strings.Contains(string(body), "QUOTA_EXCEEDED") || h.Get("Retry-After") == "" {
		t.Fatalf("quota: %d %s", status, body)
	}

	// TLS 1.3 only: a TLS 1.2 client is refused at the handshake, and plain HTTP gets no answer.
	conn, err := tls.Dial("tcp", s.Addr(), &tls.Config{RootCAs: w.cert, MaxVersion: tls.VersionTLS12})
	if err == nil {
		conn.Close()
		t.Error("a TLS 1.2 client was served")
	}
	conn, err = tls.Dial("tcp", s.Addr(), &tls.Config{RootCAs: w.cert, MinVersion: tls.VersionTLS13})
	if err != nil || conn.ConnectionState().Version != tls.VersionTLS13 {
		t.Errorf("TLS 1.3: %v", err)
	} else {
		conn.Close()
	}
	plain, err := net.Dial("tcp", s.Addr())
	if err == nil {
		_, _ = plain.Write([]byte("GET /healthz HTTP/1.1\r\nHost: x\r\n\r\n"))
		buf := make([]byte, 64)
		_ = plain.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _ := plain.Read(buf)
		if strings.Contains(string(buf[:n]), "200 OK") {
			t.Error("plain HTTP was answered on the TLS port")
		}
		plain.Close()
	}

	// The log: start, access lines with the allowed fields only, and none of the secrets.
	s.cancel()
	<-s.Done()
	s.client.CloseIdleConnections()
	if err := s.Wait(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	start := s.log.events(t, "start")
	if len(start) != 1 || start[0]["connections"] != float64(1) || start[0]["cursor_keys"] != float64(1) || start[0]["ledger"] != float64(0) || start[0]["tls"] != true {
		t.Errorf("start line: %v", start)
	}
	if kids, _ := start[0]["trusted_kids"].([]any); len(kids) != 1 || kids[0] != "kid-a" {
		t.Errorf("trusted kids: %v", start[0]["trusted_kids"])
	}
	accesses := s.log.events(t, "access")
	if len(accesses) < 20 {
		t.Errorf("%d access lines", len(accesses))
	}
	routes := map[string]bool{}
	for _, a := range accesses {
		for k := range a {
			switch k {
			case "ts", "level", "event", "method", "route", "status", "duration_ms", "request_id", "trace_id", "span_id":
			default:
				t.Errorf("an access line has the field %q", k)
			}
		}
		if id, _ := a["request_id"].(string); len(id) != 32 {
			t.Errorf("request id %v", a["request_id"])
		}
		routes[a["route"].(string)] = true
	}
	for _, want := range []string{"GET /healthz", "GET /readyz", "GET " + api + "/jobs/{id}", "GET " + api + "/artifacts/{id}/content", "POST " + api + "/exports", "GET " + api + "/session", "unmatched"} {
		if !routes[want] {
			t.Errorf("no access line for %q: %v", want, routes)
		}
	}
	text := s.log.String()
	for _, secret := range secretsOf(w, good) {
		if strings.Contains(strings.ToLower(text), strings.ToLower(secret)) {
			t.Errorf("the log carries %q", secret)
		}
	}
	for _, line := range s.log.lines(t) {
		if _, ok := line["ts"].(string); !ok || line["level"] == nil || line["event"] == nil {
			t.Errorf("a line lacks ts, level or event: %v", line)
		}
	}
	if s.inflight() != 0 || s.requests.count() != 0 {
		t.Errorf("slots held after the drain: %d %d", s.inflight(), s.requests.count())
	}
	if fake.count() != 1 {
		t.Errorf("the export is not in the object store: %d objects", fake.count())
	}
	fake.Server.Close()
	noNewGoroutines(t, snap)
}

// X-Reporting-Data-Class follows the profile (S05-A): business-data on every answer of a production server, contract-simulator when simulators
// are allowed, always exactly one value.
func TestDataClassFollowsTheProfile(t *testing.T) {
	for _, c := range []struct {
		simulated bool
		want      string
	}{{false, "business-data"}, {true, "contract-simulator"}} {
		w := newWorld(t)
		w.cfg.AllowSimulator = c.simulated
		// A production server runs an adapter that does not call itself a simulator (the wrapper hides the method).
		ctx, cancel := context.WithCancel(context.Background())
		lb := &logBuffer{}
		var reg Registry
		if c.simulated {
			reg = w.registry()
		} else {
			reg = Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return struct{ r.Adapter }{w.sim}, nil }}
		}
		srv, err := Start(ctx, w.cfg, reg, w.getenv, lb, WithStoreForTests(NewMemoryStoreForTests(time.Now)))
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		s := &started{Server: srv, log: lb, cancel: cancel, w: w, base: "https://" + srv.Addr()}
		tr := newTLSTransport(w)
		s.client = &http.Client{Transport: tr}
		t.Cleanup(func() { cancel(); <-srv.Done(); tr.CloseIdleConnections() })
		good := mint(t, w.keys[0], claims{})
		for _, req := range []struct{ method, path, token string }{
			{"GET", "/healthz", ""}, {"GET", "/readyz", ""}, {"POST", "/healthz", ""}, {"GET", "/nope", ""}, {"GET", "/", ""},
			{"GET", api + "/session", ""}, {"GET", api + "/session", good}, {"GET", api + "/capabilities", good}, {"GET", api + "/nope", good},
			{"GET", "/console/", ""}, {"GET", "/embed/report", ""},
		} {
			_, h, _ := s.do(t, req.method, req.path, req.token, nil)
			if got := h.Values("X-Reporting-Data-Class"); len(got) != 1 || got[0] != c.want {
				t.Errorf("simulated=%v %s %s: %q, want exactly %q", c.simulated, req.method, req.path, got, c.want)
			}
		}
	}
}
