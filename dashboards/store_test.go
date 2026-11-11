package dashboards_test

import (
	"encoding/json"
	"errors"
	"idp.local/reporting/dashboards"
	r "idp.local/reporting/reporting"
	"path/filepath"
	"strings"
	"testing"
)

func doc() dashboards.Document {
	id := "amount"
	return dashboards.Document{Schema: "1.0", Kind: "dashboard", Binding: dashboards.Binding{Integration: "source", Dataset: r.Ref{ID: "dataset", Version: "1"}}, Widgets: []dashboards.Widget{{Type: "metric", Field: &id}}}
}
func TestDashboardOwnerIsolationReopenAndImmutableVersions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "boards")
	s, e := dashboards.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	first, e := s.Save(owner, "", 0, doc())
	if e != nil {
		t.Fatal(e)
	}
	for _, other := range []r.Identity{{Tenant: "x", Issuer: "i", Subject: "s"}, {Tenant: "t", Issuer: "x", Subject: "s"}, {Tenant: "t", Issuer: "i", Subject: "x"}} {
		if _, e = s.Get(other, first.ID, 1); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("private board disclosed", e)
		}
		all, e := s.List(other)
		if e != nil || len(all) != 0 {
			t.Fatal("private listing")
		}
	}
	empty := doc()
	empty.Widgets = []dashboards.Widget{}
	if _, e = s.Save(owner, first.ID, 1, empty); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Save(owner, first.ID, 1, empty); !errors.Is(e, r.ErrConflict) {
		t.Fatal("version overwrite", e)
	}
	s.Close()
	s, e = dashboards.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	old, e := s.Get(owner, first.ID, 1)
	if e != nil || len(old.Layout.Widgets) != 1 || *old.Layout.Widgets[0].Field != "amount" {
		t.Fatal("lost immutable history", old, e)
	}
	latest, e := s.List(owner)
	if e != nil || len(latest) != 1 || latest[0].Version != 2 {
		t.Fatal(latest, e)
	}
}
func TestDashboardStrictWidgetShapesAndLimits(t *testing.T) {
	for _, text := range []string{
		`{"schema_version":"1.0","kind":"dashboard","binding":{"integration_id":"s","dataset":{"id":"d","version":"1"}},"widgets":[{"type":"metric","field":"x","columns":null}]}`,
		`{"schema_version":"1.0","kind":"dashboard","binding":{"integration_id":"s","dataset":{"id":"d","version":"1"}},"widgets":[{"type":"table","field":null,"columns":["x"]}]}`,
		`{"schema_version":"1.0","kind":"dashboard","binding":{"integration_id":"s","dataset":{"id":"d","version":"1"}},"widgets":null}`,
	} {
		var value dashboards.Document
		e := r.DecodeStrict(strings.NewReader(text), &value)
		if e == nil && dashboards.Validate(value) == nil {
			t.Fatal("invalid widget accepted")
		}
	}
	value := doc()
	value.Widgets = make([]dashboards.Widget, 13)
	for i := range value.Widgets {
		value.Widgets[i] = doc().Widgets[0]
	}
	if dashboards.Validate(value) == nil {
		t.Fatal("unbounded widgets")
	}
	b, _ := json.Marshal(doc())
	var loaded dashboards.Document
	if r.DecodeStrict(strings.NewReader(string(b)), &loaded) != nil || dashboards.Validate(loaded) != nil {
		t.Fatal("valid roundtrip")
	}
}

func TestDashboardReleasePersistsWithoutChangingDrafts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	s, e := dashboards.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	v1, e := s.Save(owner, "", 0, doc())
	if e != nil {
		t.Fatal(e)
	}
	state, e := s.Publish(owner, v1.ID, "publish", 1, 0)
	if e != nil || state.Revision != 1 || state.PublishedVersion != 1 {
		t.Fatal(state, e)
	}
	empty := doc()
	empty.Widgets = []dashboards.Widget{}
	if _, e = s.Save(owner, v1.ID, 1, empty); e != nil {
		t.Fatal(e)
	}
	current, e := s.Published(owner, v1.ID)
	if e != nil || current.Version != 1 || len(current.Layout.Widgets) != 1 {
		t.Fatal("draft changed publication", current, e)
	}
	if _, e = s.Publish(owner, v1.ID, "withdraw", 0, 0); !errors.Is(e, r.ErrConflict) {
		t.Fatal("stale release", e)
	}
	s.Close()
	s, e = dashboards.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	state, e = s.Release(owner, v1.ID)
	if e != nil || state.Revision != 1 || len(state.History) != 1 || state.History[0].Action != "publish" || state.History[0].At == "" {
		t.Fatal(state, e)
	}
	if _, e = s.Publish(owner, v1.ID, "withdraw", 0, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Published(owner, v1.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = s.Publish(owner, v1.ID, "publish", 1, 2); e != nil {
		t.Fatal("explicit rollback", e)
	}
}

func TestBarWidgetStrictShapeRoundtripAndReopen(t *testing.T) {
	category, field := "customer.id", "amount"
	d := doc()
	d.Widgets = []dashboards.Widget{{Type: "bar", Category: &category, Field: &field}}
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	var loaded dashboards.Document
	if e = r.DecodeStrict(strings.NewReader(string(b)), &loaded); e != nil || dashboards.Validate(loaded) != nil {
		t.Fatal("bar roundtrip", e)
	}
	for _, widget := range []string{`{"type":"bar","category":"c","field":"n","columns":null}`, `{"type":"bar","category":null,"field":"n"}`, `{"type":"bar","field":"n"}`, `{"type":"table","columns":["c"],"category":null}`, `{"type":"metric","field":"n","category":null}`} {
		var w dashboards.Widget
		e = json.Unmarshal([]byte(widget), &w)
		invalid := doc()
		invalid.Widgets = []dashboards.Widget{w}
		if e == nil && dashboards.Validate(invalid) == nil {
			t.Fatal("invalid variant accepted", widget)
		}
	}
	dir := filepath.Join(t.TempDir(), "charts")
	s, e := dashboards.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	entry, e := s.Save(owner, "", 0, d)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Publish(owner, entry.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = dashboards.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	current, e := s.Published(owner, entry.ID)
	if e != nil || current.Layout.Widgets[0].Type != "bar" || *current.Layout.Widgets[0].Category != "customer.id" {
		t.Fatal(current, e)
	}
}

// S21-A: a line and a pie are charts of the bar's shape (a category column and a numeric field, no columns); every widget may carry a
// title and a width; the exact field sets, the 12-widget and 64 KiB bounds and the refusal of unknown types stay.
func TestLineAndPieWidgetsAreChartsOfTheBarShape(t *testing.T) {
	category, field := "customer.id", "amount"
	d := doc()
	d.Widgets = []dashboards.Widget{{Type: "line", Category: &category, Field: &field}, {Type: "pie", Category: &category, Field: &field}}
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	var loaded dashboards.Document
	if e = r.DecodeStrict(strings.NewReader(string(b)), &loaded); e != nil || dashboards.Validate(loaded) != nil {
		t.Fatal("line and pie roundtrip", e)
	}
	if loaded.Widgets[0].Type != "line" || loaded.Widgets[1].Type != "pie" || *loaded.Widgets[1].Category != "customer.id" || *loaded.Widgets[1].Field != "amount" {
		t.Fatal(loaded.Widgets)
	}
	for _, widget := range []string{
		`{"type":"line","field":"n"}`,
		`{"type":"pie","category":"c"}`,
		`{"type":"line","category":"","field":"n"}`,
		`{"type":"pie","category":"c","field":""}`,
		`{"type":"line","category":null,"field":"n"}`,
		`{"type":"pie","category":"c","field":"n","columns":["c"]}`,
		`{"type":"line","category":"c","field":"n","columns":null}`,
		`{"type":"pie","category":"c","field":"n","colour":"red"}`,
		`{"type":"area","category":"c","field":"n"}`,
		`{"type":"scatter","category":"c","field":"n"}`,
		`{"type":"Line","category":"c","field":"n"}`,
		`{"type":"line","category":"` + strings.Repeat("c", 129) + `","field":"n"}`,
	} {
		var w dashboards.Widget
		e = json.Unmarshal([]byte(widget), &w)
		invalid := doc()
		invalid.Widgets = []dashboards.Widget{w}
		if e == nil && dashboards.Validate(invalid) == nil {
			t.Fatal("invalid chart accepted", widget)
		}
	}
	many := doc()
	for i := 0; i < 13; i++ {
		many.Widgets = append(many.Widgets, dashboards.Widget{Type: "pie", Category: &category, Field: &field})
	}
	if dashboards.Validate(many) == nil {
		t.Fatal("13 charts accepted")
	}
	many.Widgets = many.Widgets[:12]
	if e = dashboards.Validate(many); e != nil {
		t.Fatal("12 charts refused", e)
	}
}

func TestWidgetTitleAndWidthRules(t *testing.T) {
	category, field := "customer.id", "amount"
	for _, widget := range []string{
		`{"type":"metric","field":"n","title":"应收概览"}`,
		`{"type":"metric","field":"n","title":"x"}`,
		`{"type":"metric","field":"n","title":"` + strings.Repeat("标", 64) + `"}`,
		`{"type":"metric","field":"n","width":"half"}`,
		`{"type":"metric","field":"n","width":"full"}`,
		`{"type":"table","columns":["c"],"title":"明细","width":"full"}`,
		`{"type":"bar","category":"c","field":"n","title":"柱","width":"half"}`,
		`{"type":"line","category":"c","field":"n","title":"走势","width":"full"}`,
		`{"type":"pie","category":"c","field":"n","width":"full","title":"占比 (2026)"}`,
	} {
		var w dashboards.Widget
		if e := json.Unmarshal([]byte(widget), &w); e != nil {
			t.Fatal(widget, e)
		}
		valid := doc()
		valid.Widgets = []dashboards.Widget{w}
		if e := dashboards.Validate(valid); e != nil {
			t.Fatal("valid title or width refused", widget, e)
		}
	}
	for _, widget := range []string{
		`{"type":"metric","field":"n","title":""}`,
		`{"type":"metric","field":"n","title":"` + strings.Repeat("标", 65) + `"}`,
		`{"type":"metric","field":"n","title":"a\tb"}`,
		`{"type":"metric","field":"n","title":"a\nb"}`,
		`{"type":"metric","field":"n","title":"a\u0000b"}`,
		`{"type":"metric","field":"n","title":"a\u001fb"}`,
		`{"type":"metric","field":"n","title":"a\u007fb"}`,
		`{"type":"metric","field":"n","title":"a\u0085b"}`,
		`{"type":"metric","field":"n","title":null}`,
		`{"type":"metric","field":"n","title":7}`,
		`{"type":"metric","field":"n","title":["x"]}`,
		`{"type":"metric","field":"n","width":"third"}`,
		`{"type":"metric","field":"n","width":"Full"}`,
		`{"type":"metric","field":"n","width":""}`,
		`{"type":"metric","field":"n","width":null}`,
		`{"type":"metric","field":"n","width":2}`,
		`{"type":"table","columns":["c"],"width":"wide"}`,
		`{"type":"pie","category":"c","field":"n","title":"x","width":"both"}`,
	} {
		var w dashboards.Widget
		e := json.Unmarshal([]byte(widget), &w)
		invalid := doc()
		invalid.Widgets = []dashboards.Widget{w}
		if e == nil && dashboards.Validate(invalid) == nil {
			t.Fatal("invalid title or width accepted", widget)
		}
	}
	// The same rules on a document built in Go, not read from JSON.
	for text, ok := range map[string]bool{"看板": true, strings.Repeat("a", 64): true, " ": true, "": false, strings.Repeat("a", 65): false, "a\rb": false, "\xff": false} {
		title := text
		value := doc()
		value.Widgets = []dashboards.Widget{{Type: "line", Category: &category, Field: &field, Title: &title}}
		if (dashboards.Validate(value) == nil) != ok || dashboards.ValidTitle(text) != ok {
			t.Fatalf("title %q: want valid=%v", text, ok)
		}
	}
	for width, ok := range map[string]bool{"half": true, "full": true, "": false, "HALF": false, "100%": false} {
		w := width
		value := doc()
		value.Widgets = []dashboards.Widget{{Type: "pie", Category: &category, Field: &field, Width: &w}}
		if (dashboards.Validate(value) == nil) != ok {
			t.Fatalf("width %q: want valid=%v", width, ok)
		}
	}
	// The 64 KiB bound stays: twelve tables of a hundred long column ids.
	huge := doc()
	huge.Widgets = nil
	for i := 0; i < 12; i++ {
		columns := make([]string, 100)
		for j := range columns {
			columns[j] = strings.Repeat(string(rune('a'+i)), 120) + "." + strings.Repeat("0", 4) + string(rune('a'+j%26)) + string(rune('a'+j/26))
		}
		huge.Widgets = append(huge.Widgets, dashboards.Widget{Type: "table", Columns: columns})
	}
	if e := dashboards.Validate(huge); !errors.Is(e, r.ErrBudget) {
		t.Fatal("a document over 64 KiB accepted", e)
	}
}

func TestTitledAndWideChartsReopenAsSaved(t *testing.T) {
	category, field, title, full := "customer.id", "amount", "账龄走势 · 2026", "full"
	d := doc()
	d.Widgets = []dashboards.Widget{
		{Type: "line", Category: &category, Field: &field, Title: &title, Width: &full},
		{Type: "pie", Category: &category, Field: &field},
		{Type: "metric", Field: &field, Title: &title},
	}
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	text := string(b)
	if !strings.Contains(text, `"title":"账龄走势 · 2026"`) || !strings.Contains(text, `"width":"full"`) || strings.Count(text, `"width"`) != 1 || strings.Count(text, `"title"`) != 2 {
		t.Fatal("the optional keys are written only where set", text)
	}
	dir := filepath.Join(t.TempDir(), "titled")
	s, e := dashboards.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	entry, e := s.Save(owner, "", 0, d)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Publish(owner, entry.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = dashboards.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	current, e := s.Published(owner, entry.ID)
	if e != nil || len(current.Layout.Widgets) != 3 {
		t.Fatal(current, e)
	}
	line, pie, metric := current.Layout.Widgets[0], current.Layout.Widgets[1], current.Layout.Widgets[2]
	if line.Type != "line" || line.Title == nil || *line.Title != title || line.Width == nil || *line.Width != "full" {
		t.Fatal("line lost its title or width", line)
	}
	if pie.Type != "pie" || pie.Title != nil || pie.Width != nil {
		t.Fatal("pie gained a title or width", pie)
	}
	if metric.Type != "metric" || metric.Title == nil || *metric.Title != title || metric.Width != nil {
		t.Fatal("metric", metric)
	}
}
