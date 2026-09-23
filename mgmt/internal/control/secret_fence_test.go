package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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

func TestFreshSecretProducersFailClosed(t *testing.T) {
	base := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := base.Pool.Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st := &store.Store{Pool: pool}
	sub := newSubscriber(uuid.NewString(), 0)
	sub.certificateSerial = "aa"
	if _, err := pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values ($1,'secret-fence','aa',$2)`, sub.id, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ('aa',$1,now(),now()+interval '1 hour')`, sub.id); err != nil {
		t.Fatal(err)
	}
	h := NewHub(st, "test")
	f := NewDNSTLSFanout(st)
	ch, err := f.Register(ctx, sub.engineID, sub.sessionID, sub.certificateSerial, "")
	if err != nil {
		t.Fatal(err)
	}
	offer := func(seed string, allowed bool) {
		t.Helper()
		h.offerOdohKeys(ctx, sub, &controlv1.OdohKeys{Keys: []*controlv1.OdohKey{{Seed: []byte(seed)}}}, seed)
		h.offerKeys(ctx, sub, &controlv1.RpzTsigKeys{Keys: []*controlv1.RpzTsigKey{{Secret: []byte(seed)}}}, seed)
		h.offerKeyMaterial(ctx, sub, &controlv1.KeyMaterial{TsigKeys: []*controlv1.TsigSecret{{Secret: []byte(seed)}}}, seed)
		f.Set(&pki.DNSTLSMaterial{KeyPEM: []byte(seed), FingerprintSHA256: seed})
		want := 0
		if allowed {
			want = 1
		}
		if len(sub.control) != want || len(sub.keys) != want || len(sub.keyMaterial) != want || len(ch) != want {
			t.Fatalf("%s: queue counts %d/%d/%d/%d want %d", seed, len(sub.control), len(sub.keys), len(sub.keyMaterial), len(ch), want)
		}
		if allowed {
			transport := &barrierRecordingStream{}
			sendPreparedServerMessage(ctx, transport, <-sub.control)
			sendPreparedRpzTsigKeys(ctx, transport, <-sub.keys)
			sendPreparedKeyMaterial(ctx, transport, <-sub.keyMaterial)
			sendPreparedTlsMaterial(ctx, transport, <-ch)
			if transport.sent.Load() != 4 {
				t.Fatal("authorized committed secrets did not reach transport")
			}
		}
		if !allowed {
			late, err := f.Register(ctx, sub.engineID, sub.sessionID, sub.certificateSerial, "")
			if err == nil || len(late) != 0 {
				t.Fatalf("%s: unauthorized initial Register accepted", seed)
			}
			f.Unregister(sub.engineID, late)
		}
	}
	offer("owner", true)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`update engines set connection_session=$2 where id=$1`, sub.id, uuid.New())
	offer("superseded", false)
	exec(`update engines set connection_session=$2,revoked_at=now() where id=$1`, sub.id, sub.sessionID)
	offer("engine-revoked", false)
	exec(`update engines set revoked_at=null where id=$1`, sub.id)
	exec(`update engine_certificates set revoked_at=now(),revoke_reason='revoked' where engine_id=$1`, sub.id)
	offer("cert-revoked", false)
	exec(`update engine_certificates set revoked_at=null,revoke_reason=null where engine_id=$1`, sub.id)
	offer("restored", true)
	exec(`update engines set deleted_at=now() where id=$1`, sub.id)
	offer("deleted", false)
	exec(`update engines set deleted_at=null where id=$1`, sub.id)
	pool.Close()
	offer("database-unavailable", false)
}

// S1 pauses after its real claim, before TLS Register. S2 completes Connect and
// owns the row. Releasing S1 must neither enqueue nor replace S2's membership.
func TestDNSTLSActualRegistrationRace(t *testing.T) {
	base := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	for _, placement := range []string{"same-hub", "reused-instance", "different-instance"} {
		t.Run(placement, func(t *testing.T) {
			id := uuid.NewString()
			serial := big.NewInt(time.Now().UnixNano())
			if _, err := pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial) values ($1,'tls-race',$2)`, id, serial.Text(16)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ($1,$2,now()-interval '1 hour',now()+interval '1 hour')`, serial.Text(16), id); err != nil {
				t.Fatal(err)
			}
			i1, i2 := uuid.NewString(), uuid.NewString()
			if placement != "different-instance" {
				i2 = i1
			}
			h1, h2 := NewHub(st, i1), NewHub(st, i2)
			f1, f2 := NewDNSTLSFanout(st), NewDNSTLSFanout(st)
			if placement == "same-hub" {
				h2, f2 = h1, f1
			}
			cert := &x509.Certificate{Subject: pkix.Name{CommonName: id}, SerialNumber: serial}
			sc := peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}}})
			start := func(h *Hub, f *DNSTLSFanout, blocked bool) *recordingOdohStream {
				s := &recordingOdohStream{controlledStream: &controlledStream{ctx: sc, in: make(chan *controlv1.EngineMessage, 1), reading: make(chan struct{}, 2), done: make(chan error, 1)}}
				if blocked {
					s.headerGate = make(chan struct{})
					s.headerReached = make(chan struct{})
				}
				s.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id}}}
				server := NewServer(st, nil, h, h.instanceID, f)
				go func() { s.done <- server.Connect(s) }()
				waitReading(t, s.controlledStream)
				if blocked {
					select {
					case <-s.headerReached:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				} else {
					waitReading(t, s.controlledStream)
				}
				return s
			}
			old := start(h1, f1, true)
			current := start(h2, f2, false)
			material := &pki.DNSTLSMaterial{KeyPEM: []byte("fresh-after-takeover"), FingerprintSHA256: "fresh"}
			f1.Set(material)
			f2.Set(material)
			close(old.headerGate)
			waitEnd(t, old.controlledStream, codes.Aborted)
			// Old Connect cleanup has run, including its late Unregister.
			material = &pki.DNSTLSMaterial{KeyPEM: []byte("after-old-cleanup"), FingerprintSHA256: "next"}
			f1.Set(material)
			f2.Set(material)
			deadline := time.Now().Add(2 * time.Second)
			for !current.hasTLS("after-old-cleanup") && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !current.hasTLS("after-old-cleanup") {
				t.Fatal("late old registration/cleanup detached current owner")
			}
			close(current.in)
			waitEnd(t, current.controlledStream, codes.OK)
			if old.hasTLS("fresh-after-takeover") || old.hasTLS("after-old-cleanup") {
				t.Fatal("delayed Register delivered fresh TLS secret")
			}
		})
	}
}

func TestDNSTLSEnqueueRacesPersistedClaim(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := NewDNSTLSFanout(st)
	sub := newSubscriber(uuid.NewString(), 0)
	sub.certificateSerial = strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := st.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values ($1,'odoh-race',$2,$3)`, sub.id, sub.certificateSerial, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ($1,$2,now(),now()+interval '1 hour')`, sub.certificateSerial, sub.id); err != nil {
		t.Fatal(err)
	}
	ch, err := f.Register(ctx, sub.engineID, sub.sessionID, sub.certificateSerial, "")
	if err != nil {
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
		f.Set(&pki.DNSTLSMaterial{KeyPEM: []byte("after-claim"), FingerprintSHA256: "after-claim"})
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
	if len(ch) != 0 {
		t.Fatal("stale authorization survived concurrent committed claim")
	}
}

func TestDNSTLSEnqueuePrecedesUnlock(t *testing.T) {
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
	f := NewDNSTLSFanout(st)
	ch, err := f.Register(ctx, sub.engineID, sub.sessionID, sub.certificateSerial, "")
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	transport := &barrierRecordingStream{}
	delivered := make(chan struct{})
	trace := &odohCommitTrace{beforeCommit: func() {
		observed = true
		if len(ch) != 1 {
			t.Error("ownership transaction ended before enqueue")
		}
		prepared := <-ch
		go func() { sendPreparedTlsMaterial(ctx, transport, prepared); close(delivered) }()
		select {
		case <-delivered:
			t.Error("TLS delivered before commit acknowledgement")
		case <-time.After(10 * time.Millisecond):
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
	f.st = &store.Store{Pool: pool}
	f.Set(&pki.DNSTLSMaterial{KeyPEM: []byte("atomic"), FingerprintSHA256: "atomic"})
	if !observed {
		t.Fatal("no fenced enqueue transaction")
	}

	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("committed TLS waiter hung")
	}
	if transport.sent.Load() != 1 {
		t.Fatal("committed TLS not delivered")
	}
}

func TestFreshSecretFenceRequiresDatabase(t *testing.T) {
	ctx := context.Background()
	sub := newSubscriber(uuid.NewString(), 0)
	sub.certificateSerial = "aa"
	h := NewHub(nil, "no-database")
	h.offerOdohKeys(ctx, sub, &controlv1.OdohKeys{Keys: []*controlv1.OdohKey{{Seed: []byte("fresh")}}}, "fresh")
	h.offerKeys(ctx, sub, &controlv1.RpzTsigKeys{Keys: []*controlv1.RpzTsigKey{{Secret: []byte("fresh")}}}, "fresh")
	h.offerKeyMaterial(ctx, sub, &controlv1.KeyMaterial{TsigKeys: []*controlv1.TsigSecret{{Secret: []byte("fresh")}}}, "fresh")
	f := NewDNSTLSFanout()
	ch, err := f.Register(ctx, sub.engineID, sub.sessionID, sub.certificateSerial, "")
	if err == nil || len(ch) != 0 || len(f.engines) != 0 {
		t.Fatal("registration without a database accepted")
	}
	// Model an existing registration when the database is unavailable.
	e := &tlsEngine{id: sub.id, sessionID: sub.sessionID, serial: sub.certificateSerial, ch: make(chan Delivery[*controlv1.TlsMaterial], 1)}
	f.engines[sub.engineID] = e
	f.Set(&pki.DNSTLSMaterial{KeyPEM: []byte("fresh"), FingerprintSHA256: "fresh"})
	if len(sub.control)+len(sub.keys)+len(sub.keyMaterial)+len(e.ch) != 0 {
		t.Fatal("fresh secret queued without database authorization")
	}
	if sub.odohKeysDigest != "" || sub.keysDigest != "" || sub.keyMaterialDigest != "" {
		t.Fatal("failed enqueue suppressed retries")
	}
}
