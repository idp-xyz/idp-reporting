package pgsource_test

import (
	"context"
	"errors"
	"idp.local/reporting/adapters/contractsim/pgsource"
	r "idp.local/reporting/reporting"
	"testing"
)

func TestDurableSourceRejectsPlatformDBAndRemoteFallback(t *testing.T) {
	for _, dsn := range []string{"postgresql://postgres@127.0.0.1/reporting_test_s01?sslmode=disable", "postgresql://postgres@192.0.2.1/reporting_source_test_s01?sslmode=disable", "host=127.0.0.1,192.0.2.1 dbname=reporting_source_test_s01 user=postgres sslmode=disable"} {
		s, e := pgsource.Open(context.Background(), dsn, true)
		if s != nil {
			s.Close()
		}
		if !errors.Is(e, r.ErrDenied) {
			t.Fatal("unprotected source fixture accepted", e)
		}
	}
}
