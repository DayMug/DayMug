package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
)

// errRollbackTx lets a withTx callback end the transaction without committing
// and without failing — the dry-run paths compute everything a real run would,
// then discard it.
var errRollbackTx = errors.New("roll back transaction")

// withTx runs fn inside one deferred SQLite transaction and commits when fn
// returns nil. Any other return rolls the transaction back and is passed
// through unchanged; errRollbackTx rolls back and reports success.
//
// fn must talk to the database only through tx. Reaching for s.db inside fn
// checks out a second pooled connection while this one holds the transaction:
// that connection reads the pre-transaction snapshot, its writes queue behind
// the lock this transaction holds until busy_timeout expires, and with the pool
// capped at maxOpenConns enough concurrent callers doing it starve the pool
// outright. Do any s.db lookups before calling withTx and pass the results in.
//
// The transaction is IMMEDIATE (see dsnWithPragmas): it holds the write lock
// from BEGIN, so reads inside fn see the latest commit and cannot be
// invalidated by a concurrent writer. withTxRetry remains for callers that
// must also survive a lock wait outlasting busy_timeout.
func (s *SQLiteStore) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		if errors.Is(err, errRollbackTx) {
			return tx.Rollback()
		}
		return err
	}
	return tx.Commit()
}

// withTxRetry is withTx re-run from scratch while it fails with lock
// contention, for transactions that read before they write. Each attempt gets
// a fresh transaction and so a fresh snapshot; fn must therefore be safe to
// run more than once and should only publish results through variables it
// overwrites on every attempt.
func (s *SQLiteStore) withTxRetry(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return retryOnBusySnapshot(ctx, func() error {
		return s.withTx(ctx, fn)
	})
}

// withForeignKeysOffTx is withTx on a connection whose foreign-key enforcement
// is switched off for the duration. The PRAGMA must be issued outside the
// transaction (inside one SQLite ignores it) and on the very connection the
// transaction runs on, so the connection is pinned rather than taken from the
// pool twice. Enforcement is restored before the connection goes back; if
// that fails the connection is discarded so no later caller inherits it. The
// transaction itself has already committed by then, so that is not an error.
func (s *SQLiteStore) withForeignKeysOffTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
