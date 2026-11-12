package conformance

import (
	"context"
	"time"

	r "idp.local/reporting/reporting"
)

// The rules of a source that vouches for the scope of its reads (S25-B2, ADR 0026 decision 2), checked as a connection that declares
// `kept_results` would take the source's reads. They only read: nothing is reserved, and they are asked of the sample read.
//
//   - K1: the read vouches for a scope: its `scope_expires_at` is after its `expires_at`. That the time is well formed (canonical, not
//     before the read's expiry, not after the year 9999) the adapter has verified when it took the read (R1); a source that declares
//     no scope, or one that ends with the read, has nothing for the platform to keep by and fails here: the flag says it vouches.
//   - K2: the source holds the scope's end itself. The read comes back as the source gave it and is authorized; the same read with
//     another scope expiry, and the same read without one, is not the read the source made and is refused, as a read with another
//     watermark is (R7); and the read as the source gave it is authorized again after the two, so that their refusals are not those
//     of a read that ran out while the rule was asked. A source that passed these because it reads the scope's end from the request
//     would authorize a read for as long as whoever sends it says.
//
// Without the flag a read that vouches for a scope does not verify at all, as through a connection that does not declare kept
// results, and the check ends at R1 with the adapter's words: the flag is the product saying that it vouches.
//
// What no check of minutes can show is the promise itself: that the source goes on answering `authorize` after the read has expired,
// until the scope ends, by the rights of that moment, and refuses afterwards. A read lasts up to a quarter of an hour and a scope may
// last years; the product proves that with a test of its own, on a clock it moves (docs/REAL_SOURCE_ONBOARDING.md).
const (
	ruleVouches    = "the read vouches for a scope: scope_expires_at is after expires_at"
	ruleScopeIsOwn = "the read is authorized as the source gave it, and refused with another scope expiry or without one (denied or expired)"
)

// skipKeptResults skips the two rules for one reason.
func skipKeptResults(skip func(id, rule, why string), why string) {
	skip("K1", ruleVouches, why)
	skip("K2", ruleScopeIsOwn, why)
}

// keptResults runs the K rules on the sample read p, which the adapter of a connection that declares kept results took.
func keptResults(ctx context.Context, a r.Adapter, sc r.SourceContext, p r.PreparedRead, add func(id, rule string, err error) bool, skip func(id, rule, why string)) {
	var vouches error
	if !p.ScopeExpiresAt.After(p.ExpiresAt) {
		vouches = fail("the read declares no scope beyond its expiry", r.ErrUnsupported)
	}
	if !add("K1", ruleVouches, vouches) {
		skip("K2", ruleScopeIsOwn, "the read vouches for no scope (K1)")
		return
	}
	add("K2", ruleScopeIsOwn, scopeIsTheSourcesOwn(ctx, a, sc, p))
}

// scopeIsTheSourcesOwn asks for the read's authorization four times: as the source gave it, with another scope expiry, without one,
// and as the source gave it again. The first must be a yes: a read that is refused as it was given has nothing to compare the two
// altered reads with. The last must be a yes too: a read that ran out between the first ask and the others would refuse them for
// that reason, whatever scope they carry, and the rule would pass on refusals that show nothing.
func scopeIsTheSourcesOwn(ctx context.Context, a r.Adapter, sc r.SourceContext, p r.PreparedRead) error {
	if err := a.AuthorizeCurrent(ctx, sc, p); err != nil {
		return fail("the read as the source gave it was refused", err)
	}
	other := p
	other.ScopeExpiresAt = anotherScope(p)
	if e := expect(a.AuthorizeCurrent(ctx, sc, other), r.ErrDenied); e != nil {
		return fail("the read with another scope expiry", e)
	}
	without := p
	without.ScopeExpiresAt = time.Time{}
	if e := expect(a.AuthorizeCurrent(ctx, sc, without), r.ErrDenied); e != nil {
		return fail("the read without its scope expiry", e)
	}
	if err := a.AuthorizeCurrent(ctx, sc, p); err != nil {
		return fail("the read as the source gave it was refused after the two that were not: their refusals show nothing", err)
	}
	return nil
}

// anotherScope is a scope expiry the source did not give for the read. It is one the adapter still sends, and it lies as far ahead
// of the clock as the one the source gave, so that a source which takes the scope's end from the request goes on saying yes to it
// and is found out: an hour later, or, for a scope within the last hour the protocol's times can name (what a product that vouches
// "for ever" answers), an hour earlier. K1 has shown that the scope is after the read's expiry; should neither of the two be a scope
// of the read at all, the read's own expiry is.
func anotherScope(p r.PreparedRead) time.Time {
	for _, d := range []time.Duration{time.Hour, -time.Hour} {
		other := p
		other.ScopeExpiresAt = p.ScopeExpiresAt.Add(d)
		if other.ValidLifetimes() && other.ScopeExpiresAt.After(p.ExpiresAt) {
			return other.ScopeExpiresAt
		}
	}
	return p.ExpiresAt
}
