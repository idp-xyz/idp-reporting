// Command capacity measures the platform's PostgreSQL store at sizes you choose, before and after migration 009 (S04-G, docs/CAPACITY.md).
// It seeds only the guarded fixture database (REPORTING_S01_TEST_DSN, loopback, never port 55432) and removes what it seeded. Run it through
// scripts/capacity_run.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"idp.local/reporting/internal/capacity"
	"os"
	"strconv"
	"strings"
)

func main() {
	var sizes, phases string
	var growth bool
	c := capacity.Config{Out: os.Stdout}
	flag.StringVar(&sizes, "sizes", "10000,200000,1000000", "job rows of each size, comma separated")
	flag.StringVar(&phases, "phases", "before,after", "before (migration 009 reverted), after, or both")
	flag.IntVar(&c.Identities, "identities", 1000, "identities the history is spread over (identity 0 is the one with a long history)")
	flag.IntVar(&c.Tenants, "tenants", 10, "tenants the identities are spread over")
	flag.IntVar(&c.HotPercent, "hot-percent", 10, "share of the rows held by identity 0")
	flag.IntVar(&c.AssetsPercent, "assets-percent", 50, "asset rows as a percent of the job rows")
	flag.IntVar(&c.Days, "days", 90, "span of the history in days")
	flag.IntVar(&c.Ops, "ops", 300, "operations per measured case")
	flag.IntVar(&c.WriterOps, "writer-ops", 25, "admissions per writer in the parallel cases (32 writers on one identity must stay under the 1000 jobs-in-flight bound)")
	flag.IntVar(&c.SweepRows, "sweep-rows", 2000, "expired artifacts (and their jobs) one sweep is measured over")
	flag.IntVar(&c.VerifyRows, "verify-rows", 2000, "artifact rows and objects verify-objects is measured over (needs the object store variables)")
	flag.BoolVar(&growth, "growth", false, "instead of the sizes: run the real service and print what a job, a snapshot and an artifact row weigh (G4)")
	flag.Parse()
	c.DSN = os.Getenv("REPORTING_S01_TEST_DSN")
	c.ObjectEndpoint = os.Getenv("REPORTING_OBJECT_STORE_TEST_ENDPOINT")
	c.ObjectAccess = os.Getenv("REPORTING_OBJECT_STORE_TEST_ACCESS_KEY")
	c.ObjectSecret = os.Getenv("REPORTING_OBJECT_STORE_TEST_SECRET_KEY")
	for _, s := range strings.Split(sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1000 {
			fmt.Fprintln(os.Stderr, "bad -sizes")
			os.Exit(2)
		}
		c.Sizes = append(c.Sizes, n)
	}
	c.Phases = strings.Split(phases, ",")
	if c.DSN == "" {
		fmt.Fprintln(os.Stderr, "set REPORTING_S01_TEST_DSN (the guarded fixture database)")
		os.Exit(2)
	}
	if growth {
		if err := capacity.Growth(context.Background(), c); err != nil {
			fmt.Fprintln(os.Stderr, "capacity:", err)
			os.Exit(1)
		}
		return
	}
	if err := capacity.Run(context.Background(), c); err != nil {
		fmt.Fprintln(os.Stderr, "capacity:", err)
		os.Exit(1)
	}
}
