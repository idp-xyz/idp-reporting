package authoritytest

import (
	"context"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type profileScenario struct {
	name string
	run  func(*testing.T, *Rig)
}
type ruleScenario struct {
	name string
	run  func(*testing.T, NewEnv)
}

var ctx = context.Background()

func sameReservation(t *testing.T, got, want r.IssueReservation) {
	t.Helper()
	if got.Ref != want.Ref || got.JobID != want.JobID || got.Number != want.Number || !got.IssuedAt.Equal(want.IssuedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("reservation changed: got %+v, want %+v", got, want)
	}
}

// near: the time is the simulator's clock, give or take the time the call took.
func near(t *testing.T, g *Rig, at time.Time, what string) {
	t.Helper()
	if d := at.Sub(g.Env.Now()); d < -5*time.Second || d > 5*time.Second {
		t.Fatalf("%s %s is not the simulator's time %s", what, at, g.Env.Now())
	}
}

// utcSecond: UTC and a whole second, as the contract says of the issue and confirmation times.
func utcSecond(t *testing.T, at time.Time, what string) {
	t.Helper()
	if at.Location() != time.UTC || !at.Equal(at.Truncate(time.Second)) {
		t.Fatalf("%s %s is not a whole second in UTC", what, at)
	}
}

var profileScenarios = []profileScenario{
	{"the_frozen_read_has_the_documented_rows", func(t *testing.T, g *Rig) {
		rows, cursor := [][]string{}, ""
		for {
			page, e := g.Src.ReadPage(ctx, g.SC, g.Prep, cursor, 50)
			if e != nil {
				t.Fatal(e)
			}
			rows = append(rows, page.Rows...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if len(rows) != len(g.P.RowSHA256) {
			t.Fatal("the fixture has", len(rows), "rows")
		}
		for i, row := range rows {
			if got := r.Digest(row); got != g.P.RowSHA256[i] {
				t.Fatalf("row %d %v has digest %s, want %s", i, row, got, g.P.RowSHA256[i])
			}
		}
	}},
	{"numbers_carry_the_prefix_and_count_from_one", func(t *testing.T, g *Rig) {
		for i := range g.P.RowSHA256 {
			res, e := g.Prepare(i+1, i)
			want := fmt.Sprintf("%s-00000%d", g.P.Prefix, i+1)
			if e != nil || res.Number != want || !r.ValidIssueNumber(res.Number) {
				t.Fatalf("row %d: %+v %v, want number %s", i, res, e, want)
			}
			g.Holds(i+1, contractsim.IssueReserved, want)
		}
	}},
}

var ruleScenarios = []ruleScenario{
	{"reservation_describes_the_job", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		// The platform refuses a reference that is not 1..128 printable ASCII characters, and a time that is not a
		// whole second between 1970 and 9999 (reporting.ValidProductRef, ValidProductTime): a simulator must give
		// what the worker accepts.
		if !r.ValidProductRef(res.Ref) || !r.ValidProductTime(res.IssuedAt) || res.JobID != Job(1) || res.Number != "AR-000001" {
			t.Fatalf("%+v", res)
		}
		utcSecond(t, res.IssuedAt, "issue time")
		near(t, g, res.IssuedAt, "issue time")
		// ten minutes after the issue time, or the end of the source read, whichever is first
		want := res.IssuedAt.Add(10 * time.Minute)
		if e := g.Prep.ExpiresAt.Truncate(time.Microsecond); e.Before(want) {
			want = e
		}
		if res.ExpiresAt.Location() != time.UTC || !res.ExpiresAt.Equal(want) || !res.ExpiresAt.After(res.IssuedAt) {
			t.Fatalf("expires %s, issued %s, read expires %s, want %s", res.ExpiresAt, res.IssuedAt, g.Prep.ExpiresAt, want)
		}
		rec := g.Holds(1, contractsim.IssueReserved, "AR-000001")
		if rec.Ref != res.Ref || rec.JobID != Job(1) || rec.SHA256 != "" || rec.Pages != 0 || rec.Reason != "" {
			t.Fatalf("%+v", rec)
		}
	}},
	{"numbers_are_consecutive_in_reservation_order", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for i, want := range []string{"AR-000001", "AR-000002", "AR-000003"} {
			if res := g.Reserve(i + 1); res.Number != want {
				t.Fatalf("job %d got %s, want %s", i+1, res.Number, want)
			}
		}
		// the third job asked first; the numbers follow the order of reservation, not of job ids
		g = NewRig(t, newEnv, Finance())
		for i, n := range []int{3, 1, 2} {
			if res := g.Reserve(n); res.Number != fmt.Sprintf("AR-00000%d", i+1) {
				t.Fatalf("job %d got %s", n, res.Number)
			}
		}
	}},
	{"refused_calls_consume_no_number", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		wrongRow := g.Intent(1, 0)
		wrongRow.RowSHA256 = g.P.RowSHA256[1]
		malformed := g.Intent(1, 0)
		malformed.TemplateVersion = 0
		_, e := g.Src.PrepareDocument(ctx, g.SC, wrongRow)
		is(t, e, r.ErrDenied, "wrong row")
		_, e = g.Src.PrepareDocument(ctx, g.SC, malformed)
		is(t, e, r.ErrInvalid, "malformed intent")
		g.Env.SetRefused(true)
		_, e = g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "not approved")
		g.Env.SetRefused(false)
		noIssue := g.P.Grant
		noIssue.IssueDocuments = false
		g.Env.SetGrant(g.SC, noIssue)
		_, e = g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "no right to issue")
		g.Env.SetGrant(g.SC, g.P.Grant)
		g.Env.SetUnavailable(true)
		_, e = g.Prepare(1, 0)
		is(t, e, r.ErrUnavailable, "outage")
		g.Env.SetUnavailable(false)
		if _, ok := g.Env.Record(Job(1)); ok {
			t.Fatal("a refused call left a record")
		}
		if res := g.Reserve(1); res.Number != "AR-000001" {
			t.Fatal("refused calls used numbers:", res.Number)
		}
		if res := g.Reserve(2); res.Number != "AR-000002" {
			t.Fatal(res.Number)
		}
	}},
	{"numbers_belong_to_the_integration", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance(), OtherIntegration)
		other := g.Context(OtherIntegration)
		otherRead := g.Read(other)
		prepareOther := func(n int) (r.IssueReservation, error) {
			in := g.Intent(n, 0)
			in.Prepared = otherRead
			return g.Src.PrepareDocument(ctx, other, in)
		}
		// the same job id in two integrations is two jobs, each with its own sequence
		a1 := g.Reserve(1)
		b1, e := prepareOther(1)
		if e != nil || a1.Number != "AR-000001" || b1.Number != "AR-000001" || a1.Ref == b1.Ref {
			t.Fatal(a1, b1, e)
		}
		if a2 := g.Reserve(2); a2.Number != "AR-000002" {
			t.Fatal(a2)
		}
		if b2, e := prepareOther(2); e != nil || b2.Number != "AR-000002" {
			t.Fatal(b2, e)
		}
		// a reference belongs to its integration's job
		c := g.Confirmation(b1)
		_, e = g.Src.ConfirmIssue(ctx, g.SC, c)
		is(t, e, r.ErrConflict, "the other integration's reference")
		if _, e = g.Src.ConfirmIssue(ctx, other, c); e != nil {
			t.Fatal(e)
		}
	}},
	{"numbers_are_not_reused_after_a_void", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Reserve(1)
		if e := g.Src.VoidIssue(ctx, g.SC, Job(1), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		if res := g.Reserve(2); res.Number != "AR-000002" {
			t.Fatal("a voided number was handed out again:", res.Number)
		}
		// a confirmed document that is voided keeps its number as well
		g.Confirm(g.Reserve(3))
		if e := g.Src.VoidIssue(ctx, g.SC, Job(3), "ISSUE_UNRECONCILED"); e != nil {
			t.Fatal(e)
		}
		if res := g.Reserve(4); res.Number != "AR-000004" {
			t.Fatal(res.Number)
		}
		// and the voided job stays voided
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrConflict, "job asked again after its void")
		g.Holds(1, contractsim.IssueVoid, "AR-000001")
		g.Holds(3, contractsim.IssueVoid, "AR-000003")
	}},

	// PrepareDocument is idempotent by job id.
	{"the_same_intent_returns_the_same_reservation", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		first := g.Reserve(1)
		again, e := g.Prepare(1, 0)
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, first)
		if res := g.Reserve(2); res.Number != "AR-000002" {
			t.Fatal("asking again used a number:", res.Number)
		}
		// and a confirmed one is returned as it was reserved
		g.Confirm(first)
		again, e = g.Prepare(1, 0)
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, first)
	}},
	{"another_intent_under_the_job_conflicts", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		differences := map[string]func(*r.IssueIntent){
			"row":              func(in *r.IssueIntent) { in.RowIndex, in.RowSHA256 = 1, g.P.RowSHA256[1] },
			"template":         func(in *r.IssueIntent) { in.TemplateID = "fedcba9876543210fedcba9876543210" },
			"template version": func(in *r.IssueIntent) { in.TemplateVersion = 4 },
			"template digest":  func(in *r.IssueIntent) { in.TemplateSHA256 = strings.Repeat("b", 64) },
		}
		for _, state := range []string{contractsim.IssueReserved, contractsim.IssueConfirmed} {
			for name, change := range differences {
				in := g.Intent(1, 0)
				change(&in)
				_, e := g.Src.PrepareDocument(ctx, g.SC, in)
				is(t, e, r.ErrConflict, name+" of a "+state+" job")
			}
			if state == contractsim.IssueReserved {
				g.Confirm(res)
			}
		}
		again, e := g.Prepare(1, 0)
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, res)
		g.Holds(1, contractsim.IssueConfirmed, "AR-000001")
	}},
	{"a_voided_reservation_conflicts", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Reserve(1)
		if e := g.Src.VoidIssue(ctx, g.SC, Job(1), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrConflict, "voided reservation")
	}},
	{"an_expired_unconfirmed_reservation_is_denied", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Reserve(1)
		g.Env.Expire(Job(1))
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "expired reservation")
		g.Holds(1, contractsim.IssueReserved, "AR-000001")
		// its number stays taken
		if res := g.Reserve(2); res.Number != "AR-000002" {
			t.Fatal(res.Number)
		}
	}},
	{"a_confirmed_reservation_is_returned_after_its_expiry", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		g.Confirm(res)
		g.Env.Expire(Job(1))
		again, e := g.Prepare(1, 0)
		if e != nil {
			t.Fatal(e)
		}
		// the reservation as it was (a simulator that moved the expiry to expire it says so)
		if again.Ref != res.Ref || again.JobID != res.JobID || again.Number != res.Number || !again.IssuedAt.Equal(res.IssuedAt) {
			t.Fatalf("reservation changed: got %+v, want %+v", again, res)
		}
		if again.ExpiresAt.After(g.Env.Now()) {
			t.Fatalf("the reservation has not expired: %s, now %s", again.ExpiresAt, g.Env.Now())
		}
	}},

	// The product's own checks.
	{"a_grant_without_issue_documents_is_denied", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		noIssue := g.P.Grant
		noIssue.IssueDocuments = false
		g.Env.SetGrant(g.SC, noIssue)
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "grant without IssueDocuments")
		if e = g.Src.AuthorizeCurrent(ctx, g.SC, g.Prep); e != nil {
			t.Fatal("the read must stay authorized:", e)
		}
		g.Env.SetGrant(g.SC, g.P.Grant)
		if res := g.Reserve(1); res.Number != "AR-000001" {
			t.Fatal(res.Number)
		}
		// a principal that never held the right: its own read, its own grant
		g = Bare(t, newEnv, Finance())
		g.Env.SetGrant(g.SC, noIssue)
		g.Prep = g.Read(g.SC)
		_, e = g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "grant that never had IssueDocuments")
	}},
	{"a_revoked_principal_is_denied_even_for_an_existing_reservation", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		g.Env.Revoke(g.SC)
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "revoked principal, existing job")
		_, e = g.Prepare(2, 0)
		is(t, e, r.ErrDenied, "revoked principal, new job")
		g.Env.SetGrant(g.SC, g.P.Grant)
		again, e := g.Prepare(1, 0)
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, res)
	}},
	{"the_approval_switch_refuses_as_not_approved", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Env.SetRefused(true)
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "not approved")
		if _, ok := g.Env.Record(Job(1)); ok {
			t.Fatal("a refusal left a record")
		}
		g.Env.SetRefused(false)
		res := g.Reserve(1)
		// the switch applies to every call, so to a job that is reserved already
		g.Env.SetRefused(true)
		_, e = g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "not approved, existing job")
		g.Env.SetRefused(false)
		again, e := g.Prepare(1, 0)
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, res)
	}},
	{"the_context_must_be_the_intents", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		// a principal who may do everything, but is not the one the read was made for
		colleague := g.SC
		colleague.Owner.Subject = "user_b"
		g.Env.SetGrant(colleague, g.P.Grant)
		_, e := g.Src.PrepareDocument(ctx, colleague, g.Intent(1, 0))
		is(t, e, r.ErrDenied, "other principal")
		elsewhere := g.SC
		elsewhere.SourceTenant = "tenant_b"
		_, e = g.Src.PrepareDocument(ctx, elsewhere, g.Intent(1, 0))
		is(t, e, r.ErrDenied, "other source tenant")
		elsewhere = g.SC
		elsewhere.Owner.Integration = "elsewhere"
		_, e = g.Src.PrepareDocument(ctx, elsewhere, g.Intent(1, 0))
		is(t, e, r.ErrDenied, "other integration")
		if _, ok := g.Env.Record(Job(1)); ok {
			t.Fatal("a refusal left a record")
		}
	}},
	{"the_source_read_must_be_the_products_own", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		unknown := g.Intent(1, 0)
		unknown.Prepared.Ref = r.NewID()
		_, e := g.Src.PrepareDocument(ctx, g.SC, unknown)
		is(t, e, r.ErrDenied, "read the product never made")
		// a read that does not match what the product froze fails as AuthorizeCurrent says
		forged := g.Intent(1, 0)
		forged.Prepared.ScopeHandle = "forged"
		want := g.Src.AuthorizeCurrent(ctx, g.SC, forged.Prepared)
		if want == nil {
			t.Fatal("a forged read is authorized")
		}
		_, e = g.Src.PrepareDocument(ctx, g.SC, forged)
		is(t, e, want, "forged read")
	}},
	{"the_source_read_must_still_be_authorized", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		narrower := g.P.Grant
		narrower.Rows = g.P.Grant.Rows[:2]
		g.Env.SetGrant(g.SC, narrower)
		want := g.Src.AuthorizeCurrent(ctx, g.SC, g.Prep)
		is(t, want, r.ErrDenied, "the read after the grant lost a row")
		_, e := g.Prepare(1, 0)
		is(t, e, want, "read no longer authorized")
		g.Env.SetGrant(g.SC, g.P.Grant)
		g.Reserve(1)
	}},

	// The row check.
	{"the_row_must_be_the_one_the_frozen_read_produced", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		bad := map[string]func(*r.IssueIntent){
			"another row's digest":       func(in *r.IssueIntent) { in.RowSHA256 = g.P.RowSHA256[1] },
			"a digest of nothing":        func(in *r.IssueIntent) { in.RowSHA256 = strings.Repeat("0", 64) },
			"the first index past":       func(in *r.IssueIntent) { in.RowIndex = len(g.P.RowSHA256) },
			"an index far past":          func(in *r.IssueIntent) { in.RowIndex = 1 << 30 },
			"another row's index":        func(in *r.IssueIntent) { in.RowIndex = 1 },
			"a digest of something else": func(in *r.IssueIntent) { in.RowSHA256 = r.Digest("AR-000001") },
		}
		for name, change := range bad {
			in := g.Intent(1, 0)
			change(&in)
			_, e := g.Src.PrepareDocument(ctx, g.SC, in)
			is(t, e, r.ErrDenied, name)
		}
		if _, ok := g.Env.Record(Job(1)); ok {
			t.Fatal("a refusal left a record")
		}
		// every row of the read is accepted with its own digest
		for i := range g.P.RowSHA256 {
			if _, e := g.Prepare(i+1, i); e != nil {
				t.Fatal("row", i, e)
			}
		}
	}},
	{"the_row_is_the_frozen_reads_not_the_current_data", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Env.ReplaceRecords([]contractsim.Record{{Company: "company_demo", Key: "r1", Values: []string{"changed", "current", "999.00"}}})
		// the product's current data has one row, ["changed","current","999.00"]; the frozen read has three
		if _, e := g.Prepare(1, 0); e != nil {
			t.Fatal("row 0 of the frozen read:", e)
		}
		if _, e := g.Prepare(2, 2); e != nil {
			t.Fatal("row 2 of the frozen read, which the current data lacks:", e)
		}
		now := g.Intent(3, 0)
		now.RowSHA256 = "5c1d1783e733f19dd730d8ab67de3842faf05e24c8b8b6e419ed7de8a5aad7d3"
		_, e := g.Src.PrepareDocument(ctx, g.SC, now)
		is(t, e, r.ErrDenied, "the current data's row")
	}},
	{"rows_beyond_the_first_source_pages_are_checked", func(t *testing.T, newEnv NewEnv) {
		g := Bare(t, newEnv, Finance())
		grant, data := g.P.Grant, []contractsim.Record{}
		grant.Rows = []string{}
		for i := 0; i < 250; i++ {
			key := fmt.Sprintf("row_%04d", i)
			grant.Rows = append(grant.Rows, key)
			data = append(data, contractsim.Record{Company: "company_demo", Key: key, Values: []string{key, "current", "1.00"}})
		}
		g.Env.ReplaceRecords(data)
		g.Env.SetGrant(g.SC, grant)
		g.Prep = g.Read(g.SC)
		// sha256 of ["row_0099","current","1.00"], ["row_0100",...], ["row_0230",...] and ["row_0249",...]
		digests := map[int]string{99: "3d68b22bdbd7aa4fc6018b4a796fe9e16385f2a0c1ede11407383254722e3c44", 100: "841a61529a6e8879caa683f86eceef168e3f3927a3fc6eaa3e12f530d8262ece", 230: "1fce181769c90c76f6422cc6a53648fcdaa3ad604f06320ec76e4a1f44fe7f6c", 249: "9e262de84248c66ec2f2cf662d07969e180cec3e5a8bf12ab0e5539388ce08d3"}
		n := 0
		for index, digest := range digests {
			n++
			in := g.Intent(n, 0)
			in.RowIndex, in.RowSHA256 = index, digest
			if _, e := g.Src.PrepareDocument(ctx, g.SC, in); e != nil {
				t.Fatal("row", index, e)
			}
			in = g.Intent(n+10, 0)
			in.RowIndex, in.RowSHA256 = index+1, digest
			_, e := g.Src.PrepareDocument(ctx, g.SC, in)
			is(t, e, r.ErrDenied, fmt.Sprintf("row %d with the digest of row %d", index+1, index))
		}
	}},

	// Input validation.
	{"an_intent_that_is_not_well_formed_is_invalid", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		const jobWithLetters = "abcdefabcdefabcdefabcdefabcdefab"
		bad := map[string]func(*r.IssueIntent){
			"job id empty":                 func(in *r.IssueIntent) { in.JobID = "" },
			"job id too short":             func(in *r.IssueIntent) { in.JobID = jobWithLetters[:31] },
			"job id too long":              func(in *r.IssueIntent) { in.JobID = jobWithLetters + "a" },
			"job id uppercase":             func(in *r.IssueIntent) { in.JobID = strings.ToUpper(jobWithLetters) },
			"job id not hexadecimal":       func(in *r.IssueIntent) { in.JobID = strings.Repeat("g", 32) },
			"row index negative":           func(in *r.IssueIntent) { in.RowIndex = -1 },
			"row digest too short":         func(in *r.IssueIntent) { in.RowSHA256 = in.RowSHA256[:63] },
			"row digest uppercase":         func(in *r.IssueIntent) { in.RowSHA256 = strings.ToUpper(in.RowSHA256) },
			"row digest not hexadecimal":   func(in *r.IssueIntent) { in.RowSHA256 = strings.Repeat("z", 64) },
			"template id empty":            func(in *r.IssueIntent) { in.TemplateID = "" },
			"template id too short":        func(in *r.IssueIntent) { in.TemplateID = in.TemplateID[:31] },
			"template id not hexadecimal":  func(in *r.IssueIntent) { in.TemplateID = strings.Repeat("g", 32) },
			"template version zero":        func(in *r.IssueIntent) { in.TemplateVersion = 0 },
			"template version negative":    func(in *r.IssueIntent) { in.TemplateVersion = -1 },
			"template digest too short":    func(in *r.IssueIntent) { in.TemplateSHA256 = in.TemplateSHA256[:63] },
			"template digest not hex":      func(in *r.IssueIntent) { in.TemplateSHA256 = strings.Repeat("z", 64) },
			"template digest empty":        func(in *r.IssueIntent) { in.TemplateSHA256 = "" },
			"job id with a trailing space": func(in *r.IssueIntent) { in.JobID = jobWithLetters[:31] + " " },
		}
		for name, change := range bad {
			in := g.Intent(1, 0)
			change(&in)
			_, e := g.Src.PrepareDocument(ctx, g.SC, in)
			is(t, e, r.ErrInvalid, name)
		}
	}},

	// ConfirmIssue.
	{"confirming_binds_the_file_to_the_reserved_number", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		rc := g.Confirm(res)
		if !r.ValidProductRef(rc.Ref) || !r.ValidProductTime(rc.ConfirmedAt) || rc.Ref == res.Ref {
			t.Fatalf("%+v", rc)
		}
		utcSecond(t, rc.ConfirmedAt, "confirmation time")
		near(t, g, rc.ConfirmedAt, "confirmation time")
		if rc.ConfirmedAt.Before(res.IssuedAt) {
			t.Fatal("confirmed before it was issued")
		}
		rec := g.Holds(1, contractsim.IssueConfirmed, "AR-000001")
		c := g.Confirmation(res)
		if rec.SHA256 != c.SHA256 || rec.Pages != c.Pages || rec.Ref != res.Ref || rec.ConfirmationRef != rc.Ref || !rec.ConfirmedAt.Equal(rc.ConfirmedAt) {
			t.Fatalf("%+v", rec)
		}
	}},
	{"confirming_again_returns_the_same_receipt", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		first := g.Confirm(res)
		again := g.Confirm(res)
		if again.Ref != first.Ref || !again.ConfirmedAt.Equal(first.ConfirmedAt) {
			t.Fatalf("receipt changed: %+v then %+v", first, again)
		}
	}},
	{"confirming_another_file_conflicts", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		first := g.Confirm(res)
		other := g.Confirmation(res)
		other.SHA256 = strings.Repeat("c", 64)
		_, e := g.Src.ConfirmIssue(ctx, g.SC, other)
		is(t, e, r.ErrConflict, "another digest")
		other = g.Confirmation(res)
		other.Pages = 3
		_, e = g.Src.ConfirmIssue(ctx, g.SC, other)
		is(t, e, r.ErrConflict, "another page count")
		rec := g.Holds(1, contractsim.IssueConfirmed, "AR-000001")
		if rec.SHA256 != g.Confirmation(res).SHA256 || rec.Pages != 2 || rec.ConfirmationRef != first.Ref {
			t.Fatalf("a refused confirmation changed the record: %+v", rec)
		}
	}},
	{"confirming_needs_the_reservations_reference", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		for _, ref := range []string{"", "ffffffffffffffffffffffffffffffff", res.Ref + "0", strings.ToUpper(res.Ref + "a")} {
			c := g.Confirmation(res)
			c.ReservationRef = ref
			_, e := g.Src.ConfirmIssue(ctx, g.SC, c)
			is(t, e, r.ErrConflict, "reference "+ref)
		}
		g.Holds(1, contractsim.IssueReserved, "AR-000001")
		g.Confirm(res)
	}},
	{"confirming_an_unknown_job_is_not_found", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		c := g.Confirmation(res)
		c.JobID = Job(2)
		_, e := g.Src.ConfirmIssue(ctx, g.SC, c)
		is(t, e, r.ErrNotFound, "job that was never reserved")
	}},
	{"a_confirmation_that_is_not_well_formed_is_invalid", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		bad := map[string]func(*r.IssueConfirmation){
			"job id short":            func(c *r.IssueConfirmation) { c.JobID = c.JobID[:31] },
			"job id uppercase":        func(c *r.IssueConfirmation) { c.JobID = strings.ToUpper(strings.Repeat("ab", 16)) },
			"job id not hexadecimal":  func(c *r.IssueConfirmation) { c.JobID = strings.Repeat("g", 32) },
			"digest empty":            func(c *r.IssueConfirmation) { c.SHA256 = "" },
			"digest short":            func(c *r.IssueConfirmation) { c.SHA256 = c.SHA256[:63] },
			"digest uppercase":        func(c *r.IssueConfirmation) { c.SHA256 = strings.ToUpper(strings.Repeat("ab", 32)) },
			"digest not hexadecimal":  func(c *r.IssueConfirmation) { c.SHA256 = strings.Repeat("z", 64) },
			"no pages":                func(c *r.IssueConfirmation) { c.Pages = 0 },
			"negative pages":          func(c *r.IssueConfirmation) { c.Pages = -1 },
			"more pages than allowed": func(c *r.IssueConfirmation) { c.Pages = 101 },
		}
		for name, change := range bad {
			c := g.Confirmation(res)
			change(&c)
			_, e := g.Src.ConfirmIssue(ctx, g.SC, c)
			is(t, e, r.ErrInvalid, name)
		}
		g.Holds(1, contractsim.IssueReserved, "AR-000001")
		// the limits themselves are valid
		for _, pages := range []int{1, 100} {
			g = NewRig(t, newEnv, Finance())
			c := g.Confirmation(g.Reserve(1))
			c.Pages = pages
			if _, e := g.Src.ConfirmIssue(ctx, g.SC, c); e != nil {
				t.Fatal(pages, "pages:", e)
			}
		}
	}},
	{"confirming_a_voided_reservation_conflicts", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		if e := g.Src.VoidIssue(ctx, g.SC, Job(1), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		_, e := g.Src.ConfirmIssue(ctx, g.SC, g.Confirmation(res))
		is(t, e, r.ErrConflict, "voided reservation")
		// a confirmed document that was voided cannot be confirmed again either
		res = g.Reserve(2)
		g.Confirm(res)
		if e = g.Src.VoidIssue(ctx, g.SC, Job(2), "ISSUE_UNRECONCILED"); e != nil {
			t.Fatal(e)
		}
		_, e = g.Src.ConfirmIssue(ctx, g.SC, g.Confirmation(res))
		is(t, e, r.ErrConflict, "voided confirmed document")
	}},
	{"an_expired_reservation_cannot_be_confirmed", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		g.Env.Expire(Job(1))
		_, e := g.Src.ConfirmIssue(ctx, g.SC, g.Confirmation(res))
		is(t, e, r.ErrDenied, "expired reservation")
		g.Holds(1, contractsim.IssueReserved, "AR-000001")
	}},
	{"a_confirmed_reservation_is_confirmed_again_after_its_expiry", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		first := g.Confirm(res)
		g.Env.Expire(Job(1))
		again := g.Confirm(res)
		if again.Ref != first.Ref || !again.ConfirmedAt.Equal(first.ConfirmedAt) {
			t.Fatalf("receipt changed: %+v then %+v", first, again)
		}
	}},
	{"confirming_does_not_check_the_principal_again", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		first, second, third := g.Reserve(1), g.Reserve(2), g.Reserve(3)
		noIssue := g.P.Grant
		noIssue.IssueDocuments = false
		g.Env.SetGrant(g.SC, noIssue)
		g.Confirm(first)
		g.Env.Revoke(g.SC)
		g.Confirm(second)
		g.Env.SetRefused(true)
		g.Confirm(third)
	}},
	{"confirming_is_scoped_to_the_source", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance(), OtherIntegration)
		res := g.Reserve(1)
		for name, sc := range map[string]r.SourceContext{"other integration": g.Context(OtherIntegration), "other source tenant": {Owner: g.SC.Owner, SourceTenant: "tenant_b"}} {
			_, e := g.Src.ConfirmIssue(ctx, sc, g.Confirmation(res))
			is(t, e, r.ErrNotFound, name)
		}
		g.Holds(1, contractsim.IssueReserved, "AR-000001")
	}},

	// VoidIssue.
	// A void is final for the job id: a void of a job the source holds nothing for is recorded, as
	// a tombstone with no reservation and no number.
	{"voiding_a_job_the_source_holds_nothing_for_records_a_tombstone", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		if e := g.Src.VoidIssue(ctx, g.SC, Job(7), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		rec := g.Holds(7, contractsim.IssueVoid, "")
		if !rec.Tombstone() || rec.Reason != "EXECUTION_FAILED" || rec.Integration != g.SC.Owner.Integration || rec.SourceTenant != SourceTenant || rec.JobID != Job(7) {
			t.Fatalf("%+v", rec)
		}
		if rec.Ref != "" || rec.IntentSHA256 != "" || rec.SHA256 != "" || rec.Pages != 0 || rec.ConfirmationRef != "" || !rec.IssuedAt.IsZero() || !rec.ExpiresAt.IsZero() || !rec.ConfirmedAt.IsZero() {
			t.Fatalf("a tombstone holds a reservation: %+v", rec)
		}
		utcSecond(t, rec.VoidedAt, "void time")
		near(t, g, rec.VoidedAt, "void time")
	}},
	{"a_voided_job_id_cannot_be_prepared_and_uses_no_number", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		if e := g.Src.VoidIssue(ctx, g.SC, Job(7), "TEMPLATE_NOT_PUBLISHED"); e != nil {
			t.Fatal(e)
		}
		_, e := g.Prepare(7, 0)
		is(t, e, r.ErrConflict, "job voided before it was ever reserved")
		_, e = g.Prepare(7, 1)
		is(t, e, r.ErrConflict, "another intent of the voided job")
		// the checks still come first, as for a voided reservation: a revoked principal is denied
		g.Env.Revoke(g.SC)
		_, e = g.Prepare(7, 0)
		is(t, e, r.ErrDenied, "revoked principal, voided job")
		g.Env.SetGrant(g.SC, g.P.Grant)
		_, e = g.Prepare(7, 0)
		is(t, e, r.ErrConflict, "restored principal, voided job")
		// the refusals took no number, and the void stays a void without one
		if res := g.Reserve(1); res.Number != "AR-000001" {
			t.Fatal("the voided job used a number:", res.Number)
		}
		rec := g.Holds(7, contractsim.IssueVoid, "")
		if rec.Reason != "TEMPLATE_NOT_PUBLISHED" {
			t.Fatalf("%+v", rec)
		}
	}},
	{"a_voided_job_id_cannot_be_confirmed", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		if e := g.Src.VoidIssue(ctx, g.SC, Job(7), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		// the source holds the void, so this is a conflict and not a job it does not know; a
		// tombstone has no reservation, so whatever reference a worker brings does not match
		for name, ref := range map[string]string{"a reference": strings.Repeat("a", 32), "no reference": ""} {
			c := g.Confirmation(r.IssueReservation{JobID: Job(7), Ref: ref})
			_, e := g.Src.ConfirmIssue(ctx, g.SC, c)
			is(t, e, r.ErrConflict, "confirming a voided job id with "+name)
		}
		g.Holds(7, contractsim.IssueVoid, "")
	}},
	{"voiding_a_tombstone_again_keeps_the_first_reason", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for _, reason := range []string{"first", "second"} {
			if e := g.Src.VoidIssue(ctx, g.SC, Job(7), reason); e != nil {
				t.Fatal(e)
			}
		}
		if rec := g.Holds(7, contractsim.IssueVoid, ""); rec.Reason != "first" {
			t.Fatalf("%+v", rec)
		}
	}},
	{"the_lookup_prefers_a_reservation_to_a_tombstone_of_the_same_job_id", func(t *testing.T, newEnv NewEnv) {
		const sortsFirst = "aaa-contract-demo" // sorts before the principal's integration
		g := NewRig(t, newEnv, Finance(), sortsFirst)
		res := g.Reserve(1)
		if e := g.Src.VoidIssue(ctx, g.Context(sortsFirst), Job(1), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		rec := g.Holds(1, contractsim.IssueReserved, "AR-000001")
		if rec.Ref != res.Ref || rec.Integration != g.SC.Owner.Integration {
			t.Fatalf("%+v", rec)
		}
	}},
	{"a_tombstone_belongs_to_its_source", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance(), OtherIntegration)
		other := g.Context(OtherIntegration)
		if e := g.Src.VoidIssue(ctx, g.SC, Job(7), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		if rec := g.Holds(7, contractsim.IssueVoid, ""); rec.Integration != g.SC.Owner.Integration {
			t.Fatalf("%+v", rec)
		}
		// the same job id in the other integration is another job, free to be reserved
		in := g.Intent(7, 0)
		in.Prepared = g.Read(other)
		res, e := g.Src.PrepareDocument(ctx, other, in)
		if e != nil || res.Number != "AR-000001" {
			t.Fatal(res, e)
		}
		_, e = g.Prepare(7, 0)
		is(t, e, r.ErrConflict, "the integration that was told to void it")
	}},
	{"voiding_a_reservation_records_the_reason", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Reserve(1)
		if e := g.Src.VoidIssue(ctx, g.SC, Job(1), "TEMPLATE_NOT_PUBLISHED"); e != nil {
			t.Fatal(e)
		}
		rec := g.Holds(1, contractsim.IssueVoid, "AR-000001")
		if rec.Reason != "TEMPLATE_NOT_PUBLISHED" || rec.SHA256 != "" || rec.ConfirmationRef != "" {
			t.Fatalf("%+v", rec)
		}
		utcSecond(t, rec.VoidedAt, "void time")
		near(t, g, rec.VoidedAt, "void time")
	}},
	{"voiding_twice_keeps_the_first_reason", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Reserve(1)
		for _, reason := range []string{"first", "second"} {
			if e := g.Src.VoidIssue(ctx, g.SC, Job(1), reason); e != nil {
				t.Fatal(e)
			}
		}
		if rec := g.Holds(1, contractsim.IssueVoid, "AR-000001"); rec.Reason != "first" {
			t.Fatalf("%+v", rec)
		}
	}},
	{"voiding_a_confirmed_document_voids_it", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		g.Confirm(res)
		if e := g.Src.VoidIssue(ctx, g.SC, Job(1), "ISSUE_UNRECONCILED"); e != nil {
			t.Fatal(e)
		}
		rec := g.Holds(1, contractsim.IssueVoid, "AR-000001")
		if rec.Reason != "ISSUE_UNRECONCILED" || rec.SHA256 != g.Confirmation(res).SHA256 || rec.Pages != 2 {
			t.Fatalf("a voided confirmation lost its history: %+v", rec)
		}
	}},
	{"voiding_does_not_check_the_principal", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Reserve(1)
		g.Env.Revoke(g.SC)
		g.Env.SetRefused(true)
		if e := g.Src.VoidIssue(ctx, g.SC, Job(1), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		g.Holds(1, contractsim.IssueVoid, "AR-000001")
		// nor does the void of a job the source holds nothing for
		if e := g.Src.VoidIssue(ctx, g.SC, Job(2), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		g.Holds(2, contractsim.IssueVoid, "")
	}},
	{"an_expired_reservation_can_be_voided", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Reserve(1)
		g.Env.Expire(Job(1))
		if e := g.Src.VoidIssue(ctx, g.SC, Job(1), "LEASE_LOST"); e != nil {
			t.Fatal(e)
		}
		g.Holds(1, contractsim.IssueVoid, "AR-000001")
	}},
	{"void_reasons_are_cut_to_200_bytes_at_a_rune_boundary", func(t *testing.T, newEnv NewEnv) {
		cases := []struct{ name, reason, want string }{
			{"short", "INTEGRITY_FAILURE", "INTEGRITY_FAILURE"},
			{"empty", "", ""},
			{"exactly 200 bytes", strings.Repeat("x", 200), strings.Repeat("x", 200)},
			{"ascii over", strings.Repeat("x", 300), strings.Repeat("x", 200)},
			{"two-byte runes on the boundary", strings.Repeat("é", 150), strings.Repeat("é", 100)},
			{"three-byte runes across the boundary", "a" + strings.Repeat("世", 100), "a" + strings.Repeat("世", 66)},
			{"four-byte runes across the boundary", "ab" + strings.Repeat("😀", 60), "ab" + strings.Repeat("😀", 49)},
			{"invalid UTF-8", "ab\xffcd", "ab\uFFFDcd"},
			{"NUL", "a\x00b", "ab"},
		}
		for i, c := range cases {
			// the reason of a reservation that is voided and of a tombstone are cut alike
			for _, reserved := range []bool{true, false} {
				g := NewRig(t, newEnv, Finance())
				if reserved {
					g.Reserve(1)
				}
				if e := g.Src.VoidIssue(ctx, g.SC, Job(1), c.reason); e != nil {
					t.Fatal(c.name, e)
				}
				got := g.Record(1).Reason
				if got != c.want || len(got) > 200 || !utf8.ValidString(got) {
					t.Fatalf("case %d %s (reserved %v): kept %d bytes %q", i, c.name, reserved, len(got), got)
				}
			}
		}
	}},
	{"a_job_id_that_is_not_well_formed_cannot_be_voided", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		g.Reserve(1)
		for name, id := range map[string]string{"empty": "", "short": Job(1)[:31], "uppercase": strings.ToUpper(strings.Repeat("ab", 16)), "not hexadecimal": strings.Repeat("g", 32), "long": Job(1) + "0"} {
			is(t, g.Src.VoidIssue(ctx, g.SC, id, "x"), r.ErrInvalid, name)
			if _, ok := g.Env.Record(id); ok {
				t.Fatal(name, "left a tombstone")
			}
		}
		g.Holds(1, contractsim.IssueReserved, "AR-000001")
	}},
	{"voiding_is_scoped_to_the_source", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance(), OtherIntegration)
		res := g.Reserve(1)
		other := g.Context(OtherIntegration)
		// the same job id in another source is another job; voiding it there leaves this one alone
		for name, sc := range map[string]r.SourceContext{"other integration": other, "other source tenant": {Owner: g.SC.Owner, SourceTenant: "tenant_b"}} {
			if e := g.Src.VoidIssue(ctx, sc, Job(1), "EXECUTION_FAILED"); e != nil {
				t.Fatal(name, e)
			}
		}
		again, e := g.Prepare(1, 0)
		if e != nil {
			t.Fatal("the job of this source was touched by a void of another source:", e)
		}
		sameReservation(t, again, res)
		g.Confirm(res)
		// ...while in the other integration the job id is void now
		in := g.Intent(1, 0)
		in.Prepared = g.Read(other)
		_, e = g.Src.PrepareDocument(ctx, other, in)
		is(t, e, r.ErrConflict, "the job id voided in the other integration")
	}},

	{"an_unavailable_source_refuses_every_call", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		res := g.Reserve(1)
		g.Env.SetUnavailable(true)
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrUnavailable, "prepare, existing job")
		_, e = g.Prepare(2, 0)
		is(t, e, r.ErrUnavailable, "prepare, new job")
		_, e = g.Src.ConfirmIssue(ctx, g.SC, g.Confirmation(res))
		is(t, e, r.ErrUnavailable, "confirm")
		is(t, g.Src.VoidIssue(ctx, g.SC, Job(1), "EXECUTION_FAILED"), r.ErrUnavailable, "void")
		is(t, g.Src.VoidIssue(ctx, g.SC, Job(9), "EXECUTION_FAILED"), r.ErrUnavailable, "void of a job nothing is held for")
		g.Env.SetUnavailable(false)
		// the outage changed nothing and used no number, and recorded no tombstone
		g.Holds(1, contractsim.IssueReserved, "AR-000001")
		if _, ok := g.Env.Record(Job(9)); ok {
			t.Fatal("a void refused during the outage was recorded")
		}
		g.Confirm(res)
		if second := g.Reserve(2); second.Number != "AR-000002" {
			t.Fatal(second.Number)
		}
		if third := g.Reserve(9); third.Number != "AR-000003" {
			t.Fatal(third.Number)
		}
	}},
}
