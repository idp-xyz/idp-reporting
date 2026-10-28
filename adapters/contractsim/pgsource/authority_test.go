package pgsource_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/internal/authoritytest"
	"idp.local/reporting/adapters/contractsim/pgsource"
	r "idp.local/reporting/reporting"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	ctx = context.Background()
	dsn string // the guarded source database, set by TestDurableDocumentAuthority
)

const schema = "reporting_source_s01."

// openSource opens the durable source with the schema created or upgraded and the fixture empty.
func openSource(t *testing.T) *pgsource.Source {
	t.Helper()
	s, e := pgsource.Open(ctx, dsn, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	if e = s.ResetFixture(ctx); e != nil {
		t.Fatal(e)
	}
	return s
}

// admin is a direct connection to the source database, for what a test does behind the source's
// back, destructive things included; it refuses any database but the guarded test databases.
func admin(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal("the source database admin could not open:", e)
	}
	t.Cleanup(p.Close)
	if name := p.Config().ConnConfig.Database; !strings.HasPrefix(name, "reporting_source_test_s01") {
		t.Fatal("the admin of", name, "is not for a guarded source database")
	}
	return p
}

// freshSchema drops the source schema and lets Open create it again, so that a test sees exactly
// what this code creates and not what an earlier run left in the database.
func freshSchema(t *testing.T) {
	t.Helper()
	exec(t, admin(t), "DROP SCHEMA IF EXISTS "+strings.TrimSuffix(schema, ".")+" CASCADE")
	s, e := pgsource.Open(ctx, dsn, true)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
}

func exec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, e := p.Exec(ctx, sql, args...); e != nil {
		t.Fatal(e)
	}
}
func scalar[T any](t *testing.T, p *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if e := p.QueryRow(ctx, sql, args...).Scan(&v); e != nil {
		t.Fatal(sql, e)
	}
	return v
}

// durableEnv is the durable source as the conformance suite drives it. Its clock is the
// database's; Expire moves a reservation's expiry into the past with SQL.
type durableEnv struct {
	t     *testing.T
	src   *pgsource.Source
	admin *pgxpool.Pool
	ids   []string
	build func() *contractsim.Simulator
}

func newDurableEnv(t *testing.T, p authoritytest.Profile, integrations ...string) authoritytest.Env {
	t.Helper()
	src := openSource(t)
	for _, id := range integrations {
		if e := src.EnsureFixture(ctx, id, authoritytest.SourceTenant, p.Build().Fixture()); e != nil {
			t.Fatal(e)
		}
	}
	return &durableEnv{t: t, src: src, admin: admin(t), ids: integrations, build: p.Build}
}
func (e *durableEnv) Source() authoritytest.Source { return e.src }
func (e *durableEnv) Now() time.Time               { return time.Now() }
func (e *durableEnv) SetGrant(sc r.SourceContext, g contractsim.Grant) {
	if err := e.src.SetGrant(ctx, sc, g); err != nil {
		e.t.Fatal(err)
	}
}
func (e *durableEnv) Revoke(sc r.SourceContext) {
	if err := e.src.Revoke(ctx, sc); err != nil {
		e.t.Fatal(err)
	}
}
func (e *durableEnv) SetUnavailable(v bool) {
	for _, id := range e.ids {
		if err := e.src.SetUnavailable(ctx, id, authoritytest.SourceTenant, v); err != nil {
			e.t.Fatal(err)
		}
	}
}
func (e *durableEnv) SetRefused(v bool) {
	for _, id := range e.ids {
		if err := e.src.SetIssueRefused(ctx, id, authoritytest.SourceTenant, v); err != nil {
			e.t.Fatal(err)
		}
	}
}
func (e *durableEnv) ReplaceRecords(records []contractsim.Record) {
	f := e.build().Fixture()
	f.Records = records
	for _, id := range e.ids {
		if err := e.src.ReplaceFixture(ctx, id, authoritytest.SourceTenant, f); err != nil {
			e.t.Fatal(err)
		}
	}
}
func (e *durableEnv) Expire(jobID string) {
	tag, err := e.admin.Exec(ctx, "UPDATE "+schema+"issues SET expires_at=clock_timestamp()-interval '1 minute' WHERE job_id=$1", jobID)
	if err != nil || tag.RowsAffected() != 1 {
		e.t.Fatal("no reservation to expire for job", jobID, err)
	}
}
func (e *durableEnv) Record(jobID string) (contractsim.IssueRecord, bool) {
	rec, ok, err := e.src.IssueRecord(ctx, jobID)
	if err != nil {
		e.t.Fatal(err)
	}
	return rec, ok
}

func sourceDSN(t *testing.T) string {
	t.Helper()
	v := os.Getenv("REPORTING_S01_SOURCE_DSN")
	if v == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required source PostgreSQL DSN missing")
		}
		t.Skip("durable source gate not run: REPORTING_S01_SOURCE_DSN required")
	}
	return v
}

// TestDurableDocumentAuthority runs the document-authority rules against the durable source: the
// conformance scenarios the in-memory simulator passes too, the concurrent ones, and what only
// the durable source has: its sequences and schema, its reopening and the upgrade of version 1.
func TestDurableDocumentAuthority(t *testing.T) {
	dsn = sourceDSN(t)
	// Schema version 2 was never released and changed in place (void tombstones): a test database
	// made by an earlier revision of this code is dropped, not upgraded.
	freshSchema(t)
	t.Run("conformance", func(t *testing.T) { authoritytest.Run(t, newDurableEnv) })
	t.Run("concurrency", func(t *testing.T) { authoritytest.RunConcurrent(t, newDurableEnv) })
	for _, s := range durableScenarios {
		t.Run(s.name, s.run)
	}
}

type scenario struct {
	name string
	run  func(*testing.T)
}

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func is(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}

// pgCode is the SQLSTATE of a database error, or "".
func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

var durableScenarios = []scenario{
	{"numbers_belong_to_the_source_tenant", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		src := g.Env.(*durableEnv).src
		// the same integration under a second source tenant is a second source with its own sequence
		other := *g
		other.SC.SourceTenant = "tenant_b"
		must(t, src.EnsureFixture(ctx, g.SC.Owner.Integration, "tenant_b", authoritytest.Finance().Build().Fixture()))
		must(t, src.SetGrant(ctx, other.SC, g.P.Grant))
		other.Prep = other.Read(other.SC)
		a1, b1, a2, b2 := g.Reserve(1), other.Reserve(2), g.Reserve(3), other.Reserve(4)
		for i, want := range []string{"AR-000001", "AR-000001", "AR-000002", "AR-000002"} {
			if got := []string{a1.Number, b1.Number, a2.Number, b2.Number}[i]; got != want {
				t.Fatalf("reservation %d: %s, want %s", i, got, want)
			}
		}
		// a job belongs to its source: the other tenant neither confirms nor voids it
		_, e := src.ConfirmIssue(ctx, other.SC, g.Confirmation(a1))
		is(t, e, r.ErrNotFound, "confirming in the other tenant")
		must(t, src.VoidIssue(ctx, other.SC, authoritytest.Job(1), "EXECUTION_FAILED"))
		g.Holds(1, contractsim.IssueReserved, "AR-000001")
		a := admin(t)
		for tenant, want := range map[string]int64{"tenant_a": 2, "tenant_b": 2} {
			if got := scalar[int64](t, a, "SELECT last_number FROM "+schema+"issue_numbers WHERE integration=$1 AND source_tenant=$2", g.SC.Owner.Integration, tenant); got != want {
				t.Fatal(tenant, "has given out", got, "numbers")
			}
		}
	}},
	{"ensuring_a_fixture_records_its_number_prefix", func(t *testing.T) {
		src := openSource(t)
		a := admin(t)
		idF := authoritytest.Finance().Integration
		idS := authoritytest.Shipment().Integration
		must(t, src.EnsureFixture(ctx, idF, authoritytest.SourceTenant, contractsim.Financial().Fixture()))
		must(t, src.EnsureFixture(ctx, idS, authoritytest.SourceTenant, contractsim.Shipment().Fixture()))
		prefix := func(id string) string {
			return scalar[string](t, a, "SELECT prefix FROM "+schema+"issue_numbers WHERE integration=$1 AND source_tenant='tenant_a'", id)
		}
		if prefix(idF) != "AR" || prefix(idS) != "SH" {
			t.Fatal(prefix(idF), prefix(idS))
		}
		// a fixture that names no prefix is numbered DOC
		plain := contractsim.Financial().Fixture()
		plain.NumberPrefix = ""
		must(t, src.EnsureFixture(ctx, "plain-contract-demo", authoritytest.SourceTenant, plain))
		if got := prefix("plain-contract-demo"); got != "DOC" {
			t.Fatal(got)
		}
		// a prefix that cannot make a valid number is refused and creates nothing
		for _, bad := range []string{"ar", "1A", "A-", "TOOLONGPFX", "A B", "É"} {
			f := contractsim.Financial().Fixture()
			f.NumberPrefix = bad
			is(t, src.EnsureFixture(ctx, "bad-prefix-demo", authoritytest.SourceTenant, f), r.ErrInvalid, "prefix "+bad)
			is(t, src.ReplaceFixture(ctx, idF, authoritytest.SourceTenant, f), r.ErrInvalid, "replacing with prefix "+bad)
		}
		if n := scalar[int64](t, a, "SELECT count(*) FROM "+schema+"heads WHERE integration='bad-prefix-demo'"); n != 0 {
			t.Fatal("a refused fixture was created")
		}
		// ensuring and replacing a fixture again never change the prefix, the sequence or the switch
		exec(t, a, "UPDATE "+schema+"issue_numbers SET last_number=7,refused=true WHERE integration=$1", idF)
		again := contractsim.Financial().Fixture()
		again.NumberPrefix = "XX"
		must(t, src.EnsureFixture(ctx, idF, authoritytest.SourceTenant, again))
		must(t, src.ReplaceFixture(ctx, idF, authoritytest.SourceTenant, again))
		if p, n, refused := prefix(idF), scalar[int64](t, a, "SELECT last_number FROM "+schema+"issue_numbers WHERE integration=$1", idF), scalar[bool](t, a, "SELECT refused FROM "+schema+"issue_numbers WHERE integration=$1", idF); p != "AR" || n != 7 || !refused {
			t.Fatal(p, n, refused)
		}
	}},
	{"resetting_the_fixture_clears_reservations_and_sequences", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		env := g.Env.(*durableEnv)
		g.Reserve(1)
		g.Reserve(2)
		must(t, env.src.VoidIssue(ctx, g.SC, authoritytest.Job(7), "EXECUTION_FAILED")) // a tombstone
		must(t, env.src.ResetFixture(ctx))
		for _, table := range []string{"issues", "issue_numbers"} {
			if n := scalar[int64](t, env.admin, "SELECT count(*) FROM "+schema+table); n != 0 {
				t.Fatal(table, "keeps", n, "rows")
			}
		}
		must(t, env.src.EnsureFixture(ctx, g.SC.Owner.Integration, authoritytest.SourceTenant, g.P.Build().Fixture()))
		must(t, env.src.SetGrant(ctx, g.SC, g.P.Grant))
		g.Prep = g.Read(g.SC)
		// the numbers start over and the job ids are free again, the voided one too
		if res := g.Reserve(1); res.Number != "AR-000001" {
			t.Fatal(res.Number)
		}
		if res := g.Reserve(7); res.Number != "AR-000002" {
			t.Fatal("the tombstone survived the reset:", res.Number)
		}
	}},
	{"reservations_survive_reopening_the_source", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		first := g.Reserve(1)
		receipt := g.Confirm(first)
		second := g.Reserve(2)
		must(t, g.Src.VoidIssue(ctx, g.SC, authoritytest.Job(7), "EXECUTION_FAILED")) // a tombstone
		g.Env.(*durableEnv).src.Close()
		reopened, e := pgsource.Open(ctx, dsn, false)
		must(t, e)
		defer reopened.Close()
		again, e := reopened.PrepareDocument(ctx, g.SC, g.Intent(1, 0))
		must(t, e)
		if again.Ref != first.Ref || again.Number != first.Number || !again.IssuedAt.Equal(first.IssuedAt) || !again.ExpiresAt.Equal(first.ExpiresAt) {
			t.Fatalf("reservation changed over a reopening: %+v then %+v", first, again)
		}
		same, e := reopened.ConfirmIssue(ctx, g.SC, g.Confirmation(first))
		if e != nil || same.Ref != receipt.Ref || !same.ConfirmedAt.Equal(receipt.ConfirmedAt) {
			t.Fatalf("receipt changed over a reopening: %+v then %+v %v", receipt, same, e)
		}
		back, e := reopened.PrepareDocument(ctx, g.SC, g.Intent(2, 0))
		if e != nil || back.Ref != second.Ref {
			t.Fatal(back, e)
		}
		// the sequence went on from where it was
		third, e := reopened.PrepareDocument(ctx, g.SC, g.Intent(3, 0))
		if e != nil || third.Number != "AR-000003" {
			t.Fatal(third, e)
		}
		// and a void of a job nothing was reserved for is still final
		_, e = reopened.PrepareDocument(ctx, g.SC, g.Intent(7, 0))
		is(t, e, r.ErrConflict, "prepare of a voided job id after reopening")
		_, e = reopened.ConfirmIssue(ctx, g.SC, g.Confirmation(r.IssueReservation{JobID: authoritytest.Job(7), Ref: first.Ref}))
		is(t, e, r.ErrConflict, "confirm of a voided job id after reopening")
		if rec, ok, e := reopened.IssueRecord(ctx, authoritytest.Job(7)); e != nil || !ok || !rec.Tombstone() || rec.State != contractsim.IssueVoid {
			t.Fatal(rec, ok, e)
		}
	}},
	{"a_grant_stored_before_the_capability_does_not_issue", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		a := g.Env.(*durableEnv).admin
		// a grant of version 1 has no IssueDocuments at all
		exec(t, a, "UPDATE "+schema+"grants SET data=data-'IssueDocuments',data_sha=encode(sha256(convert_to((data-'IssueDocuments')::text,'UTF8')),'hex')")
		if scalar[bool](t, a, "SELECT data ? 'IssueDocuments' FROM "+schema+"grants") {
			t.Fatal("the stored grant still has the field")
		}
		must(t, g.Src.AuthorizeCurrent(ctx, g.SC, g.Prep))
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "grant without the field")
		g.Env.SetGrant(g.SC, g.P.Grant)
		g.Reserve(1)
	}},
	{"a_source_without_its_number_sequence_is_an_integrity_failure", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		env := g.Env.(*durableEnv)
		exec(t, env.admin, "DELETE FROM "+schema+"issue_numbers")
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrIntegrity, "source whose fixture was ensured before the sequences existed")
		// ensuring the fixture again gives the source its sequence
		must(t, env.src.EnsureFixture(ctx, g.SC.Owner.Integration, authoritytest.SourceTenant, g.P.Build().Fixture()))
		if res := g.Reserve(1); res.Number != "AR-000001" {
			t.Fatal(res.Number)
		}
	}},
	{"the_approval_switch_belongs_to_one_source", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance(), authoritytest.OtherIntegration)
		src := g.Env.(*durableEnv).src
		must(t, src.SetIssueRefused(ctx, g.SC.Owner.Integration, authoritytest.SourceTenant, true))
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "the refusing source")
		other := g.Context(authoritytest.OtherIntegration)
		in := g.Intent(2, 0)
		in.Prepared = g.Read(other)
		res, e := src.PrepareDocument(ctx, other, in)
		if e != nil || res.Number != "AR-000001" {
			t.Fatal("the other source must go on:", res, e)
		}
		is(t, src.SetIssueRefused(ctx, "unknown-contract-demo", authoritytest.SourceTenant, true), r.ErrNotFound, "a source that does not exist")
		is(t, src.SetIssueRefused(ctx, g.SC.Owner.Integration, "tenant_b", true), r.ErrNotFound, "a source tenant that does not exist")
	}},
	{"the_row_check_is_not_a_data_read", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		src := g.Env.(*durableEnv).src
		g.Reserve(1)
		g.Reserve(2)
		if n := src.ReadCalls(); n != 0 {
			t.Fatal("the row check counted as", n, "reads")
		}
		if _, e := src.ReadPage(ctx, g.SC, g.Prep, "", 50); e != nil {
			t.Fatal(e)
		}
		g.Reserve(3)
		if n := src.ReadCalls(); n != 1 {
			t.Fatal("reads:", n)
		}
	}},
	{"an_expired_source_read_is_denied", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		a := g.Env.(*durableEnv).admin
		// the read ends a minute ago, in the stored read and in the one the platform holds
		ended := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		exec(t, a, "UPDATE "+schema+"prepared SET expires_at=$2,data=jsonb_set(data,'{Prepared,ExpiresAt}',to_jsonb($3::text)) WHERE id=$1", g.Prep.Ref, ended, ended.Format(time.RFC3339Nano))
		exec(t, a, "UPDATE "+schema+"prepared SET data_sha=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1", g.Prep.Ref)
		g.Prep.ExpiresAt = ended
		want := g.Src.AuthorizeCurrent(ctx, g.SC, g.Prep)
		is(t, want, r.ErrDenied, "the expired read")
		_, e := g.Prepare(1, 0)
		is(t, e, r.ErrDenied, "document on the expired read")
	}},
	{"the_schema_refuses_reused_numbers_and_inconsistent_records", func(t *testing.T) {
		freshSchema(t)
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		a := g.Env.(*durableEnv).admin
		res := g.Reserve(1)
		rec := g.Record(1)
		insert := func(job int, ref string, seq int64, number string) error {
			_, e := a.Exec(ctx, "INSERT INTO "+schema+"issues(integration,source_tenant,job_id,ref,seq,number,state,issued_at,expires_at,intent_sha) VALUES($1,'tenant_a',$2,$3,$4,$5,'reserved',now(),now(),$6)", g.SC.Owner.Integration, authoritytest.Job(job), ref, seq, number, rec.IntentSHA256)
			return e
		}
		freshRef := r.NewID()
		for name, e := range map[string]error{
			"a number taken":   insert(2, freshRef, 2, res.Number),
			"a sequence used":  insert(3, r.NewID(), 1, "AR-000099"),
			"a reference used": insert(4, res.Ref, 2, "AR-000098"),
			"a job id used":    insert(1, r.NewID(), 2, "AR-000097"),
		} {
			if pgCode(e) != "23505" {
				t.Fatalf("%s: %v, want a unique violation", name, e)
			}
		}
		if e := insert(5, freshRef, 5, "AR-000005"); e != nil {
			t.Fatal("the same insert with values nothing uses is accepted, so the refusals above are the constraints':", e)
		}
		sha := strings.Repeat("a", 64)
		_, e := a.Exec(ctx, "UPDATE "+schema+"issues SET sha256=$1 WHERE job_id=$2", sha, authoritytest.Job(1))
		if pgCode(e) != "23514" {
			t.Fatal("a reserved record with a digest:", e)
		}
		g.Confirm(res)
		_, e = a.Exec(ctx, "UPDATE "+schema+"issues SET sha256=NULL WHERE job_id=$1", authoritytest.Job(1))
		if pgCode(e) != "23514" {
			t.Fatal("a confirmed record without its digest:", e)
		}
		_, e = a.Exec(ctx, "UPDATE "+schema+"issues SET state='void' WHERE job_id=$1", authoritytest.Job(1))
		if pgCode(e) != "23514" {
			t.Fatal("a void record without a reason:", e)
		}
		_, e = a.Exec(ctx, "UPDATE "+schema+"issues SET state='lost' WHERE job_id=$1", authoritytest.Job(1))
		if pgCode(e) != "23514" {
			t.Fatal("an unknown state:", e)
		}
	}},
	{"the_schema_allows_a_tombstone_and_no_other_row_without_a_reservation", func(t *testing.T) {
		freshSchema(t)
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		a := g.Env.(*durableEnv).admin
		ref, sha := r.NewID(), strings.Repeat("a", 64)
		insert := func(job int, columns, values string) error {
			_, e := a.Exec(ctx, "INSERT INTO "+schema+"issues(integration,source_tenant,job_id,"+columns+") VALUES($1,'tenant_a',$2,"+values+")", g.SC.Owner.Integration, authoritytest.Job(job))
			return e
		}
		attempts := []struct {
			name, columns, values, code string
		}{
			{"a tombstone", "state,reason,voided_at", "'void','EXECUTION_FAILED',now()", ""},
			{"a tombstone with an empty reason", "state,reason,voided_at", "'void','',now()", ""},
			{"a numbered reservation", "ref,seq,number,state,issued_at,expires_at,intent_sha", "'" + ref + "',1,'AR-000001','reserved',now(),now(),'" + sha + "'", ""},
			{"a reserved row without a number", "state,issued_at,expires_at,intent_sha", "'reserved',now(),now(),'" + sha + "'", "23514"},
			{"a confirmed row without a number", "state,sha256,pages,confirmation_ref,confirmed_at", "'confirmed','" + sha + "',1,'" + ref + "',now()", "23514"},
			{"a reserved row that looks like a tombstone", "state,reason,voided_at", "'reserved','x',now()", "23514"},
			{"a confirmed row that looks like a tombstone", "state,reason,voided_at", "'confirmed','x',now()", "23514"},
			{"a tombstone without a reason", "state,voided_at", "'void',now()", "23514"},
			{"a tombstone without a void time", "state,reason", "'void','x'", "23514"},
			{"a tombstone with a reference", "state,reason,voided_at,ref", "'void','x',now(),'" + r.NewID() + "'", "23514"},
			{"a tombstone with a sequence", "state,reason,voided_at,seq", "'void','x',now(),9", "23514"},
			{"a tombstone with an issue time", "state,reason,voided_at,issued_at", "'void','x',now(),now()", "23514"},
			{"a tombstone with a digest", "state,reason,voided_at,sha256", "'void','x',now(),'" + sha + "'", "23514"},
			{"a numbered row without its sequence", "ref,number,state,issued_at,expires_at,intent_sha", "'" + r.NewID() + "','AR-000200','reserved',now(),now(),'" + sha + "'", "23514"},
			{"a numbered row without its reference", "seq,number,state,issued_at,expires_at,intent_sha", "200,'AR-000200','reserved',now(),now(),'" + sha + "'", "23514"},
			{"a numbered row without its intent", "ref,seq,number,state,issued_at,expires_at", "'" + r.NewID() + "',200,'AR-000200','reserved',now(),now()", "23514"},
			{"a number that is not a valid issue number", "ref,seq,number,state,issued_at,expires_at,intent_sha", "'" + r.NewID() + "',201,'-bad','reserved',now(),now(),'" + sha + "'", "23514"},
			{"a number of 33 characters", "ref,seq,number,state,issued_at,expires_at,intent_sha", "'" + r.NewID() + "',202,'" + strings.Repeat("A", 33) + "','reserved',now(),now(),'" + sha + "'", "23514"},
		}
		for i, c := range attempts {
			if e := insert(100+i, c.columns, c.values); pgCode(e) != c.code {
				t.Fatalf("%s: got %v, want SQLSTATE %q", c.name, e, c.code)
			}
		}
		// a job id has one row: a second tombstone for it is refused, tombstones of other jobs are not
		if e := insert(100, "state,reason,voided_at", "'void','again',now()"); pgCode(e) != "23505" {
			t.Fatal("a second row for the job id of a tombstone:", e)
		}
		// the tombstones took no number: the source's sequence is where it was
		if n := scalar[int64](t, a, "SELECT last_number FROM "+schema+"issue_numbers"); n != 0 {
			t.Fatal("last_number", n)
		}
	}},
	{"voiding_needs_the_source_and_its_sequence", func(t *testing.T) {
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		env := g.Env.(*durableEnv)
		count := func(job int) int64 {
			return scalar[int64](t, env.admin, "SELECT count(*) FROM "+schema+"issues WHERE job_id=$1", authoritytest.Job(job))
		}
		// a source the database does not hold can hold no job: no error and nothing recorded
		unknown := g.SC
		unknown.Owner.Integration = "unknown-contract-demo"
		must(t, env.src.VoidIssue(ctx, unknown, authoritytest.Job(7), "EXECUTION_FAILED"))
		if n := count(7); n != 0 {
			t.Fatal("a void in a source the database does not hold left", n, "rows")
		}
		// a source ensured before the sequences existed cannot take the tombstone, as it cannot
		// take a reservation: ErrIntegrity, until its fixture is ensured again
		exec(t, env.admin, "DELETE FROM "+schema+"issue_numbers")
		is(t, env.src.VoidIssue(ctx, g.SC, authoritytest.Job(7), "EXECUTION_FAILED"), r.ErrIntegrity, "source without its sequence")
		if n := count(7); n != 0 {
			t.Fatal("a refused void left", n, "rows")
		}
		must(t, env.src.EnsureFixture(ctx, g.SC.Owner.Integration, authoritytest.SourceTenant, g.P.Build().Fixture()))
		must(t, env.src.VoidIssue(ctx, g.SC, authoritytest.Job(7), "EXECUTION_FAILED"))
		g.Holds(7, contractsim.IssueVoid, "")
	}},
	{"a_database_with_the_first_revision_of_the_issues_table_is_refused", func(t *testing.T) {
		freshSchema(t)
		a := admin(t)
		// the first revision of schema version 2 had a number on every row; the table is empty
		restore := func() { exec(t, a, "ALTER TABLE "+schema+"issues ALTER COLUMN number DROP NOT NULL") }
		exec(t, a, "ALTER TABLE "+schema+"issues ALTER COLUMN number SET NOT NULL")
		t.Cleanup(restore)
		for _, initialize := range []bool{false, true} {
			s, e := pgsource.Open(ctx, dsn, initialize)
			if s != nil {
				s.Close()
			}
			if !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), "tombstones") {
				t.Fatalf("initialize=%v: %v", initialize, e)
			}
		}
		restore()
		s, e := pgsource.Open(ctx, dsn, false)
		must(t, e)
		s.Close()
	}},
	{"a_fresh_database_gets_the_version_2_schema", func(t *testing.T) {
		freshSchema(t)
		a := admin(t)
		if v := scalar[int](t, a, "SELECT version FROM "+schema+"profile"); v != 2 {
			t.Fatal("profile version", v)
		}
		for _, table := range []string{"issues", "issue_numbers", "versions", "heads", "grants", "prepared"} {
			if !scalar[bool](t, a, "SELECT to_regclass($1) IS NOT NULL", schema+table) {
				t.Fatal("no table", table)
			}
		}
		// and it works from the first call on
		g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
		if res := g.Reserve(1); res.Number != "AR-000001" {
			t.Fatal(res.Number)
		}
	}},
	{"opening_requires_profile_version_2", func(t *testing.T) {
		a := admin(t)
		openSource(t).Close()
		t.Cleanup(func() {
			exec(t, a, "UPDATE "+schema+"profile SET profile='durable-contract-simulator-only',version=2")
		})
		for name, set := range map[string]string{"a future version": "version=3", "another profile": "profile='something-else'"} {
			exec(t, a, "UPDATE "+schema+"profile SET profile='durable-contract-simulator-only',version=2")
			exec(t, a, "UPDATE "+schema+"profile SET "+set)
			for _, initialize := range []bool{false, true} {
				s, e := pgsource.Open(ctx, dsn, initialize)
				if s != nil {
					s.Close()
				}
				if !errors.Is(e, r.ErrDenied) {
					t.Fatalf("%s, initialize=%v: %v", name, initialize, e)
				}
			}
		}
	}},
	{"a_version_1_database_is_upgraded_in_one_transaction_and_keeps_its_data", upgradeFromVersion1},
}

// upgradeFromVersion1 turns a database into what S03-G's simulator left: no sequences or
// reservations, profile version 1, grants without IssueDocuments.
func upgradeFromVersion1(t *testing.T) {
	g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
	env := g.Env.(*durableEnv)
	a := env.admin
	exec(t, a, "DROP TABLE "+schema+"issues;DROP TABLE "+schema+"issue_numbers;UPDATE "+schema+"profile SET version=1;UPDATE "+schema+"grants SET data=data-'IssueDocuments',data_sha=encode(sha256(convert_to((data-'IssueDocuments')::text,'UTF8')),'hex')")
	env.src.Close()
	exists := func(table string) bool { return scalar[bool](t, a, "SELECT to_regclass($1) IS NOT NULL", schema+table) }

	// version 2 code does not open a version 1 database unless told to initialize it
	s, e := pgsource.Open(ctx, dsn, false)
	if s != nil {
		s.Close()
	}
	is(t, e, r.ErrDenied, "version 1 opened without initialize")

	// an upgrade that fails halfway leaves version 1 as it was: hold the profile row so that the
	// last statement of the upgrade, which moves the version, waits until the context ends
	holder, e := a.Begin(ctx)
	must(t, e)
	_, e = holder.Exec(ctx, "SELECT 1 FROM "+schema+"profile WHERE singleton FOR UPDATE")
	must(t, e)
	short, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	s, e = pgsource.Open(short, dsn, true)
	cancel()
	if s != nil {
		s.Close()
		t.Fatal("the upgrade finished while the profile was locked")
	}
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("the upgrade should have waited for the profile row until its context ended:", e)
	}
	must(t, holder.Rollback(ctx))
	if exists("issue_numbers") || exists("issues") || scalar[int](t, a, "SELECT version FROM "+schema+"profile") != 1 {
		t.Fatal("an upgrade that failed left part of version 2 behind")
	}

	// two initializing processes at once both succeed and leave one version 2 schema
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := pgsource.Open(ctx, dsn, true)
			if s != nil {
				s.Close()
			}
			errs[i] = e
		}()
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatal("concurrent upgrade", i, e)
		}
	}
	if scalar[int](t, a, "SELECT version FROM "+schema+"profile") != 2 || !exists("issue_numbers") || !exists("issues") {
		t.Fatal("the upgrade did not complete")
	}

	// what version 1 held is still there: the frozen read, its rows and the grant
	src, e := pgsource.Open(ctx, dsn, false)
	must(t, e)
	defer src.Close()
	must(t, src.AuthorizeCurrent(ctx, g.SC, g.Prep))
	page, e := src.ReadPage(ctx, g.SC, g.Prep, "", 50)
	if e != nil || len(page.Rows) != 3 || r.Digest(page.Rows[0]) != g.P.RowSHA256[0] {
		t.Fatal("the frozen read of version 1:", page, e)
	}
	if cat, e := src.DescribeCatalog(ctx, g.SC); e != nil || len(cat) != 1 {
		t.Fatal(cat, e)
	}
	for _, table := range []string{"issues", "issue_numbers"} {
		if n := scalar[int64](t, a, "SELECT count(*) FROM "+schema+table); n != 0 {
			t.Fatal("the upgrade invented", n, "rows in", table)
		}
	}

	// the grant of version 1 does not issue until it is set again; and the source has no sequence
	// until its fixture is ensured again, which gives it the prefix of the fixture
	_, e = src.PrepareDocument(ctx, g.SC, g.Intent(1, 0))
	is(t, e, r.ErrDenied, "grant stored by version 1")
	must(t, src.SetGrant(ctx, g.SC, g.P.Grant))
	_, e = src.PrepareDocument(ctx, g.SC, g.Intent(1, 0))
	is(t, e, r.ErrIntegrity, "source ensured by version 1")
	must(t, src.EnsureFixture(ctx, g.SC.Owner.Integration, authoritytest.SourceTenant, contractsim.Financial().Fixture()))
	res, e := src.PrepareDocument(ctx, g.SC, g.Intent(1, 0))
	if e != nil || res.Number != "AR-000001" {
		t.Fatal(res, e)
	}
	if _, e = src.ConfirmIssue(ctx, g.SC, g.Confirmation(res)); e != nil {
		t.Fatal(e)
	}
	// the upgraded database holds a void of a job nothing was reserved for as well
	must(t, src.VoidIssue(ctx, g.SC, authoritytest.Job(2), "EXECUTION_FAILED"))
	_, e = src.PrepareDocument(ctx, g.SC, g.Intent(2, 0))
	is(t, e, r.ErrConflict, "prepare of a voided job id in the upgraded database")

	// initializing again changes nothing
	again, e := pgsource.Open(ctx, dsn, true)
	must(t, e)
	defer again.Close()
	if rec, ok, e := again.IssueRecord(ctx, authoritytest.Job(1)); e != nil || !ok || rec.State != contractsim.IssueConfirmed || rec.Number != "AR-000001" {
		t.Fatal(rec, ok, e)
	}
	if rec, ok, e := again.IssueRecord(ctx, authoritytest.Job(2)); e != nil || !ok || !rec.Tombstone() || rec.State != contractsim.IssueVoid {
		t.Fatal(rec, ok, e)
	}
}
