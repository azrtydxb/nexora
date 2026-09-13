package pki

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics of the DNS serving certificate watcher; serve registers them on its registry.
var (
	DNSTLSReloadErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nexora_mgmt_dns_tls_reload_errors_total",
		Help: "Failed loads of the DNS serving certificate files.",
	})
	DNSTLSNotAfter = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nexora_mgmt_dns_tls_not_after_seconds",
		Help: "Expiry of the loaded DNS serving certificate as a Unix timestamp.",
	})
)

// DNSTLSMaterial is a loaded DNS serving certificate (DoT, DoH, DoQ) and its private key.
type DNSTLSMaterial struct {
	ChainPEM, KeyPEM      []byte
	FingerprintSHA256     string
	Subject               string
	DNSNames, IPAddresses []string
	NotBefore, NotAfter   time.Time
}

// IssueDNSServerCert issues a DNS serving certificate with a fresh P-256 key. Names that parse as
// IP addresses become IP SANs, the rest DNS SANs. The chain is the leaf followed by the CA.
func (ca *CA) IssueDNSServerCert(names []string, validity time.Duration, now time.Time) (chainPEM, keyPEM []byte, err error) {
	if len(names) == 0 {
		return nil, nil, errors.New("at least one name is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    now.Add(-5 * time.Minute),
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
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	chainPEM = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca.CertPEM...)
	return chainPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// LoadDNSTLS reads and validates a PEM certificate chain (leaf first) and its private key.
// Errors never contain key bytes.
func LoadDNSTLS(certFile, keyFile string, now time.Time) (*DNSTLSMaterial, error) {
	chainPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("dns tls: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("dns tls: %w", err)
	}
	var certs [][]byte
	for rest := chainPEM; ; {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			certs = append(certs, b.Bytes)
		}
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("dns tls: no certificate in %s", certFile)
	}
	leaf, err := x509.ParseCertificate(certs[0])
	if err != nil {
		return nil, fmt.Errorf("dns tls: %s: %w", certFile, err)
	}
	key, err := parseAnyKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("dns tls: invalid private key in %s", keyFile)
	}
	pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(leaf.PublicKey) {
		return nil, errors.New("dns tls: private key does not match certificate")
	}
	if now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("dns tls: certificate expired at %s", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	sum := sha256.Sum256(leaf.Raw)
	m := &DNSTLSMaterial{
		ChainPEM: chainPEM, KeyPEM: keyPEM, FingerprintSHA256: hex.EncodeToString(sum[:]),
		Subject: leaf.Subject.String(), DNSNames: leaf.DNSNames, IPAddresses: []string{},
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter,
	}
	if m.DNSNames == nil {
		m.DNSNames = []string{}
	}
	for _, ip := range leaf.IPAddresses {
		m.IPAddresses = append(m.IPAddresses, ip.String())
	}
	return m, nil
}

func parseAnyKey(keyPEM []byte) (crypto.Signer, error) {
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		return nil, errors.New("no PEM key")
	}
	if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
		return nil, errors.New("unsupported key type")
	}
	if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	return x509.ParsePKCS1PrivateKey(b.Bytes)
}

// DNSTLSWatcher polls the certificate and key files and reloads them when either changes.
type DNSTLSWatcher struct {
	certFile, keyFile string
	interval          time.Duration
	current           atomic.Pointer[DNSTLSMaterial]
}

// NewDNSTLSWatcher creates a watcher polling every interval.
func NewDNSTLSWatcher(certFile, keyFile string, interval time.Duration) *DNSTLSWatcher {
	return &DNSTLSWatcher{certFile: certFile, keyFile: keyFile, interval: interval}
}

// Current is the last successfully loaded material, or nil.
func (w *DNSTLSWatcher) Current() *DNSTLSMaterial { return w.current.Load() }

type fileStamp struct {
	size    int64
	modTime int64 // Unix nanoseconds: comparable without time.Time's location pointer
	ok      bool
}

func stamp(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{size: fi.Size(), modTime: fi.ModTime().UnixNano(), ok: true}
}

// Run loads the files immediately, then every interval when their size or mtime changed. A load
// with a new fingerprint replaces Current and calls onChange; a failed load keeps the previous
// material. Run returns when ctx is done.
func (w *DNSTLSWatcher) Run(ctx context.Context, onChange func(*DNSTLSMaterial)) {
	var last [2]fileStamp
	tried := false
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		// Stamps are taken before reading, so a write racing the load triggers another reload.
		now := [2]fileStamp{stamp(w.certFile), stamp(w.keyFile)}
		if !tried || now != last {
			tried, last = true, now
			w.reload(onChange)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *DNSTLSWatcher) reload(onChange func(*DNSTLSMaterial)) {
	m, err := LoadDNSTLS(w.certFile, w.keyFile, time.Now())
	if err != nil {
		DNSTLSReloadErrors.Inc()
		slog.Error("dns tls reload failed: " + err.Error())
		return
	}
	if prev := w.current.Load(); prev != nil && prev.FingerprintSHA256 == m.FingerprintSHA256 {
		return
	}
	w.current.Store(m)
	DNSTLSNotAfter.Set(float64(m.NotAfter.Unix()))
	slog.Info("dns tls certificate loaded", "fingerprint", m.FingerprintSHA256, "not_after", m.NotAfter)
	onChange(m)
}
