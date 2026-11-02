// reporting-migrate is the operator's schema tool for the platform store (S04-A): status, plan,
// up, down, roles and init-profile on the store named by REPORTING_STORE_DSN, and the retention
// sweep (S04-B). It never runs inside the server and never touches a source product database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
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
	return errors.New(`usage: reporting-migrate <status|plan [--to N]|up [--to N]|down --to N [--accept-data-loss]|roles --migrator-role NAME --app-role NAME [--sweep-role NAME]|init-profile <production|contract-simulator-only>|sweep [--grace D] [--batch N] [--dry-run] [--object-store-endpoint URL --object-store-bucket NAME [--object-store-region R] [--object-store-ca FILE] [--object-store-loopback-http]]>
the store is REPORTING_STORE_DSN; up/down/roles/init-profile run as the migrator role (or an owner); sweep runs as the sweep role or the migrator;
sweep deletes the objects of the artifacts it deletes, and reconciles the bucket, when the object store flags are given (the keys are in
REPORTING_OBJECT_STORE_ACCESS_KEY and REPORTING_OBJECT_STORE_SECRET_KEY, never flags); without them it deletes rows only`)
}

func run(args []string, out io.Writer) error {
	if len(args) < 1 {
		return usage()
	}
	// The keys of the object store are the environment's alone (S04-E): a flag for either is refused before anything is read or opened.
	if err := objects.RefuseCredentialFlags(args[1:]); err != nil {
		return err
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
	sweeper := fs.String("sweep-role", "", "roles: optional third role that runs the retention sweep (the only one that deletes; the application role never does)")
	grace := fs.Duration("grace", r.DefaultSweepGrace, "sweep: how long after its expiry an artifact or a snapshot is kept")
	batch := fs.Int("batch", r.DefaultSweepBatch, "sweep: rows per transaction")
	dryRun := fs.Bool("dry-run", false, "sweep: count what would be deleted, delete nothing")
	objectStore := objects.Register(fs)
	timeout := fs.Duration("timeout", 5*time.Minute, "the whole run's time limit (002 rewrites every row; size it for the store)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *timeout <= 0 {
		return errors.New("--timeout must be positive")
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
		if problems, err := postgres.SweeperProblems(ctx, pool); err == nil {
			if len(problems) == 0 {
				fmt.Fprintln(out, "sweeper:  the connected role satisfies the sweep role's rules")
			} else {
				fmt.Fprintf(out, "sweeper:  the connected role is not the sweep role (%d rules unmet; expected unless this is the role that runs the sweep): %s\n", len(problems), strings.Join(problems, "; "))
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
		if *sweeper != "" {
			// Refused before anything is granted: the sweep role is a third role.
			if !postgres.ValidRole(*sweeper) || *sweeper == *migrator || *sweeper == *app {
				return fmt.Errorf("--sweep-role must be a lowercase role name (letters, digits and _, at most 63 characters) different from the others: %w", r.ErrInvalid)
			}
		}
		if err := postgres.Grants(ctx, pool, *migrator, *app); err != nil {
			return err
		}
		fmt.Fprintf(out, "schema and tables owned by %s; %s may read every table and write the data tables\n", *migrator, *app)
		if *sweeper != "" {
			if err := postgres.GrantSweeper(ctx, pool, *migrator, *app, *sweeper); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s may read every table, delete expired assets, reduce finished jobs and write the sweeps ledger, nothing else\n", *sweeper)
		}
		return nil
	case "sweep":
		// The grace is a retention period: zero or negative is an operator's slip, never "no grace", so it is refused here (the service
		// would read zero as the default).
		if *grace <= 0 || *batch <= 0 {
			return fmt.Errorf("--grace and --batch must be positive: %w", r.ErrInvalid)
		}
		// With the object store flags the sweep also deletes the files of the artifacts it deletes and reconciles the bucket; the bucket is
		// reached first, so that a sweep never deletes rows whose files it was asked to delete and then cannot.
		client, err := objectStore.Client(os.Getenv)
		if err != nil {
			return err
		}
		var bucket r.ObjectStore
		if client != nil {
			if err = objects.CheckAtStart(ctx, client); err != nil {
				return err
			}
			bucket = client
		}
		res, err := postgres.SweepWithObjects(ctx, pool, r.SweepOptions{Grace: *grace, Batch: *batch, DryRun: *dryRun}, bucket, nil)
		prefix := "swept"
		if res.DryRun {
			prefix = "dry run, would sweep"
		}
		// Counts only, also for a sweep that stopped part way: they are what it did.
		fmt.Fprintf(out, "%s: %d artifacts, %d snapshots, %d job payloads reduced (grace %s, batch %d, %d transactions)\n", prefix, res.Artifacts, res.Snapshots, res.Jobs, *grace, *batch, res.Batches)
		if bucket == nil {
			fmt.Fprintf(out, "objects: no object store configured, rows only; the files of %d deleted artifacts that were in an object store, and any orphan, wait for a reconciliation (run the sweep with the object store flags)\n", res.ObjectsLeft)
		} else {
			verb := "deleted"
			if res.DryRun {
				verb = "would be deleted"
			}
			fmt.Fprintf(out, "objects: %d %s, %d orphans %s, %d left for the next reconciliation\n", res.Objects, verb, res.Orphans, verb, res.ObjectsLeft)
		}
		return err
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
