package secrets_test

import (
	"crypto/x509"
	"errors"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/secrets"
)

func TestOpenValidatesBackends(t *testing.T) {
	box, err := secrets.Open(secrets.Config{})
	if err != nil || box.Configured() {
		t.Fatalf("empty config: configured=%v err=%v", box.Configured(), err)
	}
	if _, err := box.GenerateSigningKey(t.Context(), secrets.BackendKEK, 13); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("GenerateSigningKey without key storage: %v", err)
	}
	if _, err := secrets.Open(secrets.Config{PKCS11Module: "/usr/lib/softhsm/libsofthsm2.so"}); err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("partial PKCS#11 configuration: %v", err)
	}
	kek, err := secrets.Open(secrets.Config{KEKFile: writeKEK(t, 32)})
	if err != nil || !kek.HasBackend(secrets.BackendKEK) || kek.HasBackend(secrets.BackendPKCS11) || kek.DefaultBackend() != secrets.BackendKEK {
		t.Fatalf("file KEK backends: %v", err)
	}
	if _, err := kek.GenerateSigningKey(t.Context(), secrets.BackendPKCS11, 13); !errors.Is(err, secrets.ErrBackendUnavailable) {
		t.Fatalf("PKCS#11 key without a token: %v", err)
	}
}

func TestKEKSigningKeyIsEnvelopedPKCS8(t *testing.T) {
	box, err := secrets.Open(secrets.Config{KEKFile: writeKEK(t, 32)})
	if err != nil {
		t.Fatal(err)
	}
	var keys []secrets.StoredKey
	for _, alg := range []uint8{13, 8} {
		k, err := box.GenerateSigningKey(t.Context(), secrets.BackendKEK, alg)
		if err != nil {
			t.Fatal(err)
		}
		if len(k.KeyRef) != 16 || k.Envelope == nil || k.PublicKey == "" {
			t.Fatalf("alg %d: %+v", alg, k)
		}
		der, err := box.Unseal(secrets.SigningKeyPurpose(k.KeyRef), k.Envelope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := x509.ParsePKCS8PrivateKey(der); err != nil {
			t.Fatalf("alg %d: envelope does not hold PKCS#8: %v", alg, err)
		}
		verifySignerMatchesDNSKEY(t, box, k)
		keys = append(keys, k)
	}
	// Swap attack: an envelope copied onto another key row (another key_ref) must not open.
	swapped := keys[0]
	swapped.Envelope = keys[1].Envelope
	if _, _, err := box.Signer(swapped); err == nil {
		t.Fatal("a signing key envelope opened under another key's key_ref")
	}
	if _, _, err := box.Signer(secrets.StoredKey{Backend: secrets.BackendKEK, Algorithm: 15, KeyRef: keys[0].KeyRef, Envelope: keys[0].Envelope}); err == nil {
		t.Fatal("a key whose stored algorithm does not match its private key was accepted")
	}
}
