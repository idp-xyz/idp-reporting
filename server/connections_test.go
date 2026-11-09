package server

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"idp.local/reporting/identity/accessjwt"
)

// ADR 0016: a binding must be served by a connection of its tenant, at a reload as at a start; what is in force is recorded as digests and
// counts, never as the identifiers the bindings hold.
func TestReloadHoldsBindingsToTheConnectionsAndRecordsWhatChanged(t *testing.T) {
	w := newWorld(t)
	s := startWorld(t, w)
	k1 := w.keys[0]
	first := mint(t, k1, claims{})
	if s.status(t, first) != 200 {
		t.Fatal("the starting binding")
	}
	start := s.log.events(t, "start")[0]
	rep, err := Check(w.cfg, w.registry(), w.getenv)
	if err != nil {
		t.Fatal(err)
	}
	if start["bindings_sha256"] != rep.BindingsDigest || start["connections_sha256"] != rep.ConnectionsDigest || len(rep.BindingsDigest) != 64 || len(rep.ConnectionsDigest) != 64 {
		t.Fatalf("the start line and check-config disagree: %v %+v", start, rep)
	}
	var printed bytes.Buffer
	rep.Print(&printed)
	if !strings.Contains(printed.String(), rep.BindingsDigest) || !strings.Contains(printed.String(), rep.ConnectionsDigest) {
		t.Fatal(printed.String())
	}

	// An unserved product, alone or beside a served one, or a served product under another tenant: refused, the previous bindings stay.
	good := mustRead(t, w.binds)
	for name, content := range map[string][]byte{
		"an unserved product":       bindingsJSON(bindingJSON{testSubject, testClient, testTenant, []string{w.finance, "unserved-product"}}),
		"another tenant's product":  bindingsJSON(bindingJSON{"second-subject", testClient, "another-tenant", []string{w.finance}}),
		"a second unserved binding": bindingsJSON(bindingJSON{testSubject, testClient, testTenant, []string{w.finance}}, bindingJSON{"second-subject", testClient, testTenant, []string{"unserved-product"}}),
	} {
		replaceFile(t, w.binds, content, 0o644)
		var re *ReloadError
		if err := s.Reload(); !errors.As(err, &re) || re.Code != CodeIssuerBindingUnserved {
			t.Errorf("%s: %v", name, err)
		}
		if s.status(t, first) != 200 {
			t.Errorf("%s: the previous bindings are no longer in force", name)
		}
		replaceFile(t, w.binds, good, 0o644)
	}
	// A file the access-token profile refuses keeps its own code, even when it also names an unserved product.
	replaceFile(t, w.binds, bindingsJSON(bindingJSON{"x", "c", testTenant, []string{"unserved-product"}}, bindingJSON{"x", "c", "t2", []string{"unserved-product"}}), 0o644)
	var refusedFirst *ReloadError
	if !errors.As(s.Reload(), &refusedFirst) || refusedFirst.Code != CodeIssuerConfigRefused {
		t.Error("the profile's refusal comes first")
	}
	replaceFile(t, w.binds, good, 0o644)

	// A reload that changes nothing, then one that adds a binding, then one that removes the first.
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	replaceFile(t, w.binds, bindingsJSON(bindingJSON{testSubject, testClient, testTenant, []string{w.finance}}, bindingJSON{"second-subject", testClient, testTenant, []string{w.finance}}), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	second := mint(t, k1, claims{Sub: "second-subject"})
	if s.status(t, second) != 200 {
		t.Fatal("the added binding")
	}
	replaceFile(t, w.binds, bindingsJSON(bindingJSON{"second-subject", testClient, testTenant, []string{w.finance}}), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	s.cancel()
	<-s.Done()
	var oks []map[string]any
	refused := 0
	for _, line := range s.log.events(t, "reload") {
		switch line["outcome"] {
		case "ok":
			oks = append(oks, line)
		case "refused":
			if line["code"] == CodeIssuerBindingUnserved {
				refused++
			}
		}
	}
	if refused != 3 || len(oks) != 3 {
		t.Fatalf("%d unserved refusals, %d successful reloads", refused, len(oks))
	}
	counts := func(l map[string]any) [3]float64 {
		return [3]float64{l["bindings_added"].(float64), l["bindings_removed"].(float64), l["bindings_changed"].(float64)}
	}
	if counts(oks[0]) != [3]float64{0, 0, 0} || oks[0]["bindings_sha256"] != start["bindings_sha256"] {
		t.Errorf("an unchanged reload: %v", oks[0])
	}
	if counts(oks[1]) != [3]float64{1, 0, 0} || oks[1]["bindings_sha256"] == start["bindings_sha256"] {
		t.Errorf("an added binding: %v", oks[1])
	}
	if counts(oks[2]) != [3]float64{0, 1, 0} {
		t.Errorf("a removed binding: %v", oks[2])
	}
	// The log never names a subject, a client, a tenant or a product.
	for _, id := range []string{testSubject, "second-subject", testClient, testTenant, w.finance, "unserved-product", "another-tenant"} {
		if strings.Contains(s.log.String(), `"`+id+`"`) {
			t.Errorf("the log names %q", id)
		}
	}
}

// check-config refuses unserved bindings with a count and the order to follow, and names nothing.
func TestCheckRefusesUnservedBindings(t *testing.T) {
	w := newWorld(t)
	writeFile(t, w.binds, bindingsJSON(bindingJSON{testSubject, testClient, testTenant, []string{w.finance, "unserved-a"}}, bindingJSON{"other", testClient, testTenant, []string{"unserved-b"}}), 0o644)
	_, err := Check(w.cfg, w.registry(), w.getenv)
	if err == nil || !strings.Contains(err.Error(), "2 product(s)") || !strings.Contains(err.Error(), "before the binding") || strings.Contains(err.Error(), "unserved-a") {
		t.Fatalf("%v", err)
	}
}

// The connections' digest does not depend on their order in the file, and changes with any field, the options included.
func TestConnectionsDigestIsCanonical(t *testing.T) {
	a := ConnectionConfig{Tenant: "t", Integration: "a", SourceTenant: "s", Adapter: "x", Options: []byte(`{}`)}
	b := ConnectionConfig{Tenant: "t", Integration: "b", SourceTenant: "s", Adapter: "x", Options: []byte(`{}`)}
	if connectionsDigest([]ConnectionConfig{a, b}) != connectionsDigest([]ConnectionConfig{b, a}) {
		t.Fatal("order")
	}
	c := b
	c.Options = []byte(`{"u":1}`)
	if connectionsDigest([]ConnectionConfig{a, b}) == connectionsDigest([]ConnectionConfig{a, c}) {
		t.Fatal("options")
	}
}

// A binding is its subject, client and tenant; another list of products is a change, the same products in another order is none.
func TestBindingChangesCountsAddedRemovedChanged(t *testing.T) {
	b := func(sub, tenant string, products ...string) accessjwt.Binding {
		return accessjwt.Binding{Subject: sub, ClientID: "c", Tenant: tenant, Integrations: products}
	}
	before := []accessjwt.Binding{b("a", "t", "x", "y"), b("b", "t", "x"), b("c", "t", "x")}
	after := []accessjwt.Binding{b("a", "t", "y", "x"), b("b", "t", "x", "z"), b("d", "t", "x"), b("c", "t2", "x")}
	if a, r, c := bindingChanges(before, after); a != 2 || r != 1 || c != 1 {
		t.Fatalf("added %d removed %d changed %d", a, r, c)
	}
}
