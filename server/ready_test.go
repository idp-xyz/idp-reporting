package server

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock the test moves.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now()} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// /readyz is 200 only while the store answers, its schema and its role are right and, when configured, the object store answered; otherwise
// 503 with one reason and nothing else in the body; and it recovers when the cause goes. /healthz stays 200 through all of it.
func TestReadinessSaysWhyAndRecovers(t *testing.T) {
	w := newWorld(t)
	store := NewMemoryStoreForTests(time.Now)
	s := startWorld(t, w, WithStoreForTests(store))
	check := func(wantStatus int, wantBody string) {
		t.Helper()
		status, h, body := s.do(t, "GET", "/readyz", "", nil)
		if status != wantStatus || strings.TrimSpace(string(body)) != wantBody {
			t.Fatalf("readyz: %d %s, want %d %s", status, body, wantStatus, wantBody)
		}
		if h.Get("Cache-Control") != "no-store" || h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("headers: %v", h)
		}
		if hs, _, hb := s.do(t, "GET", "/healthz", "", nil); hs != 200 || strings.TrimSpace(string(hb)) != `{"status":"ok"}` {
			t.Fatalf("healthz: %d %s", hs, hb)
		}
	}
	check(200, `{"status":"ready"}`)
	for _, reason := range []string{"store", "schema", "roles"} {
		store.SetNotReady(reason)
		check(503, `{"status":"not_ready","reason":"`+reason+`"}`)
		store.SetNotReady("")
		check(200, `{"status":"ready"}`)
	}
	// A reason the store invents is the store's, not a word of the body.
	store.SetNotReady("<script>")
	check(503, `{"status":"not_ready","reason":"store"}`)
	store.SetNotReady("")
	check(200, `{"status":"ready"}`)
}

func TestReadinessFollowsTheObjectStoreThroughItsCache(t *testing.T) {
	w := newWorld(t)
	fake := newFakeS3(t)
	w.cfg.ObjectStore = &ObjectStoreConfig{Endpoint: fake.URL, Bucket: "bucket", LoopbackHTTP: true}
	clock := newFakeClock()
	s := startWorld(t, w, WithClockForTests(clock.Now))
	ready := func() (int, string) {
		status, _, body := s.do(t, "GET", "/readyz", "", nil)
		return status, strings.TrimSpace(string(body))
	}
	startup := fake.heads.Load() // the start-up check
	if st, body := ready(); st != 200 {
		t.Fatal(st, body)
	}
	if fake.heads.Load() != startup+1 {
		t.Fatalf("the first probe checked %d times", fake.heads.Load()-startup)
	}
	// Probes within the ten seconds ask the bucket nothing: the load balancer's probe is not a request to the bucket.
	for i := 0; i < 5; i++ {
		ready()
	}
	if fake.heads.Load() != startup+1 {
		t.Fatalf("probes reached the bucket: %d", fake.heads.Load()-startup)
	}
	fake.down.Store(true)
	if st, _ := ready(); st != 200 {
		t.Fatal("the answer changed inside the cache window")
	}
	clock.Advance(9 * time.Second)
	if st, _ := ready(); st != 200 {
		t.Fatal("the answer changed before ten seconds")
	}
	clock.Advance(2 * time.Second)
	if st, body := ready(); st != 503 || body != `{"status":"not_ready","reason":"object_store"}` {
		t.Fatalf("%d %s", st, body)
	}
	// A failure is cached for the same ten seconds, and a recovery is seen after them.
	fake.down.Store(false)
	if st, _ := ready(); st != 503 {
		t.Fatal("the failure was not cached")
	}
	clock.Advance(11 * time.Second)
	if st, body := ready(); st != 200 {
		t.Fatalf("%d %s", st, body)
	}
	// The store's reason comes before the object store's.
}

// Concurrent probes share the check that is running instead of each making their own: a flood of /readyz is one query at a time.
func TestReadinessProbesDoNotQueueUpChecks(t *testing.T) {
	var v verdict
	var calls, inside int
	var mu sync.Mutex
	maxInside := 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.get(time.Now, 0, func() string {
				mu.Lock()
				calls++
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()
				mu.Lock()
				inside--
				mu.Unlock()
				return ""
			})
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("%d checks ran at once", maxInside)
	}
}
