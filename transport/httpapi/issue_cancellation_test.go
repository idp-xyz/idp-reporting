package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/transport/httpapi"
)

// The cancellation of an issued document over HTTP (S24-A, ADR 0025 decisions 2 to 5): POST /issues/{id}/cancellation.

// reasonMarker is in every reason these tests send: no refusal, no header and no audit event may repeat it.
const reasonMarker = "REASON-MARKER-7c1"

// askedProduct is the finance simulator with the cancellations it was asked for counted.
type askedProduct struct {
	*contractsim.Simulator
	mu      sync.Mutex
	cancels int
}

func (p *askedProduct) CancelDocument(ctx context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	p.mu.Lock()
	p.cancels++
	p.mu.Unlock()
	return p.Simulator.CancelDocument(ctx, sc, c)
}

func (p *askedProduct) asked() int { p.mu.Lock(); defer p.mu.Unlock(); return p.cancels }

// issuingOnly is a product that issues and cannot be asked to cancel: the four source calls and the document authority, nothing else.
type issuingOnly struct {
	r.Adapter
	r.DocumentAuthority
}

// deafLog accepts every event and keeps none: the route's own events succeed while the store's audit is down.
type deafLog struct{ r.AuditLog }

func (deafLog) Record(context.Context, r.AuditEvent) error { return nil }

// cancelHTTP is issueHTTP with its template published and the API (the core and the template routes) composed again under an
// authenticator whose roles and subject the test sets: api without an audit, audited under the store's.
type cancelHTTP struct {
	*issueHTTP
	product *askedProduct
	api     http.Handler
	audited http.Handler
	roles   []string
	subject string
}

func newCancelHTTP(t *testing.T, hideTheCapability bool) *cancelHTTP {
	t.Helper()
	c := &cancelHTTP{product: &askedProduct{}}
	c.issueHTTP = newIssueHTTP(t, func(a r.Adapter) r.Adapter {
		c.product.Simulator = a.(*contractsim.Simulator)
		if hideTheCapability {
			return issuingOnly{c.product, c.product}
		}
		return c.product
	})
	c.publish()
	demo, _ := httpapi.DemoBearer(secret, c.p)
	auth := func(req *http.Request) (r.Principal, error) {
		p, e := demo(req)
		p.Roles = c.roles
		if c.subject != "" {
			p.Subject = c.subject
		}
		return p, e
	}
	core, e := httpapi.Handler(c.service, auth)
	if e != nil {
		t.Fatal(e)
	}
	if c.api, e = httpapi.WithDocumentTemplates(core, c.service, auth, c.store, c.renderer, nil); e != nil {
		t.Fatal(e)
	}
	c.audited = httpapi.WithAudit(c.api, c.repo)
	return c
}

const apiBase = "/api/reporting/v1"

// queued admits an issue of the row and returns its job id; nothing has run.
func (c *cancelHTTP) queued(row int, key string) string {
	c.t.Helper()
	code, out := c.post(c.input(row), key)
	if code != 202 {
		c.t.Fatal(code, out)
	}
	return out["job_id"].(string)
}

// issued issues one document of the row and returns its id and its view.
func (c *cancelHTTP) issued(row int, key string) (string, map[string]any) {
	c.t.Helper()
	id := c.queued(row, key)
	if _, e := c.service.RunOne(c.t.Context()); e != nil {
		c.t.Fatal(e)
	}
	view := c.view(id)
	if view["state"] != "succeeded" || view["number"] == nil || view["artifact_id"] == nil {
		c.t.Fatal(view)
	}
	return id, view
}

// view is GET /issues/{id}, decoded.
func (c *cancelHTTP) view(id string) map[string]any {
	c.t.Helper()
	w := request(c.api, "GET", apiBase+"/issues/"+id, nil, true)
	var out map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		c.t.Fatal(w.Code, w.Body.String())
	}
	return out
}

// cancel asks for the cancellation of the issue with the body, through h.
func (c *cancelHTTP) cancel(h http.Handler, id, body string) *httptest.ResponseRecorder {
	return request(h, "POST", apiBase+"/issues/"+id+"/cancellation", []byte(body), true)
}

// download is the status and the body of the artifact's download.
func (c *cancelHTTP) download(artifact string) *httptest.ResponseRecorder {
	return request(c.api, "GET", apiBase+"/artifacts/"+artifact+"/content", nil, true)
}

// cancelEvents are the principal's audit events of cancellations and of refused roles, oldest first, as "action:outcome:object id".
func (c *cancelHTTP) cancelEvents() []string {
	c.t.Helper()
	events, e := c.repo.Events(c.t.Context(), c.p.Identity, false, r.MaxAuditListing, "")
	if e != nil {
		c.t.Fatal(e)
	}
	out := []string{}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Action != r.AuditIssueCancel && ev.Action != "role.refused" {
			continue
		}
		if b, _ := json.Marshal(ev); strings.Contains(string(b), reasonMarker) || strings.Contains(string(b), "AR-0000") {
			c.t.Fatalf("an audit event carries a reason or a number: %s", b)
		}
		if ev.Action == r.AuditIssueCancel && (ev.ObjectKind != "issue" || ev.Code != "") {
			c.t.Fatalf("%+v", ev)
		}
		out = append(out, ev.Action+":"+ev.Outcome+":"+ev.ObjectID)
	}
	return out
}

// refused fails unless the answer is the status with exactly the code, in the problem shape, and says nothing of the reason.
func refused(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || strings.TrimSpace(w.Body.String()) != `{"error":"`+code+`"}` {
		t.Fatalf("%s: %d %s, want %d %s", what, w.Code, w.Body.String(), status, code)
	}
	for name, values := range w.Header() {
		if strings.Contains(strings.Join(values, " "), reasonMarker) {
			t.Fatalf("%s: the header %s carries the reason", what, name)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%s: %v", what, w.Header())
	}
}

func reasonBody(reason string) string {
	b, _ := json.Marshal(map[string]string{"reason": reason})
	return string(b)
}

// The route asks the product and answers the issue as the listing shows it, with its cancellation; the record keeps every field it had;
// the file is no longer handed out; asking again answers the same and does not ask the product; the audit says who asked and that it
// was done, once.
func TestTheCancellationRouteAsksTheProductAndAnswersTheIssue(t *testing.T) {
	c := newCancelHTTP(t, false)
	const reason = "客户退回 " + reasonMarker + "：金额有误"
	// The capabilities say, per product the caller is served, that a cancellation can be asked, and name the route.
	caps := request(c.audited, "GET", apiBase+"/capabilities", nil, true)
	var capabilities struct {
		Cancellation map[string]bool `json:"issue_cancellation"`
		Routes       []string        `json:"routes"`
	}
	if caps.Code != 200 || json.Unmarshal(caps.Body.Bytes(), &capabilities) != nil || !reflect.DeepEqual(capabilities.Cancellation, map[string]bool{contractsim.FinanceIntegration: true}) || capabilities.Routes[len(capabilities.Routes)-1] != "POST /issues/{id}/cancellation" {
		t.Fatal(caps.Code, caps.Body.String())
	}
	id, before := c.issued(2, "one")
	if _, has := before["cancellation"]; has || before["integration_id"] != contractsim.FinanceIntegration || before["number"] != "AR-000001" {
		t.Fatal(before)
	}
	artifact := before["artifact_id"].(string)
	if w := c.download(artifact); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "%PDF-1.7") {
		t.Fatal("the document is served until it is cancelled", w.Code)
	}
	c.roles = []string{r.RoleIssuer} // the route is an issuer's: that role alone is enough
	w := c.cancel(c.audited, id, reasonBody(reason))
	var after map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &after) != nil || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Location") != "" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	c.roles = nil
	theirs, held := c.source.IssueRecord(id)
	if !held || theirs.State != contractsim.IssueConfirmed || !theirs.Cancelled() || theirs.CancelReason != reason || theirs.Number != "AR-000001" || c.product.asked() != 1 {
		t.Fatalf("the product holds %+v, asked %d times", theirs, c.product.asked())
	}
	want := map[string]any{"ref": theirs.CancellationRef, "cancelled_at": theirs.CancelledAt.UTC().Format(time.RFC3339), "reason": reason}
	if !reflect.DeepEqual(after["cancellation"], want) || !strings.HasSuffix(want["cancelled_at"].(string), "Z") {
		t.Fatalf("the cancellation %v, want %v", after["cancellation"], want)
	}
	// The state word stays the job's, and the record keeps every field it had.
	rest := map[string]any{}
	for k, v := range after {
		if k != "cancellation" {
			rest[k] = v
		}
	}
	if after["state"] != "succeeded" || !reflect.DeepEqual(rest, before) {
		t.Fatalf("the cancellation changed the record:\n%v\n%v", rest, before)
	}
	// The same in every view of the issue: the record's route, the listing, the job.
	if got := c.view(id); !reflect.DeepEqual(got, after) {
		t.Fatalf("GET /issues/{id}: %v", got)
	}
	list := request(c.api, "GET", apiBase+"/issues", nil, true)
	var listed []map[string]any
	if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], after) {
		t.Fatal(list.Code, list.Body.String())
	}
	job := request(c.api, "GET", apiBase+"/jobs/"+id, nil, true)
	var jobView map[string]any
	if job.Code != 200 || json.Unmarshal(job.Body.Bytes(), &jobView) != nil || jobView["state"] != "succeeded" || !reflect.DeepEqual(jobView["issue"].(map[string]any)["cancellation"], want) {
		t.Fatal(job.Code, job.Body.String())
	}
	// The file is no longer handed out, and the refusal says why; nothing of the file is in it.
	gone := c.download(artifact)
	refused(t, "the download of a cancelled document", gone, 409, "ISSUE_CANCELLED")
	if gone.Header().Get("Content-Disposition") != "" || gone.Header().Get("X-Content-SHA256") != "" {
		t.Fatal(gone.Header())
	}
	// Asked again, with another reason: the same answer, byte for byte, and the product is not asked.
	again := c.cancel(c.audited, id, reasonBody("asked again "+reasonMarker))
	if again.Code != 200 || again.Body.String() != w.Body.String() || c.product.asked() != 1 {
		t.Fatalf("the repeat: %d %s, the product asked %d times", again.Code, again.Body.String(), c.product.asked())
	}
	// The number is used for good: the next document gets the next one, and is not cancelled.
	if _, next := c.issued(1, "two"); next["number"] != "AR-000002" || next["cancellation"] != nil {
		t.Fatal(next)
	}
	// The audit: asked, done (the store's event, with the cancellation), asked again. The repeat cancelled nothing, so it adds no
	// second success; no event carries the reason or the number.
	if got, want := strings.Join(c.cancelEvents(), " "), "issue.cancel:requested:"+id+" issue.cancel:succeeded:"+id+" issue.cancel:requested:"+id; got != want {
		t.Fatalf("the audit:\n%s\nwant\n%s", got, want)
	}
	// Without the audit mounted the store's event is written all the same: it belongs to the cancellation, not to the mount.
	other, _ := c.issued(0, "three")
	if w := c.cancel(c.api, other, reasonBody(reason)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := c.cancelEvents(); len(got) != 4 || got[3] != "issue.cancel:succeeded:"+other {
		t.Fatal(got)
	}
}

// Who may not, what cannot be cancelled and what is not a request: each refusal is its code and nothing else, the product is asked only
// when the platform has nothing to refuse, and nothing is recorded.
func TestTheCancellationRouteRefusesWithoutTouchingTheDocument(t *testing.T) {
	c := newCancelHTTP(t, false)
	good := reasonBody("选错了客户 " + reasonMarker)
	id, view := c.issued(2, "one")
	artifact := view["artifact_id"].(string)
	untouched := func(what string, asked int) {
		t.Helper()
		if rec, _ := c.source.IssueRecord(id); rec.Cancelled() || c.product.asked() != asked {
			t.Fatalf("%s: the product holds %+v and was asked %d times, want %d", what, rec, c.product.asked(), asked)
		}
		if got := c.view(id); !reflect.DeepEqual(got, view) {
			t.Fatalf("%s: the issue is now %v", what, got)
		}
	}
	// The role: the route is an issuer's. Every other role together is refused before anything is read, and the refusal is recorded.
	c.roles = []string{r.RoleViewer, r.RoleDesigner, r.RolePublisher, r.RoleAdministrator}
	refused(t, "without the issuer role", c.cancel(c.audited, id, good), 403, "AUTH_ROLE_DENIED")
	c.roles = nil
	untouched("without the issuer role", 0)
	if got := strings.Join(c.cancelEvents(), " "); got != "role.refused:refused:" {
		t.Fatal(got)
	}
	// Anonymous.
	if w := request(c.audited, "POST", apiBase+"/issues/"+id+"/cancellation", []byte(good), false); w.Code != 401 || strings.Contains(w.Body.String(), "cancellation") {
		t.Fatal(w.Code, w.Body.String())
	}
	// Another identity's issue is not found, as every record of another identity is.
	c.subject = "other"
	refused(t, "another identity", c.cancel(c.audited, id, good), 404, "NOT_FOUND")
	c.subject = ""
	// What is no issued document of the caller's: an id nothing has, an id of another shape, a snapshot job, the artifact.
	snapshotJob, e := c.service.SubmitSnapshot(t.Context(), c.p, contractsim.FinancialQuery(), "another snapshot")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.service.RunOne(t.Context()); e != nil {
		t.Fatal(e)
	}
	for name, other := range map[string]string{"an id nothing has": strings.Repeat("0", 32), "an id of another shape": "nope", "a snapshot job": snapshotJob.ID, "the artifact's id": artifact, "the snapshot's id": c.snapshot} {
		refused(t, name, c.cancel(c.audited, other, good), 404, "NOT_FOUND")
	}
	// An issue that has not succeeded has no issued document: queued, cancelled before it ran, failed.
	waiting := c.queued(1, "waiting")
	refused(t, "a queued issue", c.cancel(c.audited, waiting, good), 409, "IDEMPOTENCY_CONFLICT")
	if w := request(c.api, "POST", apiBase+"/jobs/"+waiting+"/cancel", nil, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	refused(t, "an issue job that was cancelled before it ran", c.cancel(c.audited, waiting, good), 409, "IDEMPOTENCY_CONFLICT")
	failing := c.queued(1, "failing")
	c.drawn.refuse = func(map[string]string) error { return r.ErrUnsupported }
	if _, e = c.service.RunOne(t.Context()); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal(e)
	}
	c.drawn.refuse = nil
	refused(t, "a failed issue", c.cancel(c.audited, failing, good), 409, "IDEMPOTENCY_CONFLICT")
	untouched("after the refusals of other ids", 0)
	// The body is exactly {"reason": "…"}: strict JSON, a reason of 1..200 characters of text on one line.
	for name, body := range map[string]string{
		"no body":                    ``,
		"an empty object":            `{}`,
		"a null reason":              `{"reason":null}`,
		"an empty reason":            `{"reason":""}`,
		"a blank reason":             `{"reason":"   "}`,
		"a reason that is a number":  `{"reason":7}`,
		"a reason that is a list":    `{"reason":["` + reasonMarker + `"]}`,
		"a reason of two lines":      `{"reason":"first\nsecond ` + reasonMarker + `"}`,
		"a reason with a tab":        `{"reason":"a\tb ` + reasonMarker + `"}`,
		"a reason of 201 characters": reasonBody(strings.Repeat("因", 201)),
		"an unknown field":           `{"reason":"` + reasonMarker + `","force":true}`,
		"a field of another case":    `{"Reason":"` + reasonMarker + `"}`,
		"the reason twice":           `{"reason":"` + reasonMarker + `","reason":"` + reasonMarker + `"}`,
		"something after the object": `{"reason":"` + reasonMarker + `"} {}`,
		"a list":                     `["` + reasonMarker + `"]`,
		"not JSON":                   `reason=` + reasonMarker,
		"the product's own fields":   `{"reason":"` + reasonMarker + `","number":"AR-000001","ref":"x"}`,
	} {
		refused(t, name, c.cancel(c.audited, id, body), 400, "INVALID_REQUEST")
	}
	form := httptest.NewRequest("POST", apiBase+"/issues/"+id+"/cancellation", strings.NewReader(good))
	form.Header.Set("Content-Type", "text/plain")
	form.Header.Set("Authorization", "Bearer "+secret)
	plain := httptest.NewRecorder()
	c.audited.ServeHTTP(plain, form)
	refused(t, "another content type", plain, 400, "INVALID_REQUEST")
	refused(t, "a body over the small bound", c.cancel(c.audited, id, `{"reason":"`+reasonMarker+`","padding":"`+strings.Repeat("x", 4<<10)+`"}`), 413, "REQUEST_TOO_LARGE")
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if w := request(c.audited, method, apiBase+"/issues/"+id+"/cancellation", []byte(good), true); w.Code != 405 && w.Code != 404 {
			t.Fatalf("%s is answered %d", method, w.Code)
		}
	}
	untouched("after the malformed requests", 0)
	// The product decides: its refusal is the answer, and nothing is recorded. Its own rules, a grant without issuing, an outage.
	c.source.SetIssueRefused(true)
	refused(t, "the product's rules say no", c.cancel(c.audited, id, good), 403, "AUTH_SCOPE_DENIED")
	c.source.SetIssueRefused(false)
	untouched("after the product's refusal", 1)
	owner := contractsim.DemoOwner(c.p, contractsim.FinanceIntegration)
	reading := contractsim.DefaultGrant()
	reading.IssueDocuments = false
	c.source.Grant(owner, reading)
	refused(t, "a principal the product does not let issue", c.cancel(c.audited, id, good), 403, "AUTH_SCOPE_DENIED")
	c.source.Grant(owner, contractsim.DefaultGrant())
	c.source.SetUnavailable(true)
	refused(t, "a product that cannot be reached", c.cancel(c.audited, id, good), 503, "AUTHORITY_UNAVAILABLE")
	c.source.SetUnavailable(false)
	untouched("after the outage", 3)
	// After all of it the file is still handed out, and the document can still be cancelled: at the bound of 200 characters.
	if w := c.download(artifact); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := c.cancel(c.audited, id, reasonBody(strings.Repeat("因", 200))); w.Code != 200 || !strings.Contains(w.Body.String(), `"reason":"`+strings.Repeat("因", 200)+`"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// The audit of it all: a role refused; then every request the role allowed, each `requested` and then `refused` or `failed`
	// (a 503 is the platform's or the product's failure, not the person's refusal), and at the end the one that succeeded.
	events := c.cancelEvents()
	requested, refusals, failures, succeeded := 0, 0, 0, 0
	for _, ev := range events {
		switch {
		case strings.HasPrefix(ev, "issue.cancel:requested:"):
			requested++
		case strings.HasPrefix(ev, "issue.cancel:refused:"):
			refusals++
		case strings.HasPrefix(ev, "issue.cancel:failed:"):
			failures++
		case strings.HasPrefix(ev, "issue.cancel:succeeded:"):
			succeeded++
		}
	}
	if requested != refusals+failures+succeeded || failures != 1 || succeeded != 1 || events[len(events)-1] != "issue.cancel:succeeded:"+id || events[len(events)-2] != "issue.cancel:requested:"+id {
		t.Fatalf("requested %d, refused %d, failed %d, succeeded %d: %v", requested, refusals, failures, succeeded, events)
	}
}

// A product whose connection has no cancellation behaves as before S24-A: the capabilities say so, the route answers 422 without
// asking anything, the file is handed out, and the views carry no cancellation.
func TestTheCancellationRouteNeedsTheProductsCapability(t *testing.T) {
	c := newCancelHTTP(t, true)
	caps := request(c.api, "GET", apiBase+"/capabilities", nil, true)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"issue_cancellation":{"`+contractsim.FinanceIntegration+`":false}`) || !strings.Contains(caps.Body.String(), `"issuance":true`) {
		t.Fatal(caps.Body.String())
	}
	id, view := c.issued(2, "one")
	refused(t, "a product that cannot be asked", c.cancel(c.audited, id, reasonBody("a reason "+reasonMarker)), 422, "UNSUPPORTED_COMBINATION")
	if rec, _ := c.source.IssueRecord(id); rec.Cancelled() || c.product.asked() != 0 {
		t.Fatalf("the product was asked: %+v", rec)
	}
	if got := c.view(id); !reflect.DeepEqual(got, view) || got["cancellation"] != nil {
		t.Fatal(got)
	}
	if w := c.download(view["artifact_id"].(string)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := strings.Join(c.cancelEvents(), " "); got != "issue.cancel:requested:"+id+" issue.cancel:refused:"+id {
		t.Fatal(got)
	}
	// A principal that is served no product has an empty answer, not a missing one.
	demo, _ := httpapi.DemoBearer(secret, c.p)
	none, e := httpapi.Handler(c.service, func(req *http.Request) (r.Principal, error) {
		p, e := demo(req)
		p.Integrations = []string{"a-product-the-service-does-not-connect"}
		return p, e
	})
	if e != nil {
		t.Fatal(e)
	}
	if w := request(none, "GET", apiBase+"/capabilities", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"issue_cancellation":{}`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

// The audit event of a cancellation that cannot be written fails the request (ADR 0025 decision 2). When the audit is down before the
// product is asked, nothing happens anywhere. When the product has cancelled and the platform cannot write the event, the platform
// keeps no cancellation either (they are one step), goes on serving what it holds, and the person's next request completes the
// record with the product's same receipt.
func TestACancellationWhoseAuditEventCannotBeWrittenFailsAndIsCompletedByTheRetry(t *testing.T) {
	c := newCancelHTTP(t, false)
	id, view := c.issued(2, "one")
	artifact := view["artifact_id"].(string)
	first, second := "第一次的原因 "+reasonMarker, "第二次的原因 "+reasonMarker
	// The audit cannot record that the person asked: refused before the product is asked.
	broken := httpapi.WithAudit(c.api, failingLog{c.repo})
	refused(t, "the audit is down", c.cancel(broken, id, reasonBody(first)), 503, "AUDIT_UNAVAILABLE")
	if rec, _ := c.source.IssueRecord(id); rec.Cancelled() || c.product.asked() != 0 {
		t.Fatalf("the product was asked while the audit was down: %+v", rec)
	}
	// The request is recorded, the product cancels, and then the store cannot write the cancellation's own event.
	c.repo.SetAuditFailure(errors.New("the audit's table cannot be written"))
	refused(t, "the cancellation's event cannot be written", c.cancel(httpapi.WithAudit(c.api, deafLog{c.repo}), id, reasonBody(first)), 503, "AUDIT_UNAVAILABLE")
	c.repo.SetAuditFailure(nil)
	theirs, _ := c.source.IssueRecord(id)
	if !theirs.Cancelled() || theirs.CancelReason != first || c.product.asked() != 1 {
		t.Fatalf("the product holds %+v", theirs)
	}
	// The platform recorded nothing: the issue is as it was and its file is still what the platform hands out.
	if got := c.view(id); !reflect.DeepEqual(got, view) {
		t.Fatalf("a cancellation was kept without its event: %v", got)
	}
	if w := c.download(artifact); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := c.cancelEvents(); len(got) != 0 {
		t.Fatal(got)
	}
	// The retry: the product answers the receipt it gave, whatever the reason says now, and the record is completed. The platform's
	// row carries the reason of the request it recorded; the product keeps its first.
	w := c.cancel(c.audited, id, reasonBody(second))
	var after map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &after) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	want := map[string]any{"ref": theirs.CancellationRef, "cancelled_at": theirs.CancelledAt.UTC().Format(time.RFC3339), "reason": second}
	if again, _ := c.source.IssueRecord(id); !reflect.DeepEqual(after["cancellation"], want) || again.CancellationRef != theirs.CancellationRef || again.CancelReason != first || c.product.asked() != 2 {
		t.Fatalf("the retry answered %v, want %v; the product holds %+v", after["cancellation"], want, again)
	}
	refused(t, "the download after the retry", c.download(artifact), 409, "ISSUE_CANCELLED")
	if got, want := strings.Join(c.cancelEvents(), " "), "issue.cancel:requested:"+id+" issue.cancel:succeeded:"+id; got != want {
		t.Fatalf("the audit:\n%s\nwant\n%s", got, want)
	}
}
