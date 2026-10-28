package issuance_test

import (
	"context"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/issuance"
	r "idp.local/reporting/reporting"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRenderer struct {
	calls int
	stamp string
	mode  string
	bg    int
	rows  int
	out   []byte
	pages int
	err   error
}

func (f *fakeRenderer) Issue(_ context.Context, d doctemplates.Document, values map[string]string, background []byte, table *doctemplates.TableData, stamp string) ([]byte, int, error) {
	f.calls++
	f.stamp, f.bg = stamp, len(background)
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
	job := r.Job{ID: strings.Repeat("7", 32), Owner: owner, Kind: "issue", TemplateID: v1.ID, TemplateVersion: 1, Template: string(frozen), RowIndex: 1, Number: 4}
	req := r.IssueRequest{Job: job, Snapshot: snapshot(), Stamp: "ISSUED No. 000004 - 2026-10-28T00:00:00Z - " + job.ID}
	// Not published yet.
	if _, e = issuer.Issue(ctx, req); !errors.Is(e, r.ErrUnpublished) || renderer.calls != 0 {
		t.Fatal("unpublished template rendered", e)
	}
	if _, e = store.Publish(owner.Identity, v1.ID, "publish", 1, 0); e != nil {
		t.Fatal(e)
	}
	out, e := issuer.Issue(ctx, req)
	if e != nil || string(out.PDF) != "%PDF-1.7 issued" || out.Pages != 1 || renderer.calls != 1 || renderer.stamp != req.Stamp || renderer.rows != 2 || renderer.mode != "full" || renderer.bg != 0 {
		t.Fatalf("%+v %v %+v", out, e, renderer)
	}
	// Frozen bytes that are not the stored version, a wrong version number, a withdrawn and a
	// superseded publication all refuse; so does a row the snapshot lacks.
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
	} {
		j := job
		s := c.change(&j, snapshot())
		q := r.IssueRequest{Job: j, Snapshot: s, Stamp: req.Stamp}
		if name == "empty stamp" {
			q.Stamp = ""
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
	if _, e = issuer.Issue(ctx, r.IssueRequest{Job: jb, Snapshot: snapshot(), Stamp: req.Stamp}); !errors.Is(e, r.ErrUnpublished) {
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
	_ = time.Now
}
