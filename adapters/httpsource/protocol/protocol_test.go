package protocol_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"idp.local/reporting/adapters/httpsource/protocol"
	r "idp.local/reporting/reporting"
)

func context() r.SourceContext {
	return r.SourceContext{Owner: r.Owner{Identity: r.Identity{Tenant: "acme", Issuer: "https://idp.example.com", Subject: "u-123"}, Integration: "erp-finance"}, SourceTenant: "acme-prod"}
}

func read(expires time.Time) r.PreparedRead {
	return r.PreparedRead{Context: context(), Ref: "r-7f3a", ScopeHandle: "handle", QueryHash: "57ef", Dataset: r.Ref{ID: "receivables.aging", Version: "1.0.0"}, Companies: []string{"company_demo"},
		Timezone: "Asia/Shanghai", CurrencyPolicy: &r.Ref{ID: "ledger_base", Version: "1.0.0"}, Watermark: "w-1", ExpiresAt: expires}
}

// A read without a scope is on the wire what it was before the field existed, byte for byte (the literal is what the code before
// S25-B2, main at efb79dc, writes for this value), and a read that vouches for a scope is that read with one more key, last. Each
// goes back to the source as the text it came as, also after a store has kept it.
func TestAReadIsWrittenAsBeforeAndWithItsScopeWhenItVouchesForOne(t *testing.T) {
	expires := time.Date(2026, 11, 9, 10, 8, 0, 0, time.UTC)
	const before = `{"ref":"r-7f3a","scope_handle":"handle","query_hash":"57ef","dataset":{"id":"receivables.aging","version":"1.0.0"},"company_ids":["company_demo"],"timezone":"Asia/Shanghai","currency_policy":{"id":"ledger_base","version":"1.0.0"},"watermark":"w-1","expires_at":"2026-11-09T10:08:00Z"}`
	plain := read(expires)
	b, err := json.Marshal(protocol.ReadOf(plain))
	if err != nil || string(b) != before {
		t.Fatalf("a read without a scope is written\n%s\nwant\n%s (%v)", b, before, err)
	}
	// A scope in another offset and with a fraction, as a source may write it: the text goes out as it came in.
	vouched := plain
	vouched.ScopeExpiresAt = time.Date(2036, 11, 9, 18, 8, 0, 500_000_000, time.FixedZone("", 8*3600))
	b, err = json.Marshal(protocol.ReadOf(vouched))
	if want := before[:len(before)-1] + `,"scope_expires_at":"2036-11-09T18:08:00.5+08:00"}`; err != nil || string(b) != want {
		t.Fatalf("a read with a scope is written\n%s\nwant\n%s (%v)", b, want, err)
	}
	// A scope that ends with the read vouches for nothing more than the read does, and is still what the source said: the key goes
	// back with it. A source that writes the key on every read and compares what comes back would otherwise refuse its own read.
	level := plain
	level.ScopeExpiresAt = expires
	b, err = json.Marshal(protocol.ReadOf(level))
	if want := before[:len(before)-1] + `,"scope_expires_at":"2026-11-09T10:08:00Z"}`; err != nil || string(b) != want {
		t.Fatalf("a read whose scope ends with it is written\n%s\nwant\n%s (%v)", b, want, err)
	}
	for name, p := range map[string]r.PreparedRead{"without a scope": plain, "with a scope": vouched, "with a scope that ends with the read": level} {
		var wire protocol.Read
		b, _ := json.Marshal(protocol.ReadOf(p))
		if err := r.DecodeStrict(bytes.NewReader(b), &wire); err != nil {
			t.Fatal(name, err)
		}
		back, ok := wire.Prepared(context())
		if !ok || !back.ExpiresAt.Equal(p.ExpiresAt) || !back.ScopeExpiresAt.Equal(p.ScopeExpiresAt) {
			t.Fatalf("%s: read back as %+v (%v)", name, back, ok)
		}
		// The read the platform sends back is the same text again.
		again, _ := json.Marshal(protocol.ReadOf(back))
		if string(again) != string(b) {
			t.Fatalf("%s: sent back as\n%s\nwas\n%s", name, again, b)
		}
		if r.Digest(back) != r.Digest(p) {
			t.Fatalf("%s: the read that came back digests as another read", name)
		}
		// And so is the read a store has kept in between: the stores keep a read as its JSON, in the time's own offset.
		kept, err := json.Marshal(back)
		if err != nil {
			t.Fatal(name, err)
		}
		var stored r.PreparedRead
		if err = json.Unmarshal(kept, &stored); err != nil {
			t.Fatal(name, err)
		}
		if after, _ := json.Marshal(protocol.ReadOf(stored)); string(after) != string(b) {
			t.Fatalf("%s: sent back from a store as\n%s\nwas\n%s", name, after, b)
		}
	}
}

// A scope's expiry that is present is a time: the key with an empty text, with a time that is not canonical, or with the zero time
// (which a prepared read cannot tell from none, and would send back without the key) is no read at all.
func TestAScopeExpiryThatIsPresentIsATime(t *testing.T) {
	text := func(s string) *string { return &s }
	base := protocol.ReadOf(read(time.Date(2026, 11, 9, 10, 8, 0, 0, time.UTC)))
	for name, scope := range map[string]*string{
		"an empty text":      text(""),
		"not a time":         text("tomorrow"),
		"trailing zeros":     text("2026-11-10T10:08:00.500Z"),
		"a space for the T":  text("2026-11-10 10:08:00Z"),
		"an offset of zero":  text("2026-11-10T10:08:00+00:00"),
		"an offset of a day": text("2026-11-11T10:08:00+24:00"),
		"the zero time":      text("0001-01-01T00:00:00Z"),
		"the zero time's instant in another offset": text("0001-01-01T08:00:00+08:00"),
		"a date":                 text("2026-11-10"),
		"a time without a zone":  text("2026-11-10T10:08:00"),
		"a year of five figures": text("10000-01-01T00:00:00Z"),
	} {
		w := base
		w.ScopeExpiresAt = scope
		if p, ok := w.Prepared(context()); ok {
			t.Errorf("%s was read as a scope expiring at %s", name, p.ScopeExpiresAt)
		}
	}
	// What is a time is read, also where the platform will not take it: before the read's own expiry is the caller's question
	// (reporting.PreparedRead.ValidLifetimes), not the wire's.
	w := base
	w.ScopeExpiresAt = text("2026-11-09T10:07:59Z")
	if p, ok := w.Prepared(context()); !ok || p.ValidLifetimes() {
		t.Fatalf("a scope before the read's expiry: read %v, and the platform takes it: %v", ok, p.ValidLifetimes())
	}
	w.ScopeExpiresAt = nil
	if p, ok := w.Prepared(context()); !ok || !p.ScopeExpiresAt.IsZero() || !p.ScopeEnd().Equal(p.ExpiresAt) {
		t.Fatalf("a read without the key: %+v %v", p, ok)
	}
}

// A protocol time is the text Go's RFC3339Nano writes for it, of a time that JSON can write as it is. Go's parser reads an offset of a
// day or more (+24:00 to +24:59, either sign) and writes it back; RFC 3339 has none, and a store cannot encode it, so a read that
// carried one passed the adapter and failed, untyped, where the first job made from it was stored. Every time of the protocol is
// read here.
func TestAProtocolTimeIsCanonicalAndOneAStoreCanKeep(t *testing.T) {
	for _, good := range []string{
		"2026-11-09T10:08:00Z", "2026-11-09T10:08:00.5Z", "2026-11-09T10:08:00.123456789Z", "2026-11-09T18:08:00+08:00", "2026-11-09T02:08:00-08:00",
		"2026-11-09T10:08:00+23:59", "2026-11-09T10:08:00-23:59", "9999-12-31T23:59:59Z", "1970-01-01T00:00:00Z",
	} {
		at, ok := protocol.ParseTime(good)
		if !ok || protocol.FormatTime(at) != good {
			t.Errorf("%s is not read as itself: %v %s", good, ok, protocol.FormatTime(at))
			continue
		}
		if _, err := json.Marshal(at); err != nil {
			t.Errorf("%s is read and cannot be stored: %v", good, err)
		}
	}
	for _, bad := range []string{
		"", "2026-11-09", "2026-11-09T10:08:00", "2026-11-09T10:08:00z", "2026-11-09t10:08:00Z", "2026-11-09 10:08:00Z", "2026-11-09T10:08:00.50Z", "2026-11-09T10:08:00.000Z",
		"2026-11-09T10:08:00.1234567891Z", "2026-11-09T10:08:00,5Z", "2026-11-09T10:08:00+00:00", "2026-11-09T10:08:00-00:00", "2026-11-09T10:08:00+0800", "2026-11-09T10:08:00+08",
		"2026-11-09T24:00:00Z", "2026-11-09T10:60:00Z", "2026-11-09T10:08:60Z", "2026-02-30T10:08:00Z", "2026-13-01T10:08:00Z", "10000-01-01T00:00:00Z",
		"2026-11-09T10:08:00+24:00", "2026-11-09T10:08:00-24:00", "2026-11-09T10:08:00+24:01", "2026-11-09T10:08:00+24:30", "2026-11-09T10:08:00+24:59", "2026-11-09T10:08:00-24:59",
		"2026-11-09T10:08:00+23:60", "2026-11-09T10:08:00+24:60", "2026-11-09T10:08:00+25:00", " 2026-11-09T10:08:00Z", "2026-11-09T10:08:00Z ",
	} {
		if at, ok := protocol.ParseTime(bad); ok {
			t.Errorf("%q is read as %s", bad, protocol.FormatTime(at))
		}
	}
}
