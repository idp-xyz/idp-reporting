package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// layoutExportSetup is the API with a layout store and a frozen snapshot of the demo's receivables.
func layoutExportSetup(t *testing.T) (*r.Service, http.Handler, *layouts.Store, string) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"plain": fakePDF{}}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := layouts.Open(filepath.Join(t.TempDir(), "private"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	auth, e := httpapi.DemoBearer(secret, p)
	if e != nil {
		t.Fatal(e)
	}
	h, e := httpapi.Handler(s, auth, store)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(contractsim.FinancialQuery())
	w := request(h, "POST", "/api/reporting/v1/snapshot-jobs", b, true)
	var j r.JobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return s, h, store, finish(t, s, h, j.ID).SnapshotID
}

// receivablesLayout is a schema 1.1 layout of the demo's receivables: the bucket first under its own label, centred, the customer, the
// open balance in parentheses, subtotals by bucket, a mark on negative balances and one on the current bucket.
func receivablesLayout() layouts.Document {
	q := contractsim.FinancialQuery()
	return layouts.Document{
		Schema: "1.1", Binding: layouts.Binding{Integration: q.Integration, Dataset: q.Dataset},
		Title: "应收账龄", Footnote: "小计为本表所列金额之和。",
		Columns: []layouts.Column{
			{ID: "aging.bucket", Align: "center", Label: "账龄"},
			{ID: "customer.id", Align: "left"},
			{ID: "ar.open_balance", Align: "right", Format: &layouts.Format{Negative: "parentheses"}},
		},
		Subtotals: &layouts.Subtotals{By: "aging.bucket"},
		Marks:     []layouts.Mark{{Column: "ar.open_balance", When: "lt", Value: "0", Mark: "bad"}, {Column: "aging.bucket", When: "eq", Value: "current", Mark: "good"}},
	}
}

func saveLayout(t *testing.T, h http.Handler, id string, expected int, d layouts.Document) layouts.Entry {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id, "expected_version": expected, "layout": d})
	w := request(h, "POST", "/api/reporting/v1/layouts", body, true)
	if w.Code != 201 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var entry layouts.Entry
	if json.Unmarshal(w.Body.Bytes(), &entry) != nil {
		t.Fatal(w.Body.String())
	}
	return entry
}

func exportWithLayout(h http.Handler, key, snapshot, layout, format string) *httptest.ResponseRecorder {
	body := map[string]string{"snapshot_id": snapshot, "layout_id": layout}
	if format != "" {
		body["format"] = format
	}
	return exportRequest(h, key, body)
}

func errorCodeOf(w *httptest.ResponseRecorder) string {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Error
}

func TestTheLayoutRoutesAcceptSchema11AndRefuseWhatTheDatasetForbids(t *testing.T) {
	_, h, _, _ := layoutExportSetup(t)
	entry := saveLayout(t, h, "", 0, receivablesLayout())
	w := request(h, "GET", "/api/reporting/v1/layouts/"+entry.ID+"?version=1", nil, true)
	var got layouts.Entry
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Layout.Schema != "1.1" || got.Layout.Title != "应收账龄" || got.Layout.Columns[0].Align != "center" || got.Layout.Subtotals == nil || len(got.Layout.Marks) != 2 || got.Layout.Columns[2].Format == nil || got.Layout.Columns[2].Format.Negative != "parentheses" {
		t.Fatalf("read back: %d %s", w.Code, w.Body.String())
	}
	// The wire form is the ADR's: snake_case keys, the format object, the marks' keys.
	if b := w.Body.String(); !strings.Contains(b, `"schema_version":"1.1"`) || !strings.Contains(b, `"subtotals":{"by":"aging.bucket"}`) || !strings.Contains(b, `"format":{"negative":"parentheses"}`) || !strings.Contains(b, `{"column":"ar.open_balance","when":"lt","value":"0","mark":"bad"}`) {
		t.Errorf("wire form: %s", b)
	}
	for name, c := range map[string]struct {
		change func(*layouts.Document)
		status int
	}{
		"a format on a string column": {func(d *layouts.Document) { d.Columns[1].Format = &layouts.Format{} }, 400},
		"subtotals by a metric":       {func(d *layouts.Document) { d.Subtotals.By = "ar.open_balance" }, 400},
		"an order on a string column": {func(d *layouts.Document) { d.Marks[0].Column = "customer.id" }, 400},
		"blank on a numeric column": {func(d *layouts.Document) {
			d.Marks[1].Column, d.Marks[1].When, d.Marks[1].Value = "ar.open_balance", "blank", ""
		}, 400},
		"a 1.0 layout with a 1.1 key":    {func(d *layouts.Document) { d.Schema = "1.0" }, 400},
		"a mark on a column not shown":   {func(d *layouts.Document) { d.Marks[0].Column = "ar.other" }, 400},
		"a column the dataset lacks":     {func(d *layouts.Document) { d.Columns[1].ID = "ar.other"; d.Marks = nil; d.Subtotals = nil }, 403},
		"a subtotal column not shown":    {func(d *layouts.Document) { d.Subtotals.By = "customer.other" }, 400},
		"an unknown key":                 {func(d *layouts.Document) { d.Columns[0].Align = "justify" }, 400},
		"a title over the bound":         {func(d *layouts.Document) { d.Title = strings.Repeat("长", 121) }, 400},
		"an ordered mark without number": {func(d *layouts.Document) { d.Marks[0].Value = "1,000" }, 400},
	} {
		d := receivablesLayout()
		c.change(&d)
		body, _ := json.Marshal(map[string]any{"id": "", "expected_version": 0, "layout": d})
		if w := request(h, "POST", "/api/reporting/v1/layouts", body, true); w.Code != c.status {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body.String(), c.status)
		} else if strings.Contains(w.Body.String(), "1,000") || strings.Contains(w.Body.String(), "ar.other") {
			t.Errorf("%s: the answer carries a value: %s", name, w.Body.String())
		}
	}
	// A key the schema does not have is 400 by the strict decoder.
	if w := request(h, "POST", "/api/reporting/v1/layouts", []byte(`{"id":"","expected_version":0,"layout":{"schema_version":"1.1","binding":{"integration_id":"x","dataset":{"id":"y","version":"1"}},"columns":[{"id":"a","align":"left","width":10}]}}`), true); w.Code != 400 {
		t.Errorf("an unknown column key: %d %s", w.Code, w.Body.String())
	}
}

func TestAnExportMayCarryALayout(t *testing.T) {
	s, h, _, snapshot := layoutExportSetup(t)
	entry := saveLayout(t, h, "", 0, receivablesLayout())
	// The plain export, for comparison.
	w := exportRequest(h, "plain", map[string]string{"snapshot_id": snapshot})
	var plainJob r.JobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &plainJob) != nil || plainJob.Layout != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	plainJob = finish(t, s, h, plainJob.ID)
	plain := request(h, "GET", "/api/reporting/v1/artifacts/"+plainJob.ArtifactID+"/content", nil, true)
	if plain.Code != 200 {
		t.Fatal(plain.Code)
	}
	// The export with the layout: the job names the layout version, so does the poll and the listing.
	w = exportWithLayout(h, "k1", snapshot, entry.ID, "")
	var j r.JobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil || j.Layout == nil || j.Layout.ID != entry.ID || j.Layout.Version != 1 || j.Format != "" {
		t.Fatalf("export with a layout: %d %s", w.Code, w.Body.String())
	}
	if b := w.Body.String(); !strings.Contains(b, `"layout":{"id":"`+entry.ID+`","version":1}`) {
		t.Errorf("the wire form of the layout: %s", b)
	}
	j = finish(t, s, h, j.ID)
	if j.Layout == nil || j.Layout.Version != 1 || j.ArtifactID == "" {
		t.Fatalf("finished: %+v", j)
	}
	w = request(h, "GET", "/api/reporting/v1/jobs?limit=5", nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"layout":{"id":"`+entry.ID+`","version":1}`) {
		t.Errorf("the listing does not name the layout: %d %s", w.Code, w.Body.String())
	}
	// The file differs from the plain export and carries the layout's title, labels and subtotals.
	w = request(h, "GET", "/api/reporting/v1/artifacts/"+j.ArtifactID+"/content", nil, true)
	if w.Code != 200 || bytes.Equal(w.Body.Bytes(), plain.Body.Bytes()) {
		t.Fatalf("the presented workbook: %d, same as plain %v", w.Code, bytes.Equal(w.Body.Bytes(), plain.Body.Bytes()))
	}
	f, e := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	rows, e := f.GetRows("Data", excelize.Options{RawCellValue: true})
	if e != nil || len(rows) < 2 || strings.Join(rows[0], "|") != "账龄|客户|应收余额|应收余额（精确）" {
		t.Errorf("the presented header: %v (%v)", rows, e)
	}
	subtotals := 0
	for _, row := range rows {
		if len(row) > 0 && row[0] == "小计" {
			subtotals++
		}
	}
	if subtotals != 3 {
		t.Errorf("%d subtotal rows, want one per bucket", subtotals)
	}
	meta, _ := f.GetRows("Metadata", excelize.Options{RawCellValue: true})
	named := map[string]string{}
	for _, row := range meta {
		if len(row) == 2 {
			named[row[0]] = row[1]
		}
	}
	if named["layout_id"] != entry.ID || named["layout_version"] != "1" || named["layout_title"] != "应收账龄" {
		t.Errorf("the workbook does not name the layout: %v", named)
	}
	// A replay of the key answers the same job; once the layout has a newer version the same key is a conflict, a new key exports it.
	if w = exportWithLayout(h, "k1", snapshot, entry.ID, ""); w.Code != 202 || !strings.Contains(w.Body.String(), `"job_id":"`+j.ID+`"`) {
		t.Errorf("replay: %d %s", w.Code, w.Body.String())
	}
	second := receivablesLayout()
	second.Title = "应收账龄（第二版）"
	if v2 := saveLayout(t, h, entry.ID, 1, second); v2.Version != 2 {
		t.Fatal(v2)
	}
	if w = exportWithLayout(h, "k1", snapshot, entry.ID, ""); w.Code != 409 || errorCodeOf(w) != "IDEMPOTENCY_CONFLICT" {
		t.Errorf("the key with a newer layout version: %d %s", w.Code, w.Body.String())
	}
	if w = exportWithLayout(h, "k2", snapshot, entry.ID, ""); w.Code != 202 || !strings.Contains(w.Body.String(), `"version":2}`) {
		t.Errorf("a new key: %d %s", w.Code, w.Body.String())
	}
	// The plain key with a layout, and the layout's key without one, are conflicts too.
	if w = exportWithLayout(h, "plain", snapshot, entry.ID, ""); w.Code != 409 {
		t.Errorf("the plain key with a layout: %d %s", w.Code, w.Body.String())
	}
	if w = exportRequest(h, "k2", map[string]string{"snapshot_id": snapshot}); w.Code != 409 {
		t.Errorf("the layout's key without one: %d %s", w.Code, w.Body.String())
	}
}

func TestAnExportRefusesALayoutItCannotUse(t *testing.T) {
	_, h, store, snapshot := layoutExportSetup(t)
	p := contractsim.DemoPrincipal()
	// A layout bound to another dataset, saved around the routes (the catalog would refuse it): 422.
	other := receivablesLayout()
	other.Binding.Dataset.Version = "9.0.0"
	elsewhere, e := store.Save(p.Identity, "", 0, other)
	if e != nil {
		t.Fatal(e)
	}
	if w := exportWithLayout(h, "e1", snapshot, elsewhere.ID, ""); w.Code != 422 || errorCodeOf(w) != "UNSUPPORTED_COMBINATION" {
		t.Errorf("a layout of another dataset: %d %s", w.Code, w.Body.String())
	}
	foreign := receivablesLayout()
	foreign.Binding.Integration = "other_product"
	if entry, e := store.Save(p.Identity, "", 0, foreign); e != nil {
		t.Fatal(e)
	} else if w := exportWithLayout(h, "e2", snapshot, entry.ID, ""); w.Code != 422 {
		t.Errorf("a layout of another integration: %d %s", w.Code, w.Body.String())
	}
	// Another person's layout: 404, as the layout routes answer.
	stranger := r.Identity{Tenant: p.Tenant, Issuer: p.Issuer, Subject: "someone_else"}
	theirs, e := store.Save(stranger, "", 0, receivablesLayout())
	if e != nil {
		t.Fatal(e)
	}
	if w := exportWithLayout(h, "e3", snapshot, theirs.ID, ""); w.Code != 404 {
		t.Errorf("another identity's layout: %d %s", w.Code, w.Body.String())
	}
	if w := exportWithLayout(h, "e4", snapshot, "0123456789abcdef0123456789abcdef", ""); w.Code != 404 {
		t.Errorf("an unknown layout: %d %s", w.Code, w.Body.String())
	}
	if w := exportWithLayout(h, "e5", snapshot, "not-an-id", ""); w.Code != 400 {
		t.Errorf("a malformed layout id: %d %s", w.Code, w.Body.String())
	}
	// A layout of the tenant's shared space is readable: its latest version is exported.
	team, e := store.SaveAs(r.TeamOf(p.Identity), r.ActorDigest(stranger), "", 0, receivablesLayout())
	if e != nil {
		t.Fatal(e)
	}
	if w := exportWithLayout(h, "e6", snapshot, team.ID, ""); w.Code != 202 || !strings.Contains(w.Body.String(), `"layout":{"id":"`+team.ID+`","version":1}`) {
		t.Errorf("a team layout: %d %s", w.Code, w.Body.String())
	}
	// A format whose writer cannot present: 422; an unknown format stays 422.
	if w := exportWithLayout(h, "e7", snapshot, team.ID, "plain"); w.Code != 422 {
		t.Errorf("a writer that cannot present: %d %s", w.Code, w.Body.String())
	}
	// A layout that no longer fits its dataset's rules (saved around the routes): refused, never applied in part.
	unfit := receivablesLayout()
	unfit.Columns[1].Format = &layouts.Format{}
	if entry, e := store.Save(p.Identity, "", 0, unfit); e != nil {
		t.Fatal(e)
	} else if w := exportWithLayout(h, "e8", snapshot, entry.ID, ""); w.Code != 400 || errorCodeOf(w) != "INVALID_REQUEST" {
		t.Errorf("a layout with a format on a string column: %d %s", w.Code, w.Body.String())
	}
	// Without a layout store a layout_id is unsupported.
	s2, h2 := setup(t)
	b, _ := json.Marshal(contractsim.FinancialQuery())
	w := request(h2, "POST", "/api/reporting/v1/snapshot-jobs", b, true)
	var j r.JobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	snap2 := finish(t, s2, h2, j.ID).SnapshotID
	if w := exportWithLayout(h2, "e9", snap2, team.ID, ""); w.Code != 422 {
		t.Errorf("a layout without a layout store: %d %s", w.Code, w.Body.String())
	}
}
