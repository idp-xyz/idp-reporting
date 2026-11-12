package pgsource_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/internal/authoritytest"
	"idp.local/reporting/adapters/contractsim/pgsource"
	r "idp.local/reporting/reporting"
)

// moveRead rewrites the source's record of a read so that its expiry and its scope's end are the given times, and returns the read
// as the platform holds it when the source gave those times: what waiting the times out leaves, without the wait (the scenario
// an_expired_source_read_is_denied moves a read's expiry the same way). The row's digest follows the row, so the source reads its
// own record as one it wrote. The times are whole microseconds in UTC, which is what the source writes.
func moveRead(t *testing.T, a *pgxpool.Pool, read r.PreparedRead, expires, scope time.Time) r.PreparedRead {
	t.Helper()
	exec(t, a, "UPDATE "+schema+"prepared SET expires_at=$2,data=jsonb_set(jsonb_set(data,'{Prepared,ExpiresAt}',to_jsonb($3::text)),'{Prepared,ScopeExpiresAt}',to_jsonb($4::text)) WHERE id=$1",
		read.Ref, expires, expires.Format(time.RFC3339Nano), scope.Format(time.RFC3339Nano))
	exec(t, a, "UPDATE "+schema+"prepared SET data_sha=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1", read.Ref)
	read.ExpiresAt, read.ScopeExpiresAt = expires, scope
	return read
}

// readAndScopeLifetimes: the durable source vouches for the scope of its reads (S25-B2, ADR 0026 decision 2), by the database's clock,
// as the in-memory simulator does by the clock a test moves (contractsim's TestTheSimulatorServesPagesWhileTheReadLastsAndAuthorizesWhileTheScopeDoes).
// It serves a read's pages and checks a document's row while the read lasts; it goes on authorizing the read while its scope lasts, by
// the grant of the moment; it holds a read that comes back to its own record, the scope's end included, and refuses another; what
// it vouched for is in the read's row, so it keeps its word over a reopening; and after the scope it refuses.
//
// The scenario does not wait: the read's times are moved into the past in the source's own record (moveRead), and every decision is
// the source's, by the database's clock against the times it stored. Nothing in it depends on how fast the machine is.
func readAndScopeLifetimes(t *testing.T) {
	const readLife, scopeLife = 2 * time.Minute, 3 * time.Hour
	g := authoritytest.NewRig(t, newDurableEnv, authoritytest.Finance())
	src := g.Env.(*durableEnv).src
	a := g.Env.(*durableEnv).admin
	// The database's clock, which is the source's; a read was prepared within the minute before it is asked.
	lasts := func(at time.Time, life time.Duration) bool {
		left := at.Sub(scalar[time.Time](t, a, "SELECT clock_timestamp()"))
		return left <= life && left > life-time.Minute
	}
	// As it was: a read lasts ten minutes and vouches for nothing beyond itself.
	if !g.Prep.ScopeExpiresAt.IsZero() || !lasts(g.Prep.ExpiresAt, contractsim.DefaultReadLifetime) {
		t.Fatalf("a read of a source without settings: expires at %s, scope %s", g.Prep.ExpiresAt, g.Prep.ScopeExpiresAt)
	}
	// A scope not longer than the read declares none, as in the in-memory simulator.
	src.SetReadLifetime(readLife)
	src.SetScopeLifetime(readLife)
	if p := g.Read(g.SC); !p.ScopeExpiresAt.IsZero() || !lasts(p.ExpiresAt, readLife) {
		t.Fatalf("a scope as long as the read: %+v", p)
	}
	src.SetScopeLifetime(scopeLife)
	g.Prep = g.Read(g.SC)
	read := g.Prep
	if read.ScopeExpiresAt.Sub(read.ExpiresAt) != scopeLife-readLife || !lasts(read.ExpiresAt, readLife) || read.ExpiresAt.Nanosecond()%1000 != 0 || read.ScopeExpiresAt.Location() != time.UTC {
		t.Fatalf("the read's times: %s and %s", read.ExpiresAt, read.ScopeExpiresAt)
	}
	// What the source keeps of the scope is the stored read; its column is the read's own expiry.
	if stored := scalar[string](t, a, "SELECT data->'Prepared'->>'ScopeExpiresAt' FROM "+schema+"prepared WHERE id=$1", read.Ref); stored != read.ScopeExpiresAt.Format(time.RFC3339Nano) {
		t.Fatalf("the stored read's scope is %q, the read says %s", stored, read.ScopeExpiresAt.Format(time.RFC3339Nano))
	}
	if column := scalar[time.Time](t, a, "SELECT expires_at FROM "+schema+"prepared WHERE id=$1", read.Ref); !column.Equal(read.ExpiresAt) {
		t.Fatalf("the read's column is %s, the read expires at %s", column, read.ExpiresAt)
	}

	// While the read lasts: its pages, its authorization, a document reserved from it and one confirmed.
	if page, e := src.ReadPage(ctx, g.SC, read, "", 50); e != nil || len(page.Rows) != 3 {
		t.Fatal(page, e)
	}
	must(t, src.AuthorizeCurrent(ctx, g.SC, read))
	g.Reserve(1)
	g.Issued(3)

	// The read ends, a minute ago; its scope goes on. No page and no document, neither a new one nor one of the read's own asked
	// again, reserved or confirmed (all need the rows the read froze): their records would answer again, were the row checked for
	// as long as the scope lasts. The authorization goes on.
	now := time.Now().UTC().Truncate(time.Microsecond)
	read = moveRead(t, a, read, now.Add(-time.Minute), read.ScopeExpiresAt)
	g.Prep = read
	_, e := src.ReadPage(ctx, g.SC, read, "", 50)
	is(t, e, r.ErrDenied, "a page after the read")
	_, e = g.Prepare(2, 0)
	is(t, e, r.ErrDenied, "a document after the read")
	_, e = g.Prepare(1, 0)
	is(t, e, r.ErrDenied, "the document reserved within the read, asked again after it")
	_, e = g.Prepare(3, 0)
	is(t, e, r.ErrDenied, "the document confirmed within the read, asked again after it")
	must(t, src.AuthorizeCurrent(ctx, g.SC, read))
	// By the grant of the moment: unavailable, revoked, narrowed, and authorized again under the grant the read was frozen on.
	g.Env.SetUnavailable(true)
	is(t, src.AuthorizeCurrent(ctx, g.SC, read), r.ErrUnavailable, "an unavailable source within the scope")
	g.Env.SetUnavailable(false)
	g.Env.Revoke(g.SC)
	is(t, src.AuthorizeCurrent(ctx, g.SC, read), r.ErrDenied, "a revoked grant within the scope")
	narrowed := g.P.Grant
	narrowed.Companies = []string{"company_other"}
	g.Env.SetGrant(g.SC, narrowed)
	is(t, src.AuthorizeCurrent(ctx, g.SC, read), r.ErrDenied, "a narrowed grant within the scope")
	g.Env.SetGrant(g.SC, g.P.Grant)
	must(t, src.AuthorizeCurrent(ctx, g.SC, read))
	// The read is held to the source's own record, the scope's end included: with another scope, or without it, it is not the read
	// the source made, and is refused as any other read that is not (ErrDenied; the protocol's `denied`).
	later := read
	later.ScopeExpiresAt = read.ScopeExpiresAt.Add(time.Hour)
	is(t, src.AuthorizeCurrent(ctx, g.SC, later), r.ErrDenied, "a read with a later scope")
	without := read
	without.ScopeExpiresAt = time.Time{}
	is(t, src.AuthorizeCurrent(ctx, g.SC, without), r.ErrDenied, "a read without its scope")
	other := read
	other.Watermark += "-forged"
	is(t, src.AuthorizeCurrent(ctx, g.SC, other), r.ErrDenied, "a read with another watermark")
	// A record that does not agree with itself is something else: the source's own store is wrong (ErrIntegrity), whatever comes
	// back. Here the column says another expiry than the stored read.
	exec(t, a, "UPDATE "+schema+"prepared SET expires_at=expires_at-interval '1 second' WHERE id=$1", read.Ref)
	is(t, src.AuthorizeCurrent(ctx, g.SC, read), r.ErrIntegrity, "a record whose column is not its read's expiry")
	exec(t, a, "UPDATE "+schema+"prepared SET expires_at=expires_at+interval '1 second' WHERE id=$1", read.Ref)
	must(t, src.AuthorizeCurrent(ctx, g.SC, read))

	// A reopened source keeps its word: the scope is in the read's row, not in the process that gave it. Its own settings are the
	// defaults again, so a read it prepares vouches for nothing.
	src.Close()
	reopened, e := pgsource.Open(ctx, dsn, false)
	must(t, e)
	defer reopened.Close()
	must(t, reopened.AuthorizeCurrent(ctx, g.SC, read))
	q := g.P.Query(g.SC.Owner.Integration)
	if fresh, e := reopened.PrepareRead(ctx, g.SC, q, r.QueryHash(q)); e != nil || !fresh.ScopeExpiresAt.IsZero() || !lasts(fresh.ExpiresAt, contractsim.DefaultReadLifetime) {
		t.Fatalf("a read of the reopened source: %+v %v", fresh, e)
	}

	// The scope ends, a minute ago (the read two minutes ago): the source refuses, whatever the grant.
	read = moveRead(t, a, read, now.Add(-2*time.Minute), now.Add(-time.Minute))
	is(t, reopened.AuthorizeCurrent(ctx, g.SC, read), r.ErrDenied, "the authorization after the scope")
	// With the scope's end ahead of the clock again it answers again: the end is the stored time against the database's clock, and
	// nothing the source remembers of having refused.
	read = moveRead(t, a, read, now.Add(-2*time.Minute), now.Add(time.Hour))
	must(t, reopened.AuthorizeCurrent(ctx, g.SC, read))
}
