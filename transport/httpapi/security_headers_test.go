package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/dashboards"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/embedding"
	"idp.local/reporting/layouts"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// headerRenderer previews and checks without Typst.
type headerRenderer struct{}

func (headerRenderer) Render(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) ([]byte, error) {
	return []byte("%PDF-1.7\nheaders\n%%EOF"), nil
}
func (headerRenderer) CheckIssue(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) error {
	return nil
}

// headerCase is one request to a route and the status its answer must have.
type headerCase struct {
	name   string
	method string
	path   string
	body   any
	status int
	// keeps are headers the route sets deliberately; the wrapper must not take them away.
	keeps []string
}

// TestEveryRouteAnswersWithTheSecurityHeaders asks every route of every mount, a success and a refusal each (and, of each, the answer to
// a caller who is not authenticated), and requires that the answer carries Cache-Control: no-store, X-Content-Type-Options: nosniff and
// X-Reporting-Data-Class exactly once, as the core API's does, and is unframeable: X-Frame-Options DENY and a policy whose one frame-ancestors source is 'none' (ADR 0006). Each With* mount is asked over a bare fallback, so that its headers can
// only be its own: the routes it adds answer through the wrapper it applies, not through the one of the handler beneath it.
func TestEveryRouteAnswersWithTheSecurityHeaders(t *testing.T) {
	v := newIssueHTTP(t, nil)
	p, service, ctx := v.p, v.service, t.Context()
	auth, _ := httpapi.DemoBearer(secret, p)
	v.publish()
	// One official issue, issued: its record, its job and its artifact exist.
	code, out := v.post(v.input(2), "hdr-issue")
	if code != 202 {
		t.Fatal(code, out)
	}
	issueID := out["job_id"].(string)
	if _, e := service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	issued, e := service.Job(ctx, p, issueID)
	if e != nil || issued.ArtifactID == "" {
		t.Fatal(e, issued.State)
	}
	// A queued job to cancel; a second saved template to publish.
	queued, e := service.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "hdr-queued")
	if e != nil {
		t.Fatal(e)
	}
	second, e := v.store.Save(p.Identity, "", 0, savedTemplate("customer.id"))
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := service.Catalog(ctx, p, contractsim.FinanceIntegration)
	if e != nil {
		t.Fatal(e)
	}
	binding := doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: catalog[0].Ref}
	var numeric string
	for _, c := range catalog[0].Columns {
		if c.Type == "decimal" || c.Type == "integer" {
			numeric = c.ID
			break
		}
	}
	layoutStore, e := layouts.Open(filepath.Join(t.TempDir(), "layouts"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { layoutStore.Close() })
	layout, e := layoutStore.Save(p.Identity, "", 0, layouts.Document{Schema: "1.0", Binding: layouts.Binding{Integration: binding.Integration, Dataset: binding.Dataset}, Columns: []layouts.Column{{ID: catalog[0].Columns[0].ID, Align: "left"}}})
	if e != nil {
		t.Fatal(e)
	}
	boardStore, e := dashboards.Open(filepath.Join(t.TempDir(), "boards"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { boardStore.Close() })
	board, e := boardStore.Save(p.Identity, "", 0, dashboards.Document{Schema: "1.0", Kind: "dashboard", Binding: dashboards.Binding{Integration: binding.Integration, Dataset: binding.Dataset}, Widgets: []dashboards.Widget{{Type: "metric", Field: &numeric}}})
	if e != nil {
		t.Fatal(e)
	}
	assets, e := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { assets.Close() })
	image, e := assets.Images.Put(ctx, p.Identity, binding, pngBytes(t, 120, 60))
	if e != nil {
		t.Fatal(e)
	}
	background, e := os.ReadFile("../../doctemplates/testdata/a4-background.png")
	if e != nil {
		t.Fatal(e)
	}
	bare := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { http.NotFound(w, req) })
	renderer := headerRenderer{}
	core, e := httpapi.Handler(service, auth, layoutStore)
	if e != nil {
		t.Fatal(e)
	}
	withDashboards, e := httpapi.WithDashboards(bare, service, auth, boardStore)
	if e != nil {
		t.Fatal(e)
	}
	withAssets, e := httpapi.WithDocumentAssets(bare, service, auth, assets)
	if e != nil {
		t.Fatal(e)
	}
	withPreviews, e := httpapi.WithDocumentPreviews(bare, service, auth, renderer, assets)
	if e != nil {
		t.Fatal(e)
	}
	withTemplates, e := httpapi.WithDocumentTemplates(bare, service, auth, v.store, renderer, assets)
	if e != nil {
		t.Fatal(e)
	}
	embeddingConfig, e := embedding.New(map[string][]string{contractsim.FinanceIntegration: {"https://app.brant.example"}}, embedding.Options{})
	if e != nil {
		t.Fatal(e)
	}
	withEmbedding, e := httpapi.WithEmbedding(bare, service, auth, embeddingConfig)
	if e != nil {
		t.Fatal(e)
	}
	const base = "/api/reporting/v1/"
	// An identifier of the right shape that nothing holds.
	const missing = "00000000000000000000000000000000"
	query := contractsim.FinancialQuery()
	// A private document's six routes and its collection, for a store of the given base; second is a saved version 1 not yet published.
	private := func(path, id, release string, create any) []headerCase {
		return []headerCase{
			{"list", "GET", path, nil, 200, nil},
			{"create", "POST", path, create, 201, nil},
			{"create refused", "POST", path, map[string]any{}, 400, nil},
			{"get", "GET", path + "/" + id + "?version=1", nil, 200, nil},
			{"get refused", "GET", path + "/" + id, nil, 400, nil},
			{"release history", "GET", path + "/" + id + "/release", nil, 200, nil},
			{"release history refused", "GET", path + "/" + missing + "/release", nil, 404, nil},
			{"published refused", "GET", path + "/" + release + "/published", nil, 404, nil},
			{"publish", "POST", path + "/" + release + "/release", map[string]any{"action": "publish", "version": 1, "expected_revision": 0}, 200, nil},
			{"publish refused", "POST", path + "/" + release + "/release", map[string]any{"action": "publish", "version": 1, "expected_revision": 0}, 409, nil},
			{"published", "GET", path + "/" + release + "/published", nil, 200, nil},
		}
	}
	mounts := []struct {
		name    string
		handler http.Handler
		cases   []headerCase
	}{
		{"core", core, append([]headerCase{
			{"session", "GET", base + "session", nil, 200, nil},
			{"capabilities", "GET", base + "capabilities", nil, 200, nil},
			{"catalog", "GET", base + "catalog?integration_id=" + contractsim.FinanceIntegration, nil, 200, nil},
			{"catalog refused", "GET", base + "catalog", nil, 400, nil},
			{"query", "POST", base + "queries", query, 200, nil},
			{"query refused", "POST", base + "queries", map[string]any{"tenant": "other"}, 400, nil},
			{"snapshot job", "POST", base + "snapshot-jobs", query, 202, nil},
			{"snapshot job refused", "POST", base + "snapshot-jobs", map[string]any{"tenant": "other"}, 400, nil},
			{"job", "GET", base + "jobs/" + issueID, nil, 200, nil},
			{"job refused", "GET", base + "jobs/" + missing, nil, 404, nil},
			{"cancel", "POST", base + "jobs/" + queued.ID + "/cancel", nil, 200, nil},
			{"cancel refused", "POST", base + "jobs/" + missing + "/cancel", nil, 404, nil},
			{"rows", "GET", base + "snapshots/" + v.snapshot + "/rows", nil, 200, nil},
			{"rows refused", "GET", base + "snapshots/" + v.snapshot + "/rows?limit=x", nil, 400, nil},
			{"export", "POST", base + "exports", map[string]any{"snapshot_id": v.snapshot}, 202, nil},
			{"export refused", "POST", base + "exports", map[string]any{"snapshot_id": missing}, 404, nil},
			{"artifact", "GET", base + "artifacts/" + issued.ArtifactID + "/content", nil, 200, []string{"Content-Disposition", "X-Content-SHA256"}},
			{"artifact refused", "GET", base + "artifacts/" + missing + "/content", nil, 404, nil},
			{"issues", "GET", base + "issues", nil, 200, nil},
			{"issues refused", "GET", base + "issues?limit=0", nil, 400, nil},
			{"issue", "GET", base + "issues/" + issueID, nil, 200, nil},
			{"issue refused", "GET", base + "issues/" + missing, nil, 404, nil},
			// The core API alone cannot issue; the template mount overrides this route.
			{"issue unsupported", "POST", base + "issues", v.input(2), 422, nil},
		}, private(base+"layouts", layout.ID, layout.ID, map[string]any{"id": "", "expected_version": 0, "layout": layouts.Document{Schema: "1.0", Binding: layouts.Binding{Integration: binding.Integration, Dataset: binding.Dataset}, Columns: []layouts.Column{{ID: catalog[0].Columns[0].ID, Align: "left"}}}})...)},
		{"dashboards", withDashboards, private(base+"dashboards", board.ID, board.ID, map[string]any{"id": "", "expected_version": 0, "config": dashboards.Document{Schema: "1.0", Kind: "dashboard", Binding: dashboards.Binding{Integration: binding.Integration, Dataset: binding.Dataset}, Widgets: []dashboards.Widget{{Type: "metric", Field: &numeric}}}})},
		{"embedding", withEmbedding, []headerCase{
			{"capabilities", "GET", base + "capabilities", nil, 200, nil},
		}},
		{"assets and images", withAssets, []headerCase{
			{"background upload", "POST", base + "document-backgrounds", map[string]any{"binding": binding, "png": base64.StdEncoding.EncodeToString(background)}, 201, nil},
			{"background upload refused", "POST", base + "document-backgrounds", map[string]any{"binding": binding, "png": "bad base64!"}, 400, nil},
			{"background list", "GET", base + "document-backgrounds", nil, 200, nil},
			{"image upload", "POST", base + "document-images", map[string]any{"binding": binding, "image": base64.StdEncoding.EncodeToString(pngBytes(t, 80, 40))}, 201, nil},
			{"image upload refused", "POST", base + "document-images", map[string]any{"binding": binding, "image": "bad base64!"}, 400, nil},
			{"image list", "GET", base + "document-images", nil, 200, nil},
			{"image content", "GET", base + "document-images/" + image.ID + "/content", nil, 200, []string{"Content-Disposition", "Content-Security-Policy", "X-Content-SHA256"}},
			{"image content refused", "GET", base + "document-images/" + missing + "/content", nil, 404, nil},
		}},
		{"previews and checks", withPreviews, []headerCase{
			{"preview", "POST", base + "document-previews", map[string]any{"config": savedTemplate("ar.open_balance"), "snapshot_id": v.snapshot, "row_index": 2}, 200, []string{"Content-Disposition", "X-Content-SHA256"}},
			{"preview refused", "POST", base + "document-previews", map[string]any{}, 400, nil},
			{"check", "POST", base + "document-checks", map[string]any{"config": savedTemplate("ar.open_balance"), "snapshot_id": v.snapshot, "rows": []int{0, 1}}, 200, nil},
			{"check refused", "POST", base + "document-checks", map[string]any{}, 400, nil},
		}},
		{"templates", withTemplates, append(private(base+"document-templates", v.template, second.ID, map[string]any{"id": "", "expected_version": 0, "config": savedTemplate("customer.id")}), []headerCase{
			{"saved preview", "POST", base + "document-templates/" + v.template + "/previews", map[string]any{"version": 1, "snapshot_id": v.snapshot, "row_index": 2}, 200, []string{"Content-Disposition", "X-Content-SHA256", "X-Template-ID"}},
			{"saved preview refused", "POST", base + "document-templates/" + v.template + "/previews", map[string]any{}, 400, nil},
			{"issue", "POST", base + "issues", v.input(2), 202, []string{"Location"}},
			{"issue refused", "POST", base + "issues", map[string]any{}, 400, nil},
		}...)},
	}
	keys := 0
	call := func(h http.Handler, c headerCase, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if c.body != nil {
			raw, _ = json.Marshal(c.body)
		}
		if !authorized {
			return request(h, c.method, c.path, raw, false)
		}
		keys++
		return requestWithKey(h, c.method, c.path, raw, fmt.Sprintf("hdr-%d", keys))
	}
	for _, m := range mounts {
		for _, c := range m.cases {
			for _, authorized := range []bool{true, false} {
				who, want := "authenticated", c.status
				if !authorized {
					who, want = "anonymous", 401
				}
				t.Run(m.name+"/"+c.name+"/"+who, func(t *testing.T) {
					w := call(m.handler, c, authorized)
					if w.Code != want {
						t.Fatalf("%s %s: got %d want %d: %s", c.method, c.path, w.Code, want, w.Body.String())
					}
					for name, value := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "X-Reporting-Data-Class": "contract-simulator"} {
						if got := w.Header().Values(name); len(got) != 1 || got[0] != value {
							t.Errorf("%s %s answered %d with %s %q, want exactly %q", c.method, c.path, w.Code, name, got, value)
						}
					}
					// No answer of the API can be framed (ADR 0006): the one frame-ancestors source is 'none', in the policy a route has of its
					// own as well, and X-Frame-Options says DENY.
					if got := w.Header().Values("X-Frame-Options"); len(got) != 1 || got[0] != "DENY" {
						t.Errorf("%s %s answered %d with X-Frame-Options %q", c.method, c.path, w.Code, got)
					}
					if got := w.Header().Values("Content-Security-Policy"); len(got) != 1 || strings.Count(got[0], "frame-ancestors") != 1 || !strings.Contains(got[0], "frame-ancestors 'none'") {
						t.Errorf("%s %s answered %d with Content-Security-Policy %q", c.method, c.path, w.Code, got)
					}
					if authorized {
						for _, name := range c.keeps {
							if w.Header().Get(name) == "" {
								t.Errorf("%s %s lost its own %s", c.method, c.path, name)
							}
						}
					}
				})
			}
		}
	}
}
