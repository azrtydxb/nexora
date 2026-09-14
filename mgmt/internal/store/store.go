// Package store owns the PostgreSQL pool, schema migrations, transactions and error mapping.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/piwi3910/nexora/mgmt/migrations"
)

// Sentinel errors every caller maps to API responses.
var (
	ErrNotFound    = errors.New("not found")
	ErrConflict    = errors.New("conflict")
	ErrUnavailable = errors.New("database unavailable")
)

const txAttempts = 3

// Store is the management plane's database handle.
type Store struct {
	Pool *pgxpool.Pool
}

// Open creates the connection pool. It does not require the database to be reachable yet.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 16
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, MapError(err)
	}
	return &Store{Pool: pool}, nil
}

// InstallationID returns the id the installation's first migration generated.
func (s *Store) InstallationID(ctx context.Context) (string, error) {
	var id string
	if err := s.Pool.QueryRow(ctx, "select id::text from installation").Scan(&id); err != nil {
		return "", MapError(err)
	}
	return id, nil
}

// Close closes the pool.
func (s *Store) Close() { s.Pool.Close() }

// Migrate applies every pending migration. Concurrent instances serialise on an advisory lock.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return MapError(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "select pg_advisory_lock(hashtext('nexora:migrate'))"); err != nil {
		return MapError(err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock(hashtext('nexora:migrate'))")
	}()
	db := stdlib.OpenDBFromPool(s.Pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", MapError(err))
	}
	return nil
}

// InTx runs fn in a READ COMMITTED transaction and commits it. Serialization failures and
// deadlocks are retried up to three attempts. Every error is passed through MapError.
func (s *Store) InTx(ctx context.Context, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 0; attempt < txAttempts; attempt++ {
		err = s.inTxOnce(ctx, fn)
		if !retryable(err) {
			break
		}
	}
	return MapError(err)
}

func (s *Store) inTxOnce(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return tx.Commit(ctx)
}

func retryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

// MapError converts driver errors into ErrNotFound, ErrConflict or ErrUnavailable (wrapping the
// original message); other errors are returned unchanged.
func MapError(err error) error {
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrUnavailable) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505":
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.Message)
		case strings.HasPrefix(pgErr.Code, "08"), pgErr.Code == "57P01", pgErr.Code == "57P02", pgErr.Code == "57P03":
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return err
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	if errors.As(err, &connectErr) || errors.As(err, &netErr) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || pgconn.SafeToRetry(err) || strings.Contains(err.Error(), "closed pool") {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return err
}
