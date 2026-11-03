package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"idp.local/reporting/storage/postgres"
)

// Main is the whole of a host's main: `server.Main(os.Args, myRegistry)` (docs/DEPLOYMENT.md). It runs `check-config`, `run` or `version`,
// handles SIGTERM and SIGINT (drain) and SIGHUP (reload of the issuer files), and exits the process with the code of the result: 0 for a
// clean drain or a successful check, 1 for a refused start or a failed check, 2 for a drain that ran out of shutdown_grace.
func Main(args []string, reg Registry) {
	os.Exit(MainWith(args, reg))
}

// MainWith is Main without the exit, with options (the test host of the qualification passes the test-only ones; the shipped command passes none).
func MainWith(args []string, reg Registry, opts ...Option) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	return Execute(ctx, args, reg, os.Getenv, os.Stdout, os.Stderr, hup, opts...)
}

const usage = `usage: reporting-server <command>
  check-config -config FILE   parse and validate the configuration and the files it names, offline; prints counts, never a value
  run -config FILE            run the server (secrets: REPORTING_STORE_DSN and, with an object store, REPORTING_OBJECT_STORE_ACCESS_KEY and _SECRET_KEY)
  version                     print the binary's version and the schema version it needs
exit codes of run: 0 a clean drain, 1 a refused start, 2 a drain that ran out of shutdown_grace`

// Execute is Main for a caller that owns the context, the environment, the output streams and the SIGHUP channel: it returns the exit code.
// Cancelling ctx begins the drain.
func Execute(ctx context.Context, args []string, reg Registry, getenv func(string) string, stdout, stderr io.Writer, hup <-chan os.Signal, opts ...Option) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, usage)
		return 1
	}
	switch args[1] {
	case "version":
		known, err := postgres.Migrations()
		if err != nil || len(known) == 0 {
			fmt.Fprintln(stderr, "reporting-server: the embedded migrations cannot be read")
			return 1
		}
		fmt.Fprintf(stdout, "reporting-server %s schema %d\n", Version(), known[len(known)-1].Version)
		return 0
	case "check-config", "run":
	default:
		fmt.Fprintln(stderr, usage)
		return 1
	}
	fs := flag.NewFlagSet(args[1], flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "the configuration file")
	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}
	if fs.NArg() != 0 || *path == "" {
		fmt.Fprintln(stderr, "reporting-server: "+args[1]+" takes -config FILE and nothing else")
		return 1
	}
	cfg, err := LoadConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, "reporting-server:", err)
		return 1
	}
	if args[1] == "check-config" {
		report, err := Check(cfg, reg, getenv)
		if err != nil {
			fmt.Fprintln(stderr, "reporting-server: check-config:", err)
			return 1
		}
		report.Print(stdout)
		return 0
	}
	s, err := Start(ctx, cfg, reg, getenv, stderr, opts...)
	if err != nil {
		var se *StartError
		if errors.As(err, &se) {
			fmt.Fprintln(stderr, err)
		} else {
			fmt.Fprintln(stderr, "reporting-server: the server could not start")
		}
		return 1
	}
	stopHUP := make(chan struct{})
	hupDone := make(chan struct{})
	go func() {
		defer close(hupDone)
		for {
			select {
			case <-hup:
				_ = s.Reload() // its outcome is logged by its code
			case <-s.Done():
				return
			case <-stopHUP:
				return
			}
		}
	}()
	err = s.Wait()
	close(stopHUP)
	<-hupDone
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrDrainTimeout):
		fmt.Fprintln(stderr, "reporting-server: the drain ran out of shutdown_grace; a job still running keeps its lease and is claimed by another process after it runs out")
		return 2
	}
	fmt.Fprintln(stderr, "reporting-server: the server stopped on an internal error")
	return 1
}
