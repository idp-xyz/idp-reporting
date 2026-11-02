package integration_test

import (
	"context"
	"errors"
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
// throwaway MinIO, and what only the durable store has to qualify: what Publish takes, what the row must say, the ledger, a row that names an
// object that is not its own. A subtest of TestDurablePostgresContract.

// objectStoreOf makes the raw object store of a world; now is the platform's clock of the world, which moves when the world ages.
type objectStoreOf func(t *testing.T, now func() time.Time) r.ObjectStore

func memoryObjects(t *testing.T, now func() time.Time) r.ObjectStore { return objects.NewMemory(now) }

// minioObjects is the throwaway server of the qualification, or a skip, or a failure when REPORTING_OBJECT_STORE_REQUIRE=1 (as the PostgreSQL
// gate is). The bucket is created by the test, once, with the one bucket-level call; the objects of an earlier test are removed.
func minioObjects(t *testing.T, _ func() time.Time) r.ObjectStore {
	t.Helper()
	endpoint, access, secret := os.Getenv("REPORTING_OBJECT_STORE_TEST_ENDPOINT"), os.Getenv("REPORTING_OBJECT_STORE_TEST_ACCESS_KEY"), os.Getenv("REPORTING_OBJECT_STORE_TEST_SECRET_KEY")
	if endpoint == "" || access == "" || secret == "" {
		if os.Getenv("REPORTING_OBJECT_STORE_REQUIRE") == "1" {
			t.Fatal("required object store endpoint and keys (REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY, _SECRET_KEY) missing")
		}
		t.Skip("S3 gate not run: set REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY and _SECRET_KEY (a throwaway MinIO)")
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
		h := &retention{t: t, repo: v.repo, s: service(v.repo, store)(nil), p: v.p, issuer: issuer,
			age: func(d time.Duration) { ageDurable(t, admin, d); skew.Add(int64(d)) }, service: service(v.repo, store)}
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
	for name, makeObjects := range map[string]objectStoreOf{"memory_objects": memoryObjects, "minio": minioObjects} {
		t.Run(name, func(t *testing.T) {
			world := newObjectsDurable(makeObjects)
			t.Run("contract", func(t *testing.T) { runObjectContract(t, world) })
			t.Run("durable", func(t *testing.T) { durableObjectDetails(t, world) })
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
