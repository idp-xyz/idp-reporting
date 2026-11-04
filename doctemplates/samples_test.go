package doctemplates_test

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

func TestTheEmbeddedPackLoadsAndEverySamplePreviews(t *testing.T) {
	all, err := doctemplates.Samples()
	if err != nil || len(all) != 37 {
		t.Fatal(len(all), err)
	}
	domains := map[string]int{}
	forms := 0
	for _, s := range all {
		domains[s.Domain]++
		values, table, err := s.BindPreview()
		// A form (no detail band) binds no table; the others bind their own rows.
		if err != nil || len(values) == 0 || (s.Document.Form() && table != nil) || (!s.Document.Form() && (table == nil || len(table.Rows) != len(s.Rows))) {
			t.Errorf("%s: %v", s.Kind, err)
		}
		if s.Document.Form() {
			forms++
		}
		if s.ElementCount() < 2 || s.Preset() == "" {
			t.Errorf("%s: counts", s.Kind)
		}
	}
	// The two waybills, the bill of lading, the two parcel labels and the four WMS labels are forms: no detail band, no stand-in table.
	if forms != 9 {
		t.Fatalf("%d forms", forms)
	}
	// The prototype's four domains.
	if domains["仓储 WMS"] != 24 || domains["运输 / 货代"] != 7 || domains["国际贸易"] != 4 || domains["通用业务"] != 2 || len(domains) != 4 {
		t.Fatal(domains)
	}
	if s, ok, _ := doctemplates.SampleByKind("usps"); !ok || s.Preset() != "label_4x6in" {
		t.Fatal("by kind")
	}
	if _, ok, _ := doctemplates.SampleByKind("nothing"); ok {
		t.Fatal("unknown kind")
	}
}

func sampleJSON(t *testing.T, edit func(m map[string]any)) []byte {
	t.Helper()
	b, err := os.ReadFile("samples/statement.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if edit != nil {
		edit(m)
	}
	out, _ := json.Marshal(m)
	return out
}

func TestASampleThatIsNotAWholeSampleIsRefused(t *testing.T) {
	if _, err := doctemplates.DecodeSample(sampleJSON(t, nil)); err != nil {
		t.Fatal(err)
	}
	doc := func(m map[string]any) map[string]any { return m["document"].(map[string]any) }
	elements := func(m map[string]any, band string) []any {
		return doc(m)["bands"].(map[string]any)[band].(map[string]any)["elements"].([]any)
	}
	for name, edit := range map[string]func(map[string]any){
		"an upper-case kind":           func(m map[string]any) { m["kind"] = "Bad" },
		"no description":               func(m map[string]any) { m["description"] = "" },
		"an unknown key":               func(m map[string]any) { m["x"] = 1 },
		"no rows":                      func(m map[string]any) { m["rows"] = []any{} },
		"a document value that varies": func(m map[string]any) { m["rows"].([]any)[1].(map[string]any)["customer"] = "other" },
		"a number that is none":        func(m map[string]any) { m["rows"].([]any)[0].(map[string]any)["debit"] = "1e3" },
		"a row without a field":        func(m map[string]any) { delete(m["rows"].([]any)[0].(map[string]any), "ref") },
		"a group field in the rows":    func(m map[string]any) { doc(m)["group"] = map[string]any{"fields": []any{"ref"}} },
		"an undeclared slot": func(m map[string]any) {
			elements(m, "page_header")[0] = map[string]any{"id": "e1", "type": "text", "source": "field", "text": "", "field": "ghost", "wrap": false, "x": 0, "y": 0, "width": 50, "height": 8, "font_size": 12, "align": "left", "bold": false}
		},
		"a sum of a slot that is no metric": func(m map[string]any) { elements(m, "document_header")[9].(map[string]any)["field"] = "ref" },
		"another binding":                   func(m map[string]any) { doc(m)["binding"].(map[string]any)["integration_id"] = "finance" },
		"a name on the document":            func(m map[string]any) { doc(m)["name"] = "x" },
		"a metric document field":           func(m map[string]any) { m["fields"].([]any)[0].(map[string]any)["metric"] = true },
	} {
		if _, err := doctemplates.DecodeSample(sampleJSON(t, edit)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBindMakesACopyOfTheMapping(t *testing.T) {
	s, _, _ := doctemplates.SampleByKind("statement")
	col := func(c string) doctemplates.FieldMapping { return doctemplates.FieldMapping{Column: &c} }
	lit := func(c string) doctemplates.FieldMapping { return doctemplates.FieldMapping{Literal: &c} }
	b := doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}}
	d, err := s.Bind(b, "N", map[string]doctemplates.FieldMapping{"customer": col("c"), "statement": lit("A"), "date": col("i"), "debit": col("m")})
	if err != nil || d.Name != "N" || d.Binding != b || d.Group != nil {
		t.Fatal(d.Group, err)
	}
	cols := d.TableItem().Columns
	if len(cols) != 2 || math.Abs(cols[0].Width+cols[1].Width-d.TableItem().Width) > 1e-9 {
		t.Fatalf("%+v", cols)
	}
	// A slot left out of the mapping is the literal "[<label>]".
	left, err := s.Bind(b, "", map[string]doctemplates.FieldMapping{"customer": col("c"), "date": col("i")})
	if err != nil {
		t.Fatal(err)
	}
	for _, bi := range left.Items() {
		if bi.Item.ID == "e11" && (bi.Item.Source != doctemplates.SourceLiteral || bi.Item.Text != "[业务期间]" || bi.Item.Field != "") {
			t.Fatalf("an unmapped slot: %+v", bi.Item)
		}
	}
	// The sample itself is not changed by a copy.
	if s.Document.Binding != doctemplates.SampleBinding || len(s.Document.TableItem().Columns) != 5 || s.Document.Group != nil {
		t.Fatal("the sample was written to")
	}
	// A form has no table to map and no group: a copy keeps its page, binds its fields, and is valid whatever is mapped (every slot to a literal too).
	u, _, _ := doctemplates.SampleByKind("usps")
	g, err := u.Bind(b, "", map[string]doctemplates.FieldMapping{"trackingNumber": col("t")})
	if err != nil || !g.Form() || g.Group != nil || g.Page == nil || g.Page.Preset != "label_4x6in" || u.Document.Group != nil {
		t.Fatal(g.Group, err)
	}
	if all, err := u.Bind(b, "", map[string]doctemplates.FieldMapping{"trackingNumber": lit("x")}); err != nil || !all.Form() {
		t.Fatal("a form of literals", err)
	}
	if _, err = s.Bind(b, "", map[string]doctemplates.FieldMapping{"ghost": lit("x")}); !errors.Is(err, r.ErrInvalid) {
		t.Fatal("unknown slot", err)
	}
	if _, err = s.Bind(b, strings.Repeat("x", 81), nil); !errors.Is(err, r.ErrInvalid) {
		t.Fatal("name", err)
	}
	var m doctemplates.FieldMapping
	for _, bad := range []string{`{}`, `{"column":"a","literal":"b"}`, `{"column":null}`, `{"x":1}`, `"a"`, `{"column":1}`} {
		if json.Unmarshal([]byte(bad), &m) == nil {
			t.Errorf("mapping %s accepted", bad)
		}
	}
	if json.Unmarshal([]byte(`{"literal":""}`), &m) != nil || m.Literal == nil {
		t.Error("an empty literal is a literal")
	}
}
