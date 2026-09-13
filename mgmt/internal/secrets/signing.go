package secrets

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
)

// DNSSEC algorithms with signing keys (RFC 8624 MUST/RECOMMENDED for signing).
const (
	algRSASHA256       uint8 = 8
	algECDSAP256SHA256 uint8 = 13
)

// StoredKey is a DNSSEC signing key as the database keeps it: KeyRef identifies it (the PKCS#11
// CKA_ID, and the envelope purpose of a KEK key), Envelope holds the sealed PKCS#8 private key
// (KEK backend only) and PublicKey is the DNSKEY public key field in base64.
type StoredKey struct {
	Backend   Backend
	Algorithm uint8
	KeyRef    []byte
	Envelope  []byte
	PublicKey string
}

// EnsureHSMWrapKey creates the token's AES wrap key "nexora-kek" unless it exists (no-op without
// a token). Instances must serialise calls; see main.go.
func (b *Box) EnsureHSMWrapKey(ctx context.Context) error {
	if !b.HasBackend(BackendPKCS11) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.hsm.ensureWrapKey()
}

// GenerateSigningKey creates a signing key of alg (13: ECDSA P-256, 8: RSA-2048) in backend be.
func (b *Box) GenerateSigningKey(ctx context.Context, be Backend, alg uint8) (StoredKey, error) {
	if !b.Configured() {
		return StoredKey{}, ErrUnconfigured
	}
	if !b.HasBackend(be) {
		return StoredKey{}, ErrBackendUnavailable
	}
	if alg != algECDSAP256SHA256 && alg != algRSASHA256 {
		return StoredKey{}, fmt.Errorf("unsupported DNSSEC algorithm %d", alg)
	}
	if err := ctx.Err(); err != nil {
		return StoredKey{}, err
	}
	k := StoredKey{Backend: be, Algorithm: alg, KeyRef: make([]byte, 16)}
	if _, err := rand.Read(k.KeyRef); err != nil {
		return StoredKey{}, err
	}
	if be == BackendPKCS11 {
		pub, err := b.hsm.generate(alg, k.KeyRef)
		if err != nil {
			return StoredKey{}, fmt.Errorf("pkcs11 generate: %w", err)
		}
		k.PublicKey = pub
		return k, nil
	}
	var priv crypto.Signer
	switch alg {
	case algECDSAP256SHA256:
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return StoredKey{}, err
		}
		priv = key
	case algRSASHA256:
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return StoredKey{}, err
		}
		priv = key
	}
	defer zeroPrivate(priv)
	pub, err := dnskeyPublic(priv.Public())
	if err != nil {
		return StoredKey{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return StoredKey{}, err
	}
	defer clear(der)
	if k.Envelope, err = b.Seal(SigningKeyPurpose(k.KeyRef), der); err != nil {
		return StoredKey{}, err
	}
	k.PublicKey = pub
	return k, nil
}

// Signer returns a signer for k. The release func zeroes decrypted key material (best effort: Go
// may keep internal copies of parsed keys) and must be called when signing is done.
func (b *Box) Signer(k StoredKey) (crypto.Signer, func(), error) {
	pub, err := parseDNSKEYPublic(k.Algorithm, k.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	if k.Backend == BackendPKCS11 {
		if !b.HasBackend(BackendPKCS11) {
			return nil, nil, ErrBackendUnavailable
		}
		return b.hsm.signer(k.Algorithm, k.KeyRef, pub), func() {}, nil
	}
	if k.Backend != BackendKEK {
		return nil, nil, fmt.Errorf("unknown key backend %q", k.Backend)
	}
	der, err := b.Unseal(SigningKeyPurpose(k.KeyRef), k.Envelope)
	if err != nil {
		return nil, nil, err
	}
	defer clear(der)
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, nil, errors.New("signing key envelope does not hold a PKCS#8 private key")
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("signing key is not a signer")
	}
	release := func() { zeroPrivate(signer) }
	// The stored algorithm and public key must describe the sealed private key.
	if got, err := dnskeyPublic(signer.Public()); err != nil || got != k.PublicKey || algorithmOf(signer) != k.Algorithm {
		release()
		return nil, nil, errors.New("signing key does not match its stored algorithm and public key")
	}
	return signer, release, nil
}

// DestroySigningKey removes a PKCS#11 key from the token; KEK keys vanish with their row.
func (b *Box) DestroySigningKey(k StoredKey) error {
	if k.Backend != BackendPKCS11 {
		return nil
	}
	if !b.HasBackend(BackendPKCS11) {
		return ErrBackendUnavailable
	}
	return b.hsm.destroy(k.KeyRef)
}

// PKCS11SigningKeyRefs lists the KeyRefs of every DNSSEC key object Nexora created in the token
// (label "nexora-dnssec"), referenced by a database row or not.
func (b *Box) PKCS11SigningKeyRefs() ([][]byte, error) {
	if !b.HasBackend(BackendPKCS11) {
		return nil, ErrBackendUnavailable
	}
	return b.hsm.signingKeyIDs()
}

// PKCS11KeyAttributes reports CKA_EXTRACTABLE and CKA_SENSITIVE of the token private key keyRef.
func (b *Box) PKCS11KeyAttributes(keyRef []byte) (extractable, sensitive bool, err error) {
	if !b.HasBackend(BackendPKCS11) {
		return false, false, ErrBackendUnavailable
	}
	return b.hsm.privateKeyAttributes(keyRef)
}

// PKCS11WrapKeyAttributes reports CKA_EXTRACTABLE and CKA_SENSITIVE of the token wrap key.
func (b *Box) PKCS11WrapKeyAttributes() (extractable, sensitive bool, err error) {
	if !b.HasBackend(BackendPKCS11) {
		return false, false, ErrBackendUnavailable
	}
	return b.hsm.wrapKeyAttributes()
}

func algorithmOf(s crypto.Signer) uint8 {
	switch k := s.(type) {
	case *ecdsa.PrivateKey:
		if k.Curve == elliptic.P256() {
			return algECDSAP256SHA256
		}
	case *rsa.PrivateKey:
		return algRSASHA256
	}
	return 0
}

// dnskeyPublic encodes a public key as the DNSKEY public key field.
func dnskeyPublic(pub crypto.PublicKey) (string, error) {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		raw, err := k.Bytes()
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(raw[1:]), nil
	case *rsa.PublicKey:
		return rsaDNSKEYPublic(big.NewInt(int64(k.E)).Bytes(), k.N.Bytes()), nil
	}
	return "", fmt.Errorf("unsupported public key %T", pub)
}

// rsaDNSKEYPublic encodes RFC 3110 §2: exponent length octet, exponent, modulus.
func rsaDNSKEYPublic(e, n []byte) string {
	e, n = bytes.TrimLeft(e, "\x00"), bytes.TrimLeft(n, "\x00")
	buf := append([]byte{byte(len(e))}, e...)
	return base64.StdEncoding.EncodeToString(append(buf, n...))
}

// parseDNSKEYPublic decodes the DNSKEY public key field of alg.
func parseDNSKEYPublic(alg uint8, b64 string) (crypto.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("DNSKEY public key: %w", err)
	}
	switch alg {
	case algECDSAP256SHA256:
		if len(raw) != 64 {
			return nil, fmt.Errorf("ECDSA P-256 public key is %d bytes, want 64", len(raw))
		}
		return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append([]byte{4}, raw...))
	case algRSASHA256:
		if len(raw) < 2 || raw[0] == 0 || len(raw) < 1+int(raw[0])+64 || raw[0] > 4 {
			return nil, errors.New("malformed RSA DNSKEY public key")
		}
		e := new(big.Int).SetBytes(raw[1 : 1+raw[0]])
		return &rsa.PublicKey{N: new(big.Int).SetBytes(raw[1+raw[0]:]), E: int(e.Int64())}, nil
	}
	return nil, fmt.Errorf("unsupported DNSSEC algorithm %d", alg)
}

// zeroPrivate overwrites the private scalars of a parsed or generated key.
func zeroPrivate(s crypto.Signer) {
	switch k := s.(type) {
	case *ecdsa.PrivateKey:
		clear(k.D.Bits())
	case *rsa.PrivateKey:
		clear(k.D.Bits())
		for _, p := range k.Primes {
			clear(p.Bits())
		}
		for _, v := range []*big.Int{k.Precomputed.Dp, k.Precomputed.Dq, k.Precomputed.Qinv} {
			if v != nil {
				clear(v.Bits())
			}
		}
	}
}
