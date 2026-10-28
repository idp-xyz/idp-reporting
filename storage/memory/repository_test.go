package memory

import (
	"context"
	"errors"
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
