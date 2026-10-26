// reporting-demo is explicitly limited to local contract-simulator qualification.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
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
	useMemory := flag.Bool("memory", false, "explicit ephemeral demonstration; loses all data on restart")
	initialize := flag.Bool("initialize-fixture", false, "initialize guarded reporting_test_s01 PostgreSQL fixture")
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
	if *useMemory {
		if *initialize {
			return r.ErrInvalid
		}
		repo = memory.New(time.Now)
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
	}
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	a, b := contractsim.Financial(), contractsim.Shipment()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	grant := contractsim.DefaultGrant()
	grant.CurrencyPolicy = nil
	b.Grant(contractsim.DemoOwner(p, contractsim.ShipmentIntegration), grant)
	key := sha256.Sum256([]byte("reporting-demo-cursor-v1:" + secret))
	s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}, {Tenant: p.Tenant, Integration: contractsim.ShipmentIntegration, SourceTenant: "tenant_a", Adapter: b}}, CursorKey: key[:], Writer: xlsx.Writer{}})
	if e != nil {
		return e
	}
	auth, e := httpapi.DemoBearer(secret, p)
	if e != nil {
		return e
	}
	handler, e := httpapi.Handler(s, auth)
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
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("S01-A CONTRACT SIMULATORS ONLY; memory=%t; listening at http://%s", *useMemory, listener.Addr())
	e = server.Serve(listener)
	stop()
	workers.Wait()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
