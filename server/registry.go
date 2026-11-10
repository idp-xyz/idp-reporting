package server

import (
	"encoding/json"
	"fmt"
	"io"

	"idp.local/reporting/adapters/httpsource"
	r "idp.local/reporting/reporting"
)

// AdapterFactory builds the adapter of one connection from its options (passed untouched from the configuration, never logged) and the
// environment (the factory reads its own secrets from it, by names the host chooses). A failure is a refusal to start: the error is not
// shown, so that a factory cannot leak a connection string through it; the start line says which connection failed.
type AdapterFactory func(options json.RawMessage, getenv func(string) string) (r.Adapter, error)

// Registry maps the adapter names a configuration may use to their factories. The shipped command's registry is DefaultRegistry; a host
// with an adapter of its own writes a three-line main around a registry of its own (docs/DEPLOYMENT.md).
type Registry map[string]AdapterFactory

// HTTPAdapter is the name of the generic source adapter (S19-A, adapters/httpsource, docs/SOURCE_HTTP_PROTOCOL.md): a connection with
// `"adapter": "http"` reaches a source product that implements the reporting source protocol.
const HTTPAdapter = "http"

// DefaultRegistry is the shipped command's registry: the generic source adapter and nothing else. It is the platform's own reviewed code
// and is not a simulator.
func DefaultRegistry() Registry {
	return Registry{HTTPAdapter: httpsource.Factory}
}

// simulated is implemented by an adapter that is a test fixture. The contract simulators implement it; an adapter of a real product does not.
type simulated interface{ IsSimulator() bool }

// isSimulator says whether an adapter reports itself as a contract simulator. It is the adapter's own word: a wrapper that hides the method
// (embedding the Adapter interface rather than the simulator) is not found, which is why the shipped command registers only the platform's
// own generic adapter and a host's registry is its own code to review.
func isSimulator(a r.Adapter) bool {
	s, ok := a.(simulated)
	return ok && s.IsSimulator()
}

// buildAdapters makes one adapter per connection, in the order of the configuration. The errors are fixed texts naming the connection's
// integration and adapter name (both from the configuration file).
func buildAdapters(c *Config, reg Registry, getenv func(string) string) ([]r.Connection, []io.Closer, error) {
	var out []r.Connection
	var closers []io.Closer
	fail := func(err error) ([]r.Connection, []io.Closer, error) {
		closeAll(closers)
		return nil, nil, err
	}
	for _, v := range c.Connections {
		if len(reg) == 0 {
			return fail(fmt.Errorf("no adapter is registered for integration %q", v.Integration))
		}
		factory, ok := reg[v.Adapter]
		if !ok || factory == nil {
			return fail(fmt.Errorf("the adapter %q of integration %q is not registered", v.Adapter, v.Integration))
		}
		a, err := factory(v.Options, getenv)
		if err != nil || a == nil {
			return fail(fmt.Errorf("the adapter %q of integration %q could not be made (its error is not shown)", v.Adapter, v.Integration))
		}
		if cl, ok := a.(io.Closer); ok {
			closers = append(closers, cl)
		} else if cl, ok := a.(interface{ Close() }); ok {
			closers = append(closers, closerFunc(cl.Close))
		}
		if isSimulator(a) && !c.AllowSimulator {
			return fail(fmt.Errorf("the adapter %q of integration %q is a contract simulator; the configuration must say allow_simulator to run it", v.Adapter, v.Integration))
		}
		out = append(out, r.Connection{Tenant: v.Tenant, Integration: v.Integration, SourceTenant: v.SourceTenant, Adapter: a})
	}
	return out, closers, nil
}

type closerFunc func()

func (f closerFunc) Close() error { f(); return nil }

func closeAll(closers []io.Closer) {
	for i := len(closers) - 1; i >= 0; i-- {
		_ = closers[i].Close()
	}
}
