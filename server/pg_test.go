package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
)

const (
	pgApp      = "reporting_srv_app_t"
	pgMigrator = "reporting_srv_mig_t"
	pgAppPass  = "pg-app-password-Zq7-never-shown"
)

// pgWorld is a production-shaped platform store on the throwaway server: the schema at the latest version, the production profile row, the two
// roles with their grants, and the application role's connection string (with a password, which the trust authentication of the throwaway
// server ignores and every assertion below looks for).
type pgWorld struct {
	admin *pgxpool.Pool
	dsn   string // the application role's, direct
	host  string // host:port of the throwaway server
}

func newPGWorld(t *testing.T) *pgWorld {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, os.Getenv("REPORTING_S01_TEST_DSN"))
	if err != nil {
		t.Fatal("the fixture admin could not open")
	}
	cfg := admin.Config().ConnConfig
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("DROP SCHEMA IF EXISTS reporting_s01 CASCADE")
	for _, role := range []string{pgApp, pgMigrator} {
		_, _ = admin.Exec(ctx, "DROP OWNED BY "+role)
		exec("DROP ROLE IF EXISTS " + role)
		exec("CREATE ROLE " + role + " LOGIN")
	}
	if err := postgres.Up(ctx, admin, 0, "server-test"); err != nil {
		t.Fatal(err)
	}
	if err := postgres.InitProfile(ctx, admin, postgres.ProfileProduction); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Grants(ctx, admin, pgMigrator, pgApp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = admin.Exec(bg, "DROP SCHEMA IF EXISTS reporting_s01 CASCADE")
		_, _ = admin.Exec(bg, "REASSIGN OWNED BY "+pgMigrator+" TO postgres; DROP OWNED BY "+pgApp+"; DROP OWNED BY "+pgMigrator+"; DROP ROLE IF EXISTS "+pgApp+"; DROP ROLE IF EXISTS "+pgMigrator)
		admin.Close()
	})
	host := net.JoinHostPort(cfg.Host, itoa(int(cfg.Port)))
	return &pgWorld{admin: admin, host: host, dsn: "postgresql://" + pgApp + ":" + pgAppPass + "@" + host + "/" + cfg.Database + "?sslmode=disable"}
}

func itoa(n int) string { return strconv.Itoa(n) }

func jsonInto(b []byte, v any) error { return json.Unmarshal(b, v) }

// withDSN is a getenv that gives the store connection string.
func (p *pgWorld) getenv(w *world, dsn string, extra map[string]string) func(string) string {
	return func(name string) string {
		if name == StoreDSNEnv {
			return dsn
		}
		if v, ok := extra[name]; ok {
			return v
		}
		return w.getenv(name)
	}
}

func startPG(t *testing.T, w *world, getenv func(string) string, opts ...Option) *started {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lb := &logBuffer{}
	opts = append([]Option{WithInsecureStoreForTests(), WithPollIntervalForTests(5 * time.Millisecond)}, opts...)
	s, err := Start(ctx, w.cfg, w.registry(), getenv, lb, opts...)
	if err != nil {
		cancel()
		t.Fatalf("start: %v\n%s", err, lb.String())
	}
	st := &started{Server: s, log: lb, cancel: cancel, w: w}
	tr := newTLSTransport(w)
	st.client = &http.Client{Transport: tr}
	st.base = "https://" + s.Addr()
	t.Cleanup(func() { cancel(); <-s.Done(); tr.CloseIdleConnections() })
	return st
}

// tcpProxy forwards connections to the throwaway server and can be stopped and started again on the same address: the store is unreachable
// while it is stopped.
type tcpProxy struct {
	t      *testing.T
	target string
	addr   string
	mu     sync.Mutex
	ln     net.Listener
	conns  []net.Conn
	wg     sync.WaitGroup
}

func newProxy(t *testing.T, target string) *tcpProxy {
	p := &tcpProxy{t: t, target: target}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.addr = ln.Addr().String()
	p.serve(ln)
	t.Cleanup(p.Stop)
	return p
}

func (p *tcpProxy) serve(ln net.Listener) {
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", p.target)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, up)
			p.mu.Unlock()
			p.wg.Add(2)
			go func() { defer p.wg.Done(); _, _ = io.Copy(up, c); up.Close(); c.Close() }()
			go func() { defer p.wg.Done(); _, _ = io.Copy(c, up); up.Close(); c.Close() }()
		}
	}()
}

func (p *tcpProxy) Stop() {
	p.mu.Lock()
	ln, conns := p.ln, p.conns
	p.ln, p.conns = nil, nil
	p.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, c := range conns {
		c.Close()
	}
	p.wg.Wait()
}

func (p *tcpProxy) Start() {
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		p.t.Fatal(err)
	}
	p.serve(ln)
}

// TestServerAgainstPostgres qualifies the server on the throwaway PostgreSQL server (and, in a subtest, on the S3-compatible one): the production
// opener's refusals as the server reports them, a whole session through the durable store, readiness following the database, and two servers on
// one database sharing a queue and an identity's quota. It runs where the other PostgreSQL gates run and skips without them (fails when
// REPORTING_S01_REQUIRE_PG=1).
func TestServerAgainstPostgres(t *testing.T) {
	if os.Getenv("REPORTING_S01_TEST_DSN") == "" || os.Getenv("REPORTING_S01_SOURCE_DSN") == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required platform/source PostgreSQL DSNs missing")
		}
		t.Skip("durable PostgreSQL gate not run: two isolated DB DSNs required")
	}
	t.Run("the_store_the_server_refuses_to_open", func(t *testing.T) { pgRefusals(t) })
	t.Run("a_session_through_the_durable_store", func(t *testing.T) { pgSession(t, false) })
	t.Run("a_session_with_the_object_store", func(t *testing.T) { pgSession(t, true) })
	t.Run("readiness_follows_the_database", func(t *testing.T) { pgReadiness(t) })
	t.Run("two_servers_on_one_database", func(t *testing.T) { pgTwoServers(t) })
	t.Run("two_servers_share_the_designs_of_the_database", func(t *testing.T) { pgSharedDesigns(t) })
}

func pgRefusals(t *testing.T) {
	p := newPGWorld(t)
	ctx := context.Background()
	w := newWorld(t)
	try := func(name, step, want string, dsn string, opts ...Option) {
		t.Helper()
		se, logged := refusedStart(t, w, w.registry(), p.getenv(w, dsn, nil), append([]Option{WithInsecureStoreForTests()}, opts...)...)
		if se.Step != step || !strings.Contains(se.Reason, want) {
			t.Errorf("%s: step %q reason %q, want %q containing %q", name, se.Step, se.Reason, step, want)
		}
		for _, secret := range []string{pgAppPass, "postgresql://", p.host} {
			if strings.Contains(se.Error(), secret) || strings.Contains(logged, secret) {
				t.Errorf("%s: carries %q: %q", name, secret, se.Error())
			}
		}
	}
	// Without the test-only option the production rules apply: TLS verified, and the database no fixture.
	se, _ := refusedStart(t, w, w.registry(), p.getenv(w, p.dsn, nil))
	if se.Step != StepStore || !strings.Contains(se.Reason, "fixture database") || strings.Contains(se.Error(), pgAppPass) {
		t.Errorf("a fixture database: %v", se)
	}
	se, _ = refusedStart(t, w, w.registry(), p.getenv(w, strings.Replace(p.dsn, "/reporting_test_s01", "/reporting_prod", 1), nil))
	if se.Step != StepStore || !strings.Contains(se.Reason, "verify-full") || strings.Contains(se.Error(), pgAppPass) {
		t.Errorf("without TLS: %v", se)
	}
	// The role: the superuser is not the application role.
	super := strings.Replace(os.Getenv("REPORTING_S01_TEST_DSN"), "postgres@", "postgres:"+pgAppPass+"@", 1)
	try("the superuser", StepStore, "not the application role", super)
	// The ledger behind the binary.
	if err := postgres.Down(ctx, p.admin, 8, "test", false); err != nil {
		t.Fatal(err)
	}
	try("a schema behind", StepStore, fmt.Sprintf("the binary needs %d", postgres.LatestVersion()), p.dsn)
	if err := postgres.Up(ctx, p.admin, 0, "test"); err != nil {
		t.Fatal(err)
	}
	// As the upgrade procedure says (docs/DEPLOYMENT.md): `roles` after an `up` whose steps created a table (011 designs, S11-A).
	if err := postgres.Grants(ctx, p.admin, pgMigrator, pgApp); err != nil {
		t.Fatal(err)
	}
	// The profile row.
	if _, err := p.admin.Exec(ctx, "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileFixture); err != nil {
		t.Fatal(err)
	}
	try("the fixture profile", StepStore, "expected \"production\"", p.dsn)
	if _, err := p.admin.Exec(ctx, "UPDATE reporting_s01.profile SET profile=$1", postgres.ProfileProduction); err != nil {
		t.Fatal(err)
	}
	// A pool smaller than the workers need, from the configuration.
	w.cfg.Workers, w.cfg.Store.PoolMaxConns = 4, 5
	se, _ = refusedStart(t, w, w.registry(), p.getenv(w, p.dsn, nil), WithInsecureStoreForTests())
	if se.Step != StepConfig {
		t.Errorf("pool: %v", se)
	}
	w.cfg.Workers, w.cfg.Store.PoolMaxConns = 1, 3
	s := startPG(t, w, p.getenv(w, p.dsn, nil))
	s.cancel()
	<-s.Done()
}

// pgSession is the S6 session through the durable store, and the object store when asked.
func pgSession(t *testing.T, withObjects bool) {
	var objEnv map[string]string
	w := newWorld(t)
	if withObjects {
		endpoint, access, secret := os.Getenv("REPORTING_OBJECT_STORE_TEST_ENDPOINT"), os.Getenv("REPORTING_OBJECT_STORE_TEST_ACCESS_KEY"), os.Getenv("REPORTING_OBJECT_STORE_TEST_SECRET_KEY")
		if endpoint == "" || access == "" || secret == "" {
			if os.Getenv("REPORTING_OBJECT_STORE_REQUIRE") == "1" {
				t.Fatal("required object store endpoint and keys (REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY, _SECRET_KEY) missing")
			}
			t.Skip("S3 gate not run: set REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY and _SECRET_KEY")
		}
		objEnv = map[string]string{objects.AccessKeyEnv: access, objects.SecretKeyEnv: secret}
		w.cfg.ObjectStore = &ObjectStoreConfig{Endpoint: endpoint, Bucket: "reporting-test-s05a", LoopbackHTTP: strings.HasPrefix(endpoint, "http://")}
		client, err := objects.NewS3(objects.S3{Endpoint: endpoint, Bucket: "reporting-test-s05a", Region: "us-east-1", AccessKey: access, SecretKey: secret, LoopbackHTTP: strings.HasPrefix(endpoint, "http://")})
		if err != nil {
			t.Fatal(err)
		}
		if err := objects.CreateBucketForTests(context.Background(), client); err != nil {
			t.Fatal(err)
		}
	}
	p := newPGWorld(t)
	w.cfg.Quotas.AdmissionsPerHour = 2
	snap := goroutines()
	getenv := p.getenv(w, p.dsn, objEnv)
	s := startPG(t, w, getenv)
	good := mint(t, w.keys[0], claims{})
	if status, _, _ := s.do(t, "GET", api+"/session", "", nil); status != 401 {
		t.Fatal(status)
	}
	if status, _, _ := s.do(t, "GET", api+"/session", mint(t, w.keys[0], claims{Aud: "x"}), nil); status != 401 {
		t.Fatal(status)
	}
	if status, _, _ := s.do(t, "GET", api+"/session", mint(t, w.keys[0], claims{Scope: "x"}), nil); status != 403 {
		t.Fatal(status)
	}
	if status, _, body := s.do(t, "GET", "/readyz", "", nil); status != 200 {
		t.Fatalf("readyz: %d %s", status, body)
	}
	if status, _, body := s.do(t, "POST", api+"/queries", good, contractsim.FinancialQuery()); status != 200 {
		t.Fatalf("query: %d %s", status, body)
	}
	id := submitSnapshot(t, s, good, "snap-1")
	job := s.waitJob(t, good, id)
	if job.State != "succeeded" {
		t.Fatalf("%+v", job)
	}
	status, _, body := s.do(t, "POST", api+"/exports", good, map[string]string{"snapshot_id": job.SnapshotID}, "Idempotency-Key", "export-1")
	var export r.JobView
	if status != 202 || jsonInto(body, &export) != nil {
		t.Fatalf("export: %d %s", status, body)
	}
	export = s.waitJob(t, good, export.ID)
	status, _, body = s.do(t, "GET", api+"/artifacts/"+export.ArtifactID+"/content", good, nil)
	if export.State != "succeeded" || status != 200 || len(body) < 2 || string(body[:2]) != "PK" {
		t.Fatalf("download: %+v %d", export, status)
	}
	var inObjects int
	if err := p.admin.QueryRow(context.Background(), "SELECT count(*) FROM reporting_s01.assets WHERE kind='artifact' AND coalesce(data->>'Object','') <> ''").Scan(&inObjects); err != nil {
		t.Fatal(err)
	}
	if withObjects != (inObjects == 1) {
		t.Errorf("artifacts that live in the object store: %d (object store configured: %v)", inObjects, withObjects)
	}
	if status, _, body := s.do(t, "POST", api+"/snapshot-jobs", good, contractsim.FinancialQuery(), "Idempotency-Key", "snap-2"); status != 429 {
		t.Fatalf("quota: %d %s", status, body)
	}
	s.cancel()
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	s.client.CloseIdleConnections()
	for _, secret := range []string{pgAppPass, "postgresql://", good, testAccess, testSecret} {
		if strings.Contains(s.log.String(), secret) {
			t.Errorf("the log carries %q", secret)
		}
	}
	if ledger := s.log.events(t, "start")[0]["ledger"]; ledger != float64(postgres.LatestVersion()) {
		t.Errorf("ledger %v", ledger)
	}
	noNewGoroutines(t, snap)
}

func pgReadiness(t *testing.T) {
	p := newPGWorld(t)
	ctx := context.Background()
	w := newWorld(t)
	proxy := newProxy(t, p.host)
	viaProxy := strings.Replace(p.dsn, p.host, proxy.addr, 1)
	s := startPG(t, w, p.getenv(w, viaProxy, nil))
	ready := func(want int, body string) {
		t.Helper()
		status, _, got := s.do(t, "GET", "/readyz", "", nil)
		if status != want || strings.TrimSpace(string(got)) != body {
			t.Fatalf("readyz: %d %s, want %d %s", status, got, want, body)
		}
		if hs, _, _ := s.do(t, "GET", "/healthz", "", nil); hs != 200 {
			t.Fatalf("healthz: %d", hs)
		}
	}
	ready(200, `{"status":"ready"}`)
	// The database is unreachable, then back.
	proxy.Stop()
	ready(503, `{"status":"not_ready","reason":"store"}`)
	proxy.Start()
	ready(200, `{"status":"ready"}`)
	// The schema is behind the binary (a rollback by the operator), then migrated again.
	if err := postgres.Down(ctx, p.admin, 8, "test", false); err != nil {
		t.Fatal(err)
	}
	ready(503, `{"status":"not_ready","reason":"schema"}`)
	if err := postgres.Up(ctx, p.admin, 0, "test"); err != nil {
		t.Fatal(err)
	}
	// As the upgrade procedure says (docs/DEPLOYMENT.md): `roles` after an `up` whose steps created a table (011 designs, S11-A).
	if err := postgres.Grants(ctx, p.admin, pgMigrator, pgApp); err != nil {
		t.Fatal(err)
	}
	ready(200, `{"status":"ready"}`)
	// The role has gained a privilege it must not have, then loses it.
	if _, err := p.admin.Exec(ctx, "GRANT DELETE ON reporting_s01.jobs TO "+pgApp); err != nil {
		t.Fatal(err)
	}
	ready(503, `{"status":"not_ready","reason":"roles"}`)
	if _, err := p.admin.Exec(ctx, "REVOKE DELETE ON reporting_s01.jobs FROM "+pgApp); err != nil {
		t.Fatal(err)
	}
	ready(200, `{"status":"ready"}`)
	// The row-level security of the store is switched off under the running server.
	if _, err := p.admin.Exec(ctx, "ALTER TABLE reporting_s01.jobs DISABLE ROW LEVEL SECURITY"); err != nil {
		t.Fatal(err)
	}
	ready(503, `{"status":"not_ready","reason":"roles"}`)
	if _, err := p.admin.Exec(ctx, "ALTER TABLE reporting_s01.jobs ENABLE ROW LEVEL SECURITY"); err != nil {
		t.Fatal(err)
	}
	ready(200, `{"status":"ready"}`)
	s.cancel()
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
}

// Two servers on one database: a job queue they share (one holds a job, the other works the rest), and an identity's quota that counts the
// admissions of both.
func pgTwoServers(t *testing.T) {
	p := newPGWorld(t)
	snap := goroutines()
	wa, wb := newWorld(t), newWorld(t)
	wb.sim = wa.sim // one product behind both servers: a job admitted by one is read by the other
	wa.cfg.Quotas.AdmissionsPerHour, wb.cfg.Quotas.AdmissionsPerHour = 6, 6
	// Server A's adapter stops inside every read; B's does not.
	ga := newGate(wa.sim)
	ga.armed.Store(true)
	ctxA, cancelA := context.WithCancel(context.Background())
	lbA := &logBuffer{}
	a, err := Start(ctxA, wa.cfg, registryOf(ga), p.getenv(wa, p.dsn, nil), lbA, WithInsecureStoreForTests(), WithPollIntervalForTests(2*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	sa := &started{Server: a, log: lbA, cancel: cancelA, w: wa, client: &http.Client{Transport: newTLSTransport(wa)}, base: "https://" + a.Addr()}
	sb := startPG(t, wb, p.getenv(wb, p.dsn, nil))
	token := mint(t, wa.keys[0], claims{})
	var ids []string
	for i := 0; i < 4; i++ {
		ids = append(ids, submitSnapshot(t, sb, token, "job-"+itoa(i)))
	}
	// Whichever server claimed the first job, B alone finishes at least three of the four while A is held.
	deadline := time.Now().Add(30 * time.Second)
	for {
		done := 0
		for _, id := range ids {
			status, _, body := sb.do(t, "GET", api+"/jobs/"+id, token, nil)
			var j r.JobView
			if status != 200 || jsonInto(body, &j) != nil {
				t.Fatalf("%d %s", status, body)
			}
			if j.State == "succeeded" {
				done++
			}
		}
		if done >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 4 jobs finished\nB: %s\nA: %s", done, sb.log.String(), sa.log.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Each server sees the other's jobs: the queue is the database's.
	if status, _, _ := sa.do(t, "GET", api+"/jobs/"+ids[0], token, nil); status != 200 {
		t.Fatalf("A cannot see B's job: %d", status)
	}
	// The identity's hour: six admissions across both servers, the seventh from either is refused.
	submitSnapshotNoWait := func(s *started, key string) int {
		status, _, _ := s.do(t, "POST", api+"/snapshot-jobs", token, contractsim.FinancialQuery(), "Idempotency-Key", key)
		return status
	}
	if st := submitSnapshotNoWait(sa, "from-a-1"); st != 202 {
		t.Fatalf("%d", st)
	}
	if st := submitSnapshotNoWait(sb, "from-b-1"); st != 202 {
		t.Fatalf("%d", st)
	}
	if st := submitSnapshotNoWait(sa, "from-a-2"); st != 429 {
		t.Fatalf("A admitted a seventh job: %d", st)
	}
	if st := submitSnapshotNoWait(sb, "from-b-2"); st != 429 {
		t.Fatalf("B admitted a seventh job: %d", st)
	}
	// Let A go; every job finishes.
	ga.armed.Store(false)
	close(ga.release)
	sa.cancel()
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	sa.client.CloseIdleConnections()
	sb.cancel()
	if err := sb.Wait(); err != nil {
		t.Fatal(err)
	}
	sb.client.CloseIdleConnections()
	noNewGoroutines(t, snap)
}

// pgSharedDesigns: design_store database through the production store as the application role (S11-B): two servers on one database serve the
// same designs, under row-level security.
func pgSharedDesigns(t *testing.T) {
	p := newPGWorld(t)
	w := newWorld(t)
	w.cfg.DesignStore = "database"
	one := startPG(t, w, p.getenv(w, p.dsn, nil))
	two := startPG(t, w, p.getenv(w, p.dsn, nil))
	sharedDesigns(t, w, one, two)
}
