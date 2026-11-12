package httpsource_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// clock is a test's clock shared by the simulator, the adapter and the service.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *clock) add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.at = c.at.Add(d) }
func newClock() *clock {
	return &clock{at: time.Now().UTC().Truncate(time.Second).Add(500 * time.Millisecond)}
}
func (fakePDF) Write(context.Context, r.Snapshot) ([]byte, error) { return []byte("%PDF-1.7 x"), nil }

type fakePDF struct{}

// stamper renders an issue from its request alone; err fails the render after the product reserved a number.
type stamper struct{ err error }

func (stamper) Check(context.Context, r.Job, r.Snapshot) error { return nil }
func (s stamper) Issue(_ context.Context, req r.IssueRequest) (r.IssueResult, error) {
	if s.err != nil {
		return r.IssueResult{}, s.err
	}
	return r.IssueResult{PDF: []byte("%PDF-1.7\n" + req.Stamp + "\n%%EOF"), Pages: 1}, nil
}

type world struct {
	sim *contractsim.Simulator
	s   *r.Service
	p   r.Principal
	c   *clock
}

// newWorld is a service whose only product is the simulator reached through the protocol, with records rows of the receivables fixture.
// With direct it is the simulator in process: every test runs both ways and expects the same outcomes.
func newWorld(t *testing.T, direct bool, records int, issuer r.Issuer) world {
	t.Helper()
	c := newClock()
	base := contractsim.Financial().Fixture()
	var recs []contractsim.Record
	var keys []string
	for i := 0; i < records; i++ {
		key := fmt.Sprintf("r%03d", i)
		recs = append(recs, contractsim.Record{Company: "company_demo", Key: key, Values: []string{fmt.Sprintf("customer_%03d", i), "current", fmt.Sprintf("%d.%02d", i, i%100)}})
		keys = append(keys, key)
	}
	sim := contractsim.New(base.Dataset, recs)
	sim.SetClock(c.now)
	p := contractsim.DemoPrincipal()
	g := contractsim.DefaultGrant()
	g.Rows = keys
	sim.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), g)
	var a r.Adapter = sim
	if !direct {
		a = sourcetest.Remote(t, sim, func(cfg *httpsource.Config) { cfg.Now = c.now })
	}
	s, err := r.NewService(r.Config{Repository: memory.New(c.now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}},
		CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: issuer, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return world{sim, s, p, c}
}

// run runs the next job, which is id, and returns its view; the worker's error is the job's, which the view carries.
func (w world) run(t *testing.T, id string) r.JobView {
	t.Helper()
	if ran, _ := w.s.RunOne(ctx); !ran {
		t.Fatal("no job ran")
	}
	v, err := w.s.Job(ctx, w.p, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// both runs a test with the simulator in process and through the protocol.
func both(t *testing.T, f func(t *testing.T, direct bool)) {
	t.Run("in_process", func(t *testing.T) { f(t, true) })
	t.Run("over_http", func(t *testing.T) { f(t, false) })
}

func TestTheServiceFreezesAndPagesASnapshotThroughTheProtocol(t *testing.T) { both(t, freezeAndPage) }
func TestTheServiceIssuesThroughTheProtocol(t *testing.T)                   { both(t, issueAndVoid) }
func TestTheServiceCancelsAnIssueThroughTheProtocol(t *testing.T)           { both(t, issueAndCancel) }

// issueOne issues one document of the world's snapshot row and returns its view.
func issueOne(t *testing.T, w world, key string) r.IssueView {
	t.Helper()
	j, err := w.s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "snap-"+key)
	if err != nil {
		t.Fatal(err)
	}
	snap := w.run(t, j.ID)
	in := r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(`{"schema_version":"1.0"}`), SnapshotID: snap.SnapshotID, RowIndex: 1}
	issue, err := w.s.SubmitIssue(ctx, w.p, in, "issue-"+key)
	if err != nil {
		t.Fatal(err)
	}
	if done := w.run(t, issue.ID); done.State != "succeeded" {
		t.Fatalf("%+v", done)
	}
	view, err := w.s.Issue(ctx, w.p, issue.ID)
	if err != nil || view.Number == "" || view.Cancellation != nil {
		t.Fatalf("%+v %v", view, err)
	}
	return view
}

// The cancellation of an issued document (ADR 0025 decisions 1 to 5, S24-A) is the same through the protocol as in process: the
// service asks the product, records its receipt beside the issue record, shows it in the view and refuses the download; a
// connection that does not declare the capability cannot be asked, and nothing of the product's is touched.
func issueAndCancel(t *testing.T, direct bool) {
	const reason = "客户退回：金额有误"
	w := newWorld(t, direct, 3, stamper{})
	if can := w.s.CanCancel(w.p); len(can) != 1 || !can[contractsim.FinanceIntegration] {
		t.Fatal("the product can be asked to cancel:", can)
	}
	before := issueOne(t, w, "one")
	if _, err := w.s.Download(ctx, w.p, before.ArtifactID); err != nil {
		t.Fatal("the document is served until it is cancelled:", err)
	}
	w.c.add(3 * time.Second)
	view, err := w.s.CancelIssue(ctx, w.p, before.ID, reason)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := w.sim.IssueRecord(before.ID)
	if !ok || rec.State != contractsim.IssueConfirmed || !rec.Cancelled() || rec.CancelReason != reason || rec.Number != before.Number {
		t.Fatalf("the product holds %+v", rec)
	}
	c := view.Cancellation
	if c == nil || c.Ref != rec.CancellationRef || c.CancelledAt != rec.CancelledAt.UTC().Format(time.RFC3339) || c.Reason != reason {
		t.Fatalf("the view's cancellation %+v, the product's %+v", c, rec)
	}
	// The record keeps every field it had.
	kept := view
	kept.Cancellation = nil
	if !reflect.DeepEqual(kept, before) {
		t.Fatalf("the cancellation changed the record:\n%+v\n%+v", kept, before)
	}
	// The document is no longer served, and says why.
	if _, err = w.s.Download(ctx, w.p, before.ArtifactID); !errors.Is(err, r.ErrIssueCancelled) {
		t.Fatal("the download of a cancelled document:", err)
	}
	// Asked again, with another reason: the same view; the product keeps what it recorded.
	again, err := w.s.CancelIssue(ctx, w.p, before.ID, "asked again")
	if err != nil || !reflect.DeepEqual(again, view) {
		t.Fatalf("%+v %v", again, err)
	}
	if rec2, _ := w.sim.IssueRecord(before.ID); rec2.CancellationRef != rec.CancellationRef || rec2.CancelReason != reason {
		t.Fatalf("the repeat changed the product's record: %+v", rec2)
	}
	if listed, err := w.s.Issue(ctx, w.p, before.ID); err != nil || !reflect.DeepEqual(listed, view) {
		t.Fatalf("%+v %v", listed, err)
	}
	// The number is used for good: the next document gets the next number.
	if next := issueOne(t, w, "two"); next.Number == before.Number || next.Number != "DOC-000002" {
		t.Fatal("the next document's number:", next.Number)
	}
	// The product's refusal is the answer, and nothing is recorded: the document stays served.
	other := issueOne(t, w, "three")
	w.sim.SetIssueRefused(true)
	if _, err = w.s.CancelIssue(ctx, w.p, other.ID, reason); !errors.Is(err, r.ErrDenied) || strings.Contains(err.Error(), "客户") {
		t.Fatal("a cancellation the product refuses:", err)
	}
	w.sim.SetIssueRefused(false)
	if v, err := w.s.Issue(ctx, w.p, other.ID); err != nil || v.Cancellation != nil {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err = w.s.Download(ctx, w.p, other.ArtifactID); err != nil {
		t.Fatal("the document the product did not cancel:", err)
	}
}

// A connection that does not declare cancellation behaves as before S24-A: the capability is false, a cancellation is unsupported,
// the route of the source is never asked and the document stays as it was, whatever the source could do.
func TestAConnectionWithoutCancellationCannotCancel(t *testing.T) {
	c := newClock()
	sim := contractsim.New(contractsim.Financial().Fixture().Dataset, []contractsim.Record{{Company: "company_demo", Key: "r000", Values: []string{"customer_000", "current", "1.00"}}, {Company: "company_demo", Key: "r001", Values: []string{"customer_001", "current", "2.00"}}})
	sim.SetClock(c.now)
	p := contractsim.DemoPrincipal()
	g := contractsim.DefaultGrant()
	g.Rows = []string{"r000", "r001"}
	sim.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), g)
	var mu sync.Mutex
	asked := 0
	a := sourcetest.Remote(t, observed{sim, func() { mu.Lock(); defer mu.Unlock(); asked++ }}, func(cfg *httpsource.Config) { cfg.Now = c.now; cfg.Cancellation = false })
	if _, ok := a.(r.DocumentCanceller); ok {
		t.Fatal("a connection without cancellation is a canceller")
	}
	s, err := r.NewService(r.Config{Repository: memory.New(c.now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}},
		CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: stamper{}, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	w := world{sim, s, p, c}
	if can := s.CanCancel(p); len(can) != 1 || can[contractsim.FinanceIntegration] {
		t.Fatal("the capability of a connection that does not declare it:", can)
	}
	view := issueOne(t, w, "one")
	if _, err = s.CancelIssue(ctx, p, view.ID, "a reason"); !errors.Is(err, r.ErrUnsupported) {
		t.Fatal(err)
	}
	mu.Lock()
	if asked != 0 {
		t.Fatal("the source was asked to cancel")
	}
	mu.Unlock()
	if rec, _ := sim.IssueRecord(view.ID); rec.Cancelled() {
		t.Fatal("the product cancelled")
	}
	if v, err := s.Issue(ctx, p, view.ID); err != nil || !reflect.DeepEqual(v, view) {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err = s.Download(ctx, p, view.ArtifactID); err != nil {
		t.Fatal(err)
	}
}

// observed is the simulator with its cancellations counted.
type observed struct {
	*contractsim.Simulator
	cancel func()
}

func (o observed) CancelDocument(ctx context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	o.cancel()
	return o.Simulator.CancelDocument(ctx, sc, c)
}

func freezeAndPage(t *testing.T, direct bool) {
	w := newWorld(t, direct, 120, nil)
	j, err := w.s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "one")
	if err != nil {
		t.Fatal(err)
	}
	// The source's data changes after admission: the read was prepared at admission, so the snapshot is the data before.
	w.sim.ReplaceRecords(nil)
	v := w.run(t, j.ID)
	if v.State != "succeeded" {
		t.Fatalf("%+v", v)
	}
	if w.sim.ReadCalls() != 3 { // 50 + 50 + 20 rows
		t.Fatal(w.sim.ReadCalls())
	}
	// sum of i + (i % 100) / 100 for i in 0..119: 7140 + (4950 + 190) / 100 = 7191.40
	const wantTotal = "7191.40"
	var rows [][]string
	cursor := ""
	for {
		page, err := w.s.Rows(ctx, w.p, v.SnapshotID, 40, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if page.Evidence.Watermark != "fixture-v1" || page.TotalRows != 120 || page.Totals["ar.open_balance"] != wantTotal {
			t.Fatalf("%+v", page.Evidence)
		}
		rows = append(rows, page.Rows...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(rows) != 120 || rows[0][0] != "customer_000" || rows[119][0] != "customer_119" || rows[119][2] != "119.19" {
		t.Fatal(len(rows), rows[0], rows[len(rows)-1])
	}
	// A revoked grant refuses every later disclosure; an expired read is refused, through the protocol as in process.
	w.sim.Revoke(contractsim.DemoOwner(w.p, contractsim.FinanceIntegration))
	if _, err = w.s.Rows(ctx, w.p, v.SnapshotID, 40, ""); !errors.Is(err, r.ErrDenied) {
		t.Fatal(err)
	}
	g := contractsim.DefaultGrant()
	g.Rows = nil
	for i := 0; i < 120; i++ {
		g.Rows = append(g.Rows, fmt.Sprintf("r%03d", i))
	}
	w.sim.Grant(contractsim.DemoOwner(w.p, contractsim.FinanceIntegration), g)
	if _, err = w.s.Rows(ctx, w.p, v.SnapshotID, 40, ""); err != nil {
		t.Fatal(err)
	}
	// Past the read's expiry the service refuses the snapshot itself (its own rule, before the source is asked).
	w.c.add(11 * time.Minute)
	if _, err = w.s.Rows(ctx, w.p, v.SnapshotID, 40, ""); !errors.Is(err, r.ErrIntegrity) {
		t.Fatal(err)
	}
	// A new query is prepared on the source's current data.
	res, err := w.s.Query(ctx, w.p, contractsim.FinancialQuery())
	if err != nil || len(res.Rows) != 0 || res.Evidence.Watermark != "fixture-v2" || res.Totals["ar.open_balance"] != "0.00" {
		t.Fatalf("%+v %v", res, err)
	}
}

func issueAndVoid(t *testing.T, direct bool) {
	w := newWorld(t, direct, 3, stamper{})
	j, err := w.s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "snap")
	if err != nil {
		t.Fatal(err)
	}
	snap := w.run(t, j.ID)
	in := r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(`{"schema_version":"1.0"}`), SnapshotID: snap.SnapshotID, RowIndex: 1}
	issue, err := w.s.SubmitIssue(ctx, w.p, in, "issue")
	if err != nil {
		t.Fatal(err)
	}
	done := w.run(t, issue.ID)
	view, err := w.s.Issue(ctx, w.p, issue.ID)
	if done.State != "succeeded" || err != nil || view.Number != "DOC-000001" || view.ReservationRef == "" || view.ConfirmationRef == "" {
		t.Fatalf("%+v %+v %v", done, view, err)
	}
	rec, ok := w.sim.IssueRecord(issue.ID)
	if !ok || rec.State != contractsim.IssueConfirmed || rec.Ref != view.ReservationRef || rec.SHA256 != view.SHA256 {
		t.Fatalf("%+v", rec)
	}
	// A render that fails after the reservation: the job fails and the source is told to void it, through the protocol.
	failing := newWorld(t, direct, 3, stamper{err: r.ErrUnsupported})
	j, _ = failing.s.SubmitSnapshot(ctx, failing.p, contractsim.FinancialQuery(), "snap")
	snap = failing.run(t, j.ID)
	in.SnapshotID = snap.SnapshotID
	issue, err = failing.s.SubmitIssue(ctx, failing.p, in, "issue")
	if err != nil {
		t.Fatal(err)
	}
	if v := failing.run(t, issue.ID); v.State != "failed" || v.ErrorCode != "UNSUPPORTED_COMBINATION" {
		t.Fatalf("%+v", v)
	}
	if rec, ok := failing.sim.IssueRecord(issue.ID); !ok || rec.State != contractsim.IssueVoid || rec.Reason != "UNSUPPORTED_COMBINATION" {
		t.Fatalf("%+v", rec)
	}
}

func TestAConnectionWithoutIssuanceCannotIssue(t *testing.T) {
	sim := contractsim.Financial()
	p := contractsim.DemoPrincipal()
	sim.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	a := sourcetest.Remote(t, sim, func(c *httpsource.Config) { c.Issuance = httpsource.IssuanceNone })
	if _, ok := a.(r.DocumentAuthority); ok {
		t.Fatal("issuance none is a document authority")
	}
	rows := sourcetest.Remote(t, sim, func(c *httpsource.Config) { c.Issuance = httpsource.IssuanceRows })
	auth := rows.(r.DocumentAuthority)
	sc := r.SourceContext{Owner: contractsim.DemoOwner(p, contractsim.FinanceIntegration), SourceTenant: "tenant_a"}
	q, hash := query()
	prep, err := rows.PrepareRead(ctx, sc, q, hash)
	if err != nil {
		t.Fatal(err)
	}
	group := r.IssueGroup{Fields: []string{}, Key: []string{}, Rows: []int{0}, RowsSHA256: strings.Repeat("a", 64)}
	_, err = auth.PrepareDocument(ctx, sc, r.IssueIntent{JobID: strings.Repeat("a", 32), Prepared: prep, TemplateID: strings.Repeat("b", 32), TemplateVersion: 1, TemplateSHA256: strings.Repeat("c", 64), Group: &group})
	want(t, err, r.ErrUnsupported, "a group sent to a connection of issuance rows")
	if _, held := sim.IssueRecord(strings.Repeat("a", 32)); held {
		t.Fatal("the group reached the source")
	}
}
