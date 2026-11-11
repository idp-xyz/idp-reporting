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
// document authority, rows for a version-1 one, none otherwise), and so does the company directory (declared when the wrapped adapter
// is one, S19-B); the server and the adapter's connections end with the test.
//
// The data class follows the wrapped adapter too (S08-A, ADR 0012 decision 6). The protocol carries no data class, and the platform's
// adapter of a product says none (a product behind the protocol is business data); but this is a test's own composition, which knows
// what it put behind the protocol: when the wrapped adapter says a class (a contract simulator's contract-simulator), the adapter
// returned says the same word and keeps every other capability it has, so that a simulator read through the protocol is not labelled
// business data. A wrapped adapter that says none is returned as the platform's adapter is.
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
	_, companies := a.(r.CompanyDirectory)
	c := httpsource.Config{BaseURL: srv.URL, RootCAs: pool, Token: token, Issuance: issuance, Companies: companies}
	for _, o := range options {
		o(&c)
	}
	out, err := httpsource.New(c)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(out.(interface{ Close() }).Close)
	classed, ok := a.(r.ClassedAdapter)
	if !ok {
		return out
	}
	// One case for each type httpsource.New returns: the returned value has that type's methods, and so its capabilities, and the word.
	word := saying(classed.DataClass())
	switch v := out.(type) {
	case *httpsource.Adapter:
		return struct {
			*httpsource.Adapter
			saying
		}{v, word}
	case *httpsource.Directory:
		return struct {
			*httpsource.Directory
			saying
		}{v, word}
	case *httpsource.Authority:
		return struct {
			*httpsource.Authority
			saying
		}{v, word}
	case *httpsource.AuthorityDirectory:
		return struct {
			*httpsource.AuthorityDirectory
			saying
		}{v, word}
	case *httpsource.GroupAuthority:
		return struct {
			*httpsource.GroupAuthority
			saying
		}{v, word}
	case *httpsource.GroupAuthorityDirectory:
		return struct {
			*httpsource.GroupAuthorityDirectory
			saying
		}{v, word}
	}
	tb.Fatalf("sourcetest: the platform's adapter is a %T, which Remote does not know how to give a data class", out)
	return nil
}

// saying is a data class said by the adapter it is embedded beside (reporting.ClassedAdapter).
type saying string

func (s saying) DataClass() string { return string(s) }
