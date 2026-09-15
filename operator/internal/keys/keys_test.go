package keys_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"regexp"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/piwi3910/nexora/operator/internal/keys"
)

func TestGenerateCA(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	certPEM, keyPEM, err := keys.GenerateCA(now)
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || cb.Type != "CERTIFICATE" || kb == nil || kb.Type != "EC PRIVATE KEY" {
		t.Fatalf("PEM types: %v %v", cb, kb)
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() || !pub.Equal(&key.PublicKey) {
		t.Fatal("certificate key is not the P-256 private key")
	}
	if !cert.IsCA || cert.Subject.CommonName != "Nexora CA" || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("CA fields: IsCA=%v CN=%q usage=%v", cert.IsCA, cert.Subject.CommonName, cert.KeyUsage)
	}
	if got := cert.NotAfter.Sub(now); got < 10*365*24*time.Hour-2*time.Hour || got > 10*366*24*time.Hour {
		t.Fatalf("validity %v", got)
	}
}

func TestGenerateKEK(t *testing.T) {
	kek, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(string(kek))
	if err != nil || len(raw) != 32 {
		t.Fatalf("kek decodes to %d bytes: %v", len(raw), err)
	}
}

func TestGenerateBootstrapToken(t *testing.T) {
	a, err := keys.GenerateBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := keys.GenerateBootstrapToken()
	if !regexp.MustCompile(`^nxt_[A-Z2-7]{52}$`).MatchString(a) || a == b {
		t.Fatalf("tokens %q %q", a, b)
	}
}

func TestEnsureSecretNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "nx-ca"}, Data: map[string][]byte{"ca.crt": []byte("old")}}
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	gen := func() (map[string][]byte, error) {
		return map[string][]byte{"ca.crt": []byte("new"), "ca.key": []byte("new")}, nil
	}
	err := keys.EnsureSecret(ctx, c, types.NamespacedName{Namespace: "ns", Name: "nx-ca"}, nil, []string{"ca.crt", "ca.key"}, gen)
	if !errors.Is(err, keys.ErrSecretIncomplete) {
		t.Fatalf("incomplete secret: %v", err)
	}
	var got corev1.Secret
	_ = c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "nx-ca"}, &got)
	if string(got.Data["ca.crt"]) != "old" || got.Data["ca.key"] != nil {
		t.Fatalf("existing secret modified: %v", got.Data)
	}
	labels := map[string]string{"nexora.io/installation": "nx"}
	if err := keys.EnsureSecret(ctx, c, types.NamespacedName{Namespace: "ns", Name: "nx-kek"}, labels, []string{"kek"},
		func() (map[string][]byte, error) { return map[string][]byte{"kek": []byte("k1")}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := keys.EnsureSecret(ctx, c, types.NamespacedName{Namespace: "ns", Name: "nx-kek"}, labels, []string{"kek"},
		func() (map[string][]byte, error) { return map[string][]byte{"kek": []byte("k2")}, nil }); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "nx-kek"}, &got)
	if string(got.Data["kek"]) != "k1" || got.Labels["nexora.io/installation"] != "nx" || len(got.OwnerReferences) != 0 {
		t.Fatalf("created secret: data=%s labels=%v owners=%v", got.Data["kek"], got.Labels, got.OwnerReferences)
	}
}
