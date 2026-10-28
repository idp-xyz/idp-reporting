package reporting_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"strings"
	"testing"
	"time"
)

// fakeIssuer records the request it rendered and answers with a fixed PDF.
type fakeIssuer struct {
	calls   int
	last    r.IssueRequest
	err     error
	pages   int
	content []byte
}

func (f *fakeIssuer) Issue(_ context.Context, req r.IssueRequest) (r.IssueResult, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return r.IssueResult{}, f.err
	}
	content := f.content
	if content == nil {
		content = []byte("%PDF-1.7 issued " + req.Stamp)
	}
	pages := f.pages
	if pages == 0 {
		pages = 2
	}
	return r.IssueResult{PDF: content, Pages: pages}, nil
}

const template = `{"schema_version":"1.0","kind":"document","binding":{"integration_id":"i","dataset":{"id":"d","version":"1"}},"elements":[]}`

func issueService(t *testing.T, issuer r.Issuer, now func() time.Time) (*r.Service, r.Principal, string, *memory.Store) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(now)
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}, Issuer: issuer, Now: now})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	j, e := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if j, e = s.Job(ctx, p, j.ID); e != nil || j.SnapshotID == "" {
		t.Fatal(j, e)
	}
	return s, p, j.SnapshotID, repo
}

func TestIssueJobsNumberRenderStampAndRecord(t *testing.T) {
	ctx := context.Background()
	// A clock in a non-UTC zone with a fraction of a second: the stamp is UTC to the second.
	zone := time.FixedZone("CST", 8*3600)
	clock := func() time.Time { return time.Now().In(zone) }
	issuer := &fakeIssuer{}
	s, p, snapshot, repo := issueService(t, issuer, clock)
	tpl := strings.Repeat("a", 32)
	in := r.IssueInput{TemplateID: tpl, TemplateVersion: 3, Template: []byte(template), SnapshotID: snapshot, RowIndex: 1}
	j, e := s.SubmitIssue(ctx, p, in, "first")
	if e != nil {
		t.Fatal(e)
	}
	if j.Kind != "issue" || j.State != "queued" || j.Format != "pdf" || j.Issue == nil || j.Issue.Number != 1 || j.Issue.TemplateID != tpl || j.Issue.TemplateVersion != 3 || j.Issue.RowIndex != 1 || len(j.Issue.TemplateSHA256) != 64 {
		t.Fatalf("admitted issue job: %+v %+v", j, j.Issue)
	}
	// A replay with the same key returns the same job and number; another input is a conflict;
	// the next admission takes the next number.
	again, e := s.SubmitIssue(ctx, p, in, "first")
	if e != nil || again.ID != j.ID || again.Issue.Number != 1 {
		t.Fatal(again, e)
	}
	other := in
	other.RowIndex = 2
	if _, e = s.SubmitIssue(ctx, p, other, "first"); !errors.Is(e, r.ErrConflict) {
		t.Fatal("replayed key with another row admitted", e)
	}
	second, e := s.SubmitIssue(ctx, p, other, "second")
	if e != nil || second.Issue.Number != 2 {
		t.Fatal(second, e)
	}
	// The worker renders with the stamp of the number, the UTC second and the job id, and
	// publishes the artifact and the record together.
	before := time.Now().Truncate(time.Second)
	if ran, e := s.RunOne(ctx); !ran || e != nil {
		t.Fatal(ran, e)
	}
	after := time.Now()
	if issuer.calls != 1 || issuer.last.Job.ID != j.ID || issuer.last.Job.Number != 1 || issuer.last.Snapshot.ID != snapshot || issuer.last.Job.Template != template {
		t.Fatalf("issuer call: %+v", issuer.last)
	}
	rec0, e := repo.Issue(ctx, p.Identity, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if rec0.IssuedAt.Location() != time.UTC || rec0.IssuedAt.Nanosecond() != 0 || rec0.IssuedAt.Before(before) || rec0.IssuedAt.After(after) {
		t.Fatalf("issued at %v, not a UTC second of the render", rec0.IssuedAt)
	}
	wantStamp := "ISSUED No. 000001 - " + rec0.IssuedAt.Format("2006-01-02T15:04:05Z") + " - " + j.ID
	if issuer.last.Stamp != wantStamp || !strings.HasSuffix(wantStamp, "Z - "+j.ID) {
		t.Fatalf("stamp %q, want %q", issuer.last.Stamp, wantStamp)
	}
	done, e := s.Job(ctx, p, j.ID)
	if e != nil || done.State != "succeeded" || done.ArtifactID == "" || done.Issue.Number != 1 {
		t.Fatal(done, e)
	}
	a, e := s.Download(ctx, p, done.ArtifactID)
	if e != nil || a.Format != "pdf" || a.Issue != j.ID || !bytes.Equal(a.Content, []byte("%PDF-1.7 issued "+wantStamp)) {
		t.Fatal(a.Format, a.Issue, e)
	}
	rec, e := repo.Issue(ctx, p.Identity, j.ID)
	if e != nil || rec.Number != 1 || rec.ArtifactID != a.ID || rec.SHA256 != a.SHA256 || rec.Pages != 2 || rec.Stamp != wantStamp || !rec.IssuedAt.Equal(rec0.IssuedAt) || rec.TemplateSHA256 != j.Issue.TemplateSHA256 || rec.RowIndex != 1 || len(rec.SnapshotSHA256) != 64 {
		t.Fatalf("record: %+v %v", rec, e)
	}
	v, e := s.Issue(ctx, p, j.ID)
	if e != nil || v.ID != j.ID || v.State != "succeeded" || v.Number != 1 || v.SHA256 != a.SHA256 || v.Pages != 2 || v.Stamp != wantStamp || v.IssuedAt != rec0.IssuedAt.Format(time.RFC3339) || v.ArtifactID != a.ID {
		t.Fatalf("issue view: %+v %v", v, e)
	}
	// The second job fails: its number is kept, voided, and listed with its error code.
	issuer.err = r.ErrUnpublished
	if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrUnpublished) {
		t.Fatal("unpublished template did not fail the job", e)
	}
	list, e := s.Issues(ctx, p, 10)
	if e != nil || len(list) != 2 || list[0].ID != second.ID || list[0].State != "failed" || list[0].ErrorCode != "TEMPLATE_NOT_PUBLISHED" || list[0].Number != 2 || list[0].SHA256 != "" || list[1].ID != j.ID || list[1].Number != 1 || list[1].SHA256 != a.SHA256 {
		t.Fatalf("listing: %+v %v", list, e)
	}
	if _, e = s.Issues(ctx, p, 0); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("limit 0 accepted")
	}
	if _, e = s.Issues(ctx, p, r.MaxIssueListing+1); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("limit above the maximum accepted")
	}
	// A third admission continues the sequence; an export job is not an issue.
	third, e := s.SubmitIssue(ctx, p, in, "third")
	if e != nil || third.Issue.Number != 3 {
		t.Fatal(third, e)
	}
	ex, e := s.SubmitExportFormat(ctx, p, snapshot, "pdf", "export")
	if e != nil || ex.Issue != nil {
		t.Fatal(ex, e)
	}
	if _, e = s.Issue(ctx, p, ex.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("an export job read as an issue", e)
	}
	// Another identity sees nothing of it.
	stranger := p
	stranger.Subject = "stranger"
	if _, e = s.Issue(ctx, stranger, j.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("record visible to another subject", e)
	}
	if list, e = s.Issues(ctx, stranger, 10); e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
}

func TestIssueAdmissionFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, p, snapshot, _ := issueService(t, &fakeIssuer{}, nil)
	good := r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(template), SnapshotID: snapshot, RowIndex: 0}
	for name, c := range map[string]struct {
		change func(*r.IssueInput)
		key    string
		want   error
	}{
		"row beyond the snapshot":  {func(in *r.IssueInput) { in.RowIndex = 3 }, "k", r.ErrInvalid},
		"negative row":             {func(in *r.IssueInput) { in.RowIndex = -1 }, "k", r.ErrInvalid},
		"template id":              {func(in *r.IssueInput) { in.TemplateID = "x" }, "k", r.ErrInvalid},
		"version 0":                {func(in *r.IssueInput) { in.TemplateVersion = 0 }, "k", r.ErrInvalid},
		"template not JSON":        {func(in *r.IssueInput) { in.Template = []byte("{") }, "k", r.ErrInvalid},
		"template too large":       {func(in *r.IssueInput) { in.Template = []byte(`"` + strings.Repeat("x", r.MaxTemplateBytes) + `"`) }, "k", r.ErrInvalid},
		"unknown snapshot":         {func(in *r.IssueInput) { in.SnapshotID = strings.Repeat("b", 32) }, "k", r.ErrNotFound},
		"bad key":                  {func(*r.IssueInput) {}, "", r.ErrInvalid},
		"unexpected nothing wrong": {func(*r.IssueInput) {}, "k", nil},
	} {
		in := good
		c.change(&in)
		_, e := s.SubmitIssue(ctx, p, in, c.key)
		if !errors.Is(e, c.want) {
			t.Errorf("%s: %v; want %v", name, e, c.want)
		}
	}
	// Without an issuer or without the pdf format, issuing is unsupported, and the worker
	// never publishes a stored issue job.
	none, p2, snap2, _ := issueServiceWithout(t)
	if _, e := none.SubmitIssue(ctx, p2, r.IssueInput{TemplateID: good.TemplateID, TemplateVersion: 1, Template: []byte(template), SnapshotID: snap2}, "k"); !errors.Is(e, r.ErrUnsupported) {
		t.Fatal("issue admitted without an issuer", e)
	}
	// The worker refuses a bad render: no PDF prefix, too many pages, an empty body.
	for name, f := range map[string]*fakeIssuer{"not a pdf": {content: []byte("hello")}, "too many pages": {pages: r.MaxIssuePages + 1}, "empty": {content: []byte{}, pages: 1}} {
		s, p, snapshot, repo := issueService(t, f, nil)
		j, e := s.SubmitIssue(ctx, p, r.IssueInput{TemplateID: good.TemplateID, TemplateVersion: 1, Template: []byte(template), SnapshotID: snapshot}, "k")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); e == nil {
			t.Errorf("%s: published", name)
		}
		if v, e := s.Job(ctx, p, j.ID); e != nil || v.State != "failed" {
			t.Errorf("%s: %+v %v", name, v, e)
		}
		if _, e := repo.Issue(ctx, p.Identity, j.ID); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("%s: a record exists", name)
		}
	}
}

func issueServiceWithout(t *testing.T) (*r.Service, r.Principal, string, *memory.Store) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(nil)
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDF{}}})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	j, e := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if j, e = s.Job(ctx, p, j.ID); e != nil {
		t.Fatal(e)
	}
	return s, p, j.SnapshotID, repo
}

func TestIssueContractRefusesInconsistentJobsAndPublications(t *testing.T) {
	ctx := context.Background()
	s, p, snapshot, repo := issueService(t, &fakeIssuer{}, nil)
	j, e := s.SubmitIssue(ctx, p, r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 2, Template: []byte(template), SnapshotID: snapshot, RowIndex: 2}, "k")
	if e != nil {
		t.Fatal(e)
	}
	stored, e := repo.Job(ctx, p.Identity, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	// The stored job (with its number) is a valid admission; every frozen field is covered by
	// the request hash or the format rules.
	if e := r.ValidateAdmission(stored); e != nil {
		t.Fatalf("the stored issue job is not valid: %v", e)
	}
	for name, change := range map[string]func(*r.Job){
		"version":         func(j *r.Job) { j.TemplateVersion++ },
		"row":             func(j *r.Job) { j.RowIndex++ },
		"template bytes":  func(j *r.Job) { j.Template = `{"schema_version":"1.1"}` },
		"template digest": func(j *r.Job) { j.TemplateSHA256 = strings.Repeat("0", 64) },
		"format":          func(j *r.Job) { j.Format = "" },
		"negative number": func(j *r.Job) { j.Number = -1 },
		"export with number": func(j *r.Job) {
			j.Kind, j.Format, j.RequestHash = "export", "pdf", r.ExportRequestHash(j.SnapshotID, "pdf")
		},
	} {
		v := stored
		change(&v)
		if r.ValidateAdmission(v) == nil {
			t.Errorf("%s: an inconsistent issue job is admitted", name)
		}
	}
	claim, e := repo.Claim(ctx, time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	at := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	content := []byte("%PDF-1.7 x")
	artifact := r.Artifact{ID: r.NewID(), Owner: stored.Owner, SnapshotID: stored.SnapshotID, Format: "pdf", ExpiresAt: stored.Prepared.ExpiresAt, Content: content, SHA256: "3a9d9ec8b6f5a2fd3d2b22a8a7cc9f9d4f4e4b8f4b3d7c3f9a3f0f3c84c5e3d1", Issue: stored.ID}
	artifact.SHA256 = digestOf(content)
	rec := r.Issue{ID: stored.ID, Owner: stored.Owner, Number: stored.Number, TemplateID: stored.TemplateID, TemplateVersion: stored.TemplateVersion, TemplateSHA256: stored.TemplateSHA256, SnapshotID: stored.SnapshotID, SnapshotSHA256: strings.Repeat("c", 64), RowIndex: stored.RowIndex, ArtifactID: artifact.ID, SHA256: artifact.SHA256, Pages: 1, IssuedAt: at}
	rec.Stamp = r.IssueStamp(rec.Number, at, rec.ID)
	if e := r.ValidatePublication(claim.Job, r.Publication{Artifact: &artifact, Issue: &rec}); e != nil {
		t.Fatalf("a consistent issue publication is refused: %v", e)
	}
	for name, change := range map[string]func(a *r.Artifact, i *r.Issue){
		"no record":              func(a *r.Artifact, i *r.Issue) { *i = r.Issue{} },
		"artifact without issue": func(a *r.Artifact, i *r.Issue) { a.Issue = "" },
		"record of another job":  func(a *r.Artifact, i *r.Issue) { i.ID = r.NewID() },
		"number":                 func(a *r.Artifact, i *r.Issue) { i.Number++ },
		"digest":                 func(a *r.Artifact, i *r.Issue) { i.SHA256 = strings.Repeat("0", 64) },
		"artifact id":            func(a *r.Artifact, i *r.Issue) { i.ArtifactID = r.NewID() },
		"pages 0":                func(a *r.Artifact, i *r.Issue) { i.Pages = 0 },
		"pages 101":              func(a *r.Artifact, i *r.Issue) { i.Pages = r.MaxIssuePages + 1 },
		"stamp":                  func(a *r.Artifact, i *r.Issue) { i.Stamp = r.IssueStamp(i.Number+1, at, i.ID) },
		"time with a fraction":   func(a *r.Artifact, i *r.Issue) { i.IssuedAt = at.Add(time.Millisecond) },
		"no time":                func(a *r.Artifact, i *r.Issue) { i.IssuedAt = time.Time{} },
		"snapshot digest":        func(a *r.Artifact, i *r.Issue) { i.SnapshotSHA256 = "" },
		"row":                    func(a *r.Artifact, i *r.Issue) { i.RowIndex++ },
		"template version":       func(a *r.Artifact, i *r.Issue) { i.TemplateVersion++ },
		"format":                 func(a *r.Artifact, i *r.Issue) { a.Format = "" },
	} {
		a, i := artifact, rec
		change(&a, &i)
		pub := r.Publication{Artifact: &a, Issue: &i}
		if name == "no record" {
			pub.Issue = nil
		}
		if r.ValidatePublication(claim.Job, pub) == nil {
			t.Errorf("%s: an inconsistent issue publication is accepted", name)
		}
	}
}

func digestOf(b []byte) string { return bytesDigestHex(b) }

func bytesDigestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
