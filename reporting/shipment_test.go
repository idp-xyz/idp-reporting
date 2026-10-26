package reporting_test

import (
	"context"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"testing"
	"time"
)

func TestShipmentUsesSameCore(t *testing.T) {
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	a := contractsim.Shipment()
	g := contractsim.DefaultGrant()
	g.CurrencyPolicy = nil
	a.Grant(contractsim.DemoOwner(p, contractsim.ShipmentIntegration), g)
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901")})
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.Query(context.Background(), p, contractsim.ShipmentQuery())
	if e != nil {
		t.Fatal(e)
	}
	if result.Totals["shipment.count"] != "9007199254741000" || result.Rows[0][2] != "9007199254740993" || result.Columns[2].Type != "integer" {
		t.Fatalf("unexpected shipment result: %+v", result)
	}
}
