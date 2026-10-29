package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Schema 2.0 over HTTP (S03-J): previews and checks bind the group of a row, a refusal says why and where, a
// 2.0 template is saved and published like any (it is issued by group: issue_group_test.go, S03-K).

// bandedRenderer renders and checks like the PDF writer's two entry points, without Typst: a preview answers a
// small PDF and records what it was given; a check records it too and may refuse.
type bandedRenderer struct {
	renders []checkCall
	checks  []checkCall
	refuse  func(call checkCall) error
}

func (b *bandedRenderer) Render(_ context.Context, _ doctemplates.Document, values map[string]string, _ []byte, table *doctemplates.TableData) ([]byte, error) {
	call := checkCall{values: values, table: table}
	b.renders = append(b.renders, call)
	if b.refuse != nil {
		if e := b.refuse(call); e != nil {
			return nil, e
		}
	}
	return []byte("%PDF-1.7\nbanded preview\n%%EOF"), nil
}

func (b *bandedRenderer) CheckIssue(_ context.Context, _ doctemplates.Document, values map[string]string, _ []byte, table *doctemplates.TableData) error {
	call := checkCall{values: values, table: table}
	b.checks = append(b.checks, call)
	if b.refuse != nil {
		return b.refuse(call)
	}
	return nil
}

// bandedFixture is the check fixture's service and snapshot with a handler over the banded renderer.
type bandedFixture struct {
	*checkFixture
	banded   *bandedRenderer
	h        http.Handler
	customer []string
}

func newBandedFixture(t *testing.T, customers []string) *bandedFixture {
	t.Helper()
	f := &bandedFixture{checkFixture: newCheckFixture(t, customers, false), banded: &bandedRenderer{}, customer: customers}
	auth, _ := httpapi.DemoBearer(secret, contractsim.DemoPrincipal())
	fallback, _ := httpapi.Handler(f.service, auth)
	var e error
	if f.h, e = httpapi.WithDocumentPreviews(fallback, f.service, auth, f.banded, nil); e != nil {
		t.Fatal(e)
	}
	return f
}

// template is a 2.0 template grouped by customer: the customer and the bucket as fields, a table of the
// lines and the sum of the amounts in the document footer.
func (f *bandedFixture) template() doctemplates.Document {
	text := func(id, source, v string, y float64) doctemplates.Item {
		it := doctemplates.Item{ID: id, Type: "text", Source: source, X: 0, Y: y, Width: 100, Height: 8, FontSize: 10, Align: "left"}
		if source == "literal" {
			it.Text = v
		} else {
			it.Field = v
		}
		return it
	}
	return doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset},
		Group: &doctemplates.GroupSpec{Fields: []string{"customer.id"}},
		Bands: &doctemplates.Bands{
			PageHeader: &doctemplates.Band{Height: 20, Elements: []doctemplates.Item{text("e1", "field", "customer.id", 0), text("e2", "field", "aging.bucket", 9)}},
			Detail: &doctemplates.DetailBand{Elements: []doctemplates.Item{{ID: "e3", Type: "table", X: 0, Width: 190, FontSize: 10, Columns: []doctemplates.ItemColumn{
				{Field: "customer.id", Width: 60}, {Field: "aging.bucket", Width: 60}, {Field: "ar.open_balance", Width: 70}}}}},
			DocumentFooter: &doctemplates.Band{Height: 15, Elements: []doctemplates.Item{text("e4", "group_sum", "ar.open_balance", 0)}},
		}}
}

// bandedAnswer is a problem answer and a check answer, with their details.
type bandedAnswer struct {
	Error  string               `json:"error"`
	Detail *doctemplates.Detail `json:"detail"`
	Rows   []struct {
		RowIndex int                  `json:"row_index"`
		OK       bool                 `json:"ok"`
		Code     string               `json:"code"`
		Detail   *doctemplates.Detail `json:"detail"`
		Reasons  []string             `json:"reasons"`
	} `json:"rows"`
}

func (f *bandedFixture) call(path string, input map[string]any) (int, bandedAnswer, string) {
	f.t.Helper()
	if _, ok := input["snapshot_id"]; !ok {
		input["snapshot_id"] = f.snapshot
	}
	body, _ := json.Marshal(input)
	w := request(f.h, "POST", "/api/reporting/v1/"+path, body, true)
	var answer bandedAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &answer)
	return w.Code, answer, w.Body.String()
}

var sixOrders = []string{"A", "B", "A", "C", "B", "A"}

func TestBandedPreviewRendersTheGroupOfTheRow(t *testing.T) {
	f := newBandedFixture(t, sixOrders)
	// Row 2 is one of the three rows of customer A (0, 2, 5): the preview is that group's document.
	code, _, body := f.call("document-previews", map[string]any{"config": f.template(), "row_index": 2})
	if code != 200 || !strings.HasPrefix(body, "%PDF-") || len(f.banded.renders) != 1 {
		t.Fatalf("%d %q", code, body)
	}
	call := f.banded.renders[0]
	want := map[string]string{"e1": "A", "e2": "current", "e4": "307.00"}
	if !reflect.DeepEqual(call.values, want) {
		t.Errorf("values %v, want %v", call.values, want)
	}
	if call.table == nil || !reflect.DeepEqual(call.table.Index, []int{0, 2, 5}) || len(call.table.Rows) != 3 || call.table.Rows[1][2] != "102.00" || len(call.table.Totals) != 0 {
		t.Errorf("table %+v", call.table)
	}
	// Any row of the group previews the same document; another group another.
	f.banded.renders = nil
	for row, sum := range map[int]string{0: "307.00", 5: "307.00", 1: "205.00", 4: "205.00", 3: "103.00"} {
		if code, _, body := f.call("document-previews", map[string]any{"config": f.template(), "row_index": row}); code != 200 {
			t.Fatalf("row %d: %d %s", row, code, body)
		}
		if got := f.banded.renders[len(f.banded.renders)-1].values["e4"]; got != sum {
			t.Errorf("row %d: sum %s, want %s", row, got, sum)
		}
	}
	// Without a group the preview is the whole snapshot's document, whatever the row; its fields must agree over every
	// row, and here the customers differ.
	whole := f.template()
	whole.Group = nil
	code, answer, _ := f.call("document-previews", map[string]any{"config": whole, "row_index": 0})
	if code != 422 || answer.Error != "UNSUPPORTED_COMBINATION" || answer.Detail == nil || answer.Detail.Reason != "INCONSISTENT_GROUP" || answer.Detail.Band != "page_header" || answer.Detail.Element != "e1" || answer.Detail.Column != "customer.id" || answer.Detail.Row == nil || *answer.Detail.Row != 1 {
		t.Errorf("an inconsistent field: %d %+v", code, answer)
	}
	// A row the snapshot lacks is an invalid request; so is a missing one.
	if code, _, _ := f.call("document-previews", map[string]any{"config": f.template(), "row_index": 6}); code != 400 {
		t.Errorf("row 6: %d", code)
	}
	if code, _, _ := f.call("document-previews", map[string]any{"config": f.template()}); code != 400 {
		t.Errorf("no row: %d", code)
	}
	// A 1.x template previews as it always did: its box values and every row of the table.
	f.banded.renders = nil
	one := f.config()
	if code, _, _ := f.call("document-previews", map[string]any{"config": one, "row_index": 1}); code != 200 || len(f.banded.renders) != 1 || f.banded.renders[0].values["n1"] != "B" {
		t.Errorf("a 1.x preview: %d %+v", code, f.banded.renders)
	}
}

func TestBandedRefusalsAnswerTheirReasonAndPlace(t *testing.T) {
	f := newBandedFixture(t, sixOrders)
	row := 4
	f.banded.refuse = func(checkCall) error {
		return doctemplates.Refuse(fmt.Errorf("pdf: an amount does not fit its column: %w", r.ErrUnsupported), doctemplates.Detail{Reason: "DOES_NOT_FIT", Band: "detail", Element: "e3", Column: "ar.open_balance", Row: &row})
	}
	code, answer, body := f.call("document-previews", map[string]any{"config": f.template(), "row_index": 1})
	if code != 422 || answer.Error != "UNSUPPORTED_COMBINATION" || answer.Detail == nil || *answer.Detail != (doctemplates.Detail{Reason: "DOES_NOT_FIT", Band: "detail", Element: "e3", Column: "ar.open_balance", Row: &row}) && !reflect.DeepEqual(*answer.Detail, doctemplates.Detail{Reason: "DOES_NOT_FIT", Band: "detail", Element: "e3", Column: "ar.open_balance", Row: &row}) {
		t.Fatalf("%d %s", code, body)
	}
	// The answer keeps its two keys' names and carries no value: not a customer, not an amount.
	for _, v := range []string{"\"A\"", "\"B\"", "102.00", "205.00"} {
		if strings.Contains(body, v) {
			t.Errorf("the answer carries %s: %s", v, body)
		}
	}
	// Any other failure of a route has no detail, as it never had.
	f.banded.refuse = func(checkCall) error { return r.ErrIntegrity }
	if code, answer, body := f.call("document-previews", map[string]any{"config": f.template(), "row_index": 1}); code != 409 || answer.Detail != nil || strings.TrimSpace(body) != `{"error":"INTEGRITY_FAILURE"}` {
		t.Errorf("an integrity failure: %d %s", code, body)
	}
	// A 1.x document is refused where it is bound, with the element: a value of 300 characters in n1.
	long := newBandedFixture(t, []string{strings.Repeat("甲", 300), "b"})
	if code, answer, body := long.call("document-previews", map[string]any{"config": long.config(), "row_index": 0}); code != 422 || answer.Detail == nil || answer.Detail.Reason != "DOES_NOT_FIT" || answer.Detail.Element != "n1" || answer.Detail.Band != "" || strings.Contains(body, "甲") {
		t.Errorf("a long 1.x value: %d %s", code, body)
	}
	// The same through a check: the refused row carries the code and the detail, the others pass.
	code, answer, body = long.call("document-checks", map[string]any{"config": long.config(), "rows": []int{0, 1}})
	if code != 200 || len(answer.Rows) != 2 || answer.Rows[0].OK || answer.Rows[0].Code != "UNSUPPORTED_COMBINATION" || answer.Rows[0].Detail == nil || answer.Rows[0].Detail.Element != "n1" || answer.Rows[0].Detail.Reason != "DOES_NOT_FIT" || !answer.Rows[1].OK || answer.Rows[1].Detail != nil {
		t.Errorf("a 1.x check: %d %s", code, body)
	}
	if strings.Contains(body, "甲") {
		t.Errorf("the check carries a value: %s", body)
	}
}

func TestBandedChecksAnswerPerRowWithTheResultOfTheGroup(t *testing.T) {
	f := newBandedFixture(t, sixOrders)
	// Rows 0 and 2 are customer A's, row 3 customer C's: A is checked once, and both its rows say the same.
	code, answer, body := f.call("document-checks", map[string]any{"config": f.template(), "rows": []int{0, 2, 3}})
	if code != 200 || len(answer.Rows) != 3 || len(f.banded.checks) != 2 || len(f.banded.renders) != 0 {
		t.Fatalf("%d %s checks=%d", code, body, len(f.banded.checks))
	}
	for i, row := range []int{0, 2, 3} {
		if answer.Rows[i].RowIndex != row || !answer.Rows[i].OK || answer.Rows[i].Code != "" || answer.Rows[i].Detail != nil {
			t.Errorf("row %d: %+v", row, answer.Rows[i])
		}
	}
	// A refusal of one group is the result of each of its rows, with the same code and detail; the others still pass.
	f.banded.checks = nil
	f.banded.refuse = func(call checkCall) error {
		if call.values["e1"] == "A" {
			return doctemplates.Refuse(fmt.Errorf("pdf: a value does not fit: %w", r.ErrUnsupported), doctemplates.Detail{Reason: "DOES_NOT_FIT", Band: "page_header", Element: "e1"})
		}
		return nil
	}
	code, answer, body = f.call("document-checks", map[string]any{"config": f.template(), "rows": []int{0, 1, 2, 3, 4, 5}})
	if code != 200 || len(answer.Rows) != 6 || len(f.banded.checks) != 3 {
		t.Fatalf("%d %s checks=%d", code, body, len(f.banded.checks))
	}
	for i, row := range answer.Rows {
		refused := sixOrders[i] == "A"
		if row.OK == refused || (refused && (row.Code != "UNSUPPORTED_COMBINATION" || row.Detail == nil || row.Detail.Reason != "DOES_NOT_FIT" || row.Detail.Element != "e1" || row.Detail.Band != "page_header")) || (!refused && (row.Code != "" || row.Detail != nil)) {
			t.Errorf("row %d: %+v", i, row)
		}
	}
	// The group's binding refusal is that group's too, without asking the checker: the same fields differ in customer A's rows.
	f.banded.checks = nil
	f.banded.refuse = nil
	inconsistent := f.template()
	inconsistent.Bands.PageHeader.Elements[1].Field = "ar.open_balance"
	code, answer, body = f.call("document-checks", map[string]any{"config": inconsistent, "rows": []int{0, 3}})
	if code != 200 || len(answer.Rows) != 2 || answer.Rows[0].OK || answer.Rows[0].Detail == nil || answer.Rows[0].Detail.Reason != "INCONSISTENT_GROUP" || answer.Rows[0].Detail.Element != "e2" || answer.Rows[0].Detail.Column != "ar.open_balance" || answer.Rows[0].Detail.Row == nil || *answer.Rows[0].Detail.Row != 2 || !answer.Rows[1].OK || len(f.banded.checks) != 1 {
		t.Errorf("an inconsistent group: %d %s checks=%d", code, body, len(f.banded.checks))
	}
	// A failure that is not a refusal fails the request.
	f.banded.refuse = func(checkCall) error { return r.ErrIntegrity }
	if code, _, _ := f.call("document-checks", map[string]any{"config": f.template(), "rows": []int{0}}); code != 409 {
		t.Errorf("an integrity failure: %d", code)
	}
	f.banded.refuse = nil
	// Rows the snapshot lacks, and a body of both or neither of rows and samples, are invalid, as for 1.x.
	for name, input := range map[string]map[string]any{"a row the snapshot lacks": {"rows": []int{6}}, "no rows": {"rows": []int{}}, "a repeated row": {"rows": []int{1, 1}}, "samples and rows": {"rows": []int{1}, "samples": "boundary"}, "neither": {}} {
		input["config"] = f.template()
		if code, _, _ := f.call("document-checks", input); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
}

func TestBandedBoundarySamplesPickGroupsByRowCountValuesAndCells(t *testing.T) {
	// Orders: A has four lines, B two, C one, D three; the customer's names are of different widths.
	f := newBandedFixture(t, []string{"A", "BB", "A", "CCCCCC", "A", "BB", "D", "D", "A", "D"})
	code, answer, body := f.call("document-checks", map[string]any{"config": f.template(), "samples": "boundary"})
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	got := map[int][]string{}
	var order []int
	for _, row := range answer.Rows {
		got[row.RowIndex] = row.Reasons
		order = append(order, row.RowIndex)
	}
	// Groups, by first row: A (row 0), BB (1), CCCCCC (3), D (6). The picks are the groups' first rows, in row order: A
	// (first, with the most rows and, on every tie, the lowest group), CCCCCC (the fewest rows and the longest name) and
	// D (last); BB is extreme in nothing.
	if !reflect.DeepEqual(order, []int{0, 3, 6}) {
		t.Fatalf("rows %v: %s", order, body)
	}
	has := func(row int, reason string) {
		t.Helper()
		for _, have := range got[row] {
			if have == reason {
				return
			}
		}
		t.Errorf("row %d lacks %q: %v", row, reason, got[row])
	}
	has(0, "first")       // the first group
	has(6, "last")        // the last group
	has(0, "most_rows")   // A has four rows
	has(3, "fewest_rows") // CCCCCC has one
	has(3, "longest:e1")  // the longest customer name
	has(0, "shortest:e1") // the shortest, the lowest group on a tie with D
	has(0, "longest:e4")  // the largest sum by width: A's four amounts
	has(3, "cell:customer.id")
	if len(f.banded.checks) != 3 {
		t.Errorf("%d groups checked", len(f.banded.checks))
	}
	// The picks never carry a value, only indices and names; a group is a pick once however many reasons it has.
	if strings.Contains(body, "CCCCCC") {
		t.Errorf("a value in the answer: %s", body)
	}
	// A snapshot without rows has no group to sample.
	if code, answer, _ := f.call("document-checks", map[string]any{"config": func() doctemplates.Document { d := f.template(); d.Group = nil; return d }(), "samples": "boundary"}); code != 200 && code != 422 {
		t.Errorf("an ungrouped sample: %d %+v", code, answer)
	}
	// With a hundred and twenty groups of one row each, all alike, the picks are the first and the last group.
	many := make([]string, 120)
	for i := range many {
		many[i] = fmt.Sprintf("c%03d", i)
	}
	g := newBandedFixture(t, many)
	code, answer, body = g.call("document-checks", map[string]any{"config": g.template(), "samples": "boundary"})
	if code != 200 || len(answer.Rows) != 2 || answer.Rows[0].RowIndex != 0 || answer.Rows[1].RowIndex != 119 || len(g.banded.checks) != 2 {
		t.Errorf("120 groups: %d, %d rows: %s", code, len(answer.Rows), body)
	}
}

func TestBandedTemplatesAreAuthorizedAgainstTheDataset(t *testing.T) {
	f := newBandedFixture(t, sixOrders)
	for name, c := range map[string]struct {
		change func(*doctemplates.Document)
		status int
	}{
		"the valid template":             {func(d *doctemplates.Document) {}, 200},
		"a text field the dataset lacks": {func(d *doctemplates.Document) { d.Bands.PageHeader.Elements[0].Field = "ghost" }, 403},
		"a group field it lacks":         {func(d *doctemplates.Document) { d.Group.Fields = []string{"ghost"} }, 403},
		"a metric as a group field":      {func(d *doctemplates.Document) { d.Group.Fields = []string{"ar.open_balance"} }, 422},
		"a group sum of a dimension":     {func(d *doctemplates.Document) { d.Bands.DocumentFooter.Elements[0].Field = "customer.id" }, 403},
		"a table column it lacks":        {func(d *doctemplates.Document) { d.Bands.Detail.Elements[0].Columns[1].Field = "ghost" }, 403},
		"another dataset":                {func(d *doctemplates.Document) { d.Binding.Dataset.Version = "9.9.9" }, 403},
		"an invalid template":            {func(d *doctemplates.Document) { d.Bands.PageHeader.Height = 4 }, 400},
	} {
		d := f.template()
		c.change(&d)
		code, _, body := f.call("document-previews", map[string]any{"config": d, "row_index": 0})
		if code != c.status {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	// A field of the catalog the caller may no longer use is denied for a 2.0 template as for 1.x.
	f.adapter.allowed = "ar.open_balance"
	if code, _, _ := f.call("document-previews", map[string]any{"config": f.template(), "row_index": 0}); code != 403 {
		t.Errorf("a field removed from the catalog: %d", code)
	}
	f.adapter.allowed = ""
}

func TestBandedTemplatesAreSavedOpenedAndPublishedLikeAnyTemplate(t *testing.T) {
	f := newBandedFixture(t, sixOrders)
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	auth, _ := httpapi.DemoBearer(secret, contractsim.DemoPrincipal())
	fallback, _ := httpapi.Handler(f.service, auth)
	h, e := httpapi.WithDocumentTemplates(fallback, f.service, auth, store, f.banded, nil)
	if e != nil {
		t.Fatal(e)
	}
	d := f.template()
	body, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": d})
	w := request(h, "POST", "/api/reporting/v1/document-templates", body, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved struct {
		ID      string          `json:"id"`
		Version int             `json:"version"`
		Config  json.RawMessage `json:"config"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &saved); e != nil || saved.Version != 1 {
		t.Fatal(e, w.Body.String())
	}
	want, _ := doctemplates.Canonical(d)
	var a, b any
	_ = json.Unmarshal(saved.Config, &a)
	_ = json.Unmarshal(want, &b)
	if !reflect.DeepEqual(a, b) || strings.Contains(string(saved.Config), `"elements":null`) || strings.Contains(string(saved.Config), `"print"`) {
		t.Errorf("the saved form %s, want %s", saved.Config, want)
	}
	// It opens as saved and the version previews by the saved version: the group of the row.
	path := "/api/reporting/v1/document-templates/" + saved.ID
	if w := request(h, "GET", path+"?version=1", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"schema_version":"2.0"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	preview, _ := json.Marshal(map[string]any{"version": 1, "snapshot_id": f.snapshot, "row_index": 3})
	if w := request(h, "POST", path+"/previews", preview, true); w.Code != 200 || len(f.banded.renders) != 1 || f.banded.renders[0].values["e1"] != "C" {
		t.Fatal(w.Code, w.Body.String())
	}
	// It publishes and reads back.
	if w := request(h, "POST", path+"/release", []byte(`{"action":"publish","version":1,"expected_revision":0}`), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "GET", path+"/published", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"group":{"fields":["customer.id"]}`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// An invalid or unauthorized 2.0 template is not saved.
	bad := f.template()
	bad.Bands.PageHeader.Elements[0].Field = "ghost"
	body, _ = json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": bad})
	if w := request(h, "POST", "/api/reporting/v1/document-templates", body, true); w.Code != 403 {
		t.Errorf("a field the dataset lacks: %d %s", w.Code, w.Body.String())
	}
	body = []byte(`{"id":"","expected_version":0,"config":{"schema_version":"2.0","kind":"document","binding":{"integration_id":"` + contractsim.FinanceIntegration + `","dataset":{"id":"receivables.aging","version":"1.0.0"}},"elements":[],"bands":{}}}`)
	if w := request(h, "POST", "/api/reporting/v1/document-templates", body, true); w.Code != 400 {
		t.Errorf("a 2.0 template with a 1.x key: %d %s", w.Code, w.Body.String())
	}
}
