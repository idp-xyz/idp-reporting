package issuance_test

import (
	"context"
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
	f.stamp, f.at, f.bg = stamp, at, len(background)
	f.mode = "full"
	if d.Print != nil {
		f.mode = d.Print.Mode
	}
	if table != nil {
		f.rows = len(table.Rows)
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
		"other bytes":   {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Template = `{"x":1}`; return s }, r.ErrIntegrity},
		"other version": {func(j *r.Job, s r.Snapshot) r.Snapshot { j.TemplateVersion = 2; return s }, r.ErrUnpublished},
		"other row":     {func(j *r.Job, s r.Snapshot) r.Snapshot { j.RowIndex = 2; return s }, r.ErrInvalid},
		"other dataset": {func(j *r.Job, s r.Snapshot) r.Snapshot { s.Query.Dataset.Version = "2"; return s }, r.ErrDenied},
		"export job":    {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Kind = "export"; return s }, r.ErrInvalid},
		"no template":   {func(j *r.Job, s r.Snapshot) r.Snapshot { j.Template = ""; return s }, r.ErrInvalid},
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
