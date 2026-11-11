package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// sayingClass is an adapter that says the data class it is given (reporting.ClassedAdapter); sayingNothing forwards the four source
// calls and says none, as the adapter of a source product does.
type sayingClass struct {
	r.Adapter
	class string
}

func (a sayingClass) DataClass() string { return a.class }

type sayingNothing struct{ r.Adapter }

// TestTheCatalogAndTheEvidenceSayTheDataClassOfTheConnection (S08-A, ADR 0012 decisions 6 and 9), through the routes: a client learns
// where a dataset's data comes from before it reads a row, from `data_class` on each entry of GET /catalog, and every result of that
// connection says the same word in its evidence: a preview, a page of a frozen snapshot, and the workbook's Metadata sheet. The word is
// the adapter's: a contract simulator's, a manual source's, or a source product's for an adapter that says nothing. The
// X-Reporting-Data-Class header of the answers is another thing, the kind of server, and does not follow the connection.
func TestTheCatalogAndTheEvidenceSayTheDataClassOfTheConnection(t *testing.T) {
	p := contractsim.DemoPrincipal()
	cases := []struct {
		integration, class, note string
		adapter                  func(*contractsim.Simulator) r.Adapter
	}{
		{contractsim.FinanceIntegration, "contract-simulator", "CONTRACT SIMULATOR / NOT REAL BUSINESS DATA", func(s *contractsim.Simulator) r.Adapter { return s }},
		{"typed-ledger", "manual-entry", "MANUAL ENTRY / NOT FROM A SOURCE PRODUCT", func(s *contractsim.Simulator) r.Adapter { return sayingClass{s, "manual-entry"} }},
		{"erp", "business-data", "SOURCE PRODUCT DATA", func(s *contractsim.Simulator) r.Adapter { return sayingNothing{s} }},
	}
	var connections []r.Connection
	p.Integrations = nil
	for _, c := range cases {
		p.Integrations = append(p.Integrations, c.integration)
		sim := contractsim.Financial()
		sim.Grant(contractsim.DemoOwner(p, c.integration), contractsim.DefaultGrant())
		connections = append(connections, r.Connection{Tenant: p.Tenant, Integration: c.integration, SourceTenant: "tenant_a", Adapter: c.adapter(sim)})
	}
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: connections, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
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
	for _, c := range cases {
		// The catalog: the dataset as the source published it, then the class of the connection, the last key of the entry.
		w := request(h, "GET", "/api/reporting/v1/catalog?integration_id="+c.integration, nil, true)
		published, e := s.Catalog(t.Context(), p, c.integration)
		if w.Code != 200 || e != nil || len(published) != 1 {
			t.Fatal(c.integration, w.Code, w.Body.String(), e)
		}
		dataset, _ := json.Marshal(published[0])
		if want := "[" + strings.TrimSuffix(string(dataset), "}") + `,"data_class":"` + c.class + `"}]`; strings.TrimSpace(w.Body.String()) != want {
			t.Errorf("%s: the catalog is\n %s\nwant\n %s", c.integration, w.Body.String(), want)
		}
		if strings.Contains(string(dataset), "data_class") {
			t.Errorf("%s: the dataset the source published carries a data class of its own: %s", c.integration, dataset)
		}
		// The header is the server's class, the same on every answer of this API whatever the connection.
		if got := w.Header().Values("X-Reporting-Data-Class"); len(got) != 1 || got[0] != "contract-simulator" {
			t.Errorf("%s: the header is %q", c.integration, got)
		}
		// A preview's evidence.
		q := contractsim.FinancialQuery()
		q.Integration = c.integration
		body, _ := json.Marshal(q)
		w = request(h, "POST", "/api/reporting/v1/queries", body, true)
		var res r.Result
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Evidence.DataClass != c.class || !strings.Contains(w.Body.String(), `"data_class":"`+c.class+`"`) {
			t.Errorf("%s: a query answers %d %s", c.integration, w.Code, w.Body.String())
		}
		// A page of the frozen snapshot, and the workbook exported from it.
		snapshot := freezeWithKey(t, s, h, "snap-"+c.integration, q)
		w = request(h, "GET", "/api/reporting/v1/snapshots/"+snapshot+"/rows?limit=2", nil, true)
		var page r.RowsPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Evidence.DataClass != c.class {
			t.Errorf("%s: a page answers %d %s", c.integration, w.Code, w.Body.String())
		}
		meta := sheetRows(t, exportedWorkbook(t, s, h, "export-"+c.integration, snapshot, ""), "Metadata")
		if len(meta) == 0 || meta[0] != "data_class|"+c.note {
			t.Errorf("%s: the workbook says %q, want %q", c.integration, meta[:min(1, len(meta))], "data_class|"+c.note)
		}
	}
	// A product the principal does not hold is refused as before, and says nothing of its class.
	w := request(h, "GET", "/api/reporting/v1/catalog?integration_id=elsewhere", nil, true)
	if w.Code != 403 || strings.Contains(w.Body.String(), "data_class") {
		t.Errorf("a product the principal does not hold: %d %s", w.Code, w.Body.String())
	}
}
