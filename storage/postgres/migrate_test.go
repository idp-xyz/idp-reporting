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
	if err != nil || len(real) != LatestVersion() || real[0].Name != "base" || real[1].Name != "payload_digests" || real[2].Name != "issues" || real[3].Name != "source_confirmed_issue" || real[4].Name != "retention" || real[5].Name != "sweeps" || real[6].Name != "row_level_security" || real[7].Name != "object_storage" || real[8].Name != "capacity_indexes" || real[9].Name != "jobs_identity_kind_stats" {
		t.Fatalf("%+v %v", real, err)
	}
	for _, m := range real {
		if !strings.Contains(m.Up, "reporting_s01") || !strings.Contains(m.Down, "reporting_s01") {
			t.Errorf("migration %03d does not name the schema", m.Version)
		}
	}
	// A step whose down has a rule of its own is found by its number (dataLoss): the number is held to the step's name, so that a step
	// renumbered before it is merged cannot leave the rule on another step.
	if keptResultsVersion < 1 || keptResultsVersion > len(real) || real[keptResultsVersion-1].Name != "kept_results" {
		t.Errorf("keptResultsVersion is %d, which is not the step kept_results", keptResultsVersion)
	}
	// The index the stored-results count had is dropped by the step after kept_results and not by kept_results itself (S25-B1): a
	// transaction that builds an index on a table and then drops one of the same table asks for the table's exclusive lock while it
	// holds the lock of the build, and a writer that has read the table is then in a deadlock with the migration.
	if keptResultsVersion < len(real) {
		next := real[keptResultsVersion]
		if next.Name != "stored_results_index_cleanup" || strings.TrimSpace(lastStatement(next.Up)) != "DROP INDEX IF EXISTS reporting_s01.assets_identity_kind_expires" {
			t.Errorf("the step after kept_results is %q and does not only drop the index of 009", next.Name)
		}
	} else {
		t.Error("no step after kept_results drops the index the count no longer reads")
	}
	if strings.Contains(real[keptResultsVersion-1].Up, "DROP INDEX") || strings.Contains(real[keptResultsVersion-1].Down, "CREATE INDEX") {
		t.Error("kept_results builds an index and drops one, on the same table, in one transaction")
	}
}

// lastStatement is the SQL of a migration file without its comment lines, and without the final semicolon: for a file of one statement.
func lastStatement(sql string) string {
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") && strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.TrimSuffix(strings.Join(out, " "), ";")
}
