package httpapi_test

// POST /issues for a schema 2.0 template (S03-K, docs/S03K_SCOPE.md K1, docs/ADR_0004_TEMPLATE_SCHEMA_2.md decision 7): the
// request names the group by its key values or by a row it contains, the admission resolves the group in the frozen
// snapshot and freezes it, and both ways of naming one group are one request. A 1.x template keeps `row_index`.

import (
	"context"
	"encoding/json"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"net/http"
	"strings"
	"testing"
)

// groupLines is five lines of three customers in two buckets, interleaved: customer_a is rows 0, 2 and 4, customer_b
// row 1, customer_c row 3; the bucket "current" is rows 0, 3 and 4 and "1_30" rows 1 and 2.
var groupLines = [][]string{
	{"customer_a", "current", "10.00"},
	{"customer_b", "1_30", "20.00"},
	{"customer_a", "1_30", "30.00"},
	{"customer_c", "current", "40.00"},
	{"customer_a", "current", "50.00"},
}

// groupedSnapshot freezes groupLines as a snapshot of the finance product and returns its id; the environment's own
// snapshot is no longer authorized afterwards (the grant now covers other rows).
func (v *issueHTTP) groupedSnapshot() string {
	v.t.Helper()
	grant := contractsim.DefaultGrant()
	grant.Rows = nil
	var records []contractsim.Record
	for i, row := range groupLines {
		key := "g" + string(rune('1'+i))
		grant.Rows = append(grant.Rows, key)
		records = append(records, contractsim.Record{Company: "company_demo", Key: key, Values: row})
	}
	v.source.ReplaceRecords(records)
	v.source.Grant(contractsim.DemoOwner(v.p, contractsim.FinanceIntegration), grant)
	ctx := context.Background()
	job, e := v.service.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "grouped")
	if e != nil {
		v.t.Fatal(e)
	}
	if _, e = v.service.RunOne(ctx); e != nil {
		v.t.Fatal(e)
	}
	if job, e = v.service.Job(ctx, v.p, job.ID); e != nil || job.SnapshotID == "" {
		v.t.Fatal(job, e)
	}
	v.snapshot = job.SnapshotID
	return job.SnapshotID
}

// invoice is a 2.0 template of the finance dataset: the customer in the page header, the group's lines in the table and the
// sum of their balances in the document footer; grouped by the given fields, or the whole snapshot without.
func invoice(group ...string) doctemplates.Document {
	d := doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: contractsim.FinanceIntegration, Dataset: contractsim.FinancialQuery().Dataset},
		Bands: &doctemplates.Bands{
			PageHeader: &doctemplates.Band{Height: 15, Elements: []doctemplates.Item{{ID: "e1", Type: "text", Source: "field", Field: "customer.id", X: 0, Y: 2, Width: 100, Height: 8, FontSize: 12, Align: "left"}}},
			Detail: &doctemplates.DetailBand{Elements: []doctemplates.Item{{ID: "e2", Type: "table", X: 0, Width: 190, FontSize: 10,
				Columns: []doctemplates.ItemColumn{{Field: "customer.id", Width: 50}, {Field: "aging.bucket", Width: 60}, {Field: "ar.open_balance", Width: 80}}}}},
			DocumentFooter: &doctemplates.Band{Height: 10, Elements: []doctemplates.Item{{ID: "e3", Type: "text", Source: "group_sum", Field: "ar.open_balance", X: 0, Y: 1, Width: 60, Height: 8, FontSize: 10, Align: "right"}}}}}
	if len(group) > 0 {
		d.Group = &doctemplates.GroupSpec{Fields: group}
	} else {
		d.Bands.PageHeader.Elements[0] = doctemplates.Item{ID: "e1", Type: "text", Source: "literal", Text: "Statement", X: 0, Y: 2, Width: 100, Height: 8, FontSize: 12, Align: "left"}
	}
	return d
}

// publishInvoice saves and publishes a 2.0 template and returns its id (version 1).
func (v *issueHTTP) publishInvoice(group ...string) string {
	v.t.Helper()
	saved, e := v.store.Save(v.p.Identity, "", 0, invoice(group...))
	if e != nil {
		v.t.Fatal(e)
	}
	if _, e = v.store.Publish(v.p.Identity, saved.ID, "publish", 1, 0); e != nil {
		v.t.Fatal(e)
	}
	return saved.ID
}

// body is a request of the 2.0 template with the given members besides the template, the version and the snapshot.
func (v *issueHTTP) body(template string, members map[string]any) map[string]any {
	out := map[string]any{"template_id": template, "version": 1, "snapshot_id": v.snapshot}
	for k, val := range members {
		out[k] = val
	}
	return out
}

func issueOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	issue, ok := out["issue"].(map[string]any)
	if !ok {
		t.Fatalf("no issue summary: %v", out)
	}
	return issue
}

// A group named by its key and by a row it contains is one request: the same Idempotency-Key replays the one job, and a key
// that names another group is a conflict. The job shows the group instead of a row; the worker issues it and the
// record, the listing and the job view show the group, the number and no row.
func TestIssueByGroupNamedByKeyOrByRowIsOneRequest(t *testing.T) {
	v := newIssueHTTP(t, nil)
	v.groupedSnapshot()
	id := v.publishInvoice("customer.id")
	code, out := v.post(v.body(id, map[string]any{"group": []string{"customer_a"}}), "k1")
	if code != 202 || out["kind"] != "issue" || out["state"] != "queued" || out["format"] != "pdf" {
		t.Fatal(code, out)
	}
	jobID := out["job_id"].(string)
	issue := issueOf(t, out)
	group, _ := issue["group"].(map[string]any)
	if _, has := issue["row_index"]; has || group == nil || len(group["fields"].([]any)) != 1 || group["fields"].([]any)[0] != "customer.id" || group["key"].([]any)[0] != "customer_a" || group["row_count"] != 3.0 || len(group["rows_sha256"].(string)) != 64 {
		t.Fatalf("the admitted job's issue summary: %v", issue)
	}
	absent(t, "the admitted job's issue summary", issue, "number")
	// The same group by any of its rows, with the same key, is the same request: the one job again.
	for _, row := range []int{0, 2, 4} {
		code, again := v.post(v.body(id, map[string]any{"row_index": row}), "k1")
		if code != 202 || again["job_id"] != jobID {
			t.Fatalf("row %d: %d %v", row, code, again)
		}
	}
	// The group of another row under the same key is another request: a conflict. So is another key's group.
	if code, out := v.post(v.body(id, map[string]any{"row_index": 1}), "k1"); code != 409 || out["error"] != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(code, out)
	}
	if code, out := v.post(v.body(id, map[string]any{"group": []string{"customer_b"}}), "k1"); code != 409 || out["error"] != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(code, out)
	}
	// Another key makes another job, even for the same group.
	if code, other := v.post(v.body(id, map[string]any{"row_index": 4}), "k2"); code != 202 || other["job_id"] == jobID {
		t.Fatal(code, other)
	}
	// Admission asked nothing of the product.
	if _, held := v.source.IssueRecord(jobID); held {
		t.Fatal("admission reached the product")
	}
	if _, e := v.service.RunOne(context.Background()); e != nil {
		t.Fatal(e)
	}
	// The worker rendered the group: its key in the header, the sum of its lines in the footer, the document of the template.
	if v.drawn.calls != 1 || v.drawn.doc.Schema != "2.0" || v.drawn.values["e1"] != "customer_a" || v.drawn.values["e3"] != "90.00" || !strings.HasPrefix(v.drawn.stamp, "ISSUED No. AR-000001 - ") {
		t.Fatalf("renderer: %+v", v.drawn)
	}
	w := request(v.h, "GET", "/api/reporting/v1/issues/"+jobID, nil, true)
	var rec r.IssueView
	json.Unmarshal(w.Body.Bytes(), &rec)
	var raw map[string]any
	json.Unmarshal(w.Body.Bytes(), &raw)
	if w.Code != 200 || rec.State != "succeeded" || rec.Number != "AR-000001" || rec.RowIndex != nil || rec.Group == nil || rec.Group.RowCount != 3 || rec.Group.Key[0] != "customer_a" || rec.Group.Fields[0] != "customer.id" || len(rec.Group.RowsSHA256) != 64 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, has := raw["row_index"]; has {
		t.Fatalf("the record of a group issue has a row_index: %v", raw)
	}
	// The listing and the job view show the group too; the replay of the key shows the number.
	w = request(v.h, "GET", "/api/reporting/v1/issues?limit=10", nil, true)
	var list []map[string]any
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list) != 2 || list[1]["number"] != "AR-000001" || list[1]["group"] == nil || list[1]["row_index"] != nil || list[0]["group"] == nil {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(v.h, "GET", "/api/reporting/v1/jobs/"+jobID, nil, true)
	var view r.JobView
	json.Unmarshal(w.Body.Bytes(), &view)
	if w.Code != 200 || view.Issue == nil || view.Issue.Number != "AR-000001" || view.Issue.Group == nil || view.Issue.RowIndex != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if code, replay := v.post(v.body(id, map[string]any{"group": []string{"customer_a"}}), "k1"); code != 202 || replay["job_id"] != jobID || issueOf(t, replay)["number"] != "AR-000001" {
		t.Fatal(code, replay)
	}
	// The download is the artifact the record names.
	w = request(v.h, "GET", "/api/reporting/v1/artifacts/"+rec.ArtifactID+"/content", nil, true)
	if w.Code != 200 || w.Header().Get("Content-Disposition") != `attachment; filename="issued-`+jobID+`.pdf"` || !strings.Contains(w.Body.String(), rec.Stamp) {
		t.Fatal(w.Code, w.Header())
	}
}

// A template without a group makes one document of the whole snapshot: `group` is the empty list, and `row_index` of any row
// names the same group; a key is invalid for it.
func TestIssueByGroupOfAWholeSnapshotTemplate(t *testing.T) {
	v := newIssueHTTP(t, nil)
	v.groupedSnapshot()
	id := v.publishInvoice()
	code, out := v.post(v.body(id, map[string]any{"group": []string{}}), "k")
	if code != 202 {
		t.Fatal(code, out)
	}
	group := issueOf(t, out)["group"].(map[string]any)
	if len(group["fields"].([]any)) != 0 || len(group["key"].([]any)) != 0 || group["row_count"] != 5.0 {
		t.Fatalf("%v", group)
	}
	for _, row := range []int{0, 3, 4} {
		if code, again := v.post(v.body(id, map[string]any{"row_index": row}), "k"); code != 202 || again["job_id"] != out["job_id"] {
			t.Fatalf("row %d: %d %v", row, code, again)
		}
	}
	if code, out := v.post(v.body(id, map[string]any{"group": []string{"customer_a"}}), "bad"); code != 400 || out["error"] != "INVALID_REQUEST" {
		t.Fatal(code, out)
	}
	if _, e := v.service.RunOne(context.Background()); e != nil {
		t.Fatal(e)
	}
	if v.drawn.calls != 1 || v.drawn.values["e1"] != "Statement" || v.drawn.values["e3"] != "150.00" {
		t.Fatalf("%+v", v.drawn)
	}
}

// Both or neither of `group` and `row_index` is 400, a `group` for a 1.x template is 400, a group of the wrong length is 400,
// a key no group has is 404, and a row beyond the snapshot is 400 as for 1.x; none answers with a value.
func TestIssueByGroupRequestShapes(t *testing.T) {
	v := newIssueHTTP(t, nil)
	v.groupedSnapshot()
	id := v.publishInvoice("customer.id")
	two := v.publishInvoice("customer.id", "aging.bucket")
	v.publish() // the 1.x template of the environment
	for name, c := range map[string]struct {
		body map[string]any
		code int
		want string
	}{
		"both":                          {v.body(id, map[string]any{"group": []string{"customer_a"}, "row_index": 0}), 400, "INVALID_REQUEST"},
		"neither":                       {v.body(id, nil), 400, "INVALID_REQUEST"},
		"a group of 1.x":                {v.body(v.template, map[string]any{"group": []string{"customer_a"}}), 400, "INVALID_REQUEST"},
		"an empty group of 1.x":         {v.body(v.template, map[string]any{"group": []string{}}), 400, "INVALID_REQUEST"},
		"a group and a row of 1.x":      {v.body(v.template, map[string]any{"group": []string{"customer_a"}, "row_index": 0}), 400, "INVALID_REQUEST"},
		"too short a key":               {v.body(id, map[string]any{"group": []string{}}), 400, "INVALID_REQUEST"},
		"too long a key":                {v.body(id, map[string]any{"group": []string{"customer_a", "current"}}), 400, "INVALID_REQUEST"},
		"a key too short for two":       {v.body(two, map[string]any{"group": []string{"customer_a"}}), 400, "INVALID_REQUEST"},
		"a null group":                  {v.body(id, map[string]any{"group": nil}), 400, "INVALID_REQUEST"},
		"a group that is a string":      {v.body(id, map[string]any{"group": "customer_a"}), 400, "INVALID_REQUEST"},
		"a group of numbers":            {v.body(id, map[string]any{"group": []int{1}}), 400, "INVALID_REQUEST"},
		"a group of an object":          {v.body(id, map[string]any{"group": []any{map[string]any{"a": "b"}}}), 400, "INVALID_REQUEST"},
		"a row beyond the snapshot":     {v.body(id, map[string]any{"row_index": 5}), 400, "INVALID_REQUEST"},
		"a negative row":                {v.body(id, map[string]any{"row_index": -1}), 400, "INVALID_REQUEST"},
		"an unknown member":             {v.body(id, map[string]any{"group": []string{"customer_a"}, "config": invoice()}), 400, "INVALID_REQUEST"},
		"a key no group has":            {v.body(id, map[string]any{"group": []string{"customer_zzz"}}), 404, "NOT_FOUND"},
		"a key of another case":         {v.body(id, map[string]any{"group": []string{"Customer_a"}}), 404, "NOT_FOUND"},
		"a key with a trailing space":   {v.body(id, map[string]any{"group": []string{"customer_a "}}), 404, "NOT_FOUND"},
		"the first field's key for two": {v.body(two, map[string]any{"group": []string{"customer_b", "current"}}), 404, "NOT_FOUND"},
		"a snapshot that is not there":  {map[string]any{"template_id": id, "version": 1, "snapshot_id": strings.Repeat("0", 32), "group": []string{"customer_a"}}, 404, "NOT_FOUND"},
		"no version":                    {map[string]any{"template_id": id, "snapshot_id": v.snapshot, "group": []string{"customer_a"}}, 400, "INVALID_REQUEST"},
		"a version that is a draft":     {v.body(id, map[string]any{"version": 2, "group": []string{"customer_a"}}), 404, "NOT_FOUND"},
	} {
		w := v.postRaw(c.body, "shape "+name)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != c.code || out["error"] != c.want {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
		// No answer carries a key value, a row or a field of the data.
		for _, leak := range []string{"customer_a", "customer_b", "customer_zzz", "10.00", "current"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("%s: the answer names %q: %s", name, leak, w.Body.String())
			}
		}
	}
	// None of the refusals admitted a job.
	if w := request(v.h, "GET", "/api/reporting/v1/issues", nil, true); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("a job was admitted: %d %s", w.Code, w.Body.String())
	}
	// Two fields in the order of the template are a group; the order matters.
	if code, out := v.post(v.body(two, map[string]any{"group": []string{"customer_a", "current"}}), "two"); code != 202 || issueOf(t, out)["group"].(map[string]any)["row_count"] != 2.0 {
		t.Fatal(code, out)
	}
	if code, out := v.post(v.body(two, map[string]any{"group": []string{"current", "customer_a"}}), "swapped"); code != 404 {
		t.Fatal("the key in the other order", code, out)
	}
	// A 1.x template is issued by row as before, with and without a group-capable product.
	if code, out := v.post(map[string]any{"template_id": v.template, "version": 1, "snapshot_id": v.snapshot, "row_index": 1}, "row"); code != 202 || issueOf(t, out)["row_index"] != 1.0 || issueOf(t, out)["group"] != nil {
		t.Fatal(code, out)
	}
}

// A 2.0 template is refused as a preview refuses it: a field that differs inside the group is 422 with the reason and the place and no
// value, and an unpublished, withdrawn or superseded version is 409, before any group is resolved.
func TestIssueByGroupRefusalsAreThePreviewsAndCarryNoValue(t *testing.T) {
	v := newIssueHTTP(t, nil)
	v.groupedSnapshot()
	// Grouped by bucket, the page header prints the customer, which differs between the rows of a bucket.
	bad := v.publishInvoice("aging.bucket")
	code, out := v.post(v.body(bad, map[string]any{"group": []string{"current"}}), "inconsistent")
	detail, _ := out["detail"].(map[string]any)
	if code != 422 || out["error"] != "UNSUPPORTED_COMBINATION" || detail["reason"] != "INCONSISTENT_GROUP" || detail["band"] != "page_header" || detail["element"] != "e1" || detail["column"] != "customer.id" {
		t.Fatal(code, out)
	}
	for _, leak := range []string{"customer_a", "current", "10.00"} {
		if b, _ := json.Marshal(out); strings.Contains(string(b), leak) {
			t.Errorf("the refusal names %q: %s", leak, b)
		}
	}
	// By a row of the group, the same refusal.
	if code, again := v.post(v.body(bad, map[string]any{"row_index": 0}), "inconsistent-row"); code != 422 || again["detail"] == nil {
		t.Fatal(code, again)
	}
	// An unpublished version is 409 whatever the group.
	saved, e := v.store.Save(v.p.Identity, "", 0, invoice("customer.id"))
	if e != nil {
		t.Fatal(e)
	}
	body := map[string]any{"template_id": saved.ID, "version": 1, "snapshot_id": v.snapshot, "group": []string{"customer_a"}}
	if code, out := v.post(body, "draft"); code != 409 || out["error"] != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatal(code, out)
	}
	if _, e = v.store.Publish(v.p.Identity, saved.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	if code, out := v.post(body, "published"); code != 202 {
		t.Fatal(code, out)
	}
	if _, e = v.store.Publish(v.p.Identity, saved.ID, "withdraw", 0, 1); e != nil {
		t.Fatal(e)
	}
	if code, out := v.post(body, "withdrawn"); code != 409 || out["error"] != "TEMPLATE_NOT_PUBLISHED" {
		t.Fatal(code, out)
	}
	// Another subject's snapshot is not found, and its template is not either.
	v.other = true
	if code, out := v.post(body, "other"); code != 404 {
		t.Fatal(code, out)
	}
}

// versionOne can issue and speaks contract version 1 only: the authority's three calls and the source's four, and
// not IssueContractVersion.
type versionOne struct {
	r.Adapter
	r.DocumentAuthority
}

// A group is not admitted for an integration whose product does not declare contract version 2: 422 at admission, nothing
// admitted and nothing asked; a 1.x template of the same product is issued by row as before.
func TestIssueByGroupNeedsAVersionTwoAdapter(t *testing.T) {
	var source *contractsim.Simulator
	v := newIssueHTTP(t, func(a r.Adapter) r.Adapter { source = a.(*contractsim.Simulator); return versionOne{a, source} })
	v.groupedSnapshot()
	id := v.publishInvoice("customer.id")
	v.publish()
	for name, body := range map[string]map[string]any{
		"by key": v.body(id, map[string]any{"group": []string{"customer_a"}}),
		"by row": v.body(id, map[string]any{"row_index": 0}),
		// Whatever else is wrong, the answer is the same: it is the adapter that cannot take a group.
		"an unknown key": v.body(id, map[string]any{"group": []string{"customer_zzz"}}),
	} {
		code, out := v.post(body, "v1 "+name)
		if name == "an unknown key" {
			// The key is resolved before the service is asked, so an unknown one is not found.
			if code != 404 {
				t.Errorf("%s: %d %v", name, code, out)
			}
			continue
		}
		if code != 422 || out["error"] != "UNSUPPORTED_COMBINATION" || out["detail"] != nil {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if w := request(v.h, "GET", "/api/reporting/v1/issues", nil, true); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("a job was admitted: %d %s", w.Code, w.Body.String())
	}
	if code, out := v.post(map[string]any{"template_id": v.template, "version": 1, "snapshot_id": v.snapshot, "row_index": 2}, "row"); code != 202 {
		t.Fatalf("a 1.x row issue of a version-1 product: %d %v", code, out)
	}
	if _, e := v.service.RunOne(context.Background()); e != nil {
		t.Fatal(e)
	}
	if v.drawn.calls != 1 || !strings.HasPrefix(v.drawn.stamp, "ISSUED No. AR-000001 - ") {
		t.Fatalf("%+v", v.drawn)
	}
}

// An unauthenticated request for a group is 401 as for a row.
func TestIssueByGroupWithoutAuthenticationIsRefused(t *testing.T) {
	v := newIssueHTTP(t, nil)
	v.groupedSnapshot()
	id := v.publishInvoice("customer.id")
	b, _ := json.Marshal(v.body(id, map[string]any{"group": []string{"customer_a"}}))
	w := request(v.h, "POST", "/api/reporting/v1/issues", b, false)
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code, w.Body.String())
	}
}
