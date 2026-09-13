package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/quic-go/quic-go"
)

func TestEncryptedTransports(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	fx := env.StartDNSFixture()
	fx.SetRecords(t, "example.test. 300 IN A 192.0.2.10", "example.test. 300 IN AAAA 2001:db8::10")
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{DNSTLS: true})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	api.DisableForwardedValidation() // fixture upstreams serve unsigned data under the real root anchor
	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
	eng := env.StartManagedEngineWith("enc-1", []string{mg.GRPCURL}, api.CreateJoinToken(), harness.EngineOptions{DoT: true, DoH: true, DoQ: true})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := harness.EncryptedClient{RootCAs: mg.DNSTLSRoots(), ServerName: "dns.nexora.test"}
	firstFP := mg.DNSTLSFingerprint()
	firstKey := dnsTLSSecrets(t, filepath.Join(mg.DNSTLSDir, "tls.key"))
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		m, _, err := new(dns.Client).Exchange(question("example.test.", dns.TypeA), eng.DNS)
		return err == nil && len(m.Answer) == 1 && m.Answer[0].(*dns.A).A.String() == "192.0.2.10"
	}, "engine forwards to the fixture")

	// engine received the certificate over the control stream
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		_, cs, err := client.DoT(ctx, eng.DoTAddr(), question("example.test.", dns.TypeA))
		return err == nil && harness.Fingerprint(cs) == firstFP
	}, "DoT handshake with the pushed certificate")

	for _, qt := range []uint16{dns.TypeA, dns.TypeAAAA} {
		udp, _, err := new(dns.Client).Exchange(question("example.test.", qt), eng.DNS)
		if err != nil || udp.Rcode != dns.RcodeSuccess || len(udp.Answer) == 0 {
			t.Fatalf("UDP baseline: %v %v", udp, err)
		}
		want := strings.Join(harness.AnswerSet(udp), "|")

		dot, _, err := client.DoT(ctx, eng.DoTAddr(), question("example.test.", qt))
		mustSame(t, "DoT", want, dot, err)

		hc := client.HTTPClient()
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			m, resp, err := harness.DoH(ctx, hc, eng.DoHURL(), method, question("example.test.", qt))
			mustSame(t, "DoH "+method, want, m, err)
			if resp.ProtoMajor != 2 {
				t.Errorf("DoH %s used HTTP/%d, want HTTP/2", method, resp.ProtoMajor)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/dns-message" {
				t.Errorf("DoH %s content-type %q", method, ct)
			}
			if cc := resp.Header.Get("Cache-Control"); !strings.HasPrefix(cc, "max-age=") || cc == "max-age=0" {
				t.Errorf("DoH %s cache-control %q", method, cc)
			}
		}

		doq, _, err := client.DoQ(ctx, eng.DoQAddr(), question("example.test.", qt))
		mustSame(t, "DoQ", want, doq, err)
	}

	t.Run("doh-http2-multiplexing", func(t *testing.T) {
		hc := client.HTTPClient()
		var dials atomic.Int32
		trace := &httptrace.ClientTrace{ConnectStart: func(string, string) { dials.Add(1) }}
		tctx := httptrace.WithClientTrace(ctx, trace)
		if _, _, err := harness.DoH(tctx, hc, eng.DoHURL(), http.MethodPost, question("example.test.", dns.TypeA)); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 50)
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m, _, err := harness.DoH(tctx, hc, eng.DoHURL(), http.MethodPost, question("example.test.", dns.TypeA))
				if err == nil && len(m.Answer) == 0 {
					err = errors.New("empty answer")
				}
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := dials.Load(); n != 1 {
			t.Fatalf("51 DoH requests used %d TCP connections, want 1 (multiplexed)", n)
		}
	})

	t.Run("dot-pipelining", func(t *testing.T) {
		conn, err := client.DialDoT(ctx, eng.DoTAddr())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		ids := map[uint16]bool{}
		for i := 0; i < 10; i++ {
			q := question("example.test.", dns.TypeA)
			ids[q.Id] = true
			if err := conn.WriteMsg(q); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 10; i++ {
			r, err := conn.ReadMsg()
			if err != nil || !ids[r.Id] || len(r.Answer) == 0 {
				t.Fatalf("pipelined reply %d: %v %v", i, r, err)
			}
			delete(ids, r.Id)
		}
	})

	t.Run("doq-stream-per-query-and-protocol-error", func(t *testing.T) {
		conn, err := client.DialDoQ(ctx, eng.DoQAddr())
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if m, err := harness.DoQQuery(ctx, conn, question("example.test.", dns.TypeA)); err != nil || m.Id != 0 || len(m.Answer) == 0 {
					t.Errorf("DoQ concurrent query: %v %v", m, err)
				}
			}()
		}
		wg.Wait()
		q := question("example.test.", dns.TypeA)
		q.Id = 0x1234
		wire, _ := q.Pack()
		framed := append([]byte{byte(len(wire) >> 8), byte(len(wire))}, wire...)
		_, _ = harness.DoQRaw(ctx, conn, framed)
		<-conn.Context().Done()
		var appErr *quic.ApplicationError
		if !errors.As(context.Cause(conn.Context()), &appErr) || appErr.ErrorCode != 0x2 {
			t.Fatalf("non-zero DoQ message id: close cause %v, want application error 0x2", context.Cause(conn.Context()))
		}
	})

	t.Run("certificate-rotation-without-restart", func(t *testing.T) {
		pid := eng.PID()
		old, err := client.DialDoT(ctx, eng.DoTAddr())
		if err != nil {
			t.Fatal(err)
		}
		defer old.Close()
		newFP := mg.RotateDNSTLS(t)
		if newFP == firstFP {
			t.Fatal("rotation produced the same fingerprint")
		}
		harness.EventuallyTrue(t, 45*time.Second, func() bool {
			_, dotCS, err1 := client.DoT(ctx, eng.DoTAddr(), question("example.test.", dns.TypeA))
			_, dohResp, err2 := harness.DoH(ctx, client.HTTPClient(), eng.DoHURL(), http.MethodGet, question("example.test.", dns.TypeA))
			_, doqCS, err3 := client.DoQ(ctx, eng.DoQAddr(), question("example.test.", dns.TypeA))
			return err1 == nil && err2 == nil && err3 == nil &&
				harness.Fingerprint(dotCS) == newFP && harness.Fingerprint(*dohResp.TLS) == newFP && harness.Fingerprint(doqCS) == newFP
		}, "all encrypted transports serve the rotated certificate")
		_ = old.SetDeadline(time.Now().Add(5 * time.Second))
		if err := old.WriteMsg(question("example.test.", dns.TypeA)); err != nil {
			t.Fatalf("connection opened before rotation: %v", err)
		}
		if r, err := old.ReadMsg(); err != nil || len(r.Answer) == 0 {
			t.Fatalf("connection opened before rotation stopped answering: %v %v", r, err)
		}
		if eng.PID() != pid {
			t.Fatal("engine restarted during rotation")
		}
		var status struct {
			Engines []struct {
				Fingerprint string `json:"fingerprint_sha256"`
				Applied     bool   `json:"applied"`
			} `json:"engines"`
		}
		harness.EventuallyTrue(t, 10*time.Second, func() bool {
			api.Do(http.MethodGet, "/settings/dns-tls", nil, &status)
			return len(status.Engines) == 1 && status.Engines[0].Applied && status.Engines[0].Fingerprint == newFP
		}, "status API shows the engine serving the rotated certificate")
	})

	t.Run("no-key-material-on-engine-disk", func(t *testing.T) {
		// Both the initial and the rotated serving keys: neither may reach the engine's disk.
		secrets := append(firstKey, dnsTLSSecrets(t, filepath.Join(mg.DNSTLSDir, "tls.key"))...)
		scanned := 0
		err := filepath.WalkDir(eng.StateDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			scanned++
			if strings.Contains(string(b), "PRIVATE KEY") && !strings.HasPrefix(p, filepath.Join(eng.StateDir, "identity")) {
				t.Errorf("%s contains a PEM private key", p)
			}
			flat := strings.ReplaceAll(string(b), "\n", "")
			for _, secret := range secrets {
				if strings.Contains(flat, secret) {
					t.Errorf("%s contains DNS TLS key material", p)
				}
			}
			return nil
		})
		if err != nil || scanned == 0 {
			t.Fatalf("walk state dir: scanned=%d err=%v (the snapshot file must exist)", scanned, err)
		}
	})

	t.Run("per-transport-metrics", func(t *testing.T) {
		metrics := eng.MetricsText(t)
		for _, want := range []string{
			`nexora_queries_total{transport="dot",rcode="NOERROR"}`,
			`nexora_queries_total{transport="doh",rcode="NOERROR"}`,
			`nexora_queries_total{transport="doq",rcode="NOERROR"}`,
			`nexora_tls_handshakes_total{transport="dot",result="ok"}`,
			`nexora_doh_requests_total{method="GET",status="200"}`,
			`nexora_doq_protocol_errors_total`,
			`nexora_tls_certificate_not_after_seconds`,
		} {
			if !strings.Contains(metrics, want) {
				t.Errorf("metrics missing %s", want)
			}
		}
	})
}

func question(name string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	return m
}

// dnsTLSSecrets returns encodings of the private scalar of a PKCS#8 ECDSA key file: raw, hex,
// base64, and the PEM body span that carries it (every P-256 PKCS#8 key shares its first 36 DER
// bytes, so only a span from the scalar onwards identifies the key).
func dnsTLSSecrets(t *testing.T, keyFile string) []string {
	t.Helper()
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("no PEM block in %s", keyFile)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("%s: %T, want an ECDSA key", keyFile, key)
	}
	d, err := ec.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	at := bytes.Index(block.Bytes, d)
	if at < 0 {
		t.Fatalf("%s: private scalar not found in the DER encoding", keyFile)
	}
	// Base64 characters from the first whole group at or after the scalar, 40 characters long.
	start := (at + 2) / 3 * 4
	body := base64.StdEncoding.EncodeToString(block.Bytes)
	return []string{string(d), hex.EncodeToString(d), base64.StdEncoding.EncodeToString(d), body[start : start+40]}
}

func mustSame(t *testing.T, transport, want string, m *dns.Msg, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", transport, err)
	}
	if got := strings.Join(harness.AnswerSet(m), "|"); got != want {
		t.Fatalf("%s answer %q differs from UDP %q", transport, got, want)
	}
}
