package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
)

// The object storage qualification of the durable store (S04-E): the contract of objects_test.go on PostgreSQL with objects.Memory and with a
// throwaway S3-compatible server, and what only the durable store has to qualify: what Publish takes, what the row must say, the ledger, a row that names an
// object that is not its own. A subtest of TestDurablePostgresContract.

// objectStoreOf makes the raw object store of a world; now is the platform's clock of the world, which moves when the world ages.
type objectStoreOf func(t *testing.T, now func() time.Time) r.ObjectStore

func memoryObjects(t *testing.T, now func() time.Time) r.ObjectStore { return objects.NewMemory(now) }

// minioObjects is the throwaway S3-compatible server (MinIO locally, versitygw in CI) of the qualification, or a skip, or a failure when REPORTING_OBJECT_STORE_REQUIRE=1 (as the PostgreSQL
// gate is). The bucket is created by the test, once, with the one bucket-level call; the objects of an earlier test are removed.
func minioObjects(t *testing.T, _ func() time.Time) r.ObjectStore {
	t.Helper()
	endpoint, access, secret := os.Getenv("REPORTING_OBJECT_STORE_TEST_ENDPOINT"), os.Getenv("REPORTING_OBJECT_STORE_TEST_ACCESS_KEY"), os.Getenv("REPORTING_OBJECT_STORE_TEST_SECRET_KEY")
	if endpoint == "" || access == "" || secret == "" {
		if os.Getenv("REPORTING_OBJECT_STORE_REQUIRE") == "1" {
			t.Fatal("required object store endpoint and keys (REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY, _SECRET_KEY) missing")
		}
		t.Skip("S3 gate not run: set REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY and _SECRET_KEY (a throwaway S3-compatible server)")
	}
	c, err := objects.NewS3(objects.S3{Endpoint: endpoint, Bucket: "reporting-test-s04e", Region: "us-east-1", AccessKey: access, SecretKey: secret, LoopbackHTTP: strings.HasPrefix(endpoint, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = objects.CreateBucketForTests(ctx, c); err != nil {
		t.Fatalf("the test bucket could not be created: %v", err)
	}
	emptyBucket(t, c)
	t.Cleanup(func() { emptyBucket(t, c) })
	return c
}

// newObjectsDurable builds a world over the durable store opened with the object store, whose clock for the age of an object moves with the
// world's. The memory store's clock is the test's; the database's cannot move, so the world ages the stored expiries instead (ageDurable), and
// the platform's clock for objects with them.
func newObjectsDurable(makeObjects objectStoreOf) func(*testing.T) *objWorld {
	return func(t *testing.T) *objWorld {
		t.Helper()
		ctx := context.Background()
		var skew atomic.Int64
		now := func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
		store := &faultyStore{ObjectStore: makeObjects(t, now)}
		v := newDurableOpts(t, postgres.WithObjects(store), postgres.WithObjectClock(now))
		legacyRepo, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(legacyRepo.Close)
		issuer := &stampIssuer{}
		service := func(repo r.Repository, objs r.ObjectStore) func(func() time.Time) *r.Service {
			return func(clock func() time.Time) *r.Service {
				cs := []r.Connection{}
				for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
					cs = append(cs, r.Connection{Tenant: v.p.Tenant, Integration: id, SourceTenant: "tenant_a", Adapter: v.source})
				}
				s, e := r.NewService(r.Config{Repository: repo, Connections: cs, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: issuer, Now: clock, Objects: objs})
				if e != nil {
					t.Fatal(e)
				}
				return s
			}
		}
		admin := fixtureAdmin(t)
		// Time passes for the stored expiries, for the platform's clock of the objects, and for the ledger of the sweeps, whose times say
		// when the object store was last reconciled (S25-B1): the database's clock cannot move, so what it wrote moves into the past.
		age := func(d time.Duration) {
			ageDurable(t, admin, d)
			if _, e := admin.Exec(ctx, `UPDATE reporting_s01.sweeps SET at = at - $1::interval, settled_from = settled_from - $1::interval`, fmt.Sprintf("%d microseconds", d.Microseconds())); e != nil {
				t.Fatal(e)
			}
			skew.Add(int64(d))
		}
		h := &retention{t: t, repo: v.repo, s: service(v.repo, store)(nil), p: v.p, issuer: issuer, age: age, service: service(v.repo, store)}
		return &objWorld{retention: h, raw: store.ObjectStore, store: store,
			product: func(id string) (string, string, bool) {
				rec, ok, e := v.source.IssueRecord(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				return rec.State, rec.SHA256, ok
			},
			rowsOnly: func(o r.SweepOptions) r.SweepResult {
				res, e := postgres.Sweep(ctx, admin, o)
				if e != nil {
					t.Fatal(e)
				}
				return res
			},
			over:   func(repo r.Repository, objs r.ObjectStore) *r.Service { return service(repo, objs)(nil) },
			legacy: func() *r.Service { return service(legacyRepo, nil)(nil) },
			reopen: func() r.Repository { return legacyRepo },
		}
	}
}

func durableObjectStorage(t *testing.T) {
	for name, makeObjects := range map[string]objectStoreOf{"memory_objects": memoryObjects, "s3": minioObjects} {
		t.Run(name, func(t *testing.T) {
			world := newObjectsDurable(makeObjects)
			t.Run("contract", func(t *testing.T) { runObjectContract(t, world) })
			t.Run("durable", func(t *testing.T) { durableObjectDetails(t, world) })
			t.Run("restore", func(t *testing.T) { restoreObjects(t, world) })
		})
	}
}

// durableObjectDetails is what the durable store has to qualify beyond the contract.
func durableObjectDetails(t *testing.T, newWorld func(*testing.T) *objWorld) {
	ctx := context.Background()
	// claimed admits an export of a fresh snapshot and claims it, returning the claim and the publication's stem.
	claimed := func(t *testing.T, w *objWorld, repo r.Repository, key string) (r.Claim, func(id string, content []byte) r.Artifact) {
		t.Helper()
		snap := w.freeze("snap " + key)
		if _, e := w.s.SubmitExport(ctx, w.p, snap.SnapshotID, key); e != nil {
			t.Fatal(e)
		}
		claim, e := repo.Claim(ctx, time.Minute)
		if e != nil || claim == nil {
			t.Fatal(claim, e)
		}
		return *claim, func(id string, content []byte) r.Artifact {
			return r.Artifact{ID: id, Owner: claim.Job.Owner, SnapshotID: claim.Job.SnapshotID, ExpiresAt: claim.Job.Prepared.ExpiresAt, SHA256: sha(content), Content: content}
		}
	}
	t.Run("publish_takes_an_object_when_the_store_has_an_object_store_and_content_when_it_has_none", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		assets := func(id string) int {
			return count(t, admin, "SELECT count(*) FROM reporting_s01.assets WHERE id=$1", id)
		}
		claim, stem := claimed(t, w, w.repo, "with objects")
		content := []byte("PK a workbook")
		// Content in the row, with an object store configured: refused, nothing written, the job still running.
		inline := stem(r.NewID(), content)
		mustError(t, w.repo.Publish(ctx, claim, r.Publication{Artifact: &inline}), r.ErrIntegrity)
		// An object that is not the key of the artifact's own id, no length, a digest that is not one, content and object together.
		id := r.NewID()
		object := func(mutate func(*r.Artifact)) r.Artifact {
			a := stem(id, nil)
			a.Content, a.Object, a.Size = nil, r.ObjectKey(id), len(content)
			mutate(&a)
			return a
		}
		for name, a := range map[string]r.Artifact{
			"another artifact's key":            object(func(a *r.Artifact) { a.Object = r.ObjectKey(r.NewID()) }),
			"no length":                         object(func(a *r.Artifact) { a.Size = 0 }),
			"too long":                          object(func(a *r.Artifact) { a.Size = r.MaxArtifactBytes + 1 }),
			"a digest that is not":              object(func(a *r.Artifact) { a.SHA256 = "not a digest" }),
			"object and content":                object(func(a *r.Artifact) { a.Content = content }),
			"a key outside the bucket's prefix": object(func(a *r.Artifact) { a.Object = "elsewhere/" + id }),
		} {
			mustError(t, w.repo.Publish(ctx, claim, r.Publication{Artifact: &a}), r.ErrIntegrity)
			if assets(a.ID) != 0 {
				t.Errorf("%s: a refused publication wrote an asset", name)
			}
		}
		if st, e := w.repo.Job(ctx, w.p.Identity, claim.Job.ID); e != nil || st.State != "running" {
			t.Fatalf("a refused publication changed the job: %+v %v", st, e)
		}
		if assets(inline.ID) != 0 {
			t.Fatal("an artifact that still carries content was written")
		}
		good := object(func(*r.Artifact) {})
		good.SHA256 = sha(content)
		if e := w.repo.Publish(ctx, claim, r.Publication{Artifact: &good}); e != nil {
			t.Fatal(e)
		}
		if got, e := w.repo.Artifact(ctx, w.p.Identity, id); e != nil || got.Object != good.Object || got.Content != nil || got.Size != len(content) || got.SHA256 != good.SHA256 {
			t.Fatalf("%+v %v", got, e)
		}
		// A store without an object store takes the opposite: content, never an object.
		other, stem2 := claimed(t, w, w.reopen(), "without objects")
		oid := r.NewID()
		asObject := stem2(oid, nil)
		asObject.Content, asObject.Object, asObject.Size = nil, r.ObjectKey(oid), len(content)
		mustError(t, w.reopen().Publish(ctx, other, r.Publication{Artifact: &asObject}), r.ErrIntegrity)
		if assets(oid) != 0 {
			t.Fatal("a store with no object store wrote a row that promises an object")
		}
		inline2 := stem2(r.NewID(), content)
		if e := w.reopen().Publish(ctx, other, r.Publication{Artifact: &inline2}); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("a_row_must_say_where_the_file_is_exactly_once_and_name_its_own_object", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		snap := w.freeze("snap")
		ex := w.export(snap.SnapshotID, "export")
		var original string
		if e := admin.QueryRow(ctx, "SELECT data::text FROM reporting_s01.assets WHERE id=$1", ex.ArtifactID).Scan(&original); e != nil {
			t.Fatal(e)
		}
		set := func(data string) {
			t.Helper()
			if _, e := admin.Exec(ctx, "UPDATE reporting_s01.assets SET data=$2::jsonb WHERE id=$1", ex.ArtifactID, data); e != nil {
				t.Fatal(e)
			}
			if _, e := admin.Exec(ctx, "UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1", ex.ArtifactID); e != nil {
				t.Fatal(e)
			}
		}
		edit := func(expr string, args ...any) string {
			t.Helper()
			var out string
			if e := admin.QueryRow(ctx, "SELECT ("+expr+")::text FROM (SELECT data FROM reporting_s01.assets WHERE id=$1) a", append([]any{ex.ArtifactID}, args...)...).Scan(&out); e != nil {
				t.Fatal(e)
			}
			return out
		}
		for name, data := range map[string]string{
			"both the content and the object": edit(`jsonb_set(data,'{Content}','"UEs="')`),
			"neither":                         edit(`(data - 'Object') - 'Size'`),
			"no length":                       edit(`data - 'Size'`),
			"a length over the bound":         edit(`jsonb_set(data,'{Size}','9000000')`),
			"a digest that is not one":        edit(`jsonb_set(data,'{SHA256}','"zz"')`),
			"another artifact's object":       edit(`jsonb_set(data,'{Object}', to_jsonb($2::text))`, r.ObjectKey(r.NewID())),
			"an object outside the prefix":    edit(`jsonb_set(data,'{Object}', to_jsonb($2::text))`, "elsewhere/"+ex.ArtifactID),
		} {
			set(data)
			if _, e := w.repo.Artifact(ctx, w.p.Identity, ex.ArtifactID); !errors.Is(e, r.ErrIntegrity) {
				t.Errorf("%s: the row was read: %v", name, e)
			}
			if a, e := w.s.Download(ctx, w.p, ex.ArtifactID); !errors.Is(e, r.ErrIntegrity) || len(a.Content) != 0 {
				t.Errorf("%s: served (%v)", name, e)
			}
			set(original)
			if a, e := w.s.Download(ctx, w.p, ex.ArtifactID); e != nil || len(a.Content) == 0 {
				t.Fatalf("%s: restored, not served: %v", name, e)
			}
		}
		// A change of the payload that the digest does not follow is the old integrity failure.
		if _, e := admin.Exec(ctx, `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Size}','1') WHERE id=$1`, ex.ArtifactID); e != nil {
			t.Fatal(e)
		}
		if _, e := w.repo.Artifact(ctx, w.p.Identity, ex.ArtifactID); !errors.Is(e, r.ErrIntegrity) {
			t.Fatal("a payload that fails its digest was read", e)
		}
	})
	t.Run("the_ledger_counts_the_objects_and_nothing_that_names_one", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		snap := w.freeze("snap")
		w.export(snap.SnapshotID, "export")
		w.issue(snap.SnapshotID, "issue")
		b := []byte("orphan")
		orphan := r.ObjectKey(r.NewID())
		if e := w.raw.Put(ctx, orphan, b, sha(b)); e != nil {
			t.Fatal(e)
		}
		w.pastGrace()
		w.sweep(r.SweepOptions{Grace: objGrace, DryRun: true})
		w.sweep(r.SweepOptions{Grace: objGrace})
		type row struct {
			dry, complete               bool
			artifacts, objects, orphans int
		}
		var rows []row
		cur, e := admin.Query(ctx, "SELECT dry_run, complete, artifacts, objects, orphans FROM reporting_s01.sweeps ORDER BY id")
		if e != nil {
			t.Fatal(e)
		}
		for cur.Next() {
			var x row
			if e = cur.Scan(&x.dry, &x.complete, &x.artifacts, &x.objects, &x.orphans); e != nil {
				t.Fatal(e)
			}
			rows = append(rows, x)
		}
		cur.Close()
		if len(rows) != 2 || rows[0] != (row{true, true, 2, 2, 1}) || rows[1] != (row{false, true, 2, 2, 1}) {
			t.Fatalf("the ledger says %+v", rows)
		}
		// Counts only: no key or id of an object is anywhere in the ledger's text.
		var text string
		if e = admin.QueryRow(ctx, "SELECT coalesce(string_agg(s::text, '|'), '') FROM reporting_s01.sweeps s").Scan(&text); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(text, "artifacts/") || strings.Contains(text, r.ObjectKey("")) {
			t.Fatalf("the ledger holds a key: %s", text)
		}
	})
	// S25-B1: the ledger says which sweeps reconciled the object store, how many objects each left behind and, of a reconciliation that
	// left none, since when the bucket is settled; that is what the next sweep reads to know whether the bucket is due. The memory store
	// keeps the same in a field; here it is three columns, an index and a statement.
	t.Run("the_ledger_says_what_each_sweep_did_about_the_bucket_and_the_sweep_reads_it_by_its_index", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		// The ledgers this subtest writes, the one of fifty thousand rows among them, do not stay for the tests after it.
		t.Cleanup(func() {
			if _, e := admin.Exec(context.Background(), "TRUNCATE reporting_s01.sweeps"); e != nil {
				t.Errorf("the ledger of the sweeps could not be emptied: %v", e)
			}
		})
		every := r.SweepOptions{Grace: objGrace, Reconcile: 6 * time.Hour}
		dry := every
		dry.DryRun = true
		w.sweep(dry)   // 1. nothing is on record: a dry run lists the bucket, and settles nothing
		w.sweep(every) // 2. so the first sweep reconciles
		w.sweep(every) // 3. and the next, within the interval, does not
		// An export whose row is past its grace, 71 minutes later, and whose object cannot be deleted when its row goes.
		snap := w.freeze("snap")
		w.export(snap.SnapshotID, "export")
		w.pastGrace()
		w.store.fail("delete")
		if res := w.sweep(every); res.Artifacts != 1 || res.ObjectsLeft != 1 || res.Reconciled { // 4. within the interval: the object is left
			t.Fatalf("the sweep whose delete failed: %+v", res)
		}
		w.store.fail()
		if res := w.sweep(every); !res.Reconciled || res.Orphans != 1 || res.ObjectsLeft != 0 || len(w.keys()) != 0 { // 5. so this one reconciles
			t.Fatalf("the sweep after one that left an object: %+v, bucket %v", res, w.keys())
		}
		w.rowsOnly(every)                        // 6. without the object store, with nothing to delete: nothing to say
		w.sweep(r.SweepOptions{Grace: objGrace}) // 7. without an interval: a reconciliation, as at every sweep before
		// settled says that the row carries the time its listing began; early, that this time is not after the time the row was written.
		type row struct {
			dry, complete, reconciled bool
			left                      int
			settled, early            bool
		}
		var rows []row
		cur, e := admin.Query(ctx, "SELECT dry_run, complete, reconciled, objects_left, settled_from IS NOT NULL, COALESCE(settled_from <= at, false) FROM reporting_s01.sweeps ORDER BY id")
		if e != nil {
			t.Fatal(e)
		}
		for cur.Next() {
			var x row
			if e = cur.Scan(&x.dry, &x.complete, &x.reconciled, &x.left, &x.settled, &x.early); e != nil {
				t.Fatal(e)
			}
			rows = append(rows, x)
		}
		cur.Close()
		// Only the reconciliations that left nothing say since when the bucket is settled: not the dry run, which listed it too.
		want := []row{{true, true, true, 0, false, false}, {false, true, true, 0, true, true}, {false, true, false, 0, false, false}, {false, true, false, 1, false, false},
			{false, true, true, 0, true, true}, {false, true, false, 0, false, false}, {false, true, true, 0, true, true}}
		if fmt.Sprint(rows) != fmt.Sprint(want) {
			t.Fatalf("the ledger says %+v, want %+v", rows, want)
		}
		// The rule itself, on ledgers written for it. Two kinds of entry concern the bucket, dry runs apart: a sweep that left objects or
		// did not complete, which counts from when it ended, and a reconciliation that ran to its end and left none, which counts from
		// when its listing began. The bucket is settled, within an hour here, only when the newest of them is such a reconciliation and
		// its listing began less than an hour ago.
		settled := func() bool {
			t.Helper()
			var ok bool
			if e := admin.QueryRow(ctx, postgres.BucketSettledForTests(), time.Hour.Microseconds()).Scan(&ok); e != nil {
				t.Fatal(e)
			}
			return ok
		}
		// ago is how long ago the sweep ended; began, of a reconciliation that left nothing, how long ago its listing began (0: when it
		// ended, a listing that took no time); failed, that the sweep did not complete.
		type swept struct {
			ago        time.Duration
			dry        bool
			reconciled bool
			left       int
			began      time.Duration
			failed     bool
		}
		for name, c := range map[string]struct {
			ledger []swept
			want   bool
		}{
			"no sweep on record":                                  {nil, false},
			"a reconciliation ten minutes ago":                    {[]swept{{ago: 10 * time.Minute, reconciled: true}}, true},
			"a reconciliation two hours ago":                      {[]swept{{ago: 2 * time.Hour, reconciled: true}}, false},
			"a reconciliation that left objects":                  {[]swept{{ago: 10 * time.Minute, reconciled: true, left: 3}}, false},
			"a sweep that left objects after a reconciliation":    {[]swept{{ago: 30 * time.Minute, reconciled: true}, {ago: 10 * time.Minute, left: 2}}, false},
			"a reconciliation after a sweep that left objects":    {[]swept{{ago: 30 * time.Minute, left: 2}, {ago: 10 * time.Minute, reconciled: true}}, true},
			"a dry run that would leave objects":                  {[]swept{{ago: 30 * time.Minute, reconciled: true}, {ago: 10 * time.Minute, dry: true, left: 5}}, true},
			"a dry run's listing after a sweep that left objects": {[]swept{{ago: 30 * time.Minute, left: 2}, {ago: 10 * time.Minute, dry: true, reconciled: true}}, false},
			"a sweep with nothing to say after a reconciliation":  {[]swept{{ago: 30 * time.Minute, reconciled: true}, {ago: 10 * time.Minute}}, true},
			"only sweeps with nothing to say":                     {[]swept{{ago: 30 * time.Minute}, {ago: 10 * time.Minute}}, false},
			// Two sweeps at once. The reconciliation began to list forty minutes ago and ended ten minutes ago; the other sweep left
			// objects twenty minutes ago, at keys the listing may have passed already. The reconciliation's row is the later one, and it
			// does not settle what was left after its listing began.
			"a sweep left objects while a reconciliation was listing, which ended last": {[]swept{{ago: 20 * time.Minute, left: 2}, {ago: 10 * time.Minute, reconciled: true, began: 40 * time.Minute}}, false},
			// The same two rows the other way about: the objects were left before the listing began, so the listing saw them.
			"a sweep left objects before a reconciliation began to list": {[]swept{{ago: 50 * time.Minute, left: 2}, {ago: 10 * time.Minute, reconciled: true, began: 40 * time.Minute}}, true},
			// The interval counts from when the listing began: that is the moment the bucket is known as of.
			"a reconciliation that ended within the hour and began before it": {[]swept{{ago: 10 * time.Minute, reconciled: true, began: 70 * time.Minute}}, false},
			// A sweep that was cut short or failed cannot say what it left: a batch whose commit was not answered may have deleted its rows.
			"a sweep that did not complete after a reconciliation": {[]swept{{ago: 30 * time.Minute, reconciled: true}, {ago: 10 * time.Minute, failed: true}}, false},
			"a reconciliation after a sweep that did not complete": {[]swept{{ago: 30 * time.Minute, failed: true}, {ago: 10 * time.Minute, reconciled: true}}, true},
			"a dry run that did not complete":                      {[]swept{{ago: 30 * time.Minute, reconciled: true}, {ago: 10 * time.Minute, dry: true, failed: true}}, true},
			// The same instant to the microsecond: a sweep that left objects ended when a listing began. The row written later decides.
			// Written after the sweep's, the reconciliation settles: what was left by then, its listing saw. Written before it, it does
			// not: the sweep's row came after, and the next sweep lists once more.
			"a listing began the instant a sweep that left objects ended, its row the later one":   {[]swept{{ago: 20 * time.Minute, left: 2}, {ago: 10 * time.Minute, reconciled: true, began: 20 * time.Minute}}, true},
			"a listing began the instant a sweep that left objects ended, its row the earlier one": {[]swept{{ago: 10 * time.Minute, reconciled: true, began: 20 * time.Minute}, {ago: 20 * time.Minute, left: 2}}, false},
		} {
			if _, e := admin.Exec(ctx, "TRUNCATE reporting_s01.sweeps"); e != nil {
				t.Fatal(e)
			}
			// One reading of the database's clock for the whole ledger, so that two rows of it can name the same instant.
			var now time.Time
			if e := admin.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); e != nil {
				t.Fatal(e)
			}
			for _, s := range c.ledger {
				var began *time.Time
				if s.reconciled && s.left == 0 && !s.dry && !s.failed {
					at := now.Add(-max(s.began, s.ago))
					began = &at
				}
				if _, e := admin.Exec(ctx, `INSERT INTO reporting_s01.sweeps(dry_run, complete, grace_seconds, batch, artifacts, snapshots, jobs, batches, reconciled, objects_left, at, settled_from)
VALUES($1, $2, 86400, 500, 0, 0, 0, 0, $3, $4, $5, $6)`, s.dry, !s.failed, s.reconciled, s.left, now.Add(-s.ago), began); e != nil {
					t.Fatal(e)
				}
			}
			if got := settled(); got != c.want {
				t.Errorf("%s: the bucket is settled: %t, want %t", name, got, c.want)
			}
		}
		// The ledger refuses a row that says the bucket is settled by a sweep that could not have settled it: a dry run, a sweep that
		// did not reconcile, one that left objects, one that did not complete. (The values are dry_run, complete, reconciled, objects_left.)
		for name, values := range map[string]string{
			"a dry run":                      "true, true, true, 0",
			"a sweep that did not reconcile": "false, true, false, 0",
			"a sweep that left objects":      "false, true, true, 1",
			"a sweep that did not complete":  "false, false, true, 0",
		} {
			if _, e := admin.Exec(ctx, `INSERT INTO reporting_s01.sweeps(grace_seconds, batch, artifacts, snapshots, jobs, batches, dry_run, complete, reconciled, objects_left, settled_from)
VALUES(86400, 500, 0, 0, 0, 0, `+values+`, clock_timestamp())`); e == nil || !strings.Contains(e.Error(), "sweeps_settled_from_check") {
				t.Errorf("the ledger took the word of %s that the bucket is settled: %v", name, e)
			}
		}
		// A ledger of fifty thousand sweeps, a month of one a minute, of which one reconciled: the statement reads the sweeps that
		// concern the bucket by their own index and not the ledger, so its cost does not grow with the sweeps on record.
		if _, e = admin.Exec(ctx, "TRUNCATE reporting_s01.sweeps"); e != nil {
			t.Fatal(e)
		}
		if _, e = admin.Exec(ctx, `INSERT INTO reporting_s01.sweeps(dry_run, complete, grace_seconds, batch, artifacts, snapshots, jobs, batches, reconciled, at, settled_from)
SELECT false, true, 86400, 500, 0, 0, 0, 0, g = 30, clock_timestamp() - g * interval '1 minute', CASE WHEN g = 30 THEN clock_timestamp() - g * interval '1 minute' END FROM generate_series(1, 50000) g`); e != nil {
			t.Fatal(e)
		}
		if _, e = admin.Exec(ctx, "ANALYZE reporting_s01.sweeps"); e != nil {
			t.Fatal(e)
		}
		if !settled() {
			t.Error("a reconciliation half an hour ago, among fifty thousand sweeps, does not settle the bucket")
		}
		var raw []byte
		if e = admin.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+postgres.BucketSettledForTests(), time.Hour.Microseconds()).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var doc []map[string]any
		if e = json.Unmarshal(raw, &doc); e != nil || len(doc) != 1 {
			t.Fatal(string(raw), e)
		}
		var nodes []planNode
		collectPlan(t, doc[0]["Plan"].(map[string]any), &nodes)
		byIndex := false
		for _, n := range nodes {
			byIndex = byIndex || n.Index == "sweeps_reconciliation"
			if n.Type == "Seq Scan" && n.Relation == "sweeps" {
				t.Errorf("the ledger is scanned to know whether the bucket is due: %s", describePlan(nodes))
			}
		}
		if !byIndex {
			t.Errorf("the sweeps that concern the bucket are not read by sweeps_reconciliation: %s", describePlan(nodes))
		}
	})
	t.Run("a_row_that_names_another_artifacts_object_does_not_make_the_sweep_delete_it", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		snap := w.freeze("old snap")
		old := w.export(snap.SnapshotID, "old export")
		w.pastGrace()
		// A fresh export, whose row and object are valid and young.
		snap2 := w.freeze("fresh snap")
		fresh := w.export(snap2.SnapshotID, "fresh export")
		victim := r.ObjectKey(fresh.ArtifactID)
		// The old artifact's row is tampered (with a digest that follows) to name the fresh artifact's object.
		if _, e := admin.Exec(ctx, `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Object}', to_jsonb($2::text)) WHERE id=$1`, old.ArtifactID, victim); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1", old.ArtifactID); e != nil {
			t.Fatal(e)
		}
		res := w.sweep(r.SweepOptions{Grace: objGrace})
		if res.Artifacts != 1 || res.Objects != 0 {
			t.Fatalf("%+v: the key of a row that is not its own artifact's was deleted", res)
		}
		a, e := w.s.Download(ctx, w.p, fresh.ArtifactID)
		if e != nil || len(a.Content) == 0 {
			t.Fatalf("the fresh artifact lost its object: %v", e)
		}
		// The old artifact's own object had no row any more and was old: the reconciliation took it.
		if res.Orphans != 1 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a_service_and_a_store_that_disagree_about_object_storage_fail_closed", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		// A service with the object store over a store without one: the put happens, the publication is refused, the job fails.
		s := w.over(w.reopen(), w.store)
		j, e := s.SubmitExport(ctx, w.p, snap.SnapshotID, "mismatch one")
		if e != nil {
			t.Fatal(e)
		}
		if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, r.ErrIntegrity) {
			t.Fatal(ran, e)
		}
		if v, e := s.Job(ctx, w.p, j.ID); e != nil || v.State != "failed" || v.ErrorCode != "INTEGRITY_FAILURE" || v.ArtifactID != "" {
			t.Fatalf("%+v %v", v, e)
		}
		if len(w.keys()) != 1 {
			t.Fatalf("the object that was put is an orphan to reconcile: %v", w.keys())
		}
		// The reverse: a service without one over a store with one.
		s = w.over(w.repo, nil)
		j, e = s.SubmitExport(ctx, w.p, snap.SnapshotID, "mismatch two")
		if e != nil {
			t.Fatal(e)
		}
		if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, r.ErrIntegrity) {
			t.Fatal(ran, e)
		}
		if v, e := s.Job(ctx, w.p, j.ID); e != nil || v.State != "failed" || v.ErrorCode != "INTEGRITY_FAILURE" {
			t.Fatalf("%+v %v", v, e)
		}
		if len(w.keys()) != 1 {
			t.Fatalf("%v", w.keys())
		}
	})
	t.Run("a_row_that_promises_an_object_to_a_service_without_a_store_is_unavailable_not_a_file", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		ex := w.export(snap.SnapshotID, "export")
		s := w.over(w.repo, nil)
		a, e := s.Download(ctx, w.p, ex.ArtifactID)
		if !errors.Is(e, r.ErrUnavailable) || !r.IsObjectStoreError(e) || len(a.Content) != 0 {
			t.Fatalf("%v", e)
		}
	})
}
