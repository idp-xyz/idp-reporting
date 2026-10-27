package doctemplates_test

import (
	"encoding/json"
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"math"
	"testing"
)

func document() doctemplates.Document {
	return doctemplates.Document{Schema: "1.0", Kind: "document", Binding: doctemplates.Binding{Integration: "source", Dataset: r.Ref{ID: "d", Version: "1"}}, Elements: []doctemplates.Element{{ID: "n1", Source: "row", Field: "amount", X: 10, Y: 25, Width: 190, Height: 10, FontSize: 12, Align: "right"}}}
}

func TestBackgroundSchemaStrictCompatibility(t *testing.T) {
	d := document()
	d.Schema = "1.2"
	d.Print = &doctemplates.PrintProfile{Mode: "full"}
	d.Background = &doctemplates.BackgroundRef{ID: "0123456789abcdef0123456789abcdef", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if e := doctemplates.Validate(d); e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(d)
	var decoded doctemplates.Document
	if json.Unmarshal(data, &decoded) != nil || doctemplates.Validate(decoded) != nil {
		t.Fatal("roundtrip")
	}
	for _, change := range []func(*doctemplates.Document){func(d *doctemplates.Document) { d.Schema = "1.1" }, func(d *doctemplates.Document) { d.Background = nil }, func(d *doctemplates.Document) { d.Print = nil }, func(d *doctemplates.Document) { d.Background.ID = "../file" }, func(d *doctemplates.Document) { d.Background.SHA256 = "https://example.test" }} {
		var value doctemplates.Document
		json.Unmarshal(data, &value)
		change(&value)
		if doctemplates.Validate(value) == nil {
			t.Fatal("accepted invalid background")
		}
	}
	for _, raw := range []string{`{"schema_version":"1.1","background":null}`, `{"schema_version":"1.0","background":null}`, `{"schema_version":"1.2","background":{"id":"0123456789abcdef0123456789abcdef"}}`, `{"schema_version":"1.2","background":{"id":"0123456789abcdef0123456789abcdef","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"x"}}`} {
		if json.Unmarshal([]byte(raw), &decoded) == nil {
			t.Fatal("accepted unsupported/partial reference", raw)
		}
	}
}
func TestFrozenBindingExactValuesAndBounds(t *testing.T) {
	d := document()
	s := r.Snapshot{Owner: r.Owner{Integration: "source"}, Query: r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}}, Result: r.Result{Columns: []r.Column{{ID: "amount"}}, Rows: [][]string{{"9007199254740993.01"}}, Totals: map[string]string{"amount": "9007199254740993.01"}}}
	values, e := doctemplates.Bind(d, s, 0)
	if e != nil || values["n1"] != "9007199254740993.01" {
		t.Fatal(values, e)
	}
	d.Elements[0].Source = "total"
	values, e = doctemplates.Bind(d, s, 0)
	if e != nil || values["n1"] != "9007199254740993.01" {
		t.Fatal(values, e)
	}
	d.Binding.Integration = "other"
	if _, e = doctemplates.Bind(d, s, 0); !errors.Is(e, r.ErrDenied) {
		t.Fatal("wrong snapshot binding", e)
	}
	d = document()
	for _, index := range []int{-1, 1} {
		if _, e = doctemplates.Bind(d, s, index); !errors.Is(e, r.ErrInvalid) {
			t.Fatal("row", e)
		}
	}
	d.Elements[0].Field = "unknown"
	if _, e = doctemplates.Bind(d, s, 0); !errors.Is(e, r.ErrDenied) {
		t.Fatal(e)
	}
}
func TestTemplateRejectsOverlapOverflowControlsAndUnboundLiteral(t *testing.T) {
	cases := []func(*doctemplates.Document){func(d *doctemplates.Document) {
		d.Elements = append(d.Elements, d.Elements[0])
		d.Elements[1].ID = "n2"
	}, func(d *doctemplates.Document) { d.Elements[0].X = float64(^uint(0) >> 1) }, func(d *doctemplates.Document) { d.Elements[0].Height = float64(^uint(0) >> 1) }, func(d *doctemplates.Document) { d.Elements[0].ID = "_preview" }, func(d *doctemplates.Document) { d.Elements[0].Source = "formula" }, func(d *doctemplates.Document) { d.Elements[0].Source = "literal"; d.Elements[0].Text = "x" }, func(d *doctemplates.Document) {
		d.Elements[0].Source = "literal"
		d.Elements[0].Field = ""
		d.Elements[0].Text = "x\ny"
	}, func(d *doctemplates.Document) { d.Elements = nil }}
	for i, change := range cases {
		d := document()
		change(&d)
		if doctemplates.Validate(d) == nil {
			t.Fatal("accepted invalid", i)
		}
	}
}

func TestTenthMillimeterPrintProfileAndLegacyValidation(t *testing.T) {
	d := document()
	d.Elements[0].X = 20.1
	d.Elements[0].Y = 35.2
	d.Elements[0].Width = 170
	d.Schema = "1.1"
	d.Print = &doctemplates.PrintProfile{Mode: "variables", OffsetX: 1.2, OffsetY: -.7}
	if e := doctemplates.Validate(d); e != nil {
		t.Fatal(e)
	}
	invalid := []func(*doctemplates.Document){func(d *doctemplates.Document) { d.Schema = "1.0" }, func(d *doctemplates.Document) { d.Print = nil }, func(d *doctemplates.Document) { d.Print.Mode = "other" }, func(d *doctemplates.Document) { d.Print.OffsetX = 10.1 }, func(d *doctemplates.Document) { d.Print.OffsetX = .15 }, func(d *doctemplates.Document) { d.Elements[0].X = 20.15 }, func(d *doctemplates.Document) { d.Elements[0].X = 190; d.Elements[0].Width = 10; d.Print.OffsetX = .1 }, func(d *doctemplates.Document) {
		d.Elements[0].Source = "literal"
		d.Elements[0].Field = ""
		d.Elements[0].Text = "preprinted"
	}}
	for i, change := range invalid {
		copy := d
		copy.Elements = append([]doctemplates.Element{}, d.Elements...)
		profile := *d.Print
		copy.Print = &profile
		change(&copy)
		if doctemplates.Validate(copy) == nil {
			t.Fatal("invalid print", i)
		}
	}
	legacy := document()
	legacy.Elements[0].X = 10.1
	legacy.Elements[0].Width = 180
	if doctemplates.Validate(legacy) == nil {
		t.Fatal("fractional legacy")
	}
}

func TestPrintJSONStrictProfilesAndNonfinite(t *testing.T) {
	d := document()
	b, _ := json.Marshal(d)
	var decoded doctemplates.Document
	if e := json.Unmarshal(b, &decoded); e != nil || doctemplates.Validate(decoded) != nil {
		t.Fatal(e)
	}
	b = append(b[:len(b)-1], []byte(`,"print":null}`)...)
	if json.Unmarshal(b, &decoded) == nil {
		t.Fatal("legacy accepted print:null")
	}
	d.Schema = "1.1"
	d.Print = &doctemplates.PrintProfile{Mode: "full"}
	d.Elements[0].X = math.NaN()
	if doctemplates.Validate(d) == nil {
		t.Fatal("NaN")
	}
	d.Elements[0].X = 10
	d.Print.OffsetY = math.Inf(1)
	if doctemplates.Validate(d) == nil {
		t.Fatal("infinity")
	}
}

func TestPrintJSONRequiresBothExplicitOffsets(t *testing.T) {
	d := document()
	d.Schema = "1.1"
	d.Print = &doctemplates.PrintProfile{Mode: "full"}
	valid, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	for _, profile := range []string{`{"mode":"full"}`, `{"mode":"full","offset_x":0}`, `{"mode":"full","offset_y":0}`} {
		var raw map[string]json.RawMessage
		json.Unmarshal(valid, &raw)
		raw["print"] = json.RawMessage(profile)
		data, _ := json.Marshal(raw)
		var decoded doctemplates.Document
		if e = json.Unmarshal(data, &decoded); e == nil && doctemplates.Validate(decoded) == nil {
			t.Fatal("missing print offset accepted", profile)
		}
	}
}
