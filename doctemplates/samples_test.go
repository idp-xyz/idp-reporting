package doctemplates_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

func TestTheEmbeddedPackLoadsAndEverySamplePreviews(t *testing.T) {
	all, err := doctemplates.Samples()
	if err != nil || len(all) < 2 {
		t.Fatal(len(all), err)
	}
	for _, s := range all {
		values, table, err := s.BindPreview()
		if err != nil || table == nil || len(table.Rows) != len(s.Rows) || len(values) == 0 {
			t.Errorf("%s: %v", s.Kind, err)
		}
		if s.ElementCount() < 2 || s.Preset() == "" {
			t.Errorf("%s: counts", s.Kind)
		}
	}
	if s, ok, _ := doctemplates.SampleByKind("_fixture_parcel_label"); !ok || s.Preset() != "label_100x150" {
		t.Fatal("by kind")
	}
	if _, ok, _ := doctemplates.SampleByKind("nothing"); ok {
		t.Fatal("unknown kind")
	}
}

func sampleJSON(t *testing.T, edit func(m map[string]any)) []byte {
	t.Helper()
	b, err := os.ReadFile("samples/_fixture_delivery_note.json")
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
	for name, edit := range map[string]func(map[string]any){
		"an upper-case kind":           func(m map[string]any) { m["kind"] = "Bad" },
		"no description":               func(m map[string]any) { m["description"] = "" },
		"an unknown key":               func(m map[string]any) { m["x"] = 1 },
		"no rows":                      func(m map[string]any) { m["rows"] = []any{} },
		"a document value that varies": func(m map[string]any) { m["rows"].([]any)[1].(map[string]any)["customer"] = "other" },
		"a number that is none":        func(m map[string]any) { m["rows"].([]any)[0].(map[string]any)["quantity"] = "1e3" },
		"a row without a field":        func(m map[string]any) { delete(m["rows"].([]any)[0].(map[string]any), "item") },
		"a group field in the rows":    func(m map[string]any) { doc(m)["group"] = map[string]any{"fields": []any{"item"}} },
		"an undeclared slot": func(m map[string]any) {
			doc(m)["bands"].(map[string]any)["page_header"].(map[string]any)["elements"].([]any)[0] = map[string]any{"id": "e1", "type": "text", "source": "field", "text": "", "field": "ghost", "wrap": false, "x": 0, "y": 0, "width": 50, "height": 8, "font_size": 12, "align": "left", "bold": false}
		},
		"a sum of a slot that is no metric": func(m map[string]any) {
			doc(m)["bands"].(map[string]any)["document_footer"].(map[string]any)["elements"].([]any)[1].(map[string]any)["field"] = "unit_price"
		},
		"another binding":         func(m map[string]any) { doc(m)["binding"].(map[string]any)["integration_id"] = "finance" },
		"a name on the document":  func(m map[string]any) { doc(m)["name"] = "x" },
		"a metric document field": func(m map[string]any) { m["fields"].([]any)[0].(map[string]any)["metric"] = true },
	} {
		if _, err := doctemplates.DecodeSample(sampleJSON(t, edit)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBindMakesACopyOfTheMapping(t *testing.T) {
	s, _, _ := doctemplates.SampleByKind("_fixture_delivery_note")
	col := func(c string) doctemplates.FieldMapping { return doctemplates.FieldMapping{Column: &c} }
	lit := func(c string) doctemplates.FieldMapping { return doctemplates.FieldMapping{Literal: &c} }
	b := doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}}
	d, err := s.Bind(b, "N", map[string]doctemplates.FieldMapping{"customer": col("c"), "address": lit("A"), "item": col("i"), "amount": col("m")})
	if err != nil || d.Name != "N" || d.Binding != b || len(d.Group.Fields) != 1 {
		t.Fatal(d.Group, err)
	}
	cols := d.TableItem().Columns
	if len(cols) != 2 || cols[0].Width+cols[1].Width != 190 {
		t.Fatalf("%+v", cols)
	}
	// A slot left out of the mapping is the literal "[<label>]".
	left, err := s.Bind(b, "", map[string]doctemplates.FieldMapping{"customer": col("c"), "item": col("i")})
	if err != nil {
		t.Fatal(err)
	}
	for _, bi := range left.Items() {
		if bi.Item.ID == "e7" && (bi.Item.Source != doctemplates.SourceLiteral || bi.Item.Text != "[地址]" || bi.Item.Field != "") {
			t.Fatalf("an unmapped slot: %+v", bi.Item)
		}
	}
	// The sample itself is not changed by a copy.
	if s.Document.Binding != doctemplates.SampleBinding || len(s.Document.TableItem().Columns) != 4 || s.Document.Group == nil || len(s.Document.Group.Fields) != 2 {
		t.Fatal("the sample was written to")
	}
	// Every slot to a literal: no group, and no column left.
	if _, err = s.Bind(b, "", map[string]doctemplates.FieldMapping{"item": lit("x")}); !errors.Is(err, r.ErrInvalid) {
		t.Fatal("no column", err)
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
