package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func TestCommitBarrierDelivery(t *testing.T) {
	for _, committed := range []bool{false, true} {
		b := &commitBarrier{done: make(chan struct{})}
		d := Delivery[string]{value: "private", barrier: b}
		result := make(chan bool, 1)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { v, ok := d.Await(ctx); result <- ok && v == "private" }()
		select {
		case <-result:
			t.Fatal("pending envelope delivered")
		case <-time.After(10 * time.Millisecond):
		}
		b.resolve(committed)
		if got := <-result; got != committed {
			t.Fatalf("delivery=%v commit=%v", got, committed)
		}
		b.resolve(!committed) // a later result cannot reverse discard/commit
		_, ok := d.Await(ctx)
		if ok != committed {
			t.Fatal("barrier changed its decision")
		}
		cancel()
	}
	b := &commitBarrier{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v, ok := (Delivery[string]{value: "private", barrier: b}).Await(ctx); ok || v != "" {
		t.Fatal("cancelled waiter exposed payload")
	}
}

type barrierTrace struct {
	phase   string
	reached chan uint32
	resume  chan struct{}
}
type barrierQueryKey struct{}

func (t *barrierTrace) TraceQueryStart(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if t.phase == "prepared" && q.SQL == "commit" {
		t.reached <- c.PgConn().PID()
		<-t.resume
	}
	return context.WithValue(ctx, barrierQueryKey{}, q.SQL)
}
func (t *barrierTrace) TraceQueryEnd(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(barrierQueryKey{}).(string)
	if t.phase == "authorized" && strings.Contains(sql, "for share") && q.Err == nil {
		t.reached <- c.PgConn().PID()
		<-t.resume
	}
}

// The paused producer resumes only after its backend dies and a separate pool
// commits takeover/revocation. Both pauses leave the real transport barrier closed.
func TestCommitBarrierBackendLoss(t *testing.T) {
	base := storetest.New(t)
	for _, phase := range []string{"authorized", "prepared"} {
		for _, revoke := range []bool{false, true} {
			for _, producer := range []string{"odoh", "tls", "rpz", "hosted"} {
				t.Run(phase+"/"+producer+"/revoke="+map[bool]string{false: "false", true: "true"}[revoke], func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					sub := newSubscriber(uuid.NewString(), 0)
					sub.certificateSerial = strings.ReplaceAll(sub.engineID, "-", "")
					if _, err := base.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values($1,'barrier',$2,$3)`, sub.id, sub.certificateSerial, sub.sessionID); err != nil {
						t.Fatal(err)
					}
					if _, err := base.Pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values($1,$2,now(),now()+interval '1 hour')`, sub.certificateSerial, sub.id); err != nil {
						t.Fatal(err)
					}
					trace := &barrierTrace{phase: phase, reached: make(chan uint32, 1), resume: make(chan struct{})}
					cfg := base.Pool.Config()
					cfg.MaxConns, cfg.MinConns = 1, 0
					cfg.ConnConfig.Tracer = trace
					pool, err := pgxpool.NewWithConfig(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer pool.Close()
					var resumeOnce sync.Once
					resume := func() { resumeOnce.Do(func() { close(trace.resume) }) }
					defer resume()
					st := &store.Store{Pool: pool}
					h := NewHub(st, "barrier")
					f := NewDNSTLSFanout(base)
					ch, err := f.Register(ctx, sub.engineID, sub.sessionID, sub.certificateSerial, "")
					if err != nil {
						t.Fatal(err)
					}
					f.st = st
					done := make(chan struct{})
					go func() {
						defer close(done)
						switch producer {
						case "odoh":
							h.offerOdohKeys(ctx, sub, &controlv1.OdohKeys{Keys: []*controlv1.OdohKey{{Seed: []byte("private")}}}, "fresh")
						case "tls":
							f.Set(&pki.DNSTLSMaterial{KeyPEM: []byte("private"), FingerprintSHA256: "fresh"})
						case "rpz":
							h.offerKeys(ctx, sub, &controlv1.RpzTsigKeys{Keys: []*controlv1.RpzTsigKey{{Secret: []byte("private")}}}, "fresh")
						case "hosted":
							h.offerKeyMaterial(ctx, sub, &controlv1.KeyMaterial{TsigKeys: []*controlv1.TsigSecret{{Secret: []byte("private")}}}, "fresh")
						}
					}()
					var pid uint32
					select {
					case pid = <-trace.reached:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					// Start a receiver while commit is still pending. It may dequeue, but cannot deliver.
					delivered := make(chan bool, 1)
					transport := &barrierRecordingStream{}
					go func() {
						switch producer {
						case "odoh":
							d := <-sub.control
							sendPreparedServerMessage(ctx, transport, d)
							delivered <- transport.sent.Load() != 0
						case "tls":
							d := <-ch
							sendPreparedTlsMaterial(ctx, transport, d)
							delivered <- transport.sent.Load() != 0
						case "rpz":
							d := <-sub.keys
							sendPreparedRpzTsigKeys(ctx, transport, d)
							delivered <- transport.sent.Load() != 0
						case "hosted":
							d := <-sub.keyMaterial
							sendPreparedKeyMaterial(ctx, transport, d)
							delivered <- transport.sent.Load() != 0
						}
					}()
					select {
					case <-delivered:
						t.Fatal("delivered before COMMIT")
					case <-time.After(10 * time.Millisecond):
					}
					var terminated bool
					if err := base.Pool.QueryRow(ctx, `select pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
						t.Fatalf("terminate: %v %v", terminated, err)
					}
					q := `update engines set connection_session=$2 where id=$1`
					if revoke {
						q = `update engines set connection_session=$2,revoked_at=now() where id=$1`
					}
					if _, err := base.Pool.Exec(ctx, q, sub.id, uuid.New()); err != nil {
						t.Fatal(err)
					}
					resume()
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					select {
					case ok := <-delivered:
						if ok {
							t.Fatal("lost transaction delivered secret after takeover")
						}
					case <-ctx.Done():
						t.Fatal("discard left waiter hanging")
					}
					if sub.odohKeysDigest != "" || sub.keysDigest != "" || sub.keyMaterialDigest != "" {
						t.Fatal("failed commit suppressed fresh retry")
					}
				})
			}
		}
	}
}

func TestDNSTLSRetryConnectedStream(t *testing.T) {
	base := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg := base.Pool.Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st := &store.Store{Pool: pool}
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	serial := big.NewInt(123456)
	if _, err := pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial) values($1,'tls-retry',$2)`, id, serial.Text(16)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values($1,$2,now(),now()+interval '1 hour')`, serial.Text(16), id); err != nil {
		t.Fatal(err)
	}
	h := NewHub(st, uuid.NewString())
	f := NewDNSTLSFanout(st)
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: id}, SerialNumber: serial}
	sc := peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}}})
	stream := &recordingOdohStream{controlledStream: &controlledStream{ctx: sc, in: make(chan *controlv1.EngineMessage, 1), reading: make(chan struct{}, 2), done: make(chan error, 1)}}
	stream.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id}}}
	server := NewServer(st, nil, h, h.instanceID, f)
	go func() { stream.done <- server.Connect(stream) }()
	waitReading(t, stream.controlledStream)
	waitReading(t, stream.controlledStream)
	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.Set(&pki.DNSTLSMaterial{KeyPEM: []byte("latest"), FingerprintSHA256: "latest"})
	held.Release()
	if stream.hasTLS("latest") {
		t.Fatal("TLS bypassed exhausted authorization pool")
	}
	for !stream.hasTLS("latest") {
		select {
		case <-ctx.Done():
			t.Fatal("same stream did not recover TLS on resync")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Keep the old stream connected, revoke it, and exercise the identical retry path.
	if _, err := pool.Exec(ctx, `update engine_certificates set revoked_at=now(),revoke_reason='revoked' where engine_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	f.Set(&pki.DNSTLSMaterial{KeyPEM: []byte("revoked"), FingerprintSHA256: "revoked"})
	f.mu.Lock()
	ch := f.engines[id].ch
	f.mu.Unlock()
	f.RetryFor(ctx, id, ch)
	if len(ch) != 0 || stream.hasTLS("revoked") {
		t.Fatal("revoked registration received TLS retry")
	}

	// Restoring the certificate does not authorize the old session after takeover.
	if _, err := pool.Exec(ctx, `update engine_certificates set revoked_at=null,revoke_reason=null where engine_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	nextSession := uuid.New()
	if _, err := pool.Exec(ctx, `update engines set connection_session=$2 where id=$1`, id, nextSession); err != nil {
		t.Fatal(err)
	}
	f.Set(&pki.DNSTLSMaterial{KeyPEM: []byte("superseded"), FingerprintSHA256: "superseded"})
	f.RetryFor(ctx, id, ch)
	if len(ch) != 0 || stream.hasTLS("superseded") {
		t.Fatal("superseded session received TLS retry")
	}
	replacement, err := f.Register(ctx, id, nextSession, serial.Text(16), "superseded")
	if err != nil {
		t.Fatal(err)
	}
	f.RetryFor(ctx, id, ch)
	if len(replacement) != 0 {
		t.Fatal("old retry altered replacement registration")
	}
	close(stream.in)
	waitEnd(t, stream.controlledStream, codes.OK)
}

// commit error after the server committed is still ambiguous to the producer.
// The helper must discard, even though the database side has completed.
type lostCommitAck struct{ pgx.Tx }

func (t lostCommitAck) Commit(ctx context.Context) error {
	if err := t.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("injected lost COMMIT acknowledgement")
}

func TestCommitBarrierLostAcknowledgement(t *testing.T) {
	st := storetest.New(t)
	for _, producer := range []string{"odoh", "tls", "rpz", "hosted"} {
		t.Run(producer, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx, err := st.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			b := &commitBarrier{done: make(chan struct{})}
			// The backend really commits, but the caller observes an error. No Send may follow.
			if err := commitSecret(ctx, lostCommitAck{tx}, b); err == nil {
				t.Fatal("fault did not run")
			}
			stream := &barrierRecordingStream{}
			switch producer {
			case "odoh":
				sendPreparedServerMessage(ctx, stream, Delivery[*controlv1.ServerMessage]{value: &controlv1.ServerMessage{}, barrier: b})
			case "tls":
				sendPreparedTlsMaterial(ctx, stream, Delivery[*controlv1.TlsMaterial]{value: &controlv1.TlsMaterial{}, barrier: b})
			case "rpz":
				sendPreparedRpzTsigKeys(ctx, stream, Delivery[*controlv1.RpzTsigKeys]{value: &controlv1.RpzTsigKeys{}, barrier: b})
			case "hosted":
				sendPreparedKeyMaterial(ctx, stream, Delivery[*controlv1.KeyMaterial]{value: &controlv1.KeyMaterial{}, barrier: b})
			}
			if stream.sent.Load() != 0 {
				t.Fatal("ambiguous commit delivered secret")
			}
		})
	}
}

type barrierRecordingStream struct {
	controlv1.EngineControl_ConnectServer
	sent atomic.Int32
}

func (s *barrierRecordingStream) Send(*controlv1.ServerMessage) error { s.sent.Add(1); return nil }

func TestCommitBarrierTransportGates(t *testing.T) {
	for _, outcome := range []string{"commit", "rollback", "cancel"} {
		for _, producer := range []string{"odoh", "tls", "rpz", "hosted"} {
			t.Run(outcome+"/"+producer, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				b := &commitBarrier{done: make(chan struct{})}
				stream := &barrierRecordingStream{}
				done := make(chan struct{})
				go func() {
					defer close(done)
					switch producer {
					case "odoh":
						sendPreparedServerMessage(ctx, stream, Delivery[*controlv1.ServerMessage]{value: &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_OdohKeys{OdohKeys: &controlv1.OdohKeys{}}}, barrier: b})
					case "tls":
						sendPreparedTlsMaterial(ctx, stream, Delivery[*controlv1.TlsMaterial]{value: &controlv1.TlsMaterial{}, barrier: b})
					case "rpz":
						sendPreparedRpzTsigKeys(ctx, stream, Delivery[*controlv1.RpzTsigKeys]{value: &controlv1.RpzTsigKeys{}, barrier: b})
					case "hosted":
						sendPreparedKeyMaterial(ctx, stream, Delivery[*controlv1.KeyMaterial]{value: &controlv1.KeyMaterial{}, barrier: b})
					}
				}()
				select {
				case <-done:
					t.Fatal("transport did not wait for barrier")
				case <-time.After(10 * time.Millisecond):
				}
				if stream.sent.Load() != 0 {
					t.Fatal("sent private payload before commit")
				}
				switch outcome {
				case "commit":
					b.resolve(true)
				case "rollback":
					b.resolve(false)
				case "cancel":
					cancel()
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("waiter hung")
				}
				want := int32(0)
				if outcome == "commit" {
					want = 1
				}
				if stream.sent.Load() != want {
					t.Fatalf("sends=%d want=%d", stream.sent.Load(), want)
				}
			})
		}
	}
}
