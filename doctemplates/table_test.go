package doctemplates_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

func tableDocument() doctemplates.Document {
	d := document()
	d.Schema, d.Print = "1.3", &doctemplates.PrintProfile{Mode: "full"}
	d.Table = &doctemplates.Table{X: 10, Y: 60, Width: 190, Height: 100, FontSize: 9, Columns: []doctemplates.TableColumn{{Field: "customer", Width: 90}, {Field: "amount", Width: 100}}}
	return d
}

func TestTableValidation(t *testing.T) {
	if e := doctemplates.Validate(tableDocument()); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*doctemplates.Document){
		"table on schema 1.2": func(d *doctemplates.Document) {
			d.Schema = "1.2"
			d.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64)}
		},
		"schema 1.3 without a table": func(d *doctemplates.Document) { d.Table = nil },
		"widths that do not add up":  func(d *doctemplates.Document) { d.Table.Columns[0].Width = 80 },
		"a column under 8 mm":        func(d *doctemplates.Document) { d.Table.Columns[0].Width = 7.9; d.Table.Columns[1].Width = 182.1 },
		"no columns":                 func(d *doctemplates.Document) { d.Table.Columns = nil },
		"a duplicate field":          func(d *doctemplates.Document) { d.Table.Columns[1].Field = "customer" },
		"thirteen columns": func(d *doctemplates.Document) {
			d.Table.Columns = nil
			for i := range 13 {
				d.Table.Columns = append(d.Table.Columns, doctemplates.TableColumn{Field: "f" + string(rune('a'+i)), Width: 10})
			}
			d.Table.Width = 130
		},
		"a region over the safe area": func(d *doctemplates.Document) { d.Table.Y = 180 },
		"a region too low":            func(d *doctemplates.Document) { d.Table.Height = 29.9 },
		"off the grid":                func(d *doctemplates.Document) { d.Table.X = 10.05 },
		"a type size over 12":         func(d *doctemplates.Document) { d.Table.FontSize = 13 },
		"a type size under 8":         func(d *doctemplates.Document) { d.Table.FontSize = 7 },
		"overlapping a text box":      func(d *doctemplates.Document) { d.Table.Y = 30 },
		"pushed out by the offsets":   func(d *doctemplates.Document) { d.Print.OffsetY = 10; d.Table.Y = 165 },
		"an empty field":              func(d *doctemplates.Document) { d.Table.Columns[0].Field = "" },
		"x under 10":                  func(d *doctemplates.Document) { d.Table.X = 9.9 },
		"y under 25":                  func(d *doctemplates.Document) { d.Table.Y = 24.9; d.Elements[0].Y = 200 },
		"a field over 128 bytes":      func(d *doctemplates.Document) { d.Table.Columns[0].Field = strings.Repeat("f", 129) },
		"a column width off the grid": func(d *doctemplates.Document) { d.Table.Columns[0].Width = 90.04; d.Table.Columns[1].Width = 99.96 },
		"an invalid background ref":   func(d *doctemplates.Document) { d.Background = &doctemplates.BackgroundRef{ID: "x", SHA256: "y"} },
	} {
		d := tableDocument()
		change(&d)
		if e := doctemplates.Validate(d); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s: %v", name, e)
		}
	}
	// A background may accompany the table; variables mode needs no bound box when there is a table.
	d := tableDocument()
	d.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64)}
	if e := doctemplates.Validate(d); e != nil {
		t.Errorf("with a background: %v", e)
	}
	d = tableDocument()
	d.Elements[0] = doctemplates.Element{ID: "n1", Source: "literal", Text: "x", X: 10, Y: 25, Width: 190, Height: 10, FontSize: 12, Align: "left"}
	d.Print.Mode = "variables"
	if e := doctemplates.Validate(d); e != nil {
		t.Errorf("variables mode with a table only: %v", e)
	}
	// Strict decoding: unknown or missing table keys, and a table on another schema.
	for _, raw := range []string{
		`{"schema_version":"1.3","kind":"document","binding":{"integration_id":"source","dataset":{"id":"d","version":"1"}},"elements":[],"print":{"mode":"full","offset_x":0,"offset_y":0},"table":{"x":10,"y":60,"width":190,"height":100,"font_size":9,"columns":[{"field":"a","width":190,"extra":1}]}}`,
		`{"schema_version":"1.3","kind":"document","binding":{"integration_id":"source","dataset":{"id":"d","version":"1"}},"elements":[],"print":{"mode":"full","offset_x":0,"offset_y":0},"table":{"x":10,"y":60,"width":190,"height":100,"columns":[]}}`,
		`{"schema_version":"1.1","kind":"document","binding":{"integration_id":"source","dataset":{"id":"d","version":"1"}},"elements":[],"print":{"mode":"full","offset_x":0,"offset_y":0},"table":{"x":10,"y":60,"width":190,"height":100,"font_size":9,"columns":[]}}`,
	} {
		var decoded doctemplates.Document
		if json.Unmarshal([]byte(raw), &decoded) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	var decoded doctemplates.Document
	b, _ := json.Marshal(tableDocument())
	if e := json.Unmarshal(b, &decoded); e != nil || decoded.Table == nil || len(decoded.Table.Columns) != 2 {
		t.Errorf("round trip: %v %+v", e, decoded.Table)
	}
}

func TestBindTableTakesRowsFromTheSnapshot(t *testing.T) {
	d := tableDocument()
	s := r.Snapshot{Owner: r.Owner{Integration: "source"}, Query: r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}, Dimensions: []string{"customer"}, Metrics: []r.Ref{{ID: "amount"}}},
		Result: r.Result{Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2}}, Rows: [][]string{{"a", "1.00"}, {"b", "2.50"}}, Totals: map[string]string{"amount": "3.50"}}}
	data, e := doctemplates.BindTable(d, s)
	if e != nil || len(data.Rows) != 2 || data.Rows[1][1] != "2.50" || data.Columns[1].Label != "金额" || data.Metric[0] || !data.Metric[1] || data.Totals["amount"] != "3.50" || len(data.Totals) != 1 {
		t.Fatalf("%+v %v", data, e)
	}
	// Columns in the template's order, not the snapshot's.
	d.Table.Columns = []doctemplates.TableColumn{{Field: "amount", Width: 90}, {Field: "customer", Width: 100}}
	if data, e = doctemplates.BindTable(d, s); e != nil || data.Columns[0].ID != "amount" || data.Rows[0][0] != "1.00" {
		t.Fatalf("%+v %v", data, e)
	}
	// A field the snapshot lacks is a denial; another binding too; no table binds nothing.
	d.Table.Columns[0].Field = "other"
	if _, e = doctemplates.BindTable(d, s); !errors.Is(e, r.ErrDenied) {
		t.Errorf("unknown field: %v", e)
	}
	d = tableDocument()
	d.Binding.Integration = "other"
	if _, e = doctemplates.BindTable(d, s); !errors.Is(e, r.ErrDenied) {
		t.Errorf("other binding: %v", e)
	}
	d = tableDocument()
	d.Binding.Dataset.Version = "2"
	if _, e = doctemplates.BindTable(d, s); !errors.Is(e, r.ErrDenied) {
		t.Errorf("other dataset: %v", e)
	}
	// A snapshot row shorter than its columns, or with invalid UTF-8, is an integrity failure.
	short := s
	short.Result.Rows = [][]string{{"a"}}
	if _, e = doctemplates.BindTable(tableDocument(), short); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("short row: %v", e)
	}
	short.Result.Rows = [][]string{{"a", "1.\xff"}}
	if _, e = doctemplates.BindTable(tableDocument(), short); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("invalid UTF-8: %v", e)
	}
	if data, e = doctemplates.BindTable(document(), s); e != nil || data != nil {
		t.Errorf("no table: %+v %v", data, e)
	}
}
