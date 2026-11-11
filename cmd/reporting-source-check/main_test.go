package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestTheReferenceSourcePassesEveryRule(t *testing.T) {
	sim := granted()
	srv := source(t, sim, sourcetest.Options{Token: token})
	code, out := check(t, append(files(t, srv), "-issue", "-companies"))
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, id := range []string{"P1", "A1", "C1", "C2", "C3", "C4", "R1", "R2", "R3", "R4", "R5", "R6", "R7", "R8", "R9", "R10", "R11", "R12", "R13", "D1", "D2", "I1"} {
		if !strings.Contains(out, "PASS "+id+" ") {
			t.Fatalf("%s did not pass:\n%s", id, out)
		}
	}
	if !strings.HasSuffix(out, "reporting-source-check: every rule passed (22)\n") {
		t.Fatal(out)
	}
	// Without -issue the document authority is not touched; without -companies the directory is not asked (S19-B): a connection that does
	// not declare the capability never asks the route, so a product without it passes.
	code, out = check(t, files(t, source(t, muted(), sourcetest.Options{Token: token})))
	if code != 0 || !strings.Contains(out, "SKIP I1 ") || !strings.Contains(out, "SKIP D1 ") || !strings.Contains(out, "SKIP D2 ") || !strings.HasSuffix(out, "reporting-source-check: every rule passed (22)\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Contains(out, "D1 the directory answers and verifies (at most 1000 companies, each id and name 1..128 characters of text, no id twice): not asked (-companies: the connection declares the capability)") == false {
		t.Fatal(out)
	}
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
}

func TestUsageErrorsExitTwo(t *testing.T) {
	srv := source(t, granted(), sourcetest.Options{Token: token})
	args := files(t, srv)
	for name, a := range map[string][]string{
		"no arguments":          nil,
		"an unset credential":   {"-base-url", srv.URL, "-auth-env", "UNSET", "-context", args[7], "-query", args[9]},
		"a context that is not": {"-base-url", srv.URL, "-auth-env", "CHECK_AUTH", "-context", args[9], "-query", args[9]},
		"an unknown flag":       {"-token", token},
	} {
		if code, out := check(t, a); code != 2 {
			t.Fatalf("%s: exit %d %s", name, code, out)
		}
	}
	// A connection the platform would refuse is the first rule's failure.
	code, out := check(t, []string{"-base-url", "http://example.test", "-auth-env", "CHECK_AUTH", "-context", args[7], "-query", args[9]})
	if code != 1 || out != "FAIL P1 the connection is one the platform accepts (https or loopback http, a credential, the bounds): httpsource: base_url must be https://, or http:// to a loopback IP address with loopback_http (the value is not shown)\nreporting-source-check: 1 of 1 rules failed\n" {
		t.Fatalf("exit %d %q", code, out)
	}
}
