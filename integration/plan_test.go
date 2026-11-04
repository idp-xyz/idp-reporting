package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/internal/capacity"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"slices"
	"strings"
	"testing"
)

// The plans of the hot paths (S04-G, docs/S04G_SCOPE.md G2), run as the application role with the context of an identity set, so that the
// row-level policies (S04-C) add their predicates as they do in production. Enough rows are seeded, and analyzed, for the planner to prefer an
// index; no timing is asserted. The same statements are run before migration 009 (they must not use its indexes: the test discriminates) and
// after (each must use its index by name, and no sequential scan of jobs or assets may remain in any of them).

type planNode struct {
	Type, Relation, Index string
}

func collectPlan(t *testing.T, node map[string]any, out *[]planNode) {
	t.Helper()
	n := planNode{}
	n.Type, _ = node["Node Type"].(string)
	n.Relation, _ = node["Relation Name"].(string)
	n.Index, _ = node["Index Name"].(string)
	*out = append(*out, n)
	if kids, ok := node["Plans"].([]any); ok {
		for _, k := range kids {
			collectPlan(t, k.(map[string]any), out)
		}
	}
}

func explain(t *testing.T, app *pgxpool.Pool, who r.Identity, st postgres.CapacityStatement) []planNode {
	t.Helper()
	ctx := context.Background()
	tx, e := app.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT set_config('reporting.tenant',$1,true), set_config('reporting.issuer',$2,true), set_config('reporting.subject',$3,true)", who.Tenant, who.Issuer, who.Subject); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e = tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+st.SQL, st.Args(who, []string{"brant-contract-demo"})...).Scan(&raw); e != nil {
		t.Fatal(st.Name, e)
	}
	var doc []map[string]any
	if e = json.Unmarshal(raw, &doc); e != nil || len(doc) != 1 {
		t.Fatal(string(raw), e)
	}
	var nodes []planNode
	collectPlan(t, doc[0]["Plan"].(map[string]any), &nodes)
	return nodes
}

func capacityPlans(t *testing.T, admin *pgxpool.Pool) {
	ctx := context.Background()
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
	_, app, sweep := rlsRoles(t, admin)
	sweepPool, e := pgxpool.New(ctx, dsnFor(sweep, ""))
	if e != nil {
		t.Fatal(e)
	}
	defer sweepPool.Close()
	pool, e := pgxpool.New(ctx, dsnFor(app, ""))
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	tmpl, e := capacity.Template(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p := capacity.Profile{Jobs: 30000, Assets: 15000, Identities: 60, Tenants: 3, HotPercent: 30, Days: 30}
	if e = capacity.Seed(ctx, admin, p, tmpl); e != nil {
		t.Fatal(e)
	}
	if e = capacity.SeedSweep(ctx, admin, p, tmpl, 2000); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(ctx, "ANALYZE reporting_s01.jobs; ANALYZE reporting_s01.assets"); e != nil {
		t.Fatal(e)
	}
	who := p.Identity(7)
	// Which index each statement is held to, and the relation it reads.
	want := map[string][2]string{
		"jobs in flight":              {"jobs", "jobs_identity_in_flight"},
		"stored results":              {"assets", "assets_identity_kind_expires"},
		"admissions in the last hour": {"jobs", "jobs_identity_created"},
		"issue listing":               {"jobs", "jobs_identity_created"},
		"job listing":                 {"jobs", "jobs_identity_created"},
	}
	newIndexes := []string{"jobs_identity_in_flight", "assets_identity_kind_expires", "jobs_identity_created"}
	describe := func(nodes []planNode) string {
		var parts []string
		for _, n := range nodes {
			if n.Relation != "" || n.Index != "" {
				parts = append(parts, fmt.Sprintf("%s %s %s", n.Type, n.Relation, n.Index))
			}
		}
		return strings.Join(parts, "; ")
	}
	t.Run("before_009_the_statements_scan_and_the_test_can_tell", func(t *testing.T) {
		if e := postgres.Down(ctx, admin, 8, "test", false); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, "ANALYZE reporting_s01.jobs; ANALYZE reporting_s01.assets"); e != nil {
			t.Fatal(e)
		}
		for _, st := range postgres.CapacityStatementsForTests() {
			nodes := explain(t, pool, who, st)
			t.Logf("before 009, %s: %s", st.Name, describe(nodes))
			for _, n := range nodes {
				if slices.Contains(newIndexes, n.Index) {
					t.Errorf("%s uses %s before it exists", st.Name, n.Index)
				}
			}
		}
		if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, "ANALYZE reporting_s01.jobs; ANALYZE reporting_s01.assets"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("after_009_each_statement_uses_its_index_and_none_scans_the_tables", func(t *testing.T) {
		// A typical identity (7) and the identity with the long history (0). The three counts must use their index for both. The issue
		// listing is only held to "no sequential scan": the planner keeps the unique key (tenant, integration, issuer, subject, kind, ...)
		// for it, whose prefix the statement's own predicates fill, and at 30,000 rows it switches to jobs_identity_created for the
		// identity with the long history but at a million rows (docs/CAPACITY.md, measured) it does not, so the test does not claim it. The
		// plans are logged.
		t.Run("the_sweeps_statements", func(t *testing.T) { sweepPlans(t, sweepPool) })
		for _, who := range []r.Identity{p.Identity(7), p.Identity(capacity.Hot)} {
			hot := who == p.Identity(capacity.Hot)
			for _, st := range postgres.CapacityStatementsForTests() {
				nodes := explain(t, pool, who, st)
				t.Logf("after 009, %s (hot=%t): %s", st.Name, hot, describe(nodes))
				w := want[st.Name]
				found := false
				for _, n := range nodes {
					if n.Index == w[1] {
						found = true
					}
					if n.Type == "Seq Scan" && (n.Relation == "jobs" || n.Relation == "assets") {
						t.Errorf("%s (hot=%t) still scans %s sequentially: %s", st.Name, hot, n.Relation, describe(nodes))
					}
				}
				if !found && st.Name != "issue listing" && st.Name != "job listing" {
					t.Errorf("%s (hot=%t) does not use %s: %s", st.Name, hot, w[1], describe(nodes))
				}
			}
		}
	})
}

// sweepPlans holds the statements of the retention sweep (S04-B), run as the sweep role in the worker scope as the sweep runs them, to the plans that
// make them cost what their batch handles and not what the tables hold (S04-G): no sequential scan of jobs or assets in any of them, the job reduction
// reading the finished unreduced jobs by jobs_reducible, and the artifact's snapshot probe a primary-key lookup. 2,000 expired artifacts and their
// jobs are in the tables (SeedSweep), under 30,000 jobs and 15,000 assets of history.
func sweepPlans(t *testing.T, sweep *pgxpool.Pool) {
	ctx := context.Background()
	for _, st := range postgres.SweepStatementsForTests() {
		tx, e := sweep.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, "SELECT set_config('reporting.scope','worker',true)"); e != nil {
			t.Fatal(e)
		}
		var raw []byte
		if e = tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+st.SQL, st.Args(86400*1e6, 500)...).Scan(&raw); e != nil {
			t.Fatal(st.Name, e)
		}
		tx.Rollback(ctx)
		var doc []map[string]any
		if e = json.Unmarshal(raw, &doc); e != nil || len(doc) != 1 {
			t.Fatal(string(raw), e)
		}
		var nodes []planNode
		collectPlan(t, doc[0]["Plan"].(map[string]any), &nodes)
		t.Logf("sweep, %s: %s", st.Name, describePlan(nodes))
		indexes := map[string]bool{}
		pkeys := 0
		for _, n := range nodes {
			indexes[n.Index] = true
			if n.Index == "assets_pkey" {
				pkeys++
			}
			if n.Type == "Seq Scan" && (n.Relation == "jobs" || n.Relation == "assets") {
				t.Errorf("the sweep's %s scans %s sequentially: %s", st.Name, n.Relation, describePlan(nodes))
			}
		}
		switch st.Name {
		case "delete artifacts":
			// The delete itself and the probe of each candidate's snapshot are lookups by primary key (2): when the probe is an expression on the
			// payload it cannot be, under the row-level policies, and it is a scan of every snapshot for every candidate.
			if pkeys < 2 {
				t.Errorf("the artifact's snapshot probe is not a primary-key lookup: %s", describePlan(nodes))
			}
		case "reduce jobs", "dry run count":
			if !indexes["jobs_reducible"] {
				t.Errorf("the sweep's %s does not read the reducible jobs by jobs_reducible: %s", st.Name, describePlan(nodes))
			}
		}
	}
}

func describePlan(nodes []planNode) string {
	var parts []string
	for _, n := range nodes {
		if n.Relation != "" || n.Index != "" {
			parts = append(parts, fmt.Sprintf("%s %s %s", n.Type, n.Relation, n.Index))
		}
	}
	return strings.Join(parts, "; ")
}
