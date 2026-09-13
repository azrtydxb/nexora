package secrets_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/secrets"
)

func writeKEK(t *testing.T, n int) string {
	t.Helper()
	key := make([]byte, n)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvelopeRoundTripBindsPurposeAndDetectsTamper(t *testing.T) {
	box, err := secrets.LoadKEKFile(writeKEK(t, 32))
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("super-secret-tsig-bytes")
	env, err := box.Seal("nexora/rpz-tsig/v1:z1", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, secret) || string(env[:4]) != "NXE1" || env[4] != secrets.WrapFileKEK || len(env) != 85+len(secret)+16 {
		t.Fatalf("envelope layout wrong: %x", env[:13])
	}
	got, err := box.Unseal("nexora/rpz-tsig/v1:z1", env)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("unseal: %q %v", got, err)
	}
	if _, err := box.Unseal("nexora/rpz-tsig/v1:z2", env); err == nil {
		t.Fatal("purpose is not bound to the ciphertext")
	}
	env[len(env)-1] ^= 1
	if _, err := box.Unseal("nexora/rpz-tsig/v1:z1", env); err == nil {
		t.Fatal("tampering not detected")
	}
}

func TestWrongKEKIsReported(t *testing.T) {
	a, _ := secrets.LoadKEKFile(writeKEK(t, 32))
	b, _ := secrets.LoadKEKFile(writeKEK(t, 32))
	env, err := a.Seal("p", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Unseal("p", env); !errors.Is(err, secrets.ErrKEKMismatch) {
		t.Fatalf("got %v, want ErrKEKMismatch", err)
	}
	env[4] = secrets.WrapPKCS11
	if _, err := a.Unseal("p", env); !errors.Is(err, secrets.ErrBackendUnavailable) {
		t.Fatalf("got %v, want ErrBackendUnavailable for an HSM-wrapped envelope", err)
	}
}

func TestUnconfiguredBoxRefusesSecrets(t *testing.T) {
	box, err := secrets.LoadKEKFile("")
	if err != nil {
		t.Fatal(err)
	}
	if box.Configured() {
		t.Fatal("empty path reports configured")
	}
	if _, err := box.Seal("p", []byte("x")); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("Seal: %v", err)
	}
	var none *secrets.Box
	if none.Configured() {
		t.Fatal("nil box reports configured")
	}
	if _, err := none.Seal("p", []byte("x")); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("nil Seal: %v", err)
	}
}

func TestKEKFileValidation(t *testing.T) {
	if _, err := secrets.LoadKEKFile(writeKEK(t, 16)); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("16-byte KEK: %v", err)
	}
	if _, err := secrets.LoadKEKFile(filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "NEXORA_KEK_FILE") {
		t.Fatalf("missing KEK file: %v", err)
	}
}

func TestKEKFilePermissions(t *testing.T) {
	p := writeKEK(t, 32)
	if _, err := secrets.Open(secrets.Config{KEKFile: p}); err != nil {
		t.Fatalf("0600 KEK file refused: %v", err)
	}
	// Group read is accepted only for the process's own group (Kubernetes fsGroup secret volumes).
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(p, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Open(secrets.Config{KEKFile: p}); err != nil {
		t.Fatalf("0640 KEK file owned by the process group refused: %v", err)
	}
	for _, mode := range []os.FileMode{0o604, 0o644, 0o660, 0o602} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := secrets.Open(secrets.Config{KEKFile: p}); err == nil || !strings.Contains(err.Error(), "NEXORA_KEK_FILE") {
			t.Fatalf("mode %o KEK file: %v", mode, err)
		}
	}
	if os.Geteuid() == 0 { // only root can hand the file to a foreign group
		if err := os.Chmod(p, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(p, -1, os.Getegid()+1); err != nil {
			t.Fatal(err)
		}
		if _, err := secrets.Open(secrets.Config{KEKFile: p}); err == nil {
			t.Fatal("KEK file readable by a foreign group accepted")
		}
	}
}
