package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/dashboards"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/layouts"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"os"
	"testing"
	"time"
)

// countedCatalog counts the times the source is asked for its catalog.
type countedCatalog struct {
	r.Adapter
	asked int
}

func (a *countedCatalog) DescribeCatalog(ctx context.Context, sc r.SourceContext) ([]r.Dataset, error) {
	a.asked++
	return a.Adapter.DescribeCatalog(ctx, sc)
}

// countedBackend counts the reads of an owner's document in a design store.
type countedBackend struct {
	privatedocs.Backend
	loads *int
}

func (b countedBackend) Load(owner r.Identity) ([]byte, error) {
	*b.loads++
	return b.Backend.Load(owner)
}

// A listing authorizes every entry it shows. What the entries share it asks once per request: the source's catalog of their integration
// (before: once per entry, which with the http adapter is a request to the product each), the release states (before: a read of the
// owner's whole document per entry) and the caller's backgrounds and images (before: a read of the whole store per reference). The
// answers are what they were.
func TestAListingAsksTheSourceAndReadsEachStoreOncePerRequest(t *testing.T) {
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	source.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	adapter := &countedCatalog{Adapter: source}
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: adapter}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	loads := map[string]*int{}
	backend := func(name string) privatedocs.Backend {
		loads[name] = new(int)
		return countedBackend{privatedocs.NewMemoryBackend(), loads[name]}
	}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	templates, e := doctemplates.OpenBackend(backend("templates"))
	must(e)
	defer templates.Close()
	assets, e := doctemplates.OpenAssetsBackend(backend("backgrounds"), backend("images"))
	must(e)
	defer assets.Close()
	layoutStore, e := layouts.OpenBackend(backend("layouts"))
	must(e)
	defer layoutStore.Close()
	boards, e := dashboards.OpenBackend(backend("dashboards"))
	must(e)
	defer boards.Close()
	auth, e := httpapi.DemoBearer(secret, p)
	must(e)
	h, e := httpapi.Handler(service, auth, layoutStore)
	must(e)
	h, e = httpapi.WithDashboards(h, service, auth, boards)
	must(e)
	h, e = httpapi.WithDocumentAssets(h, service, auth, assets)
	must(e)
	h, e = httpapi.WithDocumentTemplates(h, service, auth, templates, nil, assets)
	must(e)
	post := func(path string, input any, want int, out any) {
		t.Helper()
		b, _ := json.Marshal(input)
		w := request(h, "POST", "/api/reporting/v1/"+path, b, true)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		if out != nil {
			must(json.Unmarshal(w.Body.Bytes(), out))
		}
	}
	plain := savedTemplate("ar.open_balance")
	binding := plain.Binding
	// Two backgrounds and two images; four templates without either, four with a background, four with an image; two of them published.
	raw, e := os.ReadFile("../../doctemplates/testdata/a4-background.png")
	must(e)
	var background doctemplates.BackgroundRef
	var picture doctemplates.ImageSummary
	for range 2 {
		post("document-backgrounds", map[string]any{"binding": binding, "png": base64.StdEncoding.EncodeToString(raw)}, 201, &background)
		post("document-images", map[string]any{"binding": binding, "image": base64.StdEncoding.EncodeToString(pngBytes(t, 120, 60))}, 201, &picture)
	}
	printed := savedTemplate("ar.open_balance")
	printed.Schema, printed.Print, printed.Background = "1.2", &doctemplates.PrintProfile{Mode: "full"}, &background
	coded := (&codedFixture{}).codedTemplate(refOf(picture))
	var saved []string
	for i := range 12 {
		var entry struct {
			ID string `json:"id"`
		}
		post("document-templates", map[string]any{"id": "", "expected_version": 0, "config": []doctemplates.Document{plain, printed, coded}[i%3]}, 201, &entry)
		saved = append(saved, entry.ID)
	}
	published := map[string]bool{saved[1]: true, saved[5]: true}
	for id := range published {
		post("document-templates/"+id+"/release", map[string]any{"action": "publish", "version": 1, "expected_revision": 0}, 200, nil)
	}
	for range 6 {
		post("layouts", map[string]any{"id": "", "expected_version": 0, "layout": map[string]any{"schema_version": "1.0", "binding": binding, "columns": []map[string]string{{"id": "ar.open_balance", "align": "right"}}}}, 201, nil)
		post("dashboards", map[string]any{"id": "", "expected_version": 0, "config": map[string]any{"schema_version": "1.0", "kind": "dashboard", "binding": binding, "widgets": []map[string]string{{"type": "metric", "field": "ar.open_balance"}}}}, 201, nil)
	}
	for _, c := range []struct {
		path    string
		entries int
		reads   map[string]int // of each store, by its name; a store that is not named is not read
	}{
		// The templates: the entries, their catalogue metadata and their release states are a read each.
		{"document-templates", 12, map[string]int{"templates": 3, "backgrounds": 1, "images": 1}},
		{"team/document-templates", 0, map[string]int{"templates": 3}},
		{"layouts", 6, map[string]int{"layouts": 1}},
		{"dashboards", 6, map[string]int{"dashboards": 1}},
		{"document-backgrounds", 2, map[string]int{"backgrounds": 1}},
		{"document-images", 2, map[string]int{"images": 1}},
	} {
		adapter.asked = 0
		for _, n := range loads {
			*n = 0
		}
		w := request(h, "GET", "/api/reporting/v1/"+c.path, nil, true)
		var entries []map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &entries) != nil || len(entries) != c.entries {
			t.Fatalf("%s: %d %s", c.path, w.Code, w.Body.String())
		}
		want := 1
		if c.entries == 0 {
			want = 0
		}
		if adapter.asked != want {
			t.Errorf("%s: the source was asked for its catalog %d times for %d entries of one integration, want %d", c.path, adapter.asked, c.entries, want)
		}
		for name, n := range loads {
			if *n != c.reads[name] {
				t.Errorf("%s: the %s store was read %d times for %d entries, want %d", c.path, name, *n, c.entries, c.reads[name])
			}
		}
		if c.path != "document-templates" {
			continue
		}
		// The answer is what it was: every template, in the order saved, each with what it has published.
		for i, entry := range entries {
			version := 0
			if published[saved[i]] {
				version = 1
			}
			if entry["id"] != saved[i] || entry["published_version"] != float64(version) {
				t.Errorf("template %d: %v", i, entry)
			}
		}
	}
	// A reference the caller's assets do not hold is told to a listing as it is told to a read of the template: a template of another
	// person's image is not listed (not found), and the others are.
	stranger := contractsim.DemoPrincipal()
	stranger.Subject = "another"
	foreign, e := assets.Images.Put(t.Context(), stranger.Identity, binding, pngBytes(t, 30, 30))
	must(e)
	if _, e = templates.Save(p.Identity, "", 0, (&codedFixture{}).codedTemplate(refOf(foreign))); e != nil {
		t.Fatal(e)
	}
	w := request(h, "GET", "/api/reporting/v1/document-templates", nil, true)
	var entries []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &entries) != nil || len(entries) != 12 {
		t.Fatalf("with a template of another person's image: %d %s", w.Code, w.Body.String())
	}
}
