package reporting_test

import (
	"context"
	"errors"
	r "idp.local/reporting/reporting"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The view of an issue job is the record read back and checked against the job: a record that is not the
// job's, or whose stamp is not its number's, is INTEGRITY_FAILURE wherever it is shown (the record, the
// listing, the job). The repository here answers with the record altered in one checked field.
func TestIssueViewChecksTheRecordAgainstTheJob(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(*r.Issue){
		"another tenant":              func(i *r.Issue) { i.Owner.Tenant = "other" },
		"another issuer":              func(i *r.Issue) { i.Owner.Issuer = "other" },
		"another subject":             func(i *r.Issue) { i.Owner.Subject = "other" },
		"another integration":         func(i *r.Issue) { i.Owner.Integration = "other" },
		"another number":              func(i *r.Issue) { i.Number = "AR-000002" },
		"another artifact":            func(i *r.Issue) { i.ArtifactID = r.NewID() },
		"another template":            func(i *r.Issue) { i.TemplateID = strings.Repeat("b", 32) },
		"another template version":    func(i *r.Issue) { i.TemplateVersion++ },
		"another template digest":     func(i *r.Issue) { i.TemplateSHA256 = strings.Repeat("1", 64) },
		"another snapshot":            func(i *r.Issue) { i.SnapshotID = r.NewID() },
		"another row":                 func(i *r.Issue) { i.RowIndex++ },
		"another stamp":               func(i *r.Issue) { i.Stamp = r.IssueStamp(i.Number, i.IssuedAt.Add(time.Second), i.ID) },
		"no stamp":                    func(i *r.Issue) { i.Stamp = "" },
		"the stamp of another record": func(i *r.Issue) { i.Stamp = r.IssueStamp(i.Number, i.IssuedAt, r.NewID()) },
	} {
		t.Run(name, func(t *testing.T) {
			v := newIssueEnv(t, envOpts{})
			j := v.submit(1, "k")
			if _, e := v.run(); e != nil {
				t.Fatal(e)
			}
			// Unaltered, every entry point answers.
			if got, e := v.s.Issue(ctx, v.p, j.ID); e != nil || got.Number != "AR-000001" {
				t.Fatal(got, e)
			}
			v.store.recordAs = change
			if _, e := v.s.Issue(ctx, v.p, j.ID); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the record: %v; want INTEGRITY_FAILURE", e)
			}
			if _, e := v.s.Issues(ctx, v.p, 10); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the listing: %v; want INTEGRITY_FAILURE", e)
			}
			if _, e := v.s.Job(ctx, v.p, j.ID); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the job: %v; want INTEGRITY_FAILURE", e)
			}
			// The cancel and the replay of a succeeded job show it like the job does, so they refuse alike.
			if _, e := v.s.Cancel(ctx, v.p, j.ID); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the cancel: %v; want INTEGRITY_FAILURE", e)
			}
			if _, e := v.s.SubmitIssue(ctx, v.p, v.input(1), "k"); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("the replay: %v; want INTEGRITY_FAILURE", e)
			}
			// Nothing of the record is shown without the alteration either: it comes back whole.
			v.store.recordAs = nil
			if got, e := v.s.Issue(ctx, v.p, j.ID); e != nil || got.Number != "AR-000001" {
				t.Fatal(got, e)
			}
		})
	}
}

// F4: a job that succeeded is shown with its number wherever a job is shown, not only by the poll: a
// replay of the key that admitted it, and the answer of a cancel that came too late.
func TestSucceededIssueJobCarriesItsNumberInEveryAnswer(t *testing.T) {
	ctx := context.Background()
	v := newIssueEnv(t, envOpts{})
	j := v.submit(1, "k")
	if _, e := v.run(); e != nil {
		t.Fatal(e)
	}
	polled := v.job(j.ID)
	replayed, e := v.s.SubmitIssue(ctx, v.p, v.input(1), "k")
	if e != nil || replayed.ID != j.ID || replayed.State != "succeeded" || replayed.Issue == nil || replayed.Issue.Number != "AR-000001" {
		t.Fatalf("the replay: %+v %+v %v", replayed, replayed.Issue, e)
	}
	cancelled, e := v.s.Cancel(ctx, v.p, j.ID)
	if e != nil || cancelled.State != "succeeded" || cancelled.Issue == nil || cancelled.Issue.Number != "AR-000001" {
		t.Fatalf("the cancel: %+v %+v %v", cancelled, cancelled.Issue, e)
	}
	// The same number, the same summary; the cancel gives no references to the output.
	if !reflect.DeepEqual(replayed.Issue, polled.Issue) || !reflect.DeepEqual(cancelled.Issue, polled.Issue) || replayed.ArtifactID != polled.ArtifactID || polled.ArtifactID == "" {
		t.Fatalf("the views differ: poll %+v, replay %+v, cancel %+v", polled, replayed, cancelled)
	}
	if cancelled.ArtifactID != "" || cancelled.SnapshotID != "" {
		t.Fatalf("a cancel answer carries references to the output: %+v", cancelled)
	}
	// A job that did not succeed carries none, in any answer.
	k := v.submit(2, "queued")
	if again, e := v.s.SubmitIssue(ctx, v.p, v.input(2), "queued"); e != nil || again.Issue == nil || again.Issue.Number != "" || again.ID != k.ID {
		t.Fatal(again, e)
	}
	if c, e := v.s.Cancel(ctx, v.p, k.ID); e != nil || c.State != "cancelled" || c.Issue.Number != "" {
		t.Fatal(c, e)
	}
}
