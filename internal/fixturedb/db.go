// Package fixturedb contains only guarded test-database plumbing.
package fixturedb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
	"net"
	"slices"
	"time"
)

func Open(ctx context.Context, dsn string, allowed ...string) (*pgxpool.Pool, error) {
	c, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, r.ErrInvalid
	}
	loopback := func(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }
	if !slices.Contains(allowed, c.ConnConfig.Database) || !loopback(c.ConnConfig.Host) {
		return nil, r.ErrDenied
	}
	for _, f := range c.ConnConfig.Fallbacks {
		if !loopback(f.Host) {
			return nil, r.ErrDenied
		}
	}
	c.MaxConns = 8
	c.MinConns = 0
	c.ConnConfig.ConnectTimeout = 3 * time.Second
	return pgxpool.NewWithConfig(ctx, c)
}

// Decode checks the exact PostgreSQL canonical JSONB text before decoding it.
// SQL writes use sha256(convert_to(value::jsonb::text,'UTF8')). The digest is
// corruption detection; it is not a defense against a privileged DB writer.
func Decode(raw []byte, digest string, out any) error {
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return r.ErrIntegrity
	}
	if json.Unmarshal(raw, out) != nil {
		return r.ErrIntegrity
	}
	return nil
}
func RollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
}
