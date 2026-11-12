package contractsim_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/internal/authoritytest"
	r "idp.local/reporting/reporting"
)

// The simulator's own lifetimes (ADR 0026 decision 2, docs/S25A_SCOPE.md A8), asked of the simulator itself and not through the service,
// which refuses first in most of these cases: pages and the check of an issue's row while the read lasts, the authorization of the read's
// scope while the scope lasts, by the grant the read was frozen on, and nothing after it.
func TestTheSimulatorServesPagesWhileTheReadLastsAndAuthorizesWhileTheScopeDoes(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 11, 7, 30, 0, 0, time.UTC)
	now := start
	s := contractsim.Financial()
	s.SetClock(func() time.Time { return now })
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sc := r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	s.Grant(owner, contractsim.DefaultGrant())
	q := r.Normalize(contractsim.FinancialQuery())
	prepare := func() r.PreparedRead {
		t.Helper()
		read, e := s.PrepareRead(ctx, sc, q, r.QueryHash(q))
		if e != nil {
			t.Fatal(e)
		}
		return read
	}
	authorized := func(read r.PreparedRead) error { return s.AuthorizeCurrent(ctx, sc, read) }
	pages := func(read r.PreparedRead) error { _, e := s.ReadPage(ctx, sc, read, "", 50); return e }

	// Without a declaration a read lasts ten minutes and says no scope; its pages can be made shorter, and a scope that is not longer
	// than the read declares nothing.
	if read := prepare(); !read.ExpiresAt.Equal(start.Add(10*time.Minute)) || !read.ScopeExpiresAt.IsZero() {
		t.Fatalf("a read without a declaration: %+v", read)
	}
	s.SetReadLifetime(2 * time.Minute)
	if read := prepare(); !read.ExpiresAt.Equal(start.Add(2*time.Minute)) || !read.ScopeExpiresAt.IsZero() {
		t.Fatalf("a shortened read: %+v", read)
	}
	s.SetReadLifetime(0)
	s.SetScopeLifetime(10 * time.Minute)
	if read := prepare(); !read.ScopeExpiresAt.IsZero() {
		t.Fatalf("a scope as long as the read was declared: %+v", read)
	}

	// With a scope of three hours the read says so, and while the read lasts everything is served.
	s.SetScopeLifetime(3 * time.Hour)
	read := prepare()
	if !read.ExpiresAt.Equal(start.Add(10*time.Minute)) || !read.ScopeExpiresAt.Equal(start.Add(3*time.Hour)) {
		t.Fatalf("a vouched read: %+v", read)
	}
	page, e := s.ReadPage(ctx, sc, read, "", 50)
	if e != nil || len(page.Rows) == 0 {
		t.Fatal(page, e)
	}
	intent := func(job int) r.IssueIntent {
		return r.IssueIntent{JobID: authoritytest.Job(job), Prepared: read, RowIndex: 0, RowSHA256: r.Digest(page.Rows[0]),
			TemplateID: strings.Repeat("b", 32), TemplateVersion: 1, TemplateSHA256: strings.Repeat("c", 64)}
	}
	if _, e := s.PrepareDocument(ctx, sc, intent(1)); e != nil {
		t.Fatal("a document is not prepared while the read lasts:", e)
	}
	if e := authorized(read); e != nil {
		t.Fatal(e)
	}

	// The read is over and the scope goes on: no page, no document (the row can no longer be checked against the rows the read froze),
	// not for the job that already holds a reservation either; the authorization still answers.
	now = start.Add(11 * time.Minute)
	if e := pages(read); !errors.Is(e, r.ErrDenied) {
		t.Fatal("a page after the read:", e)
	}
	for _, job := range []int{1, 2} {
		if _, e := s.PrepareDocument(ctx, sc, intent(job)); !errors.Is(e, r.ErrDenied) {
			t.Fatalf("a document for job %d after the read: %v", job, e)
		}
	}
	if e := authorized(read); e != nil {
		t.Fatal("the scope does not answer after the read:", e)
	}
	// It answers by the current rights against the grant the read was frozen on: unavailable, revoked, narrowed, restored.
	s.SetUnavailable(true)
	if e := authorized(read); !errors.Is(e, r.ErrUnavailable) {
		t.Fatal(e)
	}
	s.SetUnavailable(false)
	s.Revoke(owner)
	if e := authorized(read); !errors.Is(e, r.ErrDenied) {
		t.Fatal("after a revocation:", e)
	}
	narrowed := contractsim.DefaultGrant()
	narrowed.Rows = []string{"r1", "r2"}
	s.Grant(owner, narrowed)
	if e := authorized(read); !errors.Is(e, r.ErrDenied) {
		t.Fatal("under narrowed rights:", e)
	}
	s.Grant(owner, contractsim.DefaultGrant())
	if e := authorized(read); e != nil {
		t.Fatal("under the rights the read was frozen on:", e)
	}
	// A read that says another scope than the one the simulator gave is not the simulator's read.
	longer := read
	longer.ScopeExpiresAt = read.ScopeExpiresAt.Add(time.Hour)
	if e := authorized(longer); !errors.Is(e, r.ErrDenied) {
		t.Fatal("a read with a scope the simulator did not give:", e)
	}
	// So far the simulator remembers the four reads it prepared, each with the data version it froze. Preparing another forgets the three
	// that declared no scope and are over, and lets go of the data version of the one whose pages are over and whose scope goes on: two
	// reads are remembered, and one data version, the new read's. The scope still answers, the pages still do not.
	if reads, versions := contractsim.HeldForTests(s); reads != 4 || versions != 4 {
		t.Fatalf("before another read is prepared: %d reads, %d data versions", reads, versions)
	}
	later := prepare()
	if reads, versions := contractsim.HeldForTests(s); reads != 2 || versions != 1 {
		t.Fatalf("after another read is prepared: %d reads, %d data versions", reads, versions)
	}
	if e := authorized(read); e != nil {
		t.Fatal(e)
	}
	if e := pages(read); !errors.Is(e, r.ErrDenied) {
		t.Fatal(e)
	}
	if e := pages(later); e != nil {
		t.Fatal("the pages of the read prepared last:", e)
	}

	// At the scope's end the simulator answers no more, before it has forgotten the read and after. The read prepared eleven minutes
	// later is still within its scope: it is remembered, without its data version.
	now = start.Add(3 * time.Hour)
	if e := authorized(read); !errors.Is(e, r.ErrDenied) {
		t.Fatal("at the scope's end:", e)
	}
	prepare()
	if e := authorized(read); !errors.Is(e, r.ErrDenied) {
		t.Fatal("after the scope, forgotten:", e)
	}
	if reads, versions := contractsim.HeldForTests(s); reads != 2 || versions != 1 {
		t.Fatalf("after the first scope: %d reads, %d data versions", reads, versions)
	}
	if e := authorized(later); e != nil {
		t.Fatal("the read whose scope goes on:", e)
	}
}
