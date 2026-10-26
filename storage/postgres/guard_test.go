package postgres_test

import (
	"context"
	"errors"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"testing"
)

func TestFixtureRefusesOtherDatabaseAndRemoteHost(t *testing.T) {
	for _, dsn := range []string{"postgresql://postgres@127.0.0.1:55432/production?sslmode=disable", "postgresql://postgres@192.0.2.1:55432/reporting_test_s01?sslmode=disable", "postgresql://postgres@localhost:55432/reporting_test_s01?sslmode=disable"} {
		store, e := postgres.Open(context.Background(), dsn, true)
		if store != nil {
			store.Close()
		}
		if !errors.Is(e, r.ErrDenied) {
			t.Fatal("unprotected fixture connection accepted", e)
		}
	}
}
