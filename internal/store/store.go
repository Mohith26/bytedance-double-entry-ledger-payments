// Package store is the Postgres persistence layer. It enforces the ledger's
// concurrency invariants inside row-locked (SELECT ... FOR UPDATE) database
// transactions, so that under N concurrent writers there are no lost updates,
// no double-spend, and no disallowed negative balances.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IsolationMode selects the concurrency-control strategy.
type IsolationMode string

const (
	// ModeRowLock uses READ COMMITTED + SELECT ... FOR UPDATE (default).
	ModeRowLock IsolationMode = "rowlock"
	// ModeSerializable uses SERIALIZABLE isolation (still takes FOR UPDATE
	// locks); serialization failures are retried transparently.
	ModeSerializable IsolationMode = "serializable"
)

// maxRetries bounds automatic retries on serialization/deadlock errors.
const maxRetries = 25

// Store wraps a pgx connection pool and the chosen isolation mode.
type Store struct {
	pool *pgxpool.Pool
	mode IsolationMode
}

// DefaultURL returns the DATABASE_URL env var or a native-Postgres default that
// matches the local dev database created for this project.
func DefaultURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://localhost:5432/ledgercore?sslmode=disable"
}

// Open connects to Postgres and returns a Store using the row-lock mode.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool, mode: ModeRowLock}, nil
}

// WithMode returns a shallow copy of the store using the given isolation mode.
func (s *Store) WithMode(m IsolationMode) *Store {
	return &Store{pool: s.pool, mode: m}
}

// Mode reports the store's isolation mode.
func (s *Store) Mode() IsolationMode { return s.mode }

// Pool exposes the underlying pool (used by read-only helpers and tests).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// txOptions returns the pgx transaction options for the current mode.
func (s *Store) txOptions() pgx.TxOptions {
	if s.mode == ModeSerializable {
		return pgx.TxOptions{IsoLevel: pgx.Serializable}
	}
	return pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
}

// runInTx runs fn inside a transaction, committing on success and rolling back
// on error. Serialization failures (40001) and deadlocks (40P01) are retried
// with a short backoff, up to maxRetries.
func (s *Store) runInTx(ctx context.Context, fn func(pgx.Tx) error) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		tx, err := s.pool.BeginTx(ctx, s.txOptions())
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}
		err = fn(tx)
		if err != nil {
			_ = tx.Rollback(ctx)
			if isRetryable(err) {
				lastErr = err
				time.Sleep(backoff(attempt))
				continue
			}
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			_ = tx.Rollback(ctx)
			if isRetryable(err) {
				lastErr = err
				time.Sleep(backoff(attempt))
				continue
			}
			return fmt.Errorf("commit: %w", err)
		}
		return nil
	}
	return fmt.Errorf("exhausted retries: %w", lastErr)
}

// backoff returns an increasing (but small) delay for retry attempt n.
func backoff(n int) time.Duration {
	d := time.Duration(n+1) * 200 * time.Microsecond
	if d > 5*time.Millisecond {
		d = 5 * time.Millisecond
	}
	return d
}

// isRetryable reports whether err is a transient serialization/deadlock error.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

// Migrate applies the schema in migrations/001_init.sql (idempotent CREATE IF
// NOT EXISTS statements). sqlText is the file contents.
func (s *Store) Migrate(ctx context.Context, sqlText string) error {
	_, err := s.pool.Exec(ctx, sqlText)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Reset truncates all data tables (test/bench helper). It restarts identities
// so runs are reproducible.
func (s *Store) Reset(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `TRUNCATE settlement_batches, idempotency_keys, postings, transactions, accounts RESTART IDENTITY CASCADE`)
	return err
}
