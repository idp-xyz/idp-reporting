// reporting-server is the production server of the reporting platform (S05-A). It registers the platform's generic source adapter `http`
// (S19-A, docs/SOURCE_HTTP_PROTOCOL.md) and nothing else: a source product connects by implementing the reporting source protocol. A host
// with an adapter of its own writes its own three-line main around server.Main (docs/DEPLOYMENT.md).
package main

import (
	"os"

	"idp.local/reporting/server"
)

func main() { server.Main(os.Args, server.DefaultRegistry()) }
