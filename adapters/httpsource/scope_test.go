package httpsource_test

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

// The scope a source vouches for (S25-B2, ADR 0026 decision 2), through the adapter of a connection that declares kept results.

func keeps(c *httpsource.Config) { c.KeptResults = true }

// wireWatch is the reference source in front of a simulator, with the read of every request and of every /reads answer remembered as
// the JSON object it was on the wire.
type wireWatch struct {
	sim      *contractsim.Simulator
	srv      *httptest.Server
	sc       r.SourceContext
	mu       sync.Mutex
	answered map[string]any            // the read of the last /reads answer
	sent     map[string]map[string]any // the last segment of a route → the read its last request carried
	count    int                       // the requests the source received
}

func watchWire(t *testing.T) *wireWatch {
	t.Helper()
	o := &wireWatch{sim: contractsim.Financial(), sent: map[string]map[string]any{}}
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	grant := contractsim.DefaultGrant()
	grant.IssueDocuments = true
	o.sim.Grant(owner, grant)
	o.sc = r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	ref := sourcetest.New(o.sim, sourcetest.Options{Token: testToken})
	o.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var in struct {
			Read   map[string]any `json:"read"`
			Intent struct {
				Read map[string]any `json:"read"`
			} `json:"intent"`
		}
		_ = json.Unmarshal(body, &in)
		route := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		o.mu.Lock()
		o.count++
		if in.Read != nil {
			o.sent[route] = in.Read
		}
		if in.Intent.Read != nil {
			o.sent[route] = in.Intent.Read
		}
		o.mu.Unlock()
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		ref.ServeHTTP(rec, req)
		if route == "reads" && rec.Code == 200 {
			var out struct {
				Read map[string]any `json:"read"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			o.mu.Lock()
			o.answered = out.Read
			o.mu.Unlock()
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *wireWatch) answer() map[string]any { o.mu.Lock(); defer o.mu.Unlock(); return o.answered }
func (o *wireWatch) requests() int          { o.mu.Lock(); defer o.mu.Unlock(); return o.count }
func (o *wireWatch) request(route string) map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sent[route]
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The nine keys a read had before the scope existed, and the ten of a read that vouches for one.
var (
	readKeys    = []string{"company_ids", "currency_policy", "dataset", "expires_at", "query_hash", "ref", "scope_handle", "timezone", "watermark"}
	vouchedKeys = []string{"company_ids", "currency_policy", "dataset", "expires_at", "query_hash", "ref", "scope_expires_at", "scope_handle", "timezone", "watermark"}
)

// A read that vouches for a scope carries it: the adapter gives the service the time the source said, and every later request about
// the read (its authorization, its pages, a document from it) sends the read back as the source answered it, key for key. A read
// that vouches for nothing has the nine keys it always had, in the answer and in every request.
func TestAReadCarriesTheScopeTheSourceVouchesForAndComesBackUnchanged(t *testing.T) {
	o := watchWire(t)
	a := adapterOf(t, o.srv, keeps)
	q, hash := query()
	use := func(p r.PreparedRead) {
		t.Helper()
		if err := a.AuthorizeCurrent(ctx, o.sc, p); err != nil {
			t.Fatal(err)
		}
		page, err := a.ReadPage(ctx, o.sc, p, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		in := r.IssueIntent{JobID: r.NewID(), Prepared: p, RowIndex: 0, RowSHA256: r.Digest(page.Rows[0]), TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: strings.Repeat("0", 64)}
		if _, err = a.(r.DocumentAuthority).PrepareDocument(ctx, o.sc, in); err != nil {
			t.Fatal(err)
		}
		for _, route := range []string{"authorize", "pages", "prepare"} {
			if got := o.request(route); !reflect.DeepEqual(got, o.answer()) {
				t.Fatalf("%s sent the read back as %v, the source answered %v", route, got, o.answer())
			}
		}
	}

	plain, err := a.PrepareRead(ctx, o.sc, q, hash)
	if err != nil || !plain.ScopeExpiresAt.IsZero() || !plain.ScopeEnd().Equal(plain.ExpiresAt) {
		t.Fatalf("%+v %v", plain, err)
	}
	if got := keys(o.answer()); !reflect.DeepEqual(got, readKeys) {
		t.Fatalf("a read that vouches for nothing has the keys %v", got)
	}
	use(plain)

	o.sim.SetScopeLifetime(72 * time.Hour)
	vouched, err := a.PrepareRead(ctx, o.sc, q, hash)
	if err != nil || vouched.ScopeExpiresAt.Sub(vouched.ExpiresAt) != 72*time.Hour-contractsim.DefaultReadLifetime {
		t.Fatalf("%+v %v", vouched, err)
	}
	said, _ := o.answer()["scope_expires_at"].(string)
	if at, ok := protocol.ParseTime(said); !ok || !at.Equal(vouched.ScopeExpiresAt) || protocol.FormatTime(vouched.ScopeExpiresAt) != said {
		t.Fatalf("the source said %q, the adapter gives %s", said, vouched.ScopeExpiresAt)
	}
	if got := keys(o.answer()); !reflect.DeepEqual(got, vouchedKeys) {
		t.Fatalf("a read that vouches has the keys %v", got)
	}
	use(vouched)
	// The source decides by its own record: the same read with another scope expiry, or without one, is not the read it made.
	later := vouched
	later.ScopeExpiresAt = vouched.ScopeExpiresAt.Add(time.Hour)
	want(t, a.AuthorizeCurrent(ctx, o.sc, later), r.ErrDenied, "a read with a later scope expiry")
	stripped := vouched
	stripped.ScopeExpiresAt = time.Time{}
	want(t, a.AuthorizeCurrent(ctx, o.sc, stripped), r.ErrDenied, "a read without its scope expiry")
	if o.request("authorize")["scope_expires_at"] != nil {
		t.Fatal("a read without a scope was sent with the key")
	}
	// A read whose scope is not well formed is never sent: the adapter refuses it as it refuses a malformed reference. Each of the
	// three things a scope must be is asked on each of the three routes: not before the read's expiry, within the years the
	// protocol's times name, and a time that can be written (an in-process caller could hand over one that cannot).
	before := o.requests()
	backwards, beyond, unwritable := vouched, vouched, vouched
	backwards.ScopeExpiresAt = vouched.ExpiresAt.Add(-time.Second)
	beyond.ScopeExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	unwritable.ScopeExpiresAt = vouched.ScopeExpiresAt.In(time.FixedZone("a day away", 24*3600))
	for name, malformed := range map[string]r.PreparedRead{"a scope that ends before its read": backwards, "a scope after the year 9999": beyond, "a scope in an offset of a day": unwritable} {
		want(t, a.AuthorizeCurrent(ctx, o.sc, malformed), r.ErrIntegrity, name)
		_, err = a.ReadPage(ctx, o.sc, malformed, "", 50)
		want(t, err, r.ErrIntegrity, "a page of a read with "+name)
		_, err = a.(r.DocumentAuthority).PrepareDocument(ctx, o.sc, r.IssueIntent{JobID: r.NewID(), Prepared: malformed, RowSHA256: strings.Repeat("0", 64), TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: strings.Repeat("0", 64)})
		want(t, err, r.ErrIntegrity, "a document from a read with "+name)
	}
	if n := o.requests() - before; n != 0 {
		t.Fatalf("a read with a malformed scope reached the source: %d requests", n)
	}
}

// What a source may answer as a scope, through a connection that declares kept results: a canonical time at or after the read's own
// expiry, in any offset RFC 3339 has. Anything else is an integrity failure, and nothing of the read is passed on. The read's own
// expiry with an offset of a whole day is among them: it passed before S25-B2 and failed, untyped, at the first job's admission.
func TestAScopeThatDoesNotVerifyIsAnIntegrityFailure(t *testing.T) {
	q, hash := query()
	read := func(m map[string]any) map[string]any { return m["read"].(map[string]any) }
	expiry := func(m map[string]any) time.Time {
		at, ok := protocol.ParseTime(read(m)["expires_at"].(string))
		if !ok {
			panic("the reference source's expiry is not canonical")
		}
		return at
	}
	// The same instant written with an offset of a whole day, which Go's parser reads back.
	dayOffset := func(at time.Time) string {
		return at.UTC().Add(24*time.Hour).Format("2006-01-02T15:04:05.999999999") + "+24:00"
	}
	// Why each is refused, in the adapter's fixed words: the answer is not the protocol's JSON, the read is not of its form (a time
	// that is not one), or the scope is a time the platform does not take. The words are what a product team reads at R1 of the
	// checker.
	const (
		notJSON = "http source reads: the answer is not one strict JSON object of the protocol's shape: INTEGRITY_FAILURE"
		notForm = "http source reads: the read is not of the protocol's form: INTEGRITY_FAILURE"
		notTime = "http source reads: the read's scope ends before the read does, or after the year 9999: INTEGRITY_FAILURE"
	)
	refused := []struct {
		name string
		f    func(m map[string]any)
		why  string
	}{
		{"a scope before the read's expiry", func(m map[string]any) {
			read(m)["scope_expires_at"] = protocol.FormatTime(expiry(m).Add(-time.Nanosecond))
		}, notTime},
		{"a scope with trailing zeros", func(m map[string]any) {
			read(m)["scope_expires_at"] = expiry(m).Add(time.Hour).UTC().Format("2006-01-02T15:04:05") + ".000Z"
		}, notForm},
		{"a scope that is an empty text", func(m map[string]any) { read(m)["scope_expires_at"] = "" }, notForm},
		{"a scope that is null", func(m map[string]any) { read(m)["scope_expires_at"] = nil }, notJSON},
		{"a scope that is a number", func(m map[string]any) { read(m)["scope_expires_at"] = expiry(m).Add(time.Hour).Unix() }, notJSON},
		{"a scope that is the zero time", func(m map[string]any) { read(m)["scope_expires_at"] = "0001-01-01T00:00:00Z" }, notForm},
		{"a scope after the year 9999", func(m map[string]any) { read(m)["scope_expires_at"] = "9999-12-31T23:59:59-00:01" }, notTime},
		{"a scope with an offset of a whole day", func(m map[string]any) { read(m)["scope_expires_at"] = dayOffset(expiry(m).Add(time.Hour)) }, notForm},
		{"a scope spelled in another case", func(m map[string]any) {
			read(m)["Scope_Expires_At"] = protocol.FormatTime(expiry(m).Add(time.Hour))
		}, notJSON},
		{"a scope beside the read", func(m map[string]any) { m["scope_expires_at"] = protocol.FormatTime(expiry(m).Add(time.Hour)) }, notJSON},
		{"an expiry with an offset of a whole day", func(m map[string]any) { read(m)["expires_at"] = dayOffset(expiry(m)) }, notForm},
	}
	for _, c := range refused {
		g := newRig(t, keeps)
		g.set(edit("/reads", c.f))
		p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
		want(t, err, r.ErrIntegrity, c.name)
		noLeak(t, err, g.srv)
		if !strings.HasPrefix(err.Error(), c.why) {
			t.Fatalf("%s is refused as %q, want %q", c.name, err, c.why)
		}
		if !reflect.DeepEqual(p, r.PreparedRead{}) {
			t.Fatalf("%s: something of the read was passed on: %+v", c.name, p)
		}
	}
	taken := []struct {
		name string
		f    func(m map[string]any)
		want func(p r.PreparedRead) bool
	}{
		{"a scope equal to the read's expiry", func(m map[string]any) { read(m)["scope_expires_at"] = read(m)["expires_at"] },
			func(p r.PreparedRead) bool { return p.ScopeExpiresAt.Equal(p.ExpiresAt) && !p.ScopeExpiresAt.IsZero() }},
		{"a scope in another offset", func(m map[string]any) {
			read(m)["scope_expires_at"] = protocol.FormatTime(expiry(m).Add(90 * time.Minute).In(time.FixedZone("", -5*3600-30*60)))
		}, func(p r.PreparedRead) bool { return p.ScopeExpiresAt.Sub(p.ExpiresAt) == 90*time.Minute }},
		{"a scope at the last second the protocol's times name", func(m map[string]any) { read(m)["scope_expires_at"] = "9999-12-31T23:59:59Z" },
			func(p r.PreparedRead) bool { return p.ScopeExpiresAt.Year() == 9999 }},
	}
	for _, c := range taken {
		g := newRig(t, keeps)
		g.set(edit("/reads", c.f))
		p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
		if err != nil || !c.want(p) || !p.ValidLifetimes() {
			t.Fatalf("%s: %+v %v", c.name, p, err)
		}
		// What was taken can be stored as it is.
		if _, err := json.Marshal(p); err != nil {
			t.Fatalf("%s: the read cannot be stored: %v", c.name, err)
		}
	}
}

// A scope beyond the read's expiry is taken only from a connection that declares kept results: that the platform keeps a product's
// data after the read is the operator's declaration, and a source's answer does not turn it on. Without the declaration the read
// does not verify, in words that name the option; a scope that ends with the read vouches for nothing more and is taken. A read
// that was taken is sent back whatever the connection declares now.
func TestAScopeIsTakenOnlyFromAConnectionThatDeclaresKeptResults(t *testing.T) {
	q, hash := query()
	g := newRig(t) // the connection declares nothing
	g.sim.SetScopeLifetime(72 * time.Hour)
	p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
	want(t, err, r.ErrIntegrity, "a vouched read through a connection that does not declare kept results")
	noLeak(t, err, g.srv)
	if !strings.Contains(err.Error(), "http source reads: the read vouches for a scope beyond its expiry and the connection does not declare kept_results: INTEGRITY_FAILURE") || !reflect.DeepEqual(p, r.PreparedRead{}) {
		t.Fatalf("%+v %v", p, err)
	}
	// The same source through a connection that declares them.
	declared := adapterOf(t, g.srv, keeps)
	vouched, err := declared.PrepareRead(ctx, g.sc, q, hash)
	if err != nil || !vouched.ScopeExpiresAt.After(vouched.ExpiresAt) {
		t.Fatalf("%+v %v", vouched, err)
	}
	// The declaration decides which reads are taken, not which are sent back: the connection that would not take the read asks the
	// source about it as it is, on every route, so that what the platform already holds (a kept file, an export or an issue that is
	// queued) is still authorized by the source after the option is gone.
	if err = g.adapter.AuthorizeCurrent(ctx, g.sc, vouched); err != nil {
		t.Fatal(err)
	}
	page, err := g.adapter.ReadPage(ctx, g.sc, vouched, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.adapter.(r.DocumentAuthority).PrepareDocument(ctx, g.sc, r.IssueIntent{JobID: r.NewID(), Prepared: vouched, RowIndex: 0, RowSHA256: r.Digest(page.Rows[0]), TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	// A scope that is not a time the platform takes is refused for that, whatever the connection declares: the scope is judged before
	// the declaration is asked for, so the words say what is wrong with the answer and not what the operator has not done.
	g.set(edit("/reads", func(m map[string]any) { m["read"].(map[string]any)["scope_expires_at"] = "9999-12-31T23:59:59-00:01" }))
	_, err = g.adapter.PrepareRead(ctx, g.sc, q, hash)
	want(t, err, r.ErrIntegrity, "a scope after the year 9999 through a connection that does not declare kept results")
	if !strings.HasPrefix(err.Error(), "http source reads: the read's scope ends before the read does, or after the year 9999: INTEGRITY_FAILURE") {
		t.Fatal(err)
	}
	// A scope that ends with the read is taken from the undeclared connection, and kept as the source said it.
	g.set(edit("/reads", func(m map[string]any) {
		rd := m["read"].(map[string]any)
		rd["scope_expires_at"] = rd["expires_at"]
	}))
	level, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
	if err != nil || level.ScopeExpiresAt.IsZero() || !level.ScopeExpiresAt.Equal(level.ExpiresAt) {
		t.Fatalf("%+v %v", level, err)
	}
	// A source that vouches for nothing is served by either connection as it always was.
	g.set(nil)
	g.sim.SetScopeLifetime(0)
	for name, a := range map[string]r.Adapter{"undeclared": g.adapter, "declared": declared} {
		plain, err := a.PrepareRead(ctx, g.sc, q, hash)
		if err != nil || !plain.ScopeExpiresAt.IsZero() {
			t.Fatalf("%s: %+v %v", name, plain, err)
		}
	}
}

// The option of a connection (docs/DEPLOYMENT.md): kept_results true makes the adapter take a vouched read, false or absent does
// not, and anything but a boolean refuses the configuration.
func TestTheConnectionDeclaresKeptResultsInItsOptions(t *testing.T) {
	g := newRig(t)
	g.sim.SetScopeLifetime(72 * time.Hour)
	q, hash := query()
	ca := filepath.Join(t.TempDir(), "source-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	options := func(extra string) json.RawMessage {
		return json.RawMessage(`{"base_url":"` + g.srv.URL + `","ca_file":"` + ca + `","auth_env":"SOURCE_AUTH"` + extra + `}`)
	}
	getenv := func(n string) string {
		if n == "SOURCE_AUTH" {
			return testToken
		}
		return ""
	}
	for extra, takes := range map[string]bool{`,"kept_results":true`: true, `,"kept_results":false`: false, ``: false} {
		a, err := httpsource.Factory(options(extra), getenv)
		if err != nil {
			t.Fatal(extra, err)
		}
		_, err = a.PrepareRead(ctx, g.sc, q, hash)
		a.(interface{ Close() }).Close()
		if takes != (err == nil) {
			t.Fatalf("options %q: %v", extra, err)
		}
		if !takes {
			want(t, err, r.ErrIntegrity, "options "+extra)
		}
	}
	for _, extra := range []string{`,"kept_results":"true"`, `,"kept_results":1`, `,"kept_results":null`, `,"Kept_Results":true`, `,"keep_results":true`} {
		if a, err := httpsource.Factory(options(extra), getenv); err == nil || a != nil || strings.Contains(err.Error(), g.srv.URL) || strings.Contains(err.Error(), testToken) {
			t.Fatalf("options %q: %v %v", extra, a, err)
		}
	}
}
