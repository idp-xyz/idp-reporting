package integration_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
	"time"
)

// durableRetention is what only the durable store has to qualify for retention (S04-B), on top of the contract both stores meet
// (runRetentionContract): the expiry column, the batches as transactions, the ledger of the sweeps, the locks. It is a subtest of
// TestDurablePostgresContract.
func durableRetention(t *testing.T) {
	ctx := context.Background()
	const grace = time.Hour
	count := func(admin *pgxpool.Pool, sql string, args ...any) (n int) {
		t.Helper()
		if e := admin.QueryRow(ctx, sql, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	t.Run("the_expiry_column_is_written_with_the_asset_and_checked_when_it_is_read", func(t *testing.T) {
		h := newRetentionDurable(t)
		admin := fixtureAdmin(t)
		snap := h.freeze("snap")
		export := h.export(snap.SnapshotID, "export")
		issue := h.issue(snap.SnapshotID, "issue")
		// The column is the payload's expiry, to the microsecond; the issue record has none.
		for _, id := range []string{snap.SnapshotID, export.ArtifactID, issue.ArtifactID} {
			if n := count(admin, `SELECT count(*) FROM reporting_s01.assets WHERE id=$1 AND expires_at IS NOT NULL AND expires_at = date_trunc('microseconds', (data->>'ExpiresAt')::timestamptz) AND expires_at > clock_timestamp()`, id); n != 1 {
				t.Fatalf("the expiry column of %s is not the payload's", id)
			}
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.assets WHERE kind='issue' AND expires_at IS NULL`); n != 1 {
			t.Fatal("the issue record has an expiry", n)
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.assets WHERE kind IN ('snapshot','artifact') AND expires_at IS NULL`); n != 0 {
			t.Fatal("an asset that expires has no expiry column", n)
		}
		// A column that is not the payload's, or is missing, or is set on a record: the asset is not served (an integrity failure), whatever
		// the digest of the payload says; put back, it is served again.
		for name, c := range map[string]struct{ id, kind, tamper, restore string }{
			"later":         {snap.SnapshotID, "snapshot", `UPDATE reporting_s01.assets SET expires_at = expires_at + interval '1 second' WHERE id=$1`, `UPDATE reporting_s01.assets SET expires_at = expires_at - interval '1 second' WHERE id=$1`},
			"earlier":       {export.ArtifactID, "artifact", `UPDATE reporting_s01.assets SET expires_at = expires_at - interval '1 microsecond' WHERE id=$1`, `UPDATE reporting_s01.assets SET expires_at = expires_at + interval '1 microsecond' WHERE id=$1`},
			"missing":       {snap.SnapshotID, "snapshot", `UPDATE reporting_s01.assets SET expires_at = NULL WHERE id=$1`, `UPDATE reporting_s01.assets SET expires_at = (data->>'ExpiresAt')::timestamptz WHERE id=$1`},
			"on the record": {issue.ID, "issue", `UPDATE reporting_s01.assets SET expires_at = clock_timestamp() + interval '1 hour' WHERE id=$1`, `UPDATE reporting_s01.assets SET expires_at = NULL WHERE id=$1`},
		} {
			if !h.held(c.kind, c.id) {
				t.Fatal(name, "not served to begin with")
			}
			if _, e := admin.Exec(ctx, c.tamper, c.id); e != nil {
				t.Fatal(e)
			}
			var e error
			switch c.kind {
			case "snapshot":
				_, e = h.repo.Snapshot(ctx, h.p.Identity, c.id)
			case "artifact":
				_, e = h.repo.Artifact(ctx, h.p.Identity, c.id)
			default:
				_, e = h.repo.Issue(ctx, h.p.Identity, c.id)
			}
			mustError(t, e, r.ErrIntegrity)
			if _, e := admin.Exec(ctx, c.restore, c.id); e != nil {
				t.Fatal(e)
			}
			if !h.held(c.kind, c.id) {
				t.Fatal(name, "not served again once put back")
			}
		}
	})
	t.Run("a_missing_expiry_and_a_corrupt_payload_are_left_alone_and_the_sweep_goes_on", func(t *testing.T) {
		h := newRetentionDurable(t)
		admin := fixtureAdmin(t)
		a, b, c := h.freeze("a"), h.freeze("b"), h.freeze("c")
		ea, eb, ec := h.export(a.SnapshotID, "export-a"), h.export(b.SnapshotID, "export-b"), h.export(c.SnapshotID, "export-c")
		h.age(readLife + grace + time.Minute)
		// a: its snapshot has no readable expiry. b: the payload of its snapshot job no longer matches its digest. c: the payload of its
		// snapshot job is not an object.
		for _, sql := range []string{
			fmt.Sprintf(`UPDATE reporting_s01.assets SET expires_at = NULL WHERE id='%s'`, a.SnapshotID),
			fmt.Sprintf(`UPDATE reporting_s01.jobs SET data = jsonb_set(data,'{ID}','"tampered"') WHERE id='%s'`, b.ID),
			fmt.Sprintf(`UPDATE reporting_s01.jobs SET data = '[]'::jsonb, data_sha256 = encode(sha256(convert_to('[]'::jsonb::text,'UTF8')),'hex') WHERE id='%s'`, c.ID),
		} {
			if _, e := admin.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
		}
		res := h.sweep(r.SweepOptions{Grace: grace})
		// The snapshot of a is held (no expiry) and so is the artifact of its export, which is never deleted before its snapshot; b's and
		// c's snapshots and artifacts go. Of the six jobs, those of a stay (their snapshot is stored), and the snapshot jobs of b and c
		// are not reduced (a corrupt payload, one that is not an object): their export jobs are.
		if res.Artifacts != 2 || res.Snapshots != 2 || res.Jobs != 2 {
			t.Fatalf("the sweep did %+v", res)
		}
		// (Read through the store, the snapshot whose column is missing is an integrity failure: it is counted in SQL.)
		if n := count(admin, `SELECT count(*) FROM reporting_s01.assets WHERE id=$1`, a.SnapshotID); n != 1 || !h.held("artifact", ea.ArtifactID) || h.held("artifact", eb.ArtifactID) || h.held("artifact", ec.ArtifactID) {
			t.Fatal("a snapshot with no readable expiry, or its artifact, was deleted, or another was not")
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.assets WHERE id=$1 AND expires_at IS NULL`, a.SnapshotID); n != 1 {
			t.Fatal("the sweep touched the expiry")
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.jobs WHERE id IN ($1,$2) AND NOT data ? 'Reduced'`, b.ID, c.ID); n != 2 {
			t.Fatal("a payload that fails its digest, or is not an object, was rewritten")
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.jobs WHERE id IN ($1,$2) AND data ? 'Reduced'`, eb.ID, ec.ID); n != 2 {
			t.Fatal("the export jobs of b and c were not reduced")
		}
		if again := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(again, counts(0, 0, 0)) {
			t.Fatal(again)
		}
	})
	t.Run("a_failed_batch_changes_nothing_and_the_ledger_says_how_far_the_sweep_got", func(t *testing.T) {
		h := newRetentionDurable(t)
		admin := fixtureAdmin(t)
		var snaps, arts []string
		for i := range 3 {
			s := h.freeze(fmt.Sprint("snap", i))
			snaps = append(snaps, s.SnapshotID)
			arts = append(arts, h.export(s.SnapshotID, fmt.Sprint("export", i)).ArtifactID)
		}
		h.age(readLife + grace + time.Minute)
		// The third artifact cannot be deleted: a batch of ten takes it with the others and fails as a whole.
		inject(t, fmt.Sprintf(`CREATE FUNCTION reporting_s01.fixture_s01b_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.id='%s' THEN RAISE EXCEPTION 'fixture injected delete failure'; END IF; RETURN OLD; END $$;
CREATE TRIGGER fixture_s01b_fault BEFORE DELETE ON reporting_s01.assets FOR EACH ROW EXECUTE FUNCTION reporting_s01.fixture_s01b_fault();`, arts[2]))
		res, e := h.s.Sweep(ctx, r.SweepOptions{Grace: grace, Batch: 10})
		if e == nil || !strings.Contains(e.Error(), "fixture injected") || res.Artifacts != 0 || res.Snapshots != 0 || res.Jobs != 0 || res.Batches != 0 {
			t.Fatal("the failed batch was not reported, or counted", res, e)
		}
		for i := range 3 {
			if !h.held("artifact", arts[i]) || !h.held("snapshot", snaps[i]) {
				t.Fatal("a failed batch changed something")
			}
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.sweeps WHERE NOT dry_run AND NOT complete AND artifacts=0 AND snapshots=0 AND jobs=0 AND batches=0`); n != 1 {
			t.Fatal("the failed sweep left no ledger row", n)
		}
		// With batches of one, the first two artifacts are deleted, each in a transaction of its own, and the third stops the sweep: the
		// result and the ledger have what was done, and the snapshots and jobs were not reached.
		res, e = h.s.Sweep(ctx, r.SweepOptions{Grace: grace, Batch: 1})
		if e == nil || res.Artifacts != 2 || res.Snapshots != 0 || res.Jobs != 0 || res.Batches != 2 {
			t.Fatal("the partial sweep", res, e)
		}
		if h.held("artifact", arts[0]) || h.held("artifact", arts[1]) || !h.held("artifact", arts[2]) || !h.held("snapshot", snaps[0]) {
			t.Fatal("the partial sweep's effects")
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.sweeps WHERE NOT complete AND artifacts=2 AND batches=2`); n != 1 {
			t.Fatal("the partial sweep's ledger row", n)
		}
		// The job step fails the same way: no job is half reduced.
		if _, e := admin.Exec(ctx, `DROP TRIGGER fixture_s01b_fault ON reporting_s01.assets; DROP FUNCTION reporting_s01.fixture_s01b_fault()`); e != nil {
			t.Fatal(e)
		}
		inject(t, `CREATE FUNCTION reporting_s01.fixture_s01b_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data ? 'Reduced' AND NEW.kind='export' THEN RAISE EXCEPTION 'fixture injected reduction failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fixture_s01b_fault BEFORE UPDATE ON reporting_s01.jobs FOR EACH ROW EXECUTE FUNCTION reporting_s01.fixture_s01b_fault();`)
		res, e = h.s.Sweep(ctx, r.SweepOptions{Grace: grace, Batch: 100})
		if e == nil || res.Artifacts != 1 || res.Snapshots != 3 || res.Jobs != 0 {
			t.Fatal("the failing reduction", res, e)
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.jobs WHERE data ? 'Reduced'`); n != 0 {
			t.Fatal("a failed batch reduced some jobs", n)
		}
		// Once the fault is gone the next sweep finishes the work, and nothing is counted twice.
		if _, e := admin.Exec(ctx, `DROP TRIGGER fixture_s01b_fault ON reporting_s01.jobs; DROP FUNCTION reporting_s01.fixture_s01b_fault()`); e != nil {
			t.Fatal(e)
		}
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 6)) {
			t.Fatal("the sweep that finished the work", res)
		}
	})
	t.Run("the_ledger_has_counts_and_times_and_nothing_else", func(t *testing.T) {
		h := newRetentionDurable(t)
		admin := fixtureAdmin(t)
		s := h.freeze("snap")
		h.export(s.SnapshotID, "export")
		h.sweep(r.SweepOptions{Grace: grace, DryRun: true})
		h.age(readLife + grace + time.Minute)
		h.sweep(r.SweepOptions{Grace: 90 * time.Minute, Batch: 7, DryRun: true})
		h.sweep(r.SweepOptions{Grace: grace, Batch: 7})
		if n := count(admin, `SELECT count(*) FROM reporting_s01.sweeps`); n != 3 {
			t.Fatal("one ledger row per sweep, dry runs included", n)
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.sweeps WHERE dry_run AND complete AND grace_seconds=3600 AND batch=500 AND artifacts=0 AND snapshots=0 AND jobs=0 AND batches=0`); n != 1 {
			t.Fatal("the first dry run's row")
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.sweeps WHERE dry_run AND complete AND grace_seconds=5400 AND batch=7 AND artifacts=0 AND snapshots=0 AND jobs=0`); n != 1 {
			t.Fatal("the second dry run's row (inside its longer grace)")
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.sweeps WHERE NOT dry_run AND complete AND grace_seconds=3600 AND batch=7 AND artifacts=1 AND snapshots=1 AND jobs=2 AND batches=3 AND at > clock_timestamp() - interval '1 minute'`); n != 1 {
			t.Fatal("the sweep's row")
		}
		// Counts only: every column is a number, a flag or a time.
		if n := count(admin, `SELECT count(*) FROM information_schema.columns WHERE table_schema='reporting_s01' AND table_name='sweeps' AND data_type NOT IN ('bigint','integer','boolean','timestamp with time zone')`); n != 0 {
			t.Fatal("the ledger has a column that is not a count, a flag or a time", n)
		}
	})
	t.Run("the_sweep_takes_no_migration_lock_and_leaves_the_rows_a_transaction_holds", func(t *testing.T) {
		h := newRetentionDurable(t)
		admin := fixtureAdmin(t)
		var snaps, exports []r.JobView
		for i := range 3 {
			s := h.freeze(fmt.Sprint("snap", i))
			snaps = append(snaps, s)
			exports = append(exports, h.export(s.SnapshotID, fmt.Sprint("export", i)))
		}
		h.age(readLife + grace + time.Minute)
		// A migrator holds the advisory lock of the migrations (the key of migrate.go) for as long as it likes: the sweep does not wait for it.
		lock, e := admin.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Rollback(ctx)
		if _, e = lock.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x5245504f5254)); e != nil {
			t.Fatal(e)
		}
		// A transaction holds an expired artifact (of the first snapshot), an expired snapshot (the second) and a job that is free to be
		// reduced (the snapshot job of the third), as a worker publishing or a cancel would: the sweep passes them by, each step in its
		// own way, and the next one, after the transaction, takes them.
		hold, e := admin.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer hold.Rollback(ctx)
		for _, id := range []struct{ table, id string }{{"assets", exports[0].ArtifactID}, {"assets", snaps[1].SnapshotID}, {"jobs", snaps[2].ID}} {
			var got string
			if e = hold.QueryRow(ctx, `SELECT id FROM reporting_s01.`+id.table+` WHERE id=$1 FOR UPDATE`, id.id).Scan(&got); e != nil {
				t.Fatal(e)
			}
		}
		done := make(chan struct{})
		var res r.SweepResult
		go func() {
			defer close(done)
			res, e = h.s.Sweep(ctx, r.SweepOptions{Grace: grace})
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("the sweep waits for a lock")
		}
		// Artifacts: the held one stays; snapshots: the held one stays; jobs: those whose snapshot or artifact is still stored are not
		// reduced (the first export's artifact, the second snapshot's two jobs) and the held one is skipped: the first snapshot job and
		// the third export job are.
		if e != nil || res.Artifacts != 2 || res.Snapshots != 2 || res.Jobs != 2 {
			t.Fatalf("the sweep with rows held did %+v %v", res, e)
		}
		if !h.held("artifact", exports[0].ArtifactID) || count(admin, `SELECT count(*) FROM reporting_s01.assets WHERE id=$1`, snaps[1].SnapshotID) != 1 || count(admin, `SELECT count(*) FROM reporting_s01.jobs WHERE id=$1 AND NOT data ? 'Reduced'`, snaps[2].ID) != 1 {
			t.Fatal("a row another transaction holds was deleted or reduced")
		}
		if e = hold.Rollback(ctx); e != nil {
			t.Fatal(e)
		}
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(1, 1, 4)) || h.held("artifact", exports[0].ArtifactID) {
			t.Fatal("the next sweep did not take what was held", res)
		}
	})
	t.Run("an_artifact_is_never_deleted_before_its_snapshot", func(t *testing.T) {
		h := newRetentionDurable(t)
		admin := fixtureAdmin(t)
		// The artifact's own expiry is two days old; its snapshot is valid for another hour (rows as SQL, which the service never writes:
		// the two expire together). The artifact is held with the snapshot, and goes with it.
		if _, e := admin.Exec(ctx, `
INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
VALUES('s','t','i','x','y','snapshot','{"ID":"s"}', encode(sha256(convert_to('{"ID": "s"}'::jsonb::text,'UTF8')),'hex'), clock_timestamp() + interval '1 hour'),
('a','t','i','x','y','artifact','{"SnapshotID":"s"}', encode(sha256(convert_to('{"SnapshotID": "s"}'::jsonb::text,'UTF8')),'hex'), clock_timestamp() - interval '48 hours')`); e != nil {
			t.Fatal(e)
		}
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(0, 0, 0)) || count(admin, `SELECT count(*) FROM reporting_s01.assets`) != 2 {
			t.Fatal("an artifact went before its snapshot", res)
		}
		if _, e := admin.Exec(ctx, `UPDATE reporting_s01.assets SET expires_at = clock_timestamp() - interval '2 hours' WHERE id='s'`); e != nil {
			t.Fatal(e)
		}
		if res := h.sweep(r.SweepOptions{Grace: grace}); !sameCounts(res, counts(1, 1, 0)) || count(admin, `SELECT count(*) FROM reporting_s01.assets`) != 0 {
			t.Fatal("the artifact and its snapshot are past the grace", res)
		}
	})
	t.Run("the_default_batch_bounds_every_transaction", func(t *testing.T) {
		h := newRetentionDurable(t)
		admin := fixtureAdmin(t)
		// 1100 finished snapshot jobs with their snapshot and artifact, 48 hours past their expiry, written as SQL: the default batch is
		// 500, so each of the three steps takes three transactions.
		if _, e := admin.Exec(ctx, `
INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
SELECT 's' || n, 't','i','x','y','snapshot', jsonb_build_object('ID','s'||n), encode(sha256(convert_to(jsonb_build_object('ID','s'||n)::text,'UTF8')),'hex'), clock_timestamp() - interval '48 hours' FROM generate_series(1,1100) n;
INSERT INTO reporting_s01.assets(id,tenant,integration,issuer,subject,kind,data,data_sha256,expires_at)
SELECT 'a' || n, 't','i','x','y','artifact', jsonb_build_object('SnapshotID','s'||n), encode(sha256(convert_to(jsonb_build_object('SnapshotID','s'||n)::text,'UTF8')),'hex'), clock_timestamp() - interval '48 hours' FROM generate_series(1,1100) n;
INSERT INTO reporting_s01.jobs(id,tenant,integration,issuer,subject,kind,state,idem_key,request_hash,source_expires_at,snapshot_id,data,data_sha256)
SELECT 'j' || n, 't','i','x','y','snapshot','succeeded','k'||n,'h', clock_timestamp() - interval '48 hours', 's'||n, jsonb_build_object('Query',jsonb_build_object('x',n)), encode(sha256(convert_to(jsonb_build_object('Query',jsonb_build_object('x',n))::text,'UTF8')),'hex') FROM generate_series(1,1100) n`); e != nil {
			t.Fatal(e)
		}
		res := h.sweep(r.SweepOptions{})
		if !sameCounts(res, counts(1100, 1100, 1100)) || res.Batches != 9 || res.DryRun {
			t.Fatalf("the sweep with the default grace and batch did %+v", res)
		}
		if n := count(admin, `SELECT count(*) FROM reporting_s01.sweeps WHERE grace_seconds=86400 AND batch=500 AND artifacts=1100 AND snapshots=1100 AND jobs=1100 AND batches=9 AND complete`); n != 1 {
			t.Fatal("the ledger row of the large sweep", n)
		}
	})
	t.Run("a_slip_in_the_options_can_only_keep_data_longer", func(t *testing.T) {
		h := newRetentionDurable(t)
		s := h.freeze("snap")
		h.export(s.SnapshotID, "export")
		h.age(readLife + time.Second)
		for name, o := range map[string]r.SweepOptions{
			"a negative grace":   {Grace: -time.Hour},
			"a negative batch":   {Batch: -1},
			"an oversized batch": {Batch: r.MaxSweepBatch + 1},
			"an oversized grace": {Grace: r.MaxSweepGrace + time.Second},
		} {
			if _, e := h.s.Sweep(ctx, o); !errors.Is(e, r.ErrInvalid) {
				t.Errorf("%s: %v", name, e)
			}
		}
		// A zero grace is the default (24 hours), never no grace: the assets, one second past their expiry, are kept.
		if res := h.sweep(r.SweepOptions{}); !sameCounts(res, counts(0, 0, 0)) || !h.held("snapshot", s.SnapshotID) {
			t.Fatal("a zero grace deleted at once", res)
		}
		if n := count(fixtureAdmin(t), `SELECT count(*) FROM reporting_s01.sweeps`); n != 1 {
			t.Fatal("a refused sweep left a ledger row, or the accepted one did not", n)
		}
	})
}
