package capacity

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"runtime"
	"strconv"
	"strings"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
func numCPU() int                       { return runtime.NumCPU() }

func pgxCfg(dsn string) (*pgxpool.Config, error) { return pgxpool.ParseConfig(dsn) }

func dsnPort(dsn string) (int, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Port())
}

type roleSet struct {
	admin      *pgxpool.Pool
	app, sweep string
}

func (r roleSet) dsn(base, role string) string {
	return strings.Replace(base, "postgres@", role+"@", 1)
}

// makeRoles creates the two login roles the measurements run as, so that the row-level policies apply to them as they do in production: the
// application role (SELECT, INSERT, UPDATE on the data tables) and the sweep role (UPDATE and DELETE on assets, UPDATE on jobs, INSERT on the
// sweeps ledger). They own nothing and are dropped when the run ends.
func makeRoles(ctx context.Context, admin *pgxpool.Pool) (roleSet, error) {
	rs := roleSet{admin: admin, app: "reporting_cap_s04g_app", sweep: "reporting_cap_s04g_sweep"}
	for _, role := range []string{rs.app, rs.sweep} {
		if _, err := admin.Exec(ctx, "DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role); err != nil && !strings.Contains(err.Error(), "does not exist") {
			return rs, err
		}
		if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN"); err != nil {
			return rs, err
		}
	}
	for _, g := range []string{
		"GRANT USAGE ON SCHEMA reporting_s01 TO " + rs.app + ", " + rs.sweep,
		"GRANT SELECT ON ALL TABLES IN SCHEMA reporting_s01 TO " + rs.app + ", " + rs.sweep,
		"GRANT INSERT, UPDATE ON reporting_s01.jobs, reporting_s01.assets TO " + rs.app,
		"GRANT UPDATE, DELETE ON reporting_s01.assets TO " + rs.sweep,
		"GRANT UPDATE ON reporting_s01.jobs TO " + rs.sweep,
		"GRANT INSERT ON reporting_s01.sweeps TO " + rs.sweep,
	} {
		if _, err := admin.Exec(ctx, g); err != nil {
			return rs, fmt.Errorf("%s: %w", g, err)
		}
	}
	return rs, nil
}

func (r roleSet) drop() {
	for _, role := range []string{r.app, r.sweep} {
		_, _ = r.admin.Exec(context.Background(), "DROP OWNED BY "+role+"; DROP ROLE IF EXISTS "+role)
	}
}

// noParallel turns off parallel query and parallel maintenance for the tool's sessions: the throwaway container's /dev/shm is 64 MB (Docker's
// default) and a parallel plan over a million rows asks for more. The measurements are of serial plans, which is also what an admission is.
func noParallel(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "max_parallel_workers_per_gather=0&max_parallel_maintenance_workers=0"
}
