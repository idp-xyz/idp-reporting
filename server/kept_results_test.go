package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// keptWorld is the shipped server over the shipped adapter `http`, with a source that vouches for the scope of its reads for an hour
// (S25-B2, ADR 0026): the simulator behind the reference source, its reads lasting readLife by the real clock. extra are further
// options of the connection. The source's own server is returned too, for a test that takes it away.
func keptWorld(t *testing.T, readLife time.Duration, extra map[string]any) (*world, *started, string, *httptest.Server) {
	t.Helper()
	w, src := httpWorldWith(t, extra, nil)
	w.sim.SetReadLifetime(readLife)
	w.sim.SetScopeLifetime(time.Hour)
	if _, err := Check(w.cfg, w.reg, w.getenv); err != nil {
		t.Fatal(err)
	}
	return w, startWorld(t, w), mint(t, w.keys[0], claims{}), src
}

// A connection that declares `kept_results` (docs/DEPLOYMENT.md) keeps what the platform makes from a read the source vouches for:
// through the configuration file, the shipped registry and the protocol, an export is downloaded after the read it was frozen from
// has expired, each time by the source's answer of the moment, while the working snapshot it was made from has ended with its read.
//
// The read lasts six seconds of the real clock, of which the freeze, the export and the first download need tenths of one; the wait
// for its end is on the server's answer (the freeze's view says expired), not on a sleep of that length. Six, because the wall clock
// of the machine this was written on is stepped forward by about three seconds now and then (a virtual machine under time
// correction), and a read shorter than a step is over before the export is asked for.
func TestAConnectionThatDeclaresKeptResultsServesAnExportAfterItsRead(t *testing.T) {
	const readLife = 6 * time.Second
	w, s, good, src := keptWorld(t, readLife, map[string]any{"kept_results": true})
	status, _, body := s.do(t, "POST", api+"/snapshot-jobs", good, contractsim.FinancialQuery(), "Idempotency-Key", "snap-1")
	var freeze r.JobView
	if status != 202 || json.Unmarshal(body, &freeze) != nil {
		t.Fatalf("snapshot: %d %s", status, body)
	}
	if freeze = s.waitJob(t, good, freeze.ID); freeze.State != "succeeded" {
		t.Fatalf("%+v", freeze)
	}
	status, _, body = s.do(t, "POST", api+"/exports", good, map[string]string{"snapshot_id": freeze.SnapshotID}, "Idempotency-Key", "export-1")
	var export r.JobView
	if status != 202 || json.Unmarshal(body, &export) != nil {
		t.Fatalf("export: %d %s", status, body)
	}
	if export = s.waitJob(t, good, export.ID); export.State != "succeeded" || export.ArtifactID == "" {
		t.Fatalf("%+v", export)
	}
	download := func() (int, string) {
		status, _, body := s.do(t, "GET", api+"/artifacts/"+export.ArtifactID+"/content", good, nil)
		if status == 200 && (len(body) < 4 || string(body[:2]) != "PK") {
			t.Fatalf("the download is not a workbook: %q", body[:min(len(body), 16)])
		}
		return status, string(body)
	}
	if status, body := download(); status != 200 {
		t.Fatalf("the download while the read lasts: %d %s", status, body)
	}

	// The read runs out: the working snapshot's job says so, and the snapshot's rows are no longer read.
	deadline := time.Now().Add(readLife + 20*time.Second)
	for {
		status, _, body = s.do(t, "GET", api+"/jobs/"+freeze.ID, good, nil)
		var view r.JobView
		if status != 200 || json.Unmarshal(body, &view) != nil {
			t.Fatalf("the freeze's view: %d %s", status, body)
		}
		if view.Expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the freeze did not expire: %s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status, _, body = s.do(t, "GET", api+"/snapshots/"+freeze.SnapshotID+"/rows?limit=10", good, nil); status != 409 || strings.TrimSpace(string(body)) != `{"error":"INTEGRITY_FAILURE"}` {
		t.Fatalf("the working snapshot's rows after its read (refused as an expired snapshot always was): %d %s", status, body)
	}
	// The export's file is kept: its job does not say expired, and the file is served, by the source's answer of the moment.
	status, _, body = s.do(t, "GET", api+"/jobs/"+export.ID, good, nil)
	var kept r.JobView
	if status != 200 || json.Unmarshal(body, &kept) != nil || kept.Expired || kept.ArtifactID != export.ArtifactID {
		t.Fatalf("the export's view after the read: %d %s", status, body)
	}
	if status, body := download(); status != 200 {
		t.Fatalf("the kept export after its read: %d %s", status, body)
	}
	owner := r.Owner{Identity: r.Identity{Tenant: testTenant, Issuer: testIssuer, Subject: testSubject}, Integration: w.finance}
	w.sim.Revoke(owner)
	if status, body := download(); status != 403 || strings.TrimSpace(body) != `{"error":"AUTH_SCOPE_DENIED"}` {
		t.Fatalf("the kept export after a revocation: %d %s", status, body)
	}
	w.sim.Grant(owner, contractsim.DefaultGrant())
	w.sim.SetUnavailable(true)
	if status, body := download(); status != 503 {
		t.Fatalf("the kept export with the source unavailable: %d %s", status, body)
	}
	w.sim.SetUnavailable(false)
	if status, body := download(); status != 200 {
		t.Fatalf("the kept export under the rights it was frozen on: %d %s", status, body)
	}
	// The source is gone altogether: a kept file is not handed out on the owner's word alone, and nothing says where the source was.
	src.Close()
	if status, body := download(); status != 503 || strings.Contains(body, "127.0.0.1") {
		t.Fatalf("the kept export with the source gone: %d %s", status, body)
	}
	for _, secret := range []string{testSourceAuth, "127.0.0.1:"} {
		if strings.Contains(s.log.String(), secret) {
			t.Fatalf("the log shows %q", secret)
		}
	}
}

// Without the declaration the same source's reads do not verify: the platform keeps nothing a connection did not declare, and says
// so as an integrity failure of the source's answer, without a word of the source to the caller.
func TestAConnectionThatDoesNotDeclareKeptResultsTakesNoVouchedRead(t *testing.T) {
	for name, extra := range map[string]map[string]any{"no option": nil, "the option false": {"kept_results": false}} {
		_, s, good, _ := keptWorld(t, time.Minute, extra)
		status, _, body := s.do(t, "POST", api+"/queries", good, contractsim.FinancialQuery())
		if status != 409 || strings.TrimSpace(string(body)) != `{"error":"INTEGRITY_FAILURE"}` {
			t.Fatalf("%s: a query through the undeclared connection: %d %s", name, status, body)
		}
		status, _, body = s.do(t, "POST", api+"/snapshot-jobs", good, contractsim.FinancialQuery(), "Idempotency-Key", "snap-1")
		if status != 409 || strings.TrimSpace(string(body)) != `{"error":"INTEGRITY_FAILURE"}` {
			t.Fatalf("%s: a freeze through the undeclared connection: %d %s", name, status, body)
		}
	}
	// An option that is not a boolean refuses the start at the adapters, as any malformed option does, and the value is not shown.
	w, _ := httpWorldWith(t, map[string]any{"kept_results": "yes-SECRET-VALUE"}, nil)
	se, log := refusedStart(t, w, w.reg, w.getenv, memoryOnly())
	if se.Step != StepAdapters || !strings.Contains(se.Error(), `the adapter "http" of integration "brant-contract-demo" could not be made (its error is not shown)`) || strings.Contains(se.Error()+log, "SECRET-VALUE") || strings.Contains(se.Error()+log, testSourceAuth) {
		t.Fatalf("kept_results that is not a boolean: %v\n%s", se, log)
	}
}
