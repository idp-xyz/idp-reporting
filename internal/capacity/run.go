package capacity

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config is one run of the tool.
type Config struct {
	DSN                                        string // the fixture administrator's DSN (REPORTING_S01_TEST_DSN)
	Sizes                                      []int  // job rows of each size; assets are AssetsPercent of them
	Identities                                 int
	Tenants                                    int
	HotPercent                                 int
	AssetsPercent                              int
	Days                                       int
	Ops                                        int // operations per measured case
	WriterOps                                  int // operations per writer in the parallel cases
	SweepRows                                  int
	VerifyRows                                 int
	Phases                                     []string // "before" (migration 009 reverted) and/or "after"
	ObjectEndpoint, ObjectAccess, ObjectSecret string   // optional: the throwaway S3-compatible server, for verify-objects
	Out                                        io.Writer
}

// Stats summarises the latencies of one case.
type Stats struct {
	N                  int
	P50, P95, P99, Max time.Duration
	Elapsed            time.Duration
}

// Throughput is operations per second over the case's wall time.
func (s Stats) Throughput() float64 { return float64(s.N) / s.Elapsed.Seconds() }

func summarise(d []time.Duration, elapsed time.Duration) Stats {
	slices.Sort(d)
	at := func(q float64) time.Duration { return d[min(len(d)-1, int(q*float64(len(d))))] }
	return Stats{N: len(d), P50: at(0.50), P95: at(0.95), P99: at(0.99), Max: d[len(d)-1], Elapsed: elapsed}
}

func ms(d time.Duration) string { return fmt.Sprintf("%.2f", float64(d.Microseconds())/1000) }

type env struct {
	cfg      Config
	p        Profile
	tmpl     r.Job
	admin    *pgxpool.Pool
	app      *pgxpool.Pool
	sweep    *pgxpool.Pool
	store    *postgres.Store
	out      io.Writer
	size     int
	phase    string
	admitted []string
	seq      int
	mu       sync.Mutex
}

var wide = r.Quotas{JobsInFlight: r.MaxJobsInFlight, StoredResults: r.MaxStoredResults, AdmissionsPerHour: r.MaxAdmissionsPerHour, QueriesInFlight: 4}

func (e *env) row(name string, s Stats) {
	fmt.Fprintf(e.out, "%-8s %-8d %-46s n=%-6d p50=%8s ms  p95=%8s ms  p99=%8s ms  max=%8s ms  %9.1f ops/s\n", e.phase, e.size, name, s.N, ms(s.P50), ms(s.P95), ms(s.P99), ms(s.Max), s.Throughput())
}

func (e *env) timed(ctx context.Context, name string, n int, op func(i int) error) error {
	d := make([]time.Duration, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		t := time.Now()
		if err := op(i); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		d = append(d, time.Since(t))
	}
	e.row(name, summarise(d, time.Since(start)))
	return e.settle(ctx)
}

// settle finishes the jobs the case admitted, so that they leave the in-flight count and the next case is not stopped by the quota (the quotas
// are at their largest bound, 1000 in flight, and a case admits more than that for one identity).
func (e *env) settle(ctx context.Context) error {
	e.mu.Lock()
	ids := slices.Clone(e.admitted)
	e.mu.Unlock()
	_, err := e.admin.Exec(ctx, "UPDATE reporting_s01.jobs SET state='succeeded' WHERE id = ANY($1) AND state='queued'", ids)
	return err
}

func (e *env) job(o r.Owner) r.Job {
	e.mu.Lock()
	e.seq++
	n := e.seq
	e.mu.Unlock()
	j := e.tmpl
	j.ID = r.NewID()
	j.Key = fmt.Sprintf("m%d-%s", n, j.ID[:8])
	j.Owner = o
	j.Prepared.Context.Owner = o
	e.mu.Lock()
	e.admitted = append(e.admitted, j.ID)
	e.mu.Unlock()
	return j
}

func (e *env) fresh(i int) r.Owner {
	return r.Owner{Identity: r.Identity{Tenant: TenantPrefix + "f", Issuer: Issuer, Subject: fmt.Sprintf("fresh-%d-%d", e.size, i)}, Integration: e.tmpl.Owner.Integration}
}

// withContext runs fn in a transaction that has set the identity's context, as the store does.
func withContext(ctx context.Context, pool *pgxpool.Pool, i r.Identity, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT set_config('reporting.tenant',$1,true), set_config('reporting.issuer',$2,true), set_config('reporting.subject',$3,true)", i.Tenant, i.Issuer, i.Subject); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *env) measure(ctx context.Context) error {
	n := e.cfg.Ops
	hot, typical := e.p.Owner(Hot), e.p.Owner(1)
	if err := e.timed(ctx, "admit, identity with a long history", n, func(int) error {
		_, err := e.store.Admit(ctx, e.job(hot), wide)
		return err
	}); err != nil {
		return err
	}
	if err := e.timed(ctx, "admit, typical identity", n, func(int) error {
		_, err := e.store.Admit(ctx, e.job(typical), wide)
		return err
	}); err != nil {
		return err
	}
	if err := e.timed(ctx, "admit, fresh identity (no history)", n, func(i int) error {
		_, err := e.store.Admit(ctx, e.job(e.fresh(i)), wide)
		return err
	}); err != nil {
		return err
	}
	listing := postgres.CapacityStatementsForTests()[3]
	for _, c := range []struct {
		name string
		k    int
	}{{"issue listing (25), long history", Hot}, {"issue listing (25), typical identity", 1}} {
		who := e.p.Identity(c.k)
		if err := e.timed(ctx, c.name, n, func(int) error {
			return withContext(ctx, e.app, who, func(tx pgx.Tx) error {
				rows, err := tx.Query(ctx, listing.SQL, listing.Args(who, []string{typical.Integration})...)
				if err != nil {
					return err
				}
				for rows.Next() {
				}
				rows.Close()
				return rows.Err()
			})
		}); err != nil {
			return err
		}
	}
	// Reads by id of rows picked at random from the bulk history.
	type ref struct {
		id  string
		who r.Identity
	}
	pick := func(sql string, args ...any) ([]ref, error) {
		rows, err := e.admin.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []ref
		for rows.Next() {
			var x ref
			if err := rows.Scan(&x.id, &x.who.Tenant, &x.who.Issuer, &x.who.Subject); err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, rows.Err()
	}
	jobs, err := pick(`SELECT id,tenant,issuer,subject FROM reporting_s01.jobs WHERE id IN (SELECT md5('cap-job-'||(1+floor(random()*$1))::int) FROM generate_series(1,$2)) AND kind='snapshot' LIMIT $3`, e.size, n*2, n)
	if err != nil || len(jobs) < n/2 {
		return fmt.Errorf("sampling jobs: %v (%d)", err, len(jobs))
	}
	if err := e.timed(ctx, "job by id", len(jobs), func(i int) error { _, err := e.store.Job(ctx, jobs[i].who, jobs[i].id); return err }); err != nil {
		return err
	}
	assets, err := pick(`SELECT id,tenant,issuer,subject FROM reporting_s01.assets WHERE id IN (SELECT md5('cap-asset-'||(1+floor(random()*$1))::int) FROM generate_series(1,$2)) AND kind='artifact' LIMIT $3`, e.assets(), n*4, n)
	if err != nil || len(assets) < n/2 {
		return fmt.Errorf("sampling assets: %v (%d)", err, len(assets))
	}
	if err := e.timed(ctx, "artifact (asset) by id", len(assets), func(i int) error { _, err := e.store.Artifact(ctx, assets[i].who, assets[i].id); return err }); err != nil {
		return err
	}
	var queued int
	if err := e.admin.QueryRow(ctx, "SELECT count(*) FROM reporting_s01.jobs WHERE state='queued' AND tenant LIKE '"+TenantPrefix+"%' AND generation = 0").Scan(&queued); err != nil {
		return err
	}
	if err := e.timed(ctx, fmt.Sprintf("claim (deep non-queued history; %d queued)", queued), min(n, 100, queued/2), func(int) error {
		c, err := e.store.Claim(ctx, time.Minute)
		if err == nil && c == nil {
			err = fmt.Errorf("nothing to claim")
		}
		return err
	}); err != nil {
		return err
	}
	// Parallel writers: one hot identity (they queue on its advisory lock) against one identity each.
	for _, w := range []int{1, 8, 32} {
		for _, mode := range []string{"one hot identity", "one identity each"} {
			if w == 1 && mode == "one identity each" {
				continue
			}
			if err := e.parallel(ctx, w, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *env) assets() int { return e.p.Assets }

func (e *env) parallel(ctx context.Context, writers int, mode string) error {
	lat := make([][]time.Duration, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := e.p.Owner(Hot)
			if mode == "one identity each" {
				o = e.p.Owner(1 + w)
			}
			for i := 0; i < e.cfg.WriterOps; i++ {
				t := time.Now()
				if _, err := e.store.Admit(ctx, e.job(o), wide); err != nil {
					errs[w] = err
					return
				}
				lat[w] = append(lat[w], time.Since(t))
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	var all []time.Duration
	for w := range lat {
		if errs[w] != nil {
			return errs[w]
		}
		all = append(all, lat[w]...)
	}
	e.row(fmt.Sprintf("admit, %d writer(s), %s", writers, mode), summarise(all, elapsed))
	return e.settle(ctx)
}

func (e *env) sweepStep(ctx context.Context) error {
	if err := SeedSweep(ctx, e.admin, e.p, e.tmpl, e.cfg.SweepRows); err != nil {
		return err
	}
	if _, err := e.admin.Exec(ctx, "ANALYZE reporting_s01.jobs; ANALYZE reporting_s01.assets"); err != nil {
		return err
	}
	var ledger int64
	_ = e.admin.QueryRow(ctx, "SELECT coalesce(max(id),0) FROM reporting_s01.sweeps").Scan(&ledger)
	start := time.Now()
	res, err := postgres.Sweep(ctx, e.sweep, r.SweepOptions{Batch: 500})
	el := time.Since(start)
	_, _ = e.admin.Exec(ctx, "DELETE FROM reporting_s01.sweeps WHERE id > $1", ledger)
	if err != nil {
		return fmt.Errorf("sweep: %w", err)
	}
	fmt.Fprintf(e.out, "%-8s %-8d %-46s artifacts=%d jobs reduced=%d batches=%d in %.2f s: %.0f rows/s (artifacts deleted + jobs reduced)\n", e.phase, e.size, fmt.Sprintf("one sweep over %d expired rows, batch 500", e.cfg.SweepRows), res.Artifacts, res.Jobs, res.Batches, el.Seconds(), float64(res.Artifacts+res.Jobs)/el.Seconds())
	return nil
}

func (e *env) verifyStep(ctx context.Context) error {
	if e.cfg.ObjectEndpoint == "" || e.cfg.VerifyRows == 0 {
		fmt.Fprintf(e.out, "%-8s %-8d verify-objects: skipped (no object store configured)\n", e.phase, e.size)
		return nil
	}
	s3, err := objects.NewS3(objects.S3{Endpoint: e.cfg.ObjectEndpoint, Bucket: "reporting-test-s04e", Region: "us-east-1", AccessKey: e.cfg.ObjectAccess, SecretKey: e.cfg.ObjectSecret, LoopbackHTTP: true})
	if err != nil {
		return err
	}
	// The bucket must exist already (the platform never creates one, ADR 0007 decision 6): the object store tests of the qualification make it.
	var keys []string
	defer func() {
		for _, k := range keys {
			_ = s3.Delete(context.Background(), k)
		}
		_, _ = e.admin.Exec(context.Background(), "DELETE FROM reporting_s01.assets WHERE tenant LIKE '"+TenantPrefix+"%' AND data ? 'Object'")
	}()
	for i := 0; i < e.cfg.VerifyRows; i++ {
		id := r.NewID()
		content := []byte(strings.Repeat(fmt.Sprintf("%08d", i), 128))
		a := r.Artifact{ID: id, Owner: e.p.Owner(1 + i%(e.p.Identities-1)), SnapshotID: r.NewID(), ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Microsecond), Object: r.ObjectKey(id), Size: len(content)}
		a.SHA256 = r.BytesDigestForTests(content)
		if err = s3.Put(ctx, a.Object, content, a.SHA256); err != nil {
			return err
		}
		keys = append(keys, a.Object)
		b, _ := jsonMarshal(a)
		if _, err = e.admin.Exec(ctx, `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at) VALUES($1,$2,$3,$4,$5,'artifact',$6,encode(sha256(convert_to($6::jsonb::text,'UTF8')),'hex'),$7)`, id, a.Owner.Tenant, a.Owner.Integration, a.Owner.Issuer, a.Owner.Subject, b, a.ExpiresAt); err != nil {
			return err
		}
	}
	for _, deep := range []bool{false, true} {
		start := time.Now()
		res, err := postgres.VerifyObjects(ctx, e.app, s3, deep)
		el := time.Since(start)
		if err != nil || res.Failed() {
			return fmt.Errorf("verify: %+v %v", res, err)
		}
		fmt.Fprintf(e.out, "%-8s %-8d %-46s rows=%d objects=%d orphans=%d in %.2f s: %.0f rows/s\n", e.phase, e.size, fmt.Sprintf("verify-objects deep=%t over %d rows", deep, e.cfg.VerifyRows), res.Rows, res.Objects, res.Orphans, el.Seconds(), float64(res.Rows)/el.Seconds())
	}
	return nil
}

func (e *env) sizes(ctx context.Context) error {
	rows, err := e.admin.Query(ctx, `SELECT c.relname, c.relkind, pg_relation_size(c.oid), (SELECT reltuples::bigint FROM pg_class t WHERE t.oid = coalesce(i.indrelid, c.oid))
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_index i ON i.indexrelid=c.oid WHERE n.nspname='reporting_s01' AND c.relkind IN ('r','i') AND c.relname IN
('jobs','assets','jobs_pkey','jobs_claim','jobs_reducible','jobs_identity_created','jobs_identity_in_flight','assets_pkey','assets_expires_at','assets_identity_kind_expires','assets_identity_results') OR c.relname LIKE 'jobs_tenant_integration%' AND n.nspname='reporting_s01' ORDER BY 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var name, kind string
		var size, tuples int64
		if err := rows.Scan(&name, &kind, &size, &tuples); err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%-8s %-8d size  %-60s %12d bytes (%s)", e.phase, e.size, name, size, kind))
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Fprintln(e.out, l)
	}
	var toast int64
	_ = e.admin.QueryRow(ctx, "SELECT coalesce(sum(pg_total_relation_size(c.oid)-pg_relation_size(c.oid)),0) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='reporting_s01' AND c.relname IN ('jobs','assets')").Scan(&toast)
	fmt.Fprintf(e.out, "%-8s %-8d size  jobs+assets indexes and toast together                                  %12d bytes\n", e.phase, e.size, toast)
	return nil
}

func hostLines(ctx context.Context, admin *pgxpool.Pool, w io.Writer) {
	cpu, mem := "", ""
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				cpu = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		mem = strings.Fields(strings.Split(string(b), "\n")[0])[1] + " kB MemTotal"
	}
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	var version, shared, workmem, fsync, cache string
	_ = admin.QueryRow(ctx, "SELECT version(), current_setting('shared_buffers'), current_setting('work_mem'), current_setting('fsync'), current_setting('effective_cache_size')").Scan(&version, &shared, &workmem, &fsync, &cache)
	fmt.Fprintf(w, "host: %s; kernel %s; cpu %s; %d logical CPUs; %s\npostgres: %s\npostgres settings: shared_buffers=%s work_mem=%s fsync=%s effective_cache_size=%s (container, loopback, no TLS)\n", os.Getenv("HOSTTYPE"), strings.TrimSpace(string(kernel)), cpu, numCPU(), mem, version, shared, workmem, fsync, cache)
}

// Run seeds each size, measures it as configured, and removes what it seeded. The fixture is left at the latest version.
func Run(ctx context.Context, cfg Config) error {
	cfg.DSN = noParallel(cfg.DSN)
	c, err := pgxCfg(cfg.DSN)
	if err != nil {
		return err
	}
	_ = c
	admin, err := fixturedb.OpenSized(ctx, cfg.DSN, 8, "reporting_test_s01")
	if err != nil {
		return fmt.Errorf("the fixture guard refused the DSN: %w", err)
	}
	defer admin.Close()
	if port, err := dsnPort(cfg.DSN); err != nil || port == 55432 {
		return fmt.Errorf("refusing this DSN")
	}
	st, err := postgres.Open(ctx, cfg.DSN, true)
	if err != nil {
		return err
	}
	st.Close()
	tmpl, err := Template(ctx)
	if err != nil {
		return err
	}
	roles, err := makeRoles(ctx, admin)
	if err != nil {
		return err
	}
	defer roles.drop()
	hostLines(ctx, admin, cfg.Out)
	appPool, err := fixturedb.OpenSized(ctx, roles.dsn(cfg.DSN, roles.app), 64, "reporting_test_s01")
	if err != nil {
		return err
	}
	defer appPool.Close()
	sweepPool, err := fixturedb.OpenSized(ctx, roles.dsn(cfg.DSN, roles.sweep), 4, "reporting_test_s01")
	if err != nil {
		return err
	}
	defer sweepPool.Close()
	for _, size := range cfg.Sizes {
		p := Profile{Jobs: size, Assets: size * cfg.AssetsPercent / 100, Identities: cfg.Identities, Tenants: cfg.Tenants, HotPercent: cfg.HotPercent, Days: cfg.Days}
		e := &env{cfg: cfg, p: p, tmpl: tmpl, admin: admin, app: appPool, sweep: sweepPool, store: postgres.OnPoolForTests(appPool), out: cfg.Out, size: size}
		fmt.Fprintf(cfg.Out, "\n== size %d job rows, %d asset rows, %d identities in %d tenants, identity 0 holds %d%%\n", size, p.Assets, p.Identities, p.Tenants, p.HotPercent)
		if err := e.runSize(ctx); err != nil {
			_ = Remove(context.Background(), admin)
			return err
		}
	}
	return nil
}

func (e *env) runSize(ctx context.Context) error {
	defer func() { _ = Remove(context.Background(), e.admin) }()
	if err := Remove(ctx, e.admin); err != nil {
		return err
	}
	// The deleted rows of an earlier run leave their pages in the files; rewrite the tables so that the sizes printed are the seeded rows'.
	for _, t := range []string{"jobs", "assets"} {
		if _, err := e.admin.Exec(ctx, "VACUUM FULL reporting_s01."+t); err != nil {
			return err
		}
	}
	// The history is seeded at version 8, so that it is the same rows in both phases and the indexes of 009 are built over them once.
	if err := postgres.Down(ctx, e.admin, 8, "capacity", false); err != nil {
		return err
	}
	start := time.Now()
	if err := Seed(ctx, e.admin, e.p, e.tmpl); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "seeded in %.1f s (version 8, no 009 indexes)\n", time.Since(start).Seconds())
	if err := e.rowSizes(ctx); err != nil {
		return err
	}
	for _, phase := range []string{"before", "after"} {
		if !slices.Contains(e.cfg.Phases, phase) {
			continue
		}
		e.phase = phase
		if phase == "after" {
			t := time.Now()
			if err := postgres.Up(ctx, e.admin, 0, "capacity"); err != nil {
				return err
			}
			// Every step after 008 is applied over the seeded history: the indexes of 009, the statistics of 010 and, of the later
			// steps, the index of kept_results (S25-B1), which is built over the assets.
			fmt.Fprintf(e.out, "migrations 009 to %03d applied (their indexes built over the history) in %.2f s\n", postgres.LatestVersion(), time.Since(t).Seconds())
		}
		if _, err := e.admin.Exec(ctx, "ANALYZE reporting_s01.jobs; ANALYZE reporting_s01.assets"); err != nil {
			return err
		}
		e.admitted = nil
		if err := e.measure(ctx); err != nil {
			return err
		}
		// Put the history back as it was: the admissions deleted, the claimed jobs queued again.
		if _, err := e.admin.Exec(ctx, "DELETE FROM reporting_s01.jobs WHERE id = ANY($1)", e.admitted); err != nil {
			return err
		}
		if _, err := e.admin.Exec(ctx, "UPDATE reporting_s01.jobs SET state='queued', lease_until=NULL, generation=0 WHERE tenant LIKE '"+TenantPrefix+"%' AND generation > 0"); err != nil {
			return err
		}
		if phase == "after" {
			if err := e.sweepStep(ctx); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(e.out, "%-8s %-8d one sweep: not measured (the binary refuses a store below version 9)\n", e.phase, e.size)
		}
		if err := e.verifyStep(ctx); err != nil {
			return err
		}
		for _, t := range []string{"jobs", "assets"} {
			if _, err := e.admin.Exec(ctx, "VACUUM ANALYZE reporting_s01."+t); err != nil {
				return err
			}
		}
		if err := e.sizes(ctx); err != nil {
			return err
		}
	}
	// Leave the fixture at the latest version.
	return postgres.Up(ctx, e.admin, 0, "capacity")
}

// rowSizes prints what the seeded rows weigh right after the seed (before any measured write): the average stored size of the payload column
// (pg_column_size, compressed if it is) and the table's size per row, heap and toast together.
func (e *env) rowSizes(ctx context.Context) error {
	for _, t := range []struct{ table, group string }{{"jobs", "state"}, {"assets", "kind"}} {
		rows, err := e.admin.Query(ctx, "SELECT "+t.group+", count(*), avg(pg_column_size(data))::int FROM reporting_s01."+t.table+" WHERE tenant LIKE '"+TenantPrefix+"%' GROUP BY 1 ORDER BY 1")
		if err != nil {
			return err
		}
		for rows.Next() {
			var g string
			var n, avg int64
			if err := rows.Scan(&g, &n, &avg); err != nil {
				rows.Close()
				return err
			}
			fmt.Fprintf(e.out, "rowsize  %-8d %-8s %-10s rows=%-8d avg stored size of data = %d bytes\n", e.size, t.table, g, n, avg)
		}
		rows.Close()
		var total, rowsN int64
		if err := e.admin.QueryRow(ctx, "SELECT pg_table_size('reporting_s01."+t.table+"'), (SELECT count(*) FROM reporting_s01."+t.table+")").Scan(&total, &rowsN); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "rowsize  %-8d %-8s heap+toast per row = %d bytes (%d bytes over %d rows)\n", e.size, t.table, total/max(rowsN, 1), total, rowsN)
	}
	return nil
}
