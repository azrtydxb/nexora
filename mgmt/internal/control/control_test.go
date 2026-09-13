package control_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

type fixture struct {
	st   *store.Store
	ca   *pki.CA
	addr []string
	ctx  context.Context
}

func setup(t *testing.T, instances int) *fixture {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := pki.InitCA(dir); err != nil {
		t.Fatal(err)
	}
	ca, _ := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{st: st, ca: ca, ctx: ctx}
	for i := 0; i < instances; i++ {
		id := control.NewInstanceID()
		go control.RunInstanceHeartbeat(ctx, st, id)
		hub := control.NewHub(st, id)
		go func() { _ = hub.Run(ctx) }()
		tlsCfg, err := control.TLSConfig(ca, []string{"127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
		controlv1.RegisterEngineControlServer(srv, control.NewServer(st, ca, hub, id))
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		go func() { _ = srv.Serve(l) }()
		t.Cleanup(srv.Stop)
		f.addr = append(f.addr, l.Addr().String())
	}
	return f
}

func (f *fixture) enroll(t *testing.T, addr string) (controlv1.EngineControlClient, string) {
	var token string
	err := f.st.InTx(f.ctx, func(tx pgx.Tx) error {
		var e error
		_, token, e = control.CreateJoinToken(f.ctx, tx, f.ca, "test", "admin", time.Hour)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	secret, fp, _ := pki.ParseJoinToken(token)
	if fp != f.ca.Fingerprint() {
		t.Fatal("token fingerprint mismatch")
	}
	plain, _ := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: f.ca.Pool(), ServerName: "127.0.0.1"})))
	defer plain.Close()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if _, err := controlv1.NewEngineControlClient(plain).Enroll(f.ctx, &controlv1.EnrollRequest{JoinSecret: "WRONG", NodeName: "e1", CsrDer: csr}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bad secret -> %v", err)
	}
	resp, err := controlv1.NewEngineControlClient(plain).Enroll(f.ctx, &controlv1.EnrollRequest{JoinSecret: secret, NodeName: "e1", CsrDer: csr, EngineVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{resp.CertificateDer}, PrivateKey: key}
	conn, _ := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: f.ca.Pool(), ServerName: "127.0.0.1", Certificates: []tls.Certificate{cert}})))
	t.Cleanup(func() { conn.Close() })
	return controlv1.NewEngineControlClient(conn), resp.EngineId
}

func recvSnapshot(t *testing.T, s controlv1.EngineControl_ConnectClient) *controlv1.ConfigSnapshot {
	t.Helper()
	ch := make(chan *controlv1.ServerMessage, 1)
	go func() { m, _ := s.Recv(); ch <- m }()
	select {
	case m := <-ch:
		if m.GetSnapshot() == nil {
			t.Fatalf("expected snapshot, got %v", m)
		}
		return m.GetSnapshot()
	case <-time.After(3 * time.Second):
		t.Fatal("no snapshot within 3s")
	}
	return nil
}

func TestEnrollConnectPushAckReject(t *testing.T) {
	f := setup(t, 1)
	client, id := f.enroll(t, f.addr[0])
	stream, err := client.Connect(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "e1", AppliedVersion: 0}}})
	if s := recvSnapshot(t, stream); s.Version != 1 {
		t.Fatalf("initial version %d", s.Version)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: 1}}})
	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "applied_version", int64(1)) })

	_, err = snapshot.Mutate(f.ctx, f.st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(f.ctx, "update resolver_settings set block_ttl=5")
		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := recvSnapshot(t, stream); s.Version != 2 || s.Filter.BlockTtl != 5 {
		t.Fatalf("pushed snapshot %v", s)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Rejected{Rejected: &controlv1.Rejected{Version: 2, Reason: "invalid snapshot: nope"}}})
	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "rejected_reason", "invalid snapshot: nope") })
}

func TestVersionAheadIsFlaggedNotDowngraded(t *testing.T) {
	f := setup(t, 1)
	client, id := f.enroll(t, f.addr[0])
	stream, _ := client.Connect(f.ctx)
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, AppliedVersion: 99}}})
	m, err := stream.Recv()
	if err != nil || m.GetVersionAhead() == nil || m.GetVersionAhead().ServerVersion != 1 {
		t.Fatalf("expected VersionAhead, got %v %v", m, err)
	}
	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "version_ahead", true) })
}

func TestConnectWithoutClientCertIsUnauthenticated(t *testing.T) {
	f := setup(t, 1)
	conn, _ := grpc.NewClient(f.addr[0], grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: f.ca.Pool(), ServerName: "127.0.0.1"})))
	defer conn.Close()
	s, err := controlv1.NewEngineControlClient(conn).Connect(f.ctx)
	if err == nil {
		_ = s.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{}}})
		_, err = s.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v", err)
	}
}

func TestGetBlobStreamsMiBChunks(t *testing.T) {
	f := setup(t, 1)
	client, _ := f.enroll(t, f.addr[0])
	data := make([]byte, 2*control.BlobChunkSize+12345)
	_, _ = rand.Read(data)
	sha := sha256Hex(data)
	if _, err := f.st.Pool.Exec(f.ctx, "insert into blobs(sha256,size,data) values ($1,$2,$3)", sha, len(data), data); err != nil {
		t.Fatal(err)
	}
	s, err := client.GetBlob(f.ctx, &controlv1.GetBlobRequest{Sha256: sha})
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	chunks := 0
	for {
		c, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		chunks++
		got = append(got, c.Data...)
	}
	if chunks != 3 || sha256Hex(got) != sha {
		t.Fatalf("chunks=%d match=%v", chunks, sha256Hex(got) == sha)
	}
	if _, err := mustRecvErr(client, f.ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown blob -> %v", err)
	}
}

func TestNotifyFansOutToEngineOnOtherInstance(t *testing.T) {
	f := setup(t, 2)
	client, id := f.enroll(t, f.addr[1])
	stream, _ := client.Connect(f.ctx)
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, AppliedVersion: 1}}})
	time.Sleep(300 * time.Millisecond)
	v, err := snapshot.Mutate(f.ctx, f.st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(f.ctx, "update resolver_settings set block_ttl=9")
		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := recvSnapshot(t, stream); s.Version != v {
		t.Fatalf("version %d, want %d", s.Version, v)
	}
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func expectEngine(f *fixture, id, column string, want any) error {
	var got any
	if err := f.st.Pool.QueryRow(f.ctx, "select "+column+" from engines where id=$1", id).Scan(&got); err != nil {
		return err
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("%s = %v, want %v", column, got, want)
	}
	return nil
}

func mustRecvErr(c controlv1.EngineControlClient, ctx context.Context) (*controlv1.BlobChunk, error) {
	s, err := c.GetBlob(ctx, &controlv1.GetBlobRequest{Sha256: sha256Hex([]byte("nope"))})
	if err != nil {
		return nil, err
	}
	return s.Recv()
}
