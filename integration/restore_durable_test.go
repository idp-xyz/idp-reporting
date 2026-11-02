package integration_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
	"slices"
	"strings"
	"testing"
	"time"
)

// The restore cases of S04-F (docs/S04F_SCOPE.md F2, F3). A restore leaves the database at one moment and the bucket at another, and the platform
// must be what its invariants say whichever is newer. The guarded fixture databases cannot be dropped and recreated, so a restore is simulated by
// rewinding: a backup is the content of the two tables at one moment (as a dump holds it) and restoring it replaces them with it, rows the
// backup never had gone and jobs back in the state they were in; the objects of the bucket are kept, removed or replaced by the test, which is
// what a restore of a bucket does. The real thing, with pg_dump and pg_restore, is the drill (scripts/restore_drill.sh), local evidence and not CI.

// backupOf is the two tables as a dump holds them.
type backupOf struct{ assets, jobs string }

func takeBackup(t *testing.T, admin *pgxpool.Pool) backupOf {
	t.Helper()
	var b backupOf
	if e := admin.QueryRow(context.Background(), `SELECT (SELECT coalesce(jsonb_agg(to_jsonb(a)),'[]') FROM reporting_s01.assets a)::text, (SELECT coalesce(jsonb_agg(to_jsonb(j)),'[]') FROM reporting_s01.jobs j)::text`).Scan(&b.assets, &b.jobs); e != nil {
		t.Fatal(e)
	}
	return b
}

// restore puts the tables back to the backup: whatever was written since is gone, whatever changed is as it was.
func (b backupOf) restore(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	for _, s := range []string{
		`DELETE FROM reporting_s01.assets`, `DELETE FROM reporting_s01.jobs`,
		`INSERT INTO reporting_s01.assets SELECT * FROM jsonb_populate_recordset(NULL::reporting_s01.assets, '` + b.assets + `'::jsonb)`,
		`INSERT INTO reporting_s01.jobs SELECT * FROM jsonb_populate_recordset(NULL::reporting_s01.jobs, '` + b.jobs + `'::jsonb)`,
	} {
		if _, e = tx.Exec(ctx, s); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
}

func verify(t *testing.T, pool *pgxpool.Pool, objs r.ObjectStore, deep bool) postgres.VerifyResult {
	t.Helper()
	res, e := postgres.VerifyObjects(context.Background(), pool, objs, deep)
	if e != nil {
		t.Fatal(e)
	}
	return res
}

func sameIDs(got []string, want ...string) bool {
	a, b := slices.Clone(got), slices.Clone(want)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// restoreObjects is what a restore leaves between the rows and the objects, on the durable store with each object store (a subtest of
// TestDurablePostgresContract/object_storage).
func restoreObjects(t *testing.T, newWorld func(*testing.T) *objWorld) {
	ctx := context.Background()
	t.Run("the_bucket_newer_than_the_database_leaves_orphans_that_verification_counts_and_the_sweep_removes", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		snap := w.freeze("snap")
		kept := w.export(snap.SnapshotID, "kept")
		backup := takeBackup(t, admin)
		// Work after the backup: an export and an issue, whose objects are in the bucket the restore leaves alone.
		lostExport := w.export(snap.SnapshotID, "lost export")
		lostIssue := w.issue(snap.SnapshotID, "lost issue")
		backup.restore(t, admin)
		for _, id := range []string{lostExport.ArtifactID, lostIssue.ArtifactID} {
			if _, e := w.s.Download(ctx, w.p, id); !errors.Is(e, r.ErrNotFound) {
				t.Fatalf("the download of a row the restore does not have: %v, want 404", e)
			}
		}
		for _, deep := range []bool{false, true} {
			res := verify(t, admin, w.store, deep)
			if res.Rows != 1 || res.Objects != 3 || res.Orphans != 2 || res.Missing != 0 || res.Mismatched != 0 || res.Invalid != 0 || res.Failed() || res.Deep != deep {
				t.Fatalf("deep=%v: %+v, want 1 row, 3 objects, 2 orphans, nothing missing: a bucket newer than the database is harmless", deep, res)
			}
		}
		if a, e := w.s.Download(ctx, w.p, kept.ArtifactID); e != nil || len(a.Content) == 0 {
			t.Fatalf("the row the backup has is served: %v", e)
		}
		// The sweep's reconciliation removes the orphans after the grace, and only them.
		w.age(objGrace + time.Minute)
		res := w.sweep(r.SweepOptions{Grace: objGrace})
		if res.Orphans != 2 || res.Artifacts != 0 || res.ObjectsLeft != 0 {
			t.Fatalf("%+v: the reconciliation must remove the two orphans and nothing else", res)
		}
		if keys := w.keys(); !sameIDs(keys, r.ObjectKey(kept.ArtifactID)) {
			t.Fatalf("the bucket holds %v after the reconciliation", keys)
		}
		if res := verify(t, admin, w.store, true); res.Orphans != 0 || res.Failed() {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("the_database_newer_than_the_bucket_is_missing_until_the_operator_puts_the_object_back", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		snap := w.freeze("snap")
		older := w.export(snap.SnapshotID, "older")
		newer := w.export(snap.SnapshotID, "newer")
		newerKey := r.ObjectKey(newer.ArtifactID)
		row := w.artifactRow(newer.ArtifactID)
		// The bucket the operator restored does not have the newer object. The copy the operator keeps of it is what is put back.
		kept, e := w.raw.Get(ctx, newerKey, row.SHA256, row.Size)
		if e != nil {
			t.Fatal(e)
		}
		w.raw.Delete(ctx, newerKey)
		for _, deep := range []bool{false, true} {
			res := verify(t, admin, w.store, deep)
			if res.Rows != 2 || res.Objects != 1 || res.Missing != 1 || res.Orphans != 0 || res.Mismatched != 0 || !res.Failed() || !sameIDs(res.MissingIDs, newer.ArtifactID) {
				t.Fatalf("deep=%v: %+v, want exactly the newer row missing", deep, res)
			}
		}
		// Never a wrong file: the download is the integrity failure (409 over HTTP), the older one is served.
		if a, e := w.s.Download(ctx, w.p, newer.ArtifactID); !errors.Is(e, r.ErrIntegrity) || len(a.Content) != 0 {
			t.Fatalf("the download of a row whose object is missing: %v", e)
		}
		if a, e := w.s.Download(ctx, w.p, older.ArtifactID); e != nil || len(a.Content) == 0 {
			t.Fatal(e)
		}
		// Another file under the key is not the row's file. It is invisible to the presence check and found by the deep one, and never served,
		// whether it is of the same length or not.
		for name, other := range map[string][]byte{"same length": []byte(strings.Repeat("x", len(kept))), "another length": []byte("PK another file")} {
			if e := w.raw.Put(ctx, newerKey, other, sha(other)); e != nil {
				t.Fatal(e)
			}
			if res := verify(t, admin, w.store, false); res.Missing != 0 || res.Mismatched != 0 || res.Failed() {
				t.Fatalf("%s: the presence check %+v reads no bytes and cannot know", name, res)
			}
			res := verify(t, admin, w.store, true)
			if res.Missing != 0 || res.Mismatched != 1 || !res.Failed() || !sameIDs(res.MismatchedIDs, newer.ArtifactID) || len(res.MissingIDs) != 0 {
				t.Fatalf("%s: %+v, want exactly the newer row mismatched", name, res)
			}
			if a, e := w.s.Download(ctx, w.p, newer.ArtifactID); !errors.Is(e, r.ErrIntegrity) || len(a.Content) != 0 {
				t.Fatalf("%s: a file that is not the row's was served: %v", name, e)
			}
		}
		// The operator puts back the same bytes: clean, and served.
		if e = w.raw.Put(ctx, newerKey, kept, row.SHA256); e != nil {
			t.Fatal(e)
		}
		if res := verify(t, admin, w.store, true); res.Failed() || res.Rows != 2 || res.Objects != 2 || res.Orphans != 0 {
			t.Fatalf("%+v", res)
		}
		if a, e := w.s.Download(ctx, w.p, newer.ArtifactID); e != nil || string(a.Content) != string(kept) {
			t.Fatalf("the object put back is not served: %v", e)
		}
	})
	t.Run("a_row_that_does_not_say_where_its_file_is_is_invalid_not_a_pass", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		snap := w.freeze("snap")
		ex := w.export(snap.SnapshotID, "export")
		seal := func() {
			if _, e := admin.Exec(ctx, "UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1", ex.ArtifactID); e != nil {
				t.Fatal(e)
			}
		}
		var original string
		if e := admin.QueryRow(ctx, "SELECT data::text FROM reporting_s01.assets WHERE id=$1", ex.ArtifactID).Scan(&original); e != nil {
			t.Fatal(e)
		}
		for name, sql := range map[string]string{
			"another artifact's object":       `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Object}', to_jsonb('artifacts/' || md5('x')))`,
			"content and object":              `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Content}','"UEs="')`,
			"no length":                       `UPDATE reporting_s01.assets SET data=data - 'Size'`,
			"a digest that is not one":        `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{SHA256}','"zz"')`,
			"a payload that fails its digest": `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Size}','1')`,
		} {
			if _, e := admin.Exec(ctx, sql+" WHERE id='"+ex.ArtifactID+"'"); e != nil {
				t.Fatal(name, e)
			}
			if name != "a payload that fails its digest" {
				seal()
			}
			for _, deep := range []bool{false, true} {
				res := verify(t, admin, w.store, deep)
				if res.Invalid != 1 || res.Mismatched != 0 || !res.Failed() || !sameIDs(res.InvalidIDs, ex.ArtifactID) {
					t.Errorf("%s (deep=%v): %+v", name, deep, res)
				}
			}
			if _, e := admin.Exec(ctx, "UPDATE reporting_s01.assets SET data=$2::jsonb WHERE id=$1", ex.ArtifactID, original); e != nil {
				t.Fatal(e)
			}
			seal()
			if res := verify(t, admin, w.store, true); res.Failed() {
				t.Fatalf("%s: put back, still failing: %+v", name, res)
			}
		}
	})
	t.Run("verification_never_writes", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		snap := w.freeze("snap")
		w.export(snap.SnapshotID, "export")
		w.issue(snap.SnapshotID, "issue")
		orphan := []byte("an orphan")
		if e := w.raw.Put(ctx, r.ObjectKey(r.NewID()), orphan, sha(orphan)); e != nil {
			t.Fatal(e)
		}
		w.age(objGrace + time.Minute) // everything it could delete is deletable
		before, puts, deletes, keys := digestOf(t, admin), len(w.store.puts), len(w.store.deletes), w.keys()
		sweeps := count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps")
		for _, deep := range []bool{false, true} {
			if res := verify(t, admin, w.store, deep); res.Failed() || res.Orphans != 1 {
				t.Fatalf("%+v", res)
			}
		}
		if digestOf(t, admin) != before || count(t, admin, "SELECT count(*) FROM reporting_s01.sweeps") != sweeps {
			t.Fatal("the verification changed the database")
		}
		if len(w.store.puts) != puts || len(w.store.deletes) != deletes || !slices.Equal(w.keys(), keys) {
			t.Fatal("the verification changed the bucket")
		}
	})
	t.Run("verification_pages_through_rows_and_objects_that_interleave", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		// 2300 rows over three pages of rows and of objects: every 7th row has no object, every 11th a row's object is another file, and
		// between the rows' keys there are orphans (every 5th): the join must attribute each to the right side across page boundaries.
		const total = 2300
		body := []byte("PK body of a file")
		other := []byte("PK body of ANOTHER")
		ids := make([]string, total)
		for i := range ids {
			ids[i] = fmt.Sprintf("%032x", i*3+1)
		}
		var wantMissing, wantMismatched []string
		wantOrphans := 0
		for i, id := range ids {
			if _, e := admin.Exec(ctx, `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
VALUES($1,'t','i','x','y','artifact',jsonb_build_object('ID',$1::text,'Object',$2::text,'Size',$3::int,'SHA256',$4::text),'',clock_timestamp()+interval '1 hour')`, id, r.ObjectKey(id), len(body), sha(body)); e != nil {
				t.Fatal(e)
			}
			switch {
			case i%7 == 0:
				wantMissing = append(wantMissing, id)
			case i%11 == 0:
				if e := w.raw.Put(ctx, r.ObjectKey(id), other, sha(other)); e != nil {
					t.Fatal(e)
				}
				wantMismatched = append(wantMismatched, id)
			default:
				if e := w.raw.Put(ctx, r.ObjectKey(id), body, sha(body)); e != nil {
					t.Fatal(e)
				}
			}
			if i%5 == 0 {
				orphan := fmt.Sprintf("%032x", i*3+2)
				if e := w.raw.Put(ctx, r.ObjectKey(orphan), body, sha(body)); e != nil {
					t.Fatal(e)
				}
				wantOrphans++
			}
		}
		// The first and the last key of the bucket are orphans too, and a key that is not ours is nobody's.
		for _, id := range []string{fmt.Sprintf("%032x", 0), fmt.Sprintf("%032x", total*3+10)} {
			if e := w.raw.Put(ctx, r.ObjectKey(id), body, sha(body)); e != nil {
				t.Fatal(e)
			}
			wantOrphans++
		}
		if e := w.raw.Put(ctx, "artifacts/not-an-id", body, sha(body)); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex')`); e != nil {
			t.Fatal(e)
		}
		res := verify(t, admin, w.store, false)
		wantObjects := total - len(wantMissing) + wantOrphans
		if res.Rows != total || res.Objects != wantObjects || res.Missing != len(wantMissing) || res.Orphans != wantOrphans || res.Mismatched != 0 || res.Invalid != 0 || !sameIDs(res.MissingIDs, wantMissing...) {
			t.Fatalf("shallow: %+v, want %d rows, %d objects, %d missing, %d orphans", res, total, wantObjects, len(wantMissing), wantOrphans)
		}
		res = verify(t, admin, w.store, true)
		if res.Rows != total || res.Objects != wantObjects || res.Missing != len(wantMissing) || res.Orphans != wantOrphans || res.Mismatched != len(wantMismatched) || !sameIDs(res.MismatchedIDs, wantMismatched...) || !sameIDs(res.MissingIDs, wantMissing...) {
			t.Fatalf("deep: %+v, want %d mismatched", res, len(wantMismatched))
		}
	})
	t.Run("an_object_store_that_cannot_be_reached_is_an_error_not_a_count", func(t *testing.T) {
		w := newWorld(t)
		admin := fixtureAdmin(t)
		snap := w.freeze("snap")
		w.export(snap.SnapshotID, "export")
		for _, op := range []string{"list", "get"} {
			w.store.fail(op)
			deep := op == "get"
			res, e := postgres.VerifyObjects(ctx, admin, w.store, deep)
			if !errors.Is(e, r.ErrUnavailable) || res.Missing != 0 || res.Mismatched != 0 {
				t.Fatalf("%s down: %+v %v, an outage must never be counted as missing or mismatched", op, res, e)
			}
		}
		w.store.fail()
		if res := verify(t, admin, w.store, true); res.Failed() {
			t.Fatal(res)
		}
		if _, e := postgres.VerifyObjects(ctx, admin, nil, false); !errors.Is(e, r.ErrInvalid) {
			t.Fatal("verification without an object store must refuse", e)
		}
	})
}

// restoreJobs is what a restore leaves of the jobs (S04-F F1, F3): a job that was running at the backup is claimed again once its lease has run
// out, an idempotency key answers by the rows the restore holds, and the quotas count them (a subtest of TestDurablePostgresContract).
func restoreJobs(t *testing.T) {
	ctx := context.Background()
	t.Run("a_job_running_at_the_backup_is_claimed_again_only_when_its_lease_has_run_out", func(t *testing.T) {
		w := newQuotaDurable(t)
		admin := fixtureAdmin(t)
		s := w.svc(r.Quotas{})
		j := w.mustSubmit(s, w.p, "running at the backup")
		claim, e := w.repo.Claim(ctx, time.Minute) // the worker that died with the platform
		if e != nil || claim == nil || claim.Job.ID != j.ID || claim.Job.Generation != 1 {
			t.Fatal(claim, e)
		}
		backup := takeBackup(t, admin)
		// The platform is restored: the job is as the backup had it, running under a lease that is the database's to judge. While the lease
		// holds nothing claims it; once it has run out by the database's clock the next worker does, with the next generation.
		backup.restore(t, admin)
		if ran, e := s.RunOne(ctx); e != nil || ran {
			t.Fatalf("a job whose lease holds was claimed: %v %v", ran, e)
		}
		if _, e = admin.Exec(ctx, `UPDATE reporting_s01.jobs SET lease_until = clock_timestamp() - interval '1 second' WHERE id=$1`, j.ID); e != nil {
			t.Fatal(e)
		}
		if ran, e := s.RunOne(ctx); e != nil || !ran {
			t.Fatalf("a job whose lease has run out was not claimed: %v %v", ran, e)
		}
		got, e := w.repo.Job(ctx, w.p.Identity, j.ID)
		if e != nil || got.State != "succeeded" || got.Generation != 2 || got.SnapshotID == "" {
			t.Fatalf("%+v %v", got, e)
		}
		// The dead worker's claim is fenced: it cannot publish over the new one.
		mustError(t, w.repo.Publish(ctx, *claim, publication(*claim)), r.ErrLease)
		if n := count(t, admin, "SELECT count(*) FROM reporting_s01.assets WHERE kind='snapshot'"); n != 1 {
			t.Fatalf("%d snapshots, want one", n)
		}
	})
	t.Run("an_idempotency_key_answers_by_the_rows_the_restore_holds", func(t *testing.T) {
		w := newQuotaDurable(t)
		admin := fixtureAdmin(t)
		s := w.svc(r.Quotas{})
		kept := w.mustSubmit(s, w.p, "before the backup")
		backup := takeBackup(t, admin)
		lost := w.mustSubmit(s, w.p, "after the backup")
		backup.restore(t, admin)
		// A key of a job the restore holds answers that job, not a new one; the same key with another request is the conflict it always was.
		if again, e := w.submit(s, w.p, "before the backup"); e != nil || again.ID != kept.ID {
			t.Fatalf("%+v %v", again, e)
		}
		changed := contractsim.FinancialQuery()
		changed.AsOf = "2026-10-27"
		if _, e := s.SubmitSnapshot(ctx, w.p, changed, "before the backup"); !errors.Is(e, r.ErrConflict) {
			t.Fatalf("another request under a key of the restore: %v", e)
		}
		// The client's retry of a request whose job the restore does not have (the response was lost with the platform) is admitted anew: a new
		// job under the same key, never the lost id.
		retried, e := w.submit(s, w.p, "after the backup")
		if e != nil || retried.ID == lost.ID || retried.State != "queued" {
			t.Fatalf("%+v %v", retried, e)
		}
		if _, e := w.repo.Job(ctx, w.p.Identity, lost.ID); !errors.Is(e, r.ErrNotFound) {
			t.Fatalf("the lost job exists: %v", e)
		}
		if n := count(t, admin, "SELECT count(*) FROM reporting_s01.jobs"); n != 2 {
			t.Fatalf("%d jobs, want the restored one and the retried one", n)
		}
	})
	t.Run("the_quotas_count_the_rows_the_restore_holds", func(t *testing.T) {
		w := newQuotaDurable(t)
		admin := fixtureAdmin(t)
		two := w.svc(r.Quotas{JobsInFlight: 2})
		w.mustSubmit(two, w.p, "one")
		backup := takeBackup(t, admin)
		w.mustSubmit(two, w.p, "two")
		backup.restore(t, admin)
		// One job in flight is restored: one more is admitted, the next is over the quota. Counted by the restored rows, not by anything the
		// platform remembered of the work that was lost.
		w.mustSubmit(two, w.p, "two again")
		_, e := w.submit(two, w.p, "three")
		refused(t, e, r.QuotaJobsInFlight)
		// The admissions of the last hour are the restored rows' too, by the database's clock.
		hour := w.svc(r.Quotas{AdmissionsPerHour: 2})
		_, e = w.submit(hour, w.p, "four")
		refused(t, e, r.QuotaAdmissionsPerHour)
		if n := count(t, admin, "SELECT count(*) FROM reporting_s01.jobs"); n != 2 {
			t.Fatalf("%d jobs: a refusal stored something", n)
		}
	})
}

// restoreIssue is the restore of an issue job against a product that confirmed the number after the backup (a subtest of
// TestDurablePostgresIssues): the platform's copy is older than the product's, and the job runs again under its own job id.
func restoreIssue(t *testing.T, v durableEnv) {
	ctx := context.Background()
	admin := fixtureAdmin(t)
	issuer := &stampIssuer{}
	s := issueDurableService(t, v, issuer)
	snap := durableFreeze(t, durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}, contractsim.FinancialQuery(), "snap")
	j, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 1), "issued")
	if e != nil {
		t.Fatal(e)
	}
	claim, e := v.repo.Claim(ctx, time.Minute) // a worker is running the job when the backup is taken
	if e != nil || claim == nil || claim.Job.ID != j.ID {
		t.Fatal(claim, e)
	}
	backup := takeBackup(t, admin)
	// The platform goes on: the lease runs out, the job is finished, and the product has confirmed the number.
	expire := func() {
		t.Helper()
		if _, e := admin.Exec(ctx, `UPDATE reporting_s01.jobs SET lease_until = clock_timestamp() - interval '1 second' WHERE id=$1`, j.ID); e != nil {
			t.Fatal(e)
		}
	}
	expire()
	if ran, e := s.RunOne(ctx); !ran || e != nil {
		t.Fatal(ran, e)
	}
	confirmed, held := productRecord(t, v, j.ID)
	if !held || confirmed.State != contractsim.IssueConfirmed || confirmed.Number != "AR-000001" {
		t.Fatalf("%+v %v", confirmed, held)
	}
	// The restore brings back the platform as the backup had it: the job running, no issue record, no number, while the product holds the
	// confirmation. The platform's issue jobs are listed from its own rows, and the product finds each by that job id.
	backup.restore(t, admin)
	if _, e = v.repo.Issue(ctx, v.p.Identity, j.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatalf("the restored platform has the record: %v", e)
	}
	var listed []string
	rows, e := admin.Query(ctx, runbookIssueJobs)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var id, state string
		var gen int64
		var at time.Time
		if e = rows.Scan(&id, &state, &gen, &at); e != nil {
			t.Fatal(e)
		}
		listed = append(listed, id+" "+state)
	}
	rows.Close()
	if !slices.Equal(listed, []string{j.ID + " running"}) {
		t.Fatalf("the runbook's list of the restored issue jobs: %v", listed)
	}
	if ran, e := s.RunOne(ctx); e != nil || ran {
		t.Fatalf("a job whose lease holds was claimed: %v %v", ran, e)
	}
	expire()
	if ran, e := s.RunOne(ctx); !ran || e != nil {
		t.Fatalf("%v %v", ran, e)
	}
	// The same job id was answered by the product idempotently: one record, one number, on both sides.
	st, e := v.repo.Job(ctx, v.p.Identity, j.ID)
	if e != nil || st.State != "succeeded" || st.Generation != 2 {
		t.Fatalf("%+v %v", st, e)
	}
	after, held := productRecord(t, v, j.ID)
	rec, e := v.repo.Issue(ctx, v.p.Identity, j.ID)
	if e != nil || !held || after.Number != "AR-000001" || after.Ref != confirmed.Ref || after.ConfirmationRef != confirmed.ConfirmationRef || rec.Number != "AR-000001" || rec.ReservationRef != confirmed.Ref || rec.ConfirmationRef != confirmed.ConfirmationRef {
		t.Fatalf("%+v %+v %v", rec, after, e)
	}
	if n := count(t, admin, "SELECT count(*) FROM reporting_s01.assets WHERE kind='issue'"); n != 1 {
		t.Fatalf("%d issue records, want one", n)
	}
	if a, e := s.Download(ctx, v.p, st.ArtifactID); e != nil || a.SHA256 != after.SHA256 {
		t.Fatalf("the file served is not the file the product confirmed: %v", e)
	}
	// Nothing was spent by the second run: the next issue is the next number.
	next, e := s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 2), "next")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	if got, e := s.Job(ctx, v.p, next.ID); e != nil || got.Issue.Number != "AR-000002" {
		t.Fatalf("%+v %v", got, e)
	}
}

// runbookIssueJobs is the query docs/OPERATIONS.md "Backup and restore" gives the operator to list the issue jobs a restore holds; the test above
// runs the text the runbook prints.
const runbookIssueJobs = `SELECT id, state, generation, created_at FROM reporting_s01.jobs WHERE kind = 'issue' ORDER BY created_at, id`

// afterARestore is the invariants of S04-F F1 that concern the database itself, after rows have been rewound under a store that has its roles and
// its policies (a subtest of TestPostgresMigrations/row_level_security): the ledger and the profile are the binary's, the roles meet their
// rules, row-level security is enabled with both policies, and a context still sees only its own identity's rows. The verification of the
// objects runs as a role that has SELECT on assets and nothing else, which is all it needs.
func (w *rlsWorld) afterARestore(t *testing.T) {
	ctx := context.Background()
	wantJobs, wantAssets := count(t, w.admin, "SELECT count(*) FROM reporting_s01.jobs"), count(t, w.admin, "SELECT count(*) FROM reporting_s01.assets")
	// What each identity owns, by the superuser who sees everything (earlier checks of the group leave their own rows).
	owned := func(k string) (jobs, assets int) {
		i := w.identity(k)
		return count(t, w.admin, "SELECT count(*) FROM reporting_s01.jobs WHERE tenant=$1 AND issuer=$2 AND subject=$3", i.Tenant, i.Issuer, i.Subject),
			count(t, w.admin, "SELECT count(*) FROM reporting_s01.assets WHERE tenant=$1 AND issuer=$2 AND subject=$3", i.Tenant, i.Issuer, i.Subject)
	}
	backup := takeBackup(t, w.admin)
	// Work after the backup, as A: a job and a snapshot request the restore will not have.
	if _, e := w.s.SubmitSnapshot(ctx, w.p["A"], contractsim.FinancialQuery(), "after the backup"); e != nil {
		t.Fatal(e)
	}
	backup.restore(t, w.admin)
	if count(t, w.admin, "SELECT count(*) FROM reporting_s01.jobs") != wantJobs || count(t, w.admin, "SELECT count(*) FROM reporting_s01.assets") != wantAssets {
		t.Fatal("the rewind did not restore the rows")
	}
	st, e := postgres.Status(ctx, w.admin)
	if e != nil || len(st.Problems) != 0 || len(st.Pending) != 0 || st.Current() != st.Latest() {
		t.Fatalf("status after a restore: %+v %v", st, e)
	}
	if problems, e := postgres.RoleProblems(ctx, w.app); e != nil || len(problems) != 0 {
		t.Fatalf("the application role after a restore: %v %v", problems, e)
	}
	if problems, e := postgres.SweeperProblems(ctx, w.sweep); e != nil || len(problems) != 0 {
		t.Fatalf("the sweep role after a restore: %v %v", problems, e)
	}
	if s, e := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: w.appDSN, InsecureTestOnly: true}); e != nil {
		t.Fatalf("the production store does not open after a restore: %v", e)
	} else {
		s.Close()
	}
	for _, table := range []string{"jobs", "assets"} {
		if n := count(t, w.admin, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='reporting_s01' AND c.relname=$1 AND c.relrowsecurity`, table); n != 1 {
			t.Fatalf("row-level security is not enabled on %s", table)
		}
		if n := count(t, w.admin, `SELECT count(*) FROM pg_policies WHERE schemaname='reporting_s01' AND tablename=$1 AND policyname IN ('identity','worker')`, table); n != 2 {
			t.Fatalf("%s has %d of its 2 policies", table, n)
		}
	}
	// A context sees its own identity's rows and nobody else's.
	for _, k := range w.who {
		inTx(t, w.app, settingsOf(w.identity(k)), func(tx pgx.Tx) {
			wantJ, wantA := owned(k)
			if n, m := count(t, tx, "SELECT count(*) FROM reporting_s01.jobs"), count(t, tx, "SELECT count(*) FROM reporting_s01.assets"); n != wantJ || m != wantA || wantJ == 0 {
				t.Errorf("after a restore the context %s sees %d jobs and %d assets, want its own %d and %d", k, n, m, wantJ, wantA)
			}
		})
		for _, o := range w.others(k) {
			if _, e := w.store.Job(ctx, w.identity(k), w.jobs[o][0]); !errors.Is(e, r.ErrNotFound) {
				t.Errorf("after a restore %s reads a job of %s: %v", k, o, e)
			}
		}
	}
	// The verification as roles that cannot write: the application role, the sweep role, and a role that has SELECT on assets and nothing more.
	reader := "reporting_rls_reader_t"
	for _, sql := range []string{"DROP OWNED BY " + reader, "DROP ROLE IF EXISTS " + reader} {
		if _, e := w.admin.Exec(ctx, sql); e != nil && !strings.Contains(e.Error(), "does not exist") {
			t.Fatal(e)
		}
	}
	for _, sql := range []string{"CREATE ROLE " + reader + " LOGIN", "GRANT USAGE ON SCHEMA reporting_s01 TO " + reader, "GRANT SELECT ON reporting_s01.assets TO " + reader} {
		if _, e := w.admin.Exec(ctx, sql); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() {
		_, _ = w.admin.Exec(context.Background(), "DROP OWNED BY "+reader+"; DROP ROLE IF EXISTS "+reader)
	})
	ro, e := pgxpool.New(ctx, dsnFor(reader, ""))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(ro.Close)
	if _, e := ro.Exec(ctx, "DELETE FROM reporting_s01.assets"); e == nil {
		t.Fatal("the reader can delete")
	}
	// Two rows that name objects, one of which the bucket has.
	objs := objects.NewMemory(nil)
	body := []byte("PK a file")
	have, lack := r.NewID(), r.NewID()
	if e := objs.Put(ctx, r.ObjectKey(have), body, sha(body)); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{have, lack} {
		if _, e := w.admin.Exec(ctx, `INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
VALUES($1,'t','i','x','y','artifact',jsonb_build_object('ID',$1::text,'Object',$2::text,'Size',$3::int,'SHA256',$4::text),'',clock_timestamp()+interval '1 hour')`, id, r.ObjectKey(id), len(body), sha(body)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := w.admin.Exec(ctx, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id IN ($1,$2)`, have, lack); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = w.admin.Exec(context.Background(), "DELETE FROM reporting_s01.assets WHERE id IN ($1,$2)", have, lack)
	})
	before := digestOf(t, w.admin)
	for name, pool := range map[string]*pgxpool.Pool{"application role": w.app, "sweep role": w.sweep, "reader": ro} {
		res, e := postgres.VerifyObjects(ctx, pool, objs, true)
		if e != nil || res.Rows != 2 || res.Missing != 1 || res.Mismatched != 0 || res.Objects != 1 || !sameIDs(res.MissingIDs, lack) {
			t.Errorf("%s: %+v %v", name, res, e)
		}
	}
	if digestOf(t, w.admin) != before {
		t.Fatal("the verification changed the database")
	}
}
