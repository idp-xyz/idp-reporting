// Package authoritytest is the conformance suite of the document-authority capability
// (reporting.DocumentAuthority, docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md, S03-H H5): the rules both
// contract simulators implement, written once as scenarios and run against either of them. A
// scenario isolates one cause of every refusal it checks. Time-dependent rules that need a clock
// the test controls are tested in the simulators' own packages.
package authoritytest

import (
	"context"
	"errors"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"testing"
	"time"
)

// Source is a contract simulator as the suite uses it: the adapter that freezes reads and the
// document authority that issues from them.
type Source interface {
	r.Adapter
	r.DocumentAuthority
}

// Env is one simulator under test with the fixture controls the scenarios need. A control acts on
// every integration the Env was made for. The in-memory simulator is one source, the durable one
// a source per integration; the scenarios do not depend on the difference.
type Env interface {
	Source() Source
	// Now is the simulator's clock, roughly: the issue time of a reservation made now.
	Now() time.Time
	// SetGrant replaces the principal's current grant, Revoke removes it.
	SetGrant(sc r.SourceContext, g contractsim.Grant)
	Revoke(sc r.SourceContext)
	SetUnavailable(v bool)
	SetRefused(v bool)
	// ReplaceRecords gives the source new data; frozen reads keep the data they were made on.
	ReplaceRecords(records []contractsim.Record)
	// Expire lets the job's reservation expire while the source read it was made from stays valid.
	Expire(jobID string)
	// Record is the simulator's lookup of what it holds for a job id.
	Record(jobID string) (contractsim.IssueRecord, bool)
}

// NewEnv makes a fresh simulator for the profile, with a source for each of the integrations and
// the profile's fixture (and nothing else: no grant), numbering from the start.
type NewEnv func(t *testing.T, p Profile, integrations ...string) Env

// Profile is a fixture the suite runs on, with the literal expectations about it.
type Profile struct {
	Name        string
	Integration string
	Build       func() *contractsim.Simulator // the fixture: contractsim.Financial or Shipment
	Query       func(integration string) r.QuerySpec
	Grant       contractsim.Grant // what the principal is granted, issuing included
	Prefix      string            // the prefix of its numbers
	RowSHA256   []string          // Digest of each row of the frozen read, in order
}

// Finance is the receivables fixture: numbers AR-nnnnnn, three rows.
func Finance() Profile {
	return Profile{Name: "finance", Integration: contractsim.FinanceIntegration, Build: contractsim.Financial,
		Query: func(id string) r.QuerySpec { q := contractsim.FinancialQuery(); q.Integration = id; return q },
		Grant: contractsim.DefaultGrant(), Prefix: "AR",
		// sha256 of ["customer_a","current","100.25"], ["customer_b","1_30","-0.24"] and
		// ["customer_c","31_60","9007199254740893.00"]
		RowSHA256: []string{"be93f000dee340d59eabe645cf39c4b48aa5ce2f1cfffe663fe0bac279b2fec9", "fccfed3024cbe3962c09ecc08f7fdd8b6ab0694bc4c466d24b9c57dfd945c9db", "9ab0728a17bb1e8ac819a89d6e863cd79cb58b8ef28a7f3a0050462e00f0685a"}}
}

// Shipment is the shipment fixture: numbers SH-nnnnnn, two rows.
func Shipment() Profile {
	g := contractsim.DefaultGrant()
	g.CurrencyPolicy = nil
	return Profile{Name: "shipment", Integration: contractsim.ShipmentIntegration, Build: contractsim.Shipment,
		Query: func(id string) r.QuerySpec { q := contractsim.ShipmentQuery(); q.Integration = id; return q },
		Grant: g, Prefix: "SH",
		// sha256 of ["CN","express","9007199254740993"] and ["DE","standard","7"]
		RowSHA256: []string{"09e93afa3c5e4cca7c128fc7234b3f1389d065397d665bedb4029ca3dd2739d0", "d35f5ed3697ced6e38558f8bb2db8701bf5a40b3cac0621db912b54a5f7f101d"}}
}

const (
	// SourceTenant is the only source tenant both simulators grant.
	SourceTenant = "tenant_a"
	// OtherIntegration is a second integration over the same fixture, for numbering per integration.
	OtherIntegration = "other-contract-demo"
	templateID       = "0123456789abcdef0123456789abcdef"
	templateSHA256   = "a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1"
)

// Job is the n-th job id of a scenario: 32 lowercase hexadecimal characters.
func Job(n int) string { return fmt.Sprintf("%032x", n) }

// Rig is one scenario's simulator, principal and frozen read. The simulators' own tests build
// on it for what only they can check.
type Rig struct {
	t    *testing.T
	Env  Env
	P    Profile
	Src  Source
	IDs  []string
	SC   r.SourceContext // the principal's context for the first integration
	Prep r.PreparedRead  // its frozen read of the profile's query
}

// Context is the principal's source context for the integration.
func (g *Rig) Context(integration string) r.SourceContext {
	return r.SourceContext{Owner: contractsim.DemoOwner(contractsim.DemoPrincipal(), integration), SourceTenant: SourceTenant}
}

// Bare is a simulator with no grant and no read yet.
func Bare(t *testing.T, newEnv NewEnv, p Profile, extra ...string) *Rig {
	t.Helper()
	ids := append([]string{p.Integration}, extra...)
	env := newEnv(t, p, ids...)
	g := &Rig{t: t, Env: env, P: p, Src: env.Source(), IDs: ids}
	g.SC = g.Context(ids[0])
	return g
}

// GrantAll grants the principal what the profile says in every integration.
func (g *Rig) GrantAll() {
	for _, id := range g.IDs {
		g.Env.SetGrant(g.Context(id), g.P.Grant)
	}
}

// Read freezes the profile's query for sc.
func (g *Rig) Read(sc r.SourceContext) r.PreparedRead {
	g.t.Helper()
	q := g.P.Query(sc.Owner.Integration)
	prep, e := g.Src.PrepareRead(context.Background(), sc, q, r.QueryHash(q))
	if e != nil {
		g.t.Fatal("the fixture could not be frozen:", e)
	}
	return prep
}

// NewRig is a simulator whose principal may issue, with the profile's query frozen.
func NewRig(t *testing.T, newEnv NewEnv, p Profile, extra ...string) *Rig {
	t.Helper()
	g := Bare(t, newEnv, p, extra...)
	g.GrantAll()
	g.Prep = g.Read(g.SC)
	return g
}

// Intent asks for the n-th job to print the given row of the frozen read.
func (g *Rig) Intent(n, row int) r.IssueIntent {
	return r.IssueIntent{JobID: Job(n), Prepared: g.Prep, RowIndex: row, RowSHA256: g.P.RowSHA256[row], TemplateID: templateID, TemplateVersion: 3, TemplateSHA256: templateSHA256}
}

// Prepare asks the product to authorize the n-th job on the given row.
func (g *Rig) Prepare(n, row int) (r.IssueReservation, error) {
	return g.Src.PrepareDocument(context.Background(), g.SC, g.Intent(n, row))
}

// Reserve prepares the n-th job on row 0 and fails the scenario unless it is reserved.
func (g *Rig) Reserve(n int) r.IssueReservation {
	g.t.Helper()
	res, e := g.Prepare(n, 0)
	if e != nil {
		g.t.Fatalf("job %d was not reserved: %v", n, e)
	}
	return res
}

// Confirmation is the scenario's file confirmed under the reservation: 2 pages of one digest.
func (g *Rig) Confirmation(res r.IssueReservation) r.IssueConfirmation {
	return r.IssueConfirmation{JobID: res.JobID, ReservationRef: res.Ref, SHA256: "5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8", Pages: 2}
}

// Confirm confirms a reservation with the scenario's file and fails unless it is accepted.
func (g *Rig) Confirm(res r.IssueReservation) r.IssueReceipt {
	g.t.Helper()
	rc, e := g.Src.ConfirmIssue(context.Background(), g.SC, g.Confirmation(res))
	if e != nil {
		g.t.Fatalf("job %s was not confirmed: %v", res.JobID, e)
	}
	return rc
}

// Record is what the simulator holds for the n-th job; the scenario fails if it holds nothing.
func (g *Rig) Record(n int) contractsim.IssueRecord {
	g.t.Helper()
	rec, ok := g.Env.Record(Job(n))
	if !ok {
		g.t.Fatalf("the simulator holds nothing for job %d", n)
	}
	return rec
}

// Holds fails unless the simulator holds the n-th job in the state, under the number.
func (g *Rig) Holds(n int, state, number string) contractsim.IssueRecord {
	g.t.Helper()
	rec := g.Record(n)
	if rec.State != state || rec.Number != number {
		g.t.Fatalf("job %d is %s %s, want %s %s", n, rec.State, rec.Number, state, number)
	}
	return rec
}

func is(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}

// Run runs every scenario against the simulators newEnv makes.
func Run(t *testing.T, newEnv NewEnv) {
	t.Helper()
	for _, p := range []Profile{Finance(), Shipment()} {
		t.Run(p.Name, func(t *testing.T) {
			for _, s := range profileScenarios {
				t.Run(s.name, func(t *testing.T) { s.run(t, NewRig(t, newEnv, p)) })
			}
		})
	}
	for _, s := range ruleScenarios {
		t.Run(s.name, func(t *testing.T) { s.run(t, newEnv) })
	}
}
