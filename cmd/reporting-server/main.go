// reporting-server is the production server of the reporting platform (S05-A). It registers no adapter: a host that has one writes its own
// three-line main around server.Main (docs/DEPLOYMENT.md), so this command refuses to run until it is given a registry, and says so.
package main

import (
	"os"

	"idp.local/reporting/server"
)

func main() { server.Main(os.Args, server.Registry{}) }
