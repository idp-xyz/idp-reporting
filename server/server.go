package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"idp.local/reporting/console"
	"idp.local/reporting/dashboards"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/export/pdf"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/identity/accessjwt"
	"idp.local/reporting/issuance"
	"idp.local/reporting/layouts"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
	"idp.local/reporting/transport/httpapi"
)

// Store is what the server needs of its platform store: the repository, the readiness check of S05-A, the ledger's version for the start line,
// and Close. *postgres.Store opened by OpenProduction is the production one.
type Store interface {
	r.Repository
	// Ready is "" when the store answers, its ledger is at the binary's version and the role check is clean, else "store", "schema" or "roles".
	Ready(context.Context) string
	LedgerVersion(context.Context) (int, error)
	// Designs answers the backend of one of the private stores in this store (S11-B: design_store database).
	Designs(store string) (privatedocs.Backend, error)
	Close()
}

var _ Store = (*postgres.Store)(nil)

// ErrDrainTimeout is what Run returns when the drain used up shutdown_grace: a request or a worker was still busy. The exit code is 2.
var ErrDrainTimeout = errors.New("the drain ran out of shutdown_grace")

// StartError is a refusal to start. Its text is fixed: the step that failed and a reason that names configuration fields, never a value read
// from a secret, a file's content, a connection string or an adapter's error. The exit code is 1.
type StartError struct {
	Step   string
	Reason string
}

func (e *StartError) Error() string {
	return fmt.Sprintf("reporting-server: start refused at step %q: %s", e.Step, e.Reason)
}

// The steps of a start, in order.
const (
	StepConfig      = "configuration"
	StepSecrets     = "secrets"
	StepCursorRing  = "cursor keys"
	StepIssuer      = "issuer"
	StepAdapters    = "adapters"
	StepStore       = "store"
	StepObjectStore = "object store"
	StepPDF         = "pdf"
	StepDirectories = "private directories"
	StepService     = "service"
	StepListen      = "listen"
)

func refuse(step string, reason string) error { return &StartError{Step: step, Reason: reason} }

// ReloadError is a refused reload; the previous issuer configuration stays in force. Code is one of the Code* constants.
type ReloadError struct{ Code string }

func (e *ReloadError) Error() string { return "reload refused: " + e.Code }

// Option changes how Start works; the exported ones are for tests (their names end in ForTests) and are not reachable from the shipped command.
type Option func(*options)

type options struct {
	store         Store
	insecureStore bool
	now           func() time.Time
	graceTimer    func(time.Duration) (<-chan time.Time, func())
	poll          time.Duration
	sweepTicker   func(time.Duration) (<-chan time.Time, func())
	hooks         *Hooks
}

// Hooks are the signals a test uses instead of sleeping. Every field may be nil.
type Hooks struct {
	// Started receives the server once it accepts connections (buffered, so that Start never waits on it).
	Started chan *Server
	// DrainBegan is closed when the drain begins: readiness already says draining and the loops are told to stop.
	DrainBegan chan struct{}
	// ListenerClosed is closed when the listener has been closed by the drain: a new connection is refused from then on.
	ListenerClosed chan struct{}
	// Reloaded receives (buffered) after each Reload, whatever its outcome.
	Reloaded chan struct{}
	// Swept receives (buffered) after each pass of the sweep loop.
	Swept chan struct{}
}

// Server is a running (or draining) server.
type Server struct {
	cfg     Config
	log     *logger
	now     func() time.Time
	opts    options
	version string

	verifier *accessjwt.Verifier
	issuer   issuerFiles // the bindings in force and their digest (ADR 0016); replaced by a successful reload under reloadMu
	store    Store
	svc      *r.Service
	objects  *objects.S3
	srv      *http.Server
	listener net.Listener
	// metricsListener is the loopback listener of GET /metrics, when metrics_listen is set.
	metricsListener net.Listener
	tls             bool

	draining      atomic.Bool
	requests      tracker
	inflight      func() int64
	reloadMu      sync.Mutex
	busy          atomic.Int64 // workers and sweeps inside a call
	stored        verdict
	objectVerdict verdict
	metrics       *metrics

	// The workers' loops stop with loopCtx (the drain's first act); the jobs they hold run on workCtx, which nothing but the end of the
	// process cancels, so that a claimed job finishes within the grace instead of being cut off and failed.
	loopCtx    context.Context
	loopCancel context.CancelFunc
	workCtx    context.Context
	workCancel context.CancelFunc
	workers    sync.WaitGroup
	cleanups   []func() // run in reverse after a clean drain, or when a start fails

	done   chan struct{}
	result error
}

// Version is the binary's version as the build recorded it ("devel" for a build without version information).
func Version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return "devel+" + s.Value[:12]
			}
		}
	}
	return "devel"
}

// Run starts the server, serves until ctx ends, drains and returns: nil for a clean drain, ErrDrainTimeout for a drain that ran out of
// shutdown_grace, a *StartError for a refused start. Log lines are JSON, one per event, written to logOut.
func Run(ctx context.Context, cfg Config, reg Registry, getenv func(string) string, logOut io.Writer) error {
	s, err := Start(ctx, cfg, reg, getenv, logOut)
	if err != nil {
		return err
	}
	return s.Wait()
}

// Start does the steps of a start in order and returns when the server accepts connections. Cancelling ctx begins the drain. Wait returns
// when the drain is over.
func Start(ctx context.Context, cfg Config, reg Registry, getenv func(string) string, logOut io.Writer, opts ...Option) (*Server, error) {
	o := options{now: time.Now, poll: 200 * time.Millisecond}
	for _, f := range opts {
		f(&o)
	}
	if o.graceTimer == nil {
		o.graceTimer = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTimer(d)
			return t.C, func() { t.Stop() }
		}
	}
	if o.sweepTicker == nil {
		o.sweepTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	s := &Server{cfg: cfg, now: o.now, opts: o, version: Version(), done: make(chan struct{}), metrics: newMetrics()}
	s.log = newLogger(logOut, o.now)
	if err := s.start(ctx, reg, getenv); err != nil {
		s.unwind()
		var se *StartError
		if errors.As(err, &se) {
			s.log.error("start_refused", map[string]any{"step": se.Step})
		}
		return nil, err
	}
	return s, nil
}

func (s *Server) defer_(f func()) { s.cleanups = append(s.cleanups, f) }

func (s *Server) unwind() {
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
	s.cleanups = nil
}

func (s *Server) start(ctx context.Context, reg Registry, getenv func(string) string) error {
	cfg := &s.cfg
	// 1. The configuration.
	if err := cfg.Validate(); err != nil {
		return refuse(StepConfig, err.Error())
	}
	quotas, _ := cfg.QuotaValues()
	embed, _ := cfg.Embedding()
	inFlight := cfg.MaxInFlight
	// 2. The secrets: the cursor keys, the issuer files, the connection string and the object store's keys.
	keys, err := readCursorKeys(cfg.CursorKeyFiles)
	if err != nil {
		return refuse(StepSecrets, err.Error())
	}
	dsn := getenv(StoreDSNEnv)
	if dsn == "" && s.opts.store == nil {
		return refuse(StepSecrets, StoreDSNEnv+" is not set")
	}
	var client *objects.S3
	if o := cfg.ObjectStore; o != nil {
		access, secret := getenv(objects.AccessKeyEnv), getenv(objects.SecretKeyEnv)
		if access == "" || secret == "" {
			return refuse(StepSecrets, objects.AccessKeyEnv+" and "+objects.SecretKeyEnv+" must both be set when object_store is configured")
		}
		region := o.Region
		if region == "" {
			region = "us-east-1"
		}
		client, err = objects.NewS3(objects.S3{Endpoint: o.Endpoint, Bucket: o.Bucket, Region: region, AccessKey: access, SecretKey: secret, CA: o.CAFile, LoopbackHTTP: o.LoopbackHTTP})
		if err != nil {
			return refuse(StepObjectStore, "the object_store section is refused (endpoint, bucket, region, ca_file or loopback_http; the client's own text is not shown)")
		}
		s.objects = client
		s.defer_(client.Close)
	}
	var tlsConfig *tls.Config
	if cfg.TLSCertFile != "" {
		if tlsConfig, err = loadTLS(cfg.TLSCertFile, cfg.TLSKeyFile, s.now()); err != nil {
			return refuse(StepSecrets, err.Error())
		}
		s.tls = true
	}
	// 3. The cursor ring, the issuer verifier, the adapters.
	verifier, files, err := newVerifier(cfg.Issuer, cfg.Connections)
	if err != nil {
		return refuse(StepIssuer, err.Error())
	}
	s.verifier, s.issuer = verifier, files
	connections, closers, err := buildAdapters(cfg, reg, getenv)
	if err != nil {
		return refuse(StepAdapters, err.Error())
	}
	s.defer_(func() { closeAll(closers) })
	// 4. The store.
	var objectStore r.ObjectStore
	if client != nil {
		objectStore = client
	}
	switch {
	case s.opts.store != nil:
		s.store = s.opts.store
	default:
		dbctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		store, err := postgres.OpenProduction(dbctx, postgres.ProductionConfig{DSN: dsn, InsecureTestOnly: s.opts.insecureStore, Workers: cfg.WorkerCount(), PoolMaxConns: cfg.Store.PoolMaxConns}, postgres.WithObjects(objectStore))
		cancel()
		if err != nil {
			return refuse(StepStore, storeFailure(err))
		}
		s.store = store
	}
	s.defer_(s.store.Close)
	ledger, err := s.store.LedgerVersion(ctx)
	if err != nil {
		return refuse(StepStore, "the ledger's version cannot be read")
	}
	// 5. The object store: HEAD bucket.
	if client != nil {
		if err := objects.CheckAtStart(ctx, client); err != nil {
			return refuse(StepObjectStore, err.Error())
		}
	}
	// 6. The renderer and the private directories, as the demo opens them.
	formats := map[string]r.WorkbookWriter{}
	var renderer *pdf.Writer
	if p := cfg.PDF; p != nil {
		w, err := pdf.New(pdf.Config{Typst: p.Typst, TypstSHA256: p.SHA256, FontDir: p.Fonts})
		if err != nil {
			return refuse(StepPDF, "PDF export could not start: the pinned Typst binary, its SHA-256 or the font directory is refused (the renderer's own text is not shown)")
		}
		formats["pdf"], renderer = w, w
	}
	var assets *doctemplates.Assets
	var templates *doctemplates.Store
	var layoutStores []*layouts.Store
	var dashboardStore *dashboards.Store
	dirFail := func(field string) error {
		return refuse(StepDirectories, field+" cannot be opened (it must be a private directory, mode 0700, owned by the user the server runs as)")
	}
	if cfg.DesignStore == "database" {
		// S11-B: every design in the store, shared by every server on it; the four features are on (each needs nothing but the store).
		backend := func(name string) privatedocs.Backend {
			b, e := s.store.Designs(name)
			if e != nil {
				err = e
			}
			return b
		}
		layoutsB, dashboardsB, templatesB, backgroundsB, imagesB := backend("layouts"), backend("dashboards"), backend("templates"), backend("backgrounds"), backend("images")
		if err != nil {
			return refuse(StepDirectories, "the store does not keep designs")
		}
		if assets, err = doctemplates.OpenAssetsBackend(backgroundsB, imagesB); err != nil {
			return refuse(StepDirectories, "the backgrounds and images could not be opened in the store")
		}
		s.defer_(func() { assets.Close() })
		if renderer != nil {
			assets.Fits = renderer.BackgroundFits
			assets.Images.Fits = renderer.ImageFits
		}
		if templates, err = doctemplates.OpenBackend(templatesB); err != nil {
			return refuse(StepDirectories, "the templates could not be opened in the store")
		}
		s.defer_(func() { templates.Close() })
		st, err := layouts.OpenBackend(layoutsB)
		if err != nil {
			return refuse(StepDirectories, "the layouts could not be opened in the store")
		}
		s.defer_(func() { st.Close() })
		layoutStores = append(layoutStores, st)
		if dashboardStore, err = dashboards.OpenBackend(dashboardsB); err != nil {
			return refuse(StepDirectories, "the dashboards could not be opened in the store")
		}
		s.defer_(func() { dashboardStore.Close() })
	}
	if cfg.BackgroundDir != "" {
		if assets, err = doctemplates.OpenAssets(cfg.BackgroundDir); err != nil {
			return dirFail("background_directory")
		}
		s.defer_(func() { assets.Close() })
		if renderer != nil {
			assets.Fits = renderer.BackgroundFits
			assets.Images.Fits = renderer.ImageFits
		}
	}
	if cfg.TemplateDir != "" {
		if templates, err = doctemplates.Open(cfg.TemplateDir); err != nil {
			return dirFail("template_directory")
		}
		s.defer_(func() { templates.Close() })
	}
	var issuer r.Issuer
	if templates != nil && renderer != nil {
		v, err := issuance.New(templates, assets, renderer)
		if err != nil {
			return refuse(StepDirectories, "issuance could not be composed from the template and background directories")
		}
		issuer = v
	}
	if cfg.LayoutDir != "" {
		st, err := layouts.Open(cfg.LayoutDir)
		if err != nil {
			return dirFail("layout_directory")
		}
		s.defer_(func() { st.Close() })
		layoutStores = append(layoutStores, st)
	}
	if cfg.DashboardDir != "" {
		if dashboardStore, err = dashboards.Open(cfg.DashboardDir); err != nil {
			return dirFail("dashboard_directory")
		}
		s.defer_(func() { dashboardStore.Close() })
	}
	// 7. The service and the handler.
	svc, err := r.NewService(r.Config{Repository: s.store, Connections: connections, CursorKeys: keys, Writer: xlsx.Writer{}, Formats: formats, Issuer: issuer, Quotas: quotas, Objects: objectStore})
	if err != nil {
		return refuse(StepService, "the service refused the connections, the cursor ring or the quotas")
	}
	s.svc = svc
	authenticate := verifier.Authenticate
	handler, err := httpapi.Handler(svc, authenticate, layoutStores...)
	if err != nil {
		return refuse(StepService, "the API could not be composed")
	}
	compose := func(h http.Handler, err error) error {
		if err != nil {
			return refuse(StepService, "the API could not be composed")
		}
		handler = h
		return nil
	}
	if dashboardStore != nil {
		if err := compose(httpapi.WithDashboards(handler, svc, authenticate, dashboardStore)); err != nil {
			return err
		}
	}
	if assets != nil {
		if err := compose(httpapi.WithDocumentAssets(handler, svc, authenticate, assets)); err != nil {
			return err
		}
	}
	var documents httpapi.DocumentRenderer
	if renderer != nil {
		documents = renderer
		if err := compose(httpapi.WithDocumentPreviews(handler, svc, authenticate, documents, assets)); err != nil {
			return err
		}
	}
	if templates != nil {
		if err := compose(httpapi.WithDocumentTemplates(handler, svc, authenticate, templates, documents, assets)); err != nil {
			return err
		}
	}
	label := cfg.Label()
	if cfg.AllowSimulator {
		label = "simulated:" + label
	}
	if !httpapi.ValidProfileLabel(label) {
		label = "simulated"
	}
	if err := compose(httpapi.WithProfile(handler, svc, authenticate, label, embed)); err != nil {
		return err
	}
	var gauge func() int64
	{
		h, g, err := httpapi.WithMaxInFlightGauge(handler, inFlight)
		if err != nil {
			return refuse(StepService, "the API could not be composed")
		}
		handler, gauge = h, g
	}
	s.inflight = gauge
	app, err := console.HandlerWithEmbedding(handler, embed)
	if err != nil {
		return refuse(StepService, "the console could not be composed")
	}
	top := http.NewServeMux()
	s.healthHandlers(top)
	top.Handle("/", app)
	class := httpapi.DataClassBusiness
	if cfg.AllowSimulator {
		class = httpapi.DataClassSimulator
	}
	classed, err := httpapi.WithDataClass(top, class)
	if err != nil {
		return refuse(StepService, "the handler could not be composed")
	}
	s.srv = &http.Server{
		Handler:           s.access(classed),
		ReadHeaderTimeout: orDefault(cfg.ReadHeaderTimeout, DefaultReadHeaderTimeout),
		ReadTimeout:       orDefault(cfg.ReadTimeout, DefaultReadTimeout),
		WriteTimeout:      orDefault(cfg.WriteTimeout, DefaultWriteTimeout),
		IdleTimeout:       orDefault(cfg.IdleTimeout, DefaultIdleTimeout),
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          log.New(errorLogWriter{s.log}, "", 0),
	}
	if s.srv.MaxHeaderBytes == 0 {
		s.srv.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if tlsConfig != nil {
		s.srv.TLSConfig = tlsConfig
	}
	// 8. The workers and the sweep.
	s.startWorkers()
	s.defer_(func() { s.loopCancel(); s.workers.Wait(); s.workCancel() })
	// 9. Listen.
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return refuse(StepListen, "the listen address cannot be bound")
	}
	s.listener = &notifyListener{Listener: listener, onClose: s.listenerClosed}
	// 10. The metrics listener (optional, loopback only): bound after the API's, closed by the drain's cleanups.
	if cfg.MetricsListen != "" {
		ml, err := net.Listen("tcp", cfg.MetricsListen)
		if err != nil {
			_ = s.listener.Close()
			return refuse(StepListen, "the metrics_listen address cannot be bound")
		}
		s.metricsListener = ml
		s.serveMetrics(ml)
	}
	s.logStart(ledger, len(keys), len(connections), len(embed.Integrations()) > 0)
	go s.serve(ctx)
	if h := s.opts.hooks; h != nil && h.Started != nil {
		h.Started <- s
	}
	return nil
}

func orDefault(v, def int) time.Duration {
	if v == 0 {
		v = def
	}
	return seconds(v)
}

// errorLogWriter turns the standard library's server log (a panic in a handler with its stack, a failed TLS handshake with the client's
// address) into one fixed event: what it prints can carry a client's address or the text of a panic, and neither belongs in the log.
type errorLogWriter struct{ l *logger }

func (w errorLogWriter) Write(p []byte) (int, error) {
	w.l.warn("http_server_error", nil)
	return len(p), nil
}

// storeFailure is what a refused store open says. The platform's own refusals name the rule and the remedy and carry nothing of the
// connection (a fixture database, TLS, a pool size, a schema behind the binary, the role check); any other error, a connection failure for one,
// may carry the host or the user and is reduced to a fixed text.
func storeFailure(err error) string {
	for _, own := range []error{r.ErrDenied, r.ErrIntegrity, r.ErrInvalid, r.ErrConflict} {
		if errors.Is(err, own) {
			return "the store was refused: " + err.Error()
		}
	}
	return "the store could not be opened (it did not answer; the driver's text is not shown because it may name the host or the user)"
}

func (s *Server) logStart(ledger, ring, connections int, embed bool) {
	c := &s.cfg
	s.log.info("start", map[string]any{
		"version":            s.version,
		"ledger":             ledger,
		"cursor_keys":        ring,
		"connections":        connections,
		"connections_sha256": connectionsDigest(c.Connections),
		"bindings":           len(s.issuer.bindings),
		"bindings_sha256":    s.issuer.digest,
		"trusted_kids":       s.verifier.KeyIDs(),
		"workers":            c.WorkerCount(),
		"tls":                s.tls,
		"object_store":       s.objects != nil,
		"pdf":                c.PDF != nil,
		"issuance":           s.svc.CanIssue(),
		"embedding":          embed,
		"design_store":       map[bool]string{true: "database", false: "directories"}[c.DesignStore == "database"],
		"sweep_seconds":      c.Retention.SweepInterval,
		"simulators":         c.AllowSimulator,
		"listen_port":        s.listener.Addr().(*net.TCPAddr).Port,
		"metrics":            s.metricsListener != nil,
	})
}

// Addr is the address the server listens on.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// TLS says whether the server speaks TLS.
func (s *Server) TLS() bool { return s.tls }

// Done is closed when the drain is over; Wait returns the result.
func (s *Server) Done() <-chan struct{} { return s.done }

// Wait blocks until the drain is over: nil for a clean drain, ErrDrainTimeout when shutdown_grace was used up.
func (s *Server) Wait() error { <-s.done; return s.result }

// Reload re-reads the issuer files (jwks_file and bindings_file) and replaces the verifier's configuration atomically: a valid change takes
// effect from the next authentication, a failing one leaves the previous configuration in force and returns a *ReloadError with its code. It
// is what SIGHUP does. Nothing else is reloaded: the cursor ring, the quotas, the connections and TLS need a restart.
func (s *Server) Reload() error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if h := s.opts.hooks; h != nil && h.Reloaded != nil {
		defer func() {
			select {
			case h.Reloaded <- struct{}{}:
			default:
			}
		}()
	}
	files, code, err := readIssuerFiles(s.cfg.Issuer)
	if err != nil {
		s.log.warn("reload", map[string]any{"outcome": "refused", "code": code})
		s.metrics.reloaded(code)
		return &ReloadError{Code: code}
	}
	// The profile judges the files first (its refusals keep their code), then the bindings are held to the connections (ADR 0016).
	if _, err := accessjwt.New(issuerConfig(s.cfg.Issuer, files)); err != nil {
		s.log.warn("reload", map[string]any{"outcome": "refused", "code": CodeIssuerConfigRefused})
		s.metrics.reloaded(CodeIssuerConfigRefused)
		return &ReloadError{Code: CodeIssuerConfigRefused}
	}
	if unservedBindings(files.bindings, s.cfg.Connections) > 0 {
		s.log.warn("reload", map[string]any{"outcome": "refused", "code": CodeIssuerBindingUnserved})
		s.metrics.reloaded(CodeIssuerBindingUnserved)
		return &ReloadError{Code: CodeIssuerBindingUnserved}
	}
	if err := s.verifier.Replace(issuerConfig(s.cfg.Issuer, files)); err != nil {
		s.log.warn("reload", map[string]any{"outcome": "refused", "code": CodeIssuerConfigRefused})
		s.metrics.reloaded(CodeIssuerConfigRefused)
		return &ReloadError{Code: CodeIssuerConfigRefused}
	}
	added, removed, changed := bindingChanges(s.issuer.bindings, files.bindings)
	s.issuer = files
	s.log.info("reload", map[string]any{"outcome": "ok", "trusted_kids": s.verifier.KeyIDs(), "bindings": len(files.bindings), "bindings_sha256": files.digest,
		"bindings_added": added, "bindings_removed": removed, "bindings_changed": changed})
	s.metrics.reloaded("ok")
	return nil
}

// notifyListener tells the server when the drain has closed the listener.
type notifyListener struct {
	net.Listener
	once    sync.Once
	onClose func()
}

func (l *notifyListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(l.onClose)
	return err
}

func (s *Server) listenerClosed() {
	if h := s.opts.hooks; h != nil && h.ListenerClosed != nil {
		close(h.ListenerClosed)
	}
}
