package httpsource_test

import (
	"encoding/json"
	"fmt"
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
	"idp.local/reporting/storage/memory"
)

// noDirectory is a source product without the company directory: embedding the interface promotes the four contract methods only.
type noDirectory struct{ r.Adapter }

var demoCompanies = r.CompanyList{Companies: []r.Company{{ID: "company_demo", Name: "演示公司"}}}

// The company directory (ADR 0015, S19-B) is a capability the connection declares, as issuance is: without `companies` the adapter is not a
// directory and the route is never asked; with it the adapter is one, whatever the issuance, and the answer is the source's list.
func TestTheDirectoryIsDeclaredByTheConnection(t *testing.T) {
	for _, issuance := range []string{httpsource.IssuanceNone, httpsource.IssuanceRows, httpsource.IssuanceGroups} {
		silent := newRig(t, func(c *httpsource.Config) { c.Issuance = issuance })
		if _, ok := silent.adapter.(r.CompanyDirectory); ok {
			t.Fatalf("issuance %s without companies is a directory", issuance)
		}
		g := newRig(t, func(c *httpsource.Config) { c.Issuance = issuance; c.Companies = true })
		d, ok := g.adapter.(r.CompanyDirectory)
		if !ok {
			t.Fatalf("issuance %s with companies is not a directory", issuance)
		}
		if _, authority := g.adapter.(r.DocumentAuthority); authority != (issuance != httpsource.IssuanceNone) || r.GroupIssueSupported(g.adapter) != (issuance == httpsource.IssuanceGroups) {
			t.Fatalf("issuance %s with companies: authority %v, groups %v", issuance, authority, r.GroupIssueSupported(g.adapter))
		}
		list, err := d.ListCompanies(ctx, g.sc)
		if err != nil || !reflect.DeepEqual(list, demoCompanies) {
			t.Fatalf("issuance %s: %+v %v", issuance, list, err)
		}
		// The directory follows the grant, as in process: a wider grant lists more, a revoked one is denied.
		wider := contractsim.DefaultGrant()
		wider.Companies = []string{"company_demo", "company_other"}
		g.sim.Grant(g.sc.Owner, wider)
		list, err = d.ListCompanies(ctx, g.sc)
		if err != nil || len(list.Companies) != 2 || list.Companies[1] != (r.Company{ID: "company_other", Name: "其他公司"}) || list.Partial {
			t.Fatalf("%+v %v", list, err)
		}
		g.sim.Revoke(g.sc.Owner)
		_, err = d.ListCompanies(ctx, g.sc)
		want(t, err, r.ErrDenied, "a revoked grant")
		noLeak(t, err, g.srv)
	}
	// The route, the method and the credential of the request.
	var mu sync.Mutex
	var seen []string
	sim := contractsim.Financial()
	sim.Grant(contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), contractsim.DefaultGrant())
	srv := httptest.NewTLSServer(sourcetest.New(sim, sourcetest.Options{Token: testToken, Observe: func(req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, req.Method+" "+req.URL.Path+" "+req.Header.Get("Authorization"))
	}}))
	defer srv.Close()
	a := adapterOf(t, srv, func(c *httpsource.Config) { c.Companies = true })
	if _, err := a.(r.CompanyDirectory).ListCompanies(ctx, r.SourceContext{Owner: contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), SourceTenant: "tenant_a"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "POST "+protocol.PathCompanies+" Bearer "+testToken {
		t.Fatal(seen)
	}
	// An incomplete context is refused before anything is sent.
	_, err := a.(r.CompanyDirectory).ListCompanies(ctx, r.SourceContext{})
	want(t, err, r.ErrDenied, "an incomplete context")
	if len(seen) != 1 {
		t.Fatal("the incomplete context was sent")
	}
}

// A source that does not implement the directory answers the route unsupported; a connection that declares the capability anyway gets
// that refusal as it is, and the platform's route answers it as the source's refusal (422), never as a list.
func TestASourceWithoutTheDirectoryIsUnsupportedWhenDeclared(t *testing.T) {
	sim := contractsim.Financial()
	sim.Grant(contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), contractsim.DefaultGrant())
	srv := httptest.NewTLSServer(sourcetest.New(noDirectory{sim}, sourcetest.Options{Token: testToken}))
	defer srv.Close()
	a := adapterOf(t, srv, func(c *httpsource.Config) { c.Companies = true })
	sc := r.SourceContext{Owner: contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration), SourceTenant: "tenant_a"}
	_, err := a.(r.CompanyDirectory).ListCompanies(ctx, sc)
	want(t, err, r.ErrUnsupported, "a source without the directory")
	noLeak(t, err, srv)
}

func manyWireCompanies(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = map[string]any{"id": fmt.Sprintf("c%04d", i), "name": fmt.Sprintf("公司 %d", i)}
	}
	return out
}

// Every list that is not of the protocol's shape or outside ADR 0015's bounds is an integrity failure, and nothing of it is passed on.
func TestACompanyListThatDoesNotVerifyIsAnIntegrityFailure(t *testing.T) {
	company := func(m map[string]any, i int) map[string]any { return m["companies"].([]any)[i].(map[string]any) }
	for _, c := range []struct {
		name string
		f    func(m map[string]any)
	}{
		{"no companies", func(m map[string]any) { delete(m, "companies") }},
		{"no partial", func(m map[string]any) { delete(m, "partial") }},
		{"partial as a string", func(m map[string]any) { m["partial"] = "false" }},
		{"partial null", func(m map[string]any) { m["partial"] = nil }},
		{"companies as an object", func(m map[string]any) { m["companies"] = map[string]any{} }},
		{"a company that is a string", func(m map[string]any) { m["companies"] = []any{"company_demo"} }},
		{"a company without a name", func(m map[string]any) { delete(company(m, 0), "name") }},
		{"a company without an id", func(m map[string]any) { delete(company(m, 0), "id") }},
		{"an unknown field in a company", func(m map[string]any) { company(m, 0)["code"] = "x" }},
		{"a field of another case", func(m map[string]any) { company(m, 0)["Name"] = "x"; delete(company(m, 0), "name") }},
		{"1001 companies", func(m map[string]any) { m["companies"] = manyWireCompanies(1001); m["partial"] = true }},
		{"an id twice", func(m map[string]any) {
			m["companies"] = []any{map[string]any{"id": "a", "name": "A"}, map[string]any{"id": "a", "name": "B"}}
		}},
		{"an id of 129 bytes", func(m map[string]any) { company(m, 0)["id"] = strings.Repeat("a", 129) }},
		{"a name of 129 bytes", func(m map[string]any) { company(m, 0)["name"] = strings.Repeat("名", 43) }},
		{"an empty id", func(m map[string]any) { company(m, 0)["id"] = "" }},
		{"an empty name", func(m map[string]any) { company(m, 0)["name"] = "" }},
		{"a blank name", func(m map[string]any) { company(m, 0)["name"] = "   " }},
		{"a control character in a name", func(m map[string]any) { company(m, 0)["name"] = "演示\u0007公司" }},
		{"a newline in an id", func(m map[string]any) { company(m, 0)["id"] = "a\nb" }},
		{"DEL in a name", func(m map[string]any) { company(m, 0)["name"] = "a\u007fb" }},
		{"a noncharacter in a name", func(m map[string]any) { company(m, 0)["name"] = "a\ufffeb" }},
	} {
		g := newRig(t, func(c *httpsource.Config) { c.Companies = true })
		g.set(edit("/companies", c.f))
		list, err := g.adapter.(r.CompanyDirectory).ListCompanies(ctx, g.sc)
		want(t, err, r.ErrIntegrity, c.name)
		if len(list.Companies) != 0 {
			t.Fatalf("%s: a list was passed on", c.name)
		}
		noLeak(t, err, g.srv)
	}
	for _, c := range []struct{ name, ct, body string }{
		{"trailing data", "application/json", `{"companies":[],"partial":false} {}`},
		{"a duplicate key", "application/json", `{"companies":[],"companies":[],"partial":false}`},
		{"not JSON", "application/json", `<html>SECRET-BODY</html>`},
		{"another content type", "text/plain", `{"companies":[],"partial":false}`},
		{"an array", "application/json", `[]`},
		{"an unknown field", "application/json", `{"companies":[],"partial":false,"total":1}`},
	} {
		g := newRig(t, func(c *httpsource.Config) { c.Companies = true })
		g.set(raw("/companies", 200, c.ct, c.body))
		_, err := g.adapter.(r.CompanyDirectory).ListCompanies(ctx, g.sc)
		want(t, err, r.ErrIntegrity, c.name)
		noLeak(t, err, g.srv, "SECRET-BODY")
	}
	// The bounds are inclusive: 1000 companies of 128 bytes verify, and the whole of the protocol's answer travels.
	g := newRig(t, func(c *httpsource.Config) { c.Companies = true })
	g.set(edit("/companies", func(m map[string]any) {
		all := manyWireCompanies(1000)
		all[0] = map[string]any{"id": strings.Repeat("i", 128), "name": strings.Repeat("名", 42) + "ab"}
		m["companies"], m["partial"] = all, true
	}))
	list, err := g.adapter.(r.CompanyDirectory).ListCompanies(ctx, g.sc)
	if err != nil || len(list.Companies) != 1000 || !list.Partial || list.Companies[0].ID != strings.Repeat("i", 128) || list.Companies[999].Name != "公司 999" {
		t.Fatalf("%d %v %v", len(list.Companies), list.Partial, err)
	}
	// The empty list is an answer, not a failure.
	g.set(raw("/companies", 200, "application/json", `{"companies":[],"partial":false}`))
	list, err = g.adapter.(r.CompanyDirectory).ListCompanies(ctx, g.sc)
	if err != nil || len(list.Companies) != 0 || list.Partial {
		t.Fatalf("%+v %v", list, err)
	}
}

// The refusals of the route are the protocol's table: the source's own codes, unavailable for what cannot be reached or served, and an
// integrity failure for a code the route may not answer.
func TestTheDirectorysRefusalsAreTheProtocols(t *testing.T) {
	g := newRig(t, func(c *httpsource.Config) { c.Companies = true })
	d := g.adapter.(r.CompanyDirectory)
	g.sim.SetUnavailable(true)
	_, err := d.ListCompanies(ctx, g.sc)
	want(t, err, r.ErrUnavailable, "an unavailable source")
	g.sim.SetUnavailable(false)
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"a 500", 500, "", r.ErrUnavailable},
		{"a 401", 401, "", r.ErrUnavailable},
		{"denied", 403, `{"code":"denied"}`, r.ErrDenied},
		{"budget", 429, `{"code":"budget"}`, r.ErrBudget},
		{"unsupported", 422, `{"code":"unsupported"}`, r.ErrUnsupported},
		{"an issue-only code", 404, `{"code":"not_found"}`, r.ErrIntegrity},
		{"a code with another status", 400, `{"code":"denied"}`, r.ErrIntegrity},
		{"a 201", 201, `{"companies":[],"partial":false}`, r.ErrIntegrity},
	} {
		g.set(raw("/companies", c.status, "application/json", c.body))
		_, err = d.ListCompanies(ctx, g.sc)
		want(t, err, c.want, c.name)
		noLeak(t, err, g.srv)
	}
	g.set(nil)
	// A slow source is unavailable within the timeout, and the caller's own deadline wins.
	slow := newRig(t, func(c *httpsource.Config) { c.Companies = true; c.Timeout = time.Second })
	slow.set(func(path string, status int, body []byte) (int, string, []byte, bool) {
		if strings.HasSuffix(path, "/companies") {
			time.Sleep(1500 * time.Millisecond)
		}
		return 0, "", nil, false
	})
	start := time.Now()
	_, err = slow.adapter.(r.CompanyDirectory).ListCompanies(ctx, slow.sc)
	want(t, err, r.ErrUnavailable, "a slow source")
	if time.Since(start) > 5*time.Second {
		t.Fatal("the timeout did not bound the request")
	}
}

// The service lists through the protocol what it lists in process: the reference source declares the directory when the wrapped adapter
// has it, and a connection to a source without it answers supported false.
func TestTheServiceListsCompaniesThroughTheProtocol(t *testing.T) {
	p := contractsim.DemoPrincipal()
	for _, c := range []struct {
		name string
		via  func(*contractsim.Simulator) r.Adapter
	}{
		{"in process", func(s *contractsim.Simulator) r.Adapter { return s }},
		{"over the protocol", func(s *contractsim.Simulator) r.Adapter { return sourcetest.Remote(t, s) }},
	} {
		name := c.name
		sim := contractsim.Financial()
		sim.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
		a := c.via(sim)
		s, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(strings.Repeat("k", 32))})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ListCompanies(ctx, p, contractsim.FinanceIntegration)
		b, _ := json.Marshal(got)
		if err != nil || string(b) != `{"supported":true,"partial":false,"companies":[{"id":"company_demo","name":"演示公司"}]}` {
			t.Fatalf("%s: %s %v", name, b, err)
		}
	}
	// A source without the directory behind the protocol: the reference source does not declare it, the platform answers supported false.
	sim := contractsim.Financial()
	sim.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	remote := sourcetest.Remote(t, noDirectory{sim})
	if _, ok := remote.(r.CompanyDirectory); ok {
		t.Fatal("the reference source declared a directory the adapter does not have")
	}
	s, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: remote}}, CursorKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListCompanies(ctx, p, contractsim.FinanceIntegration)
	if b, _ := json.Marshal(got); err != nil || string(b) != `{"supported":false,"partial":false,"companies":[]}` {
		t.Fatalf("%s %v", b, err)
	}
}
