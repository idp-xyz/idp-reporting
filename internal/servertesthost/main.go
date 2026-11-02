// servertesthost is the test host of the S05-A qualification: the host main of docs/DEPLOYMENT.md with the contract simulator registered and the
// memory store, so that the real server can be run as a separate process without a database. It is a test fixture and is never built into
// anything that ships.
package main

import (
	"encoding/json"
	"os"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/server"
)

func main() {
	reg := server.Registry{"contract-simulator": func(options json.RawMessage, _ func(string) string) (r.Adapter, error) {
		var o struct{ Tenant, Issuer, Subject string }
		if err := json.Unmarshal(options, &o); err != nil {
			return nil, err
		}
		sim := contractsim.Financial()
		sim.Grant(r.Owner{Identity: r.Identity{Tenant: o.Tenant, Issuer: o.Issuer, Subject: o.Subject}, Integration: contractsim.FinanceIntegration}, contractsim.DefaultGrant())
		return sim, nil
	}}
	os.Exit(server.MainWith(os.Args, reg, server.WithStoreForTests(server.NewMemoryStoreForTests(time.Now))))
}
