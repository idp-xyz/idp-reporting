// Package server is the production composition of the reporting platform (S05-A): a strict configuration file, secrets from files and the
// environment, and the adapters a host registers, turned into a running server with health and readiness endpoints, hygienic logging, an
// atomic reload of the issuer files and a graceful shutdown. It contains no adapter of any source product and grants no production admission:
// GET /capabilities keeps saying production_approved: false.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"idp.local/reporting/delivery/email"
	"idp.local/reporting/delivery/webhook"
	"idp.local/reporting/embedding"
	"idp.local/reporting/identity/signin"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"idp.local/reporting/transport/httpapi"
)

// MaxConfigBytes bounds the configuration file.
const MaxConfigBytes = 256 << 10

// The environment variables that carry the secrets which are not files.
const (
	StoreDSNEnv = "REPORTING_STORE_DSN"
)

// Defaults and bounds of the numeric fields. Every duration is a whole number of seconds.
const (
	DefaultReadHeaderTimeout = 5
	DefaultReadTimeout       = 25
	DefaultWriteTimeout      = 25
	DefaultIdleTimeout       = 30
	MaxTimeout               = 300
	DefaultMaxHeaderBytes    = 16 << 10
	MaxMaxHeaderBytes        = 1 << 20
	DefaultWorkers           = 2
	MaxWorkers               = 32
	DefaultShutdownGrace     = 30
	MaxShutdownGrace         = 600
	MinSweepInterval         = 60
	MaxSweepInterval         = 7 * 24 * 3600
	MaxTokenLifetimeSeconds  = 3600
	MaxConnections           = 256
	MaxCursorKeyFiles        = r.MaxCursorKeys
	DefaultProfileLabel      = "unqualified"
)

// Config is the parsed, validated configuration. It holds no secret: secrets are files named here and environment variables.
type Config struct {
	Listen string `json:"listen"`
	// MetricsListen, when set, serves GET /metrics (Prometheus text) on its own listener; a loopback IP address only (docs/DEPLOYMENT.md "Metrics").
	MetricsListen      string `json:"metrics_listen"`
	TLSCertFile        string `json:"tls_cert_file"`
	TLSKeyFile         string `json:"tls_key_file"`
	AllowPlainLoopback bool   `json:"allow_plain_loopback"`
	// AllowSimulator lets Run register adapters that report themselves as contract simulators. The shipped example does not contain it.
	AllowSimulator    bool `json:"allow_simulator"`
	ReadHeaderTimeout int  `json:"read_header_timeout"`
	ReadTimeout       int  `json:"read_timeout"`
	WriteTimeout      int  `json:"write_timeout"`
	IdleTimeout       int  `json:"idle_timeout"`
	MaxHeaderBytes    int  `json:"max_header_bytes"`
	MaxInFlight       int  `json:"max_in_flight"`
	Workers           int  `json:"workers"`
	ShutdownGrace     int  `json:"shutdown_grace"`

	Store          StoreConfig        `json:"store"`
	ObjectStore    *ObjectStoreConfig `json:"object_store"`
	Issuer         IssuerConfig       `json:"issuer"`
	CursorKeyFiles []string           `json:"cursor_key_files"`
	Connections    []ConnectionConfig `json:"connections"`
	Quotas         QuotasConfig       `json:"quotas"`
	Retention      RetentionConfig    `json:"retention"`
	PDF            *PDFConfig         `json:"pdf"`
	LayoutDir      string             `json:"layout_directory"`
	DashboardDir   string             `json:"dashboard_directory"`
	TemplateDir    string             `json:"template_directory"`
	BackgroundDir  string             `json:"background_directory"`
	// DesignStore is where the designs are kept (S11-B, ADR 0017): "" or "directories" (the four private directories, one process each),
	// or "database" (the platform's store, shared by every server on it; the four directory fields must then be absent).
	DesignStore  string              `json:"design_store"`
	EmbedOrigins map[string][]string `json:"embed_origins"`
	ProfileLabel string              `json:"profile_label"`
	// SignIn, when set, is the console's browser sign-in (S15-A, ADR 0019); a confidential client's secret is the environment's.
	SignIn *SignInConfig `json:"sign_in"`
	// Schedules turns the scheduled reports on (S16-A): the routes under /schedules, and the scheduler in every server on the store.
	Schedules *SchedulesConfig `json:"schedules"`
}

// SignInConfig is the console's OpenID Connect sign-in (identity/signin): authorization code with PKCE (S256), the code exchanged by the
// server. The client secret, for a confidential client, is the environment's (REPORTING_SIGN_IN_CLIENT_SECRET), never a field.
type SignInConfig struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	ClientID              string `json:"client_id"`
	RedirectURI           string `json:"redirect_uri"`
	Scope                 string `json:"scope"`
	PasteFallback         bool   `json:"paste_fallback"`
	CAFile                string `json:"ca_file"`
	LoopbackHTTP          bool   `json:"loopback_http"`
	// Timeout bounds one exchange at the token endpoint, in seconds: 0 is the default, 10; at most 60.
	Timeout int `json:"timeout"`
}

// Client is the identity/signin configuration of the section, with the secret given (from the environment).
func (s *SignInConfig) Client(secret string) signin.Config {
	return signin.Config{AuthorizationEndpoint: s.AuthorizationEndpoint, TokenEndpoint: s.TokenEndpoint, ClientID: s.ClientID, RedirectURI: s.RedirectURI, Scope: s.Scope, PasteFallback: s.PasteFallback, CA: s.CAFile, LoopbackHTTP: s.LoopbackHTTP, Timeout: seconds(s.Timeout), ClientSecret: secret}
}

// SchedulesConfig is S16-A's section. The webhook's signing secret is the environment's (REPORTING_WEBHOOK_SECRET), never the file's, and so is
// the SMTP password of the email (REPORTING_SMTP_PASSWORD, S16-B). Either deliverer, both or neither may be configured.
type SchedulesConfig struct {
	// PollInterval is the seconds between two passes of the scheduler: 0 (the default, 30) or 5..300.
	PollInterval int `json:"poll_interval"`
	// PerIdentity is the schedules one identity may hold: 0 (the default, 20) or 1..200.
	PerIdentity int            `json:"per_identity"`
	Webhook     *WebhookConfig `json:"webhook"`
	Email       *EmailConfig   `json:"email"`
}

// WebhookConfig is the one endpoint every run's end is delivered to (delivery/webhook): https, or plain http to a loopback IP address with
// loopback_http (tests); the timeout of one attempt in seconds, 0 (the default, 10) or 1..30.
type WebhookConfig struct {
	URL          string `json:"url"`
	Timeout      int    `json:"timeout"`
	LoopbackHTTP bool   `json:"loopback_http"`
}

// EmailConfig is the mail every run's end is sent by to its owner's address of the bindings file (delivery/email, S16-B): the SMTP host (a
// name or an IP address) and port (0: 587), the sender's address, an optional username whose password is the environment's, STARTTLS (nil or
// true: required; false only to a loopback IP address with loopback_plain, tests), the timeout of one attempt in seconds, 0 (the default, 10)
// or 1..30, and the console's base URL the links use (https; plain http only to a loopback IP address with loopback_plain).
type EmailConfig struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	From          string `json:"from"`
	Username      string `json:"username"`
	StartTLS      *bool  `json:"starttls"`
	LoopbackPlain bool   `json:"loopback_plain"`
	Timeout       int    `json:"timeout"`
	ConsoleURL    string `json:"console_url"`
}

// startTLS is the field with its default: on unless the file says false.
func (e *EmailConfig) startTLS() bool { return e.StartTLS == nil || *e.StartTLS }

// Mailer is the delivery/email configuration of the section, with the password given (from the environment) and the server's two readers:
// the owner's addresses in the bindings in force, and a schedule's name.
func (e *EmailConfig) Mailer(password string, recipients func(r.Identity) []string, names func(context.Context, r.Identity, string) (string, error)) email.Config {
	return email.Config{Host: e.Host, Port: e.Port, From: e.From, Username: e.Username, Password: password, StartTLS: e.startTLS(), LoopbackPlain: e.LoopbackPlain,
		Timeout: seconds(e.Timeout), ConsoleURL: e.ConsoleURL, Recipients: recipients, Names: names}
}

// The bounds of the schedules section.
const (
	DefaultSchedulePoll = 30
	MinSchedulePoll     = 5
	MaxSchedulePoll     = 300
)

// StoreConfig is the platform store's non-secret configuration; the connection string is the environment's (REPORTING_STORE_DSN).
type StoreConfig struct {
	// PoolMaxConns is the pool size: 0 (the default, 8 raised to workers+2) or workers+2 to 100, as OpenProduction checks it.
	PoolMaxConns int `json:"pool_max_conns"`
}

// ObjectStoreConfig is S04-E's flags as a section; the keys are the environment's (REPORTING_OBJECT_STORE_ACCESS_KEY and _SECRET_KEY).
type ObjectStoreConfig struct {
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	Region       string `json:"region"`
	CAFile       string `json:"ca_file"`
	LoopbackHTTP bool   `json:"loopback_http"`
}

// IssuerConfig is the access-token profile of identity/accessjwt; the keys and the bindings are files.
type IssuerConfig struct {
	Issuer           string `json:"issuer"`
	Audience         string `json:"audience"`
	RequiredScope    string `json:"required_scope"`
	MaxTokenLifetime int    `json:"max_token_lifetime"`
	JWKSFile         string `json:"jwks_file"`
	BindingsFile     string `json:"bindings_file"`
}

// ConnectionConfig is one (tenant, integration) the server serves and the registry entry that serves it. Options go to the adapter's factory
// untouched and are never logged.
type ConnectionConfig struct {
	Tenant       string          `json:"tenant"`
	Integration  string          `json:"integration"`
	SourceTenant string          `json:"source_tenant"`
	Adapter      string          `json:"adapter"`
	Options      json.RawMessage `json:"options"`
}

// QuotasConfig is S04-D's and S04-G's four quotas; zero is the default of each.
type QuotasConfig struct {
	JobsInFlight      int `json:"jobs_in_flight"`
	StoredResults     int `json:"stored_results"`
	AdmissionsPerHour int `json:"admissions_per_hour"`
	QueriesInFlight   int `json:"queries_in_flight"`
}

// RetentionConfig is the sweep loop; SweepInterval 0 (the default) turns it off.
type RetentionConfig struct {
	SweepInterval int `json:"sweep_interval"`
	Grace         int `json:"grace"`
	Batch         int `json:"batch"`
}

// PDFConfig is the pinned Typst renderer: the three fields together.
type PDFConfig struct {
	Typst  string `json:"typst"`
	SHA256 string `json:"sha256"`
	Fonts  string `json:"fonts"`
}

// ConfigError is a refusal of the configuration. Its text names the field and the rule, never a value from the file.
type ConfigError struct{ Text string }

func (e *ConfigError) Error() string { return "configuration: " + e.Text }

func configProblem(format string, args ...any) error {
	return &ConfigError{Text: fmt.Sprintf(format, args...)}
}

// LoadConfig reads, parses and validates the configuration file at path. The file is at most 256 KiB, valid UTF-8, one JSON document whose
// objects have no duplicate key and no field the schema does not name (with its exact spelling), and holds no field named like a secret.
// It checks the file's own content; the files it names are checked when the server starts (and by `check-config`).
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, configProblem("the configuration file cannot be opened")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return Config{}, configProblem("the configuration file cannot be read")
	}
	return ParseConfig(data)
}

// ParseConfig is LoadConfig on the bytes of the file.
func ParseConfig(data []byte) (Config, error) {
	var c Config
	if len(data) > MaxConfigBytes {
		return c, configProblem("the file is larger than %d bytes", MaxConfigBytes)
	}
	if !utf8.Valid(data) {
		return c, configProblem("the file is not valid UTF-8")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return c, configProblem("the file is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tree, err := readTree(dec, 0)
	if err != nil {
		return c, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return c, configProblem("data follows the configuration document")
	}
	if err := checkShape(tree, reflect.TypeOf(c), "", nil); err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		var typed *json.UnmarshalTypeError
		if errors.As(err, &typed) && typed.Field != "" {
			return Config{}, configProblem("the field %q has the wrong type or is out of range", typed.Field)
		}
		return Config{}, configProblem("the file does not fit the schema")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

const maxConfigDepth = 12

// readTree reads one JSON value into maps, slices, json.Number, strings and bools, refusing a duplicate key, a null and a nesting deeper
// than maxConfigDepth. Syntax errors are a fixed text: the library's repeats a character of the file.
func readTree(d *json.Decoder, depth int) (any, error) {
	if depth > maxConfigDepth {
		return nil, configProblem("the document is nested too deeply")
	}
	t, err := d.Token()
	if err != nil {
		return nil, configProblem("the file is not valid JSON")
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, err := d.Token()
				key, ok := k.(string)
				if err != nil || !ok {
					return nil, configProblem("the file is not valid JSON")
				}
				if _, dup := m[key]; dup {
					return nil, configProblem("an object has the same key twice")
				}
				child, err := readTree(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[key] = child
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, configProblem("the file is not valid JSON")
			}
			return m, nil
		case '[':
			list := []any{}
			for d.More() {
				child, err := readTree(d, depth+1)
				if err != nil {
					return nil, err
				}
				list = append(list, child)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, configProblem("the file is not valid JSON")
			}
			return list, nil
		}
		return nil, configProblem("the file is not valid JSON")
	case nil:
		return nil, configProblem("null is not a value of any field")
	}
	return t, nil
}

// checkShape walks the parsed document against the struct it must fit: a key the struct does not name, or names with another spelling,
// is refused ("unknown field"), and a key that looks like a secret is refused with its own text. Neither text repeats the key. Fields of type
// json.RawMessage (the adapters' options) are free-form; their keys are still checked for secret-like names. path names the struct's fields,
// from the schema, never from the file.
func checkShape(v any, t reflect.Type, path string, fields func(string) bool) error {
	where := path
	if where == "" {
		where = "the top level"
	}
	switch t.Kind() {
	case reflect.Pointer:
		return checkShape(v, t.Elem(), path, nil)
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return configProblem("%s must be an object", where)
		}
		known := map[string]reflect.StructField{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
				known[name] = f
			}
		}
		for key, child := range m {
			f, ok := known[key]
			if !ok {
				if secretLike(key) {
					return configProblem("a field of %s is named like a secret; secrets are never in the file (files and the environment carry them)", where)
				}
				return configProblem("%s has a field the schema does not know", where)
			}
			name := key
			if path != "" {
				name = path + "." + key
			}
			if err := checkShape(child, f.Type, name, nil); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if t == reflect.TypeOf(json.RawMessage(nil)) {
			return scanFree(v, path, 0)
		}
		list, ok := v.([]any)
		if !ok {
			return configProblem("%s must be a list", where)
		}
		for i, child := range list {
			if err := checkShape(child, t.Elem(), fmt.Sprintf("%s[%d]", path, i), nil); err != nil {
				return err
			}
		}
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return configProblem("%s must be an object", where)
		}
		for _, child := range m {
			if err := checkShape(child, t.Elem(), path+"[]", nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanFree refuses a secret-like key anywhere in a free-form value.
func scanFree(v any, path string, depth int) error {
	switch x := v.(type) {
	case map[string]any:
		for key, child := range x {
			if secretLike(key) {
				return configProblem("a key inside %s is named like a secret; secrets are never in the file", path)
			}
			if err := scanFree(child, path, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := scanFree(child, path, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// secretLike says whether a field name reads like one that would hold a secret: a connection string, a password, a secret, a token, a
// credential, a passphrase, or a key (a name ending in key or keys, or with a key word that is not followed by file/id/path/name). The
// schema's own names that contain such a word (max_token_lifetime, cursor_key_files, tls_key_file) are known fields and never reach this.
func secretLike(name string) bool {
	var spaced strings.Builder
	for i, c := range name {
		if i > 0 && c >= 'A' && c <= 'Z' && name[i-1] >= 'a' && name[i-1] <= 'z' {
			spaced.WriteByte('_') // camelCase: accessKey is access_key
		}
		spaced.WriteRune(c)
	}
	words := strings.FieldsFunc(strings.ToLower(spaced.String()), func(c rune) bool { return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') })
	for i, w := range words {
		switch w {
		case "dsn", "password", "passwd", "pwd", "passphrase", "secret", "secrets", "token", "tokens", "credential", "credentials", "apikey", "privatekey", "bearer", "jwt":
			return true
		case "key", "keys":
			if i+1 == len(words) {
				return true
			}
			switch words[i+1] {
			case "file", "files", "id", "ids", "path", "name":
			default:
				return true
			}
		}
	}
	return false
}

// Validate checks everything the file alone decides. It is idempotent, and Start calls it again for a Config that was built by hand.
func (c *Config) Validate() error {
	// Listening.
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || port == "" {
		return configProblem("listen must be host:port")
	}
	if n, err := net.LookupPort("tcp", port); err != nil || n < 0 || n > 65535 {
		return configProblem("listen has an invalid port")
	}
	loopback := false
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	tls := c.TLSCertFile != "" || c.TLSKeyFile != ""
	switch {
	case (c.TLSCertFile == "") != (c.TLSKeyFile == ""):
		return configProblem("tls_cert_file and tls_key_file are given together")
	case tls && c.AllowPlainLoopback:
		return configProblem("allow_plain_loopback and a TLS certificate are contradictory")
	case !tls && !c.AllowPlainLoopback:
		return configProblem("tls_cert_file and tls_key_file are required (plain HTTP is only for a loopback listen address with allow_plain_loopback)")
	case !tls && !loopback:
		return configProblem("plain HTTP is refused on a listen address that is not a loopback IP address")
	}
	if c.MetricsListen != "" {
		mhost, mport, err := net.SplitHostPort(c.MetricsListen)
		if err != nil || mport == "" {
			return configProblem("metrics_listen must be host:port")
		}
		if n, err := net.LookupPort("tcp", mport); err != nil || n < 0 || n > 65535 {
			return configProblem("metrics_listen has an invalid port")
		}
		if ip := net.ParseIP(mhost); ip == nil || !ip.IsLoopback() {
			return configProblem("metrics_listen must be a loopback IP address (the metrics carry no authentication; a scraper on another host reaches them through a local proxy or agent)")
		}
		// Port 0 lets the system choose, so two zeros never collide; a fixed port given to both would refuse the second bind.
		if apiHost, apiPort, err := net.SplitHostPort(c.Listen); err == nil && mport != "0" && mport == apiPort && (mhost == apiHost || apiHost == "" || net.ParseIP(apiHost).IsUnspecified()) {
			return configProblem("metrics_listen must not be the port of listen")
		}
	}
	for name, v := range map[string]int{"read_header_timeout": c.ReadHeaderTimeout, "read_timeout": c.ReadTimeout, "write_timeout": c.WriteTimeout, "idle_timeout": c.IdleTimeout} {
		if v < 0 || v > MaxTimeout {
			return configProblem("%s must be 0 (the default) or between 1 and %d seconds", name, MaxTimeout)
		}
	}
	if c.MaxHeaderBytes < 0 || c.MaxHeaderBytes > MaxMaxHeaderBytes || c.MaxHeaderBytes > 0 && c.MaxHeaderBytes < 1024 {
		return configProblem("max_header_bytes must be 0 (the default) or between 1024 and %d", MaxMaxHeaderBytes)
	}
	if c.MaxInFlight < 0 || c.MaxInFlight > httpapi.MaxMaxInFlight {
		return configProblem("max_in_flight must be 0 (the default) or between 1 and %d", httpapi.MaxMaxInFlight)
	}
	if c.Workers < 0 || c.Workers > MaxWorkers {
		return configProblem("workers must be 0 (the default, %d) or between 1 and %d", DefaultWorkers, MaxWorkers)
	}
	if c.ShutdownGrace < 0 || c.ShutdownGrace > MaxShutdownGrace {
		return configProblem("shutdown_grace must be 0 (the default, %d seconds) or between 1 and %d", DefaultShutdownGrace, MaxShutdownGrace)
	}
	// The store: the pool bounds are OpenProduction's.
	if c.Store.PoolMaxConns != 0 {
		floor := c.WorkerCount() + 2
		if c.Store.PoolMaxConns < floor || c.Store.PoolMaxConns > postgres.MaxPoolConns {
			return configProblem("store.pool_max_conns must be 0 (the default) or between %d (workers plus 2) and %d", floor, postgres.MaxPoolConns)
		}
	}
	if o := c.ObjectStore; o != nil {
		if o.Endpoint == "" || o.Bucket == "" {
			return configProblem("object_store needs endpoint and bucket")
		}
	}
	// The issuer: accessjwt's own rules apply when the verifier is built; the ones that need no file are checked now.
	i := c.Issuer
	if i.Issuer == "" || i.Audience == "" || i.RequiredScope == "" || i.JWKSFile == "" || i.BindingsFile == "" {
		return configProblem("issuer needs issuer, audience, required_scope, max_token_lifetime, jwks_file and bindings_file")
	}
	if i.MaxTokenLifetime < 1 || i.MaxTokenLifetime > MaxTokenLifetimeSeconds {
		return configProblem("issuer.max_token_lifetime is required, in seconds, between 1 and %d", MaxTokenLifetimeSeconds)
	}
	if len(c.CursorKeyFiles) < 1 || len(c.CursorKeyFiles) > MaxCursorKeyFiles {
		return configProblem("cursor_key_files holds between 1 and %d files (newest first), not %d", MaxCursorKeyFiles, len(c.CursorKeyFiles))
	}
	seen := map[string]bool{}
	for _, f := range c.CursorKeyFiles {
		if seen[f] {
			return configProblem("cursor_key_files names a file twice")
		}
		seen[f] = true
	}
	// Designs (S11-B).
	switch c.DesignStore {
	case "", "directories":
	case "database":
		if c.LayoutDir != "" || c.DashboardDir != "" || c.TemplateDir != "" || c.BackgroundDir != "" {
			return configProblem("design_store database keeps every design in the store: layout_directory, dashboard_directory, template_directory and background_directory must be absent")
		}
	default:
		return configProblem("design_store is directories or database")
	}
	// Connections.
	if len(c.Connections) == 0 || len(c.Connections) > MaxConnections {
		return configProblem("connections holds between 1 and %d entries", MaxConnections)
	}
	pairs := map[[2]string]bool{}
	for n, v := range c.Connections {
		if v.Tenant == "" || v.Integration == "" || v.SourceTenant == "" || v.Adapter == "" {
			return configProblem("connections[%d] needs tenant, integration, source_tenant and adapter", n)
		}
		k := [2]string{v.Tenant, v.Integration}
		if pairs[k] {
			return configProblem("connections[%d] repeats the (tenant, integration) of an earlier entry", n)
		}
		pairs[k] = true
		if len(v.Options) > 0 && !bytes.HasPrefix(bytes.TrimSpace(v.Options), []byte("{")) {
			return configProblem("connections[%d].options must be an object", n)
		}
	}
	if _, err := c.QuotaValues(); err != nil {
		return err
	}
	ret := c.Retention
	if ret.SweepInterval != 0 && (ret.SweepInterval < MinSweepInterval || ret.SweepInterval > MaxSweepInterval) {
		return configProblem("retention.sweep_interval must be 0 (off, the default) or between %d and %d seconds", MinSweepInterval, MaxSweepInterval)
	}
	if _, err := (r.SweepOptions{Grace: seconds(ret.Grace), Batch: ret.Batch}).Resolved(); err != nil {
		return configProblem("retention.grace is 0 (the default, 24 hours) up to %d seconds and retention.batch 0 (the default, %d) up to %d", int64(r.MaxSweepGrace.Seconds()), r.DefaultSweepBatch, r.MaxSweepBatch)
	}
	if p := c.PDF; p != nil && (p.Typst == "" || p.SHA256 == "" || p.Fonts == "") {
		return configProblem("pdf needs typst, sha256 and fonts together")
	}
	if sc := c.Schedules; sc != nil {
		if sc.PollInterval != 0 && (sc.PollInterval < MinSchedulePoll || sc.PollInterval > MaxSchedulePoll) {
			return configProblem("schedules.poll_interval must be 0 (the default, %d seconds) or between %d and %d", DefaultSchedulePoll, MinSchedulePoll, MaxSchedulePoll)
		}
		if sc.PerIdentity < 0 || sc.PerIdentity > r.MaxSchedulesPerIdentity {
			return configProblem("schedules.per_identity must be 0 (the default, %d) or between 1 and %d", r.DefaultSchedulesPerIdentity, r.MaxSchedulesPerIdentity)
		}
		if h := sc.Webhook; h != nil {
			if !webhook.ValidURL(h.URL, h.LoopbackHTTP) {
				return configProblem("schedules.webhook.url must be an https URL without user information or fragment (plain http only to a loopback IP address, with loopback_http)")
			}
			if h.Timeout < 0 || h.Timeout > int(webhook.MaxTimeout/time.Second) {
				return configProblem("schedules.webhook.timeout must be 0 (the default, %d seconds) or between 1 and %d", int(webhook.DefaultTimeout/time.Second), int(webhook.MaxTimeout/time.Second))
			}
		}
		if m := sc.Email; m != nil {
			if m.Host == "" || m.From == "" || m.ConsoleURL == "" {
				return configProblem("schedules.email needs host, from and console_url")
			}
			if !email.ValidHost(m.Host) {
				return configProblem("schedules.email.host must be a host name or an IP address without a port")
			}
			if m.Port < 0 || m.Port > 65535 {
				return configProblem("schedules.email.port must be 0 (the default, %d) or between 1 and 65535", email.DefaultPort)
			}
			if !email.ValidAddress(m.From) {
				return configProblem("schedules.email.from must be one address (an RFC 5322 addr-spec of printable ASCII, at most %d characters, no display name)", email.MaxAddressBytes)
			}
			if m.Username != "" && (len(m.Username) > 256 || strings.ContainsFunc(m.Username, func(c rune) bool { return c < 0x21 || c > 0x7e })) {
				return configProblem("schedules.email.username is 1 to 256 printable ASCII characters without a space")
			}
			loopback := false
			if ip := net.ParseIP(m.Host); ip != nil {
				loopback = ip.IsLoopback()
			}
			if !m.startTLS() && !(m.LoopbackPlain && loopback) {
				return configProblem("schedules.email.starttls false is only for a loopback IP address host with loopback_plain (tests); a server that offers no STARTTLS gets no mail")
			}
			if m.Timeout < 0 || m.Timeout > int(email.MaxTimeout/time.Second) {
				return configProblem("schedules.email.timeout must be 0 (the default, %d seconds) or between 1 and %d", int(email.DefaultTimeout/time.Second), int(email.MaxTimeout/time.Second))
			}
			if !email.ValidConsoleURL(m.ConsoleURL, m.LoopbackPlain) {
				return configProblem("schedules.email.console_url must be the console's https base URL without user information, query or fragment (plain http only to a loopback IP address, with loopback_plain)")
			}
		}
	}
	if c.ProfileLabel != "" && !httpapi.ValidProfileLabel(c.ProfileLabel) {
		return configProblem("profile_label is 1 to %d printable ASCII characters", httpapi.MaxProfileLabel)
	}
	if _, err := c.Embedding(); err != nil {
		return err
	}
	if si := c.SignIn; si != nil {
		if si.Timeout < 0 || si.Timeout > int(signin.MaxTimeout/time.Second) {
			return configProblem("sign_in.timeout must be 0 (the default, 10 seconds) or between 1 and %d seconds", int(signin.MaxTimeout/time.Second))
		}
		if err := si.Client("").Check(); err != nil {
			return configProblem("sign_in.%s", err.Error())
		}
		if si.CAFile != "" && strings.HasPrefix(si.TokenEndpoint, "http://") {
			return configProblem("sign_in.ca_file is for an https:// token_endpoint")
		}
	}
	// Every path the file names is absolute and clean: the working directory of a service is not a place to look for files.
	for name, p := range c.paths() {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return configProblem("%s must be an absolute, clean path", name)
		}
	}
	return nil
}

// paths lists every file or directory the configuration names, by field.
func (c *Config) paths() map[string]string {
	out := map[string]string{}
	add := func(name, p string) {
		if p != "" {
			out[name] = p
		}
	}
	add("tls_cert_file", c.TLSCertFile)
	add("tls_key_file", c.TLSKeyFile)
	add("issuer.jwks_file", c.Issuer.JWKSFile)
	add("issuer.bindings_file", c.Issuer.BindingsFile)
	for i, p := range c.CursorKeyFiles {
		add(fmt.Sprintf("cursor_key_files[%d]", i), p)
	}
	if c.ObjectStore != nil {
		add("object_store.ca_file", c.ObjectStore.CAFile)
	}
	if c.PDF != nil {
		add("pdf.typst", c.PDF.Typst)
		add("pdf.fonts", c.PDF.Fonts)
	}
	if c.SignIn != nil {
		add("sign_in.ca_file", c.SignIn.CAFile)
	}
	add("layout_directory", c.LayoutDir)
	add("dashboard_directory", c.DashboardDir)
	add("template_directory", c.TemplateDir)
	add("background_directory", c.BackgroundDir)
	return out
}

// WorkerCount is the number of worker loops, with the default applied.
func (c *Config) WorkerCount() int {
	if c.Workers == 0 {
		return DefaultWorkers
	}
	return c.Workers
}

// QuotaValues resolves the four quotas (zero is the default of each); a value out of its bounds is a configuration problem naming the field.
func (c *Config) QuotaValues() (r.Quotas, error) {
	q := c.Quotas
	for _, f := range []struct {
		name     string
		v, bound int
	}{{"quotas.jobs_in_flight", q.JobsInFlight, r.MaxJobsInFlight}, {"quotas.stored_results", q.StoredResults, r.MaxStoredResults}, {"quotas.admissions_per_hour", q.AdmissionsPerHour, r.MaxAdmissionsPerHour}, {"quotas.queries_in_flight", q.QueriesInFlight, r.MaxQueriesInFlight}} {
		if f.v < 0 || f.v > f.bound {
			return r.Quotas{}, configProblem("%s must be 0 (the default) or between 1 and %d", f.name, f.bound)
		}
	}
	resolved, err := r.Quotas{JobsInFlight: q.JobsInFlight, StoredResults: q.StoredResults, AdmissionsPerHour: q.AdmissionsPerHour, QueriesInFlight: q.QueriesInFlight}.Resolved()
	if err != nil {
		return r.Quotas{}, configProblem("the quotas are out of their bounds")
	}
	return resolved, nil
}

// Embedding validates embed_origins as embedding.New does (https origins only: the loopback http option is the demo's and the tests'), and
// refuses an integration that no connection serves.
func (c *Config) Embedding() (*embedding.Config, error) {
	e, err := embedding.New(c.EmbedOrigins, embedding.Options{})
	if err != nil {
		return nil, configProblem("embed_origins: %s", strings.TrimPrefix(err.Error(), "embedding: "))
	}
	served := map[string]bool{}
	for _, v := range c.Connections {
		served[v.Integration] = true
	}
	for _, integration := range e.Integrations() {
		if !served[integration] {
			return nil, configProblem("embed_origins names an integration that no connection serves")
		}
	}
	return e, nil
}

// Label is the profile label shown in /capabilities.
func (c *Config) Label() string {
	if c.ProfileLabel == "" {
		return DefaultProfileLabel
	}
	return c.ProfileLabel
}
