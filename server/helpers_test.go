package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
)

// The values below are the known secrets and identifiers of the tests. Every test that runs a session asserts that none of them appears in a log
// line, an error or a response it must not be in.
const (
	testIssuer   = "https://issuer.example.test"
	testAudience = "reporting-api"
	testScope    = "reporting.read"
	testTenant   = "Tenant-Zq7-private"
	testSubject  = "subject-Zq7-private"
	testClient   = "client-Zq7"
	testDSNPass  = "dsn-PASSWORD-Zq7-never-shown"
	testAccess   = "OBJ-ACCESS-Zq7-never-shown"
	testSecret   = "OBJ-SECRET-Zq7-never-shown"
	testDSN      = "postgresql://appuser:" + testDSNPass + "@127.0.0.1:1/reporting_prod?sslmode=verify-full"
	cursorKeyHex = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee11"
	cursorKey2   = "badc0de0badc0de0badc0de0badc0de0badc0de0badc0de0badc0de0badc0d22"
)

var (
	keysOnce sync.Once
	keyPool  []*rsa.PrivateKey
)

// issuerKey is a test issuer's RS256 key; the keys are generated once for the whole test run.
type issuerKey struct {
	kid string
	key *rsa.PrivateKey
}

func issuerKeys(t testing.TB) []issuerKey {
	t.Helper()
	keysOnce.Do(func() {
		for i := 0; i < 3; i++ {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			keyPool = append(keyPool, k)
		}
	})
	out := make([]issuerKey, len(keyPool))
	for i, k := range keyPool {
		out[i] = issuerKey{kid: "kid-" + string(rune('a'+i)), key: k}
	}
	return out
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func jwksOf(keys ...issuerKey) []byte {
	var list []map[string]string
	for _, k := range keys {
		list = append(list, map[string]string{"kty": "RSA", "kid": k.kid, "use": "sig", "alg": "RS256", "n": b64(k.key.N.Bytes()), "e": "AQAB"})
	}
	b, _ := json.Marshal(map[string]any{"keys": list})
	return b
}

// claims is a token's payload; zero values take the valid test ones.
type claims struct {
	Sub, Client, Aud, Scope, Iss string
	Lifetime                     time.Duration
	Expired                      bool
	KID                          string
}

// mint signs an RS256 access token the profile accepts, with the changes the test asks for.
func mint(t testing.TB, k issuerKey, c claims) string {
	t.Helper()
	pick := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	now := time.Now().Unix()
	exp, iat := now+300, now-5
	if c.Expired {
		exp, iat = now-10, now-300
	}
	payload, _ := json.Marshal(map[string]any{"iss": pick(c.Iss, testIssuer), "sub": pick(c.Sub, testSubject), "aud": pick(c.Aud, testAudience), "exp": exp, "iat": iat, "jti": "jti-" + hex.EncodeToString(randBytes(6)), "client_id": pick(c.Client, testClient), "scope": pick(c.Scope, testScope)})
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "at+jwt", "kid": pick(c.KID, k.kid)})
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func randBytes(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func writeFile(t testing.TB, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// replaceFile rewrites a file the way an operator's deployment does: a new file renamed over the old one.
func replaceFile(t testing.TB, path string, data []byte, mode os.FileMode) {
	t.Helper()
	tmp := path + ".new"
	writeFile(t, tmp, data, mode)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// selfSigned writes a certificate for 127.0.0.1 and its key (0600) and returns the certificate's pool.
func selfSigned(t testing.TB, dir string) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "reporting-test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(priv)
	certFile, keyFile = filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	writeFile(t, certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	writeFile(t, keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	leaf, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(leaf)
	return
}

// world is the files of one configuration.
type world struct {
	dir         string
	cfg         Config
	cert        *x509.CertPool
	jwks, binds string
	keys        []issuerKey
	sim         *contractsim.Simulator
	cursorFile  string
	cursorFile2 string
	finance     string
	tenant      string
	env         map[string]string // further environment variables the server reads (S16-A: the webhook's secret)
}

type bindingJSON struct {
	Subject      string   `json:"subject"`
	ClientID     string   `json:"client_id"`
	Tenant       string   `json:"tenant"`
	Integrations []string `json:"integrations"`
}

func bindingsJSON(list ...bindingJSON) []byte { b, _ := json.Marshal(list); return b }

// newWorld writes every file of a valid TLS configuration into a temporary directory (trusting key 0 of the issuer) and returns it.
func newWorld(t testing.TB) *world {
	t.Helper()
	dir := t.TempDir()
	keys := issuerKeys(t)
	w := &world{dir: dir, keys: keys, finance: contractsim.FinanceIntegration, tenant: testTenant}
	certFile, keyFile, pool := selfSigned(t, dir)
	w.cert = pool
	w.jwks, w.binds = filepath.Join(dir, "jwks.json"), filepath.Join(dir, "bindings.json")
	writeFile(t, w.jwks, jwksOf(keys[0]), 0o644)
	writeFile(t, w.binds, bindingsJSON(bindingJSON{testSubject, testClient, testTenant, []string{w.finance}}), 0o644)
	w.cursorFile = filepath.Join(dir, "cursor1.key")
	writeFile(t, w.cursorFile, []byte(cursorKeyHex+"\n"), 0o600)
	w.cursorFile2 = filepath.Join(dir, "cursor2.key")
	writeFile(t, w.cursorFile2, []byte(cursorKey2+"\n"), 0o400)
	w.sim = contractsim.Financial()
	w.sim.Grant(r.Owner{Identity: r.Identity{Tenant: testTenant, Issuer: testIssuer, Subject: testSubject}, Integration: w.finance}, contractsim.DefaultGrant())
	w.cfg = Config{
		Listen: "127.0.0.1:0", TLSCertFile: certFile, TLSKeyFile: keyFile,
		AllowSimulator: true, Workers: 1, ShutdownGrace: 30,
		Issuer:         IssuerConfig{Issuer: testIssuer, Audience: testAudience, RequiredScope: testScope, MaxTokenLifetime: 900, JWKSFile: w.jwks, BindingsFile: w.binds},
		CursorKeyFiles: []string{w.cursorFile},
		Connections:    []ConnectionConfig{{Tenant: testTenant, Integration: w.finance, SourceTenant: "tenant_a", Adapter: "sim", Options: json.RawMessage(`{}`)}},
		ProfileLabel:   "test-lab",
	}
	return w
}

func (w *world) registry() Registry {
	return Registry{"sim": func(json.RawMessage, func(string) string) (r.Adapter, error) { return w.sim, nil }}
}

func (w *world) getenv(name string) string {
	if v, ok := w.env[name]; ok {
		return v
	}
	switch name {
	case StoreDSNEnv:
		return testDSN
	case "REPORTING_OBJECT_STORE_ACCESS_KEY":
		return testAccess
	case "REPORTING_OBJECT_STORE_SECRET_KEY":
		return testSecret
	}
	return ""
}

// logBuffer is the writer a server logs to.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *logBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }

// lines parses every line as a JSON object and fails the test on a line that is not one.
func (l *logBuffer) lines(t testing.TB) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.String()), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line is not a JSON object: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func (l *logBuffer) events(t testing.TB, event string) []map[string]any {
	var out []map[string]any
	for _, m := range l.lines(t) {
		if m["event"] == event {
			out = append(out, m)
		}
	}
	return out
}

// started is a running server with its client.
type started struct {
	*Server
	log    *logBuffer
	client *http.Client
	base   string
	cancel context.CancelFunc
	w      *world
}

// startWorld starts a server on the world's configuration with a memory store and the simulator registry.
func startWorld(t *testing.T, w *world, opts ...Option) *started {
	t.Helper()
	store := NewMemoryStoreForTests(time.Now)
	return startWith(t, w, append([]Option{WithStoreForTests(store), WithPollIntervalForTests(5 * time.Millisecond)}, opts...)...)
}

func startWith(t *testing.T, w *world, opts ...Option) *started {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lb := &logBuffer{}
	s, err := Start(ctx, w.cfg, w.registry(), w.getenv, lb, opts...)
	if err != nil {
		cancel()
		t.Fatalf("start: %v\n%s", err, lb.String())
	}
	st := &started{Server: s, log: lb, cancel: cancel, w: w}
	scheme := "https"
	tr := &http.Transport{}
	if s.TLS() {
		tr.TLSClientConfig = &tls.Config{RootCAs: w.cert, MinVersion: tls.VersionTLS12}
	} else {
		scheme = "http"
	}
	st.client = &http.Client{Transport: tr, Timeout: 20 * time.Second}
	st.base = scheme + "://" + s.Addr()
	t.Cleanup(func() {
		cancel()
		<-s.Done()
		tr.CloseIdleConnections()
	})
	return st
}

func (s *started) do(t testing.TB, method, path, token string, body any, headers ...string) (int, http.Header, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// goroutines lists the goroutines now, by id, with their stacks.
func goroutines() map[string]string {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	out := map[string]string{}
	for _, block := range strings.Split(string(buf), "\n\n") {
		head, _, _ := strings.Cut(block, "\n")
		out[head] = block
	}
	return out
}

func goroutineID(head string) string {
	f := strings.Fields(head)
	if len(f) >= 2 {
		return f[1]
	}
	return head
}

// noNewGoroutines fails the test if a goroutine that was not there at snapshot is still running something of this repository or of
// net/http when the check ends. It waits for them to finish by yielding, with a deadline: a goroutine that is leaked never finishes, one that is
// still unwinding does.
func noNewGoroutines(t testing.TB, snapshot map[string]string) {
	t.Helper()
	before := map[string]bool{}
	for head := range snapshot {
		before[goroutineID(head)] = true
	}
	deadline := time.Now().Add(10 * time.Second)
	var leaked []string
	for {
		leaked = leaked[:0]
		for head, block := range goroutines() {
			if before[goroutineID(head)] {
				continue
			}
			if strings.Contains(block, "idp.local/reporting/") && !strings.Contains(block, "testing.tRunner") || strings.Contains(block, "net/http.") || strings.Contains(block, "pgxpool") || strings.Contains(block, "puddle") {
				leaked = append(leaked, block)
			}
		}
		if len(leaked) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines outlive the server:\n%s", strings.Join(leaked, "\n\n"))
		}
		runtime.Gosched()
	}
}

func newTLSTransport(w *world) *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: w.cert, MinVersion: tls.VersionTLS12}}
}

// syncWriter is a writer several goroutines may use.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
