package httpsource_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/protocol"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/tracecontext"
)

var ctx = context.Background()

func query() (r.QuerySpec, string) {
	q := r.Normalize(contractsim.FinancialQuery())
	return q, r.QueryHash(q)
}

// noLeak fails when an error's text holds the credential, the source's address or a marker of an answer's body.
func noLeak(t *testing.T, err error, srv *httptest.Server, markers ...string) {
	t.Helper()
	if err == nil {
		return
	}
	text := err.Error()
	for _, bad := range append([]string{testToken, srv.URL, strings.TrimPrefix(srv.URL, "https://")}, markers...) {
		if bad != "" && strings.Contains(text, bad) {
			t.Fatalf("an error shows %q: %s", bad, text)
		}
	}
}

func want(t *testing.T, err, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: want %v, got %v", what, target, err)
	}
}

// edit is a tamper that changes the JSON answer of the routes whose path ends with suffix.
func edit(suffix string, f func(m map[string]any)) tamper {
	return func(path string, status int, body []byte) (int, string, []byte, bool) {
		if !strings.HasSuffix(path, suffix) || status != 200 {
			return 0, "", nil, false
		}
		m := map[string]any{}
		if err := json.Unmarshal(body, &m); err != nil {
			panic(err)
		}
		f(m)
		b, _ := json.Marshal(m)
		return status, "application/json", b, true
	}
}

// raw answers the routes whose path ends with suffix with the given status, content type and body.
func raw(suffix string, status int, ct, body string) tamper {
	return func(path string, _ int, _ []byte) (int, string, []byte, bool) {
		if !strings.HasSuffix(path, suffix) {
			return 0, "", nil, false
		}
		return status, ct, []byte(body), true
	}
}

func TestTheProtocolCarriesTheContractSimulatorsBehaviour(t *testing.T) {
	g := newRig(t)
	ds, err := g.adapter.DescribeCatalog(ctx, g.sc)
	if err != nil || len(ds) != 1 || ds[0].Ref != (r.Ref{ID: "receivables.aging", Version: "1.0.0"}) || ds[0].Columns[2].Scale != 2 || ds[0].Columns[2].Currency != "CNY" {
		t.Fatalf("%+v %v", ds, err)
	}
	q, hash := query()
	p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
	if err != nil || p.Context != g.sc || p.QueryHash != hash || p.Watermark != "fixture-v1" {
		t.Fatalf("%+v %v", p, err)
	}
	if err = g.adapter.AuthorizeCurrent(ctx, g.sc, p); err != nil {
		t.Fatal(err)
	}
	page, err := g.adapter.ReadPage(ctx, g.sc, p, "", 2)
	if err != nil || len(page.Rows) != 2 || page.Rows[0][2] != "100.25" || page.Totals["ar.open_balance"] != "9007199254740993.01" || page.NextCursor == "" {
		t.Fatalf("%+v %v", page, err)
	}
	last, err := g.adapter.ReadPage(ctx, g.sc, p, page.NextCursor, 2)
	if err != nil || len(last.Rows) != 1 || last.Rows[0][2] != "9007199254740893.00" || last.NextCursor != "" {
		t.Fatalf("%+v %v", last, err)
	}
	// The source's data changes; the frozen read does not.
	g.sim.ReplaceRecords([]contractsim.Record{{Company: "company_demo", Key: "r1", Values: []string{"changed", "current", "1.00"}}})
	again, err := g.adapter.ReadPage(ctx, g.sc, p, "", 2)
	if err != nil || again.Rows[0][0] != "customer_a" || again.Watermark != "fixture-v1" {
		t.Fatalf("%+v %v", again, err)
	}
	// A company outside the grant, a revoked grant, an unavailable source: the simulator's own refusals, through the protocol.
	other := q
	other.Companies = []string{"company_other"}
	_, err = g.adapter.PrepareRead(ctx, g.sc, other, r.QueryHash(other))
	want(t, err, r.ErrDenied, "a company outside the grant")
	_, err = g.adapter.PrepareRead(ctx, g.sc, q, strings.Repeat("0", 64))
	want(t, err, r.ErrIntegrity, "a query hash that is not the query's")
	_, err = g.adapter.ReadPage(ctx, g.sc, p, "not-a-cursor", 2)
	want(t, err, r.ErrInvalid, "a cursor the source never gave")
	g.sim.Revoke(g.sc.Owner)
	want(t, g.adapter.AuthorizeCurrent(ctx, g.sc, p), r.ErrDenied, "a revoked grant")
	g.sim.Grant(g.sc.Owner, contractsim.DefaultGrant())
	g.sim.SetUnavailable(true)
	_, err = g.adapter.DescribeCatalog(ctx, g.sc)
	want(t, err, r.ErrUnavailable, "an unavailable source")
	noLeak(t, err, g.srv)
}

func TestTheRequestCarriesTheContextTheCredentialAndARequestID(t *testing.T) {
	var mu sync.Mutex
	var headers []http.Header
	var bodies []map[string]any
	sim := contractsim.Financial()
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	ref := sourcetest.New(sim, sourcetest.Options{Token: testToken})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		headers, bodies = append(headers, req.Header.Clone()), append(bodies, m)
		mu.Unlock()
		req.Body = io.NopCloser(strings.NewReader(string(b)))
		ref.ServeHTTP(w, req)
	}))
	defer srv.Close()
	a := adapterOf(t, srv)
	sc := r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	span := tracecontext.New()
	if _, err := a.DescribeCatalog(tracecontext.WithSpan(ctx, span), sc); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DescribeCatalog(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if len(headers) != 2 {
		t.Fatal(len(headers))
	}
	for i, h := range headers {
		id := h.Get(protocol.RequestIDHeader)
		if h.Get("Authorization") != "Bearer "+testToken || h.Get("Content-Type") != "application/json" || !protocol.ValidJobID(id) {
			t.Fatalf("request %d: %v", i, h)
		}
		c := bodies[i]["context"].(map[string]any)
		if c["platform_tenant"] != "platform_demo" || c["issuer"] != "urn:idp:contract-demo" || c["subject"] != "user_a" || c["integration"] != contractsim.FinanceIntegration || c["source_tenant"] != "tenant_a" || len(c) != 5 {
			t.Fatalf("request %d: %v", i, c)
		}
	}
	if headers[0].Get(protocol.RequestIDHeader) == headers[1].Get(protocol.RequestIDHeader) {
		t.Fatal("two requests had one id")
	}
	if tp := headers[0].Get("traceparent"); !strings.HasPrefix(tp, span.String()[:36]) || tp == span.String() || headers[1].Get("traceparent") != "" {
		t.Fatalf("traceparent %q %q", tp, headers[1].Get("traceparent"))
	}
	// The wrong credential is 401 at the source: unavailable here, without a word of the answer.
	b := adapterOf(t, srv, func(c *httpsource.Config) { c.Token = "another-credential-0123456789" })
	_, err := b.DescribeCatalog(ctx, sc)
	want(t, err, r.ErrUnavailable, "a refused credential")
	noLeak(t, err, srv, "another-credential")
}

func TestEveryStatusMapsToItsPlatformError(t *testing.T) {
	g := newRig(t)
	q, hash := query()
	cases := []struct {
		status int
		ct     string
		body   string
		want   error
	}{
		{400, "application/json", `{"code":"invalid"}`, r.ErrInvalid},
		{401, "application/json", `{"code":"denied"}`, r.ErrUnavailable},
		{401, "", "", r.ErrUnavailable},
		{403, "application/json", `{"code":"denied"}`, r.ErrDenied},
		{403, "text/html", "<h1>Forbidden SECRET-BODY</h1>", r.ErrUnavailable},
		{404, "text/plain", "404 page not found SECRET-BODY", r.ErrUnavailable},
		{404, "application/json", `{"code":"not_found"}`, r.ErrIntegrity}, // an issue route's code on a read route
		{409, "application/json", `{"code":"conflict"}`, r.ErrIntegrity},
		{410, "application/json", `{"code":"expired"}`, r.ErrDenied},
		{422, "application/json", `{"code":"unsupported"}`, r.ErrUnsupported},
		{422, "application/json", `{"code":"integrity"}`, r.ErrIntegrity},
		{429, "application/json", `{"code":"budget"}`, r.ErrBudget},
		{429, "", "", r.ErrUnavailable},
		{500, "text/plain", "panic: SECRET-BODY", r.ErrUnavailable},
		{502, "", "", r.ErrUnavailable},
		{503, "application/json", `{"code":"unavailable"}`, r.ErrUnavailable},
		{403, "application/json", `{"code":"budget"}`, r.ErrIntegrity},
		{400, "application/json", `{"code":"SECRET-BODY"}`, r.ErrIntegrity},
		{400, "application/json", `{"code":"invalid","detail":"SECRET-BODY"}`, r.ErrUnavailable}, // not the protocol's body: no code is read
		{201, "application/json", `{}`, r.ErrIntegrity},
		{204, "", "", r.ErrIntegrity},
		{307, "", "", r.ErrUnavailable},
	}
	for _, c := range cases {
		g.set(raw("/reads", c.status, c.ct, c.body))
		_, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
		want(t, err, c.want, fmt.Sprintf("%d %s", c.status, c.body))
		noLeak(t, err, g.srv, "SECRET-BODY")
	}
}

func TestTheIssueRoutesMayAnswerNotFoundAndConflict(t *testing.T) {
	g := newRig(t)
	authority := g.adapter.(r.DocumentAuthority)
	for _, c := range []struct {
		body string
		want error
	}{{`{"code":"not_found"}`, r.ErrNotFound}, {`{"code":"conflict"}`, r.ErrConflict}} {
		status := map[error]int{r.ErrNotFound: 404, r.ErrConflict: 409}[c.want]
		g.set(raw("/confirm", status, "application/json", c.body))
		_, err := authority.ConfirmIssue(ctx, g.sc, r.IssueConfirmation{JobID: strings.Repeat("a", 32), ReservationRef: "x", SHA256: strings.Repeat("b", 64), Pages: 1})
		want(t, err, c.want, c.body)
	}
	_, err := authority.ConfirmIssue(ctx, g.sc, r.IssueConfirmation{JobID: "../catalog", ReservationRef: "x", SHA256: strings.Repeat("b", 64), Pages: 1})
	want(t, err, r.ErrInvalid, "a job id that is not the platform's")
}

func TestAnswersThatDoNotVerifyAreIntegrityFailures(t *testing.T) {
	q, hash := query()
	soon := protocol.FormatTime(time.Now().Add(5 * time.Minute).UTC())
	prepares := []struct {
		name string
		f    func(m map[string]any)
		want error
	}{
		{"another query hash", func(m map[string]any) { m["read"].(map[string]any)["query_hash"] = strings.Repeat("c", 64) }, r.ErrIntegrity},
		{"another dataset", func(m map[string]any) {
			m["read"].(map[string]any)["dataset"] = map[string]any{"id": "receivables.aging", "version": "2.0.0"}
		}, r.ErrIntegrity},
		{"another company", func(m map[string]any) { m["read"].(map[string]any)["company_ids"] = []string{"company_other"} }, r.ErrIntegrity},
		{"another zone", func(m map[string]any) { m["read"].(map[string]any)["timezone"] = "UTC" }, r.ErrIntegrity},
		{"no currency policy", func(m map[string]any) { delete(m["read"].(map[string]any), "currency_policy") }, r.ErrIntegrity},
		{"a ref with a slash", func(m map[string]any) { m["read"].(map[string]any)["ref"] = "a/../b" }, r.ErrIntegrity},
		{"an empty watermark", func(m map[string]any) { m["read"].(map[string]any)["watermark"] = "" }, r.ErrIntegrity},
		{"an expiry an hour ahead", func(m map[string]any) {
			m["read"].(map[string]any)["expires_at"] = protocol.FormatTime(time.Now().Add(time.Hour).UTC())
		}, r.ErrIntegrity},
		{"an expiry in the past", func(m map[string]any) {
			m["read"].(map[string]any)["expires_at"] = protocol.FormatTime(time.Now().Add(-time.Minute).UTC())
		}, r.ErrDenied},
		{"an expiry not in canonical form", func(m map[string]any) {
			m["read"].(map[string]any)["expires_at"] = strings.Replace(soon, "Z", ".000Z", 1)
		}, r.ErrIntegrity},
		{"an unknown field", func(m map[string]any) { m["read"].(map[string]any)["admin"] = true }, r.ErrIntegrity},
		{"a field of another case", func(m map[string]any) {
			rd := m["read"].(map[string]any)
			rd["Ref"] = rd["ref"]
			delete(rd, "ref")
		}, r.ErrIntegrity},
		{"a null", func(m map[string]any) { m["read"].(map[string]any)["currency_policy"] = nil }, r.ErrIntegrity},
		{"no read", func(m map[string]any) { delete(m, "read") }, r.ErrIntegrity},
	}
	for _, c := range prepares {
		g := newRig(t)
		g.set(edit("/reads", c.f))
		_, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
		want(t, err, c.want, c.name)
		noLeak(t, err, g.srv)
	}
	for _, c := range []struct{ name, ct, body string }{
		{"trailing data", "application/json", `{"read":null} {}`},
		{"a duplicate key", "application/json", `{"read":{},"read":{}}`},
		{"not JSON", "application/json", `<html>SECRET-BODY</html>`},
		{"another content type", "text/plain", `{"read":{}}`},
		{"an array", "application/json", `[]`},
	} {
		g := newRig(t)
		g.set(raw("/reads", 200, c.ct, c.body))
		_, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
		want(t, err, r.ErrIntegrity, c.name)
		noLeak(t, err, g.srv, "SECRET-BODY")
	}

	pages := []struct {
		name string
		f    func(m map[string]any)
	}{
		{"another ref", func(m map[string]any) { m["ref"] = "another" }},
		{"another query hash", func(m map[string]any) { m["query_hash"] = strings.Repeat("c", 64) }},
		{"another watermark", func(m map[string]any) { m["watermark"] = "fixture-v2" }},
		{"more rows than asked", func(m map[string]any) {
			rows := m["rows"].([]any)
			m["rows"] = append(rows, rows[0], rows[0])
		}},
		{"a row of another width", func(m map[string]any) { m["rows"].([]any)[0] = []string{"customer_a", "current"} }},
		{"a decimal of another scale", func(m map[string]any) { m["rows"].([]any)[0] = []string{"customer_a", "current", "100.2"} }},
		{"a number, not a string", func(m map[string]any) { m["rows"].([]any)[0] = []any{"customer_a", "current", 100.25} }},
		{"a total of another metric", func(m map[string]any) { m["totals"] = map[string]string{"ar.other": "1.00"} }},
		{"a total too many", func(m map[string]any) { m["totals"].(map[string]any)["ar.other"] = "1.00" }},
		{"a total that is not a decimal", func(m map[string]any) { m["totals"] = map[string]string{"ar.open_balance": "1e3"} }},
		{"no next cursor", func(m map[string]any) { delete(m, "next_cursor") }},
		{"a cursor of 2049 bytes", func(m map[string]any) { m["next_cursor"] = strings.Repeat("c", 2049) }},
		{"a cursor with a space", func(m map[string]any) { m["next_cursor"] = "a b" }},
		{"a cursor after an empty page", func(m map[string]any) { m["rows"] = []any{}; m["next_cursor"] = "more" }},
		{"no rows", func(m map[string]any) { delete(m, "rows") }},
	}
	for _, c := range pages {
		g := newRig(t)
		p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
		if err != nil {
			t.Fatal(err)
		}
		g.set(edit("/pages", c.f))
		_, err = g.adapter.ReadPage(ctx, g.sc, p, "", 2)
		want(t, err, r.ErrIntegrity, c.name)
	}
	// A cursor that does not move would loop for ever.
	g := newRig(t)
	p, _ := g.adapter.PrepareRead(ctx, g.sc, q, hash)
	first, err := g.adapter.ReadPage(ctx, g.sc, p, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	g.set(edit("/pages", func(m map[string]any) { m["next_cursor"] = first.NextCursor }))
	_, err = g.adapter.ReadPage(ctx, g.sc, p, first.NextCursor, 1)
	want(t, err, r.ErrIntegrity, "a cursor that does not move")

	for _, c := range []struct{ name, body string }{{"false", `{"authorized":false}`}, {"missing", `{}`}, {"a string", `{"authorized":"true"}`}} {
		g.set(raw("/authorize", 200, "application/json", c.body))
		want(t, g.adapter.AuthorizeCurrent(ctx, g.sc, p), r.ErrIntegrity, "an authorization that is "+c.name)
	}

	catalogs := []struct {
		name string
		f    func(m map[string]any)
	}{
		{"a dataset twice", func(m map[string]any) { ds := m["datasets"].([]any); m["datasets"] = append(ds, ds[0]) }},
		{"columns that are not the dimensions and metrics", func(m map[string]any) {
			d := m["datasets"].([]any)[0].(map[string]any)
			d["columns"] = d["columns"].([]any)[:2]
		}},
		{"a column of an unknown type", func(m map[string]any) {
			d := m["datasets"].([]any)[0].(map[string]any)
			d["columns"].([]any)[2].(map[string]any)["type"] = "float"
		}},
		{"a decimal scale of 19", func(m map[string]any) {
			d := m["datasets"].([]any)[0].(map[string]any)
			d["columns"].([]any)[2].(map[string]any)["scale"] = 19
		}},
		{"a zone that does not exist", func(m map[string]any) { m["datasets"].([]any)[0].(map[string]any)["timezone"] = "Mars/Olympus" }},
		{"no datasets", func(m map[string]any) { delete(m, "datasets") }},
		{"101 datasets", func(m map[string]any) {
			ds := []any{}
			for i := 0; i < 101; i++ {
				d := map[string]any{}
				b, _ := json.Marshal(m["datasets"].([]any)[0])
				_ = json.Unmarshal(b, &d)
				d["dataset"] = map[string]any{"id": fmt.Sprint("d", i), "version": "1"}
				ds = append(ds, d)
			}
			m["datasets"] = ds
		}},
	}
	for _, c := range catalogs {
		g := newRig(t)
		g.set(edit("/catalog", c.f))
		_, err := g.adapter.DescribeCatalog(ctx, g.sc)
		want(t, err, r.ErrIntegrity, c.name)
	}
}

func TestAnAnswerOverTheBoundIsRefused(t *testing.T) {
	g := newRig(t, func(c *httpsource.Config) { c.MaxResponseBytes = httpsource.MinResponseBytes })
	g.set(raw("/catalog", 200, "application/json", `{"datasets":[],"x":"`+strings.Repeat("a", httpsource.MinResponseBytes)+`"}`))
	_, err := g.adapter.DescribeCatalog(ctx, g.sc)
	want(t, err, r.ErrIntegrity, "an answer over max_response_bytes")
	// The bound is the protocol's whatever the source sends: a body of 8 MiB is not read beyond it.
	big := newRig(t)
	big.set(raw("/catalog", 200, "application/json", `{"datasets":[],"x":"`+strings.Repeat("a", 8<<20)+`"}`))
	_, err = big.adapter.DescribeCatalog(ctx, big.sc)
	want(t, err, r.ErrIntegrity, "an answer of 8 MiB")
}

func TestARedirectIsNeverFollowed(t *testing.T) {
	var reached atomic.Int32
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached.Add(1) }))
	defer elsewhere.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, elsewhere.URL+req.URL.Path, status)
		}))
		a := adapterOf(t, srv)
		_, err := a.DescribeCatalog(ctx, r.SourceContext{Owner: contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), SourceTenant: "tenant_a"})
		want(t, err, r.ErrUnavailable, fmt.Sprint("a redirect ", status))
		noLeak(t, err, srv, elsewhere.URL, strings.TrimPrefix(elsewhere.URL, "https://"))
		srv.Close()
	}
	if reached.Load() != 0 {
		t.Fatal("a redirect was followed")
	}
}

func TestASlowSourceIsUnavailableWithinTheTimeout(t *testing.T) {
	release := make(chan struct{})
	slowHead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
		case <-req.Context().Done():
		}
	}))
	defer slowHead.Close()
	defer close(release)
	slowBody := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"datasets":[`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
		}
	}))
	defer slowBody.Close()
	sc := r.SourceContext{Owner: contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), SourceTenant: "tenant_a"}
	for name, srv := range map[string]*httptest.Server{"no answer": slowHead, "a body that never ends": slowBody} {
		a := adapterOf(t, srv, func(c *httpsource.Config) { c.Timeout = 300 * time.Millisecond })
		start := time.Now()
		_, err := a.DescribeCatalog(ctx, sc)
		want(t, err, r.ErrUnavailable, name)
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("%s: %v", name, d)
		}
	}
	// The caller's own end is the caller's error, not the source's unavailability.
	a := adapterOf(t, slowHead)
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err := a.DescribeCatalog(cctx, sc)
	want(t, err, context.DeadlineExceeded, "the caller's deadline")
}

func TestTheConfigurationIsCheckedAndNeverShown(t *testing.T) {
	k := newPKI(t)
	env := map[string]string{"SOURCE_AUTH": testToken}
	getenv := func(n string) string { return env[n] }
	ok := func(extra string) string {
		return `{"base_url":"https://source.example.test:8443/api","auth_env":"SOURCE_AUTH"` + extra + `}`
	}
	if a, err := httpsource.Factory(json.RawMessage(ok("")), getenv); err != nil {
		t.Fatal(err)
	} else if _, isAuthority := a.(r.DocumentAuthority); isAuthority {
		t.Fatal("issuance none made a document authority")
	}
	if a, err := httpsource.Factory(json.RawMessage(ok(`,"issuance":"rows","ca_file":"`+k.caFile+`","client_cert_file":"`+k.clientCertFile+`","client_key_file":"`+k.clientKeyFile+`","timeout":30,"max_response_bytes":65536`)), getenv); err != nil {
		t.Fatal(err)
	} else if r.IssueContractVersion(a) != 1 || r.GroupIssueSupported(a) {
		t.Fatal("issuance rows is not version 1")
	}
	if a, err := httpsource.Factory(json.RawMessage(`{"base_url":"http://127.0.0.1:9","loopback_http":true,"issuance":"groups"}`), getenv); err != nil || !r.GroupIssueSupported(a) {
		t.Fatal(a, err)
	}
	loose := filepath.Join(k.dir, "loose.key")
	b, _ := os.ReadFile(k.clientKeyFile)
	if err := os.WriteFile(loose, b, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(loose, 0o644)
	link := filepath.Join(k.dir, "link.pem")
	if err := os.Symlink(k.caFile, link); err != nil {
		t.Fatal(err)
	}
	for name, options := range map[string]string{
		"not an object":                  `[]`,
		"an unknown field":               ok(`,"base_urls":"x"`),
		"a null":                         ok(`,"ca_file":null`),
		"a duplicate key":                ok(`,"auth_env":"SOURCE_AUTH"`),
		"http to a host":                 `{"base_url":"http://source.example.test","auth_env":"SOURCE_AUTH","loopback_http":true}`,
		"http to loopback without leave": `{"base_url":"http://127.0.0.1:9","auth_env":"SOURCE_AUTH"}`,
		"another scheme":                 `{"base_url":"ftp://source.example.test","auth_env":"SOURCE_AUTH"}`,
		"a user in the URL":              `{"base_url":"https://user:SECRET-PW@source.example.test","auth_env":"SOURCE_AUTH"}`,
		"a query":                        `{"base_url":"https://source.example.test/?SECRET-PW=1","auth_env":"SOURCE_AUTH"}`,
		"a fragment":                     `{"base_url":"https://source.example.test/#SECRET-PW","auth_env":"SOURCE_AUTH"}`,
		"a path that is not clean":       `{"base_url":"https://source.example.test/a/../b","auth_env":"SOURCE_AUTH"}`,
		"no credential":                  `{"base_url":"https://source.example.test"}`,
		"a credential that is not set":   `{"base_url":"https://source.example.test","auth_env":"UNSET_SOURCE_AUTH"}`,
		"an env name that is not one":    `{"base_url":"https://source.example.test","auth_env":"source-auth"}`,
		"a timeout of 61":                ok(`,"timeout":61`),
		"a negative timeout":             ok(`,"timeout":-1`),
		"a response bound of 100":        ok(`,"max_response_bytes":100`),
		"a response bound over 1 MiB":    ok(`,"max_response_bytes":1048577`),
		"an unknown issuance":            ok(`,"issuance":"always"`),
		"a relative CA":                  ok(`,"ca_file":"ca.pem"`),
		"a CA that is not PEM":           ok(`,"ca_file":"` + k.clientKeyFile + `"`),
		"a CA that is a link":            ok(`,"ca_file":"` + link + `"`),
		"a certificate without its key":  ok(`,"client_cert_file":"` + k.clientCertFile + `"`),
		"a key readable by others":       ok(`,"client_cert_file":"` + k.clientCertFile + `","client_key_file":"` + loose + `"`),
		"a key that is not the cert's":   ok(`,"client_cert_file":"` + k.certFile + `","client_key_file":"` + k.clientKeyFile + `"`),
		"a file that does not exist":     ok(`,"ca_file":"/nonexistent/SECRET-PW.pem"`),
	} {
		_, err := httpsource.Factory(json.RawMessage(options), getenv)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		for _, bad := range []string{"SECRET-PW", testToken, "source.example.test", k.dir} {
			if strings.Contains(err.Error(), bad) {
				t.Fatalf("%s: the error shows %q: %v", name, bad, err)
			}
		}
	}
	env["SOURCE_AUTH"] = "has a space"
	if _, err := httpsource.Factory(json.RawMessage(ok("")), getenv); err == nil || strings.Contains(err.Error(), "has a space") {
		t.Fatal(err)
	}
}

func TestMutualTLSAndAPinnedCA(t *testing.T) {
	k := newPKI(t)
	sim := contractsim.Financial()
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	sc := r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	srv := httptest.NewUnstartedServer(sourcetest.New(sim, sourcetest.Options{RequireClientCertificate: true}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{k.serverPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: k.pool, MinVersion: tls.VersionTLS12}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes below are expected
	srv.StartTLS()
	defer srv.Close()
	options := func(extra string) json.RawMessage {
		return json.RawMessage(`{"base_url":"` + srv.URL + `","ca_file":"` + k.caFile + `"` + extra + `}`)
	}
	a, err := httpsource.Factory(options(`,"client_cert_file":"`+k.clientCertFile+`","client_key_file":"`+k.clientKeyFile+`"`), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer a.(interface{ Close() }).Close()
	if _, err = a.DescribeCatalog(ctx, sc); err != nil {
		t.Fatal(err)
	}
	// Without the client certificate the handshake is refused; with the system's roots in place of the pinned CA the source is not trusted.
	env := func(string) string { return testToken }
	b, err := httpsource.Factory(options(`,"auth_env":"X"`), env)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.DescribeCatalog(ctx, sc)
	want(t, err, r.ErrUnavailable, "no client certificate")
	c, err := httpsource.Factory(json.RawMessage(`{"base_url":"`+srv.URL+`","client_cert_file":"`+k.clientCertFile+`","client_key_file":"`+k.clientKeyFile+`"}`), env)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DescribeCatalog(ctx, sc)
	want(t, err, r.ErrUnavailable, "a source the system's roots do not vouch for")
	noLeak(t, err, srv)
	// TLS 1.1 is refused.
	old := httptest.NewUnstartedServer(sourcetest.New(sim, sourcetest.Options{}))
	old.TLS = &tls.Config{Certificates: []tls.Certificate{k.serverPair}, MaxVersion: tls.VersionTLS11, MinVersion: tls.VersionTLS10}
	old.Config.ErrorLog = log.New(io.Discard, "", 0)
	old.StartTLS()
	defer old.Close()
	d, _ := httpsource.Factory(json.RawMessage(`{"base_url":"`+old.URL+`","ca_file":"`+k.caFile+`","auth_env":"X"}`), env)
	_, err = d.DescribeCatalog(ctx, sc)
	want(t, err, r.ErrUnavailable, "TLS 1.1")
}

func TestPlainHTTPOnlyToALoopbackAddressWhenAllowed(t *testing.T) {
	sim := contractsim.Financial()
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	srv := httptest.NewServer(sourcetest.New(sim, sourcetest.Options{}))
	defer srv.Close()
	if _, err := httpsource.New(httpsource.Config{BaseURL: srv.URL, Token: testToken}); err == nil {
		t.Fatal("plain HTTP without loopback_http")
	}
	a, err := httpsource.New(httpsource.Config{BaseURL: srv.URL, LoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DescribeCatalog(ctx, r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := httpsource.New(httpsource.Config{BaseURL: "http://localhost:1", LoopbackHTTP: true}); err == nil {
		t.Fatal("a loopback name, not an address")
	}
}

func TestAReadOfAnotherContextIsNeverSent(t *testing.T) {
	var calls atomic.Int32
	g := newRig(t)
	q, hash := query()
	p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
	if err != nil {
		t.Fatal(err)
	}
	g.set(func(string, int, []byte) (int, string, []byte, bool) { calls.Add(1); return 0, "", nil, false })
	other := g.sc
	other.Owner.Subject = "user_b"
	want(t, g.adapter.AuthorizeCurrent(ctx, other, p), r.ErrDenied, "another subject")
	_, err = g.adapter.ReadPage(ctx, other, p, "", 2)
	want(t, err, r.ErrDenied, "another subject's page")
	incomplete := g.sc
	incomplete.SourceTenant = ""
	_, err = g.adapter.DescribeCatalog(ctx, incomplete)
	want(t, err, r.ErrDenied, "no source tenant")
	if calls.Load() != 0 {
		t.Fatal("a refused request reached the source")
	}
}

func TestTheAdapterIsSafeForConcurrentUse(t *testing.T) {
	g := newRig(t)
	q, hash := query()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
			if err == nil {
				err = g.adapter.AuthorizeCurrent(ctx, g.sc, p)
			}
			if err == nil {
				var page r.SourcePage
				page, err = g.adapter.ReadPage(ctx, g.sc, p, "", 50)
				if err == nil && len(page.Rows) != 3 {
					err = fmt.Errorf("%d rows", len(page.Rows))
				}
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheErrorsAreFixedTexts(t *testing.T) {
	e := &httpsource.Error{Op: "pages", Reason: "the page is not of the read that was asked", RequestID: strings.Repeat("a", 32), Err: r.ErrIntegrity}
	if e.Error() != "http source pages: the page is not of the read that was asked: INTEGRITY_FAILURE (request aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)" || !errors.Is(e, r.ErrIntegrity) {
		t.Fatal(e)
	}
	// Two refusals are the same error when they are the same platform error, as an in-process adapter's are.
	if !errors.Is(e, &httpsource.Error{Op: "authorize", Err: r.ErrIntegrity}) || errors.Is(e, &httpsource.Error{Err: r.ErrDenied}) {
		t.Fatal("Is")
	}
}
