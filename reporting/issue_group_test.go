package reporting_test

// Issue by group (S03-K, docs/ADR_0004_TEMPLATE_SCHEMA_2.md decision 7, docs/S03K_SCOPE.md): a schema 2.0 template
// is issued for one row group of a frozen snapshot, under the source product's authority exactly as a 1.x row is
// (ADR 0003). The service freezes the group at admission, asks only a product that declares contract version 2,
// re-checks the group against the frozen snapshot in the worker before the product is asked, and keeps the group in
// the record; the failure order of S03-H is the same.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"slices"
	"strings"
	"testing"
	"time"
)

// groupData is five lines of three customers in three buckets, interleaved, so that a customer's rows are not
// consecutive: customer_a is rows 0, 2 and 4, customer_b row 1, customer_c row 3; the bucket "current" is rows 0, 3
// and 4 and "1_30" rows 1 and 2.
var groupData = [][]string{
	{"customer_a", "current", "10.00"},
	{"customer_b", "1_30", "20.00"},
	{"customer_a", "1_30", "30.00"},
	{"customer_c", "current", "40.00"},
	{"customer_a", "current", "50.00"},
}

// newGroupEnv is an issue environment whose finance product holds groupData, frozen as a snapshot.
func newGroupEnv(t *testing.T, o envOpts) (*issueEnv, r.Snapshot) {
	t.Helper()
	v := newIssueEnv(t, o)
	grant := contractsim.DefaultGrant()
	grant.Rows = nil
	var records []contractsim.Record
	for i, row := range groupData {
		key := "g" + string(rune('1'+i))
		grant.Rows = append(grant.Rows, key)
		records = append(records, contractsim.Record{Company: "company_demo", Key: key, Values: row})
	}
	v.finance.ReplaceRecords(records)
	v.finance.Grant(v.owner, grant)
	id := v.freeze(contractsim.FinancialQuery(), "grouped")
	v.snapshot = id // a row job of the environment is a row of this snapshot too
	snap, e := v.s.AuthorizedSnapshot(context.Background(), v.p, id)
	if e != nil || len(snap.Result.Rows) != len(groupData) {
		t.Fatal(e, len(snap.Result.Rows))
	}
	return v, snap
}

// groupOf makes the group of the snapshot with the given template fields, key and rows.
func groupOf(t *testing.T, snap r.Snapshot, fields, key []string, rows ...int) r.IssueGroup {
	t.Helper()
	g, e := r.NewIssueGroup(snap, fields, key, rows)
	if e != nil {
		t.Fatal(e)
	}
	return g
}

func groupInput(v *issueEnv, snap r.Snapshot, g r.IssueGroup) r.IssueInput {
	return r.IssueInput{TemplateID: templateID, TemplateVersion: 3, Template: []byte(template), SnapshotID: snap.ID, Group: &g}
}

// jsonDigest is the digest of the rows as docs/API.md specifies it, spelled out here and computed from the
// JSON text, not through the reporting package.
func jsonDigest(rows ...string) string {
	sum := sha256.Sum256([]byte("[" + strings.Join(rows, ",") + "]"))
	return hex.EncodeToString(sum[:])
}

// The group is frozen at admission and shown in the job view in place of a row; the worker asks the product about
// the group, and not about a row; the record keeps the group; the views show it.
func TestGroupIssueFreezesTheGroupAsksTheProductAboutItAndKeepsItInTheRecord(t *testing.T) {
	ctx := context.Background()
	v, snap := newGroupEnv(t, envOpts{})
	g := groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)
	// The digest of the rows of the group is the digest of the JSON array of their arrays.
	want := jsonDigest(`["customer_a","current","10.00"]`, `["customer_a","1_30","30.00"]`, `["customer_a","current","50.00"]`)
	if g.RowsSHA256 != want || !slices.Equal(g.Rows, []int{0, 2, 4}) {
		t.Fatalf("the group: %+v, want the digest %s", g, want)
	}
	reads := v.finance.ReadCalls()
	j, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, g), "k")
	if e != nil {
		t.Fatal(e)
	}
	// Admitted: the job has no number and no row, and shows its group; the product was not asked and no source
	// data was read.
	if j.Kind != "issue" || j.State != "queued" || j.Issue == nil || j.Issue.Number != "" || j.Issue.RowIndex != nil || j.Issue.Group == nil {
		t.Fatalf("admitted job: %+v %+v", j, j.Issue)
	}
	if got := j.Issue.Group; !slices.Equal(got.Fields, []string{"customer.id"}) || !slices.Equal(got.Key, []string{"customer_a"}) || got.RowCount != 3 || got.RowsSHA256 != want {
		t.Fatalf("the group of the view: %+v", got)
	}
	if len(v.finance.intents) != 0 || v.finance.ReadCalls() != reads || v.issuer.calls != 0 {
		t.Fatal("admission reached the product, read source data or rendered")
	}
	// The view is JSON without a row_index; a 1.x job's has one, row 0 included.
	b, _ := json.Marshal(j)
	if strings.Contains(string(b), "row_index") || !strings.Contains(string(b), `"group":{"fields":["customer.id"],"key":["customer_a"],"row_count":3,"rows_sha256":"`+want+`"}`) {
		t.Fatalf("the JSON of the view: %s", b)
	}
	row := v.submit(0, "row-zero")
	if b, _ = json.Marshal(row); !strings.Contains(string(b), `"row_index":0`) || strings.Contains(string(b), `"group"`) {
		t.Fatalf("the JSON of a row job's view: %s", b)
	}
	// The stored job freezes the group and no row, under the request hash of the resolved group.
	stored := v.stored(j.ID)
	if stored.Group == nil || !stored.Group.Equal(&g) || stored.RowIndex != 0 || stored.RequestHash != r.IssueGroupRequestHash(templateID, 3, stored.TemplateSHA256, snap.ID, g) {
		t.Fatalf("the stored job: %+v", stored)
	}
	if e = r.ValidateAdmission(stored); e != nil {
		t.Fatal(e)
	}
	// The worker asks the product for the group, with the digest of its rows, and for no row.
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	if ran, e := v.run(); !ran || e != nil { // the row job, queued after it
		t.Fatal(ran, e)
	}
	if len(v.finance.intents) != 2 {
		t.Fatalf("%d calls to prepare", len(v.finance.intents))
	}
	in := v.finance.intents[0]
	if in.JobID != j.ID || in.Group == nil || !in.Group.Equal(&g) || in.RowIndex != 0 || in.RowSHA256 != "" || in.TemplateID != templateID || in.TemplateVersion != 3 || in.TemplateSHA256 != stored.TemplateSHA256 || in.Prepared.Ref != stored.Prepared.Ref {
		t.Fatalf("intent: %+v %+v", in, in.Group)
	}
	if rowIntent := v.finance.intents[1]; rowIntent.Group != nil || rowIntent.RowSHA256 == "" {
		t.Fatalf("the row job's intent changed: %+v", rowIntent)
	}
	// The renderer is handed the job and the snapshot the group is bound from.
	if v.issuer.checks != 2 || v.issuer.checkedAs.ID != row.ID || v.issuer.calls != 2 {
		t.Fatalf("checks %d, renders %d", v.issuer.checks, v.issuer.calls)
	}
	done := v.job(j.ID)
	if done.State != "succeeded" || done.Issue == nil || done.Issue.Number != "AR-000001" || done.Issue.RowIndex != nil || done.Issue.Group == nil || done.ArtifactID == "" {
		t.Fatalf("job view: %+v %+v", done, done.Issue)
	}
	// The record keeps the group in place of the row, and the product's number, as for a row.
	rec, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
	theirs, ok := held(v, j.ID)
	if e != nil || !ok || theirs.State != contractsim.IssueConfirmed || theirs.Number != "AR-000001" {
		t.Fatalf("the product's record: %+v %v %v", theirs, ok, e)
	}
	if rec.Group == nil || !rec.Group.Equal(&g) || rec.RowIndex != 0 || rec.Number != "AR-000001" || rec.ID != j.ID || rec.Pages != 2 {
		t.Fatalf("record: %+v %+v", rec, rec.Group)
	}
	// Every view shows the group, and the number, and no row.
	view, e := v.s.Issue(ctx, v.p, j.ID)
	if e != nil || view.Number != "AR-000001" || view.RowIndex != nil || view.Group == nil || view.Group.RowCount != 3 || !slices.Equal(view.Group.Key, []string{"customer_a"}) || view.ReservationRef != theirs.Ref {
		t.Fatalf("issue view: %+v %v", view, e)
	}
	list, e := v.s.Issues(ctx, v.p, 10)
	if e != nil || len(list) != 2 || list[1].ID != j.ID || list[1].Group == nil || list[1].RowIndex != nil || list[1].Number != "AR-000001" || list[0].ID != row.ID || list[0].Group != nil || list[0].RowIndex == nil || *list[0].RowIndex != 0 {
		t.Fatalf("listing: %+v %v", list, e)
	}
	// A replay of the key shows the same job, with its number and its group.
	again, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, g), "k")
	if e != nil || again.ID != j.ID || again.Issue.Number != "AR-000001" || again.Issue.Group == nil || again.Issue.RowIndex != nil {
		t.Fatalf("replay: %+v %v", again, e)
	}
	cancelled, e := v.s.Cancel(ctx, v.p, j.ID)
	if e != nil || cancelled.State != "succeeded" || cancelled.Issue.Number != "AR-000001" || cancelled.Issue.Group == nil {
		t.Fatalf("cancel: %+v %v", cancelled, e)
	}
}

// A template without a group makes one document of the whole snapshot: the group has no fields and no key, and every
// row. A group of several fields keeps their order. Both are issued.
func TestGroupIssueOfTheWholeSnapshotAndOfSeveralFields(t *testing.T) {
	ctx := context.Background()
	v, snap := newGroupEnv(t, envOpts{})
	whole := groupOf(t, snap, nil, nil, 0, 1, 2, 3, 4)
	if whole.Fields == nil || whole.Key == nil || len(whole.Fields) != 0 || len(whole.Key) != 0 {
		t.Fatalf("the whole snapshot's group has nil lists: %+v", whole)
	}
	two := groupOf(t, snap, []string{"customer.id", "aging.bucket"}, []string{"customer_a", "current"}, 0, 4)
	swapped := groupOf(t, snap, []string{"aging.bucket", "customer.id"}, []string{"current", "customer_a"}, 0, 4)
	if two.RowsSHA256 != swapped.RowsSHA256 {
		t.Fatal("the same rows have two digests")
	}
	var ids []string
	for i, g := range []r.IssueGroup{whole, two, swapped} {
		j, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, g), "k"+string(rune('a'+i)))
		if e != nil {
			t.Fatal(i, e)
		}
		ids = append(ids, j.ID)
		if _, e = v.run(); e != nil {
			t.Fatal(i, e)
		}
	}
	for i, in := range v.finance.intents {
		if in.Group == nil || in.JobID != ids[i] {
			t.Fatalf("intent %d: %+v", i, in)
		}
	}
	if g := v.finance.intents[0].Group; len(g.Fields) != 0 || len(g.Key) != 0 || !slices.Equal(g.Rows, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("the whole snapshot's group as the product saw it: %+v", g)
	}
	// Two groups of the same rows under other fields are different requests: the fields are part of the group.
	if v.stored(ids[1]).RequestHash == v.stored(ids[2]).RequestHash {
		t.Fatal("two field orders are one request")
	}
	for i, id := range ids {
		if got := v.job(id); got.State != "succeeded" || got.Issue.Number != "AR-00000"+string(rune('1'+i)) {
			t.Fatalf("job %d: %+v %+v", i, got, got.Issue)
		}
	}
}

// The request hash covers the resolved group: the same group is the same request however it was made, another group
// of the same template and snapshot is another, and a group request is never a row request.
func TestGroupRequestHashIsTheResolvedGroup(t *testing.T) {
	ctx := context.Background()
	v, snap := newGroupEnv(t, envOpts{})
	g := groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)
	first, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, g), "k")
	if e != nil {
		t.Fatal(e)
	}
	// The group made again, from the same rows, with the lists made another way, is the same request.
	again := r.IssueGroup{Fields: []string{"customer.id"}, Key: []string{"customer_a"}, Rows: []int{0, 2, 4}, RowsSHA256: g.RowsSHA256}
	if replay, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, again), "k"); e != nil || replay.ID != first.ID {
		t.Fatal("the same group is another request", replay, e)
	}
	if r.IssueGroupRequestHash(templateID, 3, "x", snap.ID, g) != r.IssueGroupRequestHash(templateID, 3, "x", snap.ID, again) {
		t.Fatal("the hash depends on how the group was made")
	}
	// Another group under the key is a conflict; so is a row.
	other := groupOf(t, snap, []string{"customer.id"}, []string{"customer_b"}, 1)
	if _, e = v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, other), "k"); !errors.Is(e, r.ErrConflict) {
		t.Fatal("another group under the key", e)
	}
	if _, e = v.s.SubmitIssue(ctx, v.p, v.input(1), "k"); !errors.Is(e, r.ErrConflict) {
		t.Fatal("a row under the key of a group", e)
	}
	// The group of customer_b is also row 1's group of the aging bucket "1_30" of a single row? No: it is another group
	// of other rows, and the same rows under other fields are another request.
	sameRows := groupOf(t, snap, []string{"aging.bucket"}, []string{"1_30"}, 1, 2)
	if r.IssueGroupRequestHash(templateID, 3, "x", snap.ID, other) == r.IssueGroupRequestHash(templateID, 3, "x", snap.ID, sameRows) {
		t.Fatal("two groups share a hash")
	}
	if r.IssueGroupRequestHash(templateID, 3, "x", snap.ID, g) == r.IssueRequestHash(templateID, 3, "x", snap.ID, 0) {
		t.Fatal("a group request is a row request")
	}
	// Another template version, digest or snapshot makes another request.
	for name, hash := range map[string]string{
		"version":  r.IssueGroupRequestHash(templateID, 4, "x", snap.ID, g),
		"digest":   r.IssueGroupRequestHash(templateID, 3, "y", snap.ID, g),
		"snapshot": r.IssueGroupRequestHash(templateID, 3, "x", r.NewID(), g),
	} {
		if hash == r.IssueGroupRequestHash(templateID, 3, "x", snap.ID, g) {
			t.Errorf("another %s is the same request", name)
		}
	}
}

// Admission verifies the group against the snapshot it names: a group that is not the snapshot's own, whole, is
// invalid, and nothing is admitted.
func TestGroupIssueAdmissionRefusesAGroupThatIsNotTheSnapshots(t *testing.T) {
	ctx := context.Background()
	v, snap := newGroupEnv(t, envOpts{})
	good := groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)
	for name, change := range map[string]func(g *r.IssueGroup){
		"a digest of other rows":      func(g *r.IssueGroup) { g.RowsSHA256 = jsonDigest(`["customer_a","current","10.00"]`) },
		"a row left out":              func(g *r.IssueGroup) { g.Rows = []int{0, 2}; g.RowsSHA256 = rowsDigest(t, snap, 0, 2) },
		"a row of another key":        func(g *r.IssueGroup) { g.Rows = []int{0, 1, 2, 4}; g.RowsSHA256 = rowsDigest(t, snap, 0, 1, 2, 4) },
		"another key":                 func(g *r.IssueGroup) { g.Key = []string{"customer_b"} },
		"a key of another case":       func(g *r.IssueGroup) { g.Key = []string{"Customer_a"} },
		"a key with a trailing space": func(g *r.IssueGroup) { g.Key = []string{"customer_a "} },
		"no key value":                func(g *r.IssueGroup) { g.Key = nil },
		"a key value too many":        func(g *r.IssueGroup) { g.Key = []string{"customer_a", "current"} },
		"rows out of order":           func(g *r.IssueGroup) { g.Rows = []int{2, 0, 4} },
		"a row twice":                 func(g *r.IssueGroup) { g.Rows = []int{0, 0, 2, 4} },
		"a row beyond the snapshot":   func(g *r.IssueGroup) { g.Rows = []int{0, 2, 4, 5} },
		"a negative row":              func(g *r.IssueGroup) { g.Rows = []int{-1, 0, 2, 4} },
		"no row":                      func(g *r.IssueGroup) { g.Rows = nil },
		"a field that is a metric":    func(g *r.IssueGroup) { g.Fields, g.Key = []string{"ar.open_balance"}, []string{"10.00"} },
		"a field the snapshot lacks":  func(g *r.IssueGroup) { g.Fields, g.Key = []string{"ghost"}, []string{"customer_a"} },
		"an empty field name":         func(g *r.IssueGroup) { g.Fields = []string{""} },
		"a field twice": func(g *r.IssueGroup) {
			g.Fields, g.Key = []string{"customer.id", "customer.id"}, []string{"customer_a", "customer_a"}
		},
		"four fields": func(g *r.IssueGroup) {
			g.Fields, g.Key = []string{"customer.id", "aging.bucket", "a", "b"}, []string{"customer_a", "current", "", ""}
		},
		"no digest":            func(g *r.IssueGroup) { g.RowsSHA256 = "" },
		"an upper-case digest": func(g *r.IssueGroup) { g.RowsSHA256 = strings.ToUpper(g.RowsSHA256) },
		"the whole snapshot as a keyed group": func(g *r.IssueGroup) {
			g.Fields, g.Key, g.Rows, g.RowsSHA256 = nil, nil, []int{0, 2, 4}, rowsDigest(t, snap, 0, 2, 4)
		},
	} {
		g := good.Clone()
		change(&g)
		if _, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, g), "k"); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s: %v", name, e)
		}
	}
	// A group together with a row is invalid, whatever the group.
	both := groupInput(v, snap, good)
	both.RowIndex = 1
	if _, e := v.s.SubmitIssue(ctx, v.p, both, "k"); !errors.Is(e, r.ErrInvalid) {
		t.Errorf("a group and a row: %v", e)
	}
	// The whole snapshot is a group of every row only.
	if _, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, groupOfAllRows(t, snap, 3)), "k"); !errors.Is(e, r.ErrInvalid) {
		t.Errorf("the whole snapshot without a row: %v", e)
	}
	if jobs, e := v.repo.Issues(ctx, v.p.Identity, []string{contractsim.FinanceIntegration}, 10); e != nil || len(jobs) != 0 {
		t.Fatal("a job was admitted", jobs, e)
	}
	// None of the refusals carries a value of the snapshot.
	_, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, r.IssueGroup{Fields: good.Fields, Key: []string{"customer_a"}, Rows: []int{0}, RowsSHA256: good.RowsSHA256}), "k")
	if e == nil || strings.Contains(e.Error(), "customer") || strings.Contains(e.Error(), "10.00") {
		t.Fatalf("the refusal names a value: %v", e)
	}
	if _, e = v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, good), "k"); e != nil {
		t.Fatal("the good group is refused", e)
	}
}

func rowsDigest(t *testing.T, snap r.Snapshot, rows ...int) string {
	t.Helper()
	picked, e := r.GroupRows(snap, rows)
	if e != nil {
		t.Fatal(e)
	}
	return r.RowsDigest(picked)
}

// groupOfAllRows is a group of the first n rows of the snapshot without fields: not the whole snapshot when n is
// short of it.
func groupOfAllRows(t *testing.T, snap r.Snapshot, n int) r.IssueGroup {
	t.Helper()
	rows := make([]int, n)
	for i := range rows {
		rows[i] = i
	}
	return r.IssueGroup{Fields: []string{}, Key: []string{}, Rows: rows, RowsSHA256: rowsDigest(t, snap, rows...)}
}

// A product that does not declare contract version 2 is never sent a group: admission answers unsupported and
// admits nothing (a row is admitted and issued as it always was), and a group job that is run against an adapter
// without the declaration fails before anything is read or asked.
func TestGroupIssueIsNeverSentToAVersionOneAdapter(t *testing.T) {
	ctx := context.Background()
	v, snap := newGroupEnv(t, envOpts{contractOne: true})
	good := groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)
	if _, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, good), "k"); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("a group was admitted for a version-1 adapter", e)
	}
	// Whatever else is wrong with the group, the answer is the same: no value decides it.
	bad := good.Clone()
	bad.RowsSHA256 = strings.Repeat("0", 64)
	if _, e := v.s.SubmitIssue(ctx, v.p, groupInput(v, snap, bad), "k"); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("an invalid group for a version-1 adapter", e)
	}
	if jobs, e := v.repo.Issues(ctx, v.p.Identity, []string{contractsim.FinanceIntegration}, 10); e != nil || len(jobs) != 0 {
		t.Fatal("a job was admitted", jobs, e)
	}
	// The same adapter issues a row as before.
	row, e := v.s.SubmitIssue(ctx, v.p, v.input(1), "row")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.run(); e != nil {
		t.Fatal(e)
	}
	if got := v.job(row.ID); got.State != "succeeded" || got.Issue.Number != "AR-000001" {
		t.Fatalf("%+v %+v", got, got.Issue)
	}
	if in := v.finance.intents; len(in) != 1 || in[0].Group != nil || in[0].RowSHA256 == "" {
		t.Fatalf("the intents the product saw: %+v", in)
	}
	if r.GroupIssueSupported(contractOne{v.finance, v.finance}) || r.IssueContractVersion(contractOne{v.finance, v.finance}) != 1 || !r.GroupIssueSupported(v.finance) || r.IssueContractVersion(v.finance) != 2 || r.GroupIssueSupported(hidden{v.finance}) {
		t.Fatal("the declaration of the contract version")
	}
	// A group job stored while the adapter declared version 2, run by a service whose adapter does not: it fails as
	// unsupported, the product is asked nothing and has nothing to void.
	w, wsnap := newGroupEnv(t, envOpts{})
	j, e := w.s.SubmitIssue(ctx, w.p, groupInput(w, wsnap, groupOf(t, wsnap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)), "k")
	if e != nil {
		t.Fatal(e)
	}
	old, e := r.NewService(r.Config{Repository: w.repo, Connections: []r.Connection{{Tenant: w.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: contractOne{w.finance, w.finance}}}, CursorKey: []byte("01234567890123456789012345678901"), Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: w.issuer, Now: w.clock.Now})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = old.RunOne(ctx); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("a group job ran against a version-1 adapter", e)
	}
	if st := w.stored(j.ID); st.State != "failed" || st.ErrorCode != "UNSUPPORTED_COMBINATION" {
		t.Fatalf("%+v", st)
	}
	if len(w.finance.intents) != 0 || len(w.finance.voids) != 0 || w.issuer.calls != 0 || w.issuer.checks != 0 {
		t.Fatal("the product or the renderer was called for a group a version-1 adapter cannot take")
	}
}

// The worker verifies the frozen group against the frozen snapshot before it asks the product: a stored job whose group
// is not the snapshot's (a hand-made job with its request hash recomputed, which admission through the service would
// never make) fails INTEGRITY_FAILURE with nothing asked, as a row beyond its snapshot does.
func TestGroupJobThatIsNotItsSnapshotsFailsWithoutCallingTheProduct(t *testing.T) {
	ctx := context.Background()
	v, snap := newGroupEnv(t, envOpts{})
	good := v.stored(mustSubmit(t, v, groupInput(v, snap, groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)), "good").ID)
	if ran, e := v.run(); !ran || e != nil {
		t.Fatal(ran, e)
	}
	calls := len(v.finance.intents)
	for name, change := range map[string]func(g *r.IssueGroup){
		"a digest of other rows": func(g *r.IssueGroup) { g.RowsSHA256 = jsonDigest(`["x"]`) },
		"a row left out":         func(g *r.IssueGroup) { g.Rows = []int{0, 2}; g.RowsSHA256 = rowsDigest(t, snap, 0, 2) },
		"another key":            func(g *r.IssueGroup) { g.Key = []string{"customer_b"} },
		"a row beyond":           func(g *r.IssueGroup) { g.Rows = []int{0, 2, 4, 5} },
	} {
		crafted := good
		g := good.Group.Clone()
		change(&g)
		crafted.ID, crafted.Key, crafted.Group = r.NewID(), "crafted "+name, &g
		crafted.RequestHash = r.IssueGroupRequestHash(crafted.TemplateID, crafted.TemplateVersion, crafted.TemplateSHA256, crafted.SnapshotID, g)
		if e := r.ValidateAdmission(crafted); e != nil {
			t.Fatalf("%s: the crafted job is not a valid admission: %v", name, e)
		}
		if _, e := v.repo.Admit(ctx, crafted); e != nil {
			t.Fatal(name, e)
		}
		if ran, e := v.run(); !ran || !errors.Is(e, r.ErrIntegrity) {
			t.Fatal(name, ran, e)
		}
		if st := v.stored(crafted.ID); st.State != "failed" || st.ErrorCode != "INTEGRITY_FAILURE" || st.Generation != 1 {
			t.Fatalf("%s: %+v", name, st)
		}
		if _, ok := held(v, crafted.ID); ok {
			t.Fatalf("%s: the product holds something for the crafted job", name)
		}
	}
	if len(v.finance.intents) != calls || len(v.finance.voids) != 0 || v.issuer.checks != 1 {
		t.Fatalf("the product was asked about a group that is not the snapshot's: intents %d (was %d), voids %+v, checks %d", len(v.finance.intents), calls, v.finance.voids, v.issuer.checks)
	}
}

func mustSubmit(t *testing.T, v *issueEnv, in r.IssueInput, key string) r.JobView {
	t.Helper()
	j, e := v.s.SubmitIssue(context.Background(), v.p, in, key)
	if e != nil {
		t.Fatal(e)
	}
	return j
}

// The product checks the rows against its own source snapshot and refuses what they are not: the platform's frozen
// group is true to its snapshot, but the product's source holds other rows (here the intent is altered on its way, as
// a source whose snapshot differs would see it). The job fails AUTH_SCOPE_DENIED, nothing is reserved, and the failure
// goes the way of S03-H: recorded first as ISSUE_UNRECONCILED, fenced by the claim, then voided, then resolved to the
// job's own code.
func TestGroupRefusedByTheProductIsVoidedInTheOrderOfS03H(t *testing.T) {
	for name, change := range map[string]func(*r.IssueIntent){
		"a digest of other rows": func(in *r.IssueIntent) { in.Group.RowsSHA256 = jsonDigest(`["x"]`) },
		"a row the source does not have": func(in *r.IssueIntent) {
			g := in.Group.Clone()
			g.Rows = append(g.Rows, 9)
			in.Group = &g
		},
		"a group left incomplete": func(in *r.IssueIntent) {
			g := in.Group.Clone()
			g.Rows = g.Rows[:2]
			in.Group = &g
		},
		"another key": func(in *r.IssueIntent) {
			g := in.Group.Clone()
			g.Key = []string{"customer_b"}
			in.Group = &g
		},
	} {
		t.Run(name, func(t *testing.T) {
			v, snap := newGroupEnv(t, envOpts{})
			j := mustSubmit(t, v, groupInput(v, snap, groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)), "k")
			v.finance.intentAs = change
			var atVoid r.Job
			v.finance.voidHook = func() { atVoid = v.stored(j.ID) }
			if _, e := v.run(); !errors.Is(e, r.ErrDenied) || errors.Is(e, r.ErrUnreconciled) {
				t.Fatal("the product's refusal did not fail the job", e)
			}
			if atVoid.State != "failed" || atVoid.ErrorCode != "ISSUE_UNRECONCILED" {
				t.Fatalf("when the product was told to void, the job was %s %q", atVoid.State, atVoid.ErrorCode)
			}
			if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "AUTH_SCOPE_DENIED" {
				t.Fatalf("%+v", st)
			}
			if len(v.store.fails) != 1 || v.store.fails[0] != "ISSUE_UNRECONCILED" || len(v.store.resolves) != 1 || v.store.resolves[0] != "AUTH_SCOPE_DENIED" {
				t.Fatalf("failed with %v, resolved to %v", v.store.fails, v.store.resolves)
			}
			if len(v.finance.voids) != 1 || v.finance.voids[0].JobID != j.ID || v.finance.voids[0].Reason != "AUTH_SCOPE_DENIED" {
				t.Fatalf("voids %+v", v.finance.voids)
			}
			// Nothing was reserved, rendered or confirmed, and no number was spent; the tombstone is the product's.
			if rec, ok := held(v, j.ID); !ok || rec.State != contractsim.IssueVoid || rec.Number != "" {
				t.Fatalf("the product's record: %+v %v", rec, ok)
			}
			if v.issuer.calls != 0 || len(v.finance.confirmed) != 0 {
				t.Fatal("a refused group was rendered or confirmed")
			}
			if view, e := v.s.Issue(context.Background(), v.p, j.ID); e != nil || view.State != "failed" || view.ErrorCode != "AUTH_SCOPE_DENIED" || view.Number != "" || view.Group == nil || view.RowIndex != nil {
				t.Fatalf("the failed job's view: %+v %v", view, e)
			}
		})
	}
}

// The failures after the product reserved a number are the same for a group as for a row: voided, in the order of
// S03-H, with the code as the reason; a lost confirmation is voided; a void that cannot be delivered leaves the job
// ISSUE_UNRECONCILED.
func TestGroupFailuresVoidAtTheProductAsRowFailuresDo(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		arrange func(v *issueEnv)
		want    string
	}{
		"the template is withdrawn while rendering": {func(v *issueEnv) { v.issuer.err = r.ErrUnpublished }, "TEMPLATE_NOT_PUBLISHED"},
		"the layout refuses the render":             {func(v *issueEnv) { v.issuer.err = r.ErrUnsupported }, "UNSUPPORTED_COMBINATION"},
		"the confirmation is refused":               {func(v *issueEnv) { v.finance.confirmE = r.ErrDenied }, "AUTH_SCOPE_DENIED"},
		"the renderer panics":                       {func(v *issueEnv) { v.issuer.panics = true }, "EXECUTION_FAILED"},
		"the product's number is not a number":      {func(v *issueEnv) { v.finance.prepareAs = func(res *r.IssueReservation) { res.Number = "AR 1" } }, "INTEGRITY_FAILURE"},
	} {
		t.Run(name, func(t *testing.T) {
			v, snap := newGroupEnv(t, envOpts{})
			j := mustSubmit(t, v, groupInput(v, snap, groupOf(t, snap, []string{"aging.bucket"}, []string{"current"}, 0, 3, 4)), "k")
			c.arrange(v)
			if _, e := v.run(); e == nil {
				t.Fatal("the job did not fail")
			}
			if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != c.want {
				t.Fatalf("%+v", st)
			}
			assertVoided(t, v, j.ID, c.want)
			if len(v.store.fails) != 1 || v.store.fails[0] != "ISSUE_UNRECONCILED" || len(v.store.resolves) != 1 || v.store.resolves[0] != c.want {
				t.Fatalf("failed with %v, resolved to %v", v.store.fails, v.store.resolves)
			}
			if _, e := v.repo.Issue(ctx, v.p.Identity, j.ID); !errors.Is(e, r.ErrNotFound) {
				t.Fatal("a record of a failed job", e)
			}
		})
	}
	// A void that cannot be delivered leaves the job unreconciled, with the group still the job's.
	v, snap := newGroupEnv(t, envOpts{})
	j := mustSubmit(t, v, groupInput(v, snap, groupOf(t, snap, nil, nil, 0, 1, 2, 3, 4)), "k")
	v.issuer.err = r.ErrUnpublished
	v.finance.voidE = r.ErrUnavailable
	if _, e := v.run(); !errors.Is(e, r.ErrUnreconciled) {
		t.Fatal(e)
	}
	if st := v.stored(j.ID); st.State != "failed" || st.ErrorCode != "ISSUE_UNRECONCILED" || st.Group == nil || len(st.Group.Rows) != 5 {
		t.Fatalf("%+v", st)
	}
}

// A lost lease never voids: the worker that holds the job reuses the reservation, which the product answers again by the job
// id with the same group, renders the same bytes and confirms idempotently.
func TestGroupJobClaimedAgainReusesItsReservation(t *testing.T) {
	ctx := context.Background()
	v, snap := newGroupEnv(t, envOpts{})
	j := mustSubmit(t, v, groupInput(v, snap, groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)), "k")
	v.store.lose = 1
	if _, e := v.run(); !errors.Is(e, r.ErrLease) {
		t.Fatal(e)
	}
	if _, e := v.run(); e != nil {
		t.Fatal(e)
	}
	if len(v.finance.voids) != 0 || len(v.finance.intents) != 2 || !v.finance.intents[0].Group.Equal(v.finance.intents[1].Group) || len(v.finance.prepared) != 2 || !sameReservation(v.finance.prepared[0], v.finance.prepared[1]) {
		t.Fatalf("intents %d, reservations %+v, voids %+v", len(v.finance.intents), v.finance.prepared, v.finance.voids)
	}
	if got := v.job(j.ID); got.State != "succeeded" || got.Issue.Number != "AR-000001" || got.Issue.Group == nil {
		t.Fatalf("%+v %+v", got, got.Issue)
	}
	if _, e := v.s.Download(ctx, v.p, v.job(j.ID).ArtifactID); e != nil {
		t.Fatal(e)
	}
}

// The record and the views are cross-checked against the job: a record whose group is not the job's is an integrity
// failure wherever it is shown; so is a record that gains a group the job lacks, and one that loses it.
func TestGroupViewsCheckTheRecordsGroupAgainstTheJobs(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(i *r.Issue){
		"another key": func(i *r.Issue) {
			i.Group = &r.IssueGroup{Fields: i.Group.Fields, Key: []string{"customer_b"}, Rows: i.Group.Rows, RowsSHA256: i.Group.RowsSHA256}
		},
		"another rows":   func(i *r.Issue) { g := i.Group.Clone(); g.Rows = g.Rows[:2]; i.Group = &g },
		"another digest": func(i *r.Issue) { g := i.Group.Clone(); g.RowsSHA256 = strings.Repeat("0", 64); i.Group = &g },
		"another fields": func(i *r.Issue) { g := i.Group.Clone(); g.Fields = []string{"aging.bucket"}; i.Group = &g },
		"no group":       func(i *r.Issue) { i.Group = nil },
		"a row as well":  func(i *r.Issue) { i.RowIndex = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			v, snap := newGroupEnv(t, envOpts{})
			j := mustSubmit(t, v, groupInput(v, snap, groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)), "k")
			if _, e := v.run(); e != nil {
				t.Fatal(e)
			}
			v.store.recordAs = change
			if _, e := v.s.Issue(ctx, v.p, j.ID); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the record: %v", e)
			}
			if _, e := v.s.Issues(ctx, v.p, 10); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the listing: %v", e)
			}
			if _, e := v.s.Job(ctx, v.p, j.ID); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the job: %v", e)
			}
			v.store.recordAs = nil
			if got, e := v.s.Issue(ctx, v.p, j.ID); e != nil || got.Group == nil || got.Number != "AR-000001" {
				t.Fatal(got, e)
			}
		})
	}
	// And the other way: a row job whose record gains a group.
	v := newIssueEnv(t, envOpts{})
	j := v.submit(1, "k")
	if _, e := v.run(); e != nil {
		t.Fatal(e)
	}
	v.store.recordAs = func(i *r.Issue) { i.Group = &r.IssueGroup{Rows: []int{1}} }
	if _, e := v.s.Issue(ctx, v.p, j.ID); !errors.Is(e, r.ErrIntegrity) {
		t.Errorf("a row record that gained a group: %v", e)
	}
}

// What a group job and a group record must be to be admitted and published: the contract's own checks.
func TestGroupContractRefusesInconsistentJobsAndPublications(t *testing.T) {
	ctx := context.Background()
	v, snap := newGroupEnv(t, envOpts{})
	good := groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)
	j := mustSubmit(t, v, groupInput(v, snap, good), "k")
	stored := v.stored(j.ID)
	if e := r.ValidateAdmission(stored); e != nil {
		t.Fatalf("the stored group job is not valid: %v", e)
	}
	rehash := func(j *r.Job) {
		if j.Group != nil {
			j.RequestHash = r.IssueGroupRequestHash(j.TemplateID, j.TemplateVersion, j.TemplateSHA256, j.SnapshotID, *j.Group)
		}
	}
	for name, change := range map[string]func(*r.Job){
		"another key":    func(j *r.Job) { g := j.Group.Clone(); g.Key = []string{"x"}; j.Group = &g },
		"another digest": func(j *r.Job) { g := j.Group.Clone(); g.RowsSHA256 = strings.Repeat("0", 64); j.Group = &g },
		"another rows":   func(j *r.Job) { g := j.Group.Clone(); g.Rows = g.Rows[1:]; j.Group = &g },
		"the request hash unmoved": func(j *r.Job) {
			g := j.Group.Clone()
			g.Rows = g.Rows[1:]
			j.Group = &g
			j.RequestHash = stored.RequestHash
		},
		"a row as well": func(j *r.Job) { j.RowIndex = 1; rehash(j) },
		"a row's request hash": func(j *r.Job) {
			j.RequestHash = r.IssueRequestHash(j.TemplateID, j.TemplateVersion, j.TemplateSHA256, j.SnapshotID, 0)
		},
		"no rows":               func(j *r.Job) { g := j.Group.Clone(); g.Rows = []int{}; j.Group = &g; rehash(j) },
		"unordered rows":        func(j *r.Job) { g := j.Group.Clone(); g.Rows = []int{2, 0, 4}; j.Group = &g; rehash(j) },
		"a row past the limit":  func(j *r.Job) { g := j.Group.Clone(); g.Rows = []int{0, r.MaxRows}; j.Group = &g; rehash(j) },
		"more keys than fields": func(j *r.Job) { g := j.Group.Clone(); g.Key = []string{"a", "b"}; j.Group = &g; rehash(j) },
		"an export with a group": func(j *r.Job) {
			j.Kind, j.Format, j.RequestHash, j.TemplateID, j.TemplateVersion, j.TemplateSHA256, j.Template = "export", "pdf", r.ExportRequestHash(j.SnapshotID, "pdf"), "", 0, "", ""
		},
	} {
		c := stored
		change(&c)
		if r.ValidateAdmission(c) == nil {
			t.Errorf("%s: an inconsistent group job is admitted", name)
		}
	}
	// A row job with a group's request hash, and a group job under a row's, are refused too.
	rowJob := v.stored(v.submit(1, "row").ID)
	rowJob.Group = &good
	rowJob.RowIndex = 0
	if r.ValidateAdmission(rowJob) == nil {
		t.Error("a row job's hash with a group")
	}
	claim, e := v.repo.Claim(ctx, time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	at := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	content := []byte("%PDF-1.7 x")
	artifact := r.Artifact{ID: r.NewID(), Owner: stored.Owner, SnapshotID: stored.SnapshotID, Format: "pdf", ExpiresAt: stored.Prepared.ExpiresAt, Content: content, Issue: stored.ID}
	artifact.SHA256 = digestOf(content)
	rec := r.Issue{ID: stored.ID, Owner: stored.Owner, Number: "AR-000007", TemplateID: stored.TemplateID, TemplateVersion: stored.TemplateVersion, TemplateSHA256: stored.TemplateSHA256, SnapshotID: stored.SnapshotID, SnapshotSHA256: strings.Repeat("c", 64), Group: &good, ArtifactID: artifact.ID, SHA256: artifact.SHA256, Pages: 2, IssuedAt: at,
		ReservationRef: "reservation-7", ConfirmationRef: "confirmation-7", ConfirmedAt: at.Add(2 * time.Second)}
	rec.Stamp = r.IssueStamp(rec.Number, at, rec.ID)
	if e := r.ValidatePublication(claim.Job, r.Publication{Artifact: &artifact, Issue: &rec}); e != nil {
		t.Fatalf("a consistent group publication is refused: %v", e)
	}
	for name, change := range map[string]func(i *r.Issue){
		"no group":       func(i *r.Issue) { i.Group = nil },
		"another key":    func(i *r.Issue) { g := i.Group.Clone(); g.Key = []string{"customer_b"}; i.Group = &g },
		"another rows":   func(i *r.Issue) { g := i.Group.Clone(); g.Rows = []int{0, 2}; i.Group = &g },
		"another digest": func(i *r.Issue) { g := i.Group.Clone(); g.RowsSHA256 = strings.Repeat("1", 64); i.Group = &g },
		"another fields": func(i *r.Issue) { g := i.Group.Clone(); g.Fields = nil; i.Group = &g },
		"a row as well":  func(i *r.Issue) { i.RowIndex = 2 },
	} {
		i := rec
		change(&i)
		if r.ValidatePublication(claim.Job, r.Publication{Artifact: &artifact, Issue: &i}) == nil {
			t.Errorf("%s: an inconsistent group publication is accepted", name)
		}
	}
}

// The group's own rules, apart from any service: VerifyGroup, NewIssueGroup, the clone and the comparison.
func TestIssueGroupHelpers(t *testing.T) {
	_, snap := newGroupEnv(t, envOpts{})
	g := groupOf(t, snap, []string{"customer.id"}, []string{"customer_a"}, 0, 2, 4)
	if e := r.VerifyGroup(snap, g); e != nil {
		t.Fatal(e)
	}
	// A snapshot row that does not fit the columns is an integrity failure, not a refusal of the group.
	broken := snap
	broken.Result.Rows = append([][]string{}, snap.Result.Rows...)
	broken.Result.Rows[3] = []string{"customer_c"}
	if e := r.VerifyGroup(broken, g); !errors.Is(e, r.ErrIntegrity) {
		t.Fatal(e)
	}
	// Neither the key nor a row is named by an error.
	for _, bad := range []r.IssueGroup{{Fields: g.Fields, Key: []string{"customer_a-secret"}, Rows: g.Rows, RowsSHA256: g.RowsSHA256}, {Fields: []string{"customer.id-secret"}, Key: g.Key, Rows: g.Rows, RowsSHA256: g.RowsSHA256}} {
		if e := r.VerifyGroup(snap, bad); e == nil || strings.Contains(e.Error(), "secret") || strings.Contains(e.Error(), "customer") {
			t.Fatalf("the refusal: %v", e)
		}
	}
	// Equality: nil is nil only; empty lists are missing ones.
	var none *r.IssueGroup
	empty := r.IssueGroup{}
	other := g.Clone()
	if !none.Equal(nil) || none.Equal(&g) || g.Equal(nil) || !g.Equal(&other) || !(&empty).Equal(&r.IssueGroup{Fields: []string{}, Key: []string{}, Rows: []int{}}) {
		t.Fatal("equality")
	}
	other.Rows[0] = 1
	if g.Equal(&other) || g.Rows[0] != 0 {
		t.Fatal("a clone shares its lists")
	}
	// The digest of no rows is of `[]`, whether the list is nil or empty.
	if r.RowsDigest(nil) != r.RowsDigest([][]string{}) || r.RowsDigest(nil) != jsonDigest() {
		t.Fatal("the digest of no rows")
	}
	// The digest is the SHA-256 of the JSON array of the rows' arrays, escaped as Go escapes: the form docs/API.md gives.
	if got := r.RowsDigest([][]string{{"甲方<&>", "a" + "\u2028" + "b"}, {"", "x\"y"}}); got != jsonDigest(`["甲方\u003c\u0026\u003e","a\u2028b"]`, `["","x\"y"]`) {
		t.Fatalf("the digest of rows with characters JSON escapes: %s", got)
	}
}

// A 1.x issue is stored and sent exactly as it was before groups existed: the group is absent, not null, from the job's and the
// record's payloads and from the intent the product is asked about, and the views of a row issue have no group member.
func TestRowIssueIsStoredAndSentWithoutAGroup(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	j := v.submit(1, "k")
	if _, e := v.run(); e != nil {
		t.Fatal(e)
	}
	stored, e := json.Marshal(v.stored(j.ID))
	if e != nil || strings.Contains(string(stored), "Group") || !strings.Contains(string(stored), `"RowIndex":1`) {
		t.Fatalf("the stored job: %s %v", stored, e)
	}
	rec, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if b, _ := json.Marshal(rec); strings.Contains(string(b), "Group") || !strings.Contains(string(b), `"RowIndex":1`) {
		t.Fatalf("the record: %s", b)
	}
	if b, _ := json.Marshal(v.finance.intents[0]); strings.Contains(string(b), "Group") || !strings.Contains(string(b), `"RowIndex":1`) {
		t.Fatalf("the intent: %s", b)
	}
	view, e := v.s.Issue(ctx, v.p, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if b, _ := json.Marshal(view); strings.Contains(string(b), "group") || !strings.Contains(string(b), `"row_index":1`) {
		t.Fatalf("the view: %s", b)
	}
	if r.IssueRequestHash(templateID, 3, v.stored(j.ID).TemplateSHA256, v.snapshot, 1) != v.stored(j.ID).RequestHash {
		t.Fatal("the request hash of a row issue changed")
	}
}
