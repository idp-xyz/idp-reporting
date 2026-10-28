package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/issuance"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stampingRenderer stands in for the Typst renderer behind the real issuer: a PDF carrying
// the stamp, two pages; it records what it was asked to draw.
type stampingRenderer struct {
	calls  int
	doc    doctemplates.Document
	values map[string]string
	stamp  string
	at     time.Time
	checks int
	// refuse is the layout's refusal of some values, which the real renderer makes in CheckIssue and
	// again in Issue.
	refuse func(values map[string]string) error
}

func (f *stampingRenderer) CheckIssue(_ context.Context, _ doctemplates.Document, values map[string]string, _ []byte, _ *doctemplates.TableData) error {
	f.checks++
	if f.refuse != nil {
		return f.refuse(values)
	}
	return nil
}

func (f *stampingRenderer) Issue(_ context.Context, d doctemplates.Document, values map[string]string, _ []byte, _ *doctemplates.TableData, stamp string, at time.Time) ([]byte, int, error) {
	f.calls++
	f.doc, f.values, f.stamp, f.at = d, values, stamp, at
	if f.refuse != nil {
		if e := f.refuse(values); e != nil {
			return nil, 0, e
		}
	}
	return []byte("%PDF-1.7\n" + stamp + "\n%%EOF"), 2, nil
}

// hiddenAuthority embeds only r.Adapter: whatever DocumentAuthority the wrapped adapter has is hidden.
type hiddenAuthority struct{ r.Adapter }

// issueHTTP is the HTTP surface of official issue over the in-memory store and the contract
// simulators: the finance product numbers its documents AR-000001, AR-000002, ...; one frozen
// snapshot; one saved template version 1 that is not published yet.
type issueHTTP struct {
	t        *testing.T
	p        r.Principal
	source   *contractsim.Simulator
	store    *doctemplates.Store
	drawn    *stampingRenderer
	repo     *memory.Store
	service  *r.Service
	fallback http.Handler
	h        http.Handler
	renderer httpapi.DocumentRenderer
	other    bool // another subject is authenticated while set
	snapshot string
	template string
}

func newIssueHTTP(t *testing.T, wrap func(r.Adapter) r.Adapter) *issueHTTP {
	t.Helper()
	v := &issueHTTP{t: t, p: contractsim.DemoPrincipal(), source: contractsim.Financial(), drawn: &stampingRenderer{}, repo: memory.New(time.Now)}
	v.source.Grant(contractsim.DemoOwner(v.p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	var adapter r.Adapter = v.source
	if wrap != nil {
		adapter = wrap(v.source)
	}
	var e error
	if v.store, e = doctemplates.Open(filepath.Join(t.TempDir(), "templates")); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { v.store.Close() })
	issuer, e := issuance.New(v.store, nil, v.drawn)
	if e != nil {
		t.Fatal(e)
	}
	if v.service, e = r.NewService(r.Config{Repository: v.repo, Connections: []r.Connection{{Tenant: v.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: adapter}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: issuer}); e != nil {
		t.Fatal(e)
	}
	demo, _ := httpapi.DemoBearer(secret, v.p)
	auth := func(req *http.Request) (r.Principal, error) {
		p, e := demo(req)
		if v.other {
			p.Subject = "other"
		}
		return p, e
	}
	v.fallback, _ = httpapi.Handler(v.service, auth)
	v.renderer = documentRenderer(func(context.Context, doctemplates.Document, map[string]string) ([]byte, error) {
		return []byte("%PDF-1.7 preview"), nil
	})
	if v.h, e = httpapi.WithDocumentTemplates(v.fallback, v.service, auth, v.store, v.renderer, nil); e != nil {
		t.Fatal(e)
	}
	ctx := t.Context()
	job, e := v.service.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if job, e = v.service.Job(ctx, v.p, job.ID); e != nil {
		t.Fatal(e)
	}
	v.snapshot = job.SnapshotID
	v1, e := v.store.Save(v.p.Identity, "", 0, savedTemplate("ar.open_balance"))
	if e != nil {
		t.Fatal(e)
	}
	v.template = v1.ID
	return v
}

// post asks for an official issue; a key of "" is the default one of the test requests.
func (v *issueHTTP) post(body map[string]any, key string) (int, map[string]any) {
	v.t.Helper()
	b, _ := json.Marshal(body)
	var q *httptest.ResponseRecorder
	if key == "" {
		q = request(v.h, "POST", "/api/reporting/v1/issues", b, true)
	} else {
		q = requestWithKey(v.h, "POST", "/api/reporting/v1/issues", b, key)
	}
	var out map[string]any
	_ = json.Unmarshal(q.Body.Bytes(), &out)
	return q.Code, out
}
func (v *issueHTTP) publish() {
	v.t.Helper()
	if _, e := v.store.Publish(v.p.Identity, v.template, "publish", 1, 0); e != nil {
		v.t.Fatal(e)
	}
}
func (v *issueHTTP) input(row int) map[string]any {
	return map[string]any{"template_id": v.template, "version": 1, "snapshot_id": v.snapshot, "row_index": row}
}

// absent fails the test when a JSON object has any of the keys.
func absent(t *testing.T, what string, object map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, has := object[k]; has {
			t.Errorf("%s carries %q before the job succeeded: %v", what, k, object)
		}
	}
}

func TestIssueRoutesFreezeThePublishedVersionAndListRecords(t *testing.T) {
	changing := &changingLayoutCatalog{}
	v := newIssueHTTP(t, func(a r.Adapter) r.Adapter { changing.Adapter = a; return changing })
	p, h, service, store, drawn, fallback, ctx := v.p, v.h, v.service, v.store, v.drawn, v.fallback, t.Context()
	post, input := v.post, v.input(2)
	// Capabilities announce issuance; a draft (unpublished) version is refused as such; a
	// missing version is not found; the caller cannot pass a configuration.
	caps := request(h, "GET", "/api/reporting/v1/capabilities", nil, true)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"issuance":true`) || !strings.Contains(caps.Body.String(), `"GET /issues/{id}"`) || !strings.Contains(caps.Body.String(), `"template_routes":["POST /issues"`) || strings.Contains(caps.Body.String(), `"GET /artifacts/{id}/content","POST /issues"`) {
		t.Fatal(caps.Body.String())
	}
	if code, out := post(input, ""); code != 409 || out["error"] != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatal("draft version issued", code, out)
	}
	if code, _ := post(map[string]any{"template_id": v.template, "version": 2, "snapshot_id": v.snapshot, "row_index": 2}, ""); code != 404 {
		t.Fatal("missing version", code)
	}
	v.publish()
	// A saved later version that is not the published one is refused like a draft.
	if _, e := store.Save(p.Identity, v.template, 1, savedTemplate("customer.id")); e != nil {
		t.Fatal(e)
	}
	if code, out := post(map[string]any{"template_id": v.template, "version": 2, "snapshot_id": v.snapshot, "row_index": 2}, ""); code != 409 || out["error"] != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatal("superseding draft issued", code, out)
	}
	withConfig := map[string]any{"template_id": v.template, "version": 1, "snapshot_id": v.snapshot, "row_index": 2, "config": savedTemplate("customer.id")}
	if code, _ := post(withConfig, ""); code != 400 {
		t.Fatal("caller configuration accepted", code)
	}
	if code, _ := post(map[string]any{"template_id": v.template, "version": 1, "snapshot_id": v.snapshot, "row_index": 3}, ""); code != 400 {
		t.Fatal("row beyond the snapshot", code)
	}
	if code, _ := post(map[string]any{"template_id": v.template, "version": 1, "snapshot_id": v.snapshot}, ""); code != 400 {
		t.Fatal("row missing", code)
	}
	// A field the catalog no longer allows is a denial at admission.
	changing.allowed = "customer.id"
	if code, _ := post(input, ""); code != 403 {
		t.Fatal("unauthorized field issued", code)
	}
	changing.allowed = ""
	code, out := post(input, "")
	if code != 202 || out["kind"] != "issue" || out["state"] != "queued" || out["format"] != "pdf" {
		t.Fatal(code, out)
	}
	// The job has no number: the product gives it when the worker prepares the document.
	issue := out["issue"].(map[string]any)
	if issue["template_id"] != v.template || issue["template_version"] != 1.0 || issue["row_index"] != 2.0 || len(issue["template_sha256"].(string)) != 64 {
		t.Fatal(issue)
	}
	absent(t, "the admitted job's issue summary", issue, "number")
	id := out["job_id"].(string)
	// The same key replays; another key is another job. Admission asked nothing of the product.
	if code, again := post(input, ""); code != 202 || again["job_id"] != id {
		t.Fatal("replay", code, again)
	}
	code, second := post(input, "another")
	if code != 202 || second["job_id"] == id {
		t.Fatal(code, second)
	}
	secondID := second["job_id"].(string)
	if _, held := v.source.IssueRecord(id); held {
		t.Fatal("admission reached the product")
	}
	// A queued issue job is cancelled.
	code, third := post(input, "third")
	if code != 202 {
		t.Fatal(code, third)
	}
	thirdID := third["job_id"].(string)
	w := request(h, "POST", "/api/reporting/v1/jobs/"+thirdID+"/cancel", nil, true)
	var cancelled r.JobView
	json.Unmarshal(w.Body.Bytes(), &cancelled)
	if w.Code != 200 || cancelled.State != "cancelled" || cancelled.Kind != "issue" || cancelled.Issue == nil || cancelled.Issue.Number != "" {
		t.Fatal(w.Code, w.Body.String())
	}
	// The worker issues the first; the frozen configuration is what was published.
	if _, e := service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if drawn.calls != 1 || drawn.doc.Elements[0].Field != "ar.open_balance" || drawn.values["n1"] != "9007199254740893.00" || !strings.HasPrefix(drawn.stamp, "ISSUED No. AR-000001 - ") || drawn.at.IsZero() {
		t.Fatalf("renderer: %+v", drawn)
	}
	// The job view, the record, the listing and the download.
	w = request(h, "GET", "/api/reporting/v1/jobs/"+id, nil, true)
	var view r.JobView
	json.Unmarshal(w.Body.Bytes(), &view)
	if w.Code != 200 || view.State != "succeeded" || view.ArtifactID == "" || view.Issue == nil || view.Issue.Number != "AR-000001" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", "/api/reporting/v1/issues/"+id, nil, true)
	var rec r.IssueView
	json.Unmarshal(w.Body.Bytes(), &rec)
	if w.Code != 200 || rec.ID != id || rec.Number != "AR-000001" || rec.ArtifactID != view.ArtifactID || rec.Pages != 2 || len(rec.SHA256) != 64 || rec.Stamp != "ISSUED No. AR-000001 - "+rec.IssuedAt+" - "+id || !strings.HasSuffix(rec.IssuedAt, "Z") {
		t.Fatal(w.Code, w.Body.String())
	}
	// The record carries the product's references and confirmation time, which are what the
	// product itself holds for the job; the times are RFC 3339 in UTC.
	theirs, held := v.source.IssueRecord(id)
	if !held || theirs.State != contractsim.IssueConfirmed || theirs.Number != "AR-000001" || rec.ReservationRef == "" || rec.ReservationRef != theirs.Ref || rec.ConfirmationRef == "" || rec.ConfirmationRef != theirs.ConfirmationRef || !strings.HasSuffix(rec.ConfirmedAt, "Z") || rec.ConfirmedAt != theirs.ConfirmedAt.UTC().Format(time.RFC3339) || rec.IssuedAt != theirs.IssuedAt.UTC().Format(time.RFC3339) || theirs.SHA256 != rec.SHA256 {
		t.Fatalf("%+v against the product's %+v", rec, theirs)
	}
	var raw map[string]any
	json.Unmarshal(w.Body.Bytes(), &raw)
	for _, k := range []string{"number", "reservation_ref", "confirmation_ref", "confirmed_at", "issued_at", "stamp", "sha256", "pages", "artifact_id"} {
		if _, has := raw[k]; !has {
			t.Errorf("the record of a succeeded job lacks %q: %v", k, raw)
		}
	}
	w = request(h, "GET", "/api/reporting/v1/issues?limit=10", nil, true)
	var list []r.IssueView
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list) != 3 || list[0].ID != thirdID || list[0].State != "cancelled" || list[1].ID != secondID || list[1].State != "queued" || list[1].SHA256 != "" || list[2].ID != id || list[2].SHA256 != rec.SHA256 || list[2].Number != "AR-000001" {
		t.Fatal(w.Code, w.Body.String())
	}
	// Before success an entry has no number and none of the record's fields.
	var rawList []map[string]any
	json.Unmarshal(w.Body.Bytes(), &rawList)
	for _, entry := range rawList[:2] {
		absent(t, "a listed "+entry["state"].(string)+" job", entry, "number", "reservation_ref", "confirmation_ref", "confirmed_at", "issued_at", "stamp", "sha256", "pages", "artifact_id", "snapshot_sha256")
	}
	w = request(h, "GET", "/api/reporting/v1/jobs/"+secondID, nil, true)
	var queued map[string]any
	json.Unmarshal(w.Body.Bytes(), &queued)
	if w.Code != 200 || queued["state"] != "queued" {
		t.Fatal(w.Code, w.Body.String())
	}
	absent(t, "a queued job's issue summary", queued["issue"].(map[string]any), "number")
	for _, path := range []string{"/api/reporting/v1/issues?limit=0", "/api/reporting/v1/issues?limit=101", "/api/reporting/v1/issues?limit=x", "/api/reporting/v1/issues?other=1"} {
		if request(h, "GET", path, nil, true).Code != 400 {
			t.Fatal(path)
		}
	}
	// The download keeps the name of the record's id; the console saves it under the number.
	w = request(h, "GET", "/api/reporting/v1/artifacts/"+view.ArtifactID+"/content", nil, true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || w.Header().Get("Content-Disposition") != `attachment; filename="issued-`+id+`.pdf"` || !strings.Contains(w.Body.String(), rec.Stamp) || w.Header().Get("X-Content-SHA256") != rec.SHA256 {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	// Another subject sees no record, no listing entry and no job.
	v.other = true
	if request(h, "GET", "/api/reporting/v1/issues/"+id, nil, true).Code != 404 {
		t.Fatal("record visible to another subject")
	}
	w = request(h, "GET", "/api/reporting/v1/issues", nil, true)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
	v.other = false
	// Withdrawn after admission: the queued second job fails with TEMPLATE_NOT_PUBLISHED, which the
	// issuer's check finds before the product is asked: nothing is reserved or voided at the product,
	// the job shows no number, and no number is spent.
	if _, e := store.Publish(p.Identity, v.template, "withdraw", 0, 1); e != nil {
		t.Fatal(e)
	}
	renders := drawn.calls
	if _, e := service.RunOne(ctx); e == nil {
		t.Fatal("withdrawn template issued")
	}
	if drawn.calls != renders {
		t.Fatal("a withdrawn template was rendered")
	}
	w = request(h, "GET", "/api/reporting/v1/issues/"+secondID, nil, true)
	var failed map[string]any
	json.Unmarshal(w.Body.Bytes(), &failed)
	if w.Code != 200 || failed["state"] != "failed" || failed["error_code"] != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatal(w.Body.String())
	}
	absent(t, "the failed job", failed, "number", "reservation_ref", "confirmation_ref", "confirmed_at")
	if rec, held := v.source.IssueRecord(secondID); held {
		t.Fatalf("the product holds %+v for a job refused before it was asked", rec)
	}
	// Published again, the next job takes the number the withdrawn one would have taken.
	if _, e := store.Publish(p.Identity, v.template, "publish", 1, 2); e != nil {
		t.Fatal(e)
	}
	code, next := post(input, "next")
	if code != 202 {
		t.Fatal(code, next)
	}
	if _, e := service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	w = request(h, "GET", "/api/reporting/v1/jobs/"+next["job_id"].(string), nil, true)
	var after r.JobView
	json.Unmarshal(w.Body.Bytes(), &after)
	if w.Code != 200 || after.State != "succeeded" || after.Issue == nil || after.Issue.Number != "AR-000002" {
		t.Fatal(w.Code, w.Body.String())
	}
	// Anonymous, without the templates mounted (422 in the problem shape, not the mux's 405) and
	// without issuance.
	b, _ := json.Marshal(input)
	if w := request(fallback, "POST", "/api/reporting/v1/issues", b, true); w.Code != 422 || !strings.Contains(w.Body.String(), "UNSUPPORTED_COMBINATION") {
		t.Fatal("without templates", w.Code, w.Body.String())
	}
	if request(h, "POST", "/api/reporting/v1/issues", b, false).Code != 401 {
		t.Fatal("anonymous")
	}
	plain, _ := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: changing}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	demo, _ := httpapi.DemoBearer(secret, p)
	fallback2, _ := httpapi.Handler(plain, demo)
	// The route is mounted with the templates whether or not a renderer is: 422, never 404.
	for _, rendererOrNil := range []httpapi.DocumentRenderer{v.renderer, nil} {
		h2, _ := httpapi.WithDocumentTemplates(fallback2, plain, demo, store, rendererOrNil, nil)
		if w := request(h2, "POST", "/api/reporting/v1/issues", b, true); w.Code != 422 {
			t.Fatal("issued without an issuer", w.Code, w.Body.String())
		}
		if w := request(h2, "GET", "/api/reporting/v1/capabilities", nil, true); !strings.Contains(w.Body.String(), `"issuance":false`) || strings.Contains(w.Body.String(), `"routes":[`+`"GET /catalog"`) == false {
			t.Fatal(w.Body.String())
		}
	}
}

// A value the renderer's layout refuses (too wide for its box, say) is found by the issuer's check
// before the product is asked for a number: the job fails with UNSUPPORTED_COMBINATION, nothing is
// rendered, nothing is reserved at the product and no number is spent.
func TestIssueRefusedByTheLayoutLeavesNothingReservedAtTheProduct(t *testing.T) {
	v := newIssueHTTP(t, nil)
	v.publish()
	v.drawn.refuse = func(values map[string]string) error {
		if values["n1"] == "9007199254740893.00" { // the amount of row 2, too wide for its box
			return fmt.Errorf("pdf: a value does not fit its box: %w", r.ErrUnsupported)
		}
		return nil
	}
	code, wide := v.post(v.input(2), "wide")
	if code != 202 {
		t.Fatal(code, wide)
	}
	ctx := t.Context()
	if _, e := v.service.RunOne(ctx); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal(e)
	}
	id := wide["job_id"].(string)
	w := request(v.h, "GET", "/api/reporting/v1/issues/"+id, nil, true)
	var failed map[string]any
	json.Unmarshal(w.Body.Bytes(), &failed)
	if w.Code != 200 || failed["state"] != "failed" || failed["error_code"] != "UNSUPPORTED_COMBINATION" {
		t.Fatal(w.Code, w.Body.String())
	}
	absent(t, "the refused job", failed, "number", "reservation_ref", "confirmation_ref", "confirmed_at")
	if v.drawn.checks != 1 || v.drawn.calls != 0 {
		t.Fatalf("checks %d, renders %d", v.drawn.checks, v.drawn.calls)
	}
	if rec, held := v.source.IssueRecord(id); held {
		t.Fatalf("the product holds %+v for a document its renderer cannot lay out", rec)
	}
	// A row that does fit is issued, and takes the first number.
	code, fits := v.post(v.input(0), "fits")
	if code != 202 {
		t.Fatal(code, fits)
	}
	if _, e := v.service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	w = request(v.h, "GET", "/api/reporting/v1/jobs/"+fits["job_id"].(string), nil, true)
	var done r.JobView
	json.Unmarshal(w.Body.Bytes(), &done)
	if w.Code != 200 || done.State != "succeeded" || done.Issue == nil || done.Issue.Number != "AR-000001" {
		t.Fatal(w.Code, w.Body.String())
	}
}

// The source product issues: an integration whose adapter does not implement the capability cannot,
// and POST /issues says so with 422 before it admits a job. Embedding only r.Adapter hides it.
func TestIssueRouteAnswers422ForAnAdapterThatCannotIssue(t *testing.T) {
	v := newIssueHTTP(t, func(a r.Adapter) r.Adapter { return hiddenAuthority{a} })
	v.publish()
	code, out := v.post(v.input(0), "")
	if code != 422 || out["error"] != "UNSUPPORTED_COMBINATION" {
		t.Fatal(code, out)
	}
	if w := request(v.h, "GET", "/api/reporting/v1/issues", nil, true); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("a job was admitted", w.Code, w.Body.String())
	}
	// The same adapter serves everything else: the snapshot it froze is readable and exports.
	if w := request(v.h, "GET", "/api/reporting/v1/snapshots/"+v.snapshot+"/rows?limit=1", nil, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// A running issue job cannot be cancelled: the product may already hold a reservation for it, so it
// finishes, or fails and voids. The cancel route returns it unchanged; a queued one is cancelled.
func TestCancelRouteReturnsARunningIssueJobUnchanged(t *testing.T) {
	v := newIssueHTTP(t, nil)
	v.publish()
	ctx := t.Context()
	code, out := v.post(v.input(1), "running")
	if code != 202 {
		t.Fatal(code, out)
	}
	id := out["job_id"].(string)
	claim, e := v.repo.Claim(ctx, r.LeaseDuration)
	if e != nil || claim == nil || claim.Job.ID != id {
		t.Fatal(claim, e)
	}
	w := request(v.h, "POST", "/api/reporting/v1/jobs/"+id+"/cancel", nil, true)
	var got map[string]any
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got["state"] != "running" || got["kind"] != "issue" || got["job_id"] != id {
		t.Fatal("a running issue job was cancelled or the route refused it", w.Code, w.Body.String())
	}
	absent(t, "the unchanged job's issue summary", got["issue"].(map[string]any), "number")
	w = request(v.h, "GET", "/api/reporting/v1/jobs/"+id, nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"running"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Its claim is intact: the worker that holds it publishes.
	stored, e := v.repo.Job(ctx, v.p.Identity, id)
	if e != nil || stored.State != "running" || stored.Generation != 1 || stored.LeaseUntil.IsZero() {
		t.Fatalf("%+v %v", stored, e)
	}
}

func requestWithKey(h http.Handler, method, path string, body []byte, key string) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, path, bytes.NewReader(body))
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Idempotency-Key", key)
	q.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, q)
	return w
}
