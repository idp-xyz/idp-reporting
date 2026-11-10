package contractsim_test

import (
	"testing"

	"idp.local/reporting/adapters/contractsim/internal/authoritytest"
	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/sourcetest"
)

// remoteEnv is the in-memory simulator reached through the reporting source protocol (S19-A): the reference source in front of it and the
// platform's adapter httpsource in front of that, on the simulator's clock. The document authority's conformance suite runs against it
// unchanged: every rule it holds the simulator to holds through the protocol.
type remoteEnv struct {
	*memEnv
	source authoritytest.Source
}

func newRemoteEnv(t *testing.T, p authoritytest.Profile, extra ...string) authoritytest.Env {
	e := newMemEnv(t, p, extra...).(*memEnv)
	a := sourcetest.Remote(t, e.s, func(c *httpsource.Config) { c.Now = e.now })
	src, ok := a.(authoritytest.Source)
	if !ok {
		t.Fatal("the adapter of a source that issues is not a document authority")
	}
	return &remoteEnv{memEnv: e, source: src}
}
func (e *remoteEnv) Source() authoritytest.Source { return e.source }

func TestDocumentAuthorityOverHTTP(t *testing.T) { authoritytest.Run(t, newRemoteEnv) }

func TestDocumentAuthorityConcurrentCallsOverHTTP(t *testing.T) {
	authoritytest.RunConcurrent(t, newRemoteEnv)
}
