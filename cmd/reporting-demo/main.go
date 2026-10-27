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
	"idp.local/reporting/export/pdf"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/layouts"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/postgres"
	"idp.local/reporting/transport/httpapi"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
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
func run() error {
	profile := flag.String("profile", "", "required: contract-simulator")
	listen := flag.String("listen", "127.0.0.1:7810", "loopback address only")
	useMemory := flag.Bool("memory", false, "explicit ephemeral query/job store; loses query/job data on restart")
	initialize := flag.Bool("initialize-fixture", false, "initialize guarded reporting_test_s01 PostgreSQL fixture")
	pauseWorkers := flag.Bool("pause-workers", false, "explicit recovery fixture: admit jobs without worker execution")
	layoutDir := flag.String("layout-directory", "", "optional private single-process layout directory (0700)")
	dashboardDir := flag.String("dashboard-directory", "", "optional private single-process dashboard directory (0700)")
	typst := flag.String("pdf-typst", "", "optional pinned Typst binary; with -pdf-typst-sha256 and -pdf-fonts enables PDF export")
	typstSum := flag.String("pdf-typst-sha256", "", "SHA-256 of the -pdf-typst binary in lowercase hex")
	pdfFonts := flag.String("pdf-fonts", "", "directory holding the only fonts PDF export may use")
	flag.Parse()
	if *profile != "contract-simulator" {
		return fmt.Errorf("profile must explicitly be contract-simulator")
	}
	host, _, e := net.SplitHostPort(*listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("loopback listen address required")
	}
	secret := os.Getenv("REPORTING_DEMO_TOKEN")
	if len(secret) < 32 {
		return fmt.Errorf("REPORTING_DEMO_TOKEN must contain at least 32 characters")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var repo r.Repository
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
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
		store, e := postgres.Open(dbctx, dsn, *initialize)
		if e != nil {
			return fmt.Errorf("guarded fixture could not be opened")
		}
		defer store.Close()
		repo = store
		source, e := pgsource.Open(dbctx, os.Getenv("REPORTING_S01_SOURCE_DSN"), *initialize)
		if e != nil {
			return fmt.Errorf("separate durable source fixture could not be opened")
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
	switch {
	case *typst != "" && *typstSum != "" && *pdfFonts != "":
		w, err := pdf.New(pdf.Config{Typst: *typst, TypstSHA256: *typstSum, FontDir: *pdfFonts})
		if err != nil {
			return fmt.Errorf("PDF export could not start: %w", err)
		}
		formats["pdf"] = w
	case *typst != "" || *typstSum != "" || *pdfFonts != "":
		return fmt.Errorf("PDF export needs -pdf-typst, -pdf-typst-sha256 and -pdf-fonts together")
	}
	key := sha256.Sum256([]byte("reporting-demo-cursor-v1:" + secret))
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}, {Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: shipment}}, CursorKey: key[:], Writer: xlsx.Writer{}, Formats: formats})
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
	handler, e = console.Handler(handler)
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
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("S01-B CONTRACT SIMULATORS ONLY; memory=%t; paused=%t; pdf=%t; pid=%d; listening at http://%s", *useMemory, *pauseWorkers, formats["pdf"] != nil, os.Getpid(), listener.Addr())
	e = server.Serve(listener)
	stop()
	workers.Wait()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
