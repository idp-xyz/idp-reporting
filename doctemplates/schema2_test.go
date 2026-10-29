package doctemplates_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
)

// schema2Cases is the table the console's DOM tests run too (console/dom.test.mjs): a base 2.0 document and
// edits of it, each valid, invalid (both validators refuse it) or unauthorized (the structure is valid; the
// console and the routes refuse it for the dataset it names, which this validator never sees).
type schema2Cases struct {
	Base  map[string]any `json:"base"`
	Cases []struct {
		Name   string         `json:"name"`
		Expect string         `json:"expect"`
		Edit   map[string]any `json:"edit"`
	} `json:"cases"`
}

func loadSchema2Cases(t testing.TB) schema2Cases {
	t.Helper()
	b, err := os.ReadFile("testdata/schema2_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases schema2Cases
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// walk returns the container that holds the last step of a path, and that step: keys and array indices
// joined by dots.
func walk(t testing.TB, root any, path string) (parent any, last string) {
	t.Helper()
	steps := strings.Split(path, ".")
	cur := root
	for _, s := range steps[:len(steps)-1] {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[s]
		case []any:
			i, err := strconv.Atoi(s)
			if err != nil || i >= len(c) {
				t.Fatalf("path %q: no element %q", path, s)
			}
			cur = c[i]
		default:
			t.Fatalf("path %q: cannot step into %T at %q", path, cur, s)
		}
		if cur == nil {
			t.Fatalf("path %q: nothing at %q", path, s)
		}
	}
	return cur, steps[len(steps)-1]
}

func setAt(t testing.TB, root any, path string, value any) {
	parent, last := walk(t, root, path)
	switch c := parent.(type) {
	case map[string]any:
		c[last] = value
	case []any:
		i, _ := strconv.Atoi(last)
		c[i] = value
	}
}

func getAt(t testing.TB, root any, path string) any {
	parent, last := walk(t, root, path)
	switch c := parent.(type) {
	case map[string]any:
		return c[last]
	case []any:
		i, _ := strconv.Atoi(last)
		return c[i]
	}
	return nil
}

func deleteAt(t testing.TB, root any, path string) {
	parent, last := walk(t, root, path)
	switch c := parent.(type) {
	case map[string]any:
		delete(c, last)
	case []any:
		t.Fatalf("path %q: delete from an array", path)
	}
}

func clone(t testing.TB, v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// edited applies the edits of a case to a copy of the base document, as the console's tests do.
func edited(t testing.TB, base map[string]any, edit map[string]any) map[string]any {
	doc := clone(t, base).(map[string]any)
	// Deletions of array elements are not needed; keys of maps are.
	if paths, ok := edit["delete"].([]any); ok {
		for _, p := range paths {
			deleteAt(t, doc, p.(string))
		}
	}
	if sets, ok := edit["set"].(map[string]any); ok {
		// Shorter paths first: a set of a whole band comes before a set inside it.
		keys := make([]string, 0, len(sets))
		for k := range sets {
			keys = append(keys, k)
		}
		for i := range keys {
			for j := i + 1; j < len(keys); j++ {
				if strings.Count(keys[j], ".") < strings.Count(keys[i], ".") {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		for _, k := range keys {
			setAt(t, doc, k, clone(t, sets[k]))
		}
	}
	if pushes, ok := edit["push"].(map[string]any); ok {
		for path, element := range pushes {
			parent, last := walk(t, doc, path)
			list := parent.(map[string]any)[last].([]any)
			parent.(map[string]any)[last] = append(list, clone(t, element))
		}
	}
	if extends, ok := edit["extend"].([]any); ok {
		for _, e := range extends {
			spec := e.(map[string]any)
			parent, last := walk(t, doc, spec["path"].(string))
			list := parent.(map[string]any)[last].([]any)
			for n := 0; n < int(spec["count"].(float64)); n++ {
				item := clone(t, spec["template"]).(map[string]any)
				item["id"] = strings.ReplaceAll(item["id"].(string), "{n}", strconv.Itoa(int(spec["from"].(float64))+n))
				list = append(list, item)
			}
			parent.(map[string]any)[last] = list
		}
	}
	return doc
}

// TestSchema2Cases runs every case of the shared table: the edited document is read as the stores and routes
// read a template (strict JSON, then the validator), and valid ones must write back as the same JSON.
func TestSchema2Cases(t *testing.T) {
	cases := loadSchema2Cases(t)
	if len(cases.Cases) < 150 {
		t.Fatalf("%d cases", len(cases.Cases))
	}
	var valid, invalid, unauthorized int
	for _, c := range cases.Cases {
		doc := edited(t, cases.Base, c.Edit)
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(c.Name, err)
		}
		var d doctemplates.Document
		decodeErr := json.Unmarshal(raw, &d)
		validateErr := error(nil)
		if decodeErr == nil {
			validateErr = doctemplates.Validate(d)
		}
		ok := decodeErr == nil && validateErr == nil
		switch c.Expect {
		case "valid", "unauthorized":
			if c.Expect == "valid" {
				valid++
			} else {
				unauthorized++
			}
			if !ok {
				t.Errorf("%s: refused (%v, %v), want accepted", c.Name, decodeErr, validateErr)
				continue
			}
			// The document writes back as it was read: no key dropped, none added, the order of the arrays kept.
			canonical, err := doctemplates.Canonical(d)
			if err != nil {
				t.Errorf("%s: canonical: %v", c.Name, err)
				continue
			}
			var back any
			if err := json.Unmarshal(canonical, &back); err != nil || !reflect.DeepEqual(back, clone(t, doc)) {
				t.Errorf("%s: the canonical form differs from the document:\n%s\n%s", c.Name, canonical, raw)
			}
			// The canonical form is stable: a read of it writes the same bytes.
			var again doctemplates.Document
			if err := json.Unmarshal(canonical, &again); err != nil {
				t.Errorf("%s: %v", c.Name, err)
				continue
			}
			if second, err := doctemplates.Canonical(again); err != nil || !bytes.Equal(second, canonical) {
				t.Errorf("%s: the canonical form is not stable", c.Name)
			}
		case "invalid":
			invalid++
			if ok {
				t.Errorf("%s: accepted, want refused", c.Name)
			}
		default:
			t.Fatalf("%s: expect %q", c.Name, c.Expect)
		}
	}
	if valid < 35 || invalid < 100 || unauthorized < 5 {
		t.Errorf("cases: %d valid, %d invalid, %d unauthorized", valid, invalid, unauthorized)
	}
}

func baseDocument(t testing.TB) doctemplates.Document {
	t.Helper()
	cases := loadSchema2Cases(t)
	raw, _ := json.Marshal(cases.Base)
	var d doctemplates.Document
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if err := doctemplates.Validate(d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSchema2DocumentIsClosedAndStrict(t *testing.T) {
	cases := loadSchema2Cases(t)
	base, _ := json.Marshal(cases.Base)
	for name, raw := range map[string]string{
		"a duplicate key":                  strings.Replace(string(base), `"kind":"document"`, `"kind":"document","kind":"document"`, 1),
		"a null group":                     strings.Replace(string(base), `"group":{"fields":["customer.id"]}`, `"group":null`, 1),
		"a null band":                      strings.Replace(string(base), `"document_footer":{`, `"document_footer":null,"x":{`, 1),
		"a wrongly cased key":              strings.Replace(string(base), `"schema_version"`, `"Schema_Version"`, 1),
		"trailing data":                    string(base) + ` {}`,
		"an element that is not an object": strings.Replace(string(base), `"elements":[`, `"elements":[3,`, 1),
	} {
		var d doctemplates.Document
		if err := json.Unmarshal([]byte(raw), &d); err == nil && doctemplates.Validate(d) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var d doctemplates.Document
	if err := json.Unmarshal(base, &d); err != nil {
		t.Fatal(err)
	}
	// A 2.0 document is bound by group, never carries 1.x fields, and a 1.x document never carries 2.0's.
	for name, change := range map[string]func(*doctemplates.Document){
		"elements":        func(d *doctemplates.Document) { d.Elements = []doctemplates.Element{{ID: "n1"}} },
		"a print profile": func(d *doctemplates.Document) { d.Print = &doctemplates.PrintProfile{Mode: "full"} },
		"a background":    func(d *doctemplates.Document) { d.Background = &doctemplates.BackgroundRef{} },
		"a 1.x table":     func(d *doctemplates.Document) { d.Table = &doctemplates.Table{} },
		"no bands":        func(d *doctemplates.Document) { d.Bands = nil },
		"no detail":       func(d *doctemplates.Document) { b := *d.Bands; b.Detail = nil; d.Bands = &b },
		"a band of nil": func(d *doctemplates.Document) {
			b := *d.Bands
			b.PageHeader = &doctemplates.Band{Height: 10}
			d.Bands = &b
		},
		"a reserved type":    func(d *doctemplates.Document) { d.Bands.PageHeader.Elements[0].Type = "barcode" },
		"an unknown type":    func(d *doctemplates.Document) { d.Bands.PageHeader.Elements[0].Type = "circle" },
		"an empty type":      func(d *doctemplates.Document) { d.Bands.PageHeader.Elements[0].Type = "" },
		"a missing table":    func(d *doctemplates.Document) { d.Bands.Detail.Elements = nil },
		"a table in a band":  func(d *doctemplates.Document) { d.Bands.PageHeader.Elements[0] = d.Bands.Detail.Elements[0] },
		"another schema":     func(d *doctemplates.Document) { d.Schema = "2.2" },
		"a table with no id": func(d *doctemplates.Document) { d.Bands.Detail.Elements[0].ID = "" },
	} {
		c := cloneDocument(t, d)
		change(&c)
		if err := doctemplates.Validate(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	one := document()
	one.Group = &doctemplates.GroupSpec{Fields: []string{"customer.id"}}
	if err := doctemplates.Validate(one); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a 1.x document with a group: %v", err)
	}
	one = document()
	one.Bands = d.Bands
	if err := doctemplates.Validate(one); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a 1.x document with bands: %v", err)
	}
	for _, raw := range []string{
		`{"schema_version":"1.1","kind":"document","binding":{"integration_id":"i","dataset":{"id":"d","version":"1"}},"elements":[],"print":{"mode":"full","offset_x":0,"offset_y":0},"group":{"fields":["a"]}}`,
		`{"schema_version":"1.0","kind":"document","binding":{"integration_id":"i","dataset":{"id":"d","version":"1"}},"elements":[],"bands":{}}`,
	} {
		var v doctemplates.Document
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			t.Errorf("a 1.x document read with 2.0 keys: %s", raw)
		}
	}
	// Items and the table, in band order.
	items := d.Items()
	ids := make([]string, len(items))
	for i, bi := range items {
		ids[i] = bi.Band + ":" + bi.Item.ID
	}
	if got := strings.Join(ids, " "); got != "page_header:e1 page_header:e2 page_header:e3 document_header:e4 document_header:e5 detail:e6 document_footer:e7 document_footer:e8 page_footer:e9" {
		t.Errorf("items %s", got)
	}
	if tbl := d.TableItem(); tbl == nil || tbl.ID != "e6" || len(tbl.Columns) != 3 {
		t.Errorf("table %+v", tbl)
	}
	if document().TableItem() != nil || (doctemplates.Document{}).Items() != nil {
		t.Error("a 1.x document has no 2.0 items")
	}
}

func cloneDocument(t testing.TB, d doctemplates.Document) doctemplates.Document {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var out doctemplates.Document
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSchema2WritesOnlyTheKeysOfItsTypes(t *testing.T) {
	d := baseDocument(t)
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	if _, has := generic["elements"]; has {
		t.Error("a 2.0 document writes no elements key")
	}
	for _, k := range []string{"print", "background", "table"} {
		if _, has := generic[k]; has {
			t.Errorf("a 2.0 document writes %s", k)
		}
	}
	want := map[string]string{
		"text":  "align,bold,field,font_size,height,id,source,text,type,width,wrap,x,y",
		"line":  "id,stroke,type,x1,x2,y1,y2",
		"rect":  "fill,height,id,stroke,type,width,x,y",
		"table": "columns,font_size,id,type,width,x",
	}
	for _, bi := range d.Items() {
		raw, _ := json.Marshal(bi.Item)
		var keys map[string]any
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		sortStrings(names)
		if got := strings.Join(names, ","); got != want[bi.Item.Type] {
			t.Errorf("%s %s: keys %s, want %s", bi.Item.ID, bi.Item.Type, got, want[bi.Item.Type])
		}
	}
	// The optional keys of a column are written when set and only then.
	col := d.TableItem().Columns
	if raw, _ := json.Marshal(col[0]); string(raw) != `{"field":"aging.bucket","width":60}` {
		t.Errorf("a plain column: %s", raw)
	}
	if raw, _ := json.Marshal(col[1]); string(raw) != `{"field":"customer.id","width":60,"label":"Customer"}` {
		t.Errorf("a labelled column: %s", raw)
	}
	if raw, _ := json.Marshal(col[2]); string(raw) != `{"field":"ar.open_balance","width":70,"align":"right"}` {
		t.Errorf("an aligned column: %s", raw)
	}
	// A type with no form cannot be written, so that it cannot be stored.
	if _, err := json.Marshal(doctemplates.Item{ID: "e1", Type: "circle"}); err == nil {
		t.Error("a reserved type was written")
	}
	// A 1.x document is written as it always was.
	one, _ := json.Marshal(document())
	if bytes.Contains(one, []byte(`"group"`)) || bytes.Contains(one, []byte(`"bands"`)) {
		t.Errorf("a 1.x document: %s", one)
	}
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func TestSchema2Limits(t *testing.T) {
	d := baseDocument(t)
	// 64 KiB: with 24 wrapped literals of 1024 ideographs (3 bytes each) the template is larger than that, and the
	// refusal is the budget's, as for 1.x; the same elements with short texts are valid.
	build := func(n int, text string) doctemplates.Document {
		c := cloneDocument(t, d)
		c.Bands.DocumentFooter = nil
		c.Bands.PageFooter = nil
		c.Bands.DocumentHeader = nil
		c.Bands.PageHeader = &doctemplates.Band{Height: 200, Elements: []doctemplates.Item{}}
		for i := range n {
			c.Bands.PageHeader.Elements = append(c.Bands.PageHeader.Elements, doctemplates.Item{ID: "e" + strconv.Itoa(i+10), Type: "text", Source: "literal", Text: text, Wrap: true, X: 0, Y: float64(i) * 8, Width: 190, Height: 8, FontSize: 8, Align: "left"})
		}
		return c
	}
	if err := doctemplates.Validate(build(24, "字")); err != nil {
		t.Fatalf("a small document: %v", err)
	}
	if err := doctemplates.Validate(build(24, strings.Repeat("字", 1024))); !errors.Is(err, r.ErrBudget) {
		t.Errorf("a document over 64 KiB: %v", err)
	}
	if _, err := doctemplates.Canonical(build(24, strings.Repeat("字", 1024))); !errors.Is(err, r.ErrBudget) {
		t.Errorf("canonical of a document over 64 KiB: %v", err)
	}
	// 19 such texts are 58368 bytes of ideographs and about 3.5 KiB of keys and the rest of the document: under 64 KiB.
	if err := doctemplates.Validate(build(19, strings.Repeat("字", 1024))); err != nil {
		t.Errorf("a document just under 64 KiB: %v", err)
	}
	// The detail minimum is the layout's four rows: more than the page header can leave, it is invalid.
	for size, want := range map[int]int{8: 222, 9: 242, 10: 263, 11: 284, 12: 304} {
		if got := doctemplates.MinDetailHeightTenths(size); got != want {
			t.Errorf("MinDetailHeightTenths(%d) = %d, want %d", size, got, want)
		}
	}
	c := cloneDocument(t, d)
	c.Bands.DocumentHeader, c.Bands.DocumentFooter, c.Bands.PageFooter = nil, nil, nil
	for height, ok := range map[float64]bool{210.8: true, 210.9: false, 220.8: false} {
		c.Bands.PageHeader.Height = height
		c.Bands.PageHeader.Elements = c.Bands.PageHeader.Elements[:1]
		c.Bands.PageHeader.Elements[0].Y, c.Bands.PageHeader.Elements[0].Height = 0, 9
		c.Bands.Detail.Elements[0].FontSize = 10
		// 245 - 26.3 (four rows at 10 pt) = 218.7 is the most the page header may take.
		if got := doctemplates.Validate(c) == nil; got != (height <= 218.7) {
			t.Errorf("page header of %.1f mm: valid %v", height, got)
		}
		_ = ok
	}
}

// TestSchema2NamesTheFirstPageStackNotTheFooter: the sum of the bands that every first page carries is
// what must leave room for the table; the document footer is placed after the last row, on that page or
// the next, and the layout refuses one that fits no empty page.
func TestSchema2NamesTheFirstPageStackNotTheFooter(t *testing.T) {
	d := baseDocument(t)
	d.Bands.DocumentFooter.Height = 245
	if err := doctemplates.Validate(d); err != nil {
		t.Errorf("a document footer of 245 mm: %v", err)
	}
	d.Bands.PageHeader.Height = 200
	d.Bands.DocumentHeader.Height = 24
	if err := doctemplates.Validate(d); err == nil {
		t.Error("a page header and document header that leave no room for the table")
	}
}

func TestRefusalTypeCarriesTheSentinelAndNeverAValue(t *testing.T) {
	row := 7
	err := doctemplates.Refuse(fmt.Errorf("pdf: a value does not fit: %w", r.ErrUnsupported), doctemplates.Detail{Reason: doctemplates.ReasonDoesNotFit, Band: "detail", Element: "e6", Column: "customer.id", Row: &row})
	if !errors.Is(err, r.ErrUnsupported) {
		t.Error("the refusal is not the sentinel")
	}
	d, ok := doctemplates.DetailOf(fmt.Errorf("wrapped: %w", err))
	if !ok || d.Reason != "DOES_NOT_FIT" || d.Band != "detail" || d.Element != "e6" || d.Column != "customer.id" || *d.Row != 7 {
		t.Errorf("detail %+v", d)
	}
	if !strings.Contains(err.Error(), "DOES_NOT_FIT band detail element e6 row 7 column customer.id") {
		t.Errorf("message %q", err)
	}
	b, _ := json.Marshal(d)
	if string(b) != `{"reason":"DOES_NOT_FIT","band":"detail","element":"e6","row":7,"column":"customer.id"}` {
		t.Errorf("json %s", b)
	}
	// Row 0 is written, an absent row is not, and the other places are omitted when empty.
	if b, _ := json.Marshal(doctemplates.Detail{Reason: "TOO_MANY_PAGES"}); string(b) != `{"reason":"TOO_MANY_PAGES"}` {
		t.Errorf("json %s", b)
	}
	if b, _ := json.Marshal(doctemplates.Detail{Reason: "NOT_COVERED"}.At(0)); string(b) != `{"reason":"NOT_COVERED","row":0}` {
		t.Errorf("json %s", b)
	}
	if doctemplates.Refuse(nil, doctemplates.Detail{}) != nil {
		t.Error("a nil error gained a detail")
	}
	if _, ok := doctemplates.DetailOf(r.ErrUnsupported); ok {
		t.Error("a plain error has a detail")
	}
}
