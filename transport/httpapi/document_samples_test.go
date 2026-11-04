package httpapi_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// sampleRenderer is a renderer that records what it was asked to draw; it can draw 2.1 documents, as the samples are.
type sampleRenderer struct {
	last   doctemplates.Document
	values map[string]string
	table  *doctemplates.TableData
	calls  int
}

func (f *sampleRenderer) Render(_ context.Context, d doctemplates.Document, v map[string]string, _ []byte, t *doctemplates.TableData) ([]byte, error) {
	f.last, f.values, f.table, f.calls = d, v, t, f.calls+1
	return []byte("%PDF-1.7\nsample preview\n%%EOF"), nil
}
func (f *sampleRenderer) RenderWith(ctx context.Context, d doctemplates.Document, v map[string]string, b []byte, t *doctemplates.TableData, _ doctemplates.Resolved) ([]byte, error) {
	return f.Render(ctx, d, v, b, t)
}

type samplesFixture struct {
	h        http.Handler
	store    *doctemplates.Store
	renderer *sampleRenderer
	other    *bool
	p        r.Principal
}

func newSamplesFixture(t *testing.T, withRenderer bool) *samplesFixture {
	t.Helper()
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	source.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: source}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	demo, _ := httpapi.DemoBearer(secret, p)
	other := new(bool)
	auth := func(req *http.Request) (r.Principal, error) {
		v, e := demo(req)
		if *other {
			v.Subject = "other"
		}
		return v, e
	}
	fallback, _ := httpapi.Handler(service, auth)
	f := &samplesFixture{store: store, other: other, p: p, renderer: &sampleRenderer{}}
	var renderer httpapi.DocumentRenderer
	if withRenderer {
		renderer = f.renderer
	}
	if f.h, e = httpapi.WithDocumentTemplates(fallback, service, auth, store, renderer, nil); e != nil {
		t.Fatal(e)
	}
	return f
}

const samplesBase = "/api/reporting/v1/document-templates"

func (f *samplesFixture) copyBody(mapping map[string]any, name string) []byte {
	b, _ := json.Marshal(map[string]any{"dataset": map[string]string{"integration_id": contractsim.FinanceIntegration, "id": "receivables.aging", "version": "1.0.0"}, "name": name, "mapping": mapping})
	return b
}

func TestSamplesAreReadableByAnyIdentityAndNeverWritable(t *testing.T) {
	f := newSamplesFixture(t, true)
	if request(f.h, "GET", samplesBase+"/samples", nil, false).Code != 401 {
		t.Fatal("anonymous list")
	}
	w := request(f.h, "GET", samplesBase+"/samples", nil, true)
	var list []struct {
		Kind, Name, Domain, Flow, Preset string
		Elements, Fields                 int
		Rows                             any
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 37 {
		t.Fatal(w.Code, w.Body.String())
	}
	presets := map[string]string{}
	for _, s := range list {
		presets[s.Kind] = s.Preset
		if s.Name == "" || s.Domain == "" || s.Flow == "" || s.Elements < 2 || s.Fields < 1 || s.Rows != nil {
			t.Fatalf("a card lacks what it shows, or carries rows: %+v", s)
		}
	}
	if presets["statement"] != "a4" || presets["usps"] != "label_4x6in" || presets["wms_sku_label"] != "label_150x100" {
		t.Fatal(presets)
	}
	*f.other = true // any signed-in identity reads them
	w = request(f.h, "GET", samplesBase+"/samples/usps", nil, true)
	pack, _, _ := doctemplates.SampleByKind("usps")
	var one struct {
		Kind       string
		FieldSlots []map[string]any `json:"field_slots"`
		Rows       []map[string]string
		Document   doctemplates.Document
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &one) != nil || one.Kind != "usps" || len(one.FieldSlots) != len(pack.Fields) || len(one.Rows) != len(pack.Rows) || one.Document.Page == nil || one.Document.Page.Preset != "label_4x6in" {
		t.Fatal(w.Code, w.Body.String())
	}
	if request(f.h, "GET", samplesBase+"/samples/nothing", nil, true).Code != 404 {
		t.Fatal("unknown sample")
	}
	// Never writable: no verb but the three routes'.
	for _, c := range [][2]string{{"POST", samplesBase + "/samples"}, {"PUT", samplesBase + "/samples/usps"}, {"DELETE", samplesBase + "/samples/usps"}, {"POST", samplesBase + "/samples/usps"}} {
		if code := request(f.h, c[0], c[1], []byte(`{}`), true).Code; code != 405 && code != 404 {
			t.Fatalf("%s %s: %d", c[0], c[1], code)
		}
	}
	// A template's own routes still work beside them (the id patterns do not collide with "samples").
	if request(f.h, "GET", samplesBase, nil, true).Code != 200 {
		t.Fatal("the listing of templates")
	}
}

func TestSamplePreviewRendersTheSamplesOwnRowsWithoutADataset(t *testing.T) {
	f := newSamplesFixture(t, true)
	w := request(f.h, "POST", samplesBase+"/samples/statement/previews", []byte(`{}`), true)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "%PDF-") || w.Header().Get("X-Content-SHA256") == "" || f.renderer.calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	pack, _, _ := doctemplates.SampleByKind("statement")
	if f.renderer.values["e7"] != pack.Rows[0]["customer"] || f.renderer.values["e9"] != pack.Rows[0]["statement"] || f.renderer.table == nil || len(f.renderer.table.Rows) != len(pack.Rows) || len(pack.Rows) != 28 {
		t.Fatalf("not the sample's own rows: %v", f.renderer.values)
	}
	if f.renderer.values["e15"] != "224800.00" {
		t.Fatalf("a group sum of the sample's own rows: %q", f.renderer.values["e15"])
	}
	if f.renderer.last.Binding != doctemplates.SampleBinding {
		t.Fatal("a preview is bound to no dataset of the caller's")
	}
	// No body at all is as good as {}; another key is not.
	q := httptest.NewRequest("POST", samplesBase+"/samples/usps/previews", nil)
	q.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, q)
	if rec.Code != 200 {
		t.Fatal("no body", rec.Code)
	}
	if request(f.h, "POST", samplesBase+"/samples/usps/previews", []byte(`{"x":1}`), true).Code != 400 {
		t.Fatal("a body with a key")
	}
	if request(f.h, "POST", samplesBase+"/samples/usps/previews", []byte(`{}`), false).Code != 401 {
		t.Fatal("anonymous")
	}
	if request(f.h, "POST", samplesBase+"/samples/nothing/previews", []byte(`{}`), true).Code != 404 {
		t.Fatal("unknown")
	}
	// Without a renderer the route is not there, as a template's own preview is not.
	g := newSamplesFixture(t, false)
	if request(g.h, "POST", samplesBase+"/samples/statement/previews", []byte(`{}`), true).Code == 200 {
		t.Fatal("a preview without a renderer")
	}
}

func TestSampleCopyAppliesTheMappingOnTheServerAndSavesLikeASave(t *testing.T) {
	f := newSamplesFixture(t, false)
	mapping := map[string]any{
		"customer":  map[string]string{"column": "customer.id"},
		"statement": map[string]string{"literal": "ST-上海-1"},
		"date":      map[string]string{"column": "aging.bucket"},
		"ref":       map[string]string{"column": "customer.id"},
		"debit":     map[string]string{"column": "ar.open_balance"},
		// desc and credit are unmapped: a table column has no literal, so both are dropped
	}
	w := request(f.h, "POST", samplesBase+"/samples/statement/copy", f.copyBody(mapping, "我的对账单"), true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved struct {
		ID      string
		Version int
		Config  doctemplates.Document
	}
	json.Unmarshal(w.Body.Bytes(), &saved)
	d := saved.Config
	if saved.Version != 1 || d.Name != "我的对账单" || d.Binding.Integration != contractsim.FinanceIntegration || d.Binding.Dataset.ID != "receivables.aging" || d.Page == nil || d.Page.Preset != "a4" {
		t.Fatal("not bound to the caller's dataset as version 1", saved)
	}
	if d.Group != nil {
		t.Fatalf("the statement has no group: %+v", d.Group)
	}
	cols := d.TableItem().Columns
	if len(cols) != 3 || cols[0].Field != "aging.bucket" || cols[1].Field != "customer.id" || cols[2].Field != "ar.open_balance" {
		t.Fatalf("columns %+v", cols)
	}
	sum := 0.0
	for _, c := range cols {
		sum += c.Width
	}
	if math.Abs(sum-d.TableItem().Width) > 1e-9 {
		t.Fatalf("the widths of the dropped columns are shared: %v of %v", sum, d.TableItem().Width)
	}
	byID := map[string]doctemplates.Item{}
	for _, bi := range d.Items() {
		byID[bi.Item.ID] = bi.Item
	}
	if it := byID["e9"]; it.Source != "literal" || it.Text != "ST-上海-1" || it.Field != "" {
		t.Fatalf("a literal mapping: %+v", it)
	}
	if it := byID["e7"]; it.Source != "field" || it.Field != "customer.id" {
		t.Fatalf("a column mapping: %+v", it)
	}
	if it := byID["e15"]; it.Source != "group_sum" || it.Field != "ar.open_balance" {
		t.Fatalf("a group sum: %+v", it)
	}
	if it := byID["e17"]; it.Source != "literal" || it.Text != "[本期已收]" {
		t.Fatalf("a group sum whose column is unmapped: %+v", it)
	}
	if it := byID["e11"]; it.Source != "literal" || it.Text != "[业务期间]" {
		t.Fatalf("an unmapped field: %+v", it)
	}
	// The copy is the caller's: readable, listed with the sample's domain and flow, and counted by the store.
	if request(f.h, "GET", samplesBase+"/"+saved.ID+"?version=1", nil, true).Code != 200 {
		t.Fatal("the copy is not readable")
	}
	var list []struct {
		ID      string
		Catalog struct{ Domain, Flow string }
	}
	json.Unmarshal(request(f.h, "GET", samplesBase, nil, true).Body.Bytes(), &list)
	if len(list) != 1 || list[0].Catalog.Domain != "通用业务" || list[0].Catalog.Flow != "财务" {
		t.Fatal(list)
	}
	// Another identity cannot read it.
	*f.other = true
	if request(f.h, "GET", samplesBase+"/"+saved.ID+"?version=1", nil, true).Code != 404 || request(f.h, "GET", samplesBase+"/"+saved.ID+"/catalog", nil, true).Code != 404 {
		t.Fatal("another identity read a copy")
	}
	var none []any
	json.Unmarshal(request(f.h, "GET", samplesBase, nil, true).Body.Bytes(), &none)
	if len(none) != 0 {
		t.Fatal("another identity lists a copy")
	}
	*f.other = false
	// The name defaults to the sample's.
	w = request(f.h, "POST", samplesBase+"/samples/statement/copy", f.copyBody(map[string]any{"customer": map[string]string{"column": "customer.id"}, "ref": map[string]string{"column": "aging.bucket"}, "debit": map[string]string{"column": "ar.open_balance"}}, ""), true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &saved)
	if saved.Config.Name != "客户往来对账单" {
		t.Fatal("the name defaults to the sample's", saved.Config.Name)
	}
	// A fixed form is a form: no detail band, no group, the sample's own page; a literal mapping and an unmapped slot are as in any copy.
	w = request(f.h, "POST", samplesBase+"/samples/usps/copy", f.copyBody(map[string]any{"trackingNumber": map[string]string{"column": "customer.id"}, "recipientName": map[string]string{"literal": "测试收件人"}}, "我的面单"), true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &saved)
	if !saved.Config.Form() || saved.Config.Group != nil || saved.Config.Page.Preset != "label_4x6in" || saved.Config.TableItem() != nil {
		t.Fatalf("a form: %+v", saved.Config.Bands)
	}
	for _, bi := range saved.Config.Items() {
		if bi.Item.ID == "e9" && (bi.Item.Source != "literal" || bi.Item.Text != "测试收件人") {
			t.Fatalf("a literal on a form: %+v", bi.Item)
		}
		if bi.Item.ID == "e10" && (bi.Item.Source != "literal" || bi.Item.Text != "[收件人公司]") {
			t.Fatalf("an unmapped slot on a form: %+v", bi.Item)
		}
		if bi.Item.ID == "e15" && (bi.Item.Source != "field" || bi.Item.Field != "customer.id") {
			t.Fatalf("a column on a form: %+v", bi.Item)
		}
	}
}

func TestSampleCopyIsRefusedLikeASave(t *testing.T) {
	f := newSamplesFixture(t, false)
	url := samplesBase + "/samples/statement/copy"
	ok := map[string]any{"customer": map[string]string{"column": "customer.id"}, "ref": map[string]string{"column": "customer.id"}, "debit": map[string]string{"column": "ar.open_balance"}}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range ok {
			m[a] = b
		}
		m[k] = v
		return m
	}
	cases := []struct {
		name string
		url  string
		body []byte
		want int
	}{
		{"a column the dataset lacks", url, f.copyBody(with("desc", map[string]string{"column": "nothing"}), ""), 403},
		{"a slot the sample lacks", url, f.copyBody(with("nothing", map[string]string{"literal": "x"}), ""), 400},
		{"a mapping with both", url, f.copyBody(with("desc", map[string]string{"column": "aging.bucket", "literal": "x"}), ""), 400},
		{"a mapping with neither", url, f.copyBody(with("desc", map[string]string{}), ""), 400},
		{"a group sum of a dimension", url, f.copyBody(with("debit", map[string]string{"column": "aging.bucket"}), ""), 403},
		{"a name of 81 characters", url, f.copyBody(ok, strings.Repeat("名", 81)), 400},
		{"no table column left", url, f.copyBody(map[string]any{}, ""), 400},
		{"a text over 256 characters", url, f.copyBody(with("statement", map[string]string{"literal": strings.Repeat("a", 257)}), ""), 400},
		{"a dataset the caller cannot read", url, []byte(`{"dataset":{"integration_id":"` + contractsim.FinanceIntegration + `","id":"nothing","version":"1"},"name":"","mapping":{}}`), 403},
		{"no dataset", url, []byte(`{"name":"","mapping":{}}`), 400},
		{"no mapping", url, []byte(`{"dataset":{"integration_id":"` + contractsim.FinanceIntegration + `","id":"receivables.aging","version":"1.0.0"}}`), 400},
		{"another key", url, []byte(`{"dataset":{"integration_id":"x","id":"y","version":"1"},"mapping":{},"x":1}`), 400},
	}
	for _, c := range cases {
		if w := request(f.h, "POST", c.url, c.body, true); w.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
	}
	if request(f.h, "POST", url, f.copyBody(ok, ""), false).Code != 401 || request(f.h, "POST", samplesBase+"/samples/nothing/copy", f.copyBody(ok, ""), true).Code != 404 {
		t.Fatal("anonymous or unknown sample")
	}
	// A body over the document bound (1 MiB) is 413 before it is parsed.
	big := f.copyBody(with("statement", map[string]string{"literal": strings.Repeat("a", 1<<20)}), "")
	if w := request(f.h, "POST", url, big, true); w.Code != 413 {
		t.Errorf("a body over 1 MiB: %d", w.Code)
	}
	var list []any
	json.Unmarshal(request(f.h, "GET", samplesBase, nil, true).Body.Bytes(), &list)
	if len(list) != 0 {
		t.Fatalf("a refused copy was stored: %d", len(list))
	}
}

func TestSampleCopyCountsAgainstThePerIdentityCap(t *testing.T) {
	f := newSamplesFixture(t, false)
	if doctemplates.MaxTemplates != 64 {
		t.Fatal("the cap is 64")
	}
	url := samplesBase + "/samples/usps/copy"
	body := f.copyBody(map[string]any{"trackingNumber": map[string]string{"column": "customer.id"}}, "")
	for i := range 64 {
		if w := request(f.h, "POST", url, body, true); w.Code != 201 {
			t.Fatalf("copy %d: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	if w := request(f.h, "POST", url, body, true); w.Code != 413 {
		t.Fatalf("the 65th copy: %d", w.Code)
	}
	// A plain save is bound by the same cap; another identity has its own 64.
	save, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": savedTemplate("customer.id")})
	if w := request(f.h, "POST", samplesBase, save, true); w.Code != 413 {
		t.Fatalf("a save at the cap: %d", w.Code)
	}
	otherID := r.Identity{Tenant: f.p.Tenant, Issuer: f.p.Issuer, Subject: "other"}
	if _, e := f.store.Save(otherID, "", 0, savedTemplate("customer.id")); e != nil {
		t.Fatalf("another identity has its own 64: %v", e)
	}
}

func TestCatalogMetadataRoundTripConflictLimitsAndLastOpened(t *testing.T) {
	f := newSamplesFixture(t, false)
	save, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": savedTemplate("customer.id")})
	var saved struct{ ID string }
	json.Unmarshal(request(f.h, "POST", samplesBase, save, true).Body.Bytes(), &saved)
	path := samplesBase + "/" + saved.ID + "/catalog"
	type catalog struct {
		Domain, Flow        string
		Favourite, Archived bool
		Collections         []string
		LastOpened          string `json:"last_opened"`
		Revision            int
	}
	get := func() catalog {
		var c catalog
		w := request(f.h, "GET", path, nil, true)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return c
	}
	if c := get(); c.Revision != 0 || c.Domain != "" || c.Favourite || c.Collections == nil || len(c.Collections) != 0 || c.LastOpened != "" {
		t.Fatalf("a template that never had metadata: %+v", c)
	}
	put := func(m map[string]any) int {
		b, _ := json.Marshal(m)
		return request(f.h, "PUT", path, b, true).Code
	}
	whole := func(rev int, collections []string) map[string]any {
		return map[string]any{"domain": "仓储 WMS", "flow": "入库", "favourite": true, "archived": false, "collections": collections, "expected_revision": rev}
	}
	if put(whole(0, []string{"常用", "月结"})) != 200 {
		t.Fatal("put")
	}
	c := get()
	if c.Revision != 1 || c.Domain != "仓储 WMS" || c.Flow != "入库" || !c.Favourite || len(c.Collections) != 2 {
		t.Fatalf("round trip: %+v", c)
	}
	if put(whole(0, nil)) != 400 || put(whole(0, []string{})) != 409 {
		t.Fatal("a stale revision is a conflict (and a null collections list is no list)")
	}
	if get().Revision != 1 {
		t.Fatal("a conflict changed the revision")
	}
	names := make([]string, 17)
	for i := range names {
		names[i] = "c" + strconv.Itoa(i)
	}
	for name, m := range map[string]map[string]any{
		"17 collections":      whole(1, names),
		"41 characters":       whole(1, []string{strings.Repeat("集", 41)}),
		"a repeated name":     whole(1, []string{"a", "a"}),
		"an empty name":       whole(1, []string{""}),
		"a control character": whole(1, []string{"a\nb"}),
		"space around":        whole(1, []string{" a"}),
		"a domain of 41":      func() map[string]any { m := whole(1, []string{}); m["domain"] = strings.Repeat("域", 41); return m }(),
		"a missing key":       {"domain": "", "flow": "", "favourite": false, "collections": []string{}, "expected_revision": 1},
		"an unknown key":      func() map[string]any { m := whole(1, []string{}); m["last_opened"] = "2026-01-01T00:00:00Z"; return m }(),
		"a negative revision": whole(-1, []string{}),
	} {
		if code := put(m); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if put(whole(1, names[:16])) != 200 || put(whole(2, []string{strings.Repeat("集", 40)})) != 200 {
		t.Fatal("16 collections and 40 characters are the limits")
	}
	// last_opened is the server's: set by reading a version, not by the revision, kept by a change.
	if get().LastOpened != "" {
		t.Fatal("opened before any read")
	}
	rev := get().Revision
	if request(f.h, "GET", samplesBase+"/"+saved.ID+"?version=1", nil, true).Code != 200 {
		t.Fatal("read")
	}
	c = get()
	if _, err := time.Parse(time.RFC3339Nano, c.LastOpened); err != nil || c.Revision != rev {
		t.Fatalf("last_opened %q, revision %d (was %d)", c.LastOpened, c.Revision, rev)
	}
	opened := c.LastOpened
	if put(whole(rev, []string{"x"})) != 200 || get().LastOpened != opened {
		t.Fatal("a change of the metadata kept the time")
	}
	var list []struct {
		ID      string
		Catalog catalog
	}
	json.Unmarshal(request(f.h, "GET", samplesBase, nil, true).Body.Bytes(), &list)
	if len(list) != 1 || list[0].Catalog.LastOpened != opened || !list[0].Catalog.Favourite {
		t.Fatalf("the listing carries it: %+v", list)
	}
	// Per identity and unknown ids.
	*f.other = true
	if request(f.h, "GET", path, nil, true).Code != 404 || put(whole(0, []string{})) != 404 {
		t.Fatal("another identity")
	}
	*f.other = false
	if request(f.h, "GET", samplesBase+"/00000000000000000000000000000000/catalog", nil, true).Code != 404 || request(f.h, "GET", samplesBase+"/zz/catalog", nil, true).Code != 400 {
		t.Fatal("unknown or malformed id")
	}
	// A denied read is not an open.
	if request(f.h, "GET", path, nil, false).Code != 401 {
		t.Fatal("anonymous")
	}
}

// S06-B part 3: a card of the template centre shows the template's name, schema, page preset and published version, which the listing carries so that
// the page does not open every template (opening one records last_opened, and a listing must not make every template "recent").
func TestListingCarriesWhatACardShowsAndOpensNothing(t *testing.T) {
	f := newSamplesFixture(t, false)
	label := request(f.h, "POST", samplesBase+"/samples/usps/copy", f.copyBody(map[string]any{"trackingNumber": map[string]string{"column": "customer.id"}}, "标签副本"), true)
	if label.Code != 201 {
		t.Fatalf("copy: %d %s", label.Code, label.Body.String())
	}
	var made struct{ ID string }
	json.Unmarshal(label.Body.Bytes(), &made)
	save, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "config": savedTemplate("customer.id")})
	if request(f.h, "POST", samplesBase, save, true).Code != 201 {
		t.Fatal("a 1.x save")
	}
	publish, _ := json.Marshal(map[string]any{"action": "publish", "version": 1, "expected_revision": 0})
	if w := request(f.h, "POST", samplesBase+"/"+made.ID+"/release", publish, true); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	var list []struct {
		ID               string
		Name             string
		Schema           string `json:"schema_version"`
		Preset           string
		PublishedVersion int `json:"published_version"`
		Catalog          struct {
			LastOpened string `json:"last_opened"`
		}
	}
	for range 2 {
		if w := request(f.h, "GET", samplesBase, nil, true); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(list) != 2 {
		t.Fatalf("%d templates", len(list))
	}
	for _, item := range list {
		switch item.ID {
		case made.ID:
			if item.Name != "标签副本" || item.Schema != "2.1" || item.Preset != "label_4x6in" || item.PublishedVersion != 1 {
				t.Fatalf("a published label: %+v", item)
			}
		default:
			if item.Name != "" || item.Schema == "" || item.Preset != "a4" || item.PublishedVersion != 0 {
				t.Fatalf("an unnamed unpublished 1.x template is A4: %+v", item)
			}
		}
		if item.Catalog.LastOpened != "" {
			t.Fatalf("listing opened a template: %+v", item)
		}
	}
}
