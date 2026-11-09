// reporting-demo is explicitly limited to local contract-simulator qualification.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/pgsource"
	"idp.local/reporting/console"
	"idp.local/reporting/dashboards"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/embedding"
	"idp.local/reporting/export/pdf"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/issuance"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
	"idp.local/reporting/transport/httpapi"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}

// openFailure is what the demo says when a fixture store cannot be opened. When the error is one of
// the platform's own refusals its message is kept: it names the reason and the remedy (the
// precondition of migration 004 with its `down --to 2 --accept-data-loss`, a schema older than the
// binary's, a database that is not a fixture one) and carries nothing of the connection. Any other
// error, a connection failure for one, may carry the connection's details and is reduced to the
// generic text.
func openFailure(generic string, e error) error {
	for _, own := range []error{r.ErrDenied, r.ErrIntegrity, r.ErrInvalid, r.ErrConflict} {
		if errors.Is(e, own) {
			return fmt.Errorf("%s: %w", generic, e)
		}
	}
	return errors.New(generic)
}

// sweeper is what the worker loop sweeps with: the service.
type sweeper interface {
	Sweep(context.Context, r.SweepOptions) (r.SweepResult, error)
}

// sweepLoop runs a retention sweep (S04-B) every interval with the default grace and batch until the context ends. What it logs is counts
// only: the demo's store is the guarded fixture, whose connection may delete; a server connected as the application role cannot (it
// has no DELETE) and sweeps through `reporting-migrate sweep` instead. A sweep that fails is logged without its error, which may carry
// the connection's details, and the next one goes on; the sweeps ledger holds what was done.
func sweepLoop(ctx context.Context, s sweeper, interval time.Duration, logf func(string, ...any)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, e := s.Sweep(ctx, r.SweepOptions{})
			if e != nil && ctx.Err() != nil {
				return
			}
			// The object store's counts (S04-E) are logged only when there are some, so that a deployment without one logs what it did.
			objects := ""
			if res.Objects+res.Orphans+res.ObjectsLeft > 0 {
				objects = fmt.Sprintf("; objects: %d deleted, %d orphans deleted, %d left for the next reconciliation", res.Objects, res.Orphans, res.ObjectsLeft)
			}
			switch {
			case e != nil:
				logf("retention sweep stopped; it had deleted %d artifacts and %d snapshots and reduced %d job payloads%s", res.Artifacts, res.Snapshots, res.Jobs, objects)
			case res.Artifacts+res.Snapshots+res.Jobs+res.Objects+res.Orphans+res.ObjectsLeft > 0:
				logf("retention sweep: %d artifacts and %d snapshots deleted, %d job payloads reduced%s", res.Artifacts, res.Snapshots, res.Jobs, objects)
			}
		}
	}
}

// embedConfig reads the -embed-origin flags: the origins that may frame the embed pages of an integration, exact (https://host[:port], or
// http://127.0.0.1:port in this demo). An origin that is not one, or an integration the demo does not have, is an error, so that a typo
// stops the start instead of silently allowing nothing, or something else.
func embedConfig(values []string, integrations []string) (*embedding.Config, error) {
	config, err := embedding.Parse(values, embedding.Options{LoopbackHTTP: true})
	if err != nil {
		return nil, fmt.Errorf("-embed-origin: %w", err)
	}
	for _, integration := range config.Integrations() {
		if !slices.Contains(integrations, integration) {
			return nil, fmt.Errorf("-embed-origin: integration %q is not one of this demo's", integration)
		}
	}
	return config, nil
}

// quotaConfig reads the -quota-* flags (S04-D): zero is the default of each, a value outside its bounds stops the start with the flag's
// name and the bounds, before anything is opened.
func quotaConfig(jobs, results, admissions, queries int) (r.Quotas, error) {
	for _, f := range []struct {
		name     string
		v, bound int
	}{{"-quota-jobs", jobs, r.MaxJobsInFlight}, {"-quota-results", results, r.MaxStoredResults}, {"-quota-admissions", admissions, r.MaxAdmissionsPerHour}, {"-quota-queries", queries, r.MaxQueriesInFlight}} {
		if f.v < 0 || f.v > f.bound {
			return r.Quotas{}, fmt.Errorf("%s must be 0 (the default) or between 1 and %d", f.name, f.bound)
		}
	}
	return r.Quotas{JobsInFlight: jobs, StoredResults: results, AdmissionsPerHour: admissions, QueriesInFlight: queries}.Resolved()
}

// maxInFlightConfig reads -max-in-flight (S04-G): zero is the default, 128; a value outside 1..4096 stops the start with the flag's name.
func maxInFlightConfig(n int) (int, error) {
	if n < 0 || n > httpapi.MaxMaxInFlight {
		return 0, fmt.Errorf("-max-in-flight must be 0 (the default) or between 1 and %d", httpapi.MaxMaxInFlight)
	}
	if n == 0 {
		return httpapi.DefaultMaxInFlight, nil
	}
	return n, nil
}

// openObjectStore reads the object store flags and the environment and, when the store is configured, reaches its bucket: when HEAD bucket
// fails the server does not start, with a text that names the bucket and the status and nothing of the endpoint or the keys. The memory
// store keeps content inline whatever is configured (it is the explicit ephemeral store), so the two together are refused rather than
// silently ignored. A nil store, and no error, is the demo as it was: files inline in the rows.
func openObjectStore(flags *objects.Flags, getenv func(string) string, useMemory bool) (r.ObjectStore, error) {
	client, e := flags.Client(getenv)
	if e != nil || client == nil {
		return nil, e
	}
	if useMemory {
		return nil, fmt.Errorf("the object store flags do not apply to -memory, which keeps artifact files inline in memory")
	}
	startup, cancel := context.WithTimeout(context.Background(), objects.StartupTimeout)
	defer cancel()
	if e = objects.CheckAtStart(startup, client); e != nil {
		return nil, e
	}
	return client, nil
}

// previousTokensEnv names the optional older demo tokens (S04-H): comma-separated, newest first, whose cursors are still accepted so that a
// rotation of the demo token can be rehearsed. They only verify; the current token signs.
const previousTokensEnv = "REPORTING_DEMO_PREVIOUS_TOKENS"

// maxPreviousTokens is the ring's size, 4, less the current key.
const maxPreviousTokens = r.MaxCursorKeys - 1

// demoCursorKey is the demo's cursor key for one token: unchanged since S01, so that a single token keeps the cursors it always had.
func demoCursorKey(token string) []byte {
	key := sha256.Sum256([]byte("reporting-demo-cursor-v1:" + token))
	return key[:]
}

// demoCursorKeys derives the demo's cursor key ring from the current token and the optional previous ones (S04-H). A refusal names the
// variable and the position of the entry, never a token or a key.
func demoCursorKeys(current, previous string) ([][]byte, error) {
	if len(current) < 32 {
		return nil, fmt.Errorf("REPORTING_DEMO_TOKEN must contain at least 32 characters")
	}
	ring := [][]byte{demoCursorKey(current)}
	if previous == "" {
		return ring, nil
	}
	entries := strings.Split(previous, ",")
	if len(entries) > maxPreviousTokens {
		return nil, fmt.Errorf("%s holds at most %d tokens", previousTokensEnv, maxPreviousTokens)
	}
	tokens := []string{current}
	for i, token := range entries {
		if len(token) < 32 {
			return nil, fmt.Errorf("%s entry %d must contain at least 32 characters", previousTokensEnv, i+1)
		}
		if slices.Contains(tokens, token) {
			return nil, fmt.Errorf("%s entry %d repeats another token", previousTokensEnv, i+1)
		}
		tokens = append(tokens, token)
		ring = append(ring, demoCursorKey(token))
	}
	return ring, nil
}

func run() error {
	profile := flag.String("profile", "", "required: contract-simulator")
	listen := flag.String("listen", "127.0.0.1:7810", "loopback address only")
	useMemory := flag.Bool("memory", false, "explicit ephemeral query/job store; loses query/job data on restart")
	initialize := flag.Bool("initialize-fixture", false, "initialize guarded reporting_test_s01 PostgreSQL fixture")
	pauseWorkers := flag.Bool("pause-workers", false, "explicit recovery fixture: admit jobs without worker execution")
	sweepInterval := flag.Duration("sweep-interval", 10*time.Minute, "retention sweep (S04-B) every interval in the worker loop: expired artifacts and snapshots are deleted once 24 hours past their expiry; 0 disables it")
	layoutDir := flag.String("layout-directory", "", "optional private single-process layout directory (0700)")
	dashboardDir := flag.String("dashboard-directory", "", "optional private single-process dashboard directory (0700)")
	typst := flag.String("pdf-typst", "", "optional pinned Typst binary; with -pdf-typst-sha256 and -pdf-fonts enables PDF export")
	typstSum := flag.String("pdf-typst-sha256", "", "SHA-256 of the -pdf-typst binary in lowercase hex")
	pdfFonts := flag.String("pdf-fonts", "", "directory holding the only fonts PDF export may use")
	templateDir := flag.String("template-directory", "", "optional private single-process document template directory (0700)")
	assetDir := flag.String("background-directory", "", "optional private immutable PNG background directory (0700); it also holds the private images of schema 2.1 templates, in its images subdirectory")
	var embedOrigins []string
	quotaJobs := flag.Int("quota-jobs", 0, "quota per identity: queued and running jobs (S04-D); 0 is the default, 8; 1..1000")
	quotaResults := flag.Int("quota-results", 0, "quota per identity: snapshots and artifacts not yet expired (S04-D); 0 is the default, 64; 1..100000")
	quotaAdmissions := flag.Int("quota-admissions", 0, "quota per identity: jobs admitted in the last hour (S04-D); 0 is the default, 120; 1..100000")
	quotaQueries := flag.Int("quota-queries", 0, "quota per identity: POST /queries and snapshot row reads running at once in this process (S04-G); 0 is the default, 4; 1..64")
	maxInFlight := flag.Int("max-in-flight", 0, "API requests worked on at once in this process (S04-G); beyond it a request is answered 503 OVERLOADED with Retry-After: 1; 0 is the default, 128; 1..4096")
	objectStore := objects.Register(flag.CommandLine)
	flag.Func("embed-origin", "repeatable integration=origin: a page of that origin may show the embed pages of that integration in a frame (ADR 0006); an https origin as https://host[:port], or http://127.0.0.1:port for a local product page", func(value string) error {
		embedOrigins = append(embedOrigins, value)
		return nil
	})
	if e := objects.RefuseCredentialFlags(os.Args[1:]); e != nil {
		return e
	}
	flag.Parse()
	if *profile != "contract-simulator" {
		return fmt.Errorf("profile must explicitly be contract-simulator")
	}
	host, _, e := net.SplitHostPort(*listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("loopback listen address required")
	}
	if *sweepInterval < 0 {
		return fmt.Errorf("-sweep-interval must be 0 (off) or positive")
	}
	quotas, e := quotaConfig(*quotaJobs, *quotaResults, *quotaAdmissions, *quotaQueries)
	if e != nil {
		return e
	}
	inFlight, e := maxInFlightConfig(*maxInFlight)
	if e != nil {
		return e
	}
	// The object store (S04-E): read and refused before anything is opened, and reached before the server starts.
	objectBucket, e := openObjectStore(objectStore, os.Getenv, *useMemory)
	if e != nil {
		return e
	}
	secret := os.Getenv("REPORTING_DEMO_TOKEN")
	cursorKeys, e := demoCursorKeys(secret, os.Getenv(previousTokensEnv))
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var repo r.Repository
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	// Read before anything is opened: a malformed origin, or an integration this demo does not have, stops the start.
	embed, e := embedConfig(embedOrigins, p.Integrations)
	if e != nil {
		return e
	}
	var finance, shipment r.Adapter
	if *useMemory {
		if *initialize {
			return r.ErrInvalid
		}
		repo = memory.New(time.Now)
		a, b := contractsim.Financial(), contractsim.Shipment()
		a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
		grant := contractsim.DefaultGrant()
		grant.CurrencyPolicy = nil
		b.Grant(contractsim.DemoOwner(p, contractsim.ShipmentIntegration), grant)
		finance, shipment = a, b
	} else {
		dsn := os.Getenv("REPORTING_S01_TEST_DSN")
		if dsn == "" {
			return fmt.Errorf("set REPORTING_S01_TEST_DSN for a guarded fixture, or explicitly choose -memory")
		}
		dbctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		store, e := postgres.Open(dbctx, dsn, *initialize, postgres.WithObjects(objectBucket))
		if e != nil {
			return openFailure("guarded fixture could not be opened", e)
		}
		defer store.Close()
		repo = store
		source, e := pgsource.Open(dbctx, os.Getenv("REPORTING_S01_SOURCE_DSN"), *initialize)
		if e != nil {
			return openFailure("separate durable source fixture could not be opened", e)
		}
		defer source.Close()
		for i, seed := range []contractsim.Fixture{contractsim.Financial().Fixture(), contractsim.Shipment().Fixture()} {
			id := []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration}[i]
			if e = source.EnsureFixture(dbctx, id, "tenant_a", seed); e != nil {
				return fmt.Errorf("source fixture could not be ensured")
			}
			grant := contractsim.DefaultGrant()
			if i == 1 {
				grant.CurrencyPolicy = nil
			}
			if e = source.EnsureGrant(dbctx, r.SourceContext{Owner: contractsim.DemoOwner(p, id), SourceTenant: "tenant_a"}, grant); e != nil {
				return fmt.Errorf("source fixture grant could not be ensured")
			}
		}
		finance, shipment = source, source
	}
	formats := map[string]r.WorkbookWriter{}
	var renderer *pdf.Writer // table exports and document previews share one Typst
	switch {
	case *typst != "" && *typstSum != "" && *pdfFonts != "":
		w, err := pdf.New(pdf.Config{Typst: *typst, TypstSHA256: *typstSum, FontDir: *pdfFonts})
		if err != nil {
			return fmt.Errorf("PDF export could not start: %w", err)
		}
		formats["pdf"], renderer = w, w
	case *typst != "" || *typstSum != "" || *pdfFonts != "":
		return fmt.Errorf("PDF export needs -pdf-typst, -pdf-typst-sha256 and -pdf-fonts together")
	}
	// The template and background stores open before the service: the worker issues from them.
	var assets *doctemplates.Assets
	if *assetDir != "" {
		var err error
		if assets, err = doctemplates.OpenAssets(*assetDir); err != nil {
			return err
		}
		defer assets.Close()
		if renderer != nil {
			assets.Fits = renderer.BackgroundFits
			assets.Images.Fits = renderer.ImageFits
		}
	}
	var templates *doctemplates.Store
	if *templateDir != "" {
		var err error
		if templates, err = doctemplates.Open(*templateDir); err != nil {
			return err
		}
		defer templates.Close()
	}
	var issuer r.Issuer
	if templates != nil && renderer != nil {
		v, err := issuance.New(templates, assets, renderer)
		if err != nil {
			return err
		}
		issuer = v
	}
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}, {Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: shipment}}, CursorKeys: cursorKeys, Writer: xlsx.Writer{}, Formats: formats, Issuer: issuer, Quotas: quotas, Objects: objectBucket})
	if e != nil {
		return e
	}
	auth, e := httpapi.DemoBearer(secret, p)
	if e != nil {
		return e
	}
	var layoutStores []*layouts.Store
	if *layoutDir != "" {
		store, err := layouts.Open(*layoutDir)
		if err != nil {
			return err
		}
		defer store.Close()
		layoutStores = append(layoutStores, store)
	}
	handler, e := httpapi.Handler(s, auth, layoutStores...)
	if e != nil {
		return e
	}
	if *dashboardDir != "" {
		store, err := dashboards.Open(*dashboardDir)
		if err != nil {
			return err
		}
		defer store.Close()
		handler, err = httpapi.WithDashboards(handler, s, auth, store)
		if err != nil {
			return err
		}
	}
	if assets != nil {
		if handler, e = httpapi.WithDocumentAssets(handler, s, auth, assets); e != nil {
			return e
		}
	}
	var documents httpapi.DocumentRenderer
	if renderer != nil {
		documents = renderer
		if handler, e = httpapi.WithDocumentPreviews(handler, s, auth, documents, assets); e != nil {
			return e
		}
	}
	if templates != nil {
		if handler, e = httpapi.WithDocumentTemplates(handler, s, auth, templates, documents, assets); e != nil {
			return e
		}
	}
	if len(embed.Integrations()) > 0 {
		if handler, e = httpapi.WithEmbedding(handler, s, auth, embed); e != nil {
			return e
		}
	}
	// S12-B: the demo records people's actions in its store too (memory, or the fixture database).
	if log, ok := repo.(r.AuditLog); ok {
		handler = httpapi.WithAudit(handler, log)
	}
	// The cap on requests in flight wraps the API alone, under the console, whose static pages are not what it protects (S04-G).
	if handler, e = httpapi.WithMaxInFlight(handler, inFlight); e != nil {
		return e
	}
	handler, e = console.HandlerWithEmbedding(handler, embed)
	if e != nil {
		return e
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 25 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, e := net.Listen("tcp", *listen)
	if e != nil {
		return e
	}
	defer listener.Close()
	var workers sync.WaitGroup
	if !*pauseWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, e := s.RunOne(ctx); e != nil && !errors.Is(e, context.Canceled) {
						log.Print("contract-simulator task stopped; inspect the private job error code")
					}
				}
			}
		}()
	}
	if !*pauseWorkers && *sweepInterval > 0 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			sweepLoop(ctx, s, *sweepInterval, log.Printf)
		}()
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("S01-B CONTRACT SIMULATORS ONLY; memory=%t; paused=%t; pdf=%t; issuance=%t; sweep=%s; objects=%t; cursor-keys=%d; pid=%d; listening at http://%s", *useMemory, *pauseWorkers, formats["pdf"] != nil, issuer != nil, *sweepInterval, objectBucket != nil, len(cursorKeys), os.Getpid(), listener.Addr())
	e = server.Serve(listener)
	stop()
	workers.Wait()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
