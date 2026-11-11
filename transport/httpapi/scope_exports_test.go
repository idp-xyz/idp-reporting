package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// freezeWithKey freezes a query through POST /snapshot-jobs under its own Idempotency-Key and returns the snapshot's id.
func freezeWithKey(t *testing.T, s *r.Service, h http.Handler, key string, q r.QuerySpec) string {
	t.Helper()
	b, _ := json.Marshal(q)
	req := httptest.NewRequest("POST", "/api/reporting/v1/snapshot-jobs", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var j r.JobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return finish(t, s, h, j.ID).SnapshotID
}

// exportedWorkbook exports a snapshot as the workbook (through a layout when one is named), downloads it and opens it.
func exportedWorkbook(t *testing.T, s *r.Service, h http.Handler, key, snapshot, layout string) *excelize.File {
	t.Helper()
	body := map[string]string{"snapshot_id": snapshot}
	if layout != "" {
		body["layout_id"] = layout
	}
	w := exportRequest(h, key, body)
	var j r.JobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	j = finish(t, s, h, j.ID)
	w = request(h, "GET", "/api/reporting/v1/artifacts/"+j.ArtifactID+"/content", nil, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	f, e := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// sheetRows is a sheet's rows, each row's cells joined by "|".
func sheetRows(t *testing.T, f *excelize.File, sheet string) []string {
	t.Helper()
	rows, e := f.GetRows(sheet, excelize.Options{RawCellValue: true})
	if e != nil {
		t.Fatal(e)
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = strings.Join(row, "|")
	}
	return out
}

// TestAFilteredSnapshotsWorkbookSaysItsScope (S20-C, ADR 0023 decision 6), through the routes: a query with filters and a sort is
// frozen by POST /snapshot-jobs, exported by POST /exports and downloaded; the workbook holds the narrowed rows and totals the source
// gave and its Metadata sheet ends with the scope: the count row, a row per filter in the normalized order and a row per sort key, the
// words with the column's label and the exact field, operator and value beside them. The workbook of the whole dataset counts none; an
// export through a layout says the same scope.
func TestAFilteredSnapshotsWorkbookSaysItsScope(t *testing.T) {
	s, h, _, whole := layoutExportSetup(t)
	q := contractsim.FinancialQuery()
	q.Filters = []r.Filter{{Field: "ar.open_balance", Operator: "ge", Value: "-1.00"}, {Field: "aging.bucket", Operator: "in", Value: `["current","1_30"]`}}
	q.Sort = []r.Sort{{Field: "ar.open_balance", Direction: "desc"}}
	narrowed := freezeWithKey(t, s, h, "filtered", q)
	if narrowed == whole {
		t.Fatal("the filtered query froze the snapshot of the whole dataset")
	}
	f := exportedWorkbook(t, s, h, "x-filtered", narrowed, "")
	scope := []string{
		`query_scope|filters|2|sort_keys|1`,
		`filter_1|账龄区间 is one of current, 1_30|aging.bucket|in|["current","1_30"]`,
		`filter_2|应收余额 is greater than or equal to -1.00|ar.open_balance|ge|-1.00`,
		`sort_1|应收余额 descending|ar.open_balance|desc`,
	}
	meta := sheetRows(t, f, "Metadata")
	// The scope is the end of the sheet, after the columns of the dataset; the query hash above it is the narrowed query's.
	if len(meta) < len(scope) || !slices.Equal(meta[len(meta)-len(scope):], scope) {
		t.Errorf("the Metadata sheet ends with\n %s\nwant\n %s", strings.Join(meta[max(0, len(meta)-len(scope)):], "\n "), strings.Join(scope, "\n "))
	}
	if !slices.Contains(meta, "query_hash|"+r.QueryHash(q)) || !slices.Contains(meta, "ar.open_balance|decimal|2|CNY") {
		t.Errorf("the Metadata sheet: %q", meta)
	}
	// The rows and the total are the narrowed scope's, in the order asked: the source applied the filters and the sort, the workbook
	// writes what the snapshot holds.
	if data := sheetRows(t, f, "Data"); !slices.Equal(data, []string{"客户|账龄区间|应收余额|应收余额（精确）", "customer_a|current|100.25|100.25", "customer_b|1_30|-0.24|-0.24"}) {
		t.Errorf("the Data sheet: %q", data)
	}
	if totals := sheetRows(t, f, "Totals"); len(totals) != 2 || totals[1] != "ar.open_balance|100.01|100.01|CNY" {
		t.Errorf("the Totals sheet: %q", totals)
	}
	// The whole dataset's workbook has the count row alone: no filter, no sort key, which a workbook written before the row existed
	// could not say.
	meta = sheetRows(t, exportedWorkbook(t, s, h, "x-whole", whole, ""), "Metadata")
	if last := meta[len(meta)-1]; last != `query_scope|filters|0|sort_keys|0` || strings.HasPrefix(meta[len(meta)-2], "filter_") || !slices.Contains(meta, "query_hash|"+r.QueryHash(contractsim.FinancialQuery())) {
		t.Errorf("the whole dataset's Metadata sheet ends with %q", meta[len(meta)-2:])
	}
	// Through a layout that shows the bucket under another label and leaves the scope alone: the same scope rows, with the dataset's
	// labels (the filters are the query's, whatever the layout).
	entry := saveLayout(t, h, "", 0, receivablesLayout())
	presented := exportedWorkbook(t, s, h, "x-presented", narrowed, entry.ID)
	meta = sheetRows(t, presented, "Metadata")
	if len(meta) < len(scope) || !slices.Equal(meta[len(meta)-len(scope):], scope) || !slices.Contains(meta, "layout_id|"+entry.ID) {
		t.Errorf("the presented workbook's Metadata sheet ends with\n %s", strings.Join(meta[max(0, len(meta)-len(scope)):], "\n "))
	}
	if data := sheetRows(t, presented, "Data"); len(data) != 5 || data[0] != "账龄|客户|应收余额|应收余额（精确）" || data[1] != "current|customer_a|100.25|100.25" || data[2] != "小计||100.25|100.25" {
		t.Errorf("the presented Data sheet: %q", data)
	}
}
