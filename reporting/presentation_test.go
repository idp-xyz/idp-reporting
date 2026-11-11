package reporting_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
)

// financialPresentation is a presentation of the demo's receivables dataset: the bucket first under its own label, the customer, the open
// balance in parentheses, subtotals by bucket, a mark on negative balances.
func financialPresentation(id string, version int) r.Presentation {
	parens := r.NumberFormat{Grouping: true, Negative: r.NegativeParentheses, Zero: r.ZeroDigits}
	return r.Presentation{
		LayoutID: id, LayoutVersion: version, Title: "应收账龄", Footnote: "按账龄区间小计。",
		Columns:    []r.PresentedColumn{{ID: "aging.bucket", Label: "账龄", Align: r.AlignCenter}, {ID: "customer.id", Align: r.AlignLeft}, {ID: "ar.open_balance", Align: r.AlignRight, Format: &parens}},
		SubtotalBy: "aging.bucket",
		Marks:      []r.Mark{{Column: "ar.open_balance", When: r.WhenLess, Value: "0", Mark: r.MarkBad}},
	}
}

const layoutA = "0123456789abcdef0123456789abcdef"

func TestPresentationValidateHoldsTheShape(t *testing.T) {
	valid := financialPresentation(layoutA, 2)
	if e := valid.Validate(); e != nil {
		t.Fatal(e)
	}
	if e := (r.Presentation{Columns: []r.PresentedColumn{{ID: "a", Align: r.AlignLeft}}}).Validate(); e != nil {
		t.Fatal("a presentation without a layout identity is a shape of its own:", e)
	}
	long := strings.Repeat("长", 121)
	f := r.DefaultNumberFormat
	for name, change := range map[string]func(*r.Presentation){
		"half a layout identity":   func(p *r.Presentation) { p.LayoutVersion = 0 },
		"a version without an id":  func(p *r.Presentation) { p.LayoutID = "" },
		"a malformed layout id":    func(p *r.Presentation) { p.LayoutID = "ABC" },
		"a title too long":         func(p *r.Presentation) { p.Title = long },
		"a control character":      func(p *r.Presentation) { p.Title = "a\tb" },
		"a footnote too long":      func(p *r.Presentation) { p.Footnote = strings.Repeat("注", 401) },
		"no column":                func(p *r.Presentation) { p.Columns = nil },
		"too many columns":         func(p *r.Presentation) { p.Columns = make([]r.PresentedColumn, 101) },
		"a duplicate column":       func(p *r.Presentation) { p.Columns = append(p.Columns, p.Columns[0]) },
		"an unknown alignment":     func(p *r.Presentation) { p.Columns[0].Align = "justify" },
		"an empty alignment":       func(p *r.Presentation) { p.Columns[0].Align = "" },
		"a label too long":         func(p *r.Presentation) { p.Columns[0].Label = strings.Repeat("标", 65) },
		"an unknown negative form": func(p *r.Presentation) { f := f; f.Negative = "red"; p.Columns[2].Format = &f },
		"an unknown zero form":     func(p *r.Presentation) { f := f; f.Zero = "blank"; p.Columns[2].Format = &f },
		"a subtotal column not shown": func(p *r.Presentation) {
			p.SubtotalBy = "nope"
		},
		"too many marks": func(p *r.Presentation) {
			for range 8 {
				p.Marks = append(p.Marks, p.Marks[0])
			}
		},
		"a mark on a column not shown": func(p *r.Presentation) { p.Marks[0].Column = "nope" },
		"an unknown relation":          func(p *r.Presentation) { p.Marks[0].When = "between" },
		"an unknown mark":              func(p *r.Presentation) { p.Marks[0].Mark = "red" },
		"blank with a value":           func(p *r.Presentation) { p.Marks[0].When, p.Marks[0].Value = r.WhenBlank, "x" },
		"an ordered relation without a number": func(p *r.Presentation) {
			p.Marks[0].Value = "1,000"
		},
		"a value too long": func(p *r.Presentation) { p.Marks[0].When, p.Marks[0].Value = r.WhenEqual, strings.Repeat("v", 129) },
	} {
		p := financialPresentation(layoutA, 2).Clone()
		change(&p)
		if e := p.Validate(); !errors.Is(e, r.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, e)
		} else if strings.Contains(e.Error(), "nope") || strings.Contains(e.Error(), "1,000") {
			t.Errorf("%s: the error carries a value: %v", name, e)
		}
	}
	// Eight marks are the bound, not seven.
	p := financialPresentation(layoutA, 2)
	for len(p.Marks) < 8 {
		p.Marks = append(p.Marks, p.Marks[0])
	}
	if e := p.Validate(); e != nil {
		t.Error("eight marks refused:", e)
	}
}

func TestPresentationFitsTheSnapshotOrIsRefusedWhole(t *testing.T) {
	cols := []r.Column{{ID: "customer.id", Type: "string"}, {ID: "aging.bucket", Type: "string"}, {ID: "ar.open_balance", Type: "decimal", Scale: 2}}
	dims := []string{"customer.id", "aging.bucket"}
	if e := financialPresentation(layoutA, 1).Fits(cols, dims); e != nil {
		t.Fatal(e)
	}
	f := r.DefaultNumberFormat
	for name, change := range map[string]func(*r.Presentation){
		"a column the snapshot lacks":  func(p *r.Presentation) { p.Columns[1].ID = "ar.other" },
		"a format on a string":         func(p *r.Presentation) { p.Columns[1].Format = &f },
		"a subtotal by a metric":       func(p *r.Presentation) { p.SubtotalBy = "ar.open_balance" },
		"blank on a number":            func(p *r.Presentation) { p.Marks[0].When, p.Marks[0].Value = r.WhenBlank, "" },
		"an order on a string":         func(p *r.Presentation) { p.Marks[0].Column = "customer.id" },
		"a word compared to a number":  func(p *r.Presentation) { p.Marks[0].When, p.Marks[0].Value = r.WhenEqual, "none" },
		"a mark on a column not there": func(p *r.Presentation) { p.Marks[0].Column = "ar.other"; p.Columns[2].ID = "ar.other" },
	} {
		p := financialPresentation(layoutA, 1).Clone()
		change(&p)
		if e := p.Fits(cols, dims); !errors.Is(e, r.ErrUnsupported) {
			t.Errorf("%s: %v, want ErrUnsupported", name, e)
		}
	}
	// A string column may be compared for equality and blankness.
	p := financialPresentation(layoutA, 1)
	p.Marks = []r.Mark{{Column: "customer.id", When: r.WhenBlank, Mark: r.MarkNote}, {Column: "customer.id", When: r.WhenNotEqual, Value: "x", Mark: r.MarkWarn}}
	if e := p.Fits(cols, dims); e != nil {
		t.Error(e)
	}
}

func TestPresentationCloneSharesNothing(t *testing.T) {
	p := financialPresentation(layoutA, 1)
	c := p.Clone()
	c.Columns[2].Format.Negative = r.NegativeMinus
	c.Columns[0].Label = "x"
	c.Marks[0].Mark = r.MarkGood
	if p.Columns[2].Format.Negative != r.NegativeParentheses || p.Columns[0].Label != "账龄" || p.Marks[0].Mark != r.MarkBad {
		t.Error("the clone shares memory with its origin")
	}
}

func TestExportRequestHashBindsTheLayoutVersion(t *testing.T) {
	plain := r.ExportRequestHash("snap", "pdf")
	if r.ExportRequestHashWithLayout("snap", "pdf", "", 0) != plain || r.ExportRequestHashWithLayout("snap", "", "", 0) != r.Digest("snap") {
		t.Error("without a layout the hash must be the one exports always had")
	}
	v1, v2 := r.ExportRequestHashWithLayout("snap", "pdf", layoutA, 1), r.ExportRequestHashWithLayout("snap", "pdf", layoutA, 2)
	other := r.ExportRequestHashWithLayout("snap", "pdf", "fedcba9876543210fedcba9876543210", 1)
	if v1 == plain || v1 == v2 || v1 == other || v1 != r.ExportRequestHashWithLayout("snap", "pdf", layoutA, 1) {
		t.Error("the hash does not bind the layout id and version")
	}
}

// presentedWriter is a stand-in format that records what it was asked to render.
type presentedWriter struct {
	presented []r.Presentation
	plain     int
}

func (w *presentedWriter) Write(context.Context, r.Snapshot) ([]byte, error) {
	w.plain++
	return []byte("%PDF-1.7 plain"), nil
}

func (w *presentedWriter) WritePresented(_ context.Context, _ r.Snapshot, p r.Presentation) ([]byte, error) {
	w.presented = append(w.presented, p)
	return []byte("%PDF-1.7 presented " + p.Title), nil
}

func TestAPresentedExportFreezesItsPresentationAndRecordsItsLayout(t *testing.T) {
	w := &presentedWriter{}
	s, p, snapshot, repo := formatService(t, map[string]r.WorkbookWriter{"pdf": w, "plain": fakePDF{}})
	ctx := context.Background()
	presentation := financialPresentation(layoutA, 2)
	j, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "pdf", Presentation: &presentation}, "presented")
	if e != nil {
		t.Fatal(e)
	}
	if j.Layout == nil || j.Layout.ID != layoutA || j.Layout.Version != 2 || j.Format != "pdf" {
		t.Fatalf("the view does not name the layout: %+v", j)
	}
	// The job froze a copy: a change to the caller's presentation changes nothing of the job.
	presentation.Title = "changed"
	stored, e := repo.Job(ctx, p.Identity, j.ID)
	if e != nil || stored.Presentation == nil || stored.Presentation.Title != "应收账龄" || stored.Presentation.LayoutID != layoutA || stored.RequestHash != r.ExportRequestHashWithLayout(snapshot, "pdf", layoutA, 2) {
		t.Fatalf("the stored job: %+v (%v)", stored, e)
	}
	if b, _ := json.Marshal(stored); !bytes.Contains(b, []byte(`"Presentation":{`)) || !bytes.Contains(b, []byte(`"SubtotalBy":"aging.bucket"`)) {
		t.Errorf("the presentation is not in the job's payload: %s", b)
	}
	// A replay with the same key and layout version answers the same job; another version, or no layout, is a conflict.
	again, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "pdf", Presentation: &presentation}, "presented")
	if e != nil || again.ID != j.ID {
		t.Fatalf("replay: %+v %v", again, e)
	}
	v3 := financialPresentation(layoutA, 3)
	if _, e = s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "pdf", Presentation: &v3}, "presented"); !errors.Is(e, r.ErrConflict) {
		t.Errorf("another layout version under the key: %v, want conflict", e)
	}
	if _, e = s.SubmitExportFormat(ctx, p, snapshot, "pdf", "presented"); !errors.Is(e, r.ErrConflict) {
		t.Errorf("no layout under the key: %v, want conflict", e)
	}
	// The worker renders through the frozen presentation, and the artifact records the layout version.
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if len(w.presented) != 1 || w.plain != 0 || w.presented[0].Title != "应收账龄" || w.presented[0].LayoutVersion != 2 {
		t.Fatalf("the writer was asked for %d presented and %d plain renders: %+v", len(w.presented), w.plain, w.presented)
	}
	if j, e = s.Job(ctx, p, j.ID); e != nil || j.State != "succeeded" || j.Layout == nil || j.Layout.Version != 2 {
		t.Fatal(j, e)
	}
	a, e := s.Download(ctx, p, j.ArtifactID)
	if e != nil || a.LayoutID != layoutA || a.LayoutVersion != 2 || !bytes.Equal(a.Content, []byte("%PDF-1.7 presented 应收账龄")) {
		t.Fatalf("the artifact: %+v (%v)", a, e)
	}
	listed, e := s.Jobs(ctx, p, r.JobListing{Limit: 10})
	if e != nil || len(listed) == 0 || listed[0].ID != j.ID || listed[0].Layout == nil || listed[0].Layout.ID != layoutA {
		t.Errorf("the listing does not name the layout: %+v (%v)", listed, e)
	}
	// An export without a layout is rendered as it always was, with no layout on the job or the artifact.
	plain, e := s.SubmitExportFormat(ctx, p, snapshot, "pdf", "plain")
	if e != nil || plain.Layout != nil {
		t.Fatal(plain, e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	plain, _ = s.Job(ctx, p, plain.ID)
	if a, e = s.Download(ctx, p, plain.ArtifactID); e != nil || a.LayoutID != "" || a.LayoutVersion != 0 || w.plain != 1 || !bytes.Equal(a.Content, []byte("%PDF-1.7 plain")) {
		t.Fatalf("the plain artifact: %+v (%v)", a, e)
	}
	if b, _ := json.Marshal(a); bytes.Contains(b, []byte("Layout")) {
		t.Errorf("a plain artifact encodes a layout: %s", b)
	}
}

func TestAPresentedExportIsRefusedWhenItCannotBeRendered(t *testing.T) {
	w := &presentedWriter{}
	s, p, snapshot, _ := formatService(t, map[string]r.WorkbookWriter{"pdf": w, "plain": fakePDF{}})
	ctx := context.Background()
	presentation := financialPresentation(layoutA, 1)
	// A format whose writer cannot present.
	if _, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "plain", Presentation: &presentation}, "k1"); !errors.Is(e, r.ErrUnsupported) {
		t.Errorf("a writer that cannot present: %v, want unsupported", e)
	}
	// A presentation that is not whole, or names no layout.
	broken := financialPresentation(layoutA, 1)
	broken.Columns[0].Align = "x"
	if _, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "pdf", Presentation: &broken}, "k2"); !errors.Is(e, r.ErrInvalid) {
		t.Errorf("a broken presentation: %v, want invalid", e)
	}
	anonymous := financialPresentation("", 0)
	if _, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "pdf", Presentation: &anonymous}, "k3"); !errors.Is(e, r.ErrInvalid) {
		t.Errorf("a presentation without its layout: %v, want invalid", e)
	}
	// A presentation that does not fit the snapshot is refused whole, not applied in part.
	elsewhere := financialPresentation(layoutA, 1)
	elsewhere.Columns[1].ID = "ar.other"
	if _, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "pdf", Presentation: &elsewhere}, "k4"); !errors.Is(e, r.ErrUnsupported) {
		t.Errorf("a presentation of other columns: %v, want unsupported", e)
	}
	bySum := financialPresentation(layoutA, 1)
	bySum.SubtotalBy = "ar.open_balance"
	if _, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "pdf", Presentation: &bySum}, "k5"); !errors.Is(e, r.ErrUnsupported) {
		t.Errorf("subtotals by a metric: %v, want unsupported", e)
	}
	if len(w.presented) != 0 {
		t.Error("a refused export reached the writer")
	}
	// The workbook writer presents too: the default format takes a presentation.
	if j, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Presentation: &presentation}, "k6"); e != nil || j.Layout == nil || j.Format != "" {
		t.Errorf("a presented workbook: %+v %v", j, e)
	}
}

func TestAdmissionAndPublicationHoldThePresentation(t *testing.T) {
	w := &presentedWriter{}
	s, p, snapshot, repo := formatService(t, map[string]r.WorkbookWriter{"pdf": w})
	ctx := context.Background()
	presentation := financialPresentation(layoutA, 2)
	if _, e := s.SubmitExportWith(ctx, p, snapshot, r.ExportOptions{Format: "pdf", Presentation: &presentation}, "contract"); e != nil {
		t.Fatal(e)
	}
	claim, e := repo.Claim(ctx, time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	job := claim.Job
	job.State, job.Generation, job.LeaseUntil = "queued", 0, time.Time{}
	if e := r.ValidateAdmission(job); e != nil {
		t.Fatal("a valid presented export job is rejected:", e)
	}
	for name, change := range map[string]func(*r.Job){
		"a hash without the layout":     func(j *r.Job) { j.RequestHash = r.ExportRequestHash(j.SnapshotID, j.Format) },
		"another version than hashed":   func(j *r.Job) { j.Presentation.LayoutVersion = 3 },
		"a presentation without layout": func(j *r.Job) { j.Presentation.LayoutID, j.Presentation.LayoutVersion = "", 0 },
		"a broken presentation":         func(j *r.Job) { j.Presentation.Marks[0].Mark = "red" },
		"a presentation on a snapshot job": func(j *r.Job) {
			j.Kind, j.SnapshotID, j.Format, j.RequestHash = "snapshot", "", "", r.Digest(r.Normalize(j.Query))
		},
	} {
		j := job
		frozen := job.Presentation.Clone()
		j.Presentation = &frozen
		change(&j)
		if r.ValidateAdmission(j) == nil {
			t.Errorf("%s: admitted", name)
		}
		if reduced := r.ReduceJob(j); reduced.Kind == "export" && r.ValidateReduced(reduced) == nil {
			t.Errorf("%s: valid once reduced", name)
		}
	}
	reduced := r.ReduceJob(job)
	if e := r.ValidateReduced(reduced); e != nil || reduced.Presentation == nil || reduced.Presentation.LayoutVersion != 2 {
		t.Errorf("a reduced presented export loses its layout: %+v (%v)", reduced.Presentation, e)
	}
	artifact := func(id string, version int) *r.Artifact {
		return &r.Artifact{ID: r.NewID(), Owner: claim.Job.Owner, SnapshotID: claim.Job.SnapshotID, Format: "pdf", ExpiresAt: claim.Job.Prepared.ExpiresAt, Content: []byte("x"), SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881", LayoutID: id, LayoutVersion: version}
	}
	if e := r.ValidatePublication(claim.Job, r.Publication{Artifact: artifact(layoutA, 2)}); e != nil {
		t.Fatalf("the matching artifact is not publishable: %v", e)
	}
	for name, a := range map[string]*r.Artifact{"no layout": artifact("", 0), "another version": artifact(layoutA, 1), "another layout": artifact("fedcba9876543210fedcba9876543210", 2)} {
		if r.ValidatePublication(claim.Job, r.Publication{Artifact: a}) == nil {
			t.Errorf("%s: an artifact that does not record the job's layout is publishable", name)
		}
	}
	plainJob := claim.Job
	plainJob.Presentation, plainJob.RequestHash = nil, r.ExportRequestHash(plainJob.SnapshotID, "pdf")
	if r.ValidatePublication(plainJob, r.Publication{Artifact: artifact(layoutA, 2)}) == nil {
		t.Error("an artifact that records a layout is publishable for a job without one")
	}
}
