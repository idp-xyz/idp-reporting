package httpsource_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/protocol"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
)

// The reason of these tests is what a person writes (Chinese, a space, punctuation) around a marker no error text may repeat.
const (
	reasonMarker = "REASON-MARKER"
	cancelReason = "客户退回 " + reasonMarker + "：金额有误"
)

func cancelling(c *httpsource.Config) { c.Cancellation = true }

// job is the n-th job id of a test.
func job(n int) string { return fmt.Sprintf("%032x", n) }

// reserve asks the source, through the adapter, to reserve a document on the first row of a fresh read.
func reserve(t *testing.T, g *rig, jobID string) r.IssueReservation {
	t.Helper()
	q, hash := query()
	p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
	if err != nil {
		t.Fatal(err)
	}
	page, err := g.adapter.ReadPage(ctx, g.sc, p, "", 1)
	if err != nil || len(page.Rows) != 1 {
		t.Fatal(page, err)
	}
	res, err := g.adapter.(r.DocumentAuthority).PrepareDocument(ctx, g.sc, r.IssueIntent{JobID: jobID, Prepared: p, RowSHA256: r.Digest(page.Rows[0]), TemplateID: strings.Repeat("b", 32), TemplateVersion: 1, TemplateSHA256: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// issued is a document the source confirmed through the adapter, as the cancellation that names it.
func issued(t *testing.T, g *rig, jobID string) r.IssueCancellation {
	t.Helper()
	res := reserve(t, g, jobID)
	rc, err := g.adapter.(r.DocumentAuthority).ConfirmIssue(ctx, g.sc, r.IssueConfirmation{JobID: jobID, ReservationRef: res.Ref, SHA256: strings.Repeat("d", 64), Pages: 1})
	if err != nil {
		t.Fatal(err)
	}
	return r.IssueCancellation{JobID: jobID, ReservationRef: res.Ref, ConfirmationRef: rc.Ref, Number: res.Number, Reason: cancelReason}
}

// silentAbout fails when an error's text holds anything of the cancellation: the reason, the number or one of the source's references.
func silentAbout(t *testing.T, err error, g *rig, c r.IssueCancellation) {
	t.Helper()
	noLeak(t, err, g.srv, reasonMarker, "客户", c.Number, c.ReservationRef, c.ConfirmationRef)
}

// notCancelled fails unless the source still holds the job's document as confirmed and not cancelled.
func notCancelled(t *testing.T, g *rig, jobID string) {
	t.Helper()
	if rec, ok := g.sim.IssueRecord(jobID); !ok || rec.State != contractsim.IssueConfirmed || rec.Cancelled() {
		t.Fatalf("the source holds %+v for the job", rec)
	}
}

// The cancellation (ADR 0025 decision 5, S24-A) is a capability the connection declares, as issuance and the company directory are:
// without `cancellation` the adapter is not a canceller and the route is never asked; with it, and an issuance, the adapter is one,
// whatever else the connection declares, and a cancellation is the source's.
func TestCancellationIsDeclaredByTheConnection(t *testing.T) {
	n := 0
	for _, issuance := range []string{httpsource.IssuanceRows, httpsource.IssuanceGroups} {
		for _, companies := range []bool{false, true} {
			name := fmt.Sprintf("issuance %s, companies %v", issuance, companies)
			declare := func(c *httpsource.Config) { c.Issuance, c.Companies = issuance, companies }
			silent := newRig(t, declare)
			if _, ok := silent.adapter.(r.DocumentCanceller); ok {
				t.Fatalf("%s without cancellation is a canceller", name)
			}
			g := newRig(t, declare, cancelling)
			canceller, ok := g.adapter.(r.DocumentCanceller)
			if !ok {
				t.Fatalf("%s with cancellation is not a canceller", name)
			}
			// What else the connection declared is what it was.
			_, authority := g.adapter.(r.DocumentAuthority)
			_, directory := g.adapter.(r.CompanyDirectory)
			if !authority || directory != companies || r.GroupIssueSupported(g.adapter) != (issuance == httpsource.IssuanceGroups) || r.GroupIssueSupported(silent.adapter) != r.GroupIssueSupported(g.adapter) || r.IssueContractVersion(silent.adapter) != r.IssueContractVersion(g.adapter) {
				t.Fatalf("%s: authority %v, directory %v, groups %v", name, authority, directory, r.GroupIssueSupported(g.adapter))
			}
			n++
			c := issued(t, g, job(n))
			receipt, err := canceller.CancelDocument(ctx, g.sc, c)
			rec, _ := g.sim.IssueRecord(c.JobID)
			if err != nil || !receipt.Valid() || receipt.Ref != rec.CancellationRef || !receipt.CancelledAt.Equal(rec.CancelledAt) || rec.CancelReason != cancelReason || rec.State != contractsim.IssueConfirmed || rec.Number != c.Number {
				t.Fatalf("%s: %+v %v, the source holds %+v", name, receipt, err, rec)
			}
			// Asked again, with another reason: the same receipt, and the source keeps the first reason.
			again := c
			again.Reason = "asked again"
			same, err := canceller.CancelDocument(ctx, g.sc, again)
			if rec, _ = g.sim.IssueRecord(c.JobID); err != nil || same.Ref != receipt.Ref || !same.CancelledAt.Equal(receipt.CancelledAt) || rec.CancelReason != cancelReason {
				t.Fatalf("%s: the repeat answered %+v %v, the source holds %+v", name, same, err, rec)
			}
		}
	}
	// A source cancels the documents it issues: a connection that issues nothing cannot declare it. The refusal names the field and
	// shows no value.
	for _, issuance := range []string{"", httpsource.IssuanceNone} {
		for _, companies := range []bool{false, true} {
			a, err := httpsource.New(httpsource.Config{BaseURL: "https://source.example.test", Token: testToken, Issuance: issuance, Companies: companies, Cancellation: true})
			if err == nil || a != nil || !strings.Contains(err.Error(), "cancellation") || strings.Contains(err.Error(), "source.example.test") || strings.Contains(err.Error(), testToken) {
				t.Fatalf("issuance %q, companies %v: %v %v", issuance, companies, a, err)
			}
		}
	}
	// The connection's options object says it with one boolean.
	getenv := func(string) string { return testToken }
	options := func(extra string) json.RawMessage {
		return json.RawMessage(`{"base_url":"https://source.example.test","auth_env":"SOURCE_AUTH"` + extra + `}`)
	}
	for extra, declared := range map[string]bool{
		`,"issuance":"rows","cancellation":true`:                     true,
		`,"issuance":"groups","cancellation":true`:                   true,
		`,"issuance":"groups","companies":true,"cancellation":true`:  true,
		`,"issuance":"rows","companies":true,"cancellation":true`:    true,
		`,"issuance":"rows"`:                                         false,
		`,"issuance":"groups","cancellation":false`:                  false,
		`,"issuance":"groups","companies":true,"cancellation":false`: false,
		`,"cancellation":false`:                                      false,
		``:                                                           false,
	} {
		a, err := httpsource.Factory(options(extra), getenv)
		if err != nil {
			t.Fatalf("%s: %v", extra, err)
		}
		if _, ok := a.(r.DocumentCanceller); ok != declared {
			t.Fatalf("%s: a canceller %v, declared %v", extra, ok, declared)
		}
		a.(interface{ Close() }).Close()
	}
	for _, extra := range []string{`,"cancellation":true`, `,"issuance":"none","cancellation":true`, `,"companies":true,"cancellation":true`, `,"issuance":"rows","cancellation":"true"`, `,"issuance":"rows","cancellation":1`, `,"issuance":"rows","cancellation":null`, `,"issuance":"rows","cancellations":true`} {
		if a, err := httpsource.Factory(options(extra), getenv); err == nil || a != nil || strings.Contains(err.Error(), "source.example.test") || strings.Contains(err.Error(), testToken) {
			t.Fatalf("%s: %v %v", extra, a, err)
		}
	}
}

// The request is the protocol's: one POST of the route with the context and the cancellation as the platform holds it, under the
// connection's credential. A request that is not of the contract's form is never sent.
func TestTheCancellationRequestIsTheProtocols(t *testing.T) {
	type seen struct {
		line   string
		header http.Header
		body   map[string]any
	}
	var mu sync.Mutex
	var requests []seen
	sim := contractsim.Financial()
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	ref := sourcetest.New(sim, sourcetest.Options{Token: testToken})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		if strings.Contains(req.URL.Path, "cancel") {
			m := map[string]any{}
			_ = json.Unmarshal(b, &m)
			mu.Lock()
			requests = append(requests, seen{req.Method + " " + req.URL.Path, req.Header.Clone(), m})
			mu.Unlock()
		}
		req.Body = io.NopCloser(strings.NewReader(string(b)))
		ref.ServeHTTP(w, req)
	}))
	defer srv.Close()
	g := &rig{sim: sim, srv: srv, adapter: adapterOf(t, srv, cancelling), sc: r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}}
	canceller := g.adapter.(r.DocumentCanceller)
	c := issued(t, g, job(1))
	receipt, err := canceller.CancelDocument(ctx, g.sc, c)
	if err != nil || !receipt.Valid() {
		t.Fatal(receipt, err)
	}
	mu.Lock()
	if len(requests) != 1 {
		t.Fatal("requests about a cancellation:", len(requests))
	}
	got := requests[0]
	mu.Unlock()
	// The route's text is pinned here as the document writes it; the constant the three parties share must be that text.
	const route = "/reporting-source/v1/issue-cancellations"
	if got.line != "POST "+route {
		t.Fatal(got.line)
	}
	if shared := protocol.PathCancellations; shared != route {
		t.Fatal("the protocol's path is", shared)
	}
	if h := got.header; h.Get("Authorization") != "Bearer "+testToken || h.Get("Content-Type") != "application/json" || !protocol.ValidJobID(h.Get(protocol.RequestIDHeader)) {
		t.Fatal(got.header)
	}
	wantBody := map[string]any{
		"context":      map[string]any{"platform_tenant": "platform_demo", "issuer": "urn:idp:contract-demo", "subject": "user_a", "integration": contractsim.FinanceIntegration, "source_tenant": "tenant_a"},
		"cancellation": map[string]any{"job_id": c.JobID, "reservation_ref": c.ReservationRef, "confirmation_ref": c.ConfirmationRef, "number": c.Number, "reason": cancelReason},
	}
	if !reflect.DeepEqual(got.body, wantBody) {
		t.Fatalf("the request's body:\n%v\nwant\n%v", got.body, wantBody)
	}
	// What is not of the contract's form is refused here and never sent: an incomplete context, and a cancellation that does not
	// name the document by the platform's job id and the source's own references and number, or whose reason is not 1..200
	// characters of text on one line.
	bad := map[string]func(*r.IssueCancellation){
		"a job id that is not the platform's":  func(c *r.IssueCancellation) { c.JobID = "../catalog" },
		"no job id":                            func(c *r.IssueCancellation) { c.JobID = "" },
		"no reservation reference":             func(c *r.IssueCancellation) { c.ReservationRef = "" },
		"a reservation reference with a space": func(c *r.IssueCancellation) { c.ReservationRef = "a b" },
		"no confirmation reference":            func(c *r.IssueCancellation) { c.ConfirmationRef = "" },
		"a confirmation reference of 129":      func(c *r.IssueCancellation) { c.ConfirmationRef = strings.Repeat("r", 129) },
		"no number":                            func(c *r.IssueCancellation) { c.Number = "" },
		"a number with a slash":                func(c *r.IssueCancellation) { c.Number = "AR/1" },
		"no reason":                            func(c *r.IssueCancellation) { c.Reason = "" },
		"a blank reason":                       func(c *r.IssueCancellation) { c.Reason = "   " },
		"a reason of two lines":                func(c *r.IssueCancellation) { c.Reason = "first\nsecond " + reasonMarker },
		"a reason with a control character":    func(c *r.IssueCancellation) { c.Reason = "bell\u0007 " + reasonMarker },
		"a reason of 201 characters":           func(c *r.IssueCancellation) { c.Reason = strings.Repeat("因", 201) },
		"a reason that is not UTF-8":           func(c *r.IssueCancellation) { c.Reason = "bad \xff " + reasonMarker },
	}
	other := issued(t, g, job(2))
	for name, change := range bad {
		in := other
		change(&in)
		out, err := canceller.CancelDocument(ctx, g.sc, in)
		want(t, err, r.ErrInvalid, name)
		if out != (r.IssueCancellationReceipt{}) {
			t.Fatalf("%s: a receipt was answered", name)
		}
		silentAbout(t, err, g, other)
	}
	incomplete := g.sc
	incomplete.SourceTenant = ""
	_, err = canceller.CancelDocument(ctx, incomplete, other)
	want(t, err, r.ErrDenied, "an incomplete context")
	silentAbout(t, err, g, other)
	mu.Lock()
	if len(requests) != 1 {
		t.Fatal("a request that is not of the contract's form was sent:", len(requests))
	}
	mu.Unlock()
	notCancelled(t, g, other.JobID)
	// The bound is inclusive and counts characters: 200 of them, three bytes each, travel whole.
	long := other
	long.Reason = strings.Repeat("因", 200)
	if _, err = canceller.CancelDocument(ctx, g.sc, long); err != nil {
		t.Fatal(err)
	}
	if rec, _ := sim.IssueRecord(other.JobID); rec.CancelReason != long.Reason {
		t.Fatal("the reason did not travel whole")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[1].body["cancellation"].(map[string]any)["reason"] != long.Reason {
		t.Fatal(len(requests))
	}
}

// A receipt that is not of the protocol's shape, not of the job that was asked, or whose reference or time the platform cannot keep is
// an integrity failure: nothing of it is passed on, and the error says nothing of the reason, the number or the references. The
// source has cancelled all the same (it decided before it answered), which the repeat by the job id finds: the same receipt.
func TestACancellationReceiptThatDoesNotVerifyIsAnIntegrityFailure(t *testing.T) {
	receipt := func(m map[string]any) map[string]any { return m["receipt"].(map[string]any) }
	edits := []struct {
		name string
		f    func(m map[string]any)
	}{
		{"another job id", func(m map[string]any) { receipt(m)["job_id"] = job(99) }},
		{"no job id", func(m map[string]any) { delete(receipt(m), "job_id") }},
		{"an empty job id", func(m map[string]any) { receipt(m)["job_id"] = "" }},
		{"no reference", func(m map[string]any) { delete(receipt(m), "ref") }},
		{"an empty reference", func(m map[string]any) { receipt(m)["ref"] = "" }},
		{"a reference with a space", func(m map[string]any) { receipt(m)["ref"] = "CXL 1" }},
		{"a reference of 129 bytes", func(m map[string]any) { receipt(m)["ref"] = strings.Repeat("r", 129) }},
		{"a reference that is not ASCII", func(m map[string]any) { receipt(m)["ref"] = "作废-1" }},
		{"a reference that is a number", func(m map[string]any) { receipt(m)["ref"] = 7 }},
		{"no time", func(m map[string]any) { delete(receipt(m), "cancelled_at") }},
		{"a time that is not one", func(m map[string]any) { receipt(m)["cancelled_at"] = "yesterday" }},
		{"a time not in canonical form", func(m map[string]any) {
			receipt(m)["cancelled_at"] = strings.Replace(receipt(m)["cancelled_at"].(string), "Z", ".000Z", 1)
		}},
		{"a time with a fraction of a second", func(m map[string]any) {
			receipt(m)["cancelled_at"] = strings.Replace(receipt(m)["cancelled_at"].(string), "Z", ".5Z", 1)
		}},
		{"a time without a zone", func(m map[string]any) {
			receipt(m)["cancelled_at"] = strings.TrimSuffix(receipt(m)["cancelled_at"].(string), "Z")
		}},
		{"a time in the year 10000", func(m map[string]any) { receipt(m)["cancelled_at"] = "10000-01-01T00:00:00Z" }},
		{"a time that is a number", func(m map[string]any) { receipt(m)["cancelled_at"] = 1793188800 }},
		{"an unknown field in the receipt", func(m map[string]any) { receipt(m)["reason"] = cancelReason }},
		{"a field of another case", func(m map[string]any) { receipt(m)["Ref"] = receipt(m)["ref"]; delete(receipt(m), "ref") }},
		{"a null receipt", func(m map[string]any) { m["receipt"] = nil }},
		{"no receipt", func(m map[string]any) { delete(m, "receipt") }},
		{"a receipt that is a list", func(m map[string]any) { m["receipt"] = []any{receipt(m)} }},
		{"an unknown field beside the receipt", func(m map[string]any) { m["cancelled"] = true }},
	}
	for i, c := range edits {
		g := newRig(t, cancelling)
		canceller := g.adapter.(r.DocumentCanceller)
		in := issued(t, g, job(i+1))
		g.set(edit("/issue-cancellations", c.f))
		out, err := canceller.CancelDocument(ctx, g.sc, in)
		want(t, err, r.ErrIntegrity, c.name)
		if out != (r.IssueCancellationReceipt{}) {
			t.Fatalf("%s: a receipt was passed on: %+v", c.name, out)
		}
		silentAbout(t, err, g, in)
		rec, _ := g.sim.IssueRecord(in.JobID)
		if !rec.Cancelled() {
			t.Fatalf("%s: the source did not cancel", c.name)
		}
		g.set(nil)
		again, err := canceller.CancelDocument(ctx, g.sc, in)
		if err != nil || again.Ref != rec.CancellationRef || !again.CancelledAt.Equal(rec.CancelledAt) {
			t.Fatalf("%s: the repeat answered %+v %v, the source holds %+v", c.name, again, err, rec)
		}
	}
	// A receipt of the job, well formed, in an answer that is not the protocol's.
	wellFormed := `{"receipt":{"job_id":"` + job(1) + `","ref":"CXL-1","cancelled_at":"2026-11-11T08:00:00Z"}}`
	for _, c := range []struct {
		name   string
		status int
		ct     string
		body   string
	}{
		{"trailing data", 200, "application/json", wellFormed + ` {}`},
		{"a duplicate key", 200, "application/json", `{"receipt":{},"receipt":{}}`},
		{"not JSON", 200, "application/json", `<html>SECRET-BODY</html>`},
		{"another content type", 200, "text/plain", wellFormed},
		{"an array", 200, "application/json", `[` + wellFormed + `]`},
		{"an empty object", 200, "application/json", `{}`},
		{"a success other than 200", 201, "application/json", wellFormed},
		{"a 204", 204, "", ``},
	} {
		g := newRig(t, cancelling)
		in := issued(t, g, job(1))
		g.set(raw("/issue-cancellations", c.status, c.ct, c.body))
		out, err := g.adapter.(r.DocumentCanceller).CancelDocument(ctx, g.sc, in)
		want(t, err, r.ErrIntegrity, c.name)
		if out != (r.IssueCancellationReceipt{}) {
			t.Fatalf("%s: a receipt was passed on: %+v", c.name, out)
		}
		silentAbout(t, err, g, in)
		noLeak(t, err, g.srv, "SECRET-BODY", "CXL-1")
	}
	// What verifies is passed on as it is: the source's own reference of any printable form and its time in any offset, to the second.
	g := newRig(t, cancelling)
	in := issued(t, g, job(1))
	g.set(edit("/issue-cancellations", func(m map[string]any) {
		receipt(m)["ref"], receipt(m)["cancelled_at"] = "CXL/2026-000017#a", "2026-11-11T16:00:07+08:00"
	}))
	out, err := g.adapter.(r.DocumentCanceller).CancelDocument(ctx, g.sc, in)
	if err != nil || out.Ref != "CXL/2026-000017#a" || !out.CancelledAt.Equal(time.Date(2026, 11, 11, 8, 0, 7, 0, time.UTC)) || !out.Valid() {
		t.Fatal(out, err)
	}
}

// The refusals of the route are the protocol's table. The source's own: denied for a principal that may not cancel, not_found for a job
// it never reserved, conflict for a document that is not a confirmed one or that the request names otherwise, unavailable. The
// route is an issue route, so not_found and conflict are its codes; a status without the protocol's body is a source that cannot serve.
func TestTheCancellationsRefusalsAreTheProtocols(t *testing.T) {
	g := newRig(t, cancelling)
	canceller := g.adapter.(r.DocumentCanceller)
	doc := issued(t, g, job(1))
	// A reservation that was never confirmed is not a document: conflict. A job the source never heard of: not found.
	res := reserve(t, g, job(2))
	_, err := canceller.CancelDocument(ctx, g.sc, r.IssueCancellation{JobID: res.JobID, ReservationRef: res.Ref, ConfirmationRef: doc.ConfirmationRef, Number: res.Number, Reason: cancelReason})
	want(t, err, r.ErrConflict, "a reservation that was not confirmed")
	silentAbout(t, err, g, doc)
	unknown := doc
	unknown.JobID = job(3)
	_, err = canceller.CancelDocument(ctx, g.sc, unknown)
	want(t, err, r.ErrNotFound, "a job the source never reserved")
	silentAbout(t, err, g, doc)
	// The request must name the document as the source gave it.
	for name, change := range map[string]func(*r.IssueCancellation){
		"another reservation reference":  func(c *r.IssueCancellation) { c.ReservationRef = res.Ref },
		"another confirmation reference": func(c *r.IssueCancellation) { c.ConfirmationRef = c.ConfirmationRef + "x" },
		"another number":                 func(c *r.IssueCancellation) { c.Number = res.Number },
	} {
		in := doc
		change(&in)
		_, err = canceller.CancelDocument(ctx, g.sc, in)
		want(t, err, r.ErrConflict, name)
		silentAbout(t, err, g, doc)
	}
	// The product decides who: a principal it does not grant issuing may not cancel, nor one it does not know.
	noIssue := contractsim.DefaultGrant()
	noIssue.IssueDocuments = false
	g.sim.Grant(g.sc.Owner, noIssue)
	_, err = canceller.CancelDocument(ctx, g.sc, doc)
	want(t, err, r.ErrDenied, "a principal that may not issue")
	silentAbout(t, err, g, doc)
	g.sim.Grant(g.sc.Owner, contractsim.DefaultGrant())
	stranger := g.sc
	stranger.Owner.Subject = "user_b"
	_, err = canceller.CancelDocument(ctx, stranger, doc)
	want(t, err, r.ErrDenied, "a principal the product does not know")
	// The product's own rules refuse, and an outage is an outage.
	g.sim.SetIssueRefused(true)
	_, err = canceller.CancelDocument(ctx, g.sc, doc)
	want(t, err, r.ErrDenied, "the product's own rules")
	g.sim.SetIssueRefused(false)
	g.sim.SetUnavailable(true)
	_, err = canceller.CancelDocument(ctx, g.sc, doc)
	want(t, err, r.ErrUnavailable, "an unavailable source")
	silentAbout(t, err, g, doc)
	g.sim.SetUnavailable(false)
	notCancelled(t, g, doc.JobID)

	// Every status and code a source may answer the route with. The rig answers in place of the reference source after it served the
	// request, so the product refuses throughout: what the adapter makes of the answer is under test, not what the product did.
	g.sim.SetIssueRefused(true)
	for _, c := range []struct {
		name   string
		status int
		ct     string
		body   string
		want   error
	}{
		{"invalid", 400, "application/json", `{"code":"invalid"}`, r.ErrInvalid},
		{"denied", 403, "application/json", `{"code":"denied"}`, r.ErrDenied},
		{"not_found", 404, "application/json", `{"code":"not_found"}`, r.ErrNotFound},
		{"conflict", 409, "application/json", `{"code":"conflict"}`, r.ErrConflict},
		{"expired", 410, "application/json", `{"code":"expired"}`, r.ErrDenied},
		{"unsupported", 422, "application/json", `{"code":"unsupported"}`, r.ErrUnsupported},
		{"integrity", 422, "application/json", `{"code":"integrity"}`, r.ErrIntegrity},
		{"budget", 429, "application/json", `{"code":"budget"}`, r.ErrBudget},
		{"unavailable", 503, "application/json", `{"code":"unavailable"}`, r.ErrUnavailable},
		{"a 401", 401, "", ``, r.ErrUnavailable},
		{"a 404 that is not the protocol's", 404, "text/plain", "404 page not found SECRET-BODY", r.ErrUnavailable},
		{"a 500 with a body", 500, "text/plain", "panic: SECRET-BODY " + cancelReason, r.ErrUnavailable},
		{"a 502", 502, "", ``, r.ErrUnavailable},
		{"a redirect", 307, "", ``, r.ErrUnavailable},
		{"a code with another status", 400, "application/json", `{"code":"conflict"}`, r.ErrIntegrity},
		{"a code the protocol does not have", 409, "application/json", `{"code":"already_cancelled"}`, r.ErrIntegrity},
		{"a refusal with a detail", 409, "application/json", `{"code":"conflict","detail":"SECRET-BODY"}`, r.ErrUnavailable},
	} {
		g.set(raw("/issue-cancellations", c.status, c.ct, c.body))
		out, err := canceller.CancelDocument(ctx, g.sc, doc)
		want(t, err, c.want, c.name)
		if out != (r.IssueCancellationReceipt{}) {
			t.Fatalf("%s: a receipt was answered", c.name)
		}
		silentAbout(t, err, g, doc)
		noLeak(t, err, g.srv, "SECRET-BODY", "already_cancelled")
	}
	g.set(nil)
	g.sim.SetIssueRefused(false)
	notCancelled(t, g, doc.JobID)
	// After every refusal the document is still the product's to cancel.
	if receipt, err := canceller.CancelDocument(ctx, g.sc, doc); err != nil || !receipt.Valid() {
		t.Fatal(receipt, err)
	}
}

// The shapes of an adapter that issues and cancels, behind the reference source: by rows or by groups, with or without the company
// directory, each saying a data class (S08-A); one that says none; and one that would cancel what it cannot issue.
type (
	cancelsRows struct {
		r.Adapter
		r.DocumentAuthority
		r.DocumentCanceller
		word
	}
	cancelsRowsDirectory struct {
		r.Adapter
		r.DocumentAuthority
		r.CompanyDirectory
		r.DocumentCanceller
		word
	}
	cancelsGroups struct {
		r.Adapter
		r.DocumentAuthority
		r.DocumentCanceller
		word
	}
	cancelsGroupsDirectory struct {
		r.Adapter
		r.DocumentAuthority
		r.CompanyDirectory
		r.DocumentCanceller
		word
	}
	cancelsSayingNothing struct {
		r.Adapter
		r.DocumentAuthority
		r.DocumentCanceller
	}
	cancelsWithoutIssuing struct {
		r.Adapter
		r.DocumentCanceller
		word
	}
)

func (cancelsGroups) IssueContractVersion() int          { return r.IssueContractGroups }
func (cancelsGroupsDirectory) IssueContractVersion() int { return r.IssueContractGroups }

// The reference composition of the tests (sourcetest.Remote) passes the cancellation on as it passes the issuance and the directory,
// and beside the data class (S08-A): the adapter it returns for a wrapped adapter that issues and cancels is a canceller, says the
// wrapped adapter's word (or none, as the wrapped adapter), keeps every other capability, and cancels through the protocol. A wrapped
// adapter that does not cancel, or that issues nothing, is reached through a connection that does not declare the capability.
func TestTheReferenceCompositionPassesTheCancellationOnBesideTheDataClass(t *testing.T) {
	g := newRig(t)
	n := 100
	for _, c := range []struct {
		name                                  string
		wrapped                               r.Adapter
		class                                 string
		cancels, authority, groups, directory bool
	}{
		{"the simulator itself", g.sim, "contract-simulator", true, true, true, true},
		{"rows", cancelsRows{g.sim, g.sim, g.sim, "manual-entry"}, "manual-entry", true, true, false, false},
		{"rows and a directory", cancelsRowsDirectory{g.sim, g.sim, g.sim, g.sim, "manual-entry"}, "manual-entry", true, true, false, true},
		{"groups", cancelsGroups{g.sim, g.sim, g.sim, "manual-entry"}, "manual-entry", true, true, true, false},
		{"groups and a directory", cancelsGroupsDirectory{g.sim, g.sim, g.sim, g.sim, "manual-entry"}, "manual-entry", true, true, true, true},
		{"an adapter that says no class", cancelsSayingNothing{g.sim, g.sim, g.sim}, "business-data", true, true, false, false},
		{"an adapter that issues and does not cancel", shapeRows{g.sim, g.sim, "manual-entry"}, "manual-entry", false, true, false, false},
		{"an adapter that would cancel what it cannot issue", cancelsWithoutIssuing{g.sim, g.sim, "manual-entry"}, "manual-entry", false, false, false, false},
	} {
		out := sourcetest.Remote(t, c.wrapped)
		canceller, cancels := out.(r.DocumentCanceller)
		_, authority := out.(r.DocumentAuthority)
		_, directory := out.(r.CompanyDirectory)
		_, closes := out.(interface{ Close() })
		if cancels != c.cancels || authority != c.authority || r.GroupIssueSupported(out) != c.groups || directory != c.directory || !closes || r.DataClassOf(out) != c.class {
			t.Fatalf("%s: a canceller %v, authority %v, groups %v, directory %v, closes %v, says %q", c.name, cancels, authority, r.GroupIssueSupported(out), directory, closes, r.DataClassOf(out))
		}
		if !cancels {
			continue
		}
		// It is the platform's adapter of the source: a document issued through it is cancelled through it, over the protocol.
		n++
		through := &rig{sim: g.sim, adapter: out, sc: g.sc}
		doc := issued(t, through, job(n))
		receipt, err := canceller.CancelDocument(ctx, g.sc, doc)
		if rec, _ := g.sim.IssueRecord(doc.JobID); err != nil || !receipt.Valid() || receipt.Ref != rec.CancellationRef || rec.CancelReason != cancelReason {
			t.Fatalf("%s: %+v %v, the source holds %+v", c.name, receipt, err, rec)
		}
	}
}

// noCanceller is a source product that issues and cannot cancel: embedding the interfaces promotes their methods only.
type noCanceller struct {
	r.Adapter
	r.DocumentAuthority
}

// A source that does not implement the route answers it unsupported; a connection that declares the capability anyway gets that
// refusal as it is. A slow source is unavailable within the timeout, the caller's own end is the caller's; in both the platform has
// no receipt, and asking again by the job id finds what the source did.
func TestASourceThatCannotCancelOrDoesNotAnswer(t *testing.T) {
	sim := contractsim.Financial()
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	srv := httptest.NewTLSServer(sourcetest.New(noCanceller{sim, sim}, sourcetest.Options{Token: testToken}))
	defer srv.Close()
	g := &rig{sim: sim, srv: srv, adapter: adapterOf(t, srv, func(c *httpsource.Config) { c.Issuance = httpsource.IssuanceRows }, cancelling), sc: r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}}
	doc := issued(t, g, job(1))
	_, err := g.adapter.(r.DocumentCanceller).CancelDocument(ctx, g.sc, doc)
	want(t, err, r.ErrUnsupported, "a source without the route")
	silentAbout(t, err, g, doc)
	notCancelled(t, g, doc.JobID)

	// The source cancels and then takes longer than the timeout to say so.
	slow := newRig(t, cancelling, func(c *httpsource.Config) { c.Timeout = time.Second })
	late := issued(t, slow, job(2))
	slow.set(func(path string, status int, body []byte) (int, string, []byte, bool) {
		if strings.HasSuffix(path, "/issue-cancellations") {
			time.Sleep(1500 * time.Millisecond)
		}
		return 0, "", nil, false
	})
	start := time.Now()
	out, err := slow.adapter.(r.DocumentCanceller).CancelDocument(ctx, slow.sc, late)
	want(t, err, r.ErrUnavailable, "a slow source")
	if time.Since(start) > 5*time.Second || out != (r.IssueCancellationReceipt{}) {
		t.Fatal("the timeout did not bound the request:", time.Since(start), out)
	}
	silentAbout(t, err, slow, late)
	slow.set(nil)
	rec, _ := slow.sim.IssueRecord(late.JobID)
	again, err := slow.adapter.(r.DocumentCanceller).CancelDocument(ctx, slow.sc, late)
	if err != nil || !rec.Cancelled() || again.Ref != rec.CancellationRef || !again.CancelledAt.Equal(rec.CancelledAt) {
		t.Fatalf("the repeat answered %+v %v, the source holds %+v", again, err, rec)
	}
	// The caller's own end is the caller's error, and a context that has ended sends nothing.
	ended, cancel := context.WithCancel(ctx)
	cancel()
	other := issued(t, slow, job(3))
	_, err = slow.adapter.(r.DocumentCanceller).CancelDocument(ended, slow.sc, other)
	want(t, err, context.Canceled, "a context that has ended")
	notCancelled(t, slow, other.JobID)
}
