package accessjwt_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"idp.local/reporting/identity/accessjwt"
	r "idp.local/reporting/reporting"
)

// Emails (S16-B) is where an identity's scheduled runs' ends are mailed: the distinct addresses of the bindings in force that name the identity,
// whatever their client, sorted and bounded to four; none without one, for another issuer, or after a reload removed them. An address is
// validated when the configuration is, as the other fields of a binding are, and a refused configuration changes nothing.
func TestEmailsAreTheBindingsAddresses(t *testing.T) {
	c := config(t, key(t))
	a := r.Identity{Tenant: "platform-a", Issuer: c.Issuer, Subject: "subject-a"}
	c.Bindings = []accessjwt.Binding{
		{Subject: "subject-a", ClientID: "web-a", Tenant: "platform-a", Integrations: []string{"finance-a"}, Email: "zhang.san@example.com"},
		{Subject: "subject-a", ClientID: "cli-a", Tenant: "platform-a", Integrations: []string{"ship-a"}, Email: "zhang.san@example.com"},
		{Subject: "subject-a", ClientID: "app-a", Tenant: "platform-a", Integrations: []string{"finance-a"}, Email: "alt@example.org"},
		{Subject: "subject-a", ClientID: "old-a", Tenant: "platform-a", Integrations: []string{"finance-a"}},
		{Subject: "subject-b", ClientID: "web-a", Tenant: "platform-a", Integrations: []string{"finance-a"}, Email: "li.si@example.com"},
	}
	v, e := accessjwt.New(c)
	if e != nil {
		t.Fatal(e)
	}
	if got := v.Emails(a); !slices.Equal(got, []string{"alt@example.org", "zhang.san@example.com"}) {
		t.Fatalf("%v", got)
	}
	// The bindings share no product, so the identity is not bound; it is still mailed, so that it hears of its refused run.
	if _, ok := v.Bound(a); ok {
		t.Fatal("bindings without a common product bind")
	}
	for _, i := range []r.Identity{{Tenant: "platform-a", Issuer: c.Issuer, Subject: "subject-c"}, {Tenant: "platform-b", Issuer: c.Issuer, Subject: "subject-a"}, {Tenant: "platform-a", Issuer: "https://other.example", Subject: "subject-a"}, {}} {
		if got := v.Emails(i); len(got) != 0 {
			t.Fatalf("%+v is mailed at %v", i, got)
		}
	}
	// Six bindings with six addresses: the first four in sorted order.
	c.Bindings = nil
	for _, address := range []string{"f@example.com", "b@example.com", "d@example.com", "a@example.com", "e@example.com", "c@example.com"} {
		c.Bindings = append(c.Bindings, accessjwt.Binding{Subject: "subject-a", ClientID: "client-" + address, Tenant: "platform-a", Integrations: []string{"finance-a"}, Email: address})
	}
	if e = v.Replace(c); e != nil {
		t.Fatal(e)
	}
	if got := v.Emails(a); !slices.Equal(got, []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"}) {
		t.Fatalf("%v", got)
	}
	// An address that is not one addr-spec refuses the whole configuration, which stays as it was.
	for _, bad := range []string{"Zhang San <zhang.san@example.com>", "<a@example.com>", "a@example.com (comment)", "a@", "@example.com", "a b@example.com", "a@example.com, b@example.com",
		"a@example..com", ".a@example.com", "a@exam ple.com", "a@example.com\r\nBcc: b@example.com", "张三@example.com", "\"quoted\"@example.com", strings.Repeat("a", 243) + "@example.com"} {
		invalid := config(t, key(t))
		invalid.Bindings[0].Email = bad
		if _, err := accessjwt.New(invalid); !errors.Is(err, accessjwt.ErrInvalidConfig) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	replaced := c
	replaced.Bindings = slices.Clone(c.Bindings)
	replaced.Bindings[0].Email = "not an address"
	if err := v.Replace(replaced); !errors.Is(err, accessjwt.ErrInvalidConfig) {
		t.Fatal("a bad address was accepted")
	}
	if got := v.Emails(a); len(got) != 4 {
		t.Fatalf("a refused replacement changed the addresses: %v", got)
	}
	for _, good := range []string{"a@b", "first.last@sub.example.co.uk", "user+tag@example.com", "UPPER@EXAMPLE.COM", "a_b-c@ex-ample.com", strings.Repeat("a", 242) + "@example.com"} {
		valid := config(t, key(t))
		valid.Bindings[0].Email = good
		if _, err := accessjwt.New(valid); err != nil {
			t.Errorf("%q refused", good)
		}
	}
	// Removed with the bindings.
	c.Bindings = nil
	if e = v.Replace(c); e != nil {
		t.Fatal(e)
	}
	if got := v.Emails(a); len(got) != 0 {
		t.Fatal("a removed binding is still mailed")
	}
	var zero *accessjwt.Verifier
	if got := zero.Emails(a); got != nil {
		t.Fatal("the nil verifier mails")
	}
}
