package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dynupdate"
	"github.com/piwi3910/nexora/mgmt/internal/xfrin"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

// controlledStream drives the real Connect lifecycle without transport scheduling:
// waiting for Recv proves the preceding message (or Hello initialization) completed.
type controlledStream struct {
	grpc.ServerStream
	ctx           context.Context
	in            chan *controlv1.EngineMessage
	reading       chan struct{}
	done          chan error
	headerGate    chan struct{}
	headerReached chan struct{}
}

func (s *controlledStream) Context() context.Context            { return s.ctx }
func (s *controlledStream) Send(*controlv1.ServerMessage) error { return nil }
func (s *controlledStream) SendHeader(metadata.MD) error {
	if s.headerGate != nil {
		close(s.headerReached)
		select {
		case <-s.headerGate:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	return nil
}
func (s *controlledStream) Recv() (*controlv1.EngineMessage, error) {
	select {
	case s.reading <- struct{}{}:
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
	select {
	case m, ok := <-s.in:
		if !ok {
			return nil, io.EOF
		}
		return m, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}
func waitReading(t *testing.T, s *controlledStream) {
	t.Helper()
	select {
	case <-s.reading:
	case err := <-s.done:
		t.Fatalf("stream ended: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not reach Recv")
	}
}
func waitEnd(t *testing.T, s *controlledStream, code codes.Code) {
	t.Helper()
	select {
	case err := <-s.done:
		if status.Code(err) != code {
			t.Fatalf("stream end = %v, want %s", err, code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end")
	}
}

func TestConnectionOwnershipAcrossServers(t *testing.T) {
	st := storetest.New(t)
	cfg := st.Pool.Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st = &store.Store{Pool: pool}
	// Ephemeral test signer only; no deployment identity or trust files are read.
	dir := t.TempDir()
	if err := pki.InitCA(dir); err != nil {
		t.Fatal(err)
	}
	ca, err := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, sameInstance := range []bool{false, true} {
		for _, kind := range []string{"disconnect", "applied", "rejected", "stats", "tls", "renew", "hello-ahead", "notify", "logs", "update"} {
			name := kind + "/different-instance"
			if sameInstance {
				name = kind + "/same-instance-id"
			}
			t.Run(name, func(t *testing.T) {
				id := uuid.NewString()
				serial := big.NewInt(int64(time.Now().UnixNano()))
				if _, err := st.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial) values ($1,'owner-test',$2)`, id, serial.Text(16)); err != nil {
					t.Fatal(err)
				}
				if _, err := st.Pool.Exec(ctx, `insert into engine_certificates(serial,engine_id,not_before,not_after) values ($1,$2,now()-interval '1 hour',now()+interval '1 hour')`, serial.Text(16), id); err != nil {
					t.Fatal(err)
				}
				firstID, secondID := uuid.NewString(), uuid.NewString()
				if sameInstance {
					secondID = firstID
				}
				oldServer := NewServer(st, ca, NewHub(st, firstID), firstID, NewDNSTLSFanout())
				currentServer := NewServer(st, ca, NewHub(st, secondID), secondID, NewDNSTLSFanout())
				var statsCalls atomic.Int32
				oldServer.OnStats = func(context.Context, pgx.Tx, string, *controlv1.Stats) error { statsCalls.Add(1); return nil }
				cert := &x509.Certificate{Subject: pkix.Name{CommonName: id}, SerialNumber: serial}
				streamCtx, stop := context.WithCancel(peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}}}))
				defer stop()
				start := func(server *Server, ahead bool) *controlledStream {
					s := &controlledStream{ctx: streamCtx, in: make(chan *controlv1.EngineMessage, 1), reading: make(chan struct{}, 1), done: make(chan error, 1)}
					applied := uint64(0)
					if ahead {
						applied = 99
						s.headerGate = make(chan struct{})
						s.headerReached = make(chan struct{})
					}
					s.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, AppliedVersion: applied}}}
					go func() { s.done <- server.Connect(s) }()
					waitReading(t, s) // Hello
					if !ahead {
						waitReading(t, s)
					} else {
						select {
						case <-s.headerReached:
						case <-time.After(5 * time.Second):
							t.Fatal("no Hello acceptance")
						}
					}
					return s
				}
				old := start(oldServer, kind == "hello-ahead")
				// Real production adapters; no scheduler is running, so NOTIFY
				// schedules work without making a network request.
				zs := &zone.Service{Store: st, Now: time.Now}
				actor := auth.Actor{Type: "system", ID: "ownership-test", Name: "ownership-test"}
				var primary, secondary *zone.Zone
				if kind == "update" || kind == "notify" {
					primary, err = zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "p-" + id + ".test.", Kind: "primary", DefaultTTL: 300,
						SOA: zone.SOA{MName: "ns.test.", RName: "hostmaster.test."}, Nameservers: []string{"ns.test."}})
					if err != nil {
						t.Fatal(err)
					}
					secondary, err = zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "s-" + id + ".test.", Kind: "secondary", Primaries: []zone.Endpoint{{Address: "192.0.2.1:53"}}})
					if err != nil {
						t.Fatal(err)
					}
				}
				scheduler := &xfrin.Scheduler{Store: st}
				notify := func(ctx context.Context, tx pgx.Tx, _ string, ev *controlv1.NotifyReceived) error {
					return scheduler.NotifyTx(ctx, tx, ev.Zone, ev.Source)
				}
				oldServer.OnNotify, currentServer.OnNotify = notify, notify
				broker := NewLogBroker(st)
				oldServer.SetLogBroker(broker)
				currentServer.SetLogBroker(broker)
				logID := uuid.NewString()
				logWaiter := make(chan *controlv1.LogBatch, 1)
				broker.waiters[logID] = logWaiter
				for _, sub := range oldServer.hub.subscribers(func(s *subscriber) bool { return s.engineID == id }) {
					sub.offerLog(&controlv1.LogRequest{RequestId: logID}, time.Now())
				}
				var updateReq *controlv1.UpdateRequest
				updateEntered, releaseUpdate, updateDone := make(chan struct{}), make(chan struct{}), make(chan *controlv1.UpdateResult, 1)
				if kind == "update" {
					m := new(dns.Msg)
					m.SetUpdate(primary.Name)
					rr, e := dns.NewRR("host." + primary.Name + " 300 IN A 192.0.2.10")
					if e != nil {
						t.Fatal(e)
					}
					m.Insert([]dns.RR{rr})
					wire, e := m.Pack()
					if e != nil {
						t.Fatal(e)
					}
					updateReq = &controlv1.UpdateRequest{RequestId: "delayed", Zone: primary.Name, Message: wire}
					applier := &dynupdate.Applier{Zones: zs, Now: time.Now}
					currentServer.OnUpdate = applier.ApplyFenced
					oldServer.OnUpdate = func(ctx context.Context, engine string, req *controlv1.UpdateRequest, fence func(pgx.Tx) error) *controlv1.UpdateResult {
						close(updateEntered)
						select {
						case <-releaseUpdate:
						case <-ctx.Done():
						}
						res := applier.ApplyFenced(ctx, engine, req, fence)
						updateDone <- res
						return res
					}
					old.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_UpdateRequest{UpdateRequest: updateReq}}
					waitReading(t, old)
					select {
					case <-updateEntered:
					case <-time.After(3 * time.Second):
						t.Fatal("update did not start")
					}
				}

				var statsErr error
				currentServer.OnStats = func(ctx context.Context, tx pgx.Tx, engineID string, sample *controlv1.Stats) error {
					statsErr = stats.RecordWithQuerier(ctx, tx, engineID, sample)
					return statsErr
				}
				current := start(currentServer, false)
				current.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Stats{Stats: &controlv1.Stats{}}}
				waitReading(t, current)
				if statsErr != nil {
					t.Fatalf("current stats callback: %v", statsErr)
				}
				current.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: 7, PersistError: "current"}}}
				waitReading(t, current)
				current.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_TlsMaterialResult{TlsMaterialResult: &controlv1.TlsMaterialResult{FingerprintSha256: strings.Repeat("a", 64), Applied: true}}}
				waitReading(t, current)
				state := func() string {
					var v string
					if err := st.Pool.QueryRow(ctx, `select row_to_json(e)::text from engines e where id=$1`, id).Scan(&v); err != nil {
						t.Fatal(err)
					}
					return v
				}
				extraState := func() string {
					var value string
					if e := pool.QueryRow(ctx, `select json_build_array(
                        (select json_agg(z order by name) from zones z),
                        (select count(*) from config_versions),
                        (select count(*) from audit_log),
                        (select count(*) from engine_log_replies))::text`).Scan(&value); e != nil {
						t.Fatal(e)
					}
					return value
				}
				extraBefore := extraState()
				before := state()
				switch kind {
				case "notify":
					old.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_NotifyReceived{NotifyReceived: &controlv1.NotifyReceived{Zone: secondary.Name, Source: "192.0.2.1:1234"}}}
				case "logs":
					old.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_LogBatch{LogBatch: &controlv1.LogBatch{RequestId: logID}}}
				case "update":
					close(releaseUpdate)
					select {
					case res := <-updateDone:
						if res.Rcode != dns.RcodeServerFailure {
							t.Fatalf("stale update: %v", res)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("delayed update blocked")
					}
					close(old.in)
				case "disconnect":
					close(old.in)
				case "applied":
					old.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: 2, PersistError: "stale"}}}
				case "rejected":
					old.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Rejected{Rejected: &controlv1.Rejected{Version: 9, Reason: "stale"}}}
				case "stats":
					old.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Stats{Stats: &controlv1.Stats{}}}
				case "tls":
					old.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_TlsMaterialResult{TlsMaterialResult: &controlv1.TlsMaterialResult{FingerprintSha256: strings.Repeat("b", 64), Applied: true}}}
				case "renew":
					key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
					if err != nil {
						t.Fatal(err)
					}
					csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: id}}, key)
					if err != nil {
						t.Fatal(err)
					}
					old.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{CsrDer: csr}}}
				case "hello-ahead":
					close(old.headerGate)
				}
				code := codes.Aborted
				if kind == "disconnect" || kind == "update" {
					code = codes.OK
				}
				waitEnd(t, old, code)
				select {
				case <-logWaiter:
					t.Fatal("stale log reply woke local waiter")
				default:
				}
				if extraState() != extraBefore {
					t.Fatal("stale callback changed zone, audit, snapshot, or logs")
				}
				// Legitimate callbacks on the replacement must still work with
				// the same single-slot pool.
				if kind == "notify" {
					current.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_NotifyReceived{NotifyReceived: &controlv1.NotifyReceived{Zone: secondary.Name, Source: "192.0.2.1:1234"}}}
					waitReading(t, current)
					var trigger string
					if e := pool.QueryRow(ctx, "select refresh_trigger from zones where id=$1", secondary.ID).Scan(&trigger); e != nil || trigger != "notify" {
						t.Fatalf("current notify: %q %v", trigger, e)
					}
				}
				if kind == "update" {
					sub := currentServer.hub.subscribers(func(s *subscriber) bool { return s.engineID == id })[0]
					res := currentServer.OnUpdate(ctx, id, updateReq, currentServer.connectionFence(ctx, sub))
					if res.Rcode != dns.RcodeSuccess {
						t.Fatalf("current update: %v", res)
					}
					records, _, e := zs.ListRecords(ctx, primary.ID, "host."+primary.Name, "A", "", 100)
					if e != nil || len(records) != 1 {
						t.Fatalf("current update records: %v %v", records, e)
					}
				}
				if kind == "logs" {
					sub := currentServer.hub.subscribers(func(s *subscriber) bool { return s.engineID == id })[0]
					sub.offerLog(&controlv1.LogRequest{RequestId: logID}, time.Now())
					current.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_LogBatch{LogBatch: &controlv1.LogBatch{RequestId: logID}}}
					waitReading(t, current)
					var n int
					if e := pool.QueryRow(ctx, "select count(*) from engine_log_replies where request_id=$1", logID).Scan(&n); e != nil || n != 1 {
						t.Fatalf("current log reply: %d %v", n, e)
					}
					select {
					case batch := <-logWaiter:
						if batch != nil {
							t.Fatal("local waiter bypassed committed storage")
						}
					default:
						t.Fatal("current log reply did not wake local waiter")
					}
				}

				if after := state(); before != after {
					t.Fatalf("stale %s changed engine\nbefore: %s\nafter: %s", kind, before, after)
				}
				if statsCalls.Load() != 0 {
					t.Fatal("stale stats reached persistence callback")
				}
				var fingerprint string
				if err := st.Pool.QueryRow(ctx, "select fingerprint from engine_tls_state where engine_id=$1", id).Scan(&fingerprint); err != nil {
					t.Fatal(err)
				}
				var samples int
				if err := st.Pool.QueryRow(ctx, "select count(*) from engine_stats where engine_id=$1", id).Scan(&samples); err != nil {
					t.Fatal(err)
				}
				if samples != 1 {
					t.Fatalf("stats samples = %d, want current stream sample only", samples)
				}
				if fingerprint != strings.Repeat("a", 64) {
					t.Fatal("stale TLS result persisted")
				}
				// The replacement still accepts state and its own disconnect clears ownership.
				current.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: 8}}}
				waitReading(t, current)
				close(current.in)
				waitEnd(t, current, codes.OK)
				var cleared bool
				if err := st.Pool.QueryRow(ctx, "select connected_instance is null and connection_session is null and applied_version=8 from engines where id=$1", id).Scan(&cleared); err != nil {
					t.Fatal(err)
				}
				if !cleared {
					t.Fatal("current stream could not update/clear its ownership")
				}
			})
		}
	}
}

// A single pool slot is occupied by withConnection. Persistence must use that
// transaction, including resolution status, and errors must roll everything back.
func TestStatsOwnershipSingleConnection(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := st.Pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	one := &store.Store{Pool: pool}
	id := uuid.NewString()
	sub := newSubscriber(id, 0)
	if _, err := pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session,last_seen_at) values ($1,'single-slot','single-slot-cert',$2,'2000-01-01')`, id, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	var zone store.RPZZone
	if err := one.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		zone, err = store.CreateRPZZone(ctx, tx, store.RPZZone{Name: "single-slot.test.", SourceType: "file", MinRefreshSeconds: 60, PolicyOverride: "given"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(one, nil, nil, "", nil)
	callbackErr := errors.New("persistence failure")
	fail := false
	calls := 0
	s.OnStats = func(ctx context.Context, tx pgx.Tx, engineID string, sample *controlv1.Stats) error {
		calls++
		if err := stats.RecordWithQuerier(ctx, tx, engineID, sample); err != nil {
			return err
		}
		if err := stats.RecordM3WithQuerier(ctx, tx, engineID, sample); err != nil {
			return err
		}
		if fail {
			return callbackErr
		}
		return nil
	}
	deliver := func() error {
		stream := &controlledStream{ctx: ctx, in: make(chan *controlv1.EngineMessage, 1), reading: make(chan struct{}, 2)}
		stream.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Stats{Stats: &controlv1.Stats{
			QueriesTotal: 42, Dnssec: &controlv1.DnssecStats{Secure: 7},
			RpzZones: []*controlv1.RpzZoneStatus{{Id: zone.ID.String(), Serial: 7}},
		}}}
		close(stream.in)
		return s.receive(ctx, stream, sub)
	}
	state := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `select json_build_array(
			(select last_seen_at from engines where id=$1),
			(select count(*) from engine_stats where engine_id=$1),
			(select row_to_json(r) from engine_stats_rollup r where engine_id=$1),
			(select row_to_json(d) from engine_dnssec_status d where engine_id=$1),
			(select row_to_json(r) from engine_rpz_status r where engine_id=$1))::text`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := state()
	fail = true
	if err := deliver(); status.Code(err) != codes.Internal {
		t.Fatalf("callback failure = %v", err)
	}
	if after := state(); after != before {
		t.Fatalf("failed callback committed writes: %s -> %s", before, after)
	}
	fail = false
	if err := deliver(); err != nil {
		t.Fatalf("single-connection persistence: %v", err)
	}
	var samples, rollups, dnssec, rpz int
	if err := pool.QueryRow(ctx, `select
		(select count(*) from engine_stats where engine_id=$1),
		(select count(*) from engine_stats_rollup where engine_id=$1),
		(select count(*) from engine_dnssec_status where engine_id=$1),
		(select count(*) from engine_rpz_status where engine_id=$1)`, id).Scan(&samples, &rollups, &dnssec, &rpz); err != nil {
		t.Fatal(err)
	}
	if samples != 1 || rollups != 1 || dnssec != 1 || rpz != 1 {
		t.Fatalf("persisted rows = %d/%d/%d/%d", samples, rollups, dnssec, rpz)
	}
	if _, err := pool.Exec(ctx, "update engines set connection_session=$2 where id=$1", id, uuid.New()); err != nil {
		t.Fatal(err)
	}
	before = state()
	if err := deliver(); status.Code(err) != codes.Aborted {
		t.Fatalf("stale callback = %v", err)
	}
	if calls != 2 || state() != before {
		t.Fatal("stale stream reached callback or changed stats")
	}
}

// A failed TLS write must not suppress delivery by changing the fanout's memory.
func TestTLSResultRollbackKeepsFingerprint(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := uuid.NewString()
	sub := newSubscriber(id, 0)
	if _, err := st.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values ($1,'tls-rollback','tls-rollback',$2)`, id, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	f := NewDNSTLSFanout()
	sub.tlsCh = f.Register(id, "old")
	s := NewServer(st, nil, nil, "", f)
	stream := &controlledStream{ctx: ctx, in: make(chan *controlv1.EngineMessage, 1), reading: make(chan struct{}, 2)}
	// Violates the real fingerprint constraint, after ownership was acquired.
	stream.in <- &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_TlsMaterialResult{TlsMaterialResult: &controlv1.TlsMaterialResult{Applied: true, FingerprintSha256: "invalid"}}}
	close(stream.in)
	if err := s.receive(ctx, stream, sub); status.Code(err) != codes.Internal {
		t.Fatalf("invalid TLS state: %v", err)
	}
	f.Set(&pki.DNSTLSMaterial{FingerprintSHA256: "invalid"})
	select {
	case <-sub.tlsCh:
	default:
		t.Fatal("rolled-back TLS result changed in-memory fingerprint")
	}
}

// The ownership check must retain its row lock until the mutation commits. A
// check-then-mutate implementation would allow this competing claim to succeed.
func TestConnectionFenceSerializesClaimWithMutation(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := uuid.NewString()
	sub := newSubscriber(id, 0)
	if _, err := st.Pool.Exec(ctx, `insert into engines(id,node_name,certificate_serial,connection_session) values ($1,'fence-lock','fence-lock',$2)`, id, sub.sessionID); err != nil {
		t.Fatal(err)
	}
	server := NewServer(st, nil, nil, "", nil)
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := server.connectionFence(ctx, sub)(tx); err != nil {
		t.Fatal(err)
	}
	contender, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Rollback(context.Background())
	if _, err := contender.Exec(ctx, "set local lock_timeout = '100ms'"); err != nil {
		t.Fatal(err)
	}
	_, err = contender.Exec(ctx, "update engines set connection_session=$2 where id=$1", id, uuid.New())
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("claim was not blocked by ownership transaction: %v", err)
	}
	if err := contender.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "update engines set persist_error='fenced' where id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, "update engines set connection_session=$2 where id=$1", id, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if err := server.withConnection(ctx, sub, func(pgx.Tx) error { t.Fatal("stale mutation ran"); return nil }); status.Code(err) != codes.Aborted {
		t.Fatalf("stale mutation: %v", err)
	}
}

func TestLogReplyRollbackDoesNotWakeWaiter(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	broker := NewLogBroker(st)
	id := uuid.NewString()
	waiter := make(chan *controlv1.LogBatch, 1)
	broker.waiters[id] = waiter
	failure := errors.New("abort after log persistence")
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		if err := broker.DeliverTx(ctx, tx, &controlv1.LogBatch{RequestId: id}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("rollback: %v", err)
	}
	select {
	case <-waiter:
		t.Fatal("uncommitted reply woke waiter")
	default:
	}
	var n int
	if err := st.Pool.QueryRow(ctx, "select count(*) from engine_log_replies where request_id=$1", id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rolled-back log reply: %d %v", n, err)
	}
}
