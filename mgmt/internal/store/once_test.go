package store_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Real PostgreSQL executes the commands; only selected wire responses are lost.
// CancelRequest uses a pipe to withhold EOF until its deadline/close, modelling
// the separate lost cancel response that must not retain the pool's sole slot.
type onceLostResponse struct {
	cancelObserved chan net.Conn
	net.Conn
	command                string
	armed                  *atomic.Bool
	drop                   atomic.Bool
	cancelWire             atomic.Bool
	cancelRead, cancelPeer net.Conn
	onCommand              func()
}

func (c *onceLostResponse) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if len(p) >= 12 && binary.BigEndian.Uint32(p[4:8]) == 80877102 {
		c.cancelWire.Store(true)
	}
	if err == nil && len(p) > 5 && p[0] == 'Q' && strings.EqualFold(strings.TrimRight(string(p[5:]), "\x00"), c.command) && c.armed.CompareAndSwap(true, false) {
		c.drop.Store(true)
		if c.onCommand != nil {
			c.onCommand()
		}
	}
	return n, err
}
func (c *onceLostResponse) Read(p []byte) (int, error) {
	if c.cancelWire.Load() {
		select {
		case c.cancelObserved <- c.cancelPeer:
		default:
		}
		return c.cancelRead.Read(p)
	}
	for {
		n, err := c.Conn.Read(p)
		if !c.drop.Load() || err != nil {
			return n, err
		}
	}
}
func (c *onceLostResponse) SetDeadline(t time.Time) error {
	_ = c.cancelRead.SetDeadline(t)
	return c.Conn.SetDeadline(t)
}
func (c *onceLostResponse) SetReadDeadline(t time.Time) error {
	_ = c.cancelRead.SetReadDeadline(t)
	return c.Conn.SetReadDeadline(t)
}
func (c *onceLostResponse) Close() error {
	_ = c.cancelRead.Close()
	_ = c.cancelPeer.Close()
	return c.Conn.Close()
}

func TestInTxOnceLostResponsesReleaseSingleSlot(t *testing.T) {
	pg := harness.New(t).StartPostgres()
	for _, mode := range []string{"canceled begin", "lost begin", "lost rollback", "canceled rollback", "lost commit"} {
		t.Run(mode, func(t *testing.T) {
			var cancel context.CancelFunc
			cancelObserved := make(chan net.Conn, 1)
			var armed atomic.Bool
			command := "begin isolation level read committed"
			if strings.Contains(mode, "rollback") {
				command = "rollback"
			}
			if mode == "lost commit" {
				command = "commit"
			}
			cfg, err := pgxpool.ParseConfig(pg.URL)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MaxConns = 1
			cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				a, b := net.Pipe()
				c := &onceLostResponse{Conn: conn, command: command, armed: &armed, cancelRead: a, cancelPeer: b, cancelObserved: cancelObserved}
				if mode == "canceled begin" {
					c.onCommand = func() { cancel() }
				}
				return c, nil
			}
			pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			warm, warmCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err = pool.Ping(warm); err != nil {
				warmCancel()
				t.Fatal(err)
			}
			warmCancel()
			ctx, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
			cancel = stop
			defer cancel()
			armed.Store(true)
			calls := 0
			callbackErr := errors.New("rollback once")
			before := time.Now()
			err = (&store.Store{Pool: pool}).InTxOnce(ctx, func(tx pgx.Tx) error {
				calls++
				if mode == "canceled rollback" {
					cancel()
				}
				if mode == "lost commit" {
					return nil
				}
				return callbackErr
			})
			if armed.Load() {
				t.Fatal("wire fault never triggered")
			}
			if err == nil || time.Since(before) > 2500*time.Millisecond {
				t.Fatalf("unbounded or successful fault: %v, %s", err, time.Since(before))
			}
			wantCalls := 0
			if strings.Contains(mode, "rollback") {
				wantCalls = 1
				if !errors.Is(err, callbackErr) {
					t.Fatal(err)
				}
			}
			if mode == "lost commit" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("callback calls=%d want %d", calls, wantCalls)
			}
			reuse, reuseCancel := context.WithTimeout(context.Background(), time.Second)
			defer reuseCancel()
			var one int
			if err = pool.QueryRow(reuse, "select 1").Scan(&one); err != nil || one != 1 {
				t.Fatalf("single slot unavailable: %d %v", one, err)
			}
			select {
			case peer := <-cancelObserved:
				_ = peer.Close()
			case <-time.After(time.Second):
				t.Fatal("lost CancelRequest response was not exercised")
			}
		})
	}
}

func TestInTxOnceDoesNotRetryAndMapsErrors(t *testing.T) {
	pg := harness.New(t).StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, code := range []string{"40001", "40P01", "23505"} {
		calls := 0
		err = st.InTxOnce(ctx, func(tx pgx.Tx) error {
			calls++
			_, err := tx.Exec(ctx, "do $$ begin raise exception 'once' using errcode = '"+code+"'; end $$")
			return err
		})
		if calls != 1 {
			t.Fatalf("%s retried %d times", code, calls)
		}
		if code == "23505" {
			if !errors.Is(err, store.ErrConflict) {
				t.Fatal(err)
			}
		} else {
			var p *pgconn.PgError
			if !errors.As(err, &p) || p.Code != code {
				t.Fatal(err)
			}
		}
	}
	if err = st.InTxOnce(ctx, func(pgx.Tx) error { return io.EOF }); !errors.Is(err, store.ErrUnavailable) {
		t.Fatal(err)
	}
}
