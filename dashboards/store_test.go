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
