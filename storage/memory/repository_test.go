package memory

import (
	"context"
	"errors"
	"fmt"
	r "idp.local/reporting/reporting"
	"testing"
	"time"
)

func snapshotJob(key string, expires time.Time) r.Job {
	owner := r.Owner{Identity: r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}, Integration: "i"}
	q := r.QuerySpec{SchemaVersion: "1.0", Integration: "i", Dataset: r.Ref{ID: "d", Version: "1"}, Companies: []string{"c"}, Page: r.PageSpec{Limit: 10}}
	return r.Job{ID: r.NewID(), Owner: owner, Kind: "snapshot", State: "queued", Key: key, RequestHash: r.Digest(r.Normalize(q)), Query: q,
		Prepared: r.PreparedRead{Context: r.SourceContext{Owner: owner, SourceTenant: "st"}, QueryHash: r.QueryHash(q), ExpiresAt: expires}}
}

// A value the store cannot copy (JSON has no year 10000) is an error, never a panic: a worker that dies
// inside the store leaves its job running. The store goes on working.
func TestStoreRefusesAValueItCannotCopyWithoutPanicking(t *testing.T) {
	ctx := context.Background()
	s := New(time.Now)
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("the store panicked: %v", p)
		}
	}()
	if _, e := s.Admit(ctx, snapshotJob("far", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))); e == nil || errors.Is(e, r.ErrConflict) {
		t.Fatalf("a job whose time JSON cannot hold was admitted or refused as another thing: %v", e)
	}
	if c, e := s.Claim(ctx, time.Minute); c != nil || e != nil {
		t.Fatalf("the refused job is in the store: %v %v", c, e)
	}
	good := snapshotJob("good", time.Now().Add(time.Minute))
	if _, e := s.Admit(ctx, good); e != nil {
		t.Fatal(e)
	}
	if c, e := s.Claim(ctx, time.Minute); e != nil || c == nil || c.Job.ID != good.ID {
		t.Fatal(c, e)
	}
}

// A job the store cannot copy as it is stored is what the durable store cannot decode (corrupt, or written by a binary that knows more):
// it is failed at its claim, terminally, with the code of its kind and the generation it had (ISSUE_UNRECONCILED only for an issue job
// claimed before, which may have reserved a number at the product), and Claim goes on to the next job. It does not block the jobs
// behind it, and it is not claimed again when leases run out.
func TestUnreadableJobIsFailedAtItsClaimAndNeverClaimedAgain(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		kind  string
		prior int64
		code  string
	}{
		{"snapshot", 0, "INTEGRITY_FAILURE"},
		{"snapshot", 1, "INTEGRITY_FAILURE"},
		{"issue", 0, "INTEGRITY_FAILURE"},
		{"issue", 1, "ISSUE_UNRECONCILED"},
	} {
		t.Run(fmt.Sprintf("%s_generation_%d", c.kind, c.prior), func(t *testing.T) {
			now := time.Now()
			s := New(func() time.Time { return now })
			bad, first, second := snapshotJob("bad", now.Add(time.Hour)), snapshotJob("good-1", now.Add(time.Hour)), snapshotJob("good-2", now.Add(time.Hour))
			for _, j := range []r.Job{bad, first, second} {
				if _, e := s.Admit(ctx, j); e != nil {
					t.Fatal(e)
				}
			}
			if c.prior > 0 {
				// Claimed before, its lease run out: claimable again.
				claim, e := s.Claim(ctx, time.Second)
				if e != nil || claim == nil || claim.Job.ID != bad.ID || claim.Job.Generation != c.prior {
					t.Fatal(claim, e)
				}
				now = now.Add(2 * time.Second)
			}
			// The stored job can no longer be copied (JSON has no year 10000); its kind and generation are its own fields.
			s.mu.Lock()
			stored := s.jobs[bad.ID]
			stored.Kind = c.kind
			stored.Prepared.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			s.jobs[bad.ID] = stored
			s.mu.Unlock()
			// The first claim is the unreadable job's: it is failed, and the claim is the next job's.
			claim, e := s.Claim(ctx, time.Minute)
			if e != nil || claim == nil || claim.Job.ID != first.ID || claim.Job.Generation != 1 {
				t.Fatalf("the job behind the unreadable one was not claimed: %v %v", claim, e)
			}
			failed := func() r.Job {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.jobs[bad.ID]
			}
			if j := failed(); j.State != "failed" || j.ErrorCode != c.code || j.Generation != c.prior+1 || !j.LeaseUntil.IsZero() {
				t.Fatalf("the unreadable job: %s %s generation %d lease %v", j.State, j.ErrorCode, j.Generation, j.LeaseUntil)
			}
			if claim, e = s.Claim(ctx, time.Minute); e != nil || claim == nil || claim.Job.ID != second.ID {
				t.Fatalf("%v %v", claim, e)
			}
			// Never claimed again, however often the leases run out; the others are, each time, at the next generation.
			for round := int64(2); round < 5; round++ {
				now = now.Add(2 * time.Minute)
				seen := map[string]int64{}
				for {
					claim, e := s.Claim(ctx, time.Minute)
					if e != nil {
						t.Fatal(e)
					}
					if claim == nil {
						break
					}
					seen[claim.Job.ID] = claim.Job.Generation
				}
				if len(seen) != 2 || seen[first.ID] != round || seen[second.ID] != round {
					t.Fatalf("round %d claimed %v", round, seen)
				}
				if j := failed(); j.State != "failed" || j.ErrorCode != c.code || j.Generation != c.prior+1 {
					t.Fatalf("the unreadable job changed: %s %s generation %d", j.State, j.ErrorCode, j.Generation)
				}
			}
		})
	}
	// Alone in the queue, it leaves nothing to claim: nil, as for an empty queue.
	s := New(time.Now)
	bad := snapshotJob("bad", time.Now().Add(time.Hour))
	if _, e := s.Admit(ctx, bad); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	stored := s.jobs[bad.ID]
	stored.Prepared.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	s.jobs[bad.ID] = stored
	s.mu.Unlock()
	if claim, e := s.Claim(ctx, time.Minute); claim != nil || e != nil {
		t.Fatalf("%v %v", claim, e)
	}
	if j := s.jobs[bad.ID]; j.State != "failed" || j.ErrorCode != "INTEGRITY_FAILURE" {
		t.Fatalf("%s %s", j.State, j.ErrorCode)
	}
}

// A lease the store's own clock cannot give (beyond the years JSON holds) is not the job's fault: the job is not failed.
func TestAClaimTheClockCannotLeaseLeavesTheJobQueued(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := New(func() time.Time { return now })
	j := snapshotJob("one", now.Add(time.Hour))
	if _, e := s.Admit(ctx, j); e != nil {
		t.Fatal(e)
	}
	now = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if claim, e := s.Claim(ctx, time.Minute); claim != nil || e == nil {
		t.Fatalf("%v %v", claim, e)
	}
	if got := s.jobs[j.ID]; got.State != "queued" || got.Generation != 0 || got.ErrorCode != "" {
		t.Fatalf("%s %s generation %d", got.State, got.ErrorCode, got.Generation)
	}
}
