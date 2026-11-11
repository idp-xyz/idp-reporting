package layouts_test

import (
	"encoding/json"
	"errors"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func document() layouts.Document {
	return layouts.Document{Schema: "1.0", Binding: layouts.Binding{Integration: "source", Dataset: r.Ref{ID: "dataset", Version: "1"}}, Columns: []layouts.Column{{ID: "amount", Align: "right"}}}
}
func TestPersistentPrivateVersions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	s, e := layouts.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	first, e := s.Save(owner, "", 0, document())
	if e != nil {
		t.Fatal(e)
	}
	for _, other := range []r.Identity{{Tenant: "x", Issuer: "i", Subject: "s"}, {Tenant: "t", Issuer: "x", Subject: "s"}, {Tenant: "t", Issuer: "i", Subject: "x"}} {
		if _, e = s.Get(other, first.ID, 1); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("owner disclosure", e)
		}
		all, e := s.List(other)
		if e != nil || len(all) != 0 {
			t.Fatal("list disclosure")
		}
	}
	if other, e := layouts.Open(dir); e == nil {
		other.Close()
		t.Fatal("second writer allowed")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.Save(owner, first.ID, 1, document()); results <- e }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, r.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = layouts.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	old, e := s.Get(owner, first.ID, 1)
	if e != nil || old.Version != 1 || old.Layout.Columns[0].Align != "right" {
		t.Fatal(old, e)
	}
	all, e := s.List(owner)
	if e != nil || len(all) != 1 || all[0].Version != 2 {
		t.Fatal(all, e)
	}
	bad := document()
	bad.Binding.Dataset.Version = "2"
	if _, e = s.Save(owner, first.ID, 2, bad); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("changed binding", e)
	}
	for v := 2; v < 20; v++ {
		if _, e = s.Save(owner, first.ID, v, document()); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.Save(owner, first.ID, 20, document()); !errors.Is(e, r.ErrBudget) {
		t.Fatal("history budget", e)
	}
}
func TestInvalidLayouts(t *testing.T) {
	for _, change := range []func(*layouts.Document){func(d *layouts.Document) { d.Columns = nil }, func(d *layouts.Document) { d.Columns = append(d.Columns, d.Columns[0]) }, func(d *layouts.Document) { d.Columns[0].Align = "html" }, func(d *layouts.Document) { d.Binding.Integration = "" }} {
		d := document()
		change(&d)
		if layouts.Validate(d) == nil {
			t.Fatal("invalid layout accepted")
		}
	}
}

// document11 is a schema 1.1 layout of a receivables dataset that uses every 1.1 key (S23-A, ADR 0024 decision 1).
func document11() layouts.Document {
	off := false
	return layouts.Document{
		Schema: "1.1", Binding: layouts.Binding{Integration: "source", Dataset: r.Ref{ID: "dataset", Version: "1"}},
		Title: "应收账龄", Footnote: "金额含税。",
		Columns: []layouts.Column{
			{ID: "bucket", Align: "center", Label: "账龄"},
			{ID: "customer", Align: "left"},
			{ID: "amount", Align: "right", Format: &layouts.Format{Grouping: &off, Negative: "parentheses", Zero: "dash"}},
			{ID: "count", Align: "right", Format: &layouts.Format{}},
		},
		Subtotals: &layouts.Subtotals{By: "bucket"},
		Marks:     []layouts.Mark{{Column: "amount", When: "lt", Value: "0", Mark: "bad"}, {Column: "customer", When: "blank", Mark: "note"}, {Column: "bucket", When: "eq", Value: "current", Mark: "good"}},
	}
}

// dataset11 is the dataset document11 is bound to: two string dimensions, a decimal and an integer metric.
func dataset11() ([]r.Column, []string) {
	return []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "bucket", Label: "账龄区间", Type: "string"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2}, {ID: "count", Label: "笔数", Type: "integer"}}, []string{"customer", "bucket"}
}

func TestSchema11IsValidAndSchema10IsUnchanged(t *testing.T) {
	if e := layouts.Validate(document11()); e != nil {
		t.Fatal(e)
	}
	if e := layouts.Validate(document()); e != nil {
		t.Fatal("a 1.0 layout:", e)
	}
	minimal := document()
	minimal.Schema = "1.1"
	if e := layouts.Validate(minimal); e != nil {
		t.Fatal("a 1.1 layout with no optional key:", e)
	}
	// A 1.0 layout that speaks 1.1 is refused: a layout says which schema it speaks.
	for name, change := range map[string]func(*layouts.Document){
		"title":    func(d *layouts.Document) { d.Title = "t" },
		"footnote": func(d *layouts.Document) { d.Footnote = "f" },
		"label":    func(d *layouts.Document) { d.Columns[0].Label = "l" },
		"format":   func(d *layouts.Document) { d.Columns[0].Format = &layouts.Format{} },
		"center":   func(d *layouts.Document) { d.Columns[0].Align = "center" },
		"subtotal": func(d *layouts.Document) { d.Subtotals = &layouts.Subtotals{By: "amount"} },
		"marks": func(d *layouts.Document) {
			d.Marks = []layouts.Mark{{Column: "amount", When: "lt", Value: "0", Mark: "bad"}}
		},
	} {
		d := document()
		change(&d)
		if e := layouts.Validate(d); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("a 1.0 layout with %s: %v", name, e)
		}
	}
}

func TestSchema11RefusalsNameNoValue(t *testing.T) {
	long := strings.Repeat("长", 121)
	for name, change := range map[string]func(*layouts.Document){
		"an unknown schema":        func(d *layouts.Document) { d.Schema = "1.2" },
		"a title too long":         func(d *layouts.Document) { d.Title = long },
		"a title with a control":   func(d *layouts.Document) { d.Title = "a\nb" },
		"a footnote too long":      func(d *layouts.Document) { d.Footnote = strings.Repeat("注", 401) },
		"a label too long":         func(d *layouts.Document) { d.Columns[0].Label = strings.Repeat("标", 65) },
		"an unknown alignment":     func(d *layouts.Document) { d.Columns[0].Align = "justify" },
		"an unknown negative form": func(d *layouts.Document) { d.Columns[2].Format.Negative = "red" },
		"an unknown zero form":     func(d *layouts.Document) { d.Columns[2].Format.Zero = "blank" },
		"a subtotal column not shown": func(d *layouts.Document) {
			d.Subtotals.By = "secretcolumn"
		},
		"an empty subtotal column": func(d *layouts.Document) { d.Subtotals.By = "" },
		"no mark in marks":         func(d *layouts.Document) { d.Marks = []layouts.Mark{} },
		"nine marks": func(d *layouts.Document) {
			for len(d.Marks) < 9 {
				d.Marks = append(d.Marks, d.Marks[0])
			}
		},
		"a mark on a column not shown": func(d *layouts.Document) { d.Marks[0].Column = "secretcolumn" },
		"an unknown relation":          func(d *layouts.Document) { d.Marks[0].When = "between" },
		"an unknown mark":              func(d *layouts.Document) { d.Marks[0].Mark = "red" },
		"blank with a value":           func(d *layouts.Document) { d.Marks[1].Value = "secretvalue" },
		"an order without a number":    func(d *layouts.Document) { d.Marks[0].Value = "secretvalue" },
		"a value too long":             func(d *layouts.Document) { d.Marks[2].Value = strings.Repeat("v", 129) },
	} {
		d := document11()
		change(&d)
		e := layouts.Validate(d)
		if !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, e)
		} else if strings.Contains(e.Error(), "secret") {
			t.Errorf("%s: the error carries a value: %v", name, e)
		}
	}
	// Eight marks are the bound.
	d := document11()
	for len(d.Marks) < 8 {
		d.Marks = append(d.Marks, d.Marks[0])
	}
	if e := layouts.Validate(d); e != nil {
		t.Error("eight marks:", e)
	}
	// 64 KiB is the bound of the document, judged before anything else; 100 columns the bound of the columns.
	d = document11()
	for len(d.Columns) < 1000 {
		d.Columns = append(d.Columns, layouts.Column{ID: strings.Repeat("c", 120) + strconv.Itoa(len(d.Columns)), Align: "left", Label: strings.Repeat("标", 64)})
	}
	if e := layouts.Validate(d); !errors.Is(e, r.ErrBudget) {
		t.Errorf("a 1.1 layout over 64 KiB: %v", e)
	}
	d = document11()
	for len(d.Columns) < 101 {
		d.Columns = append(d.Columns, layouts.Column{ID: "c" + strconv.Itoa(len(d.Columns)), Align: "left"})
	}
	if e := layouts.Validate(d); !errors.Is(e, r.ErrInvalid) {
		t.Errorf("101 columns: %v", e)
	}
}

func TestCheckHoldsALayoutToItsDataset(t *testing.T) {
	cols, dims := dataset11()
	if e := layouts.Check(document11(), cols, dims); e != nil {
		t.Fatal(e)
	}
	for name, c := range map[string]struct {
		change func(*layouts.Document)
		want   error
	}{
		"a column the dataset lacks":  {func(d *layouts.Document) { d.Columns[1].ID = "other" }, r.ErrDenied},
		"a format on a string column": {func(d *layouts.Document) { d.Columns[1].Format = &layouts.Format{} }, r.ErrInvalid},
		"subtotals by a metric":       {func(d *layouts.Document) { d.Subtotals.By = "amount" }, r.ErrInvalid},
		"blank on a numeric column":   {func(d *layouts.Document) { d.Marks[1].Column = "amount" }, r.ErrInvalid},
		"an order on a string column": {func(d *layouts.Document) { d.Marks[0].Column = "customer" }, r.ErrInvalid},
		"a word compared to a number": {func(d *layouts.Document) { d.Marks[2].Column = "count" }, r.ErrInvalid},
	} {
		d := document11()
		c.change(&d)
		if e := layouts.Check(d, cols, dims); !errors.Is(e, c.want) {
			t.Errorf("%s: %v, want %v", name, e, c.want)
		}
	}
	// A 1.0 layout is held to the dataset as before: its columns must exist, nothing else is asked of it.
	if e := layouts.Check(document(), cols, dims); e != nil {
		t.Error(e)
	}
}

func TestPresentationOfALayoutFillsTheDefaults(t *testing.T) {
	p := layouts.Presentation(document11(), "0123456789abcdef0123456789abcdef", 3)
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	if p.LayoutID != "0123456789abcdef0123456789abcdef" || p.LayoutVersion != 3 || p.Title != "应收账龄" || p.Footnote != "金额含税。" || p.SubtotalBy != "bucket" || len(p.Marks) != 3 || len(p.Columns) != 4 {
		t.Fatalf("%+v", p)
	}
	if c := p.Columns[0]; c.ID != "bucket" || c.Label != "账龄" || c.Align != r.AlignCenter || c.Format != nil {
		t.Errorf("column 0: %+v", c)
	}
	if c := p.Columns[1]; c.Label != "" || c.Format != nil {
		t.Errorf("column 1: %+v", c)
	}
	if f := p.Columns[2].Format; f == nil || f.Grouping || f.Negative != r.NegativeParentheses || f.Zero != r.ZeroDash {
		t.Errorf("an explicit format: %+v", f)
	}
	if f := p.Columns[3].Format; f == nil || *f != r.DefaultNumberFormat {
		t.Errorf("an empty format takes the defaults: %+v", f)
	}
	if m := p.Marks[1]; m.Column != "customer" || m.When != r.WhenBlank || m.Value != "" || m.Mark != r.MarkNote {
		t.Errorf("mark 1: %+v", m)
	}
	// A 1.0 layout presents as it always showed: no texts, no formats, no subtotals, no marks.
	q := layouts.Presentation(document(), "", 0)
	if q.LayoutID != "" || q.Title != "" || len(q.Columns) != 1 || q.Columns[0].Format != nil || q.SubtotalBy != "" || q.Marks != nil {
		t.Errorf("%+v", q)
	}
}

func TestASchema11LayoutIsStoredAsSaved(t *testing.T) {
	s, e := layouts.Open(filepath.Join(t.TempDir(), "private"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	saved, e := s.Save(owner, "", 0, document11())
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Get(owner, saved.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	// Every key survives, the explicit grouping:false included (it is not a default to be dropped).
	want, _ := json.Marshal(document11())
	have, _ := json.Marshal(got.Layout)
	if string(want) != string(have) {
		t.Errorf("stored %s, saved %s", have, want)
	}
	if !strings.Contains(string(have), `"grouping":false`) || !strings.Contains(string(have), `"schema_version":"1.1"`) {
		t.Errorf("the stored document lost a key: %s", have)
	}
}

func TestDamagedOwnerFileCannotEraseHistory(t *testing.T) {
	for _, payload := range []string{"null", "[]", "{}", "[null]"} {
		t.Run(payload, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "private")
			store, e := layouts.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
			first, e := store.Save(owner, "", 0, document())
			if e != nil {
				t.Fatal(e)
			}
			files, e := os.ReadDir(dir)
			if e != nil {
				t.Fatal(e)
			}
			for _, file := range files {
				if strings.HasSuffix(file.Name(), ".json") {
					path := filepath.Join(dir, file.Name())
					info, _ := os.Stat(path)
					if info.Mode().Perm() != 0600 {
						t.Fatal("private file mode")
					}
					if e = os.WriteFile(path, []byte(payload), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			if _, e = store.List(owner); !errors.Is(e, r.ErrIntegrity) {
				t.Fatal("corruption listed as empty", e)
			}
			if _, e = store.Get(owner, first.ID, 1); !errors.Is(e, r.ErrIntegrity) {
				t.Fatal("corruption hidden", e)
			}
			if _, e = store.Save(owner, "", 0, document()); !errors.Is(e, r.ErrIntegrity) {
				t.Fatal("corruption overwritten", e)
			}
		})
	}
}
