package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource/protocol"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
)

// The cancellation of an issued document through the shipped server (S24-A, ADR 0025): the configuration's `cancellation` option of the
// adapter `http`, the capabilities, the route, the views, the download's refusal, the audit and the log.

// cancelReasonMarker is in every reason of these tests: the log must never carry it.
const cancelReasonMarker = "REASON-MARKER-51f"

// stampOnly issues from its request alone. The server under test has no renderer and cannot issue; the documents it is asked to cancel
// were issued before it started, into the store it is started on.
type stampOnly struct{}

func (stampOnly) Check(context.Context, r.Job, r.Snapshot) error { return nil }
func (stampOnly) Issue(_ context.Context, req r.IssueRequest) (r.IssueResult, error) {
	return r.IssueResult{PDF: []byte("%PDF-1.7\n" + req.Stamp + "\n%%EOF"), Pages: 1}, nil
}

type pdfStub struct{}

func (pdfStub) Write(context.Context, r.Snapshot) ([]byte, error) { return []byte("%PDF-1.7 x"), nil }

// issuedBefore issues n documents of the world's subject into the store through the world's own connection (the shipped adapter, made
// as the server makes it), as a server with a renderer would have before this one started. It returns their views, oldest first.
func issuedBefore(t *testing.T, w *world, store *MemoryStore, n int) []r.IssueView {
	t.Helper()
	ctx := t.Context()
	adapter, err := DefaultRegistry()[HTTPAdapter](w.cfg.Connections[0].Options, w.getenv)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := adapter.(interface{ Close() }); ok {
		t.Cleanup(c.Close)
	}
	p := r.Principal{Identity: r.Identity{Tenant: testTenant, Issuer: testIssuer, Subject: testSubject}, Integrations: []string{w.finance}}
	svc, err := r.NewService(r.Config{Repository: store, Connections: []r.Connection{{Tenant: testTenant, Integration: w.finance, SourceTenant: "tenant_a", Adapter: adapter}},
		CursorKey: []byte(strings.Repeat("k", 32)), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": pdfStub{}}, Issuer: stampOnly{}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(id string) r.JobView {
		t.Helper()
		if ran, err := svc.RunOne(ctx); !ran || err != nil {
			t.Fatal(ran, err)
		}
		j, err := svc.Job(ctx, p, id)
		if err != nil || j.State != "succeeded" {
			t.Fatalf("%+v %v", j, err)
		}
		return j
	}
	snap, err := svc.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "before-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	snap = run(snap.ID)
	var out []r.IssueView
	for i := range n {
		issue, err := svc.SubmitIssue(ctx, p, r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(`{"schema_version":"1.0"}`), SnapshotID: snap.SnapshotID, RowIndex: i}, "before-issue-"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		run(issue.ID)
		view, err := svc.Issue(ctx, p, issue.ID)
		if err != nil || view.Number == "" || view.ArtifactID == "" || view.Cancellation != nil {
			t.Fatalf("%+v %v", view, err)
		}
		out = append(out, view)
	}
	return out
}

// sourceRequests counts the requests a source received, by path.
type sourceRequests struct {
	mu    sync.Mutex
	paths map[string]int
}

func (s *sourceRequests) observe(req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths == nil {
		s.paths = map[string]int{}
	}
	s.paths[req.URL.Path]++
}
func (s *sourceRequests) cancellations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[protocol.PathCancellations]
}

func TestTheShippedServerCancelsAnIssueThroughAnHTTPSource(t *testing.T) {
	var asked sourceRequests
	w, src := httpWorldWith(t, map[string]any{"issuance": "rows", "cancellation": true}, asked.observe)
	if _, err := Check(w.cfg, w.reg, w.getenv); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStoreForTests(time.Now)
	issued := issuedBefore(t, w, store, 3)
	first, second, third := issued[0], issued[1], issued[2]
	s := startWith(t, w, WithStoreForTests(store), WithPollIntervalForTests(5*time.Millisecond))
	good := mint(t, w.keys[0], claims{})
	const reason = "客户退回 " + cancelReasonMarker + "：金额有误"
	body := func(text string) map[string]string { return map[string]string{"reason": text} }

	// The capabilities: this server cannot issue (it has no renderer), and it can ask the product to cancel what was issued.
	status, _, raw := s.do(t, "GET", api+"/capabilities", good, nil)
	if status != 200 || !strings.Contains(string(raw), `"issue_cancellation":{"`+w.finance+`":true}`) || !strings.Contains(string(raw), `"issuance":false`) || !strings.Contains(string(raw), `"POST /issues/{id}/cancellation"`) {
		t.Fatalf("capabilities: %d %s", status, raw)
	}
	if status, _, raw = s.do(t, "GET", api+"/artifacts/"+first.ArtifactID+"/content", good, nil); status != 200 || !strings.Contains(string(raw), first.Number) {
		t.Fatalf("the document before its cancellation: %d %s", status, raw)
	}
	// The cancellation: asked of the product over the protocol, recorded, answered as the issue's view.
	status, header, raw := s.do(t, "POST", api+"/issues/"+first.ID+"/cancellation", good, body(reason))
	var view r.IssueView
	if status != 200 || json.Unmarshal(raw, &view) != nil || header.Get("Cache-Control") != "no-store" || header.Get("X-Reporting-Data-Class") != "business-data" {
		t.Fatalf("the cancellation: %d %v %s", status, header, raw)
	}
	theirs, held := w.sim.IssueRecord(first.ID)
	if !held || !theirs.Cancelled() || theirs.CancelReason != reason || theirs.State != contractsim.IssueConfirmed || asked.cancellations() != 1 {
		t.Fatalf("the product holds %+v after %d requests", theirs, asked.cancellations())
	}
	c := view.Cancellation
	if c == nil || c.Ref != theirs.CancellationRef || c.CancelledAt != theirs.CancelledAt.UTC().Format(time.RFC3339) || c.Reason != reason {
		t.Fatalf("the view's cancellation %+v, the product's %+v", c, theirs)
	}
	// The record keeps every field it had: without its cancellation the view is the one of before, to the byte.
	kept := view
	kept.Cancellation = nil
	if was, is := mustJSON(first), mustJSON(kept); was != is || kept.State != "succeeded" {
		t.Fatalf("the cancellation changed the record:\n%s\n%s", is, was)
	}
	// The views say it, the file is no longer handed out, and the others are as they were.
	if status, _, raw = s.do(t, "GET", api+"/issues/"+first.ID, good, nil); status != 200 || !strings.Contains(string(raw), `"cancellation":{"ref":"`+c.Ref+`","cancelled_at":"`+c.CancelledAt+`","reason":"`+reason+`"}`) {
		t.Fatalf("the issue: %d %s", status, raw)
	}
	if status, _, raw = s.do(t, "GET", api+"/artifacts/"+first.ArtifactID+"/content", good, nil); status != 409 || strings.TrimSpace(string(raw)) != `{"error":"ISSUE_CANCELLED"}` {
		t.Fatalf("the download of the cancelled document: %d %s", status, raw)
	}
	if status, _, raw = s.do(t, "GET", api+"/artifacts/"+second.ArtifactID+"/content", good, nil); status != 200 {
		t.Fatalf("the download of another document: %d %s", status, raw)
	}
	// Asked again with another reason: the same answer, and the product is not asked again.
	status, _, again := s.do(t, "POST", api+"/issues/"+first.ID+"/cancellation", good, body("asked again "+cancelReasonMarker))
	var repeated r.IssueView
	if status != 200 || json.Unmarshal(again, &repeated) != nil || repeated.Cancellation == nil || *repeated.Cancellation != *c || asked.cancellations() != 1 {
		t.Fatalf("the repeat: %d %s after %d requests", status, again, asked.cancellations())
	}
	// The product's refusal travels as the platform's code and nothing is recorded; so does its outage.
	w.sim.SetIssueRefused(true)
	if status, _, raw = s.do(t, "POST", api+"/issues/"+second.ID+"/cancellation", good, body(reason)); status != 403 || strings.TrimSpace(string(raw)) != `{"error":"AUTH_SCOPE_DENIED"}` {
		t.Fatalf("a cancellation the product refuses: %d %s", status, raw)
	}
	w.sim.SetIssueRefused(false)
	// What is not a request is refused before the product is asked.
	for name, bad := range map[string]any{"no reason": map[string]any{}, "an unknown field": map[string]any{"reason": reason, "force": true}, "a reason of two lines": body("first\nsecond " + cancelReasonMarker)} {
		if status, _, raw = s.do(t, "POST", api+"/issues/"+second.ID+"/cancellation", good, bad); status != 400 || strings.TrimSpace(string(raw)) != `{"error":"INVALID_REQUEST"}` {
			t.Fatalf("%s: %d %s", name, status, raw)
		}
	}
	if asked.cancellations() != 2 {
		t.Fatal("the product was asked", asked.cancellations(), "times")
	}
	// The audit, as the person reads it: asked, done, asked again, asked and refused four times. No event carries a value.
	status, _, raw = s.do(t, "GET", api+"/audit?limit=100", good, nil)
	var events []struct {
		Action   string `json:"action"`
		Outcome  string `json:"outcome"`
		ObjectID string `json:"object_id"`
	}
	if status != 200 || json.Unmarshal(raw, &events) != nil || strings.Contains(string(raw), cancelReasonMarker) || strings.Contains(string(raw), first.Number) {
		t.Fatalf("the audit: %d %s", status, raw)
	}
	var got []string
	for i := len(events) - 1; i >= 0; i-- {
		if e := events[i]; e.Action == "issue.cancel" {
			which := "second"
			if e.ObjectID == first.ID {
				which = "first"
			}
			got = append(got, e.Outcome+":"+which)
		}
	}
	if want := "requested:first succeeded:first requested:first requested:second refused:second requested:second refused:second requested:second refused:second requested:second refused:second"; strings.Join(got, " ") != want {
		t.Fatalf("the audit of the cancellations:\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
	// The source cannot be reached: 503, nothing recorded, and no word of where it is.
	src.Close()
	if status, _, raw = s.do(t, "POST", api+"/issues/"+third.ID+"/cancellation", good, body(reason)); status != 503 || strings.TrimSpace(string(raw)) != `{"error":"AUTHORITY_UNAVAILABLE"}` {
		t.Fatalf("a cancellation while the source is closed: %d %s", status, raw)
	}
	if rec, _ := w.sim.IssueRecord(third.ID); rec.Cancelled() {
		t.Fatal("the product cancelled a document it was never asked about")
	}
	if status, _, raw = s.do(t, "GET", api+"/issues/"+third.ID, good, nil); status != 200 || strings.Contains(string(raw), "cancellation") {
		t.Fatalf("the issue after the outage: %d %s", status, raw)
	}
	// The log: an access line for the route, by its pattern, and nothing of the reason, the number, the product's references, the
	// credential or the source's address.
	routes := 0
	for _, line := range s.log.events(t, "access") {
		if line["route"] == "POST "+api+"/issues/{id}/cancellation" {
			routes++
		}
	}
	if routes != 7 {
		t.Fatalf("%d access lines of the cancellation route:\n%s", routes, s.log.String())
	}
	for _, secret := range []string{cancelReasonMarker, "客户", "金额", first.Number, second.Number, c.Ref, first.ReservationRef, first.ConfirmationRef, testSourceAuth, src.URL, strings.TrimPrefix(src.URL, "https://")} {
		if strings.Contains(s.log.String(), secret) {
			t.Fatalf("the log shows %q:\n%s", secret, s.log.String())
		}
	}
}

// A connection that does not declare `cancellation` is as it was before S24-A, whatever its source could do: the capabilities say
// false, the route answers 422 and the source's cancellation route is never asked.
func TestAConnectionWithoutTheOptionCannotBeAskedToCancel(t *testing.T) {
	var asked sourceRequests
	w, _ := httpWorldWith(t, map[string]any{"issuance": "rows"}, asked.observe)
	store := NewMemoryStoreForTests(time.Now)
	issued := issuedBefore(t, w, store, 1)
	s := startWith(t, w, WithStoreForTests(store), WithPollIntervalForTests(5*time.Millisecond))
	good := mint(t, w.keys[0], claims{})
	status, _, raw := s.do(t, "GET", api+"/capabilities", good, nil)
	if status != 200 || !strings.Contains(string(raw), `"issue_cancellation":{"`+w.finance+`":false}`) {
		t.Fatalf("capabilities: %d %s", status, raw)
	}
	status, _, raw = s.do(t, "POST", api+"/issues/"+issued[0].ID+"/cancellation", good, map[string]string{"reason": "a reason " + cancelReasonMarker})
	if status != 422 || strings.TrimSpace(string(raw)) != `{"error":"UNSUPPORTED_COMBINATION"}` || asked.cancellations() != 0 {
		t.Fatalf("a cancellation through a connection without the option: %d %s, the source asked %d times", status, raw, asked.cancellations())
	}
	if rec, _ := w.sim.IssueRecord(issued[0].ID); rec.Cancelled() {
		t.Fatal("the product cancelled")
	}
	if status, _, raw = s.do(t, "GET", api+"/artifacts/"+issued[0].ArtifactID+"/content", good, nil); status != 200 {
		t.Fatalf("the download: %d %s", status, raw)
	}
	// A configuration that declares the cancellation of a source that issues nothing refuses the start, and shows no value.
	bad, _ := httpWorldWith(t, map[string]any{"cancellation": true}, nil)
	se, log := refusedStart(t, bad, bad.reg, bad.getenv, memoryOnly())
	if se.Step != StepAdapters || strings.Contains(se.Error()+log, testSourceAuth) || strings.Contains(se.Error()+log, "127.0.0.1") {
		t.Fatalf("%v\n%s", se, log)
	}
}
