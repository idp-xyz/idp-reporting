package integration_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
	"os"
	"strings"
	"testing"
	"time"
)

// Row-level security (S04-C, docs/S04C_SCOPE.md C4): the database refuses the rows of another identity to a connection that has not said
// whose rows it works on, proved as the application role and the sweep role through the production path, with two identities of one tenant
// and a third of another. The fixture path (the superuser) bypasses the policies and is not what this proves.

// rlsWorld is the store after three identities each made a snapshot, an export and an issue through the service.
type rlsWorld struct {
	t          *testing.T
	admin      *pgxpool.Pool
	app        *pgxpool.Pool // raw SQL as the application role
	sweep      *pgxpool.Pool // raw SQL as the sweep role
	store      *postgres.Store
	s          *r.Service
	v          durableEnv
	appDSN     string
	sweepDSN   string
	who        []string // "A", "B", "C"
	p          map[string]r.Principal
	jobs       map[string][]string // per identity: the ids of its jobs (snapshot, export, issue)
	assets     map[string][]string // per identity: snapshot, artifact, issue record
	snapJob    map[string]string
	snapshot   map[string]string
	artifact   map[string]string
	issueJobID map[string]string
}

func isRLS(e error) bool {
	var pg *pgconn.PgError
	return errors.As(e, &pg) && pg.Code == "42501" && strings.Contains(pg.Message, "row-level security")
}

func settingsOf(i r.Identity) [][2]string {
	return [][2]string{{"reporting.tenant", i.Tenant}, {"reporting.issuer", i.Issuer}, {"reporting.subject", i.Subject}}
}

// inTx runs fn in a transaction that has set the settings (as set_config(..., true), as the store does) and rolls it back: whatever fn did is
// undone, so that the checks do not disturb each other.
func inTx(t *testing.T, pool *pgxpool.Pool, settings [][2]string, fn func(pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	for _, kv := range settings {
		if _, e = tx.Exec(ctx, "SELECT set_config($1,$2,true)", kv[0], kv[1]); e != nil {
			t.Fatal(e)
		}
	}
	fn(tx)
}

func count(t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, sql string, args ...any) int {
	t.Helper()
	var n int
	if e := q.QueryRow(context.Background(), sql, args...).Scan(&n); e != nil {
		t.Fatal(sql, e)
	}
	return n
}

func affected(t *testing.T, tx pgx.Tx, sql string, args ...any) (int64, error) {
	t.Helper()
	tag, e := tx.Exec(context.Background(), sql, args...)
	return tag.RowsAffected(), e
}

func digestOf(t *testing.T, admin *pgxpool.Pool) string {
	t.Helper()
	var d string
	if e := admin.QueryRow(context.Background(), `SELECT md5(coalesce((SELECT string_agg(j::text, '|' ORDER BY id) FROM reporting_s01.jobs j),'') || coalesce((SELECT string_agg(a::text, '|' ORDER BY id) FROM reporting_s01.assets a),''))`).Scan(&d); e != nil {
		t.Fatal(e)
	}
	return d
}

const (
	rlsInsertJob   = `INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,data,data_sha256) VALUES($1,$2,'i',$3,$4,'snapshot','queued','k','h',now(),'{}','x')`
	rlsInsertAsset = `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256) VALUES($1,$2,'i',$3,$4,'snapshot','{}','x')`
)

func rlsRoles(t *testing.T, admin *pgxpool.Pool) (mig, app, sweep string) {
	t.Helper()
	ctx := context.Background()
	mig, app, sweep = "reporting_rls_mig_t", "reporting_rls_app_t", "reporting_rls_sweep_t"
	for _, role := range []string{app, mig, sweep} {
		if _, e := admin.Exec(ctx, "DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role); e != nil && !strings.Contains(e.Error(), "does not exist") {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN"); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "REASSIGN OWNED BY "+mig+" TO postgres; DROP OWNED BY "+app+"; DROP OWNED BY "+sweep+"; DROP OWNED BY "+mig+"; DROP ROLE IF EXISTS "+app+"; DROP ROLE IF EXISTS "+sweep+"; DROP ROLE IF EXISTS "+mig)
	})
	if e := postgres.Grants(ctx, admin, mig, app); e != nil {
		t.Fatal(e)
	}
	if e := postgres.GrantSweeper(ctx, admin, mig, app, sweep); e != nil {
		t.Fatal(e)
	}
	return
}

func dsnFor(role string, extra string) string {
	return strings.Replace(os.Getenv("REPORTING_S01_TEST_DSN"), "postgres@", role+"@", 1) + extra
}

func rlsService(t *testing.T, v durableEnv, repo r.Repository) *r.Service {
	t.Helper()
	connections := []r.Connection{}
	for _, tenant := range []string{v.p.Tenant, "other_tenant"} {
		for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
			connections = append(connections, r.Connection{Tenant: tenant, Integration: id, SourceTenant: "tenant_a", Adapter: v.source})
		}
	}
	s, e := r.NewService(r.Config{Repository: repo, Connections: connections, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: &stampIssuer{}})
	if e != nil {
		t.Fatal(e)
	}
	return s
}

// newRLSWorld makes the store at the latest version with the application role, the sweep role and the migrator, switches the profile to
// production, opens the production store as the application role, and has A, B (the same tenant) and C (another tenant) each freeze a
// snapshot, export it and issue a document through the service.
func newRLSWorld(t *testing.T, admin *pgxpool.Pool) *rlsWorld {
	t.Helper()
	ctx := context.Background()
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
	v := newDurable(t)
	v.repo.Close()
	mig, app, sweep := rlsRoles(t, admin)
	_ = mig
	w := &rlsWorld{t: t, admin: admin, v: v, appDSN: dsnFor(app, ""), sweepDSN: dsnFor(sweep, ""),
		p: map[string]r.Principal{}, jobs: map[string][]string{}, assets: map[string][]string{}, snapJob: map[string]string{}, snapshot: map[string]string{}, artifact: map[string]string{}, issueJobID: map[string]string{}}
	if _, e := admin.Exec(ctx, "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileProduction); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileFixture)
	})
	var e error
	if w.app, e = pgxpool.New(ctx, w.appDSN); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(w.app.Close)
	if w.sweep, e = pgxpool.New(ctx, w.sweepDSN); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(w.sweep.Close)
	if w.store, e = postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: w.appDSN, InsecureTestOnly: true}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(w.store.Close)
	w.s = rlsService(t, v, w.store)
	base := v.p
	base.Integrations = []string{contractsim.FinanceIntegration}
	w.who = []string{"A", "B", "C"}
	w.p["A"] = base
	pb, pc := base, base
	pb.Subject = "user_b"
	pc.Tenant = "other_tenant"
	w.p["B"], w.p["C"] = pb, pc
	for _, k := range w.who {
		p := w.p[k]
		if e := v.source.EnsureGrant(ctx, sourceContext(p, contractsim.FinanceIntegration), contractsim.DefaultGrant()); e != nil {
			t.Fatal(e)
		}
		env := durableEnv{s: w.s, repo: w.store, source: v.source, p: p}
		snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap-"+k)
		ex := durableExport(t, env, snap.SnapshotID, "export-"+k)
		issue, e := w.s.SubmitIssue(ctx, p, issueInput(snap.SnapshotID, 0), "issue-"+k)
		if e != nil {
			t.Fatal(e)
		}
		if ran, e := w.s.RunOne(ctx); e != nil || !ran {
			t.Fatal(ran, e)
		}
		if rec, e := w.s.Issue(ctx, p, issue.ID); e != nil || rec.State != "succeeded" {
			t.Fatalf("%+v %v", rec, e)
		}
		w.snapJob[k], w.snapshot[k], w.artifact[k], w.issueJobID[k] = snap.ID, snap.SnapshotID, ex.ArtifactID, issue.ID
		w.jobs[k] = []string{snap.ID, ex.ID, issue.ID}
		// Four assets each: the snapshot, the export's artifact, the issued document's artifact and the issue record.
		rows, e := w.admin.Query(ctx, "SELECT id FROM reporting_s01.assets WHERE tenant=$1 AND issuer=$2 AND subject=$3 ORDER BY id", p.Tenant, p.Issuer, p.Subject)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				t.Fatal(e)
			}
			w.assets[k] = append(w.assets[k], id)
		}
		rows.Close()
		if len(w.assets[k]) != 4 {
			t.Fatalf("%s has %d assets, want 4", k, len(w.assets[k]))
		}
	}
	return w
}

func (w *rlsWorld) identity(k string) r.Identity { return w.p[k].Identity }

// others lists the identities that are not k.
func (w *rlsWorld) others(k string) []string {
	var out []string
	for _, o := range w.who {
		if o != k {
			out = append(out, o)
		}
	}
	return out
}

// rowLevelSecurity is the S04-C qualification (TestPostgresMigrations/row_level_security).
func rowLevelSecurity(t *testing.T, admin *pgxpool.Pool) {
	w := newRLSWorld(t, admin)
	if n, m := count(t, admin, "SELECT count(*) FROM reporting_s01.jobs"), count(t, admin, "SELECT count(*) FROM reporting_s01.assets"); n != 9 || m != 12 {
		t.Fatalf("the world has %d jobs and %d assets, want 9 and 12", n, m)
	}
	t.Run("the_mistake_on_purpose_leaks_nothing_under_a_context", func(t *testing.T) { w.mistakeOnPurpose(t) })
	t.Run("no_context_no_rows", func(t *testing.T) { w.noContext(t) })
	t.Run("a_context_sees_and_writes_only_its_identity", func(t *testing.T) { w.identityContext(t) })
	t.Run("no_context_outlives_its_call", func(t *testing.T) { w.noLeakBetweenCalls(t) })
	t.Run("worker_scope", func(t *testing.T) { w.workerScope(t) })
	t.Run("a_store_without_its_policies_is_refused", func(t *testing.T) { w.policiesAreChecked(t) })
	t.Run("the_sweep_deletes_across_identities_only_in_the_worker_scope", func(t *testing.T) { w.sweepAcrossIdentities(t) })
	t.Run("designs_are_seen_and_written_only_by_their_identity", func(t *testing.T) { w.designs(t) })
	t.Run("the_audit_is_read_by_its_identity_or_an_administrator_and_never_rewritten", func(t *testing.T) { w.audit(t) })
	t.Run("schedules_and_runs_are_their_identitys_and_the_schedulers", func(t *testing.T) { w.schedules(t) })
	t.Run("a_restore_leaves_the_roles_the_policies_and_the_isolation_in_place", func(t *testing.T) { w.afterARestore(t) })
	t.Run("migration_007_round_trip_and_upgrade_from_version_6", func(t *testing.T) { migration007(t, admin) })
}

// mistakeOnPurpose is the heart of the proof: the statements of Job and of the asset reads with their identity predicates removed, run under
// A's context, answer A's rows and no others; the same statements run as the superuser answer everyone's. Without the policies this fails.
func (w *rlsWorld) mistakeOnPurpose(t *testing.T) {
	jobSQL, assetSQL := postgres.UnscopedQueriesForTests()
	for _, sql := range []string{jobSQL, assetSQL} {
		if strings.Contains(sql, "tenant=") || strings.Contains(sql, "issuer=") || strings.Contains(sql, "subject=") {
			t.Fatal("the statement still names an identity:", sql)
		}
	}
	found := func(q interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	}, sql, id string) bool {
		rows, e := q.Query(context.Background(), sql, id)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		if rows.Err() != nil {
			t.Fatal(rows.Err())
		}
		return n > 0
	}
	for _, k := range w.who {
		inTx(t, w.app, settingsOf(w.identity(k)), func(tx pgx.Tx) {
			for _, o := range w.who {
				for i, id := range w.jobs[o] {
					if got := found(tx, jobSQL, id); got != (o == k) {
						t.Errorf("context %s, job %d of %s by id with no identity predicate: found %v", k, i, o, got)
					}
				}
				for i, id := range w.assets[o] {
					if got := found(tx, assetSQL, id); got != (o == k) {
						t.Errorf("context %s, asset %d of %s by id with no identity predicate: found %v", k, i, o, got)
					}
				}
			}
			// And with the id predicate gone too: the whole table, as the context sees it.
			if n := count(t, tx, "SELECT count(*) FROM ("+strings.Replace(jobSQL, " WHERE id=$1", "", 1)+") q"); n != 3 {
				t.Errorf("context %s sees %d jobs of the whole table, want its own 3", k, n)
			}
			if n := count(t, tx, "SELECT count(*) FROM ("+strings.Replace(assetSQL, " WHERE id=$1", "", 1)+") q"); n != 4 {
				t.Errorf("context %s sees %d assets of the whole table, want its own 4", k, n)
			}
		})
	}
	// The same statements as the superuser, which bypasses the policies: everyone's rows.
	for _, o := range w.who {
		for _, id := range w.jobs[o] {
			if !found(w.admin, jobSQL, id) {
				t.Error("the superuser does not see a job:", id)
			}
		}
		for _, id := range w.assets[o] {
			if !found(w.admin, assetSQL, id) {
				t.Error("the superuser does not see an asset:", id)
			}
		}
	}
	if n := count(t, w.admin, "SELECT count(*) FROM ("+strings.Replace(jobSQL, " WHERE id=$1", "", 1)+") q"); n != 9 {
		t.Errorf("the superuser sees %d jobs, want 9", n)
	}
}

// noContext: a connection that has said nothing sees nothing and writes nothing, as the application role and as the sweep role, whether it
// set no setting, empty ones (what a pooled connection shows after a transaction that did set them), or only some of the three.
func (w *rlsWorld) noContext(t *testing.T) {
	before := digestOf(t, w.admin)
	a := w.identity("A")
	empty := [][2]string{{"reporting.tenant", ""}, {"reporting.issuer", ""}, {"reporting.subject", ""}, {"reporting.scope", ""}}
	partial := [][2]string{{"reporting.tenant", a.Tenant}, {"reporting.issuer", a.Issuer}}
	wrongScope := [][2]string{{"reporting.scope", "Worker"}}
	for name, settings := range map[string][][2]string{"nothing set": nil, "empty settings": empty, "tenant and issuer only": partial, "another scope": wrongScope} {
		for role, pool := range map[string]*pgxpool.Pool{"application role": w.app, "sweep role": w.sweep} {
			inTx(t, pool, settings, func(tx pgx.Tx) {
				if n := count(t, tx, "SELECT count(*) FROM reporting_s01.jobs"); n != 0 {
					t.Errorf("%s, %s: %d jobs visible", role, name, n)
				}
				if n := count(t, tx, "SELECT count(*) FROM reporting_s01.assets"); n != 0 {
					t.Errorf("%s, %s: %d assets visible", role, name, n)
				}
				if n, e := affected(t, tx, "UPDATE reporting_s01.jobs SET error_code=error_code"); e != nil || n != 0 {
					t.Errorf("%s, %s: an update touched %d jobs (%v)", role, name, n, e)
				}
				if n, e := affected(t, tx, "UPDATE reporting_s01.assets SET kind=kind"); e != nil || n != 0 {
					t.Errorf("%s, %s: an update touched %d assets (%v)", role, name, n, e)
				}
				if role == "sweep role" {
					if n, e := affected(t, tx, "DELETE FROM reporting_s01.assets"); e != nil || n != 0 {
						t.Errorf("%s, %s: a delete removed %d assets (%v)", role, name, n, e)
					}
				}
			})
		}
		// The application role may insert; with no context the database refuses it, SQLSTATE 42501 and the policy's message.
		inTx(t, w.app, settings, func(tx pgx.Tx) {
			if _, e := tx.Exec(context.Background(), rlsInsertJob, "rls-job", a.Tenant, a.Issuer, a.Subject); !isRLS(e) {
				t.Errorf("%s: a job inserted without a context: %v", name, e)
			}
		})
		inTx(t, w.app, settings, func(tx pgx.Tx) {
			if _, e := tx.Exec(context.Background(), rlsInsertAsset, "rls-asset", a.Tenant, a.Issuer, a.Subject); !isRLS(e) {
				t.Errorf("%s: an asset inserted without a context: %v", name, e)
			}
		})
	}
	// Outside a transaction too (the plain statement of a pooled connection that never set anything).
	for _, pool := range []*pgxpool.Pool{w.app, w.sweep} {
		if count(t, pool, "SELECT count(*) FROM reporting_s01.jobs") != 0 || count(t, pool, "SELECT count(*) FROM reporting_s01.assets") != 0 {
			t.Error("rows are visible to a bare connection")
		}
	}
	if _, e := w.app.Exec(context.Background(), rlsInsertJob, "rls-job", a.Tenant, a.Issuer, a.Subject); !isRLS(e) {
		t.Errorf("a bare connection inserted a job: %v", e)
	}
	// The service says it too: a call for an identity with nothing in it (an empty identity) finds nothing.
	if _, e := w.store.Job(context.Background(), r.Identity{}, w.jobs["A"][0]); !errors.Is(e, r.ErrNotFound) {
		t.Error("an empty identity found a job:", e)
	}
	if after := digestOf(t, w.admin); after != before {
		t.Fatal("a refused statement changed the store")
	}
}

// identityContext: with A's context, A's rows through the service and in raw SQL, nothing of B's or of the other tenant's, and the database
// refuses a write that would make a row someone else's.
func (w *rlsWorld) identityContext(t *testing.T) {
	ctx := context.Background()
	before := digestOf(t, w.admin)
	a := w.identity("A")
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		if n := count(t, tx, "SELECT count(*) FROM reporting_s01.jobs"); n != 3 {
			t.Errorf("A sees %d jobs", n)
		}
		if n := count(t, tx, "SELECT count(*) FROM reporting_s01.assets"); n != 4 {
			t.Errorf("A sees %d assets", n)
		}
		if n := count(t, tx, "SELECT count(*) FROM reporting_s01.jobs WHERE tenant<>$1 OR issuer<>$2 OR subject<>$3", a.Tenant, a.Issuer, a.Subject) + count(t, tx, "SELECT count(*) FROM reporting_s01.assets WHERE tenant<>$1 OR issuer<>$2 OR subject<>$3", a.Tenant, a.Issuer, a.Subject); n != 0 {
			t.Errorf("A sees %d rows that are not A's", n)
		}
	})
	for _, o := range w.others("A") {
		oi := w.identity(o)
		// A write that would make a row B's or the other tenant's is refused by the database, wherever the row comes from.
		inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
			if _, e := tx.Exec(ctx, rlsInsertJob, "rls-"+o, oi.Tenant, oi.Issuer, oi.Subject); !isRLS(e) {
				t.Errorf("A inserted a job for %s: %v", o, e)
			}
		})
		inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
			if _, e := tx.Exec(ctx, rlsInsertAsset, "rls-"+o, oi.Tenant, oi.Issuer, oi.Subject); !isRLS(e) {
				t.Errorf("A inserted an asset for %s: %v", o, e)
			}
		})
		inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
			if _, e := tx.Exec(ctx, "UPDATE reporting_s01.jobs SET tenant=$1, issuer=$2, subject=$3 WHERE id=$4", oi.Tenant, oi.Issuer, oi.Subject, w.jobs["A"][0]); !isRLS(e) {
				t.Errorf("A handed its job to %s: %v", o, e)
			}
		})
		inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
			if _, e := tx.Exec(ctx, "UPDATE reporting_s01.assets SET tenant=$1, issuer=$2, subject=$3 WHERE id=$4", oi.Tenant, oi.Issuer, oi.Subject, w.assets["A"][0]); !isRLS(e) {
				t.Errorf("A handed its asset to %s: %v", o, e)
			}
		})
		// B's rows cannot be read or updated: there is no row to touch.
		inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
			for _, id := range w.jobs[o] {
				if n := count(t, tx, "SELECT count(*) FROM reporting_s01.jobs WHERE id=$1", id); n != 0 {
					t.Errorf("A reads %s's job", o)
				}
				if n, e := affected(t, tx, "UPDATE reporting_s01.jobs SET error_code='stolen' WHERE id=$1", id); e != nil || n != 0 {
					t.Errorf("A updated %s's job: %d %v", o, n, e)
				}
			}
			for _, id := range w.assets[o] {
				if n, e := affected(t, tx, "UPDATE reporting_s01.assets SET kind=kind WHERE id=$1", id); e != nil || n != 0 {
					t.Errorf("A updated %s's asset: %d %v", o, n, e)
				}
			}
		})
		// As the sweep role, which deletes: A's context deletes A's assets and none of B's.
		inTx(t, w.sweep, settingsOf(a), func(tx pgx.Tx) {
			for _, id := range w.assets[o] {
				if n, e := affected(t, tx, "DELETE FROM reporting_s01.assets WHERE id=$1", id); e != nil || n != 0 {
					t.Errorf("the sweep role under A's context deleted %s's asset: %d %v", o, n, e)
				}
			}
			if n, e := affected(t, tx, "UPDATE reporting_s01.jobs SET error_code='stolen' WHERE id=$1", w.jobs[o][0]); e != nil || n != 0 {
				t.Errorf("the sweep role under A's context updated %s's job: %d %v", o, n, e)
			}
		})
		// Through the service: not found, as ever.
		p := w.p["A"]
		if _, e := w.s.Job(ctx, p, w.jobs[o][0]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("A's service call for %s's job: %v", o, e)
		}
		if _, e := w.s.Rows(ctx, p, w.snapshot[o], 1, ""); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("A's service call for %s's snapshot: %v", o, e)
		}
		if _, e := w.s.Download(ctx, p, w.artifact[o]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("A's service call for %s's artifact: %v", o, e)
		}
		if _, e := w.s.Issue(ctx, p, w.issueJobID[o]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("A's service call for %s's issue: %v", o, e)
		}
		if _, e := w.s.Cancel(ctx, p, w.jobs[o][0]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("A cancelled %s's job: %v", o, e)
		}
		// And the store itself, whatever the service checks.
		if _, e := w.store.Job(ctx, a, w.jobs[o][0]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("store.Job: %v", e)
		}
		if _, e := w.store.Cancel(ctx, a, w.jobs[o][0]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("store.Cancel: %v", e)
		}
		if _, e := w.store.Snapshot(ctx, a, w.snapshot[o]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("store.Snapshot: %v", e)
		}
		if _, e := w.store.Artifact(ctx, a, w.artifact[o]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("store.Artifact: %v", e)
		}
		if _, e := w.store.Issue(ctx, a, w.issueJobID[o]); !errors.Is(e, r.ErrNotFound) {
			t.Errorf("store.Issue: %v", e)
		}
	}
	// And every pair, not only A's: a service call by one identity for another's job, snapshot, artifact, issue record or cancellation is not
	// found, whoever the two are (B for A's, C for B's, A for C's, ...).
	for _, k := range w.who {
		for _, o := range w.others(k) {
			p := w.p[k]
			if _, e := w.s.Job(ctx, p, w.jobs[o][0]); !errors.Is(e, r.ErrNotFound) {
				t.Errorf("%s's service call for %s's job: %v", k, o, e)
			}
			if _, e := w.s.Rows(ctx, p, w.snapshot[o], 1, ""); !errors.Is(e, r.ErrNotFound) {
				t.Errorf("%s's service call for %s's snapshot: %v", k, o, e)
			}
			if _, e := w.s.Download(ctx, p, w.artifact[o]); !errors.Is(e, r.ErrNotFound) {
				t.Errorf("%s's service call for %s's artifact: %v", k, o, e)
			}
			if _, e := w.s.Issue(ctx, p, w.issueJobID[o]); !errors.Is(e, r.ErrNotFound) {
				t.Errorf("%s's service call for %s's issue: %v", k, o, e)
			}
			if _, e := w.s.Cancel(ctx, p, w.jobs[o][0]); !errors.Is(e, r.ErrNotFound) {
				t.Errorf("%s cancelled %s's job: %v", k, o, e)
			}
		}
	}
	// A's own rows, through the service and the store.
	if j, e := w.s.Job(ctx, w.p["A"], w.jobs["A"][0]); e != nil || j.State != "succeeded" {
		t.Errorf("A's own job: %+v %v", j, e)
	}
	if _, e := w.store.Snapshot(ctx, a, w.snapshot["A"]); e != nil {
		t.Error(e)
	}
	if _, e := w.store.Artifact(ctx, a, w.artifact["A"]); e != nil {
		t.Error(e)
	}
	if _, e := w.store.Issue(ctx, a, w.issueJobID["A"]); e != nil {
		t.Error(e)
	}
	if list, e := w.store.Issues(ctx, a, []string{contractsim.FinanceIntegration}, 10); e != nil || len(list) != 1 || list[0].ID != w.issueJobID["A"] {
		t.Errorf("A's issue listing: %v %v", list, e)
	}
	// An insert or an update that stays A's is allowed (the policies refuse another identity's rows, not writes).
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		if _, e := tx.Exec(ctx, rlsInsertJob, "rls-own", a.Tenant, a.Issuer, a.Subject); e != nil {
			t.Error("A cannot insert its own job:", e)
		}
		if n, e := affected(t, tx, "UPDATE reporting_s01.jobs SET error_code=error_code WHERE id=$1", w.jobs["A"][0]); e != nil || n != 1 {
			t.Error("A cannot update its own job:", n, e)
		}
	})
	if after := digestOf(t, w.admin); after != before {
		t.Fatal("a refused statement changed the store")
	}
}

// noLeakBetweenCalls: with a pool of one connection, a probe on the pool after each kind of call sees no context on the connection the call
// used. The settings are transaction-local, so the probe answers NULL or, after a transaction that did set them, the empty string that
// PostgreSQL leaves on the connection (which is why the policies compare nullif(setting, ”)): never a value.
func (w *rlsWorld) noLeakBetweenCalls(t *testing.T) {
	ctx := context.Background()
	one := func(role string) *pgxpool.Pool {
		pool, e := pgxpool.New(ctx, dsnFor(role, "&pool_max_conns=1"))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(pool.Close)
		if pool.Config().MaxConns != 1 {
			t.Fatal("not a pool of one")
		}
		return pool
	}
	appOne, sweepOne := one("reporting_rls_app_t"), one("reporting_rls_sweep_t")
	probe := func(pool *pgxpool.Pool, after string) {
		t.Helper()
		var tenant, issuer, subject, scope *string
		if e := pool.QueryRow(ctx, "SELECT current_setting('reporting.tenant', true), current_setting('reporting.issuer', true), current_setting('reporting.subject', true), current_setting('reporting.scope', true)").Scan(&tenant, &issuer, &subject, &scope); e != nil {
			t.Fatal(e)
		}
		for name, v := range map[string]*string{"tenant": tenant, "issuer": issuer, "subject": subject, "scope": scope} {
			if v != nil && *v != "" {
				t.Errorf("after %s the connection still carries reporting.%s = %q", after, name, *v)
			}
		}
	}
	// The probe bites: a session-level setting is a leak it sees (and the one thing the store never does).
	if _, e := appOne.Exec(ctx, "SELECT set_config('reporting.tenant', 'leak', false)"); e != nil {
		t.Fatal(e)
	}
	var seen string
	if e := appOne.QueryRow(ctx, "SELECT current_setting('reporting.tenant', true)").Scan(&seen); e != nil || seen != "leak" {
		t.Fatal("the probe cannot see a session-level setting", seen, e)
	}
	if _, e := appOne.Exec(ctx, "RESET reporting.tenant"); e != nil {
		t.Fatal(e)
	}
	probe(appOne, "resetting the probe's own leak")

	st := postgres.OnPoolForTests(appOne)
	s := rlsService(t, w.v, st)
	pA, a := w.p["A"], w.identity("A")
	probe(appOne, "nothing")
	job, e := s.SubmitSnapshot(ctx, pA, contractsim.FinancialQuery(), "leak-1") // Admit
	if e != nil {
		t.Fatal(e)
	}
	probe(appOne, "Admit")
	if _, e = s.Job(ctx, pA, job.ID); e != nil { // Job
		t.Fatal(e)
	}
	probe(appOne, "Job")
	if _, e = s.Cancel(ctx, pA, job.ID); e != nil { // Cancel
		t.Fatal(e)
	}
	probe(appOne, "Cancel")
	if _, e = st.Snapshot(ctx, a, w.snapshot["A"]); e != nil {
		t.Fatal(e)
	}
	probe(appOne, "Snapshot")
	if _, e = st.Artifact(ctx, a, w.artifact["A"]); e != nil {
		t.Fatal(e)
	}
	probe(appOne, "Artifact")
	if _, e = st.Issue(ctx, a, w.issueJobID["A"]); e != nil {
		t.Fatal(e)
	}
	probe(appOne, "Issue")
	if _, e = st.Issues(ctx, a, []string{contractsim.FinanceIntegration}, 5); e != nil {
		t.Fatal(e)
	}
	probe(appOne, "Issues")
	// A call that fails after it set its context leaves none either.
	if _, e = st.Job(ctx, a, "nothing"); !errors.Is(e, r.ErrNotFound) {
		t.Fatal(e)
	}
	probe(appOne, "a Job that found nothing")
	// The worker's calls: claim and publish (RunOne), fail, resolve.
	if _, e = s.SubmitSnapshot(ctx, pA, contractsim.FinancialQuery(), "leak-2"); e != nil {
		t.Fatal(e)
	}
	if ran, e := s.RunOne(ctx); e != nil || !ran { // Claim and Publish
		t.Fatal(ran, e)
	}
	probe(appOne, "Claim and Publish")
	if c, e := st.Claim(ctx, time.Minute); e != nil || c != nil {
		t.Fatal("something left to claim", c, e)
	}
	probe(appOne, "a Claim that found nothing")
	if _, e = s.SubmitSnapshot(ctx, pA, contractsim.FinancialQuery(), "leak-3"); e != nil {
		t.Fatal(e)
	}
	c, e := st.Claim(ctx, time.Minute)
	if e != nil || c == nil {
		t.Fatal(c, e)
	}
	probe(appOne, "Claim")
	if e = st.Fail(ctx, *c, r.ErrUnreconciled.Error()); e != nil {
		t.Fatal(e)
	}
	probe(appOne, "Fail")
	if e = st.Fail(ctx, *c, "again"); !errors.Is(e, r.ErrLease) {
		t.Fatal(e)
	}
	probe(appOne, "a Fail that lost its lease")
	// ResolveFailure applies to an issue job only; one that matches nothing is ErrLease, and has set its scope all the same.
	if e = st.ResolveFailure(ctx, *c, "SOMETHING_ELSE"); !errors.Is(e, r.ErrLease) {
		t.Fatal(e)
	}
	probe(appOne, "ResolveFailure")
	// The sweep, a dry run and a real one, on the sweep role's pool of one.
	for _, dry := range []bool{true, false} {
		if _, e = postgres.Sweep(ctx, sweepOne, r.SweepOptions{DryRun: dry}); e != nil {
			t.Fatal(e)
		}
		probe(sweepOne, fmt.Sprintf("a sweep (dry run %v)", dry))
	}
}

// workerScope: the claim takes the oldest job of any identity, the publication writes assets under the job's owner, the failure and the
// resolution act on any identity's job, and a transaction that did not say it is a worker's claims nothing.
func (w *rlsWorld) workerScope(t *testing.T) {
	ctx := context.Background()
	// Publish wrote each asset under its job's owner (the setup made them with RunOne).
	if n := count(t, w.admin, `SELECT count(*) FROM reporting_s01.assets a JOIN reporting_s01.jobs j ON j.id IN (j.snapshot_id, j.artifact_id) AND (j.snapshot_id=a.id OR j.artifact_id=a.id) WHERE (a.tenant,a.issuer,a.subject,a.integration) IS DISTINCT FROM (j.tenant,j.issuer,j.subject,j.integration)`); n != 0 {
		t.Errorf("%d assets were published under another owner than their job's", n)
	}
	var queued []string
	for _, k := range w.who {
		j, e := w.s.SubmitSnapshot(ctx, w.p[k], contractsim.FinancialQuery(), "queued-"+k)
		if e != nil {
			t.Fatal(e)
		}
		queued = append(queued, j.ID)
	}
	// A transaction without the scope claims nothing; with an identity's context it sees that identity's queued job only; with the scope
	// it sees them all.
	claimSQL := `SELECT id FROM reporting_s01.jobs WHERE state='queued' ORDER BY created_at,id FOR UPDATE SKIP LOCKED`
	rows := func(tx pgx.Tx) []string {
		rs, e := tx.Query(ctx, claimSQL)
		if e != nil {
			t.Fatal(e)
		}
		defer rs.Close()
		var ids []string
		for rs.Next() {
			var id string
			if e = rs.Scan(&id); e != nil {
				t.Fatal(e)
			}
			ids = append(ids, id)
		}
		return ids
	}
	inTx(t, w.app, nil, func(tx pgx.Tx) {
		if ids := rows(tx); len(ids) != 0 {
			t.Errorf("a worker transaction without the scope claims %v", ids)
		}
	})
	inTx(t, w.app, [][2]string{{"reporting.scope", "worker"}}, func(tx pgx.Tx) {
		if ids := rows(tx); len(ids) != 3 || ids[0] != queued[0] || ids[1] != queued[1] || ids[2] != queued[2] {
			t.Errorf("a worker transaction sees %v, want %v", ids, queued)
		}
	})
	inTx(t, w.app, settingsOf(w.identity("B")), func(tx pgx.Tx) {
		if ids := rows(tx); len(ids) != 1 || ids[0] != queued[1] {
			t.Errorf("B's context sees the queue as %v", ids)
		}
	})
	// Claim claims every identity's job in admission order.
	for i, k := range w.who {
		c, e := w.store.Claim(ctx, time.Minute)
		if e != nil || c == nil {
			t.Fatal(i, c, e)
		}
		if c.Job.ID != queued[i] || c.Job.Owner.Identity != w.identity(k) || c.Job.State != "running" {
			t.Errorf("claim %d is job %s of %v, want %s of %s", i, c.Job.ID, c.Job.Owner.Identity, queued[i], k)
		}
		if k == "A" { // ...and fails and resolves an issue job, which acts on any identity's job
			if e = w.store.Fail(ctx, *c, "RLS_TEST"); e != nil {
				t.Error("Fail:", e)
			}
		} else if e = w.store.Fail(ctx, *c, "RLS_TEST"); e != nil {
			t.Error("Fail:", e)
		}
	}
	if c, e := w.store.Claim(ctx, time.Minute); e != nil || c != nil {
		t.Errorf("a fourth claim: %v %v", c, e)
	}
	// ResolveFailure: an issue job of B, claimed, failed as unreconciled, then resolved.
	issue, e := w.s.SubmitIssue(ctx, w.p["B"], issueInput(w.snapshot["B"], 0), "issue-resolve")
	if e != nil {
		t.Fatal(e)
	}
	c, e := w.store.Claim(ctx, time.Minute)
	if e != nil || c == nil || c.Job.ID != issue.ID {
		t.Fatal(c, e)
	}
	if e = w.store.Fail(ctx, *c, r.ErrUnreconciled.Error()); e != nil {
		t.Fatal(e)
	}
	if e = w.store.ResolveFailure(ctx, *c, "RLS_RESOLVED"); e != nil {
		t.Fatal(e)
	}
	var code string
	if e = w.admin.QueryRow(ctx, "SELECT error_code FROM reporting_s01.jobs WHERE id=$1", issue.ID).Scan(&code); e != nil || code != "RLS_RESOLVED" {
		t.Fatal(code, e)
	}
	// The sweep role under the worker scope deletes across identities (the next subtest runs the sweep itself); a worker scope for the
	// application role is no deletion: it has no privilege.
	inTx(t, w.app, [][2]string{{"reporting.scope", "worker"}}, func(tx pgx.Tx) {
		if _, e := tx.Exec(ctx, "DELETE FROM reporting_s01.assets"); e == nil || !strings.Contains(e.Error(), "permission denied") {
			t.Error("the application role deleted:", e)
		}
	})
}

// sweepAcrossIdentities: the sweep deletes the expired assets of every identity and keeps the issue records; the same statements under no
// scope delete nothing.
func (w *rlsWorld) sweepAcrossIdentities(t *testing.T) {
	ctx := context.Background()
	if _, e := w.admin.Exec(ctx, "UPDATE reporting_s01.assets SET expires_at=clock_timestamp()-interval '48 hours' WHERE kind IN ('snapshot','artifact')"); e != nil {
		t.Fatal(e)
	}
	// Under no scope, the sweep's own statements find and delete nothing.
	inTx(t, w.sweep, nil, func(tx pgx.Tx) {
		if n, e := affected(t, tx, "DELETE FROM reporting_s01.assets WHERE kind IN ('snapshot','artifact') AND expires_at < clock_timestamp()"); e != nil || n != 0 {
			t.Errorf("a sweep-role transaction without the scope deleted %d assets (%v)", n, e)
		}
	})
	// What there is to delete: every snapshot and artifact, of the three identities (the earlier checks made a snapshot more for A).
	snapshots := count(t, w.admin, "SELECT count(*) FROM reporting_s01.assets WHERE kind='snapshot'")
	artifacts := count(t, w.admin, "SELECT count(*) FROM reporting_s01.assets WHERE kind='artifact'")
	total := count(t, w.admin, "SELECT count(*) FROM reporting_s01.assets")
	if snapshots < 4 || artifacts != 6 || total != snapshots+artifacts+3 || count(t, w.admin, "SELECT count(DISTINCT (tenant,issuer,subject)) FROM reporting_s01.assets WHERE kind IN ('snapshot','artifact')") != 3 {
		t.Fatalf("the store holds %d snapshots, %d artifacts and %d assets", snapshots, artifacts, total)
	}
	dry, e := postgres.Sweep(ctx, w.sweep, r.SweepOptions{DryRun: true})
	if e != nil || dry.Artifacts != artifacts || dry.Snapshots != snapshots {
		t.Fatalf("the dry run counts %+v (%v), want %d artifacts and %d snapshots, those of all three identities", dry, e, artifacts, snapshots)
	}
	if n := count(t, w.admin, "SELECT count(*) FROM reporting_s01.assets"); n != total {
		t.Fatalf("a dry run deleted: %d assets of %d", n, total)
	}
	// The reconciliation of the object store (S04-E) looks assets up by id as the sweep role, in the worker scope: it sees the rows of every
	// identity and of every kind. Objects named by the snapshots, artifacts and issue records of all three identities are old and kept; the one
	// no row names is the orphan. Without the scope the lookup would see no row and every one of them would be an orphan.
	bucket := objects.NewMemory(nil)
	ids, e := w.admin.Query(ctx, "SELECT id FROM reporting_s01.assets")
	if e != nil {
		t.Fatal(e)
	}
	named := 0
	for ids.Next() {
		var id string
		if e = ids.Scan(&id); e != nil {
			t.Fatal(e)
		}
		bucket.Plant(r.ObjectKey(id), []byte("x"), time.Now().Add(-72*time.Hour))
		named++
	}
	ids.Close()
	orphan := r.ObjectKey(r.NewID())
	bucket.Plant(orphan, []byte("x"), time.Now().Add(-72*time.Hour))
	if named != total {
		t.Fatalf("%d ids of %d assets", named, total)
	}
	recon, e := postgres.SweepWithObjects(ctx, w.sweep, r.SweepOptions{DryRun: true}, bucket, nil)
	if e != nil || recon.Orphans != 1 || recon.Objects != 0 || recon.ObjectsLeft != 0 {
		t.Fatalf("the reconciliation as the sweep role found %+v (%v), want exactly the one orphan among %d objects", recon, e, named+1)
	}
	if got := len(bucket.Keys()); got != named+1 {
		t.Fatalf("a dry run deleted objects: %d left of %d", got, named+1)
	}
	res, e := postgres.Sweep(ctx, w.sweep, r.SweepOptions{Batch: 2})
	if e != nil || res.Artifacts != artifacts || res.Snapshots != snapshots {
		t.Fatalf("the sweep did %+v (%v)", res, e)
	}
	for _, k := range w.who {
		if n := count(t, w.admin, "SELECT count(*) FROM reporting_s01.assets WHERE tenant=$1 AND issuer=$2 AND subject=$3", w.identity(k).Tenant, w.identity(k).Issuer, w.identity(k).Subject); n != 1 {
			t.Errorf("%s has %d assets after the sweep, want its issue record", k, n)
		}
		if _, e := w.store.Issue(ctx, w.identity(k), w.issueJobID[k]); e != nil {
			t.Errorf("%s lost its issue record: %v", k, e)
		}
	}
	// The migrator, the owner, sweeps without any scope: it bypasses the policies like the superuser (dry run only needs the ledger).
	if _, e := postgres.Sweep(ctx, w.admin, r.SweepOptions{DryRun: true}); e != nil {
		t.Fatal(e)
	}
}

// policiesAreChecked: row-level security disabled, a policy dropped, a policy added or changed, are problems of the role check (the ledger
// is intact, so it alone would not see them), OpenProduction refuses with the table named, and restoring them makes it open again.
func (w *rlsWorld) policiesAreChecked(t *testing.T) {
	ctx := context.Background()
	identity := func(table string) string {
		return `CREATE POLICY identity ON reporting_s01.` + table + ` AS PERMISSIVE FOR ALL TO PUBLIC USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), '')) WITH CHECK (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))`
	}
	for name, c := range map[string]struct{ break_, fix, want string }{
		"row-level security disabled on jobs":          {"ALTER TABLE reporting_s01.jobs DISABLE ROW LEVEL SECURITY", "ALTER TABLE reporting_s01.jobs ENABLE ROW LEVEL SECURITY", "row-level security is not enabled on jobs"},
		"row-level security disabled on assets":        {"ALTER TABLE reporting_s01.assets DISABLE ROW LEVEL SECURITY", "ALTER TABLE reporting_s01.assets ENABLE ROW LEVEL SECURITY", "row-level security is not enabled on assets"},
		"identity policy dropped on assets":            {"DROP POLICY identity ON reporting_s01.assets", identity("assets"), "policy identity is missing on assets"},
		"worker policy dropped on jobs":                {"DROP POLICY worker ON reporting_s01.jobs", `CREATE POLICY worker ON reporting_s01.jobs AS PERMISSIVE FOR ALL TO PUBLIC USING (current_setting('reporting.scope', true) = 'worker') WITH CHECK (current_setting('reporting.scope', true) = 'worker')`, "policy worker is missing on jobs"},
		"identity policy widened on jobs":              {"ALTER POLICY identity ON reporting_s01.jobs USING (true)", "ALTER POLICY identity ON reporting_s01.jobs USING (tenant = nullif(current_setting('reporting.tenant', true), '') AND issuer = nullif(current_setting('reporting.issuer', true), '') AND subject = nullif(current_setting('reporting.subject', true), ''))", "policy identity on jobs does not have its qualification"},
		"check of the worker policy widened on assets": {"ALTER POLICY worker ON reporting_s01.assets WITH CHECK (true)", "ALTER POLICY worker ON reporting_s01.assets WITH CHECK (current_setting('reporting.scope', true) = 'worker')", "policy worker on assets does not have its qualification"},
		"a policy of someone's own on assets":          {"CREATE POLICY open ON reporting_s01.assets AS PERMISSIVE FOR SELECT TO PUBLIC USING (true)", "DROP POLICY open ON reporting_s01.assets", "unexpected policy open on assets"},
		"a policy for one role only on jobs":           {"ALTER POLICY identity ON reporting_s01.jobs TO reporting_rls_sweep_t", "ALTER POLICY identity ON reporting_s01.jobs TO PUBLIC", "policy identity on jobs does not have its qualification"},
	} {
		if _, e := w.admin.Exec(ctx, c.break_); e != nil {
			t.Fatal(name, e)
		}
		for role, check := range map[string]func(context.Context, *pgxpool.Pool) ([]string, error){"application": postgres.RoleProblems, "sweep": postgres.SweeperProblems} {
			pool := w.app
			if role == "sweep" {
				pool = w.sweep
			}
			if problems, e := check(ctx, pool); e != nil || !strings.Contains(strings.Join(problems, ";"), c.want) {
				t.Errorf("%s, the %s role's check: %v %v, want %q", name, role, problems, e, c.want)
			}
		}
		if s, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: w.appDSN, InsecureTestOnly: true}); !errors.Is(e, r.ErrDenied) || !strings.Contains(e.Error(), c.want) {
			if s != nil {
				s.Close()
			}
			t.Errorf("%s: OpenProduction: %v", name, e)
		}
		if _, e := w.admin.Exec(ctx, c.fix); e != nil {
			t.Fatal(name, e)
		}
		for _, pool := range []*pgxpool.Pool{w.app, w.sweep} {
			check := postgres.RoleProblems
			if pool == w.sweep {
				check = postgres.SweeperProblems
			}
			if problems, e := check(ctx, pool); e != nil || len(problems) != 0 {
				t.Fatalf("%s not restored: %v %v", name, problems, e)
			}
		}
		s, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: w.appDSN, InsecureTestOnly: true})
		if e != nil {
			t.Fatalf("%s: restored, still refused: %v", name, e)
		}
		s.Close()
	}
	// The superuser and the role check: the superuser is no application role for its attributes, as before.
	if problems, e := postgres.RoleProblems(ctx, w.admin); e != nil || !strings.Contains(strings.Join(problems, ";"), "superuser") {
		t.Fatal(problems, e)
	}
}

// migration007 qualifies the step: a store at version 6 with rows upgrades keeping them, row-level security on, with the committed catalog;
// down gives back exactly the catalog of 006 and the rows.
func migration007(t *testing.T, admin *pgxpool.Pool) {
	ctx := context.Background()
	known, e := postgres.Migrations()
	if e != nil || len(known) != postgres.LatestVersion() || known[6].Name != "row_level_security" {
		t.Fatalf("%+v %v", known, e)
	}
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 6, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
	if want, e := os.ReadFile("testdata/catalog_006.txt"); e != nil || string(want) != catalog(t, admin) {
		t.Fatalf("the store at version 6 does not have the catalog of 006 (%v)", e)
	}
	if _, e := admin.Exec(ctx, rlsInsertJob, "old-job", "t", "x", "y"); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(ctx, rlsInsertAsset, "old-asset", "t", "x", "y"); e != nil {
		t.Fatal(e)
	}
	st := must(postgres.Status(ctx, admin))
	if st.Current() != 6 || len(st.Pending) != postgres.LatestVersion()-6 || st.Pending[0] != 7 {
		t.Fatalf("%+v", st)
	}
	if plan, e := postgres.Plan(st, 7, postgres.Upward); e != nil || len(plan) != 1 || plan[0] != "up   007 row_level_security" {
		t.Fatal(plan, e)
	}
	// The step alone: 008, 009 and 010 are applied on top elsewhere.
	if e := postgres.Up(ctx, admin, 7, "test"); e != nil {
		t.Fatal(e)
	}
	st = must(postgres.Status(ctx, admin))
	if st.Current() != 7 || len(st.Pending) != postgres.LatestVersion()-7 || len(st.Problems) != 0 || st.Applied[6].Checksum != known[6].Checksum {
		t.Fatalf("%+v", st)
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.jobs") != 1 || count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 1 {
		t.Fatal("the upgrade lost rows")
	}
	if os.Getenv("REPORTING_WRITE_CATALOG") == "1" {
		if e := os.WriteFile("testdata/catalog_007.txt", []byte(catalog(t, admin)), 0o644); e != nil {
			t.Fatal(e)
		}
	}
	at7 := catalog(t, admin)
	if want, e := os.ReadFile("testdata/catalog_007.txt"); e != nil || string(want) != at7 {
		t.Fatalf("the catalog at version 7 differs from testdata/catalog_007.txt (%v):\n%s", e, at7)
	}
	for _, want := range []string{"rowsecurity jobs enabled=true forced=false", "rowsecurity assets enabled=true forced=false", "policy jobs identity ALL permissive=PERMISSIVE roles={public}", "policy assets worker ALL permissive=PERMISSIVE roles={public}"} {
		if !strings.Contains(at7, want) {
			t.Errorf("the catalog does not say %q:\n%s", want, at7)
		}
	}
	for _, table := range []string{"profile", "schema_migrations", "sweeps"} {
		if strings.Contains(at7, "rowsecurity "+table) || strings.Contains(at7, "policy "+table) {
			t.Errorf("%s carries row-level security", table)
		}
	}
	// Down to 6: the catalog of 006 exactly, the rows still there, no acceptance needed (nothing is discarded); up again, the same as before.
	if e := postgres.Down(ctx, admin, 6, "test", false); e != nil {
		t.Fatal("007 down needs no acceptance:", e)
	}
	if want, e := os.ReadFile("testdata/catalog_006.txt"); e != nil || string(want) != catalog(t, admin) {
		t.Fatalf("down from 007 does not give the catalog of 006 (%v):\n%s", e, catalog(t, admin))
	}
	if count(t, admin, "SELECT count(*) FROM reporting_s01.jobs") != 1 || count(t, admin, "SELECT count(*) FROM reporting_s01.assets") != 1 {
		t.Fatal("007 down lost rows")
	}
	if e := postgres.Up(ctx, admin, 7, "test"); e != nil {
		t.Fatal(e)
	}
	if catalog(t, admin) != at7 {
		t.Fatal("the catalog differs after down and up")
	}
	// Wherever the row-level security flags are read, the policies are enforced for a role that is not the owner: the old rows are invisible
	// to the application role without a context (the point of the step), though the migrator and the superuser see them.
	dropSchema(t, admin)
	if e := postgres.Up(ctx, admin, 0, "test"); e != nil {
		t.Fatal(e)
	}
	if e := postgres.InitProfile(ctx, admin, postgres.ProfileFixture); e != nil {
		t.Fatal(e)
	}
}

// designs (011, S11-A): as the application role, an identity's context sees and writes only its own designs; without a context nothing at
// all; a row of another identity is refused on insert. The rows are written in transactions that are rolled back.
func (w *rlsWorld) designs(t *testing.T) {
	ctx := context.Background()
	ids := w.who
	if len(ids) < 2 {
		t.Fatal("two identities are needed")
	}
	a, b := w.identity(ids[0]), w.identity(ids[1])
	insert := `INSERT INTO reporting_s01.designs (store, tenant, issuer, subject, data) VALUES ('templates',$1,$2,$3,'\x7b7d')`
	// One committed row per identity, written as the admin (the owner bypasses row-level security).
	for _, i := range []r.Identity{a, b} {
		if _, e := w.admin.Exec(ctx, insert+" ON CONFLICT DO NOTHING", i.Tenant, i.Issuer, i.Subject); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() { _, _ = w.admin.Exec(context.Background(), "DELETE FROM reporting_s01.designs") })
	if n := count(t, w.app, "SELECT count(*) FROM reporting_s01.designs"); n != 0 {
		t.Fatalf("without a context the application role sees %d designs", n)
	}
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		if n := count(t, tx, "SELECT count(*) FROM reporting_s01.designs"); n != 1 {
			t.Fatalf("an identity sees %d designs", n)
		}
		if n, e := affected(t, tx, "UPDATE reporting_s01.designs SET data='\\x5b5d' WHERE subject=$1", b.Subject); e != nil || n != 0 {
			t.Fatalf("another identity's design was updated: %d %v", n, e)
		}
		if n, e := affected(t, tx, "UPDATE reporting_s01.designs SET data='\\x5b5d' WHERE subject=$1", a.Subject); e != nil || n != 1 {
			t.Fatalf("its own design: %d %v", n, e)
		}
	})
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		_, e := tx.Exec(ctx, `INSERT INTO reporting_s01.designs (store, tenant, issuer, subject, data) VALUES ('layouts',$1,$2,$3,'\x')`, b.Tenant, b.Issuer, b.Subject)
		if !isRLS(e) {
			t.Fatalf("a row of another identity was inserted: %v", e)
		}
	})
	// The application role never deletes a design.
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		if _, e := tx.Exec(ctx, "DELETE FROM reporting_s01.designs"); e == nil {
			t.Fatal("the application role deleted a design")
		}
	})
}

// audit_events (012, S12-B), as the application role: an identity sees and inserts only its own events; with reporting.audit = 'tenant' it
// reads, never writes, every event of its tenant and issuer; no event is updated or deleted.
func (w *rlsWorld) audit(t *testing.T) {
	ctx := context.Background()
	a, b := w.identity(w.who[0]), w.identity(w.who[1])
	insert := `INSERT INTO reporting_s01.audit_events (id, tenant, issuer, subject, action, object_kind, outcome) VALUES ($1,$2,$3,$4,'design.change','layout','succeeded')`
	for n, i := range []r.Identity{a, b} {
		if _, e := w.admin.Exec(ctx, insert, fmt.Sprintf("%032x", n+1), i.Tenant, i.Issuer, i.Subject); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() { _, _ = w.admin.Exec(context.Background(), "DELETE FROM reporting_s01.audit_events") })
	sameTenant := a.Tenant == b.Tenant && a.Issuer == b.Issuer
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		if n := count(t, tx, "SELECT count(*) FROM reporting_s01.audit_events"); n != 1 {
			t.Fatalf("an identity sees %d events", n)
		}
		if _, e := tx.Exec(ctx, insert, fmt.Sprintf("%032x", 9), b.Tenant, b.Issuer, b.Subject); !isRLS(e) {
			t.Fatalf("an event of another identity was inserted: %v", e)
		}
	})
	inTx(t, w.app, append(settingsOf(a), [2]string{"reporting.audit", "tenant"}), func(tx pgx.Tx) {
		want := 1
		if sameTenant {
			want = 2
		}
		if n := count(t, tx, "SELECT count(*) FROM reporting_s01.audit_events"); n != want {
			t.Fatalf("the tenant listing sees %d events, want %d", n, want)
		}
		if _, e := tx.Exec(ctx, insert, fmt.Sprintf("%032x", 8), b.Tenant, b.Issuer, b.Subject); !isRLS(e) {
			t.Fatalf("the tenant listing wrote another identity's event: %v", e)
		}
	})
	for _, sql := range []string{"UPDATE reporting_s01.audit_events SET outcome='failed'", "DELETE FROM reporting_s01.audit_events"} {
		inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
			if _, e := tx.Exec(ctx, sql); e == nil {
				t.Fatalf("the application role ran %s", sql)
			}
		})
	}
}

// schedules and schedule_runs (013, S16-A), as the application role: the store's whole contract runs under the policies (the person's calls in
// the identity's context, the scheduler's in the worker scope); without a context nothing is seen, an identity sees and writes only its own
// rows, and nothing is deleted.
func (w *rlsWorld) schedules(t *testing.T) {
	ctx := context.Background()
	clean := func() {
		_, _ = w.admin.Exec(context.Background(), "DELETE FROM reporting_s01.schedule_runs")
		_, _ = w.admin.Exec(context.Background(), "DELETE FROM reporting_s01.schedules")
	}
	clean()
	t.Cleanup(clean)
	day := 0
	runScheduleContract(t, scheduleHarness{store: w.store,
		makeDue: func(t *testing.T) {
			day++
			if _, e := w.admin.Exec(ctx, "UPDATE reporting_s01.schedules SET next_run_at = timestamptz '2026-01-01 08:00:00+00' + $1 * interval '1 day' WHERE state='active'", day); e != nil {
				t.Fatal(e)
			}
		},
		expireLeases: func(t *testing.T) {
			if _, e := w.admin.Exec(ctx, "UPDATE reporting_s01.schedule_runs SET lease_until = clock_timestamp() - interval '1 second' WHERE lease_until IS NOT NULL"); e != nil {
				t.Fatal(e)
			}
		},
	})
	a := r.Identity{Tenant: "t", Issuer: "i", Subject: "a"}
	b := r.Identity{Tenant: "t", Issuer: "i", Subject: "b"}
	all := count(t, w.admin, "SELECT count(*) FROM reporting_s01.schedules")
	runs := count(t, w.admin, "SELECT count(*) FROM reporting_s01.schedule_runs")
	if all < 3 || runs < 2 {
		t.Fatalf("the contract left %d schedules and %d runs", all, runs)
	}
	for _, table := range []string{"schedules", "schedule_runs"} {
		if n := count(t, w.app, "SELECT count(*) FROM reporting_s01."+table); n != 0 {
			t.Fatalf("without a context the application role sees %d rows of %s", n, table)
		}
		mine := count(t, w.admin, "SELECT count(*) FROM reporting_s01."+table+" WHERE subject='a'")
		inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
			if n := count(t, tx, "SELECT count(*) FROM reporting_s01."+table); n != mine {
				t.Fatalf("an identity sees %d rows of %s, want %d", n, table, mine)
			}
			if n, e := affected(t, tx, "UPDATE reporting_s01."+table+" SET state=state WHERE subject=$1", b.Subject); e != nil || n != 0 {
				t.Fatalf("another identity's %s was updated: %d %v", table, n, e)
			}
		})
		inTx(t, w.app, [][2]string{{"reporting.scope", "worker"}}, func(tx pgx.Tx) {
			if n := count(t, tx, "SELECT count(*) FROM reporting_s01."+table); n != map[string]int{"schedules": all, "schedule_runs": runs}[table] {
				t.Fatalf("the worker scope sees %d rows of %s", n, table)
			}
		})
		inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
			if _, e := tx.Exec(ctx, "DELETE FROM reporting_s01."+table); e == nil {
				t.Fatalf("the application role deleted from %s", table)
			}
		})
	}
	inTx(t, w.app, settingsOf(a), func(tx pgx.Tx) {
		_, e := tx.Exec(ctx, `INSERT INTO reporting_s01.schedules (id,tenant,integration,issuer,subject,idem_key,request_hash,state,data,data_sha256,next_run_at)
VALUES ($1,$2,'p',$3,$4,'k',repeat('0',64),'paused','{}',repeat('0',64),NULL)`, r.NewID(), b.Tenant, b.Issuer, b.Subject)
		if !isRLS(e) {
			t.Fatalf("a schedule of another identity was inserted: %v", e)
		}
	})
}
