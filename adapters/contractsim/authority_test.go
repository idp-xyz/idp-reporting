package contractsim_test

import (
	"context"
	"encoding/json"
	"errors"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/internal/authoritytest"
	r "idp.local/reporting/reporting"
	"strings"
	"sync"
	"testing"
	"time"
)

// base is half a second into a minute. A reservation is issued at the whole second, so one made
// in the same instant as its source read expires half a second before the read does, which lets
// a scenario expire the reservation while the read is still valid.
var base = time.Date(2026, 10, 28, 12, 0, 0, 500_000_000, time.UTC)

// memEnv is an in-memory simulator on a clock the scenarios move.
type memEnv struct {
	t  *testing.T
	s  *contractsim.Simulator
	mu sync.Mutex
	at time.Time
}

func newMemEnv(t *testing.T, p authoritytest.Profile, _ ...string) authoritytest.Env {
	e := &memEnv{t: t, s: p.Build(), at: base}
	e.s.SetClock(e.now)
	return e
}
func (e *memEnv) now() time.Time                                   { e.mu.Lock(); defer e.mu.Unlock(); return e.at }
func (e *memEnv) set(at time.Time)                                 { e.mu.Lock(); defer e.mu.Unlock(); e.at = at }
func (e *memEnv) Source() authoritytest.Source                     { return e.s }
func (e *memEnv) Now() time.Time                                   { return e.now() }
func (e *memEnv) SetGrant(sc r.SourceContext, g contractsim.Grant) { e.s.Grant(sc.Owner, g) }
func (e *memEnv) Revoke(sc r.SourceContext)                        { e.s.Revoke(sc.Owner) }
func (e *memEnv) SetUnavailable(v bool)                            { e.s.SetUnavailable(v) }
func (e *memEnv) SetRefused(v bool)                                { e.s.SetIssueRefused(v) }
func (e *memEnv) ReplaceRecords(records []contractsim.Record)      { e.s.ReplaceRecords(records) }
func (e *memEnv) Record(id string) (contractsim.IssueRecord, bool) { return e.s.IssueRecord(id) }
func (e *memEnv) Expire(id string) {
	rec, ok := e.s.IssueRecord(id)
	if !ok {
		e.t.Fatal("no reservation to expire for job", id)
	}
	e.set(rec.ExpiresAt) // the reservation is over at its expiry; the source read ends half a second later
}

func TestDocumentAuthority(t *testing.T) { authoritytest.Run(t, newMemEnv) }

func TestDocumentAuthorityConcurrentCalls(t *testing.T) { authoritytest.RunConcurrent(t, newMemEnv) }

func at(h, m, s, ns int) time.Time { return time.Date(2026, 10, 28, h, m, s, ns, time.UTC) }

// The clock of the simulator drives every time it reports: the issue, confirmation and void times,
// the expiry of reservations and the expiry of the source reads they stand on.
func TestDocumentAuthorityFollowsTheSimulatorsClock(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*authoritytest.Rig, *memEnv) {
		g := authoritytest.NewRig(t, newMemEnv, authoritytest.Finance())
		return g, g.Env.(*memEnv)
	}
	t.Run("a_reservation_made_with_its_read_expires_first", func(t *testing.T) {
		g, _ := setup(t)
		if !g.Prep.ExpiresAt.Equal(at(12, 10, 0, 500_000_000)) {
			t.Fatal("the read expires ten minutes after the clock:", g.Prep.ExpiresAt)
		}
		res := g.Reserve(1)
		if !res.IssuedAt.Equal(at(12, 0, 0, 0)) || !res.ExpiresAt.Equal(at(12, 10, 0, 0)) {
			t.Fatalf("issued %s, expires %s", res.IssuedAt, res.ExpiresAt)
		}
	})
	t.Run("a_reservation_made_later_expires_with_the_read", func(t *testing.T) {
		g, env := setup(t)
		env.set(at(12, 0, 2, 500_000_000))
		res := g.Reserve(1)
		if !res.IssuedAt.Equal(at(12, 0, 2, 0)) || !res.ExpiresAt.Equal(at(12, 10, 0, 500_000_000)) {
			t.Fatalf("issued %s, expires %s", res.IssuedAt, res.ExpiresAt)
		}
	})
	t.Run("the_confirmation_time_is_the_clock_and_never_before_the_issue_time", func(t *testing.T) {
		g, env := setup(t)
		env.set(at(12, 0, 2, 500_000_000))
		res, other := g.Reserve(1), g.Reserve(2)
		env.set(at(12, 3, 0, 900_000_000))
		if rc := g.Confirm(res); !rc.ConfirmedAt.Equal(at(12, 3, 0, 0)) {
			t.Fatal("confirmed at", rc.ConfirmedAt)
		}
		env.set(at(12, 0, 1, 200_000_000)) // the clock went back before the second job was issued
		if rc := g.Confirm(other); !rc.ConfirmedAt.Equal(at(12, 0, 2, 0)) {
			t.Fatal("confirmed at", rc.ConfirmedAt, "before it was issued")
		}
	})
	t.Run("the_void_time_is_the_clock_to_the_second", func(t *testing.T) {
		g, env := setup(t)
		g.Reserve(1)
		env.set(at(12, 4, 30, 700_000_000))
		if e := g.Src.VoidIssue(ctx, g.SC, authoritytest.Job(1), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		if rec := g.Record(1); !rec.VoidedAt.Equal(at(12, 4, 30, 0)) {
			t.Fatal("voided at", rec.VoidedAt)
		}
	})
	t.Run("a_reservation_is_over_at_its_expiry_instant", func(t *testing.T) {
		g, env := setup(t)
		env.set(at(12, 0, 2, 500_000_000))
		last, over := g.Reserve(1), g.Reserve(2) // both expire at 12:10:00.5
		env.set(at(12, 10, 0, 499_999_999))
		g.Confirm(last)
		env.set(at(12, 10, 0, 500_000_000))
		_, e := g.Src.ConfirmIssue(ctx, g.SC, g.Confirmation(over))
		if !errors.Is(e, r.ErrDenied) {
			t.Fatal("confirmed at the instant of expiry:", e)
		}
	})
	t.Run("the_source_read_ends_with_the_clock", func(t *testing.T) {
		g, env := setup(t)
		res := g.Reserve(1)
		g.Confirm(res)
		env.set(at(12, 10, 0, 499_999_999))
		if e := g.Src.AuthorizeCurrent(ctx, g.SC, g.Prep); e != nil {
			t.Fatal("the read is valid until its expiry:", e)
		}
		// issued in the last instant of the read: the reservation ends with it
		last, e := g.Prepare(2, 0)
		if e != nil || !last.IssuedAt.Equal(at(12, 10, 0, 0)) || !last.ExpiresAt.Equal(at(12, 10, 0, 500_000_000)) {
			t.Fatalf("%+v %v", last, e)
		}
		env.set(at(12, 10, 0, 500_000_000))
		if e = g.Src.AuthorizeCurrent(ctx, g.SC, g.Prep); !errors.Is(e, r.ErrDenied) {
			t.Fatal("the read is expired:", e)
		}
		// the checks come first, so even the confirmed job's reservation is not returned any more
		if _, e = g.Prepare(1, 0); !errors.Is(e, r.ErrDenied) {
			t.Fatal("job on an expired read:", e)
		}
		if _, e = g.Prepare(3, 0); !errors.Is(e, r.ErrDenied) {
			t.Fatal("new job on an expired read:", e)
		}
	})
	t.Run("a_nil_clock_is_the_real_one", func(t *testing.T) {
		g, env := setup(t)
		env.s.SetClock(nil)
		before := time.Now()
		q := authoritytest.Finance().Query(g.SC.Owner.Integration)
		prep, e := g.Src.PrepareRead(ctx, g.SC, q, r.QueryHash(q))
		if e != nil || prep.ExpiresAt.Before(before.Add(10*time.Minute-time.Second)) || prep.ExpiresAt.After(time.Now().Add(10*time.Minute+time.Second)) {
			t.Fatal(prep.ExpiresAt, e)
		}
		in := g.Intent(1, 0)
		in.Prepared = prep
		res, e := g.Src.PrepareDocument(ctx, g.SC, in)
		if d := time.Since(res.IssuedAt); e != nil || d < -time.Second || d > 5*time.Second {
			t.Fatal(res, e)
		}
	})
}

// The product checks the row by re-reading the frozen read itself; that is not a data read of the
// platform, which ReadCalls counts.
func TestDocumentAuthorityDoesNotCountItsOwnRowCheckAsARead(t *testing.T) {
	a := contractsim.Financial()
	g := authoritytest.NewRig(t, func(t *testing.T, _ authoritytest.Profile, _ ...string) authoritytest.Env {
		e := &memEnv{t: t, s: a, at: base}
		a.SetClock(e.now)
		return e
	}, authoritytest.Finance())
	g.Reserve(1)
	g.Reserve(2)
	if a.ReadCalls() != 0 {
		t.Fatal("the row check counted as", a.ReadCalls(), "reads")
	}
	if _, e := g.Src.ReadPage(context.Background(), g.SC, g.Prep, "", 50); e != nil {
		t.Fatal(e)
	}
	g.Reserve(3)
	if a.ReadCalls() != 1 {
		t.Fatal("reads:", a.ReadCalls())
	}
}

func TestFixtureCarriesTheNumberPrefix(t *testing.T) {
	finance, shipment := contractsim.Financial().Fixture(), contractsim.Shipment().Fixture()
	if finance.NumberPrefix != "AR" || shipment.NumberPrefix != "SH" {
		t.Fatal(finance.NumberPrefix, shipment.NumberPrefix)
	}
	if generic := contractsim.New(finance.Dataset, finance.Records).Fixture(); generic.NumberPrefix != "DOC" {
		t.Fatal("a simulator that names no prefix numbers its documents", generic.NumberPrefix)
	}
	if contractsim.DefaultNumberPrefix != "DOC" {
		t.Fatal(contractsim.DefaultNumberPrefix)
	}
}

func TestGrantIssueDocuments(t *testing.T) {
	if !contractsim.DefaultGrant().IssueDocuments {
		t.Fatal("the default grant may issue documents")
	}
	// a grant stored before the capability existed reads as not issuing
	var old contractsim.Grant
	if e := json.Unmarshal([]byte(`{"Companies":["company_demo"],"Rows":["r1"],"Timezone":"Asia/Shanghai","CurrencyPolicy":null}`), &old); e != nil || old.IssueDocuments {
		t.Fatal(old, e)
	}
	b, e := json.Marshal(contractsim.DefaultGrant())
	if e != nil || !strings.Contains(string(b), `"IssueDocuments":true`) {
		t.Fatal(string(b), e)
	}
}

func TestIssueRuleHelpers(t *testing.T) {
	t.Run("number_prefixes", func(t *testing.T) {
		for _, ok := range []string{"AR", "SH", "A", "DOC", "A1", "ABCDEFGH", "X9Z"} {
			if !contractsim.ValidNumberPrefix(ok) {
				t.Error("refused", ok)
			}
		}
		for _, bad := range []string{"", "ar", "1A", "A-", "A B", "ABCDEFGHI", "É", "A_", "a1"} {
			if contractsim.ValidNumberPrefix(bad) {
				t.Error("accepted", bad)
			}
		}
	})
	t.Run("numbers_are_six_digits_then_more_and_always_valid", func(t *testing.T) {
		for seq, want := range map[int64]string{1: "AR-000001", 12: "AR-000012", 999999: "AR-999999", 1000000: "AR-1000000", 9223372036854775807: "AR-9223372036854775807"} {
			if got := contractsim.IssueNumber("AR", seq); got != want || !r.ValidIssueNumber(got) {
				t.Error(seq, got, want)
			}
		}
		if got := contractsim.IssueNumber("ABCDEFGH", 9223372036854775807); !r.ValidIssueNumber(got) {
			t.Error("the longest number is not valid:", got)
		}
	})
	t.Run("reservation_times", func(t *testing.T) {
		cases := []struct {
			name              string
			now, read         time.Time
			issuedAt, expires time.Time
		}{
			{"the ten minutes end first", at(12, 0, 0, 500_000_000), at(12, 10, 0, 500_000_000), at(12, 0, 0, 0), at(12, 10, 0, 0)},
			{"the read ends first", at(12, 0, 2, 500_000_000), at(12, 10, 0, 500_000_000), at(12, 0, 2, 0), at(12, 10, 0, 500_000_000)},
			{"both end together", at(12, 0, 0, 0), at(12, 10, 0, 0), at(12, 0, 0, 0), at(12, 10, 0, 0)},
			{"the read ends within a second", at(12, 0, 0, 900_000_000), at(12, 0, 1, 100_000_000), at(12, 0, 0, 0), at(12, 0, 1, 100_000_000)},
			{"to the microsecond", at(12, 0, 0, 0), at(12, 5, 0, 123_456_789), at(12, 0, 0, 0), at(12, 5, 0, 123_456_000)},
			{"any zone is UTC", time.Date(2026, 10, 28, 20, 0, 0, 5, time.FixedZone("CST", 8*3600)), time.Date(2026, 10, 28, 20, 30, 0, 0, time.FixedZone("CST", 8*3600)), at(12, 0, 0, 0), at(12, 10, 0, 0)},
		}
		for _, c := range cases {
			issued, expires := contractsim.ReservationTimes(c.now, c.read)
			if !issued.Equal(c.issuedAt) || !expires.Equal(c.expires) || issued.Location() != time.UTC || expires.Location() != time.UTC {
				t.Errorf("%s: issued %s expires %s, want %s %s", c.name, issued, expires, c.issuedAt, c.expires)
			}
		}
	})
	t.Run("an_intent_has_one_digest_in_any_zone", func(t *testing.T) {
		in := r.IssueIntent{JobID: authoritytest.Job(1), Prepared: r.PreparedRead{Ref: "x", ExpiresAt: at(12, 10, 0, 0)}, RowSHA256: strings.Repeat("a", 64), TemplateID: strings.Repeat("b", 32), TemplateVersion: 1, TemplateSHA256: strings.Repeat("c", 64)}
		shifted := in
		shifted.Prepared.ExpiresAt = at(12, 10, 0, 0).In(time.FixedZone("CST", 8*3600))
		if contractsim.IntentDigest(in) != contractsim.IntentDigest(shifted) {
			t.Fatal("the zone of the read's expiry changes the intent")
		}
		other := in
		other.RowIndex = 1
		if contractsim.IntentDigest(in) == contractsim.IntentDigest(other) {
			t.Fatal("another row is the same intent")
		}
	})
	t.Run("hexadecimal_ids", func(t *testing.T) {
		for _, ok := range []string{strings.Repeat("0", 32), "0123456789abcdef0123456789abcdef"} {
			if !contractsim.ValidJobID(ok) {
				t.Error("refused", ok)
			}
		}
		for _, bad := range []string{"", strings.Repeat("0", 31), strings.Repeat("0", 33), strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("0", 31) + "\n"} {
			if contractsim.ValidJobID(bad) {
				t.Error("accepted", bad)
			}
		}
	})
}
