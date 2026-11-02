package integration_test

import (
	"context"
	"errors"
	"fmt"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"sync"
	"testing"
	"time"
)

// The quota qualification that only PostgreSQL has (docs/S04D_SCOPE.md D2, D4): the contract of both stores, then the identity's advisory lock
// under real concurrency. The subtests run inside TestDurablePostgresContract.
func durableQuotas(t *testing.T) {
	t.Run("quota_contract", durableQuotaContract)
	t.Run("parallel_admissions_of_one_identity_admit_exactly_the_limit", func(t *testing.T) {
		const parallel = 32
		for _, quota := range []string{r.QuotaJobsInFlight, r.QuotaAdmissionsPerHour} {
			for _, limit := range []int{1, 5} {
				w := newQuotaDurable(t)
				q := r.Quotas{}
				if quota == r.QuotaJobsInFlight {
					q.JobsInFlight = limit
				} else {
					q.AdmissionsPerHour = limit
				}
				s := w.svc(q)
				start := make(chan struct{})
				var wg sync.WaitGroup
				answers := make([]error, parallel)
				for i := range answers {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						_, answers[i] = w.submit(s, w.p, fmt.Sprintf("parallel-%d", i))
					}()
				}
				close(start)
				wg.Wait()
				admitted, refusedN := 0, 0
				for _, e := range answers {
					switch {
					case e == nil:
						admitted++
					case errors.Is(e, r.ErrQuota) && r.QuotaOf(e) == quota:
						refusedN++
					default:
						t.Fatalf("%s %d: %v", quota, limit, e)
					}
				}
				if admitted != limit || refusedN != parallel-limit {
					t.Fatalf("%s limit %d: %d admitted and %d refused of %d parallel admissions", quota, limit, admitted, refusedN, parallel)
				}
				var stored int
				if e := fixtureAdmin(t).QueryRow(context.Background(), `SELECT count(*) FROM reporting_s01.jobs`).Scan(&stored); e != nil || stored != limit {
					t.Fatalf("%d jobs stored, want %d: %v", stored, limit, e)
				}
			}
		}
	})
	t.Run("the_lock_is_the_identitys_alone_and_ends_with_the_transaction", func(t *testing.T) {
		ctx := context.Background()
		w := newQuotaDurable(t)
		admin := fixtureAdmin(t)
		s := w.svc(r.Quotas{})
		a, b := w.p, w.other("someone-else", "")
		advisory := func(granted bool) (n int) {
			t.Helper()
			e := admin.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND granted=$1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`, granted).Scan(&n)
			if e != nil {
				t.Fatal(e)
			}
			return n
		}
		eventually := func(label string, ok func() bool) {
			t.Helper()
			for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
				if ok() {
					return
				}
			}
			t.Fatal("not reached: " + label)
		}
		// A's lock is held by a transaction of ours, as a concurrent admission of A would hold it.
		holder, e := admin.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer holder.Rollback(ctx)
		lockSQL, key := postgres.AdmissionLockForTests(a.Identity)
		if _, e = holder.Exec(ctx, lockSQL, key); e != nil {
			t.Fatal(e)
		}
		if advisory(true) != 1 {
			t.Fatal("the holder's lock is not the one advisory lock")
		}
		blocked := make(chan error, 1)
		go func() { _, e := w.submit(s, a, "a-1"); blocked <- e }()
		eventually("A's admission waits on A's lock", func() bool { return advisory(false) == 1 })
		// B's admission passes while A waits: the lock is keyed on the identity.
		done := make(chan error, 1)
		go func() { _, e := w.submit(s, b, "b-1"); done <- e }()
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("another identity's admission waited for A's lock")
		}
		select {
		case e := <-blocked:
			t.Fatalf("A's admission did not wait: %v", e)
		default:
		}
		// The key is the identity's: the same subject under another issuer, and another subject, are other keys.
		if _, other := postgres.AdmissionLockForTests(b.Identity); other == key {
			t.Fatal("two identities share a lock key")
		}
		if e = holder.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		select {
		case e := <-blocked:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("A's admission did not go on when its lock was released")
		}
		// The lock ended with the transaction of each admission, committed or refused: none is left.
		eventually("no advisory lock left", func() bool { return advisory(true) == 0 && advisory(false) == 0 })
		refusing := w.svc(r.Quotas{JobsInFlight: 1})
		if _, e = w.submit(refusing, a, "a-2"); !errors.Is(e, r.ErrQuota) {
			t.Fatal(e)
		}
		if n := advisory(true) + advisory(false); n != 0 {
			t.Fatalf("%d advisory locks left after a refusal", n)
		}
	})
}
