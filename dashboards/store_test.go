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
