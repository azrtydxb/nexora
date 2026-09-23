package control

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// Drop the real PostgreSQL COMMIT response after reading it from the socket.
// The server committed, but pgx cannot positively acknowledge that outcome.
type dropCommitResponse struct {
	net.Conn
	armed   *atomic.Bool
	dropped *atomic.Bool
}

func (c *dropCommitResponse) Read(p []byte) (int, error) {
	if !c.armed.Load() {
		return c.Conn.Read(p)
	}
	// Withhold all bytes once armed; tolerate a fragmented command-complete tag.
	var response []byte
	for {
		n, err := c.Conn.Read(p)
		response = append(response, p[:n]...)
		if bytes.Contains(response, []byte("COMMIT\x00")) {
			c.dropped.Store(true)
			return 0, errors.New("injected lost commit response")
		}
		if err != nil {
			return 0, err
		}
		if len(response) > 4096 {
			return 0, errors.New("unexpected commit response")
		}
	}
}

type countedBarrierTrace struct {
	*barrierTrace
	commits atomic.Int32
}

func (t *countedBarrierTrace) TraceQueryStart(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if q.SQL == "commit" {
		t.commits.Add(1)
	}
	return t.barrierTrace.TraceQueryStart(ctx, c, q)
}

func TestDisplacedTSIGRecovery(t *testing.T) {
	base := storetest.New(t)
	for _, producer := range []string{"hosted", "rpz"} {
		for _, fault := range []string{"backend-loss", "lost-ack"} {
			for _, empty := range []bool{false, true} {
				t.Run(producer+"/"+fault+"/empty="+boolName(empty), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					sub := newSubscriber(uuid.NewString(), 0)
					sub.certificateSerial = strings.ReplaceAll(sub.engineID, "-", "")
					if _, err := base.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values($1,'displaced',$2,$3)`, sub.id, sub.certificateSerial, sub.sessionID); err != nil {
						t.Fatal(err)
					}
					if _, err := base.Pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values($1,$2,now(),now()+interval '1 hour')`, sub.certificateSerial, sub.id); err != nil {
						t.Fatal(err)
					}
					offer := func(h *Hub, digest string) {
						if producer == "hosted" {
							m := &controlv1.KeyMaterial{}
							if digest != "" {
								m.TsigKeys = []*controlv1.TsigSecret{{Secret: []byte(digest)}}
							}
							h.offerKeyMaterial(ctx, sub, m, digest)
						} else {
							m := &controlv1.RpzTsigKeys{}
							if digest != "" {
								m.Keys = []*controlv1.RpzTsigKey{{Secret: []byte(digest)}}
							}
							h.offerKeys(ctx, sub, m, digest)
						}
					}
					healthy := NewHub(base, "healthy")
					a := "A"
					if empty {
						a = ""
						offer(healthy, "prior")
					}
					offer(healthy, a)
					trace := &countedBarrierTrace{barrierTrace: &barrierTrace{phase: "prepared", reached: make(chan uint32, 1), resume: make(chan struct{})}}
					var once sync.Once
					resume := func() { once.Do(func() { close(trace.resume) }) }
					var armed, dropped atomic.Bool
					cfg := base.Pool.Config()
					cfg.MaxConns, cfg.MinConns = 1, 0
					cfg.ConnConfig.Tracer = trace
					cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
						c, err := (&net.Dialer{}).DialContext(ctx, network, address)
						if err != nil {
							return nil, err
						}
						return &dropCommitResponse{Conn: c, armed: &armed, dropped: &dropped}, nil
					}
					pool, err := pgxpool.NewWithConfig(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer pool.Close()
					defer resume()
					done := make(chan struct{})
					go func() { defer close(done); offer(NewHub(&store.Store{Pool: pool}, "fault"), "B") }()
					var pid uint32
					select {
					case pid = <-trace.reached:
					case <-ctx.Done():
						t.Fatal("producer never reached commit")
					}
					transport := &barrierRecordingStream{}
					delivered := make(chan struct{})
					go func() {
						defer close(delivered)
						if producer == "hosted" {
							sendPreparedKeyMaterial(ctx, transport, <-sub.keyMaterial)
						} else {
							sendPreparedRpzTsigKeys(ctx, transport, <-sub.keys)
						}
					}()
					select {
					case <-delivered:
						t.Fatal("send escaped pending commit")
					case <-time.After(20 * time.Millisecond):
					}
					if transport.sent.Load() != 0 {
						t.Fatal("secret sent under authorization locks")
					}
					if fault == "backend-loss" {
						var killed bool
						if err := base.Pool.QueryRow(ctx, `select pg_terminate_backend($1)`, pid).Scan(&killed); err != nil || !killed {
							t.Fatalf("terminate failed: %v", err)
						}
					} else {
						armed.Store(true)
					}
					resume()
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal("faulted offer hung")
					}
					select {
					case <-delivered:
					case <-ctx.Done():
						t.Fatal("discard hung")
					}
					if trace.commits.Load() != 1 {
						t.Fatal("side-effect transaction was retried")
					}
					if fault == "lost-ack" && !dropped.Load() {
						t.Fatal("real commit response was not dropped")
					}
					if transport.sent.Load() != 0 {
						t.Fatal("faulted replacement sent")
					}
					offer(healthy, a)
					if producer == "hosted" {
						select {
						case d := <-sub.keyMaterial:
							sendPreparedKeyMaterial(ctx, transport, d)
						default:
							t.Fatal("A suppressed after failed B")
						}
					} else {
						select {
						case d := <-sub.keys:
							sendPreparedRpzTsigKeys(ctx, transport, d)
						default:
							t.Fatal("A suppressed after failed B")
						}
					}
					if transport.sent.Load() != 1 {
						t.Fatal("recovery A did not deliver")
					}
					offer(healthy, a)
					if len(sub.keys)+len(sub.keyMaterial) != 0 {
						t.Fatal("successful recovery was not deduplicated")
					}
				})
			}
		}
	}
}
