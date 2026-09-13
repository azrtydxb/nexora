package harness

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// dnsTLSNames are the names on the DNS serving certificate the harness issues.
const dnsTLSNames = "dns.nexora.test,127.0.0.1"

// issueDNSTLS runs `nexora-mgmt ca issue-dns` for ca into dir (tls.crt, tls.key).
func (e *Env) issueDNSTLS(t *testing.T, ca *CA, dir string) {
	t.Helper()
	// The binary path comes from Bin (the harness's own build output directories).
	out, err := exec.Command(e.Bin("nexora-mgmt"), "ca", "issue-dns", "--ca-cert", ca.CertFile, "--ca-key", ca.KeyFile, // nosemgrep: dangerous-exec-command
		"--names", dnsTLSNames, "--days", "30", "--out", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("nexora-mgmt ca issue-dns: %v\n%s", err, out)
	}
}

// DNSTLSRoots returns a pool holding the CA that signed the DNS serving certificate.
func (m *Mgmt) DNSTLSRoots() *x509.CertPool { return m.roots }

// caPool loads ca's certificate into a new pool.
func caPool(t *testing.T, ca *CA) *x509.CertPool {
	t.Helper()
	pemData, err := os.ReadFile(ca.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		t.Fatalf("no certificate in %s", ca.CertFile)
	}
	return pool
}

// DNSTLSFingerprint is the hex SHA-256 of the leaf certificate currently in DNSTLSDir.
func (m *Mgmt) DNSTLSFingerprint() string {
	m.env.T.Helper()
	return leafFingerprint(m.env.T, filepath.Join(m.DNSTLSDir, "tls.crt"))
}

// RotateDNSTLS issues a fresh certificate and moves it into DNSTLSDir (key first, then
// certificate: the management plane keeps the old pair while the files disagree) and returns the
// new leaf fingerprint.
func (m *Mgmt) RotateDNSTLS(t *testing.T) string {
	t.Helper()
	next := filepath.Join(m.env.Dir, "dnstls-next")
	if err := os.RemoveAll(next); err != nil {
		t.Fatal(err)
	}
	m.env.issueDNSTLS(t, m.ca, next)
	for _, f := range []string{"tls.key", "tls.crt"} {
		if err := os.Rename(filepath.Join(next, f), filepath.Join(m.DNSTLSDir, f)); err != nil {
			t.Fatal(err)
		}
	}
	return leafFingerprint(t, filepath.Join(m.DNSTLSDir, "tls.crt"))
}

func leafFingerprint(t *testing.T, certFile string) string {
	t.Helper()
	data, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("no PEM certificate in %s", certFile)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}
