package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
)

const (
	k1 = "K1 the read vouches for a scope: scope_expires_at is after expires_at"
	k2 = "K2 the read is authorized as the source gave it, and refused with another scope expiry or without one (denied or expired)"
)

// vouching is the reference product vouching for the scope of its reads for a week after their preparation (ADR 0026).
func vouching() *contractsim.Simulator {
	s := granted()
	s.SetScopeLifetime(7 * 24 * time.Hour)
	return s
}

// nextNumber is the number the product gives the next document it reserves: what a check left behind in its sequence.
func nextNumber(t *testing.T, sim *contractsim.Simulator) string {
	t.Helper()
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
	res, err := sim.PrepareDocument(context.Background(), sc, r.IssueIntent{JobID: r.NewID(), Prepared: p, RowSHA256: r.Digest(page.Rows[0]), TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return res.Number
}

// The reference source that vouches passes every rule with -kept-results (S25-B2, ADR 0026 decision 2), the two new ones last, and
// nothing is skipped when every flag is given. Without the flag its reads do not verify, as through a connection that does not
// declare kept results: the check ends at R1, in the adapter's words. With the flag, a source that vouches for nothing fails K1: the
// flag says that it does.
func TestASourceThatVouchesIsCheckedWithKeptResults(t *testing.T) {
	sim := vouching()
	code, out := check(t, append(files(t, source(t, sim, sourcetest.Options{Token: token})), "-issue", "-companies", "-cancellation", "-kept-results"))
	if code != 0 || strings.Contains(out, "SKIP ") || strings.Contains(out, "FAIL ") || !strings.HasSuffix(out, "PASS "+k1+"\nPASS "+k2+"\n"+everyRulePassed) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if order := regexp.MustCompile(`(?m)^(?:PASS|SKIP|FAIL) ([A-Z]+\d+) `).FindAllStringSubmatch(out, -1); len(order) != rules || order[31][1] != "X4" || order[32][1] != "K1" || order[33][1] != "K2" {
		t.Fatal(out)
	}
	// The two rules only read: with the document authority at hand (-issue), the product has given out the four numbers the other
	// rules reserve (I1's, the cancelled document's, X2's and X4's, as without the flag) and no more.
	if next := nextNumber(t, sim); next != "AR-000005" {
		t.Fatal("the next number after a check with every flag:", next)
	}
	// The flag alone, as a product that issues nothing would run it.
	code, out = check(t, append(files(t, source(t, vouching(), sourcetest.Options{Token: token})), "-kept-results"))
	if code != 0 || !strings.Contains(out, "PASS "+k1+"\n") || !strings.Contains(out, "PASS "+k2+"\n") || !strings.Contains(out, "SKIP I1 ") || !strings.HasSuffix(out, everyRulePassed) {
		t.Fatalf("exit %d\n%s", code, out)
	}

	// Without the flag a read that vouches does not verify, and the check says why and stops: seven rules, the read's the last.
	code, out = check(t, files(t, source(t, vouching(), sourcetest.Options{Token: token})))
	if code != 1 || !strings.Contains(out, "FAIL R1 a read is prepared for the query and verifies (hash, dataset, companies, zone, policy, watermark, expiry within 15 minutes): http source reads: the read vouches for a scope beyond its expiry and the connection does not declare kept_results: INTEGRITY_FAILURE\n") ||
		!strings.HasSuffix(out, "reporting-source-check: 1 of 7 rules failed\n") || strings.Contains(out, " R2 ") || strings.Contains(out, " K1 ") {
		t.Fatalf("exit %d\n%s", code, out)
	}

	// With the flag, a source that vouches for nothing fails K1 and K2 is skipped; everything else is as without the flag.
	code, out = check(t, append(files(t, source(t, granted(), sourcetest.Options{Token: token})), "-kept-results"))
	if code != 1 || !strings.Contains(out, "FAIL "+k1+": the read declares no scope beyond its expiry: UNSUPPORTED_COMBINATION\nSKIP "+k2+": the read vouches for no scope (K1)\n") ||
		!strings.HasSuffix(out, "reporting-source-check: 1 of "+strconv.Itoa(rules)+" rules failed\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, id := range []string{"P1", "A1", "C1", "R1", "R2", "R3", "R7", "R8", "R13", "F1", "F6"} {
		if !strings.Contains(out, "PASS "+id+" ") {
			t.Fatalf("%s did not pass:\n%s", id, out)
		}
	}
}

// level is a product that writes the key and vouches for nothing: every read's scope ends with the read. It holds a read that comes
// back to what it wrote: without that scope, or with another, the read is not its own.
type level struct{ *contractsim.Simulator }

func (p level) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, hash string) (r.PreparedRead, error) {
	read, err := p.Simulator.PrepareRead(ctx, sc, q, hash)
	if err == nil {
		read.ScopeExpiresAt = read.ExpiresAt
	}
	return read, err
}

// own is the read as the simulator behind the product made it, when the read came back with the scope the product wrote on it.
func (p level) own(read r.PreparedRead) (r.PreparedRead, bool) {
	if read.ScopeExpiresAt.IsZero() || !read.ScopeExpiresAt.Equal(read.ExpiresAt) {
		return read, false
	}
	read.ScopeExpiresAt = time.Time{}
	return read, true
}
func (p level) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, read r.PreparedRead) error {
	own, ok := p.own(read)
	if !ok {
		return r.ErrDenied
	}
	return p.Simulator.AuthorizeCurrent(ctx, sc, own)
}
func (p level) ReadPage(ctx context.Context, sc r.SourceContext, read r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	own, ok := p.own(read)
	if !ok {
		return r.SourcePage{}, r.ErrDenied
	}
	return p.Simulator.ReadPage(ctx, sc, own, cursor, limit)
}

// A scope that ends with the read vouches for nothing. Such a read is taken from any connection and goes back to the product with
// the key it wrote, on every request: without the flag the product passes every rule (a platform that dropped the key on the way
// back would have its read refused at R2). With the flag it fails K1, as a product that says nothing does: the flag says that the
// product vouches, and K2 has nothing to ask.
func TestAScopeThatEndsWithTheReadVouchesForNothing(t *testing.T) {
	code, out := check(t, files(t, source(t, level{granted()}, sourcetest.Options{Token: token})))
	if code != 0 || !strings.HasSuffix(out, everyRulePassed) || !strings.Contains(out, "SKIP "+k1+": ") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, id := range []string{"R1", "R2", "R3", "R4", "R7", "R8", "R9", "R10", "F1", "F2"} {
		if !strings.Contains(out, "PASS "+id+" ") {
			t.Fatalf("%s did not pass:\n%s", id, out)
		}
	}
	code, out = check(t, append(files(t, source(t, level{granted()}, sourcetest.Options{Token: token})), "-kept-results"))
	if code != 1 || !strings.Contains(out, "FAIL "+k1+": the read declares no scope beyond its expiry: UNSUPPORTED_COMBINATION\nSKIP "+k2+": the read vouches for no scope (K1)\n") ||
		!strings.HasSuffix(out, "reporting-source-check: 1 of "+strconv.Itoa(rules)+" rules failed\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// lastSecond is the last time the protocol's times can name: what a product that vouches "for ever" answers as its scope.
var lastSecond = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// eternal is a product that vouches for ever: every read's scope is lastSecond, which a product whose handle describes itself, and
// costs it nothing to keep, is likely to answer. A read that comes back with that scope is the product's own; with another, or
// with none, it is not, and the simulator behind the product refuses it. With trusting set the product is broken: it takes whatever
// scope a read comes back with for its own, as long as there is one, and so would authorize for as long as the sender says. With
// over set as well, the read itself has run out for such a product after that many asks of its authorization: from then on it
// believes that a scope which does not go beyond the read's expiry has ended, and refuses it, as it would by its clock.
type eternal struct {
	*contractsim.Simulator
	trusting bool
	over     int
	mu       sync.Mutex
	asks     int
	given    map[string]time.Time // the read's reference → the scope the simulator behind the product gave it
}

func (p *eternal) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, hash string) (r.PreparedRead, error) {
	read, err := p.Simulator.PrepareRead(ctx, sc, q, hash)
	if err == nil {
		p.mu.Lock()
		p.given[read.Ref] = read.ScopeExpiresAt
		p.mu.Unlock()
		read.ScopeExpiresAt = lastSecond
	}
	return read, err
}

// own is the read as the simulator behind the product is asked about it.
func (p *eternal) own(read r.PreparedRead) r.PreparedRead {
	p.mu.Lock()
	given, ok := p.given[read.Ref]
	p.mu.Unlock()
	if ok && (read.ScopeExpiresAt.Equal(lastSecond) || p.trusting && !read.ScopeExpiresAt.IsZero()) {
		read.ScopeExpiresAt = given
	}
	return read
}
func (p *eternal) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, read r.PreparedRead) error {
	p.mu.Lock()
	p.asks++
	ranOut := p.trusting && p.over > 0 && p.asks > p.over
	p.mu.Unlock()
	if ranOut && !read.ScopeExpiresAt.After(read.ExpiresAt) {
		return r.ErrDenied
	}
	return p.Simulator.AuthorizeCurrent(ctx, sc, p.own(read))
}
func (p *eternal) ReadPage(ctx context.Context, sc r.SourceContext, read r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	return p.Simulator.ReadPage(ctx, sc, p.own(read), cursor, limit)
}

// A scope at the last second the protocol's times can name has no later time to be forged into: K2 asks with an earlier one, which
// lies as far ahead of the clock, and a correct product that vouches for ever passes. One that takes the scope from the request
// says yes to that earlier time and fails, in the rule's words for a read with another scope expiry; and it fails so also when
// its read has run out by the time K2 asks (the five asks of R2 and R7 to R10 are before it). Asked with the read's own expiry
// for the other scope, that product would have refused, by its clock, and passed.
func TestAProductThatVouchesForEverIsAskedWithAnEarlierScope(t *testing.T) {
	code, out := check(t, append(files(t, source(t, &eternal{Simulator: vouching(), given: map[string]time.Time{}}, sourcetest.Options{Token: token})), "-kept-results"))
	if code != 0 || !strings.Contains(out, "PASS "+k1+"\nPASS "+k2+"\n") || !strings.HasSuffix(out, everyRulePassed) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for name, product := range map[string]*eternal{
		"takes the scope from the request":                             {Simulator: vouching(), trusting: true, given: map[string]time.Time{}},
		"takes the scope from the request, and whose read has run out": {Simulator: vouching(), trusting: true, over: 5, given: map[string]time.Time{}},
	} {
		code, out = check(t, append(files(t, source(t, product, sourcetest.Options{Token: token})), "-kept-results"))
		if code != 1 || !strings.Contains(out, "PASS "+k1+"\nFAIL "+k2+": the read with another scope expiry: the source accepted it; want AUTH_SCOPE_DENIED\n") || !strings.HasSuffix(out, "reporting-source-check: 1 of "+strconv.Itoa(rules)+" rules failed\n") {
			t.Fatalf("a product that %s: exit %d\n%s", name, code, out)
		}
	}
}

// credulous is a broken product that does not hold the end of a read's scope to its own record: whatever scope expiry the read it made
// comes back with, and without one, it decides as if the read had come back as it gave it. When always is false it does so only for a
// read that comes back without a scope expiry.
type credulous struct {
	*contractsim.Simulator
	always bool
	mu     sync.Mutex
	given  map[string]time.Time // the read's reference → the scope expiry the product gave it
}

func (p *credulous) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, hash string) (r.PreparedRead, error) {
	read, err := p.Simulator.PrepareRead(ctx, sc, q, hash)
	if err == nil {
		p.mu.Lock()
		p.given[read.Ref] = read.ScopeExpiresAt
		p.mu.Unlock()
	}
	return read, err
}
func (p *credulous) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, read r.PreparedRead) error {
	p.mu.Lock()
	given, ok := p.given[read.Ref]
	p.mu.Unlock()
	if ok && (p.always || read.ScopeExpiresAt.IsZero()) {
		read.ScopeExpiresAt = given
	}
	return p.Simulator.AuthorizeCurrent(ctx, sc, read)
}

// fading is a product whose read runs out during the check: it answers the first asks of a read's authorization as the reference
// product does and refuses every later one, whatever the read says.
type fading struct {
	*contractsim.Simulator
	mu    sync.Mutex
	asks  int
	limit int
}

func (p *fading) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, read r.PreparedRead) error {
	p.mu.Lock()
	p.asks++
	over := p.asks > p.limit
	p.mu.Unlock()
	if over {
		return r.ErrDenied
	}
	return p.Simulator.AuthorizeCurrent(ctx, sc, read)
}

// K2's two refusals prove something only while the read itself is authorized: a read that has run out is refused whatever scope it
// carries. So the rule asks for the read as the source gave it before the two altered reads and again after them, and fails,
// saying which, when either is refused; it does not pass on refusals that the read's own end explains. The product here answers
// five asks before K2 (R2 and the four forged reads of R7 to R10); K2's own four are the sixth to the ninth. A read that runs out
// before the first of them fails the rule there; one that runs out after it, wherever, fails it at the last; and one that lasts
// through all four passes.
func TestTheKeptResultsRulesDoNotPassOnAReadThatHasRunOut(t *testing.T) {
	const (
		first = "the read as the source gave it was refused: http source authorize: the source refused with denied: AUTH_SCOPE_DENIED (request "
		last  = "the read as the source gave it was refused after the two that were not: their refusals show nothing: http source authorize: the source refused with denied: AUTH_SCOPE_DENIED (request "
	)
	for limit, why := range map[int]string{5: first, 6: last, 7: last, 8: last} {
		code, out := check(t, append(files(t, source(t, &fading{Simulator: vouching(), limit: limit}, sourcetest.Options{Token: token})), "-kept-results"))
		if code != 1 || !strings.Contains(out, "PASS "+k1+"\nFAIL "+k2+": "+why) || !strings.HasSuffix(out, "reporting-source-check: 1 of "+strconv.Itoa(rules)+" rules failed\n") {
			t.Fatalf("a read that runs out after %d asks: exit %d\n%s", limit, code, out)
		}
		for _, id := range []string{"R2", "R7", "R8", "R9", "R10"} {
			if !strings.Contains(out, "PASS "+id+" ") {
				t.Fatalf("a read that runs out after %d asks: %s did not pass:\n%s", limit, id, out)
			}
		}
	}
	code, out := check(t, append(files(t, source(t, &fading{Simulator: vouching(), limit: 9}, sourcetest.Options{Token: token})), "-kept-results"))
	if code != 0 || !strings.Contains(out, "PASS "+k2+"\n") {
		t.Fatalf("a read that lasts through the rule: exit %d\n%s", code, out)
	}
}

// The kept-results rules fail a product that does not hold the scope's end itself (K2), in fixed words: one that authorizes its read
// with whatever scope expiry it comes back with, and one that refuses another time and still authorizes the read when the time is
// gone. Such a product would authorize a read for as long as whoever sends it says. Everything else passes: the read is the
// product's own in every other field, and the refusals of R7 to R10 still refuse.
func TestASourceThatTakesTheScopeFromTheRequestFailsTheKeptResultsRules(t *testing.T) {
	for _, c := range []struct {
		name    string
		product *credulous
		why     string
	}{
		{"authorizes its read with any scope expiry", &credulous{Simulator: vouching(), always: true, given: map[string]time.Time{}}, "the read with another scope expiry"},
		{"authorizes its read without its scope expiry", &credulous{Simulator: vouching(), given: map[string]time.Time{}}, "the read without its scope expiry"},
	} {
		code, out := check(t, append(files(t, source(t, c.product, sourcetest.Options{Token: token})), "-kept-results"))
		if code != 1 || !strings.Contains(out, "PASS "+k1+"\nFAIL "+k2+": "+c.why+": the source accepted it; want AUTH_SCOPE_DENIED\n") || !strings.HasSuffix(out, "reporting-source-check: 1 of "+strconv.Itoa(rules)+" rules failed\n") {
			t.Fatalf("a product that %s: exit %d\n%s", c.name, code, out)
		}
		for _, id := range []string{"C1", "R1", "R2", "R3", "R7", "R8", "R9", "R10", "F1"} {
			if !strings.Contains(out, "PASS "+id+" ") {
				t.Fatalf("a product that %s: %s did not pass:\n%s", c.name, id, out)
			}
		}
	}
}
