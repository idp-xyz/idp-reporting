package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

// DesignStores are the private stores whose designs the database keeps (migration 011): the report layouts, the dashboards, the document
// templates, the page backgrounds and the images.
var DesignStores = []string{"layouts", "dashboards", "templates", "backgrounds", "images"}

// designTimeout bounds one read or one change of an owner's document: privatedocs' calls carry no context, and a change holds a row lock.
const designTimeout = 15 * time.Second

const (
	designLoadSQL   = `SELECT data FROM reporting_s01.designs WHERE store=$1 AND tenant=$2 AND issuer=$3 AND subject=$4`
	designEnsureSQL = `INSERT INTO reporting_s01.designs (store, tenant, issuer, subject, data) VALUES ($1,$2,$3,$4,'\x') ON CONFLICT DO NOTHING`
	designLockSQL   = designLoadSQL + ` FOR UPDATE`
	designStoreSQL  = `UPDATE reporting_s01.designs SET data=$5, updated_at=clock_timestamp() WHERE store=$1 AND tenant=$2 AND issuer=$3 AND subject=$4`
)

// Designs answers the backend of one private store in this database (S11-A): every server on the same database serves the same designs.
// Each read and each change is a transaction of the owner's identity, so row-level security keeps every owner to its own rows. The backend
// shares the store's pool; closing it closes nothing.
func (s *Store) Designs(store string) (privatedocs.Backend, error) {
	for _, known := range DesignStores {
		if store == known {
			return &designBackend{pool: s.pool, store: store}, nil
		}
	}
	return nil, r.ErrInvalid
}

type designBackend struct {
	pool  *pgxpool.Pool
	store string
}

func (b *designBackend) args(owner r.Identity, more ...any) []any {
	return append([]any{b.store, owner.Tenant, owner.Issuer, owner.Subject}, more...)
}

func (b *designBackend) Load(owner r.Identity) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), designTimeout)
	defer cancel()
	var data []byte
	e := asIdentity(ctx, b.pool, owner, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, designLoadSQL, b.args(owner)...).Scan(&data)
		if errors.Is(err, pgx.ErrNoRows) {
			data = nil
			return nil
		}
		return err
	})
	if e != nil {
		return nil, e
	}
	if len(data) == 0 {
		return nil, nil
	}
	return data, nil
}

// Begin inserts the owner's row empty if it has none and locks it: a second change of the same document waits for this one to end.
func (b *designBackend) Begin(owner r.Identity) (privatedocs.Txn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), designTimeout)
	tx, e := beginAs(ctx, b.pool, owner)
	if e != nil {
		cancel()
		return nil, e
	}
	t := &designTxn{b: b, owner: owner, ctx: ctx, cancel: cancel, tx: tx}
	if _, e = tx.Exec(ctx, designEnsureSQL, b.args(owner)...); e == nil {
		e = tx.QueryRow(ctx, designLockSQL, b.args(owner)...).Scan(&t.data)
	}
	if e != nil {
		t.Rollback()
		return nil, e
	}
	if len(t.data) == 0 {
		t.data = nil
	}
	return t, nil
}

func (b *designBackend) Close() error { return nil }

type designTxn struct {
	b      *designBackend
	owner  r.Identity
	ctx    context.Context
	cancel context.CancelFunc
	tx     pgx.Tx
	data   []byte
	done   bool
}

func (t *designTxn) Load() ([]byte, error) { return t.data, nil }

func (t *designTxn) Store(data []byte) error {
	if len(data) > privatedocs.MaxDocument {
		return r.ErrBudget
	}
	_, e := t.tx.Exec(t.ctx, designStoreSQL, t.b.args(t.owner, data)...)
	return e
}

func (t *designTxn) Commit() error {
	if t.done {
		return r.ErrConflict
	}
	t.done = true
	defer t.cancel()
	return t.tx.Commit(t.ctx)
}

func (t *designTxn) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	defer t.cancel()
	rollback(t.ctx, t.tx)
	return nil
}
