package secrets_test

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
)

func softhsmConfig(t *testing.T) secrets.Config {
	t.Helper()
	tok := harness.InitSoftHSM(t, "nexora-test")
	return secrets.Config{PKCS11Module: tok.Module, PKCS11TokenLabel: tok.Label, PKCS11PinFile: tok.PinFile}
}

func verifySignerMatchesDNSKEY(t *testing.T, box *secrets.Box, k secrets.StoredKey) {
	t.Helper()
	signer, release, err := box.Signer(k)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	key := &dns.DNSKEY{Hdr: dns.RR_Header{Name: "keys.test.", Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 300},
		Flags: 257, Protocol: 3, Algorithm: k.Algorithm, PublicKey: k.PublicKey}
	a := &dns.A{Hdr: dns.RR_Header{Name: "www.keys.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: net.ParseIP("192.0.2.1")}
	sig := &dns.RRSIG{KeyTag: key.KeyTag(), SignerName: "keys.test.", Algorithm: k.Algorithm,
		Inception: uint32(time.Now().Add(-time.Hour).Unix()), Expiration: uint32(time.Now().Add(time.Hour).Unix())}
	if err := sig.Sign(signer, []dns.RR{a}); err != nil {
		t.Fatalf("sign alg %d: %v", k.Algorithm, err)
	}
	if err := sig.Verify(key, []dns.RR{a}); err != nil {
		t.Fatalf("verify alg %d: %v", k.Algorithm, err)
	}
}

func TestPKCS11SigningKeysStayInToken(t *testing.T) {
	box, err := secrets.Open(softhsmConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	for _, alg := range []uint8{13, 8} {
		k, err := box.GenerateSigningKey(t.Context(), secrets.BackendPKCS11, alg)
		if err != nil {
			t.Fatal(err)
		}
		if k.Envelope != nil {
			t.Fatalf("alg %d: PKCS#11 key has an envelope", alg)
		}
		verifySignerMatchesDNSKEY(t, box, k)
		extractable, sensitive, err := box.PKCS11KeyAttributes(k.KeyRef)
		if err != nil || extractable || !sensitive {
			t.Fatalf("alg %d: extractable=%v sensitive=%v err=%v", alg, extractable, sensitive, err)
		}
		if err := box.DestroySigningKey(k); err != nil {
			t.Fatal(err)
		}
		if _, _, err := box.PKCS11KeyAttributes(k.KeyRef); err == nil {
			t.Fatalf("alg %d: key still present after destroy", alg)
		}
	}
}

func TestPKCS11WrapsEnvelopesWhenNoKEKFile(t *testing.T) {
	box, err := secrets.Open(softhsmConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	if !box.Configured() || box.DefaultBackend() != secrets.BackendPKCS11 {
		t.Fatal("a PKCS#11-only box must be configured with the pkcs11 default backend")
	}
	if err := box.EnsureHSMWrapKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := box.EnsureHSMWrapKey(t.Context()); err != nil {
		t.Fatalf("second EnsureHSMWrapKey must reuse the key: %v", err)
	}
	extractable, sensitive, err := box.PKCS11WrapKeyAttributes()
	if err != nil || extractable || !sensitive {
		t.Fatalf("wrap key: extractable=%v sensitive=%v err=%v", extractable, sensitive, err)
	}
	purpose := secrets.TSIGPurpose([16]byte{1}, "x.", "hmac-sha256")
	env, err := box.Seal(purpose, []byte("hsm-wrapped"))
	if err != nil || env[4] != secrets.WrapPKCS11 {
		t.Fatalf("seal: %v", err)
	}
	got, err := box.Unseal(purpose, env)
	if err != nil || string(got) != "hsm-wrapped" {
		t.Fatalf("unseal: %q %v", got, err)
	}
	if _, err := box.Unseal(secrets.TSIGPurpose([16]byte{2}, "x.", "hmac-sha256"), env); err == nil {
		t.Fatal("an HSM-wrapped envelope opened for another row")
	}
	kek, err := secrets.Open(secrets.Config{KEKFile: writeKEK(t, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kek.Unseal(purpose, env); !errors.Is(err, secrets.ErrBackendUnavailable) {
		t.Fatalf("HSM envelope on a KEK-only box: %v", err)
	}
}

func TestPKCS11PinFileIsRequiredAndChecked(t *testing.T) {
	cfg := softhsmConfig(t)
	if err := os.Chmod(cfg.PKCS11PinFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Open(cfg); err == nil || !strings.Contains(err.Error(), "NEXORA_PKCS11_PIN_FILE") {
		t.Fatalf("world-readable PIN file: %v", err)
	}
	if err := os.Chmod(cfg.PKCS11PinFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.PKCS11PinFile, []byte("000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Open(cfg); err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("wrong PIN: %v", err)
	}
}
