package authoritytest

import (
	"fmt"
	r "idp.local/reporting/reporting"
	"strings"
	"sync"
	"testing"
)

// workers is how many goroutines each concurrent scenario starts, and rounds how many jobs a
// scenario races on: a race that needs two calls to interleave within a millisecond is caught by
// repetition, not by one attempt.
const (
	workers = 8
	rounds  = 12
)

// together runs f(0..n-1) on n goroutines released at the same moment and waits for all of them.
func together(n int, f func(i int)) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f(i)
		}()
	}
	close(start)
	wg.Wait()
}

// RunConcurrent runs the scenarios that call one simulator from several goroutines at once: the
// numbers stay distinct and consecutive, one job is one reservation, and a job's confirmations
// and voids settle on one outcome. Run it with the race detector.
func RunConcurrent(t *testing.T, newEnv NewEnv) {
	t.Helper()
	for _, s := range concurrentScenarios {
		t.Run(s.name, func(t *testing.T) { s.run(t, newEnv) })
	}
}

var concurrentScenarios = []ruleScenario{
	{"different_jobs_get_distinct_consecutive_numbers", func(t *testing.T, newEnv NewEnv) {
		for round := 0; round < 3; round++ {
			g := NewRig(t, newEnv, Finance())
			got := make([]r.IssueReservation, workers)
			errs := make([]error, workers)
			together(workers, func(i int) { got[i], errs[i] = g.Prepare(i+1, 0) })
			seen := map[string]bool{}
			for i := range got {
				if errs[i] != nil {
					t.Fatalf("round %d job %d: %v", round, i+1, errs[i])
				}
				if rec := g.Record(i + 1); rec.Number != got[i].Number || rec.Ref != got[i].Ref {
					t.Fatalf("round %d job %d answered %+v but the source holds %+v", round, i+1, got[i], rec)
				}
				seen[got[i].Number] = true
			}
			for k := 1; k <= workers; k++ {
				if want := fmt.Sprintf("AR-00000%d", k); !seen[want] {
					t.Fatalf("round %d: no job got %s: %v", round, want, seen)
				}
			}
			if len(seen) != workers {
				t.Fatalf("round %d: %d distinct numbers for %d jobs: %v", round, len(seen), workers, seen)
			}
		}
	}},
	{"calls_for_one_job_make_one_reservation", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for round := 1; round <= rounds; round++ {
			got := make([]r.IssueReservation, workers)
			errs := make([]error, workers)
			together(workers, func(i int) { got[i], errs[i] = g.Prepare(round, 0) })
			for i := range got {
				if errs[i] != nil {
					t.Fatalf("round %d call %d: %v", round, i, errs[i])
				}
				sameReservation(t, got[i], got[0])
			}
			// one job, one number, and none used up by the calls that found it reserved
			if want := fmt.Sprintf("AR-%06d", round); got[0].Number != want {
				t.Fatalf("round %d: the job got %s, want %s", round, got[0].Number, want)
			}
		}
	}},
	{"confirmations_of_one_file_return_one_receipt", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for round := 1; round <= rounds; round++ {
			res := g.Reserve(round)
			got := make([]r.IssueReceipt, workers)
			errs := make([]error, workers)
			together(workers, func(i int) { got[i], errs[i] = g.Src.ConfirmIssue(ctx, g.SC, g.Confirmation(res)) })
			for i := range got {
				if errs[i] != nil {
					t.Fatalf("round %d call %d: %v", round, i, errs[i])
				}
				if got[i].Ref != got[0].Ref || !got[i].ConfirmedAt.Equal(got[0].ConfirmedAt) {
					t.Fatalf("round %d: receipts differ: %+v and %+v", round, got[0], got[i])
				}
			}
			if rec := g.Record(round); rec.State != "confirmed" || rec.ConfirmationRef != got[0].Ref {
				t.Fatalf("round %d: %+v", round, rec)
			}
		}
	}},
	{"confirmations_of_two_files_have_one_winner", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for round := 1; round <= rounds; round++ {
			res := g.Reserve(round)
			files := [2]r.IssueConfirmation{g.Confirmation(res), g.Confirmation(res)}
			files[1].SHA256 = strings.Repeat("c", 64)
			got := make([]r.IssueReceipt, workers)
			errs := make([]error, workers)
			together(workers, func(i int) { got[i], errs[i] = g.Src.ConfirmIssue(ctx, g.SC, files[i%2]) })
			rec := g.Record(round)
			if rec.State != "confirmed" {
				t.Fatalf("round %d: %+v", round, rec)
			}
			for i := range got {
				if files[i%2].SHA256 != rec.SHA256 {
					is(t, errs[i], r.ErrConflict, fmt.Sprintf("round %d call %d with the losing file", round, i))
					continue
				}
				if errs[i] != nil || got[i].Ref != rec.ConfirmationRef || !got[i].ConfirmedAt.Equal(rec.ConfirmedAt) {
					t.Fatalf("round %d call %d with the winning file: %+v %v, the source holds %+v", round, i, got[i], errs[i], rec)
				}
			}
		}
	}},
	// A void and a PrepareDocument that race on a job the source holds nothing for end with the job
	// void: either the void came first and is a tombstone the reservation conflicts with, or the
	// reservation came first and the void voids it. Never a live reservation of a voided job.
	{"a_void_and_prepares_racing_on_an_unknown_job_never_leave_a_live_reservation", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		taken := 0 // the numbers given out so far
		for round := 1; round <= rounds; round++ {
			got := make([]r.IssueReservation, workers)
			errs := make([]error, workers)
			together(workers, func(i int) {
				if i%2 == 0 {
					got[i], errs[i] = g.Prepare(round, 0)
				} else {
					errs[i] = g.Src.VoidIssue(ctx, g.SC, Job(round), "EXECUTION_FAILED")
				}
			})
			var reserved *r.IssueReservation
			for i := range got {
				switch {
				case i%2 == 1:
					if errs[i] != nil {
						t.Fatalf("round %d: void %d: %v", round, i, errs[i])
					}
				case errs[i] == nil:
					if reserved != nil {
						sameReservation(t, got[i], *reserved)
					}
					reserved = &got[i]
				default:
					is(t, errs[i], r.ErrConflict, fmt.Sprintf("round %d: prepare %d after the void", round, i))
				}
			}
			rec := g.Record(round)
			if rec.State != "void" || rec.Reason != "EXECUTION_FAILED" {
				t.Fatalf("round %d: the job is not void: %+v", round, rec)
			}
			if reserved != nil {
				taken++
				if rec.Tombstone() || rec.Ref != reserved.Ref || rec.Number != reserved.Number {
					t.Fatalf("round %d: the void record %+v is not the reservation %+v", round, rec, *reserved)
				}
			} else if !rec.Tombstone() {
				t.Fatalf("round %d: no reservation was made, yet the record holds one: %+v", round, rec)
			}
			// whatever the race, the job id cannot be reserved or confirmed any more
			_, e := g.Prepare(round, 0)
			is(t, e, r.ErrConflict, fmt.Sprintf("round %d: prepare after the race", round))
			c := g.Confirmation(r.IssueReservation{JobID: Job(round), Ref: rec.Ref})
			_, e = g.Src.ConfirmIssue(ctx, g.SC, c)
			is(t, e, r.ErrConflict, fmt.Sprintf("round %d: confirm after the race", round))
			// the conflicts and the tombstones took no number: the next job gets the next one
			probe := g.Reserve(1000 + round)
			taken++
			if want := fmt.Sprintf("AR-%06d", taken); probe.Number != want {
				t.Fatalf("round %d: the next job got %s, want %s", round, probe.Number, want)
			}
		}
	}},
	{"voids_of_one_unknown_job_record_one_tombstone", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for round := 1; round <= rounds; round++ {
			errs := make([]error, workers)
			together(workers, func(i int) { errs[i] = g.Src.VoidIssue(ctx, g.SC, Job(round), fmt.Sprintf("REASON_%d", i)) })
			for i, e := range errs {
				if e != nil {
					t.Fatalf("round %d: void %d: %v", round, i, e)
				}
			}
			rec := g.Holds(round, "void", "")
			if !strings.HasPrefix(rec.Reason, "REASON_") {
				t.Fatalf("round %d: %+v", round, rec)
			}
			// one of the voids was first and its reason stays
			if e := g.Src.VoidIssue(ctx, g.SC, Job(round), "LATE"); e != nil {
				t.Fatal(e)
			}
			if again := g.Record(round); again.Reason != rec.Reason || !again.VoidedAt.Equal(rec.VoidedAt) {
				t.Fatalf("round %d: the tombstone changed: %+v then %+v", round, rec, again)
			}
		}
	}},
	{"confirmations_and_voids_of_one_job_settle_on_void", func(t *testing.T, newEnv NewEnv) {
		g := NewRig(t, newEnv, Finance())
		for round := 1; round <= rounds; round++ {
			res := g.Reserve(round)
			got := make([]r.IssueReceipt, workers)
			errs := make([]error, workers)
			together(workers, func(i int) {
				if i%2 == 0 {
					got[i], errs[i] = g.Src.ConfirmIssue(ctx, g.SC, g.Confirmation(res))
				} else {
					errs[i] = g.Src.VoidIssue(ctx, g.SC, Job(round), "EXECUTION_FAILED")
				}
			})
			// a void always applies, so the job ends void; a confirmation either came first or conflicts
			rec := g.Holds(round, "void", fmt.Sprintf("AR-%06d", round))
			confirmed := ""
			for i := range got {
				switch {
				case i%2 == 1 && errs[i] != nil:
					t.Fatalf("round %d: void %d: %v", round, i, errs[i])
				case i%2 == 0 && errs[i] == nil:
					if confirmed == "" {
						confirmed = got[i].Ref
					}
					if got[i].Ref != confirmed {
						t.Fatalf("round %d: two confirmations with different receipts", round)
					}
				case i%2 == 0:
					is(t, errs[i], r.ErrConflict, fmt.Sprintf("round %d: confirmation %d after the void", round, i))
				}
			}
			if rec.ConfirmationRef != confirmed || (confirmed == "") != (rec.SHA256 == "") {
				t.Fatalf("round %d: the void record %+v does not match the receipt %q", round, rec, confirmed)
			}
		}
	}},
}
