package postgres

import (
	"context"
	"errors"
	r "idp.local/reporting/reporting"
	"testing"
)

// The pool of the production opener (S04-G): pool_max_conns is read, bounded by the workers plus 2 below and by 100 above; an absent one is the
// default the opener always used.
func TestPoolSizeIsReadFromTheDSNAndBounded(t *testing.T) {
	const base = "postgresql://u@127.0.0.1:1/db?sslmode=disable"
	for _, c := range []struct {
		dsn     string
		parsed  int32
		workers int
		want    int32
		ok      bool
	}{
		{base, 4, 0, 8, true},                        // not named: the default, whatever the library parsed
		{base, 4, 10, 12, true},                      // not named, and the workers need more than the default
		{base + "&pool_max_conns=3", 3, 0, 3, true},  // one worker plus 2
		{base + "&pool_max_conns=2", 2, 0, 0, false}, // below the workers plus 2
		{base + "&pool_max_conns=5", 5, 4, 0, false}, // four workers need 6
		{base + "&pool_max_conns=6", 6, 4, 6, true},  // ... and 6 is enough
		{base + "&pool_max_conns=100", 100, 0, 100, true},
		{base + "&pool_max_conns=101", 101, 0, 0, false},
		{base + "&pool_max_conns=0", 0, 0, 0, false},
		{"host=127.0.0.1 dbname=db pool_max_conns=1", 1, 0, 0, false}, // the keyword form
		{"host=127.0.0.1 dbname=db pool_max_conns=20", 20, 0, 20, true},
		{"host=127.0.0.1 dbname=db", 4, 0, 8, true},
		{base, 4, -1, 0, false},
		{base, 4, 99, 0, false},
	} {
		got, err := poolSize(c.dsn, c.parsed, c.workers)
		if c.ok != (err == nil) || got != c.want || (err != nil && !errors.Is(err, r.ErrInvalid)) {
			t.Errorf("%s workers=%d: %d %v", c.dsn, c.workers, got, err)
		}
	}
}

// OpenProduction refuses a pool size out of bounds before it connects to anything: the DSN points at a port nothing listens on.
func TestOpenProductionRefusesAPoolOutOfBounds(t *testing.T) {
	for _, dsn := range []string{"postgresql://u@127.0.0.1:1/db?sslmode=disable&pool_max_conns=2", "postgresql://u@127.0.0.1:1/db?sslmode=disable&pool_max_conns=101"} {
		if _, err := OpenProduction(context.Background(), ProductionConfig{DSN: dsn, InsecureTestOnly: true}); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%s: %v", dsn, err)
		}
	}
}
