package control_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
)

func recvMsg(t *testing.T, s controlv1.EngineControl_ConnectClient, within time.Duration) (*controlv1.ServerMessage, error) {
	t.Helper()
	type res struct {
		m   *controlv1.ServerMessage
		err error
	}
	ch := make(chan res, 1)
	go func() { m, err := s.Recv(); ch <- res{m, err} }()
	select {
	case r := <-ch:
		return r.m, r.err
	case <-time.After(within):
		t.Fatalf("no server message within %s", within)
	}
	return nil, nil
}

func csrFor(t *testing.T, cn string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

func blobCode(c controlv1.EngineControlClient, f *fixture) codes.Code {
	_, err := mustRecvErr(c, f.ctx)
	return status.Code(err)
}

// certIssuedWithin reports whether a CertificateIssued arrives on s within d (other messages are skipped).
func certIssuedWithin(s controlv1.EngineControl_ConnectClient, d time.Duration) bool {
	got := make(chan bool, 1)
	go func() {
		for {
			m, err := s.Recv()
			if err != nil {
				got <- false
				return
			}
			if m.GetCertIssued() != nil {
				got <- true
				return
			}
		}
	}()
	select {
	case ok := <-got:
		return ok
	case <-time.After(d):
		return false
	}
}

// Catches: the issuance limit kept per stream (a reconnect or a second stream bypasses it) or
// checked outside the engine row lock (concurrent streams both issue).
func TestCertificateIssuanceRateLimitIsPerEngine(t *testing.T) {
	f := setupServers(t, 1, nil, func(s *control.Server) { s.EngineCertTTL = time.Hour })
	client, id := f.enroll(t, f.addr[0])
	connect := func() (controlv1.EngineControl_ConnectClient, context.CancelFunc) {
		ctx, cancel := context.WithCancel(f.ctx)
		s, err := client.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "rate-1"}}})
		return s, cancel
	}
	request := func(s controlv1.EngineControl_ConnectClient) {
		csr, _ := csrFor(t, id)
		_ = s.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{
			CsrDer: csr, Reason: controlv1.CertificateRequest_REASON_RENEWAL}}})
	}
	issued := func() int {
		var n int
		if err := f.st.Pool.QueryRow(f.ctx, "select count(*) from engine_certificates where engine_id = $1", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n - 1 // minus the enrollment certificate
	}

	first, cancelFirst := connect()
	request(first)
	if !certIssuedWithin(first, 3*time.Second) {
		t.Fatal("first renewal not issued")
	}
	cancelFirst()

	again, cancelAgain := connect()
	defer cancelAgain()
	request(again)
	if certIssuedWithin(again, 2*time.Second) {
		t.Fatal("a reconnected stream got a second certificate within the renewal interval")
	}

	// Two streams at once, after the interval has passed: exactly one issuance.
	if _, err := f.st.Pool.Exec(f.ctx, "update engines set cert_renewed_at = now() - interval '11 seconds' where id = $1", id); err != nil {
		t.Fatal(err)
	}
	a, cancelA := connect()
	defer cancelA()
	b, cancelB := connect()
	defer cancelB()
	request(a)
	request(b)
	results := make(chan bool, 2)
	go func() { results <- certIssuedWithin(a, 3*time.Second) }()
	go func() { results <- certIssuedWithin(b, 3*time.Second) }()
	if n := boolCount(<-results, <-results); n > 1 {
		t.Fatalf("concurrent streams got %d certificates, want at most 1", n)
	}
	if n := issued(); n != 2 {
		t.Fatalf("renewal certificates recorded = %d, want 2", n)
	}
}

func boolCount(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

func TestCertificateRenewalRotationAndRevocation(t *testing.T) {
	f := setupServers(t, 1, nil, func(s *control.Server) { s.EngineCertTTL = time.Hour })
	oldClient, id := f.enroll(t, f.addr[0])
	var oldSerial string
	if err := f.st.Pool.QueryRow(f.ctx, `select certificate_serial from engines where id = $1`, id).Scan(&oldSerial); err != nil {
		t.Fatal(err)
	}
	if c := blobCode(oldClient, f); c != codes.NotFound {
		t.Fatalf("enrolled engine GetBlob -> %v, want NotFound (authenticated)", c)
	}
	stream, _ := oldClient.Connect(f.ctx)
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "life-1"}}})
	if m, err := recvMsg(t, stream, 3*time.Second); err != nil || m.GetSnapshot() == nil {
		t.Fatalf("initial snapshot: %v %v", m, err)
	}

	if err := f.st.InTx(f.ctx, func(tx pgx.Tx) error { return fleet.RequestRotation(f.ctx, tx, uuid.MustParse(id)) }); err != nil {
		t.Fatal(err)
	}
	if m, err := recvMsg(t, stream, 3*time.Second); err != nil || m.GetRenewCertificate().GetReason() != controlv1.CertificateRequest_REASON_ROTATE {
		t.Fatalf("rotation request: %v %v", m, err)
	}
	foreign, _ := csrFor(t, uuid.NewString())
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{CsrDer: foreign, Reason: controlv1.CertificateRequest_REASON_ROTATE}}})
	csr, key := csrFor(t, id)
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{CsrDer: csr, Reason: controlv1.CertificateRequest_REASON_ROTATE}}})
	m, err := recvMsg(t, stream, 3*time.Second)
	if err != nil || m.GetCertIssued() == nil {
		t.Fatalf("certificate issued: %v %v (a CSR with a foreign CN must get no answer)", m, err)
	}
	cert, err := x509.ParseCertificate(m.GetCertIssued().CertDer)
	if err != nil || cert.Subject.CommonName != id || cert.SerialNumber.Text(16) == oldSerial || time.Until(cert.NotAfter) > time.Hour+time.Minute {
		t.Fatalf("issued certificate %v err %v", cert, err)
	}
	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "cert_rotate_requested_at is null", true) })

	conn, _ := grpc.NewClient(f.addr[0], grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: f.ca.Pool(), ServerName: "127.0.0.1",
		Certificates: []tls.Certificate{{Certificate: [][]byte{m.GetCertIssued().CertDer}, PrivateKey: key}}})))
	t.Cleanup(func() { conn.Close() })
	newClient := controlv1.NewEngineControlClient(conn)
	renewed, _ := newClient.Connect(f.ctx)
	_ = renewed.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "life-1", AppliedVersion: 1}}})
	harness.Eventually(t, 3*time.Second, func() error {
		return expectRow(f, "select revoke_reason from engine_certificates where serial = '"+oldSerial+"'", "superseded")
	})
	if c := blobCode(oldClient, f); c != codes.PermissionDenied {
		t.Fatalf("superseded certificate GetBlob -> %v, want PermissionDenied", c)
	}
	if c := blobCode(newClient, f); c != codes.NotFound {
		t.Fatalf("renewed certificate GetBlob -> %v, want NotFound", c)
	}

	if err := f.st.InTx(f.ctx, func(tx pgx.Tx) error { return fleet.RevokeEngine(f.ctx, tx, uuid.MustParse(id)) }); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := recvMsg(t, renewed, 3*time.Second)
		if err == nil {
			continue
		}
		if s, _ := status.FromError(err); s.Code() != codes.PermissionDenied || s.Message() != "certificate revoked" {
			t.Fatalf("stream end after revocation: %v", err)
		}
		break
	}
	if c := blobCode(newClient, f); c != codes.PermissionDenied {
		t.Fatalf("revoked engine GetBlob -> %v, want PermissionDenied", c)
	}
}
