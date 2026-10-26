package httpapi_test

import (
	"bytes"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const secret = "01234567890123456789012345678901"

func setup(t *testing.T) (*r.Service, http.Handler) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	auth, e := httpapi.DemoBearer(secret, p)
	if e != nil {
		t.Fatal(e)
	}
	h, e := httpapi.Handler(s, auth)
	if e != nil {
		t.Fatal(e)
	}
	return s, h
}
func request(h http.Handler, method, path string, body []byte, authorized bool) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, path, bytes.NewReader(body))
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Idempotency-Key", "test")
	if authorized {
		q.Header.Set("Authorization", "Bearer "+secret)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, q)
	return w
}
func TestHTTPAsynchronousAdmission(t *testing.T) {
	s, h := setup(t)
	b, _ := json.Marshal(contractsim.FinancialQuery())
	w := request(h, "POST", "/api/reporting/v1/snapshot-jobs", b, true)
	if w.Code != 202 || w.Header().Get("Location") == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	var j r.JobView
	if e := json.Unmarshal(w.Body.Bytes(), &j); e != nil {
		t.Fatal(e)
	}
	if j.State != "queued" {
		t.Fatal(j)
	}
	if _, e := s.RunOne(httptest.NewRequest("GET", "/", nil).Context()); e != nil {
		t.Fatal(e)
	}
	w = request(h, "GET", w.Header().Get("Location"), nil, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestHTTPRejectsUntrustedAuthorityAndAmbiguousJSON(t *testing.T) {
	_, h := setup(t)
	b, _ := json.Marshal(contractsim.FinancialQuery())
	for _, extra := range []string{`,"tenant":"other"}`, `,"actor":"admin"}`, `,"roles":["admin"]}`, `,"budget":999999}`, `,"integration_id":"shipment-contract-demo"}`, `,"INTEGRATION_ID":"brant-contract-demo"}`} {
		input := append(append([]byte{}, b[:len(b)-1]...), extra...)
		w := request(h, "POST", "/api/reporting/v1/snapshot-jobs", input, true)
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", extra, w.Code, w.Body.String())
		}
	}
	if w := request(h, "POST", "/api/reporting/v1/queries", b, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
}
