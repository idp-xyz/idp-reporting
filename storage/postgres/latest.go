package postgres

// LatestVersion is the highest embedded migration: the schema version this binary needs. Tests compare against it instead of a literal, so
// that adding a migration changes the files that describe that migration and not every test that opens a store.
func LatestVersion() int {
	known, err := Migrations()
	if err != nil || len(known) == 0 {
		return 0
	}
	return known[len(known)-1].Version
}
