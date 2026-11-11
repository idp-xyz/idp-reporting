package httpapi_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// TestTheCatalogCarriesTheDeclaredFiltersAndSorts (S20-A, ADR 0023): GET /catalog answers each dataset's `filters` and `sorts` as the
// source declares them, in the JSON names the console will read.
func TestTheCatalogCarriesTheDeclaredFiltersAndSorts(t *testing.T) {
	_, h := setup(t)
	w := request(h, "GET", "/api/reporting/v1/catalog?integration_id="+contractsim.FinanceIntegration, nil, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	wantFilters := `"filters":[{"field":"customer.id","operators":["eq","ne","in","prefix"]},{"field":"aging.bucket","operators":["eq","in"],"values":["current","1_30","31_60","61_90","90+"]},{"field":"ar.open_balance","operators":["lt","le","gt","ge","between"]}]`
	wantSorts := `"sorts":[{"field":"ar.open_balance","directions":["asc","desc"]},{"field":"customer.id","directions":["asc","desc"]}]`
	// A declaration never says `required` or `values` it does not have.
	if !strings.Contains(body, wantFilters) || !strings.Contains(body, wantSorts) || strings.Contains(body, `"required"`) || strings.Count(body, `"values"`) != 1 {
		t.Fatal(body)
	}
	var typed []r.Dataset
	if e := json.Unmarshal(w.Body.Bytes(), &typed); e != nil || len(typed) != 1 || len(typed[0].Filters) != 3 || len(typed[0].Sorts) != 2 || typed[0].Filters[1].Required {
		t.Fatal(e, body)
	}
	// A dataset that declares nothing has no lists at all, so a console that reads the catalog of an older source sees what it saw.
	p := contractsim.DemoPrincipal()
	plain := contractsim.Financial().Fixture()
	plain.Dataset.Filters, plain.Dataset.Sorts = nil, nil
	bare := contractsim.New(plain.Dataset, plain.Records)
	bare.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: bare}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	h2, _ := httpapi.Handler(s, auth)
	if w := request(h2, "GET", "/api/reporting/v1/catalog?integration_id="+contractsim.FinanceIntegration, nil, true); w.Code != 200 || strings.Contains(w.Body.String(), `"filters"`) || strings.Contains(w.Body.String(), `"sorts"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

// TestQueriesAndSnapshotJobsTakeDeclaredFiltersAndRefuseTheRest: the two routes accept what the catalog entry declares and answer 422
// UNSUPPORTED_COMBINATION, the code of every unpublished combination, for what it does not; the result is the narrowed scope's.
func TestQueriesAndSnapshotJobsTakeDeclaredFiltersAndRefuseTheRest(t *testing.T) {
	s, h := setup(t)
	q := contractsim.FinancialQuery()
	q.Filters = []r.Filter{{Field: "aging.bucket", Operator: "in", Value: `["current","1_30"]`}}
	q.Sort = []r.Sort{{Field: "ar.open_balance", Direction: "asc"}}
	b, _ := json.Marshal(q)
	w := request(h, "POST", "/api/reporting/v1/queries", b, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var res r.Result
	if e := json.Unmarshal(w.Body.Bytes(), &res); e != nil || len(res.Rows) != 2 || res.Rows[0][0] != "customer_b" || res.Rows[1][0] != "customer_a" || res.Totals["ar.open_balance"] != "100.01" || res.Completeness != "complete" || res.Evidence.QueryHash != r.QueryHash(q) {
		t.Fatalf("%+v %v", res, e)
	}
	w = request(h, "POST", "/api/reporting/v1/snapshot-jobs", b, true)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var j r.JobView
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	if _, e := s.RunOne(httptest.NewRequest("GET", "/", nil).Context()); e != nil {
		t.Fatal(e)
	}
	w = request(h, "GET", "/api/reporting/v1/jobs/"+j.ID, nil, true)
	_ = json.Unmarshal(w.Body.Bytes(), &j)
	if w.Code != 200 || j.State != "succeeded" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", "/api/reporting/v1/snapshots/"+j.SnapshotID+"/rows?limit=10", nil, true)
	var page r.RowsPage
	if e := json.Unmarshal(w.Body.Bytes(), &page); w.Code != 200 || e != nil || page.TotalRows != 2 || page.Rows[0][2] != "-0.24" || page.Totals["ar.open_balance"] != "100.01" {
		t.Fatal(w.Code, w.Body.String())
	}
	// The same query without its filters is another snapshot: another hash, every row.
	plain, _ := json.Marshal(contractsim.FinancialQuery())
	w = request(h, "POST", "/api/reporting/v1/queries", plain, true)
	var whole r.Result
	if e := json.Unmarshal(w.Body.Bytes(), &whole); w.Code != 200 || e != nil || len(whole.Rows) != 3 || whole.Evidence.QueryHash == res.Evidence.QueryHash {
		t.Fatal(w.Code, w.Body.String())
	}

	for name, body := range map[string]string{
		"an undeclared field":      `"filters":[{"field":"aging.bucket","operator":"prefix","value":"cur"}],"sort":[]`,
		"an undeclared operator":   `"filters":[{"field":"customer.id","operator":"contains","value":"a"}],"sort":[]`,
		"an unknown operator":      `"filters":[{"field":"customer.id","operator":"like","value":"a%"}],"sort":[]`,
		"a value off the list":     `"filters":[{"field":"aging.bucket","operator":"eq","value":"91+"}],"sort":[]`,
		"a malformed decimal":      `"filters":[{"field":"ar.open_balance","operator":"ge","value":"100"}],"sort":[]`,
		"an in that is not a list": `"filters":[{"field":"customer.id","operator":"in","value":"customer_a"}],"sort":[]`,
		"a between out of order":   `"filters":[{"field":"ar.open_balance","operator":"between","value":"[\"1.00\",\"0.00\"]"}],"sort":[]`,
		"17 filters":               `"filters":[` + strings.Repeat(`{"field":"customer.id","operator":"eq","value":"a"},`, 16) + `{"field":"customer.id","operator":"eq","value":"a"}],"sort":[]`,
		"an undeclared sort field": `"filters":[],"sort":[{"field":"aging.bucket","direction":"asc"}]`,
		"a direction that is not":  `"filters":[],"sort":[{"field":"customer.id","direction":"up"}]`,
		"a sort field twice":       `"filters":[],"sort":[{"field":"customer.id","direction":"asc"},{"field":"customer.id","direction":"desc"}]`,
		"four sort keys":           `"filters":[],"sort":[{"field":"customer.id","direction":"asc"},{"field":"ar.open_balance","direction":"asc"},{"field":"customer.id","direction":"desc"},{"field":"ar.open_balance","direction":"desc"}]`,
	} {
		input := withLists(t, plain, body)
		for _, route := range []string{"/api/reporting/v1/queries", "/api/reporting/v1/snapshot-jobs"} {
			w := request(h, "POST", route, input, true)
			if w.Code != 422 || strings.TrimSpace(w.Body.String()) != `{"error":"UNSUPPORTED_COMBINATION"}` {
				t.Errorf("%s %s: %d %s", route, name, w.Code, w.Body.String())
			}
		}
	}
	// A filter that is not of the query's shape is a malformed request, as any other unknown field: 400.
	for name, body := range map[string]string{
		"a filter with an unknown key": `"filters":[{"field":"customer.id","operator":"eq","value":"a","values":["b"]}],"sort":[]`,
		"a filter value as a number":   `"filters":[{"field":"ar.open_balance","operator":"ge","value":0}],"sort":[]`,
		"a filter value as a list":     `"filters":[{"field":"customer.id","operator":"in","value":["a"]}],"sort":[]`,
		"a null filter":                `"filters":[null],"sort":[]`,
	} {
		if w := request(h, "POST", "/api/reporting/v1/queries", withLists(t, plain, body), true); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// withLists replaces the `filters` and `sort` members of a serialized query with the given JSON members.
func withLists(t *testing.T, query []byte, lists string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if e := json.Unmarshal(query, &m); e != nil {
		t.Fatal(e)
	}
	delete(m, "filters")
	delete(m, "sort")
	rest, _ := json.Marshal(m)
	return []byte(`{` + lists + `,` + string(rest[1:]))
}
