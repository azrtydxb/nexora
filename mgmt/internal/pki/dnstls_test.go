package pki_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

func testCA(t *testing.T) *pki.CA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &pki.CA{Cert: cert, Key: key}
}

func write(t *testing.T, dir string, chain, key []byte) (string, string) {
	t.Helper()
	c, k := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(c, chain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(k, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return c, k
}

func TestIssueAndLoadDNSTLS(t *testing.T) {
	ca := testCA(t)
	now := time.Now()
	chain, key, err := ca.IssueDNSServerCert([]string{"dns.nexora.test", "127.0.0.1"}, 90*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	c, k := write(t, t.TempDir(), chain, key)
	m, err := pki.LoadDNSTLS(c, k, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.FingerprintSHA256) != 64 || m.DNSNames[0] != "dns.nexora.test" || m.IPAddresses[0] != "127.0.0.1" {
		t.Fatalf("material = %+v", m)
	}
	if !m.NotAfter.After(now.Add(89 * 24 * time.Hour)) {
		t.Fatalf("not after = %v", m.NotAfter)
	}
	_, other, _ := ca.IssueDNSServerCert([]string{"dns.nexora.test"}, time.Hour, now)
	c2, k2 := write(t, t.TempDir(), chain, other)
	if _, err := pki.LoadDNSTLS(c2, k2, now); err == nil || !strings.Contains(err.Error(), "private key does not match certificate") {
		t.Fatalf("mismatch err = %v", err)
	}
	if _, err := pki.LoadDNSTLS(c, k, now.Add(91*24*time.Hour)); err == nil || !strings.Contains(err.Error(), "certificate expired") {
		t.Fatalf("expired err = %v", err)
	}
}

func TestDNSTLSWatcherRotationKeepsLastGood(t *testing.T) {
	ca := testCA(t)
	dir := t.TempDir()
	chain, key, _ := ca.IssueDNSServerCert([]string{"dns.nexora.test"}, time.Hour, time.Now())
	c, k := write(t, dir, chain, key)
	w := pki.NewDNSTLSWatcher(c, k, 20*time.Millisecond)
	var changes atomic.Int32
	var last atomic.Value
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, func(m *pki.DNSTLSMaterial) { changes.Add(1); last.Store(m.FingerprintSHA256) })
	waitFor(t, func() bool { return changes.Load() == 1 })
	first := w.Current().FingerprintSHA256

	if err := os.WriteFile(k, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if w.Current().FingerprintSHA256 != first || changes.Load() != 1 {
		t.Fatalf("broken files must keep the last good material")
	}
	chain2, key2, _ := ca.IssueDNSServerCert([]string{"dns.nexora.test"}, time.Hour, time.Now())
	write(t, dir, chain2, key2)
	waitFor(t, func() bool { return changes.Load() == 2 })
	if last.Load().(string) == first {
		t.Fatalf("rotation did not change fingerprint")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 3s")
}
