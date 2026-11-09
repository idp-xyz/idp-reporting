package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	r "idp.local/reporting/reporting"
)

// The database context of a call (S04-C, docs/S04C_SCOPE.md, docs/OPERATIONS.md "Row-level security"). Migration 007 enables row-level
// security on jobs and assets, and its policies read three settings that say whose rows a transaction works on (reporting.tenant,
// reporting.issuer, reporting.subject) and one that says the transaction is a worker's or a sweep's (reporting.scope = 'worker'). A
// transaction that said nothing sees no row and may write none.
//
// The context is transaction-local and nothing else: every call of the store runs in one transaction whose first statement sets it with
// set_config(name, $n, true), the values being parameters, so that it ends with the transaction and a pooled connection carries none into the
// next call. The package never uses set_config(..., false), SET or SET SESSION (a source test holds it to that).
const (
	setIdentitySQL = `SELECT set_config('reporting.tenant', $1, true), set_config('reporting.issuer', $2, true), set_config('reporting.subject', $3, true)`
	setWorkerSQL   = `SELECT set_config('reporting.scope', $1, true)`
	workerScope    = "worker"
)

// beginAs starts a transaction that works on the rows of one identity.
func beginAs(ctx context.Context, pool *pgxpool.Pool, i r.Identity) (pgx.Tx, error) {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, setIdentitySQL, i.Tenant, i.Issuer, i.Subject); e != nil {
		rollback(ctx, tx)
		return nil, e
	}
	return tx, nil
}

// beginWorker starts a transaction of the worker scope: the workers' claim, publication and failure, and the retention sweep, which work on
// the rows of every identity.
func beginWorker(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, setWorkerSQL, workerScope); e != nil {
		rollback(ctx, tx)
		return nil, e
	}
	return tx, nil
}

// beginWorkerReadOnly is beginWorker for a transaction that can write nothing: the database refuses any statement that would (the
// verification of the object store after a restore, S04-F, reads and never writes).
func beginWorkerReadOnly(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, e := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, setWorkerSQL, workerScope); e != nil {
		rollback(ctx, tx)
		return nil, e
	}
	return tx, nil
}

// asIdentity runs fn in a transaction of the identity's context and commits it (a read commits too: there is nothing to undo).
func asIdentity(ctx context.Context, pool *pgxpool.Pool, i r.Identity, fn func(pgx.Tx) error) error {
	tx, e := beginAs(ctx, pool, i)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// asWorker is asIdentity for the worker scope.
func asWorker(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, e := beginWorker(ctx, pool)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// setAuditTenantSQL is the one other setting of the context (S12-B, migration 012): with it the transaction may read every audit event of its
// tenant and issuer (the tenant_audit policy). Transaction-local, like the identity's.
const setAuditTenantSQL = `SELECT set_config('reporting.audit', $1, true)`

// setAuditTenant says, in a transaction begun as an identity, that it reads its tenant's audit: the server does so only for an administrator.
func setAuditTenant(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, setAuditTenantSQL, "tenant")
	return err
}
