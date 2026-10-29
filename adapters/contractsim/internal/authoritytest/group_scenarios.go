package authoritytest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
)

// Contract version 2 of the document authority (S03-K): the intent of a row group of a schema 2.0 template. Both
// contract simulators declare it, take the group as they take a row (the same reservation, idempotency, void and
// tombstone rules) and check every row of the group against their own frozen read. The scenarios are the suite's
// rule scenarios for the group; they run against either simulator.

var (
	byCustomerA = func() *r.IssueGroup { return GroupOf([]string{"customer.id"}, []string{"customer_a"}, 0, 2, 4) }
	byCustomerB = func() *r.IssueGroup { return GroupOf([]string{"customer.id"}, []string{"customer_b"}, 1) }
	byCurrent   = func() *r.IssueGroup { return GroupOf([]string{"aging.bucket"}, []string{"current"}, 0, 3, 4) }
	everyRow    = func() *r.IssueGroup { return GroupOf(nil, nil, 0, 1, 2, 3, 4) }
)

// literalDigest is the digest of customer_a's three rows as docs/API.md specifies the digest of rows, spelled out from
// the JSON text and not computed by the platform's own function.
func literalDigest() string {
	sum := sha256.Sum256([]byte(`[["customer_a","current","10.00"],["customer_a","1_30","30.00"],["customer_a","current","50.00"]]`))
	return hex.EncodeToString(sum[:])
}

// rowOfGroupData asks for the n-th job to print one row of GroupData, with that row's digest.
func rowOfGroupData(g *Rig, n, row int) (r.IssueReservation, error) {
	in := g.Intent(n, row)
	in.RowSHA256 = r.Digest(GroupData[row])
	return g.Src.PrepareDocument(ctx, g.SC, in)
}

var groupScenarios = []ruleScenario{
	{"the_adapter_declares_contract_version_2", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		v, ok := g.Src.(r.VersionedAuthority)
		if !ok || v.IssueContractVersion() != 2 || r.IssueContractVersion(g.Src) != 2 || !r.GroupIssueSupported(g.Src) {
			t.Fatal("the simulator does not declare contract version 2")
		}
	}},
	{"a_group_is_reserved_and_numbered_as_a_row_is", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		in := byCustomerA()
		if in.RowsSHA256 != literalDigest() {
			t.Fatalf("the platform's digest of the rows %s is not the documented one %s", in.RowsSHA256, literalDigest())
		}
		res := g.ReserveGroup(1, in)
		if !r.ValidProductRef(res.Ref) || !r.ValidProductTime(res.IssuedAt) || res.JobID != Job(1) || res.Number != "AR-000001" {
			t.Fatalf("%+v", res)
		}
		utcSecond(t, res.IssuedAt, "issue time")
		near(t, g, res.IssuedAt, "issue time")
		rec := g.Holds(1, contractsim.IssueReserved, "AR-000001")
		if rec.Ref != res.Ref || rec.JobID != Job(1) {
			t.Fatalf("%+v", rec)
		}
		// a row and another group take the next numbers, in the order of the reservations
		if row, e := rowOfGroupData(g, 2, 1); e != nil || row.Number != "AR-000002" {
			t.Fatal(row, e)
		}
		if other := g.ReserveGroup(3, byCustomerB()); other.Number != "AR-000003" {
			t.Fatal(other.Number)
		}
		g.Confirm(g.ReserveGroup(4, byCurrent()))
		g.Holds(4, contractsim.IssueConfirmed, "AR-000004")
	}},
	{"the_same_group_returns_the_same_reservation", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		first := g.ReserveGroup(1, byCustomerA())
		// the group made again, its lists made another way, is the same intent
		again, e := g.PrepareGroup(1, &r.IssueGroup{Fields: []string{"customer.id"}, Key: []string{"customer_a"}, Rows: []int{0, 2, 4}, RowsSHA256: literalDigest()})
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, first)
		if res := g.ReserveGroup(2, byCustomerB()); res.Number != "AR-000002" {
			t.Fatal("asking again used a number:", res.Number)
		}
		g.Confirm(first)
		again, e = g.PrepareGroup(1, byCustomerA())
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, first)
		// the whole snapshot with no fields: its lists are empty, whether nil or not
		whole := g.ReserveGroup(3, everyRow())
		nils := everyRow()
		nils.Fields, nils.Key = nil, nil
		again, e = g.PrepareGroup(3, nils)
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, whole)
	}},
	{"another_group_or_a_row_under_the_job_conflicts", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		res := g.ReserveGroup(1, byCustomerA())
		// Each difference is a valid intent for the frozen read, so that only the job's own record can refuse it.
		differences := map[string]func(in *r.IssueIntent){
			"another group": func(in *r.IssueIntent) { in.Group = byCustomerB() },
			"another fields": func(in *r.IssueIntent) {
				in.Group = GroupOf([]string{"customer.id", "aging.bucket"}, []string{"customer_a", "current"}, 0, 4)
			},
			"another rows":       func(in *r.IssueIntent) { in.Group = byCurrent() },
			"the whole snapshot": func(in *r.IssueIntent) { in.Group = everyRow() },
			"a row":              func(in *r.IssueIntent) { in.Group, in.RowIndex, in.RowSHA256 = nil, 0, r.Digest(GroupData[0]) },
			"template":           func(in *r.IssueIntent) { in.TemplateID = "fedcba9876543210fedcba9876543210" },
			"template version":   func(in *r.IssueIntent) { in.TemplateVersion = 4 },
			"template digest":    func(in *r.IssueIntent) { in.TemplateSHA256 = strings.Repeat("b", 64) },
		}
		for _, state := range []string{contractsim.IssueReserved, contractsim.IssueConfirmed} {
			for name, change := range differences {
				in := g.GroupIntent(1, byCustomerA())
				change(&in)
				_, e := g.Src.PrepareDocument(ctx, g.SC, in)
				is(t, e, r.ErrConflict, name+" of a "+state+" job")
			}
			if state == contractsim.IssueReserved {
				g.Confirm(res)
			}
		}
		again, e := g.PrepareGroup(1, byCustomerA())
		if e != nil {
			t.Fatal(e)
		}
		sameReservation(t, again, res)
		g.Holds(1, contractsim.IssueConfirmed, "AR-000001")
		// and a group asked under the job of a row conflicts the other way round
		if _, e = rowOfGroupData(g, 2, 0); e != nil {
			t.Fatal(e)
		}
		_, e = g.PrepareGroup(2, byCustomerA())
		is(t, e, r.ErrConflict, "a group under the job of a row")
	}},
	{"the_group_must_be_the_one_the_frozen_read_produced", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		bad := map[string]func() *r.IssueGroup{
			"a digest of other rows": func() *r.IssueGroup {
				x := byCustomerA()
				x.RowsSHA256 = r.RowsDigest(rowsOf(GroupData, []int{0, 2}))
				return x
			},
			"a digest of nothing":  func() *r.IssueGroup { x := byCustomerA(); x.RowsSHA256 = strings.Repeat("0", 64); return x },
			"a digest of one row":  func() *r.IssueGroup { x := byCustomerA(); x.RowsSHA256 = r.Digest(GroupData[0]); return x },
			"a row left out":       func() *r.IssueGroup { return GroupOf([]string{"customer.id"}, []string{"customer_a"}, 0, 2) },
			"a row of another key": func() *r.IssueGroup { return GroupOf([]string{"customer.id"}, []string{"customer_a"}, 0, 1, 2, 4) },
			"the first row past":   func() *r.IssueGroup { x := byCustomerA(); x.Rows = []int{0, 2, 4, 5}; return x },
			"another customer":     func() *r.IssueGroup { x := byCustomerA(); x.Key = []string{"customer_b"}; return x },
			"a customer the read lacks": func() *r.IssueGroup {
				x := byCustomerA()
				x.Key = []string{"customer_z"}
				return x
			},
			"a key of another case": func() *r.IssueGroup { x := byCustomerA(); x.Key = []string{"Customer_a"}; return x },
			"a metric as a field": func() *r.IssueGroup {
				return &r.IssueGroup{Fields: []string{"ar.open_balance"}, Key: []string{"10.00"}, Rows: []int{0}, RowsSHA256: r.RowsDigest(rowsOf(GroupData, []int{0}))}
			},
			"a field the read lacks": func() *r.IssueGroup {
				return &r.IssueGroup{Fields: []string{"ghost"}, Key: []string{"customer_a"}, Rows: []int{0, 2, 4}, RowsSHA256: literalDigest()}
			},
			"the whole snapshot as a keyed group": func() *r.IssueGroup {
				return &r.IssueGroup{Fields: []string{}, Key: []string{}, Rows: []int{0, 2, 4}, RowsSHA256: literalDigest()}
			},
			"the whole snapshot short of a row": func() *r.IssueGroup { return GroupOf(nil, nil, 0, 1, 2, 3) },
			"a keyed group as the whole snapshot": func() *r.IssueGroup {
				return &r.IssueGroup{Fields: []string{}, Key: []string{}, Rows: []int{0, 1, 2, 3, 4}, RowsSHA256: literalDigest()}
			},
			"another field's key": func() *r.IssueGroup { return GroupOf([]string{"aging.bucket"}, []string{"customer_a"}, 0, 2, 4) },
		}
		for name, build := range bad {
			_, e := g.PrepareGroup(1, build())
			is(t, e, r.ErrDenied, name)
		}
		if _, ok := g.Env.Record(Job(1)); ok {
			t.Fatal("a refusal left a record")
		}
		// every group of the read is accepted with its own digest, and none of the refusals used a number
		for i, in := range []*r.IssueGroup{byCustomerA(), byCustomerB(), byCurrent(), everyRow(), GroupOf([]string{"customer.id", "aging.bucket"}, []string{"customer_a", "current"}, 0, 4), GroupOf([]string{"aging.bucket", "customer.id"}, []string{"1_30", "customer_a"}, 2)} {
			res, e := g.PrepareGroup(i+1, in)
			if e != nil || res.Number != fmt.Sprintf("AR-%06d", i+1) {
				t.Fatalf("group %d: %+v %v", i, res, e)
			}
		}
	}},
	{"the_group_is_the_frozen_reads_not_the_current_data", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		g.Env.ReplaceRecords([]contractsim.Record{{Company: "company_demo", Key: "g1", Values: []string{"customer_a", "current", "999.00"}}})
		// the product's current data has one row of customer_a; the frozen read has three
		if _, e := g.PrepareGroup(1, byCustomerA()); e != nil {
			t.Fatal("customer_a of the frozen read:", e)
		}
		if _, e := g.PrepareGroup(2, byCustomerB()); e != nil {
			t.Fatal("customer_b of the frozen read, whom the current data lacks:", e)
		}
		now := GroupOf([]string{"customer.id"}, []string{"customer_a"}, 0)
		now.RowsSHA256 = r.RowsDigest([][]string{{"customer_a", "current", "999.00"}})
		_, e := g.PrepareGroup(3, now)
		is(t, e, r.ErrDenied, "the current data's group")
	}},
	{"groups_beyond_the_first_source_pages_are_checked", func(t *testing.T, newEnv NewEnv) {
		g := Bare(t, newEnv, Finance())
		grant, data := g.P.Grant, []contractsim.Record{}
		grant.Rows = []string{}
		var rows [][]string
		var members []int
		for i := 0; i < 250; i++ {
			key := fmt.Sprintf("row_%04d", i)
			customer := fmt.Sprintf("customer_%d", i%5)
			grant.Rows = append(grant.Rows, key)
			data = append(data, contractsim.Record{Company: "company_demo", Key: key, Values: []string{customer, "current", "1.00"}})
			rows = append(rows, []string{customer, "current", "1.00"})
			if i%5 == 3 {
				members = append(members, i)
			}
		}
		g.Env.ReplaceRecords(data)
		g.Env.SetGrant(g.SC, grant)
		g.Prep = g.Read(g.SC)
		group := func(rows [][]string, ids []int) *r.IssueGroup {
			return &r.IssueGroup{Fields: []string{"customer.id"}, Key: []string{"customer_3"}, Rows: ids, RowsSHA256: r.RowsDigest(rowsOf(rows, ids))}
		}
		// customer_3 has 50 rows, up to row 248, which the source returns on its third page
		if len(members) != 50 || members[49] != 248 {
			t.Fatal(len(members))
		}
		if _, e := g.PrepareGroup(1, group(rows, members)); e != nil {
			t.Fatal("the group across the source's pages:", e)
		}
		// the last row left out, and the first row of another customer put in
		_, e := g.PrepareGroup(2, group(rows, members[:49]))
		is(t, e, r.ErrDenied, "a group short of its last row")
		swapped := append(append([]int{}, members[:49]...), 249)
		_, e = g.PrepareGroup(3, group(rows, swapped))
		is(t, e, r.ErrDenied, "a group with a row of another customer")
	}},
	{"a_group_intent_that_is_not_well_formed_is_invalid", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		bad := map[string]func(*r.IssueIntent){
			"a row index as well":            func(in *r.IssueIntent) { in.RowIndex = 1 },
			"a row digest as well":           func(in *r.IssueIntent) { in.RowSHA256 = g.P.RowSHA256[0] },
			"no rows":                        func(in *r.IssueIntent) { in.Group.Rows = []int{} },
			"nil rows":                       func(in *r.IssueIntent) { in.Group.Rows = nil },
			"rows out of order":              func(in *r.IssueIntent) { in.Group.Rows = []int{2, 0, 4} },
			"a row twice":                    func(in *r.IssueIntent) { in.Group.Rows = []int{0, 0, 2, 4} },
			"a negative row":                 func(in *r.IssueIntent) { in.Group.Rows = []int{-1, 0, 2, 4} },
			"a row past the platform's rows": func(in *r.IssueIntent) { in.Group.Rows = []int{0, 2, 4, r.MaxRows} },
			"more keys than fields":          func(in *r.IssueIntent) { in.Group.Key = []string{"customer_a", "x"} },
			"fewer keys than fields":         func(in *r.IssueIntent) { in.Group.Key = []string{} },
			"a field twice": func(in *r.IssueIntent) {
				in.Group.Fields, in.Group.Key = []string{"customer.id", "customer.id"}, []string{"customer_a", "customer_a"}
			},
			"four fields": func(in *r.IssueIntent) {
				in.Group.Fields, in.Group.Key = []string{"customer.id", "aging.bucket", "a", "b"}, []string{"customer_a", "current", "x", "y"}
			},
			"an empty field name":      func(in *r.IssueIntent) { in.Group.Fields = []string{""} },
			"a digest too short":       func(in *r.IssueIntent) { in.Group.RowsSHA256 = in.Group.RowsSHA256[:63] },
			"a digest in upper case":   func(in *r.IssueIntent) { in.Group.RowsSHA256 = strings.ToUpper(in.Group.RowsSHA256) },
			"a digest not hexadecimal": func(in *r.IssueIntent) { in.Group.RowsSHA256 = strings.Repeat("z", 64) },
			"no digest":                func(in *r.IssueIntent) { in.Group.RowsSHA256 = "" },
			"a key that is not UTF-8":  func(in *r.IssueIntent) { in.Group.Key = []string{"customer_a\xff"} },
			"job id empty":             func(in *r.IssueIntent) { in.JobID = "" },
			"template version zero":    func(in *r.IssueIntent) { in.TemplateVersion = 0 },
			"template digest empty":    func(in *r.IssueIntent) { in.TemplateSHA256 = "" },
		}
		for name, change := range bad {
			in := g.GroupIntent(1, byCustomerA())
			change(&in)
			_, e := g.Src.PrepareDocument(ctx, g.SC, in)
			is(t, e, r.ErrInvalid, name)
		}
		// a row intent that carries neither row nor group is as invalid as it was
		in := g.GroupIntent(1, nil)
		_, e := g.Src.PrepareDocument(ctx, g.SC, in)
		is(t, e, r.ErrInvalid, "an intent of nothing")
		if _, ok := g.Env.Record(Job(1)); ok {
			t.Fatal("a refusal left a record")
		}
	}},
	{"refused_group_calls_consume_no_number", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		wrong := byCustomerA()
		wrong.RowsSHA256 = strings.Repeat("0", 64)
		malformed := g.GroupIntent(1, byCustomerA())
		malformed.TemplateVersion = 0
		_, e := g.PrepareGroup(1, wrong)
		is(t, e, r.ErrDenied, "wrong group")
		_, e = g.Src.PrepareDocument(ctx, g.SC, malformed)
		is(t, e, r.ErrInvalid, "malformed intent")
		g.Env.SetRefused(true)
		_, e = g.PrepareGroup(1, byCustomerA())
		is(t, e, r.ErrDenied, "not approved")
		g.Env.SetRefused(false)
		noIssue := g.P.Grant
		noIssue.IssueDocuments = false
		g.Env.SetGrant(g.SC, noIssue)
		_, e = g.PrepareGroup(1, byCustomerA())
		is(t, e, r.ErrDenied, "no right to issue")
		g.Env.SetGrant(g.SC, g.P.Grant)
		g.Env.SetUnavailable(true)
		_, e = g.PrepareGroup(1, byCustomerA())
		is(t, e, r.ErrUnavailable, "outage")
		g.Env.SetUnavailable(false)
		g.Env.Revoke(g.SC)
		_, e = g.PrepareGroup(1, byCustomerA())
		is(t, e, r.ErrDenied, "revoked principal")
		g.Env.SetGrant(g.SC, g.P.Grant)
		if _, ok := g.Env.Record(Job(1)); ok {
			t.Fatal("a refused call left a record")
		}
		if res := g.ReserveGroup(1, byCustomerA()); res.Number != "AR-000001" {
			t.Fatal("refused calls used numbers:", res.Number)
		}
		// the checks apply to a job that is reserved already, as for a row
		g.Env.SetRefused(true)
		_, e = g.PrepareGroup(1, byCustomerA())
		is(t, e, r.ErrDenied, "not approved, existing job")
		g.Env.SetRefused(false)
		// the context must be the intent's, for a group as for a row
		colleague := g.SC
		colleague.Owner.Subject = "user_b"
		g.Env.SetGrant(colleague, g.P.Grant)
		_, e = g.Src.PrepareDocument(ctx, colleague, g.GroupIntent(2, byCustomerA()))
		is(t, e, r.ErrDenied, "other principal")
	}},
	{"a_group_is_confirmed_and_voided_as_a_row_is", func(t *testing.T, newEnv NewEnv) {
		g := NewGroupRig(t, newEnv)
		res := g.ReserveGroup(1, byCustomerA())
		rc := g.Confirm(res)
		if !r.ValidProductRef(rc.Ref) || !r.ValidProductTime(rc.ConfirmedAt) {
			t.Fatalf("%+v", rc)
		}
		g.Holds(1, contractsim.IssueConfirmed, "AR-000001")
		if e := g.Src.VoidIssue(ctx, g.SC, Job(1), "ISSUE_UNRECONCILED"); e != nil {
			t.Fatal(e)
		}
		rec := g.Holds(1, contractsim.IssueVoid, "AR-000001")
		if rec.Reason != "ISSUE_UNRECONCILED" || rec.SHA256 == "" {
			t.Fatalf("%+v", rec)
		}
		// the voided group stays voided, and its number is not handed out again
		_, e := g.PrepareGroup(1, byCustomerA())
		is(t, e, r.ErrConflict, "group asked again after its void")
		if next := g.ReserveGroup(2, byCustomerA()); next.Number != "AR-000002" {
			t.Fatal("a voided number was handed out again:", next.Number)
		}
		// a void of a group job nothing was reserved for is a tombstone like any other
		if e = g.Src.VoidIssue(ctx, g.SC, Job(3), "EXECUTION_FAILED"); e != nil {
			t.Fatal(e)
		}
		_, e = g.PrepareGroup(3, byCustomerB())
		is(t, e, r.ErrConflict, "group asked after a tombstone")
	}},
}
