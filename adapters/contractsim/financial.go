package contractsim

import (
	r "idp.local/reporting/reporting"
	"slices"
)

const FinanceIntegration = "brant-contract-demo"

func DemoPrincipal() r.Principal {
	return r.Principal{Identity: r.Identity{Tenant: "platform_demo", Issuer: "urn:idp:contract-demo", Subject: "user_a"}, Integrations: []string{FinanceIntegration}}
}
func DemoOwner(p r.Principal, integration string) r.Owner {
	return r.Owner{Identity: p.Identity, Integration: integration}
}
func DefaultGrant() Grant {
	return Grant{Companies: []string{"company_demo"}, Rows: []string{"r1", "r2", "r3"}, Timezone: "Asia/Shanghai", CurrencyPolicy: &r.Ref{ID: "ledger_base", Version: "1.0.0"}, IssueDocuments: true}
}
func FinancialQuery() r.QuerySpec {
	return r.QuerySpec{SchemaVersion: "1.0", Integration: FinanceIntegration, Dataset: r.Ref{ID: "receivables.aging", Version: "1.0.0"}, Companies: []string{"company_demo"}, Metrics: []r.Ref{{ID: "ar.open_balance", Version: "1.0.0"}}, Dimensions: []string{"customer.id", "aging.bucket"}, Filters: []r.Filter{}, Sort: []r.Sort{}, AsOf: "2026-10-26", Timezone: "Asia/Shanghai", CurrencyPolicy: &r.Ref{ID: "ledger_base", Version: "1.0.0"}, Page: r.PageSpec{Limit: 50}}
}

// AgingBuckets are the closed values of the receivables fixture's `aging.bucket` filter: the buckets of the fixture's rows (current, 1_30,
// 31_60) and the two the ledger would have beyond them.
var AgingBuckets = []string{"current", "1_30", "31_60", "61_90", "90+"}

// Financial is the receivables simulator. Its dataset version declares what a query may narrow by and order by (S20-A, ADR 0023): the
// customer by equality, inequality, a list or a prefix; the aging bucket by equality or a list, from AgingBuckets; the open balance by a
// bound or a range; the rows ordered by the open balance or the customer, either way.
func Financial() *Simulator {
	q := FinancialQuery()
	d := r.Dataset{Ref: q.Dataset, Dimensions: q.Dimensions, Metrics: q.Metrics, CurrencyPolicy: q.CurrencyPolicy, Timezone: q.Timezone, Columns: []r.Column{{ID: "customer.id", Label: "客户", Type: "string"}, {ID: "aging.bucket", Label: "账龄区间", Type: "string"}, {ID: "ar.open_balance", Label: "应收余额", Type: "decimal", Scale: 2, Currency: "CNY"}},
		Filters: []r.FilterSpec{
			{Field: "customer.id", Operators: []string{r.OpEq, r.OpNe, r.OpIn, r.OpPrefix}},
			{Field: "aging.bucket", Operators: []string{r.OpEq, r.OpIn}, Values: slices.Clone(AgingBuckets)},
			{Field: "ar.open_balance", Operators: []string{r.OpLt, r.OpLe, r.OpGt, r.OpGe, r.OpBetween}},
		},
		Sorts: []r.SortSpec{
			{Field: "ar.open_balance", Directions: []string{"asc", "desc"}},
			{Field: "customer.id", Directions: []string{"asc", "desc"}},
		}}
	s := New(d, []Record{{Company: "company_demo", Key: "r1", Values: []string{"customer_a", "current", "100.25"}}, {Company: "company_demo", Key: "r2", Values: []string{"customer_b", "1_30", "-0.24"}}, {Company: "company_demo", Key: "r3", Values: []string{"customer_c", "31_60", "9007199254740893.00"}}, {Company: "company_demo", Key: "secret", Values: []string{"hidden", "current", "999.99"}}, {Company: "company_other", Key: "r1", Values: []string{"other_company", "current", "666.66"}}})
	s.prefix = "AR"
	return s
}
