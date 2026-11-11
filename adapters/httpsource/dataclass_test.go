package httpsource_test

import (
	"context"
	"testing"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
)

// word is a data class said by the adapter it is embedded in (reporting.ClassedAdapter).
type word string

func (w word) DataClass() string { return string(w) }

// The six shapes an adapter behind the reference source may have (issuance none, rows or groups, with or without the company
// directory), each saying a data class.
type (
	shapePlain struct {
		r.Adapter
		word
	}
	shapeDirectory struct {
		r.Adapter
		r.CompanyDirectory
		word
	}
	shapeRows struct {
		r.Adapter
		r.DocumentAuthority
		word
	}
	shapeRowsDirectory struct {
		r.Adapter
		r.DocumentAuthority
		r.CompanyDirectory
		word
	}
	shapeGroups struct {
		r.Adapter
		r.DocumentAuthority
		word
	}
	shapeGroupsDirectory struct {
		r.Adapter
		r.DocumentAuthority
		r.CompanyDirectory
		word
	}
)

func (shapeGroups) IssueContractVersion() int          { return r.IssueContractGroups }
func (shapeGroupsDirectory) IssueContractVersion() int { return r.IssueContractGroups }

// TestTheAdapterSaysNoDataClassAndTheReferenceCompositionPassesTheWrappedOneOn (S08-A, ADR 0012 decision 6). The protocol carries no data
// class and the platform's adapter of a product says none, in any of its forms: a product behind the protocol is business data. The
// reference composition of the tests (sourcetest.Remote) knows what it put behind the protocol: its adapter says the wrapped adapter's
// word and keeps exactly the capabilities the connection declares; a wrapped adapter that says nothing is the platform's adapter as it is.
func TestTheAdapterSaysNoDataClassAndTheReferenceCompositionPassesTheWrappedOneOn(t *testing.T) {
	ctx := context.Background()
	g := newRig(t)
	for name, configure := range map[string]func(*httpsource.Config){
		"no issuance":            func(c *httpsource.Config) { c.Issuance = httpsource.IssuanceNone },
		"a directory":            func(c *httpsource.Config) { c.Issuance, c.Companies = httpsource.IssuanceNone, true },
		"rows":                   func(c *httpsource.Config) { c.Issuance = httpsource.IssuanceRows },
		"rows and a directory":   func(c *httpsource.Config) { c.Issuance, c.Companies = httpsource.IssuanceRows, true },
		"groups":                 func(c *httpsource.Config) { c.Issuance = httpsource.IssuanceGroups },
		"groups and a directory": func(c *httpsource.Config) { c.Issuance, c.Companies = httpsource.IssuanceGroups, true },
	} {
		a := adapterOf(t, g.srv, configure)
		if _, says := a.(r.ClassedAdapter); says || r.DataClassOf(a) != "business-data" {
			t.Errorf("the platform's adapter with %s says a data class of its own, or is not business-data: %q", name, r.DataClassOf(a))
		}
	}
	// The simulator itself behind the reference source: its word, with the issuance by groups and the directory it has.
	if out := sourcetest.Remote(t, g.sim); r.DataClassOf(out) != "contract-simulator" || !r.GroupIssueSupported(out) {
		t.Errorf("the simulator through the protocol says %q", r.DataClassOf(out))
	} else if _, directory := out.(r.CompanyDirectory); !directory {
		t.Error("the simulator through the protocol lost its directory")
	}
	for name, c := range map[string]struct {
		wrapped                      r.Adapter
		authority, groups, directory bool
	}{
		"no issuance":            {shapePlain{g.sim, "manual-entry"}, false, false, false},
		"a directory":            {shapeDirectory{g.sim, g.sim, "manual-entry"}, false, false, true},
		"rows":                   {shapeRows{g.sim, g.sim, "manual-entry"}, true, false, false},
		"rows and a directory":   {shapeRowsDirectory{g.sim, g.sim, g.sim, "manual-entry"}, true, false, true},
		"groups":                 {shapeGroups{g.sim, g.sim, "manual-entry"}, true, true, false},
		"groups and a directory": {shapeGroupsDirectory{g.sim, g.sim, g.sim, "manual-entry"}, true, true, true},
	} {
		out := sourcetest.Remote(t, c.wrapped)
		if got := r.DataClassOf(out); got != "manual-entry" {
			t.Errorf("%s: the adapter says %q, want the wrapped adapter's manual-entry", name, got)
		}
		_, authority := out.(r.DocumentAuthority)
		_, directory := out.(r.CompanyDirectory)
		_, closes := out.(interface{ Close() })
		if authority != c.authority || r.GroupIssueSupported(out) != c.groups || directory != c.directory || !closes {
			t.Errorf("%s: authority %v, groups %v, directory %v, closes %v", name, authority, r.GroupIssueSupported(out), directory, closes)
		}
		// It is still the platform's adapter of the source: the catalogue comes through the protocol.
		if ds, err := out.DescribeCatalog(ctx, g.sc); err != nil || len(ds) != 1 || ds[0].Ref.ID != "receivables.aging" {
			t.Errorf("%s: the catalogue through the protocol: %v (%v)", name, ds, err)
		}
	}
	// A wrapped adapter that says no class: the platform's adapter as it is, a source product's data.
	out := sourcetest.Remote(t, struct{ r.Adapter }{g.sim})
	if _, same := out.(*httpsource.Adapter); !same || r.DataClassOf(out) != "business-data" {
		t.Errorf("an adapter that says nothing, through the protocol: a %T that says %q", out, r.DataClassOf(out))
	}
	// Through a service, in process and through the protocol alike: the evidence says the simulator's word.
	for _, direct := range []bool{true, false} {
		w := newWorld(t, direct, 3, nil)
		res, err := w.s.Query(ctx, w.p, contractsim.FinancialQuery())
		if err != nil || len(res.Rows) != 3 || res.Evidence.DataClass != "contract-simulator" {
			t.Errorf("direct %v: the evidence says %q (%v)", direct, res.Evidence.DataClass, err)
		}
	}
}
