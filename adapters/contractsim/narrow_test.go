package contractsim_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// page reads the frozen fixture with the query's filters and sort applied: the rows and the totals of the narrowed scope.
func page(t *testing.T, seed contractsim.Fixture, q r.QuerySpec) r.SourcePage {
	t.Helper()
	q = r.Normalize(q)
	p := r.PreparedRead{Ref: "frozen_ref", QueryHash: r.QueryHash(q), Watermark: "original"}
	out, e := contractsim.ReadFrozenPage(context.Background(), seed, q, contractsim.DefaultGrant(), p, "", 100)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func customers(p r.SourcePage) []string {
	out := []string{}
	for _, row := range p.Rows {
		out = append(out, row[0])
	}
	return out
}

func TestTheFixtureAppliesEveryDeclaredFilterAndTotalsTheNarrowedRows(t *testing.T) {
	seed := contractsim.Financial().Fixture()
	if len(seed.Dataset.Filters) != 3 || len(seed.Dataset.Sorts) != 2 || !slices.Equal(seed.Dataset.Filters[1].Values, contractsim.AgingBuckets) {
		t.Fatalf("%+v", seed.Dataset)
	}
	// The granted scope: customer_a current 100.25, customer_b 1_30 -0.24, customer_c 31_60 9007199254740893.00.
	for _, c := range []struct {
		filter r.Filter
		rows   []string
		total  string
	}{
		{r.Filter{Field: "customer.id", Operator: "eq", Value: "customer_b"}, []string{"customer_b"}, "-0.24"},
		{r.Filter{Field: "customer.id", Operator: "ne", Value: "customer_b"}, []string{"customer_a", "customer_c"}, "9007199254740993.25"},
		{r.Filter{Field: "customer.id", Operator: "in", Value: `["customer_c","customer_a","nobody"]`}, []string{"customer_a", "customer_c"}, "9007199254740993.25"},
		{r.Filter{Field: "customer.id", Operator: "prefix", Value: "customer_"}, []string{"customer_a", "customer_b", "customer_c"}, "9007199254740993.01"},
		{r.Filter{Field: "customer.id", Operator: "prefix", Value: "ustomer"}, []string{}, "0.00"},
		{r.Filter{Field: "aging.bucket", Operator: "eq", Value: "current"}, []string{"customer_a"}, "100.25"},
		{r.Filter{Field: "aging.bucket", Operator: "eq", Value: "90+"}, []string{}, "0.00"},
		{r.Filter{Field: "aging.bucket", Operator: "in", Value: `["1_30","31_60"]`}, []string{"customer_b", "customer_c"}, "9007199254740892.76"},
		{r.Filter{Field: "ar.open_balance", Operator: "lt", Value: "100.25"}, []string{"customer_b"}, "-0.24"},
		{r.Filter{Field: "ar.open_balance", Operator: "le", Value: "100.25"}, []string{"customer_a", "customer_b"}, "100.01"},
		{r.Filter{Field: "ar.open_balance", Operator: "gt", Value: "100.25"}, []string{"customer_c"}, "9007199254740893.00"},
		{r.Filter{Field: "ar.open_balance", Operator: "ge", Value: "100.25"}, []string{"customer_a", "customer_c"}, "9007199254740993.25"},
		{r.Filter{Field: "ar.open_balance", Operator: "ge", Value: "9007199254740892.99"}, []string{"customer_c"}, "9007199254740893.00"}, // exact, not a float's 2^53
		{r.Filter{Field: "ar.open_balance", Operator: "between", Value: `["-0.24","100.25"]`}, []string{"customer_a", "customer_b"}, "100.01"},
		{r.Filter{Field: "ar.open_balance", Operator: "between", Value: `["-0.23","100.24"]`}, []string{}, "0.00"},
	} {
		q := contractsim.FinancialQuery()
		q.Filters = []r.Filter{c.filter}
		got := page(t, seed, q)
		if !slices.Equal(customers(got), c.rows) || got.Totals["ar.open_balance"] != c.total || got.NextCursor != "" {
			t.Errorf("%s %s %q: %v %v", c.filter.Field, c.filter.Operator, c.filter.Value, customers(got), got.Totals)
		}
	}
	// Filters are a conjunction.
	q := contractsim.FinancialQuery()
	q.Filters = []r.Filter{{Field: "ar.open_balance", Operator: "ge", Value: "0.00"}, {Field: "customer.id", Operator: "in", Value: `["customer_a","customer_b"]`}}
	if got := page(t, seed, q); !slices.Equal(customers(got), []string{"customer_a"}) || got.Totals["ar.open_balance"] != "100.25" {
		t.Fatalf("%v %v", customers(got), got.Totals)
	}
	// The shipments fixture declares its own; an integer bound compares exactly.
	ship := contractsim.Shipment().Fixture()
	sq := contractsim.ShipmentQuery()
	sq.Filters = []r.Filter{{Field: "shipment.count", Operator: "ge", Value: "9007199254740993"}}
	if got := page(t, ship, sq); len(got.Rows) != 1 || got.Rows[0][0] != "CN" || got.Totals["shipment.count"] != "9007199254740993" {
		t.Fatalf("%+v", got)
	}
	sq.Filters = []r.Filter{{Field: "shipment.destination_country", Operator: "in", Value: `["DE","US"]`}, {Field: "shipment.service_code", Operator: "eq", Value: "standard"}}
	if got := page(t, ship, sq); len(got.Rows) != 1 || got.Rows[0][0] != "DE" || got.Totals["shipment.count"] != "7" {
		t.Fatalf("%+v", got)
	}
}

func TestTheFixtureSortsStablyAndTheSameAcrossPages(t *testing.T) {
	seed := contractsim.Financial().Fixture()
	for _, c := range []struct {
		sort []r.Sort
		rows []string
	}{
		{[]r.Sort{{Field: "ar.open_balance", Direction: "asc"}}, []string{"customer_b", "customer_a", "customer_c"}},
		{[]r.Sort{{Field: "ar.open_balance", Direction: "desc"}}, []string{"customer_c", "customer_a", "customer_b"}},
		{[]r.Sort{{Field: "customer.id", Direction: "desc"}}, []string{"customer_c", "customer_b", "customer_a"}},
		{[]r.Sort{{Field: "customer.id", Direction: "asc"}, {Field: "ar.open_balance", Direction: "desc"}}, []string{"customer_a", "customer_b", "customer_c"}},
	} {
		q := contractsim.FinancialQuery()
		q.Sort = c.sort
		if got := page(t, seed, q); !slices.Equal(customers(got), c.rows) || got.Totals["ar.open_balance"] != "9007199254740993.01" {
			t.Errorf("%v: %v", c.sort, customers(got))
		}
	}
	// 120 rows whose balance repeats every 10 rows: sorted by balance descending, the rows of one balance keep the fixture's order (stable),
	// and the order is the same read page by page from a prepared read as read whole.
	var records []contractsim.Record
	var keys []string
	for i := 0; i < 120; i++ {
		key := fmt.Sprintf("r%03d", i)
		records = append(records, contractsim.Record{Company: "company_demo", Key: key, Values: []string{fmt.Sprintf("customer_%03d", i), "current", fmt.Sprintf("%d.00", i%10)}})
		keys = append(keys, key)
	}
	sim := contractsim.New(seed.Dataset, records)
	p := contractsim.DemoPrincipal()
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	g := contractsim.DefaultGrant()
	g.Rows = keys
	sim.Grant(owner, g)
	sc := r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	q := r.Normalize(contractsim.FinancialQuery())
	q.Sort = []r.Sort{{Field: "ar.open_balance", Direction: "desc"}}
	q.Filters = []r.Filter{{Field: "ar.open_balance", Operator: "ge", Value: "5.00"}}
	ctx := context.Background()
	prepared, e := sim.PrepareRead(ctx, sc, q, r.QueryHash(q))
	if e != nil {
		t.Fatal(e)
	}
	var paged [][]string
	cursor := ""
	for pages := 0; ; pages++ {
		pg, e := sim.ReadPage(ctx, sc, prepared, cursor, 7)
		if e != nil || pg.Totals["ar.open_balance"] != "420.00" { // 12 rows each of 5, 6, 7, 8 and 9: 12 × 35
			t.Fatal(pg.Totals, e)
		}
		paged = append(paged, pg.Rows...)
		if pg.NextCursor == "" {
			if pages != 8 { // 60 rows in pages of 7
				t.Fatal(pages)
			}
			break
		}
		cursor = pg.NextCursor
	}
	whole, e := sim.ReadPage(ctx, sc, prepared, "", 100)
	if e != nil || len(whole.Rows) != 60 || !slices.EqualFunc(whole.Rows, paged, slices.Equal) {
		t.Fatal(len(whole.Rows), e)
	}
	for i, row := range whole.Rows {
		wantBalance := fmt.Sprintf("%d.00", 9-i/12)
		wantCustomer := fmt.Sprintf("customer_%03d", (9-i/12)+10*(i%12))
		if row[2] != wantBalance || row[0] != wantCustomer {
			t.Fatalf("row %d: %v, want %s %s", i, row, wantCustomer, wantBalance)
		}
	}
}

func TestTheSimulatorRefusesWhatItsVersionDoesNotDeclare(t *testing.T) {
	sim := contractsim.Financial()
	p := contractsim.DemoPrincipal()
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	sc := r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	ctx := context.Background()
	for name, change := range map[string]func(q *r.QuerySpec){
		"an undeclared field": func(q *r.QuerySpec) {
			q.Filters = []r.Filter{{Field: "aging.bucket", Operator: "prefix", Value: "cur"}}
		},
		"an undeclared operator":      func(q *r.QuerySpec) { q.Filters = []r.Filter{{Field: "customer.id", Operator: "contains", Value: "a"}} },
		"an operator outside the set": func(q *r.QuerySpec) { q.Filters = []r.Filter{{Field: "customer.id", Operator: "like", Value: "a%"}} },
		"a value outside the list": func(q *r.QuerySpec) {
			q.Filters = []r.Filter{{Field: "aging.bucket", Operator: "in", Value: `["current","91+"]`}}
		},
		"a malformed decimal": func(q *r.QuerySpec) { q.Filters = []r.Filter{{Field: "ar.open_balance", Operator: "lt", Value: "100"}} },
		"a between out of order": func(q *r.QuerySpec) {
			q.Filters = []r.Filter{{Field: "ar.open_balance", Operator: "between", Value: `["1.00","0.00"]`}}
		},
		"an undeclared sort": func(q *r.QuerySpec) { q.Sort = []r.Sort{{Field: "aging.bucket", Direction: "asc"}} },
		"a sort field twice": func(q *r.QuerySpec) {
			q.Sort = []r.Sort{{Field: "customer.id", Direction: "asc"}, {Field: "customer.id", Direction: "desc"}}
		},
	} {
		q := r.Normalize(contractsim.FinancialQuery())
		change(&q)
		if _, e := sim.PrepareRead(ctx, sc, q, r.QueryHash(q)); !errors.Is(e, r.ErrUnsupported) {
			t.Errorf("%s: %v", name, e)
		}
	}
	// A frozen query the version does not publish is refused by the projection too (a stored read is checked again when it is read).
	seed := sim.Fixture()
	q := r.Normalize(contractsim.FinancialQuery())
	q.Filters = []r.Filter{{Field: "aging.bucket", Operator: "prefix", Value: "cur"}}
	prep := r.PreparedRead{Ref: "frozen_ref", QueryHash: r.QueryHash(q), Watermark: "original"}
	if _, e := contractsim.ReadFrozenPage(ctx, seed, q, contractsim.DefaultGrant(), prep, "", 10); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal(e)
	}
	// A version that declares nothing is what it was: no filter, no sort; the grant is unchanged by any of this.
	plain := seed
	plain.Dataset.Filters, plain.Dataset.Sorts = nil, nil
	bare := contractsim.New(plain.Dataset, plain.Records)
	bare.Grant(owner, contractsim.DefaultGrant())
	q = r.Normalize(contractsim.FinancialQuery())
	q.Sort = []r.Sort{{Field: "customer.id", Direction: "asc"}}
	if _, e := bare.PrepareRead(ctx, sc, q, r.QueryHash(q)); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal(e)
	}
	if ds, e := bare.DescribeCatalog(ctx, sc); e != nil || ds[0].Filters != nil || ds[0].Sorts != nil {
		t.Fatal(ds, e)
	}
	other := sc
	other.Owner.Subject = "user_b"
	if _, e := sim.DescribeCatalog(ctx, other); !errors.Is(e, r.ErrDenied) {
		t.Fatal(e)
	}
}
