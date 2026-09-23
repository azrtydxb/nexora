package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type observedTransport struct {
	grpc.ServerStream
	ctx             context.Context
	entered, exited chan struct{}
	failSecond      bool
	sends           int
}

func (s *observedTransport) Context() context.Context { return s.ctx }
func (s *observedTransport) SendMsg(m any) error {
	// Observe real grpc SendMsg, never gate or unblock it ourselves.
	keys := m.(*controlv1.ServerMessage).GetRpzTsigKeys()
	// Bootstrap also sends small key sets and snapshots. Start observing only
	// at our quota-filling message, then observe every subsequent Send: a
	// pending bootstrap snapshot can consume the exhausted quota before B.
	observed := s.sends > 0 || (keys != nil && len(keys.Keys) == 1 && keys.Keys[0] != nil && len(keys.Keys[0].Secret) == 2<<20)
	if observed {
		s.entered <- struct{}{}
		s.sends++
		if s.failSecond && s.sends == 2 {
			s.exited <- struct{}{}
			return status.Error(codes.Unavailable, "injected send error")
		}
	}
	err := s.ServerStream.SendMsg(m)
	if observed {
		s.exited <- struct{}{}
	}
	return err
}

// Real HTTP/2 flow control over bufconn; the client never calls Recv, even
// during shutdown assertions. Authentication is injected by the interceptor;
// certificate/session authorization and cleanup still use real PostgreSQL.
func TestConnectNonReadingPeerShutdown(t *testing.T) {
	base := storetest.New(t)
	for _, mode := range []string{"half-close", "revoke", "peer-cancel", "revoke-busy-receive", "revoke-stalled-rollback", "revoke-busy-update", "half-close-busy-update", "replacement-same-cert", "replacement-new-cert", "replacement-other-hub", "send-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg := base.Pool.Config()
			cfg.MaxConns, cfg.MinConns = 1, 0
			loss := &rollbackResponseLoss{entered: make(chan struct{}), dropped: make(chan struct{})}
			if mode == "revoke-stalled-rollback" {
				dial := cfg.ConnConfig.DialFunc
				cfg.ConnConfig.TLSConfig = nil
				cfg.ConnConfig.Fallbacks = nil
				cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
					c, err := dial(ctx, network, addr)
					if err != nil {
						return nil, err
					}
					return &rollbackLossConn{Conn: c, dropped: loss.dropped}, nil
				}
				cfg.ConnConfig.Tracer = loss
			}
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
			serial := big.NewInt(time.Now().UnixNano())
			if _, err := pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial) values($1,'flow-control',$2)`, id, serial.Text(16)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values($1,$2,now(),now()+interval '1 hour')`, serial.Text(16), id); err != nil {
				t.Fatal(err)
			}
			h := NewHub(st, uuid.NewString())
			f := NewDNSTLSFanout(st)
			server := NewServer(st, nil, h, h.instanceID, f)
			processing := make(chan struct{})
			processingExited := make(chan struct{})
			server.OnStats = func(ctx context.Context, _ pgx.Tx, _ string, _ *controlv1.Stats) error {
				close(processing)
				<-ctx.Done()
				close(processingExited)
				return ctx.Err()
			}
			updateEntered, updateCanceled, updateExited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			cleanupRelease := make(chan struct{})
			var releaseOnce sync.Once
			releaseCleanup := func() { releaseOnce.Do(func() { close(cleanupRelease) }) }
			defer releaseCleanup()
			server.OnUpdate = func(ctx context.Context, _ string, _ *controlv1.UpdateRequest, _ func(pgx.Tx) error) *controlv1.UpdateResult {
				close(updateEntered)
				<-ctx.Done()
				close(updateCanceled)
				// Model bounded application cleanup after cancellation, not transport IO.
				select {
				case <-cleanupRelease:
				case <-time.After(2 * time.Second):
				}
				close(updateExited)
				return &controlv1.UpdateResult{Detail: "cleanup finished"}
			}
			entered, exited := make(chan struct{}, 8), make(chan struct{}, 8)
			returned := make(chan error, 1)
			cert := &x509.Certificate{Subject: pkix.Name{CommonName: id}, SerialNumber: serial}
			gs := grpc.NewServer(grpc.InitialWindowSize(65535), grpc.InitialConnWindowSize(65535), grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
				sc := peer.NewContext(stream.Context(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}}})
				err := handler(srv, &observedTransport{ServerStream: stream, ctx: sc, entered: entered, exited: exited, failSecond: mode == "send-error"})
				returned <- err
				return err
			}))
			listener := bufconn.Listen(64 << 10)
			controlv1.RegisterEngineControlServer(gs, server)
			go func() { _ = gs.Serve(listener) }()
			defer gs.Stop()
			defer listener.Close()
			cc, err := grpc.NewClient("passthrough:///flow-control", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithInitialWindowSize(65535), grpc.WithInitialConnWindowSize(65535))
			if err != nil {
				t.Fatal(err)
			}
			defer cc.Close()
			rpcCtx, rpcCancel := context.WithCancel(ctx)
			defer rpcCancel()
			stream, err := controlv1.NewEngineControlClient(cc).Connect(rpcCtx)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Header(); err != nil {
				t.Fatal(err)
			}
			// Wait for publication of the subscriber and its TLS registration.
			// Observing Send below then proves the worker phase has started.
			var sub *subscriber
			for {
				h.mu.Lock()
				sub = h.latest[id]
				h.mu.Unlock()
				f.mu.Lock()
				registered := f.engines[id] != nil
				f.mu.Unlock()
				if sub != nil && registered {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("registration hung")
				case <-time.After(time.Millisecond):
				}
			}
			wait := func(ch <-chan struct{}, what string) {
				t.Helper()
				select {
				case <-ch:
				case <-ctx.Done():
					t.Fatal(what)
				}
			}
			offer := func(digest string) {
				h.offerKeys(ctx, sub, &controlv1.RpzTsigKeys{Keys: []*controlv1.RpzTsigKey{{Secret: make([]byte, 2<<20)}}}, digest)
			}
			offer("A")
			wait(entered, "first Send not entered")
			wait(exited, "first Send did not consume write quota")
			offer("B")
			wait(entered, "second Send not entered")
			if mode == "send-error" {
				wait(exited, "failed Send did not exit")
			} else {
				select {
				case <-exited:
					t.Fatal("Send did not block on peer flow control")
				case <-time.After(100 * time.Millisecond):
				}
			}
			var replacement *subscriber
			replacementHub, replacementTLS := h, f
			if mode == "replacement-same-cert" || mode == "replacement-new-cert" || mode == "replacement-other-hub" {
				if mode == "replacement-other-hub" {
					replacementHub = NewHub(st, uuid.NewString())
					replacementTLS = NewDNSTLSFanout(st)
				}
				replacement = newSubscriber(id, 0)
				replacement.certificateSerial = serial.Text(16)
				if mode == "replacement-new-cert" {
					next := new(big.Int).Add(serial, big.NewInt(1))
					replacement.certificateSerial = next.Text(16)
					if _, err := pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values($1,$2,now(),now()+interval '1 hour')`, replacement.certificateSerial, id); err != nil {
						t.Fatal(err)
					}
				}
				if err := replacementHub.registerConnection(replacement, func() error {
					_, err := pool.Exec(ctx, `update engines set connection_session=$2,certificate_serial=$3 where id=$1`, id, replacement.sessionID, replacement.certificateSerial)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				replacement.tlsCh, err = replacementTLS.Register(ctx, id, replacement.sessionID, replacement.certificateSerial, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "revoke-busy-receive" || mode == "revoke-stalled-rollback" {
				if err := stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Stats{Stats: &controlv1.Stats{}}}); err != nil {
					t.Fatal(err)
				}
				wait(processing, "receive never acquired sole pool slot")
			}
			busyUpdate := mode == "revoke-busy-update" || mode == "half-close-busy-update"
			if busyUpdate {
				if err := stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_UpdateRequest{UpdateRequest: &controlv1.UpdateRequest{RequestId: "cleanup"}}}); err != nil {
					t.Fatal(err)
				}
				wait(updateEntered, "UPDATE callback not entered")
			}
			shutdownStarted := time.Now()
			want := codes.PermissionDenied
			switch mode {
			case "half-close", "half-close-busy-update":
				want = codes.OK
				if err := stream.CloseSend(); err != nil {
					t.Fatal(err)
				}
			case "peer-cancel":
				rpcCancel()
			case "send-error":
				want = codes.Unavailable
			default:
				// The busy-receive case models notification arriving while a callback
				// owns the sole slot; the plain revoke case persists revocation first.
				if mode == "revoke" {
					if _, err := pool.Exec(ctx, `update engine_certificates set revoked_at=now(),revoke_reason='revoked' where engine_id=$1`, id); err != nil {
						t.Fatal(err)
					}
				}
				sub.revoke()
			}
			if busyUpdate {
				wait(updateCanceled, "UPDATE was not canceled")
				select {
				case err := <-returned:
					t.Fatalf("handler returned before UPDATE cleanup: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				h.mu.Lock()
				current := h.latest[id]
				h.mu.Unlock()
				f.mu.Lock()
				registered := f.engines[id] != nil
				f.mu.Unlock()
				if current != sub || !registered {
					t.Fatal("identity cleanup preceded UPDATE exit")
				}
				releaseCleanup()
			}
			select {
			case err := <-returned:
				if mode != "peer-cancel" && status.Code(err) != want {
					t.Fatalf("handler code %s want %s", status.Code(err), want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handler could not return with non-reading peer")
			}
			if time.Since(shutdownStarted) >= 5*time.Second {
				t.Fatal("handler exceeded shutdown bound")
			}
			if mode == "revoke-stalled-rollback" {
				select {
				case <-loss.entered:
				default:
					t.Fatal("rollback did not start")
				}
				select {
				case <-loss.dropped:
				default:
					t.Fatal("actual rollback response was not dropped")
				}
			}
			// No Recv, rollback stall release, connection Close, server Stop, or client cancel
			// (except the explicit peer-cancel case) precedes these termination checks.
			wait(sub.workers.sendDone, "sender survived handler return")
			wait(sub.workers.recvDone, "transport receiver survived handler return")
			wait(sub.workers.receiveDone, "receive processing survived cleanup")
			wait(sub.workers.retryDone, "TLS retry survived cleanup")
			if mode == "revoke-busy-receive" || mode == "revoke-stalled-rollback" {
				wait(processingExited, "receive callback survived cleanup")
			}
			if busyUpdate {
				select {
				case <-updateExited:
				default:
					t.Fatal("UPDATE survived cleanup")
				}
				if len(sub.results) != 0 || len(sub.updateSlots) != 0 {
					t.Fatal("canceled UPDATE published a result or retained a slot")
				}
				server.update(context.Background(), sub, &controlv1.UpdateRequest{RequestId: "after-cleanup"})
				if len(sub.results) != 0 || len(sub.updateSlots) != 0 {
					t.Fatal("UPDATE admitted after cleanup")
				}
			}
			var session *uuid.UUID
			if err := pool.QueryRow(ctx, `select connection_session from engines where id=$1`, id).Scan(&session); err != nil {
				t.Fatal(err)
			}
			h.mu.Lock()
			latest := h.latest[id]
			members := len(h.subs)
			h.mu.Unlock()
			f.mu.Lock()
			tlsRegistration := f.engines[id]
			f.mu.Unlock()
			if replacement == nil {
				if session != nil || latest != nil || members != 0 || tlsRegistration != nil {
					t.Fatal("old stream cleanup incomplete")
				}
			} else {
				if replacementHub != h {
					if latest != nil || members != 0 || tlsRegistration != nil {
						t.Fatal("old instance membership survived cleanup")
					}
					replacementHub.mu.Lock()
					latest = replacementHub.latest[id]
					members = len(replacementHub.subs)
					replacementHub.mu.Unlock()
					replacementTLS.mu.Lock()
					tlsRegistration = replacementTLS.engines[id]
					replacementTLS.mu.Unlock()
				}
				if session == nil || *session != replacement.sessionID || latest != replacement || members != 1 || tlsRegistration == nil || tlsRegistration.ch != replacement.tlsCh || tlsRegistration.serial != replacement.certificateSerial {
					t.Fatal("old cleanup damaged replacement identity")
				}
				replacementTLS.Unregister(id, replacement.tlsCh)
				replacementHub.unregister(replacement)
			}
			if pool.Stat().AcquiredConns() != 0 {
				t.Fatal("worker retained sole pool slot")
			}
		})
	}
}

// The transport reader remains bounded when application cancellation happens
// during Recv. Its completion still depends on the RPC context, not the child.
func TestCancelableReceiveLifetime(t *testing.T) {
	rpcCtx, rpcCancel := context.WithCancel(context.Background())
	defer rpcCancel()
	ctx, cancel := context.WithCancel(rpcCtx)
	defer cancel()
	stream := &controlledStream{ctx: rpcCtx, in: make(chan *controlv1.EngineMessage), reading: make(chan struct{}, 1)}
	done := make(chan struct{})
	receiver := newCancelableReceive(ctx, stream, done)
	processed := make(chan struct{})
	go func() { defer close(processed); _, _ = receiver.Recv() }()
	select {
	case <-stream.reading:
	case <-time.After(time.Second):
		t.Fatal("transport reader did not start")
	}
	cancel()
	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("application receive did not cancel")
	}
	select {
	case <-done:
		t.Fatal("fixture transport unexpectedly ended before RPC cancellation")
	default:
	}
	rpcCancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transport reader leaked")
	}
}

type transportLifetimeProbe struct {
	controlv1.UnimplementedEngineControlServer
	stop, entered, exited, sendDone, recvDone, returned chan struct{}
}

func (p *transportLifetimeProbe) Connect(stream controlv1.EngineControl_ConnectServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	receiver := newCancelableReceive(ctx, stream, p.recvDone)
	processed := make(chan struct{})
	go func() {
		defer close(processed)
		for {
			if _, err := receiver.Recv(); err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(p.sendDone)
		m := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_RpzTsigKeys{RpzTsigKeys: &controlv1.RpzTsigKeys{Keys: []*controlv1.RpzTsigKey{{Secret: make([]byte, 2<<20)}}}}}
		for range 2 {
			p.entered <- struct{}{}
			err := stream.Send(m)
			p.exited <- struct{}{}
			if err != nil {
				return
			}
		}
	}()
	<-p.stop
	cancel()
	<-processed
	close(p.returned)
	return nil
}

// Check the grpc cancellation assumption without requiring a database. The
// preceding integration test exercises the actual Connect handler and cleanup.
func TestGRPCReturnReleasesTransportWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &transportLifetimeProbe{stop: make(chan struct{}), entered: make(chan struct{}, 2), exited: make(chan struct{}, 2), sendDone: make(chan struct{}), recvDone: make(chan struct{}), returned: make(chan struct{})}
	var once sync.Once
	stop := func() { once.Do(func() { close(p.stop) }) }
	defer stop()
	gs := grpc.NewServer()
	controlv1.RegisterEngineControlServer(gs, p)
	listener := bufconn.Listen(64 << 10)
	defer listener.Close()
	go func() { _ = gs.Serve(listener) }()
	defer gs.Stop()
	cc, err := grpc.NewClient("passthrough:///transport-lifetime", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithInitialWindowSize(65535), grpc.WithInitialConnWindowSize(65535))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	stream, err := controlv1.NewEngineControlClient(cc).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{}}}); err != nil {
		t.Fatal(err)
	}
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal("transport worker did not complete")
		}
	}
	wait(p.entered)
	wait(p.exited)
	wait(p.entered)
	select {
	case <-p.exited:
		t.Fatal("non-reading peer did not block Send")
	case <-time.After(100 * time.Millisecond):
	}
	stop()
	wait(p.returned)
	wait(p.sendDone)
	wait(p.recvDone)
	if ctx.Err() != nil {
		t.Fatal("test deadline, rather than handler return, released IO")
	}
}

// Drop the actual rollback response and keep the socket stalled until its own
// deadline/retirement. There is deliberately no test-controlled release gate.
type rollbackLossConn struct {
	net.Conn
	drop    atomic.Bool
	dropped chan struct{}
	once    sync.Once
}

func (c *rollbackLossConn) Write(p []byte) (int, error) {
	// Also blackhole the driver's separate cancellation request. PostgreSQL
	// receives no startup packet on that connection, so it cannot acknowledge it.
	if len(p) >= 8 && binary.BigEndian.Uint32(p[4:8]) == 80877102 {
		return len(p), nil
	}
	return c.Conn.Write(p)
}
func (c *rollbackLossConn) Read(p []byte) (int, error) {
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

type rollbackResponseLoss struct {
	entered chan struct{}
	dropped chan struct{}
	once    sync.Once
}

func (l *rollbackResponseLoss) TraceQueryStart(ctx context.Context, c *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if q.SQL == "rollback" {
		l.once.Do(func() {
			c.PgConn().Conn().(*rollbackLossConn).drop.Store(true)
			close(l.entered)
		})
	}
	return ctx
}
func (*rollbackResponseLoss) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Exercise the actual UPDATE dispatch without PostgreSQL as well. The integration
// modes above additionally assert Connect's registration and transport ordering.
func TestUpdateWorkersJoinCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := newSubscriber(uuid.NewString(), 0)
	entered, cleaning, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	s := &Server{OnUpdate: func(ctx context.Context, _ string, _ *controlv1.UpdateRequest, _ func(pgx.Tx) error) *controlv1.UpdateResult {
		close(entered)
		<-ctx.Done()
		close(cleaning)
		select {
		case <-release:
		case <-time.After(time.Second):
		}
		close(exited)
		return &controlv1.UpdateResult{Detail: "finished"}
	}}
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not progress")
		}
	}
	s.update(ctx, sub, &controlv1.UpdateRequest{RequestId: "before"})
	wait(entered)
	sub.updateWorkers.stop()
	cancel()
	wait(cleaning)
	joined := make(chan struct{})
	go func() { sub.updateWorkers.wait(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("join ignored callback cleanup")
	case <-time.After(25 * time.Millisecond):
	}
	finish()
	wait(joined)
	select {
	case <-exited:
	default:
		t.Fatal("callback survived join")
	}
	if len(sub.results) != 0 || len(sub.updateSlots) != 0 {
		t.Fatal("canceled callback published or retained slot")
	}
	s.update(context.Background(), sub, &controlv1.UpdateRequest{RequestId: "after"})
	if len(sub.results) != 0 || len(sub.updateSlots) != 0 {
		t.Fatal("admission reopened after join")
	}
}

func TestApplicationWorkersAdmissionStopRace(t *testing.T) {
	for range 100 {
		var workers applicationWorkers
		var callers sync.WaitGroup
		begin := make(chan struct{})
		for range 16 {
			callers.Add(1)
			go func() {
				defer callers.Done()
				<-begin
				if workers.start() {
					workers.wg.Done()
				}
			}()
		}
		close(begin)
		workers.stop()
		workers.wait()
		callers.Wait()
		if workers.start() {
			t.Fatal("admitted work after stop/wait")
		}
	}
}
