package reporting_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClock is the one clock of a test: the service, the store and the simulators follow it, so
// that a test can let a lease or a reservation run out without waiting.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

// newClock starts at a time in a zone other than UTC with a fraction of a second: whatever the
// product dates by it must come out as a UTC whole second.
func newClock() *testClock {
	return &testClock{t: time.Date(2026, 10, 28, 14, 12, 3, 700_000_000, time.FixedZone("CST", 8*3600))}
}
func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeIssuer renders from its request alone, as the real issuer does: the same request gives
// the same bytes. Like the renderer behind the real one it takes only a UTC whole second. Check
// fails with checkErr and Issue with err, so that a test can tell a refusal before the product is
// asked from one after.
type fakeIssuer struct {
	calls     int
	last      r.IssueRequest
	err       error
	pages     int
	content   []byte // replaces the rendering
	hook      func() // runs inside the call: time passes or the world changes while the document renders
	checks    int
	checkedAs r.Job      // the job and the snapshot of the last Check
	checkedOn r.Snapshot //
	checkErr  error
	checkHook func() // runs inside Check
}

func (f *fakeIssuer) Check(_ context.Context, j r.Job, s r.Snapshot) error {
	f.checks++
	f.checkedAs, f.checkedOn = j, s
	if f.checkHook != nil {
		f.checkHook()
	}
	return f.checkErr
}

func (f *fakeIssuer) Issue(_ context.Context, req r.IssueRequest) (r.IssueResult, error) {
	f.calls++
	f.last = req
	if f.hook != nil {
		f.hook()
	}
	if f.err != nil {
		return r.IssueResult{}, f.err
	}
	if req.At.Location() != time.UTC || req.At.Nanosecond() != 0 {
		return r.IssueResult{}, r.ErrInvalid
	}
	content := f.content
	if content == nil {
		content = []byte("%PDF-1.7\n" + req.Stamp + "\n" + req.At.Format(time.RFC3339))
	}
	pages := f.pages
	if pages == 0 {
		pages = 2
	}
	return r.IssueResult{PDF: content, Pages: pages}, nil
}

type voidCall struct{ JobID, Reason string }

// voidContext is what the product saw of the context of a void.
type voidContext struct {
	err         error
	hasDeadline bool
	remaining   time.Duration
}

// product is the source product of a test: a contract simulator, whose DocumentAuthority it
// keeps by embedding it, with the calls it received and the means to change or break its answers.
type product struct {
	*contractsim.Simulator
	intents   []r.IssueIntent
	prepared  []r.IssueReservation
	confirmed []r.IssueConfirmation
	receipts  []r.IssueReceipt
	voids     []voidCall
	voidCtx   voidContext
	prepareAs func(*r.IssueReservation) // changes the reservation the product answers with
	confirmAs func(*r.IssueReceipt)     // changes the receipt
	prepareE  error                     // the answer to PrepareDocument is lost after the product reserved
	confirmE  error                     // ConfirmIssue is refused before it reaches the simulator
	voidE     error                     // VoidIssue never reaches the simulator
}

func (p *product) PrepareDocument(ctx context.Context, sc r.SourceContext, in r.IssueIntent) (r.IssueReservation, error) {
	p.intents = append(p.intents, in)
	res, e := p.Simulator.PrepareDocument(ctx, sc, in)
	if e != nil {
		return res, e
	}
	p.prepared = append(p.prepared, res)
	if p.prepareE != nil {
		return r.IssueReservation{}, p.prepareE
	}
	if p.prepareAs != nil {
		p.prepareAs(&res)
	}
	return res, nil
}
func (p *product) ConfirmIssue(ctx context.Context, sc r.SourceContext, c r.IssueConfirmation) (r.IssueReceipt, error) {
	p.confirmed = append(p.confirmed, c)
	if p.confirmE != nil {
		return r.IssueReceipt{}, p.confirmE
	}
	rc, e := p.Simulator.ConfirmIssue(ctx, sc, c)
	if e != nil {
		return rc, e
	}
	p.receipts = append(p.receipts, rc)
	if p.confirmAs != nil {
		p.confirmAs(&rc)
	}
	return rc, nil
}
func (p *product) VoidIssue(ctx context.Context, sc r.SourceContext, jobID, reason string) error {
	p.voids = append(p.voids, voidCall{jobID, reason})
	deadline, ok := ctx.Deadline()
	p.voidCtx = voidContext{err: ctx.Err(), hasDeadline: ok, remaining: time.Until(deadline)}
	if p.voidE != nil {
		return p.voidE
	}
	return p.Simulator.VoidIssue(ctx, sc, jobID, reason)
}

// sameReservation and sameReceipt compare what the product answered by value: a time read twice
// is the same time, whatever its representation.
func sameReservation(a, b r.IssueReservation) bool {
	return a.Ref == b.Ref && a.JobID == b.JobID && a.Number == b.Number && a.IssuedAt.Equal(b.IssuedAt) && a.ExpiresAt.Equal(b.ExpiresAt)
}
func sameReceipt(a, b r.IssueReceipt) bool {
	return a.Ref == b.Ref && a.ConfirmedAt.Equal(b.ConfirmedAt)
}

// hidden is an adapter that forwards the four source calls and nothing else: a wrapper that
// embeds only r.Adapter hides the DocumentAuthority of what it wraps.
type hidden struct{ r.Adapter }

// loseLease wraps the repository of a test. Its first publications lose the lease while the job
// stays running: the clock passes the lease as the store would see it, and the publication is
// answered with ErrLease. A lying publication commits and still reports an error, as a commit
// whose acknowledgement was lost does. A refusing one commits nothing and reports its error.
type loseLease struct {
	r.Repository
	clock  *testClock
	lose   int
	lie    int
	refuse error
}

func (l *loseLease) Publish(ctx context.Context, c r.Claim, p r.Publication) error {
	if l.lose > 0 {
		l.lose--
		l.clock.Advance(r.LeaseDuration + time.Second)
		return r.ErrLease
	}
	if l.refuse != nil {
		return l.refuse
	}
	if l.lie > 0 {
		l.lie--
		if e := l.Repository.Publish(ctx, c, p); e != nil {
			return e
		}
		return errors.New("the acknowledgement of the commit was lost")
	}
	return l.Repository.Publish(ctx, c, p)
}

const template = `{"schema_version":"1.0","kind":"document","binding":{"integration_id":"i","dataset":{"id":"d","version":"1"}},"elements":[]}`

var templateID = strings.Repeat("a", 32)

type envOpts struct {
	issuer        *fakeIssuer
	hideAuthority bool // the finance adapter is wrapped so that it hides DocumentAuthority
}

// issueEnv is a service with two integrations (finance numbers AR-, shipment SH-), a fake issuer,
// a store and a clock the simulators share, and the finance snapshot every issue is made from.
type issueEnv struct {
	t        *testing.T
	s        *r.Service
	p        r.Principal
	snapshot string
	repo     *memory.Store
	store    *loseLease // the repository the service writes through, around repo
	clock    *testClock
	finance  *product
	shipment *product
	issuer   *fakeIssuer
	owner    r.Owner
}

func newIssueEnv(t *testing.T, o envOpts) *issueEnv {
	t.Helper()
	v := &issueEnv{t: t, clock: newClock(), issuer: o.issuer}
	if v.issuer == nil {
		v.issuer = &fakeIssuer{}
	}
	v.p = contractsim.DemoPrincipal()
	v.p.Integrations = append(v.p.Integrations, contractsim.ShipmentIntegration)
	v.owner = contractsim.DemoOwner(v.p, contractsim.FinanceIntegration)
	v.finance = &product{Simulator: contractsim.Financial()}
	v.finance.Grant(v.owner, contractsim.DefaultGrant())
	v.finance.SetClock(v.clock.Now)
	v.shipment = &product{Simulator: contractsim.Shipment()}
	grant := contractsim.DefaultGrant()
	grant.CurrencyPolicy = nil
	v.shipment.Grant(contractsim.DemoOwner(v.p, contractsim.ShipmentIntegration), grant)
	v.shipment.SetClock(v.clock.Now)
	v.repo = memory.New(v.clock.Now)
	v.store = &loseLease{Repository: v.repo, clock: v.clock}
	var finance r.Adapter = v.finance
	if o.hideAuthority {
		finance = hidden{v.finance}
	}
	s, e := r.NewService(r.Config{Repository: v.store, Connections: []r.Connection{{Tenant: v.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}, {Tenant: v.p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: v.shipment}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: v.issuer, Now: v.clock.Now})
	if e != nil {
		t.Fatal(e)
	}
	v.s = s
	v.snapshot = v.freeze(contractsim.FinancialQuery(), "snap")
	return v
}

// freeze runs a snapshot job and returns the snapshot's id.
func (v *issueEnv) freeze(q r.QuerySpec, key string) string {
	v.t.Helper()
	ctx := context.Background()
	j, e := v.s.SubmitSnapshot(ctx, v.p, q, key)
	if e != nil {
		v.t.Fatal(e)
	}
	if _, e = v.s.RunOne(ctx); e != nil {
		v.t.Fatal(e)
	}
	if j, e = v.s.Job(ctx, v.p, j.ID); e != nil || j.SnapshotID == "" {
		v.t.Fatal(j, e)
	}
	return j.SnapshotID
}

func (v *issueEnv) input(row int) r.IssueInput {
	return r.IssueInput{TemplateID: templateID, TemplateVersion: 3, Template: []byte(template), SnapshotID: v.snapshot, RowIndex: row}
}

// submit admits an issue job of the finance snapshot's row.
func (v *issueEnv) submit(row int, key string) r.JobView {
	v.t.Helper()
	j, e := v.s.SubmitIssue(context.Background(), v.p, v.input(row), key)
	if e != nil {
		v.t.Fatal(e)
	}
	return j
}

// job reads a job back through the service; stored reads it as the store keeps it.
func (v *issueEnv) job(id string) r.JobView {
	v.t.Helper()
	j, e := v.s.Job(context.Background(), v.p, id)
	if e != nil {
		v.t.Fatal(e)
	}
	return j
}
func (v *issueEnv) stored(id string) r.Job {
	v.t.Helper()
	j, e := v.repo.Job(context.Background(), v.p.Identity, id)
	if e != nil {
		v.t.Fatal(e)
	}
	return j
}

// run runs the worker once and returns what it returned.
func (v *issueEnv) run() (bool, error) { v.t.Helper(); return v.s.RunOne(context.Background()) }

// held is what the finance product holds for a job. A void is final for the job id: a product
// may record a void even for a job it held nothing for, so where nothing may be held a test accepts
// both no record and a void one.
func held(v *issueEnv, id string) (contractsim.IssueRecord, bool) { return v.finance.IssueRecord(id) }

func assertNothingHeld(t *testing.T, v *issueEnv, id string) {
	t.Helper()
	if rec, ok := held(v, id); ok && rec.State != contractsim.IssueVoid {
		t.Fatalf("the product holds %s for a job that was refused: %+v", rec.State, rec)
	}
}

// assertVoided: the product's record of the job is void, with the code as the reason.
func assertVoided(t *testing.T, v *issueEnv, id, reason string) contractsim.IssueRecord {
	t.Helper()
	rec, ok := held(v, id)
	if !ok || rec.State != contractsim.IssueVoid || rec.Reason != reason {
		t.Fatalf("the product's record of the job: %+v (held %v); want void with the reason %q", rec, ok, reason)
	}
	return rec
}

func TestIssueJobsNumberRenderStampAndRecord(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	reads := v.finance.ReadCalls()
	in := v.input(1)
	j := v.submit(1, "first")
	// Admission asks nothing of the product and reads no source data; the job has no number, the
	// product gives it when the worker prepares the document.
	if j.Kind != "issue" || j.State != "queued" || j.Format != "pdf" || j.Issue == nil || j.Issue.Number != "" || j.Issue.TemplateID != templateID || j.Issue.TemplateVersion != 3 || j.Issue.RowIndex != 1 || len(j.Issue.TemplateSHA256) != 64 {
		t.Fatalf("admitted issue job: %+v %+v", j, j.Issue)
	}
	if len(v.finance.intents) != 0 || v.finance.ReadCalls() != reads || v.issuer.calls != 0 {
		t.Fatal("admission reached the product, read source data or rendered")
	}
	// A replay with the same key returns the same job; another input is a conflict; the next
	// key is another job.
	if again, e := v.s.SubmitIssue(ctx, v.p, in, "first"); e != nil || again.ID != j.ID || again.Issue.Number != "" {
		t.Fatal(again, e)
	}
	other := v.input(2)
	if _, e := v.s.SubmitIssue(ctx, v.p, other, "first"); !errors.Is(e, r.ErrConflict) {
		t.Fatal("replayed key with another row admitted", e)
	}
	second, e := v.s.SubmitIssue(ctx, v.p, other, "second")
	if e != nil || second.ID == j.ID {
		t.Fatal(second, e)
	}
	// The worker asks the product to prepare the document, renders with the stamp of the number
	// and the issue time the product gave (its UTC second, not the clock's zone and fraction),
	// has the product confirm the file and publishes the artifact and the record together.
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	issuedAt := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	stamp := "ISSUED No. AR-000001 - 2026-10-28T06:12:03Z - " + j.ID
	if v.issuer.calls != 1 || v.issuer.last.Job.ID != j.ID || v.issuer.last.Snapshot.ID != v.snapshot || v.issuer.last.Job.Template != template || v.issuer.last.Stamp != stamp || !v.issuer.last.At.Equal(issuedAt) || v.issuer.last.At.Location() != time.UTC {
		t.Fatalf("issuer call: %+v", v.issuer.last)
	}
	rows, e := v.s.Rows(ctx, v.p, v.snapshot, 100, "")
	if e != nil {
		t.Fatal(e)
	}
	stored := v.stored(j.ID)
	if len(v.finance.intents) != 1 {
		t.Fatalf("%d calls to prepare", len(v.finance.intents))
	}
	intent := v.finance.intents[0]
	if intent.JobID != j.ID || intent.RowIndex != 1 || intent.RowSHA256 != r.Digest(rows.Rows[1]) || intent.TemplateID != templateID || intent.TemplateVersion != 3 || intent.TemplateSHA256 != stored.TemplateSHA256 || intent.Prepared.Ref != stored.Prepared.Ref || intent.Prepared.Context != stored.Prepared.Context {
		t.Fatalf("intent: %+v", intent)
	}
	done := v.job(j.ID)
	if done.State != "succeeded" || done.ArtifactID == "" || done.Issue == nil || done.Issue.Number != "AR-000001" {
		t.Fatalf("job view: %+v %+v", done, done.Issue)
	}
	a, e := v.s.Download(ctx, v.p, done.ArtifactID)
	if e != nil || a.Format != "pdf" || a.Issue != j.ID || !bytes.Equal(a.Content, []byte("%PDF-1.7\n"+stamp+"\n2026-10-28T06:12:03Z")) {
		t.Fatal(a.Format, a.Issue, e)
	}
	// The record is the product's: its number, issue time and the references of its reservation
	// and confirmation, which are what the product itself holds under the job id.
	rec, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
	theirs, ok := held(v, j.ID)
	if e != nil || !ok || theirs.State != contractsim.IssueConfirmed || theirs.Number != "AR-000001" || theirs.SHA256 != a.SHA256 || theirs.Pages != 2 {
		t.Fatalf("the product's record: %+v %v %v", theirs, ok, e)
	}
	if rec.Number != "AR-000001" || rec.ArtifactID != a.ID || rec.SHA256 != a.SHA256 || rec.Pages != 2 || rec.Stamp != stamp || !rec.IssuedAt.Equal(issuedAt) || rec.IssuedAt.Location() != time.UTC ||
		rec.ReservationRef != theirs.Ref || rec.ConfirmationRef != theirs.ConfirmationRef || !rec.ConfirmedAt.Equal(theirs.ConfirmedAt) || rec.ConfirmedAt.Location() != time.UTC || rec.TemplateSHA256 != stored.TemplateSHA256 || rec.RowIndex != 1 || len(rec.SnapshotSHA256) != 64 || rec.ID != j.ID || rec.Owner != stored.Owner {
		t.Fatalf("record: %+v", rec)
	}
	view, e := v.s.Issue(ctx, v.p, j.ID)
	if e != nil || view.ID != j.ID || view.State != "succeeded" || view.Number != "AR-000001" || view.SHA256 != a.SHA256 || view.Pages != 2 || view.Stamp != stamp || view.IssuedAt != "2026-10-28T06:12:03Z" || view.ArtifactID != a.ID ||
		view.ReservationRef != theirs.Ref || view.ConfirmationRef != theirs.ConfirmationRef || view.ConfirmedAt != "2026-10-28T06:12:03Z" {
		t.Fatalf("issue view: %+v %v", view, e)
	}
	// The second job is refused at rendering: the product voided what it had reserved, the job
	// has no number to show, and the number stays voided: the next document gets the next one.
	v.issuer.err = r.ErrUnpublished
	if _, e = v.run(); !errors.Is(e, r.ErrUnpublished) {
		t.Fatal("unpublished template did not fail the job", e)
	}
	voided := assertVoided(t, v, second.ID, "TEMPLATE_NOT_PUBLISHED")
	if voided.Number != "AR-000002" {
		t.Fatalf("the voided reservation: %+v", voided)
	}
	if _, e = v.repo.Issue(ctx, v.p.Identity, second.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("a record of a failed job", e)
	}
	v.issuer.err = nil
	third := v.submit(1, "third")
	if _, e = v.run(); e != nil {
		t.Fatal(e)
	}
	if got := v.job(third.ID); got.State != "succeeded" || got.Issue.Number != "AR-000003" {
		t.Fatalf("%+v %+v", got, got.Issue)
	}
	list, e := v.s.Issues(ctx, v.p, 10)
	if e != nil || len(list) != 3 || list[0].ID != third.ID || list[0].Number != "AR-000003" || list[0].State != "succeeded" || list[1].ID != second.ID || list[1].State != "failed" || list[1].ErrorCode != "TEMPLATE_NOT_PUBLISHED" || list[1].Number != "" || list[1].SHA256 != "" || list[1].ConfirmationRef != "" || list[2].ID != j.ID || list[2].Number != "AR-000001" || list[2].SHA256 != a.SHA256 {
		t.Fatalf("listing: %+v %v", list, e)
	}
	if _, e = v.s.Issues(ctx, v.p, 0); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("limit 0 accepted")
	}
	if _, e = v.s.Issues(ctx, v.p, r.MaxIssueListing+1); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("limit above the maximum accepted")
	}
	// The listing is filtered by the integrations the principal holds, in the repository, so a
	// page is never cut short by jobs the caller may not see; a principal without integrations
	// gets an empty page, not an error.
	if list, e := v.repo.Issues(ctx, v.p.Identity, []string{"other"}, 10); e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
	if _, e := v.repo.Issues(ctx, v.p.Identity, nil, 10); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("a listing without integrations", e)
	}
	none := v.p
	none.Integrations = nil
	if list, e := v.s.Issues(ctx, none, 10); e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
	// Another integration is another product with its own numbers: SH-000001 beside AR-000003;
	// an export job is not an issue.
	ship := v.freeze(contractsim.ShipmentQuery(), "ship")
	shipped, e := v.s.SubmitIssue(ctx, v.p, r.IssueInput{TemplateID: templateID, TemplateVersion: 1, Template: []byte(template), SnapshotID: ship}, "ship-issue")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.run(); e != nil {
		t.Fatal(e)
	}
	if got := v.job(shipped.ID); got.State != "succeeded" || got.Issue.Number != "SH-000001" {
		t.Fatalf("%+v %+v", got, got.Issue)
	}
	if rec, ok := v.shipment.IssueRecord(shipped.ID); !ok || rec.State != contractsim.IssueConfirmed || rec.Number != "SH-000001" {
		t.Fatalf("the shipment product's record: %+v %v", rec, ok)
	}
	if len(v.finance.prepared) != 3 || len(v.shipment.prepared) != 1 {
		t.Fatal("a product was asked about another product's document")
	}
	finance := v.p
	finance.Integrations = []string{contractsim.FinanceIntegration, "an-integration-without-a-connection"}
	if list, e := v.s.Issues(ctx, finance, 1); e != nil || len(list) != 1 || list[0].ID != third.ID {
		t.Fatalf("a page of one for finance: %+v %v", list, e)
	}
	ex, e := v.s.SubmitExportFormat(ctx, v.p, v.snapshot, "pdf", "export")
	if e != nil || ex.Issue != nil {
		t.Fatal(ex, e)
	}
	if _, e = v.s.Issue(ctx, v.p, ex.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("an export job read as an issue", e)
	}
	// Another identity sees nothing of it.
	stranger := v.p
	stranger.Subject = "stranger"
	if _, e = v.s.Issue(ctx, stranger, j.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("record visible to another subject", e)
	}
	if list, e = v.s.Issues(ctx, stranger, 10); e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
}

// What the product returns may carry any location (a time read back from its database), judged by
// value: the worker takes the second, renders with the UTC time the renderer requires and keeps
// UTC in the record.
func TestIssueTimesFromTheProductAreJudgedByValueAndKeptInUTC(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	zone := time.FixedZone("elsewhere", -(5*3600 + 30*60))
	v.finance.prepareAs = func(res *r.IssueReservation) {
		res.IssuedAt, res.ExpiresAt = res.IssuedAt.In(zone), res.ExpiresAt.In(zone)
	}
	v.finance.confirmAs = func(rc *r.IssueReceipt) { rc.ConfirmedAt = rc.ConfirmedAt.In(zone).Add(3 * time.Second) }
	j := v.submit(0, "k")
	if _, e := v.run(); e != nil {
		t.Fatal(e)
	}
	issuedAt := time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC)
	if !v.issuer.last.At.Equal(issuedAt) || v.issuer.last.At.Location() != time.UTC || v.issuer.last.Stamp != "ISSUED No. AR-000001 - 2026-10-28T06:12:03Z - "+j.ID {
		t.Fatalf("the issuer was asked for %v in %v with the stamp %q", v.issuer.last.At, v.issuer.last.At.Location(), v.issuer.last.Stamp)
	}
	rec, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
	if e != nil || rec.IssuedAt.Location() != time.UTC || rec.ConfirmedAt.Location() != time.UTC || !rec.IssuedAt.Equal(issuedAt) || !rec.ConfirmedAt.Equal(issuedAt.Add(3*time.Second)) {
		t.Fatalf("%+v %v", rec, e)
	}
	if view, e := v.s.Issue(ctx, v.p, j.ID); e != nil || view.IssuedAt != "2026-10-28T06:12:03Z" || view.ConfirmedAt != "2026-10-28T06:12:06Z" {
		t.Fatalf("%+v %v", view, e)
	}
}

func TestIssueAdmissionFailsClosed(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	good := r.IssueInput{TemplateID: templateID, TemplateVersion: 1, Template: []byte(template), SnapshotID: v.snapshot, RowIndex: 0}
	// One idempotency key per case, so that a refusal that reached the store would surface as
	// a conflict and not as the expected error.
	for name, c := range map[string]struct {
		change func(*r.IssueInput)
		key    string
		want   error
	}{
		"row beyond the snapshot":  {func(in *r.IssueInput) { in.RowIndex = 3 }, "k1", r.ErrInvalid},
		"negative row":             {func(in *r.IssueInput) { in.RowIndex = -1 }, "k2", r.ErrInvalid},
		"template id":              {func(in *r.IssueInput) { in.TemplateID = "x" }, "k3", r.ErrInvalid},
		"uppercase template id":    {func(in *r.IssueInput) { in.TemplateID = strings.Repeat("A", 32) }, "k3b", r.ErrInvalid},
		"version 0":                {func(in *r.IssueInput) { in.TemplateVersion = 0 }, "k4", r.ErrInvalid},
		"template not JSON":        {func(in *r.IssueInput) { in.Template = []byte("{") }, "k5", r.ErrInvalid},
		"template too large":       {func(in *r.IssueInput) { in.Template = []byte(`"` + strings.Repeat("x", r.MaxTemplateBytes) + `"`) }, "k6", r.ErrInvalid},
		"unknown snapshot":         {func(in *r.IssueInput) { in.SnapshotID = strings.Repeat("b", 32) }, "k7", r.ErrNotFound},
		"bad key":                  {func(*r.IssueInput) {}, "", r.ErrInvalid},
		"unexpected nothing wrong": {func(*r.IssueInput) {}, "k9", nil},
	} {
		in := good
		c.change(&in)
		_, e := v.s.SubmitIssue(ctx, v.p, in, c.key)
		if !errors.Is(e, c.want) {
			t.Errorf("%s: %v; want %v", name, e, c.want)
		}
	}
	// Without an issuer or without the pdf format, issuing is unsupported.
	none, p2, snap2, _ := issueServiceWithout(t)
	if _, e := none.SubmitIssue(ctx, p2, r.IssueInput{TemplateID: good.TemplateID, TemplateVersion: 1, Template: []byte(template), SnapshotID: snap2}, "k"); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("issue admitted without an issuer", e)
	}
	// The worker refuses a bad render (no PDF prefix, too many pages, an empty body) before it
	// asks the product to confirm anything: the product voids what it had reserved.
	for name, f := range map[string]*fakeIssuer{"not a pdf": {content: []byte("hello")}, "too many pages": {pages: r.MaxIssuePages + 1}, "empty": {content: []byte{}, pages: 1}} {
		w := newIssueEnv(t, envOpts{issuer: f})
		j := w.submit(0, "k")
		if _, e := w.run(); e == nil {
			t.Errorf("%s: published", name)
		}
		if got := w.job(j.ID); got.State != "failed" {
			t.Errorf("%s: %+v", name, got)
		}
		if _, e := w.repo.Issue(ctx, w.p.Identity, j.ID); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("%s: a record exists", name)
		}
		if len(w.finance.confirmed) != 0 {
			t.Errorf("%s: the product was asked to confirm a file that was refused", name)
		}
		if rec, ok := held(w, j.ID); !ok || rec.State != contractsim.IssueVoid {
			t.Errorf("%s: the product's record: %+v %v", name, rec, ok)
		}
	}
}

func issueServiceWithout(t *testing.T) (*r.Service, r.Principal, string, *memory.Store) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(nil)
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	j, e := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if j, e = s.Job(ctx, p, j.ID); e != nil {
		t.Fatal(e)
	}
	return s, p, j.SnapshotID, repo
}

// An integration whose adapter does not implement the capability cannot issue: admission says so
// before it admits anything, and a stored job (made when the adapter could) fails as unsupported
// without calling anything at the product.
func TestIssueNeedsAnAdapterThatCanIssue(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{hideAuthority: true})
	if _, e := v.s.SubmitIssue(ctx, v.p, v.input(0), "k"); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("an issue was admitted for an adapter that cannot issue", e)
	}
	if jobs, e := v.repo.Issues(ctx, v.p.Identity, []string{contractsim.FinanceIntegration}, 10); e != nil || len(jobs) != 0 {
		t.Fatal("a job was admitted", jobs, e)
	}
	// The shipment integration's adapter can: the refusal is the finance integration's own.
	ship := v.freeze(contractsim.ShipmentQuery(), "ship")
	if _, e := v.s.SubmitIssue(ctx, v.p, r.IssueInput{TemplateID: templateID, TemplateVersion: 1, Template: []byte(template), SnapshotID: ship}, "k"); e != nil {
		t.Fatal(e)
	}
	// A job stored while the adapter could issue, run by a service whose adapter cannot.
	w := newIssueEnv(t, envOpts{})
	j := w.submit(0, "k")
	blind, e := r.NewService(r.Config{Repository: w.repo, Connections: []r.Connection{{Tenant: w.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: hidden{w.finance}}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: w.issuer, Now: w.clock.Now})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = blind.RunOne(ctx); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("a job ran against an adapter that cannot issue", e)
	}
	if st := w.stored(j.ID); st.State != "failed" || st.ErrorCode != "UNSUPPORTED_COMBINATION" {
		t.Fatalf("%+v", st)
	}
	if len(w.finance.intents) != 0 || len(w.finance.voids) != 0 || w.issuer.calls != 0 {
		t.Fatal("the product was called for a job it cannot issue")
	}
}

// The product refuses: its approval rules say no, or the principal's grant lacks IssueDocuments.
// The job fails as AUTH_SCOPE_DENIED, nothing stays reserved and no number is spent.
func TestIssueRefusedByTheProductLeavesNothingReserved(t *testing.T) {
	v := newIssueEnv(t, envOpts{})
	// The approval switch.
	v.finance.SetIssueRefused(true)
	refused := v.submit(0, "refused")
	if _, e := v.run(); !errors.Is(e, r.ErrDenied) {
		t.Fatal("the product's refusal did not fail the job", e)
	}
	if st := v.stored(refused.ID); st.State != "failed" || st.ErrorCode != "AUTH_SCOPE_DENIED" {
		t.Fatalf("%+v", st)
	}
	assertNothingHeld(t, v, refused.ID)
	if v.issuer.calls != 0 || len(v.finance.confirmed) != 0 || len(v.finance.prepared) != 0 {
		t.Fatal("a refused document was rendered or confirmed")
	}
	v.finance.SetIssueRefused(false)
	// A grant without IssueDocuments.
	g := contractsim.DefaultGrant()
	g.IssueDocuments = false
	v.finance.Grant(v.owner, g)
	noGrant := v.submit(1, "no-grant")
	if _, e := v.run(); !errors.Is(e, r.ErrDenied) {
		t.Fatal("a grant without IssueDocuments issued", e)
	}
	if st := v.stored(noGrant.ID); st.State != "failed" || st.ErrorCode != "AUTH_SCOPE_DENIED" {
		t.Fatalf("%+v", st)
	}
	assertNothingHeld(t, v, noGrant.ID)
	v.finance.Grant(v.owner, contractsim.DefaultGrant())
	// Neither refusal used a number.
	ok := v.submit(2, "ok")
	if _, e := v.run(); e != nil {
		t.Fatal(e)
	}
	if got := v.job(ok.ID); got.State != "succeeded" || got.Issue.Number != "AR-000001" {
		t.Fatalf("%+v %+v", got, got.Issue)
	}
}

// The issuer checks what it can without the product and without rendering before the worker asks
// the product for a number (S03-H): a product must account for every number it voids, so a withdrawn
// template or a document the layout refuses must not cost it a reservation. The check gets the job
// and its verified snapshot, and runs after the source and the row were checked and before the
// product is called.
func TestIssueIsCheckedBeforeTheProductIsAsked(t *testing.T) {
	v := newIssueEnv(t, envOpts{})
	var asked []int // the calls the product had received when each check ran
	v.issuer.checkHook = func() { asked = append(asked, len(v.finance.intents)+len(v.finance.confirmed)+len(v.finance.voids)) }
	j := v.submit(1, "one")
	if v.issuer.checks != 0 {
		t.Fatal("admission ran the check")
	}
	if _, e := v.run(); e != nil {
		t.Fatal(e)
	}
	if v.issuer.checks != 1 || len(asked) != 1 || asked[0] != 0 || v.issuer.calls != 1 {
		t.Fatalf("checks %d, product calls at the check %v, renders %d", v.issuer.checks, asked, v.issuer.calls)
	}
	if v.issuer.checkedAs.ID != j.ID || v.issuer.checkedAs.Kind != "issue" || v.issuer.checkedAs.RowIndex != 1 || v.issuer.checkedAs.Template != template || v.issuer.checkedOn.ID != v.snapshot || len(v.issuer.checkedOn.Result.Rows) != 3 {
		t.Fatalf("the check was given %+v and %q", v.issuer.checkedAs, v.issuer.checkedOn.ID)
	}
	// The check is the worker's: a source that is revoked fails the job before it, and a row beyond the
	// snapshot cannot reach it.
	w := newIssueEnv(t, envOpts{})
	w.submit(0, "two")
	w.finance.Revoke(w.owner)
	if _, e := w.run(); !errors.Is(e, r.ErrDenied) || w.issuer.checks != 0 {
		t.Fatal(e, w.issuer.checks)
	}
}

// What the check refuses fails the job with the code of its error before the product is asked: nothing
// is reserved, nothing is voided and no number is spent (the next job takes AR-000001): the template
// withdrawn while the job waited, a value the renderer's layout will not print, a row that does not
// bind, a field the catalog no longer allows.
func TestIssueRefusedByTheCheckLeavesNothingReserved(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		code string
	}{
		"the template was withdrawn":         {r.ErrUnpublished, "TEMPLATE_NOT_PUBLISHED"},
		"a value the layout refuses":         {r.ErrUnsupported, "UNSUPPORTED_COMBINATION"},
		"a row that does not bind":           {r.ErrIntegrity, "INTEGRITY_FAILURE"},
		"a field the catalog no longer has":  {r.ErrDenied, "AUTH_SCOPE_DENIED"},
		"a background that does not resolve": {r.ErrNotFound, "NOT_FOUND"},
	} {
		t.Run(name, func(t *testing.T) {
			v := newIssueEnv(t, envOpts{})
			v.issuer.checkErr = c.err
			j := v.submit(1, "refused")
			ran, e := v.run()
			if !ran || !errors.Is(e, c.err) {
				t.Fatal(ran, e)
			}
			if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != c.code || st.Generation != 1 {
				t.Fatalf("%+v; want failed with %s", st, c.code)
			}
			if v.issuer.checks != 1 || v.issuer.calls != 0 {
				t.Fatalf("checks %d, renders %d", v.issuer.checks, v.issuer.calls)
			}
			if len(v.finance.intents) != 0 || len(v.finance.confirmed) != 0 || len(v.finance.voids) != 0 {
				t.Fatalf("the product was called: %d prepares, %d confirmations, voids %+v", len(v.finance.intents), len(v.finance.confirmed), v.finance.voids)
			}
			if rec, held := held(v, j.ID); held {
				t.Fatalf("the product holds %+v for a job refused before it was asked", rec)
			}
			// The next job takes the first number: the refused one spent none.
			v.issuer.checkErr = nil
			next := v.submit(2, "next")
			if _, e := v.run(); e != nil {
				t.Fatal(e)
			}
			if got := v.job(next.ID); got.State != "succeeded" || got.Issue.Number != "AR-000001" {
				t.Fatalf("%+v %+v", got, got.Issue)
			}
		})
	}
}

// Whatever goes wrong after the worker called the product to prepare a document, the job fails with
// its own code and the product is told to void the job under that code: a render failure (the
// template can still be withdrawn between the check and the render), a file the worker refuses, a
// confirmation the product refuses, a reservation or a receipt that is not what the contract says,
// a source revoked meanwhile, a publication that fails, an answer lost on its way back. The voided
// number is never handed out again.
func TestIssueFailuresVoidAtTheProduct(t *testing.T) {
	type scene struct {
		code      string
		asked     bool              // the product was asked to confirm the file
		confirmed bool              // and had confirmed it when the job failed
		publish   error             // the store refuses the publication with this error
		setup     func(v *issueEnv) // what goes wrong
		mend      func(v *issueEnv) // what a test undoes to issue again
	}
	scenes := map[string]scene{
		"the template is withdrawn after the check": {code: "TEMPLATE_NOT_PUBLISHED", setup: func(v *issueEnv) { v.issuer.err = r.ErrUnpublished }},
		"an unclassified render failure":            {code: "EXECUTION_FAILED", setup: func(v *issueEnv) { v.issuer.err = errors.New("the renderer broke") }},
		"the product refuses the confirmation":      {code: "AUTH_SCOPE_DENIED", asked: true, setup: func(v *issueEnv) { v.finance.confirmE = r.ErrDenied }},
		"the product is unavailable to confirm":     {code: "AUTHORITY_UNAVAILABLE", asked: true, setup: func(v *issueEnv) { v.finance.confirmE = r.ErrUnavailable }},
		"the answer to the reservation is lost":     {code: "AUTHORITY_UNAVAILABLE", setup: func(v *issueEnv) { v.finance.prepareE = r.ErrUnavailable }},
		"the source is revoked while rendering":     {code: "AUTH_SCOPE_DENIED", asked: true, confirmed: true, setup: func(v *issueEnv) { v.issuer.hook = func() { v.finance.Revoke(v.owner) } }, mend: func(v *issueEnv) { v.finance.Grant(v.owner, contractsim.DefaultGrant()) }},
		"the publication is refused as invalid":     {code: "INTEGRITY_FAILURE", asked: true, confirmed: true, publish: r.ErrIntegrity},
		"the publication fails for the database":    {code: "EXECUTION_FAILED", asked: true, confirmed: true, publish: errors.New("the database is down")},
	}
	for name, change := range map[string]func(*r.IssueReservation){
		"a reservation without a reference":          func(res *r.IssueReservation) { res.Ref = "" },
		"a reservation reference of 129 bytes":       func(res *r.IssueReservation) { res.Ref = strings.Repeat("r", 129) },
		"a reservation of another job":               func(res *r.IssueReservation) { res.JobID = strings.Repeat("9", 32) },
		"a number with a space":                      func(res *r.IssueReservation) { res.Number = "AR 000001" },
		"a number of 33 characters":                  func(res *r.IssueReservation) { res.Number = strings.Repeat("N", 33) },
		"an empty number":                            func(res *r.IssueReservation) { res.Number = "" },
		"a number that starts with a dash":           func(res *r.IssueReservation) { res.Number = "-1" },
		"an issue time with a fraction of a second":  func(res *r.IssueReservation) { res.IssuedAt = res.IssuedAt.Add(500 * time.Millisecond) },
		"no issue time":                              func(res *r.IssueReservation) { res.IssuedAt = time.Time{} },
		"an expiry that is not after the issue time": func(res *r.IssueReservation) { res.ExpiresAt = res.IssuedAt },
		"an expiry before the issue time":            func(res *r.IssueReservation) { res.ExpiresAt = res.IssuedAt.Add(-time.Second) },
	} {
		scenes[name] = scene{code: "INTEGRITY_FAILURE", setup: func(v *issueEnv) { v.finance.prepareAs = change }}
	}
	for name, change := range map[string]func(*r.IssueReceipt){
		"a receipt without a reference":        func(rc *r.IssueReceipt) { rc.Ref = "" },
		"a receipt reference of 129 bytes":     func(rc *r.IssueReceipt) { rc.Ref = strings.Repeat("c", 129) },
		"a confirmation time with a fraction":  func(rc *r.IssueReceipt) { rc.ConfirmedAt = rc.ConfirmedAt.Add(time.Millisecond) },
		"no confirmation time":                 func(rc *r.IssueReceipt) { rc.ConfirmedAt = time.Time{} },
		"a confirmation before the issue time": func(rc *r.IssueReceipt) { rc.ConfirmedAt = rc.ConfirmedAt.Add(-time.Second) },
	} {
		scenes[name] = scene{code: "INTEGRITY_FAILURE", asked: true, confirmed: true, setup: func(v *issueEnv) { v.finance.confirmAs = change }}
	}
	for name, sc := range scenes {
		t.Run(name, func(t *testing.T) {
			v := newIssueEnv(t, envOpts{})
			if sc.setup != nil {
				sc.setup(v)
			}
			v.store.refuse = sc.publish
			j := v.submit(1, "k")
			ran, e := v.run()
			if !ran || e == nil {
				t.Fatal("the job did not fail", ran, e)
			}
			if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != sc.code {
				t.Fatalf("the job: %+v; want failed with %s (the error: %v)", st, sc.code, e)
			}
			if _, e := v.repo.Issue(context.Background(), v.p.Identity, j.ID); !errors.Is(e, r.ErrNotFound) {
				t.Fatal("a record exists for a failed job", e)
			}
			// The product was told to void the job under the code, and holds it void; the file
			// it had confirmed, if any, stays on its record.
			if len(v.finance.voids) != 1 || v.finance.voids[0] != (voidCall{j.ID, sc.code}) {
				t.Fatalf("voids: %+v", v.finance.voids)
			}
			rec := assertVoided(t, v, j.ID, sc.code)
			if (len(v.finance.confirmed) > 0) != sc.asked || (rec.SHA256 != "") != sc.confirmed || rec.Number != "AR-000001" {
				t.Fatalf("confirmations asked %d, the void record %+v; want asked %v, confirmed %v", len(v.finance.confirmed), rec, sc.asked, sc.confirmed)
			}
			// What went wrong is mended and the next document is issued: it has the next number.
			v.finance.prepareAs, v.finance.confirmAs, v.finance.prepareE, v.finance.confirmE = nil, nil, nil, nil
			v.issuer.err, v.issuer.hook, v.store.refuse = nil, nil, nil
			if sc.mend != nil {
				sc.mend(v)
			}
			next := v.submit(2, "next")
			if _, e := v.run(); e != nil {
				t.Fatal(e)
			}
			if got := v.job(next.ID); got.State != "succeeded" || got.Issue.Number != "AR-000002" {
				t.Fatalf("%+v %+v", got, got.Issue)
			}
		})
	}
}

// A void that cannot be delivered fails the job with ISSUE_UNRECONCILED instead of its own code; the
// product keeps the reservation, which it can find by the job id.
func TestIssueVoidThatFailsLeavesTheJobUnreconciled(t *testing.T) {
	// The product answers every void with an error.
	v := newIssueEnv(t, envOpts{})
	v.finance.voidE = r.ErrUnavailable
	v.issuer.err = r.ErrUnpublished
	j := v.submit(0, "one")
	_, e := v.run()
	if !errors.Is(e, r.ErrUnreconciled) || !errors.Is(e, r.ErrUnpublished) || !errors.Is(e, r.ErrUnavailable) {
		t.Fatal("the worker's error names neither the unreconciled job, its own failure nor the void's", e)
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "ISSUE_UNRECONCILED" {
		t.Fatalf("%+v", st)
	}
	if len(v.finance.voids) != 1 || v.finance.voids[0] != (voidCall{j.ID, "TEMPLATE_NOT_PUBLISHED"}) {
		t.Fatalf("the void asked for: %+v", v.finance.voids)
	}
	if rec, ok := held(v, j.ID); !ok || rec.State != contractsim.IssueReserved || rec.Number != "AR-000001" {
		t.Fatalf("the reservation the product keeps for the job: %+v %v", rec, ok)
	}
	list, e := v.s.Issues(context.Background(), v.p, 5)
	if e != nil || len(list) != 1 || list[0].ErrorCode != "ISSUE_UNRECONCILED" || list[0].Number != "" {
		t.Fatalf("%+v %v", list, e)
	}
	// The product becomes unavailable after it reserved, while the document renders: the
	// confirmation fails, and so does the void.
	w := newIssueEnv(t, envOpts{})
	w.issuer.hook = func() { w.finance.SetUnavailable(true) }
	k := w.submit(0, "two")
	if _, e = w.run(); !errors.Is(e, r.ErrUnreconciled) || !errors.Is(e, r.ErrUnavailable) {
		t.Fatal(e)
	}
	if st := w.stored(k.ID); st.State != "failed" || st.ErrorCode != "ISSUE_UNRECONCILED" {
		t.Fatalf("%+v", st)
	}
	w.finance.SetUnavailable(false)
	if rec, ok := held(w, k.ID); !ok || rec.State != contractsim.IssueReserved {
		t.Fatalf("%+v %v", rec, ok)
	}
	// ISSUE_UNRECONCILED is a code like the others: a job read back says so.
	if got := w.job(k.ID); got.State != "failed" || got.ErrorCode != "ISSUE_UNRECONCILED" {
		t.Fatalf("%+v", got)
	}
}

// The void runs in a context of its own: the job's may have ended (it timed out or was cancelled
// while the worker rendered), which must not skip the void, and the void is bounded in time.
func TestIssueVoidRunsInAContextOfItsOwn(t *testing.T) {
	v := newIssueEnv(t, envOpts{})
	j := v.submit(0, "k")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.issuer.err = errors.New("the renderer broke")
	v.issuer.hook = cancel // the job's context ends while the document renders
	if ran, e := v.s.RunOne(ctx); !ran || e == nil {
		t.Fatal(ran, e)
	}
	if ctx.Err() == nil {
		t.Fatal("the job's context did not end")
	}
	if len(v.finance.voids) != 1 || v.finance.voidCtx.err != nil || !v.finance.voidCtx.hasDeadline || v.finance.voidCtx.remaining > r.VoidTimeout || v.finance.voidCtx.remaining < r.VoidTimeout-2*time.Second {
		t.Fatalf("the void ran in %+v after %+v", v.finance.voidCtx, v.finance.voids)
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "EXECUTION_FAILED" {
		t.Fatalf("%+v", st)
	}
	assertVoided(t, v, j.ID, "EXECUTION_FAILED")
}

// A worker that loses the lease never voids: the worker that holds the job reuses the same
// reservation, which the product answers again by the job id, renders the same bytes and has the
// product confirm them again, which it does idempotently.
func TestLostLeaseDoesNotVoidAndTheNextClaimReusesTheReservation(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	j := v.submit(1, "one")
	v.store.lose = 1
	// The first worker confirmed the file and then lost the lease: the publication is refused
	// while the job stays running.
	if ran, e := v.run(); !ran || !errors.Is(e, r.ErrLease) {
		t.Fatal(ran, e)
	}
	if st := v.stored(j.ID); st.State != "running" || st.Generation != 1 || st.ErrorCode != "" {
		t.Fatalf("a lost lease failed or finished the job: %+v", st)
	}
	if len(v.finance.voids) != 0 {
		t.Fatalf("a lost lease voided the job: %+v", v.finance.voids)
	}
	if rec, ok := held(v, j.ID); !ok || rec.State != contractsim.IssueConfirmed {
		t.Fatalf("the product's record after the lost lease: %+v %v", rec, ok)
	}
	// The lease ran out (the wrapper moved the clock): another claim takes the job, at the next
	// generation, and completes it under the same number.
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	st := v.stored(j.ID)
	if st.State != "succeeded" || st.Generation != 2 {
		t.Fatalf("%+v", st)
	}
	if got := v.job(j.ID); got.Issue == nil || got.Issue.Number != "AR-000001" {
		t.Fatalf("%+v", got)
	}
	if len(v.finance.voids) != 0 || len(v.finance.prepared) != 2 || !sameReservation(v.finance.prepared[0], v.finance.prepared[1]) {
		t.Fatalf("the second claim did not reuse the reservation: %+v (voids %+v)", v.finance.prepared, v.finance.voids)
	}
	// The same bytes, so the same digest, confirmed twice with the same receipt.
	if v.issuer.calls != 2 || len(v.finance.confirmed) != 2 || v.finance.confirmed[0] != v.finance.confirmed[1] || len(v.finance.receipts) != 2 || !sameReceipt(v.finance.receipts[0], v.finance.receipts[1]) {
		t.Fatalf("confirmations: %+v receipts %+v", v.finance.confirmed, v.finance.receipts)
	}
	a, e := v.s.Download(ctx, v.p, st.ArtifactID)
	if e != nil || v.finance.confirmed[0].SHA256 != a.SHA256 {
		t.Fatal("the file confirmed is not the file published", e)
	}
	// One confirmed record at the product, which the platform's record names, and no number
	// was spent on the second claim: the next document is AR-000002.
	rec, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
	theirs, ok := held(v, j.ID)
	if e != nil || !ok || theirs.State != contractsim.IssueConfirmed || theirs.SHA256 != a.SHA256 || rec.ConfirmationRef != theirs.ConfirmationRef || rec.ReservationRef != theirs.Ref || rec.Number != "AR-000001" {
		t.Fatalf("%+v %+v %v %v", rec, theirs, ok, e)
	}
	next := v.submit(2, "two")
	if _, e = v.run(); e != nil {
		t.Fatal(e)
	}
	if got := v.job(next.ID); got.Issue == nil || got.Issue.Number != "AR-000002" {
		t.Fatalf("%+v", got)
	}
}

// The rule for voiding has two sides: an attempt that reached the product, and a claim later than the
// first (an earlier attempt may have reserved or confirmed). A first attempt that fails before it
// reached the product calls nothing there and keeps its own code.
func TestIssueVoidsOnLaterGenerationsAndNotOnFirstAttemptsThatNeverReachedTheProduct(t *testing.T) {
	ctx := context.Background()
	// First side: generation 1, the source is revoked before the worker gets to the product.
	v := newIssueEnv(t, envOpts{})
	j := v.submit(0, "one")
	v.finance.Revoke(v.owner)
	if _, e := v.run(); !errors.Is(e, r.ErrDenied) {
		t.Fatal(e)
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "AUTH_SCOPE_DENIED" || st.Generation != 1 {
		t.Fatalf("%+v", st)
	}
	if len(v.finance.intents) != 0 || len(v.finance.voids) != 0 || v.issuer.calls != 0 {
		t.Fatalf("a first attempt that never reached the product called it: intents %d, voids %+v", len(v.finance.intents), v.finance.voids)
	}
	if _, ok := held(v, j.ID); ok {
		t.Fatal("the product holds something for a job it was never asked about")
	}
	// The other side: a worker took the job (generation 1) and vanished; its lease runs out; the
	// next claim, generation 2, fails before the product too, but an earlier attempt may have
	// reserved, so it voids, with its own code as the reason.
	w := newIssueEnv(t, envOpts{})
	k := w.submit(0, "two")
	crashed, e := w.repo.Claim(ctx, r.LeaseDuration)
	if e != nil || crashed == nil || crashed.Job.ID != k.ID || crashed.Job.Generation != 1 {
		t.Fatal(crashed, e)
	}
	w.clock.Advance(r.LeaseDuration + time.Second)
	w.finance.Revoke(w.owner)
	if ran, e := w.run(); !ran || !errors.Is(e, r.ErrDenied) {
		t.Fatal(ran, e)
	}
	if st := w.stored(k.ID); st.State != "failed" || st.ErrorCode != "AUTH_SCOPE_DENIED" || st.Generation != 2 {
		t.Fatalf("%+v", st)
	}
	if len(w.finance.intents) != 0 || len(w.finance.voids) != 1 || w.finance.voids[0] != (voidCall{k.ID, "AUTH_SCOPE_DENIED"}) {
		t.Fatalf("a claim of generation 2 did not void: voids %+v", w.finance.voids)
	}
	// A claim of generation 2 whose predecessor reserved and confirmed: the template is withdrawn
	// while the job waits, so it fails at the check (before it would call the product) or at the
	// render (after the check passed), and either way voids the confirmed document of the earlier
	// attempt.
	for name, withdraw := range map[string]func(*fakeIssuer){
		"at the check":  func(i *fakeIssuer) { i.checkErr = r.ErrUnpublished },
		"at the render": func(i *fakeIssuer) { i.err = r.ErrUnpublished },
	} {
		x := newIssueEnv(t, envOpts{})
		l := x.submit(0, "three")
		x.store.lose = 1
		if _, e := x.run(); !errors.Is(e, r.ErrLease) {
			t.Fatal(name, e)
		}
		confirmed, ok := held(x, l.ID)
		if !ok || confirmed.State != contractsim.IssueConfirmed {
			t.Fatalf("%s: %+v %v", name, confirmed, ok)
		}
		withdraw(x.issuer)
		prepares := len(x.finance.intents)
		if _, e := x.run(); !errors.Is(e, r.ErrUnpublished) {
			t.Fatal(name, e)
		}
		if st := x.stored(l.ID); st.State != "failed" || st.ErrorCode != "TEMPLATE_NOT_PUBLISHED" || st.Generation != 2 {
			t.Fatalf("%s: %+v", name, st)
		}
		if (len(x.finance.intents) > prepares) != (name == "at the render") {
			t.Errorf("%s: the second claim asked the product %d times", name, len(x.finance.intents)-prepares)
		}
		rec := assertVoided(t, x, l.ID, "TEMPLATE_NOT_PUBLISHED")
		if rec.SHA256 != confirmed.SHA256 || rec.Number != confirmed.Number {
			t.Fatalf("%s: the voided confirmed document: %+v, was %+v", name, rec, confirmed)
		}
	}
}

// The store, not the worker's memory, says whether this attempt still holds the job: when the
// publication committed although it reported an error, or another worker claimed the job, the
// confirmed document is not voided and the job is not failed.
func TestIssueFailureDoesNotVoidWhatAnotherOutcomeOwns(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	j := v.submit(0, "one")
	v.store.lie = 1
	if _, e := v.run(); e == nil {
		t.Fatal("the lost acknowledgement was not reported")
	}
	if st := v.stored(j.ID); st.State != "succeeded" || st.ArtifactID == "" {
		t.Fatalf("%+v", st)
	}
	if len(v.finance.voids) != 0 {
		t.Fatalf("a document that was published was voided: %+v", v.finance.voids)
	}
	if rec, ok := held(v, j.ID); !ok || rec.State != contractsim.IssueConfirmed {
		t.Fatalf("%+v %v", rec, ok)
	}
	if got := v.job(j.ID); got.State != "succeeded" || got.Issue.Number != "AR-000001" {
		t.Fatalf("%+v", got)
	}
	// Another worker claims the job while this one renders and then fails: its reservation is not
	// voided under it.
	w := newIssueEnv(t, envOpts{})
	k := w.submit(0, "two")
	w.issuer.err = errors.New("the renderer broke")
	w.issuer.hook = func() {
		w.clock.Advance(r.LeaseDuration + time.Second)
		taken, e := w.repo.Claim(ctx, r.LeaseDuration)
		if e != nil || taken == nil || taken.Job.Generation != 2 {
			t.Error("the job was not claimed again", taken, e)
		}
	}
	if _, e := w.run(); e == nil {
		t.Fatal("the render did not fail")
	}
	if len(w.finance.voids) != 0 {
		t.Fatalf("a worker voided a reservation another worker holds: %+v", w.finance.voids)
	}
	if st := w.stored(k.ID); st.State != "running" || st.Generation != 2 {
		t.Fatalf("the stale worker failed a job it no longer holds: %+v", st)
	}
	if rec, ok := held(w, k.ID); !ok || rec.State != contractsim.IssueReserved {
		t.Fatalf("%+v %v", rec, ok)
	}
}

// An issue job is cancelled only while queued: a running one is returned unchanged, because the
// product may hold a reservation for it. Other kinds are cancelled while running as before.
func TestIssueJobsCancelOnlyWhileQueued(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	queued := v.submit(0, "queued")
	c, e := v.s.Cancel(ctx, v.p, queued.ID)
	if e != nil || c.State != "cancelled" || c.Issue == nil || c.Issue.Number != "" {
		t.Fatal(c, e)
	}
	if ran, e := v.run(); ran || e != nil {
		t.Fatal("a cancelled job ran", ran, e)
	}
	running := v.submit(1, "running")
	claim, e := v.repo.Claim(ctx, r.LeaseDuration)
	if e != nil || claim == nil || claim.Job.ID != running.ID {
		t.Fatal(claim, e)
	}
	c, e = v.s.Cancel(ctx, v.p, running.ID)
	if e != nil || c.State != "running" || c.Kind != "issue" {
		t.Fatal("a running issue job was cancelled", c, e)
	}
	if st := v.stored(running.ID); st.State != "running" || st.LeaseUntil.IsZero() || st.Generation != 1 {
		t.Fatalf("the cancel changed the running issue job: %+v", st)
	}
	// The claim is still good: the job publishes. A running snapshot job is cancelled, as before.
	snap, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.ShipmentQuery(), "snap2")
	if e != nil {
		t.Fatal(e)
	}
	claimed, e := v.repo.Claim(ctx, r.LeaseDuration)
	if e != nil || claimed == nil || claimed.Job.ID != snap.ID {
		t.Fatal(claimed, e)
	}
	if c, e = v.s.Cancel(ctx, v.p, snap.ID); e != nil || c.State != "cancelled" {
		t.Fatal("a running snapshot job was not cancelled", c, e)
	}
}

func TestIssueContractRefusesInconsistentJobsAndPublications(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	j := v.submit(2, "k")
	stored, e := v.repo.Job(ctx, v.p.Identity, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	// The stored job is a valid admission; every frozen field is covered by the request hash or
	// the format rules.
	if e := r.ValidateAdmission(stored); e != nil {
		t.Fatalf("the stored issue job is not valid: %v", e)
	}
	for name, change := range map[string]func(*r.Job){
		"version":         func(j *r.Job) { j.TemplateVersion++ },
		"row":             func(j *r.Job) { j.RowIndex++ },
		"template bytes":  func(j *r.Job) { j.Template = `{"schema_version":"1.1"}` },
		"template digest": func(j *r.Job) { j.TemplateSHA256 = strings.Repeat("0", 64) },
		"format":          func(j *r.Job) { j.Format = "" },
		"export with template fields": func(j *r.Job) {
			j.Kind, j.Format, j.RequestHash = "export", "pdf", r.ExportRequestHash(j.SnapshotID, "pdf")
		},
	} {
		v := stored
		change(&v)
		if r.ValidateAdmission(v) == nil {
			t.Errorf("%s: an inconsistent issue job is admitted", name)
		}
	}
	claim, e := v.repo.Claim(ctx, time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	at := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	content := []byte("%PDF-1.7 x")
	artifact := r.Artifact{ID: r.NewID(), Owner: stored.Owner, SnapshotID: stored.SnapshotID, Format: "pdf", ExpiresAt: stored.Prepared.ExpiresAt, Content: content, Issue: stored.ID}
	artifact.SHA256 = digestOf(content)
	rec := r.Issue{ID: stored.ID, Owner: stored.Owner, Number: "AR-000007", TemplateID: stored.TemplateID, TemplateVersion: stored.TemplateVersion, TemplateSHA256: stored.TemplateSHA256, SnapshotID: stored.SnapshotID, SnapshotSHA256: strings.Repeat("c", 64), RowIndex: stored.RowIndex, ArtifactID: artifact.ID, SHA256: artifact.SHA256, Pages: 1, IssuedAt: at,
		ReservationRef: "reservation-7", ConfirmationRef: "confirmation-7", ConfirmedAt: at.Add(2 * time.Second)}
	rec.Stamp = r.IssueStamp(rec.Number, at, rec.ID)
	if e := r.ValidatePublication(claim.Job, r.Publication{Artifact: &artifact, Issue: &rec}); e != nil {
		t.Fatalf("a consistent issue publication is refused: %v", e)
	}
	restamp := func(i *r.Issue) { i.Stamp = r.IssueStamp(i.Number, i.IssuedAt, i.ID) }
	// What a record may carry that is unusual and still valid: the product's number of any shape
	// the contract allows, references of 1 and 128 bytes, a confirmation in the same second as the
	// issue, times that are another zone's.
	for name, change := range map[string]func(a *r.Artifact, i *r.Issue){
		"the longest number":                func(a *r.Artifact, i *r.Issue) { i.Number = strings.Repeat("W", 32); restamp(i) },
		"a number with every character":     func(a *r.Artifact, i *r.Issue) { i.Number = "a.B_c-9"; restamp(i) },
		"references of 1 and 128 bytes":     func(a *r.Artifact, i *r.Issue) { i.ReservationRef, i.ConfirmationRef = "x", strings.Repeat("y", 128) },
		"a confirmation in the same second": func(a *r.Artifact, i *r.Issue) { i.ConfirmedAt = i.IssuedAt },
		"times in another zone": func(a *r.Artifact, i *r.Issue) {
			z := time.FixedZone("CST", 8*3600)
			i.IssuedAt, i.ConfirmedAt = i.IssuedAt.In(z), i.ConfirmedAt.In(z)
		},
	} {
		a, i := artifact, rec
		change(&a, &i)
		if e := r.ValidatePublication(claim.Job, r.Publication{Artifact: &a, Issue: &i}); e != nil {
			t.Errorf("%s: a valid publication is refused: %v", name, e)
		}
	}
	for name, change := range map[string]func(a *r.Artifact, i *r.Issue){
		"no record":                           func(a *r.Artifact, i *r.Issue) { *i = r.Issue{} },
		"artifact without issue":              func(a *r.Artifact, i *r.Issue) { a.Issue = "" },
		"record of another job":               func(a *r.Artifact, i *r.Issue) { i.ID = r.NewID() },
		"number changed":                      func(a *r.Artifact, i *r.Issue) { i.Number = "AR-000008" },
		"no number":                           func(a *r.Artifact, i *r.Issue) { i.Number = ""; restamp(i) },
		"number with a space":                 func(a *r.Artifact, i *r.Issue) { i.Number = "AR 7"; restamp(i) },
		"number of 33 characters":             func(a *r.Artifact, i *r.Issue) { i.Number = strings.Repeat("N", 33); restamp(i) },
		"number starting with a dash":         func(a *r.Artifact, i *r.Issue) { i.Number = "-7"; restamp(i) },
		"number with a newline":               func(a *r.Artifact, i *r.Issue) { i.Number = "AR-7\n"; restamp(i) },
		"digest":                              func(a *r.Artifact, i *r.Issue) { i.SHA256 = strings.Repeat("0", 64) },
		"artifact id":                         func(a *r.Artifact, i *r.Issue) { i.ArtifactID = r.NewID() },
		"pages 0":                             func(a *r.Artifact, i *r.Issue) { i.Pages = 0 },
		"pages 101":                           func(a *r.Artifact, i *r.Issue) { i.Pages = r.MaxIssuePages + 1 },
		"stamp of another number":             func(a *r.Artifact, i *r.Issue) { i.Stamp = r.IssueStamp("AR-000008", at, i.ID) },
		"stamp of another time":               func(a *r.Artifact, i *r.Issue) { i.Stamp = r.IssueStamp(i.Number, at.Add(time.Second), i.ID) },
		"no stamp":                            func(a *r.Artifact, i *r.Issue) { i.Stamp = "" },
		"time with a fraction":                func(a *r.Artifact, i *r.Issue) { i.IssuedAt = at.Add(time.Millisecond); restamp(i) },
		"no time":                             func(a *r.Artifact, i *r.Issue) { i.IssuedAt = time.Time{}; restamp(i) },
		"snapshot digest":                     func(a *r.Artifact, i *r.Issue) { i.SnapshotSHA256 = "" },
		"row":                                 func(a *r.Artifact, i *r.Issue) { i.RowIndex++ },
		"template version":                    func(a *r.Artifact, i *r.Issue) { i.TemplateVersion++ },
		"format":                              func(a *r.Artifact, i *r.Issue) { a.Format = "" },
		"no reservation reference":            func(a *r.Artifact, i *r.Issue) { i.ReservationRef = "" },
		"reservation reference of 129 bytes":  func(a *r.Artifact, i *r.Issue) { i.ReservationRef = strings.Repeat("r", 129) },
		"no confirmation reference":           func(a *r.Artifact, i *r.Issue) { i.ConfirmationRef = "" },
		"confirmation reference of 129 bytes": func(a *r.Artifact, i *r.Issue) { i.ConfirmationRef = strings.Repeat("c", 129) },
		"no confirmation time":                func(a *r.Artifact, i *r.Issue) { i.ConfirmedAt = time.Time{} },
		"confirmation with a fraction":        func(a *r.Artifact, i *r.Issue) { i.ConfirmedAt = i.ConfirmedAt.Add(time.Millisecond) },
		"confirmation before the issue time":  func(a *r.Artifact, i *r.Issue) { i.ConfirmedAt = i.IssuedAt.Add(-time.Second) },
	} {
		a, i := artifact, rec
		change(&a, &i)
		pub := r.Publication{Artifact: &a, Issue: &i}
		if name == "no record" {
			pub.Issue = nil
		}
		if r.ValidatePublication(claim.Job, pub) == nil {
			t.Errorf("%s: an inconsistent issue publication is accepted", name)
		}
	}
}

// Platform ids are the 32 lowercase hexadecimal characters NewID makes, and no other spelling of
// the same bytes: the issue stamp's width guarantee assumes a lowercase record id.
func TestIDsAreCanonicalLowercaseHex(t *testing.T) {
	v := newIssueEnv(t, envOpts{})
	j := v.submit(0, "k")
	stored := v.stored(j.ID)
	if e := r.ValidateAdmission(stored); e != nil {
		t.Fatal(e)
	}
	// An id that is not the canonical spelling is refused wherever the contract takes one: the
	// job's own, the snapshot's and the template's.
	for _, id := range []string{"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("g", 32), " " + strings.Repeat("a", 31), strings.Repeat("A", 32), "0123456789ABCDEF0123456789abcdef", "0123456789abcdef0123456789abcdeF"} {
		for field, set := range map[string]func(*r.Job){
			"job id":      func(j *r.Job) { j.ID = id },
			"snapshot id": func(j *r.Job) { j.SnapshotID = id },
			"template id": func(j *r.Job) { j.TemplateID = id },
		} {
			c := stored
			c.Key = "other"
			set(&c)
			c.RequestHash = r.IssueRequestHash(c.TemplateID, c.TemplateVersion, c.TemplateSHA256, c.SnapshotID, c.RowIndex)
			if r.ValidateAdmission(c) == nil {
				t.Errorf("%s %q is admitted", field, id)
			}
		}
	}
	// The same ids in lowercase are valid, the shape NewID makes.
	for _, id := range []string{strings.Repeat("a", 32), "0123456789abcdef0123456789abcdef", r.NewID()} {
		c := stored
		c.ID = id
		if e := r.ValidateAdmission(c); e != nil {
			t.Errorf("the lowercase id %q is refused: %v", id, e)
		}
	}
}

func digestOf(b []byte) string { return bytesDigestHex(b) }

func bytesDigestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
