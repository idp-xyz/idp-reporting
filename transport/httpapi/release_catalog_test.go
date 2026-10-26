package httpapi_test

import (
	"context"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"path/filepath"
	"testing"
	"time"
)

type changingLayoutCatalog struct {
	r.Adapter
	allowed string
}

func (a *changingLayoutCatalog) DescribeCatalog(ctx context.Context, source r.SourceContext) ([]r.Dataset, error) {
	list, e := a.Adapter.DescribeCatalog(ctx, source)
	if e != nil {
		return nil, e
	}
	if a.allowed != "" {
		for i := range list {
			columns := []r.Column{}
			for _, c := range list[i].Columns {
				if c.ID == a.allowed {
					columns = append(columns, c)
				}
			}
			list[i].Columns = columns
		}
	}
	return list, nil
}
func TestPublicationChecksTargetColumnsAfterCatalogChange(t *testing.T) {
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	source.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	adapter := &changingLayoutCatalog{Adapter: source}
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: adapter}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := layouts.Open(filepath.Join(t.TempDir(), "private"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	auth, _ := httpapi.DemoBearer(secret, p)
	h, e := httpapi.Handler(service, auth, store)
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := service.Catalog(t.Context(), p, contractsim.FinanceIntegration)
	if e != nil || len(catalog[0].Columns) < 2 {
		t.Fatal(e)
	}
	d := layouts.Document{Schema: "1.0", Binding: layouts.Binding{Integration: contractsim.FinanceIntegration, Dataset: catalog[0].Ref}, Columns: []layouts.Column{{ID: catalog[0].Columns[0].ID, Align: "left"}}}
	save := func(id string, expected int) layouts.Entry {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id": id, "expected_version": expected, "layout": d})
		w := request(h, "POST", "/api/reporting/v1/layouts", body, true)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var entry layouts.Entry
		json.Unmarshal(w.Body.Bytes(), &entry)
		return entry
	}
	first := save("", 0)
	d.Columns[0].ID = catalog[0].Columns[1].ID
	save(first.ID, 1)
	path := "/api/reporting/v1/layouts/" + first.ID
	adapter.allowed = catalog[0].Columns[0].ID
	if request(h, "POST", path+"/release", []byte(`{"action":"publish","version":2,"expected_revision":0}`), true).Code != 403 {
		t.Fatal("invalid target columns published")
	}
	if request(h, "POST", path+"/release", []byte(`{"action":"publish","version":1,"expected_revision":0}`), true).Code != 200 {
		t.Fatal("valid v1 denied")
	}
	adapter.allowed = catalog[0].Columns[1].ID
	if request(h, "GET", path+"/published", nil, true).Code != 403 {
		t.Fatal("removed published columns disclosed")
	}
	if request(h, "GET", path+"/release", nil, true).Code != 200 {
		t.Fatal("owner cannot inspect binding-scoped history")
	}
	if request(h, "POST", path+"/release", []byte(`{"action":"withdraw","version":0,"expected_revision":1}`), true).Code != 200 {
		t.Fatal("cannot withdraw after column removal")
	}
	if request(h, "POST", path+"/release", []byte(`{"action":"publish","version":2,"expected_revision":2}`), true).Code != 200 {
		t.Fatal("valid v2 blocked by obsolete v1")
	}
	if request(h, "GET", path+"/published", nil, true).Code != 200 {
		t.Fatal("valid published target denied")
	}
}
