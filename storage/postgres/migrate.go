package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The schema lifecycle (S04-A): numbered migration pairs embedded in the binary, a ledger of what
// was applied with the checksum of each up step, one transaction per step under an advisory lock.

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migration is one embedded step.
type Migration struct {
	Version  int
	Name     string
	Up, Down string
	Checksum string // SHA-256 of the up file, hex
}

// Applied is one ledger row.
type Applied struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt time.Time
	AppliedBy string
}

// State is what Status found: the embedded steps, the ledger, and what keeps the store from
// opening (Problems) or what Up would do (Pending).
type State struct {
	Known    []Migration
	Applied  []Applied
	Pending  []int
	Problems []string
	Legacy   bool // the ledger did not exist; the profile's version was taken as applied
}

// Latest is the highest embedded version.
func (s State) Latest() int { return s.Known[len(s.Known)-1].Version }

// Current is the highest applied version, 0 before the first.
func (s State) Current() int {
	if len(s.Applied) == 0 {
		return 0
	}
	return s.Applied[len(s.Applied)-1].Version
}

// lockKey is the transaction-scoped advisory lock every step holds.
const lockKey int64 = 0x5245504f5254 // "REPORT"

var migrationName = regexp.MustCompile(`^(\d{3})_([a-z0-9_]+)\.(up|down)\.sql$`)

// Migrations lists the embedded steps in order; an inconsistent set is a programming error.
func Migrations() ([]Migration, error) { return migrationsFrom(migrationFiles) }

func migrationsFrom(files fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, err
	}
	byVersion := map[int]*Migration{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q is not NNN_name.up|down.sql", e.Name())
		}
		b, err := fs.ReadFile(files, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		v, _ := strconv.Atoi(m[1])
		step := byVersion[v]
		if step == nil {
			step = &Migration{Version: v, Name: m[2]}
			byVersion[v] = step
		}
		if step.Name != m[2] {
			return nil, fmt.Errorf("migration %d has two names", v)
		}
		if m[3] == "up" {
			step.Up = string(b)
			sum := sha256.Sum256(b)
			step.Checksum = hex.EncodeToString(sum[:])
		} else {
			step.Down = string(b)
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, s := range byVersion {
		if s.Up == "" || s.Down == "" {
			return nil, fmt.Errorf("migration %d lacks its up or down step", s.Version)
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, s := range out {
		if s.Version != i+1 {
			return nil, fmt.Errorf("migration versions are not consecutive from 1: %d at position %d", s.Version, i+1)
		}
	}
	return out, nil
}

// Status reads the ledger and compares it with the embedded steps. A ledger that names a version
// the binary does not know, a checksum that differs from the embedded up file, or a gap in the
// applied versions is a Problem: the store must not open on such a database. Without a ledger
// but with a profile row (a database made before S04-A), the profile's version is taken as
// applied and reported as Legacy; Up records those steps as legacy rows.
func Status(ctx context.Context, pool *pgxpool.Pool) (State, error) {
	known, err := Migrations()
	if err != nil {
		return State{}, err
	}
	st := State{Known: known}
	var hasLedger, hasProfile bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('reporting_s01.schema_migrations') IS NOT NULL, to_regclass('reporting_s01.profile') IS NOT NULL").Scan(&hasLedger, &hasProfile); err != nil {
		return State{}, err
	}
	switch {
	case hasLedger:
		rows, err := pool.Query(ctx, "SELECT version,name,checksum,applied_at,applied_by FROM reporting_s01.schema_migrations ORDER BY version")
		if err != nil {
			return State{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var a Applied
			if err := rows.Scan(&a.Version, &a.Name, &a.Checksum, &a.AppliedAt, &a.AppliedBy); err != nil {
				return State{}, err
			}
			st.Applied = append(st.Applied, a)
		}
		if err := rows.Err(); err != nil {
			return State{}, err
		}
	case hasProfile:
		var version int
		if err := pool.QueryRow(ctx, "SELECT version FROM reporting_s01.profile WHERE singleton").Scan(&version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				st.Problems = append(st.Problems, "a profile table without a row and no ledger: the database is half-initialised")
				return st, nil
			}
			return State{}, err
		}
		st.Legacy = true
		for v := 1; v <= version; v++ {
			st.Applied = append(st.Applied, Applied{Version: v, Name: "legacy", Checksum: "legacy", AppliedBy: "profile"})
		}
	}
	for i, a := range st.Applied {
		switch {
		case a.Version != i+1:
			st.Problems = append(st.Problems, fmt.Sprintf("the ledger has a gap before version %d", a.Version))
		case a.Version > len(known):
			st.Problems = append(st.Problems, fmt.Sprintf("version %d was applied by a newer binary", a.Version))
		case a.Checksum != "legacy" && a.Checksum != known[a.Version-1].Checksum:
			st.Problems = append(st.Problems, fmt.Sprintf("version %d was applied from a different file (checksum %.12s, embedded %.12s)", a.Version, a.Checksum, known[a.Version-1].Checksum))
		case a.Checksum != "legacy" && a.Name != known[a.Version-1].Name:
			st.Problems = append(st.Problems, fmt.Sprintf("version %d was applied under another name (%s)", a.Version, a.Name))
		case a.Checksum == "legacy" && !st.Legacy && !strings.HasPrefix(a.AppliedBy, "legacy:"):
			st.Problems = append(st.Problems, fmt.Sprintf("version %d claims to be a legacy row but was not recorded by the bootstrap", a.Version))
		}
	}
	if hasLedger && hasProfile {
		var version *int
		if err := pool.QueryRow(ctx, "SELECT version FROM reporting_s01.profile WHERE singleton").Scan(&version); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return State{}, err
		}
		if version != nil && *version != st.Current() {
			st.Problems = append(st.Problems, fmt.Sprintf("the profile says version %d while the ledger is at %d", *version, st.Current()))
		}
	}
	for v := st.Current() + 1; v <= len(known); v++ {
		st.Pending = append(st.Pending, v)
	}
	return st, nil
}

// Direction of a plan: Up only applies, Down only reverts; a target on the wrong side of the
// current version is refused instead of silently doing nothing.
type Direction int

const (
	Either Direction = iota
	Upward
	Downward
)

// Plan describes what Up or Down to target would do, without doing it.
func Plan(st State, target int, dir Direction) ([]string, error) {
	if target < 0 || target > st.Latest() {
		return nil, fmt.Errorf("target %d is outside 0..%d: %w", target, st.Latest(), r.ErrInvalid)
	}
	if len(st.Problems) > 0 {
		return nil, fmt.Errorf("the ledger is not usable: %s: %w", strings.Join(st.Problems, "; "), r.ErrIntegrity)
	}
	current := st.Current()
	if dir == Upward && target < current || dir == Downward && target > current {
		return nil, fmt.Errorf("target %d is on the wrong side of the current version %d: %w", target, current, r.ErrInvalid)
	}
	var out []string
	for v := current + 1; v <= target; v++ {
		out = append(out, fmt.Sprintf("up   %03d %s", v, st.Known[v-1].Name))
	}
	for v := current; v > target; v-- {
		out = append(out, fmt.Sprintf("down %03d %s", v, st.Known[v-1].Name))
	}
	return out, nil
}

// Up applies the pending steps up to target (0: the latest), one transaction each under the
// advisory lock, recording each in the ledger and keeping the profile's version current. On a
// legacy database the ledger is created first with the profile's versions as legacy rows.
func Up(ctx context.Context, pool *pgxpool.Pool, target int, by string) error {
	st, err := Status(ctx, pool)
	if err != nil {
		return err
	}
	if target == 0 {
		target = st.Latest()
	}
	if _, err := Plan(st, target, Upward); err != nil {
		return err
	}
	if st.Legacy {
		if err := bootstrapLedger(ctx, pool, st, by); err != nil {
			return err
		}
	}
	for v := st.Current() + 1; v <= target; v++ {
		if err := step(ctx, pool, st.Known[v-1], true, by, false); err != nil {
			return fmt.Errorf("migration %03d up: %w", v, err)
		}
	}
	return nil
}

// Down reverts the applied steps above target, one transaction each, newest first. A step
// whose down would discard rows is refused unless acceptDataLoss.
func Down(ctx context.Context, pool *pgxpool.Pool, target int, by string, acceptDataLoss bool) error {
	st, err := Status(ctx, pool)
	if err != nil {
		return err
	}
	if _, err := Plan(st, target, Downward); err != nil {
		return err
	}
	if st.Legacy {
		if err := bootstrapLedger(ctx, pool, st, by); err != nil {
			return err
		}
	}
	for v := st.Current(); v > target; v-- {
		if err := step(ctx, pool, st.Known[v-1], false, by, acceptDataLoss); err != nil {
			return fmt.Errorf("migration %03d down: %w", v, err)
		}
	}
	return nil
}

const ledgerDDL = `CREATE TABLE IF NOT EXISTS reporting_s01.schema_migrations (
  version integer PRIMARY KEY CHECK (version > 0), name text NOT NULL, checksum text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT clock_timestamp(), applied_by text NOT NULL
)`

func bootstrapLedger(ctx context.Context, pool *pgxpool.Pool, st State, by string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if err := lock(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, ledgerDDL); err != nil {
		return err
	}
	for _, a := range st.Applied {
		if _, err := tx.Exec(ctx, "INSERT INTO reporting_s01.schema_migrations(version,name,checksum,applied_by) VALUES($1,$2,'legacy',$3) ON CONFLICT (version) DO NOTHING", a.Version, st.Known[a.Version-1].Name, "legacy:"+by); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// dataLoss says which rows a down step discards; the operator must accept that explicitly.
func dataLoss(ctx context.Context, tx pgx.Tx, m Migration) (string, error) {
	var n int64
	switch m.Version {
	case 1:
		var profiles int64
		if err := tx.QueryRow(ctx, "SELECT (SELECT count(*) FROM reporting_s01.jobs)+(SELECT count(*) FROM reporting_s01.assets), (SELECT count(*) FROM reporting_s01.profile)").Scan(&n, &profiles); err != nil {
			return "", err
		}
		if n > 0 || profiles > 0 {
			return fmt.Sprintf("%d jobs and assets and the profile row (init-profile is needed after the next up)", n), nil
		}
	case 3:
		var counters, issued int64
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM reporting_s01.jobs WHERE kind='issue')+(SELECT count(*) FROM reporting_s01.assets WHERE kind='issue'),
(SELECT count(*) FROM reporting_s01.issue_numbers), (SELECT count(*) FROM reporting_s01.assets WHERE kind='artifact' AND data ? 'Issue')`).Scan(&n, &counters, &issued); err != nil {
			return "", err
		}
		if n > 0 || counters > 0 || issued > 0 {
			return fmt.Sprintf("%d issue jobs and records, %d issued PDFs and %d issue counters (the numbering of every tenant and integration would restart at 1, reusing numbers already printed)", n, issued, counters), nil
		}
	}
	return "", nil
}

// step runs one migration in one transaction under the advisory lock, re-reading the ledger
// inside the lock so that two migrators cannot both apply the same step.
func step(ctx context.Context, pool *pgxpool.Pool, m Migration, up bool, by string, acceptDataLoss bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if err := lock(ctx, tx); err != nil {
		return err
	}
	if m.Version == 1 && up {
		// A backend that waited on the lock has not processed the catalog invalidations of the
		// migrator it waited for, and a negative syscache entry from the earlier Status would
		// make CREATE SCHEMA IF NOT EXISTS fail on a duplicate; a catalog scan takes a lock and
		// accepts them. The schema is created only when absent, so the migrator role needs
		// CREATE on the database for the very first run only.
		var schema bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname='reporting_s01')").Scan(&schema); err != nil {
			return err
		}
		if !schema {
			if _, err := tx.Exec(ctx, "CREATE SCHEMA reporting_s01"); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, ledgerDDL); err != nil {
			return err
		}
	}
	// Under the lock, the ledger's head must be exactly where this step expects it: the step
	// before it for up, the step itself for down. Another migrator may have moved it since the
	// plan was made; a stale plan is refused, never applied onto the wrong schema.
	var head int
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(version),0) FROM reporting_s01.schema_migrations").Scan(&head); err != nil {
		return err
	}
	want := m.Version - 1
	if !up {
		want = m.Version
	}
	if head != want {
		if up && head >= m.Version || !up && head < m.Version {
			return nil // another migrator did this step; the lock serialized us behind it
		}
		return fmt.Errorf("the ledger is at version %d, not %d: another migrator changed the schema, rerun: %w", head, want, r.ErrConflict)
	}
	if !(m.Version == 1 && up) { // the profile table exists from 001 on
		var profileVersion *int
		if err := tx.QueryRow(ctx, "SELECT version FROM reporting_s01.profile WHERE singleton").Scan(&profileVersion); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if profileVersion != nil && *profileVersion != head {
			return fmt.Errorf("the profile says version %d while the ledger is at %d: %w", *profileVersion, head, r.ErrIntegrity)
		}
	}
	if up {
		if _, err := tx.Exec(ctx, m.Up); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO reporting_s01.schema_migrations(version,name,checksum,applied_by) VALUES($1,$2,$3,$4)", m.Version, m.Name, m.Checksum, by); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE reporting_s01.profile SET version=$1 WHERE singleton", m.Version); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	loss, err := dataLoss(ctx, tx, m)
	if err != nil {
		return err
	}
	if loss != "" && !acceptDataLoss {
		return fmt.Errorf("would discard %s; pass --accept-data-loss to proceed: %w", loss, r.ErrDenied)
	}
	if m.Version > 1 {
		if _, err := tx.Exec(ctx, "UPDATE reporting_s01.profile SET version=$1 WHERE singleton", m.Version-1); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, m.Down); err != nil {
		return err
	}
	if m.Version == 1 {
		if _, err := tx.Exec(ctx, "DROP TABLE reporting_s01.schema_migrations"); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, "DELETE FROM reporting_s01.schema_migrations WHERE version=$1", m.Version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lock takes the migration lock, waiting at most a minute: a session holding it (any login can
// take an advisory lock) is reported instead of hanging the operator's run.
func lock(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '60s'"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return fmt.Errorf("another session holds the migration lock (pg_locks shows it) or the server did not answer within a minute: %w", err)
	}
	return nil
}

func rollback(ctx context.Context, tx pgx.Tx) {
	cleanup, cancel := fixturedb.RollbackContext(ctx)
	defer cancel()
	_ = tx.Rollback(cleanup)
}

// Verify is the opening check of a store: the ledger at the binary's latest version with no
// problem and the profile row named as expected.
func Verify(ctx context.Context, pool *pgxpool.Pool, profile string) error {
	st, err := Status(ctx, pool)
	if err != nil {
		return err
	}
	if len(st.Problems) > 0 {
		return fmt.Errorf("schema ledger: %s: %w", strings.Join(st.Problems, "; "), r.ErrIntegrity)
	}
	if st.Legacy {
		return fmt.Errorf("no schema ledger (the database predates S04-A); run reporting-migrate up to record it: %w", r.ErrDenied)
	}
	if st.Current() != st.Latest() {
		return fmt.Errorf("schema at version %d, the binary needs %d (run reporting-migrate up): %w", st.Current(), st.Latest(), r.ErrDenied)
	}
	if profile == ProfileProduction {
		// No production store existed before the ledger: a legacy row there is not a bootstrap
		// but an unverifiable step.
		for _, a := range st.Applied {
			if a.Checksum == "legacy" {
				return fmt.Errorf("version %d of a production store has no verifiable checksum: %w", a.Version, r.ErrIntegrity)
			}
		}
	}
	var name string
	var version int
	if err := pool.QueryRow(ctx, "SELECT profile,version FROM reporting_s01.profile WHERE singleton").Scan(&name, &version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no profile row (run reporting-migrate init-profile): %w", r.ErrDenied)
		}
		return err
	}
	if name != profile || version != st.Latest() {
		return fmt.Errorf("profile %q at version %d, expected %q at %d: %w", name, version, profile, st.Latest(), r.ErrDenied)
	}
	return nil
}

// InitProfile inserts the profile row once the schema exists; it never changes an existing one.
func InitProfile(ctx context.Context, pool *pgxpool.Pool, profile string) error {
	if profile != ProfileFixture && profile != ProfileProduction {
		return r.ErrInvalid
	}
	st, err := Status(ctx, pool)
	if err != nil {
		return err
	}
	if st.Legacy || st.Current() == 0 {
		return fmt.Errorf("the schema must be migrated before the profile is set: %w", r.ErrDenied)
	}
	tag, err := pool.Exec(ctx, "INSERT INTO reporting_s01.profile(singleton,profile,version) VALUES(true,$1,$2) ON CONFLICT DO NOTHING", profile, st.Current())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var name string
		if err := pool.QueryRow(ctx, "SELECT profile FROM reporting_s01.profile WHERE singleton").Scan(&name); err != nil {
			return err
		}
		if name != profile {
			return fmt.Errorf("the profile is already %q: %w", name, r.ErrConflict)
		}
	}
	return nil
}

const (
	ProfileFixture    = "contract-simulator-only"
	ProfileProduction = "production"
)

// ApplyStepForTests runs one step as a stale migrator would, for the qualification tests.
func ApplyStepForTests(ctx context.Context, pool *pgxpool.Pool, version int, up bool) error {
	known, err := Migrations()
	if err != nil {
		return err
	}
	return step(ctx, pool, known[version-1], up, "test", true)
}
