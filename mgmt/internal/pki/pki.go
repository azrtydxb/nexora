// Package pki owns the Nexora CA: engine and server certificates and join tokens.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EngineCertValidity is the lifetime of an engine client certificate.
const EngineCertValidity = 365 * 24 * time.Hour

const caValidity = 10 * 365 * 24 * time.Hour

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// CA is a loaded certificate authority.
type CA struct {
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
	CertPEM []byte
}

// InitCA writes a new ECDSA P-256 CA to outDir/ca.crt (0644) and outDir/ca.key (0600).
// It refuses to overwrite existing files.
func InitCA(outDir string) error {
	certPath, keyPath := filepath.Join(outDir, "ca.crt"), filepath.Join(outDir, "ca.key")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("refusing to overwrite %s", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now()
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
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeExclusive(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return writeExclusive(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// LoadCA reads a PEM CA certificate and its ECDSA private key.
func LoadCA(certFile, keyFile string) (*CA, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	cb, _ := pem.Decode(certPEM)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s: no PEM certificate", certFile)
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", certFile, err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("%s: not a CA certificate", certFile)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("%s: no PEM key", keyFile)
	}
	key, err := parseECKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyFile, err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("CA key does not match certificate")
	}
	return &CA{Cert: cert, Key: key, CertPEM: certPEM}, nil
}

func parseECKey(der []byte) (*ecdsa.PrivateKey, error) {
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("CA key is not ECDSA")
	}
	return ek, nil
}

// Fingerprint is the lowercase hex SHA-256 of the CA certificate DER.
func (ca *CA) Fingerprint() string {
	sum := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Pool returns a pool containing only this CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

// ServerCertificate issues a TLS server certificate with a fresh P-256 key.
// Names that parse as IP addresses become IP SANs, the rest DNS SANs.
func (ca *CA) ServerCertificate(names []string, validity time.Duration) (tls.Certificate, error) {
	if len(names) == 0 {
		return tls.Certificate{}, errors.New("server certificate needs at least one name")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// SignEngineCSR signs an ECDSA P-256 CSR as an engine client certificate with CN = engineID.
// It returns the certificate DER and its serial number as lowercase hex.
func (ca *CA) SignEngineCSR(csrDER []byte, engineID string, validity time.Duration) ([]byte, string, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, "", fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("CSR signature: %w", err)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, "", errors.New("CSR key must be ECDSA P-256")
	}
	if engineID == "" {
		return nil, "", errors.New("engine id is empty")
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: engineID},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
	if err != nil {
		return nil, "", err
	}
	return der, serial.Text(16), nil
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

// NewJoinToken returns a token `nxj1.<secret>.<caFingerprint>` and its secret part.
func NewJoinToken(caFingerprint string) (token, secret string, err error) {
	if !isFingerprint(caFingerprint) {
		return "", "", errors.New("invalid CA fingerprint")
	}
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	secret = tokenEncoding.EncodeToString(b)
	return "nxj1." + secret + "." + caFingerprint, secret, nil
}

// ParseJoinToken splits a join token into its secret and CA fingerprint.
func ParseJoinToken(token string) (secret, caFingerprint string, err error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != "nxj1" {
		return "", "", errors.New("join token must be nxj1.<secret>.<ca sha256>")
	}
	if parts[1] == "" {
		return "", "", errors.New("join token secret is empty")
	}
	if _, err := tokenEncoding.DecodeString(parts[1]); err != nil {
		return "", "", errors.New("join token secret is not base32")
	}
	if !isFingerprint(parts[2]) {
		return "", "", errors.New("join token CA fingerprint must be 64 lowercase hex characters")
	}
	return parts[1], parts[2], nil
}

func isFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// HashSecret is the SHA-256 of a secret, as stored in the database.
func HashSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// FingerprintFile is the lowercase hex SHA-256 of the DER of the PEM certificate in certFile, like
// (*CA).Fingerprint, for callers that hold only the CA certificate.
func FingerprintFile(certFile string) (string, error) {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("%s: no PEM certificate", certFile)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return "", fmt.Errorf("%s: %w", certFile, err)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}
