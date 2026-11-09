package integration_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/postgres"
)

// scheduleHarness is a schedule store with the two moves of time its contract needs: every active schedule due now (each call a distinct
// occurrence in the past), and every lease run out.
type scheduleHarness struct {
	store        r.ScheduleStore
	makeDue      func(t *testing.T)
	expireLeases func(t *testing.T)
}

func scheduleOf(i r.Identity, key string) r.Schedule {
	q := contractsim.FinancialQuery()
	v := r.Schedule{ID: r.NewID(), Owner: r.Owner{Identity: i, Integration: q.Integration}, Key: key, Name: "AR " + key, Query: r.Normalize(q), AsOf: r.AsOfRunDate,
		Recurrence: r.Recurrence{Frequency: "daily", At: "08:00", Timezone: "Asia/Shanghai"}}
	v.RequestHash = r.Digest(v.Name + key)
	return v
}

// S16-A: the schedule store's contract, the same on the memory and the PostgreSQL stores: creation under a key and a limit, one identity's
// rows only, one run per due schedule and occurrence, claims fenced by a generation and a lease, the states.
func runScheduleContract(t *testing.T, h scheduleHarness) {
	ctx := context.Background()
	s := h.store
	a := r.Identity{Tenant: "t", Issuer: "i", Subject: "a"}
	b := r.Identity{Tenant: "t", Issuer: "i", Subject: "b"}
	first, e := s.CreateSchedule(ctx, scheduleOf(a, "k1"), 2)
	if e != nil || first.State != r.ScheduleActive || first.NextRunAt.IsZero() || first.CreatedAt.IsZero() || first.Owner.Identity != a {
		t.Fatalf("%+v %v", first, e)
	}
	if again, e := s.CreateSchedule(ctx, scheduleOf(a, "k1"), 2); e != nil || again.ID != first.ID {
		t.Fatalf("a replay: %+v %v", again, e)
	}
	conflicting := scheduleOf(a, "k1")
	conflicting.RequestHash = r.Digest("another")
	if _, e := s.CreateSchedule(ctx, conflicting, 2); !errors.Is(e, r.ErrConflict) {
		t.Fatalf("another request under the key: %v", e)
	}
	second, e := s.CreateSchedule(ctx, scheduleOf(a, "k2"), 2)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.CreateSchedule(ctx, scheduleOf(a, "k3"), 2); r.QuotaOf(e) != r.QuotaSchedules {
		t.Fatalf("over the limit: %v", e)
	}
	bad := scheduleOf(a, "k4")
	bad.Recurrence.At = "25:00"
	if _, e := s.CreateSchedule(ctx, bad, 2); !errors.Is(e, r.ErrInvalid) {
		t.Fatalf("an invalid schedule: %v", e)
	}
	theirs, e := s.CreateSchedule(ctx, scheduleOf(b, "k1"), 2)
	if e != nil {
		t.Fatal(e)
	}
	// One identity's rows only.
	if _, e := s.Schedule(ctx, b, first.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatalf("another identity read a schedule: %v", e)
	}
	if _, e := s.SetScheduleState(ctx, b, first.ID, r.SchedulePaused); !errors.Is(e, r.ErrNotFound) {
		t.Fatalf("another identity paused a schedule: %v", e)
	}
	list, e := s.Schedules(ctx, a, r.ScheduleListing{Limit: 10})
	if e != nil || len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("%+v %v", list, e)
	}
	if page, e := s.Schedules(ctx, a, r.ScheduleListing{Limit: 1, Before: second.ID}); e != nil || len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("%+v %v", page, e)
	}
	got, e := s.Schedule(ctx, a, first.ID)
	if e != nil || got.Name != first.Name || got.Recurrence != first.Recurrence || !got.NextRunAt.Equal(first.NextRunAt) || got.Query.Dataset != first.Query.Dataset {
		t.Fatalf("%+v %v", got, e)
	}
	// Nothing is due yet; then the three active schedules are, and each gets one run; not again while it is open.
	if n, e := s.StartDueRuns(ctx, 10); n != 0 || e != nil {
		t.Fatal("a run before its occurrence", n, e)
	}
	if _, e := s.SetScheduleState(ctx, a, second.ID, r.SchedulePaused); e != nil {
		t.Fatal(e)
	}
	h.makeDue(t)
	if n, e := s.StartDueRuns(ctx, 10); n != 2 || e != nil {
		t.Fatal("the due schedules", n, e)
	}
	h.makeDue(t)
	if n, e := s.StartDueRuns(ctx, 10); n != 0 || e != nil {
		t.Fatal("a second run while one is open", n, e)
	}
	claims, e := s.ClaimRuns(ctx, r.RunLease, 10)
	if e != nil || len(claims) != 2 {
		t.Fatalf("%+v %v", claims, e)
	}
	var mine r.RunClaim
	for _, c := range claims {
		if c.Run.Owner.Identity == a {
			mine = c
		}
		if c.ScheduleState != r.ScheduleActive || c.Run.State != r.RunDue || c.Run.Generation != 1 || c.Run.Delivery != r.DeliveryNone || c.Run.Query.AsOf == "" || c.Run.CreatedAt.IsZero() {
			t.Fatalf("%+v", c)
		}
	}
	if mine.Run.ScheduleID != first.ID {
		t.Fatalf("%+v", mine)
	}
	if again, e := s.ClaimRuns(ctx, r.RunLease, 10); e != nil || len(again) != 0 {
		t.Fatalf("a leased run was claimed again: %+v %v", again, e)
	}
	// The update is fenced by the claim.
	run := mine.Run
	run.State, run.SnapshotJob = r.RunSnapshot, r.NewID()
	stale := mine
	stale.Run.Generation = 0
	staleRun := run
	staleRun.Generation = 0
	if e := s.UpdateRun(ctx, stale, staleRun, 0); !errors.Is(e, r.ErrLease) {
		t.Fatalf("another generation updated the run: %v", e)
	}
	moved := run
	moved.Owner.Identity = b
	if e := s.UpdateRun(ctx, mine, moved, 0); !errors.Is(e, r.ErrInvalid) {
		t.Fatalf("an update moved the run to another identity: %v", e)
	}
	if e := s.UpdateRun(ctx, mine, run, 0); e != nil {
		t.Fatal(e)
	}
	if e := s.UpdateRun(ctx, mine, run, 0); !errors.Is(e, r.ErrLease) {
		t.Fatalf("a released claim updated the run: %v", e)
	}
	// A run whose lease has run out is claimed again, and the earlier claim no longer updates it.
	claims, _ = s.ClaimRuns(ctx, r.RunLease, 10)
	for _, c := range claims {
		if c.Run.ID == run.ID {
			mine = c
		}
	}
	if mine.Run.Generation != 2 || mine.Run.State != r.RunSnapshot || mine.Run.SnapshotJob != run.SnapshotJob {
		t.Fatalf("%+v", mine)
	}
	h.expireLeases(t)
	claims, _ = s.ClaimRuns(ctx, r.RunLease, 10)
	var reclaimed r.RunClaim
	for _, c := range claims {
		if c.Run.ID == run.ID {
			reclaimed = c
		}
	}
	if reclaimed.Run.Generation != 3 {
		t.Fatalf("the expired lease was not claimed again: %+v", claims)
	}
	run = mine.Run
	if e := s.UpdateRun(ctx, mine, run, 0); !errors.Is(e, r.ErrLease) {
		t.Fatalf("a claim whose lease ran out updated the run: %v", e)
	}
	// It ends; it stays claimable while its delivery is pending; then it is closed.
	run = reclaimed.Run
	run.State, run.SnapshotID, run.Delivery, run.FinishedAt = r.RunSucceeded, r.NewID(), r.DeliveryPending, time.Now()
	if e := s.UpdateRun(ctx, reclaimed, run, 0); e != nil {
		t.Fatal(e)
	}
	for _, c := range mustClaim(t, s) {
		if c.Run.ID == run.ID {
			reclaimed = c
		}
	}
	if reclaimed.Run.State != r.RunSucceeded || reclaimed.Run.Delivery != r.DeliveryPending || reclaimed.Run.FinishedAt.IsZero() {
		t.Fatalf("%+v", reclaimed)
	}
	run = reclaimed.Run
	run.Delivery, run.Attempts = r.DeliveryDelivered, 1
	if e := s.UpdateRun(ctx, reclaimed, run, 0); e != nil {
		t.Fatal(e)
	}
	for _, c := range mustClaim(t, s) {
		if c.Run.ID == run.ID {
			t.Fatal("a closed run was claimed")
		}
	}
	runs, e := s.ScheduleRuns(ctx, a, first.ID, r.ScheduleListing{Limit: 10})
	if e != nil || len(runs) != 1 || runs[0].State != r.RunSucceeded || runs[0].Delivery != r.DeliveryDelivered || runs[0].Attempts != 1 || runs[0].SnapshotID != run.SnapshotID || runs[0].SnapshotJob == "" {
		t.Fatalf("%+v %v", runs, e)
	}
	if theirs, e := s.ScheduleRuns(ctx, b, first.ID, r.ScheduleListing{Limit: 10}); e != nil || len(theirs) != 0 {
		t.Fatalf("another identity lists the runs: %+v %v", theirs, e)
	}
	// Its next occurrence is a second run; the schedule paused before it was claimed says so in the claim.
	h.makeDue(t)
	if n, e := s.StartDueRuns(ctx, 10); n != 1 || e != nil {
		t.Fatal("the next occurrence", n, e)
	}
	if _, e := s.SetScheduleState(ctx, a, first.ID, r.SchedulePaused); e != nil {
		t.Fatal(e)
	}
	var paused r.RunClaim
	for _, c := range mustClaim(t, s) {
		if c.Run.ScheduleID == first.ID {
			paused = c
		}
	}
	if paused.ScheduleState != r.SchedulePaused || paused.Run.State != r.RunDue {
		t.Fatalf("%+v", paused)
	}
	if runs, _ := s.ScheduleRuns(ctx, a, first.ID, r.ScheduleListing{Limit: 10}); len(runs) != 2 || !runs[0].DueAt.After(runs[1].DueAt) {
		t.Fatalf("%+v", runs)
	}
	if page, _ := s.ScheduleRuns(ctx, a, first.ID, r.ScheduleListing{Limit: 10, Before: paused.Run.ID}); len(page) != 1 || page[0].ID != run.ID {
		t.Fatalf("%+v", page)
	}
	// States: paused, resumed with a next run in the future, deleted (not found, not listed, not counted).
	v, e := s.SetScheduleState(ctx, a, second.ID, r.ScheduleActive)
	if e != nil || v.State != r.ScheduleActive || v.NextRunAt.IsZero() {
		t.Fatalf("%+v %v", v, e)
	}
	if v, e = s.SetScheduleState(ctx, a, second.ID, r.ScheduleActive); e != nil || v.State != r.ScheduleActive {
		t.Fatalf("the same state again: %+v %v", v, e)
	}
	if v, e = s.SetScheduleState(ctx, a, second.ID, r.ScheduleDeleted); e != nil || v.State != r.ScheduleDeleted || !v.NextRunAt.IsZero() {
		t.Fatalf("%+v %v", v, e)
	}
	if _, e := s.Schedule(ctx, a, second.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatalf("a deleted schedule: %v", e)
	}
	if _, e := s.SetScheduleState(ctx, a, second.ID, r.ScheduleActive); !errors.Is(e, r.ErrNotFound) {
		t.Fatalf("a deleted schedule resumed: %v", e)
	}
	if _, e := s.SetScheduleState(ctx, a, first.ID, "running"); !errors.Is(e, r.ErrInvalid) {
		t.Fatalf("an unknown state: %v", e)
	}
	if list, _ := s.Schedules(ctx, a, r.ScheduleListing{Limit: 10}); len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("%+v", list)
	}
	if _, e := s.CreateSchedule(ctx, scheduleOf(a, "k5"), 2); e != nil {
		t.Fatalf("a deleted schedule still counts: %v", e)
	}
	if _, e := s.CreateSchedule(ctx, scheduleOf(a, "k2"), 2); !errors.Is(e, r.ErrNotFound) {
		t.Fatalf("the key of a deleted schedule: %v", e)
	}
	_ = theirs
	for _, l := range []r.ScheduleListing{{Limit: 0}, {Limit: r.MaxScheduleListing + 1}, {Limit: 1, Before: "x"}} {
		if _, e := s.Schedules(ctx, a, l); !errors.Is(e, r.ErrInvalid) {
			t.Fatalf("%+v: %v", l, e)
		}
	}
	if _, e := s.ClaimRuns(ctx, 0, 1); !errors.Is(e, r.ErrInvalid) {
		t.Fatal("a lease of zero")
	}
}

func mustClaim(t *testing.T, s r.ScheduleStore) []r.RunClaim {
	t.Helper()
	claims, e := s.ClaimRuns(context.Background(), r.RunLease, 100)
	if e != nil {
		t.Fatal(e)
	}
	return claims
}

// concurrentStarts is several schedulers starting the due schedules at once: each due schedule gets exactly one run, and no run is claimed
// by two of them.
func concurrentStarts(t *testing.T, h scheduleHarness, schedules int) {
	ctx := context.Background()
	for n := 0; n < schedules; n++ {
		i := r.Identity{Tenant: "t", Issuer: "i", Subject: "c" + string(rune('a'+n))}
		if _, e := h.store.CreateSchedule(ctx, scheduleOf(i, "c"), 1); e != nil {
			t.Fatal(e)
		}
	}
	h.makeDue(t)
	var mu sync.Mutex
	started, claimed := 0, map[string]int{}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 5; k++ {
				got, e := h.store.StartDueRuns(ctx, 2)
				if e != nil {
					t.Error(e)
					return
				}
				claims, e := h.store.ClaimRuns(ctx, r.RunLease, 3)
				if e != nil {
					t.Error(e)
					return
				}
				mu.Lock()
				started += got
				for _, c := range claims {
					claimed[c.Run.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if started != schedules || len(claimed) != schedules {
		t.Fatalf("%d runs started and %d claimed for %d due schedules", started, len(claimed), schedules)
	}
	for id, n := range claimed {
		if n != 1 {
			t.Fatalf("run %s was claimed %d times", id, n)
		}
	}
}

func TestMemoryScheduleContract(t *testing.T) {
	clock := &retentionClock{t: time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC)}
	harness := func() scheduleHarness {
		// The clock moves a millisecond at each reading, as the database's does between statements, so that two creations are ordered.
		store := memory.New(func() time.Time { clock.Advance(time.Millisecond); return clock.Now() })
		return scheduleHarness{store: store, makeDue: func(*testing.T) { clock.Advance(25 * time.Hour) }, expireLeases: func(*testing.T) { clock.Advance(r.RunLease + time.Second) }}
	}
	t.Run("contract", func(t *testing.T) { runScheduleContract(t, harness()) })
	t.Run("concurrent_schedulers", func(t *testing.T) { concurrentStarts(t, harness(), 6) })
}

func TestPostgresScheduleContract(t *testing.T) {
	dsn := os.Getenv("REPORTING_S01_TEST_DSN")
	if dsn == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required real PostgreSQL DSN missing")
		}
		t.Skip("real PostgreSQL gate not run; use scripts/run_pg16_vm.py or REPORTING_S01_TEST_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := postgres.Open(ctx, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin := fixtureAdmin(t)
	day := 0
	harness := scheduleHarness{store: store,
		// Each call a distinct occurrence in the past, so that a second run is a second occurrence.
		makeDue: func(t *testing.T) {
			day++
			if _, e := admin.Exec(ctx, "UPDATE reporting_s01.schedules SET next_run_at = timestamptz '2026-01-01 08:00:00+00' + $1 * interval '1 day' WHERE state='active'", day); e != nil {
				t.Fatal(e)
			}
		},
		expireLeases: func(t *testing.T) {
			if _, e := admin.Exec(ctx, "UPDATE reporting_s01.schedule_runs SET lease_until = clock_timestamp() - interval '1 second' WHERE lease_until IS NOT NULL"); e != nil {
				t.Fatal(e)
			}
		},
	}
	reset := func(t *testing.T) {
		if err := store.ResetFixture(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("contract", func(t *testing.T) { reset(t); runScheduleContract(t, harness) })
	t.Run("concurrent_schedulers", func(t *testing.T) { reset(t); concurrentStarts(t, harness, 6) })
	t.Run("an_occurrence_that_has_its_run_gets_no_second_one", func(t *testing.T) {
		reset(t)
		i := r.Identity{Tenant: "t", Issuer: "i", Subject: "a"}
		v, e := store.CreateSchedule(ctx, scheduleOf(i, "k"), 5)
		if e != nil {
			t.Fatal(e)
		}
		harness.makeDue(t)
		if n, e := store.StartDueRuns(ctx, 5); n != 1 || e != nil {
			t.Fatal(n, e)
		}
		// The run is closed and the schedule is put back on the same occurrence (as a restore of an older backup could): it moves on, it
		// does not run the occurrence twice and it does not stop the scheduler.
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.schedule_runs SET state='succeeded'"); e != nil {
			t.Fatal(e)
		}
		if _, e := admin.Exec(ctx, "UPDATE reporting_s01.schedules SET next_run_at = timestamptz '2026-01-01 08:00:00+00' + $1 * interval '1 day'", day); e != nil {
			t.Fatal(e)
		}
		if n, e := store.StartDueRuns(ctx, 5); n != 0 || e != nil {
			t.Fatal(n, e)
		}
		if got, _ := store.Schedule(ctx, i, v.ID); !got.NextRunAt.After(time.Now()) {
			t.Fatalf("the schedule did not move on: %v", got.NextRunAt)
		}
	})
	t.Run("a_row_that_does_not_match_its_digest_is_an_integrity_failure", func(t *testing.T) {
		reset(t)
		i := r.Identity{Tenant: "t", Issuer: "i", Subject: "a"}
		v, e := store.CreateSchedule(ctx, scheduleOf(i, "k"), 5)
		if e != nil {
			t.Fatal(e)
		}
		harness.makeDue(t)
		if n, e := store.StartDueRuns(ctx, 5); n != 1 || e != nil {
			t.Fatal(n, e)
		}
		if _, e := admin.Exec(ctx, `UPDATE reporting_s01.schedule_runs SET data = jsonb_set(data, '{Format}', '"pdf"')`); e != nil {
			t.Fatal(e)
		}
		// The run that cannot be read is ended, never handed to a scheduler, and not delivered.
		if claims := mustClaim(t, store); len(claims) != 0 {
			t.Fatalf("%+v", claims)
		}
		runs, e := store.ScheduleRuns(ctx, i, v.ID, r.ScheduleListing{Limit: 5})
		if !errors.Is(e, r.ErrIntegrity) || runs != nil {
			t.Fatalf("a tampered run was listed: %+v %v", runs, e)
		}
		var state, code, delivery string
		if e := admin.QueryRow(ctx, "SELECT state, error_code, delivery FROM reporting_s01.schedule_runs").Scan(&state, &code, &delivery); e != nil || state != "failed" || code != "INTEGRITY_FAILURE" || delivery != "none" {
			t.Fatal(state, code, delivery, e)
		}
		if _, e := admin.Exec(ctx, `UPDATE reporting_s01.schedules SET data = jsonb_set(data, '{Name}', '"another"')`); e != nil {
			t.Fatal(e)
		}
		if _, e := store.Schedule(ctx, i, v.ID); !errors.Is(e, r.ErrIntegrity) {
			t.Fatalf("a tampered schedule: %v", e)
		}
	})
}
