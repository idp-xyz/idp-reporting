package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource/protocol"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
)

const token = "CHECK-TOKEN-Zq7-never-shown-0123456789"

// lax is a broken product: it authorizes every read it is asked about, whoever asks and whatever the read says.
type lax struct{ *contractsim.Simulator }

func (lax) AuthorizeCurrent(context.Context, r.SourceContext, r.PreparedRead) error { return nil }

// mute is a product without the company directory (S19-B): the interface's four methods and the document authority, nothing else.
type mute struct {
	r.Adapter
	r.DocumentAuthority
}

// boastful is a broken product whose directory lists a company its reads refuse.
type boastful struct{ *contractsim.Simulator }

func (boastful) ListCompanies(context.Context, r.SourceContext) (r.CompanyList, error) {
	return r.CompanyList{Companies: []r.Company{{ID: "company_demo", Name: "演示公司"}, {ID: "company_other", Name: "其他公司"}}}, nil
}

func muted() mute { s := granted(); return mute{s, s} }

// repetitive is a broken product whose directory lists an id twice.
type repetitive struct{ *contractsim.Simulator }

func (repetitive) ListCompanies(context.Context, r.SourceContext) (r.CompanyList, error) {
	return r.CompanyList{Companies: []r.Company{{ID: "company_demo", Name: "演示公司"}, {ID: "company_demo", Name: "又一次"}}}, nil
}

// deaf is a broken product that reads every query without its filters and sort (S20-A): it prepares the plain query under the narrowed
// query's hash and answers the whole scope's rows and totals for a read that asked for less, and accepts what its catalog does not declare.
type deaf struct {
	*contractsim.Simulator
	mu    sync.Mutex
	plain map[string]string // the hash of the query as asked → the hash of the plain query it prepared instead
}

func (d *deaf) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, hash string) (r.PreparedRead, error) {
	q.Filters, q.Sort = []r.Filter{}, []r.Sort{}
	p, err := d.Simulator.PrepareRead(ctx, sc, q, r.QueryHash(q))
	if err != nil {
		return p, err
	}
	d.mu.Lock()
	d.plain[hash] = p.QueryHash
	d.mu.Unlock()
	p.QueryHash = hash
	return p, nil
}
func (d *deaf) inner(p r.PreparedRead) r.PreparedRead {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h, ok := d.plain[p.QueryHash]; ok {
		p.QueryHash = h
	}
	return p
}
func (d *deaf) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, p r.PreparedRead) error {
	return d.Simulator.AuthorizeCurrent(ctx, sc, d.inner(p))
}
func (d *deaf) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	page, err := d.Simulator.ReadPage(ctx, sc, d.inner(p), cursor, limit)
	page.QueryHash = p.QueryHash
	return page, err
}

// files writes the CA of the server, the sample context and the query, and returns the common arguments.
func files(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}
	ca := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	c, _ := json.Marshal(protocol.ContextOf(r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}))
	q, _ := json.Marshal(contractsim.FinancialQuery())
	return []string{"-base-url", srv.URL, "-auth-env", "CHECK_AUTH", "-ca-file", ca, "-context", write("context.json", c), "-query", write("query.json", q)}
}

func source(t *testing.T, a r.Adapter, o sourcetest.Options) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(sourcetest.New(a, o))
	t.Cleanup(srv.Close)
	return srv
}

func granted() *contractsim.Simulator {
	s := contractsim.Financial()
	s.Grant(contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), contractsim.DefaultGrant())
	return s
}

func check(t *testing.T, args []string) (int, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := run(args, func(n string) string {
		if n == "CHECK_AUTH" {
			return token
		}
		return ""
	}, &out, &errs)
	all := out.String() + errs.String()
	if strings.Contains(all, token) || strings.Contains(all, "127.0.0.1") {
		t.Fatalf("the output shows the credential or the address:\n%s", all)
	}
	return code, out.String()
}

// rules is how many rules the checker has: every run that reaches the end prints this many lines, passed, failed or skipped. K1 and K2
// (S25-B2) are the last two.
const rules = 34

var everyRulePassed = "reporting-source-check: every rule passed (" + strconv.Itoa(rules) + ")\n"

func TestTheReferenceSourcePassesEveryRule(t *testing.T) {
	sim := granted()
	watch := &watching{Simulator: sim}
	srv := source(t, watch, sourcetest.Options{Token: token})
	code, out := check(t, append(files(t, srv), "-issue", "-companies", "-cancellation"))
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, id := range []string{"P1", "A1", "C1", "C2", "C3", "C4", "R1", "R2", "R3", "R4", "R5", "R6", "R7", "R8", "R9", "R10", "R11", "R12", "R13", "F1", "F2", "F3", "F4", "F5", "F6", "D1", "D2", "I1", "X1", "X2", "X3", "X4"} {
		if !strings.Contains(out, "PASS "+id+" ") {
			t.Fatalf("%s did not pass:\n%s", id, out)
		}
	}
	if !strings.HasSuffix(out, everyRulePassed) {
		t.Fatal(out)
	}
	// The rules are printed in their order: the connection, the credential, the catalog, the read, the published filters and sorts, the
	// directory, the document authority, the cancellation.
	if order := regexp.MustCompile(`(?m)^(?:PASS|SKIP|FAIL) ([A-Z]+\d+) `).FindAllStringSubmatch(out, -1); len(order) != rules || order[18][1] != "R13" || order[19][1] != "F1" || order[24][1] != "F6" || order[25][1] != "D1" || order[26][1] != "D2" || order[27][1] != "I1" || order[28][1] != "X1" || order[29][1] != "X2" || order[30][1] != "X3" || order[31][1] != "X4" {
		t.Fatal(out)
	}
	// The scope a source vouches for (S25-B2) is not asked without -kept-results, as a connection that does not declare kept results
	// takes no such read: the two rules are skipped, last, and a source that vouches for nothing passes.
	if !strings.Contains(out, "SKIP K1 the read vouches for a scope: scope_expires_at is after expires_at: not asked (-kept-results: the connection declares the capability)\nSKIP K2 the read is authorized as the source gave it, and refused with another scope expiry or without one (denied or expired): not asked (-kept-results: the connection declares the capability)\n"+everyRulePassed) {
		t.Fatal(out)
	}
	// What the cancellation rules left in the product (S24-A): one document, confirmed and cancelled once under the check's own reason
	// (asked twice, the second time with another reason), and four numbers given out in all: I1's, the document's, X2's and X4's. The
	// output says nothing of the number.
	asked := watch.jobs()
	if len(asked) != 2 || asked[0] != asked[1] {
		t.Fatal("the cancellations the product accepted:", asked)
	}
	rec, ok := sim.IssueRecord(asked[0])
	if !ok || rec.State != contractsim.IssueConfirmed || !rec.Cancelled() || rec.CancelReason != "reporting-source-check" || rec.Number != "AR-000002" || strings.Contains(out, "AR-0000") {
		t.Fatalf("the product holds %+v\n%s", rec, out)
	}
	sc := r.SourceContext{Owner: contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), SourceTenant: "tenant_a"}
	q := r.Normalize(contractsim.FinancialQuery())
	p, err := sim.PrepareRead(context.Background(), sc, q, r.QueryHash(q))
	if err != nil {
		t.Fatal(err)
	}
	page, err := sim.ReadPage(context.Background(), sc, p, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	next, err := sim.PrepareDocument(context.Background(), sc, r.IssueIntent{JobID: r.NewID(), Prepared: p, RowSHA256: r.Digest(page.Rows[0]), TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: strings.Repeat("0", 64)})
	if err != nil || next.Number != "AR-000005" {
		t.Fatal("the next number after the check:", next.Number, err)
	}
	// A dataset that declares no filter and no sort skips the rules that need one and still refuses what is not declared (S20-A).
	plain := contractsim.Financial().Fixture()
	plain.Dataset.Filters, plain.Dataset.Sorts = nil, nil
	bare := contractsim.New(plain.Dataset, plain.Records)
	bare.Grant(contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), contractsim.DefaultGrant())
	code, out = check(t, files(t, source(t, bare, sourcetest.Options{Token: token})))
	if code != 0 || !strings.Contains(out, "SKIP F1 ") || !strings.Contains(out, "SKIP F2 ") || !strings.Contains(out, "PASS F3 ") || !strings.Contains(out, "SKIP F4 ") || !strings.Contains(out, "SKIP F5 ") || !strings.Contains(out, "PASS F6 ") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// Without -issue the document authority is not touched; without -companies the directory is not asked (S19-B); without
	// -cancellation no document is confirmed or cancelled (S24-A): a connection that does not declare a capability never asks its
	// route, so a product without it passes.
	code, out = check(t, files(t, source(t, muted(), sourcetest.Options{Token: token})))
	if code != 0 || !strings.Contains(out, "SKIP I1 ") || !strings.Contains(out, "SKIP D1 ") || !strings.Contains(out, "SKIP D2 ") || !strings.HasSuffix(out, everyRulePassed) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Contains(out, "D1 the directory answers and verifies (at most 1000 companies, each id and name 1..128 characters of text, no id twice): not asked (-companies: the connection declares the capability)") == false {
		t.Fatal(out)
	}
	for _, id := range []string{"X1", "X2", "X3", "X4"} {
		if !strings.Contains(out, "SKIP "+id+" ") {
			t.Fatalf("%s was not skipped:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "SKIP X1 a confirmed document is cancelled (a receipt of the job, with a reference and a whole second not before the confirmation), and asking again, with another reason, answers the same receipt: not asked (-cancellation: the connection declares the capability; confirms and cancels one document)\n") {
		t.Fatal(out)
	}
	// With -issue and without -cancellation the document authority is checked as before S24-A and nothing is confirmed: the product
	// is never asked to cancel, and a product that cannot cancel passes.
	quiet := &watching{Simulator: granted()}
	code, out = check(t, append(files(t, source(t, quiet, sourcetest.Options{Token: token})), "-issue"))
	if code != 0 || !strings.Contains(out, "PASS I1 ") || !strings.Contains(out, "SKIP X4 ") || !strings.HasSuffix(out, everyRulePassed) || quiet.asked() != 0 {
		t.Fatalf("exit %d, cancellations asked %d\n%s", code, quiet.asked(), out)
	}
	code, out = check(t, append(files(t, source(t, muted(), sourcetest.Options{Token: token})), "-issue"))
	if code != 0 || !strings.Contains(out, "PASS I1 ") || !strings.Contains(out, "SKIP X1 ") || !strings.HasSuffix(out, everyRulePassed) {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// watching is the reference product with the cancellations it was asked for counted and those it accepted remembered, so that a test
// can look at what the check left.
type watching struct {
	*contractsim.Simulator
	mu       sync.Mutex
	requests int
	accepted []string // the job ids of the cancellations the product accepted, in order
}

func (w *watching) CancelDocument(ctx context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	receipt, err := w.Simulator.CancelDocument(ctx, sc, c)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.requests++
	if err == nil {
		w.accepted = append(w.accepted, c.JobID)
	}
	return receipt, err
}
func (w *watching) jobs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.accepted...)
}
func (w *watching) asked() int { w.mu.Lock(); defer w.mu.Unlock(); return w.requests }

// recycling is a broken product that gives the number of a cancelled document to the next job it reserves.
type recycling struct {
	*contractsim.Simulator
	mu   sync.Mutex
	free string
}

func (p *recycling) CancelDocument(ctx context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	receipt, err := p.Simulator.CancelDocument(ctx, sc, c)
	if err == nil {
		p.mu.Lock()
		p.free = c.Number
		p.mu.Unlock()
	}
	return receipt, err
}
func (p *recycling) PrepareDocument(ctx context.Context, sc r.SourceContext, in r.IssueIntent) (r.IssueReservation, error) {
	res, err := p.Simulator.PrepareDocument(ctx, sc, in)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil && p.free != "" {
		res.Number, p.free = p.free, ""
	}
	return res, err
}

// trusting is a broken product that cancels for whoever asks: it decides as if the subject it granted had asked.
type trusting struct {
	*contractsim.Simulator
	as r.Owner
}

func (p trusting) CancelDocument(ctx context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	sc.Owner = p.as
	return p.Simulator.CancelDocument(ctx, sc, c)
}

// eager is a broken product that cancels what was never confirmed: where the reference product answers conflict, it answers a receipt.
type eager struct{ *contractsim.Simulator }

func (p eager) CancelDocument(ctx context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	receipt, err := p.Simulator.CancelDocument(ctx, sc, c)
	if errors.Is(err, r.ErrConflict) {
		return r.IssueCancellationReceipt{Ref: r.NewID(), CancelledAt: time.Now().UTC().Truncate(time.Second)}, nil
	}
	return receipt, err
}

// forgetful is a broken product that makes a new receipt every time it is asked about a document it cancelled.
type forgetful struct{ *contractsim.Simulator }

func (p forgetful) CancelDocument(ctx context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	receipt, err := p.Simulator.CancelDocument(ctx, sc, c)
	if err == nil {
		receipt.Ref = r.NewID()
	}
	return receipt, err
}

// early is a broken product whose receipt is dated before the confirmation of the document it cancels.
type early struct{ *contractsim.Simulator }

func (p early) CancelDocument(ctx context.Context, sc r.SourceContext, c r.IssueCancellation) (r.IssueCancellationReceipt, error) {
	receipt, err := p.Simulator.CancelDocument(ctx, sc, c)
	if err == nil {
		receipt.CancelledAt = receipt.CancelledAt.Add(-time.Hour)
	}
	return receipt, err
}

// The cancellation rules (S24-A, ADR 0025) fail a product that cancels wrongly, each in fixed words that hold no number, reference or
// reason: one that gives a cancelled number out again (X4), one that cancels for a subject it does not know (X3), one that cancels
// a reservation that was never confirmed (X2), one that answers another receipt when asked again or a receipt dated before the
// confirmation (X1), and one that declares the capability without implementing the route (X1, the other three skipped). Everything
// else passes: the rules of the read and of the document authority do not depend on the cancellation.
func TestASourceThatCancelsWronglyFailsTheCancellationRules(t *testing.T) {
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	const x1 = "X1 a confirmed document is cancelled (a receipt of the job, with a reference and a whole second not before the confirmation), and asking again, with another reason, answers the same receipt"
	const x2 = "X2 a reservation that was not confirmed is not cancelled (conflict), nor is a job the source never reserved (not_found)"
	const x3 = "X3 a cancellation asked by a subject the product does not know is refused (denied), before the document is cancelled and after"
	const x4 = "X4 the number of a cancelled document stays used: a job reserved afterwards gets another number"
	for _, c := range []struct {
		name    string
		product r.Adapter
		line    string   // the line of the one rule that fails, whole
		passes  []string // the cancellation rules that pass
		skips   []string // and those that are skipped
	}{
		{"gives the cancelled number out again", &recycling{Simulator: granted()},
			"FAIL " + x4 + ": a job reserved after the cancellation was given the cancelled document's number: INTEGRITY_FAILURE\n", []string{"X1", "X2", "X3"}, nil},
		{"cancels for a subject it does not know", trusting{granted(), owner},
			"FAIL " + x3 + ": before the document was cancelled: the source accepted it; want AUTH_SCOPE_DENIED\n", []string{"X1", "X2", "X4"}, nil},
		{"cancels a reservation that was never confirmed", eager{granted()},
			"FAIL " + x2 + ": a reservation that was not confirmed: the source accepted it; want IDEMPOTENCY_CONFLICT\n", []string{"X1", "X3", "X4"}, nil},
		{"answers another receipt when asked again", forgetful{granted()},
			"FAIL " + x1 + ": asking again answered another receipt: INTEGRITY_FAILURE\n", []string{"X2", "X3"}, []string{"X4"}},
		{"dates the receipt before the confirmation", early{granted()},
			"FAIL " + x1 + ": the receipt's time is before the confirmation's: INTEGRITY_FAILURE\n", []string{"X2", "X3"}, []string{"X4"}},
	} {
		code, out := check(t, append(files(t, source(t, c.product, sourcetest.Options{Token: token})), "-issue", "-cancellation"))
		if code != 1 || !strings.Contains(out, c.line) || !strings.HasSuffix(out, "reporting-source-check: 1 of "+strconv.Itoa(rules)+" rules failed\n") {
			t.Fatalf("a product that %s: exit %d\n%s", c.name, code, out)
		}
		for _, id := range c.passes {
			if !strings.Contains(out, "PASS "+id+" ") {
				t.Fatalf("a product that %s: %s did not pass:\n%s", c.name, id, out)
			}
		}
		for _, id := range c.skips {
			if !strings.Contains(out, "SKIP "+id+" ") {
				t.Fatalf("a product that %s: %s was not skipped:\n%s", c.name, id, out)
			}
		}
		for _, id := range []string{"C1", "R1", "R3", "F1", "I1"} {
			if !strings.Contains(out, "PASS "+id+" ") {
				t.Fatalf("a product that %s: %s did not pass:\n%s", c.name, id, out)
			}
		}
		if strings.Contains(out, "AR-0000") || strings.Contains(out, "reporting-source-check,") {
			t.Fatalf("a product that %s: the output shows a number or a reason:\n%s", c.name, out)
		}
	}
	// A product that issues and cannot cancel, behind a connection that declares the capability: the route answers unsupported, X1 says
	// so in the adapter's words and the other three are skipped; the document the check confirmed is voided, not left confirmed.
	held := &holding{mute: muted()}
	code, out := check(t, append(files(t, source(t, held, sourcetest.Options{Token: token})), "-issue", "-cancellation"))
	if code != 1 || !strings.Contains(out, "FAIL "+x1+": the source does not implement the route: http source cancel: the source refused with unsupported: UNSUPPORTED_COMBINATION (request ") || !strings.Contains(out, "SKIP "+x2+": the source does not cancel (X1)\n") || !strings.Contains(out, "SKIP X3 ") || !strings.Contains(out, "SKIP X4 ") || !strings.Contains(out, "PASS I1 ") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if confirmed, voided := held.counts(); confirmed != 1 || voided < 2 {
		t.Fatalf("the check confirmed %d documents and voided %d jobs", confirmed, voided)
	}
	sim := held.Adapter.(*contractsim.Simulator)
	for _, job := range held.confirmedJobs() {
		if rec, ok := sim.IssueRecord(job); !ok || rec.State != contractsim.IssueVoid {
			t.Fatalf("the check left %+v", rec)
		}
	}
}

// holding is a product that issues and cannot cancel (mute), with its confirmations and voids counted.
type holding struct {
	mute
	mu        sync.Mutex
	confirmed []string
	voided    int
}

func (h *holding) ConfirmIssue(ctx context.Context, sc r.SourceContext, c r.IssueConfirmation) (r.IssueReceipt, error) {
	rc, err := h.mute.ConfirmIssue(ctx, sc, c)
	if err == nil {
		h.mu.Lock()
		h.confirmed = append(h.confirmed, c.JobID)
		h.mu.Unlock()
	}
	return rc, err
}
func (h *holding) VoidIssue(ctx context.Context, sc r.SourceContext, jobID, reason string) error {
	err := h.mute.VoidIssue(ctx, sc, jobID, reason)
	if err == nil {
		h.mu.Lock()
		h.voided++
		h.mu.Unlock()
	}
	return err
}
func (h *holding) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.confirmed), h.voided
}
func (h *holding) confirmedJobs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.confirmed...)
}

// The directory rules (S19-B) fail a product that declares the capability without implementing it, one whose list does not verify, and one
// whose list names a company its reads refuse; each in the adapter's fixed words, without a byte of the answer.
func TestABrokenDirectoryFailsTheDirectoryRules(t *testing.T) {
	code, out := check(t, append(files(t, source(t, muted(), sourcetest.Options{Token: token})), "-companies"))
	if code != 1 || !strings.Contains(out, "FAIL D1 ") || !strings.Contains(out, "the source refused with unsupported") || !strings.Contains(out, "SKIP D2 ") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	code, out = check(t, append(files(t, source(t, repetitive{granted()}, sourcetest.Options{Token: token})), "-companies"))
	if code != 1 || !strings.Contains(out, "FAIL D1 ") || !strings.Contains(out, "an id repeats") || strings.Contains(out, "又一次") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	code, out = check(t, append(files(t, source(t, boastful{granted()}, sourcetest.Options{Token: token})), "-companies"))
	if code != 1 || !strings.Contains(out, "PASS D1 ") || !strings.Contains(out, "FAIL D2 ") || !strings.Contains(out, "a read naming the listed companies was refused") || strings.Contains(out, "company_other") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, id := range []string{"C1", "R1", "R12", "R13"} {
		if !strings.Contains(out, "PASS "+id+" ") {
			t.Fatalf("%s did not pass:\n%s", id, out)
		}
	}
}

func TestABrokenSourceFailsTheRulesItBreaks(t *testing.T) {
	// No credential check and an authorization that says yes to everything.
	srv := source(t, lax{granted()}, sourcetest.Options{})
	code, out := check(t, files(t, srv))
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, id := range []string{"A1", "R7", "R8", "R9", "R10"} {
		if !strings.Contains(out, "FAIL "+id+" ") {
			t.Fatalf("%s did not fail:\n%s", id, out)
		}
	}
	for _, id := range []string{"C1", "R1", "R3", "R11", "R12"} {
		if !strings.Contains(out, "PASS "+id+" ") {
			t.Fatalf("%s did not pass:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "FAIL A1 a request without the service credential is refused with 401: the source answered status 200\n") {
		t.Fatal(out)
	}
	// A source that cannot be reached fails at the catalog, and says so in fixed words.
	srv.Close()
	code, out = check(t, files(t, srv))
	if code != 1 || !strings.Contains(out, "FAIL C1 ") || !strings.Contains(out, "the source did not answer in time or could not be reached") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// A source that ignores the filters and the sort of a query fails every rule of the declaration (S20-A): the rows it answers do not
	// satisfy the filter, the order is not the one asked, and what its catalog does not declare is accepted.
	code, out = check(t, files(t, source(t, &deaf{Simulator: granted(), plain: map[string]string{}}, sourcetest.Options{Token: token})))
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, id := range []string{"F1", "F2", "F3", "F4", "F5", "F6"} {
		if !strings.Contains(out, "FAIL "+id+" ") {
			t.Fatalf("%s did not fail:\n%s", id, out)
		}
	}
	for _, id := range []string{"C1", "C2", "R1", "R3", "R4"} {
		if !strings.Contains(out, "PASS "+id+" ") {
			t.Fatalf("%s did not pass:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "FAIL F1 "+"every declared filter, with each of its operators, narrows the rows to those that satisfy it (rows of the whole scope; totals agree where they are sums): filter customer.id eq: a row does not satisfy the filter: INTEGRITY_FAILURE\n") {
		t.Fatal(out)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	srv := source(t, granted(), sourcetest.Options{Token: token})
	args := files(t, srv)
	for name, a := range map[string][]string{
		"no arguments":          nil,
		"an unset credential":   {"-base-url", srv.URL, "-auth-env", "UNSET", "-context", args[7], "-query", args[9]},
		"a context that is not": {"-base-url", srv.URL, "-auth-env", "CHECK_AUTH", "-context", args[9], "-query", args[9]},
		"an unknown flag":       {"-token", token},
		// A source cancels the documents it issues (S24-A): the cancellation is checked with the document authority or not at all.
		"a cancellation without the document authority": append(append([]string{}, args...), "-cancellation"),
		"a cancellation with the directory alone":       append(append([]string{}, args...), "-companies", "-cancellation"),
	} {
		if code, out := check(t, a); code != 2 || out != "" {
			t.Fatalf("%s: exit %d %s", name, code, out)
		}
	}
	// A connection the platform would refuse is the first rule's failure.
	code, out := check(t, []string{"-base-url", "http://example.test", "-auth-env", "CHECK_AUTH", "-context", args[7], "-query", args[9]})
	if code != 1 || out != "FAIL P1 the connection is one the platform accepts (https or loopback http, a credential, the bounds): httpsource: base_url must be https://, or http:// to a loopback IP address with loopback_http (the value is not shown)\nreporting-source-check: 1 of 1 rules failed\n" {
		t.Fatalf("exit %d %q", code, out)
	}
}
