package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// checkCall is what the route handed the checker for one row.
type checkCall struct {
	values     map[string]string
	background int
	table      *doctemplates.TableData
}

// checkingRenderer is a renderer that can also check: the route must never render to check.
type checkingRenderer struct {
	calls   []checkCall
	check   func(call checkCall) error
	renders int
}

func (c *checkingRenderer) Render(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) ([]byte, error) {
	c.renders++
	return nil, r.ErrUnsupported
}
func (c *checkingRenderer) CheckIssue(_ context.Context, _ doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData) error {
	call := checkCall{values: values, background: len(background), table: table}
	c.calls = append(c.calls, call)
	if c.check != nil {
		return c.check(call)
	}
	return nil
}

type checkFixture struct {
	t        *testing.T
	service  *r.Service
	source   *contractsim.Simulator
	owner    r.Owner
	adapter  *changingLayoutCatalog
	snapshot string
	rows     [][]string
	handler  http.Handler
	renderer *checkingRenderer
	other    bool
	assets   *doctemplates.Assets
}

// newCheckFixture freezes a snapshot of the given customer values (one row each) for the demo
// identity and mounts the previews, and so the check route, over a checking renderer.
func newCheckFixture(t *testing.T, customers []string, withAssets bool) *checkFixture {
	t.Helper()
	p := contractsim.DemoPrincipal()
	q := contractsim.FinancialQuery()
	d := r.Dataset{Ref: q.Dataset, Dimensions: q.Dimensions, Metrics: q.Metrics, CurrencyPolicy: q.CurrencyPolicy, Timezone: q.Timezone, Columns: []r.Column{{ID: "customer.id", Label: "客户", Type: "string"}, {ID: "aging.bucket", Label: "账龄区间", Type: "string"}, {ID: "ar.open_balance", Label: "应收余额", Type: "decimal", Scale: 2, Currency: "CNY"}}}
	var records []contractsim.Record
	var keys []string
	for i, c := range customers {
		key := fmt.Sprintf("r%03d", i)
		keys = append(keys, key)
		records = append(records, contractsim.Record{Company: "company_demo", Key: key, Values: []string{c, "current", fmt.Sprintf("%d.00", 100+i)}})
	}
	source := contractsim.New(d, records)
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	grant := contractsim.DefaultGrant()
	grant.Rows = keys
	source.Grant(owner, grant)
	f := &checkFixture{t: t, source: source, owner: owner, adapter: &changingLayoutCatalog{Adapter: source}, renderer: &checkingRenderer{}}
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: f.adapter}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	f.service = service
	job, e := service.SubmitSnapshot(t.Context(), p, q, "check-test")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.RunOne(t.Context()); e != nil {
		t.Fatal(e)
	}
	if job, e = service.Job(t.Context(), p, job.ID); e != nil || job.SnapshotID == "" {
		t.Fatal(e, job.State)
	}
	f.snapshot = job.SnapshotID
	snap, e := service.AuthorizedSnapshot(t.Context(), p, f.snapshot)
	if e != nil {
		t.Fatal(e)
	}
	f.rows = snap.Result.Rows
	demo, _ := httpapi.DemoBearer(secret, p)
	auth := func(req *http.Request) (r.Principal, error) {
		v, e := demo(req)
		if f.other {
			v.Subject = "other"
		}
		return v, e
	}
	fallback, _ := httpapi.Handler(service, auth)
	if withAssets {
		f.assets, e = doctemplates.OpenAssets(filepath.Join(t.TempDir(), "assets"))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { f.assets.Close() })
		if fallback, e = httpapi.WithDocumentAssets(fallback, service, auth, f.assets); e != nil {
			t.Fatal(e)
		}
	}
	if f.handler, e = httpapi.WithDocumentPreviews(fallback, service, auth, f.renderer, f.assets); e != nil {
		t.Fatal(e)
	}
	return f
}

func (f *checkFixture) config() doctemplates.Document {
	return doctemplates.Document{Schema: "1.0", Kind: "document", Binding: doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset}, Elements: []doctemplates.Element{
		{ID: "n1", Source: "row", Field: "customer.id", X: 10, Y: 25, Width: 100, Height: 10, FontSize: 12, Align: "left"},
		{ID: "n2", Source: "row", Field: "ar.open_balance", X: 10, Y: 40, Width: 100, Height: 10, FontSize: 12, Align: "right"},
		{ID: "n3", Source: "literal", Text: "title", X: 10, Y: 55, Width: 100, Height: 10, FontSize: 12, Align: "left"},
	}}
}

type checkAnswer struct {
	Rows []struct {
		RowIndex int      `json:"row_index"`
		OK       bool     `json:"ok"`
		Code     string   `json:"code"`
		Reasons  []string `json:"reasons"`
	} `json:"rows"`
}

func (f *checkFixture) post(input map[string]any, authorized bool) (int, string, checkAnswer) {
	f.t.Helper()
	if _, ok := input["config"]; !ok {
		input["config"] = f.config()
	}
	if _, ok := input["snapshot_id"]; !ok {
		input["snapshot_id"] = f.snapshot
	}
	body, _ := json.Marshal(input)
	w := request(f.handler, "POST", "/api/reporting/v1/document-checks", body, authorized)
	var answer checkAnswer
	if w.Code == 200 {
		if w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			f.t.Fatalf("headers: %v", w.Header())
		}
		if e := json.Unmarshal(w.Body.Bytes(), &answer); e != nil {
			f.t.Fatal(e, w.Body.String())
		}
	}
	return w.Code, w.Body.String(), answer
}

func numbers(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func TestDocumentChecksAreAuthorizedLikeAPreview(t *testing.T) {
	f := newCheckFixture(t, []string{"alpha", "beta", "gamma"}, false)
	rows := map[string]any{"rows": []int{0, 1}}
	if code, _, _ := f.post(rows, false); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code, body, answer := f.post(rows, true); code != 200 || len(answer.Rows) != 2 || len(f.renderer.calls) != 2 || f.renderer.renders != 0 {
		t.Fatalf("%d %s calls=%d renders=%d", code, body, len(f.renderer.calls), f.renderer.renders)
	}
	// Another identity's snapshot is not found.
	f.renderer.calls = nil
	f.other = true
	if code, _, _ := f.post(rows, true); code != 404 || len(f.renderer.calls) != 0 {
		t.Fatalf("another identity's snapshot: %d", code)
	}
	f.other = false
	// A binding that is not the snapshot's, and a field the catalog does not authorize, are denied
	// before anything is checked; so is a table column the catalog does not authorize.
	wrong := f.config()
	wrong.Binding.Integration = "other"
	if code, _, _ := f.post(map[string]any{"rows": []int{0}, "config": wrong}, true); code != 403 {
		t.Fatalf("wrong binding: %d", code)
	}
	if code, _, _ := f.post(map[string]any{"samples": "boundary", "config": wrong}, true); code != 403 {
		t.Fatalf("wrong binding by samples: %d", code)
	}
	f.adapter.allowed = "customer.id"
	if code, _, _ := f.post(rows, true); code != 403 || len(f.renderer.calls) != 0 {
		t.Fatalf("unauthorized field: %d", code)
	}
	withTable := f.config()
	withTable.Elements = withTable.Elements[:1]
	withTable.Schema, withTable.Print = "1.3", &doctemplates.PrintProfile{Mode: "full"}
	withTable.Table = &doctemplates.Table{X: 10, Y: 100, Width: 190, Height: 100, FontSize: 9, Columns: []doctemplates.TableColumn{{Field: "customer.id", Width: 90}, {Field: "ar.open_balance", Width: 100}}}
	if code, _, _ := f.post(map[string]any{"rows": []int{0}, "config": withTable}, true); code != 403 || len(f.renderer.calls) != 0 {
		t.Fatalf("unauthorized table column: %d", code)
	}
	f.adapter.allowed = ""
	// The table the checker gets is the snapshot's, in the template's column order.
	if code, body, _ := f.post(map[string]any{"rows": []int{0}, "config": withTable}, true); code != 200 || len(f.renderer.calls) != 1 {
		t.Fatalf("table: %d %s", code, body)
	} else if table := f.renderer.calls[0].table; table == nil || len(table.Columns) != 2 || table.Columns[0].ID != "customer.id" || len(table.Rows) != 3 || table.Metric[0] || !table.Metric[1] {
		t.Fatalf("bound table: %+v", table)
	}
	// An invalid template is an ordinary problem answer, as is a field the snapshot lacks.
	bad := f.config()
	bad.Elements[0].Width = 5
	if code, _, _ := f.post(map[string]any{"rows": []int{0}, "config": bad}, true); code != 400 {
		t.Fatalf("invalid template: %d", code)
	}
	f.adapter.extra = "later.column"
	later := f.config()
	later.Elements[0].Field = "later.column"
	f.renderer.calls = nil
	if code, _, _ := f.post(map[string]any{"rows": []int{0}, "config": later}, true); code != 403 || len(f.renderer.calls) != 0 {
		t.Fatalf("field the snapshot lacks: %d", code)
	}
	if code, _, _ := f.post(map[string]any{"samples": "boundary", "config": later}, true); code != 403 || len(f.renderer.calls) != 0 {
		t.Fatalf("field the snapshot lacks, sampled: %d", code)
	}
	f.adapter.extra = ""
	// A template that names a background on a server without a background store is unsupported.
	withBackground := f.config()
	withBackground.Schema, withBackground.Print = "1.2", &doctemplates.PrintProfile{Mode: "full"}
	withBackground.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64)}
	if code, _, _ := f.post(map[string]any{"rows": []int{0}, "config": withBackground}, true); code != 422 {
		t.Fatalf("background without a store: %d", code)
	}
}

func TestDocumentChecksAuthorizeAgainBeforeAnswering(t *testing.T) {
	f := newCheckFixture(t, []string{"alpha", "beta", "gamma"}, false)
	rows := map[string]any{"rows": []int{0, 1, 2}}
	f.renderer.check = func(checkCall) error { f.source.Revoke(f.owner); return nil }
	if code, body, _ := f.post(rows, true); code != 403 || strings.Contains(body, "row_index") {
		t.Fatalf("source revoked while checking: %d %s", code, body)
	}
	f.source.Grant(f.owner, contractsim.DefaultGrant())
	f.renderer.check = func(checkCall) error { f.adapter.allowed = "customer.id"; return nil }
	f.renderer.calls = nil
	if code, body, _ := f.post(rows, true); code != 403 || strings.Contains(body, "row_index") {
		t.Fatalf("field revoked while checking: %d %s", code, body)
	}
}

func TestDocumentChecksBoundTheRequestAndTheRows(t *testing.T) {
	var customers []string
	for i := 0; i < 60; i++ {
		customers = append(customers, fmt.Sprintf("customer-%02d", i))
	}
	f := newCheckFixture(t, customers, false)
	if len(f.rows) != 60 {
		t.Fatalf("fixture has %d rows", len(f.rows))
	}
	for name, input := range map[string]map[string]any{
		"neither":              {},
		"both":                 {"rows": []int{0}, "samples": "boundary"},
		"another sampling":     {"samples": "random"},
		"empty sampling":       {"samples": ""},
		"null rows":            {"rows": nil},
		"no rows":              {"rows": []int{}},
		"a negative row":       {"rows": []int{-1}},
		"a duplicate":          {"rows": []int{3, 4, 3}},
		"one past the last":    {"rows": []int{60}},
		"one past, with some":  {"rows": []int{0, 1, 60}},
		"fifty-one rows":       {"rows": numbers(51)},
		"a fractional row":     {"rows": []float64{1.5}},
		"rows as text":         {"rows": "1"},
		"caller values":        {"rows": []int{0}, "values": map[string]string{"n1": "evil"}},
		"an unknown key":       {"rows": []int{0}, "extra": true},
		"a template key":       {"rows": []int{0}, "config": map[string]any{"schema_version": "1.0"}},
		"no snapshot":          {"rows": []int{0}, "snapshot_id": ""},
		"a snapshot not there": {"rows": []int{0}, "snapshot_id": strings.Repeat("0", 32)},
	} {
		code, _, _ := f.post(input, true)
		if name == "a snapshot not there" || name == "no snapshot" {
			if code != 404 && code != 400 {
				t.Errorf("%s: %d", name, code)
			}
			continue
		}
		if code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if len(f.renderer.calls) != 0 {
		t.Fatalf("a refused request checked %d rows", len(f.renderer.calls))
	}
	// A body that is not JSON of the right type, and a wrong media type, are refused.
	w := request(f.handler, "POST", "/api/reporting/v1/document-checks", []byte(`{"rows":[0]`), true)
	if w.Code != 400 {
		t.Fatalf("truncated body: %d", w.Code)
	}
	// Fifty distinct rows are the most one request may check.
	if code, _, answer := f.post(map[string]any{"rows": numbers(50)}, true); code != 200 || len(answer.Rows) != 50 || len(f.renderer.calls) != 50 {
		t.Fatalf("fifty rows: %d %d", code, len(answer.Rows))
	}
	// The last row is addressable, and the rows come back in row order whatever the request's.
	f.renderer.calls = nil
	code, body, answer := f.post(map[string]any{"rows": []int{59, 7, 0}}, true)
	if code != 200 || len(answer.Rows) != 3 || answer.Rows[0].RowIndex != 0 || answer.Rows[1].RowIndex != 7 || answer.Rows[2].RowIndex != 59 {
		t.Fatalf("%d %s", code, body)
	}
	for _, row := range answer.Rows {
		if !row.OK || row.Code != "" || row.Reasons != nil {
			t.Fatalf("an explicit row has no code or reason when it passes: %+v", row)
		}
	}
	// Each row is bound from the frozen snapshot, never from the caller; the answer holds no value.
	if len(f.renderer.calls) != 3 || f.renderer.calls[0].values["n1"] != f.rows[0][0] || f.renderer.calls[2].values["n1"] != f.rows[59][0] || f.renderer.calls[1].values["n3"] != "title" {
		t.Fatalf("bound values: %+v", f.renderer.calls)
	}
	for _, secret := range []string{f.rows[0][0], f.rows[7][0], "100.00"} {
		if strings.Contains(body, secret) {
			t.Fatalf("the answer carries a value: %s", body)
		}
	}
}

func TestDocumentChecksAnswerEachRowWithTheCodeAPreviewWould(t *testing.T) {
	f := newCheckFixture(t, []string{"fits", "TOO-WIDE", "fits too", strings.Repeat("x", 257), "last"}, false)
	f.renderer.check = func(call checkCall) error {
		if call.values["n1"] == "TOO-WIDE" {
			return fmt.Errorf("pdf: a value does not fit its box on one line: %w", r.ErrUnsupported)
		}
		return nil
	}
	code, body, answer := f.post(map[string]any{"rows": numbers(5)}, true)
	if code != 200 || len(answer.Rows) != 5 {
		t.Fatalf("%d %s", code, body)
	}
	index := func(value string) int {
		for i, row := range f.rows {
			if row[0] == value {
				return i
			}
		}
		t.Fatalf("no row for %q", value)
		return -1
	}
	for i, row := range answer.Rows {
		refused := i == index("TOO-WIDE") || i == index(strings.Repeat("x", 257))
		if row.RowIndex != i || row.OK == refused || (refused && row.Code != "UNSUPPORTED_COMBINATION") || (!refused && row.Code != "") {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	// A row the preview could not bind (over 256 characters) is refused without asking the checker.
	for _, call := range f.renderer.calls {
		if len(call.values["n1"]) > 256 {
			t.Fatal("the checker was asked about a row that cannot be bound")
		}
	}
	if strings.Contains(body, "TOO-WIDE") || strings.Contains(body, "xxxx") {
		t.Fatalf("the answer carries a value or a message: %s", body)
	}
	if f.renderer.renders != 0 {
		t.Fatal("a check rendered")
	}
}

func TestDocumentChecksFailAsAWholeOnAnythingButALayoutRefusalOfARow(t *testing.T) {
	f := newCheckFixture(t, []string{"alpha", "beta", "gamma"}, false)
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"integrity":   {fmt.Errorf("pdf: totals: %w", r.ErrIntegrity), 409, "INTEGRITY_FAILURE"},
		"unavailable": {r.ErrUnavailable, 503, "AUTHORITY_UNAVAILABLE"},
		"budget":      {r.ErrBudget, 413, "BUDGET_EXCEEDED"},
		"invalid":     {r.ErrInvalid, 400, "INVALID_REQUEST"},
		"cancelled":   {context.Canceled, 500, "EXECUTION_FAILED"},
		"deadline":    {context.DeadlineExceeded, 500, "EXECUTION_FAILED"},
		"unknown":     {fmt.Errorf("pdf: font missing"), 500, "EXECUTION_FAILED"},
	} {
		// The second row is the one that fails: the first row's pass is not answered alone.
		f.renderer.calls = nil
		f.renderer.check = func(call checkCall) error {
			if len(f.renderer.calls) == 2 {
				return tc.err
			}
			return nil
		}
		code, body, _ := f.post(map[string]any{"rows": []int{0, 1, 2}}, true)
		if code != tc.status || strings.Contains(body, "row_index") || !strings.Contains(body, tc.code) {
			t.Errorf("%s: %d %s", name, code, body)
		}
		if len(f.renderer.calls) != 2 {
			t.Errorf("%s: the third row was checked after the failure", name)
		}
	}
	// A row that does not bind for another reason than its value is the whole request's: the row is shorter than its columns.
	f.renderer.check = nil
	f.renderer.calls = nil
	if code, _, _ := f.post(map[string]any{"rows": []int{0, 1}}, true); code != 200 || len(f.renderer.calls) != 2 {
		t.Fatalf("recovered: %d", code)
	}
}

// A refusal of one row among rows that pass is that row's, and the answer still lists them all.
func TestDocumentChecksMixRowsThatPassAndRowsThatDoNot(t *testing.T) {
	f := newCheckFixture(t, []string{"a", "b", "c", "d"}, false)
	f.renderer.check = func(call checkCall) error {
		if call.values["n1"] == "b" || call.values["n1"] == "d" {
			return r.ErrUnsupported
		}
		return nil
	}
	_, _, answer := f.post(map[string]any{"rows": []int{0, 1, 2, 3}}, true)
	var oks []bool
	for _, row := range answer.Rows {
		oks = append(oks, row.OK)
	}
	want := []bool{}
	for _, row := range f.rows {
		want = append(want, row[0] == "a" || row[0] == "c")
	}
	if !reflect.DeepEqual(oks, want) {
		t.Fatalf("%v, want %v", oks, want)
	}
}

func TestDocumentChecksSampleTheBoundaryRowsWithTheirReasons(t *testing.T) {
	// Row values by display width: wide characters count two, so "宽宽宽" (6) is longer than "abcde" (5).
	customers := []string{"mid", "abcde", "宽宽宽", "ab", "abcde", "x", "end"}
	f := newCheckFixture(t, customers, false)
	at := func(value string) int {
		for i, row := range f.rows {
			if row[0] == value {
				return i
			}
		}
		t.Fatalf("no row for %q", value)
		return -1
	}
	code, body, answer := f.post(map[string]any{"samples": "boundary"}, true)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	got := map[int][]string{}
	last := -1
	for _, row := range answer.Rows {
		if row.RowIndex <= last || !row.OK || row.Code != "" {
			t.Fatalf("rows are in order and pass: %+v", answer.Rows)
		}
		last = row.RowIndex
		got[row.RowIndex] = row.Reasons
	}
	// n1 is the customer (the rows' own widths), n2 the balance: "100.00".. all 6 wide, ties to row 0.
	want := map[int][]string{0: {"first"}, len(f.rows) - 1: {"last"}}
	merge := func(i int, reason string) { want[i] = append(want[i], reason) }
	merge(at("宽宽宽"), "longest:n1")
	merge(at("x"), "shortest:n1")
	merge(0, "longest:n2")
	merge(0, "shortest:n2")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons %v, want %v\n%s", got, want, body)
	}
	if len(f.renderer.calls) != len(want) || f.renderer.renders != 0 {
		t.Fatalf("%d checks for %d rows", len(f.renderer.calls), len(want))
	}
	// A refused boundary row says so and keeps its reasons.
	f.renderer.check = func(call checkCall) error {
		if call.values["n1"] == "宽宽宽" {
			return r.ErrUnsupported
		}
		return nil
	}
	_, _, answer = f.post(map[string]any{"samples": "boundary"}, true)
	for _, row := range answer.Rows {
		if (row.RowIndex == at("宽宽宽")) == row.OK || (!row.OK && (row.Code != "UNSUPPORTED_COMBINATION" || !reflect.DeepEqual(row.Reasons, []string{"longest:n1"}))) {
			t.Fatalf("%+v", row)
		}
	}
	// The same template with the boxes' other fields: only row-field boxes are sampled, and the
	// rows the table lists are not.
	literals := f.config()
	literals.Elements = []doctemplates.Element{literals.Elements[2]}
	_, _, answer = f.post(map[string]any{"samples": "boundary", "config": literals}, true)
	if len(answer.Rows) != 2 || answer.Rows[0].RowIndex != 0 || answer.Rows[1].RowIndex != len(f.rows)-1 {
		t.Fatalf("a template without row fields is sampled at its ends: %+v", answer.Rows)
	}
}

func TestDocumentChecksGiveVariablesModeNoBackgroundAndFullModeTheTemplatesOwn(t *testing.T) {
	f := newCheckFixture(t, []string{"alpha", "beta"}, true)
	raw, e := os.ReadFile("../../doctemplates/testdata/a4-background.png")
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(map[string]any{"binding": doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset}, "png": base64.StdEncoding.EncodeToString(raw)})
	w := request(f.handler, "POST", "/api/reporting/v1/document-backgrounds", body, true)
	var ref doctemplates.BackgroundRef
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &ref) != nil {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	config := f.config()
	config.Schema, config.Print, config.Background = "1.2", &doctemplates.PrintProfile{Mode: "full"}, &ref
	if code, text, _ := f.post(map[string]any{"rows": []int{0}, "config": config}, true); code != 200 || len(f.renderer.calls) != 1 || f.renderer.calls[0].background == 0 {
		t.Fatalf("full mode: %d %s %+v", code, text, f.renderer.calls)
	}
	config.Print.Mode = "variables"
	if code, text, _ := f.post(map[string]any{"rows": []int{0}, "config": config}, true); code != 200 || len(f.renderer.calls) != 2 || f.renderer.calls[1].background != 0 {
		t.Fatalf("variables mode: %d %s %+v", code, text, f.renderer.calls)
	}
	// A background that is not the caller's is denied.
	config.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("c", 32), SHA256: ref.SHA256}
	if code, _, _ := f.post(map[string]any{"rows": []int{0}, "config": config}, true); code == 200 {
		t.Fatal("a background the caller does not have")
	}
}

type previewOnlyRenderer struct{ renders int }

func (p *previewOnlyRenderer) Render(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) ([]byte, error) {
	p.renders++
	return []byte("%PDF-1.7\nonly\n%%EOF"), nil
}

func TestDocumentChecksAreMountedOnlyWhenTheRendererCanCheck(t *testing.T) {
	p := contractsim.DemoPrincipal()
	source := contractsim.Financial()
	source.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: source}}, CursorKey: []byte(secret), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	fallback, _ := httpapi.Handler(service, auth)
	h, e := httpapi.WithDocumentPreviews(fallback, service, auth, &previewOnlyRenderer{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	body := []byte(`{"samples":"boundary","snapshot_id":"` + strings.Repeat("0", 32) + `"}`)
	if w := request(h, "POST", "/api/reporting/v1/document-checks", body, true); w.Code != 404 {
		t.Fatalf("a renderer that cannot check: %d", w.Code)
	}
	if w := request(fallback, "POST", "/api/reporting/v1/document-checks", body, true); w.Code != 404 {
		t.Fatalf("not mounted without previews: %d", w.Code)
	}
	// The previews stay: an unauthenticated call is refused, not missing.
	if w := request(h, "POST", "/api/reporting/v1/document-previews", []byte(`{}`), false); w.Code != 401 {
		t.Fatalf("previews: %d", w.Code)
	}
	c, e := httpapi.WithDocumentPreviews(fallback, service, auth, &checkingRenderer{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if w := request(c, "POST", "/api/reporting/v1/document-checks", body, false); w.Code != 401 {
		t.Fatalf("a renderer that can check: %d", w.Code)
	}
	var _ httpapi.DocumentChecker = &checkingRenderer{}
}
