package server

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
)

const testSourceAuth = "SOURCE-AUTH-Zq7-never-shown-0123456789"

// httpWorld is a world whose one connection is the shipped adapter `http` to the simulator behind the reference source (S19-A), on a
// loopback TLS server whose certificate is pinned by ca_file, with the credential in the environment. Simulators are not allowed.
func httpWorld(t *testing.T) (*world, *httptest.Server) {
	t.Helper()
	return httpWorldWith(t, nil, nil)
}

// httpWorldWith is httpWorld with further options of the connection (issuance, cancellation) and, when observe is not nil, with every
// request the source receives shown to it before it is served.
func httpWorldWith(t *testing.T, extra map[string]any, observe func(*http.Request)) (*world, *httptest.Server) {
	t.Helper()
	w := newWorld(t)
	src := httptest.NewTLSServer(sourcetest.New(w.sim, sourcetest.Options{Token: testSourceAuth, Observe: observe}))
	t.Cleanup(src.Close)
	ca := filepath.Join(w.dir, "source-ca.pem")
	writeFile(t, ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: src.Certificate().Raw}), 0o644)
	o := map[string]any{"base_url": src.URL, "ca_file": ca, "auth_env": "REPORTING_SOURCE_AUTH", "timeout": 5, "companies": true}
	for k, v := range extra {
		o[k] = v
	}
	options, _ := json.Marshal(o)
	w.cfg.AllowSimulator = false
	w.cfg.Connections[0].Adapter = HTTPAdapter
	w.cfg.Connections[0].Options = options
	w.env = map[string]string{"REPORTING_SOURCE_AUTH": testSourceAuth}
	w.reg = DefaultRegistry()
	return w, src
}

func TestTheShippedRegistryServesAnHTTPSourceEndToEnd(t *testing.T) {
	w, src := httpWorld(t)
	if _, err := Check(w.cfg, w.reg, w.getenv); err != nil {
		t.Fatal(err)
	}
	s := startWorld(t, w)
	good := mint(t, w.keys[0], claims{})
	status, _, body := s.do(t, "GET", api+"/capabilities", good, nil)
	var caps struct {
		Profile string `json:"profile"`
	}
	if status != 200 || json.Unmarshal(body, &caps) != nil || strings.HasPrefix(caps.Profile, "simulated:") {
		t.Fatalf("capabilities: %d %s", status, body)
	}
	status, _, body = s.do(t, "GET", api+"/catalog?integration_id="+w.finance, good, nil)
	if status != 200 || !strings.Contains(string(body), "receivables.aging") {
		t.Fatalf("catalog: %d %s", status, body)
	}
	// S08-A (ADR 0012 decision 6): the shipped adapter says no data class and the protocol carries none, so whatever stands behind the
	// protocol is a source product's data to the platform: the catalog's entry and the evidence say business-data.
	if !strings.Contains(string(body), `"data_class":"business-data"`) {
		t.Fatalf("the catalog does not say business-data: %s", body)
	}
	// S19-B: the connection declares companies, so the directory is asked over the protocol and answered in the platform's shape.
	status, _, body = s.do(t, "GET", api+"/companies?integration_id="+w.finance, good, nil)
	if status != 200 || strings.TrimSpace(string(body)) != `{"supported":true,"partial":false,"companies":[{"id":"company_demo","name":"演示公司"}]}` {
		t.Fatalf("companies: %d %s", status, body)
	}
	status, header, body := s.do(t, "POST", api+"/queries", good, contractsim.FinancialQuery())
	var result r.Result
	if status != 200 || json.Unmarshal(body, &result) != nil || len(result.Rows) != 3 || result.Totals["ar.open_balance"] != "9007199254740993.01" {
		t.Fatalf("query: %d %s", status, body)
	}
	if result.Evidence.DataClass != "business-data" || header.Get("X-Reporting-Data-Class") != "business-data" {
		t.Fatalf("the evidence says %q under the header %q", result.Evidence.DataClass, header.Get("X-Reporting-Data-Class"))
	}
	status, _, body = s.do(t, "POST", api+"/snapshot-jobs", good, contractsim.FinancialQuery(), "Idempotency-Key", "snap-1")
	var job r.JobView
	if status != 202 || json.Unmarshal(body, &job) != nil {
		t.Fatalf("snapshot: %d %s", status, body)
	}
	if job = s.waitJob(t, good, job.ID); job.State != "succeeded" {
		t.Fatalf("%+v", job)
	}
	// The source revokes the grant: the platform refuses the frozen rows, as for an adapter in process.
	w.sim.Revoke(r.Owner{Identity: r.Identity{Tenant: testTenant, Issuer: testIssuer, Subject: testSubject}, Integration: w.finance})
	if status, _, body = s.do(t, "GET", api+"/snapshots/"+job.SnapshotID+"/rows?limit=10", good, nil); status != 403 {
		t.Fatalf("rows after the revocation: %d %s", status, body)
	}
	// The source cannot be reached: 503, and no word of where it is.
	src.Close()
	status, _, body = s.do(t, "POST", api+"/queries", good, contractsim.FinancialQuery())
	if status != 503 || strings.Contains(string(body), "127.0.0.1") {
		t.Fatalf("query to a closed source: %d %s", status, body)
	}
	for _, secret := range []string{testSourceAuth, src.URL, strings.TrimPrefix(src.URL, "https://")} {
		if strings.Contains(s.log.String(), secret) {
			t.Fatalf("the log shows %q", secret)
		}
	}
}

func TestABadHTTPConnectionRefusesTheStartAndIsNeverShown(t *testing.T) {
	for name, options := range map[string]string{
		"plain http to a host":   `{"base_url":"http://SECRET-HOST.example.test/x","auth_env":"REPORTING_SOURCE_AUTH"}`,
		"an unknown field":       `{"base_url":"https://SECRET-HOST.example.test","auth_env":"REPORTING_SOURCE_AUTH","proxy":"SECRET-HOST"}`,
		"an unset credential":    `{"base_url":"https://SECRET-HOST.example.test","auth_env":"REPORTING_SECRET_HOST_UNSET"}`,
		"no credential":          `{"base_url":"https://SECRET-HOST.example.test"}`,
		"a CA that is not there": `{"base_url":"https://SECRET-HOST.example.test","auth_env":"REPORTING_SOURCE_AUTH","ca_file":"/nonexistent/SECRET-HOST.pem"}`,
	} {
		w, _ := httpWorld(t)
		w.cfg.Connections[0].Options = json.RawMessage(options)
		se, log := refusedStart(t, w, w.reg, w.getenv, memoryOnly())
		if se.Step != StepAdapters || !strings.Contains(se.Error(), `the adapter "http" of integration "brant-contract-demo" could not be made (its error is not shown)`) {
			t.Fatalf("%s: %v", name, se)
		}
		for _, secret := range []string{"SECRET-HOST", testSourceAuth} {
			if strings.Contains(se.Error()+log, secret) {
				t.Fatalf("%s: shows %q: %v %s", name, secret, se, log)
			}
		}
	}
}

func TestTheHTTPAdapterIsNotASimulator(t *testing.T) {
	w, _ := httpWorld(t)
	a, err := DefaultRegistry()[HTTPAdapter](w.cfg.Connections[0].Options, w.getenv)
	if err != nil || isSimulator(a) {
		t.Fatal(a, err)
	}
	if len(DefaultRegistry()) != 1 {
		t.Fatal("the shipped registry has more than the http adapter")
	}
}
