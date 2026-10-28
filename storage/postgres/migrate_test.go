package postgres

import (
	"strings"
	"testing"
	"testing/fstest"
)

// The embedded set's consistency rules, on synthetic sets: a bad file name, two names for one
// version, a missing half, a gap in the versions; and the real set is consistent.
func TestMigrationSetRules(t *testing.T) {
	good := fstest.MapFS{
		"migrations/001_a.up.sql": {Data: []byte("up1")}, "migrations/001_a.down.sql": {Data: []byte("down1")},
		"migrations/002_b.up.sql": {Data: []byte("up2")}, "migrations/002_b.down.sql": {Data: []byte("down2")},
	}
	set, err := migrationsFrom(good)
	if err != nil || len(set) != 2 || set[1].Name != "b" || set[1].Up != "up2" || len(set[1].Checksum) != 64 || set[0].Checksum == set[1].Checksum {
		t.Fatalf("%+v %v", set, err)
	}
	for name, change := range map[string]func(fstest.MapFS){
		"bad name":     func(m fstest.MapFS) { m["migrations/3_c.up.sql"] = &fstest.MapFile{Data: []byte("x")} },
		"two names":    func(m fstest.MapFS) { m["migrations/002_c.up.sql"] = &fstest.MapFile{Data: []byte("x")} },
		"missing down": func(m fstest.MapFS) { m["migrations/003_c.up.sql"] = &fstest.MapFile{Data: []byte("x")} },
		"gap": func(m fstest.MapFS) {
			m["migrations/004_d.up.sql"] = &fstest.MapFile{Data: []byte("x")}
			m["migrations/004_d.down.sql"] = &fstest.MapFile{Data: []byte("x")}
		},
		"not from 1": func(m fstest.MapFS) {
			delete(m, "migrations/001_a.up.sql")
			delete(m, "migrations/001_a.down.sql")
		},
	} {
		m := fstest.MapFS{}
		for k, v := range good {
			m[k] = v
		}
		change(m)
		if _, err := migrationsFrom(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	real, err := Migrations()
	if err != nil || len(real) != 3 || real[0].Name != "base" || real[1].Name != "payload_digests" || real[2].Name != "issues" {
		t.Fatalf("%+v %v", real, err)
	}
	for _, m := range real {
		if !strings.Contains(m.Up, "reporting_s01") || !strings.Contains(m.Down, "reporting_s01") {
			t.Errorf("migration %03d does not name the schema", m.Version)
		}
	}
}
