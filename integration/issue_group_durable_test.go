package integration_test

// Issue by group on the PostgreSQL fixture (S03-K): the frozen group lives in the job's and the record's JSON payloads
// (no migration), the durable product declares contract version 2 and checks the group against the version of its
// data the read was frozen on, and the failure order of S03-H is the same. These are subtests of
// TestDurablePostgresIssues, so that they skip and fail with it.

import (
	"context"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/pgsource"
	r "idp.local/reporting/reporting"
	"slices"
	"strings"
	"testing"
)

var durableGroupLines = [][]string{
	{"customer_a", "current", "10.00"},
	{"customer_b", "1_30", "20.00"},
	{"customer_a", "1_30", "30.00"},
	{"customer_c", "current", "40.00"},
	{"customer_a", "current", "50.00"},
}

// groupedDurable gives the finance product of the durable fixture five lines of three customers and freezes them.
func groupedDurable(t *testing.T, v durableEnv, s *r.Service) r.Snapshot {
	t.Helper()
	ctx := context.Background()
	fixture := contractsim.Financial().Fixture()
	fixture.Records = nil
	grant := contractsim.DefaultGrant()
	grant.Rows = nil
	for i, row := range durableGroupLines {
		key := "g" + string(rune('1'+i))
		grant.Rows = append(grant.Rows, key)
		fixture.Records = append(fixture.Records, contractsim.Record{Company: "company_demo", Key: key, Values: row})
	}
	if e := v.source.ReplaceFixture(ctx, contractsim.FinanceIntegration, "tenant_a", fixture); e != nil {
		t.Fatal(e)
	}
	if e := v.source.SetGrant(ctx, sourceContext(v.p, contractsim.FinanceIntegration), grant); e != nil {
		t.Fatal(e)
	}
	job := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.FinancialQuery(), "grouped")
	snap, e := s.AuthorizedSnapshot(ctx, v.p, job.SnapshotID)
	if e != nil || len(snap.Result.Rows) != len(durableGroupLines) {
		t.Fatal(e, len(snap.Result.Rows))
	}
	return snap
}

func durableGroupInput(snap r.Snapshot, g r.IssueGroup) r.IssueInput {
	return r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(frozenTemplate), SnapshotID: snap.ID, Group: &g}
}

// versionOneProduct wraps the durable source so that it speaks contract version 1 only.
type versionOneProduct struct {
	r.Adapter
	r.DocumentAuthority
}

// alteredProduct is a product whose own source snapshot differs from the platform's: the intent it is asked about
// is altered on its way to the durable source, which refuses what its frozen read does not produce.
type alteredProduct struct {
	*pgsource.Source
	change func(*r.IssueIntent)
}

func (a alteredProduct) PrepareDocument(ctx context.Context, sc r.SourceContext, in r.IssueIntent) (r.IssueReservation, error) {
	a.change(&in)
	return a.Source.PrepareDocument(ctx, sc, in)
}

func durableGroupIssues(t *testing.T) {
	ctx := context.Background()
	t.Run("a_group_is_frozen_asked_of_the_product_and_kept_in_the_record", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		snap := groupedDurable(t, v, s)
		a, e := r.NewIssueGroup(snap, []string{"customer.id"}, []string{"customer_a"}, []int{0, 2, 4})
		if e != nil {
			t.Fatal(e)
		}
		whole, e := r.NewIssueGroup(snap, nil, nil, []int{0, 1, 2, 3, 4})
		if e != nil {
			t.Fatal(e)
		}
		first, e := s.SubmitIssue(ctx, v.p, durableGroupInput(snap, a), "one")
		if e != nil || first.Issue == nil || first.Issue.RowIndex != nil || first.Issue.Group == nil || first.Issue.Group.RowCount != 3 || first.Issue.Number != "" {
			t.Fatal(first, e)
		}
		if _, held := productRecord(t, v, first.ID); held {
			t.Fatal("admission reached the product")
		}
		// A replay is the same job, another group under the key and a row under it are conflicts.
		if again, e := s.SubmitIssue(ctx, v.p, durableGroupInput(snap, a), "one"); e != nil || again.ID != first.ID {
			t.Fatal(again, e)
		}
		if _, e = s.SubmitIssue(ctx, v.p, durableGroupInput(snap, whole), "one"); !errors.Is(e, r.ErrConflict) {
			t.Fatal("another group under the key", e)
		}
		if _, e = s.SubmitIssue(ctx, v.p, issueInput(snap.ID, 0), "one"); !errors.Is(e, r.ErrConflict) {
			t.Fatal("a row under the key of a group", e)
		}
		second, e := s.SubmitIssue(ctx, v.p, durableGroupInput(snap, whole), "two")
		if e != nil {
			t.Fatal(e)
		}
		// What the store holds: the group, and no row, in the job's payload.
		p := fixtureAdmin(t)
		var hasRow bool
		var rows string
		if e = p.QueryRow(ctx, `SELECT data ? 'RowIndex', data->'Group'->>'RowsSHA256' FROM reporting_s01.jobs WHERE id=$1`, first.ID).Scan(&hasRow, &rows); e != nil || hasRow || rows != a.RowsSHA256 {
			t.Fatalf("the stored job: row %v, digest %q (want %q): %v", hasRow, rows, a.RowsSHA256, e)
		}
		for range 2 {
			if _, e = s.RunOne(ctx); e != nil {
				t.Fatal(e)
			}
		}
		// The durable product took the group and numbered it as a row.
		theirs, held := productRecord(t, v, first.ID)
		if !held || theirs.State != contractsim.IssueConfirmed || theirs.Number != "AR-000001" {
			t.Fatalf("the product's record: %+v %v", theirs, held)
		}
		if other, held := productRecord(t, v, second.ID); !held || other.Number != "AR-000002" || other.State != contractsim.IssueConfirmed {
			t.Fatalf("the product's record of the whole snapshot: %+v %v", other, held)
		}
		rec, e := v.repo.Issue(ctx, v.p.Identity, first.ID)
		if e != nil || rec.Number != "AR-000001" || rec.RowIndex != 0 || !rec.Group.Equal(&a) || rec.ReservationRef != theirs.Ref || rec.ConfirmationRef != theirs.ConfirmationRef {
			t.Fatalf("%+v %v", rec, e)
		}
		whole2, e := v.repo.Issue(ctx, v.p.Identity, second.ID)
		if e != nil || !whole2.Group.Equal(&whole) || len(whole2.Group.Fields) != 0 {
			t.Fatalf("%+v %v", whole2, e)
		}
		done, e := s.Job(ctx, v.p, first.ID)
		if e != nil || done.State != "succeeded" || done.Issue.Number != "AR-000001" || done.Issue.Group == nil || done.Issue.RowIndex != nil || done.ArtifactID == "" {
			t.Fatalf("%+v %v", done, e)
		}
		view, e := s.Issue(ctx, v.p, first.ID)
		if e != nil || view.Group == nil || !slices.Equal(view.Group.Key, []string{"customer_a"}) || view.Group.RowCount != 3 || view.RowIndex != nil || view.Number != "AR-000001" {
			t.Fatalf("%+v %v", view, e)
		}
		list, e := s.Issues(ctx, v.p, 10)
		if e != nil || len(list) != 2 || list[0].ID != second.ID || list[0].Group == nil || list[1].ID != first.ID || list[1].Group == nil {
			t.Fatalf("%+v %v", list, e)
		}
		if _, e = s.Download(ctx, v.p, done.ArtifactID); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("a_version_one_product_is_not_sent_a_group", func(t *testing.T) {
		v := newDurable(t)
		base := issueDurableService(t, v, &stampIssuer{})
		snap := groupedDurable(t, v, base)
		g, e := r.NewIssueGroup(snap, []string{"customer.id"}, []string{"customer_a"}, []int{0, 2, 4})
		if e != nil {
			t.Fatal(e)
		}
		old := issueDurableServiceAs(t, v, v.repo, &stampIssuer{}, versionOneProduct{v.source, v.source})
		if _, e = old.SubmitIssue(ctx, v.p, durableGroupInput(snap, g), "one"); !errors.Is(e, r.ErrUnsupported) {
			t.Fatal("a group admitted for a version-1 product", e)
		}
		if jobs, e := v.repo.Issues(ctx, v.p.Identity, []string{contractsim.FinanceIntegration}, 10); e != nil || len(jobs) != 0 {
			t.Fatal(jobs, e)
		}
		// Stored while the product declared version 2, run by a service whose product does not: it fails as unsupported,
		// the product is asked nothing.
		j, e := base.SubmitIssue(ctx, v.p, durableGroupInput(snap, g), "two")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = old.RunOne(ctx); !errors.Is(e, r.ErrUnsupported) {
			t.Fatal("a group job ran against a version-1 product", e)
		}
		if st, e := v.repo.Job(ctx, v.p.Identity, j.ID); e != nil || st.State != "failed" || st.ErrorCode != "UNSUPPORTED_COMBINATION" {
			t.Fatalf("%+v %v", st, e)
		}
		if _, held := productRecord(t, v, j.ID); held {
			t.Fatal("the product holds something for a group job it could not be sent")
		}
	})
	t.Run("a_mismatch_is_refused_by_the_product_and_voided_in_the_order_of_s03h", func(t *testing.T) {
		v := newDurable(t)
		base := issueDurableService(t, v, &stampIssuer{})
		snap := groupedDurable(t, v, base)
		g, e := r.NewIssueGroup(snap, []string{"customer.id"}, []string{"customer_a"}, []int{0, 2, 4})
		if e != nil {
			t.Fatal(e)
		}
		for name, change := range map[string]func(*r.IssueIntent){
			"a digest of other rows": func(in *r.IssueIntent) { in.Group.RowsSHA256 = strings.Repeat("0", 64) },
			"a group left incomplete": func(in *r.IssueIntent) {
				c := in.Group.Clone()
				c.Rows = c.Rows[:2]
				in.Group = &c
			},
		} {
			t.Run(name, func(t *testing.T) {
				store := &recordedFailures{Repository: v.repo}
				s := issueDurableServiceAs(t, v, store, &stampIssuer{}, alteredProduct{v.source, change})
				j, e := s.SubmitIssue(ctx, v.p, durableGroupInput(snap, g), "k "+name)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.RunOne(ctx); !errors.Is(e, r.ErrDenied) {
					t.Fatal("the product's refusal did not fail the job", e)
				}
				st, e := v.repo.Job(ctx, v.p.Identity, j.ID)
				if e != nil || st.State != "failed" || st.ErrorCode != "AUTH_SCOPE_DENIED" {
					t.Fatalf("%+v %v", st, e)
				}
				if len(store.fails) != 1 || store.fails[0] != "ISSUE_UNRECONCILED" || len(store.resolves) != 1 || store.resolves[0] != "AUTH_SCOPE_DENIED" {
					t.Fatalf("failed with %v, resolved to %v", store.fails, store.resolves)
				}
				// Nothing reserved; the void is the product's tombstone and no number was spent.
				if rec, held := productRecord(t, v, j.ID); !held || rec.State != contractsim.IssueVoid || rec.Number != "" || rec.Reason != "AUTH_SCOPE_DENIED" {
					t.Fatalf("the product's record: %+v %v", rec, held)
				}
			})
		}
		// The numbers are the product's and none was used by the refusals.
		ok, e := base.SubmitIssue(ctx, v.p, durableGroupInput(snap, g), "good")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = base.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if got, e := base.Job(ctx, v.p, ok.ID); e != nil || got.Issue.Number != "AR-000001" {
			t.Fatalf("%+v %v", got, e)
		}
	})
	t.Run("a_tampered_group_is_integrity_failure", func(t *testing.T) {
		v := newDurable(t)
		s := issueDurableService(t, v, &stampIssuer{})
		snap := groupedDurable(t, v, s)
		g, e := r.NewIssueGroup(snap, []string{"customer.id"}, []string{"customer_a"}, []int{0, 2, 4})
		if e != nil {
			t.Fatal(e)
		}
		j, e := s.SubmitIssue(ctx, v.p, durableGroupInput(snap, g), "one")
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
		rehash := func(table, kind string) {
			t.Helper()
			sql := `UPDATE reporting_s01.` + table + ` SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1`
			if kind != "" {
				sql += ` AND kind='` + kind + `'`
			}
			if _, e := p.Exec(ctx, sql, j.ID); e != nil {
				t.Fatal(e)
			}
		}
		// The record's group changed with its digest recomputed: consistent in itself, not with the job.
		if _, e = p.Exec(ctx, `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Group,Key}','["customer_b"]') WHERE id=$1 AND kind='issue'`, j.ID); e != nil {
			t.Fatal(e)
		}
		rehash("assets", "issue")
		for name, read := range map[string]func() error{
			"record":  func() error { _, e := s.Issue(ctx, v.p, j.ID); return e },
			"listing": func() error { _, e := s.Issues(ctx, v.p, 10); return e },
			"job":     func() error { _, e := s.Job(ctx, v.p, j.ID); return e },
		} {
			if e := read(); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("%s with a record of another group: %v", name, e)
			}
		}
		if _, e = p.Exec(ctx, `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Group,Key}','["customer_a"]') WHERE id=$1 AND kind='issue'`, j.ID); e != nil {
			t.Fatal(e)
		}
		rehash("assets", "issue")
		if _, e = s.Issue(ctx, v.p, j.ID); e != nil {
			t.Fatal("the restored record does not read:", e)
		}
		// The job's group changed with the payload digest recomputed, but not its request hash: the stored job is corrupt.
		if _, e = p.Exec(ctx, `UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{Group,Rows}','[0,2]') WHERE id=$1`, j.ID); e != nil {
			t.Fatal(e)
		}
		rehash("jobs", "")
		_, e = v.repo.Job(ctx, v.p.Identity, j.ID)
		mustError(t, e, r.ErrIntegrity)
		_, e = s.Job(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
		_, e = s.Issues(ctx, v.p, 10)
		mustError(t, e, r.ErrIntegrity)
		// A row index put into a group job, with the hash left: refused as well.
		if _, e = p.Exec(ctx, `UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{Group,Rows}','[0,2,4]') WHERE id=$1`, j.ID); e != nil {
			t.Fatal(e)
		}
		rehash("jobs", "")
		if _, e = v.repo.Job(ctx, v.p.Identity, j.ID); e != nil {
			t.Fatal("the restored job does not read:", e)
		}
		if _, e = p.Exec(ctx, `UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{RowIndex}','1') WHERE id=$1`, j.ID); e != nil {
			t.Fatal(e)
		}
		rehash("jobs", "")
		_, e = v.repo.Job(ctx, v.p.Identity, j.ID)
		mustError(t, e, r.ErrIntegrity)
	})
}
