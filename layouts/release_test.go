package layouts_test

import (
	"encoding/json"
	"errors"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateReleaseIsPinnedAndWithdrawable(t *testing.T) {
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
	if _, e = s.Published(owner, first.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("draft exposed")
	}
	released, e := s.Publish(owner, first.ID, "publish", 1, 0)
	if e != nil || released.Revision != 1 || released.PublishedVersion != 1 {
		t.Fatal(released, e)
	}
	changed := document()
	changed.Columns[0].Align = "left"
	if _, e = s.Save(owner, first.ID, 1, changed); e != nil {
		t.Fatal(e)
	}
	p, e := s.Published(owner, first.ID)
	if e != nil || p.Version != 1 || p.Layout.Columns[0].Align != "right" {
		t.Fatal("draft changed published layout", p, e)
	}
	if _, e = s.Publish(owner, first.ID, "publish", 2, 0); !errors.Is(e, r.ErrConflict) {
		t.Fatal("stale publication accepted")
	}
	other := owner
	other.Subject = "other"
	if _, e = s.Release(other, first.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("release disclosed")
	}
	if _, e = s.Published(other, first.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("published metadata disclosed")
	}
	if _, e = s.Publish(owner, first.ID, "withdraw", 0, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Published(owner, first.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("withdrawn exposed")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = layouts.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	state, e := s.Release(owner, first.ID)
	if e != nil || state.Revision != 2 || state.PublishedVersion != 0 || len(state.History) != 2 {
		t.Fatal(state, e)
	}
	if _, e = s.Publish(owner, first.ID, "publish", 1, 2); e != nil {
		t.Fatal("explicit rollback publish", e)
	}
	for revision := 3; revision < 64; revision++ {
		if _, e = s.Publish(owner, first.ID, "publish", 1, revision); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.Publish(owner, first.ID, "publish", 1, 64); !errors.Is(e, r.ErrBudget) {
		t.Fatal("release event budget", e)
	}
}
func TestLegacyLayoutMigrationPreservesDefinitions(t *testing.T) {
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
	files, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	var path string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".json") {
			path = filepath.Join(dir, f.Name())
		}
	}
	data, _ := json.Marshal([][]layouts.Entry{{first}})
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	old, e := s.Get(owner, first.ID, 1)
	if e != nil || old.ID != first.ID || old.Layout.Binding != first.Layout.Binding {
		t.Fatal("legacy changed", e)
	}
	if _, e = s.Publish(owner, first.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	data, e = os.ReadFile(path)
	if e != nil || !strings.Contains(string(data), `"schema_version":2`) {
		t.Fatal("schema not upgraded")
	}
	published, e := s.Published(owner, first.ID)
	if e != nil || published.Version != 1 || published.Layout.Columns[0] != first.Layout.Columns[0] {
		t.Fatal("migration altered layout", e)
	}
	var db map[string]any
	json.Unmarshal(data, &db)
	records := db["records"].([]any)
	records[0].(map[string]any)["releases"] = nil
	bad, _ := json.Marshal(db)
	os.WriteFile(path, bad, 0600)
	if _, e = s.Release(owner, first.ID); !errors.Is(e, r.ErrIntegrity) {
		t.Fatal("null releases accepted", e)
	}
}
