package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
)

// S04-H H3: no connection string, and no password in it, reaches an error. The pool library redacts the password of a string it can read
// (`xxxxx`) and names a string it cannot read as it is, so a password in the wrong place was echoed; the opener now gives a fixed text for any
// string it cannot parse. The other failures (a refused connection, an unresolvable host) are the library's own and are checked as well.
func TestOpenProductionRefusalsCarryNoPassword(t *testing.T) {
	const password = "S04H-pw-0123456789-NEVER-SHOWN"
	for name, dsn := range map[string]string{
		"not a connection string":           "::" + password,
		"a password as the whole string":    password,
		"keyword form with a bad sslmode":   "host=127.0.0.1 port=1 user=app password=" + password + " dbname=db sslmode=bogus",
		"url with a bad sslmode":            "postgresql://app:" + password + "@127.0.0.1:1/db?sslmode=bogus",
		"url with a bad port":               "postgresql://app:" + password + "@127.0.0.1:notaport/db",
		"url with a bad escape":             "postgresql://app:" + password + "%zz@127.0.0.1/db",
		"url with a broken host":            "postgresql://app:" + password + "@[::1/db",
		"url with a bad pool size":          "postgresql://app:" + password + "@127.0.0.1:1/db?pool_max_conns=abc&sslmode=disable",
		"url with a pool size out of bound": "postgresql://app:" + password + "@127.0.0.1:1/db?pool_max_conns=1&sslmode=disable",
		"connection refused":                "postgresql://app:" + password + "@127.0.0.1:1/db?sslmode=disable",
		"keyword form, connection refused":  "host=127.0.0.1 port=1 user=app password=" + password + " dbname=db sslmode=disable",
		"a host that does not resolve":      "postgresql://app:" + password + "@db.invalid/reporting?sslmode=disable",
		"a fixture database":                "postgresql://app:" + password + "@db.invalid/reporting_test_s01?sslmode=verify-full",
		"no TLS":                            "postgresql://app:" + password + "@db.invalid/reporting?sslmode=disable",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: dsn, InsecureTestOnly: !strings.Contains(name, "fixture") && !strings.Contains(name, "TLS")})
		cancel()
		if err == nil {
			t.Errorf("%s: opened", name)
			continue
		}
		if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "password=") || strings.Contains(err.Error(), "postgresql://") {
			t.Errorf("%s: the refusal carries the connection string or the password: %q", name, err)
		}
	}
	// The string that cannot be parsed is ErrInvalid with the fixed text, not the library's.
	_, err := postgres.OpenProduction(context.Background(), postgres.ProductionConfig{DSN: "::" + password})
	if !errors.Is(err, r.ErrInvalid) || !strings.Contains(err.Error(), "its text is not shown") {
		t.Errorf("an unparseable string: %v", err)
	}
	if _, err := postgres.ParseDSN("::" + password); !errors.Is(err, r.ErrInvalid) || strings.Contains(err.Error(), password) {
		t.Errorf("ParseDSN: %v", err)
	}
	if c, err := postgres.ParseDSN("postgresql://app:" + password + "@127.0.0.1:1/db?sslmode=disable"); err != nil || c.ConnConfig.Password != password {
		t.Errorf("ParseDSN does not parse a good string: %v", err)
	}
}
