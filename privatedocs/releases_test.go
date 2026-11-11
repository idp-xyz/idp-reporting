package privatedocs_test

import (
	"errors"
	"reflect"
	"testing"

	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

// countedBackend counts the reads of an owner's document.
type countedBackend struct {
	privatedocs.Backend
	loads *int
}

func (b countedBackend) Load(owner r.Identity) ([]byte, error) {
	*b.loads++
	return b.Backend.Load(owner)
}

// Releases answers what Release answers for each record, from one read of the owner's document.
func TestReleasesIsEveryRecordsReleaseFromOneRead(t *testing.T) {
	owner := r.Identity{Tenant: "t", Issuer: "i", Subject: "s"}
	loads := 0
	s, e := privatedocs.OpenBackend(countedBackend{privatedocs.NewMemoryBackend(), &loads}, privatedocs.DefaultCap, func(string) error { return nil }, func(string) string { return "b" })
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if all, e := s.Releases(owner); e != nil || len(all) != 0 {
		t.Fatalf("an owner without records: %v %v", all, e)
	}
	var ids []string
	for i := range 5 {
		entry, e := s.Save(owner, "", 0, "x")
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, entry.ID)
		if i%2 == 0 {
			if _, e = s.Save(owner, entry.ID, 1, "y"); e != nil {
				t.Fatal(e)
			}
		}
	}
	// One published, one published and withdrawn, one published twice, two never.
	for _, step := range []struct {
		id       string
		action   string
		version  int
		expected int
	}{{ids[0], "publish", 2, 0}, {ids[1], "publish", 1, 0}, {ids[1], "withdraw", 0, 1}, {ids[2], "publish", 1, 0}, {ids[2], "publish", 2, 1}} {
		if _, e = s.Publish(owner, step.id, step.action, step.version, step.expected); e != nil {
			t.Fatal(step, e)
		}
	}
	loads = 0
	all, e := s.Releases(owner)
	if e != nil || loads != 1 {
		t.Fatalf("%v after %d reads", e, loads)
	}
	if len(all) != len(ids) {
		t.Fatalf("%d records of %d", len(all), len(ids))
	}
	for i, want := range []int{2, 0, 2, 0, 0} {
		one, e := s.Release(owner, ids[i])
		if e != nil {
			t.Fatal(e)
		}
		if got, ok := all[ids[i]]; !ok || got.PublishedVersion != want || !reflect.DeepEqual(got, one) {
			t.Errorf("record %d: %+v, Release says %+v, published version wanted %d", i, got, one, want)
		}
	}
	// Another owner has nothing here, and an identity that is not one is refused as every read refuses it.
	other := owner
	other.Subject = "another"
	if all, e = s.Releases(other); e != nil || len(all) != 0 {
		t.Fatalf("another owner: %v %v", all, e)
	}
	if _, e = s.Releases(r.Identity{}); !errors.Is(e, r.ErrDenied) {
		t.Fatalf("no identity: %v", e)
	}
}
