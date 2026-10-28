package reporting_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"slices"
	"testing"
	"time"
)

type fakePDF struct{}

func (fakePDF) Write(context.Context, r.Snapshot) ([]byte, error) {
	return []byte("%PDF-1.7 fake"), nil
}

func formatService(t *testing.T, formats map[string]r.WorkbookWriter) (*r.Service, r.Principal, string, r.Repository) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(time.Now)
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: formats})
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

func TestExportFormatsAreRecordedAndServed(t *testing.T) {
	s, p, snapshot, _ := formatService(t, map[string]r.WorkbookWriter{"pdf": fakePDF{}})
	ctx := context.Background()
	if got := s.Formats(); !slices.Equal(got, []string{"xlsx", "pdf"}) {
		t.Fatalf("Formats() = %v", got)
	}
	j, e := s.SubmitExportFormat(ctx, p, snapshot, "pdf", "pdf-1")
	if e != nil || j.Format != "pdf" {
		t.Fatal(j, e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if j, e = s.Job(ctx, p, j.ID); e != nil || j.State != "succeeded" || j.Format != "pdf" {
		t.Fatal(j, e)
	}
	a, e := s.Download(ctx, p, j.ArtifactID)
	if e != nil || a.Format != "pdf" || !bytes.Equal(a.Content, []byte("%PDF-1.7 fake")) {
		t.Fatal(a.Format, string(a.Content), e)
	}
	// "xlsx" and the empty format are the same workbook export.
	x, e := s.SubmitExportFormat(ctx, p, snapshot, "xlsx", "xlsx-1")
	if e != nil || x.Format != "" {
		t.Fatal(x, e)
	}
}

func TestExportFormatRequestsFailClosed(t *testing.T) {
	s, p, snapshot, _ := formatService(t, nil)
	ctx := context.Background()
	if got := s.Formats(); !slices.Equal(got, []string{"xlsx"}) {
		t.Fatalf("Formats() = %v", got)
	}
	if none, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: "t", Integration: "i", SourceTenant: "s", Adapter: contractsim.Financial()}}, CursorKey: []byte("01234567890123456789012345678901")}); err != nil || none.Formats() == nil || len(none.Formats()) != 0 {
		t.Fatalf("Formats() without writers = %#v, %v; want an empty list", none.Formats(), err)
	}
	for _, format := range []string{"pdf", "doc", "PDF", "../x"} {
		if _, e := s.SubmitExportFormat(ctx, p, snapshot, format, "k-"+format[:1]); !errors.Is(e, r.ErrUnsupported) {
			t.Errorf("format %q: %v; want unsupported", format, e)
		}
	}
	s, p, snapshot, _ = formatService(t, map[string]r.WorkbookWriter{"pdf": fakePDF{}})
	if _, e := s.SubmitExportFormat(ctx, p, snapshot, "", "same-key"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SubmitExportFormat(ctx, p, snapshot, "pdf", "same-key"); !errors.Is(e, r.ErrConflict) {
		t.Fatalf("reusing an idempotency key for another format: %v; want conflict", e)
	}
	for _, name := range []string{"", "xlsx", "PDF", "a/b", "abcdefghijklmnopq"} {
		_, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: "t", Integration: "i", SourceTenant: "s", Adapter: contractsim.Financial()}}, CursorKey: []byte("01234567890123456789012345678901"), Formats: map[string]r.WorkbookWriter{name: fakePDF{}}})
		if !errors.Is(e, r.ErrInvalid) {
			t.Errorf("format name %q accepted: %v", name, e)
		}
	}
}

func TestExportFormatKeepsWorkbookEncodingAndContract(t *testing.T) {
	// The workbook keeps the original request digest and JSON encoding of stored jobs and artifacts.
	if r.ExportRequestHash("abc", "") != r.Digest("abc") || r.ExportRequestHash("abc", "pdf") == r.Digest("abc") {
		t.Fatal("export request hash does not keep the workbook digest or does not bind the format")
	}
	for _, v := range []any{r.Job{ID: "x"}, r.Artifact{ID: "x"}} {
		if b, _ := json.Marshal(v); bytes.Contains(b, []byte("Format")) {
			t.Errorf("workbook record encodes a format: %s", b)
		}
	}
	s, p, snapshot, repo := formatService(t, map[string]r.WorkbookWriter{"pdf": fakePDF{}})
	ctx := context.Background()
	if _, e := s.SubmitExportFormat(ctx, p, snapshot, "pdf", "contract"); e != nil {
		t.Fatal(e)
	}
	claim, e := repo.Claim(ctx, time.Minute)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	job := claim.Job
	job.State, job.Generation, job.LeaseUntil = "queued", 0, time.Time{}
	if r.ValidateAdmission(job) != nil {
		t.Fatal("a valid pdf export job is rejected")
	}
	job.Format = ""
	if r.ValidateAdmission(job) == nil {
		t.Error("a job whose format does not match its request hash is admitted")
	}
	snapshotJob := job
	snapshotJob.Kind, snapshotJob.SnapshotID, snapshotJob.Format, snapshotJob.RequestHash = "snapshot", "", "pdf", r.Digest(r.Normalize(job.Query))
	if r.ValidateAdmission(snapshotJob) == nil {
		t.Error("a snapshot job with a format is admitted")
	}
	if snapshotJob.Format = ""; r.ValidateAdmission(snapshotJob) != nil {
		t.Error("the snapshot job without a format is rejected; the test above proves nothing")
	}
	// A stored record with a malformed format is refused even when its hash is consistent.
	for _, format := range []string{"PDF", "xlsx", "../x", "abcdefghijklmnopq"} {
		job.Format, job.RequestHash = format, r.ExportRequestHash(job.SnapshotID, format)
		if r.ValidateAdmission(job) == nil {
			t.Errorf("a job with format %q is admitted", format)
		}
	}
	bad := &r.Artifact{ID: r.NewID(), Owner: claim.Job.Owner, SnapshotID: claim.Job.SnapshotID, Format: "", ExpiresAt: claim.Job.Prepared.ExpiresAt, Content: []byte("x"), SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"}
	good := *bad
	good.Format = "pdf"
	if e := r.ValidatePublication(claim.Job, r.Publication{Artifact: &good}); e != nil {
		t.Fatalf("the matching pdf artifact is not publishable: %v", e)
	}
	if r.ValidatePublication(claim.Job, r.Publication{Artifact: bad}) == nil {
		t.Error("an artifact whose format differs from its job is publishable")
	}
}
