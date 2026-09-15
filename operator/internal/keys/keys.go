// Package keys generates the key material the operator stores in Secrets: the internal CA, the KEK and
// the bootstrap token. A Secret that exists is never modified.
package keys

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrSecretIncomplete reports a Secret that lacks a required key.
var ErrSecretIncomplete = errors.New("secret incomplete")

// caValidity matches pki.InitCA in the management plane.
const caValidity = 10 * 365 * 24 * time.Hour

// GenerateCA returns a PEM "CERTIFICATE" and "EC PRIVATE KEY" (ECDSA P-256, CN "Nexora CA"), as
// pki.InitCA writes them.
func GenerateCA(now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Nexora CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// GenerateKEK returns base64 (standard, padded) of 32 random bytes, without a newline.
func GenerateKEK() ([]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	out := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
	base64.StdEncoding.Encode(out, raw)
	return out, nil
}

// GenerateBootstrapToken returns "nxt_" followed by unpadded standard base32 of 32 random bytes.
func GenerateBootstrapToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "nxt_" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// EnsureSecret creates the Secret key (type Opaque, labels, no owner reference) from gen when it does
// not exist. An existing Secret is never modified; it must hold every name in required.
func EnsureSecret(ctx context.Context, c client.Client, key types.NamespacedName, labels map[string]string, required []string, gen func() (map[string][]byte, error)) error {
	err := CheckSecret(ctx, c, key, required)
	if !apierrors.IsNotFound(err) {
		return err
	}
	data, err := gen()
	if err != nil {
		return fmt.Errorf("generate secret %s: %w", key.Name, err)
	}
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, Labels: labels},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
	if err := c.Create(ctx, s); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Created concurrently: verify what is there instead of overwriting it.
			return CheckSecret(ctx, c, key, required)
		}
		return err
	}
	return nil
}

// CheckSecret verifies a Secret exists and holds every name in required. Not found is returned as is.
func CheckSecret(ctx context.Context, c client.Client, key types.NamespacedName, required []string) error {
	var s corev1.Secret
	if err := c.Get(ctx, key, &s); err != nil {
		return err
	}
	for _, k := range required {
		if _, ok := s.Data[k]; !ok {
			return fmt.Errorf("secret %s lacks key %s: %w", key.Name, k, ErrSecretIncomplete)
		}
	}
	return nil
}
