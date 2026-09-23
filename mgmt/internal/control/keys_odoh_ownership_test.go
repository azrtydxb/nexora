package control

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// This loader also acquires the single-slot pool. Loading inside the ownership
// transaction would deadlock. The gate models an initial load delayed by IO.
type ownershipOdohLoader struct {
	pool          *pgxpool.Pool
	mu            sync.Mutex
	seed          []byte
	gate, reached chan struct{}
}

func (l *ownershipOdohLoader) rotate() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seed = make([]byte, 32)
	if _, err := rand.Read(l.seed); err != nil {
		panic(err)
	}
}
func (l *ownershipOdohLoader) Load(ctx context.Context) (*controlv1.OdohKeys, string, error) {
	l.mu.Lock()
	gate, reached := l.gate, l.reached
	l.gate, l.reached = nil, nil
	l.mu.Unlock()
	if gate != nil {
		close(reached)
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if _, err := l.pool.Exec(ctx, "select 1"); err != nil {
		return nil, "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return &controlv1.OdohKeys{Keys: []*controlv1.OdohKey{{Seed: append([]byte(nil), l.seed...)}}}, string(l.seed), nil
}

type recordingOdohStream struct {
	*controlledStream
	mu          sync.Mutex
	seeds       []string
	tlsSeeds    []string
	sendGate    chan struct{}
	sendReached chan struct{}
	sendOnce    sync.Once
}

func (s *recordingOdohStream) Send(m *controlv1.ServerMessage) error {
	if s.sendGate != nil {
		s.sendOnce.Do(func() { close(s.sendReached) })
		select {
		case <-s.sendGate:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	if k := m.GetTlsMaterial(); k != nil {
		s.mu.Lock()
		s.tlsSeeds = append(s.tlsSeeds, string(k.PrivateKeyPem))
		s.mu.Unlock()
	}
	if k := m.GetOdohKeys(); k != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, k := range k.Keys {
			s.seeds = append(s.seeds, string(k.Seed))
		}
	}
	return nil
}
func (s *recordingOdohStream) has(seed string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.seeds {
		if v == seed {
			return true
		}
	}
	return false
}

func (s *recordingOdohStream) hasTLS(seed string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.tlsSeeds {
		if v == seed {
			return true
		}
	}
	return false
}

// Silent S1 sends nothing after Hello. S2 claims the persisted session, then a
// genuinely new seed is generated. No inbound fence can hide this regression.
func TestOdohSilentSupersededStream(t *testing.T) {
	base := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	for _, placement := range []string{"same-hub", "other-instance", "reused-instance-id", "blocked-old-send"} {
		for _, renewed := range []bool{false, true} {
			for _, initialRace := range []bool{false, true} {
				t.Run(placement+"/renewed="+boolName(renewed)+"/initial-race="+boolName(initialRace), func(t *testing.T) {
					id := uuid.NewString()
					oldSerial := big.NewInt(time.Now().UnixNano())
					newSerial := new(big.Int).Add(oldSerial, big.NewInt(1))
					if !renewed {
						newSerial = oldSerial
					}
					if _, err := pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial) values ($1,'odoh-owner',$2)`, id, oldSerial.Text(16)); err != nil {
						t.Fatal(err)
					}
					if _, err := pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after,issued_at) values ($1,$2,now()-interval '1 hour',now()+interval '1 hour',now()-interval '1 minute')`, oldSerial.Text(16), id); err != nil {
						t.Fatal(err)
					}
					if renewed {
						if _, err := pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ($1,$2,now()-interval '1 hour',now()+interval '1 hour')`, newSerial.Text(16), id); err != nil {
							t.Fatal(err)
						}
					}
					loader := &ownershipOdohLoader{pool: pool}
					loader.rotate()
					firstID, secondID := uuid.NewString(), uuid.NewString()
					if placement != "other-instance" {
						secondID = firstID
					}
					h1 := NewHub(st, firstID)
					h2 := NewHub(st, secondID)
					if placement == "same-hub" {
						h2 = h1
					}
					h1.ODoH, h2.ODoH = loader, loader
					tls1, tls2 := NewDNSTLSFanout(st), NewDNSTLSFanout(st)
					if h1 == h2 {
						tls2 = tls1
					}
					fanouts := map[*Hub]*DNSTLSFanout{h1: tls1, h2: tls2}
					start := func(h *Hub, serial *big.Int, block bool) *recordingOdohStream {
						cert := &x509.Certificate{Subject: pkix.Name{CommonName: id}, SerialNumber: serial}
						sc, stop := context.WithCancel(peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}}}))
						t.Cleanup(stop)
						stream := &recordingOdohStream{controlledStream: &controlledStream{ctx: sc, in: make(chan *controlv1.EngineMessage, 1), reading: make(chan struct{}, 2), done: make(chan error, 1)}}
						if block {
							stream.sendGate = make(chan struct{})
							stream.sendReached = make(chan struct{})
						}
						stream.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id}}}
						server := NewServer(st, nil, h, h.instanceID, fanouts[h])
						go func() { stream.done <- server.Connect(stream) }()
						waitReading(t, stream.controlledStream)
						return stream
					}
					gate := make(chan struct{})
					reached := make(chan struct{})
					if initialRace {
						loader.gate, loader.reached = gate, reached
					}
					blocked := placement == "blocked-old-send" && !initialRace
					old := start(h1, oldSerial, blocked)
					if initialRace {
						select {
						case <-reached:
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					} else {
						waitReading(t, old.controlledStream)
					}
					if blocked {
						select {
						case <-old.sendReached:
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					oldSub := h1.subscribers(func(s *subscriber) bool { return s.engineID == id })[0]
					current := start(h2, newSerial, false)
					waitReading(t, current.controlledStream)
					loader.mu.Lock()
					bootstrap := string(loader.seed)
					loader.mu.Unlock()
					deadline := time.Now().Add(2 * time.Second)
					for (!current.has(bootstrap) || (!initialRace && !blocked && !old.has(bootstrap))) && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if !current.has(bootstrap) {
						t.Fatal("replacement missed bootstrap keys")
					}
					if !initialRace && !blocked && !old.has(bootstrap) {
						t.Fatal("first stream missed bootstrap keys")
					}
					loader.rotate() // Only now create the secret whose absence on S1 matters.
					loader.mu.Lock()
					fresh := string(loader.seed)
					loader.mu.Unlock()
					if initialRace {
						close(gate)
						waitReading(t, old.controlledStream)
					}
					// Models a watcher completing its load only after takeover.
					material := &pki.DNSTLSMaterial{KeyPEM: []byte(fresh), FingerprintSHA256: "fresh"}
					tls1.Set(material)
					tls2.Set(material)
					h1.offerOdohKeysToAll(ctx)
					h1.pushAll(ctx) // periodic resync uses the same fence
					if h2 != h1 {
						h2.offerOdohKeysToAll(ctx)
					}
					deadline = time.Now().Add(2 * time.Second)
					for (!current.has(fresh) || !current.hasTLS(fresh)) && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if !current.has(fresh) || !current.hasTLS(fresh) {
						t.Fatal("replacement did not receive new seed")
					}
					if blocked {
						close(old.sendGate)
					}
					// End both streams and wait for their send loops before inspecting all output.
					close(old.in)
					close(current.in)
					for _, s := range []*recordingOdohStream{old, current} {
						select {
						case err := <-s.done:
							if err != nil {
								t.Fatal(err)
							}
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					for len(oldSub.control) > 0 {
						prepared := <-oldSub.control
						msg, _ := prepared.Await(ctx)
						if k := msg.GetOdohKeys(); k != nil {
							for _, key := range k.Keys {
								if string(key.Seed) == fresh {
									t.Fatal("post-takeover seed left queued on old subscriber")
								}
							}
						}
					}
					select {
					case prepared := <-oldSub.tlsCh:
						m, ok := prepared.Await(ctx)
						if ok && string(m.PrivateKeyPem) == fresh {
							t.Fatal("post-takeover TLS secret queued on old stream")
						}
					default:
					}
					if old.hasTLS(fresh) {
						t.Fatal("silent superseded stream received post-takeover TLS secret")
					}
					if old.has(fresh) {
						t.Fatal("silent superseded stream received post-takeover seed")
					}
					if placement == "same-hub" && h1.Connected() != 0 {
						t.Fatal("subscriptions leaked")
					}
				})
			}
		}
	}
}
func boolName(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// A successful replacement retires membership, but a failed claim must leave
// the current subscriber live. No production IO is needed for this regression.
func TestOdohLocalSubscriptionRetirement(t *testing.T) {
	h := NewHub(nil, "same-instance")
	old := newSubscriber(uuid.NewString(), 0)
	next := newSubscriber(old.engineID, 0)
	if err := h.registerConnection(old, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.registerConnection(next, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	subs := h.subscribers(func(*subscriber) bool { return true })
	if len(subs) != 1 || subs[0] != next {
		t.Fatal("superseded subscriber remains eligible for fanout")
	}
	failed := newSubscriber(old.engineID, 0)
	if err := h.registerConnection(failed, func() error { return context.Canceled }); err == nil {
		t.Fatal("claim unexpectedly succeeded")
	}
	subs = h.subscribers(func(*subscriber) bool { return true })
	if len(subs) != 1 || subs[0] != next {
		t.Fatal("failed claim retired legitimate subscriber")
	}
}

// Queue saturation must neither record a digest nor retain the ownership lock.
// Old queued seeds deliberately remain; after a takeover no NEW seed may join them.
func TestOdohQueueFenceAndRevocation(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := st.Pool.Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := NewHub(&store.Store{Pool: pool}, "test")
	sub := newSubscriber(uuid.NewString(), 0)
	sub.certificateSerial = strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values ($1,'odoh-queue',$2,$3)`, sub.id, sub.certificateSerial, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ($1,$2,now(),now()+interval '1 hour')`, sub.certificateSerial, sub.id); err != nil {
		t.Fatal(err)
	}
	offer := func(digest string) {
		h.offerOdohKeys(ctx, sub, &controlv1.OdohKeys{Keys: []*controlv1.OdohKey{{Seed: []byte(digest)}}}, digest)
	}
	for i := 0; i < cap(sub.control); i++ {
		offer(string(rune('a' + i)))
	}
	offer("full")
	if sub.odohKeysDigest == "full" {
		t.Fatal("full queue suppressed retry")
	}
	<-sub.control
	offer("full")
	if sub.odohKeysDigest != "full" {
		t.Fatal("key not retried after freeing capacity")
	}
	// This uses the only pool slot and takes the same engine lock. It cannot
	// finish if fanout leaked a transaction or waited on the undrained queue.
	if _, err := pool.Exec(ctx, `update engines set connection_session=$2 where id=$1`, sub.id, uuid.New()); err != nil {
		t.Fatal(err)
	}
	offer("post-takeover")
	for len(sub.control) > 0 {
		prepared := <-sub.control
		msg, _ := prepared.Await(ctx)
		if string(msg.GetOdohKeys().Keys[0].Seed) == "post-takeover" {
			t.Fatal("new secret joined stale queue")
		}
	}
	t.Log("pre-takeover messages remained in subscriber.control; these are outside the new-secret fence")
	// Restore ownership, then revoke without delivering a notification.
	if _, err := pool.Exec(ctx, `update engines set connection_session=$2,revoked_at=now() where id=$1`, sub.id, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	offer("engine-revoked")
	if len(sub.control) != 0 {
		t.Fatal("persisted engine revocation bypassed")
	}
	if _, err := pool.Exec(ctx, `update engines set revoked_at=null where id=$1`, sub.id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update engine_certificates set revoked_at=now(),revoke_reason='revoked' where serial=$1`, sub.certificateSerial); err != nil {
		t.Fatal(err)
	}
	offer("certificate-revoked")
	if len(sub.control) != 0 {
		t.Fatal("persisted certificate revocation bypassed")
	}
	if _, err := pool.Exec(ctx, `update engine_certificates set revoked_at=null,revoke_reason=null where serial=$1`, sub.certificateSerial); err != nil {
		t.Fatal(err)
	}
	offer("current")
	if len(sub.control) != 1 {
		t.Fatal("legitimate delivery disabled")
	}
	<-sub.control
	if _, err := pool.Exec(ctx, `update engines set deleted_at=now() where id=$1`, sub.id); err != nil {
		t.Fatal(err)
	}
	offer("deleted")
	if len(sub.control) != 0 {
		t.Fatal("deleted engine received seed")
	}
}

// Force the broadcast to wait behind an uncommitted takeover, then let the
// takeover commit. PostgreSQL must recheck the session after obtaining the lock.
func TestOdohEnqueueRacesPersistedClaim(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := NewHub(st, "race")
	sub := newSubscriber(uuid.NewString(), 0)
	sub.certificateSerial = strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := st.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values ($1,'odoh-race',$2,$3)`, sub.id, sub.certificateSerial, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ($1,$2,now(),now()+interval '1 hour')`, sub.certificateSerial, sub.id); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `update engines set connection_session=$2 where id=$1`, sub.id, uuid.New()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.offerOdohKeys(ctx, sub, &controlv1.OdohKeys{Keys: []*controlv1.OdohKey{{Seed: []byte("after-claim")}}}, "after-claim")
	}()
	// Observe an actual lock wait, not a scheduling delay.
	for {
		var waiting bool
		if err := st.Pool.QueryRow(ctx, `select exists(select 1 from pg_stat_activity where datname=current_database() and pid<>pg_backend_pid() and wait_event_type='Lock' and query like '%connection_session=$2%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-done:
			t.Fatal("enqueue did not wait for persisted ownership lock")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if len(sub.control) != 0 {
		t.Fatal("stale authorization survived concurrent committed claim")
	}
}

type odohCommitTrace struct{ beforeCommit func() }

func (t *odohCommitTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if q.SQL == "commit" && t.beforeCommit != nil {
		t.beforeCommit()
	}
	return ctx
}
func (*odohCommitTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A check-then-enqueue implementation fails here even when there is no lucky
// scheduling window: the queue must already contain the secret at COMMIT, while
// the engine row still refuses a competing claim. The tracer is test-only IO.
func TestOdohEnqueuePrecedesUnlock(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sub := newSubscriber(uuid.NewString(), 0)
	sub.certificateSerial = strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := st.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values ($1,'odoh-atomic',$2,$3)`, sub.id, sub.certificateSerial, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ($1,$2,now(),now()+interval '1 hour')`, sub.certificateSerial, sub.id); err != nil {
		t.Fatal(err)
	}
	observed := false
	trace := &odohCommitTrace{beforeCommit: func() {
		observed = true
		if len(sub.control) != 1 {
			t.Error("ownership transaction ended before enqueue")
		}
		tx, err := st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, "set local lock_timeout='100ms'"); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `update engines set connection_session=$2 where id=$1`, sub.id, uuid.New())
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "55P03" {
			t.Errorf("enqueue did not retain ownership lock: %v", err)
		}
	}}
	cfg := st.Pool.Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := NewHub(&store.Store{Pool: pool}, "atomic")
	h.offerOdohKeys(ctx, sub, &controlv1.OdohKeys{Keys: []*controlv1.OdohKey{{Seed: []byte("atomic")}}}, "atomic")
	if !observed {
		t.Fatal("no fenced enqueue transaction")
	}
	if len(sub.control) != 1 {
		t.Fatal("current owner did not receive key")
	}
}
