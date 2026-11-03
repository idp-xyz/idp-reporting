package privatedocs_test

import (
	"errors"
	"path/filepath"
	"testing"

	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

func openString(t *testing.T, dir string, cap int) *privatedocs.Store[string] {
	t.Helper()
	open := func(validate func(string) error, binding func(string) string) (*privatedocs.Store[string], error) {
		if cap == 0 {
			return privatedocs.Open(dir, validate, binding)
		}
		return privatedocs.OpenCap(dir, cap, validate, binding)
	}
	s, e := open(func(string) error { return nil }, func(string) string { return "b" })
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestTheDefaultCapStaysSixteenAndACapIsChosenAtOpen(t *testing.T) {
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	s := openString(t, filepath.Join(t.TempDir(), "d"), 0)
	defer s.Close()
	for i := range privatedocs.DefaultCap {
		if _, e := s.Save(owner, "", 0, "x"); e != nil {
			t.Fatalf("record %d: %v", i+1, e)
		}
	}
	if _, e := s.Save(owner, "", 0, "x"); !errors.Is(e, r.ErrBudget) || privatedocs.DefaultCap != 16 {
		t.Fatalf("the 17th of a default store: %v", e)
	}
	if _, e := privatedocs.OpenCap(filepath.Join(t.TempDir(), "x"), 0, func(string) error { return nil }, func(string) string { return "" }); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("a cap of 0")
	}
}

func TestAStoreOpenedWithALowerCapThanItWasWrittenWithReadsAsCorrupt(t *testing.T) {
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	dir := filepath.Join(t.TempDir(), "d")
	s := openString(t, dir, 40)
	for range 20 {
		if _, e := s.Save(owner, "", 0, "x"); e != nil {
			t.Fatal(e)
		}
	}
	s.Close()
	low := openString(t, dir, 0)
	defer low.Close()
	if _, e := low.List(owner); !errors.Is(e, r.ErrIntegrity) {
		t.Fatalf("20 records under a cap of 16: %v", e)
	}
}

func TestCatalogValidation(t *testing.T) {
	ok := privatedocs.Catalog{Domain: "d", Flow: "f", Collections: []string{"a"}}
	if privatedocs.ValidCatalog(ok) != nil {
		t.Fatal("a valid catalog")
	}
	for name, c := range map[string]privatedocs.Catalog{
		"nil collections": {Collections: nil},
		"a tab":           {Domain: "a\tb", Collections: []string{}},
		"padded":          {Flow: " f", Collections: []string{}},
		"duplicate":       {Collections: []string{"a", "a"}},
		"invalid UTF-8":   {Collections: []string{"\xff"}},
	} {
		if privatedocs.ValidCatalog(c) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
