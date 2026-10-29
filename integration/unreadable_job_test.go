package integration_test

import (
	"context"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"testing"
	"time"
)

// durableUnreadableJobs is a subtest of TestDurablePostgresContract (it needs the fixture databases): a job whose stored payload cannot
// be decoded is failed in the transaction of its claim, terminally, with the code of r.UnreadableJobCode, taken from the plain columns
// (kind, generation) and never from the payload; it is not left running for the lease to run out and the next claim to take it again;
// and the jobs behind it are claimed, by the same Claim and by the worker.
func durableUnreadableJobs(t *testing.T) {
	ctx := context.Background()
	const digestOf = `encode(sha256(convert_to(data::text,'UTF8')),'hex')`
	corruptions := []struct {
		name string
		sql  []string
	}{
		{"payload changed, digest stale", []string{`UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{Key}','"altered"') WHERE id=$1`}},
		{"digest changed", []string{`UPDATE reporting_s01.jobs SET data_sha256=repeat('0',64) WHERE id=$1`}},
		// What a binary that knows more writes and this one cannot read: consistent with its own digest, naming a kind this one does not know.
		{"written by a binary this one cannot read", []string{`UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{Kind}','"snapshot-v2"') WHERE id=$1`, `UPDATE reporting_s01.jobs SET data_sha256=` + digestOf + ` WHERE id=$1`}},
	}
	for _, c := range []struct {
		kind  string
		prior int64 // the generation the job has when it is corrupted: how often it was claimed before
		code  string
	}{
		{"snapshot", 0, "INTEGRITY_FAILURE"},
		{"snapshot", 1, "INTEGRITY_FAILURE"},
		{"issue", 0, "INTEGRITY_FAILURE"},
		{"issue", 1, "ISSUE_UNRECONCILED"},
	} {
		for _, corruption := range corruptions {
			t.Run(fmt.Sprintf("%s_generation_%d_%s", c.kind, c.prior, corruption.name), func(t *testing.T) {
				v := newDurable(t)
				s := issueDurableService(t, v, &stampIssuer{})
				env := durableEnv{s: s, repo: v.repo, source: v.source, p: v.p}
				snap := durableFreeze(t, env, contractsim.FinancialQuery(), "snap")
				var bad r.JobView
				var e error
				if c.kind == "issue" {
					bad, e = s.SubmitIssue(ctx, v.p, issueInput(snap.SnapshotID, 0), "bad")
				} else {
					bad, e = s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "bad")
				}
				if e != nil {
					t.Fatal(e)
				}
				// Two jobs behind it, queued later.
				var good []string
				for _, key := range []string{"good-1", "good-2"} {
					j, e := s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), key)
					if e != nil {
						t.Fatal(e)
					}
					good = append(good, j.ID)
				}
				admin := fixtureAdmin(t)
				if c.prior > 0 {
					// Claimed before, its lease run out: claimable again, at the next generation.
					first, e := v.repo.Claim(ctx, time.Second)
					if e != nil || first == nil || first.Job.ID != bad.ID || first.Job.Generation != c.prior {
						t.Fatal(first, e)
					}
					if _, e = admin.Exec(ctx, `UPDATE reporting_s01.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, bad.ID); e != nil {
						t.Fatal(e)
					}
				}
				for _, statement := range corruption.sql {
					if _, e = admin.Exec(ctx, statement, bad.ID); e != nil {
						t.Fatal(e)
					}
				}
				// The worker's first claim is the corrupt job's: it is failed, and the worker goes on to the next job.
				for _, id := range good {
					if ran, e := s.RunOne(ctx); !ran || e != nil {
						t.Fatalf("the worker did not reach %s: %v %v", id, ran, e)
					}
					if j, e := s.Job(ctx, v.p, id); e != nil || j.State != "succeeded" {
						t.Fatal(j.State, e)
					}
				}
				state := func() (string, string, int64, bool) {
					var state, code string
					var generation int64
					var released bool
					if e := admin.QueryRow(ctx, `SELECT state,error_code,generation,lease_until IS NULL FROM reporting_s01.jobs WHERE id=$1`, bad.ID).Scan(&state, &code, &generation, &released); e != nil {
						t.Fatal(e)
					}
					return state, code, generation, released
				}
				if st, code, generation, released := state(); st != "failed" || code != c.code || generation != c.prior+1 || !released {
					t.Fatalf("the unreadable job: state %s, code %s, generation %d, lease released %v", st, code, generation, released)
				}
				// Never claimed again: whatever the leases, nothing is claimable, and the row is as it was left.
				for range 3 {
					if _, e = admin.Exec(ctx, `UPDATE reporting_s01.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE state='running'`); e != nil {
						t.Fatal(e)
					}
					if claim, e := v.repo.Claim(ctx, time.Second); claim != nil || e != nil {
						t.Fatalf("a job is claimed again: %v %v", claim, e)
					}
					if ran, e := s.RunOne(ctx); ran || e != nil {
						t.Fatal(ran, e)
					}
				}
				if st, code, generation, _ := state(); st != "failed" || code != c.code || generation != c.prior+1 {
					t.Fatalf("the unreadable job changed: state %s, code %s, generation %d", st, code, generation)
				}
				// Its owner reads it as the integrity failure it is.
				_, e = v.repo.Job(ctx, v.p.Identity, bad.ID)
				mustError(t, e, r.ErrIntegrity)
			})
		}
	}
	// Alone in the queue, an unreadable job leaves nothing to claim: Claim answers nil, as it does for an empty queue.
	t.Run("alone", func(t *testing.T) {
		v := newDurable(t)
		bad, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "bad")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = fixtureAdmin(t).Exec(ctx, `UPDATE reporting_s01.jobs SET data_sha256=repeat('0',64) WHERE id=$1`, bad.ID); e != nil {
			t.Fatal(e)
		}
		if claim, e := v.repo.Claim(ctx, time.Minute); claim != nil || e != nil {
			t.Fatalf("%v %v", claim, e)
		}
		var state, code string
		if e = fixtureAdmin(t).QueryRow(ctx, `SELECT state,error_code FROM reporting_s01.jobs WHERE id=$1`, bad.ID).Scan(&state, &code); e != nil || state != "failed" || code != "INTEGRITY_FAILURE" {
			t.Fatal(state, code, e)
		}
	})
}
