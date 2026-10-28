package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"slices"
	"strings"
	"time"
)

// The production opening path (S04-A). It is a mechanism with its qualification, not production
// admission: the demo server does not use it.

// ProductionConfig opens a store for a deployment: any database but the fixture ones, TLS
// verified against the server's certificate, the production profile row, the schema at the
// binary's version, and a connected role that can only read and write rows.
type ProductionConfig struct {
	DSN string
	// InsecureTestOnly lifts the TLS and database-name rules; it exists so that the role and
	// schema rules can be qualified on the throwaway test server, and nowhere else.
	InsecureTestOnly bool
}

var fixtureDatabases = []string{"reporting_test_s01", "reporting_test_s01_race"}

// OpenProduction verifies the DSN, the schema, the profile and the role, in that order; any
// failure is a refusal to start.
func OpenProduction(ctx context.Context, cfg ProductionConfig) (*Store, error) {
	c, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, r.ErrInvalid
	}
	if !cfg.InsecureTestOnly {
		if slices.Contains(fixtureDatabases, c.ConnConfig.Database) {
			return nil, fmt.Errorf("a fixture database is not a production store: %w", r.ErrDenied)
		}
		if c.ConnConfig.TLSConfig == nil || c.ConnConfig.TLSConfig.InsecureSkipVerify || c.ConnConfig.TLSConfig.ServerName == "" {
			return nil, fmt.Errorf("production stores require sslmode=verify-full: %w", r.ErrDenied)
		}
	}
	c.MaxConns = 8
	c.MinConns = 0
	c.ConnConfig.ConnectTimeout = 3 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Store, error) { pool.Close(); return nil, err }
	if err := Verify(ctx, pool, ProfileProduction); err != nil {
		return fail(err)
	}
	if problems, err := RoleProblems(ctx, pool); err != nil {
		return fail(err)
	} else if len(problems) > 0 {
		return fail(fmt.Errorf("the connected role is not the application role: %s: %w", strings.Join(problems, "; "), r.ErrDenied))
	}
	return &Store{pool: pool}, nil
}

// dataTables are the tables the application role reads and writes; the ledger and the profile
// it only reads.
var dataTables = []string{"jobs", "assets", "issue_numbers"}
var readTables = []string{"profile", "schema_migrations"}

// RoleProblems lists every way the connected role exceeds or lacks the application role's
// privileges: superuser, role or database creation, RLS bypass, schema ownership or CREATE on
// the schema, DELETE or TRUNCATE on any table, DDL ownership of any table, or a missing SELECT,
// INSERT or UPDATE on a data table.
func RoleProblems(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var problems []string
	var super, createRole, createDB, bypassRLS bool
	if err := pool.QueryRow(ctx, "SELECT rolsuper,rolcreaterole,rolcreatedb,rolbypassrls FROM pg_roles WHERE rolname=current_user").Scan(&super, &createRole, &createDB, &bypassRLS); err != nil {
		return nil, err
	}
	for name, v := range map[string]bool{"superuser": super, "role creation": createRole, "database creation": createDB, "row security bypass": bypassRLS} {
		if v {
			problems = append(problems, name)
		}
	}
	var ownsSchema, createOnSchema bool
	if err := pool.QueryRow(ctx, "SELECT pg_catalog.pg_get_userbyid(nspowner)=current_user, has_schema_privilege(current_user,'reporting_s01','CREATE') FROM pg_namespace WHERE nspname='reporting_s01'").Scan(&ownsSchema, &createOnSchema); err != nil {
		return nil, err
	}
	if ownsSchema {
		problems = append(problems, "owns the schema")
	}
	if createOnSchema {
		problems = append(problems, "CREATE on the schema")
	}
	rows, err := pool.Query(ctx, `SELECT c.relname, pg_catalog.pg_get_userbyid(c.relowner)=current_user,
has_table_privilege(current_user,c.oid,'SELECT'), has_table_privilege(current_user,c.oid,'INSERT'), has_table_privilege(current_user,c.oid,'UPDATE'),
has_table_privilege(current_user,c.oid,'DELETE'), has_table_privilege(current_user,c.oid,'TRUNCATE')
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='reporting_s01' AND c.relkind='r' ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		var owns, sel, ins, upd, del, trunc bool
		if err := rows.Scan(&name, &owns, &sel, &ins, &upd, &del, &trunc); err != nil {
			return nil, err
		}
		seen[name] = true
		if owns {
			problems = append(problems, "owns table "+name)
		}
		if del || trunc {
			problems = append(problems, "DELETE or TRUNCATE on "+name)
		}
		if !sel {
			problems = append(problems, "no SELECT on "+name)
		}
		if slices.Contains(dataTables, name) && (!ins || !upd) {
			problems = append(problems, "no INSERT and UPDATE on "+name)
		}
		if slices.Contains(readTables, name) && (ins || upd) {
			problems = append(problems, "INSERT or UPDATE on "+name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range append(slices.Clone(dataTables), readTables...) {
		if !seen[t] {
			problems = append(problems, "table "+t+" is missing")
		}
	}
	slices.Sort(problems)
	return problems, nil
}

// Grants gives the two roles exactly the privileges A3 describes: the migrator owns the schema
// and every table; the application role gets USAGE on the schema, SELECT on every table and
// INSERT and UPDATE on the data tables, nothing else; PUBLIC loses CREATE on the schema. Both
// roles must exist (their login and passwords are the operator's). It runs as a role that may
// alter ownership (the current owner or a superuser) and is idempotent.
func Grants(ctx context.Context, pool *pgxpool.Pool, migrator, app string) error {
	if !validRole(migrator) || !validRole(app) || migrator == app {
		return r.ErrInvalid
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	q := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	stmts := []string{
		"ALTER SCHEMA reporting_s01 OWNER TO " + q(migrator),
		"REVOKE ALL ON SCHEMA reporting_s01 FROM PUBLIC",
		"REVOKE ALL ON ALL TABLES IN SCHEMA reporting_s01 FROM PUBLIC",
		"REVOKE ALL ON ALL TABLES IN SCHEMA reporting_s01 FROM " + q(app),
		"GRANT USAGE ON SCHEMA reporting_s01 TO " + q(app),
		"GRANT SELECT ON ALL TABLES IN SCHEMA reporting_s01 TO " + q(app),
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + q(migrator) + " IN SCHEMA reporting_s01 REVOKE ALL ON TABLES FROM " + q(app),
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + q(migrator) + " IN SCHEMA reporting_s01 GRANT SELECT ON TABLES TO " + q(app),
	}
	for _, t := range dataTables {
		stmts = append(stmts, "GRANT INSERT, UPDATE ON reporting_s01."+t+" TO "+q(app))
	}
	rows, err := tx.Query(ctx, "SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='reporting_s01' AND c.relkind='r'")
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	owners := make([]string, 0, len(tables))
	for _, t := range tables {
		owners = append(owners, "ALTER TABLE reporting_s01."+t+" OWNER TO "+q(migrator))
	}
	for _, s := range append(owners, stmts...) {
		if _, err := tx.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return tx.Commit(ctx)
}

func validRole(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
