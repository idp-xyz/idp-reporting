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
	"strings"
	"time"
)

func Open(ctx context.Context, dsn string, allowed ...string) (*pgxpool.Pool, error) {
	return open(ctx, dsn, func(name string) bool { return slices.Contains(allowed, name) })
}

// DrillPrefix starts the name of every scratch database of the restore drill (S04-F, scripts/restore_drill.sh): databases the drill creates
// and drops itself, never a fixture database and never the gate database on port 55432.
const DrillPrefix = "reporting_restore_s04f_"

// OpenDrill opens a scratch database of the restore drill: a loopback address, a database whose name starts with DrillPrefix (so none of the
// fixture databases, which are named exactly), and not port 55432.
func OpenDrill(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return open(ctx, dsn, func(name string) bool { return strings.HasPrefix(name, DrillPrefix) && len(name) > len(DrillPrefix) }, 55432)
}

func open(ctx context.Context, dsn string, allowed func(string) bool, refusedPorts ...uint16) (*pgxpool.Pool, error) {
	c, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, r.ErrInvalid
	}
	loopback := func(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }
	if !allowed(c.ConnConfig.Database) || !loopback(c.ConnConfig.Host) || slices.Contains(refusedPorts, c.ConnConfig.Port) {
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
