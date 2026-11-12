package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/postgres"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The cancellation of an issued document in the stores (S24-A, ADR 0025 decision 3): one contract for the memory store and the PostgreSQL
// store (runContract), and what only PostgreSQL has: the migration, the roles and the row-level security (migrate_test.go, rls_test.go).

// issuedThrough publishes an issue record through the repository alone, as a worker does once the product has confirmed the file: an
// issue job of the owner over the frozen read, claimed and published with its artifact and its record. The owner need not be the
// snapshot's: a repository keeps what it is given under the owner it is given, and that the owner may read the snapshot is the service's
// to check. Nothing else may be queued when it is called (it claims the oldest job).
func issuedThrough(t *testing.T, repo r.Repository, stored r.Snapshot, owner r.Owner, key, number string, confirmed time.Time) r.Issue {
	t.Helper()
	ctx := context.Background()
	sum := sha256.Sum256([]byte(frozenTemplate))
	templateID, digest := strings.Repeat("a", 32), hex.EncodeToString(sum[:])
	prepared := stored.Prepared
	prepared.Context.Owner = owner
	job := r.Job{ID: r.NewID(), Owner: owner, Kind: "issue", State: "queued", Key: key, RequestHash: r.IssueRequestHash(templateID, 1, digest, stored.ID, 0), Query: stored.Query, Prepared: prepared,
		SnapshotID: stored.ID, Format: "pdf", TemplateID: templateID, TemplateVersion: 1, TemplateSHA256: digest, Template: frozenTemplate}
	if _, e := repo.Admit(ctx, job, r.Quotas{}); e != nil {
		t.Fatal(e)
	}
	claim, e := repo.Claim(ctx, time.Minute)
	if e != nil || claim == nil || claim.Job.ID != job.ID {
		t.Fatal(claim, e)
	}
	content := []byte("%PDF-1.7 " + number)
	fileSum := sha256.Sum256(content)
	artifact := r.Artifact{ID: r.NewID(), Owner: owner, SnapshotID: stored.ID, Format: "pdf", ExpiresAt: prepared.ExpiresAt, Content: content, SHA256: hex.EncodeToString(fileSum[:]), Issue: job.ID}
	rec := r.Issue{ID: job.ID, Owner: owner, Number: number, TemplateID: templateID, TemplateVersion: 1, TemplateSHA256: digest, SnapshotID: stored.ID, SnapshotSHA256: stored.SHA256, ArtifactID: artifact.ID, SHA256: artifact.SHA256, Pages: 1,
		IssuedAt: confirmed.Add(-time.Second), ReservationRef: "res-" + number, ConfirmationRef: "con-" + number, ConfirmedAt: confirmed}
	rec.Stamp = r.IssueStamp(rec.Number, rec.IssuedAt, rec.ID)
	if e = repo.Publish(ctx, *claim, r.Publication{Artifact: &artifact, Issue: &rec}); e != nil {
		t.Fatal(e)
	}
	got, e := repo.Issue(ctx, owner.Identity, job.ID)
	if e != nil || got.Number != number || got.Cancellation != nil {
		t.Fatalf("%+v %v", got, e)
	}
	return got
}

// auditDown makes the store unable to write the audit event of a cancellation until the function it returns is called: the memory store's
// fixture control, and for the PostgreSQL fixture a constraint on the audit's table that refuses the event (the fixture connects as the
// owner, which no grant and no policy stops).
func auditDown(t *testing.T, repo r.Repository) (restore func()) {
	t.Helper()
	switch s := repo.(type) {
	case *memory.Store:
		s.SetAuditFailure(errors.New("the audit cannot be written"))
		return func() { s.SetAuditFailure(nil) }
	case *postgres.Store:
		admin := fixtureAdmin(t)
		drop := func() {
			if _, e := admin.Exec(context.Background(), "ALTER TABLE reporting_s01.audit_events DROP CONSTRAINT IF EXISTS audit_events_down_for_the_test"); e != nil {
				t.Error(e)
			}
		}
		t.Cleanup(drop)
		if _, e := admin.Exec(context.Background(), "ALTER TABLE reporting_s01.audit_events ADD CONSTRAINT audit_events_down_for_the_test CHECK (action <> 'issue.cancel') NOT VALID"); e != nil {
			t.Fatal(e)
		}
		return drop
	}
	t.Fatalf("a store the contract does not know how to break: %T", repo)
	return nil
}

// cancellationEvents are the identity's audit events of cancellations, newest first.
func cancellationEvents(t *testing.T, repo r.Repository, i r.Identity) []r.AuditEvent {
	t.Helper()
	log, ok := repo.(r.AuditLog)
	if !ok {
		t.Fatalf("%T keeps no audit", repo)
	}
	all, e := log.Events(context.Background(), i, false, r.MaxAuditListing, "")
	if e != nil {
		t.Fatal(e)
	}
	out := []r.AuditEvent{}
	for _, event := range all {
		if event.Action == r.AuditIssueCancel {
			out = append(out, event)
		}
	}
	return out
}

// sameCancellation compares two records of a cancellation by value: a time read back is the same time, whatever its representation.
func sameCancellation(a, b r.IssueCancellationRecord) bool {
	return a.IssueID == b.IssueID && a.Ref == b.Ref && a.Reason == b.Reason && a.CancelledAt.Equal(b.CancelledAt) && a.RecordedAt.Equal(b.RecordedAt)
}

// cancellationContract is what both stores do with the cancellation of an issue record (reporting.Repository.RecordCancellation).
func cancellationContract(t *testing.T, v environment) {
	ctx := context.Background()
	repo, me := v.repo, v.p.Identity
	snap := freeze(t, v, contractsim.FinancialQuery(), "snap")
	stored, e := repo.Snapshot(ctx, me, snap.SnapshotID)
	if e != nil {
		t.Fatal(e)
	}
	confirmed := time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	first := issuedThrough(t, repo, stored, stored.Owner, "first", "AR-000001", confirmed)
	second := issuedThrough(t, repo, stored, stored.Owner, "second", "AR-000002", confirmed)
	// The same documents of a colleague of the tenant and of another tenant: theirs, under their own identities.
	colleague, elsewhere := stored.Owner, stored.Owner
	colleague.Subject = "a_colleague"
	elsewhere.Tenant = "another_tenant"
	theirs := issuedThrough(t, repo, stored, colleague, "theirs", "AR-000003", confirmed)
	foreign := issuedThrough(t, repo, stored, elsewhere, "foreign", "AR-000004", confirmed)
	// An issue job that never succeeded has no record to cancel: admitted and cancelled before any worker took it.
	unissued := r.NewID()
	sum := sha256.Sum256([]byte(frozenTemplate))
	if _, e = repo.Admit(ctx, r.Job{ID: unissued, Owner: stored.Owner, Kind: "issue", State: "queued", Key: "unissued", RequestHash: r.IssueRequestHash(strings.Repeat("a", 32), 1, hex.EncodeToString(sum[:]), stored.ID, 1), Query: stored.Query, Prepared: stored.Prepared,
		SnapshotID: stored.ID, Format: "pdf", TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, TemplateSHA256: hex.EncodeToString(sum[:]), Template: frozenTemplate, RowIndex: 1}, r.Quotas{}); e != nil {
		t.Fatal(e)
	}
	if j, e := repo.Cancel(ctx, me, unissued); e != nil || j.State != "cancelled" {
		t.Fatal(j, e)
	}
	cancelled := confirmed.Add(26 * time.Hour)
	good := r.IssueCancellationRecord{IssueID: first.ID, Ref: "CXL-2026-0001", CancelledAt: cancelled, Reason: "客户选错了：订单已取消"}
	untouched := func(what string) {
		t.Helper()
		for _, c := range []struct {
			i  r.Identity
			id string
		}{{me, first.ID}, {me, second.ID}, {colleague.Identity, theirs.ID}, {elsewhere.Identity, foreign.ID}} {
			if rec, e := repo.Issue(ctx, c.i, c.id); e != nil || rec.Cancellation != nil {
				t.Fatalf("%s: the record of %s carries a cancellation: %+v %v", what, c.id, rec.Cancellation, e)
			}
			if events := cancellationEvents(t, repo, c.i); len(events) != 0 {
				t.Fatalf("%s: the audit says %+v", what, events)
			}
		}
	}
	// Who cannot, and what is not a cancellation of the record: nothing is kept and no event is written.
	for name, c := range map[string]struct {
		i      r.Identity
		change func(*r.IssueCancellationRecord)
		want   error
	}{
		"a colleague of the tenant":                   {colleague.Identity, func(*r.IssueCancellationRecord) {}, r.ErrNotFound},
		"another tenant":                              {elsewhere.Identity, func(*r.IssueCancellationRecord) {}, r.ErrNotFound},
		"an identity that is none":                    {r.Identity{}, func(*r.IssueCancellationRecord) {}, r.ErrNotFound},
		"a colleague's issue":                         {me, func(c *r.IssueCancellationRecord) { c.IssueID = theirs.ID }, r.ErrNotFound},
		"another tenant's issue":                      {me, func(c *r.IssueCancellationRecord) { c.IssueID = foreign.ID }, r.ErrNotFound},
		"an issue nothing has":                        {me, func(c *r.IssueCancellationRecord) { c.IssueID = r.NewID() }, r.ErrNotFound},
		"the snapshot, which is no issue record":      {me, func(c *r.IssueCancellationRecord) { c.IssueID = stored.ID }, r.ErrNotFound},
		"the artifact, which is no issue record":      {me, func(c *r.IssueCancellationRecord) { c.IssueID = first.ArtifactID }, r.ErrNotFound},
		"an issue job that never succeeded":           {me, func(c *r.IssueCancellationRecord) { c.IssueID = unissued }, r.ErrNotFound},
		"no reference":                                {me, func(c *r.IssueCancellationRecord) { c.Ref = "" }, r.ErrInvalid},
		"a reference with a space":                    {me, func(c *r.IssueCancellationRecord) { c.Ref = "CXL 1" }, r.ErrInvalid},
		"a reference of 129 characters":               {me, func(c *r.IssueCancellationRecord) { c.Ref = strings.Repeat("C", 129) }, r.ErrInvalid},
		"a reference that is not ASCII":               {me, func(c *r.IssueCancellationRecord) { c.Ref = "作废-1" }, r.ErrInvalid},
		"no time":                                     {me, func(c *r.IssueCancellationRecord) { c.CancelledAt = time.Time{} }, r.ErrInvalid},
		"a time with a fraction":                      {me, func(c *r.IssueCancellationRecord) { c.CancelledAt = cancelled.Add(time.Millisecond) }, r.ErrInvalid},
		"the second before the record's confirmation": {me, func(c *r.IssueCancellationRecord) { c.CancelledAt = confirmed.Add(-time.Second) }, r.ErrInvalid},
		"no reason":                                   {me, func(c *r.IssueCancellationRecord) { c.Reason = "" }, r.ErrInvalid},
		"a blank reason":                              {me, func(c *r.IssueCancellationRecord) { c.Reason = "  " }, r.ErrInvalid},
		"a reason of 201 characters":                  {me, func(c *r.IssueCancellationRecord) { c.Reason = strings.Repeat("因", 201) }, r.ErrInvalid},
		"a reason of two lines":                       {me, func(c *r.IssueCancellationRecord) { c.Reason = "first\nsecond" }, r.ErrInvalid},
	} {
		c2 := good
		c.change(&c2)
		kept, e := repo.RecordCancellation(ctx, c.i, c2)
		if !errors.Is(e, c.want) || kept != (r.IssueCancellationRecord{}) {
			t.Errorf("%s: %+v %v; want %v", name, kept, e, c.want)
		}
	}
	untouched("after the refusals")
	// The audit cannot be written: the cancellation is not kept either, and the store says which of the two failed.
	restore := auditDown(t, repo)
	if kept, e := repo.RecordCancellation(ctx, me, good); !errors.Is(e, r.ErrAuditUnavailable) || kept != (r.IssueCancellationRecord{}) {
		t.Fatalf("a cancellation whose audit event cannot be written: %+v %v", kept, e)
	}
	restore()
	untouched("while the audit was down")
	// Kept: the row as given, in UTC, at the store's clock, whatever time the caller put there; with its audit event at that time.
	before, e := repo.Issue(ctx, me, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	asked := good
	asked.RecordedAt = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	asked.CancelledAt = cancelled.In(time.FixedZone("CST", 8*3600))
	kept, e := repo.RecordCancellation(ctx, me, asked)
	if e != nil || kept.IssueID != first.ID || kept.Ref != "CXL-2026-0001" || kept.Reason != "客户选错了：订单已取消" || !kept.CancelledAt.Equal(cancelled) || kept.CancelledAt.Location() != time.UTC || kept.RecordedAt.Location() != time.UTC {
		t.Fatalf("%+v %v", kept, e)
	}
	if age := time.Since(kept.RecordedAt); age < -2*time.Minute || age > 2*time.Minute {
		t.Fatalf("the cancellation was recorded at %s, which is not the store's clock", kept.RecordedAt)
	}
	// Read back with the record, which keeps every field it had.
	after, e := repo.Issue(ctx, me, first.ID)
	if e != nil || after.Cancellation == nil || !sameCancellation(*after.Cancellation, kept) || after.Cancellation.CancelledAt.Location() != time.UTC {
		t.Fatalf("%+v %v", after.Cancellation, e)
	}
	after.Cancellation = nil
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("the issue record changed:\n%+v\n%+v", after, before)
	}
	events := cancellationEvents(t, repo, me)
	if len(events) != 1 || len(events[0].ID) != 32 || events[0].ObjectKind != "issue" || events[0].ObjectID != first.ID || events[0].Outcome != "succeeded" || events[0].Code != "" || events[0].Subject != "" || !events[0].At.Equal(kept.RecordedAt) {
		t.Fatalf("the audit: %+v", events)
	}
	// Once: the same receipt again answers the row as it was first kept, whatever the reason says now, and writes nothing.
	again := good
	again.Reason = "another reason"
	for _, at := range []time.Time{cancelled, cancelled.In(time.FixedZone("EST", -5*3600))} {
		again.CancelledAt = at
		if same, e := repo.RecordCancellation(ctx, me, again); e != nil || !sameCancellation(same, kept) {
			t.Fatalf("the same receipt again: %+v %v", same, e)
		}
	}
	// Another receipt for the issue is a conflict and changes nothing.
	for name, change := range map[string]func(*r.IssueCancellationRecord){
		"another reference":         func(c *r.IssueCancellationRecord) { c.Ref = "CXL-2026-0002" },
		"another time":              func(c *r.IssueCancellationRecord) { c.CancelledAt = cancelled.Add(time.Second) },
		"another reference in case": func(c *r.IssueCancellationRecord) { c.Ref = "cxl-2026-0001" },
	} {
		other := good
		change(&other)
		if conflict, e := repo.RecordCancellation(ctx, me, other); !errors.Is(e, r.ErrConflict) || conflict != (r.IssueCancellationRecord{}) {
			t.Errorf("%s: %+v %v; want a conflict", name, conflict, e)
		}
	}
	if rec, e := repo.Issue(ctx, me, first.ID); e != nil || rec.Cancellation == nil || !sameCancellation(*rec.Cancellation, kept) || len(cancellationEvents(t, repo, me)) != 1 {
		t.Fatalf("a repeat or a conflict changed the cancellation: %+v %v", rec.Cancellation, e)
	}
	// The other records are not touched, and nobody else sees this one's cancellation or its event.
	if rec, e := repo.Issue(ctx, me, second.ID); e != nil || rec.Cancellation != nil {
		t.Fatalf("%+v %v", rec.Cancellation, e)
	}
	for _, other := range []r.Identity{colleague.Identity, elsewhere.Identity} {
		if _, e := repo.Issue(ctx, other, first.ID); !errors.Is(e, r.ErrNotFound) {
			t.Fatalf("another identity reads the record: %v", e)
		}
		if _, e := repo.RecordCancellation(ctx, other, good); !errors.Is(e, r.ErrNotFound) {
			t.Fatalf("another identity records the same receipt: %v", e)
		}
		if events := cancellationEvents(t, repo, other); len(events) != 0 {
			t.Fatalf("another identity reads the event: %+v", events)
		}
	}
	// Each identity cancels its own: the colleague's document, with a receipt of the same reference (references are the product's, per
	// document, and the platform does not compare them across issues).
	theirCancellation := r.IssueCancellationRecord{IssueID: theirs.ID, Ref: "CXL-2026-0001", CancelledAt: cancelled, Reason: "duplicate"}
	if _, e := repo.RecordCancellation(ctx, colleague.Identity, theirCancellation); e != nil {
		t.Fatal(e)
	}
	if rec, e := repo.Issue(ctx, colleague.Identity, theirs.ID); e != nil || rec.Cancellation == nil || rec.Cancellation.Reason != "duplicate" {
		t.Fatalf("%+v %v", rec.Cancellation, e)
	}
	if len(cancellationEvents(t, repo, colleague.Identity)) != 1 || len(cancellationEvents(t, repo, me)) != 1 {
		t.Fatal("the events of two identities")
	}
	// At the bounds: a reason of 200 characters of four bytes each, a reference of 128 characters, the second of the confirmation.
	wide := r.IssueCancellationRecord{IssueID: second.ID, Ref: strings.Repeat("~", 128), CancelledAt: confirmed, Reason: strings.Repeat("😀", 200)}
	if bound, e := repo.RecordCancellation(ctx, me, wide); e != nil || bound.Reason != wide.Reason || bound.Ref != wide.Ref || !bound.CancelledAt.Equal(confirmed) {
		t.Fatalf("a cancellation at the bounds: %v", e)
	}
	if rec, e := repo.Issue(ctx, me, second.ID); e != nil || rec.Cancellation == nil || rec.Cancellation.Reason != wide.Reason {
		t.Fatal(e)
	}
	// A sweep leaves the cancellations where they are.
	if _, e := repo.Sweep(ctx, r.SweepOptions{}); e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		i  r.Identity
		id string
	}{{me, first.ID}, {me, second.ID}, {colleague.Identity, theirs.ID}} {
		if rec, e := repo.Issue(ctx, c.i, c.id); e != nil || rec.Cancellation == nil {
			t.Fatalf("a cancellation after a sweep: %+v %v", rec.Cancellation, e)
		}
	}
	if rec, e := repo.Issue(ctx, elsewhere.Identity, foreign.ID); e != nil || rec.Cancellation != nil {
		t.Fatalf("%+v %v", rec.Cancellation, e)
	}
}

// The qualifications of the two policies of issue_cancellations, as the migration writes them: what the tests restore a broken policy to.
const (
	cancellationReadQual   = `tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), '')`
	cancellationInsertQual = cancellationReadQual + ` AND EXISTS (SELECT 1 FROM reporting_s01.assets a WHERE a.id = issue_cancellations.issue_id AND a.kind = 'issue' AND a.tenant = issue_cancellations.tenant AND a.issuer = issue_cancellations.issuer AND a.subject = issue_cancellations.subject)`
	// cancellationInsert inserts a cancellation as raw SQL: the issue, the owner the row claims, a reference.
	cancellationInsert = `INSERT INTO reporting_s01.issue_cancellations (issue_id, tenant, issuer, subject, ref, cancelled_at, reason) VALUES ($1,$2,$3,$4,$5,clock_timestamp(),'a reason')`
)

// sqlState is the SQLSTATE of a database error, or "".
func sqlState(e error) string {
	var pg *pgconn.PgError
	if errors.As(e, &pg) {
		return pg.Code
	}
	return ""
}

// cancellationsStep is the version of the migration that adds the cancellations: the tests name it by its name, so that they hold whatever
// number it is given.
func cancellationsStep(t *testing.T) int {
	t.Helper()
	known, e := postgres.Migrations()
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range known {
		if m.Name == "issue_cancellations" {
			return m.Version
		}
	}
	t.Fatal("no migration is named issue_cancellations")
	return 0
}

// migrationCancellations qualifies the step (TestPostgresMigrations): a store at the version before it upgrades with its rows, gets the
// table, its checks, its two policies and the audit's action, with the committed catalog; down gives back exactly the catalog before, and
// is refused while a cancellation exists unless the loss is accepted, when the cancellations and the audit's events of cancellations go
// and nothing else does.
func migrationCancellations(t *testing.T, admin *pgxpool.Pool) {
	ctx := context.Background()
	step := cancellationsStep(t)
	known, e := postgres.Migrations()
	if e != nil {
		t.Fatal(e)
	}
	before, at := fmt.Sprintf("testdata/catalog_%03d.txt", step-1), fmt.Sprintf("testdata/catalog_%03d.txt", step)
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, step-1, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
	wantBefore, e := os.ReadFile(before)
	if e != nil || string(wantBefore) != catalog(t, admin) {
		t.Fatalf("the store at version %d does not have the catalog of %s (%v)", step-1, before, e)
	}
	// Rows the step must carry over and its down must leave: an issue record and an audit event of another action. Before the step the
	// audit does not admit a cancellation's event.
	issue, other := strings.Repeat("ab", 16), strings.Repeat("cd", 16)
	event := `INSERT INTO reporting_s01.audit_events (id, tenant, issuer, subject, action, object_kind, object_id, outcome) VALUES ($1,'t','i','s',$2,'issue',$3,$4)`
	for _, id := range []string{issue, other} {
		if _, e := admin.Exec(ctx, `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256) VALUES($1,'t','p','i','s','issue','{}','x')`, id); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := admin.Exec(ctx, event, strings.Repeat("1", 32), "issue.submit", issue, "succeeded"); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(ctx, event, strings.Repeat("2", 32), "issue.cancel", issue, "succeeded"); sqlState(e) != "23514" {
		t.Fatalf("before the step the audit admits issue.cancel: %v", e)
	}
	st := must(postgres.Status(ctx, admin))
	if plan, e := postgres.Plan(st, step, postgres.Upward); e != nil || len(plan) != 1 || plan[0] != fmt.Sprintf("up   %03d issue_cancellations", step) {
		t.Fatal(plan, e)
	}
	if e := postgres.Up(ctx, admin, step, "test"); e != nil {
		t.Fatal(e)
	}
	st = must(postgres.Status(ctx, admin))
	if st.Current() != step || len(st.Pending) != postgres.LatestVersion()-step || len(st.Problems) != 0 || st.Applied[step-1].Checksum != known[step-1].Checksum || st.Applied[step-1].Name != "issue_cancellations" {
		t.Fatalf("%+v", st)
	}
	atStep := catalog(t, admin)
	if os.Getenv("REPORTING_WRITE_CATALOG") == "1" {
		if e := os.WriteFile(at, []byte(atStep), 0o644); e != nil {
			t.Fatal(e)
		}
	}
	if want, e := os.ReadFile(at); e != nil || string(want) != atStep {
		t.Fatalf("the catalog at version %d differs from %s (%v):\n%s", step, at, e, atStep)
	}
	for _, want := range []string{
		"constraint reporting_s01.issue_cancellations issue_cancellations_pkey PRIMARY KEY (issue_id)",
		"rowsecurity issue_cancellations enabled=true forced=false",
		"policy issue_cancellations identity SELECT permissive=PERMISSIVE roles={public}",
		"policy issue_cancellations issue_owner INSERT permissive=PERMISSIVE roles={public}",
		"'schedule.run'::text, 'issue.cancel'::text])))",
	} {
		if !strings.Contains(atStep, want) {
			t.Errorf("the catalog does not say %q:\n%s", want, atStep)
		}
	}
	// One policy reads and one inserts, and none admits an update, a delete or a worker.
	for _, not := range []string{"policy issue_cancellations worker", "policy issue_cancellations identity ALL", "policy issue_cancellations issue_owner ALL", "issue_cancellations UPDATE", "issue_cancellations DELETE"} {
		if strings.Contains(atStep, not) {
			t.Errorf("the catalog says %q", not)
		}
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 2 || count(t, admin, "SELECT count(*) FROM reporting_s01.audit_events") != 1 {
		t.Fatal("the upgrade lost rows")
	}
	// The table's own checks, as its owner, whom no policy stops: what a cancellation's columns hold.
	insert := func(issueID, ref, reason string) error {
		_, e := admin.Exec(ctx, `INSERT INTO reporting_s01.issue_cancellations (issue_id, tenant, issuer, subject, ref, cancelled_at, reason) VALUES ($1,'t','i','s',$2,clock_timestamp(),$3)`, issueID, ref, reason)
		return e
	}
	for name, c := range map[string]struct{ issue, ref, reason, state string }{
		"an issue id in upper case":           {strings.ToUpper(other), "CXL-1", "a reason", "23514"},
		"an issue id of 31 characters":        {other[:31], "CXL-1", "a reason", "23514"},
		"no reference":                        {other, "", "a reason", "23514"},
		"a reference with a space":            {other, "CXL 1", "a reason", "23514"},
		"a reference of 129 characters":       {other, strings.Repeat("C", 129), "a reason", "23514"},
		"a reference that is not ASCII":       {other, "作废-1", "a reason", "23514"},
		"a reference with a DEL":              {other, "CXL\x7f1", "a reason", "23514"},
		"a reference with a newline":          {other, "CXL-1\n", "a reason", "23514"},
		"no reason":                           {other, "CXL-1", "", "23514"},
		"a reason of 201 characters":          {other, "CXL-1", strings.Repeat("因", 201), "23514"},
		"every printable character, 200 wide": {issue, "!\"#$%&'()*+,-./0189:;<=>?@AZ[\\]^_`az{|}~", strings.Repeat("😀", 200), ""},
	} {
		if e := insert(c.issue, c.ref, c.reason); sqlState(e) != c.state {
			t.Errorf("%s: %v, want SQLSTATE %q", name, e, c.state)
		}
	}
	if e := insert(issue, "CXL-2", "a second cancellation of the issue"); sqlState(e) != "23505" {
		t.Fatalf("a second cancellation of one issue: %v", e)
	}
	if e := insert(other, strings.Repeat("~", 128), "x"); e != nil {
		t.Fatal("a reference of 128 characters and a reason of one:", e)
	}
	// With the step applied the audit admits the cancellation's events.
	for n, outcome := range []string{"requested", "succeeded", "refused"} {
		if _, e := admin.Exec(ctx, event, strings.Repeat(fmt.Sprint(n+2), 32), "issue.cancel", issue, outcome); e != nil {
			t.Fatal(e)
		}
	}
	// Down: refused while a cancellation exists, with what would go; the store stays where it is.
	e = postgres.Down(ctx, admin, step-1, "test", false)
	if !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "2 cancellations of issued documents") || !strings.Contains(e.Error(), "--accept-data-loss") || !strings.Contains(e.Error(), fmt.Sprintf("migration %03d down", step)) {
		t.Fatalf("down with cancellations: %v", e)
	}
	if st := must(postgres.Status(ctx, admin)); st.Current() != step || count(t, admin, "SELECT count(*) FROM reporting_s01.issue_cancellations") != 2 {
		t.Fatal("a refused down changed the store")
	}
	// Accepted: the cancellations and the audit's events of cancellations go; the issue records and the other events stay; the catalog
	// is the one before the step, byte for byte.
	if e := postgres.Down(ctx, admin, step-1, "test", true); e != nil {
		t.Fatal(e)
	}
	if got := catalog(t, admin); got != string(wantBefore) {
		t.Fatalf("down from %03d does not give the catalog of %s:\n%s", step, before, got)
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.assets WHERE kind='issue'") != 2 || count(t, admin, "SELECT count(*) FROM reporting_s01.audit_events WHERE action='issue.submit'") != 1 || count(t, admin, "SELECT count(*) FROM reporting_s01.audit_events") != 1 {
		t.Fatal("the down step lost an issue record or an event that is not a cancellation's, or kept one that is")
	}
	// Up again: the table is there, empty, and the catalog is the step's; with nothing to lose the down needs no acceptance.
	if e := postgres.Up(ctx, admin, step, "test"); e != nil {
		t.Fatal(e)
	}
	if catalog(t, admin) != atStep || count(t, admin, "SELECT count(*) FROM reporting_s01.issue_cancellations") != 0 {
		t.Fatal("the catalog differs after down and up, or the table is not empty")
	}
	if e := postgres.Down(ctx, admin, step-1, "test", false); e != nil {
		t.Fatal("a down with no cancellation needs no acceptance:", e)
	}
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
}

// cancellations (S24-A): as the application role, the cancellation of an issue is its owner's. The store keeps one under the owner's
// context with its audit event and leaves the issue record's row as it was; another identity, of the same tenant or of another, neither
// reads it nor inserts one for the issue, whatever owner the row claims and whatever scope the transaction says; the owner inserts one
// only for an issue record of its own; nothing updates or deletes one, and the role check holds the grants and the two policies.
func (w *rlsWorld) cancellations(t *testing.T) {
	ctx := context.Background()
	a, b := w.identity("A"), w.identity("B")
	issueA, issueB := w.issueJobID["A"], w.issueJobID["B"]
	record, e := w.store.Issue(ctx, a, issueA)
	if e != nil || record.Cancellation != nil {
		t.Fatal(record.Cancellation, e)
	}
	rows := digestOf(t, w.admin)
	kept, e := w.store.RecordCancellation(ctx, a, r.IssueCancellationRecord{IssueID: issueA, Ref: "CXL-A-0001", CancelledAt: record.ConfirmedAt.Add(time.Hour), Reason: "发错客户"})
	if e != nil || kept.RecordedAt.IsZero() {
		t.Fatal(kept, e)
	}
	// The issue record's row, and every other row of jobs and assets, is byte for byte what it was.
	if digestOf(t, w.admin) != rows {
		t.Fatal("recording a cancellation changed a job or an asset")
	}
	const all = "SELECT count(*) FROM reporting_s01.issue_cancellations"
	if n := count(t, w.admin, all); n != 1 {
		t.Fatalf("the store holds %d cancellations", n)
	}
	// Without a context, or with one that is not whole, nothing is seen and nothing is inserted; the worker scope sees none either:
	// there is no worker policy on this table.
	worker := [][2]string{{"reporting.scope", "worker"}}
	for name, settings := range map[string][][2]string{"nothing set": nil, "tenant and issuer only": {{"reporting.tenant", a.Tenant}, {"reporting.issuer", a.Issuer}}, "the worker scope": worker} {
		for role, pool := range map[string]*pgxpool.Pool{"application role": w.app, "sweep role": w.sweep} {
			inTx(t, pool, settings, func(tx pgx.Tx) {
				if n := count(t, tx, all); n != 0 {
					t.Errorf("%s, %s: %d cancellations visible", role, name, n)
				}
			})
		}
		inTx(t, w.app, settings, func(tx pgx.Tx) {
			if _, e := tx.Exec(ctx, cancellationInsert, issueB, b.Tenant, b.Issuer, b.Subject, "CXL-X"); !isRLS(e) {
				t.Errorf("%s: a cancellation inserted: %v", name, e)
			}
		})
	}
	if count(t, w.app, all) != 0 {
		t.Error("a cancellation is visible to a bare connection")
	}
	// The owner reads its own; a colleague of the tenant and another tenant read nothing, by the table or by the issue's id.
	for k, want := range map[string]int{"A": 1, "B": 0, "C": 0} {
		inTx(t, w.app, settingsOf(w.identity(k)), func(tx pgx.Tx) {
			if n := count(t, tx, all); n != want {
				t.Errorf("%s sees %d cancellations, want %d", k, n, want)
			}
			if n := count(t, tx, all+" WHERE issue_id=$1", issueA); n != want {
				t.Errorf("%s sees %d cancellations of A's issue, want %d", k, n, want)
			}
		})
	}
	// Another identity cannot insert a cancellation of A's issue: not as A (the row is not its own), not as itself (the issue record is
	// not one it can see), and not with the worker scope beside its identity; nor can it make a cancellation of its own issue A's.
	for _, k := range []string{"B", "C"} {
		o := w.identity(k)
		for name, attempt := range map[string]struct {
			settings      [][2]string
			issue         string
			claimedOwner  r.Identity
			wantRLSRefuse bool
		}{
			"A's issue under A's name":                       {settingsOf(o), issueA, a, true},
			"A's issue under its own name":                   {settingsOf(o), issueA, o, true},
			"A's issue under its own name, as a worker too":  {append(settingsOf(o), worker...), issueA, o, true},
			"its own issue under A's name":                   {settingsOf(o), w.issueJobID[k], a, true},
			"an issue that does not exist, under its name":   {settingsOf(o), r.NewID(), o, true},
			"a job of its own that is no issue record":       {settingsOf(o), w.jobs[k][0], o, true},
			"its own issue under its own name (the control)": {settingsOf(o), w.issueJobID[k], o, false},
		} {
			inTx(t, w.app, attempt.settings, func(tx pgx.Tx) {
				_, e := tx.Exec(ctx, cancellationInsert, attempt.issue, attempt.claimedOwner.Tenant, attempt.claimedOwner.Issuer, attempt.claimedOwner.Subject, "CXL-"+k)
				if attempt.wantRLSRefuse != isRLS(e) || (!attempt.wantRLSRefuse && e != nil) {
					t.Errorf("%s inserts a cancellation of %s: %v", k, name, e)
				}
			})
		}
	}
	// The owner inserts once: a second row for the issue is refused by the key, and the store's own statement inserts nothing.
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		if _, e := tx.Exec(ctx, cancellationInsert, issueA, a.Tenant, a.Issuer, a.Subject, "CXL-A-0002"); sqlState(e) != "23505" {
			t.Errorf("a second cancellation of A's issue: %v", e)
		}
	})
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		if n, e := affected(t, tx, cancellationInsert+" ON CONFLICT (issue_id) DO NOTHING", issueA, a.Tenant, a.Issuer, a.Subject, "CXL-A-0002"); e != nil || n != 0 {
			t.Errorf("an insert that does nothing on a conflict: %d %v", n, e)
		}
	})
	// Nothing rewrites a cancellation: the application role and the sweep role have no such privilege, under any context.
	for role, pool := range map[string]*pgxpool.Pool{"application role": w.app, "sweep role": w.sweep} {
		for _, settings := range [][][2]string{settingsOf(a), worker, nil} {
			for _, sql := range []string{"UPDATE reporting_s01.issue_cancellations SET reason='rewritten'", "DELETE FROM reporting_s01.issue_cancellations", "TRUNCATE reporting_s01.issue_cancellations"} {
				inTx(t, pool, settings, func(tx pgx.Tx) {
					if _, e := tx.Exec(ctx, sql); e == nil || !strings.Contains(e.Error(), "permission denied") {
						t.Errorf("the %s ran %q: %v", role, sql, e)
					}
				})
			}
		}
	}
	inTx(t, w.sweep, settingsOf(a), func(tx pgx.Tx) {
		if _, e := tx.Exec(ctx, cancellationInsert, issueA, a.Tenant, a.Issuer, a.Subject, "CXL-S"); e == nil || !strings.Contains(e.Error(), "permission denied") {
			t.Errorf("the sweep role inserted a cancellation: %v", e)
		}
	})
	// Through the store and the service: not found for another identity, the record with its cancellation for the owner.
	for _, k := range []string{"B", "C"} {
		o := w.identity(k)
		if _, e := w.store.Issue(ctx, o, issueA); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("%s reads A's record: %v", k, e)
		}
		if _, e := w.store.RecordCancellation(ctx, o, r.IssueCancellationRecord{IssueID: issueA, Ref: "CXL-A-0001", CancelledAt: kept.CancelledAt, Reason: "x"}); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("%s records a cancellation of A's issue: %v", k, e)
		}
		if _, e := w.s.CancelIssue(ctx, w.p[k], issueA, "not mine"); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("%s cancels A's issue: %v", k, e)
		}
		if view, e := w.s.Issue(ctx, w.p[k], w.issueJobID[k]); e != nil || view.Cancellation != nil {
			t.Errorf("%s's own issue: %+v %v", k, view.Cancellation, e)
		}
	}
	if rec, e := w.store.Issue(ctx, a, issueA); e != nil || rec.Cancellation == nil || rec.Cancellation.Ref != "CXL-A-0001" || !rec.Cancellation.RecordedAt.Equal(kept.RecordedAt) {
		t.Fatalf("%+v %v", rec.Cancellation, e)
	}
	if view, e := w.s.Issue(ctx, w.p["A"], issueA); e != nil || view.State != "succeeded" || view.Cancellation == nil || view.Cancellation.Reason != "发错客户" {
		t.Fatalf("%+v %v", view, e)
	}
	// The audit event went in with the cancellation, as the application role under A's context, at the cancellation's time.
	events := cancellationEvents(t, w.store, a)
	if len(events) != 1 || events[0].ObjectID != issueA || events[0].Outcome != "succeeded" || !events[0].At.Equal(kept.RecordedAt) {
		t.Fatalf("%+v", events)
	}
	if others := cancellationEvents(t, w.store, b); len(others) != 0 {
		t.Fatalf("B reads the event: %+v", others)
	}
	// The role check holds the grants: an application role that may update or delete a cancellation, or may not insert one, is refused.
	for name, g := range map[string]struct{ give, take, want string }{
		"UPDATE granted": {"GRANT UPDATE ON reporting_s01.issue_cancellations TO reporting_rls_app_t", "REVOKE UPDATE ON reporting_s01.issue_cancellations FROM reporting_rls_app_t", "INSERT or UPDATE on issue_cancellations"},
		"DELETE granted": {"GRANT DELETE ON reporting_s01.issue_cancellations TO reporting_rls_app_t", "REVOKE DELETE ON reporting_s01.issue_cancellations FROM reporting_rls_app_t", "DELETE or TRUNCATE on issue_cancellations"},
		"INSERT revoked": {"REVOKE INSERT ON reporting_s01.issue_cancellations FROM reporting_rls_app_t", "GRANT INSERT ON reporting_s01.issue_cancellations TO reporting_rls_app_t", "no INSERT on issue_cancellations"},
		"SELECT revoked": {"REVOKE SELECT ON reporting_s01.issue_cancellations FROM reporting_rls_app_t", "GRANT SELECT ON reporting_s01.issue_cancellations TO reporting_rls_app_t", "no SELECT on issue_cancellations"},
	} {
		if _, e := w.admin.Exec(ctx, g.give); e != nil {
			t.Fatal(name, e)
		}
		if problems, e := postgres.RoleProblems(ctx, w.app); e != nil || !strings.Contains(strings.Join(problems, ";"), g.want) {
			t.Errorf("%s: %v %v, want %q", name, problems, e, g.want)
		}
		if s, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: w.appDSN, InsecureTestOnly: true}); !errors.Is(e, r.ErrDenied) {
			if s != nil {
				s.Close()
			}
			t.Errorf("%s: the production store opened: %v", name, e)
		}
		if _, e := w.admin.Exec(ctx, g.take); e != nil {
			t.Fatal(name, e)
		}
	}
	if problems, e := postgres.RoleProblems(ctx, w.app); e != nil || len(problems) != 0 {
		t.Fatalf("the application role after the grants were restored: %v %v", problems, e)
	}
	if problems, e := postgres.SweeperProblems(ctx, w.sweep); e != nil || len(problems) != 0 {
		t.Fatalf("the sweep role: %v %v", problems, e)
	}
	if n := count(t, w.admin, all); n != 1 || digestOf(t, w.admin) != rows {
		t.Fatalf("a refused statement changed the store: %d cancellations", n)
	}
}
