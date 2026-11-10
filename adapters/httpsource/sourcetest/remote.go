package sourcetest

import (
	"crypto/x509"
	"net/http/httptest"
	"testing"

	"idp.local/reporting/adapters/httpsource"
	r "idp.local/reporting/reporting"
)

// Remote puts the adapter behind the reference source on a loopback TLS server and returns the platform's adapter of that source: the
// adapter's behaviour as the platform sees it through the protocol. The issuance follows the wrapped adapter (groups for a version-2
// document authority, rows for a version-1 one, none otherwise); the server and the adapter's connections end with the test.
func Remote(tb testing.TB, a r.Adapter, options ...func(*httpsource.Config)) r.Adapter {
	tb.Helper()
	token := r.NewID() + r.NewID()
	srv := httptest.NewTLSServer(New(a, Options{Token: token}))
	tb.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	issuance := httpsource.IssuanceNone
	if _, ok := a.(r.DocumentAuthority); ok {
		issuance = httpsource.IssuanceRows
		if r.GroupIssueSupported(a) {
			issuance = httpsource.IssuanceGroups
		}
	}
	c := httpsource.Config{BaseURL: srv.URL, RootCAs: pool, Token: token, Issuance: issuance}
	for _, o := range options {
		o(&c)
	}
	out, err := httpsource.New(c)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(out.(interface{ Close() }).Close)
	return out
}
