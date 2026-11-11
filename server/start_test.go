package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
)

// refusedStart starts a server that must not start and returns the refusal and the log it wrote.
func refusedStart(t *testing.T, w *world, reg Registry, getenv func(string) string, opts ...Option) (*StartError, string) {
	t.Helper()
	snap := goroutines()
	lb := &logBuffer{}
	s, err := Start(context.Background(), w.cfg, reg, getenv, lb, opts...)
	if err == nil {
		s.cancelForTest()
		t.Fatal("the server started")
	}
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("not a StartError: %v", err)
	}
	// Nothing a refused start began is still running.
	noNewGoroutines(t, snap)
	return se, lb.String()
}

func (s *Server) cancelForTest() {}

func memoryOnly() Option { return WithStoreForTests(NewMemoryStoreForTests(time.Now)) }

// Every refusal of a start is a fixed text that names the step, and none carries a secret that was in play: the connection string with a
// password, the object store's keys, the file contents, the factory's error.
func TestStartRefusalsNameTheStepAndCarryNoSecret(t *testing.T) {
	const factorySecret = "factory-error-Zq7-postgres://u:pw@h"
	type tc struct {
		name   string
		step   string
		want   string // a word the text must contain
		change func(w *world) (Registry, func(string) string, []Option)
	}
	cases := []tc{
		{"no adapter registered", StepAdapters, `no adapter is registered for integration "brant-contract-demo"`, func(w *world) (Registry, func(string) string, []Option) {
			return Registry{}, w.getenv, []Option{memoryOnly()}
		}},
		{"an unknown adapter name", StepAdapters, `the adapter "sim" of integration "brant-contract-demo" is not registered`, func(w *world) (Registry, func(string) string, []Option) {
			return Registry{"other": nil}, w.getenv, []Option{memoryOnly()}
		}},
		{"a factory that fails", StepAdapters, "could not be made", func(w *world) (Registry, func(string) string, []Option) {
			return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) {
				return nil, errors.New(factorySecret)
			}}, w.getenv, []Option{memoryOnly()}
		}},
		{"a factory that returns nothing", StepAdapters, "could not be made", func(w *world) (Registry, func(string) string, []Option) {
			return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return nil, nil }}, w.getenv, []Option{memoryOnly()}
		}},
		{"a simulator without the allowance", StepAdapters, "allow_simulator", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.AllowSimulator = false
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"a simulator that says so itself", StepAdapters, "allow_simulator", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.AllowSimulator = false
			return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return selfDeclared{w.sim}, nil }}, w.getenv, []Option{memoryOnly()}
		}},
		// S08-A (ADR 0012 decision 6): the data class is the adapter's other word, and the two agree or the server does not start.
		{"a simulator by its data class alone", StepAdapters, "allow_simulator", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.AllowSimulator = false
			return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) {
				return worded{w.sim, r.DataClassSimulator}, nil
			}}, w.getenv, []Option{memoryOnly()}
		}},
		{"a simulator that hides the simulator's data class, even when simulators are allowed", StepAdapters, "without the data class contract-simulator", func(w *world) (Registry, func(string) string, []Option) {
			return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return halfDeclared{w.sim}, nil }}, w.getenv, []Option{memoryOnly()}
		}},
		{"a simulator that says another data class", StepAdapters, "without the data class contract-simulator", func(w *world) (Registry, func(string) string, []Option) {
			return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return misworded{w.sim}, nil }}, w.getenv, []Option{memoryOnly()}
		}},
		{"an adapter that says a data class outside the set", StepAdapters, "a data class the platform does not know", func(w *world) (Registry, func(string) string, []Option) {
			return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) {
				return worded{w.sim, "real-data-Zq7"}, nil
			}}, w.getenv, []Option{memoryOnly()}
		}},
		{"an adapter that says no data class at all", StepAdapters, "a data class the platform does not know", func(w *world) (Registry, func(string) string, []Option) {
			return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return worded{w.sim, ""}, nil }}, w.getenv, []Option{memoryOnly()}
		}},
		{"the connection string missing", StepSecrets, StoreDSNEnv, func(w *world) (Registry, func(string) string, []Option) {
			return w.registry(), func(string) string { return "" }, nil
		}},
		{"object store keys missing", StepSecrets, "must both be set", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.ObjectStore = &ObjectStoreConfig{Endpoint: "https://s3.example.test", Bucket: "bucket"}
			return w.registry(), func(n string) string {
				if n == "REPORTING_OBJECT_STORE_ACCESS_KEY" {
					return testAccess
				}
				return ""
			}, []Option{memoryOnly()}
		}},
		{"an object store the client refuses", StepObjectStore, "object_store section is refused", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.ObjectStore = &ObjectStoreConfig{Endpoint: "http://203.0.113.9:9000", Bucket: "bucket", LoopbackHTTP: true}
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"an object store that does not answer", StepObjectStore, `bucket "bucket" is not usable`, func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.ObjectStore = &ObjectStoreConfig{Endpoint: "http://127.0.0.1:1", Bucket: "bucket", LoopbackHTTP: true}
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"a cursor key file that anyone can read", StepSecrets, "0600 or 0400", func(w *world) (Registry, func(string) string, []Option) {
			writeFile(t, w.cursorFile, []byte(cursorKeyHex), 0o644)
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"a TLS key that anyone can read", StepSecrets, "0600 or 0400", func(w *world) (Registry, func(string) string, []Option) {
			writeFile(t, w.cfg.TLSKeyFile, mustRead(t, w.cfg.TLSKeyFile), 0o640)
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"a certificate that is not valid now", StepSecrets, "not valid now", func(w *world) (Registry, func(string) string, []Option) {
			return w.registry(), w.getenv, []Option{memoryOnly(), WithClockForTests(func() time.Time { return time.Now().Add(400 * 24 * time.Hour) })}
		}},
		{"an issuer file the profile refuses", StepIssuer, "access-token profile", func(w *world) (Registry, func(string) string, []Option) {
			writeFile(t, w.jwks, []byte(`{"keys":[]}`), 0o644)
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"an issuer that is not https", StepIssuer, "access-token profile", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.Issuer.Issuer = "http://issuer.example.test"
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"a binding that names a product no connection serves", StepIssuer, "not served by a connection", func(w *world) (Registry, func(string) string, []Option) {
			writeFile(t, w.binds, bindingsJSON(bindingJSON{testSubject, testClient, testTenant, []string{w.finance, "unserved-product"}}), 0o644)
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"a binding of a tenant that has no such connection", StepIssuer, "not served by a connection", func(w *world) (Registry, func(string) string, []Option) {
			writeFile(t, w.binds, bindingsJSON(bindingJSON{testSubject, testClient, "another-tenant", []string{w.finance}}), 0o644)
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"a ring of five", StepConfig, "cursor_key_files", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.CursorKeyFiles = []string{"/1", "/2", "/3", "/4", "/5"}
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"plain HTTP on a public address", StepConfig, "plain HTTP is refused", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.TLSCertFile, w.cfg.TLSKeyFile, w.cfg.AllowPlainLoopback, w.cfg.Listen = "", "", true, "0.0.0.0:0"
			return w.registry(), w.getenv, []Option{memoryOnly()}
		}},
		{"a store that is a fixture database", StepStore, "fixture database", func(w *world) (Registry, func(string) string, []Option) {
			return w.registry(), func(n string) string {
				if n == StoreDSNEnv {
					return "postgresql://appuser:" + testDSNPass + "@127.0.0.1:1/reporting_test_s01?sslmode=verify-full"
				}
				return w.getenv(n)
			}, nil
		}},
		{"a store without TLS verification", StepStore, "verify-full", func(w *world) (Registry, func(string) string, []Option) {
			return w.registry(), func(n string) string {
				if n == StoreDSNEnv {
					return "postgresql://appuser:" + testDSNPass + "@127.0.0.1:1/reporting_prod?sslmode=require"
				}
				return w.getenv(n)
			}, nil
		}},
		{"a store that cannot be parsed", StepStore, "its text is not shown", func(w *world) (Registry, func(string) string, []Option) {
			return w.registry(), func(n string) string {
				if n == StoreDSNEnv {
					return "::" + testDSNPass
				}
				return w.getenv(n)
			}, nil
		}},
		{"a store that does not answer", StepStore, "did not answer", func(w *world) (Registry, func(string) string, []Option) {
			return w.registry(), w.getenv, nil
		}},
		{"a pool size in both places", StepStore, "both in the configuration and in the connection string", func(w *world) (Registry, func(string) string, []Option) {
			w.cfg.Store.PoolMaxConns = 8
			return w.registry(), func(n string) string {
				if n == StoreDSNEnv {
					return "postgresql://appuser:" + testDSNPass + "@127.0.0.1:1/reporting_prod?sslmode=verify-full&pool_max_conns=8"
				}
				return w.getenv(n)
			}, nil
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			reg, getenv, opts := c.change(w)
			se, logged := refusedStart(t, w, reg, getenv, opts...)
			if se.Step != c.step || !strings.Contains(se.Reason, c.want) {
				t.Fatalf("step %q reason %q, want step %q containing %q", se.Step, se.Reason, c.step, c.want)
			}
			for _, secret := range []string{testDSNPass, testAccess, testSecret, factorySecret, cursorKeyHex, "postgresql://", "127.0.0.1:1", w.dir} {
				if strings.Contains(se.Error(), secret) || strings.Contains(logged, secret) {
					t.Errorf("the refusal or the log carries %q: %q / %q", secret, se.Error(), logged)
				}
			}
			if !strings.HasPrefix(se.Error(), "reporting-server: start refused at step ") {
				t.Errorf("%q", se.Error())
			}
			if !strings.Contains(logged, `"event":"start_refused"`) || !strings.Contains(logged, `"step":"`+c.step+`"`) {
				t.Errorf("the log does not say which step refused: %q", logged)
			}
		})
	}
}

// selfDeclared is an adapter of a real-looking name that says it is a simulator, in both of an adapter's words (S08-A).
type selfDeclared struct{ r.Adapter }

func (selfDeclared) IsSimulator() bool { return true }
func (selfDeclared) DataClass() string { return r.DataClassSimulator }

// halfDeclared says it is a simulator and hides the data class of what it wraps: its data would be labelled a source product's.
type halfDeclared struct{ r.Adapter }

func (halfDeclared) IsSimulator() bool { return true }

// misworded says it is a simulator and says a source product's data class.
type misworded struct{ r.Adapter }

func (misworded) IsSimulator() bool { return true }
func (misworded) DataClass() string { return r.DataClassBusiness }

// worded is an adapter that says a data class, and nothing of being a simulator.
type worded struct {
	r.Adapter
	class string
}

func (w worded) DataClass() string { return w.class }

// The steps are taken in the order the scope gives, so a start with two problems names the earlier one.
func TestStartStepsAreTakenInOrder(t *testing.T) {
	w := newWorld(t)
	// An unregistered adapter, an unreadable cursor key and a missing connection string: the secrets come before the adapters, the adapters
	// before the store.
	writeFile(t, w.cursorFile, []byte(cursorKeyHex), 0o644)
	se, _ := refusedStart(t, w, Registry{}, func(string) string { return "" })
	if se.Step != StepSecrets {
		t.Errorf("first: %q", se.Step)
	}
	writeFile(t, w.cursorFile, []byte(cursorKeyHex), 0o600)
	writeFile(t, w.jwks, []byte(`{}`), 0o644)
	se, _ = refusedStart(t, w, Registry{}, w.getenv, memoryOnly())
	if se.Step != StepIssuer {
		t.Errorf("second: %q", se.Step)
	}
	writeFile(t, w.jwks, jwksOf(w.keys[0]), 0o644)
	se, _ = refusedStart(t, w, Registry{}, w.getenv)
	if se.Step != StepAdapters {
		t.Errorf("third: %q", se.Step)
	}
	se, _ = refusedStart(t, w, w.registry(), func(string) string { return "" })
	if se.Step != StepSecrets { // the connection string is a secret and is read before the adapters are made
		t.Errorf("fourth: %q", se.Step)
	}
	w.cfg.AllowSimulator = false
	w.cfg.CursorKeyFiles = []string{"/nope"}
	se, _ = refusedStart(t, w, w.registry(), w.getenv, memoryOnly())
	if se.Step != StepSecrets {
		t.Errorf("fifth: %q", se.Step)
	}
}

// closingAdapter counts its Close calls.
type closingAdapter struct {
	r.Adapter
	closed *atomic.Int32
}

func (c closingAdapter) Close() error      { c.closed.Add(1); return nil }
func (c closingAdapter) IsSimulator() bool { return true }
func (c closingAdapter) DataClass() string { return r.DataClassSimulator }

// A start that fails after the adapters were made gives them back, and one that fails at listen stops the workers it had started.
func TestARefusedStartUnwindsWhatItOpened(t *testing.T) {
	w := newWorld(t)
	var closed atomic.Int32
	reg := Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) {
		return closingAdapter{w.sim, &closed}, nil
	}}
	store := NewMemoryStoreForTests(time.Now)
	// The port is taken.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	w.cfg.Listen = taken.Addr().String()
	se, logged := refusedStart(t, w, reg, w.getenv, WithStoreForTests(store), WithPollIntervalForTests(time.Millisecond))
	if se.Step != StepListen || strings.Contains(se.Error(), taken.Addr().String()) || strings.Contains(logged, taken.Addr().String()) {
		t.Fatalf("%v", se)
	}
	if closed.Load() != 1 || !store.Closed() {
		t.Errorf("adapters closed %d times, store closed %v", closed.Load(), store.Closed())
	}
	// The store failing to open closes the adapters too.
	closed.Store(0)
	w.cfg.Listen = "127.0.0.1:0"
	se, _ = refusedStart(t, w, reg, w.getenv)
	if se.Step != StepStore || closed.Load() != 1 {
		t.Errorf("%v closed %d", se, closed.Load())
	}
}
