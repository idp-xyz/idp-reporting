package fixturedb

import (
	"context"
	"errors"
	r "idp.local/reporting/reporting"
	"testing"
)

// The restore drill's scratch databases (S04-F) are opened by their own guard, and the fixture guard does not widen: the drill reaches nothing
// the fixtures reach, and the fixtures' tests nothing of the drill's. Neither opens a connection here: a refusal is before the pool is made.
func TestTheDrillGuardAndTheFixtureGuardDoNotOverlap(t *testing.T) {
	ctx := context.Background()
	dsn := func(host, port, db string) string {
		return "postgresql://postgres@" + host + ":" + port + "/" + db + "?sslmode=disable"
	}
	fixtures := []string{"reporting_test_s01", "reporting_source_test_s01", "reporting_test_s01_race", "reporting_source_test_s01_race"}
	for _, db := range fixtures {
		if _, e := OpenDrill(ctx, dsn("127.0.0.1", "55433", db)); !errors.Is(e, r.ErrDenied) {
			t.Errorf("the drill opened the fixture database %s: %v", db, e)
		}
	}
	for name, d := range map[string]string{
		"a database that is not the drill's":  dsn("127.0.0.1", "55433", "reporting_restore"),
		"the bare prefix":                     dsn("127.0.0.1", "55433", DrillPrefix),
		"a prefix inside the name":            dsn("127.0.0.1", "55433", "x"+DrillPrefix+"a"),
		"the gate server's port":              dsn("127.0.0.1", "55432", DrillPrefix+"a"),
		"an address that is not the loopback": dsn("10.0.0.5", "55433", DrillPrefix+"a"),
		"a host name":                         dsn("db.example.com", "55433", DrillPrefix+"a"),
	} {
		if _, e := OpenDrill(ctx, d); !errors.Is(e, r.ErrDenied) {
			t.Errorf("%s: %v", name, e)
		}
	}
	if _, e := Open(ctx, dsn("127.0.0.1", "55433", DrillPrefix+"a"), fixtures...); !errors.Is(e, r.ErrDenied) {
		t.Errorf("the fixture guard opened a drill database: %v", e)
	}
	// What it is for: the drill's own, on the loopback (the pool is lazy, nothing is connected).
	p, e := OpenDrill(ctx, dsn("127.0.0.1", "55433", DrillPrefix+"20261102_plat"))
	if e != nil {
		t.Fatal(e)
	}
	p.Close()
}
