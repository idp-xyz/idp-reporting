package layouts_test

import (
	"errors"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"os"
	"path/filepath"
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
