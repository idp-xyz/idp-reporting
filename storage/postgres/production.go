package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
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
		return nil, fmt.Errorf("%v: %w", err, r.ErrInvalid)
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

// dataTables are the tables the application role reads and writes; the ledger, the profile and the sweeps ledger (S04-B) it only reads.
// issue_numbers, S03-G's counters, exists at version 3 only (004 drops it): where it exists it is a data table, and the schemas older than
// the latest stay serviceable by roles. sweeps exists from version 6.
var dataTables = []string{"jobs", "assets", "issue_numbers"}
var readTables = []string{"profile", "schema_migrations", "sweeps"}

// requiredTables are the tables a store at the latest version has for the application role; issue_numbers is not one, and sweeps is the
// sweep role's (the application role never writes it and a store older than 006 still serves the rest).
var requiredTables = []string{"jobs", "assets", "profile", "schema_migrations"}

// A role's rules are what it may do to each table beyond reading it: the application role writes the data tables (INSERT and UPDATE) and
// never deletes; the sweep role (S04-B) is the one role that deletes, and only from assets, updates the jobs (it reduces their payload)
// and writes the sweeps ledger. Nothing else for either: no TRUNCATE, no DDL, no ownership. The sweep role holds UPDATE on assets too
// because PostgreSQL requires it for the row locks (FOR UPDATE SKIP LOCKED) with which concurrent sweeps take different rows.
type writes struct{ insert, update, delete bool }

type roleRules struct {
	tables   func(table string) (writes, bool) // what the role may do to the table; false for a table the rules do not know
	required []string                          // tables a store at the latest version has for this role
}

var applicationRules = roleRules{
	tables: func(t string) (writes, bool) {
		switch {
		case slices.Contains(dataTables, t):
			return writes{insert: true, update: true}, true
		case slices.Contains(readTables, t):
			return writes{}, true
		}
		return writes{}, false
	},
	required: requiredTables,
}

var sweepRules = roleRules{
	tables: func(t string) (writes, bool) {
		switch t {
		case "assets":
			return writes{update: true, delete: true}, true
		case "jobs":
			return writes{update: true}, true
		case "sweeps":
			return writes{insert: true}, true
		}
		_, known := applicationRules.tables(t)
		return writes{}, known
	},
	required: []string{"jobs", "assets", "profile", "schema_migrations", "sweeps"},
}

// RoleProblems lists every way the connected role exceeds or lacks the application role's
// privileges: superuser, role or database creation, RLS bypass, schema ownership or CREATE on
// the schema, DELETE or TRUNCATE on any table, DDL ownership of any table, or a missing SELECT,
// INSERT or UPDATE on a data table.
func RoleProblems(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	return roleProblems(ctx, pool, applicationRules)
}

// SweeperProblems is RoleProblems for the sweep role of S04-B (reporting-migrate roles --sweep-role): the same attribute, ownership and
// schema rules, and on the tables UPDATE and DELETE on assets, UPDATE on jobs and INSERT on sweeps, nothing else beyond SELECT.
func SweeperProblems(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	return roleProblems(ctx, pool, sweepRules)
}

func roleProblems(ctx context.Context, pool *pgxpool.Pool, rules roleRules) ([]string, error) {
	var problems []string
	var super, createRole, createDB, bypassRLS, replication, sameSession, ownsDB, createOnDB bool
	var memberships int
	if err := pool.QueryRow(ctx, `SELECT rolsuper,rolcreaterole,rolcreatedb,rolbypassrls,rolreplication, session_user=current_user,
(SELECT count(*) FROM pg_auth_members m WHERE m.member=r.oid),
(SELECT pg_has_role(current_user, d.datdba, 'USAGE') FROM pg_database d WHERE d.datname=current_database()),
has_database_privilege(current_user, current_database(), 'CREATE')
FROM pg_roles r WHERE rolname=current_user`).Scan(&super, &createRole, &createDB, &bypassRLS, &replication, &sameSession, &memberships, &ownsDB, &createOnDB); err != nil {
		return nil, err
	}
	for name, v := range map[string]bool{"superuser": super, "role creation": createRole, "database creation": createDB, "row security bypass": bypassRLS, "replication": replication, "SET ROLE in effect": !sameSession,
		"owns the database": ownsDB, "CREATE on the database": createOnDB} {
		if v {
			problems = append(problems, name)
		}
	}
	if memberships > 0 {
		// A member of another role can SET ROLE to it (or inherits its privileges): the
		// application role is a member of nothing.
		problems = append(problems, fmt.Sprintf("member of %d roles", memberships))
	}
	var ownsSchema, createOnSchema bool
	if err := pool.QueryRow(ctx, "SELECT pg_has_role(current_user,nspowner,'USAGE'), has_schema_privilege(current_user,'reporting_s01','CREATE') FROM pg_namespace WHERE nspname='reporting_s01'").Scan(&ownsSchema, &createOnSchema); err != nil {
		return nil, err
	}
	if ownsSchema {
		problems = append(problems, "owns the schema")
	}
	if createOnSchema {
		problems = append(problems, "CREATE on the schema")
	}
	rows, err := pool.Query(ctx, `SELECT c.relname, pg_has_role(current_user,c.relowner,'USAGE'),
has_table_privilege(current_user,c.oid,'SELECT'), has_table_privilege(current_user,c.oid,'INSERT'), has_table_privilege(current_user,c.oid,'UPDATE'),
has_table_privilege(current_user,c.oid,'DELETE'), has_table_privilege(current_user,c.oid,'TRUNCATE'),
EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped AND a.attacl IS NOT NULL)
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='reporting_s01' AND c.relkind='r' ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		var owns, sel, ins, upd, del, trunc, columnACL bool
		if err := rows.Scan(&name, &owns, &sel, &ins, &upd, &del, &trunc, &columnACL); err != nil {
			return nil, err
		}
		if columnACL {
			// A column-level grant is invisible to the table-level probes and may reach the
			// profile or the ledger: none is expected on any table.
			problems = append(problems, "column-level privileges on "+name)
		}
		seen[name] = true
		want, known := rules.tables(name)
		if !known {
			problems = append(problems, "unexpected table "+name)
		}
		if owns {
			problems = append(problems, "owns table "+name)
		}
		if del && !want.delete || trunc {
			problems = append(problems, "DELETE or TRUNCATE on "+name)
		}
		if !sel {
			problems = append(problems, "no SELECT on "+name)
		}
		if want.insert && want.update && (!ins || !upd) {
			problems = append(problems, "no INSERT and UPDATE on "+name)
		} else {
			if want.insert && !ins {
				problems = append(problems, "no INSERT on "+name)
			}
			if want.update && !upd {
				problems = append(problems, "no UPDATE on "+name)
			}
		}
		if want.delete && !del {
			problems = append(problems, "no DELETE on "+name)
		}
		if ins && !want.insert || upd && !want.update {
			problems = append(problems, "INSERT or UPDATE on "+name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range rules.required {
		if !seen[t] {
			problems = append(problems, "table "+t+" is missing")
		}
	}
	slices.Sort(problems)
	return problems, nil
}

// tablesToGrant lists the tables of the schema, refusing a schema that holds one the binary does not know: only the tables the binary
// knows are touched, and every identifier is quoted, so that a table planted in the schema under a name that is SQL is reported, never
// executed.
func tablesToGrant(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, "SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='reporting_s01' AND c.relkind='r' ORDER BY 1")
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		if !slices.Contains(dataTables, name) && !slices.Contains(readTables, name) {
			rows.Close()
			return nil, fmt.Errorf("unexpected table %q in the schema: %w", name, r.ErrIntegrity)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
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
	q := func(name string) string { return pgx.Identifier{name}.Sanitize() }
	table := func(name string) string { return pgx.Identifier{"reporting_s01", name}.Sanitize() }
	tables, err := tablesToGrant(ctx, tx)
	if err != nil {
		return err
	}
	stmts := []string{"ALTER SCHEMA reporting_s01 OWNER TO " + q(migrator)}
	for _, t := range tables {
		stmts = append(stmts, "ALTER TABLE "+table(t)+" OWNER TO "+q(migrator))
	}
	stmts = append(stmts,
		"REVOKE ALL ON SCHEMA reporting_s01 FROM PUBLIC",
		"REVOKE ALL ON SCHEMA reporting_s01 FROM "+q(app),
		"REVOKE ALL ON ALL TABLES IN SCHEMA reporting_s01 FROM PUBLIC",
		"REVOKE ALL ON ALL TABLES IN SCHEMA reporting_s01 FROM "+q(app),
		"GRANT USAGE ON SCHEMA reporting_s01 TO "+q(app),
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+q(migrator)+" IN SCHEMA reporting_s01 REVOKE ALL ON TABLES FROM "+q(app),
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+q(migrator)+" IN SCHEMA reporting_s01 GRANT SELECT ON TABLES TO "+q(app),
	)
	for _, t := range tables {
		stmts = append(stmts, "GRANT SELECT ON "+table(t)+" TO "+q(app))
		if slices.Contains(dataTables, t) {
			stmts = append(stmts, "GRANT INSERT, UPDATE ON "+table(t)+" TO "+q(app))
		}
	}
	return execAll(ctx, tx, stmts)
}

// GrantSweeper gives the sweep role of S04-B exactly its privileges (see sweepRules): USAGE on the schema, SELECT on every table, UPDATE and
// DELETE on assets, UPDATE on jobs and INSERT on the sweeps ledger, nothing else. It is the one role that deletes, and the only deletion the
// platform's roles hold: the application role of A3 keeps no DELETE. The role must exist, be neither the migrator nor the application
// role, and the tables must already be the migrator's (run Grants first, as the owner or a superuser). It is idempotent. The migrator
// can sweep without this role; a deployment that keeps the migrator's credentials off its scheduler creates it.
func GrantSweeper(ctx context.Context, pool *pgxpool.Pool, migrator, app, sweeper string) error {
	if !validRole(migrator) || !validRole(app) || !validRole(sweeper) || migrator == app || migrator == sweeper || app == sweeper {
		return r.ErrInvalid
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	q := func(name string) string { return pgx.Identifier{name}.Sanitize() }
	table := func(name string) string { return pgx.Identifier{"reporting_s01", name}.Sanitize() }
	tables, err := tablesToGrant(ctx, tx)
	if err != nil {
		return err
	}
	stmts := []string{
		"REVOKE ALL ON SCHEMA reporting_s01 FROM " + q(sweeper),
		"REVOKE ALL ON ALL TABLES IN SCHEMA reporting_s01 FROM " + q(sweeper),
		"GRANT USAGE ON SCHEMA reporting_s01 TO " + q(sweeper),
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + q(migrator) + " IN SCHEMA reporting_s01 REVOKE ALL ON TABLES FROM " + q(sweeper),
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + q(migrator) + " IN SCHEMA reporting_s01 GRANT SELECT ON TABLES TO " + q(sweeper),
	}
	for _, t := range tables {
		stmts = append(stmts, "GRANT SELECT ON "+table(t)+" TO "+q(sweeper))
		switch t {
		case "assets":
			stmts = append(stmts, "GRANT UPDATE, DELETE ON "+table(t)+" TO "+q(sweeper))
		case "jobs":
			stmts = append(stmts, "GRANT UPDATE ON "+table(t)+" TO "+q(sweeper))
		case "sweeps":
			stmts = append(stmts, "GRANT INSERT ON "+table(t)+" TO "+q(sweeper))
		}
	}
	return execAll(ctx, tx, stmts)
}

func execAll(ctx context.Context, tx pgx.Tx, stmts []string) error {
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return tx.Commit(ctx)
}

// ValidRole says whether a name is one the roles step accepts: lowercase letters, digits and _, at most 63 characters.
func ValidRole(name string) bool { return validRole(name) }

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
