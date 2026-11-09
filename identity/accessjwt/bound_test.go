package accessjwt_test

import (
	"slices"
	"testing"

	"idp.local/reporting/identity/accessjwt"
	r "idp.local/reporting/reporting"
)

// Bound (S16-A) is the principal a scheduled run acts as: the bindings in force for the identity, whatever its client; with several, only
// what they all grant; nothing for an identity no binding names, for another issuer, or after a reload removed it.
func TestBoundIsTheBindingInForce(t *testing.T) {
	c := config(t, key(t))
	c.Bindings = []accessjwt.Binding{
		{Subject: "subject-a", ClientID: "web-a", Tenant: "platform-a", Integrations: []string{"finance-a", "ship-a"}, Roles: []string{r.RoleViewer, r.RoleDesigner}},
		{Subject: "subject-a", ClientID: "cli-a", Tenant: "platform-a", Integrations: []string{"ship-a", "finance-a", "other"}},
		{Subject: "subject-b", ClientID: "web-a", Tenant: "platform-a", Integrations: []string{"finance-a"}, Roles: []string{r.RoleDesigner}},
	}
	v, e := accessjwt.New(c)
	if e != nil {
		t.Fatal(e)
	}
	a := r.Identity{Tenant: "platform-a", Issuer: c.Issuer, Subject: "subject-a"}
	p, ok := v.Bound(a)
	if !ok || p.Identity != a || !slices.Equal(p.Integrations, []string{"finance-a", "ship-a"}) || !slices.Equal(p.Roles, []string{r.RoleViewer, r.RoleDesigner}) {
		t.Fatalf("%+v %v", p, ok)
	}
	b, ok := v.Bound(r.Identity{Tenant: "platform-a", Issuer: c.Issuer, Subject: "subject-b"})
	if !ok || b.Has(r.RoleViewer) || !b.Has(r.RoleDesigner) {
		t.Fatalf("%+v", b)
	}
	for _, i := range []r.Identity{{Tenant: "platform-a", Issuer: c.Issuer, Subject: "subject-c"}, {Tenant: "platform-b", Issuer: c.Issuer, Subject: "subject-a"}, {Tenant: "platform-a", Issuer: "https://other.example", Subject: "subject-a"}, {}} {
		if _, ok := v.Bound(i); ok {
			t.Fatalf("%+v is bound", i)
		}
	}
	// Two bindings that share no product bind nothing.
	c.Bindings = []accessjwt.Binding{{Subject: "subject-a", ClientID: "web-a", Tenant: "platform-a", Integrations: []string{"finance-a"}}, {Subject: "subject-a", ClientID: "cli-a", Tenant: "platform-a", Integrations: []string{"ship-a"}}}
	if e = v.Replace(c); e != nil {
		t.Fatal(e)
	}
	if _, ok := v.Bound(a); ok {
		t.Fatal("bindings without a common product bind")
	}
	c.Bindings = nil
	if e = v.Replace(c); e != nil {
		t.Fatal(e)
	}
	if _, ok := v.Bound(a); ok {
		t.Fatal("a removed binding still binds")
	}
	var zero *accessjwt.Verifier
	if _, ok := zero.Bound(a); ok {
		t.Fatal("the nil verifier binds")
	}
}
