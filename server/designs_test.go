package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// S11-B: with design_store database, two servers on one store serve the same designs: a layout saved through one is read, published and
// listed through the other, and a stale save through the second is a conflict as it would be on one server.
func TestTwoServersShareTheDesignsOfTheStore(t *testing.T) {
	w := newWorld(t)
	w.cfg.DesignStore = "database"
	store := NewMemoryStoreForTests(time.Now)
	one := startWith(t, w, WithStoreForTests(store), WithPollIntervalForTests(5*time.Millisecond))
	two := startWith(t, w, WithStoreForTests(store), WithPollIntervalForTests(5*time.Millisecond))
	sharedDesigns(t, w, one, two)
}

// sharedDesigns is the check of two servers that keep their designs in one store (memory here, PostgreSQL in pgSharedDesigns).
func sharedDesigns(t *testing.T, w *world, one, two *started) {
	t.Helper()
	token := mint(t, w.keys[0], claims{})
	code, _, body := one.do(t, "GET", "/api/reporting/v1/catalog?integration_id="+w.finance, token, nil)
	var catalog []struct {
		Dataset json.RawMessage `json:"dataset"`
		Columns []struct {
			ID string `json:"id"`
		} `json:"columns"`
	}
	if code != 200 || json.Unmarshal(body, &catalog) != nil || len(catalog) == 0 {
		t.Fatal(code, string(body))
	}
	layout := map[string]any{"schema_version": "1.0", "binding": map[string]any{"integration_id": w.finance, "dataset": catalog[0].Dataset},
		"columns": []map[string]string{{"id": catalog[0].Columns[0].ID, "align": "left"}}}
	code, _, body = one.do(t, "POST", "/api/reporting/v1/layouts", token, map[string]any{"id": "", "expected_version": 0, "layout": layout})
	var saved struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	if code != 201 || json.Unmarshal(body, &saved) != nil || saved.Version != 1 {
		t.Fatal(code, string(body))
	}
	// The second server reads it, saves the next version and publishes it.
	if code, _, body = two.do(t, "GET", "/api/reporting/v1/layouts/"+saved.ID+"?version=1", token, nil); code != 200 {
		t.Fatal("the second server does not read the first's layout", code, string(body))
	}
	if code, _, body = two.do(t, "POST", "/api/reporting/v1/layouts", token, map[string]any{"id": saved.ID, "expected_version": 1, "layout": layout}); code != 201 {
		t.Fatal(code, string(body))
	}
	if code, _, body = two.do(t, "POST", "/api/reporting/v1/layouts/"+saved.ID+"/release", token, map[string]any{"action": "publish", "version": 2, "expected_revision": 0}); code != 200 {
		t.Fatal(code, string(body))
	}
	// The first server sees both, and a save against version 1 is now stale.
	code, _, body = one.do(t, "GET", "/api/reporting/v1/layouts/"+saved.ID+"/published", token, nil)
	if code != 200 || !strings.Contains(string(body), `"version":2`) {
		t.Fatal("the first server does not see the second's publication", code, string(body))
	}
	if code, _, body = one.do(t, "POST", "/api/reporting/v1/layouts", token, map[string]any{"id": saved.ID, "expected_version": 1, "layout": layout}); code != 409 {
		t.Fatal("a stale save across servers", code, string(body))
	}
	// The start line says where the designs are.
	for _, s := range []*started{one, two} {
		if got := s.log.events(t, "start")[0]["design_store"]; got != "database" {
			t.Errorf("the start line says design_store %v", got)
		}
	}
}
