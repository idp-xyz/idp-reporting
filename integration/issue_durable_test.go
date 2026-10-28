package integration_test

import (
	"context"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePDFWriter struct{}

func (fakePDFWriter) Write(context.Context, r.Snapshot) ([]byte, error) {
	return []byte("%PDF-1.7 x"), nil
}

// stampIssuer answers with a PDF carrying the stamp; it never touches a template store.
type stampIssuer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *stampIssuer) Issue(_ context.Context, req r.IssueRequest) (r.IssueResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return r.IssueResult{}, f.err
	}
	return r.IssueResult{PDF: []byte("%PDF-1.7\n" + req.Stamp + "\n%%EOF"), Pages: 3}, nil
}

func issueDurableService(t *testing.T, v durableEnv, issuer r.Issuer) *r.Service {
	t.Helper()
	connections := []r.Connection{}
	for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
		connections = append(connections, r.Connection{Tenant: v.p.Tenant, Integration: id, SourceTenant: "tenant_a", Adapter: v.source})
	}
	s, e := r.NewService(r.Config{Repository: v.repo, Connections: connections, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: issuer})
	if e != nil {
		t.Fatal(e)
	}
	return s
}

const frozenTemplate = `{"schema_version":"1.0","kind":"document","binding":{"integration_id":"i","dataset":{"id":"d","version":"1"}},"elements":[]}`

func issueInput(snapshot string, row int) r.IssueInput {
	return r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(frozenTemplate), SnapshotID: snapshot, RowIndex: row}
}

// TestDurablePostgresIssues covers the issue kind (S03-G) on the PostgreSQL fixture: the
// numbers taken at admission in the admitting transaction, replays, cancellations, the record
// and the artifact published together, the listing, the v2→v3 fixture upgrade and tampering.
func TestDurablePostgresIssues(t *testing.T) {
	if os.Getenv("REPORTING_S01_TEST_DSN") == "" || os.Getenv("REPORTING_S01_SOURCE_DSN") == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required platform/source PostgreSQL DSNs missing")
		}
		t.Skip("durable PostgreSQL gate not run: two isolated DB DSNs required")
	}
	ctx := context.Background()
	t.Run("numbers_records_and_listing", func(t *testing.T) {
		v := newDurable(t)
		issuer := &stampIssuer{}
		s := issueDurableService(t, v, issuer)
		snap := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.FinancialQuery(), "snap")
		first, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		if e != nil || first.Issue.Number != 1 {
			t.Fatal(first, e)
		}
		// A replay returns the same job and gives its number back; another input under the same
		// key conflicts; the next key takes the next number.
		if again, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one"); e != nil || again.ID != first.ID || again.Issue.Number != 1 {
			t.Fatal(again, e)
		}
		if _, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 1), "one"); !errors.Is(e, r.ErrConflict) {
			t.Fatal("conflicting replay admitted", e)
		}
		second, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 1), "two")
		if e != nil || second.Issue.Number != 2 {
			t.Fatal(second, e)
		}
		// A cancelled job keeps its number; it is never reused.
		if c, e := s.Cancel(ctx, v.p, second.ID); e != nil || c.State != "cancelled" || c.Issue.Number != 2 {
			t.Fatal(c, e)
		}
		third, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 2), "three")
		if e != nil || third.Issue.Number != 3 {
			t.Fatal(third, e)
		}
		// The worker issues the first job: artifact and record in one transaction.
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		done, e := s.Job(ctx, v.p, first.ID)
		if e != nil || done.State != "succeeded" || done.ArtifactID == "" {
			t.Fatal(done, e)
		}
		rec, e := v.repo.Issue(ctx, v.p.Identity, first.ID)
		if e != nil || rec.Number != 1 || rec.ArtifactID != done.ArtifactID || rec.Pages != 3 || rec.Stamp != r.IssueStamp(1, rec.IssuedAt, first.ID) || rec.IssuedAt.IsZero() {
			t.Fatalf("%+v %v", rec, e)
		}
		a, e := s.Download(ctx, v.p, done.ArtifactID)
		if e != nil || a.Issue != first.ID || a.SHA256 != rec.SHA256 || !strings.Contains(string(a.Content), rec.Stamp) {
			t.Fatal(a.Issue, e)
		}
		view, e := s.Issue(ctx, v.p, first.ID)
		if e != nil || view.Number != 1 || view.SHA256 != rec.SHA256 || view.Pages != 3 || view.IssuedAt != rec.IssuedAt.UTC().Format(time.RFC3339) {
			t.Fatalf("%+v %v", view, e)
		}
		// The third job fails on the issuer's word and keeps its number with the error code.
		issuer.err = r.ErrUnpublished
		if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrUnpublished) {
			t.Fatal(e)
		}
		list, e := s.Issues(ctx, v.p, 10)
		if e != nil || len(list) != 3 || list[0].Number != 3 || list[0].State != "failed" || list[0].ErrorCode != "TEMPLATE_NOT_PUBLISHED" || list[1].Number != 2 || list[1].State != "cancelled" || list[2].Number != 1 || list[2].SHA256 != rec.SHA256 {
			t.Fatalf("%+v %v", list, e)
		}
		if list, e = s.Issues(ctx, v.p, 2); e != nil || len(list) != 2 || list[0].Number != 3 {
			t.Fatalf("%+v %v", list, e)
		}
		// Another identity or another tenant's integration sees none of it, and its numbers
		// start at 1.
		stranger := v.p
		stranger.Subject = "stranger"
		if list, e = s.Issues(ctx, stranger, 10); e != nil || len(list) != 0 {
			t.Fatal(list, e)
		}
		if _, e = v.repo.Issue(ctx, stranger.Identity, first.ID); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("record visible to another subject", e)
		}
		// An export artifact is not an issue and never gets a number.
		ex := durableExport(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, snap.SnapshotID, "export")
		if ex.Issue != nil {
			t.Fatal(ex)
		}
		if _, e = s.Issue(ctx, v.p, ex.ID); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("export read as an issue", e)
		}
	})
	t.Run("concurrent_admissions_take_distinct_consecutive_numbers", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		snap := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.FinancialQuery(), "snap")
		const n = 8
		numbers := make([]int64, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, i%3), "key-"+string(rune('a'+i)))
				errs[i] = e
				if e == nil {
					numbers[i] = j.Issue.Number
				}
			}()
		}
		wg.Wait()
		seen := map[int64]bool{}
		for i := range n {
			if errs[i] != nil || numbers[i] < 1 || numbers[i] > n || seen[numbers[i]] {
				t.Fatalf("%d: number %d, %v (seen %v)", i, numbers[i], errs[i], seen)
			}
			seen[numbers[i]] = true
		}
	})
	t.Run("fixture_v2_upgrade_adds_issues_and_keeps_assets", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		ex := durableExport(t, env, snap.SnapshotID, "export")
		before, e := s.Download(ctx, v.p, ex.ArtifactID)
		if e != nil {
			t.Fatal(e)
		}
		v.repo.Close()
		p := fixtureAdmin(t)
		if _, e = p.Exec(ctx, `DROP TABLE reporting_s01.issue_numbers;
ALTER TABLE reporting_s01.jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE reporting_s01.jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('snapshot','export'));
ALTER TABLE reporting_s01.assets DROP CONSTRAINT assets_kind_check;
ALTER TABLE reporting_s01.assets ADD CONSTRAINT assets_kind_check CHECK(kind IN ('snapshot','artifact'));
UPDATE reporting_s01.profile SET version=2 WHERE singleton;`); e != nil {
			t.Fatal(e)
		}
		bad, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false)
		if bad != nil {
			bad.Close()
		}
		mustError(t, e, r.ErrDenied)
		reopened, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), true)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		v.repo = reopened
		s = issueDurableService(t, v, &stampIssuer{})
		after, e := s.Download(ctx, v.p, ex.ArtifactID)
		if e != nil || after.SHA256 != before.SHA256 {
			t.Fatal(e)
		}
		j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		if e != nil || j.Issue.Number != 1 {
			t.Fatal(j, e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if rec, e := reopened.Issue(ctx, v.p.Identity, j.ID); e != nil || rec.Number != 1 {
			t.Fatal(rec, e)
		}
	})
	t.Run("tampered_record_or_job_is_integrity_failure", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
		j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "one")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Issue(ctx, v.p, j.ID); e != nil {
			t.Fatal(e)
		}
		p := fixtureAdmin(t)
		// The record's number changed with its digest recomputed: the stamp no longer matches.
		for _, sql := range []string{`UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Number}','7') WHERE id=$1 AND kind='issue'`, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1 AND kind='issue'`} {
			if _, e = p.Exec(ctx, sql, j.ID); e != nil {
				t.Fatal(e)
			}
		}
		_, e = s.Issue(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
		// The job's frozen template changed without its digest: the stored job is corrupt.
		if _, e = p.Exec(ctx, `UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{RowIndex}','2') WHERE id=$1`, j.ID); e != nil {
			t.Fatal(e)
		}
		_, e = s.Job(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
		_, e = s.Issues(ctx, v.p, 10)
		mustError(t, e, r.ErrIntegrity)
	})
}
