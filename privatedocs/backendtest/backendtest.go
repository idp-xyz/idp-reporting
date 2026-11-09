// Package backendtest holds the contract every privatedocs.Backend keeps (S11-A): the file, memory and database backends run it.
package backendtest

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

// Options says what a backend promises beyond the common contract.
type Options struct {
	// Serializes: two changes of one owner's document never interleave even without the Store's lock (memory, database). The file
	// backend is one process's and relies on the Store's lock.
	Serializes bool
	// Second opens another backend over the same storage (a second server on the same database); nil when there is no such thing.
	Second func(t *testing.T) privatedocs.Backend
}

// Run holds the backend that open makes to the contract. open is called once per subtest and must answer an empty storage.
func Run(t *testing.T, open func(t *testing.T) privatedocs.Backend, o Options) {
	a := r.Identity{Tenant: "t", Issuer: "i", Subject: "a"}
	b := r.Identity{Tenant: "t", Issuer: "i", Subject: "b"}
	t.Run("load_change_rollback_and_owners", func(t *testing.T) {
		be := open(t)
		if data, e := be.Load(a); e != nil || data != nil {
			t.Fatalf("an owner without a document: %q %v", data, e)
		}
		commit := func(owner r.Identity, data []byte) {
			t.Helper()
			tx, e := be.Begin(owner)
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Store(data); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			tx.Rollback() // after Commit: nothing
		}
		commit(a, []byte(`{"x":1}`))
		if data, e := be.Load(a); e != nil || string(data) != `{"x":1}` {
			t.Fatalf("%q %v", data, e)
		}
		tx, e := be.Begin(a)
		if e != nil {
			t.Fatal(e)
		}
		if seen, _ := tx.Load(); string(seen) != `{"x":1}` {
			t.Fatalf("the change found %q", seen)
		}
		if e = tx.Store([]byte(`{"x":2}`)); e != nil {
			t.Fatal(e)
		}
		if data, _ := be.Load(a); string(data) != `{"x":1}` {
			t.Fatalf("an uncommitted change is visible: %q", data)
		}
		tx.Rollback()
		if data, _ := be.Load(a); string(data) != `{"x":1}` {
			t.Fatalf("a rolled back change was kept: %q", data)
		}
		if data, e := be.Load(b); e != nil || data != nil {
			t.Fatalf("another owner sees %q %v", data, e)
		}
		// A change that is begun and rolled back leaves an owner without a document as it was.
		tx, _ = be.Begin(b)
		tx.Rollback()
		if data, e := be.Load(b); e != nil || data != nil {
			t.Fatalf("an empty change left %q %v", data, e)
		}
		// The bound of a document.
		tx, _ = be.Begin(b)
		if e := tx.Store(bytes.Repeat([]byte("x"), privatedocs.MaxDocument+1)); !errors.Is(e, r.ErrBudget) {
			t.Errorf("a document over the bound: %v", e)
		}
		tx.Rollback()
	})
	t.Run("a_store_over_the_backend", func(t *testing.T) {
		be := open(t)
		s, e := privatedocs.OpenBackend(be, 4, func(string) error { return nil }, func(string) string { return "b" })
		if e != nil {
			t.Fatal(e)
		}
		first, e := s.Save(a, "", 0, "one")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Save(a, first.ID, 1, "two"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Save(a, first.ID, 1, "stale"); !errors.Is(e, r.ErrConflict) {
			t.Fatalf("a stale version: %v", e)
		}
		if _, e = s.Publish(a, first.ID, "publish", 2, 0); e != nil {
			t.Fatal(e)
		}
		if _, e = s.SetCatalog(a, first.ID, 0, privatedocs.Catalog{Domain: "通用业务", Collections: []string{}}); e != nil {
			t.Fatal(e)
		}
		if _, recorded, e := s.Opened(a, first.ID, 1, time.Now()); e != nil || !recorded {
			t.Fatal(recorded, e)
		}
		// Another store over the same storage (a restart, or a second server) reads all of it.
		var again *privatedocs.Store[string]
		if o.Second != nil {
			again, e = privatedocs.OpenBackend(o.Second(t), 4, func(string) error { return nil }, func(string) string { return "b" })
		} else {
			again, e = privatedocs.OpenBackend(be, 4, func(string) error { return nil }, func(string) string { return "b" })
		}
		if e != nil {
			t.Fatal(e)
		}
		pub, e := again.Published(a, first.ID)
		c, ce := again.Catalog(a, first.ID)
		if e != nil || pub.Layout != "two" || ce != nil || c.Domain != "通用业务" || c.Revision != 1 || c.LastOpened == "" {
			t.Fatalf("%+v %v %+v %v", pub, e, c, ce)
		}
		if list, e := again.List(b); e != nil || len(list) != 0 {
			t.Fatalf("another owner's list: %v %v", list, e)
		}
	})
	if !o.Serializes {
		return
	}
	t.Run("changes_of_one_owner_never_interleave", func(t *testing.T) {
		first := open(t)
		second := first
		if o.Second != nil {
			second = o.Second(t)
		}
		const each = 10
		var wg sync.WaitGroup
		errs := make(chan error, 2*each)
		for _, be := range []privatedocs.Backend{first, second} {
			wg.Add(1)
			go func(be privatedocs.Backend) {
				defer wg.Done()
				for range each {
					tx, e := be.Begin(a)
					if e != nil {
						errs <- e
						return
					}
					data, _ := tx.Load()
					n := 0
					if data != nil {
						n, _ = strconv.Atoi(string(data))
					}
					time.Sleep(time.Millisecond)
					if e = tx.Store([]byte(strconv.Itoa(n + 1))); e == nil {
						e = tx.Commit()
					}
					if e != nil {
						tx.Rollback()
						errs <- e
						return
					}
				}
			}(be)
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		if data, _ := first.Load(a); string(data) != fmt.Sprint(2*each) {
			t.Fatalf("a change was lost: %q", data)
		}
	})
}
