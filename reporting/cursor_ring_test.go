package reporting_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// The cursor key ring (S04-H, docs/S04H_SCOPE.md H1). The keys are fixed test strings so that the assertions can look for them in the
// error texts.
var (
	ringK1 = []byte("cursor-ring-test-key-ONE-0123456789")
	ringK2 = []byte("cursor-ring-test-key-TWO-0123456789")
	ringK3 = []byte("cursor-ring-test-key-THREE-01234567")
	ringK4 = []byte("cursor-ring-test-key-FOUR-012345678")
	ringK5 = []byte("cursor-ring-test-key-FIVE-012345678")
)

type ringWorld struct {
	repo    r.Repository
	adapter *contractsim.Simulator
	p       r.Principal
	snap    string
}

func newRingWorld(t *testing.T) *ringWorld {
	t.Helper()
	w := &ringWorld{repo: memory.New(time.Now), adapter: contractsim.Financial(), p: contractsim.DemoPrincipal()}
	w.adapter.Grant(contractsim.DemoOwner(w.p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s := w.service(t, ringK1)
	ctx := context.Background()
	job, err := s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "ring")
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := s.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if job, err = s.Job(ctx, w.p, job.ID); err != nil || job.State != "succeeded" {
		t.Fatal(job, err)
	}
	w.snap = job.SnapshotID
	return w
}

// service is a server process holding the ring: the same repository and source, as after a restart with another configuration.
func (w *ringWorld) service(t *testing.T, ring ...[]byte) *r.Service {
	t.Helper()
	s, err := r.NewService(r.Config{Repository: w.repo, Connections: []r.Connection{{Tenant: w.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: w.adapter}}, CursorKeys: ring})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// cursor is the first cursor the ring's newest key signs.
func (w *ringWorld) cursor(t *testing.T, ring ...[]byte) string {
	t.Helper()
	page, err := w.service(t, ring...).Rows(context.Background(), w.p, w.snap, 1, "")
	if err != nil || page.NextCursor == "" {
		t.Fatalf("the snapshot has no second page: %v %q", err, page.NextCursor)
	}
	return page.NextCursor
}

func (w *ringWorld) accepts(t *testing.T, cursor string, ring ...[]byte) bool {
	t.Helper()
	_, err := w.service(t, ring...).Rows(context.Background(), w.p, w.snap, 1, cursor)
	if err != nil && !errors.Is(err, r.ErrInvalid) {
		t.Fatalf("a cursor is refused with something other than ErrInvalid: %v", err)
	}
	return err == nil
}

// A cursor issued under K1 is accepted by [K2,K1] and [K1] and refused by [K2]; one issued under K2 verifies under [K3,K2], [K2,K1] and [K2].
// The first key of the ring signs: the cursors of [K2,K1] are not accepted by [K1].
func TestCursorRingVerifiesOlderKeysAndSignsWithTheNewest(t *testing.T) {
	w := newRingWorld(t)
	under1 := w.cursor(t, ringK1)
	for name, c := range map[string]struct {
		ring [][]byte
		ok   bool
	}{
		"[K1]":          {[][]byte{ringK1}, true},
		"[K2,K1]":       {[][]byte{ringK2, ringK1}, true},
		"[K3,K2,K1]":    {[][]byte{ringK3, ringK2, ringK1}, true},
		"[K4,K3,K2,K1]": {[][]byte{ringK4, ringK3, ringK2, ringK1}, true},
		"[K2]":          {[][]byte{ringK2}, false},
		"[K3,K2]":       {[][]byte{ringK3, ringK2}, false},
	} {
		if got := w.accepts(t, under1, c.ring...); got != c.ok {
			t.Errorf("a cursor issued under K1 under the ring %s: accepted=%v, want %v", name, got, c.ok)
		}
	}
	under2 := w.cursor(t, ringK2, ringK1)
	if under2 == under1 {
		t.Fatal("the newest key did not sign: the cursor is the one K1 issues")
	}
	for name, ring := range map[string][][]byte{"[K3,K2]": {ringK3, ringK2}, "[K2,K1]": {ringK2, ringK1}, "[K2]": {ringK2}} {
		if !w.accepts(t, under2, ring...) {
			t.Errorf("a cursor issued under K2 is refused by the ring %s", name)
		}
	}
	if w.accepts(t, under2, ringK1) || w.accepts(t, under2, ringK3, ringK1) {
		t.Error("a cursor issued under K2 is accepted by a ring without K2")
	}
	// The format has not changed: a ring of one signs exactly what Config.CursorKey signs.
	one, err := r.NewService(r.Config{Repository: w.repo, Connections: []r.Connection{{Tenant: w.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: w.adapter}}, CursorKey: ringK1})
	if err != nil {
		t.Fatal(err)
	}
	page, err := one.Rows(context.Background(), w.p, w.snap, 1, "")
	if err != nil || page.NextCursor != under1 {
		t.Fatalf("Config.CursorKey is not the ring of one: %v %q", err, page.NextCursor)
	}
	if strings.Count(under1, ".") != 1 {
		t.Errorf("the cursor format changed: %q", under1)
	}
}

// A cursor that was never valid is ErrInvalid under every ring, exactly as before: tampered, another snapshot's or owner's, oversized, signed
// by a key the ring does not hold. The ring evaluates every key and accepts the first page's own cursor only.
func TestCursorRingRefusesWhatWasNeverValid(t *testing.T) {
	w := newRingWorld(t)
	ring := [][]byte{ringK2, ringK1}
	good := w.cursor(t, ring...)
	if !w.accepts(t, good, ring...) {
		t.Fatal("the control cursor is refused")
	}
	payload, sig, _ := strings.Cut(good, ".")
	flip := func(s string) string {
		b := []byte(s)
		if b[0] == 'A' {
			b[0] = 'B'
		} else {
			b[0] = 'A'
		}
		return string(b)
	}
	other := w.cursor(t, ringK3)
	// A second snapshot of the same owner, a second owner's snapshot.
	ctx := context.Background()
	s := w.service(t, ring...)
	job, err := s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "ring-two")
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := s.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if job, err = s.Job(ctx, w.p, job.ID); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]string{
		"a tampered payload":        flip(payload) + "." + sig,
		"a tampered signature":      payload + "." + flip(sig),
		"an unknown key":            other,
		"no signature":              payload + ".",
		"no dot":                    payload,
		"an oversized cursor":       strings.Repeat("A", 2049),
		"an oversized, well-formed": payload + "." + strings.Repeat("A", 2048),
		"empty payload":             "." + sig,
		"another dot":               good + ".x",
	} {
		if w.accepts(t, c, ring...) {
			t.Errorf("%s was accepted", name)
		}
	}
	// Another snapshot: the cursor of the first, presented to the second, and a different owner.
	if _, err := s.Rows(ctx, w.p, job.SnapshotID, 1, good); !errors.Is(err, r.ErrInvalid) {
		t.Errorf("a cursor of another snapshot: %v", err)
	}
	stranger := w.p
	stranger.Identity.Subject = "someone-else"
	if _, err := s.Rows(ctx, stranger, w.snap, 1, good); err == nil {
		t.Error("a cursor presented by another identity was accepted")
	}
}

// The ring is validated at NewService: ErrInvalid, and a text that carries no key.
func TestCursorRingValidation(t *testing.T) {
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	connections := []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}
	short := []byte("short-ring-key-SECRET-0123456")
	for name, cfg := range map[string]r.Config{
		"none":               {},
		"empty ring":         {CursorKeys: [][]byte{}},
		"both fields":        {CursorKey: ringK1, CursorKeys: [][]byte{ringK2}},
		"a short key":        {CursorKeys: [][]byte{short}},
		"a short key, 2nd":   {CursorKeys: [][]byte{ringK1, short}},
		"a short CursorKey":  {CursorKey: short},
		"an empty key":       {CursorKeys: [][]byte{ringK1, nil}},
		"a duplicate":        {CursorKeys: [][]byte{ringK1, ringK2, ringK1}},
		"adjacent duplicate": {CursorKeys: [][]byte{ringK1, ringK1}},
		"five keys":          {CursorKeys: [][]byte{ringK1, ringK2, ringK3, ringK4, ringK5}},
	} {
		cfg.Repository, cfg.Connections = memory.New(time.Now), connections
		s, err := r.NewService(cfg)
		if s != nil || !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%s: %v %v", name, s, err)
			continue
		}
		for _, k := range [][]byte{ringK1, ringK2, ringK3, ringK4, ringK5, short} {
			if strings.Contains(err.Error(), string(k)) {
				t.Errorf("%s: the error carries a key: %q", name, err)
			}
		}
	}
	// One to four keys are accepted, and a key of exactly 32 bytes.
	for _, ring := range [][][]byte{{ringK1}, {ringK1, ringK2}, {ringK1, ringK2, ringK3}, {ringK1, ringK2, ringK3, ringK4}, {bytes.Repeat([]byte("k"), 32)}} {
		if _, err := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: connections, CursorKeys: ring}); err != nil {
			t.Errorf("%d keys: %v", len(ring), err)
		}
	}
	// The service keeps its own copy: changing the caller's slices afterwards changes nothing.
	w := newRingWorld(t)
	key := bytes.Clone(ringK1)
	s, err := r.NewService(r.Config{Repository: w.repo, Connections: []r.Connection{{Tenant: w.p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: w.adapter}}, CursorKeys: [][]byte{key}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.Rows(context.Background(), w.p, w.snap, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	clear(key)
	if _, err := s.Rows(context.Background(), w.p, w.snap, 1, page.NextCursor); err != nil {
		t.Errorf("the service shares the caller's key: %v", err)
	}
}

// The rotation procedure of docs/OPERATIONS.md "Keys and credentials", as a rolling restart: four rings, each deployed one server at a
// time, so that during each step servers of two neighbouring rings answer the same user. A cursor issued by a server of one ring is accepted by
// the servers of the ring before and after it: no user's pagination breaks during the roll. The cursors issued before the last step are not
// accepted by the ring that drops K1, which is why the procedure waits for them to expire first.
func TestCursorKeyRotationAsARollingRestartRefusesNoCursor(t *testing.T) {
	w := newRingWorld(t)
	rings := [][][]byte{{ringK1}, {ringK1, ringK2}, {ringK2, ringK1}, {ringK2}}
	names := []string{"[K1]", "[K1,K2]", "[K2,K1]", "[K2]"}
	issued := make([]string, len(rings))
	for i, ring := range rings {
		issued[i] = w.cursor(t, ring...)
	}
	for i := range rings {
		for _, j := range []int{i - 1, i + 1} {
			if j < 0 || j >= len(rings) {
				continue
			}
			if !w.accepts(t, issued[i], rings[j]...) {
				t.Errorf("a cursor issued by a server with the ring %s is refused by a server with the ring %s during the roll", names[i], names[j])
			}
		}
	}
	// The newest key signs from step 2 on, and K1's cursors are what the last step drops.
	if issued[0] != issued[1] || issued[2] == issued[1] || issued[2] != issued[3] {
		t.Error("the signing key did not change at the step where the new key goes first")
	}
	if w.accepts(t, issued[0], rings[3]...) || w.accepts(t, issued[1], rings[3]...) {
		t.Error("the final ring still accepts a cursor of the dropped key")
	}
}

// A cursor is good for as long as its snapshot is: the key ring decides who signed it, the snapshot's expiry decides how long it reads.
// That is the bound the runbook waits for before it drops a key.
func TestACursorStopsWorkingWhenItsSnapshotExpires(t *testing.T) {
	var skew time.Duration
	clock := func() time.Time { return time.Now().Add(skew) }
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	s, err := r.NewService(r.Config{Repository: memory.New(clock), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKeys: [][]byte{ringK1}, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job, err := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "expiry")
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := s.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if job, err = s.Job(ctx, p, job.ID); err != nil {
		t.Fatal(err)
	}
	page, err := s.Rows(ctx, p, job.SnapshotID, 1, "")
	if err != nil || page.NextCursor == "" {
		t.Fatal(err)
	}
	if _, err := s.Rows(ctx, p, job.SnapshotID, 1, page.NextCursor); err != nil {
		t.Fatalf("the cursor does not work while the snapshot lives: %v", err)
	}
	if page.ExpiresAt.Sub(time.Now()) > 15*time.Minute {
		t.Fatalf("a snapshot outlives the 15 minutes the runbook waits: %s", page.ExpiresAt)
	}
	skew = 16 * time.Minute
	if _, err := s.Rows(ctx, p, job.SnapshotID, 1, page.NextCursor); err == nil {
		t.Error("a cursor reads a snapshot that expired")
	}
}
