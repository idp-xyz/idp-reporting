package httpsource_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/sourcetest"
	r "idp.local/reporting/reporting"
)

// The known secret of these tests: no error text may contain it, nor the source's address.
const testToken = "SRC-TOKEN-Zq7-never-shown-0123456789"

// answer is what a hostile source does to the reference source's answer of one request: it may change the status, the content type and
// the body. Returning ok false answers unchanged.
type tamper func(path string, status int, body []byte) (int, string, []byte, bool)

// rig is a simulator behind the reference source behind an optional tamper, on a loopback TLS server, and the platform's adapter of it.
type rig struct {
	sim     *contractsim.Simulator
	srv     *httptest.Server
	adapter r.Adapter
	sc      r.SourceContext
	mu      sync.Mutex
	change  tamper
}

// set makes the source answer through change from now on (nil: the reference source's answers).
func (g *rig) set(change tamper) { g.mu.Lock(); defer g.mu.Unlock(); g.change = change }
func (g *rig) current() tamper   { g.mu.Lock(); defer g.mu.Unlock(); return g.change }

func newRig(t *testing.T, configure ...func(*httpsource.Config)) *rig {
	t.Helper()
	g := &rig{sim: contractsim.Financial()}
	p := contractsim.DemoPrincipal()
	owner := contractsim.DemoOwner(p, contractsim.FinanceIntegration)
	g.sim.Grant(owner, contractsim.DefaultGrant())
	g.sc = r.SourceContext{Owner: owner, SourceTenant: "tenant_a"}
	ref := sourcetest.New(g.sim, sourcetest.Options{Token: testToken})
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec := httptest.NewRecorder()
		ref.ServeHTTP(rec, req)
		status, ct, body := rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes()
		if change := g.current(); change != nil {
			if s, c, b, ok := change(req.URL.Path, status, bytes.Clone(body)); ok {
				status, ct, body = s, c, b
			}
		}
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(g.srv.Close)
	g.adapter = adapterOf(t, g.srv, configure...)
	return g
}

func poolOf(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

func adapterOf(t *testing.T, srv *httptest.Server, configure ...func(*httpsource.Config)) r.Adapter {
	t.Helper()
	c := httpsource.Config{BaseURL: srv.URL, RootCAs: poolOf(srv), Token: testToken, Issuance: httpsource.IssuanceGroups}
	for _, f := range configure {
		f(&c)
	}
	a, err := httpsource.New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.(interface{ Close() }).Close)
	return a
}

// pki is a CA, a server certificate for 127.0.0.1 and a client certificate, all written as PEM files (the keys 0600).
type pki struct {
	dir                           string
	caFile, certFile, keyFile     string
	clientCertFile, clientKeyFile string
	pool                          *x509.CertPool
	serverPair                    tls.Certificate
}

func newPKI(t *testing.T) pki {
	t.Helper()
	dir := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "source-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	issue := func(name string, serial int64, usage x509.ExtKeyUsage, ip bool) (certPEM, keyPEM []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		if ip {
			tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kd, _ := x509.MarshalECPrivateKey(k)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
	}
	p := pki{dir: dir, pool: x509.NewCertPool()}
	p.pool.AddCert(ca)
	sc, sk := issue("source", 2, x509.ExtKeyUsageServerAuth, true)
	cc, ck := issue("platform", 3, x509.ExtKeyUsageClientAuth, false)
	p.serverPair, err = tls.X509KeyPair(sc, sk)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte, mode os.FileMode) string {
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, b, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(f, mode); err != nil {
			t.Fatal(err)
		}
		return f
	}
	p.caFile = write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644)
	p.certFile, p.keyFile = write("server.pem", sc, 0o644), write("server.key", sk, 0o600)
	p.clientCertFile, p.clientKeyFile = write("client.pem", cc, 0o644), write("client.key", ck, 0o600)
	return p
}
