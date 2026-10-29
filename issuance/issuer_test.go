package issuance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/issuance"
	r "idp.local/reporting/reporting"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// issuedAt is the issue time the product reserved: the stamps below print it, and the issuer
// hands it to the renderer as it came.
var issuedAt = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

type fakeRenderer struct {
	calls int
	stamp string
	at    time.Time
	mode  string
	bg    int
	rows  int
	table [][]string
	vals  map[string]string // the values of the last issue
	out   []byte
	pages int
	err   error
	// What CheckIssue was given, and how it answers.
	checks        int
	checkedValues map[string]string
	checkedMode   string
	checkedBg     int
	checkedRows   int
	checkErr      error
}

func (f *fakeRenderer) CheckIssue(_ context.Context, d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData) error {
	f.checks++
	f.checkedValues, f.checkedBg, f.checkedMode, f.checkedRows = values, len(background), "full", 0
	if d.Print != nil {
		f.checkedMode = d.Print.Mode
	}
	if table != nil {
		f.checkedRows = len(table.Rows)
	}
	return f.checkErr
}

func (f *fakeRenderer) Issue(_ context.Context, d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData, stamp string, at time.Time) ([]byte, int, error) {
	f.calls++
	f.stamp, f.at, f.bg, f.vals = stamp, at, len(background), values
	f.mode = "full"
	if d.Print != nil {
		f.mode = d.Print.Mode
	}
	if table != nil {
		f.rows, f.table = len(table.Rows), table.Rows
	}
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.out, f.pages, nil
}

func snapshot() r.Snapshot {
	return r.Snapshot{ID: strings.Repeat("5", 32), Owner: r.Owner{Identity: contractsim.DemoPrincipal().Identity, Integration: "i"}, Query: r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}},
		Result: r.Result{Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2}}, Rows: [][]string{{"甲", "1.00"}, {"乙", "2.50"}}, Totals: map[string]string{"amount": "3.50"}, Completeness: "complete"}}
}

func document() doctemplates.Document {
	return doctemplates.Document{Schema: "1.3", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Elements: []doctemplates.Element{{ID: "n1", Source: "row", Field: "customer", X: 20, Y: 30, Width: 100, Height: 10, FontSize: 12, Align: "left"}},
		Print:    &doctemplates.PrintProfile{Mode: "full"},
		Table:    &doctemplates.Table{X: 20, Y: 60, Width: 100, Height: 100, FontSize: 9, Columns: []doctemplates.TableColumn{{Field: "customer", Width: 50}, {Field: "amount", Width: 50}}}}
}

// notCanonical is the same configuration in other bytes: indented, with its keys and values spaced out. It
// decodes strictly to the same document, and is not what doctemplates.Canonical writes.
func notCanonical(t *testing.T, canonical string) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(canonical), "", "  "); err != nil {
		t.Fatal(err)
	}
	if out.String() == canonical {
		t.Fatal("the canonical bytes are already indented")
	}
	// It is the same configuration: it decodes strictly, and to the same canonical bytes.
	var d, again doctemplates.Document
	if err := r.DecodeStrict(strings.NewReader(canonical), &d); err != nil {
		t.Fatal(err)
	}
	if err := r.DecodeStrict(strings.NewReader(out.String()), &again); err != nil {
		t.Fatal("the indented configuration does not decode:", err)
	}
	a, _ := doctemplates.Canonical(d)
	b, _ := doctemplates.Canonical(again)
	if string(a) != canonical || string(b) != canonical {
		t.Fatal("the indented configuration is not the same configuration")
	}
	return out.String()
}

func TestIssuerRendersOnlyThePublishedFrozenVersion(t *testing.T) {
	ctx := context.Background()
	owner := snapshot().Owner
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	v1, e := store.Save(owner.Identity, "", 0, document())
	if e != nil {
		t.Fatal(e)
	}
	frozen, e := doctemplates.Canonical(v1.Layout)
	if e != nil {
		t.Fatal(e)
	}
	renderer := &fakeRenderer{out: []byte("%PDF-1.7 issued"), pages: 1}
	issuer, e := issuance.New(store, nil, renderer)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = issuance.New(nil, nil, renderer); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("issuer without a store")
	}
	job := r.Job{ID: strings.Repeat("7", 32), Owner: owner, Kind: "issue", TemplateID: v1.ID, TemplateVersion: 1, Template: string(frozen), RowIndex: 1}
	req := r.IssueRequest{Job: job, Snapshot: snapshot(), Stamp: r.IssueStamp("AR-000004", issuedAt, job.ID), At: issuedAt}
	// Not published yet.
	if _, e = issuer.Issue(ctx, req); !errors.Is(e, r.ErrUnpublished) || renderer.calls != 0 {
		t.Fatal("unpublished template rendered", e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	out, e := issuer.Issue(ctx, req)
	if e != nil || string(out.PDF) != "%PDF-1.7 issued" || out.Pages != 1 || renderer.calls != 1 || renderer.stamp != req.Stamp || !renderer.at.Equal(issuedAt) || renderer.rows != 2 || renderer.mode != "full" || renderer.bg != 0 {
		t.Fatalf("%+v %v %+v", out, e, renderer)
	}
	// Frozen bytes that are not the stored version, a wrong version number, a withdrawn and a
	// superseded publication all refuse; so does a row the snapshot lacks, and a request
	// without the product's issue time (the zero time) is invalid before anything renders.
	for name, c := range map[string]struct {
		change func(*r.Job, r.Snapshot) r.Snapshot
		want   error
	}{
		"other bytes":   {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Template = `{"x":1}`; return s }, r.ErrIntegrity},
		"other version": {func(j *r.Job, s r.Snapshot) r.Snapshot { j.TemplateVersion = 2; return s }, r.ErrUnpublished},
		"other row":     {func(j *r.Job, s r.Snapshot) r.Snapshot { j.RowIndex = 2; return s }, r.ErrInvalid},
		"other dataset": {func(j *r.Job, s r.Snapshot) r.Snapshot { s.Query.Dataset.Version = "2"; return s }, r.ErrDenied},
		"export job":    {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Kind = "export"; return s }, r.ErrInvalid},
		"empty stamp":   {func(j *r.Job, s r.Snapshot) r.Snapshot { return s }, r.ErrInvalid},
		"no issue time": {func(j *r.Job, s r.Snapshot) r.Snapshot { return s }, r.ErrInvalid},
		// The same configuration, as JSON says it, in bytes that are not the canonical ones the version was frozen as.
		"equal but not canonical bytes": {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Template = notCanonical(t, j.Template); return s }, r.ErrIntegrity},
	} {
		j := job
		s := c.change(&j, snapshot())
		q := r.IssueRequest{Job: j, Snapshot: s, Stamp: req.Stamp, At: req.At}
		switch name {
		case "empty stamp":
			q.Stamp = ""
		case "no issue time":
			q.At = time.Time{}
		}
		calls := renderer.calls
		if _, e := issuer.Issue(ctx, q); !errors.Is(e, c.want) || renderer.calls != calls {
			t.Errorf("%s: %v (rendered: %v); want %v", name, e, renderer.calls != calls, c.want)
		}
	}
	// Superseded: version 2 saved and published; the frozen version 1 no longer issues.
	d2 := document()
	d2.Elements[0].Field = "amount"
	if _, e = store.Save(owner.Identity, v1.ID, 1, d2); e != nil {
		t.Fatal(e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "publish", 2, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = issuer.Issue(ctx, req); !errors.Is(e, r.ErrUnpublished) {
		t.Fatal("superseded version issued", e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "withdraw", 0, 2); e != nil {
		t.Fatal(e)
	}
	if _, e = issuer.Issue(ctx, req); !errors.Is(e, r.ErrUnpublished) {
		t.Fatal("withdrawn template issued", e)
	}
	// A background named without a store is unsupported; in variables mode no background is
	// passed; a renderer answer that is not a PDF is an integrity failure.
	if _, e = store.Publish(owner.Identity, v1.ID, "publish", 1, 3); e != nil {
		t.Fatal(e)
	}
	withBackground := document()
	withBackground.Background = &doctemplates.BackgroundRef{ID: strings.Repeat("b", 32), SHA256: strings.Repeat("c", 64)}
	frozenBg, _ := doctemplates.Canonical(withBackground)
	jb := job
	jb.Template = string(frozenBg)
	// The frozen bytes must match the published version, which has no background: integrity.
	if _, e = issuer.Issue(ctx, r.IssueRequest{Job: jb, Snapshot: snapshot(), Stamp: req.Stamp, At: req.At}); !errors.Is(e, r.ErrUnpublished) {
		t.Fatal("frozen configuration differing from the published one", e)
	}
	renderer.out = []byte("not a pdf")
	if _, e = issuer.Issue(ctx, req); !errors.Is(e, r.ErrIntegrity) {
		t.Fatal("non-PDF accepted", e)
	}
	renderer.out, renderer.err = []byte("%PDF-1.7"), r.ErrUnsupported
	if _, e = issuer.Issue(ctx, req); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("renderer refusal not passed on", e)
	}
}

// TestIssuerChecksWhatItCanWithoutRendering: Check is the issuer's part that needs neither the
// product's number nor the render (S03-H): the frozen configuration is the published version, the row
// and the table bind, the background resolves, and the renderer's layout accepts the values. It fails
// as Issue does for the same reasons, takes no stamp and no issue time, never renders, and Issue still
// repeats its checks, because the template can be withdrawn between the two.
func TestIssuerChecksWhatItCanWithoutRendering(t *testing.T) {
	ctx := context.Background()
	owner := snapshot().Owner
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	v1, e := store.Save(owner.Identity, "", 0, document())
	if e != nil {
		t.Fatal(e)
	}
	frozen, e := doctemplates.Canonical(v1.Layout)
	if e != nil {
		t.Fatal(e)
	}
	renderer := &fakeRenderer{out: []byte("%PDF-1.7 issued"), pages: 1}
	issuer, e := issuance.New(store, nil, renderer)
	if e != nil {
		t.Fatal(e)
	}
	job := r.Job{ID: strings.Repeat("7", 32), Owner: owner, Kind: "issue", TemplateID: v1.ID, TemplateVersion: 1, Template: string(frozen), RowIndex: 1}
	// Not published yet: refused before the renderer is asked anything.
	if e = issuer.Check(ctx, job, snapshot()); !errors.Is(e, r.ErrUnpublished) || renderer.checks != 0 {
		t.Fatal("an unpublished template passed the check", e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	// Published: the renderer is asked once, with the values of the row and the table of the
	// snapshot, and nothing is rendered; no stamp and no issue time are needed.
	if e = issuer.Check(ctx, job, snapshot()); e != nil {
		t.Fatal(e)
	}
	if renderer.checks != 1 || renderer.calls != 0 || renderer.checkedValues["n1"] != "乙" || renderer.checkedRows != 2 || renderer.checkedMode != "full" || renderer.checkedBg != 0 {
		t.Fatalf("%+v", renderer)
	}
	// It refuses what Issue refuses for the same reasons, with the same errors, and neither asks the
	// renderer for anything: frozen bytes that are not the stored version, another version, a row the
	// snapshot lacks, another dataset, a job that is not an issue.
	req := r.IssueRequest{Job: job, Snapshot: snapshot(), Stamp: r.IssueStamp("AR-000004", issuedAt, job.ID), At: issuedAt}
	for name, c := range map[string]struct {
		change func(*r.Job, r.Snapshot) r.Snapshot
		want   error
	}{
		"other bytes":                   {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Template = `{"x":1}`; return s }, r.ErrIntegrity},
		"other version":                 {func(j *r.Job, s r.Snapshot) r.Snapshot { j.TemplateVersion = 2; return s }, r.ErrUnpublished},
		"other row":                     {func(j *r.Job, s r.Snapshot) r.Snapshot { j.RowIndex = 2; return s }, r.ErrInvalid},
		"other dataset":                 {func(j *r.Job, s r.Snapshot) r.Snapshot { s.Query.Dataset.Version = "2"; return s }, r.ErrDenied},
		"export job":                    {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Kind = "export"; return s }, r.ErrInvalid},
		"no template":                   {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Template = ""; return s }, r.ErrInvalid},
		"equal but not canonical bytes": {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Template = notCanonical(t, j.Template); return s }, r.ErrIntegrity},
	} {
		j := job
		s := c.change(&j, snapshot())
		checks, calls := renderer.checks, renderer.calls
		checked := issuer.Check(ctx, j, s)
		_, issued := issuer.Issue(ctx, r.IssueRequest{Job: j, Snapshot: s, Stamp: req.Stamp, At: req.At})
		if !errors.Is(checked, c.want) || !errors.Is(issued, c.want) || renderer.checks != checks || renderer.calls != calls {
			t.Errorf("%s: check %v, issue %v; want both %v and no call to the renderer", name, checked, issued, c.want)
		}
	}
	// The renderer's layout refuses: its error is the check's, and nothing is rendered.
	renderer.checkErr = r.ErrUnsupported
	if e = issuer.Check(ctx, job, snapshot()); !errors.Is(e, r.ErrUnsupported) || renderer.calls != 0 {
		t.Fatal("the layout's refusal was not passed on", e)
	}
	renderer.checkErr = nil
	// A context that has ended is not asked of the renderer.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	checks := renderer.checks
	if e = issuer.Check(cancelled, job, snapshot()); !errors.Is(e, context.Canceled) || renderer.checks != checks {
		t.Fatal(e)
	}
	// The check passes, then the template is withdrawn: Issue still refuses it, and renders nothing.
	if e = issuer.Check(ctx, job, snapshot()); e != nil {
		t.Fatal(e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "withdraw", 0, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = issuer.Issue(ctx, req); !errors.Is(e, r.ErrUnpublished) || renderer.calls != 0 {
		t.Fatal("a template withdrawn after the check was rendered", e)
	}
	if e = issuer.Check(ctx, job, snapshot()); !errors.Is(e, r.ErrUnpublished) {
		t.Fatal("a withdrawn template passed the check", e)
	}
}

// TestIssuerResolvesTheBackgroundAsThePreviewDoes: a published template naming a background
// issues with that PNG in full mode and without it in variables mode; on a deployment without
// a background store it is unsupported.
func TestIssuerResolvesTheBackgroundAsThePreviewDoes(t *testing.T) {
	ctx := context.Background()
	owner := snapshot().Owner
	png, e := os.ReadFile("../doctemplates/testdata/a4-background.png")
	if e != nil {
		t.Fatal(e)
	}
	assets, e := doctemplates.OpenAssets(filepath.Join(t.TempDir(), "backgrounds"))
	if e != nil {
		t.Fatal(e)
	}
	defer assets.Close()
	ref, e := assets.Put(ctx, owner.Identity, document().Binding, png)
	if e != nil {
		t.Fatal(e)
	}
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	for _, mode := range []string{"full", "variables"} {
		d := document()
		d.Print.Mode = mode
		d.Background = &ref
		v, e := store.Save(owner.Identity, "", 0, d)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.Publish(owner.Identity, v.ID, "publish", 1, 0); e != nil {
			t.Fatal(e)
		}
		frozen, _ := doctemplates.Canonical(v.Layout)
		job := r.Job{ID: strings.Repeat("8", 32), Owner: owner, Kind: "issue", TemplateID: v.ID, TemplateVersion: 1, Template: string(frozen), RowIndex: 0}
		req := r.IssueRequest{Job: job, Snapshot: snapshot(), Stamp: r.IssueStamp("AR-000001", issuedAt, job.ID), At: issuedAt}
		renderer := &fakeRenderer{out: []byte("%PDF-1.7 issued"), pages: 1}
		without, _ := issuance.New(store, nil, renderer)
		if _, e := without.Issue(ctx, req); !errors.Is(e, r.ErrUnsupported) || renderer.calls != 0 {
			t.Fatalf("%s without a background store: %v", mode, e)
		}
		if e := without.Check(ctx, job, snapshot()); !errors.Is(e, r.ErrUnsupported) || renderer.checks != 0 {
			t.Fatalf("%s without a background store, checked: %v", mode, e)
		}
		with, _ := issuance.New(store, assets, renderer)
		if _, e := with.Issue(ctx, req); e != nil || renderer.calls != 1 || renderer.mode != mode || !renderer.at.Equal(issuedAt) {
			t.Fatalf("%s: %v %+v", mode, e, renderer)
		}
		if (mode == "full") != (renderer.bg > 0) {
			t.Errorf("%s: background of %d bytes passed to the renderer", mode, renderer.bg)
		}
		// The check hands the renderer the same background: the PNG in full mode, none in variables mode.
		if e := with.Check(ctx, job, snapshot()); e != nil || renderer.checks != 1 || renderer.checkedMode != mode || (mode == "full") != (renderer.checkedBg > 0) || renderer.calls != 1 {
			t.Fatalf("%s checked: %v %+v", mode, e, renderer)
		}
	}
}

// A 2.0 template is issued by row group (S03-K): the job freezes the group, and the issuer binds the document from that
// group of the verified snapshot, never from the request: the values of the group's fields, the group's rows as the
// table, the template's own layout. The check binds the same document, and asks the renderer's layout the same
// question before the product is asked.
func TestIssuerIssuesA2xTemplateByGroup(t *testing.T) {
	ctx := context.Background()
	owner := groupSnapshot().Owner
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	v1, e := store.Save(owner.Identity, "", 0, bandedDocument("customer"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	frozen, e := doctemplates.Canonical(v1.Layout)
	if e != nil {
		t.Fatal(e)
	}
	renderer := &fakeRenderer{out: []byte("%PDF-1.7 issued"), pages: 2}
	issuer, _ := issuance.New(store, nil, renderer)
	snap := groupSnapshot()
	group, e := r.NewIssueGroup(snap, []string{"customer"}, []string{"乙"}, []int{1, 3})
	if e != nil {
		t.Fatal(e)
	}
	job := r.Job{ID: strings.Repeat("7", 32), Owner: owner, Kind: "issue", TemplateID: v1.ID, TemplateVersion: 1, Template: string(frozen), Group: &group}
	req := r.IssueRequest{Job: job, Snapshot: snap, Stamp: r.IssueStamp("AR-000004", issuedAt, job.ID), At: issuedAt}
	if e := issuer.Check(ctx, job, snap); e != nil || renderer.checks != 1 || renderer.calls != 0 {
		t.Fatalf("check: %v (%d checks, %d renders)", e, renderer.checks, renderer.calls)
	}
	// What the check was asked, and what the render is: the group's rows as the table, its key as the field's value.
	if renderer.checkedRows != 2 || renderer.checkedBg != 0 || renderer.checkedValues["e1"] != "乙" {
		t.Fatalf("the check was asked %+v with %d rows", renderer.checkedValues, renderer.checkedRows)
	}
	out, e := issuer.Issue(ctx, req)
	if e != nil || renderer.calls != 1 || string(out.PDF) != "%PDF-1.7 issued" || out.Pages != 2 || !renderer.at.Equal(issuedAt) || renderer.stamp != req.Stamp {
		t.Fatalf("issue: %v %+v", e, renderer)
	}
	want := [][]string{{"乙", "2.50"}, {"乙", "4.00"}}
	if renderer.rows != 2 || len(renderer.table) != 2 || renderer.table[0][1] != want[0][1] || renderer.table[1][1] != want[1][1] || renderer.vals["e1"] != "乙" || renderer.vals["e2"] != "6.50" {
		t.Fatalf("the rendered group: values %+v, rows %v", renderer.vals, renderer.table)
	}
	// The same job and snapshot bind the same document again.
	if _, e = issuer.Issue(ctx, req); e != nil || renderer.calls != 2 || renderer.rows != 2 {
		t.Fatal(e)
	}
	// The group's key is not a request value: the whole snapshot of a template without a group has no key at all.
	whole := bandedDocument()
	w1, e := store.Save(owner.Identity, "", 0, whole)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Publish(owner.Identity, w1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	wholeFrozen, _ := doctemplates.Canonical(w1.Layout)
	all, e := r.NewIssueGroup(snap, nil, nil, []int{0, 1, 2, 3})
	if e != nil {
		t.Fatal(e)
	}
	wholeJob := r.Job{ID: strings.Repeat("8", 32), Owner: owner, Kind: "issue", TemplateID: w1.ID, TemplateVersion: 1, Template: string(wholeFrozen), Group: &all}
	if e := issuer.Check(ctx, wholeJob, snap); e != nil || renderer.checkedRows != 4 {
		t.Fatalf("check of the whole snapshot: %v, %d rows", e, renderer.checkedRows)
	}
	// What the issuer refuses, before the renderer is asked: a group that is not the job's template's (other fields),
	// not whole, not the snapshot's rows, or no group at all for a 2.0 template; a group for a 1.x template.
	checks, calls := renderer.checks, renderer.calls
	for name, c := range map[string]struct {
		change func(j *r.Job, s r.Snapshot) r.Snapshot
		want   error
	}{
		"no group for a 2.0 template": {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Group = nil; return s }, r.ErrUnsupported},
		"fields other than the template's": {func(j *r.Job, s r.Snapshot) r.Snapshot {
			g := j.Group.Clone()
			g.Fields = []string{"amount"}
			j.Group = &g
			return s
		}, r.ErrIntegrity},
		"no fields for a template with a group": {func(j *r.Job, s r.Snapshot) r.Snapshot {
			g := j.Group.Clone()
			g.Fields, g.Key = []string{}, []string{}
			j.Group = &g
			return s
		}, r.ErrIntegrity},
		"a group that leaves a row out": {func(j *r.Job, s r.Snapshot) r.Snapshot {
			g := j.Group.Clone()
			g.Rows = g.Rows[:1]
			j.Group = &g
			return s
		}, r.ErrIntegrity},
		"a digest of other rows": {func(j *r.Job, s r.Snapshot) r.Snapshot {
			g := j.Group.Clone()
			g.RowsSHA256 = strings.Repeat("0", 64)
			j.Group = &g
			return s
		}, r.ErrIntegrity},
		"a snapshot whose rows changed": {func(j *r.Job, s r.Snapshot) r.Snapshot {
			s.Result.Rows = append([][]string{}, s.Result.Rows...)
			s.Result.Rows[3] = []string{"乙", "9.99"}
			return s
		}, r.ErrIntegrity},
		"another key": {func(j *r.Job, s r.Snapshot) r.Snapshot {
			g := j.Group.Clone()
			g.Key = []string{"甲"}
			j.Group = &g
			return s
		}, r.ErrIntegrity},
		"a template that is not the published one": {func(j *r.Job, s r.Snapshot) r.Snapshot { j.TemplateVersion = 2; return s }, r.ErrUnpublished},
	} {
		j, s := job, groupSnapshot()
		s = c.change(&j, s)
		if e := issuer.Check(ctx, j, s); !errors.Is(e, c.want) {
			t.Errorf("check, %s: %v; want %v", name, e, c.want)
		}
		if _, e := issuer.Issue(ctx, r.IssueRequest{Job: j, Snapshot: s, Stamp: req.Stamp, At: issuedAt}); !errors.Is(e, c.want) {
			t.Errorf("issue, %s: %v; want %v", name, e, c.want)
		}
	}
	if renderer.checks != checks || renderer.calls != calls {
		t.Errorf("the renderer was asked for a group the issuer refused: %d checks (was %d), %d renders (was %d)", renderer.checks, checks, renderer.calls, calls)
	}
	// A group for a 1.x template has no rows to bind a table from: invalid, whatever the group.
	oneX, e := store.Save(owner.Identity, "", 0, document())
	if e != nil {
		t.Fatal(e)
	}
	oneFrozen, _ := doctemplates.Canonical(oneX.Layout)
	crossed := r.Job{ID: strings.Repeat("6", 32), Owner: owner, Kind: "issue", TemplateID: oneX.ID, TemplateVersion: 1, Template: string(oneFrozen), Group: &group}
	if e := issuer.Check(ctx, crossed, snap); !errors.Is(e, r.ErrInvalid) || renderer.checks != checks {
		t.Errorf("check of a 1.x job with a group: %v", e)
	}
	if _, e := issuer.Issue(ctx, r.IssueRequest{Job: crossed, Snapshot: snap, Stamp: req.Stamp, At: issuedAt}); !errors.Is(e, r.ErrInvalid) || renderer.calls != calls {
		t.Errorf("issue of a 1.x job with a group: %v", e)
	}
	// The renderer's refusal of the layout is the group's refusal, as for a row: the check and the issue pass it on.
	renderer.checkErr = r.ErrUnsupported
	if e := issuer.Check(ctx, job, snap); !errors.Is(e, r.ErrUnsupported) {
		t.Errorf("the renderer's refusal: %v", e)
	}
}

// A group's own refusal of the binding names no value: a field that differs inside a group is INCONSISTENT_GROUP with
// the place, and the error is the same for the check and the issue.
func TestIssuerRefusesAGroupWhoseFieldDiffersWithoutNamingTheValue(t *testing.T) {
	ctx := context.Background()
	snap := groupSnapshot()
	owner := snap.Owner
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	// The page header prints the amount, which differs between the rows of a customer.
	d := bandedDocument("customer")
	d.Bands.PageHeader.Elements[0].Field = "amount"
	v1, e := store.Save(owner.Identity, "", 0, d)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	frozen, _ := doctemplates.Canonical(v1.Layout)
	group, e := r.NewIssueGroup(snap, []string{"customer"}, []string{"乙"}, []int{1, 3})
	if e != nil {
		t.Fatal(e)
	}
	renderer := &fakeRenderer{out: []byte("%PDF-1.7 issued"), pages: 1}
	issuer, _ := issuance.New(store, nil, renderer)
	job := r.Job{ID: strings.Repeat("7", 32), Owner: owner, Kind: "issue", TemplateID: v1.ID, TemplateVersion: 1, Template: string(frozen), Group: &group}
	e = issuer.Check(ctx, job, snap)
	detail, found := doctemplates.DetailOf(e)
	if !errors.Is(e, r.ErrUnsupported) || !found || detail.Reason != doctemplates.ReasonInconsistentGroup || detail.Band != doctemplates.BandPageHeader || detail.Element != "e1" {
		t.Fatalf("%v %+v", e, detail)
	}
	if strings.Contains(e.Error(), "2.50") || strings.Contains(e.Error(), "4.00") || strings.Contains(e.Error(), "乙") || renderer.checks != 0 {
		t.Fatalf("the refusal names a value or asked the renderer: %v", e)
	}
}

// A job that names a 2.0 template but no group could only be bound by row, which a 2.0 template has none of: the check
// and the issue refuse it before the renderer is asked or anything is bound.
func TestIssuerRefusesA2xJobWithoutAGroup(t *testing.T) {
	ctx := context.Background()
	owner := snapshot().Owner
	store, e := doctemplates.Open(filepath.Join(t.TempDir(), "templates"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	banded := doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Bands: &doctemplates.Bands{Detail: &doctemplates.DetailBand{Elements: []doctemplates.Item{{ID: "e1", Type: "table", X: 0, Width: 100, FontSize: 10, Columns: []doctemplates.ItemColumn{{Field: "customer", Width: 50}, {Field: "amount", Width: 50}}}}}}}
	v1, e := store.Save(owner.Identity, "", 0, banded)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	frozen, e := doctemplates.Canonical(v1.Layout)
	if e != nil {
		t.Fatal(e)
	}
	renderer := &fakeRenderer{out: []byte("%PDF-1.7 issued"), pages: 1}
	issuer, _ := issuance.New(store, nil, renderer)
	job := r.Job{ID: strings.Repeat("7", 32), Owner: owner, Kind: "issue", TemplateID: v1.ID, TemplateVersion: 1, Template: string(frozen), RowIndex: 0}
	if e := issuer.Check(ctx, job, snapshot()); !errors.Is(e, r.ErrUnsupported) || renderer.checks != 0 {
		t.Errorf("check of a 2.0 job without a group: %v (%d checks)", e, renderer.checks)
	}
	req := r.IssueRequest{Job: job, Snapshot: snapshot(), Stamp: r.IssueStamp("AR-000004", issuedAt, job.ID), At: issuedAt}
	if _, e := issuer.Issue(ctx, req); !errors.Is(e, r.ErrUnsupported) || renderer.calls != 0 {
		t.Errorf("issue of a 2.0 job without a group: %v (%d renders)", e, renderer.calls)
	}
}

// groupSnapshot is four lines of two customers, interleaved: 甲 is rows 0 and 2, 乙 rows 1 and 3.
func groupSnapshot() r.Snapshot {
	return r.Snapshot{ID: strings.Repeat("5", 32), Owner: r.Owner{Identity: contractsim.DemoPrincipal().Identity, Integration: "i"},
		Query: r.QuerySpec{Dataset: r.Ref{ID: "d", Version: "1"}, Dimensions: []string{"customer"}, Metrics: []r.Ref{{ID: "amount", Version: "1"}}},
		Result: r.Result{Columns: []r.Column{{ID: "customer", Label: "客户", Type: "string"}, {ID: "amount", Label: "金额", Type: "decimal", Scale: 2}},
			Rows: [][]string{{"甲", "1.00"}, {"乙", "2.50"}, {"甲", "3.00"}, {"乙", "4.00"}}, Totals: map[string]string{"amount": "10.50"}, Completeness: "complete"}}
}

// bandedDocument is a 2.0 template over groupSnapshot: the customer in the page header, the group's lines in the
// table and the sum of its amounts in the document footer; grouped by the given fields, or the whole snapshot without.
func bandedDocument(groupFields ...string) doctemplates.Document {
	d := doctemplates.Document{Schema: "2.0", Kind: "document", Binding: doctemplates.Binding{Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}},
		Bands: &doctemplates.Bands{
			PageHeader:     &doctemplates.Band{Height: 15, Elements: []doctemplates.Item{{ID: "e1", Type: "text", Source: "field", Field: "customer", X: 0, Y: 2, Width: 100, Height: 8, FontSize: 12, Align: "left"}}},
			Detail:         &doctemplates.DetailBand{Elements: []doctemplates.Item{{ID: "e3", Type: "table", X: 0, Width: 100, FontSize: 10, Columns: []doctemplates.ItemColumn{{Field: "customer", Width: 50}, {Field: "amount", Width: 50}}}}},
			DocumentFooter: &doctemplates.Band{Height: 10, Elements: []doctemplates.Item{{ID: "e2", Type: "text", Source: "group_sum", Field: "amount", X: 0, Y: 1, Width: 60, Height: 8, FontSize: 10, Align: "right"}}}}}
	if len(groupFields) > 0 {
		d.Group = &doctemplates.GroupSpec{Fields: groupFields}
	} else {
		// Without a group the customers differ inside the document: no field may be printed from the rows.
		d.Bands.PageHeader.Elements[0] = doctemplates.Item{ID: "e1", Type: "text", Source: "literal", Text: "Statement", X: 0, Y: 2, Width: 100, Height: 8, FontSize: 12, Align: "left"}
	}
	return d
}
