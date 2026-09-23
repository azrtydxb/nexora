package store_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

type lostRollbackConn struct {
	net.Conn
	drop    atomic.Bool
	dropped chan struct{}
	once    sync.Once
}

func (c *lostRollbackConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if c.drop.Load() && n > 0 {
			c.once.Do(func() { close(c.dropped) })
		}
		if !c.drop.Load() || err != nil {
			return n, err
		}
	}
}
func (c *lostRollbackConn) Write(p []byte) (int, error) {
	// Lose CancelRequest too, exposing pgx's delayed asynchronous retirement.
	if len(p) >= 8 && binary.BigEndian.Uint32(p[4:8]) == 80877102 {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

type lostRollbackTrace struct {
	armed   atomic.Bool
	entered chan struct{}
}

func (l *lostRollbackTrace) TraceQueryStart(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if q.SQL == "rollback" && l.armed.CompareAndSwap(true, false) {
		c.PgConn().Conn().(*lostRollbackConn).drop.Store(true)
		close(l.entered)
	}
	return ctx
}
func (*lostRollbackTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestInTxRollbackResponseLoss(t *testing.T) {
	base := storetest.New(t)
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback-error", true: "canceled-callback"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			trace := &lostRollbackTrace{entered: make(chan struct{})}
			dropped := make(chan struct{})
			cfg := base.Pool.Config()
			cfg.MaxConns, cfg.MinConns = 1, 0
			cfg.ConnConfig.TLSConfig, cfg.ConnConfig.Fallbacks = nil, nil
			cfg.ConnConfig.Tracer = trace
			dial := cfg.ConnConfig.DialFunc
			cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
				c, err := dial(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &lostRollbackConn{Conn: c, dropped: dropped}, nil
			}
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			st := &store.Store{Pool: pool}
			if _, err := pool.Exec(ctx, "create table if not exists rollback_probe (id integer primary key)"); err != nil {
				t.Fatal(err)
			}
			callbackCtx, stop := context.WithCancel(ctx)
			defer stop()
			original := errors.New("original callback error")
			if canceled {
				original = context.Canceled
			}
			attempts := 0
			var oldPID uint32
			result := st.InTx(callbackCtx, func(tx pgx.Tx) error {
				attempts++
				oldPID = tx.Conn().PgConn().PID()
				if _, err := tx.Exec(callbackCtx, "insert into rollback_probe values (1)"); err != nil {
					return err
				}
				trace.armed.Store(true)
				if canceled {
					stop()
				}
				return original
			})
			if result != original || attempts != 1 {
				t.Fatalf("result=%v attempts=%d; original error and single attempt required", result, attempts)
			}
			select {
			case <-trace.entered:
			default:
				t.Fatal("rollback loss was not exercised")
			}
			select {
			case <-dropped:
			default:
				t.Fatal("actual rollback response was not dropped")
			}
			// No stall release: a new connection must occupy the only slot while the
			// old driver's cancellation exchange is still unacknowledged.
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatalf("sole pool slot not recovered: %v", err)
			}
			defer conn.Release()
			if conn.Conn().PgConn().PID() == oldPID {
				t.Fatal("uncertain connection reused")
			}
			var count int
			if err := conn.QueryRow(ctx, "select count(*) from rollback_probe").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("failed transaction persisted")
			}
			conn.Release()
			if err := st.InTx(ctx, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, "insert into rollback_probe values (2)"); return err }); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, "select count(*) from rollback_probe where id=2").Scan(&count); err != nil || count != 1 {
				t.Fatalf("successful transaction did not commit: count=%d err=%v", count, err)
			}
			if _, err := pool.Exec(ctx, "delete from rollback_probe"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// closeObservedConn retains net.Conn's concurrent Close/IO contract. The TLS
// wrapper above it is real; net.Pipe makes close_notify deterministically block
// when the peer stops reading (without relying on TCP buffer sizes).
type closeObservedConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *closeObservedConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func cleanupTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"cleanup.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{RootCAs: roots, ServerName: "cleanup.test"}, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
}

func TestTLSGracefulCloseBlocks(t *testing.T) {
	clientCfg, serverCfg := cleanupTLSConfigs(t)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	client, server := tls.Client(a, clientCfg), tls.Server(b, serverCfg)
	handshake := make(chan error, 1)
	go func() { handshake <- server.Handshake() }()
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := <-handshake; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Close() }()
	select {
	case err := <-done:
		t.Fatalf("TLS Close did not block: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	// Closing the transport also safely interrupts a concurrent TLS Close.
	if err := client.NetConn().Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transport close did not interrupt TLS close")
	}
}

func TestInTxTLSCleanupBoundaries(t *testing.T) {
	for _, scenario := range []string{"begin-loss", "commit-loss", "panic-rollback-loss", "begin-rejected", "callback-error", "commit-rejected", "success", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			clientTLS, serverTLS := cleanupTLSConfigs(t)
			stop := make(chan struct{})
			var workers sync.WaitGroup
			var mu sync.Mutex
			var sockets []net.Conn
			var first *closeObservedConn
			var dials atomic.Int32
			lost := make(chan struct{}, 1)
			cancelSeen := make(chan struct{}, 1)
			cfg, err := pgxpool.ParseConfig("postgres://test:test@localhost/test?sslmode=disable")
			if err != nil {
				t.Fatal(err)
			}
			cfg.MaxConns, cfg.MinConns = 1, 0
			cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
				a, b := net.Pipe()
				raw := &closeObservedConn{Conn: a, closed: make(chan struct{})}
				n := dials.Add(1)
				mu.Lock()
				sockets = append(sockets, a, b)
				if n == 1 {
					first = raw
				}
				mu.Unlock()
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer b.Close()
					server := tls.Server(b, serverTLS)
					backend := pgproto3.NewBackend(server, server)
					startup, err := backend.ReceiveStartupMessage()
					if err != nil {
						return
					}
					if _, ok := startup.(*pgproto3.CancelRequest); ok {
						select {
						case cancelSeen <- struct{}{}:
						default:
						}
						<-stop // Never acknowledge the separate cancellation exchange.
						return
					}
					backend.Send(&pgproto3.AuthenticationOk{})
					backend.Send(&pgproto3.BackendKeyData{ProcessID: uint32(n), SecretKey: []byte{0, 0, 0, 1}})
					backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
					if backend.Flush() != nil {
						return
					}
					rejected := false
					for {
						msg, err := backend.Receive()
						if err != nil {
							return
						}
						q, ok := msg.(*pgproto3.Query)
						if !ok {
							return
						}
						command := strings.Fields(q.String)[0]
						if n == 1 && ((scenario == "begin-loss" && command == "begin") || (scenario == "commit-loss" && command == "commit") || (scenario == "panic-rollback-loss" && command == "rollback")) {
							lost <- struct{}{}
							<-stop // No reads: TLS Close would block writing close_notify.
							return
						}
						status := byte('I')
						if command == "begin" {
							status = 'T'
						}
						if n == 1 && !rejected && ((scenario == "begin-rejected" && command == "begin") || (scenario == "commit-rejected" && command == "commit")) {
							rejected = true
							backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42501", Message: "original rejection"})
							status = 'I'
						} else {
							backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(strings.ToUpper(command))})
						}
						backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
						if backend.Flush() != nil {
							return
						}
					}
				}()
				// Verified TLS, supplied through DialFunc to make the transport a
				// deterministic pipe. No insecure verification setting is needed.
				return tls.Client(raw, clientTLS), nil
			}
			pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			var old *pgx.Conn
			defer func() {
				close(stop)
				mu.Lock()
				for _, c := range sockets {
					_ = c.Close()
				}
				mu.Unlock()
				pool.Close()
				workers.Wait()
				if old != nil && old.IsClosed() {
					select {
					case <-old.PgConn().CleanupDone():
					case <-time.After(time.Second):
						t.Error("driver cleanup did not finish after blackhole teardown")
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			old = conn.Conn()
			conn.Release()
			callCtx, endCall := context.WithTimeout(ctx, 150*time.Millisecond)
			defer endCall()
			original := errors.New("original callback/panic")
			attempts := 0
			var result error
			var recovered any
			start := time.Now()
			func() {
				defer func() { recovered = recover() }()
				result = (&store.Store{Pool: pool}).InTx(callCtx, func(pgx.Tx) error {
					attempts++
					if strings.HasPrefix(scenario, "panic") {
						panic(original)
					}
					if scenario == "callback-error" {
						return original
					}
					return nil
				})
			}()
			if time.Since(start) > 2*time.Second {
				t.Fatal("cleanup exceeded two-second bound")
			}
			wantAttempts := 1
			if strings.HasPrefix(scenario, "begin-") {
				wantAttempts = 0
			}
			if attempts != wantAttempts {
				t.Fatalf("callback attempts=%d want=%d", attempts, wantAttempts)
			}
			if strings.HasPrefix(scenario, "panic") {
				if recovered != original {
					t.Fatalf("panic changed: %v", recovered)
				}
			} else if scenario == "callback-error" {
				if result != original {
					t.Fatalf("callback error changed: %v", result)
				}
			} else if strings.HasSuffix(scenario, "loss") {
				if !errors.Is(result, context.DeadlineExceeded) {
					t.Fatalf("deadline error changed: %v", result)
				}
			} else if strings.HasSuffix(scenario, "rejected") {
				var pgErr *pgconn.PgError
				if !errors.As(result, &pgErr) || pgErr.Code != "42501" || pgErr.Message != "original rejection" {
					t.Fatalf("server error changed: %v", result)
				}
			} else if result != nil {
				t.Fatal(result)
			}
			uncertain := strings.HasSuffix(scenario, "loss")
			if uncertain {
				select {
				case <-lost:
				default:
					t.Fatal("loss boundary not reached")
				}
				select {
				case <-first.closed:
				default:
					t.Fatal("underlying TLS transport not retired")
				}
				select {
				case <-cancelSeen:
				case <-ctx.Done():
					t.Fatal("cancel blackhole not exercised")
				}
			}
			// InTx's deferred Release already ran after Hijack. Reacquire before
			// ending the blackhole to prove idempotency and immediate capacity.
			next, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatalf("sole slot not recovered: %v", err)
			}
			defer next.Release()
			if (next.Conn() == old) == uncertain {
				t.Fatalf("reuse=%v uncertain=%v", next.Conn() == old, uncertain)
			}
			next.Release()
			if err := (&store.Store{Pool: pool}).InTx(ctx, func(pgx.Tx) error { return nil }); err != nil {
				t.Fatalf("subsequent transaction failed: %v", err)
			}
		})
	}
}
