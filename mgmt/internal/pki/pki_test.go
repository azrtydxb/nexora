package pki_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

func TestCAInitLoadAndIssue(t *testing.T) {
	dir := t.TempDir()
	if err := pki.InitCA(dir); err != nil {
		t.Fatal(err)
	}
	if err := pki.InitCA(dir); err == nil {
		t.Fatal("InitCA must refuse to overwrite")
	}
	ca, err := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ca.Fingerprint()) != 64 {
		t.Fatalf("fingerprint %q", ca.Fingerprint())
	}
	srv, err := ca.ServerCertificate([]string{"mgmt.nexora.test", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(srv.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), DNSName: "mgmt.nexora.test"}); err != nil {
		t.Fatalf("server cert does not verify: %v", err)
	}
	if len(leaf.IPAddresses) != 1 {
		t.Fatal("IP SAN missing")
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	der, serial, err := ca.SignEngineCSR(csr, "0b0e7f3c-1111-4222-8333-944455556666", pki.EngineCertValidity)
	if err != nil || serial == "" {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	if cert.Subject.CommonName != "0b0e7f3c-1111-4222-8333-944455556666" {
		t.Fatalf("CN = %q", cert.Subject.CommonName)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("engine cert: %v", err)
	}
	if _, _, err := ca.SignEngineCSR([]byte("garbage"), "x", time.Hour); err == nil {
		t.Fatal("garbage CSR accepted")
	}
}

func TestJoinTokenFormat(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	tok, secret, err := pki.NewJoinToken(fp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "nxj1.") || !strings.HasSuffix(tok, "."+fp) {
		t.Fatalf("token %q", tok)
	}
	s2, fp2, err := pki.ParseJoinToken(tok)
	if err != nil || s2 != secret || fp2 != fp {
		t.Fatalf("parse: %v %v %v", s2, fp2, err)
	}
	for _, bad := range []string{"nxj2.AAAA." + fp, "nxj1..", "nxj1.AAAA.zz", "nxj1.AAAA"} {
		if _, _, err := pki.ParseJoinToken(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if len(pki.HashSecret(secret)) != 32 {
		t.Fatal("hash length")
	}
}
