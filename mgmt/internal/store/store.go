// Package store owns the PostgreSQL pool, schema migrations, transactions and error mapping.
package store

import (
	"context"
	"crypto/tls"
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
	// A pooled session that sits idle holds up a PostgreSQL failover: CloudNativePG shuts the old primary
	// down smartly first, waiting for client sessions to end. pgx's defaults keep an idle connection for
	// 30 minutes and check every minute; these close it within 45 seconds, and recycle a long-lived one
	// well inside a maintenance window. Reconnecting costs one TLS handshake on the next query.
	cfg.MaxConnIdleTime = 30 * time.Second
	cfg.HealthCheckPeriod = 15 * time.Second
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnLifetimeJitter = 5 * time.Minute
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
	if err := checkM8MigrationHistory(ctx, conn); err != nil {
		return err
	}

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

// InTxOnce runs one READ COMMITTED transaction with the same bounded cleanup as
// InTx, but never retries. The caller must bound ctx and use it for callback IO.
// Success means COMMIT was acknowledged; every error is passed through MapError.
func (s *Store) InTxOnce(ctx context.Context, fn func(pgx.Tx) error) error {
	return MapError(s.inTxOnce(ctx, fn))
}

func (s *Store) inTxOnce(ctx context.Context, fn func(pgx.Tx) error) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var tx pgx.Tx
	defer func() {
		// Cancellation must not skip rollback, but a lost response must not hold
		// shutdown (or the pool's only slot) indefinitely. Match the one-second
		// cleanup budget used by control's secret enqueue transactions.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		var rollbackErr error
		if tx != nil {
			rollbackErr = tx.Rollback(cleanupCtx)
		}
		if (rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed)) || conn.Conn().IsClosed() || conn.Conn().PgConn().IsBusy() || conn.Conn().PgConn().TxStatus() != 'I' {
			// pgx's asynchronous failure cleanup can wait on a separate cancel
			// connection. Hijack releases pool capacity before that cleanup ends.
			// Retire the socket immediately; never reuse an uncertain transaction.
			broken := conn.Hijack()
			transport := broken.PgConn().Conn()
			// tls.Conn.Close writes close_notify, with its own five-second
			// deadline. Only unwrap the known standard-library wrapper; do not
			// invoke arbitrary wrapper methods or manipulate TLS internals.
			for {
				tlsConn, ok := transport.(*tls.Conn)
				if !ok {
					break
				}
				transport = tlsConn.NetConn()
			}
			_ = transport.Close()
			// asyncClose marks pgx closed synchronously before launching its
			// worker. Close then returns without touching its frontend/watcher.
			// Otherwise we still own the idle driver and close it after the
			// transport, so neither PostgreSQL nor TLS writes can block.
			closeCtx, stop := context.WithCancel(context.Background())
			stop()
			_ = broken.Close(closeCtx)
		}
	}()
	tx, err = conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
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
