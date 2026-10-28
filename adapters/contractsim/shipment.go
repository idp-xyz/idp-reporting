package contractsim

import r "idp.local/reporting/reporting"

const ShipmentIntegration = "shipment-contract-demo"

func ShipmentQuery() r.QuerySpec {
	return r.QuerySpec{SchemaVersion: "1.0", Integration: ShipmentIntegration, Dataset: r.Ref{ID: "shipments.daily", Version: "1.0.0"}, Companies: []string{"company_demo"}, Metrics: []r.Ref{{ID: "shipment.count", Version: "1.0.0"}}, Dimensions: []string{"shipment.destination_country", "shipment.service_code"}, Filters: []r.Filter{}, Sort: []r.Sort{}, AsOf: "2026-10-26", Timezone: "Asia/Shanghai", Page: r.PageSpec{Limit: 50}}
}
func Shipment() *Simulator {
	q := ShipmentQuery()
	d := r.Dataset{Ref: q.Dataset, Dimensions: q.Dimensions, Metrics: q.Metrics, Timezone: q.Timezone, Columns: []r.Column{{ID: "shipment.destination_country", Label: "目的国", Type: "string"}, {ID: "shipment.service_code", Label: "服务", Type: "string"}, {ID: "shipment.count", Label: "运单数", Type: "integer", Unit: "shipment"}}}
	s := New(d, []Record{{Company: "company_demo", Key: "r1", Values: []string{"CN", "express", "9007199254740993"}}, {Company: "company_demo", Key: "r2", Values: []string{"DE", "standard", "7"}}, {Company: "company_demo", Key: "secret", Values: []string{"US", "hidden", "999"}}, {Company: "company_other", Key: "r1", Values: []string{"GB", "other_company", "666"}}})
	s.prefix = "SH"
	return s
}
