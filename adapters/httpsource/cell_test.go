package httpsource_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
)

// The cell and the wire (S26-A, ADR 0028 decision 1). In the core a row is a list of cells; on the wire of this revision of the
// protocol it is still a list of texts, and a total a text, because no column can declare that it may be absent (S26-B adds the
// declaration and, with it, null on the wire). So nothing a product sends or is sent has changed.

// A null in a page, as a cell or as a total, is an answer that is not the protocol's JSON: it is refused where every null in an answer
// is refused, by the strict decoder, in its words, and nothing of the page is passed on. A page that verifies is passed on as cells,
// each with the value the source wrote.
func TestAPageHoldsTextsAndANullInItIsNoAnswer(t *testing.T) {
	q, hash := query()
	const notJSON = "http source pages: the answer is not one strict JSON object of the protocol's shape: INTEGRITY_FAILURE"
	rows := func(m map[string]any) []any { return m["rows"].([]any) }
	for name, f := range map[string]func(m map[string]any){
		"a null cell of a dimension": func(m map[string]any) { rows(m)[0].([]any)[0] = nil },
		"a null cell of a metric":    func(m map[string]any) { row := rows(m)[0].([]any); row[len(row)-1] = nil },
		"a null row":                 func(m map[string]any) { rows(m)[0] = nil },
		"a number as a cell":         func(m map[string]any) { rows(m)[0].([]any)[0] = 1 },
		"a null total": func(m map[string]any) {
			totals := m["totals"].(map[string]any)
			for k := range totals {
				totals[k] = nil
			}
		},
	} {
		g := newRig(t)
		p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
		if err != nil {
			t.Fatal(err)
		}
		g.set(edit("/pages", f))
		page, err := g.adapter.ReadPage(ctx, g.sc, p, "", 50)
		want(t, err, r.ErrIntegrity, name)
		noLeak(t, err, g.srv)
		if !strings.HasPrefix(err.Error(), notJSON) {
			t.Errorf("%s is refused as %q, want %q", name, err, notJSON)
		}
		if !reflect.DeepEqual(page, r.SourcePage{}) {
			t.Errorf("%s: something of the page was passed on: %+v", name, page)
		}
	}
	g := newRig(t)
	p, err := g.adapter.PrepareRead(ctx, g.sc, q, hash)
	if err != nil {
		t.Fatal(err)
	}
	page, err := g.adapter.ReadPage(ctx, g.sc, p, "", 50)
	if err != nil || len(page.Rows) == 0 || len(page.Totals) == 0 {
		t.Fatal(err, len(page.Rows))
	}
	for _, row := range page.Rows {
		for _, c := range row {
			if c.IsNull() {
				t.Fatal("a cell of a page that verified is without a value")
			}
		}
	}
	for k, c := range page.Totals {
		if v, ok := c.Value(); !ok || v == "" {
			t.Fatalf("the total %s of a page that verified is %q", k, v)
		}
	}
}

// A group's key is written on the wire as texts. A key that holds a cell without a value is not well formed (no dimension can be
// without one yet), and the adapter refuses the intent before anything is sent, so the wire never carries an empty text in the place
// of a value that is absent.
func TestAnIntentWhoseKeyHoldsACellWithoutAValueIsNotSent(t *testing.T) {
	o := watchWire(t)
	a := adapterOf(t, o.srv)
	q, hash := query()
	p, err := a.PrepareRead(ctx, o.sc, q, hash)
	if err != nil {
		t.Fatal(err)
	}
	before := o.requests()
	group := &r.IssueGroup{Fields: []string{q.Dimensions[0]}, Key: []r.Cell{r.NoValue()}, Rows: []int{0}, RowsSHA256: strings.Repeat("0", 64)}
	_, err = a.(r.DocumentAuthority).PrepareDocument(ctx, o.sc, r.IssueIntent{JobID: r.NewID(), Prepared: p, TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: strings.Repeat("0", 64), Group: group})
	want(t, err, r.ErrInvalid, "a group whose key holds a cell without a value")
	if n := o.requests() - before; n != 0 {
		t.Fatalf("the intent reached the source: %d requests", n)
	}
	// The same intent with a value in the key is sent (and refused by the source, whose rows the digest of zeros is not).
	group.Key = r.Cells("customer_a")
	_, err = a.(r.DocumentAuthority).PrepareDocument(ctx, o.sc, r.IssueIntent{JobID: r.NewID(), Prepared: p, TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: strings.Repeat("0", 64), Group: group})
	if n := o.requests() - before; n != 1 || err == nil {
		t.Fatalf("the intent with a value in its key: %d requests, %v", n, err)
	}
	var sent struct {
		Intent struct {
			Group struct {
				Key []any `json:"key"`
			} `json:"group"`
		} `json:"intent"`
	}
	if e := json.Unmarshal(o.body("prepare"), &sent); e != nil || len(sent.Intent.Group.Key) != 1 || sent.Intent.Group.Key[0] != "customer_a" {
		t.Fatalf("the key on the wire is %v (%v)", sent.Intent.Group.Key, e)
	}
}

// holed is an adapter that answers the pages of another with something changed.
type holed struct {
	r.Adapter
	change func(*r.SourcePage)
}

func (h holed) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	page, e := h.Adapter.ReadPage(ctx, sc, p, cursor, limit)
	if e == nil {
		page.Rows = append([][]r.Cell{}, page.Rows...)
		for i := range page.Rows {
			page.Rows[i] = append([]r.Cell{}, page.Rows[i]...)
		}
		totals := map[string]r.Cell{}
		for k, v := range page.Totals {
			totals[k] = v
		}
		page.Totals = totals
		h.change(&page)
	}
	return page, e
}

// The reference source writes this revision's wire: an adapter behind it that answers a cell without a value, in a row or as a total,
// answers what the wire cannot carry, and the source says so (integrity) and writes no page. Nothing the simulators answer is such a
// cell, so the reference source serves them as it did.
func TestTheReferenceSourceWritesNoCellWithoutAValue(t *testing.T) {
	sim := contractsim.Financial()
	owner := contractsim.DemoOwner(contractsim.DemoPrincipal(), contractsim.FinanceIntegration)
	sim.Grant(owner, contractsim.DefaultGrant())
	sc := r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	q, hash := query()
	for name, change := range map[string]func(*r.SourcePage){
		"a cell":  func(page *r.SourcePage) { page.Rows[0][0] = r.NoValue() },
		"a total": func(page *r.SourcePage) { page.Totals[q.Metrics[0].ID] = r.NoValue() },
	} {
		remote := sourcetest.Remote(t, holed{sim, change})
		p, err := remote.PrepareRead(ctx, sc, q, hash)
		if err != nil {
			t.Fatal(err)
		}
		page, err := remote.ReadPage(ctx, sc, p, "", 50)
		want(t, err, r.ErrIntegrity, name+" without a value behind the reference source")
		if !reflect.DeepEqual(page, r.SourcePage{}) {
			t.Errorf("%s: something of the page was passed on: %+v", name, page)
		}
	}
	remote := sourcetest.Remote(t, holed{sim, func(*r.SourcePage) {}})
	p, err := remote.PrepareRead(ctx, sc, q, hash)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := sim.ReadPage(ctx, sc, p, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := remote.ReadPage(ctx, sc, p, "", 50); err != nil || !reflect.DeepEqual(page.Rows, direct.Rows) || !reflect.DeepEqual(page.Totals, direct.Totals) {
		t.Fatalf("the page through the reference source is not the simulator's: %v", err)
	}
}
