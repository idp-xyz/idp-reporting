// reporting-source-check checks a source product's implementation of the reporting source protocol (S19-A, docs/SOURCE_HTTP_PROTOCOL.md)
// through the platform's own adapter: what passes here is what the platform's adapter `http` accepts. It prints one line per rule, PASS,
// FAIL or SKIP with a fixed text, and exits 0 when every rule passed or was skipped, 1 when one failed, 2 for a usage error. It never prints
// the credential, a URL or a byte of the source's answers.
//
//	reporting-source-check -base-url https://product.internal -auth-env PRODUCT_REPORTING_AUTH -ca-file ca.pem \
//	    -context context.json -query query.json [-client-cert-file c.pem -client-key-file c.key] [-issue] [-companies] [-loopback-http] [-timeout 10]
//
// context.json is the protocol's context object (platform_tenant, issuer, subject, integration, source_tenant) of a subject the product has
// granted; query.json a query of the platform (docs/API.md) inside that grant, with at least one row. The rules F1–F6 (S20-A, ADR 0023)
// exercise every filter and sort the query's dataset declares, with values of the query's own rows, and the refusal of what it does not
// declare; a dataset that declares nothing skips F1, F2, F4 and F5. -companies checks the company directory (ADR 0015, S19-B) as a
// connection with `companies: true` would use it.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/conformance"
	"idp.local/reporting/adapters/httpsource/protocol"
	r "idp.local/reporting/reporting"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reporting-source-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o httpsource.Options
	var contextFile, queryFile string
	var issue, companies bool
	fs.StringVar(&o.BaseURL, "base-url", "", "the source's base URL (https://, or http:// to a loopback IP with -loopback-http)")
	fs.StringVar(&o.AuthEnv, "auth-env", "", "the name of the environment variable that holds the bearer credential")
	fs.StringVar(&o.CAFile, "ca-file", "", "a PEM file of the CA that vouches for the source (absolute path); default: the system's roots")
	fs.StringVar(&o.ClientCertFile, "client-cert-file", "", "the platform's client certificate for mTLS (absolute path)")
	fs.StringVar(&o.ClientKeyFile, "client-key-file", "", "its private key (absolute path, mode 0600 or 0400)")
	fs.BoolVar(&o.LoopbackHTTP, "loopback-http", false, "allow http:// to a loopback IP address (tests only)")
	fs.IntVar(&o.Timeout, "timeout", 10, "seconds for one request, 1..60")
	fs.StringVar(&contextFile, "context", "", "a JSON file of the protocol's context object of a granted subject")
	fs.StringVar(&queryFile, "query", "", "a JSON file of a query inside that grant")
	fs.BoolVar(&issue, "issue", false, "also check the document authority: reserves one number and voids it (never against production)")
	fs.BoolVar(&companies, "companies", false, "also check the company directory (ADR 0015), as a connection with companies: true would use it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.BaseURL == "" || contextFile == "" || queryFile == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "reporting-source-check: -base-url, -context and -query are required")
		return 2
	}
	cfg, err := o.Config(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "reporting-source-check:", err)
		return 2
	}
	var wire protocol.Context
	if err := readJSON(contextFile, &wire); err != nil || !wire.Valid() {
		fmt.Fprintln(stderr, "reporting-source-check: -context must be one JSON object with platform_tenant, issuer, subject, integration and source_tenant")
		return 2
	}
	var q r.QuerySpec
	if err := readJSON(queryFile, &q); err != nil {
		fmt.Fprintln(stderr, "reporting-source-check: -query must be one JSON object of a query (docs/API.md)")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results := conformance.Run(ctx, conformance.Target{Config: cfg, Context: wire.Source(), Query: q, Issue: issue, Companies: companies})
	failed := 0
	for _, x := range results {
		fmt.Fprintln(stdout, x.Line())
		if !x.Pass && !x.Skip {
			failed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(stdout, "reporting-source-check: %d of %d rules failed\n", failed, len(results))
		return 1
	}
	fmt.Fprintf(stdout, "reporting-source-check: every rule passed (%d)\n", len(results))
	return 0
}

func readJSON(name string, out any) error {
	b, err := os.ReadFile(name)
	if err != nil || len(b) > 64<<10 {
		return os.ErrInvalid
	}
	return r.DecodeStrict(bytes.NewReader(b), out)
}
