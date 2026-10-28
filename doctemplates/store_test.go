package doctemplates_test

import (
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"path/filepath"
	"testing"
)

func TestTemplatePrivateVersionsPublicationAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "templates")
	s, e := doctemplates.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	d := document()
	v1, e := s.Save(owner, "", 0, d)
	if e != nil {
		t.Fatal(e)
	}
	for _, other := range []r.Identity{{Tenant: "x", Issuer: "i", Subject: "s"}, {Tenant: "t", Issuer: "x", Subject: "s"}, {Tenant: "t", Issuer: "i", Subject: "x"}} {
		if _, e = s.Get(other, v1.ID, 1); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("owner leak", e)
		}
	}
	if _, e = s.Publish(owner, v1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	d.Elements[0].Align = "left"
	if _, e = s.Save(owner, v1.ID, 1, d); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Save(owner, v1.ID, 1, d); !errors.Is(e, r.ErrConflict) {
		t.Fatal("draft CAS", e)
	}
	if _, e = s.Publish(owner, v1.ID, "withdraw", 0, 0); !errors.Is(e, r.ErrConflict) {
		t.Fatal("release CAS", e)
	}
	s.Close()
	s, e = doctemplates.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	old, e := s.Published(owner, v1.ID)
	if e != nil || old.Version != 1 || old.Layout.Elements[0].Align != "right" {
		t.Fatal("draft replaced published", old, e)
	}
	if _, e = s.Publish(owner, v1.ID, "withdraw", 0, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Published(owner, v1.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal(e)
	}
}

func TestLegacyAndPrintVersionsReopenWithoutRewriting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "print-versions")
	s, e := doctemplates.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	d := document()
	first, e := s.Save(owner, "", 0, d)
	if e != nil {
		t.Fatal(e)
	}
	d.Schema = "1.1"
	d.Print = &doctemplates.PrintProfile{Mode: "variables", OffsetX: 1.2, OffsetY: -.7}
	d.Elements[0].X = 20.1
	d.Elements[0].Y = 35.2
	d.Elements[0].Width = 170
	if _, e = s.Save(owner, first.ID, 1, d); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = doctemplates.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	old, e := s.Get(owner, first.ID, 1)
	if e != nil || old.Layout.Schema != "1.0" || old.Layout.Print != nil || old.Layout.Elements[0].X != 10 {
		t.Fatal("legacy changed", old, e)
	}
	current, e := s.Get(owner, first.ID, 2)
	if e != nil || current.Layout.Print == nil || current.Layout.Print.OffsetX != 1.2 || current.Layout.Elements[0].X != 20.1 {
		t.Fatal("print profile lost", current, e)
	}
}
