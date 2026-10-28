// reporting-migrate is the operator's schema tool for the platform store (S04-A): status, plan,
// up, down, roles and init-profile on the store named by REPORTING_STORE_DSN. It never runs
// inside the server and never touches a source product database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"io"
	"os"
	"os/signal"
	"os/user"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "reporting-migrate:", err)
		os.Exit(1)
	}
}

func usage() error {
	return errors.New(`usage: reporting-migrate <status|plan [--to N]|up [--to N]|down --to N [--accept-data-loss]|roles --migrator-role NAME --app-role NAME|init-profile <production|contract-simulator-only>>
the store is REPORTING_STORE_DSN; up/down/roles/init-profile run as the migrator role (or an owner)`)
}

func run(args []string, out io.Writer) error {
	if len(args) < 1 {
		return usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("REPORTING_STORE_DSN")
	if dsn == "" {
		return errors.New("set REPORTING_STORE_DSN")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("REPORTING_STORE_DSN: %v: %w", err, r.ErrInvalid)
	}
	cfg.MaxConns = 2
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	by := "reporting-migrate"
	if u, err := user.Current(); err == nil && u.Username != "" {
		by += ":" + u.Username
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(out)
	to := fs.Int("to", 0, "target version (up: 0 means the latest)")
	accept := fs.Bool("accept-data-loss", false, "down: discard the rows a step cannot keep")
	migrator := fs.String("migrator-role", "", "roles: the role that owns the schema and runs migrations")
	app := fs.String("app-role", "", "roles: the role the server connects as")
	timeout := fs.Duration("timeout", 5*time.Minute, "the whole run's time limit (002 rewrites every row; size it for the store)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	switch args[0] {
	case "status":
		st, err := postgres.Status(ctx, pool)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "embedded: %d steps (latest %03d)\n", len(st.Known), st.Latest())
		for _, a := range st.Applied {
			when := "-"
			if !a.AppliedAt.IsZero() {
				when = a.AppliedAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(out, "applied:  %03d %-24s %s by %s (%.12s)\n", a.Version, a.Name, when, a.AppliedBy, a.Checksum)
		}
		if st.Legacy {
			fmt.Fprintln(out, "ledger:   none; the profile's version was taken as applied (legacy); up will record it")
		}
		for _, v := range st.Pending {
			fmt.Fprintf(out, "pending:  %03d %s\n", v, st.Known[v-1].Name)
		}
		for _, p := range st.Problems {
			fmt.Fprintf(out, "problem:  %s\n", p)
		}
		if problems, err := postgres.RoleProblems(ctx, pool); err == nil {
			if len(problems) == 0 {
				fmt.Fprintln(out, "role:     the connected role satisfies the application role's rules")
			} else {
				fmt.Fprintf(out, "role:     the connected role is not the application role (%d rules unmet; expected for the migrator, the server must connect as the application role): %s\n", len(problems), strings.Join(problems, "; "))
			}
		}
		if len(st.Problems) > 0 {
			return r.ErrIntegrity
		}
		return nil
	case "plan":
		st, err := postgres.Status(ctx, pool)
		if err != nil {
			return err
		}
		target := *to
		dir := postgres.Either
		if target == 0 && !isFlagSet(fs, "to") {
			target = st.Latest()
		}
		steps, err := postgres.Plan(st, target, dir)
		if err != nil {
			return err
		}
		if len(steps) == 0 {
			fmt.Fprintf(out, "nothing to do: at version %03d\n", st.Current())
		}
		for _, s := range steps {
			fmt.Fprintln(out, s)
		}
		return nil
	case "up":
		if err := postgres.Up(ctx, pool, *to, by); err != nil {
			return err
		}
		st, err := postgres.Status(ctx, pool)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "at version %03d\n", st.Current())
		return nil
	case "down":
		if !isFlagSet(fs, "to") {
			return errors.New("down needs an explicit --to N")
		}
		if err := postgres.Down(ctx, pool, *to, by, *accept); err != nil {
			return err
		}
		st, err := postgres.Status(ctx, pool)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "at version %03d\n", st.Current())
		return nil
	case "roles":
		if *migrator == "" || *app == "" {
			return errors.New("roles needs --migrator-role and --app-role (lowercase letters, digits and _, at most 63 characters, different)")
		}
		if err := postgres.Grants(ctx, pool, *migrator, *app); err != nil {
			return err
		}
		fmt.Fprintf(out, "schema and tables owned by %s; %s may read every table and write the data tables\n", *migrator, *app)
		return nil
	case "init-profile":
		if fs.NArg() != 1 || (fs.Arg(0) != postgres.ProfileProduction && fs.Arg(0) != postgres.ProfileFixture) {
			return fmt.Errorf("init-profile takes %s or %s", postgres.ProfileProduction, postgres.ProfileFixture)
		}
		if err := postgres.InitProfile(ctx, pool, fs.Arg(0)); err != nil {
			return err
		}
		fmt.Fprintf(out, "profile %s\n", fs.Arg(0))
		return nil
	}
	return usage()
}

func isFlagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
