// Package httpsource is the platform's generic source adapter (S19-A, docs/SOURCE_HTTP_PROTOCOL.md): it speaks the reporting source
// protocol, JSON over HTTPS, to a source product that implements it, so that a product connects by implementing a fixed protocol instead of
// the platform carrying Go code for each product. It is registered in the shipped server as the adapter `http`.
//
// Nothing a source answers is passed on before it verifies against what was asked: the catalog's datasets, the read's query hash,
// dataset, companies, zone, currency policy and expiry, every page's reference, hash, watermark, row widths and cell types, totals and
// cursor, the authorization's answer, the reservation's job and number and every time, the directory's companies (S19-B, for a
// connection that declares them). An answer that does not verify is
// reporting.ErrIntegrity; a source that cannot be reached, is too slow, redirects or answers a 5xx is reporting.ErrUnavailable; a refusal
// in the protocol's closed set of codes is the platform error of its code. Error texts are fixed: never the URL, the credential or a byte
// of the body. It is safe for concurrent use and depends on the standard library only.
package httpsource

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"idp.local/reporting/adapters/httpsource/protocol"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/tracecontext"
)

// The issuance modes of a connection: whether the source is the document authority of ADR 0003, and of which contract version.
const (
	IssuanceNone   = "none"   // the default: the adapter is not a document authority, and issuing through it is unsupported
	IssuanceRows   = "rows"   // contract version 1: one row per document
	IssuanceGroups = "groups" // contract version 2 (S03-K): a row group per document as well
)

const (
	DefaultTimeout = 10 * time.Second
	MaxTimeout     = 60 * time.Second
	// MinResponseBytes and protocol.MaxResponseBytes bound max_response_bytes; the default is the maximum.
	MinResponseBytes = 4096
	maxCatalogCache  = 1024
)

// Config is an adapter's configuration as Go values. Factory builds it from a connection's options and the environment.
type Config struct {
	// BaseURL is the source's https:// URL (a path prefix allowed, no user, query or fragment); http:// only to a loopback IP address with
	// LoopbackHTTP.
	BaseURL      string
	LoopbackHTTP bool
	// Timeout bounds one request, connection to the last byte of the answer: 0 is DefaultTimeout, at most MaxTimeout.
	Timeout time.Duration
	// RootCAs, when set, replaces the system's roots for the source's certificate.
	RootCAs *x509.CertPool
	// Certificate, when set, is the platform's client certificate (mTLS).
	Certificate *tls.Certificate
	// Token, when set, is sent as `Authorization: Bearer`. A connection needs a token or a client certificate, unless LoopbackHTTP.
	Token string
	// MaxResponseBytes bounds every answer: 0 is protocol.MaxResponseBytes, the bounds MinResponseBytes..protocol.MaxResponseBytes.
	MaxResponseBytes int
	// Issuance is IssuanceNone (or ""), IssuanceRows or IssuanceGroups.
	Issuance string
	// Companies declares that the source implements the company directory (ADR 0015, S19-B): the adapter is then a
	// reporting.CompanyDirectory and asks POST /reporting-source/v1/companies. Without it the route is never asked and the platform
	// answers supported false, as for any adapter without the capability.
	Companies bool
	// Now is the clock a read's expiry is judged by; nil is time.Now.
	Now func() time.Time
}

// Options is a connection's `options` object (docs/DEPLOYMENT.md). No field holds a secret: AuthEnv names the environment variable that
// holds the bearer credential, and the key of the client certificate is a file.
type Options struct {
	BaseURL          string `json:"base_url"`
	LoopbackHTTP     bool   `json:"loopback_http"`
	Timeout          int    `json:"timeout"`
	CAFile           string `json:"ca_file"`
	ClientCertFile   string `json:"client_cert_file"`
	ClientKeyFile    string `json:"client_key_file"`
	AuthEnv          string `json:"auth_env"`
	MaxResponseBytes int    `json:"max_response_bytes"`
	Issuance         string `json:"issuance"`
	Companies        bool   `json:"companies"`
}

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// errConfig is every refusal of a configuration: a fixed text naming the field, never a value.
func errConfig(field, rule string) error {
	return fmt.Errorf("httpsource: %s %s (the value is not shown)", field, rule)
}

// Factory is the server's AdapterFactory for the adapter `http`: the options are read strictly, the files they name are read now (a
// failure refuses the start) and the credential comes from the environment variable auth_env names.
func Factory(options json.RawMessage, getenv func(string) string) (r.Adapter, error) {
	var o Options
	if len(options) == 0 || r.DecodeStrict(bytes.NewReader(options), &o) != nil {
		return nil, errConfig("options", "must be one object of the documented fields")
	}
	c, err := o.Config(getenv)
	if err != nil {
		return nil, err
	}
	return New(c)
}

// Config resolves the options: reads the CA and the client certificate and its key, and the credential from the environment.
func (o Options) Config(getenv func(string) string) (Config, error) {
	c := Config{BaseURL: o.BaseURL, LoopbackHTTP: o.LoopbackHTTP, MaxResponseBytes: o.MaxResponseBytes, Issuance: o.Issuance, Companies: o.Companies}
	if o.Timeout < 0 || o.Timeout > int(MaxTimeout/time.Second) {
		return Config{}, errConfig("timeout", "must be 0 or 1..60 seconds")
	}
	c.Timeout = time.Duration(o.Timeout) * time.Second
	if o.CAFile != "" {
		b, err := readFile(o.CAFile, false)
		if err != nil {
			return Config{}, errConfig("ca_file", "must be an absolute path to a readable regular file of at most 64 KiB")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return Config{}, errConfig("ca_file", "must hold at least one PEM certificate")
		}
		c.RootCAs = pool
	}
	if (o.ClientCertFile == "") != (o.ClientKeyFile == "") {
		return Config{}, errConfig("client_cert_file, client_key_file", "must be both or neither")
	}
	if o.ClientCertFile != "" {
		cert, err := readFile(o.ClientCertFile, false)
		if err != nil {
			return Config{}, errConfig("client_cert_file", "must be an absolute path to a readable regular file of at most 64 KiB")
		}
		key, err := readFile(o.ClientKeyFile, true)
		if err != nil {
			return Config{}, errConfig("client_key_file", "must be an absolute path to a regular file of at most 64 KiB, mode 0600 or 0400")
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return Config{}, errConfig("client_cert_file, client_key_file", "must be a PEM certificate and its private key")
		}
		c.Certificate = &pair
	}
	if o.AuthEnv != "" {
		if !envName.MatchString(o.AuthEnv) {
			return Config{}, errConfig("auth_env", "must be the name of an environment variable (A-Z, 0-9, _)")
		}
		c.Token = getenv(o.AuthEnv)
		if c.Token == "" {
			return Config{}, errConfig("auth_env", "names an environment variable that is not set")
		}
	}
	return c, nil
}

// readFile reads a configuration file: absolute and clean, not a symbolic link, regular, at most 64 KiB; a key must not be readable by
// group or others.
func readFile(name string, secret bool) ([]byte, error) {
	if !path.IsAbs(name) || path.Clean(name) != name {
		return nil, os.ErrInvalid
	}
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || (secret && info.Mode().Perm()&0o077 != 0) {
		return nil, os.ErrInvalid
	}
	return os.ReadFile(name)
}

// Adapter is a source over the protocol. It implements reporting.Adapter; New returns it wrapped as a document authority when the
// connection's issuance says so, and as a company directory when the connection declares companies.
type Adapter struct {
	base    string
	token   string
	timeout time.Duration
	max     int
	now     func() time.Time
	client  *http.Client
	tr      *http.Transport

	mu      sync.Mutex
	catalog map[catalogKey]r.Dataset
}

type catalogKey struct {
	integration, sourceTenant string
	dataset                   r.Ref
}

// New checks the configuration and makes the adapter. Its errors are fixed texts naming the field. The value is the adapter wrapped for
// the capabilities the connection declares: a document authority of the issuance's contract version, a company directory with
// Companies; the method set of the value says what the connection declared, as it does for an adapter in process.
func New(c Config) (r.Adapter, error) {
	a, err := newAdapter(c)
	if err != nil {
		return nil, err
	}
	switch c.Issuance {
	case "", IssuanceNone:
		if c.Companies {
			return &Directory{Adapter: a}, nil
		}
		return a, nil
	case IssuanceRows:
		if c.Companies {
			return &AuthorityDirectory{Authority: &Authority{Adapter: a}}, nil
		}
		return &Authority{Adapter: a}, nil
	case IssuanceGroups:
		if c.Companies {
			return &GroupAuthorityDirectory{GroupAuthority: &GroupAuthority{Authority{Adapter: a, groups: true}}}, nil
		}
		return &GroupAuthority{Authority{Adapter: a, groups: true}}, nil
	}
	return nil, errConfig("issuance", `must be "none", "rows" or "groups"`)
}

func newAdapter(c Config) (*Adapter, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return nil, errConfig("base_url", "must be https://host[:port][/path] without user, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		ip := net.ParseIP(u.Hostname())
		if !c.LoopbackHTTP || ip == nil || !ip.IsLoopback() {
			return nil, errConfig("base_url", "must be https://, or http:// to a loopback IP address with loopback_http")
		}
	default:
		return nil, errConfig("base_url", "must be https://")
	}
	p := strings.TrimSuffix(u.Path, "/")
	if p != "" && (path.Clean(p) != p || !strings.HasPrefix(p, "/")) {
		return nil, errConfig("base_url", "must have a clean path")
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	if c.Timeout < 0 || c.Timeout > MaxTimeout {
		return nil, errConfig("timeout", "must be 0 or 1..60 seconds")
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = protocol.MaxResponseBytes
	}
	if c.MaxResponseBytes < MinResponseBytes || c.MaxResponseBytes > protocol.MaxResponseBytes {
		return nil, errConfig("max_response_bytes", "must be 0 or 4096..1048576")
	}
	if c.Token != "" && !protocol.Printable(c.Token, 4096) {
		return nil, errConfig("auth_env", "names a credential that is not 1..4096 printable characters without a space")
	}
	if c.Token == "" && c.Certificate == nil && !c.LoopbackHTTP {
		return nil, errConfig("auth_env, client_cert_file", "must name a credential: a bearer credential, a client certificate or both")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	tlsc := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.RootCAs}
	if c.Certificate != nil {
		tlsc.Certificates = []tls.Certificate{*c.Certificate}
	}
	dialer := &net.Dialer{Timeout: c.Timeout, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                  nil, // a proxy is the host's network, not an environment variable the process inherits
		DialContext:            dialer.DialContext,
		TLSClientConfig:        tlsc,
		TLSHandshakeTimeout:    c.Timeout,
		ResponseHeaderTimeout:  c.Timeout,
		MaxIdleConns:           32,
		MaxIdleConnsPerHost:    32,
		IdleConnTimeout:        90 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      true,
	}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Adapter{base: u.Scheme + "://" + u.Host + p, token: c.Token, timeout: c.Timeout, max: c.MaxResponseBytes, now: c.Now, client: client, tr: tr, catalog: map[catalogKey]r.Dataset{}}, nil
}

// Close releases the idle connections (the server calls it when it stops).
func (a *Adapter) Close() { a.tr.CloseIdleConnections() }

// Error is every error the adapter returns: a fixed text for the operation and the reason, the platform error it is (errors.Is), and the
// request id the source was sent, when a request was made. It never holds the URL, the credential or a byte of an answer.
type Error struct {
	Op        string
	Reason    string
	RequestID string
	Err       error
}

func (e *Error) Error() string {
	s := "http source " + e.Op + ": " + e.Reason + ": " + e.Err.Error()
	if e.RequestID != "" {
		s += " (request " + e.RequestID + ")"
	}
	return s
}
func (e *Error) Unwrap() error { return e.Err }

// Is makes two of the adapter's errors the same error when they are the same platform error, as two answers of an in-process adapter are
// (a caller that compares one refusal with another, errors.Is(e, earlier), sees no difference between the adapters).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && errors.Is(e.Err, t.Err)
}

func fail(op, reason string, err error) error { return &Error{Op: op, Reason: reason, Err: err} }

// call sends one request and decodes its answer into out. What is not a 200 with a strict JSON body of the answer's shape is mapped by the
// protocol's table (refusal).
func (a *Adapter) call(parent context.Context, op, route string, issue bool, in, out any) error {
	if e := parent.Err(); e != nil {
		return e
	}
	body, err := json.Marshal(in)
	if err != nil || len(body) > protocol.MaxRequestBytes {
		return fail(op, "the request is over the protocol's bound", r.ErrBudget)
	}
	id := r.NewID()
	ctx, cancel := context.WithTimeout(parent, a.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+route, bytes.NewReader(body))
	if err != nil {
		return fail(op, "the request cannot be made", r.ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(protocol.RequestIDHeader, id)
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	if tp := tracecontext.Traceparent(ctx); tp != "" {
		req.Header.Set("traceparent", tp)
	}
	wrap := func(reason string, e error) error { return &Error{Op: op, Reason: reason, RequestID: id, Err: e} }
	resp, err := a.client.Do(req)
	if err != nil {
		if e := parent.Err(); e != nil {
			return e
		}
		return wrap("the source did not answer in time or could not be reached", r.ErrUnavailable)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(a.max)+1))
	if err != nil {
		if e := parent.Err(); e != nil {
			return e
		}
		return wrap("the answer did not arrive whole in time", r.ErrUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		return wrap(refusal(resp, b, a.max, issue))
	}
	if len(b) > a.max {
		return wrap("the answer is over max_response_bytes", r.ErrIntegrity)
	}
	if !isJSON(resp.Header.Get("Content-Type")) {
		return wrap("the answer is not application/json", r.ErrIntegrity)
	}
	if r.DecodeStrict(bytes.NewReader(b), out) != nil {
		return wrap("the answer is not one strict JSON object of the protocol's shape", r.ErrIntegrity)
	}
	return nil
}

func isJSON(contentType string) bool {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || mt != "application/json" {
		return false
	}
	cs, ok := params["charset"]
	return !ok || strings.EqualFold(cs, "utf-8")
}

// refusal maps an answer other than 200 (docs/SOURCE_HTTP_PROTOCOL.md, "Errors"): a well-formed error body whose code is the protocol's and
// is sent with its own status is that code's error. Otherwise a redirect (never followed), 401, 407, 408, 429, a 5xx, or a 4xx without a
// well-formed error body is a source that cannot serve now (unavailable); a well-formed body that names an unknown code, a code with
// another status, or a code the route may not answer is a source speaking outside the protocol (integrity), and so is any other 2xx.
func refusal(resp *http.Response, b []byte, max int, issue bool) (string, error) {
	status := resp.StatusCode
	if status >= 200 && status < 300 {
		return "the source answered a success other than 200", r.ErrIntegrity
	}
	if status >= 300 && status < 400 {
		return "the source answered a redirect, which is never followed", r.ErrUnavailable
	}
	var body protocol.ErrorBody
	formed := len(b) <= max && isJSON(resp.Header.Get("Content-Type")) && r.DecodeStrict(bytes.NewReader(b), &body) == nil && body.Code != ""
	if formed {
		if c, ok := protocol.Lookup(body.Code); ok && c.Status == status && (issue || !c.IssueOnly) {
			return "the source refused with " + c.Name, c.Err
		}
	}
	switch {
	case status == 401 || status == 407 || status == 408 || status == 429 || status >= 500:
		return fmt.Sprintf("the source answered status %d", status), r.ErrUnavailable
	case formed:
		return fmt.Sprintf("the source answered status %d with a code the protocol does not give it", status), r.ErrIntegrity
	}
	return fmt.Sprintf("the source answered status %d without a protocol error", status), r.ErrUnavailable
}

// DescribeCatalog asks the source for the catalog of the context and verifies every dataset.
func (a *Adapter) DescribeCatalog(ctx context.Context, sc r.SourceContext) ([]r.Dataset, error) {
	if !validContext(sc) {
		return nil, fail("catalog", "the source context is incomplete", r.ErrDenied)
	}
	var out protocol.CatalogResponse
	wire := protocol.ContextOf(sc)
	if err := a.call(ctx, "catalog", protocol.PathCatalog, false, protocol.CatalogRequest{Context: &wire}, &out); err != nil {
		return nil, err
	}
	if out.Datasets == nil || len(out.Datasets) > protocol.MaxDatasets {
		return nil, fail("catalog", "the catalog is missing or over 100 datasets", r.ErrIntegrity)
	}
	seen := map[r.Ref]bool{}
	for _, d := range out.Datasets {
		if seen[d.Ref] || !validDataset(sc, d) {
			return nil, fail("catalog", "a dataset of the catalog does not verify", r.ErrIntegrity)
		}
		seen[d.Ref] = true
	}
	a.remember(sc, out.Datasets)
	return out.Datasets, nil
}

func validContext(sc r.SourceContext) bool { return protocol.ContextOf(sc).Valid() }

func validRef(v r.Ref) bool {
	return protocol.Printable(v.ID, 128) && protocol.Printable(v.Version, 128)
}

// validDataset holds a dataset to what the platform can run: the shape ValidateQuery accepts for a query of exactly its fields (columns are
// the dimensions then the metrics, typed, labelled, unique, 1..64), bounded texts.
func validDataset(sc r.SourceContext, d r.Dataset) bool {
	if !validRef(d.Ref) || d.Columns == nil || d.Metrics == nil || d.Dimensions == nil || (d.CurrencyPolicy != nil && !validRef(*d.CurrencyPolicy)) {
		return false
	}
	for _, m := range d.Metrics {
		if !validRef(m) {
			return false
		}
	}
	for _, c := range d.Columns {
		if len(c.Label) > protocol.MaxLabelBytes || len(c.Currency) > protocol.MaxCurrencyOrUnit || len(c.Unit) > protocol.MaxCurrencyOrUnit || !protocol.Printable(c.ID, 128) {
			return false
		}
	}
	q := r.QuerySpec{SchemaVersion: "1.0", Integration: sc.Owner.Integration, Dataset: d.Ref, Companies: []string{"c"}, Metrics: d.Metrics, Dimensions: d.Dimensions, Filters: []r.Filter{}, Sort: []r.Sort{}, AsOf: "2000-01-01", Timezone: d.Timezone, CurrencyPolicy: d.CurrencyPolicy, Page: r.PageSpec{Limit: 1}}
	return r.ValidateQuery(q, d) == nil
}

// remember keeps the verified datasets, which ReadPage holds each page to. A dataset version is immutable, so the newest answer is kept.
func (a *Adapter) remember(sc r.SourceContext, ds []r.Dataset) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.catalog)+len(ds) > maxCatalogCache {
		clear(a.catalog)
	}
	for _, d := range ds {
		a.catalog[catalogKey{sc.Owner.Integration, sc.SourceTenant, d.Ref}] = d
	}
}

// dataset is the verified dataset a read was made from: from the adapter's memory, or the context's catalog. A dataset the catalog of
// the context no longer has is a refusal: the principal no longer sees it.
func (a *Adapter) dataset(ctx context.Context, sc r.SourceContext, ref r.Ref) (r.Dataset, error) {
	key := catalogKey{sc.Owner.Integration, sc.SourceTenant, ref}
	a.mu.Lock()
	d, ok := a.catalog[key]
	a.mu.Unlock()
	if ok {
		return d, nil
	}
	ds, err := a.DescribeCatalog(ctx, sc)
	if err != nil {
		return r.Dataset{}, err
	}
	for _, d := range ds {
		if d.Ref == ref {
			return d, nil
		}
	}
	return r.Dataset{}, fail("pages", "the read's dataset is not in the context's catalog", r.ErrDenied)
}

// PrepareRead asks the source to prepare a stable read of the query and verifies the read against it.
func (a *Adapter) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, hash string) (r.PreparedRead, error) {
	if !validContext(sc) {
		return r.PreparedRead{}, fail("reads", "the source context is incomplete", r.ErrDenied)
	}
	var out protocol.PrepareResponse
	wire := protocol.ContextOf(sc)
	if err := a.call(ctx, "reads", protocol.PathReads, false, protocol.PrepareRequest{Context: &wire, Query: &q, QueryHash: hash}, &out); err != nil {
		return r.PreparedRead{}, err
	}
	if out.Read == nil {
		return r.PreparedRead{}, fail("reads", "the answer has no read", r.ErrIntegrity)
	}
	p, ok := out.Read.Prepared(sc)
	if !ok || !validRead(p) {
		return r.PreparedRead{}, fail("reads", "the read is not of the protocol's form", r.ErrIntegrity)
	}
	if p.QueryHash != hash || p.Dataset != q.Dataset || !slices.Equal(p.Companies, q.Companies) || p.Timezone != q.Timezone || !reflect.DeepEqual(p.CurrencyPolicy, q.CurrencyPolicy) {
		return r.PreparedRead{}, fail("reads", "the read is not of the query that was asked", r.ErrIntegrity)
	}
	now := a.now()
	if !now.Before(p.ExpiresAt) {
		return r.PreparedRead{}, fail("reads", "the read had expired when it arrived", r.ErrDenied)
	}
	if p.ExpiresAt.After(now.Add(protocol.MaxReadLifetime)) {
		return r.PreparedRead{}, fail("reads", "the read expires more than 15 minutes ahead", r.ErrIntegrity)
	}
	return p, nil
}

// validRead: the read's own fields are of the protocol's form.
func validRead(p r.PreparedRead) bool {
	return protocol.ValidRef(p.Ref) && protocol.Printable(p.ScopeHandle, protocol.MaxScopeHandle) && protocol.Printable(p.Watermark, protocol.MaxWatermark) && len(p.QueryHash) == 64 && p.Companies != nil
}

// sendable: the read is one this adapter can send back for the context: of the context, of the protocol's form.
func sendable(op string, sc r.SourceContext, p r.PreparedRead) error {
	if !validContext(sc) || p.Context != sc {
		return fail(op, "the read is not of the source context", r.ErrDenied)
	}
	if !validRead(p) {
		return fail(op, "the read is not of the protocol's form", r.ErrIntegrity)
	}
	return nil
}

// ReadPage asks for one page of the read and verifies it: the read's reference, hash and watermark, at most limit rows, every row of the
// dataset's columns, the totals of its metrics, and a cursor that is bounded and moves.
func (a *Adapter) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	if err := sendable("pages", sc, p); err != nil {
		return r.SourcePage{}, err
	}
	if limit < 1 || limit > protocol.MaxPageRows || len(cursor) > protocol.MaxCursor || (cursor != "" && !protocol.Printable(cursor, protocol.MaxCursor)) {
		return r.SourcePage{}, fail("pages", "the limit or the cursor is out of the protocol's bounds", r.ErrInvalid)
	}
	d, err := a.dataset(ctx, sc, p.Dataset)
	if err != nil {
		return r.SourcePage{}, err
	}
	var out protocol.PageResponse
	wire, read := protocol.ContextOf(sc), protocol.ReadOf(p)
	if err := a.call(ctx, "pages", protocol.Path(protocol.PathPages, p.Ref), false, protocol.PageRequest{Context: &wire, Read: &read, Cursor: cursor, Limit: limit}, &out); err != nil {
		return r.SourcePage{}, err
	}
	if out.Ref != p.Ref || out.QueryHash != p.QueryHash || out.Watermark != p.Watermark {
		return r.SourcePage{}, fail("pages", "the page is not of the read that was asked", r.ErrIntegrity)
	}
	if out.Rows == nil || out.Totals == nil || out.NextCursor == nil || len(out.Rows) > limit {
		return r.SourcePage{}, fail("pages", "the page is incomplete or has more rows than asked", r.ErrIntegrity)
	}
	next := *out.NextCursor
	if next != "" && (!protocol.Printable(next, protocol.MaxCursor) || next == cursor || len(out.Rows) == 0) {
		return r.SourcePage{}, fail("pages", "the next cursor is out of bounds, does not move or follows an empty page", r.ErrIntegrity)
	}
	if r.ValidateSourcePage(out.Rows, out.Totals, d) != nil {
		return r.SourcePage{}, fail("pages", "a row or a total does not fit the dataset's columns", r.ErrIntegrity)
	}
	return r.SourcePage{Ref: out.Ref, QueryHash: out.QueryHash, Watermark: out.Watermark, Rows: out.Rows, Totals: out.Totals, NextCursor: next}, nil
}

// AuthorizeCurrent asks whether the principal's current rights still cover the whole frozen read; only `{"authorized": true}` is a yes.
func (a *Adapter) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, p r.PreparedRead) error {
	if err := sendable("authorize", sc, p); err != nil {
		return err
	}
	var out protocol.AuthorizeResponse
	wire, read := protocol.ContextOf(sc), protocol.ReadOf(p)
	if err := a.call(ctx, "authorize", protocol.Path(protocol.PathAuthorize, p.Ref), false, protocol.AuthorizeRequest{Context: &wire, Read: &read}, &out); err != nil {
		return err
	}
	if out.Authorized == nil || !*out.Authorized {
		return fail("authorize", "the answer is not an authorization", r.ErrIntegrity)
	}
	return nil
}

// listCompanies asks the source which companies the context may choose now (ADR 0015) and verifies the list with the platform's own
// rule (reporting.ValidateCompanyList): at most 1000 entries, each id and name 1..128 bytes of text without control characters, no id
// twice. A list that does not verify, or an answer without a list or without partial, is an integrity failure: nothing of it is passed
// on. Only the adapters of a connection that declares companies expose it (Directory and its issuing forms).
func (a *Adapter) listCompanies(ctx context.Context, sc r.SourceContext) (r.CompanyList, error) {
	if !validContext(sc) {
		return r.CompanyList{}, fail("companies", "the source context is incomplete", r.ErrDenied)
	}
	var out protocol.CompaniesResponse
	wire := protocol.ContextOf(sc)
	if err := a.call(ctx, "companies", protocol.PathCompanies, false, protocol.CompaniesRequest{Context: &wire}, &out); err != nil {
		return r.CompanyList{}, err
	}
	if out.Companies == nil || out.Partial == nil {
		return r.CompanyList{}, fail("companies", "the answer has no list or does not say whether it is whole", r.ErrIntegrity)
	}
	list := r.CompanyList{Companies: out.Companies, Partial: *out.Partial}
	if r.ValidateCompanyList(list) != nil {
		return r.CompanyList{}, fail("companies", "the list is over 1000 entries, an id repeats, or an id or a name is not 1..128 characters of text", r.ErrIntegrity)
	}
	return list, nil
}

// Directory is the adapter of a connection that declares companies (ADR 0015, S19-B) without issuance: a company directory.
type Directory struct{ *Adapter }

// ListCompanies is the company directory of ADR 0015 over the protocol.
func (d *Directory) ListCompanies(ctx context.Context, sc r.SourceContext) (r.CompanyList, error) {
	return d.listCompanies(ctx, sc)
}

// AuthorityDirectory is the adapter of a connection with issuance rows that declares companies: a document authority of contract
// version 1 and a company directory.
type AuthorityDirectory struct{ *Authority }

// ListCompanies is the company directory of ADR 0015 over the protocol.
func (d *AuthorityDirectory) ListCompanies(ctx context.Context, sc r.SourceContext) (r.CompanyList, error) {
	return d.listCompanies(ctx, sc)
}

// GroupAuthorityDirectory is the adapter of a connection with issuance groups that declares companies: a document authority of
// contract version 2 and a company directory.
type GroupAuthorityDirectory struct{ *GroupAuthority }

// ListCompanies is the company directory of ADR 0015 over the protocol.
func (d *GroupAuthorityDirectory) ListCompanies(ctx context.Context, sc r.SourceContext) (r.CompanyList, error) {
	return d.listCompanies(ctx, sc)
}

// Authority is the adapter of a source that is the document authority (ADR 0003), contract version 1.
type Authority struct {
	*Adapter
	groups bool
}

// GroupAuthority is the adapter of a source that speaks contract version 2 (S03-K): a row group per document.
type GroupAuthority struct{ Authority }

// IssueContractVersion declares contract version 2.
func (GroupAuthority) IssueContractVersion() int { return r.IssueContractGroups }

func (a *Authority) issue(ctx context.Context, op, pattern, jobID string, in, out any) error {
	if !protocol.ValidJobID(jobID) {
		return fail(op, "the job id is not the platform's", r.ErrInvalid)
	}
	return a.call(ctx, op, protocol.Path(pattern, jobID), true, in, out)
}

// PrepareDocument asks the source to authorize the document and reserve its number, and verifies the reservation.
func (a *Authority) PrepareDocument(ctx context.Context, sc r.SourceContext, in r.IssueIntent) (r.IssueReservation, error) {
	if err := sendable("prepare", sc, in.Prepared); err != nil {
		return r.IssueReservation{}, err
	}
	if in.Group != nil && !a.groups {
		return r.IssueReservation{}, fail("prepare", "the connection's issuance is rows: a group is not sent", r.ErrUnsupported)
	}
	if in.Group != nil && !r.ValidGroupShape(*in.Group) {
		return r.IssueReservation{}, fail("prepare", "the group is not well formed", r.ErrInvalid)
	}
	var out protocol.PrepareIssueResponse
	wire, intent := protocol.ContextOf(sc), protocol.IntentOf(in)
	if err := a.issue(ctx, "prepare", protocol.PathPrepare, in.JobID, protocol.PrepareIssueRequest{Context: &wire, Intent: &intent}, &out); err != nil {
		return r.IssueReservation{}, err
	}
	res := out.Reservation
	if res == nil || res.JobID != in.JobID || !r.ValidProductRef(res.Ref) || !r.ValidIssueNumber(res.Number) {
		return r.IssueReservation{}, fail("prepare", "the reservation is not of the job or not of the protocol's form", r.ErrIntegrity)
	}
	issued, ok1 := protocol.ParseTime(res.IssuedAt)
	expires, ok2 := protocol.ParseTime(res.ExpiresAt)
	if !ok1 || !ok2 || !r.ValidProductTime(issued) || expires.Year() > 9999 || !expires.After(issued) {
		return r.IssueReservation{}, fail("prepare", "the reservation's issue time is not a whole second or its expiry is not after it", r.ErrIntegrity)
	}
	return r.IssueReservation{Ref: res.Ref, JobID: res.JobID, Number: res.Number, IssuedAt: issued, ExpiresAt: expires}, nil
}

// ConfirmIssue tells the source which file carries the reserved number, and verifies the receipt.
func (a *Authority) ConfirmIssue(ctx context.Context, sc r.SourceContext, c r.IssueConfirmation) (r.IssueReceipt, error) {
	if !validContext(sc) {
		return r.IssueReceipt{}, fail("confirm", "the source context is incomplete", r.ErrDenied)
	}
	var out protocol.ConfirmResponse
	wire := protocol.ContextOf(sc)
	conf := protocol.Confirmation{JobID: c.JobID, ReservationRef: c.ReservationRef, SHA256: c.SHA256, Pages: c.Pages}
	if err := a.issue(ctx, "confirm", protocol.PathConfirm, c.JobID, protocol.ConfirmRequest{Context: &wire, Confirmation: &conf}, &out); err != nil {
		return r.IssueReceipt{}, err
	}
	rec := out.Receipt
	if rec == nil || !r.ValidProductRef(rec.Ref) {
		return r.IssueReceipt{}, fail("confirm", "the receipt is not of the protocol's form", r.ErrIntegrity)
	}
	at, ok := protocol.ParseTime(rec.ConfirmedAt)
	if !ok || !r.ValidProductTime(at) {
		return r.IssueReceipt{}, fail("confirm", "the receipt's time is not a whole second", r.ErrIntegrity)
	}
	return r.IssueReceipt{Ref: rec.Ref, ConfirmedAt: at}, nil
}

// VoidIssue tells the source to void whatever it holds for the job; only `{"voided": true}` is done.
func (a *Authority) VoidIssue(ctx context.Context, sc r.SourceContext, jobID, reason string) error {
	if !validContext(sc) {
		return fail("void", "the source context is incomplete", r.ErrDenied)
	}
	reason = cutReason(reason)
	var out protocol.VoidResponse
	wire := protocol.ContextOf(sc)
	if err := a.issue(ctx, "void", protocol.PathVoid, jobID, protocol.VoidRequest{Context: &wire, JobID: jobID, Reason: reason}, &out); err != nil {
		return err
	}
	if out.Voided == nil || !*out.Voided {
		return fail("void", "the answer is not a void", r.ErrIntegrity)
	}
	return nil
}

// cutReason makes a void's reason one the protocol carries: valid UTF-8 (U+FFFD for what is not, as JSON would) of at most
// protocol.MaxReasonBytes, cut at a rune boundary. The platform's reasons are its error codes; the source keeps what it can.
func cutReason(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= protocol.MaxReasonBytes {
		return s
	}
	n := protocol.MaxReasonBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

var (
	_ r.Adapter            = (*Adapter)(nil)
	_ r.DocumentAuthority  = (*Authority)(nil)
	_ r.VersionedAuthority = (*GroupAuthority)(nil)
	_ r.CompanyDirectory   = (*Directory)(nil)
	_ r.DocumentAuthority  = (*AuthorityDirectory)(nil)
	_ r.CompanyDirectory   = (*AuthorityDirectory)(nil)
	_ r.VersionedAuthority = (*GroupAuthorityDirectory)(nil)
	_ r.CompanyDirectory   = (*GroupAuthorityDirectory)(nil)
)
